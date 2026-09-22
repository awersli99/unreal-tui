package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Settings are the user's last-used choices, persisted across launches.
type Settings struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

var thinkingLevels = []string{"low", "medium", "high", "xhigh", "max"}

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

// sessionDirectory keeps each workspace's sessions apart, the way pi and
// Claude Code do, so /resume only lists sessions for the current project.
func sessionDirectory(home, workspace string) string {
	slug := strings.ReplaceAll(strings.Trim(workspace, string(filepath.Separator)), string(filepath.Separator), "-")
	return filepath.Join(home, "sessions", "--"+slug+"--")
}

func loadSettings(home string) (Settings, error) {
	var settings Settings
	encoded, err := os.ReadFile(filepath.Join(home, "settings.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return settings, fmt.Errorf("read settings: %w", err)
	}
	if err := json.Unmarshal(encoded, &settings); err != nil {
		return settings, fmt.Errorf("parse %s: %w", filepath.Join(home, "settings.json"), err)
	}
	return settings, nil
}

func saveSettings(home string, settings Settings) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", home, err)
	}
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, "settings.json"), append(encoded, '\n'), 0o600)
}
