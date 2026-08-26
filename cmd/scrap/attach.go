package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/peelar/scraps/internal/client"
)

const attachUsage = `usage: scrap attach [user@]worker

Discover a Scraps worker on this tailnet and configure this computer to use
it. The worker's Tailscale endpoint and bearer token are fetched over the
existing SSH trust path and written to the mode-0600 client profile that the
scrap CLI and the Pi extension load automatically.

Without an argument, every online tailnet peer tagged tag:scraps-worker (or
named scraps-worker*) is probed on its Tailscale Serve HTTPS endpoint and must
answer GET /healthz. With an argument, the SSH target is used directly and no
tailnet discovery happens. The SSH user defaults to the current user.
`

// workerProfileScript runs on the worker VM and prints the authoritative
// Tailscale DNS name and bearer token, mirroring scripts/configure-remote-client.
const workerProfileScript = `set -euo pipefail
dns_name=$(tailscale status --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["Self"]["DNSName"].rstrip("."))')
token=$(sed -n 's/^SCRAPD_TOKEN=//p' /etc/scraps/scrapd.env)
[[ -n $dns_name && -n $token ]]
printf '%s\n%s\n' "$dns_name" "$token"
`

var (
	workerDNSPattern   = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
	workerTokenPattern = regexp.MustCompile(`^[A-Fa-f0-9]{64}$`)
)

// tailnetPeer is the subset of a tailscale status --json peer entry used for
// worker discovery.
type tailnetPeer struct {
	DNSName  string   `json:"DNSName"`
	HostName string   `json:"HostName"`
	Online   bool     `json:"Online"`
	Tags     []string `json:"Tags"`
}

type tailnetStatus struct {
	Peer map[string]tailnetPeer `json:"Peer"`
}

