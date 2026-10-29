// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package model

import (
	"encoding/json"
	"slices"
	"strings"
)

// A connection and a model are two different things, and this file is
// where grain keeps them apart.
//
// A connection (Connection, stored as Config.AgentBackends) is a way to
// reach a provider: a framework, an authentication, a team, a
// concurrency limit and an enabled flag. It advertises the models it can
// serve (AgentBackend.Models), and that list is a capability, not a
// choice: nothing about a connection says which model a run on it uses.
//
// A model is what the deployment, a task or a proposal chooses
// (Config.DefaultModel, Task.Model), with the deployment's own per-model
// settings (Config.ModelSettings). Dispatch asks for a model and round-robins across
// every enabled connection of the task's team that advertises it; a task
// that pinned a connection (Task.AgentBackendID) skips that rotation.

// ModelWaitCapability is the GrantWait capability a task held at
// admission for want of a connection is recorded under; its Resource is
// the whole sentence saying why (orchestrator.Deps.ModelAvailability).
const ModelWaitCapability = "model"

// TeamSetupWaitCapability is the GrantWait capability a task held for
// something its team has yet to set up is recorded under -- a provider
// connection, or a GitHub credential for its target
// (orchestrator.heldForTeamSetup). Like ModelWaitCapability, its
// Resource is the whole sentence, naming where in Team settings the
// team admin sets it.
const TeamSetupWaitCapability = "team-setup"

// BranchWaitCapability is the GrantWait capability a task held for a
// base branch grain has been asked to create, and has not yet, is
// recorded under (orchestrator.heldForPendingBranch, grain/task-2317).
// Like the two above, its Resource is the whole sentence.
const BranchWaitCapability = "branch"

// PolicyWaitCapability is the GrantWait capability a task the
// workstation's local policy refuses is held under
// (orchestrator.heldByPolicy, pkg/policy). Its Resource is the whole
// sentence: which claim the policy refused. The task stays queued, and
// is dispatched by the first cycle after the policy allows it.
const PolicyWaitCapability = "local-policy"

// CatalogueWaitCapability is the GrantWait capability a task held by the
// capability catalogue (settings/capabilities.yml, CapabilityCatalogue)
// is recorded under; its Resource is the refusal. A config change that
// revoked or narrowed a grant after the task was filed holds it here
// rather than handing its sandbox what the catalogue now forbids.
const CatalogueWaitCapability = "capability-catalogue"

// QuotaWaitCapability is the GrantWait capability a task held back by
// host sandbox resource budget, worker/merger capacity limits, or a
// provider usage-limit pause is recorded under. Like ModelWaitCapability,
// its Resource is the whole sentence saying why.
const QuotaWaitCapability = "quota"

// ModelSetting is the deployment's own configuration for one model: what it is
// asked for whichever connection ends up serving it.
type ModelSetting struct {
	Model string `json:"model"`
	// Effort is the reasoning effort the model is asked for, in its own
	// framework's vocabulary ("low", "medium", "high", ...). Empty leaves
	// it to the framework's default.
	Effort string `json:"effort,omitempty"`
}

// EncodeModelSettings is the grain_config.model_settings column's encoding.
func EncodeModelSettings(v []ModelSetting) string {
	if len(v) == 0 {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// DecodeModelSettings reads grain_config.model_settings back. A value
// that does not parse reads as no settings rather than failing the read: these
// are preferences, and every model has a framework default to fall back on.
func DecodeModelSettings(v string) []ModelSetting {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	var out []ModelSetting
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil
	}
	return out
}

// SettingFor is the deployment's own setting for model, or a zero
// setting naming it when it has none.
func (c Config) SettingFor(model string) ModelSetting {
	s, _ := c.LookupModelSetting(model)
	return s
}

// ParseDefaultModels splits a default_model string into the ordered list
// of models to try, first to last, dropping blanks and case-insensitive
// duplicates while keeping the first-seen spelling.
func ParseDefaultModels(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		m := strings.TrimSpace(part)
		if m == "" {
			continue
		}
		if !slices.ContainsFunc(out, func(have string) bool { return strings.EqualFold(have, m) }) {
			out = append(out, m)
		}
	}
	return out
}

// JoinDefaultModels formats a list of default models for Config.DefaultModel.
func JoinDefaultModels(models []string) string {
	return strings.Join(ParseDefaultModels(strings.Join(models, ",")), ",")
}

// NormalizeDefaultModel cleans a comma-separated default model string.
func NormalizeDefaultModel(raw string) string {
	return strings.Join(ParseDefaultModels(raw), ",")
}

// DefaultModels is the ordered list of deployment-wide default models
// configured in DefaultModel, tried from first to last.
func (c Config) DefaultModels() []string {
	return ParseDefaultModels(c.DefaultModel)
}

