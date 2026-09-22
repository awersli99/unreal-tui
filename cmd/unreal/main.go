// Command unreal is an interactive terminal coding agent built on the Unreal
// Agent Harness.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"

	"github.com/awersli99/unreal-tui/internal/config"
	"github.com/awersli99/unreal-tui/internal/engine"
	"github.com/awersli99/unreal-tui/internal/provider"
	"github.com/awersli99/unreal-tui/internal/tui"
)

const defaultThinking = "high"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "unreal:", err)
		os.Exit(1)
	}
}

type options struct {
	provider, model, thinking, models string
	listModels                        bool
	continueLast, resumePicker        bool
	session, sessionDirectory         string
	noContextFiles                    bool
	workspace                         string
}

func parseFlags(arguments []string) (options, []string, error) {
	var parsed options
	flags := flag.NewFlagSet("unreal", flag.ContinueOnError)
	flags.Usage = func() {
		output := flags.Output()
		fmt.Fprint(output, `Usage: unreal [options] [prompt]

Interactive coding agent on the Unreal Agent Harness. Flags go before the prompt.

Options:
  --provider <name>       provider (openai-codex, openai, anthropic, openrouter, fireworks, ollama, or one from models.json)
  --model <pattern>       model: provider/id, id, or a pattern; ":<level>" sets thinking, e.g. gpt-6-astra:max
  --thinking <level>      thinking level: low, medium, high, xhigh, max
  --models <patterns>     comma-separated models for ctrl+p cycling this session, e.g. "gpt-6*,openrouter/*"
  --list-models [search]  list available models and exit
  -c, --continue          continue the most recent session in this workspace
  -r, --resume            pick a session to resume
  --session <id>          resume the session with this ID or unique prefix
  --session-dir <dir>     directory for session files
  -nc, --no-context-files skip AGENTS.md / CLAUDE.md context files
  --workspace <dir>       workspace directory; Bash runs here (default: current directory)

Configuration lives in ~/.unreal-tui (override with UNREAL_TUI_HOME): settings.json,
models.json, SYSTEM.md, APPEND_SYSTEM.md, AGENTS.md and skills/. A project's
.unreal/settings.json overrides the global settings.
`)
	}
	flags.StringVar(&parsed.provider, "provider", "", "")
	flags.StringVar(&parsed.model, "model", "", "")
	flags.StringVar(&parsed.thinking, "thinking", "", "")
	flags.StringVar(&parsed.models, "models", "", "")
	flags.BoolVar(&parsed.listModels, "list-models", false, "")
	flags.BoolVar(&parsed.continueLast, "c", false, "")
	flags.BoolVar(&parsed.continueLast, "continue", false, "")
	flags.BoolVar(&parsed.resumePicker, "r", false, "")
	flags.BoolVar(&parsed.resumePicker, "resume", false, "")
	flags.StringVar(&parsed.session, "session", "", "")
	flags.StringVar(&parsed.sessionDirectory, "session-dir", "", "")
	flags.BoolVar(&parsed.noContextFiles, "nc", false, "")
	flags.BoolVar(&parsed.noContextFiles, "no-context-files", false, "")
	flags.StringVar(&parsed.workspace, "workspace", ".", "")
	if err := flags.Parse(arguments); err != nil {
		return parsed, nil, err
	}
	if parsed.thinking != "" {
		parsed.thinking = strings.ToLower(parsed.thinking)
		if !config.ValidThinking(parsed.thinking) {
			return parsed, nil, fmt.Errorf("--thinking must be one of %s", strings.Join(config.ThinkingLevels, ", "))
		}
	}
	return parsed, flags.Args(), nil
}

