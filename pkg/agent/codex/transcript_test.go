package codex

import (
	"strings"
	"testing"
)

// The two vocabularies transcript.go reads (rawEvent's own doc comment)
// have to produce the same Result from the same run, since which one a
// deployment gets is decided by the codex binary in its image. Every
// property below is asserted over both.
func TestParseTranscriptReadsBothEventVocabularies(t *testing.T) {
	itemForm := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"item.completed","item":{"id":"i0","item_type":"reasoning","text":"reading the repo"}}`,
		`{"type":"item.started","item":{"id":"i1","item_type":"mcp_tool_call","server":"grain-sandbox",` +
			`"tool":"run_command","arguments":{"command":"go test ./..."}}}`,
		`{"type":"item.completed","item":{"id":"i1","item_type":"mcp_tool_call","server":"grain-sandbox",` +
			`"tool":"run_command","status":"completed","result":{"content":[{"type":"text","text":"ok"}]}}}`,
		`{"type":"item.completed","item":{"id":"i2","item_type":"agent_message","text":"the tests pass"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1}}`,
	}, "\n")
	msgForm := strings.Join([]string{
		`{"id":"0","msg":{"type":"task_started"}}`,
		`{"id":"1","msg":{"type":"agent_reasoning","text":"reading the repo"}}`,
		`{"id":"2","msg":{"type":"mcp_tool_call_begin","call_id":"c1","invocation":` +
			`{"server":"grain-sandbox","tool":"run_command","arguments":{"command":"go test ./..."}}}}`,
		`{"id":"3","msg":{"type":"mcp_tool_call_end","call_id":"c1","invocation":` +
			`{"server":"grain-sandbox","tool":"run_command"},"result":{"Ok":{"content":[{"type":"text","text":"ok"}]}}}}`,
		`{"id":"4","msg":{"type":"agent_message","message":"the tests pass"}}`,
		`{"id":"5","msg":{"type":"task_complete","last_agent_message":"the tests pass"}}`,
	}, "\n")

	for name, stream := range map[string]string{"item": itemForm, "msg": msgForm} {
		t.Run(name, func(t *testing.T) {
			got, err := parseTranscript(stream)
			if err != nil {
				t.Fatalf("parseTranscript: %v", err)
			}
			if got.FinalText != "the tests pass" {
				t.Errorf("FinalText = %q", got.FinalText)
			}
			if len(got.ToolCalls) != 1 {
				t.Fatalf("ToolCalls = %+v, want one", got.ToolCalls)
			}
			call := got.ToolCalls[0]
			if call.Name != "run_command" || call.Text != "ok" || call.IsError {
				t.Errorf("ToolCalls[0] = %+v", call)
			}
			if call.Arguments["command"] != "go test ./..." {
				t.Errorf("ToolCalls[0].Arguments = %+v", call.Arguments)
			}
			for _, want := range []string{"reading the repo", "> run_command(", "ok", "the tests pass"} {
				if !strings.Contains(got.Transcript, want) {
					t.Errorf("Transcript = %q, want it to carry %q", got.Transcript, want)
				}
			}
		})
	}
}

// The shipped CLI names an item's kind in the item's own "type" field,
// where codex's documented schema calls it "item_type" (rawItem's own
// doc comment). Both spellings have to read the same, since which one a
// deployment gets is decided by the binary in its image: the lines here
// are the "type" spelling, taken from a real `codex exec --json`
// capture.
func TestParseTranscriptReadsTheItemKindUnderEitherFieldName(t *testing.T) {
	got, err := parseTranscript(strings.Join([]string{
		`{"type":"thread.started","thread_id":"01a066db-4912-79b0-8254-ff2d2927a870"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"reasoning","text":"reading the repo"}}`,
		`{"type":"item.started","item":{"id":"item_1","type":"mcp_tool_call","server":"grain-sandbox",` +
			`"tool":"ask_question","arguments":{"question":"which one?"}}}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"mcp_tool_call","server":"grain-sandbox",` +
			`"tool":"ask_question","status":"completed","result":"Recorded"}}`,
		`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"asked"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1}}`,
	}, "\n"))
	if err != nil {
		t.Fatalf("parseTranscript: %v", err)
	}
	if got.FinalText != "asked" {
		t.Errorf("FinalText = %q", got.FinalText)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "ask_question" || got.ToolCalls[0].Text != "Recorded" {
		t.Fatalf("ToolCalls = %+v, want the call recorded under its bare name", got.ToolCalls)
	}
	if !strings.Contains(got.Transcript, "reading the repo") {
		t.Errorf("Transcript = %q, want the reasoning item in it", got.Transcript)
	}
}

// A capture with no terminal event at all is a run whose output was
// truncated -- codex died, or the file was read mid-stream -- and
// parseTranscript says so rather than reporting a run that ended with no
// answer.
func TestParseTranscriptRequiresATerminalEvent(t *testing.T) {
	_, err := parseTranscript(`{"type":"item.completed","item":{"id":"i1","item_type":"agent_message","text":"hi"}}`)
	if err == nil || !strings.Contains(err.Error(), "no terminal event") {
		t.Fatalf("err = %v, want the truncated-stream error", err)
	}
}

// A failed turn comes back as an error *and* a Result: the run may well
// have pushed a branch before the failure, and a caller handed only an
// error strands that work (agent.Framework's own contract).
func TestParseTranscriptReturnsWhatAFailedTurnDid(t *testing.T) {
	got, err := parseTranscript(strings.Join([]string{
		`{"type":"item.completed","item":{"id":"i1","item_type":"mcp_tool_call","server":"grain-sandbox",` +
			`"tool":"run_command","status":"completed","result":"pushed"}}`,
		`{"type":"turn.failed","error":{"message":"stream disconnected before completion"}}`,
	}, "\n"))
	if err == nil || !strings.Contains(err.Error(), "stream disconnected") {
		t.Fatalf("err = %v, want codex's own account of the failure", err)
	}
	if got == nil || len(got.ToolCalls) != 1 {
		t.Fatalf("Result = %+v, want the call the run made before failing", got)
	}
}

// A bare "error" event is not the end of a run. codex emits one per
// attempt while it retries a dropped connection, and a run that
// reconnects goes on to finish normally -- reading the first as
// terminal would report a successful run as a failure. The lines below
// are a real capture, trimmed: `codex exec --json` against an
// unreachable endpoint, which retries five times and only then fails
// the turn.
func TestParseTranscriptDoesNotFailARunThatRecoveredFromAnError(t *testing.T) {
	got, err := parseTranscript(strings.Join([]string{
		`{"type":"thread.started","thread_id":"01a066db-4912-79b0-8254-ff2d2927a870"}`,
		`{"type":"turn.started"}`,
		`{"type":"error","message":"Reconnecting... 1/5 (unexpected status 503 Service Unavailable)"}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"back, and done"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1}}`,
	}, "\n"))
	if err != nil {
		t.Fatalf("parseTranscript: %v -- a retried connection is not a failed run", err)
	}
	if got.FinalText != "back, and done" {
		t.Errorf("FinalText = %q", got.FinalText)
	}
	// It still belongs in the narrative: an operator reading a slow run
	// should be able to see what it was busy recovering from.
	if !strings.Contains(got.Transcript, "Reconnecting... 1/5") {
		t.Errorf("Transcript = %q, want the error it recovered from", got.Transcript)
	}
}

// ...and when the retries do run out, codex ends the turn with the same
// message, which is the one that fails the run.
func TestParseTranscriptFailsOnTheTerminalTurnFailure(t *testing.T) {
	_, err := parseTranscript(strings.Join([]string{
		`{"type":"error","message":"Reconnecting... 5/5 (unexpected status 401 Unauthorized)"}`,
		`{"type":"turn.failed","error":{"message":"unexpected status 401 Unauthorized: Missing bearer or basic authentication in header"}}`,
	}, "\n"))
	if err == nil || !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Fatalf("err = %v, want codex's own account of the failure", err)
	}
}

// An error *item* is codex reporting something about itself -- here the
// optional code-mode host the deployment image deliberately does not
// carry (Dockerfile), which every run reports on startup. It is not a
// failure, and a parser that read it as one would fail every run.
func TestParseTranscriptDoesNotFailOnAnErrorItem(t *testing.T) {
	got, err := parseTranscript(strings.Join([]string{
		`{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Code Mode is unavailable because code-mode host is disabled."}}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"done"}}`,
		`{"type":"turn.completed"}`,
	}, "\n"))
	if err != nil {
		t.Fatalf("parseTranscript: %v -- an error item is not a failed run", err)
	}
	if !strings.Contains(got.Transcript, "Code Mode is unavailable") {
		t.Errorf("Transcript = %q, want the item reported in the narrative", got.Transcript)
	}
}

