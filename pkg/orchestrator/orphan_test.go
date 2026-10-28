package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// grainNotes is every comment grain left on a task in its own voice
// (automation, with no OnBehalfOf) -- the orphan note, and nothing a run
// said through relayComment, which is grain speaking *for* an agent.
func grainNotes(t *testing.T, ctx context.Context, store *model.Store, taskID string) []string {
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

// A close landing while the run was still live, after that run had opened
// its own pull request, is the case the human's own close could not say
// anything about: at the moment they closed the task there was no link on
// it to name. The finish path is where that is discovered, and where the
// same note is left -- on the task and on the pull request itself.
//
// The pull request is still not touched in any other way. See
// salvagePushedBranch's own doc comment and
// TestClosingATaskLeavesThePullRequestItsRunAlreadyOpenedAlone, which
// pins that half.
func TestAPullRequestOrphanedByACloseMidRunIsAnnouncedOnBothSides(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	pr := openedMidRun(t, ctx, store, client, task)
	closeTask(t, ctx, store, task.ID, baseTime)

	result := toolResult(agent.ToolCall{Name: "open_pull_request"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	ref := model.PullRequestRef{Repo: repo, Number: pr.Number}.String()
	notes := grainNotes(t, ctx, store, task.ID)
	if len(notes) != 1 || !strings.Contains(notes[0], ref) {
		t.Fatalf("grain's own comments = %+v, want one note naming %s", notes, ref)
	}
	posted := sim.Comments[pr.Number]
	if len(posted) != 1 || !strings.Contains(posted[0].Body, "grain has stopped watching "+ref) {
		t.Fatalf("comments on the pull request = %+v, want grain's own note", posted)
	}
	// Told, and nothing more. The one path that closes a pull request
	// needs a human to have asked for it as they closed the task
	// (ui.CloseOptions.ClosePullRequest); this path only ever finds a
	// close that already happened, and must not act on somebody's
	// earlier choice as though it had been made here.
	if got := prByHead(t, sim, model.BranchName(task.ID)); got.State != "open" || got.Merged {
		t.Fatalf("pull request = %+v, want it left open and unmerged", got)
	}
}

// closingClient is a github.Client that closes a task from underneath the
// finish path, in the middle of the very call that opens the pull
// request. That is the narrowest ordering there is: salvagePushedBranch's
// close check has already passed and read "open", and the close lands
// before the link it is about to write exists -- so the human's own close
// saw no pull request to speak of, and the check that would have caught
// it has already been made.
type closingClient struct {
	github.Client
	closeNow func()
}

func (c closingClient) CreatePullRequest(owner, repo, head, base, title, body string) (github.PullRequest, error) {
	c.closeNow()
	return c.Client.CreatePullRequest(owner, repo, head, base, title, body)
}

func TestACloseLandingWhileTheFinishPathOpensThePullRequestIsStillAnnounced(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	racing := closingClient{Client: client, closeNow: func() { closeTask(t, ctx, store, task.ID, baseTime) }}
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, racing, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if st, err := store.State(ctx, task.ID); err != nil || st != model.StateClosed {
		t.Fatalf("state = %q (%v), want closed -- the close outranks the completion written after it", st, err)
	}
	pr := prByHead(t, sim, model.BranchName(task.ID))
	ref := model.PullRequestRef{Repo: repo, Number: pr.Number}.String()
	notes := grainNotes(t, ctx, store, task.ID)
	if len(notes) != 1 || !strings.Contains(notes[0], ref) {
		t.Fatalf("grain's own comments = %+v, want one note naming %s", notes, ref)
	}
	if posted := sim.Comments[pr.Number]; len(posted) != 1 {
		t.Fatalf("comments on the pull request = %+v, want grain's own note", posted)
	}
}

// The ordinary ending says nothing. A run that pushed, had its pull
// request opened and left the task completed has orphaned nothing, and a
// note on every finished task would make the real one worth ignoring.
func TestAFinishThatOrphansNothingLeavesNoNote(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if notes := grainNotes(t, ctx, store, task.ID); len(notes) != 0 {
		t.Fatalf("grain's own comments = %+v, want none on a task that completed", notes)
	}
	pr := prByHead(t, sim, model.BranchName(task.ID))
	if posted := sim.Comments[pr.Number]; len(posted) != 0 {
		t.Fatalf("comments on the pull request = %+v, want none", posted)
	}
}

// Two ends discovering the same orphan say it once between them. The
// human's close and the finishing run's own discovery are the two, and
// which of them gets there first is a race by definition -- the copy on
// GitHub is the one that matters here, since nothing there would dedupe a
// second comment.
func TestTheSameOrphanIsNeverAnnouncedTwice(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	pr := openedMidRun(t, ctx, store, client, task)
	closeTask(t, ctx, store, task.ID, baseTime)

	result := toolResult(agent.ToolCall{Name: "open_pull_request"})
	for i := 0; i < 3; i++ {
		if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
			t.Fatalf("ProcessResult (pass %d): %v", i, err)
		}
	}

	if notes := grainNotes(t, ctx, store, task.ID); len(notes) != 1 {
		t.Fatalf("grain's own comments = %+v, want exactly one", notes)
	}
	if posted := sim.Comments[pr.Number]; len(posted) != 1 {
		t.Fatalf("comments on the pull request = %+v, want exactly one", posted)
	}
}

// A GitHub call that fails does not cost the note: it is written into the
// note itself, on the task, which is the copy grain can always leave.
type refusingClient struct {
	github.Client
}

func (refusingClient) CreateComment(owner, repo string, number int, body string) (int, error) {
	return 0, &github.Error{Status: 403, Body: []byte("no write access")}
}

func TestANoteStillLandsOnTheTaskWhenGitHubRefusesTheComment(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	pr := openedMidRun(t, ctx, store, client, task)
	closeTask(t, ctx, store, task.ID, baseTime)

	result := toolResult(agent.ToolCall{Name: "open_pull_request"})
	if err := orchestrator.ProcessResult(ctx, store, refusingClient{client}, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v -- a refused comment must not fail the finish", err)
	}

	ref := model.PullRequestRef{Repo: repo, Number: pr.Number}.String()
	notes := grainNotes(t, ctx, store, task.ID)
	if len(notes) != 1 || !strings.Contains(notes[0], ref) {
		t.Fatalf("grain's own comments = %+v, want one note naming %s", notes, ref)
	}
	if !strings.Contains(notes[0], "could not leave this note on the pull request itself") {
		t.Fatalf("note = %q, want it to say the pull request was not told", notes[0])
	}
	if posted := sim.Comments[pr.Number]; len(posted) != 0 {
		t.Fatalf("comments on the pull request = %+v, want none -- GitHub refused", posted)
	}
}

// Belt and braces on the sim: nothing above would notice if
// githubsim.Sim quietly stopped recording pull request comments, since
// every assertion about GitHub's copy reads Sim.Comments back.
func TestSimRecordsCommentsPostedOnAPullRequest(t *testing.T) {
	t.Parallel()
	sim, client := newSim(t, "acme", "widgets", "main")
	pushBranch(t, sim.BareRepo, "grain/task-1")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/task-1", "main", "t", "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateComment("acme", "widgets", pr.Number, "grain has stopped watching this"); err != nil {
		t.Fatal(err)
	}
	if got := sim.Comments[pr.Number]; len(got) != 1 || got[0].Body != "grain has stopped watching this" {
		t.Fatalf("Sim.Comments[%d] = %+v", pr.Number, got)
	}
}
