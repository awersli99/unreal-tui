package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"

	"github.com/awersli99/unreal-tui/internal/engine"
	"github.com/awersli99/unreal-tui/internal/testutil"
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
	if _, ok := (engine.Usage{LastInput: 100}).CacheHitRate(); ok {
		t.Fatal("cache hit rate shown before any caching")
	}
	rate, ok := engine.Usage{Input: 300, Cached: 150, LastInput: 200, LastCached: 150}.CacheHitRate()
	if !ok || rate != 75 {
		t.Fatalf("CacheHitRate() = %v, %v; want 75, true", rate, ok)
	}
	rate, ok = engine.Usage{Input: 100, CacheWrite: 90, LastInput: 100}.CacheHitRate()
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

	assistant := strip(render.block(engine.Block{Kind: engine.BlockAssistant, Text: "Plain answer text."}, true, false))
	text := "**Weighing options**\n\n" + strings.Repeat("considering the options ", 4)
	rendered := render.block(engine.Block{Kind: engine.BlockReasoning, Text: text}, true, false)
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

	hidden := strip(render.block(engine.Block{Kind: engine.BlockReasoning, Text: text}, false, false))
	if len(hidden) != 1 || hidden[0] != "  "+hiddenThinkingLabel {
		t.Fatalf("hidden reasoning shows %q", hidden)
	}
}

func TestGitBranch(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	testutil.WriteFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/feature/x\n")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(nested); got != "feature/x" {
		t.Errorf("branch = %q", got)
	}

	worktree := t.TempDir()
	testutil.WriteFile(t, filepath.Join(worktree, ".git"), "gitdir: ../wt-git\n")
	testutil.WriteFile(t, filepath.Join(filepath.Dir(worktree), "wt-git", "HEAD"), "0123456789abcdef\n")
	if got := gitBranch(worktree); got != "detached" {
		t.Errorf("worktree branch = %q", got)
	}
}

func TestFormatTokens(t *testing.T) {
	for count, want := range map[int64]string{999: "999", 1_234: "1.2k", 272_000: "272k", 1_500_000: "1.5M", 12_000_000: "12M"} {
		if got := formatTokens(count); got != want {
			t.Errorf("formatTokens(%d) = %q, want %q", count, got, want)
		}
	}
}

// A blank line separates the live area from the scrollback, like the one
// between printed blocks, whether the agent is idle or thinking.
func TestLiveAreaStartsWithBlankLine(t *testing.T) {
	current := newLoginTestModel(t)
	current.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	check := func(state, want string) {
		t.Helper()
		lines := strings.Split(ansiEscape.ReplaceAllString(current.View(), ""), "\n")
		if len(lines) < 2 || strings.TrimSpace(lines[0]) != "" || !strings.Contains(lines[1], want) {
			t.Fatalf("%s: live area starts with %q", state, lines[:min(2, len(lines))])
		}
	}
	check("idle", "─")
	current.transcript.Apply(sessionstore.Item{Data: session.Turn{ID: "turn"}})
	check("thinking", "Thinking…")
}
