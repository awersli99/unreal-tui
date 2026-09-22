package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/awersli99/unreal-tui/internal/config"
	"github.com/awersli99/unreal-tui/internal/provider"
)

// The model picker is laid out like Devin's: a search line, then aligned rows
// of model name, reasoning effort bars and level, where ←/→ set the effort
// of the highlighted model before choosing it, and a line describing it.

const modelNameWidth = 28

var (
	levelLabels = map[string]string{"low": "Low", "medium": "Medium", "high": "High", "xhigh": "XHigh", "max": "Max"}

	pickerHighlight = lipgloss.AdaptiveColor{Light: "#E6E4F2", Dark: "#24222E"}
)

// modelPicker holds the effort chosen for each model while the picker is open.
type modelPicker struct {
	app      *App
	selector *selector
	efforts  map[string]string
}

func (current *model) openModelPicker(query string) {
	app := current.app
	models := slices.Clone(app.Catalog.Models)
	if !slices.ContainsFunc(models, func(model provider.ModelInfo) bool { return model.Key() == app.Model.Key() }) {
		models = append([]provider.ModelInfo{app.Model}, models...)
	}
	picker := &modelPicker{app: app, efforts: make(map[string]string, len(models))}
	items := make([]selectorItem, 0, len(models))
	for _, info := range models {
		items = append(items, pickerModelItem(info, info.Key() == app.Model.Key()))
	}
	picker.selector = newSelector("", "", items, true)
	picker.selector.filter.Prompt = "/ "
	picker.selector.filter.Placeholder = "Type to search"
	picker.selector.extra = func(query string) *selectorItem {
		info, _, err := app.Catalog.Resolve(query, app.Model.Provider)
		if err != nil {
			return nil
		}
		item := pickerModelItem(info, false)
		item.Label = "Use " + info.Key()
		item.Detail = "not in the catalog"
		return &item
	}
	if query != "" {
		picker.selector.setQuery(query)
	}
	current.overlay = &overlay{
		selector: picker.selector,
		view:     picker.View,
		keys:     picker.handleKey,
		choose: func(item *selectorItem) tea.Cmd {
			info := item.Value.(provider.ModelInfo)
			return current.switchModel(info, picker.effort(info))
		},
	}
}

func pickerModelItem(info provider.ModelInfo, isCurrent bool) selectorItem {
	return selectorItem{Label: config.FirstNonEmpty(info.Name, info.ID), Detail: info.Key(), Value: info, Current: isCurrent}
}

// effort is the level the model would run at: as chosen in the picker, else
// the current level for the current model, else the level it would get when
// switched to.
func (picker *modelPicker) effort(info provider.ModelInfo) string {
	if level, ok := picker.efforts[info.Key()]; ok {
		return level
	}
	if info.Key() == picker.app.Model.Key() {
		return info.ClampLevel(picker.app.Thinking)
	}
	return picker.app.thinkingFor(info, "")
}

func (picker *modelPicker) handleKey(key string) (tea.Cmd, bool) {
	step := map[string]int{"left": -1, "right": 1}[key]
	item := picker.selector.Selected()
	if step == 0 || item == nil {
		return nil, false
	}
	info := item.Value.(provider.ModelInfo)
	levels := info.SupportedLevels()
	position := slices.Index(levels, picker.effort(info))
	picker.efforts[info.Key()] = levels[min(max(position+step, 0), len(levels)-1)]
	return nil, true
}

