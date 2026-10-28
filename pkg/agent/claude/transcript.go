package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// rawEvent is one --output-format stream-json line -- the same event shape
// grain/automation/transcript.py parses (see its own docstring for the
// field-by-field justification: each line is one Claude Code event, tagged
// by its own "type" field), narrowed to only what this package needs to
// build an agent.Result rather than a full trajectory viewer.
type rawEvent struct {
	Type    string      `json:"type"`
	Subtype string      `json:"subtype"`
	IsError bool        `json:"is_error"`
	Message *rawMessage `json:"message"`
	Result  string      `json:"result"`
	// NumTurns is the terminal result event's own count of the turns the
	// run spent, which is what claude measures its --max-turns in. Read
	// so that a run re-exec'd after a transient refusal can be handed
	// what is left of the run's budget rather than a second copy of it
	// (retry.go's attemptTurns).
	NumTurns int `json:"num_turns"`
	// SessionID is the session claude ran this exec in, which every event
	// on a --output-format stream-json run carries. It is what retry.go
	// passes back as --resume so an attempt after a transient refusal
	// picks the conversation up rather than starting the task again.
	SessionID string `json:"session_id"`
}

type rawMessage struct {
	Content json.RawMessage `json:"content"`
}

type rawBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// Thinking carries a "thinking"-type block's own text -- Claude's
	// extended-thinking output, present only when that feature is on, and
	// otherwise the one part of a real transcript neither FinalText nor
	// ToolCalls ever captured before bwsalmon/agents#446 gave it
	// somewhere to go (agent.Result.Transcript).
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	Input     map[string]any  `json:"input"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// parseTranscript turns a full --output-format stream-json capture into a
// Result: FinalText from the terminal "result" event, ToolCalls built by
// pairing each "tool_use" block in an assistant message with the
// "tool_result" block bearing the same tool_use_id wherever it later
// appears in a user message -- the same pairing transcript.py's TUI does to
// render a trajectory, done here to produce agent.ToolCall values instead
// of display strings. A malformed or unrecognized line is skipped rather
// than treated as fatal, matching transcript.py's own tolerance: Claude
// Code's event shape grows new fields and subtypes across versions, and
// this parser seeing one it doesn't know about should not make a whole run
// unreadable.
//
// The same pass also builds Result.Transcript: every "thinking" and
// "text" block's own text, and a line for each tool call and what it got
// back, in the order they appeared -- the human-readable narrative
// FinalText and ToolCalls alone do not give a reader (bwsalmon/agents#446).
func parseTranscript(stdout string) (*agent.Result, error) {
	p := parseEvents(stdout)
	if p.resultErr != nil {
		return nil, p.resultErr
	}
	if !p.sawResult {
		return nil, fmt.Errorf("claude: no result event found in output")
	}
	p.result.Transcript = strings.TrimSpace(p.transcript.String())
	return &p.result, nil
}

// ParseStreamJSON reads a finished --output-format stream-json capture
// into a Result, as this package does for its own runs. agent/custom uses
// it for a declared mapping whose CLI emits the same events.
func ParseStreamJSON(stdout string) (*agent.Result, error) { return parseTranscript(stdout) }

// PartialTranscript renders whatever of a still-in-progress
// --output-format stream-json capture is on disk so far into the same
// human-readable narrative parseTranscript builds once a run finishes --
// what LiveTranscriptDir reads back for a run with no FinishedAt yet
// (bwsalmon/agents#467). Unlike parseTranscript it never errors: a
// missing (or not-yet-written) terminal "result" event just means the run
// is still going, not that anything is wrong, and a truncated final line
// -- reading a file the whole time claude is still appending to it can
// always catch mid-write -- is handled the same way parseTranscript
// already tolerates any other malformed line, by skipping it.
func PartialTranscript(stdout string) string {
	return strings.TrimSpace(parseEvents(stdout).transcript.String())
}

// parsedEvents is one line-by-line pass over a stream-json capture,
// shared by parseTranscript (the whole thing, once a run has finished)
// and PartialTranscript (however much exists so far) so the two can never
// build the transcript text itself two different ways.
type parsedEvents struct {
	result  agent.Result
	pending map[string]int
	// sawInit records whether claude got as far as opening a session at
	// all -- its own "system"/"init" event, the first line of every
	// stream-json run, written before it so much as contacts the API.
	// retry.go reads it to tell an attempt the binary refused outright --
	// an argv it would not accept -- from one that started and then
	// failed; see that file's header.
	sawInit   bool
	sawResult bool
	// sessionID is the session claude ran this capture in, last non-empty
	// one wins. Last rather than first because a capture holds one
	// attempt per exec now that Framework.Run re-execs claude on a
	// transient refusal, and if a resume did not take -- or if the CLI
	// forks a resumed session under a new id, which is a thing it is free
	// to do -- the live session is the one the most recent attempt
	// reported rather than the one it was asked to continue.
	sessionID string
	resultErr error
	// resultSubtype is the terminal result event's own "subtype" when
	// that event reported a failure, kept apart from resultErr's
	// rendered text so a caller can branch on the machine-readable name
	// rather than by matching on a message (claude.go's runFailure).
	resultSubtype string
	// resultText is the terminal result event's own "result" field
	// verbatim, whether that event reported a failure or an ordinary
	// final answer -- kept separately from FinalText (which only a
	// successful result fills in) and from resultErr (which wraps it in
	// a sentence of this package's own) so usagelimit.go can read what
	// the CLI actually said without matching against text this file
	// wrote. Empty for a capture with no result event at all.
	resultText string
	// numTurns is what the result events in this capture said the run
	// spent, summed -- a capture holds one per exec of claude now that
	// Framework.Run re-execs it on a transient refusal (retry.go) --
	// and assistantMessages is the fallback count for an attempt that
	// was killed before it could report a total at all. Both are read by
	// retry.go's own turn accounting, and by nothing else.
	numTurns          int
	assistantMessages int
	transcript        strings.Builder
	// items is transcript, an item for each block it renders.
	items []agent.TranscriptItem
}

// maxTurnsSubtype is the result-event subtype claude reports when its own
// --max-turns budget ran out -- the single most common way a run that was
// working fine ends up an error, and the one runFailure translates into
// words an operator can act on.
const maxTurnsSubtype = "error_max_turns"

// sessionFromLine is the session one stream-json line names, or "" --
// what agent.SessionWatcher reads claude's live stream with, so that the
// session is on record from the first event (the "system"/"init" line
// carries it) rather than from parseTranscript once claude has exited.
// It is parseEvents over that one line, so the early reading and the
// final one are the same reading.
func sessionFromLine(line []byte) string {
	return parseEvents(string(line)).sessionID
}

func parseEvents(stdout string) *parsedEvents {
	p := &parsedEvents{pending: map[string]int{}}

	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev rawEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.SessionID != "" {
			p.sessionID = ev.SessionID
		}

		switch ev.Type {
		case "system":
			// The one subtype worth noticing: claude opens every
			// stream-json run with {"type":"system","subtype":"init",...}
			// and writes other system events (compaction and the like)
			// as the run goes. Nothing here renders any of them; what
			// retry.go needs is only whether a session was opened.
			if ev.Subtype == "init" {
				p.sawInit = true
			}
		case "assistant", "user":
			if ev.Message == nil {
				continue
			}
			if ev.Type == "assistant" {
				p.assistantMessages++
			}
			for _, b := range parseBlocks(ev.Message.Content) {
				switch b.Type {
				case "thinking":
					if b.Thinking != "" {
						fmt.Fprintf(&p.transcript, "%s\n\n", b.Thinking)
						p.items = append(p.items, agent.TranscriptItem{Kind: agent.ItemThinking, Text: b.Thinking})
					}
				case "text":
					if b.Text != "" {
						fmt.Fprintf(&p.transcript, "%s\n\n", b.Text)
						p.items = append(p.items, agent.TranscriptItem{Kind: agent.ItemText, Text: b.Text})
					}
				case "tool_use":
					// Bare, not as claude reported it: claude names every
					// tool it loaded from --mcp-config
					// "mcp__grain-sandbox__<tool>" (this package's own
					// allowedTools writes that prefix), and
					// agent.ToolCall.Name is the tool's identity rather
					// than one CLI's spelling of it. mcp.BareToolName's
					// own doc comment has what recording the prefixed
					// name cost.
					name := mcp.BareToolName(b.Name)
					p.result.ToolCalls = append(p.result.ToolCalls, agent.ToolCall{Name: name, Arguments: b.Input})
					p.pending[b.ID] = len(p.result.ToolCalls) - 1
					fmt.Fprintf(&p.transcript, "> %s(%s)\n", name, inputSummary(b.Input))
					p.items = append(p.items, agent.TranscriptItem{Kind: agent.ItemToolCall, Tool: name, Text: name + "(" + inputSummary(b.Input) + ")"})
				case "tool_result":
					if idx, ok := p.pending[b.ToolUseID]; ok {
						text := toolResultText(b.Content)
						p.result.ToolCalls[idx].Text = text
						p.result.ToolCalls[idx].IsError = b.IsError
						if b.IsError {
							fmt.Fprintf(&p.transcript, "! %s\n\n", text)
						} else {
							fmt.Fprintf(&p.transcript, "%s\n\n", text)
						}
						p.items = append(p.items, agent.TranscriptItem{Kind: agent.ItemToolResult, Tool: p.result.ToolCalls[idx].Name, Text: text, IsError: b.IsError})
					}
				}
			}
		case "result":
			p.sawResult = true
			p.resultText = ev.Result
			p.numTurns += ev.NumTurns
			// The last result event is what the run ended as. A single
			// capture used to hold exactly one, so clearing these was a
			// no-op; it holds one per attempt now that Framework.Run
			// re-execs claude on a transient refusal (retry.go), and an
			// earlier attempt's failure must not be what a caller is
			// handed for a run that went on to finish.
			p.resultSubtype, p.resultErr = "", nil
			if ev.IsError {
				p.resultSubtype = ev.Subtype
				p.resultErr = fmt.Errorf("claude: run ended in error (subtype=%s): %s", ev.Subtype, ev.Result)
				continue
			}
			p.result.FinalText = ev.Result
		}
	}
	return p
}

// inputSummary renders a tool_use block's own input as compact JSON for a
// transcript line -- best-effort, since a malformed value here should
// cost the transcript one unreadable line, never the whole parse (see
// parseTranscript's own doc comment on the same tolerance for the stream
// itself).
func inputSummary(input map[string]any) string {
	data, err := json.Marshal(input)
	if err != nil {
		return fmt.Sprintf("%v", input)
	}
	return string(data)
}

// parseBlocks reads a message's content field, which is either a plain
// string or a list of typed blocks (text, tool_use, tool_result) -- both
// shapes appear in a real transcript, per transcript.py's own _text_of.
func parseBlocks(raw json.RawMessage) []rawBlock {
	var blocks []rawBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return blocks
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return []rawBlock{{Type: "text", Text: s}}
	}
	return nil
}

// toolResultText reads a tool_result block's own content field, which has
// the same string-or-blocks shape as a message's content -- transcript.py's
// _text_of joins multiple text blocks with a space; this does the same.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		parts := make([]string, 0, len(blocks))
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}
