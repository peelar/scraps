package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testPrivateKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return key, string(encoded)
}

func TestSignJWT(t *testing.T) {
	key, encoded := testPrivateKey(t)
	now := time.Unix(1_700_000_000, 0)
	token, err := signJWT(1234, encoded, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT has %d parts", len(parts))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("verify JWT: %v", err)
	}
	claimsJSON, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]int64
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["iss"] != 1234 || claims["exp"] <= claims["iat"] {
		t.Fatalf("unexpected claims: %#v", claims)
	}
}

func TestAuthorizationStateExpires(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	state, _, err := manager.Start("https://scraps-worker.example.ts.net")
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.flows[state].created = time.Now().Add(-21 * time.Minute)
	manager.mu.Unlock()
	if _, ok := manager.Status(state); ok {
		t.Fatal("expired authorization state remained valid")
	}
	if _, err := manager.ManifestHTML(state); err == nil {
		t.Fatal("expired authorization state opened a manifest")
	}
}

func TestManifestInstallationConfiguresOpenShell(t *testing.T) {
	_, privateKey := testPrivateKey(t)
	github := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case strings.Contains(request.URL.Path, "/app-manifests/"):
			response.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(response).Encode(map[string]any{"id": 42, "slug": "scraps-test", "pem": privateKey})
		case strings.Contains(request.URL.Path, "/access_tokens"):
			if !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") {
				t.Error("installation token request missing App JWT")
			}
			response.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(response).Encode(map[string]string{"token": "installation-secret"})
		default:
			http.NotFound(response, request)
		}
	}))
	defer github.Close()

	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$APP_TEST_CALLS"
if [ "$1 $2 $3" = "sandbox list --output" ]; then printf '[]\n'; fi
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "openshell"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("APP_TEST_CALLS", calls)

	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	manager.apiBase = github.URL
	state, _, err := manager.Start("http://127.0.0.1:8484")
	if err != nil {
		t.Fatal(err)
	}
	page, err := manager.ManifestHTML(state)
	if err != nil || !strings.Contains(page, "contents") {
		t.Fatalf("ManifestHTML() error=%v page=%q", err, page)
	}
	if strings.Contains(page, "manifest/callback?state=") {
		t.Fatalf("redirect_url must not contain state: %q", page)
	}
	if !strings.Contains(page, "/install/callback/"+state) {
		t.Fatalf("setup callback secret missing from path: %q", page)
	}
	installURL, err := manager.CompleteManifest(context.Background(), state, "manifest-code")
	if err != nil {
		t.Fatal(err)
	}
	if installURL != "https://github.com/apps/scraps-test/installations/new" {
		t.Fatalf("install URL = %q", installURL)
	}
	if err := manager.CompleteInstallation(state, 99); err != nil {
		t.Fatal(err)
	}
	status, ok := manager.Status(state)
	if !ok || status.State != "configuring" {
		t.Fatalf("initial status = %#v, %v", status, ok)
	}
	deadline := time.Now().Add(5 * time.Second)
	for status.State == "configuring" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		status, ok = manager.Status(state)
	}
	if !ok || status.State != "complete" {
		t.Fatalf("final status = %#v, %v", status, ok)
	}
	logged, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logged), "installation-secret") {
		t.Fatalf("token leaked into argv: %s", logged)
	}
	if !strings.Contains(string(logged), "provider update github-push --from-existing") {
		t.Fatalf("provider was not configured: %s", logged)
	}
	_, reconnectURL, err := manager.Start("http://127.0.0.1:8484")
	if err != nil {
		t.Fatal(err)
	}
	if reconnectURL != "https://github.com/settings/installations/99" {
		t.Fatalf("reconnect URL = %q", reconnectURL)
	}
}
