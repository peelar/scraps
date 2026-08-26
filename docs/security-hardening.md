# Scraps security hardening review

- Review date: 2026-08-26
- Status: Active hardening backlog
- Scope: `scrap`, `scrapd`, the Pi extension, OpenShell integration, worker VM
  deployment, remote-client setup, backups, and release tooling

## Executive summary

Scraps has the right high-level boundary: Pi and model credentials remain on the
developer machine, untrusted repository code runs in an OpenShell container,
and a dedicated worker VM separates the container pool from the physical host.
Several implementation details currently weaken that design.

Scraps should not yet be presented as safe for arbitrary untrusted repositories
on other people's homelabs. The release-blocking problems are:

1. Pi's complete process environment is forwarded to remote commands, which can
   disclose unrelated local secrets to repository code.
2. Command cancellation relies on a PID file stored in the attacker-writable
   workspace, so a malicious command can evade or redirect cleanup.
3. A configuration mistake can expose an unauthenticated command-execution API,
   and clients can send its administrative bearer token over plaintext HTTP.
4. `scrapd`, OpenShell, and other VM processes have overly broad access to the
   Docker socket, making an application compromise equivalent to worker-VM root.
5. Aggregate workspace, execution, output, search, memory, and disk consumption
   is not sufficiently bounded.

The outer VM remains a valuable control: compromise of the worker should not by
itself imply compromise of the physical host kernel. It is not a complete
homelab boundary, however. A compromised VM can still attack services reachable
on its network, steal worker credentials, destroy workspaces, and consume the
worker's resources.

This document records observed implementation risks. The accepted architectural
policy remains [ADR 0004](adr/0004-sandbox-security-policy.md).

## Severity and release policy

| Severity | Meaning | Release treatment |
| --- | --- | --- |
| Critical | Directly defeats a claimed trust boundary or broadly exposes credentials/control | Must be fixed before external use |
| High | Likely worker compromise, remote command execution, or practical denial of service | Must be fixed or explicitly fail closed before external use |
| Medium | Important defense-in-depth, operational security, or supply-chain weakness | Schedule before a stable release |
| Low | Limited security impact, unsafe ergonomics, or hardening opportunity | Track and fix with normal maintenance |

## Trust model

### Trusted components

- The user and the local Pi process
- `scrapd` and its persistent state
- The worker VM kernel and hypervisor boundary
- The OpenShell gateway, runtime, and enforced policy
- Scraps' credential broker and release/update pipeline

### Untrusted inputs

- Repository contents and Git history
- Build scripts and package lifecycle hooks
- Dependencies downloaded by a workspace
- Tests, browser content, and generated shell commands
- Command arguments, output, filenames, symlinks, regexes, and repository URLs
- Any process already running inside a workspace
- Network peers that do not possess an authorized client identity

### Assets to protect

- Model-provider, cloud, package-registry, SSH, Git, and homelab credentials
- The `scrapd` bearer token and GitHub App private key
- Other workspaces and their source code
- The worker control plane, Docker socket, and VM filesystem
- The developer machine, hypervisor management plane, NAS, router, and LAN
- Availability of the worker VM and integrity of backups

## Findings

### SEC-001 — Complete local environment reaches untrusted commands

**Severity:** Critical  
**Status:** Resolved in source on 2026-08-26

The reviewed implementation passed the Bash tool's environment through the
client and daemon into OpenShell. That path has been removed. The Pi operation
now ignores its environment option, the client has no environment field, the
HTTP boundary rejects non-empty `env` objects, and providers construct only a
small provider-owned command environment.

Before this fix, a repository could run `env`, inspect child process state, or
read `/proc` and potentially obtain every exported value that existed when Pi
started. Examples included cloud credentials, package tokens, API keys, proxy
credentials, CI variables, and `SCRAP_TOKEN` when configured through the
environment. The token loaded from Scraps' profile was handled separately and
was not automatically inserted into Pi's process environment.

This is not necessary to let agents run authenticated software. The unsafe part
is the implicit grant of *all* local credentials to *every* workspace. Raw
secrets that truly must be visible to a program must be explicit, scoped grants;
where possible, software should use brokered operations or short-lived service
credentials instead.

