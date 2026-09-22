package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/unreallabsai/unreal-agent/harness/session"
)

const (
	maxInputHeight  = 10
	quitArmDuration = 2 * time.Second
)

// App holds what the TUI needs besides the engine: settings and paths.
type App struct {
	Engine    *Engine
	Home      string
	Workspace string
	Settings  Settings
	Style     string
	Initial   string
	Resume    session.ID
	Notices   []Block
}

type engineEventMsg struct{ event any }

type printedMsg struct{}

type model struct {
	app        *App
	engine     *Engine
	transcript *Transcript
	render     *renderer
	input      textarea.Model
	spinner    spinner.Model
	width      int
	height     int

	// history holds every printed block so the pager can show full output.
	history      []Block
	showThinking bool

	// Printing is serialized: commands run concurrently, so separate Println
	// commands could reorder. One flush runs at a time.
	printQueue []string
	printing   bool

	pagerOpen bool
	pager     viewport.Model

	sent       []string
	sentCursor int
	draft      string

	awaiting  bool
	busySince time.Time
	quitArmed time.Time
	notice    string
}

func newModel(app *App) *model {
	input := textarea.New()
	input.Placeholder = "Message the agent… (/help for commands)"
	input.ShowLineNumbers = false
	input.CharLimit = 0
	input.MaxHeight = 1000
	input.SetHeight(1)
	input.SetPromptFunc(2, func(line int) string {
		if line == 0 {
			return userMarkerStyle.Render("❯ ")
		}
		return "  "
	})
	input.FocusedStyle.CursorLine = lipgloss.NewStyle()
	input.FocusedStyle.Placeholder = dimStyle
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j", "alt+enter"))
	input.Focus()

	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(lipgloss.NewStyle().Foreground(accentColor)))

	current := &model{
		app:          app,
		engine:       app.Engine,
		transcript:   NewTranscript(app.Engine.ResultTranslator),
		render:       newRenderer(app.Style, 80),
		input:        input,
		spinner:      spin,
		showThinking: true,
	}
	return current
}

func (current *model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, current.spinner.Tick)
}

// start runs once the terminal size is known, so the first output is
// rendered at the right width: banner, then any resumed history, then the
// prompt given on the command line.
func (current *model) start() tea.Cmd {
	commands := []tea.Cmd{current.printBanner()}
	if current.app.Resume != "" {
		commands = append(commands, current.loadSession(current.app.Resume))
	}
	if current.app.Initial != "" {
		commands = append(commands, current.submit(current.app.Initial))
	}
	return tea.Batch(commands...)
}

func (current *model) busy() bool {
	return current.awaiting || current.transcript.Busy()
}

func (current *model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		first := current.width == 0
		current.width, current.height = message.Width, message.Height
		current.render.resize(message.Width)
		current.input.SetWidth(message.Width)
		current.pager.Width, current.pager.Height = message.Width, max(1, message.Height-1)
		if first {
			return current, current.start()
		}
		if current.pagerOpen {
			current.pager.SetContent(current.pagerContent())
		}
		return current, nil

	case printedMsg:
		current.printing = false
		return current, current.flush()

	case engineEventMsg:
		return current, current.handleEngineEvent(message.event)

	case spinner.TickMsg:
		var command tea.Cmd
		current.spinner, command = current.spinner.Update(message)
		return current, command

	case tea.KeyMsg:
		if current.pagerOpen {
			return current, current.handlePagerKey(message)
		}
		if command, handled := current.handleKey(message); handled {
			return current, command
		}
	}

	var command tea.Cmd
	current.input, command = current.input.Update(message)
	current.resizeInput()
	return current, command
}

