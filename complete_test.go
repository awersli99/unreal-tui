package main

import (
	"os"
	"path/filepath"
	"testing"
)

func suggestionLabels(suggestions []suggestion) []string {
	labels := make([]string, 0, len(suggestions))
	for _, item := range suggestions {
		labels = append(labels, item.Label)
	}
	return labels
}

func TestCommandSuggestions(t *testing.T) {
	app := &App{Catalog: &Catalog{Models: []ModelInfo{
		{Provider: "p", ID: "fast-one", Name: "Fast"},
		{Provider: "p", ID: "deep-one", Levels: []string{"medium", "high"}},
	}}}
	app.Model = app.Catalog.Models[1]
	cases := []struct {
		input string
		want  []string
	}{
		{"/", nil}, // every command; checked separately below
		{"/re", []string{"/reload", "/resume"}},
		{"/se", []string{"/settings", "/resume", "/session", "/scoped-models"}}, // /resume via its alias "sessions"
		{"/exit", []string{"/quit"}},
		{"/model ", []string{"p/fast-one", "p/deep-one"}},
		{"/model deep", []string{"p/deep-one"}},
		{"/thinking h", []string{"high"}},
		{"/thinking ", []string{"medium", "high"}},
		{"/new now", nil},
		{"/model a b", nil},
		{"hello /re", nil},
		{"/re\nmore", nil},
	}
	for _, test := range cases[1:] {
		if got := suggestionLabels(app.suggestionsFor(test.input)); !equalStrings(got, test.want) {
			t.Errorf("suggestionsFor(%q) = %q, want %q", test.input, got, test.want)
		}
	}
	if got := app.suggestionsFor("/"); len(got) != len(commands) {
		t.Errorf("/ suggests %d commands, want %d", len(got), len(commands))
	}
	if got := app.suggestionsFor("/mod")[0].Text; got != "/model " {
		t.Errorf("completion text = %q", got)
	}
}

func TestSuggestStateScrolls(t *testing.T) {
	state := suggestState{items: make([]suggestion, 8)}
	state.move(-1)
	if state.cursor != 7 || state.offset != 3 {
		t.Fatalf("wrap up: cursor %d offset %d", state.cursor, state.offset)
	}
	state.move(1)
	if state.cursor != 0 || state.offset != 0 {
		t.Fatalf("wrap down: cursor %d offset %d", state.cursor, state.offset)
	}
}

func TestGitBranch(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/feature/x\n")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(nested); got != "feature/x" {
		t.Errorf("branch = %q", got)
	}

	worktree := t.TempDir()
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: ../wt-git\n")
	writeFile(t, filepath.Join(filepath.Dir(worktree), "wt-git", "HEAD"), "0123456789abcdef\n")
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
