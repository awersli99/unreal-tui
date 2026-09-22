package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ModelInfo is one selectable model.
type ModelInfo struct {
	Provider      string
	ID            string
	Name          string
	Levels        []string // Supported thinking levels; empty means all.
	DefaultLevel  string
	ContextWindow int64 // Tokens; zero when unknown.
}

func (model ModelInfo) Key() string {
	return model.Provider + "/" + model.ID
}

func (model ModelInfo) SupportedLevels() []string {
	if len(model.Levels) == 0 {
		return thinkingLevels
	}
	return model.Levels
}

// ClampLevel returns level if the model supports it, otherwise the closest
// supported level below it, otherwise the lowest supported level.
func (model ModelInfo) ClampLevel(level string) string {
	supported := model.SupportedLevels()
	if slices.Contains(supported, level) {
		return level
	}
	rank := slices.Index(thinkingLevels, level)
	best := ""
	for _, candidate := range supported {
		if slices.Index(thinkingLevels, candidate) <= rank {
			best = candidate
		}
	}
	if best == "" {
		best = supported[0]
	}
	return best
}

// Catalog holds the configured providers and the models from those with
// credentials.
type Catalog struct {
	Providers []ProviderSpec
	Models    []ModelInfo
	Warnings  []string
}

// models.json follows pi's shape: {"providers": {"name": {...}}}.
type modelsFile struct {
	Providers map[string]providerConfig `json:"providers"`
}

type providerConfig struct {
	API     string        `json:"api"`
	BaseURL string        `json:"baseUrl"`
	APIKey  string        `json:"apiKey"`
	Models  []modelConfig `json:"models"`
}

type modelConfig struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	ThinkingLevels []string `json:"thinkingLevels"`
	ContextWindow  int64    `json:"contextWindow"`
}

func loadCatalog(home string) *Catalog {
	catalog := &Catalog{Providers: builtinProviders()}
	path := filepath.Join(home, modelsFileName)
	file, err := readModelsFile(path)
	if err != nil {
		catalog.Warnings = append(catalog.Warnings, err.Error())
	}

	entries, warning, err := readCredentials(home)
	if err != nil {
		catalog.Warnings = append(catalog.Warnings, err.Error())
	} else if warning != "" {
		catalog.Warnings = append(catalog.Warnings, warning)
	}
	stored := storedAPIKeys(entries)

	configured := make(map[string][]modelConfig)
	names := make([]string, 0, len(file.Providers))
	for name := range file.Providers {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		config := file.Providers[name]
		index := slices.IndexFunc(catalog.Providers, func(spec ProviderSpec) bool { return spec.Name == name })
		if index >= 0 {
			// Overriding a built-in provider: endpoint, key, and extra models.
			spec := &catalog.Providers[index]
			if config.BaseURL != "" {
				spec.BaseURL = strings.TrimRight(config.BaseURL, "/")
			}
			if config.APIKey != "" {
				spec.APIKey = config.APIKey
			}
		} else {
			api := apiAliases[config.API]
			if api == "" {
				catalog.Warnings = append(catalog.Warnings, fmt.Sprintf(
					"%s: provider %q has unsupported api %q; supported APIs: anthropic-messages, openai-responses, openai-codex, openrouter, fireworks and ollama",
					modelsFileName, name, config.API))
				continue
			}
			if config.BaseURL == "" && api != apiCodex && api != apiOllama {
				catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("%s: provider %q needs a baseUrl", modelsFileName, name))
				continue
			}
			spec := ProviderSpec{Name: name, API: api, BaseURL: strings.TrimRight(config.BaseURL, "/"), APIKey: config.APIKey, Custom: true}
			if spec.BaseURL == "" {
				spec.BaseURL = builtinBaseURL(api)
			}
			catalog.Providers = append(catalog.Providers, spec)
		}
		configured[name] = config.Models
	}

	// After custom providers are added, before availability is checked.
	for index := range catalog.Providers {
		spec := &catalog.Providers[index]
		spec.StoredKey = stored[spec.Name]
		if spec.Name == "anthropic" && !spec.Custom {
			var credential storedCredential
			if json.Unmarshal(entries[spec.Name], &credential) == nil && credential.Type == "oauth" {
				spec.AuthFile = authPath(home)
			}
		}
	}

	codexModels, codexErr := readCodexModels()
	for _, spec := range catalog.Providers {
		if !spec.Available() {
			continue
		}
		var models []ModelInfo
		if !spec.Custom && spec.API == apiAnthropic {
			models = builtinAnthropicModels(spec.Name)
		}
		if !spec.Custom && (spec.API == apiCodex || spec.API == apiOpenAI) {
			if codexErr == nil {
				for _, model := range codexModels {
					model.Provider = spec.Name
					models = append(models, model)
				}
			}
		}
		if len(models) == 0 && spec.DefaultModel != "" {
			models = append(models, ModelInfo{Provider: spec.Name, ID: spec.DefaultModel})
		}
		for _, config := range configured[spec.Name] {
			if strings.TrimSpace(config.ID) == "" {
				catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("%s: a model of provider %q has no id", modelsFileName, spec.Name))
				continue
			}
			model := ModelInfo{Provider: spec.Name, ID: config.ID, Name: config.Name, ContextWindow: config.ContextWindow}
			if spec.API == apiAnthropic {
				model = anthropicModelInfo(spec.Name, config.ID)
				if config.Name != "" {
					model.Name = config.Name
				}
				if config.ContextWindow != 0 {
					model.ContextWindow = config.ContextWindow
				}
				if len(config.ThinkingLevels) != 0 {
					model.Levels = nil
				}
			}
			for _, level := range config.ThinkingLevels {
				if validThinking(level) {
					model.Levels = append(model.Levels, level)
				}
			}
			if existing := slices.IndexFunc(models, func(candidate ModelInfo) bool { return candidate.ID == model.ID }); existing >= 0 {
				models[existing] = model
			} else {
				models = append(models, model)
			}
		}
		catalog.Models = append(catalog.Models, models...)
	}
	return catalog
}

