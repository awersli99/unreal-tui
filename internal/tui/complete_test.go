package tui

import (
	"slices"
	"testing"

	"github.com/awersli99/unreal-tui/internal/provider"
)

func suggestionLabels(suggestions []suggestion) []string {
	labels := make([]string, 0, len(suggestions))
	for _, item := range suggestions {
		labels = append(labels, item.Label)
	}
	return labels
}

func TestCommandSuggestions(t *testing.T) {
	app := &App{Catalog: &provider.Catalog{Models: []provider.ModelInfo{
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
		if got := suggestionLabels(app.suggestionsFor(test.input)); !slices.Equal(got, test.want) {
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
