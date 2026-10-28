package staterepo_test

// The CI step as a deployment gets it: not a command somebody ran, but a
// file Seed and Sync put in the repository and keep there.
//
// Each property here is a way this could have gone wrong. A repository
// grain seeds ends up with the workflow without anybody asking. A
// workflow a merge dropped comes back. A workflow somebody edited is
// never touched again, because a file grain rewrote on a timer would be
// a file whose editor is fighting one. The image in a workflow nobody
// has edited follows the deployment across an upgrade, because a check
// running a build that knows another schema fails every pull request
// against this repository over nothing in the change -- and a check an
// earlier grain wrote is brought up to this build's wording so that it
// can be. And a remote that
// will not accept a file under .github/workflows -- which is what GitHub
// says to a credential without the permission -- costs the deployment
// the CI step and nothing else: the commit is undone, whatever check was
// already there is left as it was, the export goes on, and grain tries
// again a day later rather than every thirty seconds.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// gitOutput is git without the t.Fatal: for the questions whose answer is
// allowed to be "there is no such file".
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// remoteWorkflow is the workflow as the remote holds it, or "" if the
// remote has none -- read out of the bare repository rather than out of
// the working tree, because "grain pushed it" is the claim.
func remoteWorkflow(t *testing.T, remote string) string {
	t.Helper()
	out, err := gitOutput(remote, "show", "main:"+staterepo.WorkflowFile)
	if err != nil {
		return ""
	}
	return out
}

// The file grain writes declares steps, and says so where somebody who
// has been told otherwise will look.
//
// Both halves come from the same report. A deployment's every pull
// request went red on this check, the operator opened one of the runs on
// github.com, and what that page showed was jobs with no steps under
// them -- which is how GitHub draws a run it concluded before any job of
// it started, and which reads as an accusation against the file below.
// It was filed as a bug in the workflow grain generates. The workflow
// grain generates has two steps and has never had none, and the header
// now says what the page is describing, so the next reader of that page
// is not sent to this file for a fault that is not in it.
func TestTheGeneratedWorkflowDeclaresStepsAndSaysWhatANoStepsRunMeans(t *testing.T) {
	t.Parallel()
	body := string(staterepo.Workflow("ghcr.io/example/grain:sha-abc1234"))

	// The steps themselves. Counted rather than parsed -- adding a YAML
	// library to assert "this list is not empty" would cost more than it
	// pins -- but counted off the block, so a `steps:` that lost its
	// entries fails here.
	_, steps, ok := strings.Cut(body, "\n    steps:\n")
	if !ok {
		t.Fatalf("the generated workflow declares no steps block:\n%s", body)
	}
	if got := strings.Count("\n"+steps, "\n      - "); got < 2 {
		t.Errorf("the generated workflow's job has %d step(s); it checks out and it runs the check:\n%s",
			got, steps)
	}

	// And the sentence that answers the page. Its exact wording is the
	// template's business; that the header names the symptom at all is
	// this test's.
	for _, want := range []string{"no steps", "never started"} {
		if !strings.Contains(body, want) {
			t.Errorf("the generated workflow's header does not say %q, so a reader who has "+
				"just been shown a run with no steps has nothing here to correct them:\n%s",
				want, body)
		}
	}
}

func TestSeedingARepositoryInstallsTheCheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	body := remoteWorkflow(t, remote)
	if !strings.Contains(body, "state check /state") {
		t.Fatalf("the seed did not push a workflow that runs the check:\n%s", body)
	}
	if !strings.Contains(body, staterepo.DefaultCheckImage) {
		t.Errorf("the workflow does not name the default image:\n%s", body)
	}
	// Its own commit, holding nothing but the workflow. That is what
	// makes a remote's refusal survivable: there is a commit to drop, and
	// dropping it takes no dump with it.
	files := git(t, dir, "show", "--name-only", "--format=", "HEAD")
	if strings.TrimSpace(files) != staterepo.WorkflowFile {
		t.Errorf("the workflow commit carries more than the workflow: %q", files)
	}
}

