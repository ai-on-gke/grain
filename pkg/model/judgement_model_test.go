package model

import "testing"

// The judgement model resolves to the deployment's own choice when it has
// one, and otherwise to the strongest model its enabled connections offer,
// whatever each connection calls it (docs/design/durable-tasks.md#DT-11).
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
