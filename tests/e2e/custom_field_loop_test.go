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

// One turn of a custom field's whole loop, with every leg the real one:
// a set-valued field declared by a merged pull request against the state
// repository, applied to a running deployment by staterepo.Apply, read
// by a dispatched run out of the prompt orchestrator.BuildPrompt handed
// it, and written back by that run's own propose_task through the real
// mocked tool. The declaration returns through the state repository's
// next export; task values do not, because tasks are operational state.
//
// Every one of those legs has a test of its own already -- the
// declaration in pkg/staterepo (custom_field_withdrawn_test.go,
// addedcolumns_test.go), the prompt section in pkg/orchestrator
// (custom_fields_internal_test.go), the CLI in cmd/grain, the
// propose_task half in pkg/orchestrator's finish_test.go -- and none of
// them can see the disagreement this file is here to catch: two writers
// on either side of a seam, agreeing with their own tests and not with
// each other about the order of a set, the separator between its values,
// or what an empty one is spelled as. A unit test on each side of that
// seam passes either way.
//
// The assertions are on the proposal rows the real tool wrote, while the
// declaration is asserted in the exported settings file a human reviews.
// Tasks and their custom-field values are operational state and therefore
// deliberately absent from that export.
//
// The scripted agent here reads its own prompt (antigravity.Script gets
// it, which is what the randomized cluster test's generator uses) rather
// than being handed the values the test filed. That is the point: the
// values that go back through propose_task are the ones that came out of
// the prompt text, parsed the way an agent has to parse them, so a
// change to how a set is written into a prompt shows up here as a
// changed proposal rather than as nothing at all.
//
// Which is also why the run here reads its prompt twice over: once as a
// run that has read the sentence under the list, and once as a run that
// has only read the list and copies a set's line back as the line it was
// (verbatimProposalTitle). What that costs differs by declaration, one
// half of it is silent, and it is written down here as the accepted
// price of a one-line rendering rather than left to be rediscovered.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// declaredFieldRows is the pull request somebody merges against
// tables/custom_field.json: two rows, written out by hand in the shape
// staterepo.Export writes them, because that is what a reviewer of that
// repository actually reads and edits.
//
// components is the set-valued one this file is about -- an enum with
// `multiple` on, so it has both a fixed vocabulary (which makes "a value
// the declaration does not allow" a thing that exists) and a set to
// disagree about the order of. customer is an ordinary single-valued
// text field beside it, so that nothing here passes by treating every
// field as a set.
//
// options is a JSON array inside a string because that is how the column
// holds it (model.PutCustomField, schema.go), and `multiple` is 1 rather
// than true because that is what SQLite stores and what the next export
// writes back: a declaration written by hand and one written by grain
// have to be the same file, or every sync would rewrite it.
//
// tags is the third: a set again, but of free text rather than of enum
// choices, which is what makes it the other half of the round trip. A
// line copied back verbatim onto components is refused, because the
// joined line is not one of the options; the same line copied back onto
// tags is a perfectly good single text value. See verbatimProposalTitle.
var declaredFieldRows = []map[string]any{
	{
		"name":          "components",
		"label":         "Components",
		"type":          "enum",
		"options":       `["prompt","staterepo","store","ui"]`,
		"description":   "which parts of grain this work touches",
		"default_value": "",
		"position":      1,
		"multiple":      1,
		"show_on_row":   0,
	},
	{
		"name":          "customer",
		"label":         "Customer",
		"type":          "text",
		"options":       "[]",
		"description":   "who asked for it",
		"default_value": "",
		"position":      2,
		"multiple":      0,
		"show_on_row":   0,
	},
	{
		"name":          "tags",
		"label":         "Tags",
		"type":          "text",
		"options":       "[]",
		"description":   "whatever this deployment sorts its backlog by",
		"default_value": "",
		"position":      3,
		"multiple":      1,
		"show_on_row":   0,
	},
}

