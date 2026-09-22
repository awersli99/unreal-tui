package anthropic

import (
	"strings"
	"time"
)

// Verified 2026-09-22 against Anthropic's model overview and Pi's Anthropic
// catalog/transport. Keep discovery and request capabilities in one table:
// https://platform.claude.com/docs/en/models/overview
// https://github.com/earendil-works/pi/tree/main/packages/ai/src
// Dated snapshots share their family's capabilities but are selectable too.
type ModelSpec struct {
	ID              string
	Name            string
	ContextWindow   int64
	MaxOutputTokens int64
	Adaptive        bool
	XHigh           bool
	ManagedEffort   bool
}

var Models = []ModelSpec{
	{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", ContextWindow: 1000000, MaxOutputTokens: 128000, Adaptive: true, XHigh: true},
	{ID: "claude-opus-5-5", Name: "Claude Opus 5.5", ContextWindow: 1000000, MaxOutputTokens: 128000, Adaptive: true, XHigh: true, ManagedEffort: true},
	{ID: "claude-fable-5-1", Name: "Claude Fable 5.1", ContextWindow: 1000000, MaxOutputTokens: 128000, Adaptive: true, XHigh: true, ManagedEffort: true},
	{ID: "claude-haiku-4-5", Name: "Claude Haiku 4.5", ContextWindow: 200000, MaxOutputTokens: 64000},
	{ID: "claude-opus-5", Name: "Claude Opus 5", ContextWindow: 1000000, MaxOutputTokens: 128000, Adaptive: true, XHigh: true, ManagedEffort: true},
	{ID: "claude-fable-5", Name: "Claude Fable 5", ContextWindow: 1000000, MaxOutputTokens: 128000, Adaptive: true, XHigh: true},
	{ID: "claude-opus-4-8", Name: "Claude Opus 4.8", ContextWindow: 1000000, MaxOutputTokens: 128000, Adaptive: true, XHigh: true},
	{ID: "claude-opus-4-7", Name: "Claude Opus 4.7", ContextWindow: 1000000, MaxOutputTokens: 128000, Adaptive: true, XHigh: true},
	{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6", ContextWindow: 1000000, MaxOutputTokens: 128000, Adaptive: true},
	{ID: "claude-opus-4-6", Name: "Claude Opus 4.6", ContextWindow: 1000000, MaxOutputTokens: 128000, Adaptive: true},
	{ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5", ContextWindow: 1000000, MaxOutputTokens: 64000},
	{ID: "claude-opus-4-5", Name: "Claude Opus 4.5", ContextWindow: 200000, MaxOutputTokens: 64000},
}

var Snapshots = []string{
	"claude-haiku-4-5-20251001",
	"claude-sonnet-4-5-20250929",
	"claude-opus-4-5-20251101",
}

func ModelForID(id string) (ModelSpec, bool) {
	// Only strip an actual date suffix, not a version suffix. In particular,
	// claude-opus-5-5 must not be mistaken for claude-opus-5.
	family := id
	if len(id) > 9 && id[len(id)-9] == '-' {
		if _, err := time.Parse("20060102", id[len(id)-8:]); err == nil {
			family = id[:len(id)-9]
		}
	}
	for _, spec := range Models {
		if spec.ID == family {
			return spec, true
		}
	}
	return ModelSpec{}, false
}

func anthropicAdaptive(id string) bool {
	spec, known := ModelForID(id)
	// Preserve explicit selection of the restricted Mythos family; it is not
	// advertised in the general-access catalog.
	return spec.Adaptive || (!known && strings.HasPrefix(id, "claude-mythos"))
}

func anthropicXHigh(id string) bool {
	spec, known := ModelForID(id)
	return spec.XHigh || (!known && strings.HasPrefix(id, "claude-mythos"))
}

func Levels(id string) []string {
	if anthropicAdaptive(id) && !anthropicXHigh(id) {
		return []string{"low", "medium", "high", "max"}
	}
	// Older models map the UI's levels to token budgets, not native effort.
	return []string{"low", "medium", "high", "xhigh", "max"}
}
