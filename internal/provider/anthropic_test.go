package provider

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/awersli99/unreal-tui/internal/anthropic"
	"github.com/awersli99/unreal-tui/internal/config"
	"github.com/awersli99/unreal-tui/internal/testutil"
)

func TestAnthropicNativeCredentialPrecedence(t *testing.T) {
	testutil.IsolateProviders(t)
	home := t.TempDir()
	t.Setenv("UNREAL_TUI_HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "sk-ant-oat-env")
	t.Setenv(config.APIKeyEnv, "override-key")
	testutil.WriteFile(t, filepath.Join(home, config.ModelsFileName), `{"providers":{"anthropic":{"apiKey":"!exit 1"},"proxy":{"api":"anthropic-messages","baseUrl":"http://localhost:1"}}}`)
	testutil.WritePrivateFile(t, os.Getenv("ANTHROPIC_AUTH_FILE"), `{"anthropic":{"type":"oauth","access":"sk-ant-oat-external","refresh":"external","expires":9999999999999}}`)
	if err := anthropic.SaveCredential(home, nativeTestCredential()); err != nil {
		t.Fatal(err)
	}
	spec, _ := LoadCatalog(home).Provider("anthropic")
	if !spec.Available() || spec.AuthFile != config.AuthPath(home) {
		t.Fatalf("OAuth not discovered: %+v", spec)
	}
	client, err := spec.NewClient(1)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	key, err := client.(*anthropic.Client).Credential(context.Background())
	if err != nil || key != "sk-ant-oat-local" {
		t.Fatalf("native OAuth did not win: %s, %v", key, err)
	}
	proxy, _ := LoadCatalog(home).Provider("proxy")
	if proxy.Available() || proxy.AuthFile != "" {
		t.Fatal("custom provider inherited native OAuth")
	}
	if err := config.SaveAPIKey(home, "anthropic", "local-api-key"); err != nil {
		t.Fatal(err)
	}
	spec, _ = LoadCatalog(home).Provider("anthropic")
	if spec.AuthFile != "" || spec.StoredKey != "local-api-key" {
		t.Fatalf("API key did not replace OAuth: %+v", spec)
	}
	client, err = spec.NewClient(1)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if key, err := client.(*anthropic.Client).Credential(context.Background()); err != nil || key != "local-api-key" {
		t.Fatalf("local key did not win: %s, %v", key, err)
	}
}

// A fresh install must start somewhere /login can be reached.
func TestFreshInstallStartsWithAnthropic(t *testing.T) {
	testutil.IsolateProviders(t)
	home := t.TempDir()
	t.Setenv("UNREAL_TUI_HOME", home)
	t.Setenv("ANTHROPIC_AUTH_FILE", "")
	t.Setenv("UNREAL_HARNESS_LLM_PROVIDER", "")
	t.Setenv("UNREAL_HARNESS_LLM_MODEL", "")
	model, _, err := StartupModel(LoadCatalog(home), config.Settings{}, ModelFlags{})
	if err != nil || model.Provider != "anthropic" {
		t.Fatalf("fresh install cannot reach /login: %+v, %v", model, err)
	}
}

func TestAnthropicCurrentCatalog(t *testing.T) {
	testutil.IsolateProviders(t)
	t.Setenv("ANTHROPIC_API_KEY", "offline-test")
	t.Setenv("UNREAL_HARNESS_LLM_PROVIDER", "")
	t.Setenv("UNREAL_HARNESS_LLM_MODEL", "")
	catalog := LoadCatalog(t.TempDir())
	for _, id := range []string{
		"claude-opus-5-5", "claude-opus-5", "claude-sonnet-5", "claude-fable-5-1", "claude-fable-5",
		"claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-4-6", "claude-sonnet-4-5",
	} {
		matches, _ := catalog.Match("anthropic/" + id)
		if len(matches) != 1 || matches[0].ContextWindow != 1000000 {
			t.Errorf("catalog metadata for %s = %+v", id, matches)
		}
		info, level, err := catalog.Resolve("anthropic/"+id+":max", "")
		if err != nil || info.Name == "" || level != "max" {
			t.Errorf("resolve %s: %+v %s %v", id, info, level, err)
		}
	}
	for _, id := range []string{"claude-haiku-4-5", "claude-opus-4-5", "claude-haiku-4-5-20251001", "claude-opus-4-5-20251101"} {
		if model := catalog.Lookup("anthropic", id); model.ContextWindow != 200000 || model.Name == "" {
			t.Errorf("legacy metadata = %+v", model)
		}
	}
	if model := catalog.Lookup("anthropic", "claude-sonnet-4-5-20250929"); model.ContextWindow != 1000000 {
		t.Fatalf("snapshot metadata = %+v", model)
	}
	model, _, err := StartupModel(catalog, config.Settings{}, ModelFlags{Provider: "anthropic"})
	if err != nil || model.ID != "claude-sonnet-5" {
		t.Fatalf("new default = %+v, %v", model, err)
	}
	model, _, err = StartupModel(catalog, config.Settings{DefaultProvider: "anthropic", DefaultModel: "claude-sonnet-4-6"}, ModelFlags{})
	if err != nil || model.ID != "claude-sonnet-4-6" {
		t.Fatalf("saved default changed = %+v, %v", model, err)
	}
	seen := map[string]bool{}
	for _, info := range builtinAnthropicModels("anthropic") {
		if seen[info.ID] {
			t.Errorf("duplicate model %s", info.ID)
		}
		seen[info.ID] = true
		if info.DefaultLevel == "" || info.ContextWindow == 0 {
			t.Errorf("incomplete metadata: %+v", info)
		}
	}
}

func TestAnthropicModelOverridesKeepCapabilities(t *testing.T) {
	testutil.IsolateProviders(t)
	t.Setenv("ANTHROPIC_API_KEY", "offline-test")
	home := t.TempDir()
	testutil.WriteFile(t, filepath.Join(home, config.ModelsFileName), `{"providers":{
		"anthropic":{"models":[{"id":"claude-opus-5-5","name":"My Opus"},{"id":"claude-fable-5-1","contextWindow":500000,"thinkingLevels":["low","high"]}]},
		"proxy":{"api":"anthropic-messages","baseUrl":"http://localhost:1","apiKey":"key","models":[{"id":"claude-sonnet-4-6"}]}
	}}`)
	catalog := LoadCatalog(home)
	if info := catalog.Lookup("anthropic", "claude-opus-5-5"); info.Name != "My Opus" || info.ContextWindow != 1000000 || !slices.Contains(info.Levels, "xhigh") {
		t.Fatalf("metadata lost on name override: %+v", info)
	}
	if info := catalog.Lookup("anthropic", "claude-fable-5-1"); info.ContextWindow != 500000 || !reflect.DeepEqual(info.Levels, []string{"low", "high"}) {
		t.Fatalf("explicit override ignored: %+v", info)
	}
	if info := catalog.Lookup("proxy", "claude-sonnet-4-6"); info.ContextWindow != 1000000 || slices.Contains(info.Levels, "xhigh") {
		t.Fatalf("proxy lost known model capabilities: %+v", info)
	}
	// Resolve an unlisted snapshot even before credentials are configured.
	bare := &Catalog{Providers: builtinProviders()}
	if info := bare.Lookup("anthropic", "claude-opus-5-5-20260922"); info.ContextWindow != 1000000 || !slices.Contains(info.Levels, "xhigh") {
		t.Fatalf("unlisted snapshot = %+v", info)
	}
	for _, id := range []string{"claude-opus-5-50", "claude-sonnet-4-60", "claude-opus-5-5-bogus", "claude-future"} {
		if _, known := anthropic.ModelForID(id); known {
			t.Errorf("invented capabilities for %s", id)
		}
	}
}

func TestAnthropicAuthPrecedenceCatalogAndCustomProvider(t *testing.T) {
	testutil.IsolateProviders(t)
	home := t.TempDir()
	if spec, ok := LoadCatalog(home).Provider("anthropic"); !ok || spec.Available() {
		t.Fatalf("unconfigured provider = %+v", spec)
	}
	path := os.Getenv("ANTHROPIC_AUTH_FILE")
	testutil.WritePrivateFile(t, path, `{"anthropic":{"type":"oauth","access":"sk-ant-oat-stored","refresh":"refresh","expires":9999999999999}}`)
	catalog := LoadCatalog(home)
	if provider, err := detectProvider(catalog); err != nil || provider != "anthropic" {
		t.Fatalf("detected = %s, %v", provider, err)
	}
	model, _, err := StartupModel(catalog, config.Settings{}, ModelFlags{Provider: "anthropic"})
	if err != nil || model.ID != anthropic.DefaultModel || model.ContextWindow == 0 {
		t.Fatalf("default = %+v, %v", model, err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "api-env")
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "sk-ant-oat-env")
	for _, test := range []struct{ configured, override, apiKey, token, want string }{
		{"config", "override", "api-env", "sk-ant-oat-env", "config"},
		{"", "override", "api-env", "sk-ant-oat-env", "override"},
		{"", "", "api-env", "sk-ant-oat-env", "api-env"},
		{"", "", "", "sk-ant-oat-env", "sk-ant-oat-env"},
		{"", "", "", "", "sk-ant-oat-stored"},
	} {
		t.Setenv(config.APIKeyEnv, test.override)
		t.Setenv("ANTHROPIC_API_KEY", test.apiKey)
		t.Setenv("ANTHROPIC_OAUTH_TOKEN", test.token)
		spec, _ := catalog.Provider("anthropic")
		spec.APIKey = test.configured
		client, err := spec.NewClient(1)
		if err != nil {
			t.Fatal(err)
		}
		got, err := client.(*anthropic.Client).Credential(context.Background())
		client.Close()
		if err != nil || got != test.want {
			t.Errorf("resolved %q, %v; want %q", got, err, test.want)
		}
	}
	testutil.WriteFile(t, filepath.Join(home, config.ModelsFileName), `{"providers":{"proxy":{"api":"anthropic-messages","baseUrl":"http://localhost:1/v1","apiKey":"test","models":[{"id":"custom-claude"}]},"no-key":{"api":"anthropic","baseUrl":"http://localhost:2","models":[{"id":"must-not-inherit-login"}]}}}`)
	catalog = LoadCatalog(home)
	proxy, ok := catalog.Provider("proxy")
	if !ok || proxy.API != APIAnthropic || !proxy.Available() || len(catalog.Warnings) != 0 {
		t.Fatalf("custom = %+v, warnings %v", proxy, catalog.Warnings)
	}
	missing, _ := catalog.Provider("no-key")
	if missing.Available() {
		t.Fatal("custom endpoint inherited built-in credentials")
	}
	if _, err := missing.NewClient(1); err == nil {
		t.Fatal("custom endpoint accepted missing key")
	}
	if _, _, err := catalog.Resolve("anthropic/new-model", ""); err != nil {
		t.Fatal(err)
	}
}

func TestAnthropicEmptyExplicitKeyDoesNotFallBack(t *testing.T) {
	testutil.IsolateProviders(t)
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "sk-ant-oat-available")
	spec := Spec{Name: "anthropic", API: APIAnthropic, APIKey: "!printf ''"}
	if _, err := spec.NewClient(1); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty explicit key fell back to OAuth: %v", err)
	}
}

func nativeTestCredential() anthropic.Credential {
	return anthropic.Credential{Type: "oauth", Access: "sk-ant-oat-local", Refresh: "local-refresh", Expires: time.Now().Add(time.Hour).UnixMilli()}
}
