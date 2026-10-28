// The work that follows a base branch and is not a verdict about it
// (grain/task-409): a repository names the jobs grain dispatches once
// that branch moves, and grain dispatches them whatever the repository's
// releases are doing.
//
// What is being pinned here is mostly what this must *not* do. It is a
// publishing job started by grain on an operator's standing instruction,
// so the event it goes out under matters; and it is not a verdict, so no
// failure of it may reach the merge queue.
package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/packagepublish"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// guestCacheJob is this repository's own case, used as the example
// throughout: one job of the release publisher, which fills the
// content-addressed sandbox guest cache every other CI job reads.
const guestCacheJob = ".grain/workflows/image.yml:guest-cache"

// runBaseBranchDispatchReconciler runs the cycle's own
// "base-branch-dispatch" reconciler, found by name in Reconcilers() so
// that a change dropping it from the cycle fails here rather than
// quietly stopping the cache following the branch.
func runBaseBranchDispatchReconciler(t *testing.T, ctx context.Context, store *model.Store,
	client github.Client, runner ci.Runner, now time.Time) {

	t.Helper()
	// No sandbox guest image, so nothing is dispatched that a repository
	// did not name: the deployment-owned default below is the other
	// helper, and every test written before it keeps its old meaning.
	runBaseBranchDispatchReconcilerForGuest(t, ctx, store, client, runner, "", now)
}

// runBaseBranchDispatchReconcilerForGuest is the same reconciler on a
// deployment whose sandboxes are built from a named guest image -- which
// is what decides whether grain fills that image's own repository's
// cache without being asked (model.GuestCacheFollowsBaseBranch).
func runBaseBranchDispatchReconcilerForGuest(t *testing.T, ctx context.Context, store *model.Store,
	client github.Client, runner ci.Runner, guestImage string, now time.Time) {

	t.Helper()
	deps := orchestrator.Deps{Store: store, Client: client, CI: runner,
		Config: orchestrator.Config{SandboxGuestImage: guestImage}}
	for _, r := range orchestrator.Reconcilers() {
		if r.Name == "base-branch-dispatch" {
			if err := r.Reconcile(ctx, deps, now); err != nil {
				t.Fatalf("dispatching after a merge: %v", err)
			}
			return
		}
	}
	t.Fatal(`no "base-branch-dispatch" reconciler in the cycle: nothing follows a base branch ` +
		`but the release run, which is what grain/task-397 measured the cost of`)
}

// dispatches is every run this fake was asked for under
// `workflow_dispatch` -- the dispatches, as against the task branches'
// own push runs, the base branch's judging run and the release run
// beside it (grain/task-461), which every test here has going on around
// it.
func (f *fakeCI) dispatches() []ci.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ci.Request
	for _, req := range f.requests {
		if req.Event == ci.EventManual {
			out = append(out, req)
		}
	}
	return out
}

// releasesOffNamingTheCacheJob is the state this whole file is about: a
// repository that publishes nothing on a merge any more, and that has
// named the one job which must go on running anyway.
//
// Deliberately with no BaseBranchCIJobs beside it, because that is the
// repository the coupling bites on: nothing here narrows what judges the
// branch, and the cache job is named separately from the judging
// precisely because grain cannot tell one from the other by looking.
func releasesOffNamingTheCacheJob(t *testing.T, ctx context.Context, store *model.Store,
	repo model.RepoRef) {

	t.Helper()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
		Work:                  model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
		PostMergeDispatchJobs: []string{guestCacheJob},
	}); err != nil {
		t.Fatal(err)
	}
}

