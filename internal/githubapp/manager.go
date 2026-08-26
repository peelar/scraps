// Package githubapp manages a self-hosted GitHub App and its short-lived
// installation credentials. App keys remain in the Scraps control plane.
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
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/peelar/scraps/internal/githubauth"
)

const githubAPI = "https://api.github.com"

type Config struct {
	AppID          int64  `json:"appId"`
	Slug           string `json:"slug"`
	PrivateKey     string `json:"privateKey"`
	InstallationID int64  `json:"installationId,omitempty"`
	CallbackSecret string `json:"callbackSecret"`
}

type FlowStatus struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
	App   string `json:"app,omitempty"`
}

type flow struct {
	callbackBase string
	status       FlowStatus
	created      time.Time
}

type Manager struct {
	mu          sync.Mutex
	refreshMu   sync.Mutex
	background  sync.WaitGroup
	config      Config
	path        string
	flows       map[string]*flow
	client      *http.Client
	apiBase     string
	stop        chan struct{}
	stopped     chan struct{}
	nextRefresh time.Time
	closing     bool
}

func New(dataDir string) (*Manager, error) {
	m := &Manager{
		path:    filepath.Join(dataDir, "github-app.json"),
		flows:   make(map[string]*flow),
		client:  &http.Client{Timeout: 30 * time.Second},
		apiBase: githubAPI,
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	data, err := os.ReadFile(m.path)
	if err == nil {
		if err := json.Unmarshal(data, &m.config); err != nil {
			return nil, fmt.Errorf("decode GitHub App configuration: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read GitHub App configuration: %w", err)
	}
	go m.refreshLoop()
	return m, nil
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.closing = true
	m.mu.Unlock()
	close(m.stop)
	<-m.stopped
	m.background.Wait()
}

func (m *Manager) Start(callbackBase string) (string, string, error) {
	parsed, err := url.Parse(callbackBase)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", "", errors.New("invalid callback URL")
	}
	id, err := randomID()
	if err != nil {
		return "", "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneFlowsLocked()
	status := FlowStatus{State: "waiting_for_github"}
	browserURL := strings.TrimRight(callbackBase, "/") + "/v1/auth/github/manifest?state=" + url.QueryEscape(id)
	if m.config.Slug != "" {
		status = FlowStatus{State: "waiting_for_installation", App: m.config.Slug}
		if m.config.InstallationID != 0 {
			browserURL = fmt.Sprintf("https://github.com/settings/installations/%d", m.config.InstallationID)
		} else {
			browserURL = "https://github.com/apps/" + url.PathEscape(m.config.Slug) + "/installations/new"
		}
	}
	m.flows[id] = &flow{callbackBase: strings.TrimRight(callbackBase, "/"), status: status, created: time.Now()}
	return id, browserURL, nil
}

func (m *Manager) ManifestHTML(state string) (string, error) {
	m.mu.Lock()
	f := m.flows[state]
	m.mu.Unlock()
	if f == nil {
		return "", errors.New("unknown or expired authorization flow")
	}
	manifest := map[string]any{
		"name": "Scraps Self-hosted " + state[:10],
		"url":  "https://github.com/peelar/scraps",
		// GitHub carries the state from the manifest form action to this URL.
		// It rejects redirect_url values that already contain that query state.
		"redirect_url":    f.callbackBase + "/v1/auth/github/manifest/callback",
		"setup_url":       f.callbackBase + "/v1/auth/github/install/callback/" + url.PathEscape(state),
		"setup_on_update": true,
		"public":          false,
		"default_permissions": map[string]string{
			"contents": "write",
		},
		"default_events": []string{},
	}
	encoded, _ := json.Marshal(manifest)
	page := `<!doctype html><html><body><p>Redirecting to GitHub…</p><form id="manifest" method="post" action="https://github.com/settings/apps/new?state=` + url.QueryEscape(state) + `"><input type="hidden" name="manifest" value="` + html.EscapeString(string(encoded)) + `"></form><script>document.getElementById('manifest').submit()</script></body></html>`
	return page, nil
}

func (m *Manager) CompleteManifest(ctx context.Context, state, code string) (string, error) {
	m.mu.Lock()
	f := m.flows[state]
	m.mu.Unlock()
	if f == nil || code == "" {
		return "", errors.New("invalid GitHub manifest callback")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, m.apiBase+"/app-manifests/"+url.PathEscape(code)+"/conversions", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := m.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return "", githubError(response)
	}
	var converted struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
		PEM  string `json:"pem"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&converted); err != nil {
		return "", err
	}
	if converted.ID == 0 || converted.Slug == "" || converted.PEM == "" {
		return "", errors.New("GitHub returned incomplete App credentials")
	}
	m.mu.Lock()
	m.config = Config{AppID: converted.ID, Slug: converted.Slug, PrivateKey: converted.PEM, CallbackSecret: state}
	f.status = FlowStatus{State: "waiting_for_installation", App: converted.Slug}
	err = m.saveLocked()
	m.mu.Unlock()
	if err != nil {
		return "", err
	}
	return "https://github.com/apps/" + url.PathEscape(converted.Slug) + "/installations/new", nil
}

func (m *Manager) CompleteInstallation(callbackSecret string, installationID int64) error {
	m.mu.Lock()
	state := ""
	var newest time.Time
	for candidate, candidateFlow := range m.flows {
		if candidateFlow.status.State == "waiting_for_installation" && candidateFlow.created.After(newest) {
			state, newest = candidate, candidateFlow.created
		}
	}
	f := m.flows[state]
	if m.closing || f == nil || installationID == 0 || m.config.AppID == 0 || callbackSecret != m.config.CallbackSecret {
		m.mu.Unlock()
		return errors.New("invalid GitHub installation callback")
	}
	m.config.InstallationID = installationID
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	f.status.State = "configuring"
	m.mu.Unlock()

	// Do not tie provider configuration to the browser request. GitHub only
	// needs an immediate acknowledgement; the CLI independently tracks when
	// the installation credential is actually ready.
	m.background.Add(1)
	go func() {
		defer m.background.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := m.Refresh(ctx); err != nil {
			m.failFlow(state, err)
			return
		}
		m.mu.Lock()
		if current := m.flows[state]; current != nil {
			current.status.State = "complete"
		}
		m.mu.Unlock()
	}()
	return nil
}

func (m *Manager) Status(state string) (FlowStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.flows[state]
	if f == nil {
		return FlowStatus{}, false
	}
	return f.status, true
}

func (m *Manager) Refresh(ctx context.Context) error {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	m.mu.Lock()
	config := m.config
	m.mu.Unlock()
	if config.AppID == 0 || config.InstallationID == 0 {
		return errors.New("GitHub App is not installed")
	}
	jwt, err := signJWT(config.AppID, config.PrivateKey, time.Now())
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/app/installations/%d/access_tokens", m.apiBase, config.InstallationID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{"permissions":{"contents":"write"}}`))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := m.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return githubError(response)
	}
	var credential struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&credential); err != nil {
		return err
	}
	if credential.Token == "" {
		return errors.New("GitHub returned an empty installation token")
	}
	defer func() { credential.Token = "" }()
	if err := githubauth.Configure(ctx, credential.Token); err != nil {
		return err
	}
	refreshAt := credential.ExpiresAt.Add(-15 * time.Minute)
	if credential.ExpiresAt.IsZero() {
		refreshAt = time.Now().Add(45 * time.Minute)
	}
	m.mu.Lock()
	m.nextRefresh = refreshAt
	m.mu.Unlock()
	return githubauth.AttachExisting(ctx)
}

