package model

import (
	"slices"
	"testing"
)

// grain/task-4527: grain names no model of its own. A connection
// advertises what the operator pinned or what its provider reported, and
// nothing else; the model it runs when nothing chose one is the
// provider's own default.
func TestConnectionModelsHaveNoHardcodedFallback(t *testing.T) {
	for name, tc := range map[string]struct {
		b             AgentBackend
		wantSupported []string
		wantEffective string
	}{
		"agy never answered": {b: AgentBackend{Framework: AgentFrameworkAntigravity}},
		"claude never answered": {b: AgentBackend{Framework: AgentFrameworkClaude,
			Discovered: &DiscoveredModels{Error: "no key"}}},
		"codex never answered":      {b: AgentBackend{Framework: AgentFrameworkCodex}},
		"custom with an empty list": {b: AgentBackend{Framework: AgentFrameworkCustom, Command: "x"}},
		"agy discovered runs auto": {b: AgentBackend{Framework: AgentFrameworkAntigravity,
			Discovered: &DiscoveredModels{Models: []string{"gemini-3.8-flash", "gemini-3.1-pro"}}},
			wantSupported: []string{"gemini-3.8-flash", "gemini-3.1-pro"}, wantEffective: ProviderModelAuto},
		"claude discovered runs its first": {b: AgentBackend{Framework: AgentFrameworkClaude,
			Discovered: &DiscoveredModels{Models: []string{"claude-opus-5-5", "claude-sonnet-5-5"}}},
			wantSupported: []string{"claude-opus-5-5", "claude-sonnet-5-5"}, wantEffective: "claude-opus-5-5"},
		"a pinned model is kept": {b: AgentBackend{Framework: AgentFrameworkAntigravity, Models: []string{"gemini-3.1-pro"},
			Discovered: &DiscoveredModels{Models: []string{"gemini-3.8-flash"}}},
			wantSupported: []string{"gemini-3.1-pro"}, wantEffective: "gemini-3.1-pro"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.b.SupportedModels(); !slices.Equal(got, tc.wantSupported) {
				t.Errorf("SupportedModels = %v, want %v", got, tc.wantSupported)
			}
			if got := tc.b.EffectiveModel(); got != tc.wantEffective {
				t.Errorf("EffectiveModel = %q, want %q", got, tc.wantEffective)
			}
			if got := tc.b.HasModels(); got != (len(tc.wantSupported) > 0) {
				t.Errorf("HasModels = %v", got)
			}
		})
	}
}

func TestEffectiveDefaultModelNamesNoModelOfItsOwn(t *testing.T) {
	if got := (Config{}).EffectiveDefaultModel(); got != "" {
		t.Errorf("EffectiveDefaultModel = %q, want \"\" (the provider default)", got)
	}
	if got := (Config{DefaultModel: "claude-sonnet-5"}).EffectiveDefaultModel(); got != "claude-sonnet-5" {
		t.Errorf("an explicitly set model = %q, want it kept", got)
	}
	if got := (Config{
		DefaultModel: "claude-opus-5-high, gemini-3.1-pro, claude-opus-5-high",
	}).DefaultModels(); !slices.Equal(got, []string{"claude-opus-5-high", "gemini-3.1-pro"}) {
		t.Errorf("DefaultModels = %v, want [claude-opus-5-high gemini-3.1-pro]", got)
	}
	if got := (Config{
		DefaultModel: "claude-unserved, gemini-3.1-pro",
		AgentBackends: []AgentBackend{
			{ID: "agy", Framework: AgentFrameworkAntigravity, Models: []string{"gemini-3.1-pro"}},
		},
	}).EffectiveDefaultModel(); got != "gemini-3.1-pro" {
		t.Errorf("EffectiveDefaultModel with first default unserved = %q, want gemini-3.1-pro", got)
	}
}
