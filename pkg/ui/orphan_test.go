package ui_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// recordingComments is ui.Config.PullRequestComments standing in for the
// daemon's own adapter over a GitHub client -- every call, in order, so a
// test can tell "said once" from "said twice" as well as "said at all".
type recordingComments struct {
	calls []struct{ Ref, Body string }
	err   error
}

func (r *recordingComments) Comment(ctx context.Context, ref, body string) error {
	r.calls = append(r.calls, struct{ Ref, Body string }{ref, body})
	return r.err
}

// recordingCloser is ui.Config.PullRequestCloser the same way: every ref
// it was asked to close, in order, so a test can tell "closed nothing"
// from "closed something" -- which is the assertion most of this file
// cares about, since closing is the one thing here that destroys work.
type recordingCloser struct {
	closed []string
	err    error
}

func (r *recordingCloser) Close(ctx context.Context, ref string) error {
	r.closed = append(r.closed, ref)
	return r.err
}

// linkedTask is a task that has been through a run: a pull request open
// for it, linked the way orchestrator.linkPullRequest links one, and
// completed -- which is exactly the state a human closing a task is
// usually looking at, and the one that orphans a pull request.
func linkedTask(t *testing.T, c *ui.Client, store *model.Store, ctx context.Context) (ui.Task, string) {
	t.Helper()
	task := create(t, c, ctx)
	ref := model.PullRequestRef{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Number: 7}
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: ref.String()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	completedAt := baseTime
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &completedAt}); err != nil {
		t.Fatal(err)
	}
	return task, ref.String()
}

// grainComments is every comment grain left in its own voice (automation,
// not on behalf of an agent) -- the note this file is about, and nothing
// a human or a run ever said.
func grainComments(t *testing.T, store *model.Store, ctx context.Context, taskID string) []string {
	t.Helper()
	comments, err := store.Comments(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range comments {
		if c.Author.Actor.Kind == model.PrincipalAutomation && c.Author.OnBehalfOf == nil {
			out = append(out, c.Body)
		}
	}
	return out
}

// Closing a task with a pull request still open orphans that pull
// request: grain will not merge it (only a completed task's link reaches
// Store.OpenPullRequestLinks) and nothing in grain closes it. The
// decision is deliberate; being told is the part that was missing, and it
// is told in both places at once -- on the task, for whoever just closed
// it, and on the pull request, for whoever finds it open weeks later with
// no reason to look at a closed task.
func TestClosingATaskWithAnOpenPullRequestSaysSoOnBothSides(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	posted := &recordingComments{}
	c.Config.PullRequestComments = posted
	task, ref := linkedTask(t, c, store, ctx)

	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	notes := grainComments(t, store, ctx, task.ID)
	if len(notes) != 1 {
		t.Fatalf("grain's own comments = %+v, want exactly the one note", notes)
	}
	if !strings.Contains(notes[0], ref) || !strings.Contains(notes[0], task.ID) {
		t.Fatalf("note = %q, want it to name both %s and task %s", notes[0], ref, task.ID)
	}
	if !strings.Contains(notes[0], "left this note on the pull request itself too") {
		t.Fatalf("note = %q, want it to say the pull request was told as well", notes[0])
	}

	if len(posted.calls) != 1 {
		t.Fatalf("comments posted on GitHub = %+v, want exactly one", posted.calls)
	}
	if posted.calls[0].Ref != ref {
		t.Fatalf("posted on %q, want %q", posted.calls[0].Ref, ref)
	}
	// The pull request's copy is the same explanation minus the line
	// about having left it there -- which would be nonsense read on the
	// pull request itself.
	if !strings.Contains(posted.calls[0].Body, "grain has stopped watching "+ref) {
		t.Fatalf("posted body = %q, want the same note", posted.calls[0].Body)
	}
	if strings.Contains(posted.calls[0].Body, "left this note on the pull request itself too") {
		t.Fatalf("posted body = %q, want it without the line about posting it", posted.calls[0].Body)
	}
}

// The close is what matters and it has already happened by the time any
// of this runs, so a GitHub call that fails costs nothing but itself --
// no failed close, and no silence either: what went wrong is written into
// the note left on the task, which is the one place this package can be
// sure a reader will find it (pkg/ui has no logger).
func TestAFailedGitHubCommentIsReportedInTheNoteRatherThanFailingTheClose(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	c.Config.PullRequestComments = &recordingComments{err: errors.New("403 from GitHub")}
	task, _ := linkedTask(t, c, store, ctx)

	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close: %v -- a pull request comment that failed must not fail the close", err)
	}
	if st, err := store.State(ctx, task.ID); err != nil || st != model.StateClosed {
		t.Fatalf("state = %q (%v), want closed", st, err)
	}
	notes := grainComments(t, store, ctx, task.ID)
	if len(notes) != 1 || !strings.Contains(notes[0], "403 from GitHub") {
		t.Fatalf("note = %+v, want it to quote what went wrong on GitHub", notes)
	}
}

