package main

import "testing"

func TestRunHelp(t *testing.T) {
	if code := run([]string{"help"}); code != 0 {
		t.Fatalf("run(help) = %d, want 0", code)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	if code := run([]string{"nope"}); code == 0 {
		t.Fatal("run(nope) succeeded, want failure")
	}
}
