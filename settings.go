package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	settingsFileName = "settings.json"
	modelsFileName   = "models.json"
	projectDirName   = ".unreal"
)

var thinkingLevels = []string{"low", "medium", "high", "xhigh", "max"}

// Settings uses pi's settings.json key names for the options that apply to
// the harness. Global settings live in ~/.unreal-tui/settings.json; a project's
// .unreal/settings.json overrides them, with nested objects merged.
type Settings struct {
	DefaultProvider      string            `json:"defaultProvider,omitempty"`
	DefaultModel         string            `json:"defaultModel,omitempty"`
	DefaultThinkingLevel string            `json:"defaultThinkingLevel,omitempty"`
	ModelThinkingLevels  map[string]string `json:"modelThinkingLevels,omitempty"`
	EnabledModels        []string          `json:"enabledModels,omitempty"`
	HideThinkingBlock    bool              `json:"hideThinkingBlock,omitempty"`
	QuietStartup         bool              `json:"quietStartup,omitempty"`
	Theme                string            `json:"theme,omitempty"`
	ShellPath            string            `json:"shellPath,omitempty"`
	SessionDir           string            `json:"sessionDir,omitempty"`
	Retry                RetrySettings     `json:"retry,omitzero"`
}

type RetrySettings struct {
	MaxRetries *int `json:"maxRetries,omitempty"`
}

// globalOnlySettings are ignored in project settings: a cloned repository
// must not be able to pick the binary that runs every Bash command.
var globalOnlySettings = []string{"shellPath"}

// legacySettingKeys maps the keys written by the first version of unreal.
var legacySettingKeys = map[string]string{
	"provider": "defaultProvider",
	"model":    "defaultModel",
	"thinking": "defaultThinkingLevel",
}

// Config is everything loaded from disk at startup (and on /reload).
type Config struct {
	Home      string
	Workspace string
	Settings  Settings
	Warnings  []string
}

func validThinking(level string) bool {
	for _, candidate := range thinkingLevels {
		if level == candidate {
			return true
		}
	}
	return false
}

// homeDirectory is ~/.unreal-tui, or $UNREAL_TUI_HOME when set.
func homeDirectory() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("UNREAL_TUI_HOME")); configured != "" {
		return configured, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".unreal-tui"), nil
}

func globalSettingsPath(home string) string {
	return filepath.Join(home, settingsFileName)
}

func projectSettingsPath(workspace string) string {
	return filepath.Join(workspace, projectDirName, settingsFileName)
}

func loadConfig(home, workspace string) (Config, error) {
	config := Config{Home: home, Workspace: workspace}
	global, err := readSettingsMap(globalSettingsPath(home))
	if err != nil {
		return config, err
	}
	if migrateLegacySettings(global) {
		if err := writeSettingsMap(globalSettingsPath(home), global); err != nil {
			return config, err
		}
	}
	project, err := readSettingsMap(projectSettingsPath(workspace))
	if err != nil {
		return config, err
	}
	for _, key := range globalOnlySettings {
		if _, present := project[key]; present {
			delete(project, key)
			config.Warnings = append(config.Warnings, fmt.Sprintf("Ignoring %q in %s: it is a global-only setting", key, projectSettingsPath(workspace)))
		}
	}

	merged, err := json.Marshal(mergeSettings(global, project))
	if err != nil {
		return config, err
	}
	if err := json.Unmarshal(merged, &config.Settings); err != nil {
		return config, fmt.Errorf("invalid settings: %w", err)
	}
	config.Warnings = append(config.Warnings, validateSettings(&config.Settings)...)
	return config, nil
}

// validateSettings drops invalid values so a typo cannot stop startup.
func validateSettings(settings *Settings) []string {
	var warnings []string
	if level := settings.DefaultThinkingLevel; level != "" && !validThinking(level) {
		warnings = append(warnings, fmt.Sprintf("Ignoring defaultThinkingLevel %q; use one of %s", level, strings.Join(thinkingLevels, ", ")))
		settings.DefaultThinkingLevel = ""
	}
	for key, level := range settings.ModelThinkingLevels {
		if !validThinking(level) {
			warnings = append(warnings, fmt.Sprintf("Ignoring modelThinkingLevels[%q] = %q", key, level))
			delete(settings.ModelThinkingLevels, key)
		}
	}
	switch settings.Theme {
	case "", "auto", "dark", "light":
	default:
		warnings = append(warnings, fmt.Sprintf("Unknown theme %q; use dark, light or auto", settings.Theme))
		settings.Theme = ""
	}
	if retries := settings.Retry.MaxRetries; retries != nil && *retries < 0 {
		warnings = append(warnings, "Ignoring negative retry.maxRetries")
		settings.Retry.MaxRetries = nil
	}
	return warnings
}