// The whole of it: releases are off, a change merges, and the job that
// fills the build cache is dispatched against the tree that merge
// produced -- under `workflow_dispatch`, untrusted, and narrowed to the
// job the repository named.
func TestAMergeDispatchesTheNamedJobsWithReleasesOff(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasesOffNamingTheCacheJob(t, ctx, store, repo)

	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	setMergeable(sim, true)
	syncWithCI(t, ctx, store, client, runner, baseTime)
	if !mergedBranch(sim, branch) {
		t.Fatal("the change did not merge")
	}

	runBaseBranchDispatchReconciler(t, ctx, store, client, runner, baseTime.Add(time.Second))
	dispatched := runner.dispatches()
	if len(dispatched) != 1 {
		t.Fatalf("grain dispatched %d runs after the merge, want exactly one: with releases off "+
			"this is the only thing still filling the cache every branch's CI reads", len(dispatched))
	}
	req := dispatched[0]
	if req.TrustedRelease {
		t.Fatal("the dispatch is a trusted release: it is a job an operator named, not a release")
	}
	if req.Ref != "main" || req.Commit != headSHA(t, client, repo, "main") {
		t.Fatalf("the dispatch is against %s at %s, want main's own tip -- the tree whose inputs "+
			"a consumer derives its cache key from", req.Ref, req.Commit)
	}
	if len(req.Jobs) != 1 || req.Jobs[0] != guestCacheJob {
		t.Fatalf("the dispatch asks for %v, want only the job the repository named: a dispatch of "+
			"the whole plan would publish a repository that has asked not to be published", req.Jobs)
	}

	// The event is the whole of what makes this dispatchable rather than
	// a widening of somebody's gate: the credential that lets a job push
	// to this repository's packages enters a sandbox for a release or a
	// dispatch and for nothing else, so a reconciler that asked under
	// `push` would build a guest for twenty-five minutes and be refused
	// at the registry.
	publish := packagepublish.New(packagepublish.Config{})
	if !publish.SupportsCIEvent(string(req.Event)) {
		t.Fatalf("the dispatch goes out under %q, which %q's material is not placed for: a job "+
			"that fills a registry cache has to be able to push to the registry",
			req.Event, packagepublish.CapabilityName)
	}

	row, err := store.GetBaseBranchDispatch(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("the dispatch was recorded nowhere: %+v (%v)", row, err)
	}
	if row.Commit != req.Commit || !row.InFlight() {
		t.Fatalf("the row reads %+v, want the commit it was dispatched for and a run still going", row)
	}
}

// The level-triggered half: the condition is the branch's tip, not a
// merge, so naming the jobs on a repository whose branch has not moved
// since dispatches them on the next cycle rather than on the next merge.
// An operator who has just turned this on because the cache is cold is
// the person that matters to.
func TestNamingTheJobsDispatchesWithoutWaitingForAMerge(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasesOffNamingTheCacheJob(t, ctx, store, repo)

	runBaseBranchDispatchReconciler(t, ctx, store, client, runner, baseTime)
	if got := len(runner.dispatches()); got != 1 {
		t.Fatalf("%d runs were dispatched for a branch nothing has been dispatched on, want one", got)
	}

	// And it is asked for once. The row is what says so, so the next
	// cycle -- same tip, same jobs -- starts nothing.
	runBaseBranchDispatchReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute))
	if got := len(runner.dispatches()); got != 1 {
		t.Fatalf("%d runs were dispatched for one commit, want one: a reconciler that re-asked every "+
			"cycle would spend a deployment's whole CI capacity on a 25-minute build", got)
	}
}