// A tool call that failed is recorded as one: orchestrator reads
// ToolCall.IsError, and a failure that reads as a success is a run whose
// story is wrong in the one place it matters.
func TestParseTranscriptMarksAFailedToolCall(t *testing.T) {
	got, err := parseTranscript(strings.Join([]string{
		`{"type":"item.completed","item":{"id":"i1","item_type":"mcp_tool_call","server":"grain-sandbox",` +
			`"tool":"read_file","status":"failed","error":"no such file"}}`,
		`{"type":"item.completed","item":{"id":"i2","item_type":"agent_message","text":"missing"}}`,
		`{"type":"turn.completed"}`,
	}, "\n"))
	if err != nil {
		t.Fatalf("parseTranscript: %v", err)
	}
	if len(got.ToolCalls) != 1 || !got.ToolCalls[0].IsError || got.ToolCalls[0].Text != "no such file" {
		t.Fatalf("ToolCalls = %+v, want one failed call carrying its message", got.ToolCalls)
	}
	if !strings.Contains(got.Transcript, "! no such file") {
		t.Errorf("Transcript = %q, want the failure marked", got.Transcript)
	}
}

// codex's own shell is not one of grain's tools. It belongs in the
// narrative an operator reads and never in ToolCalls, which
// orchestrator.ProcessResult matches against the vocabulary pkg/mcp
// registers.
func TestParseTranscriptKeepsCodexsOwnShellOutOfToolCalls(t *testing.T) {
	got, err := parseTranscript(strings.Join([]string{
		`{"type":"item.started","item":{"id":"i1","item_type":"command_execution","command":"ls -la"}}`,
		`{"type":"item.completed","item":{"id":"i1","item_type":"command_execution","command":"ls -la",` +
			`"aggregated_output":"total 0","exit_code":0}}`,
		`{"type":"item.completed","item":{"id":"i2","item_type":"agent_message","text":"looked"}}`,
		`{"type":"turn.completed"}`,
	}, "\n"))
	if err != nil {
		t.Fatalf("parseTranscript: %v", err)
	}
	if len(got.ToolCalls) != 0 {
		t.Fatalf("ToolCalls = %+v, want none: a native tool is not one grain published", got.ToolCalls)
	}
	if !strings.Contains(got.Transcript, "$ ls -la") || !strings.Contains(got.Transcript, "total 0") {
		t.Errorf("Transcript = %q, want the shell command and its output", got.Transcript)
	}
	// Announced once, not once per report of the same item.
	if strings.Count(got.Transcript, "$ ls -la") != 1 {
		t.Errorf("Transcript = %q, want the command announced exactly once", got.Transcript)
	}
}

