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

package stophook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent/hookpolicy"
	"github.com/ai-on-gke/grain/pkg/agent/stoprule"
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

func TestStopHookDecision(t *testing.T) {
	read := func(lines string) func(string) string {
		return func(string) string { return lines }
	}
	pushed := sessionLines(t,
		hookAssistantTool("t1", "mcp__grain-sandbox__run_command", map[string]any{"command": "git push origin b"}),
		hookToolResult("t1", "pushed", false),
		hookAssistantText("Pushed the fix."))
	readOnly := sessionLines(t,
		hookAssistantTool("t1", "mcp__grain-sandbox__read_file", map[string]any{"file_path": "x"}),
		hookToolResult("t1", "contents", false),
		hookAssistantText("Checking the CI log now, back in a minute."))
	answered := sessionLines(t, hookAssistantText("The queue is clear; nothing is waiting."))

	cases := []struct {
		name        string
		in          input
		interactive bool
		session     string
		block       bool
		reason      string
	}{
		{"session answer", input{HookEventName: "Stop", TranscriptPath: "t"}, true, answered, false, ""},
		{"session promise, read from the transcript", input{HookEventName: "Stop", TranscriptPath: "t"}, true, readOnly, true, stoprule.SessionContinueReason},
		{"session promise, from last_assistant_message", input{HookEventName: "Stop", LastAssistantMessage: "Let me look at that."}, true, "", true, stoprule.SessionContinueReason},
		{"session promise, already continued", input{HookEventName: "Stop", StopHookActive: true, TranscriptPath: "t"}, true, readOnly, false, ""},
		{"background with no closing move", input{HookEventName: "Stop", TranscriptPath: "t"}, false, answered, true, stoprule.BackgroundContinueReason},
		{"background that pushed", input{HookEventName: "Stop", TranscriptPath: "t"}, false, pushed, false, ""},
		{"background, already continued", input{HookEventName: "Stop", StopHookActive: true, TranscriptPath: "t"}, false, answered, false, ""},
		{"another event", input{HookEventName: "SubagentStop", TranscriptPath: "t"}, false, answered, false, ""},
	}
	for _, c := range cases {
		d, block := decide(c.in, c.interactive, read(c.session))
		if block != c.block || d.Reason != c.reason {
			t.Errorf("%s: got block=%v reason=%q, want block=%v reason=%q", c.name, block, d.Reason, c.block, c.reason)
		}
	}
}

// TestRun is the subcommand end to end: payload in, decision
// out, block recorded -- and nothing at all written for a stop it allows.
func TestRun(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(transcript, []byte(sessionLines(t, hookAssistantText("I'll keep going."))), 0o600); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "record.jsonl")
	if err := os.WriteFile(record, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "stop_hook_active": false, "transcript_path": transcript})

	var out, errOut bytes.Buffer
	Run([]string{"-interactive", "-record", record}, bytes.NewReader(payload), &out, &errOut)
	var got output
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	if got.Decision != "block" || got.Reason != stoprule.SessionContinueReason {
		t.Errorf("output = %+v", got)
	}
	recorded, _ := os.ReadFile(record)
	var b stoprule.Block
	if err := json.Unmarshal(bytes.TrimSpace(recorded), &b); err != nil || b.Via != stoprule.ViaHook || b.Reason != stoprule.SessionContinueReason {
		t.Errorf("record = %q (%v)", recorded, err)
	}

	// The continuation's own stop: allowed, silently, and not recorded.
	payload, _ = json.Marshal(map[string]any{"hook_event_name": "Stop", "stop_hook_active": true, "transcript_path": transcript})
	out.Reset()
	Run([]string{"-interactive", "-record", record}, bytes.NewReader(payload), &out, &errOut)
	if out.Len() != 0 {
		t.Errorf("stop_hook_active wrote %q, want nothing", out.String())
	}
	if again, _ := os.ReadFile(record); !bytes.Equal(again, recorded) {
		t.Errorf("record grew on an allowed stop: %q", again)
	}

	// Garbage in: nothing out, never a block.
	out.Reset()
	Run(nil, strings.NewReader("not json"), &out, &errOut)
	if out.Len() != 0 {
		t.Errorf("bad payload wrote %q", out.String())
	}
}

