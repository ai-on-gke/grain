package antigravity

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// stream builds an NDJSON capture out of raw event lines, so each test
// below reads as the sequence of events agy actually emits rather than as
// one unbroken string literal.
func stream(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

const (
	initLine = `{"event":"init","init":{"cwd":"/w","tools":["mcp__grain-sandbox__run_command"],"permission_mode":"bypass"}}`
)

func toolActive(idx int, name, params string) string {
	return `{"event":"step_update","step_update":{"step_index":` + itoa(idx) +
		`,"state":"ACTIVE","step_type":"tool","tool_name":"` + name +
		`","tool_info":{"name":"` + name + `","parameters":` + params + `}}}`
}

func toolDone(idx int, name, output string) string {
	return `{"event":"step_update","step_update":{"step_index":` + itoa(idx) +
		`,"state":"DONE","step_type":"tool","tool_name":"` + name +
		`","tool_info":{"name":"` + name + `","output":"` + output + `"}}}`
}

func itoa(i int) string { return string(rune('0' + i)) }

// TestParseTranscriptPairsToolStepsByIndex is this parser's one
// structural difference from agent/claude's: agy identifies a step by a
// number it reuses across that step's ACTIVE and DONE updates, where
// claude pairs a tool_use block with a tool_result by an opaque id. Two
// interleaved calls prove the pairing follows the index rather than
// arrival order.
func TestParseTranscriptPairsToolStepsByIndex(t *testing.T) {
	got, err := parseTranscript(stream(
		initLine,
		toolActive(0, "read_file", `{"path":"a.txt"}`),
		toolActive(1, "read_file", `{"path":"b.txt"}`),
		toolDone(1, "read_file", "contents of b"),
		toolDone(0, "read_file", "contents of a"),
		`{"event":"result","result":{"status":"SUCCESS","response":"read both"}}`,
	))
	if err != nil {
		t.Fatalf("parseTranscript: %v", err)
	}
	if got.FinalText != "read both" {
		t.Errorf("FinalText = %q, want %q", got.FinalText, "read both")
	}
	if len(got.ToolCalls) != 2 {
		t.Fatalf("ToolCalls = %+v, want 2", got.ToolCalls)
	}
	// Step 0 was opened first, so it is ToolCalls[0], and must carry the
	// output of the DONE update for index 0 -- the one that arrived last.
	if got.ToolCalls[0].Text != "contents of a" {
		t.Errorf("ToolCalls[0].Text = %q, want %q (paired by step_index, not arrival order)",
			got.ToolCalls[0].Text, "contents of a")
	}
	if got.ToolCalls[1].Text != "contents of b" {
		t.Errorf("ToolCalls[1].Text = %q, want %q", got.ToolCalls[1].Text, "contents of b")
	}
	if got.ToolCalls[0].Arguments["path"] != "a.txt" {
		t.Errorf("ToolCalls[0].Arguments = %v, want path a.txt", got.ToolCalls[0].Arguments)
	}
}

// TestParseTranscriptReadsAToolErrorFromToolInfo covers the shape agy
// nests an error in -- tool_info.error, not the step's own top level --
// which is the whole reason rawToolInfo carries an Error of its own.
func TestParseTranscriptReadsAToolErrorFromToolInfo(t *testing.T) {
	got, err := parseTranscript(stream(
		initLine,
		toolActive(0, "run_command", `{"command":"false"}`),
		`{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool",`+
			`"tool_name":"run_command","tool_info":{"name":"run_command",`+
			`"error":{"type":"exit_status","message":"exit code 1"}}}}`,
		`{"event":"result","result":{"status":"SUCCESS","response":"gave up"}}`,
	))
	if err != nil {
		t.Fatalf("parseTranscript: %v", err)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v, want 1", got.ToolCalls)
	}
	if !got.ToolCalls[0].IsError {
		t.Error("ToolCalls[0].IsError = false, want true for an ERROR step")
	}
	if !strings.Contains(got.ToolCalls[0].Text, "exit code 1") {
		t.Errorf("ToolCalls[0].Text = %q, want it to carry the nested error message", got.ToolCalls[0].Text)
	}
	if !strings.Contains(got.Transcript, "! exit_status: exit code 1") {
		t.Errorf("Transcript = %q, want the error rendered on a ! line", got.Transcript)
	}
}

// TestParseTranscriptReturnsTheResultAlongsideAFailureStatus is
// agent.Framework's own contract at this layer: a run that pushed a
// branch and then failed must not come back as "nothing happened".
func TestParseTranscriptReturnsTheResultAlongsideAFailureStatus(t *testing.T) {
	got, err := parseTranscript(stream(
		initLine,
		toolActive(0, "run_command", `{"command":"git push"}`),
		toolDone(0, "run_command", "branch pushed"),
		`{"event":"result","result":{"status":"FAILURE","error":"model gave up"}}`,
	))
	if err == nil {
		t.Fatal("parseTranscript err = nil, want an error for a FAILURE status")
	}
	if got == nil {
		t.Fatal("parseTranscript result = nil alongside an error; the push it already made would be stranded")
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Text != "branch pushed" {
		t.Errorf("ToolCalls = %+v, want the push it managed before failing", got.ToolCalls)
	}
}

// TestSucceededTreatsAnUnknownStatusAsFailure is the fail-closed rule:
// only the two spellings agy is known to report for success count as
// success.
func TestSucceededTreatsAnUnknownStatusAsFailure(t *testing.T) {
	for _, status := range []string{"SUCCESS", "OK", "success"} {
		if !succeeded(status) {
			t.Errorf("succeeded(%q) = false, want true", status)
		}
	}
	for _, status := range []string{"FAILURE", "CANCELLED", "TIMEOUT", "SOMETHING_NEW", ""} {
		if succeeded(status) {
			t.Errorf("succeeded(%q) = true, want false -- an unrecognized status must fail closed", status)
		}
	}
}

// TestParseTranscriptWithoutAResultEventIsAnError separates "the run
// finished badly" from "the stream stopped": the latter has no terminal
// event at all, and is what a killed subprocess leaves behind.
func TestParseTranscriptWithoutAResultEventIsAnError(t *testing.T) {
	_, err := parseTranscript(stream(initLine, toolActive(0, "read_file", `{"path":"a"}`)))
	if err == nil {
		t.Fatal("parseTranscript err = nil for a capture with no result event, want an error")
	}
}

// TestPartialTranscriptRendersATruncatedCapture is what
// LiveTranscriptDir depends on: reading the file while agy is still
// appending to it catches a half-written final line, and a still-open
// tool step has no output yet. Neither may lose the lines before it.
func TestPartialTranscriptRendersATruncatedCapture(t *testing.T) {
	got := PartialTranscript(stream(
		initLine,
		toolActive(0, "read_file", `{"path":"a.txt"}`),
		toolDone(0, "read_file", "contents of a"),
		toolActive(1, "run_command", `{"command":"git push"}`),
	) + `{"event":"step_up`)
	if !strings.Contains(got, "> read_file") || !strings.Contains(got, "contents of a") {
		t.Errorf("PartialTranscript = %q, want the completed step rendered", got)
	}
	if !strings.Contains(got, "> run_command") {
		t.Errorf("PartialTranscript = %q, want the still-open step's own call line", got)
	}
}

// TestPartialTranscriptRecordsATerminalStepWithNoActiveOneBeforeIt is the
// other half of reading a capture that starts mid-run: a DONE update
// whose ACTIVE update is not in the bytes at hand still has to report
// what the call returned rather than dropping it.
func TestPartialTranscriptRecordsATerminalStepWithNoActiveOneBeforeIt(t *testing.T) {
	got := PartialTranscript(stream(toolDone(3, "write_file", "wrote ok.txt")))
	if !strings.Contains(got, "wrote ok.txt") {
		t.Errorf("PartialTranscript = %q, want the orphaned terminal step's output", got)
	}
}

func agentDelta(idx int, delta string) string {
	return `{"event":"step_update","step_update":{"step_index":` + itoa(idx) +
		`,"state":"ACTIVE","step_type":"agent_response","text_delta":"` + delta + `"}}`
}

func agentDone(idx int, output string) string {
	return `{"event":"step_update","step_update":{"step_index":` + itoa(idx) +
		`,"state":"DONE","step_type":"agent_response","output":"` + output + `"}}`
}

// TestParseTranscriptCapturesFinalTextFromAgentResponse ensures that conversational
// assistant text responses in stream-json are captured as FinalText even when
// the result event's response is empty or when deltas arrive in pieces.
func TestParseTranscriptCapturesFinalTextFromAgentResponse(t *testing.T) {
	got, err := parseTranscript(stream(
		initLine,
		toolActive(0, "read_file", `{"path":"a.txt"}`),
		toolDone(0, "read_file", "contents of a"),
		agentDelta(1, "Here is the "),
		agentDelta(1, "full investigation report: root cause found."),
		agentDone(1, ""),
		`{"event":"result","result":{"status":"SUCCESS","response":""}}`,
	))
	if err != nil {
		t.Fatalf("parseTranscript: %v", err)
	}
	want := "Here is the full investigation report: root cause found."
	if got.FinalText != want {
		t.Errorf("FinalText = %q, want %q", got.FinalText, want)
	}
}

// TestParseTranscriptCapturesLastAgentResponseAcrossTurns ensures that the final
// agent response across multiple turns is the one captured as FinalText.
func TestParseTranscriptCapturesLastAgentResponseAcrossTurns(t *testing.T) {
	got, err := parseTranscript(stream(
		initLine,
		agentDelta(0, "Thinking about turn 1..."),
		agentDone(0, ""),
		toolActive(1, "run_command", `{"command":"ls"}`),
		toolDone(1, "run_command", "file.go"),
		agentDelta(2, "Final conclusion and recommendations."),
		agentDone(2, ""),
		`{"event":"result","result":{"status":"SUCCESS","response":"done"}}`,
	))
	if err != nil {
		t.Fatalf("parseTranscript: %v", err)
	}
	want := "Final conclusion and recommendations."
	if got.FinalText != want {
		t.Errorf("FinalText = %q, want %q (should prefer longer/final conversational response over 'done')", got.FinalText, want)
	}
}

// TestParseTranscriptRestoresTruncatedToolArgumentsFromRecorded ensures that
// tool call arguments truncated by agy's stream-json output (at 512 chars + ellipsis)
// are restored in full from the recorded tool call records.
func TestParseTranscriptRestoresTruncatedToolArgumentsFromRecorded(t *testing.T) {
	fullComment := strings.Repeat("x", 2000)
	truncatedComment := strings.Repeat("x", 512) + "…"

	streamOutput := stream(
		initLine,
		toolActive(0, "comment_on_issue", `{"comment":"`+truncatedComment+`"}`),
		toolDone(0, "comment_on_issue", "comment posted"),
		`{"event":"result","result":{"status":"SUCCESS","response":""}}`,
	)

	recorded := []mcp.ToolCallRecord{
		{
			Name: "comment_on_issue",
			Arguments: map[string]any{
				"comment": fullComment,
			},
		},
	}

	got, err := parseTranscriptWithRecorded(streamOutput, recorded)
	if err != nil {
		t.Fatalf("parseTranscriptWithRecorded: %v", err)
	}

	if len(got.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(got.ToolCalls))
	}

	commentArg, ok := got.ToolCalls[0].Arguments["comment"].(string)
	if !ok {
		t.Fatalf("comment argument is not string: %T", got.ToolCalls[0].Arguments["comment"])
	}
	if len(commentArg) != 2000 {
		t.Errorf("len(commentArg) = %d, want 2000 (was truncated)", len(commentArg))
	}
	if !strings.Contains(got.Transcript, fullComment) {
		t.Errorf("Transcript did not contain full untruncated comment")
	}
}

// PartialItems lists a response once its step is done, as one item of
// all its deltas, and each tool step as its call and then its result.
func TestPartialItemsListWholeItemsInOrder(t *testing.T) {
	got := PartialItems(stream(
		initLine,
		agentDelta(0, "Look"),
		agentDelta(0, "ing."),
		agentDone(0, ""),
		toolActive(1, "read_file", `{"path":"a.txt"}`),
		toolDone(1, "read_file", "contents of a"),
		agentDelta(2, "still streaming"),
	))
	want := []agent.TranscriptItem{
		{Kind: agent.ItemText, Text: "Looking."},
		{Kind: agent.ItemToolCall, Tool: "read_file", Text: `read_file({"path":"a.txt"})`},
		{Kind: agent.ItemToolResult, Tool: "read_file", Text: "contents of a"},
	}
	if len(got) != len(want) {
		t.Fatalf("PartialItems = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
