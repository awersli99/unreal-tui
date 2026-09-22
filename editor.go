package main

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
)

// The editor is laid out the way pi's is: it grows with its soft-wrapped
// content up to max(5, 30% of the terminal height), then scrolls to keep the
// cursor in view, and the rules around it count the hidden lines.
//
// The textarea's own viewport only scrolls when the cursor leaves it, and it
// is resized after the edit that wrapped the line, so it would keep a stale
// offset and hide the first line. Instead the textarea is always as tall as
// its content, so it never scrolls, and inputView windows it.

// inputRowLimit bounds the textarea's rows; it never scrolls below it.
const inputRowLimit = 10000

const minInputLines = 5

type editorLayout struct {
	// probe measures how many rows a line wraps to, with the same width and
	// wrapping as the input.
	probe textarea.Model
	rows  map[string]int
	// lines is the input's wrapped line count and scroll the first visible one.
	lines, scroll int
}

func newProbe() textarea.Model {
	probe := textarea.New()
	probe.ShowLineNumbers = false
	probe.CharLimit = 0
	probe.MaxHeight = 0
	// Without a prompt, SetWidth gives the probe exactly the wrap width.
	probe.Prompt = ""
	return probe
}

// editInput applies a change to the editor and lays it out again. The
// textarea is made as tall as it can be first, so the change cannot scroll it.
func (current *model) editInput(change func()) {
	current.input.SetHeight(inputRowLimit)
	change()
	current.layoutInput()
}

func (current *model) layoutInput() {
	layout := &current.editor
	if width := current.input.Width(); layout.probe.Width() != width || len(layout.rows) > 1000 {
		layout.probe.SetWidth(width)
		layout.rows = make(map[string]int)
	}
	cursorRow := current.input.Line()
	total, cursor := 0, 0
	for row, text := range strings.Split(current.input.Value(), "\n") {
		rows := layout.wrappedRows(text)
		if row < cursorRow {
			cursor += rows
		}
		total += rows
	}
	cursor += current.input.LineInfo().RowOffset
	current.input.SetHeight(total)

	visible := current.maxInputLines()
	if cursor < layout.scroll {
		layout.scroll = cursor
	} else if cursor >= layout.scroll+visible {
		layout.scroll = cursor - visible + 1
	}
	layout.scroll = max(0, min(layout.scroll, total-visible))
	layout.lines = total
}

func (layout *editorLayout) wrappedRows(text string) int {
	if rows, ok := layout.rows[text]; ok {
		return rows
	}
	layout.probe.SetValue(text)
	rows := max(1, layout.probe.LineInfo().Height)
	layout.rows[text] = rows
	return rows
}

func (current *model) maxInputLines() int {
	return max(minInputLines, current.height*3/10)
}

// inputView renders the visible window of the editor with the rules above and
// below it, which show how many lines are scrolled out of view.
func (current *model) inputView(rule func(string) string) string {
	layout := current.editor
	lines := strings.Split(current.input.View(), "\n")
	start := min(layout.scroll, len(lines))
	end := min(len(lines), start+current.maxInputLines())
	below := max(0, layout.lines-end)
	width := max(1, current.width)
	return rule(scrollRule("↑", start, width)) + "\n" +
		strings.Join(lines[start:end], "\n") + "\n" +
		rule(scrollRule("↓", below, width))
}

// scrollRule is a horizontal rule, labelled like pi's with the number of
// hidden lines in the given direction.
func scrollRule(direction string, hidden, width int) string {
	if hidden <= 0 {
		return strings.Repeat("─", width)
	}
	label := " " + direction + " " + strconv.Itoa(hidden) + " more "
	labelWidth := len([]rune(label))
	if labelWidth+2 <= width {
		left := (width - labelWidth) / 2
		return strings.Repeat("─", left) + label + strings.Repeat("─", width-left-labelWidth)
	}
	indicator := "─── " + direction + " " + strconv.Itoa(hidden) + " more "
	if remaining := width - len([]rune(indicator)); remaining >= 0 {
		return indicator + strings.Repeat("─", remaining)
	}
	return truncateWidth(indicator, width)
}
