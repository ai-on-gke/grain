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

import "testing"

// The judgement model resolves to the deployment's own choice when it has
// one, and otherwise to the strongest model its enabled connections offer,
// whatever each connection calls it (design/design/durable-tasks.design.md#DT-11).
func TestTheJudgementModelResolvesToTheStrongestOfferedModel(t *testing.T) {
	t.Parallel()
	gemini := AgentBackend{ID: "agy", Framework: AgentFrameworkAntigravity, Models: []string{"gemini-pro-coordinator", "opus-5.5-medium"}}
	opus := AgentBackend{ID: "agy2", Framework: AgentFrameworkAntigravity, Models: []string{"opus-5.5"}}
	agyCatalog := AgentBackend{ID: "agy-catalog", Framework: AgentFrameworkAntigravity, Models: []string{"gemini-3.8-flash-high", "gemini-4-argon-medium", "gemini-4-argon-high", "gemini-3.1-pro-high"}}
	agyMedium := AgentBackend{ID: "agy-medium", Framework: AgentFrameworkAntigravity, Models: []string{"gemini-3.8-flash-high", "gemini-4-argon-medium", "gemini-3.1-pro-high"}}
	agyPro := AgentBackend{ID: "agy-pro", Framework: AgentFrameworkAntigravity, Models: []string{"gemini-3.8-flash-high", "gemini-3.1-pro-high", "gemini-3.1-pro-low"}}
	off := AgentBackend{ID: "claude", Framework: AgentFrameworkClaude, Models: []string{"claude-opus-5-5"}, Disabled: true}

	for _, tc := range []struct {
		name     string
		cfg      Config
		backends []AgentBackend
		want     string
		ok       bool
	}{
		{"Opus 5.5 first", Config{}, []AgentBackend{gemini, opus}, "opus-5.5", true},
		{"medium effort beats the rotation's other half", Config{}, []AgentBackend{gemini}, "opus-5.5-medium", true},
		{"Antigravity catalog resolves argon high before medium and 3.1 pro", Config{}, []AgentBackend{agyCatalog}, "gemini-4-argon-high", true},
		{"Antigravity catalog resolves argon medium before 3.1 pro", Config{}, []AgentBackend{agyMedium}, "gemini-4-argon-medium", true},
		{"Antigravity catalog resolves 3.1 pro high over flash", Config{}, []AgentBackend{agyPro}, "gemini-3.1-pro-high", true},
		{"a disabled connection offers nothing", Config{}, []AgentBackend{off}, "", false},
		{"the deployment's own choice wins", Config{JudgementModel: "claude-sonnet-5-5"}, []AgentBackend{opus}, "claude-sonnet-5-5", true},
		{"nothing ranked", Config{}, []AgentBackend{{ID: "x", Models: []string{"mystery"}}}, "", false},
	} {
		got, ok := ResolveJudgementModel(tc.cfg, tc.backends)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: ResolveJudgementModel = %q, %v; want %q, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}

	// ResolveJudgementModels returns all matching ranked models in ranking order.
	models := ResolveJudgementModels(Config{}, []AgentBackend{gemini, opus})
	if len(models) != 2 || models[0] != "opus-5.5" || models[1] != "opus-5.5-medium" {
		t.Errorf("ResolveJudgementModels(gemini, opus) = %v; want [opus-5.5, opus-5.5-medium]", models)
	}

	// Config.JudgementModel is placed first, followed by ranked models.
	withChoice := ResolveJudgementModels(Config{JudgementModel: "custom-judge"}, []AgentBackend{gemini, opus})
	if len(withChoice) != 3 || withChoice[0] != "custom-judge" || withChoice[1] != "opus-5.5" || withChoice[2] != "opus-5.5-medium" {
		t.Errorf("ResolveJudgementModels(with choice) = %v; want [custom-judge, opus-5.5, opus-5.5-medium]", withChoice)
	}

	if !IsJudgementModel(" Judgement ") || IsJudgementModel("opus-5.5") {
		t.Error("IsJudgementModel does not recognise the symbol alone")
	}
}