func (current *model) handleKey(message tea.KeyMsg) (tea.Cmd, bool) {
	if message.String() != "ctrl+c" {
		current.quitArmed = time.Time{}
	}
	switch message.String() {
	case "enter":
		text := current.input.Value()
		if strings.TrimSpace(text) == "" {
			return nil, true
		}
		current.input.Reset()
		current.resizeInput()
		return current.submit(text), true

	case "esc":
		if current.busy() {
			current.interrupt()
		}
		return nil, true

	case "ctrl+c":
		switch {
		case current.input.Value() != "":
			current.input.Reset()
			current.resizeInput()
		case current.busy():
			current.interrupt()
		case !current.quitArmed.IsZero() && time.Since(current.quitArmed) < quitArmDuration:
			return tea.Quit, true
		default:
			current.quitArmed = time.Now()
			current.notice = "Press ctrl+c again to exit"
		}
		return nil, true

	case "ctrl+d":
		if current.input.Value() == "" {
			return tea.Quit, true
		}

	case "ctrl+o":
		return current.openPager(), true

	case "ctrl+t":
		current.showThinking = !current.showThinking
		if current.showThinking {
			current.notice = "Showing thinking summaries"
		} else {
			current.notice = "Hiding thinking summaries"
		}
		return nil, true

	case "up":
		if !strings.Contains(current.input.Value(), "\n") && len(current.sent) != 0 {
			if current.sentCursor == len(current.sent) {
				current.draft = current.input.Value()
			}
			current.sentCursor = max(0, current.sentCursor-1)
			current.input.SetValue(current.sent[current.sentCursor])
			current.resizeInput()
			return nil, true
		}

	case "down":
		if !strings.Contains(current.input.Value(), "\n") && current.sentCursor < len(current.sent) {
			current.sentCursor++
			if current.sentCursor == len(current.sent) {
				current.input.SetValue(current.draft)
			} else {
				current.input.SetValue(current.sent[current.sentCursor])
			}
			current.resizeInput()
			return nil, true
		}
	}
	return nil, false
}

func (current *model) submit(text string) tea.Cmd {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if len(current.sent) == 0 || current.sent[len(current.sent)-1] != text {
		current.sent = append(current.sent, text)
	}
	current.sentCursor = len(current.sent)
	current.draft = ""
	current.notice = ""

	if strings.HasPrefix(text, "/") {
		return current.runCommand(text)
	}
	wasBusy := current.busy()
	if err := current.engine.Send(text); err != nil {
		return current.print(Block{Kind: BlockError, Text: err.Error()})
	}
	if !wasBusy {
		current.awaiting = true
		current.busySince = time.Now()
	}
	return nil
}

func (current *model) interrupt() {
	if current.engine.Interrupt() {
		current.notice = "Interrupting…"
	}
}

func (current *model) handleEngineEvent(event any) tea.Cmd {
	switch event := event.(type) {
	case ItemEvent:
		if event.SessionID != current.engine.SessionID() {
			return nil // From a session we've since left.
		}
		wasBusy := current.busy()
		blocks := current.transcript.Apply(event.Item)
		if current.transcript.Thinking() {
			current.awaiting = false
		}
		if !wasBusy && current.busy() {
			current.busySince = time.Now()
		}
		if current.notice == "Interrupting…" && !current.busy() {
			current.notice = ""
		}
		return current.print(blocks...)

	case RunEndedEvent:
		current.awaiting = false
		if current.notice == "Interrupting…" {
			current.notice = ""
		}
		blocks := current.transcript.Abort()
		if event.Err != nil && !errors.Is(event.Err, context.Canceled) {
			blocks = append(blocks, Block{Kind: BlockError, Text: event.Err.Error()})
			blocks = append(blocks, Block{Kind: BlockInfo, Text: "Send a message to retry; the session resumes where it stopped."})
		}
		return current.print(blocks...)
	}
	return nil
}

// print renders blocks into the terminal scrollback above the live area.
func (current *model) print(blocks ...Block) tea.Cmd {
	for _, block := range blocks {
		current.history = append(current.history, block)
		if rendered := current.render.block(block, current.showThinking, false); rendered != "" {
			current.printQueue = append(current.printQueue, rendered)
		}
	}
	return current.flush()
}

func (current *model) printRaw(text string) tea.Cmd {
	current.printQueue = append(current.printQueue, text)
	return current.flush()
}

func (current *model) flush() tea.Cmd {
	if current.printing || current.pagerOpen || len(current.printQueue) == 0 {
		return nil
	}
	// A leading empty line separates blocks.
	text := "\n" + strings.Join(current.printQueue, "\n\n")
	current.printQueue = nil
	current.printing = true
	return tea.Sequence(tea.Println(text), func() tea.Msg { return printedMsg{} })
}

