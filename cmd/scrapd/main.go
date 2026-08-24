package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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

	httpServer := &http.Server{
		Addr:              address,
		Handler:           server.New(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	serveError := make(chan error, 1)
	go func() {
		slog.Info("scrapd listening", "address", address)
		serveError <- httpServer.ListenAndServe()
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