func runAttach(args []string) int {
	flags := flag.NewFlagSet("attach", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(flags.Output(), attachUsage) }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 1 {
		fmt.Fprint(os.Stderr, attachUsage)
		return 2
	}

	target := ""
	if flags.NArg() == 1 {
		target = flags.Arg(0)
	} else {
		discovered, err := discoverScrapsWorker()
		if err != nil {
			fmt.Fprintf(os.Stderr, "scrap: tailnet discovery failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "is this machine joined to the tailnet (tailscale status)?")
			fmt.Fprintln(os.Stderr, "or pass the worker explicitly: scrap attach user@worker")
			return 1
		}
		switch len(discovered) {
		case 0:
			fmt.Fprintln(os.Stderr, "scrap: no online Scraps worker answered on this tailnet")
			fmt.Fprintln(os.Stderr, "install one with: make deploy-worker REMOTE=user@worker")
			fmt.Fprintln(os.Stderr, "then publish it with: sudo scraps-worker tailscale-serve")
			return 1
		case 1:
			target = attachTarget(discovered[0])
			fmt.Printf("Found Scraps worker %s\n", peerName(discovered[0]))
		default:
			fmt.Fprintln(os.Stderr, "scrap: multiple Scraps workers found on this tailnet:")
			for _, peer := range discovered {
				fmt.Fprintf(os.Stderr, "  %s\n", peerName(peer))
			}
			fmt.Fprintln(os.Stderr, "choose one explicitly: scrap attach user@worker")
			return 1
		}
	}
	user, host := currentUser(), target
	if explicit, trimmed, ok := strings.Cut(target, "@"); ok {
		user, host = explicit, trimmed
	}
	// Tailscale SSH deployments commonly permit only a single policy user
	// (often root); when the default user is rejected, retry as root.
	tryUsers := []string{user}
	if user != "root" {
		tryUsers = append(tryUsers, "root")
	}
	var profile clientConfig
	var err error
	for _, candidate := range tryUsers {
		fmt.Printf("Fetching worker profile from %s@%s...\n", candidate, host)
		profile, err = fetchWorkerProfile(candidate + "@" + host)
		if err == nil {
			break
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: could not read the worker profile: %v\n", err)
		fmt.Fprintln(os.Stderr, "the worker must be reachable over SSH with passwordless sudo")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	info, err := client.New(profile.DaemonURL, profile.Token).Info(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: %s did not answer an authenticated /v1/info: %v\n", profile.DaemonURL, err)
		return 1
	}
	if info.Name != "scrapd" {
		fmt.Fprintf(os.Stderr, "scrap: %s is not a scrapd daemon (name %q)\n", profile.DaemonURL, info.Name)
		return 1
	}

	path := clientProfilePath()
	// Re-attaching changes connection details, but environment approvals are
	// an independent local choice and must survive the refresh.
	profile.EnvAllow = readClientProfile(path).EnvAllow
	if err := writeClientProfile(path, profile); err != nil {
		fmt.Fprintf(os.Stderr, "scrap: could not write %s: %v\n", path, err)
		return 1
	}
	fmt.Printf("Configured Scraps client for %s (scrapd v%s)\n", profile.DaemonURL, info.Version)
	fmt.Printf("Profile written to %s with mode 0600.\n", path)

	if extensionInstalled() {
		fmt.Println("Open Pi and run: /scrap")
	} else {
		fmt.Println("The Pi /scrap extension is not installed; from a source checkout run: make install")
	}
	fmt.Println("Local environment variables stay isolated by default; if software needs one, the Pi agent will guide you through approving its name safely.")
	return 0
}

// discoverScrapsWorker finds tailnet peers that look like Scraps workers
// and whose HTTPS endpoint answers the unauthenticated health probe. The
// peer Online flag is only a sort hint; DERP-relayed or dormant peers can
// report offline while remaining fully reachable, so every candidate is
// probed and the probe result decides.
func discoverScrapsWorker() ([]tailnetPeer, error) {
	status, err := runTailscaleStatus()
	if err != nil {
		return nil, err
	}
	var alive []tailnetPeer
	for _, peer := range scrapsWorkerCandidates(status) {
		if probeScrapsDaemon(peerURL(peer)) {
			alive = append(alive, peer)
		}
	}
	return alive, nil
}

func runTailscaleStatus() (tailnetStatus, error) {
	bins := []string{"tailscale"}
	if macApp := "/Applications/Tailscale.app/Contents/MacOS/Tailscale"; fileExists(macApp) {
		bins = append([]string{macApp}, bins...)
	}
	var lastErr error
	for _, bin := range bins {
		out, err := exec.Command(bin, "status", "--json").Output()
		if err != nil {
			lastErr = err
			continue
		}
		var status tailnetStatus
		if err := json.Unmarshal(out, &status); err != nil {
			return tailnetStatus{}, fmt.Errorf("parse tailscale status: %w", err)
		}
		return status, nil
	}
	return tailnetStatus{}, fmt.Errorf("run tailscale status: %w", lastErr)
}

// scrapsWorkerCandidates returns peers tagged tag:scraps-worker or named
// scraps-worker*, online peers first for deterministic selection.
func scrapsWorkerCandidates(status tailnetStatus) []tailnetPeer {
	var online, offline []tailnetPeer
	for _, peer := range status.Peer {
		if !isScrapsWorker(peer) {
			continue
		}
		if peer.Online {
			online = append(online, peer)
		} else {
			offline = append(offline, peer)
		}
	}
	sortPeers(online)
	sortPeers(offline)
	return append(online, offline...)
}

func isScrapsWorker(peer tailnetPeer) bool {
	for _, tag := range peer.Tags {
		if tag == "tag:scraps-worker" {
			return true
		}
	}
	return strings.HasPrefix(strings.ToLower(peerLabel(peer)), "scraps-worker")
}

// peerLabel returns the first DNS label, falling back to the hostname.
func peerLabel(peer tailnetPeer) string {
	name := strings.TrimSuffix(peer.DNSName, ".")
	if label, _, ok := strings.Cut(name, "."); ok && label != "" {
		return label
	}
	return peer.HostName
}

func peerName(peer tailnetPeer) string {
	return strings.TrimSuffix(peer.DNSName, ".")
}

func peerURL(peer tailnetPeer) string {
	return "https://" + peerName(peer)
}

func attachTarget(peer tailnetPeer) string {
	return currentUser() + "@" + peerName(peer)
}

func probeScrapsDaemon(baseURL string) bool {
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(baseURL + "/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	return err == nil && resp.StatusCode == http.StatusOK && strings.TrimSpace(string(body)) == "ok"
}

// fetchWorkerProfile reads the authoritative daemon URL and token from the
// worker over SSH without printing the token.
func fetchWorkerProfile(target string) (clientConfig, error) {
	// BatchMode keeps scripted invocations (make install, install.sh) from
	// hanging on password prompts; the trust path is key- or Tailscale-SSH-
	// based and the remote script requires passwordless sudo regardless.
	args := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ForwardAgent=no",
		"-o", "ClearAllForwardings=yes",
	}
	if config := os.Getenv("SCRAPS_SSH_CONFIG"); config != "" {
		args = append(args, "-F", config)
	}
	args = append(args, target)
	cmd := exec.Command("ssh", args...)
	cmd.Stdin = strings.NewReader(workerProfileScript)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return clientConfig{}, fmt.Errorf("ssh %s: %w", target, err)
	}
	return parseWorkerPayload(string(out))
}

func parseWorkerPayload(payload string) (clientConfig, error) {
	lines := strings.Fields(payload)
	if len(lines) != 2 {
		return clientConfig{}, errors.New("worker returned an unexpected profile payload")
	}
	dnsName, token := lines[0], lines[1]
	if !workerDNSPattern.MatchString(dnsName) {
		return clientConfig{}, errors.New("worker returned an invalid Tailscale DNS name")
	}
	if !workerTokenPattern.MatchString(token) {
		return clientConfig{}, errors.New("worker returned an invalid Scraps token")
	}
	return clientConfig{DaemonURL: "https://" + dnsName, Token: token}, nil
}

func clientProfilePath() string {
	if path := os.Getenv("SCRAPS_CLIENT_CONFIG"); path != "" {
		return path
	}
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "scraps/client.json"
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "scraps", "client.json")
}

// writeClientProfile atomically writes the client profile with mode 0600.
func writeClientProfile(path string, profile clientConfig) error {
	data, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "client.json.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := os.Chmod(name, 0o600); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

func currentUser() string {
	if current, err := user.Current(); err == nil && current.Username != "" {
		return current.Username
	}
	return os.Getenv("USER")
}

func extensionInstalled() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".pi", "agent", "extensions", "scraps"))
	return err == nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func sortPeers(peers []tailnetPeer) {
	for i := 1; i < len(peers); i++ {
		for j := i; j > 0 && peerName(peers[j-1]) > peerName(peers[j]); j-- {
			peers[j-1], peers[j] = peers[j], peers[j-1]
		}
	}
}
