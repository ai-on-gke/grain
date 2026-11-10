// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// escapeHatchText returns each escape hatch's description and the
// confirmation its handler answers a well-formed call with -- the two
// pieces of text a dispatched run actually reads about these tools.
func escapeHatchText(t *testing.T) map[string]struct{ description, confirmation string } {
	t.Helper()
	args := map[string]map[string]any{
		"ask_question":   {"question": "which config file?"},
		"request_secret": {"secret": "stripe-api-key", "reason": "the deploy script authenticates with it"},
		"request_access": {"summary": "Grant read of acme/lib", "reason": "the fix spans both repos",
			"requests": []any{map[string]any{"kind": "read", "repo": "acme/lib"}}},
		"comment_on_issue":   {"comment": "the answer is 4"},
		"nothing_needed":     {"reason": "the flag is already off on main"},
		"write_handover":     {"handover": "the merge lane may only ever hold one branch"},
		"compact_context":    {"handover": "stage 1 is built; stage 2 is the merge lane"},
		"update_brief":       {"brief": "keep the merge lane green", "reason": "stage 1 is finished"},
		"propose_task":       {"title": "follow-up", "body": "the rest of it"},
		"add_review_comment": {"body": "this loop is quadratic"},
	}
	out := map[string]struct{ description, confirmation string }{}
	for _, tool := range NewMockTools(&MockSink{}, nil) {
		call, ok := args[tool.Name]
		if !ok {
			t.Fatalf("NewMockTools registered %q, which this test has no call for", tool.Name)
		}
		res := tool.Handler(context.Background(), call)
		if res.IsError {
			t.Fatalf("%s answered a well-formed call with an error: %s", tool.Name, res.Text)
		}
		out[tool.Name] = struct{ description, confirmation string }{tool.Description, res.Text}
	}
	if len(out) != len(args) {
		t.Fatalf("NewMockTools returned %d tools, want %d", len(out), len(args))
	}
	return out
}

// TestEscapeHatchTextDescribesTheRelayRatherThanTheSink is the
// regression test for what these tools told every production run for as
// long as there was nothing downstream of them: that the call had been
// mocked, that no comment was posted, that a proposal needed a trigger
// label on a GitHub issue. orchestrator.ProcessResult relays
// ask_question, comment_on_issue and propose_task for real off
// agent.Result.ToolCalls, and has since tasks became rows, so every one
// of those sentences was false where it was read.
//
// It is pinned as a test because the falsehood is invisible from inside
// this package: MockSink really does mock, `grain mcpserver` really does
// build one and throw it away, and only the caller two packages over
// makes the text wrong. Nothing else fails when this drifts back -- the
// cost lands on a run that believes it (docs/agent-ergonomics.md,
// finding 1), which no test of grain's own behaviour would ever see.
func TestEscapeHatchTextDescribesTheRelayRatherThanTheSink(t *testing.T) {
	// Each of these is a sentence one of these tools used to carry, or
	// the vocabulary of the v1 workflow they used to describe: an issue
	// per task, a label to re-apply, a label to file a proposal under.
	// None of it survives in v2, where a task is a row and its
	// conversation is in grain's own UI.
	stale := []string{"mocked", "trigger label", "/lgtm", "new GitHub issue", "issue number"}
	for name, text := range escapeHatchText(t) {
		for _, phrase := range stale {
			if strings.Contains(strings.ToLower(text.description), strings.ToLower(phrase)) {
				t.Errorf("%s's description still says %q", name, phrase)
			}
			if strings.Contains(strings.ToLower(text.confirmation), strings.ToLower(phrase)) {
				t.Errorf("%s's confirmation still says %q", name, phrase)
			}
		}
	}
}

