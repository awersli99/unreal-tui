package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProjectSettingsOverrideGlobal(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	writeFile(t, globalSettingsPath(home), `{
		"defaultModel": "global-model",
		"defaultThinkingLevel": "low",
		"modelThinkingLevels": {"a/one": "high", "a/two": "low"},
		"shellPath": "/bin/zsh"
	}`)
	writeFile(t, projectSettingsPath(workspace), `{
		"defaultModel": "project-model",
		"modelThinkingLevels": {"a/two": "max"},
		"shellPath": "/tmp/evil"
	}`)
	config, err := loadConfig(home, workspace)
	if err != nil {
		t.Fatal(err)
	}
	settings := config.Settings
	if settings.DefaultModel != "project-model" || settings.DefaultThinkingLevel != "low" {
		t.Fatalf("scalars not merged: %+v", settings)
	}
	if want := map[string]string{"a/one": "high", "a/two": "max"}; !reflect.DeepEqual(settings.ModelThinkingLevels, want) {
		t.Fatalf("modelThinkingLevels = %v, want %v", settings.ModelThinkingLevels, want)
	}
	if settings.ShellPath != "/bin/zsh" {
		t.Fatalf("project overrode global-only shellPath: %q", settings.ShellPath)
	}
	if len(config.Warnings) != 1 || !strings.Contains(config.Warnings[0], "shellPath") {
		t.Fatalf("warnings = %q", config.Warnings)
	}
}

func TestLegacySettingsMigrate(t *testing.T) {
	home := t.TempDir()
	writeFile(t, globalSettingsPath(home), `{"provider": "openai", "model": "gpt-x", "thinking": "max", "custom": 1}`)
	config, err := loadConfig(home, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s := config.Settings; s.DefaultProvider != "openai" || s.DefaultModel != "gpt-x" || s.DefaultThinkingLevel != "max" {
		t.Fatalf("not migrated: %+v", s)
	}
	saved, err := readSettingsMap(globalSettingsPath(home))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"defaultProvider": "openai", "defaultModel": "gpt-x", "defaultThinkingLevel": "max", "custom": float64(1)}
	if !reflect.DeepEqual(saved, want) {
		t.Fatalf("file = %v, want %v", saved, want)
	}
}

