package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"

	"github.com/awersli99/unreal-tui/internal/config"
	"github.com/awersli99/unreal-tui/internal/testutil"
)

func TestStoredKeyMakesProviderAvailableAndWins(t *testing.T) {
	testutil.IsolateProviders(t)
	home := t.TempDir()
	testutil.WriteFile(t, filepath.Join(home, config.ModelsFileName), `{"providers": {"lab": {"api": "openai-responses", "baseUrl": "http://localhost:1", "models": [{"id": "m"}]}}}`)
	if spec, _ := LoadCatalog(home).Provider("lab"); spec.Available() {
		t.Fatal("lab should need a key")
	}
	t.Setenv("OPENROUTER_API_KEY", "from-env")
	if err := config.SaveAPIKey(home, "lab", "stored-lab"); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveAPIKey(home, "openrouter", "stored-or"); err != nil {
		t.Fatal(err)
	}
	catalog := LoadCatalog(home)
	lab, _ := catalog.Provider("lab")
	if !lab.Available() || lab.StoredKey != "stored-lab" {
		t.Fatalf("lab = %+v", lab)
	}
	if got := modelKeys(catalog.filter(func(model ModelInfo) bool { return model.Provider == "lab" })); !slices.Equal(got, []string{"lab/m"}) {
		t.Fatalf("lab models = %v", got)
	}
	if openrouter, _ := catalog.Provider("openrouter"); openrouter.StoredKey != "stored-or" {
		t.Fatalf("openrouter stored key = %q", openrouter.StoredKey)
	}
}

func TestStoredKeyIsSentOverModelsJSONKey(t *testing.T) {
	testutil.IsolateProviders(t)
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen <- request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte(`data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[],"usage":{}}}` + "\n\n"))
	}))
	defer server.Close()
	home := t.TempDir()
	testutil.WriteFile(t, filepath.Join(home, config.ModelsFileName), `{"providers": {"lab": {"api": "openai-responses", "baseUrl": "`+server.URL+`", "apiKey": "from-models-json", "models": [{"id": "m"}]}}}`)
	if err := config.SaveAPIKey(home, "lab", "from-login"); err != nil {
		t.Fatal(err)
	}
	spec, _ := LoadCatalog(home).Provider("lab")
	client, err := spec.NewClient(1)
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
