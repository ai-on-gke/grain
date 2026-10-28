package model_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

// A tree rooted at a design PR: the planner a merge trigger filed names
// the PR, and every task below it -- three propose_task levels deep --
// inherits it without anybody writing it down again.
func TestWorkOriginIsInheritedAtAnyDepth(t *testing.T) {
	store, ctx := open(t)
	pr := model.GitHubOrigin(model.RepoRef{Owner: "bwsalmon", Name: "grain"}, 2710)

	planner := model.Task{
		ID: "10", Intent: model.IntentImplement, Title: "plan",
		Origin:     model.Origin{Attribution: model.Attribution{Actor: bot}, Reason: model.ReasonDirect},
		Binding:    model.BindingDirective,
		WorkOrigin: pr,
	}
	if err := store.PutTask(ctx, planner); err != nil {
		t.Fatal(err)
	}
	proposal(t, store, ctx, "11", "10", nil)
	proposal(t, store, ctx, "12", "11", nil)
	proposal(t, store, ctx, "13", "12", nil)

	for _, id := range []string{"10", "11", "12", "13"} {
		got, err := store.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.WorkOrigin != pr {
			t.Errorf("task %s origin = %q, want %q", id, got.WorkOrigin, pr)
		}
	}
	if got, _ := store.GetTask(ctx, "13"); got.Parent() != "12" {
		t.Errorf("task 13 parent = %q, want 12", got.Parent())
	}

	tree, err := store.TasksWithOrigin(ctx, pr)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 4 || tree[0].ID != "10" || tree[3].ID != "13" {
		t.Errorf("TasksWithOrigin = %v", ids(tree))
	}
	children, err := store.ChildTasks(ctx, "11")
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].ID != "12" {
		t.Errorf("ChildTasks(11) = %v", ids(children))
	}
}

// A chat files work: the chat itself has no origin, and is the origin of
// everything below it. An update that rebuilds a task from scratch keeps
// the origin it was filed with.
func TestAChatIsTheOriginOfWhatItFiles(t *testing.T) {
	store, ctx := open(t)
	proposal(t, store, ctx, "20", "", nil)
	proposal(t, store, ctx, "21", "20", nil)
	proposal(t, store, ctx, "22", "21", nil)

	if chat, _ := store.GetTask(ctx, "20"); chat.WorkOrigin != "" {
		t.Errorf("chat origin = %q, want none", chat.WorkOrigin)
	}
	for _, id := range []string{"21", "22"} {
		if got, _ := store.GetTask(ctx, id); got.WorkOrigin != model.TaskOrigin("20") {
			t.Errorf("task %s origin = %q, want task:20", id, got.WorkOrigin)
		}
	}
	// Rewritten without its origin (and without its parent link), it
	// keeps both what it was filed with.
	proposal(t, store, ctx, "22", "", nil)
	if got, _ := store.GetTask(ctx, "22"); got.WorkOrigin != model.TaskOrigin("20") {
		t.Errorf("rewritten task origin = %q", got.WorkOrigin)
	}
}

