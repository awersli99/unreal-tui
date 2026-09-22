package provider

import (
	"github.com/awersli99/unreal-tui/internal/anthropic"
)

// anthropicModelInfo describes a Claude model with the capabilities the
// anthropic package knows for it, including dated snapshots of known models.
func anthropicModelInfo(provider, id string) ModelInfo {
	info := ModelInfo{Provider: provider, ID: id}
	if spec, known := anthropic.ModelForID(id); known {
		info.Name, info.ContextWindow = spec.Name, spec.ContextWindow
		info.Levels, info.DefaultLevel = anthropic.Levels(id), "high"
	}
	return info
}

// builtinAnthropicModels lists the bundled Claude catalog for a provider.
func builtinAnthropicModels(provider string) []ModelInfo {
	models := make([]ModelInfo, 0, len(anthropic.Models)+len(anthropic.Snapshots))
	for _, spec := range anthropic.Models {
		models = append(models, anthropicModelInfo(provider, spec.ID))
	}
	for _, id := range anthropic.Snapshots {
		models = append(models, anthropicModelInfo(provider, id))
	}
	return models
}
