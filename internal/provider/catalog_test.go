package provider

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/awersli99/unreal-tui/internal/config"
	"github.com/awersli99/unreal-tui/internal/testutil"
)

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	codex := testutil.IsolateProviders(t)
	testutil.WriteFile(t, filepath.Join(codex, "auth.json"), `{}`)
	cache := map[string]any{"models": []map[string]any{
		{"slug": "gpt-6-mini", "priority": 2, "visibility": "list", "default_reasoning_level": "medium",
			"supported_reasoning_levels": []map[string]string{{"effort": "low"}, {"effort": "medium"}, {"effort": "high"}}},
		{"slug": "gpt-6-astra", "display_name": "GPT-6 Astra", "priority": 1, "visibility": "list"},
		{"slug": "hidden", "priority": 0, "visibility": "hide"},
		{"slug": "router/model", "priority": 0},
	}}
	encoded, _ := json.Marshal(cache)
	testutil.WriteFile(t, filepath.Join(codex, "models_cache.json"), string(encoded))

	home := t.TempDir()
	t.Setenv("UNREAL_TEST_LOCAL_KEY", "x")
	testutil.WriteFile(t, filepath.Join(home, config.ModelsFileName), `{"providers": {
		"local": {"api": "openai-completions", "baseUrl": "http://localhost:1", "apiKey": "k", "models": [{"id": "m"}]},
		"lab": {"api": "openai-responses", "baseUrl": "http://localhost:2/v1/", "apiKey": "$UNREAL_TEST_LOCAL_KEY",
			"models": [{"id": "qwen-coder", "name": "Qwen Coder", "thinkingLevels": ["low", "high", "bogus"]}, {"id": "gpt-6-lab"}]}
	}}`)
	return LoadCatalog(home)
}

func modelKeys(models []ModelInfo) []string {
	keys := make([]string, 0, len(models))
	for _, model := range models {
		keys = append(keys, model.Key())
	}
	return keys
}

func TestCatalogLoadsCodexCacheAndModelsFile(t *testing.T) {
	catalog := testCatalog(t)
	want := []string{"openai-codex/gpt-6-astra", "openai-codex/gpt-6-mini", "lab/qwen-coder", "lab/gpt-6-lab"}
	if got := modelKeys(catalog.Models); !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
	if len(catalog.Warnings) != 1 || !strings.Contains(catalog.Warnings[0], "openai-completions") {
		t.Fatalf("warnings = %q", catalog.Warnings)
	}
	lab, ok := catalog.Provider("lab")
	if !ok || lab.BaseURL != "http://localhost:2/v1" || lab.API != APIOpenAI {
		t.Fatalf("lab provider = %+v", lab)
	}
	mini := catalog.Lookup("openai-codex", "gpt-6-mini")
	if !reflect.DeepEqual(mini.Levels, []string{"low", "medium", "high"}) || mini.DefaultLevel != "medium" {
		t.Fatalf("mini = %+v", mini)
	}
	if qwen := catalog.Lookup("lab", "qwen-coder"); !reflect.DeepEqual(qwen.Levels, []string{"low", "high"}) {
		t.Fatalf("qwen levels = %v", qwen.Levels)
	}
}