**Implemented controls:**

- The remote Bash operation ignores Pi's general shell environment.
- Providers construct a small environment from safe, provider-owned values such as
  `HOME`, `PATH`, `SHELL`, `TMPDIR`, locale, terminal settings, and explicit
  non-secret Pi metadata.
- `SCRAP_TOKEN`, SSH agent sockets, cloud credential variables, and
  generic `*_TOKEN`, `*_SECRET`, `*_KEY`, and `*_PASSWORD` values are never
  forwarded implicitly.
- Raw environment access is opt-in by exact variable name through
  `scrap env allow NAME...`; the mode-0600 local profile stores names only.
  Approvals are client-profile-global, not repository- or workspace-scoped.
- The extension snapshots values from Pi's startup environment and sends only
  approved, set names on each exec request. This works with ordinary exports
  and external injectors such as 1Password, Doppler, and Infisical.
- In remote mode, Pi's system prompt and bundled Scraps skill tell the agent
  which approved names loaded and which were missing at startup. When software
  names a missing variable, the agent directs the user to the local
  `scrap env allow NAME` flow, must not request the value in chat, and prefers
  a broker when one exists. Pi also shows a name-only activation notice.
- The daemon rejects invalid names, reserved Scraps/OpenShell controls,
  newlines, NULs, oversized values, and oversized maps.
- The client refuses to send approved values over non-loopback plain HTTP.
- Conformance tests cover sentinel AWS, npm, OpenAI, Scraps, SSH-agent, and
  unrelated secrets at the Pi and daemon/provider boundaries.

**Known limitation:** an approved raw value is not brokered. It is readable by
the agent and every workspace process and can be exfiltrated over permitted
network routes. The current OpenShell CLI also accepts exec environment values
as command arguments, so a sufficiently privileged process on the worker may
observe them while the exec client is running. Replace that CLI hop with
OpenShell's direct gRPC/Go SDK before claiming host-process confidentiality for
raw grants. Values are not intentionally written to Scraps storage or logs.

### SEC-002 — Commands can interfere with their own cancellation

**Severity:** Critical  
**Status:** Mitigated in source on 2026-08-26; live worker test remains a release gate

The reviewed implementation recorded the command's process-group ID under
`/workspace/.scrap/run`. That mechanism has been removed. A canceled OpenShell
execution now asks the trusted OpenShell control plane to stop and restart the
sandbox. Stop destroys sandbox compute and its process tree while retaining the
persistent workspace; no cancellation identifier is read from workspace data.
Execs are serialized per workspace because recycling is a sandbox-wide action.

In simple terms, the old design left the emergency-stop instructions inside the
room with the program being controlled. The program could throw the
instructions away, change them, or leave a detached child behind. The new
design keeps the emergency stop in OpenShell's control plane.

**Implemented controls and remaining verification:**

- Cancellation uses OpenShell stop/start, with no PID file or other control
  metadata under `/workspace`.
- A failed trusted stop is surfaced as a cancellation error and the sandbox is
  not restarted, so cleanup fails closed.
- Unit tests verify stop-before-start ordering, state restoration, absence of
  workspace cancellation metadata, and failure behavior.
- An opt-in `TestLiveOpenShellHostileCancellation` exercises the configured
  gateway with signal-ignoring and detached descendants while checking
  workspace persistence.
- Run live worker tests in which commands delete likely metadata, double-fork,
  daemonize, create many descendants, ignore signals, and race cancellation.

### SEC-003 — Misconfiguration can expose unauthenticated remote execution

**Severity:** High  
**Status:** Open; release blocker

`cmd/scrapd/main.go` accepts an arbitrary `SCRAPD_LISTEN_ADDR`.
`internal/server/server.go` disables authentication entirely when the configured
token is empty. The deployment scripts choose a loopback address and generate a
strong token, but the daemon does not enforce the safety relationship between
listen address and authentication.

Binding to `0.0.0.0` with an empty token exposes workspace creation, command
execution, deletion, and credential-management operations to reachable peers.
This is a configuration footgun with remote-code-execution consequences.

The Go and TypeScript clients also accept arbitrary base URLs and send the
bearer token to them. A non-loopback `http://` URL exposes the token to network
observers and malicious intermediaries.