func run() error {
	flagValues, rest, err := parseFlags(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}

	workspace, err := filepath.Abs(flagValues.workspace)
	if err != nil {
		return err
	}
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		return fmt.Errorf("workspace %s is not a directory", workspace)
	}
	home, err := config.HomeDirectory()
	if err != nil {
		return err
	}
	loaded, err := config.Load(home, workspace)
	if err != nil {
		return err
	}
	catalog := provider.LoadCatalog(home)

	if flagValues.listModels {
		return listModels(catalog, strings.Join(rest, " "))
	}

	info, level, err := provider.StartupModel(catalog, loaded.Settings, provider.ModelFlags{Provider: flagValues.provider, Model: flagValues.model})
	if err != nil {
		return err
	}
	app := &tui.App{
		Config:  loaded,
		Catalog: catalog,
		Model:   info,
		Scoped:  loaded.Settings.EnabledModels,
		Initial: strings.TrimSpace(strings.Join(rest, " ")),
	}
	if flagValues.models != "" {
		app.Scoped = nil
		for _, pattern := range strings.Split(flagValues.models, ",") {
			if pattern = strings.TrimSpace(pattern); pattern != "" {
				app.Scoped = append(app.Scoped, pattern)
			}
		}
	}
	app.Thinking = info.ClampLevel(config.FirstNonEmpty(flagValues.thinking, level,
		loaded.Settings.ModelThinkingLevels[info.Key()], loaded.Settings.DefaultThinkingLevel, info.DefaultLevel, defaultThinking))
	client, err := app.ClientFor(info.Provider)
	if err != nil {
		client = provider.UnavailableClient{Err: err}
		app.Notices = append(app.Notices, engine.Block{Kind: engine.BlockInfo, Text: "Use /login to configure a provider. " + err.Error()})
	}
	defer app.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	shell := config.ResolveShell(loaded.Settings.ShellPath)
	app.Prompt = config.PromptOptions{Workspace: workspace, Home: home, Shell: shell, ContextFiles: !flagValues.noContextFiles}
	prompt, files := config.SystemPrompt(app.Prompt)
	app.LoadedFiles = files
	skills, skillErrors := config.DiscoverSkills(workspace, home)
	app.Skills = config.SkillNames(skills)
	for _, skillErr := range skillErrors {
		app.Notices = append(app.Notices, engine.Block{Kind: engine.BlockError, Text: "Skill: " + skillErr.Error()})
	}

	var program *tea.Program
	agent, err := engine.New(ctx, engine.Config{
		StoreDirectory: config.SessionDirectory(home, workspace, config.FirstNonEmpty(flagValues.sessionDirectory, loaded.Settings.SessionDir)),
		Workspace:      workspace,
		Client:         client,
		Model:          info.ID,
		Effort:         llm.ReasoningEffort(app.Thinking),
		SystemPrompt:   prompt,
		Skills:         skills,
		Shell:          shell,
		Emit:           func(event any) { program.Send(tui.EngineEventMsg{Event: event}) },
	})
	if err != nil {
		return err
	}
	app.Engine = agent

	var resume session.ID
	switch {
	case flagValues.session != "":
		sessions, err := agent.Sessions()
		if err != nil {
			return err
		}
		if resume, err = engine.PickSession(sessions, flagValues.session); err != nil {
			return err
		}
	case flagValues.continueLast:
		sessions, err := agent.Sessions()
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			app.Notices = append(app.Notices, engine.Block{Kind: engine.BlockInfo, Text: "No previous session in this workspace; starting a new one."})
		} else {
			resume = sessions[0].ID
		}
	case flagValues.resumePicker:
		app.OpenResume = true
	}
	app.Resume = resume

	// Query the terminal before Bubble Tea takes it over.
	app.DarkBackground = lipgloss.HasDarkBackground()
	program = tea.NewProgram(tui.NewModel(app))
	_, runErr := program.Run()
	agent.Close()
	if runErr != nil {
		return runErr
	}
	if id := agent.SessionID(); id != "" {
		fmt.Printf("\nResume this session with: unreal --session %s\n", engine.ShortID(id))
	}
	return nil
}

func listModels(catalog *provider.Catalog, search string) error {
	for _, warning := range catalog.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", warning)
	}
	var models []provider.ModelInfo
	for _, model := range catalog.Models {
		if provider.FuzzyMatch(model.Key()+" "+model.Name, search) {
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		fmt.Println("No models found. Start unreal and use /login, set an API key, or configure models.json.")
		return nil
	}
	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "provider\tmodel\tname\tthinking")
	for _, model := range models {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", model.Provider, model.ID, config.FirstNonEmpty(model.Name, "-"), strings.Join(model.SupportedLevels(), ","))
	}
	return writer.Flush()
}
