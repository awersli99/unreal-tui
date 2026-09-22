// Package tui is unreal's terminal interface, built with Bubble Tea.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/unreallabsai/unreal-agent/harness/llm"

	"github.com/awersli99/unreal-tui/internal/anthropic"
	"github.com/awersli99/unreal-tui/internal/config"
	"github.com/awersli99/unreal-tui/internal/engine"
	"github.com/awersli99/unreal-tui/internal/provider"
)

const (
	quitArmDuration = 2 * time.Second
)

type EngineEventMsg struct{ Event any }

type printedMsg struct{}

type model struct {
	app        *App
	engine     *engine.Engine
	transcript *engine.Transcript
	render     *renderer
	input      textarea.Model
	editor     editorLayout
	spinner    spinner.Model
	width      int
	height     int

	// history holds every printed block so the pager can show full output.
	history      []engine.Block
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

	// overlay, when set, is a picker shown in place of the editor.
	overlay        *overlay
	anthropicLogin *anthropic.Login
	// suggest is the slash-command autocomplete list under the editor.
	suggest suggestState
	// branch is the workspace's git branch, refreshed after each event.
	branch string
}

// NewModel is the Bubble Tea model for app.
func NewModel(app *App) tea.Model {
	return newModel(app)
}

func newModel(app *App) *model {
	input := textarea.New()
	input.Placeholder = "Message the agent… (/ for commands)"
	input.ShowLineNumbers = false
	input.CharLimit = 0
	input.MaxHeight = inputRowLimit
	input.SetHeight(1)
	input.SetPromptFunc(3, func(line int) string {
		if line == 0 {
			return " " + userMarkerStyle.Render("❯ ")
		}
		return "   "
	})
	input.FocusedStyle.CursorLine = lipgloss.NewStyle()
	input.FocusedStyle.Placeholder = dimStyle
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j", "alt+enter"))
	input.Focus()

	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(lipgloss.NewStyle().Foreground(accentColor)))

	current := &model{
		app:          app,
		engine:       app.Engine,
		transcript:   engine.NewTranscript(app.Engine.ResultTranslator),
		render:       newRenderer(app.Style(), 80),
		input:        input,
		editor:       editorLayout{probe: newProbe()},
		spinner:      spin,
		showThinking: !app.Config.Settings.HideThinkingBlock,
		branch:       gitBranch(app.Config.Workspace),
	}
	return current
}

func (current *model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, current.spinner.Tick)
}

// start runs once the terminal size is known, so the first output is
// rendered at the right width: loaded resources, then any resumed history,
// then the prompt given on the command line.
func (current *model) start() tea.Cmd {
	commands := []tea.Cmd{current.printStartup()}
	if current.app.Resume != "" {
		commands = append(commands, current.loadSession(current.app.Resume))
	}
	if current.app.Initial != "" {
		commands = append(commands, current.submit(current.app.Initial))
	}
	if current.app.OpenResume {
		commands = append(commands, current.openResumePicker())
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
		current.editInput(func() { current.input.SetWidth(message.Width) })
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

	case loginFinishedMsg:
		return current, current.handleLoginFinished(message)

	case anthropicLoginFinishedMsg:
		return current, current.handleAnthropicLoginFinished(message)

	case clipboardImagePastedMsg:
		return current, current.handleClipboardImage(message)

	case EngineEventMsg:
		current.branch = gitBranch(current.app.Config.Workspace)
		return current, current.handleEngineEvent(message.Event)

	case spinner.TickMsg:
		var command tea.Cmd
		current.spinner, command = current.spinner.Update(message)
		return current, command

	case tea.KeyMsg:
		if current.pagerOpen {
			return current, current.handlePagerKey(message)
		}
		if current.overlay != nil {
			return current, current.handleOverlayKey(message)
		}
		if command, handled := current.handleKey(message); handled {
			return current, command
		}
	}

	var command tea.Cmd
	current.editInput(func() { current.input, command = current.input.Update(message) })
	current.syncSuggestions()
	return current, command
}

// syncSuggestions recomputes the autocomplete list when the editor changes.
func (current *model) syncSuggestions() {
	value := current.input.Value()
	if value == current.suggest.input {
		return
	}
	current.suggest = suggestState{input: value, items: current.app.suggestionsFor(value)}
}