**Required remediation:**

- Refuse startup on a non-loopback address without a strong authentication
  token.
- Prefer refusing non-loopback plaintext HTTP entirely. If an insecure mode is
  needed for development, require a deliberately named flag and loud warning.
- Validate client URLs and reject non-loopback HTTP by default.
- Document the daemon token as an administrative, effectively root-equivalent
  capability for all Scraps workspaces.
- Add authentication-failure throttling and tests for IPv4, IPv6, wildcard,
  hostname, proxy, and loopback edge cases.

### SEC-004 — Docker access collapses worker-VM privilege separation

**Severity:** High  
**Status:** Open; release blocker

`deploy/worker/install` places the `scraps` user in the Docker group and runs
`scrapd` as that user. `vm/lima.yaml` also configures the Docker socket with mode
`0666`. Any process in the VM that reaches that socket can normally create a
privileged container and mount the VM filesystem. Exploitation of `scrapd`, the
OpenShell gateway, or another unprivileged VM process therefore becomes
effective VM-root access.

The outer VM still protects the physical host from an ordinary container escape,
but a worker compromise exposes the GitHub App key, daemon token, database,
backups in progress, all workspaces, and the VM's network position.

**Required remediation:**

- Run `scrapd`, the OpenShell gateway, backup jobs, and deployment helpers under
  separate identities.
- Give Docker access only to the component that strictly requires it.
- Remove socket mode `0666`; use the narrowest group ownership and permissions.
- Have `scrapd` communicate with OpenShell through a narrow authenticated API,
  not a shared Docker capability.
- Evaluate rootless Docker/OpenShell support.
- Apply compatible systemd restrictions such as empty capability bounds,
  syscall/address-family restrictions, kernel protection, namespace limits, and
  `NoNewPrivileges`.

OpenShell's Docker driver necessarily manages containers through the Docker
socket. Its documented restrictions, including disabled host bind mounts by
default, are useful inner controls but do not make broad socket access safe:
<https://github.com/NVIDIA/OpenShell/blob/main/crates/openshell-driver-docker/README.md>.

### SEC-005 — Aggregate resource consumption is not bounded

**Severity:** High  
**Status:** Open; release blocker

Individual OpenShell sandboxes receive CPU and memory arguments, but Scraps does
not enforce a maximum workspace count, aggregate running-workspace budget,
worker reserve, or request concurrency limit. A ready pool adds another active
sandbox. Disk and PID limits promised by the security ADR are not explicitly
created or verified by Scraps.

Additional denial-of-service paths include:

- no default server-side execution timeout when a client omits one;
- unbounded command output streaming and local spill-file growth in the Pi Bash
  integration;
- user-controlled glob/grep result limits without a server maximum;
- whole-file reads during grep and file retrieval;
- potentially expensive attacker-supplied regular expressions;
- base64 and JSON copies of file responses up to the configured file limit;
- unlimited concurrent HTTP requests and authentication attempts.

**Required remediation:**

- Enforce maximum active/running workspaces and an aggregate CPU/memory/disk/PID
  budget per worker.
- Reserve capacity for the VM, gateway, `scrapd`, cleanup, and backups.
- Add execution and request semaphores, a bounded queue, rate limits, and
  backpressure.
- Apply a server-owned default execution deadline and maximum output byte count.
- Bound search patterns, result counts, examined files/bytes, and regex time.
- Stream bounded file content rather than using whole-file `CombinedOutput` and
  repeated base64/JSON copies.
- Enforce disk quotas and low/high watermarks; fail closed when the runtime
  cannot prove them.
- Terminate or discard output after limits rather than filling worker or
  developer-host disks.

### SEC-006 — Repository URLs permit unsafe transports and secret persistence

**Severity:** Medium  
**Status:** Open

Repository validation in `internal/provider/openshell.go` is primarily based on
string prefixes. It accepts plaintext HTTP and URLs containing user information,
for example `https://user:token@example/repo`. Repository URLs are persisted and
included in some errors, which can place embedded credentials in SQLite, API
responses, or daemon logs. Depending on network policy, user-selected URLs can
also become a server-side request path to private services.

