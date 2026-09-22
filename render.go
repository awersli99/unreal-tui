package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
)

const (
	toolPreviewHead = 8
	toolPreviewTail = 4
	// markdownMargin is the left margin of glamour's dark and light styles,
	// which assistant text is rendered with.
	markdownMargin = 2
	// hiddenThinkingLabel stands in for reasoning while it is hidden, as in pi.
	hiddenThinkingLabel = "Thinking..."
)

var (
	accentColor  = lipgloss.AdaptiveColor{Light: "#5A4FCF", Dark: "#A99BFF"}
	dimColor     = lipgloss.AdaptiveColor{Light: "#8A8A8A", Dark: "#6C6C6C"}
	successColor = lipgloss.AdaptiveColor{Light: "#1F8A3B", Dark: "#5FD787"}
	errorColor   = lipgloss.AdaptiveColor{Light: "#C62828", Dark: "#FF6B6B"}
	warnColor    = lipgloss.AdaptiveColor{Light: "#B26A00", Dark: "#FFB454"}

	userMarkerStyle = lipgloss.NewStyle().Foreground(accentColor).Bold(true)
	userTextStyle   = lipgloss.NewStyle().Bold(true)
	dimStyle        = lipgloss.NewStyle().Foreground(dimColor)
	thinkingStyle   = lipgloss.NewStyle().Foreground(dimColor).Italic(true)
	errorStyle      = lipgloss.NewStyle().Foreground(errorColor)
	toolNameStyle   = lipgloss.NewStyle().Bold(true)
	ruleStyle       = lipgloss.NewStyle().Foreground(dimColor)
)

type renderer struct {
	width    int
	markdown *glamour.TermRenderer
	// thinking renders reasoning like assistant text, in dim italics.
	thinking *glamour.TermRenderer
	style    string
}

func newRenderer(style string, width int) *renderer {
	current := &renderer{style: style}
	current.resize(width)
	return current
}

func (current *renderer) setStyle(style string) {
	if style != current.style {
		current.style = style
		current.markdown = nil
		current.resize(current.width)
	}
}

func (current *renderer) resize(width int) {
	if width <= 0 {
		width = 80
	}
	if width == current.width && current.markdown != nil {
		return
	}
	current.width = width
	markdown, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(current.style),
		glamour.WithWordWrap(max(20, width-4)),
		glamour.WithEmoji(),
	)
	if err == nil {
		current.markdown = markdown
	}
	thinking, err := glamour.NewTermRenderer(
		glamour.WithStyles(thinkingStyles(current.style)),
		glamour.WithWordWrap(max(20, width-4)),
		glamour.WithEmoji(),
	)
	if err == nil {
		current.thinking = thinking
	}
}

// thinkingStyles is the markdown style with dim italic body text, like pi's
// thinking blocks. Markdown elements keep their own styling.
func thinkingStyles(style string) ansi.StyleConfig {
	config := styles.DarkStyleConfig
	color := dimColor.Dark
	if style == styles.LightStyle {
		config, color = styles.LightStyleConfig, dimColor.Light
	}
	italic := true
	config.Document.Color = &color
	config.Document.Italic = &italic
	return config
}

// block renders a finished block. full disables tool output truncation.
func (current *renderer) block(block Block, showThinking, full bool) string {
	wrap := lipgloss.NewStyle().Width(max(20, current.width-2))
	switch block.Kind {
	case BlockUser:
		return userMarkerStyle.Render("❯ ") + userTextStyle.Render(wrap.Width(max(20, current.width-4)).Render(block.Text))
	case BlockAssistant:
		return current.renderMarkdown(block.Text)
	case BlockReasoning:
		// Rendered like assistant text, as pi does.
		if !showThinking {
			return strings.Repeat(" ", markdownMargin) + thinkingStyle.Render(hiddenThinkingLabel)
		}
		return current.renderWith(current.thinking, block.Text)
	case BlockTool:
		return current.tool(block.Tool, full)
	case BlockInfo:
		return dimStyle.Render(wrap.Render("• " + block.Text))
	case BlockError:
		return errorStyle.Render(wrap.Render("✗ " + block.Text))
	}
	return ""
}