// TestRelayedEscapeHatchesConfirmWhatGrainDoesWithTheCall holds each of
// the three relayed hatches to saying, in its confirmation, the thing
// the agent has to know at the moment it calls: that the words go
// somewhere, when, and what happens to the task as a result. A
// confirmation is the only one of the two texts a run reads *after*
// deciding to call, and it is the one that decides what it does next --
// ask again, carry on, or stop.
func TestRelayedEscapeHatchesConfirmWhatGrainDoesWithTheCall(t *testing.T) {
	text := escapeHatchText(t)
	for _, tt := range []struct {
		tool string
		want []string
	}{
		// A question is relayed into the conversation, parks the task,
		// and ends the turn -- the contract ProcessResult's ordering
		// depends on, and the one thing here that is unchanged from v1.
		{"ask_question", []string{"relays", "conversation", "replies", "no further actions"}},
		// A secret request parks the task the same way, and its
		// confirmation has one more thing to be unambiguous about: the
		// value is not coming back. A run told otherwise waits for it,
		// or asks for it again in a question -- which is exactly the
		// plain-text comment this tool exists to avoid.
		{"request_secret", []string{"conversation", "parks", "never comes back to you", "no further actions"}},
		// A comment is relayed whatever else the run did, and completes
		// the task when it is all the run did.
		{"comment_on_issue", []string{"relays", "conversation", "closing note"}},
		// nothing_needed ends the run like a closing note does, and says
		// so -- a run that carries on after it has misread it.
		{"nothing_needed", []string{"closing comment", "no further actions"}},
		// A handover is relayed as a comment too, and its confirmation
		// has the opposite thing to be unambiguous about: it does *not*
		// complete the task and is not the run's ending, so a run that
		// wrote one has to carry on with the work it was dispatched for.
		{"write_handover", []string{"conversation", "handover", "does not complete the task"}},
		// A compaction is a handover that also moves the cursor, and says
		// both halves: what later prompts leave out, and that the run goes on.
		{"compact_context", []string{"handover", "later prompts", "does not complete the task"}},
		{"update_brief", []string{"current brief", "original description"}},
		// A proposal becomes a real task in the queue, and a human's
		// approval -- not a label -- is what lets it ever be dispatched.
		{"propose_task", []string{"real task", "unapproved", "approves"}},
	} {
		for _, want := range tt.want {
			if !strings.Contains(text[tt.tool].confirmation, want) {
				t.Errorf("%s's confirmation = %q, want it to say %q",
					tt.tool, text[tt.tool].confirmation, want)
			}
		}
	}
}

// TestRequestSecretRecordsTheNameAndNeverAValue pins the two halves of
// the tool that make it worth having at all: what it records (a name and
// a reason, which is everything grain relays) and what it refuses (a
// name nothing could be stored under, caught in the turn that made the
// call rather than by parking the task on a request no box can be built
// for).
func TestRequestSecretRecordsTheNameAndNeverAValue(t *testing.T) {
	sink := &MockSink{}
	tool := toolNamed(t, NewMockTools(sink, nil), "request_secret")

	res := tool.Handler(context.Background(), map[string]any{
		"secret": "github-app/app-id", "reason": "the deploy needs to authenticate",
	})
	if res.IsError {
		t.Fatalf("well-formed call errored: %s", res.Text)
	}
	got := sink.SecretRequest()
	if got == nil || got.Secret != "github-app/app-id" || got.Reason != "the deploy needs to authenticate" {
		t.Fatalf("SecretRequest() = %+v, want the name and reason as given", got)
	}

	// First call wins, matching what orchestrator.ProcessResult relays
	// off agent.Result.ToolCalls.
	tool.Handler(context.Background(), map[string]any{"secret": "other-key", "reason": "second thoughts"})
	if sink.SecretRequest().Secret != "github-app/app-id" {
		t.Fatalf("SecretRequest() = %+v, want the first call's own name", sink.SecretRequest())
	}

	for _, bad := range []map[string]any{
		{"reason": "no name at all"},
		{"secret": "stripe-api-key"},
		{"secret": "stripe api key", "reason": "a name with a space in it"},
		{"secret": "../etc/passwd", "reason": "a path, not a name"},
		{"secret": "stripe//key", "reason": "an empty key"},
	} {
		if res := tool.Handler(context.Background(), bad); !res.IsError {
			t.Errorf("Handler(%v) = %q, want an error", bad, res.Text)
		}
	}
}