// Installing the workflow moves HEAD, and a HEAD this host's database
// has not been loaded from is precisely what Apply reads as "a pull
// request was merged": it imports the dump into the running database.
// So a workflow commit that left the loaded-head marker behind would
// roll every row written since the last export back -- the template
// somebody created a moment ago, the task somebody filed -- for a commit
// that carries no rows at all.
func TestInstallingTheWorkflowDoesNotRollTheDatabaseBack(t *testing.T) {
	t.Parallel()
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
	// A settings row created after the seed, so it is in the database and
	// not in the dump -- the ordinary state of a deployment between two
	// exports, and a settings row because those are the tables Apply
	// replaces in a database that is already running.
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-1", Name: "nightly", Title: "Run the nightly sweep", CreatedAt: now,
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	if _, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("applying: %v", err)
	}
	got, err := store.GetTemplate(ctx, "tpl-1")
	if err != nil {
		t.Fatalf("reading the template back: %v", err)
	}
	if got == nil {
		t.Fatal("the workflow commit made Apply import the dump over a newer database")
	}
}

// A merged pull request that dropped the file -- a rebase that lost it, a
// change to .github that took it with it -- is the case the file being
// rewritten exists for. Without it the check is installed once and
// silently gone forever after.
func TestSyncPutsBackAWorkflowAMergeDropped(t *testing.T) {
	t.Parallel()
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

	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	git(t, work, "rm", "--quiet", staterepo.WorkflowFile)
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-m", "Drop the workflow")
	git(t, work, "push", "--quiet", "origin", "main")

	// The daemon pulls the merge down and syncs, as its timer does.
	if _, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("applying the merge: %v", err)
	}
	if _, err := os.Stat(workflowPath(dir)); err == nil {
		t.Fatal("the merge did not actually remove the workflow; the test is not testing anything")
	}
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	changed, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion)
	if err != nil {
		t.Fatalf("syncing: %v", err)
	}
	if !changed {
		t.Error("a sync that reinstalled the workflow reported nothing changed")
	}
	if _, err := os.Stat(workflowPath(dir)); err != nil {
		t.Fatalf("the workflow did not come back: %v", err)
	}
	if !strings.Contains(remoteWorkflow(t, remote), "state check /state") {
		t.Error("the reinstalled workflow never reached the remote")
	}
}

// The property the export cycle has to have about every file an agent can
// send a pull request against: grain does not fight a hand edit. A
// deployment pinning the image, a runner somebody changed, a step added
// to the job -- all of it survives, and survives the sync after that too.
func TestSyncLeavesAnEditedWorkflowAlone(t *testing.T) {
	t.Parallel()
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

	// Somebody's pull request pins the image to the tag their deployment
	// runs, which is the edit the generated file invites.
	const edited = "# ours\nname: grain state check\non:\n  pull_request:\njobs:\n  check:\n" +
		"    runs-on: self-hosted\n    steps:\n      - uses: actions/checkout@v4\n"
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	if err := os.WriteFile(filepath.Join(work, filepath.FromSlash(staterepo.WorkflowFile)),
		[]byte(edited), 0o644); err != nil {
		t.Fatalf("editing the workflow: %v", err)
	}
	git(t, work, "add", "--all", ".")
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-m", "Run the check on our own runner")
	git(t, work, "push", "--quiet", "origin", "main")

	if _, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("applying the merge: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := store.PutTask(ctx, task("a1b2")); err != nil {
			t.Fatalf("putting: %v", err)
		}
		if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
			t.Fatalf("syncing: %v", err)
		}
	}
	if got := read(t, workflowPath(dir)); got != edited {
		t.Errorf("the export cycle rewrote a workflow somebody else wrote:\n%s", got)
	}
	if got := remoteWorkflow(t, remote); got != edited {
		t.Errorf("grain pushed its own workflow over the merged one:\n%s", got)
	}
}