func TestRunEvaluatesStagedStopPolicyAndFallsBackWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, hookpolicy.Filename)
	raw, err := hookpolicy.Marshal(hookpolicy.File{
		Version: hookpolicy.Version,
		Stop: &hookpolicy.StopPolicy{
			ToolPrefixes:             []string{"mcp__custom-ns__"},
			SessionContinueReason:    "custom session nudge",
			BackgroundContinueReason: "custom background nudge",
			ClosingTools:             []string{"future_finish"},
			ClosingCalls: []hookpolicy.ClosingCallRule{
				{Tool: "future_suspend", RequireSuccess: true},
				{Tool: "future_park", ArgBoolTrue: "hold"},
			},
			PromisePhrases:  []string{"brb checking"},
			PromisePrefixes: []string{"hold on "},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	runWithTranscript := func(args []string, lines string) string {
		t.Helper()
		tp := filepath.Join(dir, "session.jsonl")
		if err := os.WriteFile(tp, []byte(lines), 0o600); err != nil {
			t.Fatal(err)
		}
		p, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "transcript_path": tp})
		var out, errOut bytes.Buffer
		Run(args, bytes.NewReader(p), &out, &errOut)
		return strings.TrimSpace(out.String())
	}

	// Compiled-in comment_on_issue is NOT in the staged ClosingTools, so background stops block
	// with the staged nudge -- proving compiled-in closingTools were not consulted.
	got := runWithTranscript([]string{FlagPolicy, policyPath}, sessionLines(t,
		hookAssistantTool("t1", "mcp__custom-ns__comment_on_issue", map[string]any{"comment": "hi"}),
		hookToolResult("t1", "ok", false),
		hookAssistantText("Done."),
	))
	if !strings.Contains(got, `"reason":"custom background nudge"`) {
		t.Errorf("omitted closing tool = %q, want custom background nudge", got)
	}

	// Staged future_finish and non-errored future_suspend close the background run.
	if got := runWithTranscript([]string{FlagPolicy, policyPath}, sessionLines(t,
		hookAssistantTool("t1", "mcp__custom-ns__future_finish", nil),
		hookToolResult("t1", "ok", false),
	)); got != "" {
		t.Errorf("future_finish blocked: %q", got)
	}
	if got := runWithTranscript([]string{FlagPolicy, policyPath}, sessionLines(t,
		hookAssistantTool("t1", "mcp__custom-ns__future_suspend", nil),
		hookToolResult("t1", "dirty", true),
	)); !strings.Contains(got, "custom background nudge") {
		t.Errorf("errored future_suspend = %q, want block", got)
	}
	if got := runWithTranscript([]string{FlagPolicy, policyPath}, sessionLines(t,
		hookAssistantTool("t1", "mcp__custom-ns__future_suspend", nil),
		hookToolResult("t1", "ok", false),
	)); got != "" {
		t.Errorf("succeeded future_suspend blocked: %q", got)
	}

	// Interactive promise uses the staged phrases and nudge.
	if got := runWithTranscript([]string{"-interactive", FlagPolicy, policyPath}, sessionLines(t,
		hookAssistantText("Hold on while I check."),
	)); !strings.Contains(got, `"reason":"custom session nudge"`) {
		t.Errorf("interactive custom prefix = %q, want custom session nudge", got)
	}

	// Absent policy file falls back to compiled-in DefaultPolicy.
	missing := filepath.Join(dir, "absent.json")
	if got := runWithTranscript([]string{FlagPolicy, missing}, sessionLines(t,
		hookAssistantTool("t1", "mcp__grain-sandbox__comment_on_issue", map[string]any{"comment": "done"}),
		hookToolResult("t1", "ok", false),
	)); got != "" {
		t.Errorf("absent policy fallback blocked comment_on_issue: %q", got)
	}
}