// Two merges landing while one dispatch is in flight are one dispatch,
// not two -- the row is the lock, exactly as model.BaseBranchCI's is
// (grain/task-334) -- and the commits in between are skipped rather than
// queued: the tree a later branch derives its cache key from is the tip,
// not a commit grain merged past.
func TestMergesDuringADispatchDoNotStartASecond(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasesOffNamingTheCacheJob(t, ctx, store, repo)

	first, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	first = grantCI(t, ctx, store, first)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	second = grantCI(t, ctx, store, second)
	greenGrainCI(t, ctx, store, client, runner, first, repo, firstBranch)
	greenGrainCI(t, ctx, store, client, runner, second, repo, secondBranch)
	setMergeable(sim, true)

	syncWithCI(t, ctx, store, client, runner, baseTime)
	runBaseBranchDispatchReconciler(t, ctx, store, client, runner, baseTime.Add(time.Second))
	if got := len(runner.dispatches()); got != 1 {
		t.Fatalf("the first merge dispatched %d runs, want one", got)
	}
	row, err := store.GetBaseBranchDispatch(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatal(err)
	}
	inFlight := row.RunID

	// The second change merges while that run is still going.
	syncWithCI(t, ctx, store, client, runner, baseTime.Add(2*time.Second))
	if !mergedBranch(sim, secondBranch) {
		t.Fatal("the second change did not merge: nothing here holds the queue")
	}
	runBaseBranchDispatchReconciler(t, ctx, store, client, runner, baseTime.Add(3*time.Second))
	if got := len(runner.dispatches()); got != 1 {
		t.Fatalf("%d dispatches are in flight for main at once, want one: two guest builds on one "+
			"branch is exactly the stacking grain/task-334 took out of the post-merge path", got)
	}

	// It finishes, and the next cycle asks once -- against the tip as it
	// stands now, which is the tree containing both merges.
	runner.finish(inFlight, ci.ConclusionSuccess)
	runBaseBranchDispatchReconciler(t, ctx, store, client, runner, baseTime.Add(4*time.Second))
	runBaseBranchDispatchReconciler(t, ctx, store, client, runner, baseTime.Add(5*time.Second))
	dispatched := runner.dispatches()
	if len(dispatched) != 2 {
		t.Fatalf("%d dispatches for two merges, want two: one for the first tree and one for the "+
			"tip the pair of them left", len(dispatched))
	}
	if dispatched[1].Commit != headSHA(t, client, repo, "main") {
		t.Fatalf("the second dispatch is against %s, want main's tip: an intermediate commit is a "+
			"tree no branch will ever derive a key from", dispatched[1].Commit)
	}
}

// A dispatch that fails holds nothing. It is not a verdict about the
// branch: what failed is a cache that stayed cold, and the cost of that
// is a slower job on some later branch -- so the queue goes on merging,
// no repair is filed, and the same commit is not dispatched again.
func TestAFailedDispatchHoldsNothingAndIsNotRetried(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasesOffNamingTheCacheJob(t, ctx, store, repo)

	first, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	first = grantCI(t, ctx, store, first)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	second = grantCI(t, ctx, store, second)
	greenGrainCI(t, ctx, store, client, runner, first, repo, firstBranch)
	greenGrainCI(t, ctx, store, client, runner, second, repo, secondBranch)
	setMergeable(sim, true)

	syncWithCI(t, ctx, store, client, runner, baseTime)
	runBaseBranchDispatchReconciler(t, ctx, store, client, runner, baseTime.Add(time.Second))
	row, err := store.GetBaseBranchDispatch(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatal(err)
	}
	runner.finish(row.RunID, ci.ConclusionFailure, ci.JobRun{
		WorkflowPath: ".grain/workflows/image.yml", JobID: "guest-cache",
		Name: "sandbox guest cache", Conclusion: ci.ConclusionFailure,
	})
	runBaseBranchDispatchReconciler(t, ctx, store, client, runner, baseTime.Add(2*time.Second))

	row, err = store.GetBaseBranchDispatch(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatal(err)
	}
	if row.Conclusion != string(ci.ConclusionFailure) || row.FinishedAt == nil {
		t.Fatalf("the failure was recorded as %+v, want a finished, failed dispatch: an operator "+
			"asking why the cache is cold has nowhere else to read it", row)
	}

	// Nothing about the base branch changed, so nothing is held.
	ci198, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if ci198.Broken() {
		t.Fatal("a failed cache warm marked the base branch broken: it is not a verdict about the " +
			"branch, and a merge queue stopped by one would be a far larger outage than the cold " +
			"cache it was reporting")
	}
	syncWithCI(t, ctx, store, client, runner, baseTime.Add(3*time.Second))
	if !mergedBranch(sim, secondBranch) {
		t.Fatal("the queue stopped merging after a dispatch failed")
	}

	// And the same commit is not asked about again on the next cycle --
	// only a new tip is. The merge above provided one, so what is checked
	// is that there is exactly one dispatch per tree rather than one per
	// cycle.
	before := len(runner.dispatches())
	runBaseBranchDispatchReconciler(t, ctx, store, client, runner, baseTime.Add(4*time.Second))
	runBaseBranchDispatchReconciler(t, ctx, store, client, runner, baseTime.Add(5*time.Second))
	if got := len(runner.dispatches()); got != before+1 {
		t.Fatalf("%d dispatches after the failure, want %d: a failed dispatch is retried by the "+
			"branch's next merge and by nothing else", got, before+1)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.Title == "Fix grain CI on "+repo.String()+" main" {
			t.Fatal("a failed dispatch filed a repair task: an agent is what a red base branch " +
				"earns, and this is not one")
		}
	}
}

