package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/peelar/scraps/internal/store"
	"github.com/peelar/scraps/internal/workspace"
)

func TestDockerProviderIntegration(t *testing.T) {
	if os.Getenv("SCRAP_TEST_DOCKER") != "1" {
		t.Skip("set SCRAP_TEST_DOCKER=1 after `make docker-image`")
	}
	t.Setenv("SCRAPS_DOCKER_HOST_SECRET", "must-not-leak")
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "docker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d, err := NewDocker(ctx, st, "")
	if err != nil {
		t.Fatal(err)
	}

	first, err := d.Create(ctx, workspace.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.Create(ctx, workspace.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Delete(ctx, first.ID)
	defer d.Delete(ctx, second.ID)

	if err := d.WriteFile(ctx, first.ID, "persistent.txt", []byte("private")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.ReadFile(ctx, second.ID, "persistent.txt", 1024); err == nil {
		t.Fatal("parallel workspace read another workspace volume")
	}

	output, code, reason := dockerTestExec(t, d, first.ID, `uname -s; printf 'secret=%s\n' "${SCRAPS_DOCKER_HOST_SECRET-unset}"; pwd`)
	if code != 0 || reason != "" || output != "Linux\nsecret=unset\n/workspace\n" {
		t.Fatalf("sandbox output=%q code=%d reason=%q", output, code, reason)
	}
	_, _, _ = dockerTestExec(t, d, first.ID, `ln -s /etc outside`)
	if _, _, err := d.ReadFile(ctx, first.ID, "outside/passwd", 1<<20); err == nil {
		t.Fatal("filesystem API followed a symlink outside /workspace")
	}

	inspect, err := d.run(ctx, nil, "inspect", "--format", `{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} {{.HostConfig.PidsLimit}} {{.HostConfig.Privileged}} {{len .HostConfig.PortBindings}}`, dockerContainer(first.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(inspect)) != "2000000000 4294967296 512 false 0" {
		t.Fatalf("container limits = %q", inspect)
	}

	if err := d.Stop(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	stopped, err := d.Get(ctx, first.ID)
	if err != nil || stopped.State != "stopped" {
		t.Fatalf("stopped state = %+v, %v", stopped, err)
	}
	if _, _, err := d.ReadFile(ctx, first.ID, "persistent.txt", 1024); err == nil {
		t.Fatal("read succeeded while stopped")
	}
	if err := d.Start(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	got, _, err := d.ReadFile(ctx, first.ID, "persistent.txt", 1024)
	if err != nil || string(got) != "private" {
		t.Fatalf("data after stop/start = %q, %v", got, err)
	}

	baselineSleeps, err := d.execOutput(ctx, first.ID, nil, "pgrep", "-x", "sleep")
	if err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	var cancelReason string
	go func() {
		_ = d.Exec(cancelCtx, first.ID, ExecRequest{Command: "echo ready; sleep 60"}, func(e ExecEvent) {
			if e.Type == "output" && strings.Contains(string(e.Data), "ready") {
				cancel()
			}
			if e.Type == "exit" {
				cancelReason = e.Reason
			}
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled exec did not terminate")
	}
	if cancelReason != "cancelled" {
		t.Fatalf("cancel reason = %q", cancelReason)
	}
	var processes []byte
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		processes, err = d.execOutput(ctx, first.ID, nil, "pgrep", "-x", "sleep")
		if err == nil && strings.TrimSpace(string(processes)) == strings.TrimSpace(string(baselineSleeps)) {
			break
		}
	}
	if strings.TrimSpace(string(processes)) != strings.TrimSpace(string(baselineSleeps)) {
		t.Fatalf("cancelled sleep remains: before=%q after=%q", baselineSleeps, processes)
	}

	container, volume := dockerContainer(first.ID), dockerVolume(first.ID)
	if err := d.Delete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.run(ctx, nil, "inspect", container); err == nil {
		t.Fatal("container remains after delete")
	}
	if _, err := d.run(ctx, nil, "volume", "inspect", volume); err == nil {
		t.Fatal("volume remains after delete")
	}
}

func dockerTestExec(t *testing.T, d *Docker, id, command string) (string, int, string) {
	t.Helper()
	var output strings.Builder
	code := -1
	reason := ""
	err := d.Exec(context.Background(), id, ExecRequest{Command: command}, func(e ExecEvent) {
		if e.Type == "output" {
			output.Write(e.Data)
		}
		if e.Type == "exit" {
			if e.Code != nil {
				code = *e.Code
			}
			reason = e.Reason
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return output.String(), code, reason
}