// TestProposeTaskNamesTheReposThisDeploymentWorksIn pins the half of
// that tool's `repo` argument no static description can carry: which
// repositories are actually on offer.
//
// Without them the argument states a rule -- any repository this
// deployment works in may be named -- that a run has no way to apply,
// so it either guesses or files the work against its own repo, and the
// mistake is silent until a human retargets the proposal by hand
// (grain/task-144). The list reaches this process as
// pkg/cli/mcpserver.go's -propose-repo, off the store the daemon holds
// and this one does not.
func TestProposeTaskNamesTheReposThisDeploymentWorksIn(t *testing.T) {
	repos := []string{"acme/widgets", "acme/docs"}
	described := toolNamed(t, NewMockTools(&MockSink{}, repos), "propose_task")
	repoArg := argumentDescription(t, described, "repo")
	for _, want := range repos {
		if !strings.Contains(repoArg, want) {
			t.Errorf("propose_task's repo argument = %q, want it to name %q", repoArg, want)
		}
	}

	// A dispatch that was told nothing -- every test here, and any
	// caller with no store to ask -- reads exactly as it did before the
	// list existed, rather than advertising an empty one.
	bare := argumentDescription(t, toolNamed(t, NewMockTools(&MockSink{}, nil), "propose_task"), "repo")
	if strings.Contains(bare, "Repositories this deployment works in") {
		t.Errorf("propose_task's repo argument = %q, want no list at all when none was given", bare)
	}
	if !strings.HasPrefix(repoArg, bare) {
		t.Errorf("the list replaced part of the argument's description rather than adding to it:\n%q\n%q",
			repoArg, bare)
	}

	// Blanks are the store's spelling of "nothing here", and a
	// description ending in a dangling comma is worse than one with no
	// list at all.
	empty := argumentDescription(t, toolNamed(t, NewMockTools(&MockSink{}, []string{"", " "}), "propose_task"), "repo")
	if empty != bare {
		t.Errorf("propose_task's repo argument = %q, want it unchanged by blank entries", empty)
	}
}

func TestProposeTaskParsesReadsWritesReviewTemplateID(t *testing.T) {
	sink := &MockSink{}
	tool := toolNamed(t, NewMockTools(sink, nil), "propose_task")

	res := tool.Handler(context.Background(), map[string]any{
		"title":              "cross-repo task",
		"body":               "implement feature",
		"branch":             "feat/cross",
		"reads":              []any{"owner/read-repo1", "owner/read-repo2"},
		"writes":             []any{map[string]any{"repo": "owner/write-repo1", "base": "main"}},
		"review_template_id": "tmpl-review-123",
	})
	if res.IsError {
		t.Fatalf("Handler failed: %s", res.Text)
	}
	tasks := sink.ProposedTasks()
	if len(tasks) != 1 {
		t.Fatalf("len(ProposedTasks) = %d, want 1", len(tasks))
	}
	task := tasks[0]
	if task.Title != "cross-repo task" || task.Branch != "feat/cross" {
		t.Errorf("Title/Branch mismatch: %+v", task)
	}
	if !reflect.DeepEqual(task.Reads, []string{"owner/read-repo1", "owner/read-repo2"}) {
		t.Errorf("Reads = %v, want [owner/read-repo1 owner/read-repo2]", task.Reads)
	}
	wantWrites := []WriteTarget{{Repo: "owner/write-repo1", Base: "main"}}
	if !reflect.DeepEqual(task.Writes, wantWrites) {
		t.Errorf("Writes = %v, want %v", task.Writes, wantWrites)
	}
	if task.ReviewTemplateID != "tmpl-review-123" {
		t.Errorf("ReviewTemplateID = %q, want tmpl-review-123", task.ReviewTemplateID)
	}
}

func TestProposeTaskParsesAndValidatesDesigns(t *testing.T) {
	sink := &MockSink{}
	tool := toolNamed(t, NewMockTools(sink, nil), "propose_task")

	bad := tool.Handler(context.Background(), map[string]any{
		"title":   "[PLC] child",
		"body":    "implement slice",
		"designs": []any{"../escape"},
	})
	if !bad.IsError {
		t.Fatalf("expected error on escaping design path, got %+v", bad)
	}
	if len(sink.ProposedTasks()) != 0 {
		t.Fatalf("invalid proposal should not be recorded")
	}

	ok := tool.Handler(context.Background(), map[string]any{
		"title":   "[PLC] child",
		"body":    "implement slice",
		"designs": []any{"design/plc/042@abcdef1", "acme/specs:plc/042@0123456789abcdef0123456789abcdef01234567"},
	})
	if ok.IsError {
		t.Fatalf("unexpected error: %s", ok.Text)
	}
	got := sink.ProposedTasks()
	if len(got) != 1 || len(got[0].Designs) != 2 {
		t.Fatalf("ProposedTasks = %+v, want 1 task with 2 designs", got)
	}
}

// argumentDescription digs one argument's own description out of a
// tool's input schema -- where an agent reads it, and where these tools
// put everything they say about a single argument.
func argumentDescription(t *testing.T, tool Tool, arg string) string {
	t.Helper()
	properties, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s's InputSchema has no properties: %v", tool.Name, tool.InputSchema)
	}
	spec, ok := properties[arg].(map[string]any)
	if !ok {
		t.Fatalf("%s takes no %s argument: %v", tool.Name, arg, properties)
	}
	description, _ := spec["description"].(string)
	return description
}