**Required remediation:**

- Parse and canonicalize repository URLs structurally.
- Require HTTPS or a supported authenticated Git transport; allow HTTP only
  behind an explicit development option.
- Reject URL userinfo, control characters, unexpected ports, fragments, and
  ambiguous encodings.
- Redact repository URLs in persistence, errors, and logs.
- Consider allowed clone hosts or explicit private-destination grants.
- Test DNS rebinding, redirects, private IP ranges, IPv6 literals, and alternate
  numeric address forms if private-network destinations are denied.

### SEC-007 — Scraps does not assert the effective OpenShell policy

**Severity:** High  
**Status:** Open

Workspace creation relies on the policy inherited from the digest-pinned base
image and OpenShell defaults. Scraps does not supply a checked-in policy, record
its complete effective contents, verify strict enforcement, or compare an
expected policy hash at startup. `scrap status` reports a policy label rather
than proof of its actual filesystem, network, and process rules.

OpenShell policies are the primary inner mechanism for filesystem and network
isolation, and unmatched network traffic is denied by default. Scraps must still
verify the exact policy on which its security claim depends:
<https://docs.nvidia.com/openshell/latest/sandboxes/policies>.

**Required remediation:**

- Check in and review a Scraps-specific OpenShell policy.
- Pin or hash the complete effective policy and fail startup on drift.
- Require strong enforcement instead of `best_effort` where a control is part of
  Scraps' claim.
- Surface the policy identity and material exceptions in `scrap status`.
- Test that a workspace cannot reach the gateway, `scrapd`, Docker socket, VM
  management services, neighboring workspaces, physical host, hypervisor, LAN,
  NAS, router, or cloud metadata endpoints.

### SEC-008 — HTTP server limits are incomplete

**Severity:** Medium  
**Status:** Open

The HTTP server sets `ReadHeaderTimeout` but not a complete set of header, idle,
body, request, and concurrency limits. Request decoding uses a bounded reader,
but does not consistently distinguish an oversized body or reject trailing JSON
values. Long-lived execution streaming makes global write deadlines nuanced but
does not remove the need for handler-specific deadlines and connection limits.

**Required remediation:**

- Set `MaxHeaderBytes`, an idle timeout, and appropriate read deadlines.
- Use handler-specific execution contexts and streaming-safe write controls.
- Detect body-limit overflow explicitly.
- Reject unknown fields and trailing JSON where forward compatibility permits.
- Limit concurrent requests per client and workspace.
- Rate-limit invalid authentication without logging supplied tokens.

### SEC-009 — Release and installation authenticity is incomplete

**Severity:** Medium  
**Status:** Open

Setup downloads and executes an OpenShell installer. The public Scraps installer
recommends piping a mutable branch directly to a shell, obtains archives and
their checksum material from the same origin, and can continue when no checksum
tool is available. The worker bundle is extracted before its internally supplied
checksum establishes integrity; an untrusted tar archive could exploit path
traversal. GitHub Actions use mutable major-version tags, and release artifacts
have checksums but no Scraps-controlled signature, provenance, or SBOM.

Checksums from the same compromised distribution point detect corruption but do
not establish publisher authenticity.

**Required remediation:**

- Sign artifacts and checksum manifests with a separately protected identity
  such as Sigstore/cosign or minisign.
- Verify the signer through an out-of-band pinned identity and fail closed.
- Generate SBOMs and build provenance; scan images and release artifacts before
  promotion.
- Pin GitHub Actions to reviewed commit SHAs.
- Download installers before execution and verify a pinned version/digest.
- Validate all archive paths, links, owners, modes, and extraction destinations
  before extraction.
- Fail installation when required verification tooling is unavailable.

OpenShell v0.0.113 provides release checksums and a signed release commit, which
is a useful upstream control: <https://github.com/NVIDIA/OpenShell/releases/tag/v0.0.113>.

### SEC-010 — Client-token storage is not validated when read

**Severity:** Medium  
**Status:** Open

The remote-client setup script creates a `0700` directory and `0600` profile,
which is a good default. The Go and TypeScript profile loaders do not reject
symlinks, wrong ownership, group/world-readable permissions, or malformed files
with sufficiently visible errors. The bearer token remains plaintext on disk
and grants administrative access to all workspaces.

