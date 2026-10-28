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

package orchestrator

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

// treeClient is the GitHub a work tree talks to: the comments on the
// origin, the bodies of the tree's pull requests, and the merges.
type treeClient struct {
	github.Client
	created  []string
	updated  []string
	bodies   map[int]string
	merged   []string
	nextID   int
	labelled []string
}

func (c *treeClient) CreateComment(owner, repo string, number int, body string) (int, error) {
	c.nextID++
	c.created = append(c.created, body)
	return c.nextID, nil
}

func (c *treeClient) UpdateComment(owner, repo string, id int64, body string) error {
	c.updated = append(c.updated, body)
	return nil
}

func (c *treeClient) GetPullRequest(owner, repo string, number int) (github.PullRequestDetail, error) {
	return github.PullRequestDetail{State: "open", Title: "Do the thing", Body: c.bodies[number]}, nil
}

func (c *treeClient) UpdatePullRequestBody(owner, repo string, number int, body string) error {
	c.bodies[number] = body
	return nil
}

func (c *treeClient) MergePullRequest(owner, repo string, number int, sha string) error {
	c.merged = append(c.merged, "")
	return nil
}

func (c *treeClient) MergePullRequestWithMessage(owner, repo string, number int, sha, title, message string) error {
	c.merged = append(c.merged, message)
	return nil
}

func (c *treeClient) AddLabel(owner, repo string, number int, label string) error {
	c.labelled = append(c.labelled, label)
	return nil
}

func openTreeStore(t *testing.T) (*model.Store, context.Context) {
	t.Helper()
	store, _, ctx := openTreeStoreDB(t)
	return store, ctx
}

// openTreeStoreDB is openTreeStore with the database under it, for a
// test that writes rows the way an older build did.
func openTreeStoreDB(t *testing.T) (*model.Store, *sql.DB, context.Context) {
	t.Helper()
	LogToTest(t)
	store, db := sqlitetest.Open(t)
	return store, db, context.Background()
}

var widgets = model.RepoRef{Owner: "acme", Name: "widgets"}

func fileTreeTask(t *testing.T, ctx context.Context, store *model.Store, id, parent, title string, origin model.WorkOrigin, links ...model.Link) {
	t.Helper()
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: title,
		Origin:     model.Origin{Attribution: model.Attribution{Actor: model.TriggerPrincipal}, Reason: model.ReasonDirect},
		Binding:    model.BindingDirective,
		Target:     &widgets,
		WorkOrigin: origin,
		Links:      links,
	}
	if parent != "" {
		task.Links = append(task.Links, model.Link{Kind: model.LinkProposedBy, Target: parent})
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
}

// A design PR's tree: the planner (10) filed an implementation (11) and a
// verify task (13), and the implementation filed a follow-up (12).
func plantTree(t *testing.T) (*model.Store, context.Context, model.WorkOrigin) {
	store, ctx := openTreeStore(t)
	return store, ctx, plantTreeIn(t, ctx, store)
}

func plantTreeIn(t *testing.T, ctx context.Context, store *model.Store) model.WorkOrigin {
	origin := model.GitHubOrigin(widgets, 5)
	fileTreeTask(t, ctx, store, "10", "", "Plan the design doc", origin)
	fileTreeTask(t, ctx, store, "11", "10", "Implement D-1", "",
		model.Link{Kind: model.LinkFixes, Target: "acme/widgets#20"})
	fileTreeTask(t, ctx, store, "12", "11", "Follow up on D-1", "")
	fileTreeTask(t, ctx, store, "13", "10", "Verify the doc", "")
	return origin
}

