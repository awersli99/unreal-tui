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
	"text/tabwriter"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
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
  --provider <name>       provider (openai-codex, openai, openrouter, fireworks, ollama, or one from models.json)
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
		if !validThinking(parsed.thinking) {
			return parsed, nil, fmt.Errorf("--thinking must be one of %s", strings.Join(thinkingLevels, ", "))
		}
	}
	return parsed, flags.Args(), nil
}

func run() error {
	flagValues, rest, err := parseFlags(os.Args[1:])
	if err == flag.ErrHelp {
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
	home, err := homeDirectory()
	if err != nil {
		return err
	}
	config, err := loadConfig(home, workspace)
	if err != nil {
		return err
	}
	catalog := loadCatalog(home)

	if flagValues.listModels {
		return listModels(catalog, strings.Join(rest, " "))
	}

	info, level, err := startupModel(catalog, config.Settings, flagValues)
	if err != nil {
		return err
	}
	app := &App{
		Config:  config,
		Catalog: catalog,
		Model:   info,
		Scoped:  config.Settings.EnabledModels,
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
	app.Thinking = info.ClampLevel(firstNonEmpty(flagValues.thinking, level,
		config.Settings.ModelThinkingLevels[info.Key()], config.Settings.DefaultThinkingLevel, info.DefaultLevel, defaultThinking))
	client, err := app.client(info.Provider)
	if err != nil {
		return err
	}
	defer app.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	shell := expandHome(config.Settings.ShellPath)
	app.Prompt = PromptOptions{Workspace: workspace, Home: home, Shell: shell, ContextFiles: !flagValues.noContextFiles}
	prompt, files := systemPrompt(app.Prompt)
	app.LoadedFiles = files
	skills, skillErrors := discoverSkills(workspace, home)
	app.Skills = skillNames(skills)
	for _, skillErr := range skillErrors {
		app.Notices = append(app.Notices, Block{Kind: BlockError, Text: "Skill: " + skillErr.Error()})
	}

	var program *tea.Program
	engine, err := NewEngine(ctx, EngineConfig{
		StoreDirectory: sessionDirectory(home, workspace, firstNonEmpty(flagValues.sessionDirectory, config.Settings.SessionDir)),
		Workspace:      workspace,
		Client:         client,
		Model:          info.ID,
		Effort:         llm.ReasoningEffort(app.Thinking),
		SystemPrompt:   prompt,
		Skills:         skills,
		Shell:          shell,
		Emit:           func(event any) { program.Send(engineEventMsg{event: event}) },
	})
	if err != nil {
		return err
	}
	app.Engine = engine

	var resume session.ID
	switch {
	case flagValues.session != "":
		sessions, err := engine.Sessions()
		if err != nil {
			return err
		}
		if resume, err = pickSession(sessions, flagValues.session); err != nil {
			return err
		}
	case flagValues.continueLast:
		sessions, err := engine.Sessions()
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			app.Notices = append(app.Notices, Block{Kind: BlockInfo, Text: "No previous session in this workspace; starting a new one."})
		} else {
			resume = sessions[0].ID
		}
	case flagValues.resumePicker:
		app.OpenResume = true
	}
	app.Resume = resume

	// Query the terminal before Bubble Tea takes it over.
	app.DarkBackground = lipgloss.HasDarkBackground()
	program = tea.NewProgram(newModel(app))
	_, runErr := program.Run()
	engine.Close()
	if runErr != nil {
		return runErr
	}
	if id := engine.SessionID(); id != "" {
		fmt.Printf("\nResume this session with: unreal --session %s\n", string(id)[:8])
	}
	return nil
}

// startupModel picks the model from flags, then environment variables, then
// the saved defaults, then whatever credentials are available. It also
// returns a thinking level given as a ":level" suffix.
func startupModel(catalog *Catalog, settings Settings, flagValues options) (ModelInfo, string, error) {
	provider := firstNonEmpty(flagValues.provider, os.Getenv("UNREAL_HARNESS_LLM_PROVIDER"))
	reference := firstNonEmpty(flagValues.model, os.Getenv("UNREAL_HARNESS_LLM_MODEL"))
	if provider != "" {
		if _, ok := catalog.Provider(provider); !ok {
			return ModelInfo{}, "", fmt.Errorf("unknown provider %q; available: %s", provider, strings.Join(catalog.ProviderNames(), ", "))
		}
	}
	switch {
	case reference != "":
		if provider != "" && !strings.Contains(reference, "/") {
			reference = provider + "/" + reference
		}
		return catalog.Resolve(reference, firstNonEmpty(provider, settings.DefaultProvider))
	case provider != "":
		if provider == settings.DefaultProvider && settings.DefaultModel != "" {
			return catalog.Lookup(provider, settings.DefaultModel), "", nil
		}
		return providerDefault(catalog, provider)
	case settings.DefaultModel != "":
		if settings.DefaultProvider != "" {
			if _, ok := catalog.Provider(settings.DefaultProvider); ok {
				return catalog.Lookup(settings.DefaultProvider, settings.DefaultModel), "", nil
			}
		} else if info, level, err := catalog.Resolve(settings.DefaultModel, ""); err == nil {
			return info, level, nil
		}
	}
	detected, err := detectProvider(catalog)
	if err != nil {
		return ModelInfo{}, "", err
	}
	return providerDefault(catalog, detected)
}

func providerDefault(catalog *Catalog, provider string) (ModelInfo, string, error) {
	for _, model := range catalog.Models {
		if model.Provider == provider {
			return model, "", nil
		}
	}
	spec, _ := catalog.Provider(provider)
	if spec.DefaultModel == "" {
		return ModelInfo{}, "", fmt.Errorf("provider %s has no default model; pass --model or add models in %s", provider, modelsFileName)
	}
	return catalog.Lookup(provider, spec.DefaultModel), "", nil
}

func listModels(catalog *Catalog, search string) error {
	for _, warning := range catalog.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", warning)
	}
	var models []ModelInfo
	for _, model := range catalog.Models {
		if fuzzyMatch(model.Key()+" "+model.Name, search) {
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		fmt.Println("No models found. Log in with `codex login`, set an API key, or configure models.json.")
		return nil
	}
	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "provider\tmodel\tname\tthinking")
	for _, model := range models {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", model.Provider, model.ID, firstNonEmpty(model.Name, "-"), strings.Join(model.SupportedLevels(), ","))
	}
	return writer.Flush()
}