// handleSuggestKey drives the autocomplete list like pi's: ↑/↓ select, tab
// completes, enter completes and submits, esc dismisses.
func (current *model) handleSuggestKey(message tea.KeyMsg) (tea.Cmd, bool) {
	switch message.String() {
	case "up":
		current.suggest.move(-1)
	case "down":
		current.suggest.move(1)
	case "tab":
		current.setInput(current.suggest.selected().Text)
	case "enter":
		text := current.suggest.selected().Text
		current.setInput("")
		return current.submit(text), true
	case "esc":
		current.suggest.dismissed = true
	default:
		return nil, false
	}
	return nil, true
}

func (current *model) setInput(text string) {
	current.editInput(func() {
		current.input.SetValue(text)
		current.input.CursorEnd()
	})
	current.syncSuggestions()
}

func (current *model) handleKey(message tea.KeyMsg) (tea.Cmd, bool) {
	if message.String() != "ctrl+c" {
		current.quitArmed = time.Time{}
	}
	if current.suggest.visible() {
		if command, handled := current.handleSuggestKey(message); handled {
			return command, true
		}
	}
	switch message.String() {
	case "enter":
		text := current.input.Value()
		if strings.TrimSpace(text) == "" {
			return nil, true
		}
		current.setInput("")
		return current.submit(text), true

	case "esc":
		if current.busy() {
			current.interrupt()
		}
		return nil, true

	case "ctrl+c":
		switch {
		case current.input.Value() != "":
			current.setInput("")
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

	case "ctrl+v":
		return pasteClipboard(), true

	case "ctrl+l":
		current.openModelPicker("")
		return nil, true

	case "ctrl+p":
		return current.cycleModel(1), true

	case "alt+p":
		return current.cycleModel(-1), true

	case "shift+tab":
		return current.cycleThinking(), true

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
			current.setInput(current.sent[current.sentCursor])
			return nil, true
		}

	case "down":
		if !strings.Contains(current.input.Value(), "\n") && current.sentCursor < len(current.sent) {
			current.sentCursor++
			if current.sentCursor == len(current.sent) {
				current.setInput(current.draft)
			} else {
				current.setInput(current.sent[current.sentCursor])
			}
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
		return current.print(engine.Block{Kind: engine.BlockError, Text: err.Error()})
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
	case engine.ItemEvent:
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

	case engine.RunEndedEvent:
		// A message queued while the run was stopping has already started the
		// next run; keep showing it as busy so esc can interrupt it.
		current.awaiting = current.engine.Running()
		if current.notice == "Interrupting…" {
			current.notice = ""
		}
		blocks := current.transcript.Abort()
		if event.Err != nil && !errors.Is(event.Err, context.Canceled) {
			blocks = append(blocks,
				engine.Block{Kind: engine.BlockError, Text: event.Err.Error()},
				engine.Block{Kind: engine.BlockInfo, Text: "Send a message to retry; the session resumes where it stopped."})
		}
		return current.print(blocks...)
	}
	return nil
}

// print renders blocks into the terminal scrollback above the live area.
func (current *model) print(blocks ...engine.Block) tea.Cmd {
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

// printStartup lists the loaded context files and skills the way pi does
// (hidden by quietStartup), plus any configuration problems. Everything else
// pi shows lives in the footer under the editor.
func (current *model) printStartup() tea.Cmd {
	app := current.app
	var sections []string
	if !app.Config.Settings.QuietStartup {
		sections = current.loadedResources()
	}
	var command tea.Cmd
	if len(sections) != 0 {
		command = current.printRaw(strings.Join(sections, "\n\n"))
	}
	notices := slices.Concat(app.Notices, current.configProblems())
	app.Notices = nil
	if len(notices) != 0 {
		command = tea.Batch(command, current.print(notices...))
	}
	return command
}

// loadedResources renders pi's [Context] and [Skills] sections.
func (current *model) loadedResources() []string {
	heading := lipgloss.NewStyle().Foreground(accentColor).Bold(true)
	var sections []string
	if files := current.app.LoadedFiles; len(files) != 0 {
		names := make([]string, 0, len(files))
		for _, path := range files {
			names = append(names, shortenHome(path))
		}
		sections = append(sections, heading.Render("[Context]")+"\n"+dimStyle.Render(current.wrapList(names)))
	}
	if skills := current.app.Skills; len(skills) != 0 {
		sections = append(sections, heading.Render("[Skills]")+"\n"+dimStyle.Render(current.wrapList(skills)))
	}
	return sections
}

func (current *model) wrapList(items []string) string {
	return lipgloss.NewStyle().Width(max(20, current.width-2)).PaddingLeft(2).Render(strings.Join(items, ", "))
}

func (current *model) configProblems() []engine.Block {
	var blocks []engine.Block
	for _, warning := range current.app.Config.Warnings {
		blocks = append(blocks, engine.Block{Kind: engine.BlockError, Text: "Settings: " + warning})
	}
	for _, warning := range current.app.Catalog.Warnings {
		blocks = append(blocks, engine.Block{Kind: engine.BlockError, Text: "Models: " + warning})
	}
	return blocks
}

// reload re-reads settings, models.json, the system prompt files and skills,
// and applies them without restarting the session.
func (current *model) reload() tea.Cmd {
	app := current.app
	loaded, err := config.Load(app.Config.Home, app.Config.Workspace)
	if err != nil {
		return current.print(engine.Block{Kind: engine.BlockError, Text: "Reload: " + err.Error()})
	}
	app.Config = loaded
	app.Scoped = loaded.Settings.EnabledModels
	return current.reloadPrompt(current.refreshProviders())
}

// refreshProviders reloads models.json and stored credentials, then rebuilds
// the current model's client so changed keys apply to the next request.
func (current *model) refreshProviders() []engine.Block {
	app := current.app
	app.Catalog = provider.LoadCatalog(app.Config.Home)
	app.retireClients()

	var blocks []engine.Block
	info := app.Catalog.Lookup(app.Model.Provider, app.Model.ID)
	if client, err := app.ClientFor(info.Provider); err != nil {
		current.engine.SetModel(provider.UnavailableClient{Err: err}, info.ID)
		blocks = append(blocks, engine.Block{Kind: engine.BlockError, Text: fmt.Sprintf("%s is unavailable; use /login or /model: %v", info.Provider, err)})
	} else {
		current.engine.SetModel(client, info.ID)
		app.Model = info
	}
	if clamped := app.Model.ClampLevel(app.Thinking); clamped != app.Thinking {
		if err := current.engine.SetEffort(llm.ReasoningEffort(clamped)); err == nil {
			app.Thinking = clamped
		}
	}
	return blocks
}

// reloadPrompt is the second half of /reload: prompt files and skills.
func (current *model) reloadPrompt(blocks []engine.Block) tea.Cmd {
	app, loaded := current.app, current.app.Config
	prompt, files := config.SystemPrompt(app.Prompt)
	current.engine.SetSystemPrompt(prompt)
	app.LoadedFiles = files
	skills, skillErrors := config.DiscoverSkills(loaded.Workspace, loaded.Home)
	current.engine.SetSkills(skills)
	app.Skills = config.SkillNames(skills)
	for _, skillErr := range skillErrors {
		blocks = append(blocks, engine.Block{Kind: engine.BlockError, Text: "Skill: " + skillErr.Error()})
	}
	blocks = append(blocks, current.configProblems()...)
	current.applyDisplaySettings()
	blocks = append(blocks, engine.Block{Kind: engine.BlockInfo, Text: fmt.Sprintf("Reloaded settings, %d models, the system prompt and skills", len(app.Catalog.Models))})
	command := current.print(blocks...)
	if sections := current.loadedResources(); len(sections) != 0 {
		command = tea.Sequence(command, current.printRaw(strings.Join(sections, "\n\n")))
	}
	return command
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
		sections = append(sections, current.render.toolHeader(view, current.spinner.View(), elapsed, false))
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

	// The rules take the thinking level's colour, as in pi.
	style := ruleStyle
	if color, ok := thinkingColors[current.app.Thinking]; ok {
		style = lipgloss.NewStyle().Foreground(color)
	}
	if current.overlay != nil {
		rule := style.Render(strings.Repeat("─", max(1, current.width)))
		sections = append(sections, rule, current.overlay.View(current.width), rule)
	} else {
		sections = append(sections, current.inputView(func(line string) string { return style.Render(line) }))
	}
	if current.overlay == nil && current.suggest.visible() {
		sections = append(sections, current.suggest.view(current.width))
	}
	sections = append(sections, current.footer())
	// A blank line always separates the live area from the scrollback, so it
	// does not shift when pickers or suggestions open.
	return "\n" + strings.Join(sections, "\n")
}

// footer mirrors pi's: the directory, git branch and session on the first
// line; token usage and context fill on the left of the second, with the
// model and thinking level on the right.
func (current *model) footer() string {
	// Inset the footer from both edges of the terminal; some terminals clip
	// the last column.
	const leftMargin, rightMargin = 1, 1
	width := max(1, current.width-leftMargin-rightMargin)
	location := shortenHome(current.app.Config.Workspace)
	if current.branch != "" {
		location += " (" + current.branch + ")"
	}
	location += " • " + current.sessionLabel()
	if lipgloss.Width(location) > width {
		location = truncateWidth(location, max(1, width-3)) + "..."
	}

	usage := current.transcript.Usage
	var stats []string
	if usage.Input != 0 {
		stats = append(stats, "↑"+formatTokens(usage.Input))
	}
	if usage.Output != 0 {
		stats = append(stats, "↓"+formatTokens(usage.Output))
	}
	if usage.Cached != 0 {
		stats = append(stats, "R"+formatTokens(usage.Cached))
	}
	if rate, ok := usage.CacheHitRate(); ok {
		stats = append(stats, fmt.Sprintf("CH%.1f%%", rate))
	}
	parts := make([]string, 0, len(stats)+1)
	for _, stat := range stats {
		parts = append(parts, dimStyle.Render(stat))
	}
	if window := current.app.Model.ContextWindow; window > 0 {
		percent := float64(usage.Context) / float64(window) * 100
		style := dimStyle
		switch {
		case percent > 90:
			style = errorStyle
		case percent > 70:
			style = lipgloss.NewStyle().Foreground(warnColor)
		}
		parts = append(parts, style.Render(fmt.Sprintf("%.1f%%/%s", percent, formatTokens(window))))
	} else if usage.Context != 0 {
		parts = append(parts, dimStyle.Render("ctx "+formatTokens(usage.Context)))
	}
	left := strings.Join(parts, dimStyle.Render(" "))

	right := footerModel(current.app.Model.Provider, current.app.Model.ID, current.app.Thinking, width-lipgloss.Width(left)-2)
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	indent := strings.Repeat(" ", leftMargin)
	return indent + dimStyle.Render(location) + "\n" + indent + left + dimStyle.Render(strings.Repeat(" ", gap)+right)
}

// footerModel formats the right side of the footer's second line within room
// columns. The model ID and thinking level are kept whole where possible: the
// provider prefix is shortened with an ellipsis and then dropped first. Only
// when "model • level" alone does not fit is the model ID shortened, and the
// thinking level is cut last.
func footerModel(providerName, id, thinking string, room int) string {
	room = max(0, room)
	suffix := ""
	if thinking != "" {
		suffix = " • " + thinking
	}
	tail := id + suffix
	if providerName != "" {
		if full := providerName + "/" + tail; lipgloss.Width(full) <= room {
			return full
		}
		if available := room - lipgloss.Width(tail) - 2; available > 0 {
			return truncateWidth(providerName, available) + "…/" + tail
		}
	}
	if lipgloss.Width(tail) <= room {
		return tail
	}
	if available := room - lipgloss.Width(suffix) - 1; available >= 4 {
		return truncateWidth(id, available) + "…" + suffix
	}
	if thinking != "" && lipgloss.Width(thinking) <= room {
		return thinking
	}
	return truncateWidth(config.FirstNonEmpty(thinking, id), room)
}

func (current *model) sessionLabel() string {
	id := current.engine.SessionID()
	if id == "" {
		return "new session"
	}
	return engine.ShortID(id)
}

// gitBranch reads the branch of the repository containing directory from
// .git/HEAD (following worktree .git files), like pi's footer. A detached
// HEAD shows as "detached".
func gitBranch(directory string) string {
	for {
		gitPath := filepath.Join(directory, ".git")
		if info, err := os.Stat(gitPath); err == nil {
			gitDirectory := gitPath
			if !info.IsDir() {
				contents, err := os.ReadFile(gitPath)
				if err != nil {
					return ""
				}
				target, ok := strings.CutPrefix(strings.TrimSpace(string(contents)), "gitdir: ")
				if !ok {
					return ""
				}
				if !filepath.IsAbs(target) {
					target = filepath.Join(directory, target)
				}
				gitDirectory = target
			}
			head, err := os.ReadFile(filepath.Join(gitDirectory, "HEAD"))
			if err != nil {
				return ""
			}
			if branch, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/"); ok {
				return branch
			}
			return "detached"
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return ""
		}
		directory = parent
	}
}
