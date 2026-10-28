package codex

import (
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/mcp"
)

// TestEscapeHatchToolCallsAreRecordedUnderTheirBareNames is
// agent/claude's and agent/antigravity's test of the same name, for
// codex's own vocabulary. orchestrator.ProcessResult matches
// ToolCall.Name against "ask_question", "comment_on_issue" and
// "propose_task" exactly; a name recorded with the server's prefix on it
// matched none of them, so a run that asked the human a question was
// recorded no_action and the question was never relayed
// (mcp.BareToolName's own doc comment has the whole failure).
//
// codex reports the server and the tool as separate fields, so the
// common case needs no unwrapping -- but a build that reports the tool
// already qualified, in either spelling, must not be the one that brings
// that failure back.
func TestEscapeHatchToolCallsAreRecordedUnderTheirBareNames(t *testing.T) {
	for _, tool := range []string{"ask_question", "request_secret", "comment_on_issue", "propose_task"} {
		for _, reported := range []string{tool, mcp.QualifiedToolName(tool), mcpServerName + "__" + tool} {
			t.Run(tool+"/"+reported, func(t *testing.T) {
				got, err := parseTranscript(strings.Join([]string{
					`{"type":"item.completed","item":{"id":"i1","item_type":"mcp_tool_call",` +
						`"server":"` + mcpServerName + `","tool":"` + reported + `",` +
						`"status":"completed","result":"Recorded"}}`,
					`{"type":"item.completed","item":{"id":"i2","item_type":"agent_message","text":"waiting on a reply"}}`,
					`{"type":"turn.completed"}`,
				}, "\n"))
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
				if strings.Contains(got.Transcript, "mcp__") {
					t.Errorf("Transcript = %q, want no mcp__ prefix in it", got.Transcript)
				}
			})
		}
	}
}

// The older vocabulary names the tool in the same place, and has to
// normalize it the same way: it is a whole second route into ToolCalls
// (applyMsgToolCall), not a variation on the first.
func TestEscapeHatchNamesAreBareInTheOlderVocabularyToo(t *testing.T) {
	got, err := parseTranscript(strings.Join([]string{
		`{"id":"0","msg":{"type":"mcp_tool_call_begin","call_id":"c1","invocation":` +
			`{"server":"` + mcpServerName + `","tool":"` + mcp.QualifiedToolName("ask_question") + `",` +
			`"arguments":{"question":"which config file?"}}}}`,
		`{"id":"1","msg":{"type":"mcp_tool_call_end","call_id":"c1","result":"Recorded"}}`,
		`{"id":"2","msg":{"type":"task_complete","last_agent_message":"waiting on a reply"}}`,
	}, "\n"))
	if err != nil {
		t.Fatalf("parseTranscript: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "ask_question" {
		t.Fatalf("ToolCalls = %+v, want one named ask_question", got.ToolCalls)
	}
}
