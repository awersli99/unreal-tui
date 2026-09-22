package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"

	"github.com/awersli99/unreal-tui/internal/config"
	"github.com/awersli99/unreal-tui/internal/engine"
	"github.com/awersli99/unreal-tui/internal/provider"
)

// overlay is a picker shown in place of the editor.
type overlay struct {
	selector *selector
	// prompt, when set instead of selector, is a text prompt.
	prompt *promptInput
	// choose handles enter and ctrl+s. For multi-select pickers only ctrl+s
	// calls it, with a nil item.
	choose func(item *selectorItem) tea.Cmd
	// toggled runs after a multi-select item changes.
	toggled func() tea.Cmd
	// keys handles picker-specific shortcuts before the selector does.
	keys func(key string) (tea.Cmd, bool)
	// stay keeps the picker open after enter.
	stay bool
	// view, when set, renders the selector in place of its default view.
	view func(width int) string
}

var thinkingDescriptions = map[string]string{
	"low":    "fast, light reasoning",
	"medium": "balanced speed and depth",
	"high":   "deeper reasoning for complex work",
	"xhigh":  "extra-high reasoning depth",
	"max":    "maximum reasoning depth",
}

var thinkingColors = map[string]lipgloss.AdaptiveColor{
	"low":    {Light: "#8A94A6", Dark: "#5F6B7A"},
	"medium": {Light: "#2B7BB9", Dark: "#5FAFD7"},
	"high":   accentColor,
	"xhigh":  {Light: "#A0309F", Dark: "#E27AE0"},
	"max":    {Light: "#C2410C", Dark: "#FF8C5A"},
}

func (active *overlay) View(width int) string {
	if active.prompt != nil {
		return active.prompt.View(width)
	}
	if active.view != nil {
		return active.view(width)
	}
	return active.selector.View(width)
}

func (current *model) handleOverlayKey(message tea.KeyMsg) tea.Cmd {
	active := current.overlay
	if active.prompt != nil {
		return current.handlePromptKey(active.prompt, message)
	}
	if active.keys != nil {
		if command, handled := active.keys(message.String()); handled {
			return command
		}
	}
	switch active.selector.Update(message) {
	case selectorChoose:
		if !active.stay {
			current.overlay = nil
		}
		return active.choose(active.selector.Selected())
	case selectorSave:
		if active.selector.multi {
			current.overlay = nil
			return active.choose(nil)
		}
		if item := active.selector.Selected(); item != nil {
			current.overlay = nil
			return active.choose(item)
		}
	case selectorToggle:
		if active.toggled != nil {
			return active.toggled()
		}
	case selectorCancel:
		current.overlay = nil
	}
	return nil
}

// switchModel changes the model and, like pi, remembers it (and the thinking
// level) as the default for the next start.
func (current *model) switchModel(info provider.ModelInfo, level string) tea.Cmd {
	app := current.app
	client, err := app.ClientFor(info.Provider)
	if err != nil {
		return current.print(engine.Block{Kind: engine.BlockError, Text: err.Error()})
	}
	current.engine.SetModel(client, info.ID)
	app.Model = info
	thinking := app.thinkingFor(info, level)
	if thinking != app.Thinking {
		if err := current.engine.SetEffort(llm.ReasoningEffort(thinking)); err != nil {
			return current.print(engine.Block{Kind: engine.BlockError, Text: err.Error()})
		}
		app.Thinking = thinking
	}
	current.notice = fmt.Sprintf("Model: %s · thinking %s", info.Key(), app.Thinking)
	values := map[string]any{"defaultProvider": info.Provider, "defaultModel": info.ID, "defaultThinkingLevel": app.Thinking}
	if err := current.saveSettings(values); err != nil {
		return current.print(engine.Block{Kind: engine.BlockError, Text: err.Error()})
	}
	return nil
}

func (current *model) setThinking(level string) tea.Cmd {
	if err := current.engine.SetEffort(llm.ReasoningEffort(level)); err != nil {
		return current.print(engine.Block{Kind: engine.BlockError, Text: err.Error()})
	}
	current.app.Thinking = level
	current.notice = "Thinking: " + level
	if err := current.saveSettings(map[string]any{"defaultThinkingLevel": level}); err != nil {
		return current.print(engine.Block{Kind: engine.BlockError, Text: err.Error()})
	}
	return nil
}

// saveSettings writes keys to the global settings file and reloads the
// merged configuration so project overrides still apply.
func (current *model) saveSettings(values map[string]any) error {
	app := current.app
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if err := config.SaveGlobalSetting(app.Config.Home, key, values[key]); err != nil {
			return fmt.Errorf("save %s: %w", key, err)
		}
	}
	loaded, err := config.Load(app.Config.Home, app.Config.Workspace)
	if err != nil {
		return err
	}
	app.Config = loaded
	return nil
}

