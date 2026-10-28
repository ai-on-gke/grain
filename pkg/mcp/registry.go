package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// Result is a tool's outcome -- the same two fields v1's ToolResult carries,
// text plus an error flag, both reported back to the model as a single text
// content block (isError never changes the JSON-RPC envelope itself: a
// failed tool call is still a successful RPC that answered "it failed").
type Result struct {
	Text    string
	IsError bool
}

// Tool is one entry in tools/list plus the function that answers tools/call
// for it. InputSchema is a plain JSON Schema object, exactly the shape v1's
// TOOLS list uses, so it can be handed to any client -- Gemini's function
// declarations are a straightforward translation of it, and so would
// Claude's or anything else's be.
//
// Handler receives the ctx the call arrived under -- bwsalmon/agents#346's
// own wiring gap: every Handler used to build its own context.Background()
// internally, so cancelling the agent.Framework.Run call driving it had no
// way to reach whatever exec.CommandContext a Handler started. A Handler
// that shells out (run_command, sshRunCommandTool) must pass ctx straight
// through so that killing it is actually possible from outside.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(ctx context.Context, args map[string]any) Result
	// ElisionAdvice is how this tool's caller gets back what the backstop
	// cut, in the caller's own vocabulary: "narrow it with limit", "read
	// one run's log with get_ci_log". Every answer is bounded
	// (maxResultTextBytes, applied in handleCall below), and a bounded
	// answer is only usable if it says what to do next -- which is a fact
	// about the tool, so the tool is where it is written down.
	//
	// Empty is the ordinary case and means elisionAdviceGeneric: a tool
	// whose answer is small enough that the backstop never fires has
	// nothing to say here, and neither has one grain did not write
	// (managed_http.go proxies tools whose arguments this package has
	// never seen).
	ElisionAdvice string
	// ReadBytes is read_file's own read, whole and byte for byte rather
	// than as numbered lines, for grain's use rather than the agent's:
	// it is how comment_on_issue and ask_question read a file a run
	// attaches by path (SandboxFileReaderOf). Nil on every other tool,
	// and never advertised.
	ReadBytes SandboxFileReader
}

// elisionAdvice is the sentence the backstop appends when it cuts this
// tool's answer.
func (t Tool) elisionAdvice() string {
	if t.ElisionAdvice != "" {
		return t.ElisionAdvice
	}
	return elisionAdviceGeneric
}

// Registry holds the tools one server run advertises and answers the three
// JSON-RPC methods a client needs: initialize, tools/list, tools/call.
// Handle is decoupled from stdio transport so it's directly testable with
// plain bytes in, bytes out -- no subprocess, no real pipes required,
// mirroring v1's McpServer.handle.
type Registry struct {
	tools map[string]Tool
	order []string
	// deadline, when AnnounceDeadline has been called, is when grain
	// cancels the run this registry serves -- read on every tools/call so
	// that a run approaching it is told so on every answer it gets. nil
	// (no deadline announced) leaves every result exactly as its handler
	// wrote it. See run_deadline.go.
	deadline *runDeadline
	// addenda, when AnnounceAddenda has been called, is what a person has
	// typed on this run's task since the run started -- polled on every
	// tools/call so that a comment posted while the run is in flight
	// reaches it on its next answer rather than waiting for the task's
	// next dispatch. nil (nobody to ask) leaves every result exactly as
	// its handler wrote it. See addenda.go.
	addenda *runAddenda
	// activity, when AnnounceActivity has been called, is where this
	// registry says what the run it serves is doing -- written before
	// every tools/call from the name of the tool being called, so the
	// task's row keeps up with a run whether or not the agent ever
	// spends a turn saying anything. nil (nobody to tell) narrates
	// nothing. See tool_activity.go.
	activity *toolActivity
	recorder *toolRecorder
	// budget, when AnnounceToolBudget has been called, counts this run's
	// calls and result bytes and adds a notice once it passes its budget.
	// nil counts nothing. See tool_budget.go.
	budget *toolBudget
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds tools to the registry, in the order given, appended after
// whatever is already registered. Registering a name a second time replaces
// its handler in place without changing tools/list's ordering.
func (r *Registry) Register(tools ...Tool) {
	for _, t := range tools {
		if _, exists := r.tools[t.Name]; !exists {
			r.order = append(r.order, t.Name)
		}
		r.tools[t.Name] = t
	}
}

// Handle answers one already-unmarshaled-into-bytes JSON-RPC request line
// and returns the response line to write back, or nil for a notification
// (no id) that has no response at all -- notifications/initialized is the
// only one a client sends here.
func (r *Registry) Handle(ctx context.Context, line []byte) []byte {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return mustMarshalLine(rpcResponse{
			JSONRPC: "2.0",
			ID:      nullID,
			Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
		})
	}

	switch req.Method {
	case "initialize":
		return mustMarshalLine(rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
			},
		})
	case "notifications/initialized":
		return nil
	case "tools/list":
		list := make([]map[string]any, 0, len(r.order))
		for _, name := range r.order {
			t := r.tools[name]
			list = append(list, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": t.InputSchema,
			})
		}
		return mustMarshalLine(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": list}})
	case "tools/call":
		return r.handleCall(ctx, req.ID, req.Params)
	default:
		if len(req.ID) == 0 {
			return nil
		}
		return mustMarshalLine(rpcResponse{
			JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: -32601, Message: fmt.Sprintf("unknown method %s", req.Method)},
		})
	}
}

