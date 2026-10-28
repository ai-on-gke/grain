package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// rawEvent is one `codex exec --json` line. Two event vocabularies exist
// across codex releases and both are read here, because which one a
// deployment gets is decided by the binary in its image rather than by
// anything this repository controls:
//
//   - the thread/item form, tagged by a dotted top-level "type"
//     ("thread.started", "turn.started", "item.started",
//     "item.completed", "turn.completed", "turn.failed") whose payload
//     is the "item" object, plus a bare "error" event. This is the
//     current one.
//   - the older form, where every line is {"id":...,"msg":{"type":...}}
//     and the payload is that nested "msg".
//
// Telling them apart costs one field: a line with a "msg" object is the
// old form, everything else is read as the new one. Only what building
// an agent.Result needs is modeled -- the same narrowing agent/claude's
// and agent/antigravity's parsers do, and for the same reason: an
// unrecognized field or event name should cost this parser nothing.
type rawEvent struct {
	Type string   `json:"type"`
	Item *rawItem `json:"item"`
	// ThreadID is what "thread.started" names the session this exec is
	// working in, and is what `codex exec resume` takes back -- see
	// resume.go, and the measurement recorded in docs/codex-surface.md.
	ThreadID string `json:"thread_id"`
	// Error and Message are the two shapes a top-level failure arrives
	// in: "turn.failed" nests it under "error", a bare "error" event
	// carries the text at the top level.
	Error   *rawError `json:"error"`
	Message string    `json:"message"`
	Msg     *rawMsg   `json:"msg"`
}

// rawItem is one thread item -- a unit of what the agent did, reported
// once when it starts and again when it completes. Which kind it is
// arrives in one of two fields depending on the build: the CLI this was
// written against (codex-cli 0.153) spells it "type" inside the item,
// while "item_type" is the name in codex's own documented thread-item
// schema and in earlier builds. itemType folds the two together rather
// than betting on either.
type rawItem struct {
	ID       string `json:"id"`
	ItemType string `json:"item_type"`
	Type     string `json:"type"`
	// Text is an agent_message's or reasoning item's own words, and
	// Message an error item's -- codex reports a component it could not
	// start (a missing optional helper, say) as an item of its own
	// rather than as a failure of the run, so this belongs in the
	// narrative and nowhere else.
	Text    string `json:"text"`
	Message string `json:"message"`
	// Server, Tool, Arguments, Result and Status belong to an
	// mcp_tool_call: which server the tool came from, its name, what it
	// was called with, what it returned, and whether that succeeded.
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Result    json.RawMessage `json:"result"`
	Status    string          `json:"status"`
	Error     string          `json:"error"`
	// Command, AggregatedOutput and ExitCode belong to a
	// command_execution: codex's own shell tool, which this run's
	// config denies write access rather than removing (see this
	// package's doc comment). It contributes to the narrative and never
	// to ToolCalls -- see applyItem.
	Command          string `json:"command"`
	AggregatedOutput string `json:"aggregated_output"`
	ExitCode         *int   `json:"exit_code"`
}

// rawMsg is the older vocabulary's payload. The event names differ from
// the item form's but the facts are the same ones, which is why
// applyMsg lands them in the same fields.
type rawMsg struct {
	Type string `json:"type"`
	// Message is an agent_message's text and also an error event's; Text
	// is a reasoning event's.
	Message string `json:"message"`
	Text    string `json:"text"`
	// LastAgentMessage is task_complete's own copy of the final answer.
	LastAgentMessage string `json:"last_agent_message"`
	// SessionID is this vocabulary's name for the thread id, announced
	// by session_configured the way "thread.started" announces it in the
	// item form.
	SessionID        string          `json:"session_id"`
	CallID           string          `json:"call_id"`
	Invocation       *rawInvocation  `json:"invocation"`
	Result           json.RawMessage `json:"result"`
	Command          json.RawMessage `json:"command"`
	Stdout           string          `json:"stdout"`
	Stderr           string          `json:"stderr"`
	AggregatedOutput string          `json:"aggregated_output"`
	ExitCode         *int            `json:"exit_code"`
}

