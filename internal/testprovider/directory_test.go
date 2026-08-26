package testprovider

import (
	"bufio"
	"context"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peelar/scraps/internal/store"
)

func newTestDirectory(t *testing.T) *Directory {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "scrapd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	directory, err := NewDirectory(st, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func startEcho(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

func TestDirectoryTunnelDialsHostLoopback(t *testing.T) {
	directory := newTestDirectory(t)
	created, err := directory.Create(context.Background(), testOptions())
	if err != nil {
		t.Fatal(err)
	}
	port := startEcho(t)

	conn, err := directory.Tunnel(context.Background(), created.ID, port)
	if err != nil {
		t.Fatalf("tunnel: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != "ping\n" {
		t.Fatalf("echo = %q err = %v", line, err)
	}

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	_, err = directory.Tunnel(context.Background(), created.ID, deadPort)
	if err == nil || !strings.Contains(err.Error(), "dial 127.0.0.1") {
		t.Fatalf("err = %v, want dial failure", err)
	}
}

func TestDirectoryPortsProbesCandidates(t *testing.T) {
	directory := newTestDirectory(t)
	created, err := directory.Create(context.Background(), testOptions())
	if err != nil {
		t.Fatal(err)
	}
	port := startEcho(t)
	previous := ProbePorts
	ProbePorts = []int{port, port + 1}
	defer func() { ProbePorts = previous }()

	ports, err := directory.Ports(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 || ports[0].Port != port || ports[0].Address != "127.0.0.1" {
		t.Fatalf("ports = %+v", ports)
	}
}

func testOptions() struct{ Project, RepoURL string } {
	return struct{ Project, RepoURL string }{Project: "demo", RepoURL: ""}
}
