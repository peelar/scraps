package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunEnvAllowDenyAndClearStoresNamesOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scraps", "client.json")
	t.Setenv("SCRAPS_CLIENT_CONFIG", path)
	t.Setenv("DATABASE_URL", "postgres://sentinel-secret")

	if code := runEnv([]string{"allow", "STRIPE_API_KEY", "DATABASE_URL", "DATABASE_URL"}); code != 0 {
		t.Fatalf("allow exit = %d", code)
	}
	profile := readClientProfile(path)
	if want := []string{"DATABASE_URL", "STRIPE_API_KEY"}; !reflect.DeepEqual(profile.EnvAllow, want) {
		t.Fatalf("env_allow = %#v, want %#v", profile.EnvAllow, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "postgres://sentinel-secret") {
		t.Fatal("profile contains an environment value")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("profile mode = %v, err = %v", info.Mode().Perm(), err)
	}

	if code := runEnv([]string{"deny", "DATABASE_URL"}); code != 0 {
		t.Fatalf("deny exit = %d", code)
	}
	if got := readClientProfile(path).EnvAllow; !reflect.DeepEqual(got, []string{"STRIPE_API_KEY"}) {
		t.Fatalf("after deny = %#v", got)
	}
	if code := runEnv([]string{"clear"}); code != 0 {
		t.Fatalf("clear exit = %d", code)
	}
	if got := readClientProfile(path).EnvAllow; len(got) != 0 {
		t.Fatalf("after clear = %#v", got)
	}
}

func TestRunEnvRejectsInvalidAndReservedNames(t *testing.T) {
	t.Setenv("SCRAPS_CLIENT_CONFIG", filepath.Join(t.TempDir(), "client.json"))
	for _, name := range []string{"BAD-NAME", "PATH", "SCRAP_TOKEN", "OPENSHELL_TOKEN"} {
		if code := runEnv([]string{"allow", name}); code == 0 {
			t.Fatalf("allow %q succeeded", name)
		}
	}
}

func TestRunEnvDoesNotOverwriteMalformedProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	original := []byte("{not-json\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRAPS_CLIENT_CONFIG", path)
	if code := runEnv([]string{"allow", "DATABASE_URL"}); code == 0 {
		t.Fatal("allow succeeded with malformed profile")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(data, original) {
		t.Fatalf("malformed profile was overwritten: %q", data)
	}
}

func TestRunEnvHelpAndUsage(t *testing.T) {
	if code := runEnv([]string{"--help"}); code != 0 {
		t.Fatalf("help exit = %d", code)
	}
	if code := runEnv(nil); code != 2 {
		t.Fatalf("missing command exit = %d", code)
	}
}