// An upgrade is the moment a workflow written once goes wrong. The file
// names the build that installed it; the deployment now runs another
// one; and `grain state check` refuses a dump stamped with a schema it
// does not know, so the check fails every pull request against this
// repository the first time the schema moves -- for a reason that has
// nothing to do with the change in it.
//
// So the image is the one line grain maintains after the fact: a sync
// after an upgrade repoints it, on its own commit, and the syncs after
// that have nothing to do.
func TestSyncRepointsTheCheckAtTheImageThisDeploymentRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	const was = "ghcr.io/bwsalmon/grain/grain:sha-0000000"
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote, CheckImage: was})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	if body := remoteWorkflow(t, remote); !strings.Contains(body, was) {
		t.Fatalf("the seed did not push a workflow pinned to this build:\n%s", body)
	}

	// The deployment is upgraded: the same repository, the same working
	// tree, a grain that says it is a different image.
	const now = "ghcr.io/bwsalmon/grain/grain:sha-abc1234"
	upgraded, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote, CheckImage: now})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-1", "Configured after the upgrade")
	if _, err := staterepo.Sync(ctx, upgraded, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing after the upgrade: %v", err)
	}
	body := remoteWorkflow(t, remote)
	if !strings.Contains(body, now) || strings.Contains(body, was) {
		t.Errorf("the check still runs the build this deployment has stopped running:\n%s", body)
	}
	// Its own commit still, holding nothing but the workflow: the reason
	// installing one is a commit of its own is that a remote may refuse
	// it, and that is no less true of a one-line change to it.
	at := strings.TrimSpace(git(t, dir, "log", "-1", "--format=%H", "--", staterepo.WorkflowFile))
	files := git(t, dir, "show", "--name-only", "--format=", at)
	if strings.TrimSpace(files) != staterepo.WorkflowFile {
		t.Errorf("the repointing commit carries more than the workflow: %q", files)
	}
	if subject := git(t, dir, "show", "-s", "--format=%s", at); !strings.Contains(subject, "image this deployment runs") {
		t.Errorf("the repointing commit does not say what it did: %q", subject)
	}

	// And it is done: a deployment that syncs every thirty seconds must
	// not commit every thirty seconds.
	before := head(t, dir)
	putTemplate(t, ctx, store, "tpl-2", "And a second setting, a tick later")
	if _, err := staterepo.Sync(ctx, upgraded, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing again: %v", err)
	}
	if got := remoteWorkflow(t, remote); got != body {
		t.Errorf("the workflow was rewritten a second time:\n%s", got)
	}
	if before == head(t, dir) {
		t.Fatal("the second sync exported nothing at all; the test is not testing anything")
	}
}

