// Package testutil holds helpers shared by unreal's tests.
package testutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func MessageItem(role llm.Role, text string) llm.Item {
	return llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: role, Text: text}}
}

func WritePrivateFile(t *testing.T, path, body string) {
	t.Helper()
	WriteFile(t, path, body)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func WriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// IsolateProviders hides real credentials and the real Codex model cache.
func IsolateProviders(t *testing.T) string {
	t.Helper()
	codex := t.TempDir()
	t.Setenv("CODEX_HOME", codex)
	t.Setenv("ANTHROPIC_AUTH_FILE", filepath.Join(t.TempDir(), "auth.json"))
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "OPENAI_API_KEY", "OPENROUTER_API_KEY", "FIREWORKS_API_KEY", "UNREAL_HARNESS_LLM_API_KEY",
		"UNREAL_HARNESS_LLM_BASE_URL", "OPENAI_CODEX_ACCESS_TOKEN", "OPENAI_CODEX_AUTH_FILE"} {
		t.Setenv(name, "")
	}
	return codex
}