**Required remediation:**

- Store tokens in the operating-system keychain/keyring where practical.
- Otherwise open without following symlinks and validate file type, ownership,
  directory safety, and mode before reading.
- Surface malformed or insecure configuration as an actionable error.
- Separate non-secret endpoint configuration from secret material.
- Move toward per-client token hashes, IDs, expiration, rotation, and selective
  revocation rather than one shared global token.

### SEC-011 — Security-relevant operations are not auditable

**Severity:** Medium  
**Status:** Open

The security ADR calls for structured audit events, but the current service
mostly logs startup and errors. An operator cannot reliably establish which
client created, executed in, granted credentials to, stopped, or deleted a
workspace.

**Required remediation:**

- Record client/token ID, workspace ID, operation, policy/grant identity,
  result, timestamp, and duration.
- Record credential kind, scope, issuance, expiry, use, and revocation without
  recording the credential.
- Do not log commands, environment values, request authorization headers,
  private keys, tokens, or sensitive response bodies.
- Rate-limit repetitive failure events and define retention/rotation policy.
- Integrate authenticated Tailscale identity only if it can be obtained from a
  trusted, non-spoofable boundary.

### SEC-012 — Backup health can mistake a partial file for success

**Severity:** Medium  
**Status:** Open

The worker backup helper writes directly to its final `.tar.gz.age` pathname. A
failed `tar` or `age` operation can leave a partial final-named file. The health
check treats a recent matching file as a fresh backup without verifying its
checksum, completion manifest, or decryptability.

**Required remediation:**

- Write to a uniquely named temporary file on the destination filesystem.
- Clean partial files on failure.
- Verify the encrypted artifact and a separately recorded manifest/checksum.
- Flush data and atomically rename only after successful completion.
- Monitor the last successful backup unit/manifest, not file modification time
  alone.
- Regularly test restore and decryption in an isolated worker.

### SEC-013 — Lima image and network configuration can drift

**Severity:** Medium  
**Status:** Open

`vm/lima.yaml` inherits a mutable Ubuntu LTS image alias rather than pinning an
exact image URL and digest. The inspected local VM had resolved to a newer Ubuntu
template than the documentation described. Lima's proxy-environment propagation
was also not explicitly disabled. Proxy URLs, credentials, trust roots, and
unexpected routing may therefore enter the guest or change between installs.

The outer VM is a kernel boundary, not a network boundary. Its route to the
developer host and homelab remains valuable to an attacker after worker
compromise.

**Required remediation:**

- Pin a reviewed VM image URL/version and cryptographic digest.
- Explicitly disable proxy-environment propagation unless required and audited.
- Document and restrict guest-to-host integration features.
- Place remote workers on a constrained VLAN/firewall policy.
- Deny worker access to hypervisor management, routers, NAS administration, and
  unrelated private networks while allowing explicitly required services.
- Verify effective routes, DNS, proxy, CA, mounts, forwarded ports, and agent
  forwarding during deployment acceptance.

### SEC-014 — Unit tests read the developer's real Scraps profile

**Severity:** Medium  
**Status:** Open

During this review, `pnpm test` loaded the review machine's real
`~/.config/scraps/client.json`. Two tests failed because they expected localhost
but received the configured tailnet URL, which was printed in test output. The
current tests appear to mock the client paths involved, but allowing unit tests
to read real endpoints and tokens creates a future risk of contacting or
mutating a live worker.

**Required remediation:**

- Give every test a temporary home and explicit `SCRAPS_CLIENT_CONFIG`.
- Inject configuration loaders rather than consulting process-global user state.
- Ensure unit and integration tests fail if they attempt an unapproved external
  network connection.
- Never print tokens or full credential-bearing configuration in assertions.

### SEC-015 — Fresh OpenShell installation has an unclear privilege boundary

**Severity:** Low  
**Status:** Verify and redesign if confirmed

The worker setup path invokes the OpenShell installer as the unprivileged
`scraps` service user, while the upstream package installation path may require
root privileges for system package management. A failed fresh install may tempt
operators to grant the long-running service user broad passwordless sudo.

