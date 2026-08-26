// Package githubauth defines Scraps' least-privilege GitHub credential profile.
package githubauth

import _ "embed"

const (
	// ProfileID is both the OpenShell profile type and configured provider name.
	ProfileID = "github-push"
	// TokenEnvironment is used only while sending a PAT to the OpenShell gateway.
	TokenEnvironment = "GH_TOKEN"
)

// Profile is the custom OpenShell profile that permits Git fetches and pushes
// while keeping GitHub API mutations blocked.
//
//go:embed github-push.yaml
var Profile []byte