func (r *Registry) handleCall(ctx context.Context, id json.RawMessage, params json.RawMessage) []byte {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return mustMarshalLine(rpcResponse{
				JSONRPC: "2.0", ID: id,
				Error: &rpcError{Code: -32602, Message: "invalid params: " + err.Error()},
			})
		}
	}

	t, ok := r.tools[p.Name]
	if !ok {
		return mustMarshalLine(rpcResponse{
			JSONRPC: "2.0", ID: id,
			Error: &rpcError{Code: -32602, Message: unknownToolMessage(p.Name)},
		})
	}

	args := p.Arguments
	if args == nil {
		args = map[string]any{}
	}
	// Before the handler, because the point of the line is to say what
	// the run is doing *while* it does it: a tool that blocks for a
	// quarter of an hour is exactly the one a person watching the row
	// wants named (tool_activity.go).
	r.reportActivity(ctx, t.Name, args)
	r.recordToolCall(t.Name, args)
	// The handler runs under a ctx carrying the announced deadline, so a
	// tool that blocks can bound itself by what is left of the run rather
	// than being cancelled part-way through a wait it chose blind (see
	// withRunDeadline, and wait_for_checks' own budget).
	result := t.Handler(r.withRunDeadline(ctx), args)
	// Bounded before anything of grain's own is added to it, so that a
	// handler which answered with more than a run can afford cannot cost
	// it the notices below -- and so that the bound is on the tool's
	// answer rather than on an answer plus however much a person happened
	// to type into the task while the call was in flight. A tool that
	// caps its own output is under the cap and passes through untouched
	// (result_size.go).
	text := elideMiddle(result.Text, maxResultTextBytes(), t.elisionAdvice())
	// The deadline notice goes on every answer, a failed one included:
	// the run is just as close to being cancelled when its command
	// exited 1, and that is exactly the turn where it might otherwise
	// start a long repair it has no time to push.
	// The budget counts what the tool answered, after the bound above and
	// before grain's own notices, so a run is charged for what it read.
	budgetNotice := r.budget.spend(len(text))
	text = withDeadlineNotice(text, r.deadlineNotice())
	text = withDeadlineNotice(text, budgetNotice)
	// And after it, a person's own words, if anybody has said anything
	// since the last answer -- last because it is the part of this
	// result a run is most likely to have to act on, and because the
	// poll behind it is only worth making once the handler has returned:
	// a wait_for_checks that blocked for a quarter of an hour picks up
	// everything said during it (addenda.go).
	text = withDeadlineNotice(text, r.addendaNotice(ctx))
	return mustMarshalLine(rpcResponse{
		JSONRPC: "2.0", ID: id,
		Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": result.IsError,
		},
	})
}
