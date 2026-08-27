# ADR 0012: Make durable remote Pi execution the `/scrap` behavior

- Status: Accepted
- Date: 2026-08-27
- Amended by: ADR 0013 (local profile cloning and session movement)

## Context

ADR 0001 kept Pi's agent loop on the developer machine and redirected only its
project tools. That preserves local personalization, but a turn cannot continue
while the laptop is asleep or disconnected. ADR 0005 proposed durable headless
runs as a separate future mode. Requiring users to predict whether a task needs
durability is unnecessary product complexity.

## Decision

`pi` followed by `/scrap` remains the user experience. The Scraps extension
intercepts every interactive prompt and submits it as a durable Pi run through
the authenticated scrapd API. There is no separate durable mode. Durable remote
execution is the `/scrap` contract, and a worker that cannot provide it fails
closed rather than falling back to a laptop-owned agent loop.

The identities remain separate:

- the workspace owns project state;
- the remote Pi session owns conversation state;
- each submitted prompt creates a bounded run;
- the local Pi session stores the workspace ID, run ID, and event cursor.

scrapd starts Pi in JSON mode on the trusted worker, outside the OpenShell
project workspace. Pi's seven project tools still target that workspace through
scrapd. Model credentials belong to the trusted runner environment and must
never be forwarded into the workspace.

Run state and emitted JSON events are persisted before they are exposed through
the API. Creating a run starts execution independently of the request context.
A reconnecting client fetches events after its last persisted sequence number.
One mutating run per workspace is allowed initially.

Tailscale remains transport, not lifecycle: the client reaches scrapd through
Tailscale Serve HTTPS, but a lost tailnet connection does not cancel the run.

A worker advertises this required capability as `features.durableRuns`. Missing
capability is an actionable configuration or version error, not an alternate
execution mode.

## Consequences

- Closing a laptop after run acceptance no longer stops the agent turn.
- Ordinary `/scrap`, `/resume`, and `pi -c` remain the visible workflow.
- The worker needs a compatible Pi installation and protected model access.
- Remote sessions, event retention, cancellation, resource synchronization,
  and daemon-restart recovery become control-plane responsibilities.
- Exact native rendering may require a future Pi remote-runtime hook; the first
  client renders persisted JSON-mode results through the extension API.

## Initial implementation

The current vertical slice includes SQLite run/event persistence, create/get/event
and cancellation HTTP endpoints, a trusted Pi JSON-mode command runner,
persistent per-workspace concurrency enforcement, capability discovery, local
prompt interception, event polling, reconnection from the Pi session binding,
and daemon-restart reconciliation. ADR 0013 replaces separate production
credential enrollment with trusted local-profile cloning and adds local-session
import as required follow-up work.