// A deployment whose UI was never handed a GitHub client is the same
// case, said in the same place: the note lands, and says the pull request
// itself was not told.
func TestAnUnwiredDeploymentStillNotesTheOrphanOnTheTask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task, ref := linkedTask(t, c, store, ctx)

	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	notes := grainComments(t, store, ctx, task.ID)
	if len(notes) != 1 || !strings.Contains(notes[0], ref) {
		t.Fatalf("note = %+v, want one naming %s", notes, ref)
	}
	if !strings.Contains(notes[0], "could not leave this note on the pull request itself") {
		t.Fatalf("note = %q, want it to say the pull request was not told", notes[0])
	}
}

// Closing, reopening and closing again says it once. The conversation is
// not the only reason: the copy on GitHub has nothing to dedupe it, so
// the check that stops a second note is also what stops a second comment
// piling up on the pull request every time somebody changes their mind.
func TestClosingATaskTwiceLeavesOneNoteAndOneComment(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	posted := &recordingComments{}
	c.Config.PullRequestComments = posted
	task, _ := linkedTask(t, c, store, ctx)

	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.Reopen(ctx, task.ID); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close (again): %v", err)
	}

	if notes := grainComments(t, store, ctx, task.ID); len(notes) != 1 {
		t.Fatalf("grain's own comments = %+v, want exactly one after two closes", notes)
	}
	if len(posted.calls) != 1 {
		t.Fatalf("comments posted on GitHub = %+v, want exactly one after two closes", posted.calls)
	}
}

// Nothing is said about a pull request that already merged. That task is
// closed too -- orchestrator.SyncPullRequests closes one whose pull
// request landed -- and a human clicking Close in the moment before the
// UI catches up would otherwise be told grain had abandoned work it had
// just merged.
func TestClosingATaskWhosePullRequestAlreadyMergedSaysNothing(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	posted := &recordingComments{}
	c.Config.PullRequestComments = posted
	task, _ := linkedTask(t, c, store, ctx)
	mergedAt := baseTime
	if err := store.ObserveField(ctx, task.ID, baseTime, func(o *model.Observation) {
		o.PrMergedAt = &mergedAt
	}); err != nil {
		t.Fatal(err)
	}

	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if notes := grainComments(t, store, ctx, task.ID); len(notes) != 0 {
		t.Fatalf("grain's own comments = %+v, want none about a pull request that merged", notes)
	}
	if len(posted.calls) != 0 {
		t.Fatalf("comments posted on GitHub = %+v, want none", posted.calls)
	}
}