func (current *renderer) renderMarkdown(text string) string {
	return current.renderWith(current.markdown, text)
}

func (current *renderer) renderWith(markdown *glamour.TermRenderer, text string) string {
	if markdown == nil {
		return text
	}
	rendered, err := markdown.Render(text)
	if err != nil {
		return text
	}
	return strings.Trim(rendered, "\n")
}

func (current *renderer) tool(view *ToolView, full bool) string {
	var marker string
	switch view.State {
	case ToolSucceeded:
		marker = lipgloss.NewStyle().Foreground(successColor).Render("●")
	case ToolFailed:
		marker = lipgloss.NewStyle().Foreground(errorColor).Render("●")
	case ToolCanceled:
		marker = lipgloss.NewStyle().Foreground(warnColor).Render("●")
	default:
		marker = dimStyle.Render("○")
	}
	header := marker + " " + current.toolHeader(view)
	output := strings.TrimRight(view.Output, "\n")
	if output == "" {
		return header
	}
	lines := strings.Split(output, "\n")
	if !full && len(lines) > toolPreviewHead+toolPreviewTail+1 {
		hidden := len(lines) - toolPreviewHead - toolPreviewTail
		lines = append(append(lines[:toolPreviewHead:toolPreviewHead],
			fmt.Sprintf("… %d more lines (ctrl+o for full transcript)", hidden)),
			lines[len(lines)-toolPreviewTail:]...)
	}
	limit := max(10, current.width-4)
	var body strings.Builder
	for _, line := range lines {
		line = strings.ReplaceAll(line, "\t", "    ")
		if !full && lipgloss.Width(line) > limit {
			line = truncateWidth(line, limit-1) + "…"
		}
		body.WriteString("\n" + dimStyle.Render("  │ "+line))
	}
	return header + body.String()
}

func (current *renderer) toolHeader(view *ToolView) string {
	title := view.Title
	if view.Name == "Bash" {
		title = "$ " + title
	}
	name := toolNameStyle.Render(view.Name)
	available := max(10, current.width-lipgloss.Width(view.Name)-4)
	line, rest, multiline := strings.Cut(title, "\n")
	var suffix string
	if multiline {
		suffix = fmt.Sprintf(" (+%d lines)", strings.Count(rest, "\n")+1)
		available -= len(suffix)
	}
	if lipgloss.Width(line) > available {
		line = truncateWidth(line, max(1, available-1)) + "…"
	}
	return name + " " + line + dimStyle.Render(suffix)
}

func truncateWidth(text string, width int) string {
	var builder strings.Builder
	used := 0
	for _, r := range text {
		runeWidth := lipgloss.Width(string(r))
		if used+runeWidth > width {
			break
		}
		builder.WriteRune(r)
		used += runeWidth
	}
	return builder.String()
}

// formatTokens abbreviates token counts the way pi's footer does.
func formatTokens(count int64) string {
	switch {
	case count < 1_000:
		return fmt.Sprint(count)
	case count < 10_000:
		return fmt.Sprintf("%.1fk", float64(count)/1_000)
	case count < 1_000_000:
		return fmt.Sprintf("%dk", (count+500)/1_000)
	case count < 10_000_000:
		return fmt.Sprintf("%.1fM", float64(count)/1_000_000)
	default:
		return fmt.Sprintf("%dM", (count+500_000)/1_000_000)
	}
}

func formatElapsed(duration time.Duration) string {
	seconds := int(duration.Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
}

func formatAgo(when time.Time) string {
	elapsed := time.Since(when)
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(elapsed.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(elapsed.Hours()/24))
	}
}

func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~/" + rest
	}
	return path
}

func firstLine(text string, width int) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if lipgloss.Width(line) > width {
		line = truncateWidth(line, width-1) + "…"
	}
	return line
}