// Releases on and the same job named is the case an operator can get
// wrong, and it is allowed rather than second-guessed: grain cannot tell
// what a release workflow publishes by looking, so it cannot know the
// dispatch is a duplicate. What it must not do is make the dispatch
// depend on the release switch, which is the coupling this whole change
// undoes -- so the dispatch happens either way.
func TestTheDispatchDoesNotDependOnTheReleaseSwitch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
		PostMergeDispatchJobs: []string{guestCacheJob},
	}); err != nil {
		t.Fatal(err)
	}

	runBaseBranchDispatchReconciler(t, ctx, store, client, runner, baseTime)
	dispatched := runner.dispatches()
	if len(dispatched) != 1 || dispatched[0].Jobs[0] != guestCacheJob {
		t.Fatalf("a repository with releases on dispatched %v, want the jobs it named: whether "+
			"these run is the operator's decision, not the release switch's", dispatched)
	}
}

// A repository grain CI is not enabled for dispatches nothing, however
// its settings read: there is no run to ask for. It is the same
// composition baseBranchCIWanted makes, and it is said once rather than
// silently.
func TestARepositoryWithoutGrainCIDispatchesNothing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
		PostMergeDispatchJobs: []string{guestCacheJob},
	}); err != nil {
		t.Fatal(err)
	}

	runBaseBranchDispatchReconciler(t, ctx, store, client, runner, baseTime)
	if got := len(runner.dispatches()); got != 0 {
		t.Fatalf("%d runs were dispatched for a repository grain CI is not enabled for", got)
	}
}

// A repository that names nothing is left exactly as it was, which is
// what every repository on a deployment upgrading across this is: the
// setting does something only once somebody asks for it.
func TestARepositoryNamingNoDispatchJobsIsLeftAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
		Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}

	runBaseBranchDispatchReconciler(t, ctx, store, client, runner, baseTime)
	if got := len(runner.dispatches()); got != 0 {
		t.Fatalf("%d runs were dispatched for a repository that named none", got)
	}
	row, err := store.GetBaseBranchDispatch(ctx, repo, "main")
	if err != nil || row != nil {
		t.Fatalf("a row was written for a repository that asked for nothing: %+v (%v)", row, err)
	}
}

// And the same thing again with nobody having named anything
// (grain/task-457).
//
// Everything above is the mechanism working because an operator asked
// for it. These are about the operator who did not ask -- who switched
// releases off because the publisher was broken, and would otherwise
// discover an hour later that they had also switched off the cache every
// branch's CI reads. The whole of the coupling grain/task-397 measured
// is in that gap between the switch and the bill.
//
// grain cannot infer this for a repository in general and does not try:
// what it reads is the image its *own* sandboxes are created from, which
// names the one repository whose release workflow it knows produces a
// build input it depends on. So each test below removes one of the three
// things that make the inference safe and asserts the default goes away
// with it.

// guestImageFor is the reference a deployment running a repository's own
// published guest is built from -- the shape scripts/release-images.sh
// pushes and pkg/cli/sandboximage.go resolves.
func guestImageFor(repo model.RepoRef) string {
	return "ghcr.io/" + repo.Owner + "/" + repo.Name + "/guest:latest"
}

