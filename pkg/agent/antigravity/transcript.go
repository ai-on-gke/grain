package antigravity

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// rawEvent is one `--output-format stream-json` line. agy's stream is
// NDJSON with a single "event" discriminator and the payload nested under
// a key of that same name -- unlike claude's stream-json, where every
// field sits at the top level next to a "type" tag (see agent/claude's
// own rawEvent). The three events a run produces are "init" (once, first:
// session capabilities and the tool roster agy actually loaded),
// "step_update" (many: one per state transition of one numbered step) and
// "result" (once, last: the final answer and terminal status).
//
// Only what building an agent.Result needs is modeled here, not agy's
// whole event vocabulary -- the same narrowing agent/claude's parser
// does, and for the same reason: an unrecognized field or event name
// should cost this parser nothing.
//
// ConversationID is agy's own handle on the session the run is happening
// in, and the one field here that is not about rendering a transcript:
// it is what Framework.Run passes back as --conversation to resume a run
// a transient provider refusal cut short (see resume.go). agy writes it
// in two places -- at the top level of the init event, and nested inside
// the step_update and result payloads -- so it is read off all three
// rather than off whichever one a capture happens to start with.
type rawEvent struct {
	Event          string     `json:"event"`
	Diagnostic     string     `json:"grain_diagnostic,omitempty"`
	ConversationID string     `json:"conversation_id"`
	Init           *rawInit   `json:"init"`
	Step           *rawStep   `json:"step_update"`
	Result         *rawResult `json:"result"`
}

// rawInit is the opening event's payload. Tools is the roster agy loaded
// from its MCP settings -- what verifyToolRoster checks against the tools
// grain's own "mcpserver" subcommand advertises, since agy has no
// --strict-mcp-config equivalent to enforce that up front (see
// Framework.Run's own doc comment on the roster gap).
type rawInit struct {
	CWD            string   `json:"cwd"`
	Tools          []string `json:"tools"`
	PermissionMode string   `json:"permission_mode"`
}

// rawStep is one step_update payload. A step is identified by StepIndex
// and reported more than once as it advances: an "ACTIVE" update when it
// starts, then a terminal "DONE" or "ERROR" one. Tool output and tool
// errors arrive nested under tool_info rather than at the step's own top
// level, which is why ToolInfo carries Output and Error of its own
// alongside the step's.
type rawStep struct {
	ConversationID string       `json:"conversation_id"`
	StepIndex      int          `json:"step_index"`
	State          string       `json:"state"`
	StepType       string       `json:"step_type"`
	ToolName       string       `json:"tool_name"`
	ToolInfo       *rawToolInfo `json:"tool_info"`
	Output         string       `json:"output"`
	TextDelta      string       `json:"text_delta"`
	Error          *rawError    `json:"error"`
}

type rawToolInfo struct {
	Name       string         `json:"name"`
	Parameters map[string]any `json:"parameters"`
	Output     string         `json:"output"`
	Error      *rawError      `json:"error"`
}

type rawError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// rawResult is the terminal event's payload. Status is agy's own terminal
// verdict; Response is the final answer text.
type rawResult struct {
	ConversationID string `json:"conversation_id"`
	Status         string `json:"status"`
	Response       string `json:"response"`
	Error          string `json:"error"`
	NumTurns       int    `json:"num_turns"`
}

// Step types and states this parser acts on. Anything else is carried
// through the loop untouched -- a "checkpoint" or "user_input" step is
// real, it just contributes nothing to a Result.
const (
	stepTypeAgentResponse = "agent_response"
	stepTypeTool          = "tool"

	stateActive = "ACTIVE"
	stateDone   = "DONE"
	stateError  = "ERROR"
)

// succeeded reports whether a terminal result status is agy's success.
// Two spellings exist in the wild -- "SUCCESS" on current builds, "OK" on
// older ones -- and the binary also emits FAILURE, CANCELLED and TIMEOUT.
// Anything not explicitly successful is read as a failure rather than
// matched against that list, so a status this code has never seen fails
// closed instead of silently passing for a completed run.
func succeeded(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "SUCCESS", "OK":
		return true
	default:
		return false
	}
}

