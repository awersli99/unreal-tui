package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestAPIKeyStore(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := saveAPIKey(home, "openrouter", "sk-or-1"); err != nil {
		t.Fatal(err)
	}
	// Entries /login does not own are kept.
	writeFile(t, authPath(home), `{"openrouter": {"type": "api_key", "key": "sk-or-1"}, "other": {"type": "oauth", "access": "x"}}`)
	if err := os.Chmod(authPath(home), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveAPIKey(home, "fireworks", "fw-2"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(authPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("auth.json permissions = %o, want 600", perm)
	}
	entries, warning, err := readCredentials(home)
	if err != nil || warning != "" {
		t.Fatalf("read: %v, %q", err, warning)
	}
	keys := storedAPIKeys(entries)
	if len(entries) != 3 || keys["openrouter"] != "sk-or-1" || keys["fireworks"] != "fw-2" || keys["other"] != "" {
		t.Fatalf("entries %v, keys %v", entries, keys)
	}

	if removed, err := removeCredential(home, "openrouter"); err != nil || !removed {
		t.Fatalf("remove: %v, %v", removed, err)
	}
	if removed, _ := removeCredential(home, "openrouter"); removed {
		t.Fatal("second remove reported success")
	}
	if providers, _ := storedProviders(home); !equalStrings(providers, []string{"fireworks", "other"}) {
		t.Fatalf("providers = %v", providers)
	}

	for _, bad := range []string{"", "  ", "has space", "tab\there"} {
		if saveAPIKey(home, "x", bad) == nil {
			t.Errorf("saveAPIKey accepted %q", bad)
		}
	}
}

func TestReadableAuthFileWarns(t *testing.T) {
	home := t.TempDir()
	writeFile(t, authPath(home), `{}`)
	if err := os.Chmod(authPath(home), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, warning, err := readCredentials(home); err != nil || !strings.Contains(warning, "chmod 600") {
		t.Fatalf("warning %q, err %v", warning, err)
	}
}

func TestStoredKeyMakesProviderAvailableAndWins(t *testing.T) {
	isolateProviders(t)
	home := t.TempDir()
	writeFile(t, filepath.Join(home, modelsFileName), `{"providers": {"lab": {"api": "openai-responses", "baseUrl": "http://localhost:1", "models": [{"id": "m"}]}}}`)
	if spec, _ := loadCatalog(home).Provider("lab"); spec.Available() {
		t.Fatal("lab should need a key")
	}
	t.Setenv("OPENROUTER_API_KEY", "from-env")
	if err := saveAPIKey(home, "lab", "stored-lab"); err != nil {
		t.Fatal(err)
	}
	if err := saveAPIKey(home, "openrouter", "stored-or"); err != nil {
		t.Fatal(err)
	}
	catalog := loadCatalog(home)
	lab, _ := catalog.Provider("lab")
	if !lab.Available() || lab.StoredKey != "stored-lab" {
		t.Fatalf("lab = %+v", lab)
	}
	if got := modelKeys(catalog.filter(func(model ModelInfo) bool { return model.Provider == "lab" })); !equalStrings(got, []string{"lab/m"}) {
		t.Fatalf("lab models = %v", got)
	}
	if openrouter, _ := catalog.Provider("openrouter"); openrouter.StoredKey != "stored-or" {
		t.Fatalf("openrouter stored key = %q", openrouter.StoredKey)
	}
}

func TestStoredKeyIsSentOverModelsJSONKey(t *testing.T) {
	isolateProviders(t)
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen <- request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte(`data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[],"usage":{}}}` + "\n\n"))
	}))
	defer server.Close()
	home := t.TempDir()
	writeFile(t, filepath.Join(home, modelsFileName), `{"providers": {"lab": {"api": "openai-responses", "baseUrl": "`+server.URL+`", "apiKey": "from-models-json", "models": [{"id": "m"}]}}}`)
	if err := saveAPIKey(home, "lab", "from-login"); err != nil {
		t.Fatal(err)
	}
	spec, _ := loadCatalog(home).Provider("lab")
	client, err := spec.newClient(1)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Respond(context.Background(), llm.Request{Model: llm.Model{ID: "m"}}, llm.RequestOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "Bearer from-login" {
		t.Fatalf("Authorization = %q", got)
	}
}