func (picker *modelPicker) View(width int) string {
	current := picker.selector
	lines := []string{current.filter.View(), ruleStyle.Render(strings.Repeat("─", max(1, width)))}
	nameWidth := 0
	for _, item := range current.items {
		nameWidth = max(nameWidth, lipgloss.Width(item.Label))
	}
	if current.extraItem != nil {
		nameWidth = max(nameWidth, lipgloss.Width(current.extraItem.Label))
	}
	nameWidth = min(nameWidth, modelNameWidth, max(8, width-30))

	start, end := current.window()
	if start == end {
		lines = append(lines, dimStyle.Render("  No matching models"))
	}
	for position := start; position < end; position++ {
		lines = append(lines, picker.row(current.itemAt(position), position == current.cursor, nameWidth, width))
	}
	var more []string
	if start > 0 {
		more = append(more, fmt.Sprintf("↑ %d more above", start))
	}
	if hidden := current.count() - end; hidden > 0 {
		more = append(more, fmt.Sprintf("↓ %d more below", hidden))
	}
	if len(more) != 0 {
		lines = append(lines, dimStyle.Render("  "+strings.Join(more, " · ")))
	}
	if item := current.Selected(); item != nil {
		lines = append(lines, "", picker.describe(item.Value.(provider.ModelInfo), item.Current, width))
	}
	hint := "↑↓ select · ←→ reasoning effort · ↵ confirm · esc cancel"
	return strings.Join(append(lines, "", dimStyle.Render(truncateWidth(hint, max(1, width-1)))), "\n")
}

// row renders one model: the pointer, name, a ✓ on the current model, the
// effort bars with arrows when highlighted, the level, and the model's key.
func (picker *modelPicker) row(item selectorItem, highlighted bool, nameWidth, width int) string {
	info := item.Value.(provider.ModelInfo)
	style := func(base lipgloss.Style) lipgloss.Style {
		if highlighted {
			return base.Background(pickerHighlight)
		}
		return base
	}
	plain, accent, dim := style(lipgloss.NewStyle()), style(lipgloss.NewStyle().Foreground(accentColor)), style(dimStyle)
	text := func(style lipgloss.Style, value string, pad int) string {
		return style.Render(value + strings.Repeat(" ", max(0, pad-lipgloss.Width(value))))
	}

	// The highlighted row is in the accent colour, with arrows around the
	// effort bars.
	pointer, name, bars, levelStyle, arrows := "  ", plain, plain, dim, [2]string{" ", " "}
	if highlighted {
		pointer, name, bars, levelStyle, arrows = "❯ ", accent.Bold(true), accent, accent, [2]string{"←", "→"}
	}
	label := item.Label
	if lipgloss.Width(label) > nameWidth {
		label = truncateWidth(label, nameWidth-1) + "…"
	}
	mark := " "
	if item.Current {
		mark = "✓"
	}
	levels := info.SupportedLevels()
	level := picker.effort(info)
	filled := slices.Index(levels, level) + 1

	line := text(accent, pointer, 2) + text(name, label, nameWidth+2) +
		text(style(lipgloss.NewStyle().Foreground(successColor)), mark, 2) +
		text(accent, arrows[0], 2) +
		bars.Render(strings.Repeat("■", filled)) + dim.Render(strings.Repeat("■", len(levels)-filled)) +
		text(plain, "", len(config.ThinkingLevels)-len(levels)+1) +
		text(accent, arrows[1], 2) + text(levelStyle, levelLabels[level], 8)
	if room := width - 1 - lipgloss.Width(line); room > 6 {
		detail := item.Detail
		if lipgloss.Width(detail) > room-2 {
			detail = truncateWidth(detail, room-3) + "…"
		}
		line += text(dim, "  "+detail, room)
	} else if highlighted && room > 0 {
		line += text(plain, "", room)
	}
	return line
}

// describe is the line under the list: the highlighted model's effort as a
// badge, what that effort means, and the model's context window.
func (picker *modelPicker) describe(info provider.ModelInfo, isCurrent bool, width int) string {
	level := picker.effort(info)
	color, ok := thinkingColors[level]
	if !ok {
		color = accentColor
	}
	badge := lipgloss.NewStyle().Bold(true).Padding(0, 1).
		Foreground(lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#111111"}).Background(color).
		Render(strings.ToUpper(levelLabels[level]))
	parts := []string{thinkingDescriptions[level]}
	if info.ContextWindow > 0 {
		parts = append(parts, formatTokens(info.ContextWindow)+" context")
	}
	if isCurrent {
		parts = append(parts, "current model")
	}
	room := max(1, width-3-lipgloss.Width(badge))
	return "  " + badge + dimStyle.Render(firstLine(" · "+strings.Join(parts, " · "), room))
}
