package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const suggestRows = 5

// commandSpec describes a slash command for /help and autocompletion.
type commandSpec struct {
	Name    string
	Args    string
	Summary string
	Aliases []string
}

var commands = []commandSpec{
	{Name: "model", Args: "[pattern]", Summary: "Select a model (provider/id, id, glob, optional :level)", Aliases: []string{"models"}},
	{Name: "thinking", Args: "[level]", Summary: "Set the thinking level", Aliases: []string{"effort"}},
	{Name: "scoped-models", Summary: "Choose the models ctrl+p cycles through", Aliases: []string{"scoped"}},
	{Name: "settings", Summary: "Change settings", Aliases: []string{"config"}},
	{Name: "reload", Summary: "Reload settings, models.json, SYSTEM.md, AGENTS.md and skills"},
	{Name: "new", Summary: "Start a new session", Aliases: []string{"clear"}},
	{Name: "resume", Args: "[n|id]", Summary: "Resume a previous session", Aliases: []string{"sessions"}},
	{Name: "session", Summary: "Show session details"},
	{Name: "help", Summary: "Show commands and keys", Aliases: []string{"?"}},
	{Name: "quit", Summary: "Exit", Aliases: []string{"exit", "q"}},
}

func findCommand(name string) (commandSpec, bool) {
	for _, command := range commands {
		if command.Name == name || slices.Contains(command.Aliases, name) {
			return command, true
		}
	}
	return commandSpec{}, false
}

// suggestion is one autocomplete entry; Text replaces the editor contents.
type suggestion struct {
	Label  string
	Detail string
	Text   string
}

// suggestionsFor completes command names after "/" and, for some commands,
// their argument, the way pi's slash-command autocomplete does.
func (app *App) suggestionsFor(input string) []suggestion {
	if !strings.HasPrefix(input, "/") || strings.Contains(input, "\n") {
		return nil
	}
	name, argument, hasArgument := strings.Cut(input[1:], " ")
	if !hasArgument {
		return commandSuggestions(name)
	}
	command, ok := findCommand(name)
	if !ok || strings.Contains(argument, " ") {
		return nil
	}
	var suggestions []suggestion
	switch command.Name {
	case "model":
		for _, model := range app.Catalog.Models {
			if fuzzyMatch(model.Key()+" "+model.Name, argument) {
				suggestions = append(suggestions, suggestion{Label: model.Key(), Detail: model.Name, Text: "/model " + model.Key()})
			}
		}
	case "thinking":
		for _, level := range app.Model.SupportedLevels() {
			if strings.HasPrefix(level, strings.ToLower(argument)) {
				suggestions = append(suggestions, suggestion{Label: level, Detail: thinkingDescriptions[level], Text: "/thinking " + level})
			}
		}
	}
	return suggestions
}

// commandSuggestions ranks prefix matches first, then substring and
// subsequence matches.
func commandSuggestions(query string) []suggestion {
	query = strings.ToLower(query)
	var ranked [3][]suggestion
	for _, command := range commands {
		rank := -1
		for _, name := range append([]string{command.Name}, command.Aliases...) {
			var nameRank int
			switch {
			case strings.HasPrefix(name, query):
				nameRank = 0
			case strings.Contains(name, query):
				nameRank = 1
			case isSubsequence(name, query):
				nameRank = 2
			default:
				continue
			}
			if rank < 0 || nameRank < rank {
				rank = nameRank
			}
		}
		if rank >= 0 {
			ranked[rank] = append(ranked[rank], suggestion{Label: "/" + command.Name, Detail: command.Summary, Text: "/" + command.Name + " "})
		}
	}
	return slices.Concat(ranked[0], ranked[1], ranked[2])
}

func isSubsequence(text, query string) bool {
	for _, char := range query {
		index := strings.IndexRune(text, char)
		if index < 0 {
			return false
		}
		text = text[index+1:]
	}
	return true
}

// suggestState is the autocomplete list under the editor.
type suggestState struct {
	items  []suggestion
	cursor int
	offset int
	// input is the editor text the items were computed for; dismissed hides
	// the list until that text changes.
	input     string
	dismissed bool
}

func (state *suggestState) visible() bool {
	return !state.dismissed && len(state.items) != 0
}

func (state *suggestState) move(delta int) {
	count := len(state.items)
	state.cursor = ((state.cursor+delta)%count + count) % count
	if state.cursor < state.offset {
		state.offset = state.cursor
	}
	if state.cursor >= state.offset+suggestRows {
		state.offset = state.cursor - suggestRows + 1
	}
}

func (state *suggestState) selected() suggestion {
	return state.items[state.cursor]
}

func (state *suggestState) view(width int) string {
	column := 0
	for _, item := range state.items {
		column = max(column, lipgloss.Width(item.Label)+2)
	}
	column = min(column, max(12, width/2))
	selectedStyle := lipgloss.NewStyle().Foreground(accentColor)
	var lines []string
	end := min(len(state.items), state.offset+suggestRows)
	for index := state.offset; index < end; index++ {
		item := state.items[index]
		prefix := "  "
		if index == state.cursor {
			prefix = "→ "
		}
		label := item.Label
		if lipgloss.Width(label) > column-2 {
			label = truncateWidth(label, column-3) + "…"
		}
		spacing := strings.Repeat(" ", max(1, column-lipgloss.Width(label)))
		detail := ""
		if room := width - 2 - column - 2; item.Detail != "" && room > 10 {
			detail = firstLine(item.Detail, room)
		}
		if index == state.cursor {
			lines = append(lines, selectedStyle.Render(prefix+label+spacing+detail))
		} else {
			lines = append(lines, prefix+label+dimStyle.Render(spacing+detail))
		}
	}
	if len(state.items) > suggestRows {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("  (%d/%d)", state.cursor+1, len(state.items))))
	}
	return strings.Join(lines, "\n")
}