// LookupModelSetting is SettingFor, plus whether the deployment has a
// setting for model at all. A setting with an empty effort is the
// deployment saying "Model default", which a run honours by passing no
// effort, rather than falling back to an effort configured anywhere else.
func (c Config) LookupModelSetting(model string) (ModelSetting, bool) {
	model = strings.TrimSpace(model)
	for _, s := range c.ModelSettings {
		if strings.EqualFold(strings.TrimSpace(s.Model), model) {
			return s, true
		}
	}
	return ModelSetting{Model: model}, false
}

// addModelSetting adds a setting for s.Model unless the deployment
// already has one, which wins: it is the deployment's own, newer word on
// that model.
func (c *Config) addModelSetting(s ModelSetting) bool {
	if strings.TrimSpace(s.Model) == "" {
		return false
	}
	for _, have := range c.ModelSettings {
		if strings.EqualFold(have.Model, s.Model) {
			return false
		}
	}
	c.ModelSettings = append(c.ModelSettings, s)
	return true
}

// ConnectionTeam is the team a connection belongs to, with an unset team
// read as the default team's, the same as everywhere else.
func ConnectionTeam(b AgentBackend) string {
	if team := strings.TrimSpace(b.Team); team != "" {
		return team
	}
	return DefaultTeamID
}

// ModelFramework is the framework model runs on for team: the framework
// of the first of the team's connections that advertises it, or -- for a
// model none of them does -- the framework its name belongs to, or "" for
// a name grain cannot place. A model implies its framework, which is why
// a task that chose a model no longer needs to choose a framework too.
func ModelFramework(backends []AgentBackend, team, model string) string {
	for _, b := range backends {
		// Any of the deployment's connections, whichever team saved it
		// (DeploymentAgentBackends); team chooses nothing.
		if b.SupportsModel(model) {
			return NormalizeAgentFrameworkName(b.Framework)
		}
	}
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(m, "claude"):
		return AgentFrameworkClaude
	case strings.HasPrefix(m, "gpt") || strings.HasPrefix(m, "codex") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4"):
		return AgentFrameworkCodex
	case strings.HasPrefix(m, "gemini"):
		return AgentFrameworkAntigravity
	}
	return ""
}

// LegacyFrameworkModels is the per-framework model and effort fields the
// default team's row carried until schema 24 (claude_model, codex_model,
// gemini_model, codex_effort, gemini_effort), and its agent_framework,
// which said which of them was its default.
type LegacyFrameworkModels struct {
	AgentFramework                       string
	ClaudeModel, CodexModel, GeminiModel string
	CodexEffort, GeminiEffort            string
}

// WithLegacyFrameworkModels is c with t folded into DefaultModel and
// ModelSettings, as dispatch folded the default team's fields until
// schema 24 dropped the row; the state repository's fixup past 23 folds
// them once, for good (staterepo's default_team_row.go). The
// deployment's own DefaultModel and settings win over anything folded in.
func (c Config) WithLegacyFrameworkModels(t LegacyFrameworkModels) Config {
	c.ModelSettings = slices.Clone(c.ModelSettings)
	if t.ClaudeModel == "" && t.CodexModel == "" && t.GeminiModel == "" &&
		t.CodexEffort == "" && t.GeminiEffort == "" {
		return c
	}
	byFramework := map[string]string{
		AgentFrameworkClaude:      strings.TrimSpace(t.ClaudeModel),
		AgentFrameworkCodex:       strings.TrimSpace(t.CodexModel),
		AgentFrameworkAntigravity: strings.TrimSpace(t.GeminiModel),
	}
	if strings.TrimSpace(c.DefaultModel) == "" {
		want := NormalizeAgentFrameworkName(t.AgentFramework)
		if m := byFramework[want]; m != "" {
			c.DefaultModel = m
		} else {
			for _, f := range []string{AgentFrameworkClaude, AgentFrameworkCodex, AgentFrameworkAntigravity} {
				if byFramework[f] != "" {
					c.DefaultModel = byFramework[f]
					break
				}
			}
		}
	}
	if t.CodexEffort != "" && byFramework[AgentFrameworkCodex] != "" {
		c.addModelSetting(ModelSetting{Model: byFramework[AgentFrameworkCodex], Effort: strings.TrimSpace(t.CodexEffort)})
	}
	if t.GeminiEffort != "" && byFramework[AgentFrameworkAntigravity] != "" {
		c.addModelSetting(ModelSetting{Model: byFramework[AgentFrameworkAntigravity], Effort: strings.TrimSpace(t.GeminiEffort)})
	}
	return c
}
