package provider

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/awersli99/unreal-tui/internal/config"
)

// ModelFlags are the --provider and --model flags.
type ModelFlags struct {
	Provider, Model string
}

// StartupModel picks the model from flags, then environment variables, then
// the saved defaults, then whatever credentials are available. It also
// returns a thinking level given as a ":level" suffix.
func StartupModel(catalog *Catalog, settings config.Settings, flags ModelFlags) (ModelInfo, string, error) {
	provider := config.FirstNonEmpty(flags.Provider, os.Getenv("UNREAL_HARNESS_LLM_PROVIDER"))
	reference := config.FirstNonEmpty(flags.Model, os.Getenv("UNREAL_HARNESS_LLM_MODEL"))
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
		return catalog.Resolve(reference, config.FirstNonEmpty(provider, settings.DefaultProvider))
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
		// A fresh install must be able to reach /login without an API key or
		// an external CLI already configured. No request is sent until login.
		if _, ok := catalog.Provider("anthropic"); ok && len(catalog.Models) == 0 {
			return providerDefault(catalog, "anthropic")
		}
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
		return ModelInfo{}, "", fmt.Errorf("provider %s has no default model; pass --model or add models in %s", provider, config.ModelsFileName)
	}
	return catalog.Lookup(provider, spec.DefaultModel), "", nil
}

// detectProvider picks a provider when none is configured: an OpenAI API key
// wins, then an existing Codex (ChatGPT) login, then anything else usable.
func detectProvider(catalog *Catalog) (string, error) {
	if strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != "" {
		return "openai", nil
	}
	if CodexAuthExists() {
		return "openai-codex", nil
	}
	if len(catalog.Models) != 0 {
		return catalog.Models[0].Provider, nil
	}
	return "", errors.New("no LLM credentials found: set OPENAI_API_KEY or ANTHROPIC_API_KEY, use /login in unreal, or configure a provider in ~/.unreal-tui/models.json")
}