// The two proposals the run makes, named here because the export is
// matched back to them by title -- a proposal's id is allocated at filing
// time (Store.NewTaskID) and the agent never sees it.
const (
	// pieceProposalTitle is the ordinary one: a piece of the same job,
	// carrying the same fields forward, which is what the prompt asks a
	// run to do with them.
	pieceProposalTitle = "follow-up: pin the export's own ordering"
	// refusedProposalTitle is the one that names things this deployment
	// will not store: a value outside the enum's choices, and a field
	// nothing declares at all.
	refusedProposalTitle = "follow-up: whatever legal decides about the wording"
	// verbatimProposalTitle is the run that did the one thing the prompt
	// asks it not to: copied a set's line back as the line it was, rather
	// than as the list of values it stands for. The prompt writes a set as
	// one comma-joined bullet (orchestrator.customFieldsSection) and says,
	// underneath, to give such a field back as a list of strings -- and
	// this is what happens to a run that reads the list and not the
	// sentence under it.
	//
	// What happens differs by declaration, and this proposal states both
	// halves at once because it carries both fields:
	//
	//   - components is an enum, so "prompt, store, ui" is not one of its
	//     options, NormalizeValues fails, and FilterCustomFieldValues
	//     drops the field whole -- noisily, with droppedCustomFieldNote
	//     naming it in the body a human approves the proposal from.
	//   - tags is free text, so "alpha, beta, gamma" is a perfectly good
	//     single value of it. It is stored as one row whose value carries
	//     the commas, and nobody is told anything: every reader renders it
	//     as one tag and no filter matches any of the three.
	//
	// That second half is a real loss and it is pinned here as the
	// accepted cost of writing a set on one line, not as an oversight. The
	// alternative -- splitting a Multiple field's single comma-carrying
	// value back into a set, or refusing it -- would have to hold for
	// every writer that reaches model.NormalizeValues, and grain has
	// already decided the other way for them: `-field customers=Acme%2C
	// Inc.,Globex` splits on the declaration and unescapes, so a set's own
	// value may legitimately carry a comma (cmd/grain's
	// TestASetValueCanCarryAComma). A model that refused "Acme, Inc." from
	// the API could not accept it from the CLI, and one that split it
	// would turn a chip somebody typed into two. The comma-joined line is
	// a lossy rendering with an escape hatch stated beside it, which is
	// the trade this test exists to hold still: change how a set is
	// written into a prompt and this file is where it fails.
	verbatimProposalTitle = "follow-up: the line, copied back as the line"
)

// stateRepo is the deployment's own state repository: a real bare remote
// and a real working tree, opened and seeded from this world's database
// exactly as a daemon opens it at startup (staterepo.Load).
type stateRepo struct {
	repo   *staterepo.Repo
	remote string
}

func openStateRepo(w *world) *stateRepo {
	w.t.Helper()
	remote := initBareRepo(w.t, filepath.Join(w.t.TempDir(), "state.git"), staterepo.DefaultBranch)
	repo, err := staterepo.Open(w.ctx, staterepo.Config{
		Dir:    filepath.Join(w.t.TempDir(), "state"),
		Remote: remote,
	})
	if err != nil {
		w.t.Fatalf("opening the state repository: %v", err)
	}
	if err := staterepo.Load(w.ctx, repo, w.db, model.SchemaVersion); err != nil {
		w.t.Fatalf("seeding the state repository from this deployment: %v", err)
	}
	return &stateRepo{repo: repo, remote: remote}
}

// merge is the pull request against the settings, merged: clone the
// remote, rewrite tables/<table>.json with rows, commit, push, and let
// the running deployment apply it (staterepo.Apply, which is what the
// daemon calls on every tick). It is the same shape pkg/staterepo's own
// custom_field_withdrawn_test.go uses, restated here rather than shared,
// the way this package restates every small helper it borrows.
func (s *stateRepo) merge(w *world, message, table string, rows []map[string]any) {
	w.t.Helper()
	work := filepath.Join(w.t.TempDir(), "merge")
	run(w.t, w.t.TempDir(), "git", "clone", "--quiet", s.remote, work)
	out, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		w.t.Fatal(err)
	}
	path := filepath.Join(work, staterepo.TablesDir, table+".json")
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		w.t.Fatalf("editing %s: %v", path, err)
	}
	run(w.t, work, "git", "-c", "user.email=review@example.com", "-c", "user.name=reviewer",
		"commit", "--quiet", "-am", message)
	run(w.t, work, "git", "push", "--quiet", "origin", staterepo.DefaultBranch)

	applied, err := staterepo.Apply(w.ctx, s.repo, w.db, model.SchemaVersion)
	if err != nil {
		w.t.Fatalf("applying the merged change: %v", err)
	}
	if !applied {
		w.t.Fatal("a merged commit arrived and this deployment applied nothing")
	}
}