// parseTranscript turns a full stream-json capture into a Result:
// FinalText from the terminal "result" event's response, ToolCalls built
// by pairing each tool step's ACTIVE update (which carries the call's own
// name and parameters) with the DONE or ERROR update bearing the same
// step_index (which carries what it returned). That pairing is by step
// index rather than by an opaque id because agy numbers its steps and
// reuses no ids -- the one structural difference from agent/claude's
// tool_use_id pairing.
//
// A malformed or unrecognized line is skipped rather than treated as
// fatal, matching agent/claude's own tolerance and for the same reason:
// agy's event shape grows new fields and step types across versions, and
// this parser meeting one it does not know should not make a whole run
// unreadable.
func parseTranscript(stdout string) (*agent.Result, error) {
	return parseTranscriptWithRecorded(stdout, nil)
}

func parseTranscriptWithRecorded(stdout string, recorded []mcp.ToolCallRecord) (*agent.Result, error) {
	p := parseEventsWithRecorded(stdout, recorded)
	if !p.sawResult {
		return nil, fmt.Errorf("antigravity: no result event found in output")
	}
	p.result.Transcript = strings.TrimSpace(p.transcript.String())
	if p.resultErr != nil {
		// The Result travels back alongside the error, never instead of
		// it: agent.Framework's own contract (and the failure recorded in
		// gemini.Framework.Run's doc comment before this package replaced
		// it) is that a run which edited, committed and pushed before
		// failing has already changed the world, and a caller handed only
		// an error strands that work.
		return &p.result, p.resultErr
	}
	return &p.result, nil
}

// PartialTranscript renders whatever of a still-in-progress stream-json
// capture is on disk so far into the same human-readable narrative
// parseTranscript builds once a run finishes -- what LiveTranscriptDir
// reads back for a run with no FinishedAt yet. Unlike parseTranscript it
// never errors: a missing terminal "result" event just means the run is
// still going, and a truncated final line -- reading a file while agy is
// still appending to it can always catch mid-write -- is skipped the same
// way any other unparseable line is.
func PartialTranscript(stdout string) string {
	return strings.TrimSpace(parseEvents(stdout).transcript.String())
}

// PartialItems is PartialTranscript's narrative as the items it is
// written from, in the same order. A response is one item once its step
// is done, not one per delta: an item is whole when it is listed
// (agent.TranscriptItem), so one still streaming is not listed yet.
func PartialItems(stdout string) []agent.TranscriptItem {
	return parseEvents(stdout).items
}

// item records one paragraph of the narrative as an item too.
func (p *parsedEvents) item(kind, tool, text string, isError bool) {
	p.items = append(p.items, agent.TranscriptItem{Kind: kind, Tool: tool, Text: text, IsError: isError})
}

// parsedEvents is one line-by-line pass over a stream-json capture,
// shared by parseTranscript (the whole thing, once a run finished) and
// PartialTranscript (however much exists so far) so the two can never
// build the narrative two different ways.
type parsedEvents struct {
	result agent.Result
	// items is transcript, an item for each paragraph it renders.
	items []agent.TranscriptItem
	// pending maps a tool step's step_index to its slot in
	// result.ToolCalls, so the later DONE/ERROR update for that same
	// index can fill in what the call returned.
	pending map[int]int
	// spoke records which agent_response steps actually produced text.
	// A model turn that only decided to call a tool carries none, and
	// must not punctuate the transcript with a blank paragraph for
	// having said nothing.
	spoke map[int]bool
	// agentResponses maps an agent_response step's step_index to its accumulated text.
	agentResponses map[int]*strings.Builder
	tools          []string
	// sawInit records whether agy got as far as opening a session at
	// all. Framework.Run reads it to tell an attempt the binary refused
	// outright -- an argv it would not accept -- from one that started
	// and then failed; see resume.go.
	sawInit   bool
	sawResult bool
	// lastStepDoneResponse records whether the most recent step_update in
	// this capture completed a non-empty agent_response step. When agy
	// resumes a conversation (--conversation <id>) that contains an ERROR
	// step from an earlier attempt, agy still finishes the new turn's own
	// agent_response (state DONE) and populates result.response, but marks
	// the terminal result event status ERROR with that historical step's
	// error and exits 1.
	lastStepDoneResponse bool
	// conversationID is the session agy ran this capture in, last
	// non-empty one wins. Last rather than first because a capture can
	// hold more than one attempt now (Framework.Run re-execs agy on a
	// transient refusal), and if a resume did not take, the live
	// conversation is the one the most recent attempt opened rather
	// than the one it was asked to continue.
	conversationID string
	// turns counts completed agent_response steps -- what Run's own
	// turn cap is measured in, agy having no --max-turns flag of its
	// own to pass one down to (see Framework.Run).
	turns     int
	resultErr error
	// resultStatus is the terminal result event's own status, upper-cased
	// and last one wins, for a failed capture -- "" for one that
	// succeeded or never reached a result at all. resume.go reads it:
	// which word agy failed under is what says whether the run can be
	// picked back up or has ended on its own terms.
	resultStatus string
	// resultText is the terminal result event's own error (or, failing
	// that, its response) verbatim -- what agy itself said about how the
	// run ended, kept apart from resultErr's rendered sentence so
	// usagelimit.go can read the provider's own words rather than text
	// this file wrote. Empty for a capture with no result event.
	resultText  string
	transcript  strings.Builder
	recorded    []mcp.ToolCallRecord
	recordedIdx int
}

