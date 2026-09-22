package main

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
)

const helpText = `Commands
  /new                      start a new session
  /resume [n|id]            list recent sessions, or resume one
  /model [id]               show or switch the model
  /provider [name] [model]  show or switch the provider (` + "%s" + `)
  /thinking [level]         show or set reasoning effort (low, medium, high, xhigh, max)
  /session                  show session details
  /quit                     exit (also ctrl+d, or ctrl+c twice)

Keys
  enter send · ctrl+j / alt+enter newline · ↑/↓ message history
  esc interrupt · ctrl+c clear input / interrupt · ctrl+o full transcript · ctrl+t toggle thinking

Messages sent while the agent works are delivered right away and steer it.`

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
		return current.printRaw(dimStyle.Render(fmt.Sprintf(helpText, strings.Join(providerNames(), ", "))))

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
		return current.resume(args)

	case "/session":
		id := current.engine.SessionID()
		if id == "" {
			return info("No session yet; one is created with your first message.")
		}
		usage := current.transcript.Usage
		return info("Session %s\nFile: %s\nTokens: %s in (%s cached), %s out; last context %s",
			id, shortenHome(current.engine.SessionPath()),
			formatTokens(usage.Input), formatTokens(usage.Cached), formatTokens(usage.Output), formatTokens(usage.Context))

	case "/thinking", "/effort":
		if len(args) == 0 {
			return info("Thinking: %s (options: %s)", current.app.Settings.Thinking, strings.Join(thinkingLevels, ", "))
		}
		level := strings.ToLower(args[0])
		if !validThinking(level) {
			return fail("Unknown thinking level %q; use one of %s", args[0], strings.Join(thinkingLevels, ", "))
		}
		if err := current.engine.SetEffort(llm.ReasoningEffort(level)); err != nil {
			return fail("Set thinking: %v", err)
		}
		current.app.Settings.Thinking = level
		return tea.Batch(current.saveSettings(), info("Thinking set to %s", level))

	case "/model":
		if len(args) == 0 {
			return info("Model: %s/%s. Switch with /model <id>.", current.app.Settings.Provider, current.app.Settings.Model)
		}
		if current.busy() {
			return fail("The agent is working; press esc first, then switch models.")
		}
		current.engine.SetModel(nil, args[0])
		current.app.Settings.Model = args[0]
		return tea.Batch(current.saveSettings(), info("Model set to %s", args[0]))

	case "/provider":
		if len(args) == 0 {
			return info("Provider: %s (available: %s)", current.app.Settings.Provider, strings.Join(providerNames(), ", "))
		}
		if current.busy() {
			return fail("The agent is working; press esc first, then switch providers.")
		}
		provider, err := findProvider(args[0])
		if err != nil {
			return fail("%v", err)
		}
		modelID := provider.DefaultModel
		if len(args) > 1 {
			modelID = args[1]
		}
		if modelID == "" {
			return fail("Provider %s has no default model; use /provider %s <model>", provider.Name, provider.Name)
		}
		client, err := newClient(provider)
		if err != nil {
			return fail("%v", err)
		}
		current.engine.SetModel(client, modelID)
		current.app.Settings.Provider, current.app.Settings.Model = provider.Name, modelID
		return tea.Batch(current.saveSettings(), info("Now using %s/%s", provider.Name, modelID))
	}
	return fail("Unknown command %s; try /help", name)
}

func (current *model) resume(args []string) tea.Cmd {
	sessions, err := current.engine.Sessions()
	if err != nil {
		return current.print(Block{Kind: BlockError, Text: err.Error()})
	}
	if len(args) == 0 {
		if len(sessions) == 0 {
			return current.print(Block{Kind: BlockInfo, Text: "No sessions yet in this workspace."})
		}
		var list strings.Builder
		list.WriteString("Recent sessions (resume with /resume <n>):")
		for index, summary := range sessions[:min(15, len(sessions))] {
			fmt.Fprintf(&list, "\n  %2d. %-9s %s", index+1, formatAgo(summary.UpdatedAt), firstLine(summary.Title, max(20, current.width-20)))
		}
		return current.printRaw(dimStyle.Render(list.String()))
	}
	target, err := pickSession(sessions, args[0])
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

func (current *model) saveSettings() tea.Cmd {
	if err := saveSettings(current.app.Home, current.app.Settings); err != nil {
		return current.print(Block{Kind: BlockError, Text: "Save settings: " + err.Error()})
	}
	return nil
}
