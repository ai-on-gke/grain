package daemon

import (
	"context"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent/antigravity"
	"github.com/bwsalmon/grain/pkg/agent/codex"
	"github.com/bwsalmon/grain/pkg/model"
)

// A run's effort is its chosen model's own: the team's setting for that
// model, "Model default" included, and never the deployment-wide
// gemini_effort / codex_effort, which pair with the deployment's own
// model. A model that takes no effort gets none whatever is configured.
func TestBuildOnEffortIsTheChosenModelsOwn(t *testing.T) {
	agy := model.AgentBackend{ID: "agy", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, Models: []string{"gemini-3.8-flash", "auto", "gemini-3.8-flash-high"}}
	for name, tc := range map[string]struct {
		model    string
		settings []model.ModelSetting
		backend  *model.AgentBackend
		want     string
	}{
		"model default inherits no deployment effort": {model: "gemini-3.8-flash", want: ""},
		"explicit model default inherits no deployment effort": {model: "gemini-3.8-flash",
			settings: []model.ModelSetting{{Model: "gemini-3.8-flash"}}, want: ""},
		"team effort": {model: "gemini-3.8-flash",
			settings: []model.ModelSetting{{Model: "gemini-3.8-flash", Effort: "low"}}, want: "low"},
		"auto takes none": {model: "auto",
			settings: []model.ModelSetting{{Model: "auto", Effort: "low"}}, want: ""},
		"suffixed model takes none": {model: "gemini-3.8-flash-high",
			settings: []model.ModelSetting{{Model: "gemini-3.8-flash-high", Effort: "low"}}, want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			backend := agy
			if tc.backend != nil {
				backend = *tc.backend
			}
			team := &model.Config{ModelSettings: tc.settings}
			live := Config{AgyPath: "/bin/true", GeminiModel: "gemini-3.1-pro", GeminiEffort: "high"}
			built, err := (&AgentPool{}).buildOn(context.Background(), live, "", &backend, tc.model, team, nil)
			if err != nil {
				t.Fatalf("buildOn: %v", err)
			}
			f, ok := built.(*antigravity.Framework)
			if !ok {
				t.Fatalf("buildOn built %T, want an antigravity framework", built)
			}
			if got := f.Effort(); got != tc.want {
				t.Errorf("--effort = %q, want %q", got, tc.want)
			}
		})
	}
}

// A run that chose no model runs the provider's default, and no
// deployment-wide model or effort stands behind it.
func TestBuildOnWithNoChosenModelAsksForNone(t *testing.T) {
	live := Config{AgyPath: "/bin/true", GeminiModel: "gemini-3.1-pro", GeminiEffort: "high",
		AgentAuthMode: model.AntigravityAuthAmbient}
	built, err := (&AgentPool{}).buildOn(context.Background(), live, model.AgentFrameworkAntigravity, nil, "", &model.Config{}, nil)
	if err != nil {
		t.Fatalf("buildOn: %v", err)
	}
	if got := built.(*antigravity.Framework).Effort(); got != "" {
		t.Errorf("--effort = %q, want none", got)
	}
}

func TestBuildOnCodexEffortIsTheChosenModelsOwn(t *testing.T) {
	for name, tc := range map[string]struct {
		settings []model.ModelSetting
		want     string
	}{
		"model default inherits no deployment effort": {want: ""},
		"explicit model default inherits no deployment effort": {
			settings: []model.ModelSetting{{Model: "gpt-5.5"}}, want: ""},
		"team effort": {
			settings: []model.ModelSetting{{Model: "gpt-5.5", Effort: "low"}}, want: "low"},
	} {
		t.Run(name, func(t *testing.T) {
			backend := model.AgentBackend{ID: "codex", Framework: model.AgentFrameworkCodex,
				AuthMode: model.AntigravityAuthAmbient, Models: []string{"gpt-5.5"}}
			live := Config{CodexPath: "/bin/true", CodexModel: "gpt-5", CodexEffort: "high"}
			built, err := (&AgentPool{}).buildOn(context.Background(), live, "", &backend, "gpt-5.5",
				&model.Config{ModelSettings: tc.settings}, nil)
			if err != nil {
				t.Fatalf("buildOn: %v", err)
			}
			f, ok := built.(*codex.Framework)
			if !ok {
				t.Fatalf("buildOn built %T, want a codex framework", built)
			}
			if got := f.Effort(); got != tc.want {
				t.Errorf("model_reasoning_effort = %q, want %q", got, tc.want)
			}
		})
	}
}