func builtinBaseURL(api string) string {
	for _, spec := range builtinProviders() {
		if spec.API == api {
			return spec.BaseURL
		}
	}
	return ""
}

func readModelsFile(path string) (modelsFile, error) {
	var file modelsFile
	encoded, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return file, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(encoded, &file); err != nil {
		return file, fmt.Errorf("parse %s: %w", path, err)
	}
	return file, nil
}

type codexCacheModel struct {
	Slug                     string `json:"slug"`
	DisplayName              string `json:"display_name"`
	Visibility               string `json:"visibility"`
	Priority                 int    `json:"priority"`
	DefaultReasoningLevel    string `json:"default_reasoning_level"`
	ContextWindow            int64  `json:"context_window"`
	SupportedReasoningLevels []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
}

// readCodexModels lists the models Codex offers this account, from the cache
// the Codex CLI maintains. Slugs with a slash come from third-party routers
// configured in Codex, not the ChatGPT backend, so they are skipped.
func readCodexModels() ([]ModelInfo, error) {
	encoded, err := os.ReadFile(filepath.Join(codexHome(), "models_cache.json"))
	if err != nil {
		return nil, err
	}
	var cache struct {
		Models []codexCacheModel `json:"models"`
	}
	if err := json.Unmarshal(encoded, &cache); err != nil {
		return nil, fmt.Errorf("parse Codex model cache: %w", err)
	}
	slices.SortStableFunc(cache.Models, func(left, right codexCacheModel) int {
		return cmp.Compare(left.Priority, right.Priority)
	})
	var models []ModelInfo
	for _, entry := range cache.Models {
		if entry.Slug == "" || strings.Contains(entry.Slug, "/") || (entry.Visibility != "" && entry.Visibility != "list") {
			continue
		}
		model := ModelInfo{ID: entry.Slug, Name: entry.DisplayName, ContextWindow: entry.ContextWindow}
		if model.Name == entry.Slug {
			model.Name = ""
		}
		for _, level := range entry.SupportedReasoningLevels {
			if validThinking(level.Effort) {
				model.Levels = append(model.Levels, level.Effort)
			}
		}
		if validThinking(entry.DefaultReasoningLevel) {
			model.DefaultLevel = entry.DefaultReasoningLevel
		}
		models = append(models, model)
	}
	return models, nil
}

func (catalog *Catalog) Provider(name string) (ProviderSpec, bool) {
	index := slices.IndexFunc(catalog.Providers, func(spec ProviderSpec) bool { return spec.Name == name })
	if index < 0 {
		return ProviderSpec{}, false
	}
	return catalog.Providers[index], true
}

func (catalog *Catalog) ProviderNames() []string {
	names := make([]string, 0, len(catalog.Providers))
	for _, spec := range catalog.Providers {
		names = append(names, spec.Name)
	}
	return names
}

