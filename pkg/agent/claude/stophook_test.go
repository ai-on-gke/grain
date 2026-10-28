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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
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
		in          stopHookInput
		interactive bool
		session     string
		block       bool
		reason      string
	}{
		{"session answer", stopHookInput{HookEventName: "Stop", TranscriptPath: "t"}, true, answered, false, ""},
		{"session promise, read from the transcript", stopHookInput{HookEventName: "Stop", TranscriptPath: "t"}, true, readOnly, true, agent.SessionContinueReason},
		{"session promise, from last_assistant_message", stopHookInput{HookEventName: "Stop", LastAssistantMessage: "Let me look at that."}, true, "", true, agent.SessionContinueReason},
		{"session promise, already continued", stopHookInput{HookEventName: "Stop", StopHookActive: true, TranscriptPath: "t"}, true, readOnly, false, ""},
		{"background with no closing move", stopHookInput{HookEventName: "Stop", TranscriptPath: "t"}, false, answered, true, agent.BackgroundContinueReason},
		{"background that pushed", stopHookInput{HookEventName: "Stop", TranscriptPath: "t"}, false, pushed, false, ""},
		{"background, already continued", stopHookInput{HookEventName: "Stop", StopHookActive: true, TranscriptPath: "t"}, false, answered, false, ""},
		{"another event", stopHookInput{HookEventName: "SubagentStop", TranscriptPath: "t"}, false, answered, false, ""},
	}
	for _, c := range cases {
		d, block := stopHookDecision(c.in, c.interactive, read(c.session))
		if block != c.block || d.Reason != c.reason {
			t.Errorf("%s: got block=%v reason=%q, want block=%v reason=%q", c.name, block, d.Reason, c.block, c.reason)
		}
	}
}

// TestServeStopHook is the subcommand end to end: payload in, decision
// out, block recorded -- and nothing at all written for a stop it allows.
func TestServeStopHook(t *testing.T) {
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
	ServeStopHook([]string{"-interactive", "-record", record}, bytes.NewReader(payload), &out, &errOut)
	var got stopHookOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	if got.Decision != "block" || got.Reason != agent.SessionContinueReason {
		t.Errorf("output = %+v", got)
	}
	recorded, _ := os.ReadFile(record)
	var b agent.StopBlock
	if err := json.Unmarshal(bytes.TrimSpace(recorded), &b); err != nil || b.Via != agent.StopViaHook || b.Reason != agent.SessionContinueReason {
		t.Errorf("record = %q (%v)", recorded, err)
	}

	// The continuation's own stop: allowed, silently, and not recorded.
	payload, _ = json.Marshal(map[string]any{"hook_event_name": "Stop", "stop_hook_active": true, "transcript_path": transcript})
	out.Reset()
	ServeStopHook([]string{"-interactive", "-record", record}, bytes.NewReader(payload), &out, &errOut)
	if out.Len() != 0 {
		t.Errorf("stop_hook_active wrote %q, want nothing", out.String())
	}
	if again, _ := os.ReadFile(record); !bytes.Equal(again, recorded) {
		t.Errorf("record grew on an allowed stop: %q", again)
	}

	// Garbage in: nothing out, never a block.
	out.Reset()
	ServeStopHook(nil, strings.NewReader("not json"), &out, &errOut)
	if out.Len() != 0 {
		t.Errorf("bad payload wrote %q", out.String())
	}
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
