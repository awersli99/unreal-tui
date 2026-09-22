package main

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func TestFooterModelShortensProviderFirst(t *testing.T) {
	provider, id := "openrouter", "anthropic/claude-sonnet-4.5"
	cases := []struct {
		thinking string
		room     int
		want     string
	}{
		{"high", 100, "openrouter/anthropic/claude-sonnet-4.5 • high"},
		{"high", len(id) + 7 + 6, "open…/anthropic/claude-sonnet-4.5 • high"},
		{"high", len(id) + 7 + 3, "o…/anthropic/claude-sonnet-4.5 • high"},
		{"high", len(id) + 7 + 2, "anthropic/claude-sonnet-4.5 • high"},
		{"high", len(id) + 7, "anthropic/claude-sonnet-4.5 • high"},
		{"high", 20, "anthropic/cl… • high"},
		{"medium", 11, "medium"},
		{"medium", 3, "med"},
		{"", 10, "anthropic…"},
		{"high", -5, ""},
	}
	for _, test := range cases {
		if got := footerModel(provider, id, test.thinking, test.room); got != test.want {
			t.Errorf("footerModel(%q, %d) = %q, want %q", test.thinking, test.room, got, test.want)
		}
	}
}

func TestCacheHitRate(t *testing.T) {
	if _, ok := (Usage{LastInput: 100}).CacheHitRate(); ok {
		t.Fatal("cache hit rate shown before any caching")
	}
	rate, ok := Usage{Input: 300, Cached: 150, LastInput: 200, LastCached: 150}.CacheHitRate()
	if !ok || rate != 75 {
		t.Fatalf("CacheHitRate() = %v, %v; want 75, true", rate, ok)
	}
	rate, ok = Usage{Input: 100, CacheWrite: 90, LastInput: 100}.CacheHitRate()
	if !ok || rate != 0 {
		t.Fatalf("CacheHitRate() after write only = %v, %v; want 0, true", rate, ok)
	}
}

func TestEditorShowsWrappedLines(t *testing.T) {
	current := newLoginTestModel(t)
	current.Update(tea.WindowSizeMsg{Width: 20, Height: 20})
	typeText := func(text string) {
		for _, r := range text {
			current.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
	}
	editorLines := func() []string {
		lines := strings.Split(ansiEscape.ReplaceAllString(current.inputView(func(line string) string { return line }), ""), "\n")
		return lines[1 : len(lines)-1]
	}

	typeText("first line of text that wraps")
	lines := editorLines()
	if len(lines) != 2 || !strings.Contains(lines[0], "first") || !strings.Contains(lines[1], "wraps") {
		t.Fatalf("wrapped prompt shows %q", lines)
	}

	// Past max(5, 30% of the height) lines the editor scrolls to the cursor,
	// and shrinks back without a stale offset once the text fits again.
	typeText(strings.Repeat(" more words here", 8))
	lines = editorLines()
	if len(lines) != current.maxInputLines() || strings.Contains(lines[0], "first") {
		t.Fatalf("long prompt shows %q", lines)
	}
	if top := scrollRule("↑", current.editor.scroll, 20); current.editor.scroll == 0 || !strings.Contains(top, "more") {
		t.Fatalf("scroll %d, top rule %q", current.editor.scroll, top)
	}
	current.setInput("first line of text that wraps")
	if lines = editorLines(); len(lines) != 2 || !strings.Contains(lines[0], "first") {
		t.Fatalf("after shrinking the prompt shows %q", lines)
	}
}

func TestReasoningRenderedLikePi(t *testing.T) {
	render := newRenderer("dark", 40)
	strip := func(text string) []string {
		return strings.Split(ansiEscape.ReplaceAllString(text, ""), "\n")
	}
	indent := func(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }

	assistant := strip(render.block(Block{Kind: BlockAssistant, Text: "Plain answer text."}, true, false))
	text := "**Weighing options**\n\n" + strings.Repeat("considering the options ", 4)
	rendered := render.block(Block{Kind: BlockReasoning, Text: text}, true, false)
	reasoning := strip(rendered)
	if len(reasoning) < 3 || strings.Contains(rendered, "**") || strings.Contains(rendered, "∴") {
		t.Fatalf("reasoning not rendered as markdown: %q", reasoning)
	}
	if !regexp.MustCompile(`\x1b\[[0-9;]*;3m`).MatchString(rendered) {
		t.Fatalf("reasoning is not italic: %q", rendered)
	}
	for _, line := range reasoning {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if indent(line) != markdownMargin || indent(assistant[0]) != markdownMargin || len(strings.TrimRight(line, " ")) > 40 {
			t.Fatalf("reasoning lines %q, assistant %q", reasoning, assistant)
		}
	}

	hidden := strip(render.block(Block{Kind: BlockReasoning, Text: text}, false, false))
	if len(hidden) != 1 || hidden[0] != "  "+hiddenThinkingLabel {
		t.Fatalf("hidden reasoning shows %q", hidden)
	}
}
