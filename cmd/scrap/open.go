// scrap open tunnels a workspace service onto a local port and opens it in
// the browser: `scrap open [workspace-id] [port]`. Workspace and port are
// auto-detected when unambiguous, the local listener binds loopback only,
// and each local connection opens its own stream through scrapd.

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/peelar/scraps/internal/client"
	"github.com/peelar/scraps/internal/workspace"
)

func runOpen(args []string) int {
	flags := flag.NewFlagSet("open", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	noBrowser := flags.Bool("no-browser", false, "print the URL instead of opening a browser")
	localPort := flags.Int("local", 0, "local port to listen on (default: the workspace port)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	rest := flags.Args()
	if len(rest) > 2 {
		fmt.Fprintln(os.Stderr, "usage: scrap open [--no-browser] [--local <port>] [<workspace-id>] [<port>]")
		return 2
	}

	api := newClientFromEnv()
	resolveCtx, cancelResolve := context.WithTimeout(context.Background(), 30*time.Second)
	workspaceID, ok := resolveOpenWorkspace(resolveCtx, api, argAt(rest, 0))
	if !ok {
		cancelResolve()
		return 1
	}
	remotePort, ok := resolveOpenPort(resolveCtx, api, workspaceID, argAt(rest, 1))
	cancelResolve()
	if !ok {
		return 1
	}

	listener, listenPort, err := listenLocal(remotePort, *localPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: listen on 127.0.0.1: %v\n", err)
		return 1
	}
	preview := (&url.URL{Scheme: "http", Host: net.JoinHostPort("localhost", strconv.Itoa(listenPort))}).String()
	fmt.Printf("tunneling %s:%d → %s\n", workspaceID, remotePort, preview)
	if !*noBrowser {
		if err := openBrowser(preview); err != nil {
			fmt.Printf("open %s in your browser\n", preview)
		}
	}
	fmt.Println("Ctrl-C to stop")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var connections sync.WaitGroup
	defer connections.Wait()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "scrap: accept: %v\n", err)
				return 1
			}
			break
		}
		connections.Add(1)
		go func() {
			defer connections.Done()
			proxyConnection(ctx, api, workspaceID, remotePort, conn)
		}()
	}
	fmt.Println("\ntunnel closed")
	return 0
}

func argAt(args []string, index int) string {
	if index >= len(args) {
		return ""
	}
	return args[index]
}

func resolveOpenWorkspace(ctx context.Context, api *client.Client, idArg string) (string, bool) {
	if idArg != "" {
		found, err := api.GetWorkspace(ctx, idArg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "scrap: %v\n", err)
			return "", false
		}
		if found.State != "running" {
			fmt.Fprintf(os.Stderr, "scrap: workspace %s is %s — start it with: scrap start %s\n", idArg, found.State, idArg)
			return "", false
		}
		return idArg, true
	}
	workspaces, err := api.ListWorkspaces(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: %v\n", err)
		return "", false
	}
	var running []workspace.Workspace
	for _, w := range workspaces {
		if w.State == "running" {
			running = append(running, w)
		}
	}
	switch len(running) {
	case 0:
		fmt.Fprintln(os.Stderr, "scrap: no running workspaces — create one in Pi with: /scrap")
		return "", false
	case 1:
		return running[0].ID, true
	default:
		fmt.Fprintln(os.Stderr, "scrap: multiple running workspaces — pick one:")
		for _, w := range running {
			fmt.Fprintf(os.Stderr, "  %s\n", w.ID)
		}
		return "", false
	}
}

func resolveOpenPort(ctx context.Context, api *client.Client, workspaceID, portArg string) (int, bool) {
	if portArg != "" {
		port, err := strconv.Atoi(portArg)
		if err != nil || port < 1 || port > 65535 {
			fmt.Fprintf(os.Stderr, "scrap: invalid port %q\n", portArg)
			return 0, false
		}
		return port, true
	}
	ports, err := api.WorkspacePorts(ctx, workspaceID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: list ports: %v\n", err)
		return 0, false
	}
	switch len(ports) {
	case 0:
		fmt.Fprintf(os.Stderr, "scrap: nothing is listening inside %s — start a dev server first, or pass one: scrap open %s <port>\n", workspaceID, workspaceID)
		return 0, false
	case 1:
		return ports[0].Port, true
	default:
		fmt.Fprintf(os.Stderr, "scrap: several ports are listening inside %s:\n", workspaceID)
		for _, p := range ports {
			fmt.Fprintf(os.Stderr, "  %5d  %s\n", p.Port, p.Address)
		}
		fmt.Fprintf(os.Stderr, "pick one: scrap open %s <port>\n", workspaceID)
		return 0, false
	}
}

// listenLocal binds loopback. An explicit port must be free; the default
// tries the workspace port first, then the following hundred, then any.
func listenLocal(remotePort, explicit int) (net.Listener, int, error) {
	if explicit != 0 {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(explicit)))
		if err != nil {
			return nil, 0, err
		}
		return listener, explicit, nil
	}
	for port := remotePort; port < remotePort+100; port++ {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			return listener, port, nil
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	return listener, listener.Addr().(*net.TCPAddr).Port, nil
}

func proxyConnection(ctx context.Context, api *client.Client, workspaceID string, remotePort int, conn net.Conn) {
	defer conn.Close()
	tunnel, err := api.Tunnel(ctx, workspaceID, remotePort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: tunnel %s:%d: %v\n", workspaceID, remotePort, err)
		return
	}
	var done sync.WaitGroup
	done.Add(2)
	go func() {
		defer done.Done()
		_, _ = io.Copy(tunnel, conn)
		// Propagate the browser's connection close as a service-side EOF.
		_ = tunnel.CloseWrite()
	}()
	go func() {
		defer done.Done()
		_, _ = io.Copy(conn, tunnel)
	}()
	done.Wait()
	_ = tunnel.Close()
}
