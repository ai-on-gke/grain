package model_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// The two outcomes a close can have for the pull request, named once
// here so every test below reads as which of the two it is about:
// orphaned is the ordinary close, which leaves the pull request open,
// and closedToo is a close whose human asked for the pull request to be
// closed with the task and got it.
var (
	orphaned  = model.PullRequestCloseOutcome{}
	closedToo = model.PullRequestCloseOutcome{Requested: true}
)

func orphanTask(t *testing.T, store *model.Store, ctx context.Context, id string) {
	t.Helper()
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "t",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Binding:  model.BindingDirective,
		Approval: &model.Attribution{Actor: human},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing task: %v", err)
	}
}

func orphanNotes(t *testing.T, store *model.Store, ctx context.Context, id string) []string {
	t.Helper()
	comments, err := store.Comments(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range comments {
		out = append(out, c.Body)
	}
	return out
}

// The note is said once per pull request, however many times the close
// that provokes it is discovered -- by the human who closed the task, by
// the run finishing underneath them, or by both at once (see
// ui.Client.noteOrphanedPullRequests and orchestrator's own copy). The
// second write is dropped even though its body differs, because what
// varies between two writes is only how the copy on GitHub went.
func TestNoteOrphanedPullRequestSaysItOncePerPullRequest(t *testing.T) {
	store, ctx := open(t)
	orphanTask(t, store, ctx, "t1")
	ref := model.PullRequestRef{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Number: 3}

	if noted, err := store.OrphanedPullRequestNoted(ctx, "t1", ref, orphaned); err != nil || noted {
		t.Fatalf("noted = %v (%v), want false before anything is said", noted, err)
	}
	first := model.OrphanedPullRequestNote("t1", ref, orphaned, nil)
	if err := store.NoteOrphanedPullRequest(ctx, "t1", ref, first, now); err != nil {
		t.Fatal(err)
	}
	if noted, err := store.OrphanedPullRequestNoted(ctx, "t1", ref, orphaned); err != nil || !noted {
		t.Fatalf("noted = %v (%v), want true once it has been said", noted, err)
	}

	second := model.OrphanedPullRequestNote("t1", ref, orphaned, errors.New("403 from GitHub"))
	if err := store.NoteOrphanedPullRequest(ctx, "t1", ref, second, now); err != nil {
		t.Fatal(err)
	}
	notes := orphanNotes(t, store, ctx, "t1")
	if len(notes) != 1 || notes[0] != first {
		t.Fatalf("comments = %+v, want only the first note", notes)
	}
}

// Two pull requests on one task are two different things to say. A task
// can carry more than one fixes-link -- a run that opened its own pull
// request against a base that later moved, a link written by hand -- and
// each names a pull request nobody is going to merge.
func TestNoteOrphanedPullRequestSpeaksForEachPullRequest(t *testing.T) {
	store, ctx := open(t)
	orphanTask(t, store, ctx, "t1")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	first := model.PullRequestRef{Repo: repo, Number: 3}
	second := model.PullRequestRef{Repo: repo, Number: 4}

	for _, ref := range []model.PullRequestRef{first, second} {
		if err := store.NoteOrphanedPullRequest(ctx, "t1", ref,
			model.OrphanedPullRequestNote("t1", ref, orphaned, nil), now); err != nil {
			t.Fatal(err)
		}
	}
	notes := orphanNotes(t, store, ctx, "t1")
	if len(notes) != 2 {
		t.Fatalf("comments = %+v, want one per pull request", notes)
	}
	if !strings.Contains(notes[0], first.String()) || !strings.Contains(notes[1], second.String()) {
		t.Fatalf("comments = %+v, want each to name its own pull request", notes)
	}
}

// '_' is a legal character in a GitHub repo name and a single-character
// wildcard in SQL's LIKE, so a dedupe written with LIKE would read a note
// about acme/wid_ets#3 as covering acme/widgets#3 and say nothing about
// the second pull request at all. The comparison is substr for exactly
// this reason.
func TestNoteOrphanedPullRequestTreatsUnderscoresAsLiteral(t *testing.T) {
	store, ctx := open(t)
	orphanTask(t, store, ctx, "t1")
	underscored := model.PullRequestRef{Repo: model.RepoRef{Owner: "acme", Name: "wid_ets"}, Number: 3}
	plain := model.PullRequestRef{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Number: 3}

	if err := store.NoteOrphanedPullRequest(ctx, "t1", underscored,
		model.OrphanedPullRequestNote("t1", underscored, orphaned, nil), now); err != nil {
		t.Fatal(err)
	}
	noted, err := store.OrphanedPullRequestNoted(ctx, "t1", plain, orphaned)
	if err != nil {
		t.Fatal(err)
	}
	if noted {
		t.Fatalf("a note about %s must not cover %s", underscored, plain)
	}
}

// What the two copies say. The one left on the pull request is the same
// explanation as the one on the task, minus the line about having left it
// there -- which is the only part that would read as nonsense on the pull
// request itself.
func TestOrphanedPullRequestNoteSaysWhatHappenedToTheOtherCopy(t *testing.T) {
	ref := model.PullRequestRef{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Number: 3}

	landed := model.OrphanedPullRequestNote("t1", ref, orphaned, nil)
	if !strings.HasPrefix(landed, "grain has stopped watching acme/widgets#3.") {
		t.Fatalf("note = %q, want it to open by naming the pull request", landed)
	}
	if !strings.Contains(landed, "task t1") && !strings.Contains(landed, "Task t1") {
		t.Fatalf("note = %q, want it to name the task", landed)
	}
	if !strings.Contains(landed, "left this note on the pull request itself too") {
		t.Fatalf("note = %q, want it to say the pull request was told", landed)
	}

	failed := model.OrphanedPullRequestNote("t1", ref, orphaned, errors.New("403 from GitHub"))
	if !strings.Contains(failed, "could not leave this note on the pull request itself: 403 from GitHub") {
		t.Fatalf("note = %q, want it to say why the pull request was not told", failed)
	}

	posted := model.OrphanedPullRequestComment("t1", ref, orphaned)
	if strings.Contains(posted, "pull request itself") {
		t.Fatalf("comment = %q, want it without the line about the other copy", posted)
	}
	if !strings.HasPrefix(posted, "grain has stopped watching acme/widgets#3.") {
		t.Fatalf("comment = %q, want the same explanation", posted)
	}
}

// The other ending, in the other words. A close that shut the pull
// request has to read as that and not as "grain has stopped watching it":
// they are opposite facts about the same pull request, and the second
// would have a reader looking for a pull request that is not there.
func TestAClosedPullRequestGetsItsOwnWords(t *testing.T) {
	ref := model.PullRequestRef{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Number: 3}

	note := model.OrphanedPullRequestNote("t1", ref, closedToo, nil)
	if !strings.HasPrefix(note, "grain has closed acme/widgets#3.") {
		t.Fatalf("note = %q, want it to open by saying the pull request was closed", note)
	}
	if strings.Contains(note, "still open") {
		t.Fatalf("note = %q, must not call a closed pull request still open", note)
	}
	// The reassurance is the whole basis on which a human was offered the
	// choice: the commits survive, and this is reversible on GitHub.
	if !strings.Contains(note, "Nothing has been done to the branch") ||
		!strings.Contains(note, "reopening acme/widgets#3 on GitHub") {
		t.Fatalf("note = %q, want it to say the commits are kept and how to get the pull request back", note)
	}
	if !strings.Contains(note, "asked for this pull request to be closed") {
		t.Fatalf("note = %q, want it to say this happened because somebody asked", note)
	}

	posted := model.OrphanedPullRequestComment("t1", ref, closedToo)
	if !strings.HasPrefix(posted, "grain has closed acme/widgets#3.") {
		t.Fatalf("comment = %q, want the same explanation", posted)
	}
	if strings.Contains(posted, "pull request itself") {
		t.Fatalf("comment = %q, want it without the line about the other copy", posted)
	}
}

// A close GitHub refused leaves exactly the orphaned pull request an
// unasked-for close would have -- so it gets the orphan's words, plus the
// refusal. Saying only the first would hide that somebody asked; saying
// only the second would leave them thinking the pull request was shut.
func TestARefusedCloseReadsAsAnOrphanPlusTheRefusal(t *testing.T) {
	ref := model.PullRequestRef{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Number: 3}
	refused := model.PullRequestCloseOutcome{Requested: true, Err: errors.New("403 from GitHub")}

	note := model.OrphanedPullRequestNote("t1", ref, refused, nil)
	if !strings.HasPrefix(note, "grain has stopped watching acme/widgets#3.") {
		t.Fatalf("note = %q, want the orphan's own lead -- the pull request is still open", note)
	}
	if !strings.Contains(note, "GitHub refused: 403 from GitHub") {
		t.Fatalf("note = %q, want it to say the close was asked for and refused", note)
	}
	if !strings.Contains(note, "acme/widgets#3 is still open") {
		t.Fatalf("note = %q, want it to say the pull request is still open", note)
	}
}

// Closing a task, reopening it and closing it again -- this time asking
// for the pull request to go too -- is two different things happening,
// and the second is the one that threw work away. Deduping it against the
// first would leave that unsaid.
func TestAClosedPullRequestIsNotedEvenAfterAnEarlierOrphanNote(t *testing.T) {
	store, ctx := open(t)
	orphanTask(t, store, ctx, "t1")
	ref := model.PullRequestRef{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Number: 3}

	if err := store.NoteOrphanedPullRequest(ctx, "t1", ref,
		model.OrphanedPullRequestNote("t1", ref, orphaned, nil), now); err != nil {
		t.Fatal(err)
	}
	noted, err := store.OrphanedPullRequestNoted(ctx, "t1", ref, closedToo)
	if err != nil {
		t.Fatal(err)
	}
	if noted {
		t.Fatal("an orphan note must not stand in for saying the pull request was closed")
	}
	if err := store.NoteOrphanedPullRequest(ctx, "t1", ref,
		model.OrphanedPullRequestNote("t1", ref, closedToo, nil), now); err != nil {
		t.Fatal(err)
	}
	notes := orphanNotes(t, store, ctx, "t1")
	if len(notes) != 2 {
		t.Fatalf("comments = %+v, want the orphan note and the close note", notes)
	}
}

// The other order says nothing more. Once grain has said it closed a pull
// request, a later close of the same task has nothing to add -- and
// "grain has stopped watching it" would be a plainly wrong thing to say
// about a pull request grain shut.
func TestNothingIsSaidAboutAPullRequestGrainAlreadyClosed(t *testing.T) {
	store, ctx := open(t)
	orphanTask(t, store, ctx, "t1")
	ref := model.PullRequestRef{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Number: 3}

	if err := store.NoteOrphanedPullRequest(ctx, "t1", ref,
		model.OrphanedPullRequestNote("t1", ref, closedToo, nil), now); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []model.PullRequestCloseOutcome{orphaned, closedToo} {
		noted, err := store.OrphanedPullRequestNoted(ctx, "t1", ref, outcome)
		if err != nil {
			t.Fatal(err)
		}
		if !noted {
			t.Fatalf("outcome %+v: want nothing further said about a pull request grain closed", outcome)
		}
	}
}