func (m *Manager) refreshLoop() {
	defer close(m.stopped)
	m.refreshIfDue()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.refreshIfDue()
		case <-m.stop:
			return
		}
	}
}

func (m *Manager) refreshIfDue() {
	m.mu.Lock()
	due := m.config.InstallationID != 0 && (m.nextRefresh.IsZero() || !time.Now().Before(m.nextRefresh))
	if due {
		// On transient failure retry at the next minute tick rather than waiting
		// until an installation token has expired.
		m.nextRefresh = time.Now().Add(time.Minute)
	}
	m.mu.Unlock()
	if !due {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	if err := m.Refresh(ctx); err != nil {
		slog.Warn("refresh GitHub App installation credential", "error", err)
	}
	cancel()
}

func (m *Manager) failFlow(state string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f := m.flows[state]; f != nil {
		f.status = FlowStatus{State: "error", Error: err.Error()}
	}
}

func (m *Manager) saveLocked() error {
	encoded, err := json.MarshalIndent(m.config, "", "  ")
	if err != nil {
		return err
	}
	temporary := m.path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, m.path)
}

func (m *Manager) pruneFlowsLocked() {
	for id, f := range m.flows {
		if time.Since(f.created) > 20*time.Minute {
			delete(m.flows, id)
		}
	}
}

func randomID() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func signJWT(appID int64, privatePEM string, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(privatePEM))
	if block == nil {
		return "", errors.New("invalid GitHub App private key")
	}
	var key *rsa.PrivateKey
	if parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = parsed
	} else {
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return "", errors.New("invalid GitHub App private key")
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("GitHub App key is not RSA")
		}
	}
	encode := base64.RawURLEncoding.EncodeToString
	header := encode([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]int64{"iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": appID})
	unsigned := header + "." + encode(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + encode(signature), nil
}

func githubError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	var payload struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &payload)
	if payload.Message == "" {
		payload.Message = strings.TrimSpace(string(body))
	}
	return fmt.Errorf("GitHub API %s: %s", response.Status, payload.Message)
}