// toolNamed picks one tool out of a constructor's roster, failing rather
// than returning a zero Tool whose nil Handler would panic further down.
func toolNamed(t *testing.T, tools []Tool, name string) Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("no tool named %q in %d registered", name, len(tools))
	return Tool{}
}

// TestAddReviewCommentSaysWhereTheFeedbackLands is the same regression
// from the other side. This hatch spent as long saying that nothing
// relayed it -- true while no dispatch had a pull request to attach a
// draft review to, and false the moment one did
// (orchestrator.relayReviewFeedback). A run that believes the old
// sentence spends its findings on comment_on_issue instead, which puts
// line-level feedback in a paragraph on the wrong task.
//
// Both destinations are pinned, because a run cannot tell from here which
// one it gets: the review case, which is what the tool is for, and the
// fallback for a run that is not reviewing anything, which is what keeps
// the promise true on every other dispatch.
func TestAddReviewCommentSaysWhereTheFeedbackLands(t *testing.T) {
	text := escapeHatchText(t)["add_review_comment"]
	for _, where := range []struct{ what, s string }{
		{"description", text.description},
		{"confirmation", text.confirmation},
	} {
		lower := strings.ToLower(where.s)
		if strings.Contains(lower, "nothing relays") {
			t.Errorf("add_review_comment's %s = %q, want it to stop saying nothing relays these",
				where.what, where.s)
		}
		if !strings.Contains(lower, "draft review") {
			t.Errorf("add_review_comment's %s = %q, want it to name the draft review these become",
				where.what, where.s)
		}
		if !strings.Contains(lower, "conversation") {
			t.Errorf("add_review_comment's %s = %q, want it to say where a run that is "+
				"reviewing nothing has them relayed instead", where.what, where.s)
		}
	}
}

func TestProposeTaskSchemaIncludesSandboxShapeAndNetworkAccess(t *testing.T) {
	tool := toolNamed(t, NewMockTools(&MockSink{}, nil), "propose_task")
	properties, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties not found in InputSchema: %v", tool.InputSchema)
	}

	for _, prop := range []string{"sandbox_cpus", "sandbox_memory_mb", "sandbox_disk_gb"} {
		spec, ok := properties[prop].(map[string]any)
		if !ok {
			t.Fatalf("missing property %q in propose_task schema", prop)
		}
		if typ, _ := spec["type"].(string); typ != "integer" {
			t.Errorf("property %q type = %q, want integer", prop, typ)
		}
	}

	netSpec, ok := properties["network_access"].(map[string]any)
	if !ok {
		t.Fatalf("missing property network_access in propose_task schema")
	}
	if typ, _ := netSpec["type"].(string); typ != "string" {
		t.Errorf("network_access type = %q, want string", typ)
	}
	enum, ok := netSpec["enum"].([]any)
	if !ok {
		t.Fatalf("network_access enum not found: %v", netSpec["enum"])
	}
	wantEnum := []string{"open", "none"}
	if len(enum) != len(wantEnum) {
		t.Fatalf("network_access enum length = %d, want %d", len(enum), len(wantEnum))
	}
	for i, want := range wantEnum {
		if enum[i] != want {
			t.Errorf("network_access enum[%d] = %v, want %s", i, enum[i], want)
		}
	}
}

func TestProposeTaskHandlerRecordsSandboxShapeAndNetworkAccess(t *testing.T) {
	sink := &MockSink{}
	tool := toolNamed(t, NewMockTools(sink, nil), "propose_task")
	res := tool.Handler(context.Background(), map[string]any{
		"title":             "heavy job",
		"body":              "build image",
		"sandbox_cpus":      8,
		"sandbox_memory_mb": 16384,
		"sandbox_disk_gb":   50,
		"network_access":    "none",
	})
	if res.IsError {
		t.Fatalf("handler returned error: %s", res.Text)
	}
	if len(sink.proposedTasks) != 1 {
		t.Fatalf("got %d proposed tasks, want 1", len(sink.proposedTasks))
	}
	p := sink.proposedTasks[0]
	if p.SandboxCPUs != 8 || p.SandboxMemoryMB != 16384 || p.SandboxDiskGB != 50 {
		t.Errorf("sandbox shape = (%d cpus, %d memory, %d disk), want (8, 16384, 50)",
			p.SandboxCPUs, p.SandboxMemoryMB, p.SandboxDiskGB)
	}
	if p.NetworkAccess != "none" {
		t.Errorf("network_access = %q, want none", p.NetworkAccess)
	}
}

