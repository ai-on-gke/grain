package staterepo_test

// grain checking a pull request against its own settings, on this host,
// and saying so as a check run (postcheck.go).
//
// What these pin down is not that the check runs -- that is `grain state
// check`'s own business, and it is stubbed here -- but the order things
// happen in and what is left behind when they do not. The gate this
// replaces fails closed, and the whole hazard of replacing it is that
// the obvious implementation fails open: a missing check run is not a
// blocked pull request, it is a clean one. So: the run is created before
// the tree is checked, a check that could not be run leaves the created
// run unfinished rather than concluding it green, and a tick that has
// already checked a commit does not hang a second check on it.

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/staterepo"
)

// postedRun is one call a test saw on Config.PostCheckRun.
type postedRun struct {
	post staterepo.CheckRunPost
}

// checkPoster is a state repository with one open pull request on it and
// a stubbed check, gathering what grain posted about it.
type checkPoster struct {
	posts   []postedRun
	checked []string
	// sawProposal is whether the tree grain handed the check had the pull
	// request's own change in it, asked while the check is running
	// because the worktree is gone by the time it returns.
	sawProposal bool
	verdict     func(dir string) (string, error)
	next        int64
}

func (p *checkPoster) post(_ context.Context, post staterepo.CheckRunPost) (int64, error) {
	p.posts = append(p.posts, postedRun{post: post})
	if post.ID != 0 {
		return post.ID, nil
	}
	p.next++
	return p.next, nil
}

func (p *checkPoster) check(_ context.Context, dir string) (string, error) {
	p.checked = append(p.checked, dir)
	if _, err := os.Stat(filepath.Join(dir, "PROPOSED")); err == nil {
		p.sawProposal = true
	}
	if p.verdict == nil {
		return "state repository loads", nil
	}
	return p.verdict(dir)
}

// pullRequestOn publishes a pull request's head on the bare remote the
// way GitHub does -- refs/pull/N/head, pointing at a commit that is not
// on the state branch -- and answers with its sha.
func pullRequestOn(t *testing.T, remote, dir string, number int) string {
	t.Helper()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// A commit of its own, off the state branch and never merged into it,
	// so that a check reading the live working tree instead of the pull
	// request's would not see it.
	git("checkout", "--quiet", "-b", "proposal")
	if err := os.WriteFile(filepath.Join(dir, "PROPOSED"), []byte("a change nobody has merged\n"), 0o600); err != nil {
		t.Fatalf("writing the proposed change: %v", err)
	}
	git("add", "PROPOSED")
	git("-c", "user.name=tester", "-c", "user.email=tester@example.com", "commit", "--quiet", "-m", "propose")
	sha := git("rev-parse", "HEAD")
	git("push", "--quiet", remote, "HEAD:refs/pull/"+strconv.Itoa(number)+"/head")
	git("checkout", "--quiet", "-")
	git("branch", "--quiet", "-D", "proposal")
	return sha
}

// openPostingRepo is a state repository that posts its own check, with
// one open pull request waiting on it.
func openPostingRepo(t *testing.T, poster *checkPoster) (context.Context, *staterepo.Repo, *sql.DB, string) {
	t.Helper()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding the remote: %v", err)
	}
	head := pullRequestOn(t, remote, dir, 7)
	// Re-opened with the posting seams wired, now that there is something
	// to post about: Config is read at Open and this is the shape a
	// deployment with "postCheck": true is opened in
	// (pkg/cli/staterepo.go).
	repo, err = staterepo.Open(ctx, staterepo.Config{
		Dir: dir, Remote: remote, PostCheck: true,
		OpenPullRequests: func(context.Context) ([]staterepo.PullRequestHead, error) {
			return []staterepo.PullRequestHead{{Number: 7, Title: "a settings change", HeadSHA: head}}, nil
		},
		PostCheckRun: poster.post,
		CheckTree:    poster.check,
	})
	if err != nil {
		t.Fatalf("re-opening: %v", err)
	}
	_ = store
	return ctx, repo, db, head
}