**Required remediation:**

- Install a pinned and verified OpenShell package during root-owned provisioning.
- Run the installed gateway and normal workspace operations as an unprivileged
  dedicated user.
- Never grant the long-running `scraps` or broker service unrestricted sudo.
- Exercise installation on a truly fresh worker image in CI/acceptance testing.

### SEC-016 — Host installer broadens and obscures the deployment boundary

**Severity:** Low  
**Status:** Open

The public installer places both `scrap` and `scrapd` on the developer host even
though the supported architecture runs `scrapd` only inside a worker VM. Merely
installing the binary does not start it, but the extra host-side control-plane
binary can confuse operators and encourages unsupported direct-host setups.

**Required remediation:**

- Install only the client by default on developer machines.
- Package worker components separately or require an explicit worker-install
  mode.
- Make unsupported host-daemon startup fail clearly or document its lack of an
  isolation claim.

## Credential broker design

Removing environment inheritance must not prevent agents from using authenticated
software. Scraps should use a capability broker for common services and retain
an explicit raw-secret escape hatch for software that cannot be adapted.

### Proposed boundary

```text
Pi / user
   | registers a narrow grant
   v
scrapd control plane ------> scrap-broker
                                |
                                +-- GitHub App private key
                                +-- model API keys
                                +-- registry/cloud credentials
                                |
OpenShell workspace ------------+
  receives only a short-lived workspace broker capability
```

`scrap-broker` should be a separate process and Unix identity inside the worker
VM. It must not have Docker-socket or workspace-filesystem access. Only the
broker reads long-lived secret material. `scrapd` manages grants through a
separate control interface; workspaces use a narrow data interface.

### Broker mechanisms

1. **Operation broker — strongest.** The workspace asks for an operation such as
   pushing an approved Git reference. The broker validates and performs it. The
   secret never enters the workspace, but every supported operation needs an
   adapter.
2. **Authentication proxy — preferred for HTTP services.** Software uses a
   broker-owned base URL or registry endpoint. The broker adds upstream
   authentication and enforces endpoint, method, model, budget, and rate policy.
   Avoid generic TLS interception; applications should deliberately target the
   proxy.
3. **Short-lived credential vending — compatibility option.** The broker issues
   a temporary repository-, service-, and permission-scoped credential. The
   workspace can read and exfiltrate it, so expiry and limited scope are essential.
4. **Explicit raw-secret grant — last resort.** The user intentionally grants a
   test or narrowly scoped secret to one workspace for a short time. Scraps must
   warn that all code in that workspace can read it.

### Suitable provider patterns

| Service | Preferred mechanism |
| --- | --- |
| GitHub | GitHub App installation token through a Git credential helper |
| GitLab | Project-scoped, short-lived token |
| OpenAI/Anthropic and similar APIs | Authenticated HTTP proxy with model, rate, and budget policy |
| npm/PyPI/private packages | Registry proxy or read-only scoped token |
| AWS/GCP/Azure | Native STS or workload identity with short expiry |
| SSH | Restricted signing-agent protocol; never expose the private key |
| Database | Authenticated database proxy or dynamically issued database user |
| Unsupported application | Explicit raw-secret grant with expiry and warning |

The current GitHub App flow is already close to short-lived credential vending
and should become the first formal broker provider.

### Grant model

A workspace broker capability should identify an immutable server-side grant,
not allow the workspace to choose arbitrary targets. Conceptually:

```text
workspace: ws_123
expires:   2026-08-26T14:30:00Z
grants:
  - github repository acme/project, contents:write
  - model API gpt-5-mini, maximum spend 2 USD
```

The capability itself is a secret, but compromise grants only the narrow,
temporary authority already assigned to that workspace. The broker must look up
approved targets server-side. Accepting an arbitrary repository, hostname, URL,
cloud role, or account from the workspace would create a confused-deputy and
SSRF risk.

### Broker security requirements

- Deny by default; require an explicit workspace grant.
- Bind each capability to one workspace, provider, target, permission set, and
  short expiry.
- Use independently revocable capability IDs and store only hashes where
  bearer-token verification permits.
- Authenticate both control-plane and workspace requests; keep their endpoints
  and authorities separate.
