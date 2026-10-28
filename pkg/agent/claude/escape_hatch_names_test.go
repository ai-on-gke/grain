package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/mcp"
)

// TestEscapeHatchToolCallsAreRecordedUnderTheirBareNames is the
// regression test for the bug that made ask_question and
// comment_on_issue do nothing at all on a real run.
//
// claude reports every tool it loaded from --mcp-config as
// "mcp__grain-sandbox__<tool>" -- this package's own allowedTools writes
// that prefix, and --strict-mcp-config means claude will not admit a tool
// under any other name. The parser recorded that name verbatim, while
// orchestrator.ProcessResult (finish.go) decides what a run asked for by
// comparing ToolCall.Name against "ask_question", "comment_on_issue" and
// "propose_task" exactly. So a run that asked the human a question was
// recorded no_action instead: no comment was relayed, no
// PendingQuestionCommentID was parked, and the question was never asked.
//
// Every existing test of that path scripted the bare name, which is why
// nothing caught it -- so this one uses the prefixed name a real claude
// actually emits, and asserts on the names finish.go really matches.
func TestEscapeHatchToolCallsAreRecordedUnderTheirBareNames(t *testing.T) {
	for _, tt := range []struct {
		tool string
		args map[string]any
	}{
		{"ask_question", map[string]any{"question": "which config file?"}},
		{"comment_on_issue", map[string]any{"comment": "the answer is 4"}},
		{"propose_task", map[string]any{"title": "follow-up", "body": "the rest of it"}},
		{"request_secret", map[string]any{"secret": "stripe-api-key", "reason": "the deploy needs it"}},
	} {
		t.Run(tt.tool, func(t *testing.T) {
			fake := &fakeRunner{stdout: strings.Join([]string{
				streamJSONLine(t, map[string]any{
					"type": "assistant",
					"message": map[string]any{
						"content": []map[string]any{{
							"type": "tool_use", "id": "call-1",
							"name":  mcp.QualifiedToolName(tt.tool),
							"input": tt.args,
						}},
					},
				}),
				streamJSONLine(t, map[string]any{"type": "result", "result": "ok"}),
			}, "\n")}

			result, err := newFramework(fake, "grain-path").Run(context.Background(),
				agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.ToolCalls) != 1 {
				t.Fatalf("ToolCalls = %+v, want 1", result.ToolCalls)
			}
			if got := result.ToolCalls[0].Name; got != tt.tool {
				t.Errorf("ToolCalls[0].Name = %q, want %q -- orchestrator.ProcessResult "+
					"matches the bare name exactly, so anything else relays nothing", got, tt.tool)
			}
			// The arguments have to survive the same trip: finish.go reads
			// the question/comment straight off this map.
			for k, want := range tt.args {
				if got := result.ToolCalls[0].Arguments[k]; got != want {
					t.Errorf("Arguments[%q] = %v, want %v", k, got, want)
				}
			}
		})
	}
}

// TestTranscriptNarrativeNamesToolsBarely keeps the human-readable
// narrative and ToolCalls telling the same story: a reader of a run's
// transcript should see the tool the agent called, not claude's
// namespaced spelling of it.
func TestTranscriptNarrativeNamesToolsBarely(t *testing.T) {
	stdout := strings.Join([]string{
		streamJSONLine(t, map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{
					"type": "tool_use", "id": "call-1",
					"name":  mcp.QualifiedToolName("run_command"),
					"input": map[string]any{"command": "ls"},
				}},
			},
		}),
		streamJSONLine(t, map[string]any{"type": "result", "result": "ok"}),
	}, "\n")

	got, err := parseTranscript(stdout)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Transcript, "> run_command(") {
		t.Errorf("Transcript = %q, want a %q line", got.Transcript, "> run_command(")
	}
	if strings.Contains(got.Transcript, "mcp__") {
		t.Errorf("Transcript = %q, want no mcp__ prefix in it", got.Transcript)
	}
}
