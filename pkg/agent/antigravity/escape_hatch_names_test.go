package antigravity

import (
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/mcp"
)

// TestEscapeHatchToolCallsAreRecordedUnderTheirBareNames is
// agent/claude's test of the same name, for agy's own step_update
// vocabulary, and it now covers both of the two names a real agy reports
// a grain tool under.
//
// orchestrator.ProcessResult matches ToolCall.Name against "ask_question",
// "comment_on_issue" and "propose_task" exactly. agy names an eagerly
// registered MCP tool "mcp_grain-sandbox_<tool>" (single underscores) and
// reports a lazily loaded one as a "call_mcp_tool" step carrying the real
// name in its arguments. Neither matched, so a run that asked the human a
// question was recorded no_action and the question was never relayed.
// This suite scripted "mcp__grain-sandbox__<tool>" -- a spelling agy has
// never produced -- which is why nothing caught it.
func TestEscapeHatchToolCallsAreRecordedUnderTheirBareNames(t *testing.T) {
	for _, tool := range []string{"ask_question", "request_secret", "comment_on_issue", "propose_task"} {
		for _, reporting := range []struct {
			name  string
			steps func(tool string) []string
		}{{
			// What a run gets today: Framework.Run asks for every grain
			// tool eagerly, so agy registers it as a native tool of its
			// own under this name.
			name: "eagerly registered",
			steps: func(tool string) []string {
				reported := mcp.AgyQualifiedToolName(tool)
				return []string{
					toolActive(0, reported, `{"question":"which config file?"}`),
					toolDone(0, reported, "Recorded"),
				}
			},
		}, {
			// The fallback route, which stays open whatever the config
			// says: agy's dispatcher for lazily loaded MCP tools, with
			// the tool it actually called in its arguments.
			name: "through agy's dispatcher",
			steps: func(tool string) []string {
				args := `{"ServerName":"grain-sandbox","ToolName":"` + tool +
					`","Arguments":{"question":"which config file?"}}`
				return []string{
					toolActive(0, "call_mcp_tool", args),
					toolDone(0, "call_mcp_tool", "Recorded"),
				}
			},
		}} {
			t.Run(tool+"/"+reporting.name, func(t *testing.T) {
				lines := append([]string{initLine}, reporting.steps(tool)...)
				lines = append(lines,
					`{"event":"result","result":{"status":"SUCCESS","response":"waiting on a reply"}}`)
				got, err := parseTranscript(stream(lines...))
				if err != nil {
					t.Fatalf("parseTranscript: %v", err)
				}
				if len(got.ToolCalls) != 1 {
					t.Fatalf("ToolCalls = %+v, want 1", got.ToolCalls)
				}
				if got.ToolCalls[0].Name != tool {
					t.Errorf("ToolCalls[0].Name = %q, want %q -- orchestrator.ProcessResult "+
						"matches the bare name exactly, so anything else relays nothing",
						got.ToolCalls[0].Name, tool)
				}
				if got.ToolCalls[0].Arguments["question"] != "which config file?" {
					t.Errorf("ToolCalls[0].Arguments = %v, want the tool's own arguments -- "+
						"ProcessResult reads the question out of them",
						got.ToolCalls[0].Arguments)
				}
				if strings.Contains(got.Transcript, "mcp_") || strings.Contains(got.Transcript, "call_mcp_tool") {
					t.Errorf("Transcript = %q, want the tool's own name in it", got.Transcript)
				}
			})
		}
	}
}

// A dispatch naming a server this grain did not publish is left exactly
// as agy reported it: renaming it would attribute a foreign tool's call
// to one of grain's own.
func TestDispatchToAnotherServerIsNotUnwrapped(t *testing.T) {
	args := `{"ServerName":"someone-elses","ToolName":"ask_question","Arguments":{"question":"?"}}`
	got, err := parseTranscript(stream(
		initLine,
		toolActive(0, "call_mcp_tool", args),
		toolDone(0, "call_mcp_tool", "Recorded"),
		`{"event":"result","result":{"status":"SUCCESS","response":"done"}}`,
	))
	if err != nil {
		t.Fatalf("parseTranscript: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "call_mcp_tool" {
		t.Fatalf("ToolCalls = %+v, want the dispatcher's own name", got.ToolCalls)
	}
}

// TestToolStepWithNoActiveUpdateIsAlsoRecordedBarely covers applyStep's
// other route into ToolCalls -- a terminal update with no ACTIVE one
// before it, which PartialTranscript hits every time it reads a capture
// that starts mid-run. It names the call through the same rawStep.toolCall,
// so it must normalize the same way rather than only the common path.
func TestToolStepWithNoActiveUpdateIsAlsoRecordedBarely(t *testing.T) {
	got, err := parseTranscript(stream(
		initLine,
		toolDone(0, mcp.AgyQualifiedToolName("comment_on_issue"), "Recorded"),
		`{"event":"result","result":{"status":"SUCCESS","response":"done"}}`,
	))
	if err != nil {
		t.Fatalf("parseTranscript: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "comment_on_issue" {
		t.Fatalf("ToolCalls = %+v, want one named %q", got.ToolCalls, "comment_on_issue")
	}
}