// The same upgrade, on the deployments that already exist. Every state
// repository seeded before commit b898473e carries the check as the
// grain of that day wrote it, pinned to whatever image it was installed
// with; and until this test passed, grain no longer recognised that file
// as its own, so it kept that image forever and no operator found out
// until a schema bump failed every pull request against it.
//
// It is one commit on the first sync after the upgrade -- the whole
// file, because the file is grain's own text word for word and its
// comment block describes rules grain has since changed -- and nothing
// at all after that.
func TestSyncAdoptsAWorkflowAnEarlierGrainWrote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	const now = "ghcr.io/bwsalmon/grain/grain:sha-abc1234"
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote, CheckImage: now})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	// The repository as an older grain left it: the check it wrote, with
	// the image that grain had, merged in over the one this build seeded.
	// A merge rather than a hand edit, because that is the shape the
	// deployment sees either way -- a file in the remote's history that
	// this build did not write.
	old := pre258(staterepo.DefaultCheckImage)
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	if err := os.WriteFile(filepath.Join(work, filepath.FromSlash(staterepo.WorkflowFile)),
		[]byte(old), 0o644); err != nil {
		t.Fatalf("writing the older grain's workflow: %v", err)
	}
	git(t, work, "add", "--all", ".")
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-m", "The check as it was")
	git(t, work, "push", "--quiet", "origin", "main")
	if _, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("applying the merge: %v", err)
	}
	if got := read(t, workflowPath(dir)); got != old {
		t.Fatalf("the older workflow never reached the working tree; the test is not testing anything")
	}

	putTemplate(t, ctx, store, "tpl-1", "Configured on the build that adopted it")
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}
	body := remoteWorkflow(t, remote)
	if want := string(staterepo.Workflow(now)); body != want {
		t.Errorf("the check an earlier grain wrote was not brought up to date:\n%s", body)
	}
	// Its own commit still, saying what it did: a reviewer of this
	// repository sees a whole file rewritten by a machine and has to be
	// able to find out why that was allowed.
	at := strings.TrimSpace(git(t, dir, "log", "-1", "--format=%H", "--", staterepo.WorkflowFile))
	files := git(t, dir, "show", "--name-only", "--format=", at)
	if strings.TrimSpace(files) != staterepo.WorkflowFile {
		t.Errorf("the adopting commit carries more than the workflow: %q", files)
	}
	if msg := git(t, dir, "show", "-s", "--format=%B", at); !strings.Contains(msg, "an earlier grain") {
		t.Errorf("the adopting commit does not say what it did: %q", msg)
	}

	// And it is done. A deployment that syncs every thirty seconds must
	// not rewrite somebody else's repository every thirty seconds.
	before := head(t, dir)
	putTemplate(t, ctx, store, "tpl-2", "And a second setting, a tick later")
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing again: %v", err)
	}
	if got := remoteWorkflow(t, remote); got != body {
		t.Errorf("the workflow was rewritten a second time:\n%s", got)
	}
	if before == head(t, dir) {
		t.Fatal("the second sync exported nothing at all; the test is not testing anything")
	}
}

// The refusal, on the other commit. A credential that may not write
// workflows is the case this whole shape exists for, and a repointing
// that gets refused must leave the repository with the check it already
// had -- undoing it by deleting the file would cost a deployment the CI
// step it has been running for months over a one-line change it was
// never allowed to make.
func TestARefusedRepointingLeavesTheCheckThatWasAlreadyThere(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	const was = "ghcr.io/bwsalmon/grain/grain:sha-0000000"
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote, CheckImage: was})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	installed := remoteWorkflow(t, remote)
	if !strings.Contains(installed, was) {
		t.Fatalf("the seed did not push a workflow:\n%s", installed)
	}

	// The permission is taken away -- or, as this looks from the
	// deployment, was never there and the file was committed by hand.
	refuseWorkflows(t, remote)
	upgraded, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote,
		CheckImage: "ghcr.io/bwsalmon/grain/grain:sha-abc1234"})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-1", "Configured against a remote that refuses workflows")
	if _, err := staterepo.Sync(ctx, upgraded, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing against a remote that refuses workflows: %v", err)
	}
	// The export still lands, which is the property the workflow's own
	// commit protects.
	hasTemplateOn(t, remote, "main", "tpl-1", "Configured against a remote that refuses workflows")
	// The check is exactly as it was, in the tree and on the remote, and
	// no commit is stranded in front of the next push.
	if got := read(t, workflowPath(dir)); got != installed {
		t.Errorf("the refused repointing left the working tree without the check it had:\n%s", got)
	}
	if got := remoteWorkflow(t, remote); got != installed {
		t.Errorf("the remote's workflow changed under a push it refused:\n%s", got)
	}
	if local, want := head(t, dir), head(t, remote); local != want {
		t.Errorf("a commit the remote refused is in the way: local %s, remote %s", local, want)
	}
}

