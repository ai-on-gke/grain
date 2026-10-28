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
	"reflect"
	"strings"
	"testing"
)

// escapeHatchText returns each escape hatch's description and the
// confirmation its handler answers a well-formed call with -- the two
// pieces of text a dispatched run actually reads about these tools.
func escapeHatchText(t *testing.T) map[string]struct{ description, confirmation string } {
	t.Helper()
	args := map[string]map[string]any{
		"ask_question":       {"question": "which config file?"},
		"request_secret":     {"secret": "stripe-api-key", "reason": "the deploy script authenticates with it"},
		"comment_on_issue":   {"comment": "the answer is 4"},
		"nothing_needed":     {"reason": "the flag is already off on main"},
		"write_handover":     {"handover": "the merge lane may only ever hold one branch"},
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