// mergeSettings overlays project onto global, merging nested objects the way
// pi does.
func mergeSettings(global, project map[string]any) map[string]any {
	merged := make(map[string]any, len(global))
	for key, value := range global {
		merged[key] = value
	}
	for key, value := range project {
		projectObject, projectIsObject := value.(map[string]any)
		globalObject, globalIsObject := merged[key].(map[string]any)
		if projectIsObject && globalIsObject {
			merged[key] = mergeSettings(globalObject, projectObject)
		} else {
			merged[key] = value
		}
	}
	return merged
}

func migrateLegacySettings(settings map[string]any) bool {
	migrated := false
	for legacy, current := range legacySettingKeys {
		value, present := settings[legacy]
		if !present {
			continue
		}
		if _, exists := settings[current]; !exists {
			settings[current] = value
		}
		delete(settings, legacy)
		migrated = true
	}
	return migrated
}

func readSettingsMap(path string) (map[string]any, error) {
	settings := make(map[string]any)
	encoded, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(encoded)) == 0 {
		return settings, nil
	}
	if err := json.Unmarshal(encoded, &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return settings, nil
}

func writeSettingsMap(path string, settings map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o600)
}

// saveGlobalSetting rewrites one key of the global settings file, leaving the
// rest of the file (including keys this program does not know) untouched.
// A nil value removes the key.
func saveGlobalSetting(home, key string, value any) error {
	path := globalSettingsPath(home)
	settings, err := readSettingsMap(path)
	if err != nil {
		return err
	}
	migrateLegacySettings(settings)
	if value == nil {
		delete(settings, key)
	} else {
		settings[key] = value
	}
	return writeSettingsMap(path, settings)
}

// sessionDirectory is where sessions for workspace live: sessionDir when set
// (relative paths resolve against the workspace), otherwise a per-workspace
// folder under the config home, the way pi and Claude Code organise them.
func sessionDirectory(home, workspace, configured string) string {
	if configured = strings.TrimSpace(configured); configured != "" {
		configured = expandHome(configured)
		if !filepath.IsAbs(configured) {
			configured = filepath.Join(workspace, configured)
		}
		return configured
	}
	slug := strings.ReplaceAll(strings.Trim(workspace, string(filepath.Separator)), string(filepath.Separator), "-")
	return filepath.Join(home, "sessions", "--"+slug+"--")
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// resolveConfigValue applies pi's value rules for secrets in models.json:
// "!cmd" runs a shell command and uses its output, "$VAR" and "${VAR}"
// interpolate environment variables, "$$" and "$!" escape, anything else is
// literal.
func resolveConfigValue(value string) (string, error) {
	if command, ok := strings.CutPrefix(value, "!"); ok {
		output, err := exec.Command("/bin/sh", "-c", command).Output()
		if err != nil {
			return "", fmt.Errorf("run %q: %w", command, err)
		}
		return strings.TrimSpace(string(output)), nil
	}
	var resolved strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '$' || index+1 >= len(value) {
			resolved.WriteByte(value[index])
			continue
		}
		next := value[index+1]
		switch {
		case next == '$' || next == '!':
			resolved.WriteByte(next)
			index++
		case next == '{':
			end := strings.IndexByte(value[index:], '}')
			if end < 0 {
				return "", fmt.Errorf("unterminated ${ in %q", value)
			}
			name := value[index+2 : index+end]
			variable, ok := os.LookupEnv(name)
			if !ok {
				return "", fmt.Errorf("environment variable %s is not set", name)
			}
			resolved.WriteString(variable)
			index += end
		case isNameStart(next):
			end := index + 1
			for end < len(value) && isNameChar(value[end]) {
				end++
			}
			name := value[index+1 : end]
			variable, ok := os.LookupEnv(name)
			if !ok {
				return "", fmt.Errorf("environment variable %s is not set", name)
			}
			resolved.WriteString(variable)
			index = end - 1
		default:
			resolved.WriteByte('$')
		}
	}
	return resolved.String(), nil
}

func isNameStart(char byte) bool {
	return char == '_' || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

func isNameChar(char byte) bool {
	return isNameStart(char) || (char >= '0' && char <= '9')
}
