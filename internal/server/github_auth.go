package server

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
)

func (s *Server) startGitHubAuth(response http.ResponseWriter, request *http.Request) {
	var body struct {
		CallbackURL string `json:"callbackUrl"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "callbackUrl is required")
		return
	}
	state, browserURL, err := s.github.Start(body.CallbackURL)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeJSON(response, http.StatusCreated, map[string]string{"state": state, "browserUrl": browserURL})
}

func (s *Server) githubAuthStatus(response http.ResponseWriter, request *http.Request) {
	status, ok := s.github.Status(request.PathValue("state"))
	if !ok {
		writeError(response, http.StatusNotFound, "not_found", "authorization flow not found or expired")
		return
	}
	writeJSON(response, http.StatusOK, status)
}

func (s *Server) githubManifest(response http.ResponseWriter, request *http.Request) {
	page, err := s.github.ManifestHTML(request.URL.Query().Get("state"))
	if err != nil {
		browserError(response, http.StatusBadRequest, err)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	_, _ = response.Write([]byte(page))
}

func (s *Server) githubManifestCallback(response http.ResponseWriter, request *http.Request) {
	installURL, err := s.github.CompleteManifest(request.Context(), request.URL.Query().Get("state"), request.URL.Query().Get("code"))
	if err != nil {
		browserError(response, http.StatusBadGateway, err)
		return
	}
	http.Redirect(response, request, installURL, http.StatusSeeOther)
}

func (s *Server) githubInstallCallback(response http.ResponseWriter, request *http.Request) {
	installationID, err := strconv.ParseInt(request.URL.Query().Get("installation_id"), 10, 64)
	if err != nil || installationID == 0 {
		browserError(response, http.StatusBadRequest, fmt.Errorf("GitHub did not provide an installation ID"))
		return
	}
	if err := s.github.CompleteInstallation(request.PathValue("key"), installationID); err != nil {
		browserError(response, http.StatusBadGateway, err)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	_, _ = response.Write([]byte(`<!doctype html><html><body><h1>GitHub installation received</h1><p>Scraps is finishing credential setup in the background. You may close this window.</p><script>window.close()</script></body></html>`))
}

func browserError(response http.ResponseWriter, status int, err error) {
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_, _ = fmt.Fprintf(response, "<!doctype html><html><body><h1>GitHub authorization failed</h1><p>%s</p></body></html>", html.EscapeString(err.Error()))
}
