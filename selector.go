package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const selectorRows = 10

type selectorItem struct {
	Label   string
	Detail  string
	Value   any
	Current bool // Marked with ✓: the value in effect.
	Checked bool // Multi-select state.
}

type selectorAction int

const (
	selectorNone selectorAction = iota
	selectorChoose
	selectorSave
	selectorCancel
	selectorToggle
)

// selector is a filterable list that temporarily replaces the editor, like
// pi's built-in pickers.
type selector struct {
	title      string
	hint       string
	items      []selectorItem
	visible    []int
	cursor     int
	offset     int
	filterable bool
	multi      bool
	filter     textinput.Model
	// extra, when set, offers an item built from the filter text, such as
	// "use this model ID"; it is shown when nothing else matches.
	extra     func(query string) *selectorItem
	extraItem *selectorItem
}

func newSelector(title, hint string, items []selectorItem, filterable bool) *selector {
	filter := textinput.New()
	filter.Prompt = "  search: "
	filter.PromptStyle = dimStyle
	filter.Placeholder = "type to filter"
	filter.PlaceholderStyle = dimStyle
	filter.Focus()
	current := &selector{title: title, hint: hint, items: items, filterable: filterable, filter: filter}
	current.refilter()
	for position, index := range current.visible {
		if current.items[index].Current {
			current.cursor = position
			break
		}
	}
	current.scrollToCursor()
	return current
}

func (current *selector) setQuery(query string) {
	current.filter.SetValue(query)
	current.filter.CursorEnd()
	current.refilter()
	current.cursor = 0
	current.scrollToCursor()
}

func (current *selector) refilter() {
	query := current.filter.Value()
	current.visible = current.visible[:0]
	for index, item := range current.items {
		if !current.filterable || fuzzyMatch(item.Label+" "+item.Detail, query) {
			current.visible = append(current.visible, index)
		}
	}
	current.extraItem = nil
	if current.extra != nil && strings.TrimSpace(query) != "" && len(current.visible) == 0 {
		current.extraItem = current.extra(strings.TrimSpace(query))
	}
	current.cursor = min(current.cursor, max(0, current.count()-1))
}

func (current *selector) count() int {
	if current.extraItem != nil {
		return len(current.visible) + 1
	}
	return len(current.visible)
}

// Selected returns the highlighted item, or nil when the list is empty.
func (current *selector) Selected() *selectorItem {
	if current.cursor < len(current.visible) {
		return &current.items[current.visible[current.cursor]]
	}
	if current.extraItem != nil {
		return current.extraItem
	}
	return nil
}

func (current *selector) Update(message tea.KeyMsg) selectorAction {
	switch message.String() {
	case "up", "ctrl+k":
		current.move(-1)
	case "down", "ctrl+j":
		current.move(1)
	case "pgup":
		current.move(-selectorRows)
	case "pgdown":
		current.move(selectorRows)
	case "enter":
		if current.Selected() == nil {
			return selectorNone
		}
		if current.multi {
			return current.toggle()
		}
		return selectorChoose
	case " ":
		if current.multi {
			return current.toggle()
		}
		return current.typeKey(message)
	case "ctrl+s":
		return selectorSave
	case "esc", "ctrl+c":
		return selectorCancel
	default:
		return current.typeKey(message)
	}
	return selectorNone
}

func (current *selector) typeKey(message tea.KeyMsg) selectorAction {
	if !current.filterable {
		return selectorNone
	}
	before := current.filter.Value()
	current.filter, _ = current.filter.Update(message)
	if current.filter.Value() != before {
		current.refilter()
		current.cursor = 0
		current.scrollToCursor()
	}
	return selectorNone
}

func (current *selector) toggle() selectorAction {
	if item := current.Selected(); item != nil {
		item.Checked = !item.Checked
		return selectorToggle
	}
	return selectorNone
}

// SetAll checks or unchecks every visible item.
func (current *selector) SetAll(checked bool) {
	for _, index := range current.visible {
		current.items[index].Checked = checked
	}
}

func (current *selector) move(delta int) {
	count := current.count()
	if count == 0 {
		return
	}
	current.cursor = min(max(current.cursor+delta, 0), count-1)
	current.scrollToCursor()
}

func (current *selector) scrollToCursor() {
	if current.cursor < current.offset {
		current.offset = current.cursor
	}
	if current.cursor >= current.offset+selectorRows {
		current.offset = current.cursor - selectorRows + 1
	}
}

func (current *selector) View(width int) string {
	titleStyle := lipgloss.NewStyle().Foreground(accentColor).Bold(true)
	lines := []string{titleStyle.Render(current.title)}
	if current.filterable {
		lines = append(lines, current.filter.View())
	}
	count := current.count()
	if count == 0 {
		lines = append(lines, dimStyle.Render("  no matches"))
	}
	end := min(count, current.offset+selectorRows)
	for position := current.offset; position < end; position++ {
		var item selectorItem
		if position < len(current.visible) {
			item = current.items[current.visible[position]]
		} else {
			item = *current.extraItem
		}
		lines = append(lines, current.row(item, position == current.cursor, width))
	}
	if count > selectorRows {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("  %d/%d", current.cursor+1, count)))
	}
	lines = append(lines, dimStyle.Render("  "+firstLine(current.hint, max(10, width-3))))
	return strings.Join(lines, "\n")
}

func (current *selector) row(item selectorItem, highlighted bool, width int) string {
	pointer := "  "
	if highlighted {
		pointer = lipgloss.NewStyle().Foreground(accentColor).Render("❯ ")
	}
	mark := ""
	if current.multi {
		mark = "[ ] "
		if item.Checked {
			mark = lipgloss.NewStyle().Foreground(successColor).Render("[x]") + " "
		}
	}
	label := item.Label
	if highlighted {
		label = lipgloss.NewStyle().Bold(true).Render(label)
	}
	suffix := ""
	if item.Current {
		suffix = lipgloss.NewStyle().Foreground(successColor).Render(" ✓")
	}
	line := pointer + mark + label + suffix
	if item.Detail != "" {
		room := width - lipgloss.Width(line) - 3
		if room > 8 {
			line += "  " + dimStyle.Render(firstLine(item.Detail, room))
		}
	}
	return line
}
