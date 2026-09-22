package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/fireworks"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/ollama"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/openai"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/openaicodex"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/openrouter"
)

// API kinds are the harness's client implementations. models.json may use
// these names or pi's equivalent ("openai-responses").
const (
	apiOpenAI     = "openai-responses"
	apiCodex      = "openai-codex"
	apiOpenRouter = "openrouter"
	apiFireworks  = "fireworks"
	apiOllama     = "ollama"
)

const (
	baseURLEnvironment     = "UNREAL_HARNESS_LLM_BASE_URL"
	providerAPIKeyOverride = "UNREAL_HARNESS_LLM_API_KEY"
	defaultMaxAttempts     = 5
	defaultOpenAIModel     = "gpt-6-astra"
)

var apiAliases = map[string]string{
	"openai":           apiOpenAI,
	"openai-responses": apiOpenAI,
	"openai-codex":     apiCodex,
	"codex":            apiCodex,
	"openrouter":       apiOpenRouter,
	"fireworks":        apiFireworks,
	"ollama":           apiOllama,
}

// Client is an LLM adapter that owns network resources.
type Client interface {
	llm.Adapter
	Close() error
}

// ProviderSpec describes how to reach one provider: a built-in one, a
// built-in overridden by models.json, or a custom one from models.json.
type ProviderSpec struct {
	Name         string
	API          string
	BaseURL      string
	APIKey       string // Unresolved models.json value.
	APIKeyEnv    string // Built-in environment variable.
	DefaultModel string
	Custom       bool
}

func builtinProviders() []ProviderSpec {
	specs := []ProviderSpec{
		{Name: "openai-codex", API: apiCodex, BaseURL: openaicodex.BaseURL, DefaultModel: defaultOpenAIModel},
		{Name: "openai", API: apiOpenAI, BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY", DefaultModel: defaultOpenAIModel},
		{Name: "openrouter", API: apiOpenRouter, BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY"},
		{Name: "fireworks", API: apiFireworks, BaseURL: "https://api.fireworks.ai/inference/v1", APIKeyEnv: "FIREWORKS_API_KEY"},
		{Name: "ollama", API: apiOllama, BaseURL: ollama.BaseURL},
	}
	// Honour the upstream runner's base URL override for built-ins.
	if override := strings.TrimSpace(os.Getenv(baseURLEnvironment)); override != "" {
		for index := range specs {
			specs[index].BaseURL = override
		}
	}
	return specs
}

func (spec ProviderSpec) needsKey() bool {
	switch spec.API {
	case apiOpenAI, apiOpenRouter, apiFireworks:
		return true
	}
	return false
}

// Available reports whether credentials look configured. Like pi, it never
// runs "!command" values just to check.
func (spec ProviderSpec) Available() bool {
	switch {
	case spec.API == apiCodex:
		return codexAuthExists()
	case spec.APIKey != "":
		return true
	case spec.APIKeyEnv != "":
		return strings.TrimSpace(os.Getenv(providerAPIKeyOverride)) != "" || strings.TrimSpace(os.Getenv(spec.APIKeyEnv)) != ""
	}
	return !spec.needsKey()
}

func (spec ProviderSpec) newClient(maxAttempts int) (Client, error) {
	var apiKey string
	switch {
	case spec.APIKey != "":
		resolved, err := resolveConfigValue(spec.APIKey)
		if err != nil {
			return nil, fmt.Errorf("resolve apiKey for %s: %w", spec.Name, err)
		}
		apiKey = resolved
	case spec.APIKeyEnv != "":
		apiKey = firstNonEmpty(os.Getenv(providerAPIKeyOverride), os.Getenv(spec.APIKeyEnv))
	}
	if spec.needsKey() && strings.TrimSpace(apiKey) == "" {
		if spec.APIKeyEnv != "" {
			return nil, fmt.Errorf("%s must be set to use provider %s", spec.APIKeyEnv, spec.Name)
		}
		return nil, fmt.Errorf("provider %s needs an apiKey in %s", spec.Name, modelsFileName)
	}

	var client Client
	var err error
	switch spec.API {
	case apiOpenAI:
		client, err = openai.NewClient(openai.Config{APIKey: apiKey, BaseURL: spec.BaseURL, MaxAttempts: &maxAttempts})
	case apiCodex:
		config, configErr := openaicodex.EnvironmentConfig(os.Getenv)
		if configErr != nil {
			return nil, configErr
		}
		config.BaseURL, config.MaxAttempts = spec.BaseURL, &maxAttempts
		client, err = openaicodex.NewClient(config)
	case apiOpenRouter:
		client, err = openrouter.NewClient(openrouter.Config{APIKey: apiKey, BaseURL: spec.BaseURL, MaxAttempts: &maxAttempts})
	case apiFireworks:
		client, err = fireworks.NewClient(fireworks.Config{APIKey: apiKey, BaseURL: spec.BaseURL, MaxAttempts: &maxAttempts})
	case apiOllama:
		client, err = ollama.NewClient(ollama.Config{BaseURL: spec.BaseURL, MaxAttempts: &maxAttempts})
	default:
		return nil, fmt.Errorf("provider %s has unsupported api %q", spec.Name, spec.API)
	}
	if err != nil {
		return nil, fmt.Errorf("create %s client: %w", spec.Name, err)
	}
	return client, nil
}

func codexHome() string {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return home
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(userHome, ".codex")
}

func codexAuthExists() bool {
	if os.Getenv("OPENAI_CODEX_ACCESS_TOKEN") != "" || os.Getenv("OPENAI_CODEX_AUTH_FILE") != "" {
		return true
	}
	home := codexHome()
	if home == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(home, "auth.json"))
	return err == nil
}

// detectProvider picks a provider when none is configured: an OpenAI API key
// wins, then an existing Codex (ChatGPT) login, then anything else usable.
func detectProvider(catalog *Catalog) (string, error) {
	if strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != "" {
		return "openai", nil
	}
	if codexAuthExists() {
		return "openai-codex", nil
	}
	if len(catalog.Models) != 0 {
		return catalog.Models[0].Provider, nil
	}
	return "", errors.New("no LLM credentials found: set OPENAI_API_KEY, log in with `codex login`, or configure a provider in ~/.unreal-tui/models.json")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
