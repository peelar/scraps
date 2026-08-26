package main

import (
	"net"
	"testing"
)

func TestListenLocalPrefersFreePortThenFallsBack(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	busy := listener.Addr().(*net.TCPAddr).Port

	// With the preferred port free, it is used exactly.
	free, port, err := listenLocal(busy+50, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer free.Close()
	if port != busy+50 {
		t.Fatalf("port = %d, want %d", port, busy+50)
	}

	// With the preferred port busy, the next free port is chosen.
	fallback, got, err := listenLocal(busy, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer fallback.Close()
	if got == busy || got < busy {
		t.Fatalf("fallback port = %d, want > %d", got, busy)
	}

	// An explicit port must be honored exactly, busy or not.
	if _, _, err = listenLocal(0, busy); err == nil {
		t.Fatal("explicit busy port should fail")
	}
	explicit, got, err := listenLocal(0, busy+51)
	if err != nil {
		t.Fatal(err)
	}
	defer explicit.Close()
	if got != busy+51 {
		t.Fatalf("explicit port = %d", got)
	}
}

func TestArgAt(t *testing.T) {
	args := []string{"one", "2"}
	if argAt(args, 0) != "one" || argAt(args, 1) != "2" || argAt(args, 2) != "" || argAt(nil, 0) != "" {
		t.Fatal("argAt bounds handling broken")
	}
}
