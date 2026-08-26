package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestScrapsWorkerCandidates(t *testing.T) {
	status := tailnetStatus{Peer: map[string]tailnetPeer{
		"100.64.1.1": {DNSName: "scraps-worker.tailnet.ts.net.", HostName: "worker", Online: true},
		"100.64.1.2": {DNSName: "Scraps-Worker2.tailnet.ts.net.", Online: true},
		"100.64.1.3": {DNSName: "buildbox.tailnet.ts.net.", Online: true, Tags: []string{"tag:scraps-worker"}},
		"100.64.1.4": {DNSName: "old-worker.tailnet.ts.net.", Tags: []string{"tag:scraps-worker"}},
		"100.64.1.5": {DNSName: "laptop.tailnet.ts.net.", Online: true},
		"100.64.1.6": {HostName: "scraps-worker-nuc", Online: false},
	}}
	candidates := scrapsWorkerCandidates(status)
	if len(candidates) != 5 {
		t.Fatalf("candidates = %d, want 5 (including offline): %+v", len(candidates), candidates)
	}
	// Online peers sort first; offline tag and hostname matches are still probed.
	for i := 0; i < 2; i++ {
		if !candidates[i].Online {
			t.Fatalf("candidate %d is offline; online peers must sort first: %+v", i, candidates)
		}
	}
	for _, peer := range candidates {
		if !isScrapsWorker(peer) {
			t.Fatalf("non-worker peer classified as candidate: %+v", peer)
		}
	}
}

func TestPeerLabelFallback(t *testing.T) {
	peer := tailnetPeer{DNSName: "tailnet.ts.net.", HostName: "scraps-worker-x"}
	if got := peerLabel(peer); got != "tailnet" {
		t.Fatalf("peerLabel = %q, want first DNS label", got)
	}
	peer = tailnetPeer{DNSName: "", HostName: "scraps-worker-x"}
	if got := peerLabel(peer); got != "scraps-worker-x" {
		t.Fatalf("peerLabel = %q, want hostname fallback", got)
	}
}

func TestProbeScrapsDaemon(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    bool
	}{
		{"ok", func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte("ok\n"))
		}, true},
		{"wrong body", func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte("hello\n"))
		}, false},
		{"not found", func(w http.ResponseWriter, _ *http.Request) {
			http.NotFound(w, nil)
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()
			if got := probeScrapsDaemon(server.URL); got != tt.want {
				t.Fatalf("probeScrapsDaemon = %v, want %v", got, tt.want)
			}
		})
	}
	if probeScrapsDaemon("http://127.0.0.1:1") {
		t.Fatal("probe of an unreachable URL must fail")
	}
}

func TestParseWorkerPayload(t *testing.T) {
	token := strings.Repeat("ab", 32)
	profile, err := parseWorkerPayload("scraps-worker.tailnet.ts.net\n" + token + "\n")
	if err != nil {
		t.Fatalf("parseWorkerPayload: %v", err)
	}
	if profile.DaemonURL != "https://scraps-worker.tailnet.ts.net" || profile.Token != token {
		t.Fatalf("profile = %+v", profile)
	}
	for name, payload := range map[string]string{
		"bad dns":     "scraps worker\n" + token,
		"bad token":   "scraps-worker\nnothex",
		"extra lines": "scraps-worker\n" + token + "\nsurprise",
		"empty":       "",
	} {
		if _, err := parseWorkerPayload(payload); err == nil {
			t.Fatalf("parseWorkerPayload(%s) must fail", name)
		}
	}
}

func TestWriteClientProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scraps", "client.json")
	profile := clientConfig{DaemonURL: "https://scraps-worker.tailnet.ts.net", Token: strings.Repeat("cd", 32)}
	if err := writeClientProfile(path, profile); err != nil {
		t.Fatalf("writeClientProfile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("profile mode = %o, want 600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var roundTripped clientConfig
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(roundTripped, profile) {
		t.Fatalf("round trip = %+v, want %+v", roundTripped, profile)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("config dir has %d entries, want only client.json", len(entries))
	}
}

func TestRunAttachUsage(t *testing.T) {
	if code := run([]string{"attach", "--help"}); code != 0 {
		t.Fatalf("attach --help exit = %d, want 0", code)
	}
	if code := run([]string{"attach", "a", "b"}); code != 2 {
		t.Fatalf("attach with two args exit = %d, want 2", code)
	}
}
