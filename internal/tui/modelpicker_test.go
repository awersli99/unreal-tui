package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestModelPickerSetsEffortWithArrows(t *testing.T) {
	current := newLoginTestModel(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key") // Switching builds a client; nothing is sent.
	current.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	current.openModelPicker("")
	view := func() []string {
		return strings.Split(ansiEscape.ReplaceAllString(current.overlay.View(100), ""), "\n")
	}
	selectedRow := func() string {
		t.Helper()
		for _, line := range view() {
			if strings.HasPrefix(line, "❯ ") {
				return line
			}
		}
		t.Fatalf("no highlighted row in %q", view())
		return ""
	}

	lines := view()
	if lines[0] != "/ Type to search" || !strings.HasPrefix(lines[len(lines)-1], "↑↓ select · ←→ reasoning effort") {
		t.Fatalf("picker shows %q", lines)
	}
	row := selectedRow()
	if !strings.Contains(row, "✓ ← ■■■■■ → High") || !strings.Contains(row, current.app.Model.Key()) {
		t.Fatalf("current model row is %q", row)
	}

	// ←/→ step through the model's levels and stop at the ends.
	for range 3 {
		current.Update(tea.KeyMsg{Type: tea.KeyRight})
	}
	if row = selectedRow(); !strings.Contains(row, "→ Max") {
		t.Fatalf("after → row is %q", row)
	}
	if current.app.Thinking != "high" {
		t.Fatal("the effort changed before a model was chosen")
	}
	current.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if row = selectedRow(); !strings.Contains(row, "→ XHigh") {
		t.Fatalf("after ← row is %q", row)
	}

	current.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if current.overlay != nil || current.app.Thinking != "xhigh" {
		t.Fatalf("after enter: overlay %v, thinking %q", current.overlay != nil, current.app.Thinking)
	}
}