// PartialTranscript reads a capture that is still being written, which
// routinely means one that starts mid-run and ends mid-line. Neither may
// cost it the part it can read.
func TestPartialTranscriptReadsAnUnfinishedCapture(t *testing.T) {
	got := PartialTranscript(strings.Join([]string{
		`{"type":"item.completed","item":{"id":"i1","item_type":"mcp_tool_call","server":"grain-sandbox",` +
			`"tool":"run_command","status":"completed","result":"ok"}}`,
		`{"type":"item.completed","item":{"id":"i2","item_type":"agent_m`,
	}, "\n"))
	if !strings.Contains(got, "> run_command(") || !strings.Contains(got, "ok") {
		t.Errorf("PartialTranscript = %q, want the complete lines rendered", got)
	}
	if got == "" {
		t.Error("PartialTranscript = \"\" for a capture with one complete line in it")
	}
}

// A completion with no start before it -- what PartialTranscript sees
// every time it reads a capture that begins mid-run -- still records the
// call rather than dropping what it returned.
func TestToolCallWithNoStartIsStillRecorded(t *testing.T) {
	got, err := parseTranscript(strings.Join([]string{
		`{"type":"item.completed","item":{"id":"i9","item_type":"mcp_tool_call","server":"grain-sandbox",` +
			`"tool":"comment_on_issue","status":"completed","result":"Recorded"}}`,
		`{"type":"turn.completed"}`,
	}, "\n"))
	if err != nil {
		t.Fatalf("parseTranscript: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "comment_on_issue" || got.ToolCalls[0].Text != "Recorded" {
		t.Fatalf("ToolCalls = %+v, want the call recorded from its completion alone", got.ToolCalls)
	}
}

// Arguments arrive as an object or as a string holding one, depending on
// the build and on whether codex parsed the model's payload before
// reporting it.
func TestArgumentsArriveAsAnObjectOrAsAStringHoldingOne(t *testing.T) {
	got, err := parseTranscript(strings.Join([]string{
		`{"type":"item.completed","item":{"id":"i1","item_type":"mcp_tool_call","server":"grain-sandbox",` +
			`"tool":"write_file","status":"completed","arguments":"{\"file_path\":\"out.txt\"}","result":"wrote"}}`,
		`{"type":"turn.completed"}`,
	}, "\n"))
	if err != nil {
		t.Fatalf("parseTranscript: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Arguments["file_path"] != "out.txt" {
		t.Fatalf("ToolCalls = %+v, want the string-encoded arguments decoded", got.ToolCalls)
	}
}

// The thread a capture was working in, under both event vocabularies.
// retry.go points a resumed attempt back at this id, so a build whose
// spelling this parser does not know would leave a run silently
// restarting the task at every retry rather than resuming it.
func TestParseEventsRecordsTheThread(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stdout string
		want   string
	}{
		{"the item vocabulary", `{"type":"thread.started","thread_id":"01a066db-4912"}`, "01a066db-4912"},
		{"the older vocabulary", `{"id":"0","msg":{"type":"session_configured","session_id":"01a066db-4912"}}`, "01a066db-4912"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := parseEvents(tc.stdout + "\n")
			if !p.sawThread {
				t.Errorf("sawThread = false for %s: retry.go reads this as codex having refused the invocation outright", tc.stdout)
			}
			if p.threadID != tc.want {
				t.Errorf("threadID = %q, want %q", p.threadID, tc.want)
			}
		})
	}

	// Nothing at all is the case the fallback turns on: a codex that
	// would not perform the invocation writes no stdout, so there is no
	// thread to have been in.
	p := parseEvents("")
	if p.sawThread || p.threadID != "" {
		t.Errorf("an empty capture reported thread %q (seen=%v), want no thread at all", p.threadID, p.sawThread)
	}

	// A capture holding an attempt per thread reports the last one: a
	// resumed exec re-announces the thread it joined, so the two agree,
	// but an exec that started a fresh thread must leave that one behind
	// rather than an id nothing can be resumed from.
	p = parseEvents(`{"type":"thread.started","thread_id":"first"}` + "\n" +
		`{"type":"thread.started","thread_id":"second"}` + "\n")
	if p.threadID != "second" {
		t.Errorf("threadID = %q, want the last thread the capture named", p.threadID)
	}
}