// Two deployments that must end up with no workflow at all: one whose
// operator has said so, and one with no remote, where there is no GitHub
// to run anything and a workflow commit would only be waiting to be
// refused by the first push after the repository is ever published.
func TestNoWorkflowIsInstalledWhenThereIsNothingToRunIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		remote bool
		cfg    staterepo.Config
	}{
		{name: "the operator turned it off", remote: true, cfg: staterepo.Config{NoWorkflow: true}},
		{name: "local-only, with no remote at all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, db := openDB(t)
			if err := store.PutTask(ctx, task("a1b2")); err != nil {
				t.Fatalf("putting: %v", err)
			}
			cfg := tc.cfg
			cfg.Dir = filepath.Join(t.TempDir(), "state")
			if tc.remote {
				cfg.Remote = bareRemote(t)
			}
			repo, err := staterepo.Open(ctx, cfg)
			if err != nil {
				t.Fatalf("opening: %v", err)
			}
			if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
				t.Fatalf("loading: %v", err)
			}
			if err := store.PutTask(ctx, task("c3d4")); err != nil {
				t.Fatalf("putting: %v", err)
			}
			if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
				t.Fatalf("syncing: %v", err)
			}
			if _, err := os.Stat(workflowPath(cfg.Dir)); err == nil {
				t.Error("a workflow was installed anyway")
			}
		})
	}
}

// The failure this whole shape is built around. GitHub refuses a push
// that adds a file under .github/workflows unless the credential making
// it may write workflows, and grain's own installation token need not be
// able to. A deployment that hit that has to come out of it with its
// state pushed, its working tree clean, and no commit stranded in front
// of every later push.
func TestAWorkflowTheRemoteRefusesIsUndoneAndTriedAgainLater(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	putTemplate(t, ctx, store, "tpl-1", "Configured before the seed")
	remote := bareRemote(t)
	refuseWorkflows(t, remote)
	clock := now
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{
		Dir: dir, Remote: remote, Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	// The seed still seeds: the dump reaches the remote, and the refusal
	// of the workflow is not a failure the deployment sees.
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading against a remote that refuses workflows: %v", err)
	}
	hasTemplateOn(t, remote, "main", "tpl-1", "Configured before the seed")
	if _, err := os.Stat(workflowPath(dir)); err == nil {
		t.Error("the workflow grain could not push was left in the working tree")
	}
	// Nothing stranded: the local branch is exactly what the remote has,
	// so the next push is a fast-forward rather than a rejection.
	if local, want := head(t, dir), head(t, remote); local != want {
		t.Errorf("a commit that can never be pushed is in the way: local %s, remote %s", local, want)
	}

	// The permission arrives, but grain does not spend a commit per tick
	// finding that out: within the retry interval it leaves the
	// repository alone.
	allowWorkflows(t, remote)
	putTemplate(t, ctx, store, "tpl-2", "Configured while the refusal stood")
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing after the refusal: %v", err)
	}
	if _, err := os.Stat(workflowPath(dir)); err == nil {
		t.Error("grain offered the workflow again on the very next tick")
	}
	if remoteWorkflow(t, remote) != "" {
		t.Error("the remote holds a workflow grain was refused a moment ago")
	}

	// A day later it tries again, and this time it lands.
	clock = clock.Add(25 * time.Hour)
	putTemplate(t, ctx, store, "tpl-3", "Configured a day later")
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing a day later: %v", err)
	}
	if !strings.Contains(remoteWorkflow(t, remote), "state check /state") {
		t.Error("grain never tried the workflow again once the credential could push it")
	}
}