func TestGrainPostsItsOwnCheckOnAStateRepoPullRequest(t *testing.T) {
	t.Parallel()
	poster := &checkPoster{}
	ctx, repo, db, head := openPostingRepo(t, poster)
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}
	if len(poster.posts) != 2 {
		t.Fatalf("posted %d times, want a create and a conclude: %+v", len(poster.posts), poster.posts)
	}
	// The create comes first, and it is in_progress before anything has
	// been checked: that ordering is what makes a deployment which dies
	// mid-check leave a pull request that blocks rather than one that
	// merges.
	created := poster.posts[0].post
	if created.ID != 0 || created.Status != "in_progress" || created.Conclusion != "" {
		t.Fatalf("the first post was not an unfinished new check run: %+v", created)
	}
	if created.HeadSHA != head || created.Name != staterepo.PostedCheckName {
		t.Fatalf("the check run was not posted against the pull request's head: %+v", created)
	}
	concluded := poster.posts[1].post
	if concluded.ID == 0 || concluded.Status != "completed" || concluded.Conclusion != "success" {
		t.Fatalf("the second post did not conclude the run it created: %+v", concluded)
	}
	if concluded.ID != poster.next {
		t.Fatalf("the conclusion named check run %d, not the %d that was created",
			concluded.ID, poster.next)
	}
	// And what was checked is the pull request's tree, not this
	// deployment's live one.
	if len(poster.checked) != 1 {
		t.Fatalf("checked %d trees, want 1", len(poster.checked))
	}
	if !poster.sawProposal {
		t.Fatal("grain checked a tree without the proposed change in it")
	}
	if poster.checked[0] == repo.Dir() {
		t.Fatal("grain checked its own working tree instead of a worktree of the pull request")
	}
	// A tree that was checked out is a tree that is cleaned up: a
	// worktree left behind would accumulate one per settings change for
	// the life of the deployment.
	if _, err := os.Stat(poster.checked[0]); !os.IsNotExist(err) {
		t.Fatalf("the worktree grain checked in is still there: %v", err)
	}
	// The next tick says nothing more about a commit it has already
	// answered for.
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing again: %v", err)
	}
	if len(poster.posts) != 2 {
		t.Fatalf("a second tick posted again: %+v", poster.posts[2:])
	}
}

func TestAStateRepoPullRequestThatDoesNotLoadIsPostedRed(t *testing.T) {
	t.Parallel()
	poster := &checkPoster{verdict: func(string) (string, error) {
		return "tasks.json: unexpected end of JSON input", errors.New("tasks.json: unexpected end of JSON input")
	}}
	ctx, repo, db, _ := openPostingRepo(t, poster)
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}
	if len(poster.posts) != 2 {
		t.Fatalf("posted %d times, want a create and a conclude", len(poster.posts))
	}
	concluded := poster.posts[1].post
	if concluded.Conclusion != "failure" {
		t.Fatalf("a tree that does not load was concluded %q", concluded.Conclusion)
	}
	// The reason travels with the verdict. That is the whole reason this
	// is a check run rather than a commit status: a status carries 140
	// characters of description and no body, so the import's own report
	// would have had nowhere to go.
	if !strings.Contains(concluded.Text, "unexpected end of JSON input") {
		t.Fatalf("the check run does not carry what the import said: %+v", concluded)
	}
}

func TestACheckGrainCouldNotRunLeavesThePullRequestBlocked(t *testing.T) {
	t.Parallel()
	failed := true
	// ErrCheckNotRun, the way runCheckSubprocess marks its own: this is
	// the check not having run at all, which is a different fact from a
	// tree that does not load and must not be concluded from.
	poster := &checkPoster{}
	poster.verdict = func(string) (string, error) {
		if failed {
			return "", errors.Join(staterepo.ErrCheckNotRun, errors.New("the host is out of disk"))
		}
		return "state repository loads", nil
	}
	ctx, repo, db, _ := openPostingRepo(t, poster)
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}
	// One post: the check run was created and never concluded, which is
	// PENDING to the merge queue and a pull request that does not merge.
	// The one answer this must never give is a green check, and the one
	// state it must never leave is no check at all.
	if len(poster.posts) != 1 {
		t.Fatalf("posted %d times, want the create alone: %+v", len(poster.posts), poster.posts)
	}
	if poster.posts[0].post.Status != "in_progress" {
		t.Fatalf("the unfinished check run is not in progress: %+v", poster.posts[0].post)
	}
	// And the next tick picks that same run up rather than posting a
	// second one at the same commit.
	failed = false
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing again: %v", err)
	}
	if len(poster.posts) != 2 {
		t.Fatalf("the retry did not conclude the check it had already created: %+v", poster.posts)
	}
	concluded := poster.posts[1].post
	if concluded.ID == 0 || poster.next != 1 {
		t.Fatalf("the retry posted a new check run rather than finishing the old one: %+v", concluded)
	}
	if concluded.Status != "completed" || concluded.Conclusion != "success" {
		t.Fatalf("the retry did not conclude the run: %+v", concluded)
	}
}