func TestProposeTaskAcceptsAndValidatesPriority(t *testing.T) {
	sink := &MockSink{}
	tool := toolNamed(t, NewMockTools(sink, nil), "propose_task")
	props, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema has no properties map")
	}
	priSpec, ok := props["priority"].(map[string]any)
	if !ok {
		t.Fatal("propose_task InputSchema is missing priority property")
	}
	if typ, _ := priSpec["type"].(string); typ != "integer" {
		t.Errorf("priority type = %q, want integer", typ)
	}

	res := tool.Handler(context.Background(), map[string]any{
		"title":    "urgent follow-up",
		"body":     "fix first",
		"priority": float64(8),
	})
	if res.IsError {
		t.Fatalf("Handler errored: %s", res.Text)
	}
	tasks := sink.ProposedTasks()
	if len(tasks) != 1 || tasks[0].Priority != 8 {
		t.Fatalf("ProposedTasks() = %+v, want priority=8", tasks)
	}

	negRes := tool.Handler(context.Background(), map[string]any{
		"title":    "bad priority",
		"body":     "nope",
		"priority": float64(-1),
	})
	if !negRes.IsError {
		t.Errorf("propose_task with priority=-1 succeeded, want error")
	}
	zeroRes := tool.Handler(context.Background(), map[string]any{
		"title":    "zero priority",
		"body":     "nope",
		"priority": float64(0),
	})
	if !zeroRes.IsError || !strings.Contains(zeroRes.Text, "positive integer") {
		t.Errorf("propose_task with priority=0 = %+v, want a positive-integer error", zeroRes)
	}
	// Not whole, past what a JSON number holds exactly, or not a number
	// at all: refused in the same words, never filed as some other
	// priority (1.5 used to file 1, and "8" the default 5).
	for _, bad := range []any{1.5, float64(1e20), float64(1<<53 + 2), "8", true} {
		res := tool.Handler(context.Background(), map[string]any{
			"title":    "odd priority",
			"body":     "nope",
			"priority": bad,
		})
		if !res.IsError || !strings.Contains(res.Text, "positive integer") {
			t.Errorf("propose_task with priority=%v = %+v, want a positive-integer error", bad, res)
		}
	}
	if got := len(sink.ProposedTasks()); got != 1 {
		t.Errorf("ProposedTasks() after refused calls = %d, want 1", got)
	}
	// A whole number written with an exponent is that number, as it is
	// on New task, Edit, Bulk edit, the template form and the REST API:
	// decoded from the arguments' own JSON, as the MCP server does.
	var exponent map[string]any
	if err := json.Unmarshal([]byte(`{"title": "exponent priority", "body": "fine", "priority": 1e3}`), &exponent); err != nil {
		t.Fatal(err)
	}
	if res := tool.Handler(context.Background(), exponent); res.IsError {
		t.Fatalf("propose_task with priority=1e3 errored: %s", res.Text)
	}
	if tasks := sink.ProposedTasks(); len(tasks) != 2 || tasks[1].Priority != 1000 {
		t.Errorf("ProposedTasks() = %+v, want a second task at priority 1000", tasks)
	}
}

func TestProposeTaskAcceptsCapabilities(t *testing.T) {
	sink := &MockSink{}
	tool := toolNamed(t, NewMockTools(sink, nil), "propose_task")

	props, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema has no properties map")
	}
	if _, ok := props["capabilities"]; !ok {
		t.Error("propose_task InputSchema is missing capabilities property")
	}

	res := tool.Handler(context.Background(), map[string]any{
		"title":        "subtask",
		"body":         "run ci check",
		"capabilities": []any{"grain-ci", "gcp-key"},
	})
	if res.IsError {
		t.Fatalf("Handler errored: %s", res.Text)
	}
	tasks := sink.ProposedTasks()
	if len(tasks) != 1 {
		t.Fatalf("len(ProposedTasks()) = %d, want 1", len(tasks))
	}
	if len(tasks[0].Capabilities) != 2 || tasks[0].Capabilities[0] != "grain-ci" || tasks[0].Capabilities[1] != "gcp-key" {
		t.Errorf("Capabilities = %v, want [grain-ci gcp-key]", tasks[0].Capabilities)
	}
}