// rawInvocation is an mcp_tool_call_begin/end's account of which tool
// was called and with what.
type rawInvocation struct {
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

type rawError struct {
	Message string `json:"message"`
}

// The item types and event names this parser acts on. Anything else is
// carried through the loop untouched -- a "todo_list" item or a
// "token_count" event is real, it just contributes nothing to a Result.
const (
	itemAgentMessage = "agent_message"
	itemReasoning    = "reasoning"
	itemMCPToolCall  = "mcp_tool_call"
	itemCommandExec  = "command_execution"
	itemError        = "error"

	statusFailed = "failed"
)

// parseTranscript turns a full `codex exec --json` capture into a
// Result: FinalText from the last assistant message the run produced,
// ToolCalls built by pairing each MCP tool call's start with the
// completion bearing the same item id (or call id, in the older
// vocabulary). That pairing is by id rather than by position because
// codex interleaves items freely.
//
// A malformed or unrecognized line is skipped rather than treated as
// fatal, matching both other parsers' tolerance and for the same reason:
// codex's event shape grows new fields and item types across versions
// -- it has already changed vocabulary once -- and this parser meeting
// one it does not know should not make a whole run unreadable.
func parseTranscript(stdout string) (*agent.Result, error) {
	p := parseEvents(stdout)
	if !p.sawResult {
		return nil, fmt.Errorf("codex: no terminal event found in output")
	}
	p.result.Transcript = strings.TrimSpace(p.transcript.String())
	if p.resultErr != nil {
		// The Result travels back alongside the error, never instead of
		// it: agent.Framework's own contract is that a run which edited,
		// committed and pushed before failing has already changed the
		// world, and a caller handed only an error strands that work.
		return &p.result, p.resultErr
	}
	return &p.result, nil
}

// PartialTranscript renders whatever of a still-in-progress capture is
// on disk so far into the same human-readable narrative parseTranscript
// builds once a run finishes -- what LiveTranscriptDir reads back for a
// run with no FinishedAt yet. Unlike parseTranscript it never errors: a
// missing terminal event just means the run is still going, and a
// truncated final line -- reading a file while codex is still appending
// to it can always catch mid-write -- is skipped the same way any other
// unparseable line is.
func PartialTranscript(stdout string) string {
	return strings.TrimSpace(parseEvents(stdout).transcript.String())
}

// PartialItems is PartialTranscript's narrative as the items it is
// written from, one for each paragraph it renders, in the same order.
func PartialItems(stdout string) []agent.TranscriptItem {
	return parseEvents(stdout).items
}

// item records one paragraph of the narrative as an item too.
func (p *parsedEvents) item(kind, tool, text string, isError bool) {
	p.items = append(p.items, agent.TranscriptItem{Kind: kind, Tool: tool, Text: text, IsError: isError})
}

// parsedEvents is one line-by-line pass over a capture, shared by
// parseTranscript (the whole thing, once a run finished) and
// PartialTranscript (however much exists so far) so the two can never
// build the narrative two different ways.
type parsedEvents struct {
	result agent.Result
	// items is transcript, an item for each paragraph it renders.
	items []agent.TranscriptItem
	// pending maps an MCP tool call's id to its slot in
	// result.ToolCalls, so the later completion for that same id can
	// fill in what the call returned.
	pending map[string]int
	// started records which items have already had their header line
	// written, so an item reported both starting and completing is not
	// announced twice.
	started map[string]bool
	// lastAgentMessage is the most recent assistant message text, which
	// is what a successful run's FinalText is: codex reports the final
	// answer as an ordinary item and then ends the turn, rather than
	// repeating it in a terminal event the way claude's "result" does.
	lastAgentMessage string
	sawResult        bool
	// errorText is the most recent bare "error" event's message. Those
	// are not terminal -- codex reports each failed attempt of a
	// reconnect that then succeeds as one -- so it is kept apart from
	// resultText, which only a terminal failure fills in, and read only
	// as the fallback failureText below.
	errorText string
	// turns counts completed assistant messages -- what Run's own turn
	// cap is measured in, codex having no --max-turns flag of its own to
	// pass one down to (see Framework.Run).
	turns     int
	resultErr error
	// threadID is the session codex last said this capture was working
	// in, and sawThread whether it ever said so at all. retry.go reads
	// both: the first is what a resumed attempt is pointed back at, and
	// the second is how an exec codex refused outright -- a resume argv
	// it does not support, a thread it has no rollout for -- is told
	// from one it merely failed. A refused exec writes nothing to stdout
	// at all, this event included (docs/codex-surface.md).
	threadID  string
	sawThread bool
	// resultText is what codex itself said about a terminal failure,
	// verbatim, kept apart from resultErr's rendered sentence so
	// usagelimit.go can read the provider's own words rather than text
	// this file wrote. Empty for a capture that reported no failure.
	resultText string
	transcript strings.Builder
}

// failureText is what codex said about why this run went wrong,
// preferring the terminal event's own account and falling back to the
// last bare error event -- which is all there is when the process was
// killed before it could report a terminal one. usagelimit.go reads
// this: a quota refusal arrives either way, and reading only the first
// would let a limit that killed the process mid-run be diagnosed as an
// ordinary crash.
func (p *parsedEvents) failureText() string {
	if p.resultText != "" {
		return p.resultText
	}
	return p.errorText
}

// threadFromLine is the thread one --json line names, or "" -- what
// agent.SessionWatcher reads codex's live stream with. It is parseEvents
// over that one line, so the early reading and the final one agree.
func threadFromLine(line []byte) string {
	return parseEvents(string(line)).threadID
}

func parseEvents(stdout string) *parsedEvents {
	p := &parsedEvents{pending: map[string]int{}, started: map[string]bool{}}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev rawEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Msg != nil {
			p.applyMsg(ev.Msg)
			continue
		}
		switch ev.Type {
		case "thread.started":
			// The first event of an exec of codex, and therefore the
			// boundary between the attempts of one run: Framework.Run
			// execs codex again when the provider refuses it mid-flight
			// (retry.go), and one capture then holds a thread per
			// attempt. What the run ended as is what its *last* attempt
			// said, so an earlier one's terminal failure stops standing
			// in the way of this one's account of itself.
			p.startAttempt(ev.ThreadID)
		case "item.started", "item.completed":
			if ev.Item != nil {
				p.applyItem(ev.Item, ev.Type == "item.completed")
			}
		case "turn.completed":
			p.sawResult = true
			p.result.FinalText = p.lastAgentMessage
			// Last terminal event wins, for the reason above: a run that
			// was refused and then finished must not be handed back to
			// its caller as the refusal.
			p.clearFailure()
		case "turn.failed":
			p.sawResult = true
			p.fail(eventFailureText(&ev))
		case "error":
			// Deliberately not terminal. codex emits one of these for
			// every attempt of a transport reconnect ("Reconnecting...
			// 2/5"), and a run that recovers goes on to complete
			// normally -- reading the first as the end of the run would
			// report a successful run as a failure. What ends a run is
			// "turn.failed", which repeats the last of these as its own
			// message when the retries run out.
			p.noteError(eventFailureText(&ev))
		}
	}
	return p
}