// The refusal above is not the only way a workflow push fails, and the
// other ways used to be the dangerous ones. A push that fails for a
// reason grain cannot recognise as GitHub's refusal -- an unreachable
// remote, an expired token, a proxy between the two -- left the commit
// where it was, on the reasoning that the next push that works would
// carry it. It would: the next push is the *export's*, and it carries the
// workflow commit with it. So a deployment whose credential turns out not
// to have the permission gets the refusal on that push instead, where
// there is no undo -- and the commit is then in front of every push it
// will ever make. Its settings stop reaching its remote entirely, over a
// file that was only ever worth a CI step, and no later tick can clear
// it because installWorkflow sees the file in the tree and does nothing.
//
// Two ordinary things in sequence, neither of them exotic: one failed
// push, and a credential that cannot write workflows -- which is the very
// case this whole shape exists for.
func TestAWorkflowPushThatFailsForSomeOtherReasonStrandsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	// The remote takes the seed and not the workflow, in words grain has
	// no business reading as the permission refusal.
	rejectWorkflowPush(t, remote, "internal server error, try again later")
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-1", "Configured before the seed")
	// The seed itself lands whatever happens to the workflow: that is the
	// property the workflow's own commit and push exist to protect.
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading against a remote that rejected the workflow push: %v", err)
	}
	hasTemplateOn(t, remote, "main", "tpl-1", "Configured before the seed")
	// Nothing of the workflow is left behind: not in the tree, and not as
	// a commit in front of the next push.
	if _, err := os.Stat(workflowPath(dir)); err == nil {
		t.Error("the workflow grain could not push was left in the working tree")
	}
	if local, want := head(t, dir), head(t, remote); local != want {
		t.Errorf("a commit the remote has not got is in the way: local %s, remote %s", local, want)
	}

	// And the credential turns out to be one that may not write
	// workflows, which is what it always was. grain offers the file again
	// -- it did not spend its retry on a failure that said nothing about
	// the permission -- is refused, undoes it, and the export goes on
	// reaching the remote.
	refuseWorkflows(t, remote)
	putTemplate(t, ctx, store, "tpl-2", "Configured once the permission turned out to be missing")
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing after a workflow push that never landed: %v", err)
	}
	hasTemplateOn(t, remote, "main", "tpl-2", "Configured once the permission turned out to be missing")
	if remoteWorkflow(t, remote) != "" {
		t.Error("the remote holds a workflow it refused")
	}
}

// The refusal read back out of the repository, which is what the State
// pane says it with. Until this existed the only trace of it was a line
// in the journal: a deployment with no CI step on its own state looked,
// from anywhere an operator was likely to look, exactly like one whose
// check runs -- syncing happily, nothing in error, because none of that
// is untrue. The condition has to outlive the log line that reported it,
// and it has to stop being reported the moment somebody installs the
// file by hand, which is the fix the pane sends them off to make.
func TestARefusedWorkflowIsReadableAfterTheJournalLineHasScrolledPast(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	remote := bareRemote(t)
	refuseWorkflows(t, remote)
	clock := now
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{
		Dir: dir, Remote: remote, Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if _, refused := repo.WorkflowRefusedAt(ctx); refused {
		t.Fatal("a repository nothing has been pushed to yet reports a refusal")
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading against a remote that refuses workflows: %v", err)
	}

	at, refused := repo.WorkflowRefusedAt(ctx)
	if !refused {
		t.Fatal("a deployment that could not install the check has no way to say so")
	}
	if !at.Equal(now) {
		t.Errorf("refused at %s, want %s", at, now)
	}

	// The operator does what the pane tells them to: writes the file in a
	// clone of their own -- `grain state ci` -- and commits it with a
	// credential that may. The deployment pulls it down, and stops saying
	// the check is missing.
	allowWorkflows(t, remote)
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	if _, err := staterepo.EnsureWorkflow(work, staterepo.DefaultCheckImage, false); err != nil {
		t.Fatalf("writing the workflow by hand: %v", err)
	}
	git(t, work, "add", "--all", ".")
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-m", "Install the check by hand")
	git(t, work, "push", "--quiet", "origin", "main")
	if _, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("applying the merge: %v", err)
	}
	if _, refused := repo.WorkflowRefusedAt(ctx); refused {
		t.Error("the pane still says the check is missing after somebody installed it")
	}

	// And the refusal is forgotten rather than merely hidden by the file
	// on top of it. A merge drops the workflow again, within the day grain
	// would have waited out after that first refusal; the credential can
	// write workflows now, and grain has to offer it again on this tick
	// rather than sit out a retry interval belonging to a refusal the
	// repository has already got past.
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing once the check was installed: %v", err)
	}
	git(t, work, "pull", "--quiet")
	git(t, work, "rm", "--quiet", staterepo.WorkflowFile)
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-m", "Drop the workflow")
	git(t, work, "push", "--quiet", "origin", "main")
	if _, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("applying the merge that dropped it: %v", err)
	}
	if err := store.PutTask(ctx, task("c3d4")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing after the merge that dropped it: %v", err)
	}
	if !strings.Contains(remoteWorkflow(t, remote), "state check /state") {
		t.Error("grain waited out the old refusal's retry interval before putting the check back")
	}
}

