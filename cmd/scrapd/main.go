package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/peelar/scraps/internal/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "scrapd: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	address := os.Getenv("SCRAPD_LISTEN_ADDR")
	if address == "" {
		address = "127.0.0.1:8484"
	}
	dataDir := os.Getenv("SCRAPD_DATA_DIR")
	if dataDir == "" {
		userConfigDir, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("resolve data dir: %w", err)
		}
		dataDir = filepath.Join(userConfigDir, "scrapd")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	apiServer, err := server.New(server.Config{
		DataDir:        dataDir,
		Token:          os.Getenv("SCRAPD_TOKEN"),
		OpenShellImage: os.Getenv("SCRAPD_OPENSHELL_IMAGE"),
	})
	if err != nil {
		return err
	}
	defer apiServer.Close()

	httpServer := &http.Server{
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Bind explicitly so port conflicts fail before the pid file exists.
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen %s: %w", address, err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	serveError := make(chan error, 1)
	go func() {
		slog.Info("scrapd listening", "address", address)
		serveError <- httpServer.Serve(listener)
	}()

	select {
	case err := <-serveError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case received := <-stop:
		slog.Info("shutting down", "signal", received.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpServer.Shutdown(ctx)
}