func TestInvalidSettingsWarnInsteadOfFailing(t *testing.T) {
	home := t.TempDir()
	writeFile(t, globalSettingsPath(home), `{"defaultThinkingLevel": "huge", "theme": "neon", "retry": {"maxRetries": 2}}`)
	config, err := loadConfig(home, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if config.Settings.DefaultThinkingLevel != "" || config.Settings.Theme != "" || len(config.Warnings) != 2 {
		t.Fatalf("settings %+v, warnings %q", config.Settings, config.Warnings)
	}
	if app := (&App{Config: config}); app.maxAttempts() != 3 {
		t.Fatalf("maxAttempts = %d, want 3", app.maxAttempts())
	}
}

func TestSaveGlobalSettingKeepsOtherKeys(t *testing.T) {
	home := t.TempDir()
	writeFile(t, globalSettingsPath(home), `{"unknownKey": true, "theme": "dark"}`)
	if err := saveGlobalSetting(home, "defaultModel", "m"); err != nil {
		t.Fatal(err)
	}
	if err := saveGlobalSetting(home, "theme", nil); err != nil {
		t.Fatal(err)
	}
	saved, err := readSettingsMap(globalSettingsPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"unknownKey": true, "defaultModel": "m"}; !reflect.DeepEqual(saved, want) {
		t.Fatalf("file = %v, want %v", saved, want)
	}
}

func TestResolveConfigValue(t *testing.T) {
	t.Setenv("UNREAL_TEST_KEY", "secret")
	for input, want := range map[string]string{
		"literal":                 "literal",
		"$UNREAL_TEST_KEY":        "secret",
		"pre-${UNREAL_TEST_KEY}-": "pre-secret-",
		"$$UNREAL_TEST_KEY":       "$UNREAL_TEST_KEY",
		"$!":                      "!",
		"!echo from-command":      "from-command",
		"cost $5":                 "cost $5",
	} {
		got, err := resolveConfigValue(input)
		if err != nil || got != want {
			t.Errorf("resolveConfigValue(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := resolveConfigValue("$UNREAL_TEST_MISSING"); err == nil {
		t.Error("missing variable should fail")
	}
}

func TestSessionDirectory(t *testing.T) {
	if got := sessionDirectory("/h", "/work/project", ""); got != "/h/sessions/--work-project--" {
		t.Errorf("default = %q", got)
	}
	if got := sessionDirectory("/h", "/work/project", ".sessions"); got != "/work/project/.sessions" {
		t.Errorf("relative = %q", got)
	}
}

// isolateProviders hides real credentials and the real Codex model cache.
func isolateProviders(t *testing.T) string {
	t.Helper()
	codex := t.TempDir()
	t.Setenv("CODEX_HOME", codex)
	for _, name := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "FIREWORKS_API_KEY", providerAPIKeyOverride,
		baseURLEnvironment, "OPENAI_CODEX_ACCESS_TOKEN", "OPENAI_CODEX_AUTH_FILE"} {
		t.Setenv(name, "")
	}
	return codex
}

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	codex := isolateProviders(t)
	writeFile(t, filepath.Join(codex, "auth.json"), `{}`)
	cache := map[string]any{"models": []map[string]any{
		{"slug": "gpt-6-mini", "priority": 2, "visibility": "list", "default_reasoning_level": "medium",
			"supported_reasoning_levels": []map[string]string{{"effort": "low"}, {"effort": "medium"}, {"effort": "high"}}},
		{"slug": "gpt-6-astra", "display_name": "GPT-6 Astra", "priority": 1, "visibility": "list"},
		{"slug": "hidden", "priority": 0, "visibility": "hide"},
		{"slug": "router/model", "priority": 0},
	}}
	encoded, _ := json.Marshal(cache)
	writeFile(t, filepath.Join(codex, "models_cache.json"), string(encoded))

	home := t.TempDir()
	t.Setenv("UNREAL_TEST_LOCAL_KEY", "x")
	writeFile(t, filepath.Join(home, modelsFileName), `{"providers": {
		"local": {"api": "openai-completions", "baseUrl": "http://localhost:1", "apiKey": "k", "models": [{"id": "m"}]},
		"lab": {"api": "openai-responses", "baseUrl": "http://localhost:2/v1/", "apiKey": "$UNREAL_TEST_LOCAL_KEY",
			"models": [{"id": "qwen-coder", "name": "Qwen Coder", "thinkingLevels": ["low", "high", "bogus"]}, {"id": "gpt-6-lab"}]}
	}}`)
	return loadCatalog(home)
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
	if !ok || lab.BaseURL != "http://localhost:2/v1" || lab.API != apiOpenAI {
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
		if keys := modelKeys(got); !equalStrings(keys, test.want) || level != test.level {
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
		settings Settings
		flags    options
		want     string
		level    string
	}{
		{"detected", Settings{}, options{}, "openai-codex/gpt-6-astra", ""},
		{"saved default", Settings{DefaultProvider: "lab", DefaultModel: "qwen-coder"}, options{}, "lab/qwen-coder", ""},
		{"model flag", Settings{DefaultProvider: "lab", DefaultModel: "qwen-coder"}, options{model: "mini:low"}, "openai-codex/gpt-6-mini", "low"},
		{"provider flag", Settings{}, options{provider: "lab"}, "lab/qwen-coder", ""},
		{"provider and model", Settings{}, options{provider: "lab", model: "gpt-6"}, "lab/gpt-6-lab", ""},
		{"provider and unlisted model", Settings{}, options{provider: "lab", model: "other"}, "lab/other", ""},
	}
	for _, test := range cases {
		model, level, err := startupModel(catalog, test.settings, test.flags)
		if err != nil || model.Key() != test.want || level != test.level {
			t.Errorf("%s: got %s, %q, %v; want %s, %q", test.name, model.Key(), level, err, test.want, test.level)
		}
	}
	if _, _, err := startupModel(catalog, Settings{}, options{provider: "nope"}); err == nil {
		t.Error("unknown provider flag should fail")
	}
}