// eventFailureText reads whichever of the two places a failed event puts
// its text carries one.
func eventFailureText(ev *rawEvent) string {
	if ev.Error != nil && ev.Error.Message != "" {
		return ev.Error.Message
	}
	return ev.Message
}

// noteError records a non-terminal error event: in the narrative, where
// an operator reading a run that recovered can still see what it
// recovered from, and as the fallback failureText reads.
func (p *parsedEvents) noteError(text string) {
	if text == "" {
		return
	}
	p.errorText = text
	fmt.Fprintf(&p.transcript, "! %s\n\n", text)
	p.item(agent.ItemError, "", text, true)
}

// startAttempt is one exec of codex beginning inside this capture:
// whatever an earlier attempt ended as is no longer what the run ended
// as. Only the terminal outcome is cleared -- the narrative, the tool
// calls and the turn count are the run's own and go on accumulating,
// since a retry keeps the sandbox the earlier attempt was working in.
//
// A build that announces no thread of its own simply never calls this,
// and a multi-attempt capture from one then reports the first failure it
// saw rather than the last -- the wrong sentence for a run that failed
// twice, never a failure reported for a run that succeeded, since
// clearFailure below is keyed on the ending itself.
//
// It also records the thread this exec opened, which is what a resumed
// attempt is pointed back at (retry.go). The last one wins, for the same
// reason the last terminal event does: a resumed exec re-announces the
// thread it joined, so the two agree, but an exec that could not resume
// and started a fresh thread must leave *that* one behind rather than
// the id nothing can be resumed from.
func (p *parsedEvents) startAttempt(threadID string) {
	p.sawThread = true
	if threadID != "" {
		p.threadID = threadID
	}
	p.clearFailure()
}