// releasesOffNamingNothing is the repository this is all about:
// publishing switched off, `package-publish` still granted to its CI --
// which is the state switching the release toggle leaves behind -- and
// not one word about dispatching anything after a merge.
func releasesOffNamingNothing(t *testing.T, ctx context.Context, store *model.Store,
	repo model.RepoRef) {

	t.Helper()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
		CICapabilities: []string{packagepublish.CapabilityName},
		Work:           model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}
}

// The point of the change: the cache follows main with releases off and
// nothing configured, because this deployment's own sandboxes are built
// from a guest this repository publishes.
func TestTheGuestCacheFollowsTheBaseBranchWithNobodyNamingIt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasesOffNamingNothing(t, ctx, store, repo)

	runBaseBranchDispatchReconcilerForGuest(t, ctx, store, client, runner,
		guestImageFor(repo), baseTime)
	dispatched := runner.dispatches()
	if len(dispatched) != 1 {
		t.Fatalf("grain dispatched %d runs for the repository publishing this deployment's own "+
			"sandbox guest, want one: with releases off nothing else fills the cache, and an "+
			"operator should not have to know that to keep their CI fast", len(dispatched))
	}
	req := dispatched[0]
	if len(req.Jobs) != 1 || req.Jobs[0] != guestCacheJob {
		t.Fatalf("the dispatch asks for %v, want %s alone: what grain adds unasked is the one job "+
			"that fills the cache, never the suite around it", req.Jobs, guestCacheJob)
	}
	if req.Event != ci.EventManual {
		t.Fatalf("the dispatch goes out under %q, want %q: grain naming the job widens no "+
			"capability -- a job that pushes to a registry still needs the event "+
			"packagepublish.SupportsCIEvent admits", req.Event, ci.EventManual)
	}
	if req.TrustedRelease {
		t.Fatal("the dispatch is a trusted release: filling a content-addressed cache is not " +
			"publishing, which is the whole reason it may outlive the release switch")
	}
}

// With releases on it does not, and that is not a detail: the release
// run fills the same cache on every merge, so a default that fired here
// would build the same ~1GB guest twice per merge on a miss.
func TestTheGuestCacheDefaultDoesNothingWhileReleasesArePublishing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
		CICapabilities: []string{packagepublish.CapabilityName},
	}); err != nil {
		t.Fatal(err)
	}

	runBaseBranchDispatchReconcilerForGuest(t, ctx, store, client, runner,
		guestImageFor(repo), baseTime)
	if got := len(runner.dispatches()); got != 0 {
		t.Fatalf("%d runs were dispatched for a repository that is still releasing: the release "+
			"run fills this cache itself, and doing it twice per merge is the build done twice", got)
	}
}

// A repository whose CI was never granted the credential the job needs
// gets nothing dispatched. Grain adds no grant here -- the job would
// stop on its first step for want of one -- so starting it would be a
// red run per merge saying so.
func TestTheGuestCacheDefaultNeedsTheCredentialTheJobWouldUse(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
		Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}

	runBaseBranchDispatchReconcilerForGuest(t, ctx, store, client, runner,
		guestImageFor(repo), baseTime)
	if got := len(runner.dispatches()); got != 0 {
		t.Fatalf("%d runs were dispatched for a repository whose CI holds no package-publish: "+
			"grain may not hand itself a credential an operator did not grant", got)
	}
}

// And a repository that merely has releases off, on a deployment running
// somebody else's guest, is left alone -- which is every other
// repository grain releases. The inference is about this deployment's
// own dependency, not about release workflows in general.
func TestTheGuestCacheDefaultIsOnlyForThisDeploymentsOwnGuest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasesOffNamingNothing(t, ctx, store, repo)

	runBaseBranchDispatchReconcilerForGuest(t, ctx, store, client, runner,
		"ghcr.io/other/project/guest:latest", baseTime)
	if got := len(runner.dispatches()); got != 0 {
		t.Fatalf("%d runs were dispatched for a repository that publishes no guest this "+
			"deployment runs: grain would be spending another repository's CI on a cache "+
			"nothing here reads", got)
	}
}
