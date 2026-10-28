package orchestrator_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// An escape hatch is two halves in two packages with nothing checking
// them against each other. pkg/mcp declares what an agent is offered --
// the tool's name and the arguments its schema says it takes -- and
// ProcessResult (finish.go) recovers a finished run's calls by matching
// that same name and reading those same argument keys off
// agent.Result.ToolCalls, as string literals of its own.
//
// Nothing makes the two agree, and a disagreement is silent in the worst
// possible way: the run makes its call, the tool answers "Recorded", the
// agent stops as instructed -- and ProcessResult matches nothing, so the
// question is never asked, the closing comment is never relayed, the
// proposal is never filed and the secret is never requested. The run is
// recorded no_action and the words exist nowhere, since agent.Result is
// not persisted. That has happened twice already for the *name* half of
// this, through the two prefixes a CLI puts on an MCP tool
// (mcp.BareToolName's own doc comment tells both stories); the argument
// half has never had a test at all.
//
// Every existing test on both sides writes the names and the keys out by
// hand, which is exactly what keeps a rename invisible to them: rename
// the argument in pkg/mcp's schema and its handler, leave finish.go's
// literal alone, and both packages' suites still pass while every real
// run loses whatever it said.
//
// So this test writes down neither half twice. It builds each call's
// arguments out of the tool's own InputSchema, checks the tool's own
// handler accepts what its schema declares, and then asserts
// ProcessResult produced the effect that hatch exists for, carrying
// those very values.

// escapeHatchArguments is the argument keys ProcessResult reads for each
// hatch (finish.go's firstToolCallArg and relayProposedTasks calls), and
// the whole of what this test knows about finish.go. Each must be a
// required property of the tool of that name, or the relay silently
// stops working.
var escapeHatchArguments = map[string][]string{
	"ask_question":       {"question", "summary"},
	"comment_on_issue":   {"comment"},
	"request_secret":     {"secret", "reason", "summary"},
	"propose_task":       {"title", "body"},
	"add_review_comment": {"body"},
}

