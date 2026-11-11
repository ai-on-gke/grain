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

package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/claude/stophook"
	"github.com/ai-on-gke/grain/pkg/agent/hookpolicy"
)

// sessionLines is a Claude Code session transcript: the same assistant
// and user line shapes as stream-json.
func sessionLines(t *testing.T, lines ...map[string]any) string {
	t.Helper()
	var b strings.Builder
	for _, l := range lines {
		data, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String()
}

func hookAssistantText(text string) map[string]any {
	return map[string]any{"type": "assistant", "message": map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}}}
}

func hookAssistantTool(id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "assistant", "message": map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}}
}

func hookToolResult(id, text string, isError bool) map[string]any {
	return map[string]any{"type": "user", "message": map[string]any{
		"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": text, "is_error": isError}}}}
}

func TestStopHookSettings(t *testing.T) {
	settings, err := stopHookSettings("/opt/grain it's", true, "/tmp/rec.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Hooks struct {
			Stop []struct {
				Hooks []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"Stop"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(settings), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Hooks.Stop) != 1 || len(parsed.Hooks.Stop[0].Hooks) != 1 {
		t.Fatalf("settings = %s", settings)
	}
	h := parsed.Hooks.Stop[0].Hooks[0]
	want := `'/opt/grain it'\''s' claude-stop-hook -interactive -record '/tmp/rec.jsonl'`
	if h.Type != "command" || h.Command != want {
		t.Errorf("hook = %+v, want command %q", h, want)
	}
}

func TestWithStopBlocks(t *testing.T) {
	r := &agent.Result{Transcript: "work"}
	b := agent.StopBlock{Via: agent.StopViaHook, Reason: agent.SessionContinueReason}
	withStopBlocks(r, []agent.StopBlock{b})
	if len(r.StopBlocks) != 1 || !strings.HasSuffix(r.Transcript, agent.StopNote(b)) {
		t.Errorf("result = %+v", r)
	}
	withStopBlocks(nil, []agent.StopBlock{b}) // must not panic
}

// TestStopHookParsesSessionsAsParseEventsDoes holds the leaf hook's own
// session reader (stophook.ParseSession) to this package's parser: the
// same tool calls, by the same bare names, with the same error flags, and
// the same last text. The hook keeps its own copy so grain-agentd need not
// link this package; this is what keeps the copy honest.
func TestStopHookParsesSessionsAsParseEventsDoes(t *testing.T) {
	session := sessionLines(t,
		map[string]any{"type": "system", "subtype": "init"},
		hookAssistantTool("t1", "mcp__grain-sandbox__run_command", map[string]any{"command": "git push origin b"}),
		hookToolResult("t1", "rejected", true),
		hookAssistantTool("t2", "mcp_grain-sandbox_comment_on_issue", map[string]any{"comment": "done"}),
		hookToolResult("t2", "ok", false),
		hookAssistantTool("t3", "Bash", map[string]any{"command": "ls"}),
		map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "a plain string turn"}},
		hookAssistantText("Pushed the fix."),
	) + "not json\n"
	calls, last := stophook.ParseSession(session)
	p := parseEvents(session)
	if want := lastAssistantText(p); last != want {
		t.Errorf("last text = %q, parseEvents says %q", last, want)
	}
	if len(calls) != len(p.result.ToolCalls) {
		t.Fatalf("ParseSession found %d calls, parseEvents %d", len(calls), len(p.result.ToolCalls))
	}
	for i, c := range calls {
		w := p.result.ToolCalls[i]
		if c.Name != w.Name || c.IsError != w.IsError || len(c.Arguments) != len(w.Arguments) {
			t.Errorf("call %d = %+v, parseEvents says %+v", i, c, w)
		}
	}
	if stophook.Subcommand != StopHookSubcommand {
		t.Errorf("stophook.Subcommand = %q, want %q", stophook.Subcommand, StopHookSubcommand)
	}
}

func TestStopHookPolicyStagedOnlyForHostsWithFeatureHookPolicy(t *testing.T) {
	ctx := context.Background()
	t.Setenv("TMPDIR", t.TempDir())

	// Capable host (FeatureHookPolicy): stages policy file and passes -policy.
	capable := agent.HostdHost{HookPolicy: true}
	policyPath, err := stageStopPolicy(ctx, capable, false)
	if err != nil || policyPath == "" {
		t.Fatalf("stageStopPolicy(capable) = %q, %v", policyPath, err)
	}
	defer os.Remove(policyPath)
	file, err := hookpolicy.Load(policyPath)
	if err != nil || file == nil || file.Stop == nil {
		t.Fatalf("Load(%s) = %+v, %v", policyPath, file, err)
	}
	settings, err := stopHookSettingsWithPolicy("/opt/grain-agentd", false, "/tmp/rec.jsonl", policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(settings, "-policy '"+policyPath+"'") {
		t.Errorf("capable settings = %s, want -policy %s", settings, policyPath)
	}

	// Evaluating stophook.Run with the staged policy allows resume_later on success.
	tp := policyPath + ".transcript.jsonl"
	if err := os.WriteFile(tp, []byte(sessionLines(t,
		hookAssistantTool("t1", "mcp__grain-sandbox__resume_later", map[string]any{"reason": "waiting on CI"}),
		hookToolResult("t1", "parked", false),
	)), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "transcript_path": tp})
	var out, errOut bytes.Buffer
	ServeStopHook([]string{"-policy", policyPath}, bytes.NewReader(payload), &out, &errOut)
	if out.Len() != 0 {
		t.Errorf("resume_later with staged policy blocked: %s", out.String())
	}

	// Older host without FeatureHookPolicy: stages nothing and omits -policy so
	// an older agentd's flag.FlagSet does not reject an unknown flag.
	older := agent.HostdHost{HookPolicy: false}
	oldPolicy, err := stageStopPolicy(ctx, older, false)
	if err != nil || oldPolicy != "" {
		t.Fatalf("stageStopPolicy(older) = %q, %v; want empty", oldPolicy, err)
	}
	oldSettings, err := stopHookSettingsWithPolicy("/opt/old-agentd", false, "/tmp/rec.jsonl", oldPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(oldSettings, "-policy") {
		t.Errorf("older settings = %s, must not contain -policy", oldSettings)
	}
}

// lastAssistantText is the last text block of a parsed session.
func lastAssistantText(p *parsedEvents) string {
	for i := len(p.items) - 1; i >= 0; i-- {
		if p.items[i].Kind == agent.ItemText {
			return p.items[i].Text
		}
	}
	return ""
}
