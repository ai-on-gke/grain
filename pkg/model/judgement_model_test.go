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
		{"a disabled connection offers nothing", Config{}, []AgentBackend{off}, "", false},
		{"the deployment's own choice wins", Config{JudgementModel: "claude-sonnet-5-5"}, []AgentBackend{opus}, "claude-sonnet-5-5", true},
		{"nothing ranked", Config{}, []AgentBackend{{ID: "x", Models: []string{"mystery"}}}, "", false},
	} {
		got, ok := ResolveJudgementModel(tc.cfg, tc.backends)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: ResolveJudgementModel = %q, %v; want %q, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
	if !IsJudgementModel(" Judgement ") || IsJudgementModel("opus-5.5") {
		t.Error("IsJudgementModel does not recognise the symbol alone")
	}
}
