package main

import "testing"

func TestImageFromEnv(t *testing.T) {
	t.Setenv("SCRAPD_OPENSHELL_IMAGE", "openshell-image")
	if got := imageFromEnv(); got != "openshell-image" {
		t.Fatalf("imageFromEnv() = %q", got)
	}

	t.Setenv("SCRAPD_OPENSHELL_IMAGE", "")
	if got := imageFromEnv(); got != defaultImage {
		t.Fatalf("imageFromEnv() = %q", got)
	}
}

func TestSetupHelp(t *testing.T) {
	if code := runSetup([]string{"--help"}); code != 0 {
		t.Fatalf("runSetup(--help) = %d, want 0", code)
	}
}