func TestCatalogMatchAndResolve(t *testing.T) {
	catalog := testCatalog(t)
	cases := []struct {
		pattern string
		want    []string
		level   string
	}{
		{"lab/gpt-6-lab", []string{"lab/gpt-6-lab"}, ""},
		{"gpt-6-mini:low", []string{"openai-codex/gpt-6-mini"}, "low"},
		{"gpt-6*", []string{"openai-codex/gpt-6-astra", "openai-codex/gpt-6-mini", "lab/gpt-6-lab"}, ""},
		{"lab/*:max", []string{"lab/qwen-coder", "lab/gpt-6-lab"}, "max"},
		{"astra", []string{"openai-codex/gpt-6-astra"}, ""},
		{"qwen 7b", nil, ""},
		{"Qwen", []string{"lab/qwen-coder"}, ""},
	}
	for _, test := range cases {
		got, level := catalog.Match(test.pattern)
		if keys := modelKeys(got); !slices.Equal(keys, test.want) || level != test.level {
			t.Errorf("Match(%q) = %v, %q; want %v, %q", test.pattern, keys, level, test.want, test.level)
		}
	}

	if model, _, err := catalog.Resolve("gpt-6", "lab"); err != nil || model.Key() != "lab/gpt-6-lab" {
		t.Errorf("ambiguous match should prefer the default provider: %v, %v", model.Key(), err)
	}
	if _, _, err := catalog.Resolve("gpt-6", "ollama"); err == nil || !strings.Contains(err.Error(), "several") {
		t.Errorf("ambiguous match without preference: %v", err)
	}
	if model, level, err := catalog.Resolve("lab/brand-new:high", ""); err != nil || model.Key() != "lab/brand-new" || level != "high" {
		t.Errorf("unlisted model with provider = %v, %q, %v", model.Key(), level, err)
	}
	if model, _, err := catalog.Resolve("brand-new", "openai-codex"); err != nil || model.Key() != "openai-codex/brand-new" {
		t.Errorf("unlisted bare id = %v, %v", model.Key(), err)
	}
	if _, _, err := catalog.Resolve("nope/brand-new", ""); err == nil {
		t.Error("unknown provider should fail")
	}

	scoped := catalog.Scoped([]string{"gpt-6-mini:low", "gpt-6*"})
	if len(scoped) != 3 || scoped[0].Model.ID != "gpt-6-mini" || scoped[0].Level != "low" || scoped[1].Level != "" {
		t.Errorf("scoped = %+v", scoped)
	}
}

func TestClampLevel(t *testing.T) {
	model := ModelInfo{Levels: []string{"low", "medium", "high"}}
	for level, want := range map[string]string{"medium": "medium", "max": "high", "xhigh": "high", "": "low"} {
		if got := model.ClampLevel(level); got != want {
			t.Errorf("ClampLevel(%q) = %q, want %q", level, got, want)
		}
	}
	if got := (ModelInfo{}).ClampLevel("max"); got != "max" {
		t.Errorf("unrestricted model clamped max to %q", got)
	}
}

func TestStartupModel(t *testing.T) {
	catalog := testCatalog(t)
	t.Setenv("UNREAL_HARNESS_LLM_PROVIDER", "")
	t.Setenv("UNREAL_HARNESS_LLM_MODEL", "")
	cases := []struct {
		name     string
		settings config.Settings
		flags    ModelFlags
		want     string
		level    string
	}{
		{"detected", config.Settings{}, ModelFlags{}, "openai-codex/gpt-6-astra", ""},
		{"saved default", config.Settings{DefaultProvider: "lab", DefaultModel: "qwen-coder"}, ModelFlags{}, "lab/qwen-coder", ""},
		{"model flag", config.Settings{DefaultProvider: "lab", DefaultModel: "qwen-coder"}, ModelFlags{Model: "mini:low"}, "openai-codex/gpt-6-mini", "low"},
		{"provider flag", config.Settings{}, ModelFlags{Provider: "lab"}, "lab/qwen-coder", ""},
		{"provider and model", config.Settings{}, ModelFlags{Provider: "lab", Model: "gpt-6"}, "lab/gpt-6-lab", ""},
		{"provider and unlisted model", config.Settings{}, ModelFlags{Provider: "lab", Model: "other"}, "lab/other", ""},
	}
	for _, test := range cases {
		model, level, err := StartupModel(catalog, test.settings, test.flags)
		if err != nil || model.Key() != test.want || level != test.level {
			t.Errorf("%s: got %s, %q, %v; want %s, %q", test.name, model.Key(), level, err, test.want, test.level)
		}
	}
	if _, _, err := StartupModel(catalog, config.Settings{}, ModelFlags{Provider: "nope"}); err == nil {
		t.Error("unknown provider flag should fail")
	}
}
