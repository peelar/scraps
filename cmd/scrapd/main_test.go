package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasModelAuthFromEnvironment(t *testing.T) {
	for _, name := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "OPENROUTER_API_KEY",
		"AZURE_OPENAI_API_KEY", "DEEPSEEK_API_KEY", "NVIDIA_API_KEY", "MISTRAL_API_KEY",
		"GROQ_API_KEY", "CEREBRAS_API_KEY", "XAI_API_KEY", "AI_GATEWAY_API_KEY",
		"AWS_BEARER_TOKEN_BEDROCK", "GOOGLE_APPLICATION_CREDENTIALS",
	} {
		t.Setenv(name, "")
	}
	profileDir := t.TempDir()
	if hasModelAuth(profileDir) {
		t.Fatal("hasModelAuth returned true without credentials")
	}
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	if !hasModelAuth(profileDir) {
		t.Fatal("hasModelAuth returned false with ANTHROPIC_API_KEY")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	if err := os.WriteFile(filepath.Join(profileDir, "auth.json"), []byte(`{"anthropic":{"type":"api_key","key":"test"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !hasModelAuth(profileDir) {
		t.Fatal("hasModelAuth returned false with cloned auth.json")
	}
}