// TestNothingNeededIsRegisteredWithARequiredReason pins the tool's
// registration: it is among the escape hatches every run is offered, its
// one parameter is a required reason, its description carries the
// sentence the agent prompt also uses, and a call records that reason
// while a call without one is refused in the same turn.
func TestNothingNeededIsRegisteredWithARequiredReason(t *testing.T) {
	sink := &MockSink{}
	tool := toolNamed(t, NewMockTools(sink, nil), "nothing_needed")

	if !strings.Contains(tool.Description, NothingNeededDescription) {
		t.Errorf("description = %q, want it to say %q", tool.Description, NothingNeededDescription)
	}
	if got := tool.InputSchema["required"]; !reflect.DeepEqual(got, []string{"reason"}) {
		t.Errorf("required = %v, want [reason]", got)
	}

	if res := tool.Handler(context.Background(), map[string]any{"reason": "  "}); !res.IsError {
		t.Errorf("a blank reason was accepted: %q", res.Text)
	}
	if got := sink.NothingNeeded(); got != "" {
		t.Errorf("a refused call recorded %q", got)
	}
	if res := tool.Handler(context.Background(), map[string]any{"reason": "already fixed in abc123"}); res.IsError {
		t.Fatalf("a well-formed call was refused: %s", res.Text)
	}
	if got := sink.NothingNeeded(); got != "already fixed in abc123" {
		t.Errorf("NothingNeeded() = %q, want the reason", got)
	}
}

