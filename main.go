// Command unreal is an interactive terminal UI for the Unreal Agent Harness.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "unreal:", err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("unreal", flag.ContinueOnError)
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: unreal [options] [prompt]\n\nInteractive coding agent on the Unreal Agent Harness.\n\nOptions:\n")
		flags.PrintDefaults()
		fmt.Fprintf(flags.Output(), "\nProviders: %s\nSessions and settings live in ~/.unreal-tui (override with UNREAL_TUI_HOME).\n", strings.Join(providerNames(), ", "))
	}
	continueLast := flags.Bool("c", false, "continue the most recent session in this workspace")
	resumeID := flags.String("r", "", "resume the session with this ID (or unique prefix)")
	providerFlag := flags.String("provider", "", "LLM provider (default: last used, else auto-detected)")
	modelFlag := flags.String("model", "", "model ID (default: last used, else the provider default)")
	thinkingFlag := flags.String("thinking", "", "reasoning effort: low, medium, high, xhigh, max")
	workspaceFlag := flags.String("workspace", ".", "workspace directory; Bash runs here")
	if err := flags.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	workspace, err := filepath.Abs(*workspaceFlag)
	if err != nil {
		return err
	}
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		return fmt.Errorf("workspace %s is not a directory", workspace)
	}
	home, err := homeDirectory()
	if err != nil {
		return err
	}
	saved, err := loadSettings(home)
	if err != nil {
		return err
	}
	settings, provider, err := resolveSettings(saved, *providerFlag, *modelFlag, *thinkingFlag)
	if err != nil {
		return err
	}
	client, err := newClient(provider)
	if err != nil {
		return err
	}
	if settings != saved {
		if err := saveSettings(home, settings); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	skills, skillErrors := discoverSkills(workspace, home)
	var notices []Block
	for _, skillErr := range skillErrors {
		notices = append(notices, Block{Kind: BlockError, Text: "Skill: " + skillErr.Error()})
	}

	var program *tea.Program
	engine, err := NewEngine(ctx, EngineConfig{
		StoreDirectory: sessionDirectory(home, workspace),
		Workspace:      workspace,
		Client:         client,
		Model:          settings.Model,
		Effort:         llm.ReasoningEffort(settings.Thinking),
		SystemPrompt:   systemPrompt(workspace),
		Skills:         skills,
		Emit:           func(event any) { program.Send(engineEventMsg{event: event}) },
	})
	if err != nil {
		client.Close()
		return err
	}

	style := "light"
	if lipgloss.HasDarkBackground() {
		style = "dark"
	}
	app := &App{
		Engine:    engine,
		Home:      home,
		Workspace: workspace,
		Settings:  settings,
		Style:     style,
		Initial:   strings.TrimSpace(strings.Join(flags.Args(), " ")),
		Notices:   notices,
	}
	current := newModel(app)

	var resume session.ID
	switch {
	case *resumeID != "":
		sessions, err := engine.Sessions()
		if err != nil {
			return err
		}
		if resume, err = pickSession(sessions, *resumeID); err != nil {
			return err
		}
	case *continueLast:
		sessions, err := engine.Sessions()
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			notices = append(notices, Block{Kind: BlockInfo, Text: "No previous session in this workspace; starting a new one."})
			app.Notices = notices
		} else {
			resume = sessions[0].ID
		}
	}

	app.Resume = resume
	program = tea.NewProgram(current)
	_, runErr := program.Run()
	closeErr := engine.Close()
	if runErr != nil {
		return runErr
	}
	if id := engine.SessionID(); id != "" {
		fmt.Printf("\nResume this session with: unreal -r %s\n", string(id)[:8])
	}
	return closeErr
}

// resolveSettings applies flags, then environment variables, then saved
// settings, then detection.
func resolveSettings(saved Settings, providerFlag, modelFlag, thinkingFlag string) (Settings, Provider, error) {
	settings := saved
	name := firstNonEmpty(providerFlag, os.Getenv("UNREAL_HARNESS_LLM_PROVIDER"), saved.Provider)
	if name == "" {
		detected, err := detectProvider()
		if err != nil {
			return settings, Provider{}, err
		}
		name = detected
	}
	provider, err := findProvider(name)
	if err != nil {
		return settings, Provider{}, err
	}

	savedModel := ""
	if saved.Provider == provider.Name {
		savedModel = saved.Model
	}
	modelID := firstNonEmpty(modelFlag, os.Getenv("UNREAL_HARNESS_LLM_MODEL"), savedModel, provider.DefaultModel)
	if modelID == "" {
		return settings, Provider{}, fmt.Errorf("provider %s has no default model; pass -model", provider.Name)
	}

	thinking := strings.ToLower(firstNonEmpty(thinkingFlag, saved.Thinking, "high"))
	if !validThinking(thinking) {
		return settings, Provider{}, fmt.Errorf("thinking must be one of %s", strings.Join(thinkingLevels, ", "))
	}
	settings.Provider, settings.Model, settings.Thinking = provider.Name, modelID, thinking
	return settings, provider, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