func (current *model) cycleModel(step int) tea.Cmd {
	candidates := current.app.cycleCandidates()
	if len(candidates) == 0 {
		current.notice = "No models available; configure one in ~/.unreal-tui/models.json"
		return nil
	}
	position := slices.IndexFunc(candidates, func(candidate provider.ScopedModel) bool {
		return candidate.Model.Key() == current.app.Model.Key()
	})
	if position < 0 && step < 0 {
		position = 0
	}
	next := candidates[((position+step)%len(candidates)+len(candidates))%len(candidates)]
	return current.switchModel(next.Model, next.Level)
}

func (current *model) cycleThinking() tea.Cmd {
	levels := current.app.Model.SupportedLevels()
	position := slices.Index(levels, current.app.Thinking)
	return current.setThinking(levels[(position+1)%len(levels)])
}

func modelItem(info provider.ModelInfo, isCurrent bool) selectorItem {
	var details []string
	if info.Name != "" {
		details = append(details, info.Name)
	}
	if len(info.Levels) != 0 {
		details = append(details, "thinking "+strings.Join(info.Levels, "/"))
	}
	return selectorItem{Label: info.Key(), Detail: strings.Join(details, " · "), Value: info, Current: isCurrent}
}

func (current *model) openThinkingPicker() {
	var items []selectorItem
	for _, level := range current.app.Model.SupportedLevels() {
		items = append(items, selectorItem{
			Label:   level,
			Detail:  thinkingDescriptions[level],
			Value:   level,
			Current: level == current.app.Thinking,
		})
	}
	title := "Thinking level for " + current.app.Model.Key()
	current.overlay = &overlay{
		selector: newSelector(title, "enter select · esc cancel", items, false),
		choose: func(item *selectorItem) tea.Cmd {
			return current.setThinking(item.Value.(string))
		},
	}
}

func (current *model) openScopedPicker() {
	app := current.app
	var items []selectorItem
	for _, info := range app.Catalog.Models {
		item := modelItem(info, info.Key() == app.Model.Key())
		item.Checked = app.isScoped(info)
		items = append(items, item)
	}
	picker := newSelector("Scoped models for ctrl+p cycling",
		"space/enter toggle · ctrl+a all · ctrl+x none · ctrl+s save · esc done (this session only)", items, true)
	picker.multi = true
	apply := func() {
		var keys []string
		for _, item := range picker.items {
			if item.Checked {
				keys = append(keys, item.Value.(provider.ModelInfo).Key())
			}
		}
		app.Scoped = keys
	}
	current.overlay = &overlay{
		selector: picker,
		toggled:  func() tea.Cmd { apply(); return nil },
		keys: func(key string) (tea.Cmd, bool) {
			switch key {
			case "ctrl+a":
				picker.SetAll(true)
			case "ctrl+x":
				picker.SetAll(false)
			default:
				return nil, false
			}
			apply()
			return nil, true
		},
		choose: func(*selectorItem) tea.Cmd {
			var saved any
			if len(app.Scoped) != 0 {
				saved = app.Scoped
			}
			if err := current.saveSettings(map[string]any{"enabledModels": saved}); err != nil {
				return current.print(engine.Block{Kind: engine.BlockError, Text: err.Error()})
			}
			current.notice = fmt.Sprintf("Saved scoped models (%d)", len(app.Scoped))
			return nil
		},
	}
}

func (current *model) openResumePicker() tea.Cmd {
	sessions, err := current.engine.Sessions()
	if err != nil {
		return current.print(engine.Block{Kind: engine.BlockError, Text: err.Error()})
	}
	if len(sessions) == 0 {
		current.notice = "No previous sessions in this workspace"
		return nil
	}
	active := current.engine.SessionID()
	var items []selectorItem
	for _, summary := range sessions {
		items = append(items, selectorItem{
			Label:   firstLine(summary.Title, 60),
			Detail:  formatAgo(summary.UpdatedAt) + " · " + engine.ShortID(summary.ID),
			Value:   summary.ID,
			Current: summary.ID == active,
		})
	}
	current.overlay = &overlay{
		selector: newSelector("Resume session", "enter resume · esc cancel", items, true),
		choose: func(item *selectorItem) tea.Cmd {
			if current.busy() {
				current.engine.Interrupt()
			}
			return current.loadSession(item.Value.(session.ID))
		},
	}
	return nil
}

// Settings picker entries, identified by their Value.
const (
	settingHideThinking = "hideThinkingBlock"
	settingQuietStartup = "quietStartup"
	settingTheme        = "theme"
	settingDefaultLevel = "defaultThinkingLevel"
	settingModelLevel   = "modelThinkingLevel"
	settingDefaultModel = "defaultModel"
	settingScopedModels = "enabledModels"
	settingReload       = "reload"
)