// clearFailure forgets a terminal failure this capture has already
// reported.
func (p *parsedEvents) clearFailure() {
	p.resultErr, p.resultText = nil, ""
}

// fail records a terminal failure once per attempt. First one wins: a
// run that reported two of them described one ending twice, and the
// first description is the one with the provider's own words in it.
func (p *parsedEvents) fail(text string) {
	if p.resultErr != nil {
		return
	}
	p.resultText = text
	if text == "" {
		p.resultErr = fmt.Errorf("codex: run ended in error")
		return
	}
	p.resultErr = fmt.Errorf("codex: run ended in error: %s", text)
}

// shellTool is the tool a transcript item names for codex's own shell,
// which has no name of its own in either vocabulary.
const shellTool = "shell"

// applyItem folds one thread item into the Result and the narrative.
// completed says whether this is the item's terminal report; an item is
// announced when it starts (so a live transcript shows a tool call while
// it is still running) and filled in when it completes.
func (p *parsedEvents) applyItem(item *rawItem, completed bool) {
	switch item.itemType() {
	case itemAgentMessage:
		if completed && item.Text != "" {
			fmt.Fprintf(&p.transcript, "%s\n\n", item.Text)
			p.item(agent.ItemText, "", item.Text, false)
			p.lastAgentMessage = item.Text
			p.turns++
		}
	case itemReasoning:
		if completed && item.Text != "" {
			fmt.Fprintf(&p.transcript, "%s\n\n", item.Text)
			p.item(agent.ItemThinking, "", item.Text, false)
		}
	case itemMCPToolCall:
		p.applyToolCall(item, completed)
	case itemError:
		// An error *item* is codex reporting something about itself --
		// an optional component it could not start, most often -- not
		// the run failing. It belongs in the narrative and nowhere
		// else: promoting it would fail runs that went on to work.
		if completed {
			if text := item.errorMessage(); text != "" {
				fmt.Fprintf(&p.transcript, "! %s\n\n", text)
				p.item(agent.ItemError, "", text, true)
			}
		}
	case itemCommandExec:
		// codex's own shell, not one of grain's tools: it belongs in the
		// narrative an operator reads and never in ToolCalls, which
		// orchestrator.ProcessResult matches against the vocabulary
		// pkg/mcp registers. A native tool recorded there would be a
		// call to a tool that does not exist as far as every consumer
		// downstream is concerned.
		if !p.started[item.ID] && item.Command != "" {
			p.started[item.ID] = true
			fmt.Fprintf(&p.transcript, "$ %s\n", item.Command)
			p.item(agent.ItemToolCall, shellTool, "$ "+item.Command, false)
		}
		if completed {
			if out := strings.TrimSpace(item.AggregatedOutput); out != "" {
				fmt.Fprintf(&p.transcript, "%s\n\n", out)
				p.item(agent.ItemToolResult, shellTool, out, false)
			}
		}
	}
}

// applyToolCall records an MCP tool call and, on completion, what it
// returned. A completion with no start before it -- which
// PartialTranscript hits every time it reads a capture that begins
// mid-run -- records the call from the completion itself rather than
// dropping what it returned.
func (p *parsedEvents) applyToolCall(item *rawItem, completed bool) {
	idx, known := p.pending[item.ID]
	if !known {
		name := toolName(item.Server, item.Tool)
		args := decodeArguments(item.Arguments)
		p.result.ToolCalls = append(p.result.ToolCalls, agent.ToolCall{Name: name, Arguments: args})
		idx = len(p.result.ToolCalls) - 1
		p.pending[item.ID] = idx
		if !p.started[item.ID] {
			p.started[item.ID] = true
			fmt.Fprintf(&p.transcript, "> %s(%s)\n", name, inputSummary(args))
			p.item(agent.ItemToolCall, name, name+"("+inputSummary(args)+")", false)
		}
	}
	if !completed {
		return
	}
	text, isError := item.toolResult()
	p.result.ToolCalls[idx].Text = text
	p.result.ToolCalls[idx].IsError = isError
	delete(p.pending, item.ID)
	if isError {
		fmt.Fprintf(&p.transcript, "! %s\n\n", text)
	} else {
		fmt.Fprintf(&p.transcript, "%s\n\n", text)
	}
	p.item(agent.ItemToolResult, p.result.ToolCalls[idx].Name, text, isError)
}

