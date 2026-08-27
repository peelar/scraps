package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/peelar/scraps/internal/extension"
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

	piCommand := os.Getenv("SCRAPD_PI_COMMAND")
	piExtensionPath := ""
	if piCommand != "" {
		resolvedCommand, err := exec.LookPath(piCommand)
		if err != nil {
			return fmt.Errorf("durable Pi runner %q is unavailable: %w", piCommand, err)
		}
		piCommand = resolvedCommand
		piExtensionPath, err = extension.Install(filepath.Join(dataDir, "pi-runner-extension"))
		if err != nil {
			return fmt.Errorf("install durable Pi runner extension: %w", err)
		}
	}
	apiServer, err := server.New(server.Config{
		DataDir:             dataDir,
		Token:               os.Getenv("SCRAPD_TOKEN"),
		OpenShellImage:      os.Getenv("SCRAPD_OPENSHELL_IMAGE"),
		PiCommand:           piCommand,
		PiExtensionPath:     piExtensionPath,
		PiProfilePath:       filepath.Join(dataDir, "pi-profile"),
		DaemonURL:           "http://127.0.0.1:8484",
		ModelAuthConfigured: hasModelAuth(filepath.Join(dataDir, "pi-profile")),
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

func hasModelAuth(profileDir string) bool {
	for _, name := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "OPENROUTER_API_KEY",
		"AZURE_OPENAI_API_KEY", "DEEPSEEK_API_KEY", "NVIDIA_API_KEY", "MISTRAL_API_KEY",
		"GROQ_API_KEY", "CEREBRAS_API_KEY", "XAI_API_KEY", "AI_GATEWAY_API_KEY",
		"AWS_BEARER_TOKEN_BEDROCK", "GOOGLE_APPLICATION_CREDENTIALS",
	} {
		if os.Getenv(name) != "" {
			return true
		}
	}
	info, err := os.Stat(filepath.Join(profileDir, "auth.json"))
	return err == nil && info.Size() > 2
}