// Two deployments with nothing to report: the ones that were never
// offering the workflow in the first place. A local-only repository has
// no GitHub to run a check, and an operator who set noWorkflow has said
// they do not want one -- a pane telling either of them their CI step is
// missing would be reporting the setting back to the person who chose
// it.
func TestNoRefusalIsReportedWhereNoWorkflowWasEverOffered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		remote bool
		cfg    staterepo.Config
	}{
		{name: "the operator turned it off", remote: true, cfg: staterepo.Config{NoWorkflow: true}},
		{name: "local-only, with no remote at all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, db := openDB(t)
			if err := store.PutTask(ctx, task("a1b2")); err != nil {
				t.Fatalf("putting: %v", err)
			}
			cfg := tc.cfg
			cfg.Dir = filepath.Join(t.TempDir(), "state")
			if tc.remote {
				cfg.Remote = bareRemote(t)
				refuseWorkflows(t, cfg.Remote)
			}
			repo, err := staterepo.Open(ctx, cfg)
			if err != nil {
				t.Fatalf("opening: %v", err)
			}
			if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
				t.Fatalf("loading: %v", err)
			}
			if _, refused := repo.WorkflowRefusedAt(ctx); refused {
				t.Error("a deployment that never wanted a workflow reports one as refused")
			}
		})
	}
}

func head(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
}

// refuseWorkflows makes a bare remote answer a push carrying a file under
// .github/workflows the way GitHub answers one made with a credential
// that has no workflows permission: rejected, with that sentence in the
// output, which is the only thing git hands back and so the only thing
// grain can recognise it by.
func refuseWorkflows(t *testing.T, remote string) {
	t.Helper()
	rejectWorkflowPush(t, remote, "refusing to allow a GitHub App to create or update workflow "+
		"`.github/workflows/grain-state-check.yml` without `workflows` permission")
}

// rejectWorkflowPush is refuseWorkflows with the remote's own words as an
// argument, for the case that is not GitHub's refusal: a proxy, a hook of
// somebody's own, a remote that was there a second ago. grain must not
// read one as the other, and what it does about the second is what
// TestAWorkflowPushThatFailsForSomeOtherReasonStrandsNothing is about.
func rejectWorkflowPush(t *testing.T, remote, message string) {
	t.Helper()
	hook := "#!/bin/sh\n" +
		"while read -r old new ref; do\n" +
		"  if [ \"$old\" = \"0000000000000000000000000000000000000000\" ]; then\n" +
		"    files=$(git ls-tree -r --name-only \"$new\")\n" +
		"  else\n" +
		"    files=$(git diff --name-only \"$old\" \"$new\")\n" +
		"  fi\n" +
		"  case \"$files\" in\n" +
		"    *.github/workflows/*)\n" +
		"      echo '" + message + "' >&2\n" +
		"      exit 1\n" +
		"      ;;\n" +
		"  esac\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte(hook), 0o755); err != nil {
		t.Fatalf("installing the pre-receive hook: %v", err)
	}
}

func allowWorkflows(t *testing.T, remote string) {
	t.Helper()
	if err := os.Remove(filepath.Join(remote, "hooks", "pre-receive")); err != nil {
		t.Fatalf("removing the pre-receive hook: %v", err)
	}
}
