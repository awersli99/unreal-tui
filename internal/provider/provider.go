// Package provider describes the LLM providers unreal can use, creates their
// clients, and builds the catalog of selectable models.
package provider

import (
	"context"
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

	"github.com/awersli99/unreal-tui/internal/anthropic"
	"github.com/awersli99/unreal-tui/internal/config"
)

// API kinds select a harness client or the local Anthropic adapter.
// models.json may use these names or their pi equivalents.
const (
	APIAnthropic  = "anthropic-messages"
	APIOpenAI     = "openai-responses"
	APICodex      = "openai-codex"
	APIOpenRouter = "openrouter"
	APIFireworks  = "fireworks"
	APIOllama     = "ollama"
)

const defaultOpenAIModel = "gpt-6-astra"

var apiAliases = map[string]string{
	"anthropic":          APIAnthropic,
	"anthropic-messages": APIAnthropic,
	"openai":             APIOpenAI,
	"openai-responses":   APIOpenAI,
	"openai-codex":       APICodex,
	"codex":              APICodex,
	"openrouter":         APIOpenRouter,
	"fireworks":          APIFireworks,
	"ollama":             APIOllama,
}

// Client is an LLM adapter that owns network resources.
type Client interface {
	llm.Adapter
	Close() error
}

// Spec describes how to reach one provider: a built-in one, a
// built-in overridden by models.json, or a custom one from models.json.
type Spec struct {
	Name         string
	API          string
	BaseURL      string
	APIKey       string // Unresolved models.json value.
	APIKeyEnv    string // Built-in environment variable.
	StoredKey    string // Saved by /login in auth.json; used literally.
	AuthFile     string // Local Anthropic OAuth credentials, read/refreshed per request.
	DefaultModel string
	Custom       bool
}

func builtinProviders() []Spec {
	specs := []Spec{
		{Name: "openai-codex", API: APICodex, BaseURL: openaicodex.BaseURL, DefaultModel: defaultOpenAIModel},
		{Name: "openai", API: APIOpenAI, BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY", DefaultModel: defaultOpenAIModel},
		{Name: "anthropic", API: APIAnthropic, BaseURL: anthropic.BaseURL, APIKeyEnv: "ANTHROPIC_API_KEY", DefaultModel: anthropic.DefaultModel},
		{Name: "openrouter", API: APIOpenRouter, BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY"},
		{Name: "fireworks", API: APIFireworks, BaseURL: "https://api.fireworks.ai/inference/v1", APIKeyEnv: "FIREWORKS_API_KEY"},
		{Name: "ollama", API: APIOllama, BaseURL: ollama.BaseURL},
	}
	// Honour the upstream runner's base URL override for built-ins.
	if override := strings.TrimSpace(os.Getenv(config.BaseURLEnv)); override != "" {
		for index := range specs {
			specs[index].BaseURL = override
		}
	}
	return specs
}

func (spec Spec) NeedsKey() bool {
	switch spec.API {
	case APIAnthropic, APIOpenAI, APIOpenRouter, APIFireworks:
		return true
	}
	return false
}

// Available reports whether credentials look configured. Like pi, it never
// runs "!command" values just to check.
func (spec Spec) Available() bool {
	switch {
	case spec.API == APICodex:
		return CodexAuthExists()
	case spec.AuthFile != "":
		_, _, err := anthropic.ReadCredential(spec.AuthFile)
		return err == nil
	case spec.StoredKey != "":
		return true
	case spec.APIKey != "":
		return true
	case spec.API == APIAnthropic:
		return !spec.Custom && anthropic.AuthAvailable()
	case spec.APIKeyEnv != "":
		return strings.TrimSpace(os.Getenv(config.APIKeyEnv)) != "" || strings.TrimSpace(os.Getenv(spec.APIKeyEnv)) != ""
	}
	return !spec.NeedsKey()
}

func (spec Spec) NewClient(maxAttempts int) (Client, error) {
	var apiKey string
	// Like pi, a key saved with /login wins over the environment and models.json.
	switch {
	case spec.AuthFile != "":
		// OAuth resolves from the file, never from a stale cached access token.
	case spec.StoredKey != "":
		apiKey = spec.StoredKey
	case spec.APIKey != "":
		resolved, err := config.ResolveValue(spec.APIKey)
		if err != nil {
			return nil, fmt.Errorf("resolve apiKey for %s: %w", spec.Name, err)
		}
		apiKey = resolved
	case spec.APIKeyEnv != "":
		apiKey = config.FirstNonEmpty(os.Getenv(config.APIKeyEnv), os.Getenv(spec.APIKeyEnv))
	}
	if spec.API == APIAnthropic && spec.APIKey != "" && spec.StoredKey == "" && spec.AuthFile == "" && strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("provider %s apiKey resolved to an empty value", spec.Name)
	}
	if spec.NeedsKey() && spec.API != APIAnthropic && strings.TrimSpace(apiKey) == "" {
		if spec.APIKeyEnv != "" {
			return nil, fmt.Errorf("%s must be set to use provider %s", spec.APIKeyEnv, spec.Name)
		}
		return nil, fmt.Errorf("provider %s needs an apiKey in %s", spec.Name, config.ModelsFileName)
	}

	var client Client
	var err error
	switch spec.API {
	case APIAnthropic:
		client, err = anthropic.NewClient(anthropic.ClientConfig{
			BaseURL: spec.BaseURL, AuthFile: spec.AuthFile, Key: apiKey, Custom: spec.Custom, MaxAttempts: maxAttempts,
		})
	case APIOpenAI:
		client, err = openai.NewClient(openai.Config{APIKey: apiKey, BaseURL: spec.BaseURL, MaxAttempts: &maxAttempts})
	case APICodex:
		codexConfig, configErr := openaicodex.EnvironmentConfig(os.Getenv)
		if configErr != nil {
			return nil, configErr
		}
		codexConfig.BaseURL, codexConfig.MaxAttempts = spec.BaseURL, &maxAttempts
		client, err = openaicodex.NewClient(codexConfig)
	case APIOpenRouter:
		client, err = openrouter.NewClient(openrouter.Config{APIKey: apiKey, BaseURL: spec.BaseURL, MaxAttempts: &maxAttempts})
	case APIFireworks:
		client, err = fireworks.NewClient(fireworks.Config{APIKey: apiKey, BaseURL: spec.BaseURL, MaxAttempts: &maxAttempts})
	case APIOllama:
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

func CodexAuthExists() bool {
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

// Keep configuration commands accessible before login and after logout. Never
// keep using an old authenticated client when its credentials were removed.
type UnavailableClient struct{ Err error }

func (client UnavailableClient) Respond(context.Context, llm.Request, llm.RequestOptions) (llm.Response, error) {
	return llm.Response{}, fmt.Errorf("provider unavailable; use /login or /model: %w", client.Err)
}

func (UnavailableClient) Close() error { return nil }