func (current *model) printBanner() tea.Cmd {
	title := lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render("unreal") +
		dimStyle.Render(" · agent TUI on Unreal Agent Harness")
	lines := []string{
		title,
		dimStyle.Render(fmt.Sprintf("%s · %s/%s · thinking %s",
			shortenHome(current.app.Workspace), current.app.Settings.Provider, current.app.Settings.Model, current.app.Settings.Thinking)),
		dimStyle.Render("enter send · ctrl+j newline · esc interrupt · ctrl+o transcript · /help"),
	}
	command := current.printRaw(strings.Join(lines, "\n"))
	if len(current.app.Notices) != 0 {
		command = tea.Batch(command, current.print(current.app.Notices...))
		current.app.Notices = nil
	}
	return command
}

func (current *model) resizeInput() {
	current.input.SetHeight(min(maxInputHeight, max(1, current.input.LineCount())))
}

func (current *model) openPager() tea.Cmd {
	current.pagerOpen = true
	current.pager = viewport.New(current.width, max(1, current.height-1))
	current.pager.SetContent(current.pagerContent())
	current.pager.GotoBottom()
	return tea.EnterAltScreen
}

func (current *model) handlePagerKey(message tea.KeyMsg) tea.Cmd {
	switch message.String() {
	case "esc", "q", "ctrl+o", "ctrl+c":
		current.pagerOpen = false
		return tea.Sequence(tea.ExitAltScreen, current.flush())
	case "g", "home":
		current.pager.GotoTop()
		return nil
	case "G", "end":
		current.pager.GotoBottom()
		return nil
	}
	var command tea.Cmd
	current.pager, command = current.pager.Update(message)
	return command
}

func (current *model) pagerContent() string {
	var parts []string
	for _, block := range current.history {
		if rendered := current.render.block(block, true, true); rendered != "" {
			parts = append(parts, rendered)
		}
	}
	if len(parts) == 0 {
		return dimStyle.Render("Nothing here yet.")
	}
	return strings.Join(parts, "\n\n")
}

func (current *model) View() string {
	if current.pagerOpen {
		status := dimStyle.Render(fmt.Sprintf(" transcript · %3.0f%% · ↑/↓ pgup/pgdn g/G scroll · esc close", current.pager.ScrollPercent()*100))
		return current.pager.View() + "\n" + status
	}
	if current.width == 0 {
		return ""
	}

	var sections []string
	for _, view := range current.transcript.Running() {
		elapsed := ""
		if !view.Started.IsZero() {
			elapsed = dimStyle.Render(" " + formatElapsed(time.Since(view.Started)))
		}
		sections = append(sections, current.spinner.View()+" "+current.render.toolHeader(view)+elapsed)
	}
	if current.busy() {
		label := "Working"
		switch {
		case current.transcript.Thinking():
			label = "Thinking"
		case len(current.transcript.Running()) != 0:
			label = "Running tools"
		}
		status := current.spinner.View() + " " + lipgloss.NewStyle().Foreground(accentColor).Render(label+"…") +
			dimStyle.Render(fmt.Sprintf(" %s · esc to interrupt · messages you send now steer the agent", formatElapsed(time.Since(current.busySince))))
		sections = append(sections, status)
	}
	if current.notice != "" {
		sections = append(sections, dimStyle.Render(current.notice))
	}

	rule := ruleStyle.Render(strings.Repeat("─", max(1, current.width)))
	sections = append(sections, rule, current.input.View(), rule, current.footer())
	if len(sections) > 3 {
		sections[0] = "\n" + sections[0]
	}
	return strings.Join(sections, "\n")
}

func (current *model) footer() string {
	settings := current.app.Settings
	left := fmt.Sprintf("%s · %s · %s/%s · thinking %s", shortenHome(current.app.Workspace), current.sessionLabel(), settings.Provider, settings.Model, settings.Thinking)
	usage := current.transcript.Usage
	right := ""
	if usage.Input != 0 {
		right = fmt.Sprintf("ctx %s · ↑%s ↓%s", formatTokens(usage.Context), formatTokens(usage.Input), formatTokens(usage.Output))
	}
	gap := current.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 2 {
		left = truncateWidth(left, max(10, current.width-lipgloss.Width(right)-3)) + "…"
		gap = max(1, current.width-lipgloss.Width(left)-lipgloss.Width(right))
	}
	return dimStyle.Render(left + strings.Repeat(" ", gap) + right)
}

func (current *model) sessionLabel() string {
	id := string(current.engine.SessionID())
	if id == "" {
		return "new session"
	}
	return "session " + id[:min(8, len(id))]
}