func TestParseWorkOrigin(t *testing.T) {
	grain := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	for in, want := range map[string]model.WorkOrigin{
		"bwsalmon/grain#12": "bwsalmon/grain#12",
		"#12":               "bwsalmon/grain#12",
		"https://github.com/gke-labs/bouncer/pull/7": "gke-labs/bouncer#7",
		"https://github.com/bwsalmon/grain/issues/9": "bwsalmon/grain#9",
		"task 1766": "task:1766",
		"task:1766": "task:1766",
		"":          "",
	} {
		got, err := model.ParseWorkOrigin(in, &grain)
		if err != nil || got != want {
			t.Errorf("ParseWorkOrigin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"main", "task x", "https://github.com/a/b/tree/main"} {
		if _, err := model.ParseWorkOrigin(bad, &grain); err == nil {
			t.Errorf("ParseWorkOrigin(%q) accepted", bad)
		}
	}
	pr := model.WorkOrigin("bwsalmon/grain#12")
	if pr.Trailer() != "bwsalmon/grain#12" || model.TaskOrigin("5").Trailer() != "task 5" {
		t.Errorf("trailers = %q %q", pr.Trailer(), model.TaskOrigin("5").Trailer())
	}
	if got := pr.Label(grain); got != "origin/#12" {
		t.Errorf("label = %q", got)
	}
	if got := pr.Label(model.RepoRef{Owner: "gke-labs", Name: "bouncer"}); got != "origin/bwsalmon/grain#12" {
		t.Errorf("cross-repo label = %q", got)
	}
	if model.TaskOrigin("5").Label(grain) != "" {
		t.Error("a task origin has a label")
	}
}

func ids(tasks []model.Task) []string {
	out := make([]string, len(tasks))
	for i, t := range tasks {
		out[i] = t.ID
	}
	return out
}

// A chat that names an issue roots its work there; a task already in a
// GitHub tree cannot leave it by naming another.
func TestExplicitOriginOnlyWhereTheParentHasNoGitHubOrigin(t *testing.T) {
	grain := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	chat := &model.Task{ID: "1766"}
	if got, err := model.ExplicitOrigin("#42", &grain, chat); err != nil || got != "bwsalmon/grain#42" {
		t.Errorf("from a chat: %q, %v", got, err)
	}
	inTree := &model.Task{ID: "9", WorkOrigin: "bwsalmon/grain#7"}
	if got, _ := model.ExplicitOrigin("#42", &grain, inTree); got != "" {
		t.Errorf("a task in a GitHub tree moved to %q", got)
	}
	if got, _ := model.ExplicitOrigin("", &grain, chat); got != "" {
		t.Errorf("naming nothing gave %q", got)
	}
	if _, err := model.ExplicitOrigin("not an origin", &grain, chat); err == nil {
		t.Error("a malformed origin was accepted")
	}
}

// A proposal that names an origin its tree overrides is told so, rather
// than re-homed in silence.
func TestOriginKeptNoteSaysWhyANamedOriginWasNotHonoured(t *testing.T) {
	grain := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	inTree := &model.Task{ID: "9", WorkOrigin: "bwsalmon/grain#7"}
	note := model.OriginKeptNote("#42", &grain, inTree)
	for _, want := range []string{"bwsalmon/grain#7", "bwsalmon/grain#42", "task 9"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q lacks %q", note, want)
		}
	}
	for name, got := range map[string]string{
		"same origin": model.OriginKeptNote("#7", &grain, inTree),
		"none named":  model.OriginKeptNote("", &grain, inTree),
		"malformed":   model.OriginKeptNote("not an origin", &grain, inTree),
		"chat parent": model.OriginKeptNote("#42", &grain, &model.Task{ID: "1766"}),
		"no parent":   model.OriginKeptNote("#42", &grain, nil),
		"task-rooted": model.OriginKeptNote("#42", &grain, &model.Task{ID: "3", WorkOrigin: "task:1"}),
	} {
		if got != "" {
			t.Errorf("%s: note = %q, want none", name, got)
		}
	}
}

// A tree that existed before origins did, with no issue at its root,
// has no origin stored anywhere in it. Work filed from the middle of it
// joins the tree at its root rather than starting one at its parent.
func TestAProposalFromATreeWithNoStoredOriginTakesTheRoot(t *testing.T) {
	store, db := sqlitetest.Open(t)
	ctx := context.Background()
	proposal(t, store, ctx, "30", "", nil)
	proposal(t, store, ctx, "31", "30", nil)
	// Stored the way a row from before origins was: a parent link and
	// no origin.
	if _, err := db.ExecContext(ctx, "UPDATE `task` SET `origin_ref` = '' WHERE `id` = '31'"); err != nil {
		t.Fatal(err)
	}
	proposal(t, store, ctx, "32", "31", nil)
	proposal(t, store, ctx, "33", "32", nil)
	for _, id := range []string{"32", "33"} {
		if got, _ := store.GetTask(ctx, id); got.WorkOrigin != model.TaskOrigin("30") {
			t.Errorf("task %s origin = %q, want task:30", id, got.WorkOrigin)
		}
	}
}

// A run that names its own task (or any task part way down a tree) as
// a proposal's origin files the proposal in that task's tree, at the
// root, not in a new tree rooted at the task it named.
func TestANamedTaskOriginResolvesToItsTreesRoot(t *testing.T) {
	store, ctx := open(t)
	proposal(t, store, ctx, "40", "", nil)
	proposal(t, store, ctx, "41", "40", nil)
	named := model.Task{
		ID: "42", Intent: model.IntentImplement, Title: "42",
		Origin:     model.Origin{Attribution: model.Attribution{Actor: bot}, Reason: model.ReasonDirect},
		Binding:    model.BindingDirective,
		Links:      []model.Link{{Kind: model.LinkProposedBy, Target: "41"}},
		WorkOrigin: model.TaskOrigin("41"),
	}
	if err := store.PutTask(ctx, named); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetTask(ctx, "42"); got.WorkOrigin != model.TaskOrigin("40") {
		t.Errorf("origin = %q, want task:40", got.WorkOrigin)
	}

	pr := model.GitHubOrigin(model.RepoRef{Owner: "bwsalmon", Name: "grain"}, 7)
	planner := model.Task{
		ID: "51", Intent: model.IntentImplement, Title: "51",
		Origin:     model.Origin{Attribution: model.Attribution{Actor: bot}, Reason: model.ReasonDirect},
		Binding:    model.BindingDirective,
		WorkOrigin: pr,
	}
	if err := store.PutTask(ctx, planner); err != nil {
		t.Fatal(err)
	}
	// A person filing work for task 51's tree names task 51.
	filed := planner
	filed.ID, filed.WorkOrigin = "52", model.TaskOrigin("51")
	if err := store.PutTask(ctx, filed); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetTask(ctx, "52"); got.WorkOrigin != pr {
		t.Errorf("origin = %q, want %q", got.WorkOrigin, pr)
	}
}