// PartialItems lists each paragraph of the narrative as an item: the
// agent's words, and a tool's call and its result apart.
func TestPartialItemsSplitWordsFromTools(t *testing.T) {
	got := PartialItems(strings.Join([]string{
		`{"type":"item.completed","item":{"id":"i0","item_type":"reasoning","text":"thinking"}}`,
		`{"type":"item.started","item":{"id":"i1","item_type":"command_execution","command":"ls"}}`,
		`{"type":"item.completed","item":{"id":"i1","item_type":"command_execution","command":"ls","aggregated_output":"a.txt"}}`,
		`{"type":"item.completed","item":{"id":"i2","item_type":"mcp_tool_call","server":"grain-sandbox",` +
			`"tool":"run_command","status":"completed","result":"ok"}}`,
		`{"type":"item.completed","item":{"id":"i3","item_type":"agent_message","text":"done"}}`,
	}, "\n"))
	var kinds []string
	for _, it := range got {
		kinds = append(kinds, it.Kind+":"+it.Tool)
	}
	want := "thinking: tool_call:shell tool_result:shell tool_call:run_command tool_result:run_command text:"
	if strings.Join(kinds, " ") != want {
		t.Fatalf("PartialItems kinds = %q, want %q (%+v)", kinds, want, got)
	}
	if got[len(got)-1].Text != "done" || got[2].Text != "a.txt" {
		t.Fatalf("PartialItems = %+v", got)
	}
}