func TestTheOriginCommentIsPostedOnceAndEditedInPlace(t *testing.T) {
	store, ctx, origin := plantTree(t)
	client := &treeClient{bodies: map[int]string{}}
	t0 := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

	if err := syncWorkTree(ctx, store, client, origin, t0); err != nil {
		t.Fatal(err)
	}
	if len(client.created) != 1 || len(client.updated) != 0 {
		t.Fatalf("created %d, updated %d; want one comment posted", len(client.created), len(client.updated))
	}
	first := client.created[0]
	for _, want := range []string{
		"0 of 4 done",
		"- [ ] task 10: Plan the design doc (planned)\n" +
			"  - [ ] task 11: Implement D-1 (PR open [acme/widgets#20](https://github.com/acme/widgets/pull/20))\n" +
			"    - [ ] task 12: Follow up on D-1 (planned)\n" +
			"  - [ ] task 13: Verify the doc (planned)\n",
		"git log --grep 'Origin: acme/widgets#5'",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("comment lacks %q:\n%s", want, first)
		}
	}

	// Within the rate limit nothing is even looked at.
	if err := store.ObserveField(ctx, "13", t0, func(o *model.Observation) { o.ClosedAt = &t0 }); err != nil {
		t.Fatal(err)
	}
	if err := syncWorkTree(ctx, store, client, origin, t0.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(client.created)+len(client.updated) != 1 {
		t.Fatalf("a sync inside the rate limit wrote to GitHub")
	}

	// After it, the one comment is edited: never a second one.
	t1 := t0.Add(3 * time.Minute)
	if err := syncWorkTree(ctx, store, client, origin, t1); err != nil {
		t.Fatal(err)
	}
	if len(client.created) != 1 || len(client.updated) != 1 {
		t.Fatalf("created %d, updated %d; want the comment edited once", len(client.created), len(client.updated))
	}
	if got := client.updated[0]; !strings.Contains(got, "1 of 4 done") || !strings.Contains(got, "- [x] task 13: Verify the doc (closed)") {
		t.Errorf("edited comment:\n%s", got)
	}

	// Nothing moved: looked at, not written.
	if err := syncWorkTree(ctx, store, client, origin, t1.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(client.created) != 1 || len(client.updated) != 1 {
		t.Errorf("an unchanged tree was written again")
	}
}

func TestThePullRequestNamesItsParentOriginAndChildren(t *testing.T) {
	store, ctx, origin := plantTree(t)
	client := &treeClient{bodies: map[int]string{}}
	task, _ := store.GetTask(ctx, "11")

	section, err := lineageSection(ctx, store, *task)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Filed by: task 10, Plan the design doc",
		"Origin: [acme/widgets#5](https://github.com/acme/widgets/issues/5)",
		"Follow-up tasks filed:\n- task 12: Follow up on D-1 (planned)",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("section lacks %q:\n%s", want, section)
		}
	}
	body := withLineage(pullRequestBody("What the change does.", "11", 0), section)
	if !grainAuthored(body, "11") || !strings.HasPrefix(body, "What the change does.") {
		t.Fatalf("the lineage section displaced the footer:\n%s", body)
	}
	if again := withLineage(body, section); again != body {
		t.Errorf("rewriting the same section changed the body:\n%s\n---\n%s", body, again)
	}
	client.bodies[20] = body

	// The child changes state: the parent's pull request says so, once the
	// tree is next looked at.
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	if err := store.ObserveField(ctx, "12", now, func(o *model.Observation) { o.ClosedAt = &now }); err != nil {
		t.Fatal(err)
	}
	if err := syncWorkTree(ctx, store, client, origin, now); err != nil {
		t.Fatal(err)
	}
	if got := client.bodies[20]; !strings.Contains(got, "- task 12: Follow up on D-1 (closed)") || strings.Count(got, lineageStart) != 1 {
		t.Errorf("parent PR after the child closed:\n%s", got)
	}

	// The top of the tree names no parent, and a task-rooted tree its task.
	root, _ := store.GetTask(ctx, "10")
	if s, _ := lineageSection(ctx, store, *root); strings.Contains(s, "Filed by:") {
		t.Errorf("the root names a parent:\n%s", s)
	}
}

