package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/awersli99/unreal-tui/internal/testutil"
)

func TestProjectSettingsOverrideGlobal(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	testutil.WriteFile(t, GlobalSettingsPath(home), `{
		"defaultModel": "global-model",
		"defaultThinkingLevel": "low",
		"modelThinkingLevels": {"a/one": "high", "a/two": "low"},
		"shellPath": "/bin/zsh"
	}`)
	testutil.WriteFile(t, projectSettingsPath(workspace), `{
		"defaultModel": "project-model",
		"modelThinkingLevels": {"a/two": "max"},
		"shellPath": "/tmp/evil"
	}`)
	config, err := Load(home, workspace)
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
	testutil.WriteFile(t, GlobalSettingsPath(home), `{"provider": "openai", "model": "gpt-x", "thinking": "max", "custom": 1}`)
	config, err := Load(home, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s := config.Settings; s.DefaultProvider != "openai" || s.DefaultModel != "gpt-x" || s.DefaultThinkingLevel != "max" {
		t.Fatalf("not migrated: %+v", s)
	}
	saved, err := readSettingsMap(GlobalSettingsPath(home))
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
	testutil.WriteFile(t, GlobalSettingsPath(home), `{"defaultThinkingLevel": "huge", "theme": "neon", "retry": {"maxRetries": 2}}`)
	config, err := Load(home, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if config.Settings.DefaultThinkingLevel != "" || config.Settings.Theme != "" || len(config.Warnings) != 2 {
		t.Fatalf("settings %+v, warnings %q", config.Settings, config.Warnings)
	}
	if attempts := config.Settings.MaxAttempts(); attempts != 3 {
		t.Fatalf("MaxAttempts = %d, want 3", attempts)
	}
}

func TestSaveGlobalSettingKeepsOtherKeys(t *testing.T) {
	home := t.TempDir()
	testutil.WriteFile(t, GlobalSettingsPath(home), `{"unknownKey": true, "theme": "dark"}`)
	if err := SaveGlobalSetting(home, "defaultModel", "m"); err != nil {
		t.Fatal(err)
	}
	if err := SaveGlobalSetting(home, "theme", nil); err != nil {
		t.Fatal(err)
	}
	saved, err := readSettingsMap(GlobalSettingsPath(home))
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
		got, err := ResolveValue(input)
		if err != nil || got != want {
			t.Errorf("resolveConfigValue(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := ResolveValue("$UNREAL_TEST_MISSING"); err == nil {
		t.Error("missing variable should fail")
	}
}

func TestSessionDirectory(t *testing.T) {
	if got := SessionDirectory("/h", "/work/project", ""); got != "/h/sessions/--work-project--" {
		t.Errorf("default = %q", got)
	}
	if got := SessionDirectory("/h", "/work/project", ".sessions"); got != "/work/project/.sessions" {
		t.Errorf("relative = %q", got)
	}
}