// applyMsg is applyItem for the older vocabulary: the same facts under
// different event names, landing in the same fields so everything
// downstream of parseEvents reads one shape.
func (p *parsedEvents) applyMsg(msg *rawMsg) {
	switch msg.Type {
	case "agent_message":
		if msg.Message != "" {
			fmt.Fprintf(&p.transcript, "%s\n\n", msg.Message)
			p.item(agent.ItemText, "", msg.Message, false)
			p.lastAgentMessage = msg.Message
			p.turns++
		}
	case "agent_reasoning":
		if msg.Text != "" {
			fmt.Fprintf(&p.transcript, "%s\n\n", msg.Text)
			p.item(agent.ItemThinking, "", msg.Text, false)
		}
	case "mcp_tool_call_begin", "mcp_tool_call_end":
		p.applyMsgToolCall(msg)
	case "exec_command_begin":
		if cmd := commandText(msg.Command); cmd != "" {
			fmt.Fprintf(&p.transcript, "$ %s\n", cmd)
			p.item(agent.ItemToolCall, shellTool, "$ "+cmd, false)
		}
	case "exec_command_end":
		if out := strings.TrimSpace(msg.output()); out != "" {
			fmt.Fprintf(&p.transcript, "%s\n\n", out)
			p.item(agent.ItemToolResult, shellTool, out, false)
		}
	case "session_configured":
		// This vocabulary's own first event of an exec, and so its
		// boundary between the attempts of one run -- "thread.started"
		// in the item form, and startAttempt has why it matters.
		p.startAttempt(msg.SessionID)
	case "task_complete":
		p.sawResult = true
		if msg.LastAgentMessage != "" {
			p.lastAgentMessage = msg.LastAgentMessage
		}
		p.result.FinalText = p.lastAgentMessage
		// Last terminal event wins: a run that was refused and then
		// finished must not be handed back as the refusal.
		p.clearFailure()
	case "error":
		p.sawResult = true
		p.fail(msg.Message)
	}
}

// applyMsgToolCall is applyToolCall over the older vocabulary's own
// begin/end pair, which identifies a call by call_id and nests its name
// and arguments under "invocation".
func (p *parsedEvents) applyMsgToolCall(msg *rawMsg) {
	end := msg.Type == "mcp_tool_call_end"
	idx, known := p.pending[msg.CallID]
	if !known {
		var server, tool string
		var args map[string]any
		if inv := msg.Invocation; inv != nil {
			server, tool = inv.Server, inv.Tool
			args = decodeArguments(inv.Arguments)
		}
		name := toolName(server, tool)
		p.result.ToolCalls = append(p.result.ToolCalls, agent.ToolCall{Name: name, Arguments: args})
		idx = len(p.result.ToolCalls) - 1
		p.pending[msg.CallID] = idx
		fmt.Fprintf(&p.transcript, "> %s(%s)\n", name, inputSummary(args))
		p.item(agent.ItemToolCall, name, name+"("+inputSummary(args)+")", false)
	}
	if !end {
		return
	}
	text, isError := resultText(msg.Result)
	p.result.ToolCalls[idx].Text = text
	p.result.ToolCalls[idx].IsError = isError
	delete(p.pending, msg.CallID)
	if isError {
		fmt.Fprintf(&p.transcript, "! %s\n\n", text)
	} else {
		fmt.Fprintf(&p.transcript, "%s\n\n", text)
	}
	p.item(agent.ItemToolResult, p.result.ToolCalls[idx].Name, text, isError)
}

