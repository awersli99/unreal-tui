package main

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/unreallabsai/unreal-agent/harness/session"
)

const keysHelp = `Keys
  enter send · ctrl+j / alt+enter newline · ↑/↓ message history · / commands
  esc interrupt · ctrl+c clear input / interrupt · ctrl+o full transcript
  ctrl+l model picker · ctrl+p / alt+p next / previous model · shift+tab cycle thinking
  ctrl+t toggle thinking blocks
  In pickers: type to filter · ↑/↓ move · enter select · esc cancel

Model and thinking changes are remembered as the defaults for the next start.
Settings are saved to %s.
Messages sent while the agent works are delivered right away and steer it.`

func helpText(settingsPath string) string {
	var help strings.Builder
	help.WriteString("Commands\n")
	for _, command := range commands {
		usage := strings.TrimSpace("/" + command.Name + " " + command.Args)
		fmt.Fprintf(&help, "  %-24s %s\n", usage, command.Summary)
	}
	help.WriteString("\n" + fmt.Sprintf(keysHelp, settingsPath))
	return help.String()
}

func (current *model) runCommand(text string) tea.Cmd {
	fields := strings.Fields(text)
	name, args := fields[0], fields[1:]
	info := func(format string, values ...any) tea.Cmd {
		return current.print(Block{Kind: BlockInfo, Text: fmt.Sprintf(format, values...)})
	}
	fail := func(format string, values ...any) tea.Cmd {
		return current.print(Block{Kind: BlockError, Text: fmt.Sprintf(format, values...)})
	}

	switch name {
	case "/help", "/?":
		return current.printRaw(dimStyle.Render(helpText(shortenHome(globalSettingsPath(current.app.Config.Home)))))

	case "/quit", "/exit", "/q":
		return tea.Quit

	case "/new", "/clear":
		if current.busy() {
			current.engine.Interrupt()
		}
		current.engine.NewSession()
		current.resetTranscript()
		return info("New session")

	case "/resume", "/sessions":
		if len(args) == 0 {
			return current.openResumePicker()
		}
		return current.resume(args[0])

	case "/session":
		id := current.engine.SessionID()
		if id == "" {
			return info("No session yet; one is created with your first message.")
		}
		usage := current.transcript.Usage
		return info("Session %s\nFile: %s\nModel: %s · thinking %s\nTokens: %s in (%s cached), %s out; last context %s",
			id, shortenHome(current.engine.SessionPath()), current.app.Model.Key(), current.app.Thinking,
			formatTokens(usage.Input), formatTokens(usage.Cached), formatTokens(usage.Output), formatTokens(usage.Context))

	case "/thinking", "/effort":
		if len(args) == 0 {
			current.openThinkingPicker()
			return nil
		}
		level := strings.ToLower(args[0])
		if !validThinking(level) {
			return fail("Unknown thinking level %q; use one of %s", args[0], strings.Join(thinkingLevels, ", "))
		}
		if clamped := current.app.Model.ClampLevel(level); clamped != level {
			return fail("%s does not support %s thinking; it supports %s", current.app.Model.Key(), level, strings.Join(current.app.Model.SupportedLevels(), ", "))
		}
		return current.setThinking(level)

	case "/model", "/models":
		if len(args) == 0 {
			current.openModelPicker("")
			return nil
		}
		pattern := strings.Join(args, " ")
		matches, level := current.app.Catalog.Match(pattern)
		if len(matches) == 1 {
			return current.switchModel(matches[0], level)
		}
		if len(matches) == 0 {
			// Accept model IDs the catalog does not list, as pi does.
			if model, level, err := current.app.Catalog.Resolve(pattern, current.app.Model.Provider); err == nil {
				return current.switchModel(model, level)
			}
		}
		query, _ := splitLevel(pattern)
		current.openModelPicker(strings.NewReplacer("*", " ", "?", " ").Replace(query))
		return nil

	case "/scoped-models", "/scoped":
		current.openScopedPicker()
		return nil

	case "/settings", "/config":
		current.openSettingsPicker()
		return nil

	case "/reload":
		return current.reload()

	case "/login":
		return current.openLogin(strings.Join(args, " "))

	case "/logout":
		return current.openLogout(strings.Join(args, " "))
	}
	return fail("Unknown command %s; try /help", name)
}

func (current *model) resume(selector string) tea.Cmd {
	sessions, err := current.engine.Sessions()
	if err != nil {
		return current.print(Block{Kind: BlockError, Text: err.Error()})
	}
	target, err := pickSession(sessions, selector)
	if err != nil {
		return current.print(Block{Kind: BlockError, Text: err.Error()})
	}
	if current.busy() {
		current.engine.Interrupt()
	}
	return current.loadSession(target)
}

func (current *model) loadSession(id session.ID) tea.Cmd {
	items, err := current.engine.ResumeSession(id)
	if err != nil {
		return current.print(Block{Kind: BlockError, Text: err.Error()})
	}
	current.resetTranscript()
	blocks := []Block{{Kind: BlockInfo, Text: fmt.Sprintf("Resumed session %s", id)}}
	for _, item := range items {
		blocks = append(blocks, current.transcript.Apply(item)...)
	}
	// Anything still marked in flight belongs to a coordinator that is gone;
	// the next message resumes it.
	blocks = append(blocks, current.transcript.Abort()...)
	return current.print(blocks...)
}

func pickSession(sessions []SessionSummary, selector string) (session.ID, error) {
	if number, err := strconv.Atoi(selector); err == nil {
		if number < 1 || number > len(sessions) {
			return "", fmt.Errorf("no session numbered %d", number)
		}
		return sessions[number-1].ID, nil
	}
	var match session.ID
	for _, summary := range sessions {
		if strings.HasPrefix(string(summary.ID), selector) {
			if match != "" {
				return "", fmt.Errorf("session prefix %q is ambiguous", selector)
			}
			match = summary.ID
		}
	}
	if match == "" {
		return "", fmt.Errorf("no session matches %q", selector)
	}
	return match, nil
}

func (current *model) resetTranscript() {
	current.transcript = NewTranscript(current.engine.ResultTranslator)
	current.awaiting = false
}
