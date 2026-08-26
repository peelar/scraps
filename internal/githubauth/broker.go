package githubauth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Configure stores an installation token in OpenShell's broker. The token is
// passed only through the openshell process environment and never in argv.
func Configure(ctx context.Context, token string) error {
	dir, err := os.MkdirTemp("", "scrap-github-profile-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	profilePath := filepath.Join(dir, "github-push.yaml")
	if err := os.WriteFile(profilePath, Profile, 0o600); err != nil {
		return err
	}
	if err := run(ctx, nil, "status"); err != nil {
		return fmt.Errorf("OpenShell gateway unavailable: %w", err)
	}
	if err := run(ctx, nil, "provider", "profile", "export", ProfileID, "--output", "json"); err == nil {
		if err := run(ctx, nil, "provider", "profile", "update", ProfileID, "--file", profilePath); err != nil {
			return fmt.Errorf("update profile: %w", err)
		}
	} else if err := run(ctx, nil, "provider", "profile", "import", "--file", profilePath); err != nil {
		return fmt.Errorf("import profile: %w", err)
	}
	env := []string{TokenEnvironment + "=" + token}
	if err := run(ctx, nil, "provider", "get", ProfileID); err == nil {
		if err := run(ctx, env, "provider", "update", ProfileID, "--from-existing"); err != nil {
			return fmt.Errorf("update credential: %w", err)
		}
	} else if err := run(ctx, env, "provider", "create", "--name", ProfileID, "--type", ProfileID, "--from-existing"); err != nil {
		return fmt.Errorf("create credential: %w", err)
	}
	return nil
}

// AttachExisting attaches the configured provider to all Scraps sandboxes.
func AttachExisting(ctx context.Context) error {
	output, err := exec.CommandContext(ctx, "openshell", "sandbox", "list", "--output", "json").Output()
	if err != nil {
		return fmt.Errorf("list sandboxes: %w", err)
	}
	// Names and labels are extracted without importing the provider package.
	var sandboxes []struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	}
	if err := json.Unmarshal(output, &sandboxes); err != nil {
		return fmt.Errorf("decode sandboxes: %w", err)
	}
	for _, sandbox := range sandboxes {
		if sandbox.Labels["dev.scraps.workspace"] == "" {
			continue
		}
		listed, err := exec.CommandContext(ctx, "openshell", "sandbox", "provider", "list", sandbox.Name).CombinedOutput()
		if err == nil && strings.Contains(string(listed), ProfileID) {
			continue
		}
		if err := run(ctx, nil, "sandbox", "provider", "attach", sandbox.Name, ProfileID); err != nil {
			return fmt.Errorf("attach to %s: %w", sandbox.Name, err)
		}
	}
	return nil
}

func run(ctx context.Context, extraEnv []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "openshell", args...)
	cmd.Env = cleanEnvironment(extraEnv)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, message)
}

func cleanEnvironment(extra []string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "GH_TOKEN" && key != "GITHUB_TOKEN" {
			env = append(env, entry)
		}
	}
	return append(env, extra...)
}