// export is the deployment writing itself out again and a human -- or a
// restore -- reading the result: staterepo.Sync, the daemon's own export
// path, and then a fresh clone of the remote, so what the assertions read
// is what actually reached the repository rather than what was left in
// the working tree.
func (s *stateRepo) export(w *world) string {
	w.t.Helper()
	committed, err := staterepo.Sync(w.ctx, s.repo, w.db, model.SchemaVersion)
	if err != nil {
		w.t.Fatalf("exporting: %v", err)
	}
	if !committed {
		w.t.Fatal("a run filed two tasks and the export had nothing to commit")
	}
	clone := filepath.Join(w.t.TempDir(), "exported")
	run(w.t, w.t.TempDir(), "git", "clone", "--quiet", s.remote, clone)
	return clone
}

// tableRows reads one exported table back as the rows it holds, in the
// order the file lists them -- which is part of what is under test here,
// since Export sorts by primary key and a set's values are its own key
// (schema.go's task_custom_field).
func tableRows(t *testing.T, dir, table string) []map[string]any {
	t.Helper()
	path := filepath.Join(dir, staterepo.TablesDir, table+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the exported %s: %v", table, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var rows []map[string]any
	if err := dec.Decode(&rows); err != nil {
		t.Fatalf("parsing the exported %s: %v\n%s", table, err, data)
	}
	return rows
}

// promptCustomFields is the agent reading its own prompt: the fields
// section orchestrator.customFieldsSection wrote, parsed back into the
// values it was rendered from.
//
// It is anchored on the sentence that section opens with rather than on
// "any bullet list in the prompt", because a prompt has several -- the
// previous attempts, the conversation -- and because a run really does
// have to find this one by reading. A wording change that made the list
// unfindable would be a wording change that made it unusable, and this
// test failing is the right answer to it.
func promptCustomFields(prompt string) map[string][]string {
	out := map[string][]string{}
	for name, line := range promptCustomFieldLines(prompt) {
		// A set is read as the one comma-separated line it is written on,
		// and split back the same way -- the shape the prompt's own
		// closing paragraph tells a run to give back as a list.
		for _, v := range strings.Split(line, ",") {
			out[name] = append(out[name], strings.TrimSpace(v))
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// promptCustomFieldLines is the same section read one step earlier: each
// field's line as the text it is, unsplit. It is what a run that copies
// a line back verbatim copies (verbatimProposalTitle), and splitting it
// is the one thing promptCustomFields does on top.
func promptCustomFieldLines(prompt string) map[string]string {
	const heading = "records fields of its own on a task"
	i := strings.Index(prompt, heading)
	if i < 0 {
		return nil
	}
	out := map[string]string{}
	for _, line := range strings.Split(prompt[i:], "\n") {
		if !strings.HasPrefix(line, "- ") {
			// The list ends at the first line that is not one of its
			// bullets, which is the blank line before the paragraph
			// about propose_task.
			if len(out) > 0 {
				break
			}
			continue
		}
		name, values, ok := strings.Cut(strings.TrimPrefix(line, "- "), ": ")
		if !ok {
			continue
		}
		out[name] = values
	}
	return out
}

// proposingScript is the run: it reads the fields out of its prompt,
// proposes two follow-ups carrying values back, and finishes with a
// comment -- an ending that pushes nothing, so the only thing this test
// is measuring is the fields.
//
// The prompt is kept so the test can assert on the text the run was
// actually given, and the whole thing is under a mutex because a script
// is called from the goroutine running the dispatch rather than from the
// test's own.
type proposingScript struct {
	mu     sync.Mutex
	prompt string
	fields map[string][]string
	lines  map[string]string
	steps  []antigravity.Step
	next   int
}

func (s *proposingScript) Next(prompt string) (antigravity.Step, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prompt == "" {
		s.prompt = prompt
		s.fields = promptCustomFields(prompt)
		s.lines = promptCustomFieldLines(prompt)
		s.steps = proposalSteps(s.fields, s.lines)
	}
	if s.next >= len(s.steps) {
		return antigravity.Step{}, false
	}
	step := s.steps[s.next]
	s.next++
	return step, true
}

// read is the fields this run found in its prompt, and promptText the
// text it found them in -- both for the test to assert on once the run
// is over, since a script cannot fail a test from the goroutine it runs
// on.
func (s *proposingScript) read() map[string][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.fields)
}

func (s *proposingScript) promptText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prompt
}

// proposalSteps is what the run does with what it read. The first
// proposal carries the same set back, reversed, which is the ordering
// disagreement this whole file exists to look for: a set has no order
// (model.CustomField.Multiple), so writing it back backwards must
// produce the same export. The second asks for two things this
// deployment will not store, which is the other half -- what happens to a
// value `grain state check` would warn about when it arrives through
// propose_task instead of through a settings file. The third states its
// two sets the way the prompt wrote them rather than the way the prompt
// asked for them back (verbatimProposalTitle).
//
// lines is the same section unsplit, which is what that third proposal
// needs: a run copying a line back has not parsed it, so the test must
// not have parsed it for it either.
func proposalSteps(read map[string][]string, lines map[string]string) []antigravity.Step {
	backwards := slices.Clone(read["components"])
	slices.Reverse(backwards)
	return []antigravity.Step{
		toolCall("propose_task", map[string]any{
			"title": pieceProposalTitle,
			"body":  "the export's row order for a set is not pinned anywhere",
			"custom_fields": map[string]any{
				"components": toAnySlice(backwards),
				"customer":   strings.Join(read["customer"], ", "),
				"tags":       toAnySlice(read["tags"]),
			},
		}),
		toolCall("propose_task", map[string]any{
			"title": refusedProposalTitle,
			"body":  "somebody has to decide what the wording says before this can be done",
			"custom_fields": map[string]any{
				// "legal" is not one of the enum's choices, and a field
				// declared `multiple` is refused a set with a bad value
				// in it whole rather than value by value
				// (model.CustomField.NormalizeValues).
				"components": []any{"ui", "legal"},
				// A name this deployment declares nothing under at all.
				"team": "platform",
			},
		}),
		toolCall("propose_task", map[string]any{
			"title": verbatimProposalTitle,
			"body":  "whoever picks this up should read the sentence under the list",
			"custom_fields": map[string]any{
				// Both sets as the single strings the prompt wrote them
				// as -- no list, no splitting, the bullet's right-hand
				// side and nothing else. One of the two is refused for
				// it and one is stored; which is which is the whole
				// point (verbatimProposalTitle).
				"components": lines["components"],
				"tags":       lines["tags"],
			},
		}),
		toolCall("comment_on_issue", map[string]any{
			"comment": "read the fields, proposed the follow-ups they belong to",
		}),
		finalText("proposed two follow-ups"),
	}
}

// toAnySlice is the set as a tool call decoded from JSON carries it --
// []any of strings, not []string -- which is the shape both the mocked
// tool and orchestrator.argCustomFieldValue downstream of it actually
// see. A script that handed over a []string would be handing over
// something no real call can be.
func toAnySlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, v := range values {
		out = append(out, v)
	}
	return out
}

func TestASetValuedCustomFieldGoesFromTheStateRepoThroughARun(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	const owner, repoName = "acme", "widgets"
	w.newRepo(owner, repoName)
	repo := model.RepoRef{Owner: owner, Name: repoName}
	sim := githubsim.New(owner, repoName, filepath.Join(w.upstreamDir, owner, repoName+".git"), "main")
	client := github.NewClient(sim, nil)

	// 1. A deployment that is already running, with a task filed on it
	//    and its state repository seeded from the database as it stands
	//    -- which is the only state a merged settings change is ever
	//    applied to (staterepo.Apply refuses to touch a database holding
	//    nothing, since that is a host that has lost its store).
	const taskID = "t-fields"
	fileIssue(w, taskID, human("alice"), repo)
	state := openStateRepo(w)

	// 2. The declaration, decided in the state repository and nowhere
	//    else: a pull request against tables/custom_field.json, merged,
	//    and applied to the deployment on its next tick.
	state.merge(w, "Declare components and customer", "custom_field", declaredFieldRows)

	declared, err := w.store.ListCustomFields(w.ctx)
	if err != nil {
		t.Fatalf("listing declarations: %v", err)
	}
	if len(declared) != 3 || declared[0].Name != "components" ||
		declared[1].Name != "customer" || declared[2].Name != "tags" {
		t.Fatalf("declarations = %+v, want components, customer then tags, in declared position order", declared)
	}
	if !declared[0].Multiple {
		t.Fatalf("components = %+v, want the merged `multiple` to have survived the import", declared[0])
	}

	// 3. Somebody fills the new fields in on that task, through the same
	//    normalization every writer goes through -- and states the set in
	//    an order nobody would call sorted, so that what the prompt shows
	//    is what the store decided rather than what the caller typed.
	values, err := model.NormalizeCustomFieldValues(declared, map[string][]string{
		"components": {"ui", "prompt", "store"},
		"customer":   {"acme"},
		"tags":       {"gamma", "alpha", "beta"},
	})
	if err != nil {
		t.Fatalf("normalizing the values a human filed: %v", err)
	}
	if err := w.store.UpdateTask(w.ctx, taskID, func(tk *model.Task) error {
		tk.CustomFields = values
		return nil
	}); err != nil {
		t.Fatalf("putting the values on %s: %v", taskID, err)
	}

	// 4. The run: dispatched by a real cycle, handed the real prompt, and
	//    reading the fields back out of it.
	script := &proposingScript{}
	deps := orchestrator.Deps{
		Store: w.store, Client: client, Sandboxes: worldSandboxes{w}, MaxWorkers: 1,
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			return antigravity.NewForTest(script), nil
		},
	}
	if err := orchestrator.RunCycle(w.ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	assertState(w, taskID, model.StateAwaitingReply, false)

	// The prompt said it the way the section is meant to read: one line
	// per field, the set comma-joined on it, sorted by name.
	prompt := script.promptText()
	if !strings.Contains(prompt, "\n- components: prompt, store, ui\n") {
		t.Errorf("prompt did not carry the set as one comma-joined line:\n%s", prompt)
	}
	if !strings.Contains(prompt, "\n- customer: acme\n") {
		t.Errorf("prompt did not carry the single-valued field:\n%s", prompt)
	}
	if !strings.Contains(prompt, "\n- tags: alpha, beta, gamma\n") {
		t.Errorf("prompt did not carry the free-text set as one comma-joined line:\n%s", prompt)
	}
	if got := script.read(); !maps.EqualFunc(got, values, slices.Equal) {
		t.Fatalf("the run read %v out of its prompt, want the %v the store holds", got, values)
	}

	// 5. What the run proposed, filed by relayProposedTasks.
	proposals := proposedBy(w, taskID)
	piece, ok := proposals[pieceProposalTitle]
	if !ok {
		t.Fatalf("proposals = %v, want one titled %q", slices.Sorted(maps.Keys(proposals)), pieceProposalTitle)
	}
	refused, ok := proposals[refusedProposalTitle]
	if !ok {
		t.Fatalf("proposals = %v, want one titled %q", slices.Sorted(maps.Keys(proposals)), refusedProposalTitle)
	}
	verbatim, ok := proposals[verbatimProposalTitle]
	if !ok {
		t.Fatalf("proposals = %v, want one titled %q", slices.Sorted(maps.Keys(proposals)), verbatimProposalTitle)
	}

	// 6. The values the run wrote. Tasks are operational state and no
	//    longer belong in the state repository export; the proposal rows
	//    themselves are the seam between the prompt and tool writers.
	if !maps.EqualFunc(piece.CustomFields, values, slices.Equal) {
		t.Errorf("the proposal carries %v and the task it came from carries %v; a piece of the same job should match",
			piece.CustomFields, values)
	}
	if len(refused.CustomFields) != 0 {
		t.Errorf("the refused proposal carries %v, want no custom field value", refused.CustomFields)
	}
	for _, name := range []string{"components", "team"} {
		if !strings.Contains(refused.Body, "`"+name+"`") {
			t.Errorf("the refused proposal's body does not name %s:\n%s", name, refused.Body)
		}
	}
	if got := verbatim.CustomFields["components"]; len(got) != 0 {
		t.Errorf("the verbatim proposal carries components = %v, want no value", got)
	}
	if got := verbatim.CustomFields["tags"]; !slices.Equal(got, []string{"alpha, beta, gamma"}) {
		t.Errorf("the verbatim proposal carries tags = %v, want the whole line as one value", got)
	}

	// The declaration itself is settings, so it remains in the export as
	// the merge wrote it even though the tasks carrying values do not.
	dir := state.export(w)
	if _, err := os.Stat(filepath.Join(dir, staterepo.TablesDir, "task_custom_field.json")); !os.IsNotExist(err) {
		t.Fatalf("operational task_custom_field export: got err %v, want the file omitted", err)
	}
	if got := tableRows(t, dir, "custom_field"); !sameRows(got, declaredFieldRows) {
		t.Errorf("custom_field.json came back as %v, want the merged rows unchanged: %v", got, declaredFieldRows)
	}
}

// sameRows compares exported rows against the ones a merge wrote,
// column by column, as the text each is written as -- json.Number on one
// side and an int on the other are the same row in a file, and comparing
// the values directly would say they are not.
func sameRows(got []map[string]any, want []map[string]any) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if len(got[i]) != len(want[i]) {
			return false
		}
		for k, v := range want[i] {
			if fmt.Sprint(got[i][k]) != fmt.Sprint(v) {
				return false
			}
		}
	}
	return true
}