// The other ending: whoever closed the task asked for its pull request to
// be closed with it, so grain closes it -- and says so, in the words for
// that rather than the words for an orphan, in both places.
func TestClosingATaskWithTheOptionClosesThePullRequestAndSaysSo(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	posted := &recordingComments{}
	closer := &recordingCloser{}
	c.Config.PullRequestComments = posted
	c.Config.PullRequestCloser = closer
	task, ref := linkedTask(t, c, store, ctx)

	if err := c.Close(ctx, task.ID, ui.CloseOptions{ClosePullRequest: true}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if len(closer.closed) != 1 || closer.closed[0] != ref {
		t.Fatalf("closed on GitHub = %+v, want exactly %s", closer.closed, ref)
	}
	notes := grainComments(t, store, ctx, task.ID)
	if len(notes) != 1 || !strings.Contains(notes[0], "grain has closed "+ref) {
		t.Fatalf("note = %+v, want one saying grain closed %s", notes, ref)
	}
	if len(posted.calls) != 1 || !strings.Contains(posted.calls[0].Body, "grain has closed "+ref) {
		t.Fatalf("posted on GitHub = %+v, want the same words on the pull request", posted.calls)
	}
}

// The default, and the whole safety of the thing: a close that did not
// ask for it does not reach the closer at all. Every other caller of
// Close in the tree passes the zero CloseOptions, so this is the property
// that says an ordinary close still cannot destroy work.
func TestAnOrdinaryCloseNeverClosesThePullRequest(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	closer := &recordingCloser{}
	c.Config.PullRequestComments = &recordingComments{}
	c.Config.PullRequestCloser = closer
	task, _ := linkedTask(t, c, store, ctx)

	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if len(closer.closed) != 0 {
		t.Fatalf("closed on GitHub = %+v, want nothing closed without being asked", closer.closed)
	}
	notes := grainComments(t, store, ctx, task.ID)
	if len(notes) != 1 || !strings.Contains(notes[0], "grain has stopped watching") {
		t.Fatalf("note = %+v, want the orphan note", notes)
	}
}

// Reopening a task does not reach the closer either -- it takes the same
// path with no options at all, and there is nothing about reopening a
// task that should touch a pull request on GitHub.
func TestReopeningATaskNeverClosesAPullRequest(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	closer := &recordingCloser{}
	c.Config.PullRequestCloser = closer
	task, _ := linkedTask(t, c, store, ctx)

	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.Reopen(ctx, task.ID); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if len(closer.closed) != 0 {
		t.Fatalf("closed on GitHub = %+v, want nothing", closer.closed)
	}
}

// A GitHub that refuses the close costs the close of the *task* nothing,
// exactly as a failed comment does -- and what is left behind is an
// ordinary orphaned pull request, said in the orphan's own words plus the
// refusal, because that is precisely what it is.
func TestARefusedPullRequestCloseStillClosesTheTaskAndSaysWhy(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	posted := &recordingComments{}
	c.Config.PullRequestComments = posted
	c.Config.PullRequestCloser = &recordingCloser{err: errors.New("403 from GitHub")}
	task, ref := linkedTask(t, c, store, ctx)

	if err := c.Close(ctx, task.ID, ui.CloseOptions{ClosePullRequest: true}); err != nil {
		t.Fatalf("Close: %v -- a refused pull request close must not fail the close", err)
	}
	if st, err := store.State(ctx, task.ID); err != nil || st != model.StateClosed {
		t.Fatalf("state = %q (%v), want closed", st, err)
	}
	notes := grainComments(t, store, ctx, task.ID)
	if len(notes) != 1 || !strings.Contains(notes[0], "grain has stopped watching "+ref) {
		t.Fatalf("note = %+v, want the orphan note -- the pull request is still open", notes)
	}
	if !strings.Contains(notes[0], "GitHub refused: 403 from GitHub") {
		t.Fatalf("note = %q, want it to say the close was asked for and refused", notes[0])
	}
	if len(posted.calls) != 1 || !strings.Contains(posted.calls[0].Body, "GitHub refused") {
		t.Fatalf("posted on GitHub = %+v, want the pull request told the same thing", posted.calls)
	}
}

// A deployment whose UI holds a commenter but no closer is the same case
// said the same way: the ask could not be carried out, the task is
// closed, and the note says so rather than the close failing. Config
// keeps the two capabilities apart precisely so this is expressible.
func TestAskingToCloseWithNoCloserWiredIsReportedInTheNote(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	c.Config.PullRequestComments = &recordingComments{}
	task, ref := linkedTask(t, c, store, ctx)

	if err := c.Close(ctx, task.ID, ui.CloseOptions{ClosePullRequest: true}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	notes := grainComments(t, store, ctx, task.ID)
	if len(notes) != 1 || !strings.Contains(notes[0], "grain has stopped watching "+ref) {
		t.Fatalf("note = %+v, want the orphan note", notes)
	}
	if !strings.Contains(notes[0], "not wired to a GitHub client") {
		t.Fatalf("note = %q, want it to say why the pull request was not closed", notes[0])
	}
}

// Nothing is closed on a pull request that already merged, whatever the
// box said. The task's own close is the same early return that keeps
// grain from claiming it walked away from work it merged -- and a close
// call on a merged pull request is the one GitHub would rightly refuse.
func TestAskingToCloseAMergedPullRequestClosesNothing(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	closer := &recordingCloser{}
	c.Config.PullRequestCloser = closer
	c.Config.PullRequestComments = &recordingComments{}
	task, _ := linkedTask(t, c, store, ctx)
	mergedAt := baseTime
	if err := store.ObserveField(ctx, task.ID, baseTime, func(o *model.Observation) {
		o.PrMergedAt = &mergedAt
	}); err != nil {
		t.Fatal(err)
	}

	if err := c.Close(ctx, task.ID, ui.CloseOptions{ClosePullRequest: true}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(closer.closed) != 0 {
		t.Fatalf("closed on GitHub = %+v, want nothing -- that pull request merged", closer.closed)
	}
	if notes := grainComments(t, store, ctx, task.ID); len(notes) != 0 {
		t.Fatalf("grain's own comments = %+v, want none", notes)
	}
}

// Somebody who closed a task, reopened it and closed it again -- this
// time asking for the pull request to go too -- is asking for something
// the first close did not do. The note already on the task is no reason
// to ignore them, and the second close has its own thing to say.
func TestAskingToCloseAfterAnOrdinaryCloseStillClosesThePullRequest(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	posted := &recordingComments{}
	closer := &recordingCloser{}
	c.Config.PullRequestComments = posted
	c.Config.PullRequestCloser = closer
	task, ref := linkedTask(t, c, store, ctx)

	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.Reopen(ctx, task.ID); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if err := c.Close(ctx, task.ID, ui.CloseOptions{ClosePullRequest: true}); err != nil {
		t.Fatalf("Close (again): %v", err)
	}

	if len(closer.closed) != 1 || closer.closed[0] != ref {
		t.Fatalf("closed on GitHub = %+v, want %s closed by the second close", closer.closed, ref)
	}
	notes := grainComments(t, store, ctx, task.ID)
	if len(notes) != 2 {
		t.Fatalf("grain's own comments = %+v, want the orphan note and the close note", notes)
	}
	if !strings.Contains(notes[1], "grain has closed "+ref) {
		t.Fatalf("second note = %q, want it to say the pull request was closed", notes[1])
	}
	if len(posted.calls) != 2 {
		t.Fatalf("posted on GitHub = %+v, want the pull request told both times", posted.calls)
	}
}

// Nothing is said about a task with no pull request to orphan -- the
// ordinary close of a task that never ran, or of one whose run needed no
// code change. Neither is a close anybody has to be warned about.
func TestClosingATaskWithNoPullRequestSaysNothing(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	posted := &recordingComments{}
	c.Config.PullRequestComments = posted
	task := create(t, c, ctx)

	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if notes := grainComments(t, store, ctx, task.ID); len(notes) != 0 {
		t.Fatalf("grain's own comments = %+v, want none", notes)
	}
	if len(posted.calls) != 0 {
		t.Fatalf("comments posted on GitHub = %+v, want none", posted.calls)
	}
}