- Canonicalize targets before policy checks and never proxy arbitrary URLs.
- Strip caller-provided authorization and hop-by-hop headers before adding
  upstream credentials.
- Apply request size, response size, concurrency, rate, and monetary limits.
- Prevent redirect and DNS behavior from escaping the approved upstream set.
- Keep long-lived keys in an OS keyring, hardware-backed store, or tightly
  permissioned broker-only files.
- Do not persist issued credentials in workspace metadata, snapshots, Git
  remotes, shell history, command output, or logs.
- Audit grants, issuance, use, denial, expiry, and revocation without logging
  secrets or sensitive request bodies.
- Revoke all workspace capabilities immediately on stop or deletion.
- Restrict workspace egress so the broker is reachable only as intended and the
  broker can reach only its configured upstream services.

### User-facing credential modes

Scraps should make the distinction visible:

1. **No credentials** — the default clean environment.
2. **Brokered capability** — recommended and service-specific.
3. **Raw secret** — explicit advanced grant with a warning and expiration.

No design can give arbitrary hostile code a raw secret while guaranteeing that
the same code cannot print or transmit it. When software requires the raw value,
the user is extending trust to the entire workspace for the lifetime and scope
of that credential.

## Existing positive controls

The review also confirmed useful protections that should be preserved:

- Normal workloads run inside a dedicated worker VM rather than directly on the
  developer or homelab host.
- Default remote deployment binds `scrapd` to loopback and places Tailscale Serve
  plus bearer authentication in front of it.
- SSH forwarding and host-home mounts are avoided.
- The OpenShell base image is pinned by digest.
- Bearer-token comparison is constant-time.
- The deployment flow generates a high-entropy daemon token and restrictive
  initial file modes.
- The GitHub App uses limited permissions and short-lived installation tokens;
  the private key remains in the worker control plane.
- Backups are encrypted with `age`.
- Database queries reviewed during the audit use parameterized SQL.
- The outer VM remains a meaningful second boundary if an OpenShell container or
  its shared runtime is compromised.

These controls reduce risk but do not compensate for the release blockers above.

## Required adversarial test matrix

Before external use, run these tests inside a disposable worker on an isolated
network. Tests must use intentionally malicious commands and repositories, not
only cooperative fixtures.

### Secrets and broker

- Populate the Pi and daemon environments with sentinel secrets and prove none
  appear in the workspace, `/proc`, child processes, output, logs, SQLite,
  snapshots, or backups.
- Prove one workspace cannot use another workspace's broker capability.
- Prove grants reject unapproved repositories, redirects, alternate host
  encodings, DNS rebinding, methods, models, roles, and accounts.
- Prove expiry, revocation, stop, and delete terminate broker access.
- Search logs, command output, Git configuration, shell history, files, and
  snapshots for issued credentials.

### Process lifecycle

- Cancel a command that deletes or replaces cancellation metadata.
- Cancel commands that fork, double-fork, daemonize, ignore signals, and create
  large process trees.
- Confirm timeout, stop, delete, daemon restart, and VM reboot leave no workload
  descendants.
- Race cancellation against process exit and PID reuse.

### Filesystem and workspace isolation

- Attempt path traversal, symlink/hardlink races, special-file access, oversized
  files, sparse files, and archive traversal.
- Attempt reads of daemon state, broker state, Docker socket, GitHub key, other
  workspaces, host integration mounts, and VM system files.
- Confirm disk quotas and deletion reclaim actual storage.

### Network

- Attempt access to `scrapd`, OpenShell control endpoints, the broker control
  interface, Docker, cloud metadata, host gateway, hypervisor, router, NAS, and
  representative LAN targets.
- Attempt inbound listeners and implicit port publication.
- Confirm only explicitly granted broker/upstream destinations are reachable.
- Verify IPv4, IPv6, DNS, redirects, proxy variables, and unusual address forms.

### Resource exhaustion

- Create workspaces until the configured limit is reached.
- Exercise fork bombs, memory pressure, disk fill, infinite output, slow input,
  decompression bombs, expensive regexes, huge directory trees, and concurrent
  requests.