// conversationFromLine is the conversation one stream line names, or ""
// -- what agent.SessionWatcher reads agy's live stream with. It is
// parseEvents over that one line, so the early reading and the final one
// agree.
func conversationFromLine(line []byte) string {
	return parseEvents(string(line)).conversationID
}

func parseEvents(stdout string) *parsedEvents {
	return parseEventsWithRecorded(stdout, nil)
}

func parseEventsWithRecorded(stdout string, recorded []mcp.ToolCallRecord) *parsedEvents {
	p := &parsedEvents{
		pending:        map[int]int{},
		spoke:          map[int]bool{},
		agentResponses: map[int]*strings.Builder{},
		recorded:       recorded,
	}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev rawEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		p.noteConversation(ev.ConversationID)
		switch ev.Event {
		case diagnosticEventName:
			if ev.Diagnostic != "" {
				fmt.Fprintf(&p.transcript, "\n\n%s\n\n", ev.Diagnostic)
				p.item(agent.ItemError, "", ev.Diagnostic, true)
			}
		case "init":
			p.sawInit = true
			if ev.Init != nil {
				p.tools = ev.Init.Tools
			}
		case "step_update":
			if ev.Step != nil {
				p.noteConversation(ev.Step.ConversationID)
				p.applyStep(ev.Step)
			}
		case "result":
			if ev.Result == nil {
				continue
			}
			p.noteConversation(ev.Result.ConversationID)
			p.sawResult = true
			resumedTurnCompleted := strings.EqualFold(strings.TrimSpace(ev.Result.Status), stateError) &&
				p.lastStepDoneResponse && strings.TrimSpace(ev.Result.Response) != ""
			if !succeeded(ev.Result.Status) && !resumedTurnCompleted {
				detail := ev.Result.Error
				if detail == "" {
					detail = ev.Result.Response
				}
				p.resultText = detail
				p.resultStatus = strings.ToUpper(strings.TrimSpace(ev.Result.Status))
				p.resultErr = fmt.Errorf("antigravity: run ended in status %s: %s", ev.Result.Status, detail)
				continue
			}
			p.result.FinalText = ev.Result.Response
			if p.result.FinalText == "" {
				p.result.FinalText = p.lastAgentResponse()
			} else if p.lastAgentResponse() != "" && len(p.lastAgentResponse()) > len(p.result.FinalText) {
				p.result.FinalText = p.lastAgentResponse()
			}
			// The last result event is what the run ended as. A single
			// capture used to hold exactly one, so this cleared nothing;
			// it holds one per attempt now that Framework.Run re-execs
			// agy on a transient refusal, and an earlier attempt's
			// failure must not be what a caller is handed for a run that
			// went on to finish.
			p.resultErr, p.resultText, p.resultStatus = nil, "", ""
		}
	}
	return p
}

