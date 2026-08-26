package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvedClientConfigLoadsProfileAndEnvironmentOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	if err := os.WriteFile(path, []byte(`{"daemon_url":"https://worker.example","token":"profile-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRAPS_CLIENT_CONFIG", path)
	configured := resolvedClientConfig()
	if configured.DaemonURL != "https://worker.example" || configured.Token != "profile-token" {
		t.Fatalf("profile = %+v", configured)
	}
	t.Setenv("SCRAP_DAEMON_URL", "http://override:8484")
	t.Setenv("SCRAP_TOKEN", "override-token")
	configured = resolvedClientConfig()
	if configured.DaemonURL != "http://override:8484" || configured.Token != "override-token" {
		t.Fatalf("overridden profile = %+v", configured)
	}
	t.Setenv("SCRAP_TOKEN", "")
	if configured = resolvedClientConfig(); configured.Token != "" {
		t.Fatalf("empty token must override profile for authentication checks: %+v", configured)
	}
}

func TestResolvedClientConfigUsesXDGCompatibleDefaultPath(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "scraps", "client.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"daemon_url":"https://worker.example","token":"profile-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("SCRAPS_CLIENT_CONFIG", "")
	configured := resolvedClientConfig()
	if configured.DaemonURL != "https://worker.example" || configured.Token != "profile-token" {
		t.Fatalf("default profile = %+v", configured)
	}
}