func TestMergeCommitsCarryTheLineageTrailers(t *testing.T) {
	store, ctx, _ := plantTree(t)
	client := &treeClient{bodies: map[int]string{}}
	task, _ := store.GetTask(ctx, "12")
	if got := lineageTrailers(*task); strings.Join(got, "\n") != "Origin: acme/widgets#5\nFiled-by: task 11" {
		t.Errorf("trailers = %q", got)
	}
	if err := mergeTaskPullRequest(ctx, store, client, widgets, 21, "abc", "12", ""); err != nil {
		t.Fatal(err)
	}
	if len(client.merged) != 1 || client.merged[0] != "Do the thing\n\nOrigin: acme/widgets#5\nFiled-by: task 11" {
		t.Errorf("merge message = %q", client.merged)
	}

	labelOrigin(client, *task, 21)
	if len(client.labelled) != 1 || client.labelled[0] != "origin/#5" {
		t.Errorf("labels = %q", client.labelled)
	}

	// A task with no origin and no parent merges as it always did.
	fileTreeTask(t, ctx, store, "30", "", "Alone", "")
	if err := mergeTaskPullRequest(ctx, store, client, widgets, 22, "abc", "30", ""); err != nil {
		t.Fatal(err)
	}
	if client.merged[1] != "" {
		t.Errorf("a task with no lineage got a merge message %q", client.merged[1])
	}
}

// A task with no parent, no origin and no follow-ups gets no section.
func TestALoneTaskHasNoLineageSection(t *testing.T) {
	store, ctx := openTreeStore(t)
	fileTreeTask(t, ctx, store, "40", "", "Alone", "")
	task, _ := store.GetTask(ctx, "40")
	if s, err := lineageSection(ctx, store, *task); err != nil || s != "" {
		t.Errorf("section = %q, %v", s, err)
	}
	body := pullRequestBody("Change.", "40", 0)
	if withLineage(body, "") != body {
		t.Error("an empty section changed the body")
	}
}

// A task from before origins existed, filed part way down a tree, is
// stored with no origin; its pull request's lineage names the root of
// the tree it was filed in, not "this task".
func TestTheLineageOfAnOriginlessFiledTaskNamesItsTreesRoot(t *testing.T) {
	store, ctx := openTreeStore(t)
	fileTreeTask(t, ctx, store, "20", "", "Plan", "")
	// Filed, then linked: stored with no origin, as one filed before
	// origins existed is.
	fileTreeTask(t, ctx, store, "21", "", "Implement", "")
	fileTreeTask(t, ctx, store, "21", "20", "Implement", "")
	task, err := store.GetTask(ctx, "21")
	if err != nil || task.WorkOrigin != "" || task.Parent() != "20" {
		t.Fatalf("task 21 = origin %q parent %q (%v), want none and 20", task.WorkOrigin, task.Parent(), err)
	}
	section, err := lineageSection(ctx, store, *task)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(section, "Origin: task 20\n") || strings.Contains(section, "Origin: this task") {
		t.Errorf("lineage does not name task 20 as the origin:\n%s", section)
	}
}

// A changelist task in a GitHub origin's tree lists its CLs, as cl/N with
// their status, the way a pull request is listed.
func TestTheOriginCommentListsAGoogle3TasksCLs(t *testing.T) {
	store, ctx := openTreeStore(t)
	origin := model.GitHubOrigin(widgets, 5)
	fileTreeTask(t, ctx, store, "10", "", "Plan the design doc", origin)
	task := model.Task{
		ID: "14", Intent: model.IntentImplement, Title: "Port it to the depot",
		Origin:       model.Origin{Attribution: model.Attribution{Actor: model.TriggerPrincipal}, Reason: model.ReasonDirect},
		Binding:      model.BindingDirective,
		ChangeTarget: &model.ChangeTarget{Kind: "changelist"},
		Links: []model.Link{{Kind: model.LinkProposedBy, Target: "10"},
			{Kind: model.LinkChangelist, Target: "cl/612345678"}, {Kind: model.LinkChangelist, Target: "cl/612345690"}},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	client := &treeClient{bodies: map[int]string{}}
	if err := syncWorkTree(ctx, store, client, origin, time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if len(client.created) != 1 {
		t.Fatalf("created %d comments", len(client.created))
	}
	if want := "  - [ ] task 14: Port it to the depot (cl-pending cl/612345678, cl/612345690)\n"; !strings.Contains(client.created[0], want) {
		t.Errorf("comment lacks %q:\n%s", want, client.created[0])
	}
}