func TestEscapeHatchArgumentsAreTheOnesProcessResultReads(t *testing.T) {
	t.Parallel()
	tools := map[string]mcp.Tool{}
	for _, tool := range mcp.NewMockTools(&mcp.MockSink{}, nil) {
		tools[tool.Name] = tool
	}

	for _, tt := range []struct {
		tool string
		// effect is what ProcessResult owes a run that made this call,
		// checked against the arguments built from the tool's own schema.
		effect func(t *testing.T, ctx context.Context, store *model.Store, task model.Task, args map[string]any)
	}{
		{
			tool: "ask_question",
			effect: func(t *testing.T, ctx context.Context, store *model.Store, task model.Task, args map[string]any) {
				comments := commentBodies(t, ctx, store, task.ID)
				if len(comments) != 1 || comments[0] != args["question"] {
					t.Fatalf("conversation = %q, want the question the run asked (%q)",
						comments, args["question"])
				}
				obs, err := store.GetObservation(ctx, task.ID)
				if err != nil {
					t.Fatal(err)
				}
				if obs == nil || obs.PendingQuestionCommentID == nil {
					t.Fatal("the task was not parked on the question")
				}
			},
		},
		{
			tool: "comment_on_issue",
			effect: func(t *testing.T, ctx context.Context, store *model.Store, task model.Task, args map[string]any) {
				comments := commentBodies(t, ctx, store, task.ID)
				if len(comments) != 1 || comments[0] != args["comment"] {
					t.Fatalf("conversation = %q, want the run's closing comment (%q)",
						comments, args["comment"])
				}
			},
		},
		{
			tool: "request_secret",
			effect: func(t *testing.T, ctx context.Context, store *model.Store, task model.Task, args map[string]any) {
				obs, err := store.GetObservation(ctx, task.ID)
				if err != nil {
					t.Fatal(err)
				}
				if obs == nil || obs.PendingSecret != args["secret"] {
					t.Fatalf("pending secret = %+v, want the name the run asked for (%q)",
						obs, args["secret"])
				}
				comments := commentBodies(t, ctx, store, task.ID)
				if len(comments) != 1 {
					t.Fatalf("conversation = %q, want the relayed request", comments)
				}
				// The run's own reason is the whole of what the human
				// deciding what to paste has to go on, so it has to
				// survive the trip as well as the name does.
				if !strings.Contains(comments[0], args["reason"].(string)) {
					t.Errorf("relayed request = %q, want the run's own reason (%q) in it",
						comments[0], args["reason"])
				}
			},
		},
		{
			tool: "propose_task",
			effect: func(t *testing.T, ctx context.Context, store *model.Store, task model.Task, args map[string]any) {
				tasks, err := store.ListTasks(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var proposal *model.Task
				for i := range tasks {
					if tasks[i].ID != task.ID {
						proposal = &tasks[i]
					}
				}
				if proposal == nil {
					t.Fatal("the proposed task was never filed")
				}
				if proposal.Title != args["title"] {
					t.Errorf("title = %q, want the proposed one (%q)", proposal.Title, args["title"])
				}
				if !strings.Contains(proposal.Body, args["body"].(string)) {
					t.Errorf("body = %q, want the proposed one (%q) in it", proposal.Body, args["body"])
				}
			},
		},
		{
			// path and line are optional, so what this builds is a
			// finding tied to no line -- and the task it is made against
			// reviews nothing, so the effect owed is the fallback:
			// relayed into that task's own conversation. The draft review
			// a review task's own calls become is
			// reviewfeedback_test.go's subject; what is pinned here is
			// only that finish.go still reads the key pkg/mcp requires.
			tool: "add_review_comment",
			effect: func(t *testing.T, ctx context.Context, store *model.Store, task model.Task, args map[string]any) {
				comments := commentBodies(t, ctx, store, task.ID)
				if len(comments) != 1 || !strings.Contains(comments[0], args["body"].(string)) {
					t.Fatalf("conversation = %q, want the run's finding (%q) in it",
						comments, args["body"])
				}
			},
		},
	} {
		t.Run(tt.tool, func(t *testing.T) {
			tool, ok := tools[tt.tool]
			if !ok {
				t.Fatalf("pkg/mcp registers no tool named %q, which finish.go still matches "+
					"against: a run calling it would be relayed nowhere", tt.tool)
			}
			args := declaredStringArguments(t, tool)
			for _, key := range escapeHatchArguments[tt.tool] {
				if _, ok := args[key]; !ok {
					t.Fatalf("%s no longer declares %q as a required string argument, and "+
						"finish.go still reads that key off the call", tt.tool, key)
				}
			}
			// The schema and the handler are two more halves that can
			// disagree: a required property the handler does not read is
			// a call an agent makes correctly and grain rejects.
			if res := tool.Handler(context.Background(), args); res.IsError {
				t.Fatalf("%s rejected the arguments its own schema requires: %s", tt.tool, res.Text)
			}

			store, ctx := openStore(t)
			_, client := newSim(t, "acme", "widgets", "main")
			task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})

			result := toolResult(agent.ToolCall{Name: tool.Name, Arguments: args})
			if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
				t.Fatalf("ProcessResult: %v", err)
			}
			tt.effect(t, ctx, store, task, args)
		})
	}
}

// declaredStringArguments builds one call's arguments out of the tool's
// own schema: every required property of type string, with a value that
// names the key it came from, so an assertion can tell one argument's
// text from another's in whatever the relay produced.
//
// Required only, and strings only, because that is what these four
// hatches take -- a hatch that grows an argument of another shape wants
// this to say so rather than to guess at a value for it.
func declaredStringArguments(t *testing.T, tool mcp.Tool) map[string]any {
	t.Helper()
	schema, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s declares no properties", tool.Name)
	}
	required, ok := tool.InputSchema["required"].([]string)
	if !ok {
		t.Fatalf("%s declares no required arguments", tool.Name)
	}
	args := map[string]any{}
	for _, key := range required {
		prop, ok := schema[key].(map[string]any)
		if !ok {
			t.Fatalf("%s requires %q but declares no property for it", tool.Name, key)
		}
		if prop["type"] != "string" {
			t.Fatalf("%s requires %q, which is a %v rather than a string: this test builds "+
				"string arguments only", tool.Name, key, prop["type"])
		}
		// A value that is a legal secret name as well as distinctive
		// text: request_secret refuses one it could not store under, and
		// the point here is to exercise the relay rather than that
		// refusal (pkg/mcp's own tests cover it).
		args[key] = fmt.Sprintf("declared-%s", key)
	}
	return args
}