- Confirm the control plane remains responsive and retains cleanup reserve.
- Confirm developer-host spill files and logs remain bounded.

### Authentication and transport

- Prove non-loopback tokenless and plaintext configurations fail closed.
- Test invalid-token rate limiting and ensure tokens never appear in logs.
- Verify URL parsing for IPv4, IPv6, hostnames, wildcard binds, proxy headers,
  and redirects.
- Confirm token rotation and per-client revocation do not leave stale access.

### Worker compromise containment

- Assume workspace-container escape and verify the attacker still cannot access
  the Docker socket or broker secrets.
- Assume worker-VM root and verify network policy protects the physical host,
  hypervisor management plane, backups, NAS, router, and unrelated LAN services.
- Test emergency shutdown, credential rotation, rebuild, and restore procedures.

## Verification performed during this review

The following checks passed against the reviewed working tree:

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `pnpm check`
- `pnpm audit --audit-level low`
- `govulncheck ./...`
- `git diff --check`
- `make worker-check`
- shell syntax checks for repository scripts

No known Go or JavaScript dependency vulnerabilities were reported by those
tools. `pnpm test` passed 44 tests and failed 2 because the test process loaded
the real client profile described in SEC-014.

This was primarily a source/configuration review. It did not include a live
container escape, Tailscale attack, Proxmox attack, hostile LAN test, verified
restore drill, or full worker image scan. ShellCheck, Semgrep, gitleaks,
staticcheck, Trivy, Syft, Grype, and cosign were not available in the review
environment.

### Follow-up verification for SEC-001 and SEC-002

The 2026-08-26 fixes passed:

- all OpenShell provider tests other than the separately added, currently
  uncompilable `tunnel_test.go`, including focused race-detector runs;
- the affected server tests under the race detector;
- all other Go package tests and `go build ./...`;
- all 47 Pi extension tests with the real client profile isolated;
- `pnpm check`, `git diff --check`, and `make worker-check`.

The opt-in live hostile-cancellation test was attempted against the configured
OpenShell 0.0.113 gateway. The gateway repeatedly became unhealthy with SQLite
error 14 (`unable to open database file`) during the run, so this test is not
recorded as passing. Both disposable containers and their OpenShell metadata
were confirmed absent afterward. Live hostile cancellation remains a release
gate rather than being inferred from the mocked control-plane test.

## Prioritized remediation plan

### P0 — Before any external homelab use

1. **Completed in source:** remove implicit Pi environment forwarding and add
   secret-leak conformance tests.
2. **Completed in source; live hostile test pending:** replace
   workspace-controlled PID cancellation with trusted OpenShell sandbox
   recycling.
3. Enforce authenticated non-loopback operation and reject plaintext remote
   client URLs.
4. Remove broad Docker-socket access and split service identities.
5. Enforce aggregate workspace, execution, disk, output, search, and request
   limits.
6. Pin and verify an explicit OpenShell policy, then run the hostile network and
   filesystem test matrix.

### P1 — Before a stable release

1. Formalize the GitHub credential flow as the first broker provider.
2. Add workspace grant identities, expiry, revocation, and broker auditing.
3. Harden repository URL parsing and private-network destination policy.
4. Complete HTTP server limits and authentication throttling.
5. Sign releases, pin CI actions, add SBOM/provenance, validate archives, and
   scan worker images.
6. Secure client-token storage and introduce independently revocable clients.
7. Add structured security audit events.

### P2 — Operational defense in depth

1. Make backups atomic and continuously restore-tested.
2. Pin the Lima image and constrain worker networking and integration features.
3. Isolate all tests from real user configuration and networks.
4. Verify fresh privileged installation without granting service users sudo.
5. Separate client and worker packages and remove unsupported host-daemon
   ambiguity.
6. Commission an independent review after P0/P1 changes and before describing
   hostile-code isolation as production-ready.

## Release gate

A security item is complete only when:

- the implementation enforces it rather than relying on documentation or a safe
  installer default;
- status/diagnostics report the effective control accurately;
- a hostile conformance test proves the failure mode is blocked;
- unsupported runtime capabilities fail closed;
- upgrade, rollback, backup, and recovery paths preserve the control; and
- the operator can understand and rotate any authority involved.