func TestProposeTaskSchemaIsCompactedAndPointsToHelp(t *testing.T) {
	tool := toolNamed(t, NewMockTools(&MockSink{}, []string{"acme/widgets"}), "propose_task")
	raw, err := json.Marshal(struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"inputSchema"`
	}{
		Name:        tool.Name,
		Description: tool.Description,
		InputSchema: tool.InputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 3000 {
		t.Errorf("propose_task serialized schema is %d bytes, want <= 3000 bytes (compacted from ~14.7 KB)", len(raw))
	}
	if !strings.Contains(tool.Description, "grain help propose") {
		t.Errorf("propose_task description = %q, want reference to 'grain help propose'", tool.Description)
	}
}

// TestProposeTaskRefusesANetworkAccessGrainCannotBuild: every sandbox
// backend withholds the network only for model.NetworkNone exactly, so a
// word it does not know -- "restricted", which this schema used to offer
// -- would be filed and dispatched with an open network. The call is
// refused instead, and nothing is recorded.
func TestProposeTaskRefusesANetworkAccessGrainCannotBuild(t *testing.T) {
	sink := &MockSink{}
	tool := toolNamed(t, NewMockTools(sink, nil), "propose_task")
	res := tool.Handler(context.Background(), map[string]any{
		"title": "follow-up", "body": "the rest of it", "network_access": "restricted",
	})
	if !res.IsError || !strings.Contains(res.Text, `"none"`) {
		t.Errorf("result = %+v, want an error naming the accesses grain can build", res)
	}
	if got := sink.ProposedTasks(); len(got) != 0 {
		t.Errorf("recorded %v, want nothing filed", got)
	}

	res = tool.Handler(context.Background(), map[string]any{
		"title": "follow-up", "body": "the rest of it", "network_access": " none ",
	})
	if res.IsError {
		t.Fatalf("a valid network_access was refused: %s", res.Text)
	}
	if got := sink.ProposedTasks(); len(got) != 1 || got[0].NetworkAccess != "none" {
		t.Errorf("recorded %+v, want one proposal with network access none", got)
	}
}

// A review grant (model.ReviewOfArg) is grain's to give: propose_task
// refuses one in the same turn and records no proposal.
func TestProposeTaskRefusesAReviewGrant(t *testing.T) {
	sink := &MockSink{}
	tool := toolNamed(t, NewMockTools(sink, nil), "propose_task")
	res := tool.Handler(context.Background(), map[string]any{
		"title": "review it myself", "body": "pretend to be the review",
		"capabilities": []any{map[string]any{
			"capability": "https://caps.example/workspace#workspace",
			"with":       map[string]any{"readonly": true, "review_of": "51", "snapshot": 3},
		}},
	})
	if !res.IsError || !strings.Contains(res.Text, "review_of") {
		t.Fatalf("propose_task = %+v, want the review grant refused", res)
	}
	if got := sink.ProposedTasks(); len(got) != 0 {
		t.Errorf("ProposedTasks() = %+v, want none", got)
	}
}

func TestProposeTaskAcceptsUnifiedGrantsAndRefusesMixedSpellings(t *testing.T) {
	sink := &MockSink{}
	tool := toolNamed(t, NewMockTools(sink, nil), "propose_task")

	res := tool.Handler(context.Background(), map[string]any{
		"title": "child with unified grants",
		"body":  "exercise slice 2 grants on propose_task",
		"grants": []any{
			map[string]any{"capability": "https://grain.dev/capabilities/api#tasks:read", "with": map[string]any{"repo": "acme/app"}},
			map[string]any{"capability": "https://grain.dev/capabilities/repo#target", "with": map[string]any{"repo": "acme/app", "base": "release"}},
			map[string]any{"capability": "https://grain.dev/capabilities/repo#read", "with": map[string]any{"repo": "acme/docs"}},
			map[string]any{"capability": "https://grain.dev/capabilities/repo#write", "with": map[string]any{"repo": "acme/site", "base": "main"}},
		},
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Text)
	}
	got := sink.ProposedTasks()
	if len(got) != 1 {
		t.Fatalf("ProposedTasks() = %+v, want 1", got)
	}
	p := got[0]
	if p.Repo != "acme/app" || p.Branch != "release" {
		t.Errorf("Repo, Branch = %q, %q, want acme/app, release", p.Repo, p.Branch)
	}
	if want := model.APIGrantString("tasks:read", "acme/app"); len(p.Capabilities) != 1 || p.Capabilities[0] != want {
		t.Errorf("Capabilities = %v, want [%s]", p.Capabilities, want)
	}
	if len(p.Reads) != 1 || p.Reads[0] != "acme/docs" {
		t.Errorf("Reads = %v, want [acme/docs]", p.Reads)
	}
	if len(p.Writes) != 1 || p.Writes[0] != (WriteTarget{Repo: "acme/site", Base: "main"}) {
		t.Errorf("Writes = %v, want [{acme/site main}]", p.Writes)
	}

	for _, bad := range []map[string]any{
		{
			"title": "mix caps", "body": "b",
			"capabilities": []any{"api:ci:read"},
			"grants":       []any{map[string]any{"capability": "https://grain.dev/capabilities/api#tasks:read"}},
		},
		{
			"title": "mix empty grants", "body": "b",
			"capabilities": []any{"api:ci:read"},
			"grants":       []any{},
		},
		{
			"title": "mix repo", "body": "b", "repo": "acme/app",
			"grants": []any{map[string]any{"capability": "https://grain.dev/capabilities/repo#target", "with": map[string]any{"repo": "acme/app", "base": "main"}}},
		},
		{
			"title": "mix branch", "body": "b", "branch": "develop",
			"grants": []any{map[string]any{"capability": "https://grain.dev/capabilities/repo#target", "with": map[string]any{"repo": "acme/app", "base": "main"}}},
		},
		{
			"title": "mix reads", "body": "b", "reads": []any{},
			"grants": []any{map[string]any{"capability": "https://grain.dev/capabilities/repo#read", "with": map[string]any{"repo": "acme/docs"}}},
		},
		{
			"title": "mix writes", "body": "b", "writes": []any{},
			"grants": []any{map[string]any{"capability": "https://grain.dev/capabilities/repo#write", "with": map[string]any{"repo": "acme/site", "base": "main"}}},
		},
	} {
		r := tool.Handler(context.Background(), bad)
		if !r.IsError {
			t.Errorf("expected error for %v, got %+v", bad["title"], r)
		}
	}
}

func TestProposeTaskTargetRefusalNamesSentField(t *testing.T) {
	t.Cleanup(model.RegisterChangeTarget(model.ChangeTargetRegistration{Kind: testChangeTargetKind}))

	sink := &MockSink{}
	tool := toolNamed(t, NewMockTools(sink, nil), "propose_task")
	target := map[string]any{
		"capability": "https://grain.dev/capabilities/repo#target",
		"with":       map[string]any{"repo": "acme/app", "base": "main"},
	}

	for _, tc := range []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"repo", map[string]any{"repo": "acme/app"}, "the request's repo field both name the task's repository"},
		{"branch", map[string]any{"branch": "develop"}, "the request's branch field both name the task's repository"},
		{"no_repo", map[string]any{"no_repo": true}, "the request's no_repo field both name the task's repository"},
		{"change target", map[string]any{testChangeTargetKind: map[string]any{}}, "the request's " + testChangeTargetKind + " field both name the task's repository"},
		{"repo and branch", map[string]any{"repo": "acme/app", "branch": "develop"}, "the request's repo and branch fields both name the task's repository"},
	} {
		args := map[string]any{
			"title":  "mix " + tc.name,
			"body":   "b",
			"grants": []any{target},
		}
		for k, v := range tc.extra {
			args[k] = v
		}
		res := tool.Handler(context.Background(), args)
		if !res.IsError || !strings.Contains(res.Text, tc.want) {
			t.Errorf("%s: got %+v, want error containing %q", tc.name, res, tc.want)
		}
	}
	if got := sink.ProposedTasks(); len(got) != 0 {
		t.Errorf("ProposedTasks() = %+v, want none recorded", got)
	}
}

func TestProposeTaskRefusesUndeclaredArgumentsAndSuggestsNearest(t *testing.T) {
	sink := &MockSink{}
	mockTool := toolNamed(t, NewMockTools(sink, nil), "propose_task")
	filerCalled := false
	liveTool := NewLiveProposalTools(&MockSink{}, nil, "3750", filerFunc(func(context.Context, ProposedTask, string) (FiledProposal, error) {
		filerCalled = true
		return FiledProposal{ID: "99"}, nil
	}))[0]

	target := map[string]any{
		"capability": "https://grain.dev/capabilities/repo#target",
		"with":       map[string]any{"repo": "acme/widgets", "base": "main"},
	}

	for _, tc := range []struct {
		name      string
		extra     map[string]any
		wantSub   []string
		forbidSub string
	}{
		{
			name:    "base alone",
			extra:   map[string]any{"base": "release/v3.2.0"},
			wantSub: []string{`"base"`, `"branch"`},
		},
		{
			name:    "base alongside repo#target (walkthrough O2)",
			extra:   map[string]any{"base": "main", "grants": []any{target}},
			wantSub: []string{`"base"`, `"branch"`},
		},
		{
			name:    "description synonym",
			extra:   map[string]any{"description": "extra prose"},
			wantSub: []string{`"description"`, `"body"`},
		},
		{
			name:    "fields synonym",
			extra:   map[string]any{"fields": map[string]any{"k": "v"}},
			wantSub: []string{`"fields"`, `"template_fields"`},
		},
		{
			name:    "camelCase noRepo",
			extra:   map[string]any{"noRepo": true},
			wantSub: []string{`"noRepo"`, `"no_repo"`},
		},
		{
			name:    "capabilities typo",
			extra:   map[string]any{"capabilites": []any{"api:ci-read"}},
			wantSub: []string{`"capabilites"`, `"capabilities"`},
		},
		{
			name:    "singular read",
			extra:   map[string]any{"read": []any{"acme/docs"}},
			wantSub: []string{`"read"`, `"reads"`},
		},
		{
			name:    "writes entry with branch instead of base",
			extra:   map[string]any{"writes": []any{map[string]any{"repo": "acme/site", "branch": "main"}}},
			wantSub: []string{"writes[0]", `"branch"`, `"base"`},
		},
		{
			name:      "unrelated unknown argument gets no bogus suggestion",
			extra:     map[string]any{"xyzzy_nonsense": 1},
			wantSub:   []string{`"xyzzy_nonsense"`},
			forbidSub: "did you mean",
		},
	} {
		args := map[string]any{"title": "follow-up", "body": "details"}
		for k, v := range tc.extra {
			args[k] = v
		}
		for label, tool := range map[string]Tool{"mock": mockTool, "live": liveTool} {
			res := tool.Handler(context.Background(), args)
			if !res.IsError {
				t.Errorf("%s (%s): succeeded with %q, want error", tc.name, label, res.Text)
				continue
			}
			for _, sub := range tc.wantSub {
				if !strings.Contains(res.Text, sub) {
					t.Errorf("%s (%s): error = %q, want substring %q", tc.name, label, res.Text, sub)
				}
			}
			if tc.forbidSub != "" && strings.Contains(res.Text, tc.forbidSub) {
				t.Errorf("%s (%s): error = %q, did not want %q", tc.name, label, res.Text, tc.forbidSub)
			}
		}
	}

	if got := sink.ProposedTasks(); len(got) != 0 {
		t.Errorf("ProposedTasks() = %+v, want none recorded", got)
	}
	if filerCalled {
		t.Error("live ProposalFiler was called on a refused propose_task request")
	}

	// Every property advertised in InputSchema (including registered change-target kinds) is accepted.
	t.Cleanup(model.RegisterChangeTarget(model.ChangeTargetRegistration{Kind: testChangeTargetKind}))
	toolWithCT := toolNamed(t, NewMockTools(&MockSink{}, nil), "propose_task")
	props, ok := toolWithCT.InputSchema["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		t.Fatalf("InputSchema properties = %T %v", toolWithCT.InputSchema["properties"], toolWithCT.InputSchema["properties"])
	}
	for name := range props {
		if err := ValidateProposeTaskArgs(map[string]any{name: nil}); err != nil {
			t.Errorf("ValidateProposeTaskArgs rejected declared schema property %q: %v", name, err)
		}
	}
}
