package tui

import (
	"fmt"
	"slices"

	"github.com/unreallabsai/unreal-agent/harness/session"

	"github.com/awersli99/unreal-tui/internal/config"
	"github.com/awersli99/unreal-tui/internal/engine"
	"github.com/awersli99/unreal-tui/internal/provider"
)

// App is the TUI's state outside the Bubble Tea model: configuration, the
// model catalog, provider clients, and the live model selection.
type App struct {
	Engine  *engine.Engine
	Config  config.Config
	Catalog *provider.Catalog
	Prompt  config.PromptOptions

	// Live selection for this session; saved defaults live in Config.
	Model    provider.ModelInfo
	Thinking string
	// Scoped holds the enabledModels patterns Ctrl+P cycles through.
	Scoped []string

	Initial     string
	Resume      session.ID
	OpenResume  bool
	Notices     []engine.Block
	LoadedFiles []string
	Skills      []string // Loaded skill names.

	// DarkBackground is detected once before the TUI starts; querying the
	// terminal while Bubble Tea owns it would swallow input.
	DarkBackground bool

	clients map[string]provider.Client
	retired []provider.Client
}

// client returns the cached client for a provider, creating it on first use.
func (app *App) ClientFor(name string) (provider.Client, error) {
	if client, ok := app.clients[name]; ok {
		return client, nil
	}
	spec, ok := app.Catalog.Provider(name)
	if !ok {
		return nil, fmt.Errorf("unknown name %q", name)
	}
	client, err := spec.NewClient(app.Config.Settings.MaxAttempts())
	if err != nil {
		return nil, err
	}
	if app.clients == nil {
		app.clients = make(map[string]provider.Client)
	}
	app.clients[name] = client
	return client, nil
}

// retireClients drops cached clients (after models.json changes) without
// closing them: a model call may still be using one. They close at exit.
func (app *App) retireClients() {
	for _, client := range app.clients {
		app.retired = append(app.retired, client)
	}
	app.clients = nil
}

func (app *App) Close() {
	for _, client := range app.clients {
		client.Close()
	}
	for _, client := range app.retired {
		client.Close()
	}
}

// Style is the glamour style: the theme setting, or the terminal background.
func (app *App) Style() string {
	switch app.Config.Settings.Theme {
	case "dark", "light":
		return app.Config.Settings.Theme
	}
	if app.DarkBackground {
		return "dark"
	}
	return "light"
}

// thinkingFor picks the level to use with a model: an explicit level, else
// the per-model setting, else the current level, clamped to what the model
// supports.
func (app *App) thinkingFor(model provider.ModelInfo, explicit string) string {
	level := config.FirstNonEmpty(explicit, app.Config.Settings.ModelThinkingLevels[model.Key()], app.Thinking)
	return model.ClampLevel(level)
}

// cycleCandidates are the models Ctrl+P steps through: the scoped models when
// enabledModels is set, otherwise every available model.
func (app *App) cycleCandidates() []provider.ScopedModel {
	if len(app.Scoped) != 0 {
		if scoped := app.Catalog.Scoped(app.Scoped); len(scoped) != 0 {
			return scoped
		}
	}
	all := make([]provider.ScopedModel, 0, len(app.Catalog.Models))
	for _, model := range app.Catalog.Models {
		all = append(all, provider.ScopedModel{Model: model})
	}
	return all
}

func (app *App) isScoped(model provider.ModelInfo) bool {
	return slices.ContainsFunc(app.Catalog.Scoped(app.Scoped), func(candidate provider.ScopedModel) bool {
		return candidate.Model.Key() == model.Key()
	})
}