// lastAgentResponse returns the text of the latest agent_response step that produced text.
func (p *parsedEvents) lastAgentResponse() string {
	var maxIdx = -1
	for idx, b := range p.agentResponses {
		if strings.TrimSpace(b.String()) != "" {
			if maxIdx == -1 || idx > maxIdx {
				maxIdx = idx
			}
		}
	}
	if maxIdx == -1 {
		return ""
	}
	return strings.TrimSpace(p.agentResponses[maxIdx].String())
}

// noteConversation records the session id carried by an event, ignoring
// the empty string an event that does not carry one unmarshals to.
func (p *parsedEvents) noteConversation(id string) {
	if id != "" {
		p.conversationID = id
	}
}

// applyStep folds one step_update into the Result and the narrative.
func (p *parsedEvents) applyStep(s *rawStep) {
	p.lastStepDoneResponse = false
	switch s.StepType {
	case stepTypeAgentResponse:
		// Response text arrives as text_delta chunks while the step is
		// ACTIVE and, on some builds, whole in output on the DONE
		// update. Appending both would print it twice, so a DONE update
		// only contributes output the deltas did not already carry.
		b, ok := p.agentResponses[s.StepIndex]
		if !ok {
			b = &strings.Builder{}
			p.agentResponses[s.StepIndex] = b
		}
		if s.TextDelta != "" {
			fmt.Fprint(&p.transcript, s.TextDelta)
			b.WriteString(s.TextDelta)
			p.spoke[s.StepIndex] = true
		}
		if s.State == stateDone {
			if s.TextDelta == "" && s.Output != "" {
				fmt.Fprint(&p.transcript, s.Output)
				b.WriteString(s.Output)
				p.spoke[s.StepIndex] = true
			}
			// A turn that said nothing -- the model deciding to call a
			// tool and nothing else -- still counts as a turn, but
			// contributes no paragraph to the narrative.
			if p.spoke[s.StepIndex] {
				fmt.Fprint(&p.transcript, "\n\n")
				p.item(agent.ItemText, "", b.String(), false)
				p.lastStepDoneResponse = true
			}
			p.turns++
		}
	case stepTypeTool:
		switch s.State {
		case stateActive:
			name, args := s.toolCall()
			if p.recordedIdx < len(p.recorded) {
				rec := p.recorded[p.recordedIdx]
				if rec.Name == name || mcp.AgyQualifiedToolName(rec.Name) == name {
					args = rec.Arguments
					p.recordedIdx++
				}
			}
			p.result.ToolCalls = append(p.result.ToolCalls, agent.ToolCall{Name: name, Arguments: args})
			p.pending[s.StepIndex] = len(p.result.ToolCalls) - 1
			fmt.Fprintf(&p.transcript, "> %s(%s)\n", name, inputSummary(args))
			p.item(agent.ItemToolCall, name, name+"("+inputSummary(args)+")", false)
		case stateDone, stateError:
			text, isError := s.toolResult()
			idx, ok := p.pending[s.StepIndex]
			if !ok {
				// A terminal update with no ACTIVE one before it (a
				// capture that starts mid-run, which PartialTranscript
				// reads all the time). Record the call anyway rather
				// than dropping what it returned.
				name, args := s.toolCall()
				if p.recordedIdx < len(p.recorded) {
					rec := p.recorded[p.recordedIdx]
					if rec.Name == name || mcp.AgyQualifiedToolName(rec.Name) == name {
						args = rec.Arguments
						p.recordedIdx++
					}
				}
				p.result.ToolCalls = append(p.result.ToolCalls, agent.ToolCall{Name: name, Arguments: args})
				idx = len(p.result.ToolCalls) - 1
				fmt.Fprintf(&p.transcript, "> %s(%s)\n", name, inputSummary(args))
				p.item(agent.ItemToolCall, name, name+"("+inputSummary(args)+")", false)
			}
			p.result.ToolCalls[idx].Text = text
			p.result.ToolCalls[idx].IsError = isError
			delete(p.pending, s.StepIndex)
			if isError {
				fmt.Fprintf(&p.transcript, "! %s\n\n", text)
			} else {
				fmt.Fprintf(&p.transcript, "%s\n\n", text)
			}
			p.item(agent.ItemToolResult, p.result.ToolCalls[idx].Name, text, isError)
		}
	}
}

