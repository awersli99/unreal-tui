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

const (
	baseURLEnvironment     = "UNREAL_HARNESS_LLM_BASE_URL"
	maxAttemptsDefault     = 5
	defaultOpenAIModel     = "gpt-6-astra"
	providerAPIKeyOverride = "UNREAL_HARNESS_LLM_API_KEY"
)

// Client is an LLM adapter that owns network resources.
type Client interface {
	llm.Adapter
	Close() error
}

// Provider mirrors the upstream runner's provider table so the TUI accepts the
// same names and environment variables.
type Provider struct {
	Name              string
	BaseURL           string
	DefaultModel      string
	APIKeyEnvironment string // Empty when the client authenticates on its own.
	NewClient         func(apiKey, baseURL string, maxAttempts int) (Client, error)
}

func providers() []Provider {
	return []Provider{
		{
			Name:              "openai",
			BaseURL:           "https://api.openai.com/v1",
			DefaultModel:      defaultOpenAIModel,
			APIKeyEnvironment: "OPENAI_API_KEY",
			NewClient: func(apiKey, baseURL string, maxAttempts int) (Client, error) {
				return openai.NewClient(openai.Config{APIKey: apiKey, BaseURL: baseURL, MaxAttempts: &maxAttempts})
			},
		},
		{
			Name:         "openai-codex",
			BaseURL:      openaicodex.BaseURL,
			DefaultModel: defaultOpenAIModel,
			NewClient: func(_, baseURL string, maxAttempts int) (Client, error) {
				config, err := openaicodex.EnvironmentConfig(os.Getenv)
				if err != nil {
					return nil, err
				}
				config.BaseURL, config.MaxAttempts = baseURL, &maxAttempts
				return openaicodex.NewClient(config)
			},
		},
		{
			Name:              "openrouter",
			BaseURL:           "https://openrouter.ai/api/v1",
			APIKeyEnvironment: "OPENROUTER_API_KEY",
			NewClient: func(apiKey, baseURL string, maxAttempts int) (Client, error) {
				return openrouter.NewClient(openrouter.Config{APIKey: apiKey, BaseURL: baseURL, MaxAttempts: &maxAttempts})
			},
		},
		{
			Name:              "fireworks",
			BaseURL:           "https://api.fireworks.ai/inference/v1",
			APIKeyEnvironment: "FIREWORKS_API_KEY",
			NewClient: func(apiKey, baseURL string, maxAttempts int) (Client, error) {
				return fireworks.NewClient(fireworks.Config{APIKey: apiKey, BaseURL: baseURL, MaxAttempts: &maxAttempts})
			},
		},
		{
			Name:    "ollama",
			BaseURL: ollama.BaseURL,
			NewClient: func(_, baseURL string, maxAttempts int) (Client, error) {
				return ollama.NewClient(ollama.Config{BaseURL: baseURL, MaxAttempts: &maxAttempts})
			},
		},
	}
}

func providerNames() []string {
	var names []string
	for _, provider := range providers() {
		names = append(names, provider.Name)
	}
	return names
}

func findProvider(name string) (Provider, error) {
	for _, provider := range providers() {
		if provider.Name == name {
			return provider, nil
		}
	}
	return Provider{}, fmt.Errorf("unknown provider %q; available: %s", name, strings.Join(providerNames(), ", "))
}

// detectProvider picks a provider when none is configured: an OpenAI API key
// wins, then an existing Codex (ChatGPT) login, then the other API keys.
func detectProvider() (string, error) {
	if strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != "" {
		return "openai", nil
	}
	if codexAuthExists() {
		return "openai-codex", nil
	}
	for _, name := range []string{"openrouter", "fireworks"} {
		provider, _ := findProvider(name)
		if strings.TrimSpace(os.Getenv(provider.APIKeyEnvironment)) != "" {
			return name, nil
		}
	}
	return "", errors.New("no LLM credentials found: set OPENAI_API_KEY, log in with `codex login`, or pass -provider")
}

func codexAuthExists() bool {
	if os.Getenv("OPENAI_CODEX_ACCESS_TOKEN") != "" || os.Getenv("OPENAI_CODEX_AUTH_FILE") != "" {
		return true
	}
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		home = filepath.Join(userHome, ".codex")
	}
	_, err := os.Stat(filepath.Join(home, "auth.json"))
	return err == nil
}

// newClient builds a client for provider, resolving its API key and base URL
// from the environment the same way the upstream runner does.
func newClient(provider Provider) (Client, error) {
	var apiKey string
	if provider.APIKeyEnvironment != "" {
		apiKey = os.Getenv(providerAPIKeyOverride)
		if strings.TrimSpace(apiKey) == "" {
			apiKey = os.Getenv(provider.APIKeyEnvironment)
		}
		if strings.TrimSpace(apiKey) == "" {
			return nil, fmt.Errorf("%s must be set to use provider %s", provider.APIKeyEnvironment, provider.Name)
		}
	}
	baseURL := strings.TrimSpace(os.Getenv(baseURLEnvironment))
	if baseURL == "" {
		baseURL = provider.BaseURL
	}
	client, err := provider.NewClient(apiKey, baseURL, maxAttemptsDefault)
	if err != nil {
		return nil, fmt.Errorf("create %s client: %w", provider.Name, err)
	}
	return client, nil
}
