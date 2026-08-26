package provider

import "testing"

func TestValidateDockerRelativePath(t *testing.T) {
	for _, valid := range []string{"", ".", "src/main.go", "src/../README.md"} {
		if _, err := validateRelative(valid); err != nil {
			t.Errorf("valid path %q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"/etc/passwd", "..", "../other", "src/../../other"} {
		if _, err := validateRelative(invalid); err == nil {
			t.Errorf("invalid path %q accepted", invalid)
		}
	}
}