func (current *model) settingsItems() []selectorItem {
	settings := current.app.Config.Settings
	onOff := func(value bool) string {
		if value {
			return "on"
		}
		return "off"
	}
	orNone := func(value string) string {
		return config.FirstNonEmpty(value, "not set")
	}
	defaultModel := "not set"
	if settings.DefaultModel != "" {
		defaultModel = config.FirstNonEmpty(settings.DefaultProvider, "?") + "/" + settings.DefaultModel
	}
	key := current.app.Model.Key()
	scoped := "all models"
	if len(current.app.Scoped) != 0 {
		scoped = fmt.Sprintf("%d patterns", len(current.app.Scoped))
	}
	return []selectorItem{
		{Label: "Hide thinking blocks: " + onOff(settings.HideThinkingBlock), Detail: "ctrl+t toggles for this session", Value: settingHideThinking},
		{Label: "Quiet startup: " + onOff(settings.QuietStartup), Detail: "hide the [Context] and [Skills] listing at startup", Value: settingQuietStartup},
		{Label: "Theme: " + config.FirstNonEmpty(settings.Theme, "auto"), Detail: "auto, dark, light", Value: settingTheme},
		{Label: "Default thinking level: " + orNone(settings.DefaultThinkingLevel), Detail: "used at startup", Value: settingDefaultLevel},
		{Label: "Thinking level for " + key + ": " + orNone(settings.ModelThinkingLevels[key]), Detail: "applied whenever this model is selected", Value: settingModelLevel},
		{Label: "Default model: " + defaultModel, Detail: "the model picker sets it", Value: settingDefaultModel},
		{Label: "Scoped models: " + scoped, Detail: "models ctrl+p cycles through", Value: settingScopedModels},
		{Label: "Reload settings, models and context files", Detail: "same as /reload", Value: settingReload},
	}
}

func (current *model) openSettingsPicker() {
	picker := newSelector("Settings", "enter change · esc close · saved to "+shortenHome(config.GlobalSettingsPath(current.app.Config.Home)), current.settingsItems(), false)
	current.overlay = &overlay{
		selector: picker,
		stay:     true,
		choose: func(item *selectorItem) tea.Cmd {
			command := current.changeSetting(item.Value.(string))
			if current.overlay != nil && current.overlay.selector == picker {
				cursor := picker.cursor
				picker.items = current.settingsItems()
				picker.refilter()
				picker.cursor = cursor
			}
			return command
		},
	}
}

func (current *model) changeSetting(name string) tea.Cmd {
	settings := current.app.Config.Settings
	var values map[string]any
	switch name {
	case settingHideThinking:
		values = map[string]any{name: !settings.HideThinkingBlock}
	case settingQuietStartup:
		values = map[string]any{name: !settings.QuietStartup}
	case settingTheme:
		next := map[string]any{"": "dark", "auto": "dark", "dark": "light", "light": nil}[settings.Theme]
		values = map[string]any{name: next}
	case settingDefaultLevel:
		values = map[string]any{name: nextLevel(settings.DefaultThinkingLevel)}
	case settingModelLevel:
		levels := make(map[string]string)
		for key, level := range settings.ModelThinkingLevels {
			levels[key] = level
		}
		key := current.app.Model.Key()
		if next := nextLevel(levels[key]); next == nil {
			delete(levels, key)
		} else {
			levels[key] = next.(string)
		}
		var saved any
		if len(levels) != 0 {
			saved = levels
		}
		values = map[string]any{"modelThinkingLevels": saved}
	case settingDefaultModel:
		current.overlay = nil
		current.openModelPicker("")
		return nil
	case settingScopedModels:
		current.overlay = nil
		current.openScopedPicker()
		return nil
	case settingReload:
		current.overlay = nil
		return current.reload()
	}
	if err := current.saveSettings(values); err != nil {
		return current.print(engine.Block{Kind: engine.BlockError, Text: err.Error()})
	}
	current.applyDisplaySettings()
	return nil
}

// nextLevel cycles not set → low → … → max → not set. nil means unset.
func nextLevel(level string) any {
	position := slices.Index(config.ThinkingLevels, level)
	if position == len(config.ThinkingLevels)-1 {
		return nil
	}
	return config.ThinkingLevels[position+1]
}

// applyDisplaySettings applies settings that affect rendering right away.
func (current *model) applyDisplaySettings() {
	settings := current.app.Config.Settings
	current.showThinking = !settings.HideThinkingBlock
	current.render.setStyle(current.app.Style())
}