// Lookup returns the catalog entry for provider/id, or a bare entry for a
// model the catalog does not list.
func (catalog *Catalog) Lookup(provider, id string) ModelInfo {
	for _, model := range catalog.Models {
		if model.Provider == provider && model.ID == id {
			return model
		}
	}
	if spec, ok := catalog.Provider(provider); ok && spec.API == apiAnthropic {
		return anthropicModelInfo(provider, id)
	}
	return ModelInfo{Provider: provider, ID: id}
}

// splitLevel separates pi's optional ":<thinking>" suffix from a pattern.
func splitLevel(pattern string) (string, string) {
	if index := strings.LastIndex(pattern, ":"); index > 0 && validThinking(pattern[index+1:]) {
		return pattern[:index], pattern[index+1:]
	}
	return pattern, ""
}

// Match resolves a pi-style model pattern: "provider/id", a bare id, a glob
// such as "gpt-5*", or else a case-insensitive substring. It returns the
// matching models and any ":level" suffix.
func (catalog *Catalog) Match(pattern string) ([]ModelInfo, string) {
	pattern, level := splitLevel(strings.TrimSpace(pattern))
	if pattern == "" {
		return nil, level
	}
	if strings.ContainsAny(pattern, "*?") {
		expression := regexp.MustCompile("^(?i)" + strings.NewReplacer(`\*`, ".*", `\?`, ".").Replace(regexp.QuoteMeta(pattern)) + "$")
		return catalog.filter(func(model ModelInfo) bool {
			return expression.MatchString(model.Key()) || expression.MatchString(model.ID)
		}), level
	}
	if exact := catalog.filter(func(model ModelInfo) bool { return model.Key() == pattern }); len(exact) != 0 {
		return exact, level
	}
	if byID := catalog.filter(func(model ModelInfo) bool { return model.ID == pattern }); len(byID) != 0 {
		return byID, level
	}
	lower := strings.ToLower(pattern)
	return catalog.filter(func(model ModelInfo) bool {
		return strings.Contains(strings.ToLower(model.Key()+" "+model.Name), lower)
	}), level
}

// Resolve turns a model reference into one model. References that match
// nothing are accepted as raw model IDs when their provider is known, so any
// model a provider serves can be used even if the catalog does not list it.
func (catalog *Catalog) Resolve(reference, defaultProvider string) (ModelInfo, string, error) {
	matches, level := catalog.Match(reference)
	if len(matches) == 1 {
		return matches[0], level, nil
	}
	reference, _ = splitLevel(strings.TrimSpace(reference))
	if len(matches) > 1 {
		for _, match := range matches {
			if match.Provider == defaultProvider {
				return match, level, nil
			}
		}
		keys := make([]string, 0, len(matches))
		for _, match := range matches[:min(6, len(matches))] {
			keys = append(keys, match.Key())
		}
		return ModelInfo{}, "", fmt.Errorf("%q matches several models: %s", reference, strings.Join(keys, ", "))
	}
	if provider, id, ok := strings.Cut(reference, "/"); ok {
		if _, known := catalog.Provider(provider); known && id != "" {
			return catalog.Lookup(provider, id), level, nil
		}
	}
	if strings.ContainsAny(reference, "*?") {
		return ModelInfo{}, "", fmt.Errorf("no model matches %q", reference)
	}
	if _, known := catalog.Provider(defaultProvider); known {
		return catalog.Lookup(defaultProvider, reference), level, nil
	}
	return ModelInfo{}, "", fmt.Errorf("no model matches %q", reference)
}

// Scoped expands enabledModels patterns, in order and without duplicates,
// into the models Ctrl+P cycles through.
func (catalog *Catalog) Scoped(patterns []string) []scopedModel {
	var scoped []scopedModel
	seen := make(map[string]bool)
	for _, pattern := range patterns {
		matches, level := catalog.Match(pattern)
		for _, match := range matches {
			if !seen[match.Key()] {
				seen[match.Key()] = true
				scoped = append(scoped, scopedModel{Model: match, Level: level})
			}
		}
	}
	return scoped
}

type scopedModel struct {
	Model ModelInfo
	Level string
}

func (catalog *Catalog) filter(keep func(ModelInfo) bool) []ModelInfo {
	var models []ModelInfo
	for _, model := range catalog.Models {
		if keep(model) {
			models = append(models, model)
		}
	}
	return models
}

// fuzzyMatch reports whether every whitespace-separated term of query occurs
// in text, case-insensitively.
func fuzzyMatch(text, query string) bool {
	text = strings.ToLower(text)
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}