// mcpDispatcherTool is agy's own native tool for calling a *lazily*
// loaded MCP tool: one call taking the server, the tool and the
// arguments, rather than the tool itself appearing in the roster.
// Framework.Run configures grain's tools eagerly precisely so a run does
// not have to go through it (see eagerToolsConfig), but the dispatcher
// remains in every roster and a model may still reach for it, so
// unwrapDispatch below reads that shape too.
const mcpDispatcherTool = "call_mcp_tool"

// The three parameters mcpDispatcherTool takes. Capitalized as agy spells
// them, which is not the casing MCP tools' own arguments use.
const (
	dispatchServerKey = "ServerName"
	dispatchToolKey   = "ToolName"
	dispatchArgsKey   = "Arguments"
)

// toolCall reads a tool step's name and arguments, preferring the nested
// tool_info (where agy puts them) and falling back to the step's own
// tool_name so a build that reports only the flat field still names the
// call correctly.
//
// The name comes back bare rather than as agy reported it -- and a call
// made through agy's MCP dispatcher comes back as the tool it dispatched
// to. agent.ToolCall.Name is the tool's identity, not one CLI's spelling
// of it, and agy has three spellings for the same call: the tool's own
// name for a native tool, "mcp_grain-sandbox_<tool>" for an MCP tool it
// registered eagerly, and "call_mcp_tool" with the real name in its
// arguments for one it did not. mcp.BareToolName's own doc comment has
// what recording the wrong one cost, twice.
func (s *rawStep) toolCall() (string, map[string]any) {
	name := s.ToolName
	var args map[string]any
	if s.ToolInfo != nil {
		if s.ToolInfo.Name != "" {
			name = s.ToolInfo.Name
		}
		args = s.ToolInfo.Parameters
	}
	if dispatched, dispatchedArgs, ok := unwrapDispatch(name, args); ok {
		return dispatched, dispatchedArgs
	}
	return mcp.BareToolName(name), args
}

// unwrapDispatch reads a mcpDispatcherTool call as the call it actually
// made: {"ServerName":"grain-sandbox","ToolName":"comment_on_issue",
// "Arguments":{"comment":"..."}} is a comment_on_issue call with that
// comment, and recording it as "call_mcp_tool" instead is what makes
// orchestrator.ProcessResult -- which matches the bare name exactly --
// relay nothing.
//
// A dispatch naming some other server is left alone: it is a call this
// grain did not publish and has no business renaming. So is one naming no
// tool, which cannot be attributed to anything.
func unwrapDispatch(name string, args map[string]any) (string, map[string]any, bool) {
	if name != mcpDispatcherTool || args == nil {
		return "", nil, false
	}
	if server, ok := args[dispatchServerKey].(string); !ok || server != mcp.ToolNamespace {
		return "", nil, false
	}
	tool, ok := args[dispatchToolKey].(string)
	if !ok || tool == "" {
		return "", nil, false
	}
	// Absent or of some other shape means a tool called with no
	// arguments, which is ordinary -- open_pull_request takes none.
	dispatched, _ := args[dispatchArgsKey].(map[string]any)
	return mcp.BareToolName(tool), dispatched, true
}

// toolResult reads what a terminal tool step returned. agy nests both
// output and error detail under tool_info; the step's own output/error
// are read as a fallback for the same reason toolCall reads tool_name.
func (s *rawStep) toolResult() (string, bool) {
	if s.ToolInfo != nil {
		if e := s.ToolInfo.Error; e != nil && (e.Message != "" || e.Type != "") {
			return errorText(e), true
		}
		if s.ToolInfo.Output != "" {
			return s.ToolInfo.Output, s.State == stateError
		}
	}
	if e := s.Error; e != nil && (e.Message != "" || e.Type != "") {
		return errorText(e), true
	}
	return s.Output, s.State == stateError
}

func errorText(e *rawError) string {
	switch {
	case e.Message != "" && e.Type != "":
		return fmt.Sprintf("%s: %s", e.Type, e.Message)
	case e.Message != "":
		return e.Message
	default:
		return e.Type
	}
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
