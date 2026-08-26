package main

import "testing"

func TestAuthHelp(t *testing.T) {
	if code := runAuth([]string{"github", "--help"}); code != 0 {
		t.Fatalf("runAuth(--help) = %d", code)
	}
}

func TestAuthRejectsUnknownProvider(t *testing.T) {
	if code := runAuth([]string{"gitlab"}); code != 2 {
		t.Fatalf("runAuth(gitlab) = %d", code)
	}
}