// itemType is the item's kind, from whichever of the two fields carries
// it -- see rawItem's own doc comment on why there are two.
func (i *rawItem) itemType() string {
	if i.ItemType != "" {
		return i.ItemType
	}
	return i.Type
}

// errorMessage is an error item's own text, from whichever of the two
// fields this build puts it in.
func (i *rawItem) errorMessage() string {
	if i.Message != "" {
		return i.Message
	}
	return i.Text
}

// toolResult reads what a completed MCP tool call returned, and whether
// it failed. The item's own status is authoritative about the second --
// a tool that reports an error in its text is still an error even when
// the result parses -- and the error field is preferred for the text
// when it has one, since that is where codex puts the failure's own
// words.
func (i *rawItem) toolResult() (string, bool) {
	failed := strings.EqualFold(i.Status, statusFailed)
	if i.Error != "" {
		return i.Error, true
	}
	text, isError := resultText(i.Result)
	return text, isError || failed
}

// output is what a finished command_execution wrote, preferring the
// aggregated stream codex reports and falling back to the separate
// stdout/stderr an older build sent.
func (m *rawMsg) output() string {
	if m.AggregatedOutput != "" {
		return m.AggregatedOutput
	}
	return strings.TrimSpace(strings.TrimSpace(m.Stdout) + "\n" + strings.TrimSpace(m.Stderr))
}

// toolName is the name an agent.ToolCall is recorded under: the tool's
// own identity, never a CLI's spelling of it (agent.ToolCall.Name's own
// doc comment, and mcp.BareToolName's, have what recording the prefixed
// name cost). codex reports the server and the tool as separate fields,
// so the common case needs no unwrapping at all -- but a build that
// reports the tool already qualified, either the "mcp__<server>__<tool>"
// form the other two CLIs use or a bare "<server>__<tool>", is unwrapped
// here rather than recorded under a name orchestrator.ProcessResult
// matches nothing against.
func toolName(server, tool string) string {
	name := mcp.BareToolName(tool)
	if server != "" {
		name = strings.TrimPrefix(name, server+"__")
	}
	return name
}

// decodeArguments reads a tool call's arguments, which arrive either as
// a JSON object or as a string holding one -- codex has sent both, the
// string being the model's own raw function-call payload passed through
// unparsed. Anything else (a JSON array, a number, nothing at all) reads
// as no arguments rather than as a failure: the call itself is the fact
// worth recording, and its arguments are for a human reading the
// transcript.
func decodeArguments(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err == nil {
		return args
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || s == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(s), &args); err != nil {
		return nil
	}
	return args
}

// resultText renders what an MCP tool call returned as the text an
// agent.ToolCall carries, and reports whether the result itself says the
// call failed.
//
// The shape is MCP's own tool result -- {"content":[{"type":"text",
// "text":...}],"isError":bool} -- but codex wraps it differently across
// builds: sometimes bare, sometimes inside the "Ok"/"Err" pair a Rust
// Result serializes to, and sometimes as a plain string. All three are
// read here, and anything else falls back to the raw JSON, which is
// worth more to somebody reading a transcript than an empty string is.
func resultText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, false
	}
	var wrapped struct {
		Ok  json.RawMessage `json:"Ok"`
		Err json.RawMessage `json:"Err"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil {
		switch {
		case len(wrapped.Err) > 0:
			text, _ := resultText(wrapped.Err)
			return text, true
		case len(wrapped.Ok) > 0:
			return resultText(wrapped.Ok)
		}
	}
	var content struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &content); err == nil && len(content.Content) > 0 {
		parts := make([]string, 0, len(content.Content))
		for _, c := range content.Content {
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
		return strings.Join(parts, " "), content.IsError
	}
	return string(raw), false
}

// commandText renders a command_execution's command, which is either a
// string or the argv list an older build sent.
func commandText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var argv []string
	if err := json.Unmarshal(raw, &argv); err == nil {
		return strings.Join(argv, " ")
	}
	return ""
}

// inputSummary renders a tool call's own arguments as compact JSON for a
// transcript line -- best-effort, since a malformed value here should
// cost the transcript one unreadable line, never the whole parse.
func inputSummary(args map[string]any) string {
	if args == nil {
		return ""
	}
	data, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf("%v", args)
	}
	return string(data)
}
