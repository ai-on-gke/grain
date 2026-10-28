package model

import (
	"fmt"
	"slices"
	"strings"
)

// RepoConfig is one repo's own configuration -- the per-repo layer of
// what Config already says for the whole deployment (grain/task-24,
// following task-14's deployment-wide DefaultCapabilities and the ask
// that came with it: "we will also want this to be possible on
// individual repos in the future").
//
// One row per repo, keyed the same (owner, name) way qualification_config
// already is, and read only where a repo is already in hand: a task's own
// target, resolved by ui.CreateTask before it files anything. A task with
// no repo at all (Task.Target nil) has nothing to key on and gets the
// deployment's answer alone.
//
// # Two layers of this row, and which of them a repository writes
//
// Most of this row is an operator's, typed into grain's settings. Two
// fields of it have a second layer a repository writes for itself, in
// `.grain/settings.yml` (DeclaredPromptExtension, DeclaredSetupCommand),
// and the answer to *which* two is pkg/declschema's RepoSettingsSchema
// -- every field here is classified there, with the reason a repository
// is refused the ones it is refused.
//
// The rule in one sentence, because the list is the consequence and the
// rule is the thing: a setting is declarable when it changes what a
// repository's own work does, and never when it changes what that work
// is permitted to do, who judges it, or whether the repository is
// believed. A repository already controls the code that runs -- its
// workflows, its tests and its setup script are all in a tree every
// agent grain dispatches there can rewrite -- so nothing is protected by
// keeping a merely behavioural setting out of it. Everything else here
// is a grant of authority, a decision about whether a verdict counts, or
// the switch that says this repository is read at all, and none of those
// three is the repository's to make.
//
// This is deliberately not docs/data-model.md's folder `offers` tree,
// which is a different mechanism on purpose: an offer is a *floor* --
// unioned in when a task's grants are resolved, not droppable by the task
// -- while everything here is a *seed*, written onto the task at creation
// and untickable on the form that files it. Mixing the two silently would
// be the failure mode worth avoiding: a human unticking a capability and
// having it granted anyway, or an operator setting a floor and watching
// tasks file without it. When the folder tree arrives it composes at
// resolution, alongside these, rather than by feeding into them.
type RepoConfig struct {
	Repo RepoRef
	// CloneURL overrides the deployment's GitHub proxy for this repository.
	// Non-GitHub repositories use the sandbox's ambient git/SSH credentials.
	CloneURL string
	// DefaultCapabilities is the capability ids a task targeting this
	// repo is filed holding *in addition to* Config.DefaultCapabilities
	// -- union, in that order, resolved in
	// ui.(*Client).defaultCapabilities.
	//
	// Union is the whole composition rule, and the only one: a repo adds
	// to what the deployment defaults and can never subtract from it.
	// "Everything except gcp-key here" is the same "except here" question
	// docs/data-model.md defers for ceilings, and it gets the same answer
	// for the same reason -- a field the resolver ignores is a trap, and
	// the first person who actually needs it is a better trigger than a
	// guess. Until then, an operator who wants a capability on most repos
	// but not one of them lists it per repo rather than deployment-wide,
	// and whoever files a task can always untick it on the form.
	//
	// An id already in Config.DefaultCapabilities is stored here as given
	// rather than folded away: the two layers are chosen by different
	// people at different times, and a repo that names one explicitly
	// keeps it if the deployment-wide entry is later dropped. The union
	// is what a task is filed with either way.
	DefaultCapabilities []string
	// CICapabilities is the explicit capability set for this repo's CI jobs.
	// It is independent of task defaults and is read when a CI run starts.
	CICapabilities []string
	// Work is whether this deployment runs this repository's standing
	// work -- one default for the repository, plus per-item exceptions
	// -- and it is the *only* thing about a repository this row is meant
	// to be the home of (grain/task-486).
	//
	// Everything else here is either a setting the repository should be
	// declaring about itself or a grant this deployment makes, and the
	// two are told apart by pkg/declschema. This one is neither: it is
	// the question a repository genuinely cannot answer, because the
	// answer is a fact about the deployments looking at it. Another
	// deployment may already be cutting this repository's releases and
	// firing its schedules, and no file in the repository knows that.
	//
	// Nothing reads this field to decide whether work runs. RunsWorkItem
	// is the one function that resolves it -- default, then the override
	// for the item in hand -- and release is one item id
	// (model.ReleaseItem) rather than a case of its own, so that the
	// release becoming an ordinary schedule later is a rename.
	//
	// # What it replaced
	//
	// ReleaseWorkflowsDisabled, a single negative bool scoped to
	// releases, which callers read directly. It could say "off" and
	// could not say "on", so an operator's deliberate decision was
	// indistinguishable from a row nobody had touched -- and a sync that
	// blanked this row therefore turned publishing back on silently
	// (18:52 UTC, 20 Sep 2026, on this deployment's own repository).
	// WorkOwnership records which of the two it is holding, at both
	// levels.
	//
	// The release item says nothing about *judging* this repository's
	// base branches. That used to be the same switch in both directions:
	// with releases off grain started no base-branch run at all unless
	// BaseBranchCIJobs below named the jobs to judge with, and with
	// releases on the base-branch run *was* the release, so the
	// publisher's conclusion was the merge queue's gate and a flaky
	// publish job held every merge in the repository. Since
	// grain/task-461 the judging run is a push run of the repository's
	// whole push plan, BaseBranchCIJobs only narrows it, and the release
	// item decides only whether a merge also starts the trusted release
	// run that publishes (orchestrator.baseBranchReleaseWanted).
	//
	// # The default is the "standing work off" switch (grain/task-3332)
	//
	// Judging is an item of its own (BaseBranchCIItem), beside the
	// post-merge dispatch jobs (PostMergeItem), automatic ports
	// (PortsItem), the triage and repair tasks grain files on its own
	// (TriageItem) and every schedule whose work lands here. All of them
	// follow Default, so Default off is one switch that says "this
	// deployment runs no automated work for this repository" -- and an
	// item explicitly on beneath it still runs. What it never stops is
	// work people file by hand against the repository, their pull
	// requests' CI, the merge queue for them, and the repair of those
	// pull requests. The deployment's own configuration repository
	// (grainci.RequiredFor) is judged whatever this says.
	//
	// # It governs more than releases, and that has to be said out loud
	//
	// The name describes the event, not the consequences. Everything a
	// repository's release workflow does stops with it, including
	// whatever that workflow produces which is not a release artifact at
	// all -- and in this repository one of those is the
	// content-addressed sandbox guest cache
	// (.grain/workflows/release.yml, scripts/release-images.sh), which
	// is a *build input* every grain CI job resolving a sandbox guest
	// reads. Switched off, the cache stops following the base branch and
	// each branch's CI builds a ~1GB guest inline in a nested VM
	// instead: tens of minutes on the longest job in the suite, on every
	// branch at once (grain/task-397, docs/merge-queue-behind.md's
	// "Publishing is also a CI input").
	//
	// Grain does not infer that coupling for an arbitrary repository --
	// it cannot tell what a release workflow produces by looking, which
	// is BaseBranchCIJobs' own reason. What it does instead is report
	// the one coupling it can establish: the sandbox guest this
	// deployment resolves names the repository that publishes it, so the
	// repository pane and the sandbox health reading both say, of that
	// repository and no other, what this switch costs when it is off
	// (pkg/ui/sandbox_guest.go).
	//
	// And it takes the naming back the same way BaseBranchCIJobs does:
	// PostMergeDispatchJobs below names the jobs that follow the base
	// branch after every merge whatever this switch says, so a
	// repository whose release workflow also fills a cache its own CI
	// reads keeps the cache without keeping the release
	// (grain/task-409).
	//
	// For the guest cache in particular that is now the default rather
	// than a setting. Grain cannot read a release workflow, but it can
	// read the image its own sandboxes are built from, and that names
	// the one repository whose publishing it knows it depends on: with
	// this switch on, that repository's guest cache job is dispatched
	// after every merge whether or not anybody named it
	// (GuestCacheFollowsBaseBranch, grain/task-457). So what this switch
	// costs a deployment is the moving tags it is named for, and not the
	// build input that used to travel with them.
	Work WorkOwnership
	// BaseBranchCIJobs narrows the run that judges this repository's base
	// branches -- the one that says whether `main` is sound and files a
	// repair when it is not (orchestrator.reconcileBaseBranchCI),
	// separated from the publishing it used to be bundled with
	// (grain/task-330, 461). It stopped being a gate on merging in
	// grain/task-471, so what this field narrows is what grain *learns*
	// and what it spends learning it, not what may land.
	//
	// # Why it exists
	//
	// A base-branch run *was* the trusted release event: the same
	// request, under ci.EventRelease, that a merge emits. So a repository
	// with ReleaseWorkflowsDisabled lost the gate along with the
	// publishing -- an operator switching releases off because a publish
	// job was broken silently stopped `main` being tested at all, and the
	// row holding a red base branch could then never be refreshed either,
	// because nothing would start another run for it.
	//
	// Asking under ci.EventPush is what takes them apart: no trusted
	// release, no release secrets, nothing published, and the tests run.
	// What this field does is say which jobs of that push plan are worth
	// paying for -- on this deployment's own repository `tests.yml`
	// declares seven jobs and three of them would carry the gate in a
	// fraction of the wall-clock.
	//
	// # Read whether or not releases are on (grain/task-461)
	//
	// It used to be consulted exactly when ReleaseWorkflowsDisabled was
	// set, because with releases on the base-branch run was the
	// publishing as well as the judging and narrowing it to the test jobs
	// would have stopped the repository releasing.
	//
	// That is no longer the shape. Every base branch is judged by a push
	// run and the publishing is a separate run at the same tip
	// (orchestrator.baseBranchReleaseWanted), so narrowing the judging
	// run cannot take a repository's releases away and there is nothing
	// left to condition this on. It is a cost control over the push plan,
	// always.
	//
	// What judges a repository grain *does* release is therefore this
	// field and the repository's push plan, exactly as for one it does
	// not. The rule it replaces was "the jobs that judge a branch answer
	// the event the judging run is asked for", which with releases on
	// meant a repository writing `release` into its own workflow's `on:`
	// block (grain/task-355) -- sound about the mechanism, wrong about
	// which run should be the verdict, and reversed by an operator's
	// ruling that release should not be the gate.
	// docs/merge-queue-behind.md's "Releases on: the judging jobs answer
	// `release`" has that history and the alternatives weighed on the way
	// through it.
	//
	// # Empty means "everything a push would run" (grain/task-458)
	//
	// It used to be a refusal: releases off and nothing named here and
	// grain started no run at all, on the argument that it cannot tell a
	// publishing job from a judging one by looking, so "everything" is
	// not a safe reading of a repository that has asked not to publish.
	//
	// That argument is sound for one shape of repository and was wrong as
	// a default. An Actions-backed workflow triggered `on: push` to
	// `main` really can carry its publish job in the very plan a push run
	// would run, where it fails for want of secrets rather than being
	// skipped -- and a repository in that shape names its judging jobs
	// here, which is one field and exactly what this field is for. But
	// the unset case is what every other repository falls into by
	// accident, and what it cost is on the record: releases were switched
	// off on bwsalmon/grain on 19 Sep 2026 because a publish job was
	// flaky, this field was empty, and `main` went unjudged for 33 hours
	// across roughly 100 merges -- with RequireBranchUpToDate off, so the
	// one control that catches two branches green apart and broken
	// together was gone, the queue went on merging onto an unjudged tree,
	// and nothing on any surface said so. It was found by somebody
	// reading `git log` on the configuration repository.
	//
	// A switch named for releases must not take the tests with it. So the
	// unset case now falls toward a gate that may be wrong for an unusual
	// repository rather than toward no gate at all for every repository:
	// grain judges the branch under ci.EventPush with the whole push
	// plan, and this field narrows it where an operator has said how
	// (orchestrator.baseBranchCIWanted).
	//
	// That closes the route, and the other half of what went wrong is
	// that nothing said so for 33 hours: the refusal was logged once a
	// cycle and appeared on no surface a person or an agent reads
	// (grain/task-453). Which branch is judged is now a reading rather
	// than only a setting -- ui.BaseBranchGate names a base branch
	// nothing is judging, says the merge queue is merging onto it
	// anyway, and says when a run last started for it. It deliberately
	// does *not* key on this field being empty, because empty no longer
	// means anything is wrong; what it watches for is a branch that has
	// stopped having runs started for it, whatever the reason, which is
	// the fact the incident needed and which does not depend on anyone
	// having predicted the cause.
	//
	// Job ids as ci.Request.Jobs takes them: a bare `go` where it is
	// unique across the matching workflows, and the plan-wide
	// "<workflow path>:<job id>" where it is not. A name that matches no
	// job in the push plan is an error the run cannot be started from,
	// logged at the moment it happens.
	BaseBranchCIJobs []string
	// PostMergeDispatchJobs names the jobs grain dispatches on a base
	// branch once it has moved -- after every merge, and whatever this
	// repository's releases are doing (grain/task-409).
	//
	// # What it is for
	//
	// The thing a release workflow produces that is neither a release
	// artifact nor a verdict, and that every branch's CI then needs. In
	// this repository that is the content-addressed sandbox guest cache
	// (.grain/workflows/release.yml, scripts/release-images.sh): a
	// build input, not a release, and
	// ReleaseWorkflowsDisabled's own doc comment has what it costs when
	// it stops following `main` -- a ~1GB guest built inline in a nested
	// VM on every branch whose inputs are not already cached. Naming
	// `.grain/workflows/image.yml:guest-cache` here is what makes the
	// cache follow the branch with releases switched off, which is the
	// coupling grain/task-397 measured and this takes apart.
	//
	// That case no longer needs naming on the deployment it was written
	// for: grain adds the same job for the repository publishing the
	// sandbox guest it is itself running, while that repository's
	// releases are off (GuestCacheFollowsBaseBranch, grain/task-457).
	// This field is what every *other* repository, and every other
	// build input, still asks with -- and naming the guest cache job
	// here where grain would have added it anyway costs nothing, since
	// the two are merged rather than concatenated.
	//
	// It is the same shape as BaseBranchCIJobs above and the opposite
	// half of the same problem: that one keeps the *judging* when the
	// publishing is off, this one keeps a particular piece of the
	// *publishing*, named job by job by the operator who knows which
	// piece is a build input.
	//
	// # Under which event, and why that is not a loosening
	//
	// ci.EventManual -- `workflow_dispatch`. A job that fills a cache
	// pushes to a registry, so it needs the package-publish capability,
	// and that capability admits its material only to a release or a
	// dispatch (pkg/capability/packagepublish.SupportsCIEvent) --
	// never to a push. The gate is what keeps a repository-writing
	// credential off an untrusted branch push, so it is honoured rather
	// than widened: an operator naming a job in operator-owned
	// repository configuration *is* the operator asking for that
	// dispatch, which is exactly what the event already means. It stays
	// narrow, too -- these named jobs, not "whatever the release plan
	// would have run".
	//
	// Said plainly because it follows from the event rather than from
	// anything here: a dispatch also carries this repository's
	// ReleaseSecrets, which ci.Service.Start places for a release and a
	// dispatch alike. A job named here is as trusted as one an operator
	// dispatches by hand, which is the whole basis on which it runs at
	// all -- so name jobs, not workflows, and name the ones that fill
	// caches rather than the ones that ship.
	//
	// # What it is not
	//
	// Not a verdict. Nothing dispatched from here can hold the merge
	// queue: it is recorded in BaseBranchDispatch, which the queue does
	// not read, and a failure is a cache that stayed cold -- a slower CI
	// job on some later branch -- rather than a red base branch. Nothing
	// is retried for the commit it failed on either; the branch's next
	// merge asks again.
	//
	// Not deduplicated against the release run, either, because grain
	// cannot tell what a release workflow publishes by looking -- the
	// reason BaseBranchCIJobs is a list rather than an inference. A
	// repository whose releases are on *and* which names the same job
	// here has it run twice per merge, which on a warm cache is a
	// `docker pull` and on a cold one is the build done twice. An
	// operator who does not want that names the job here only while it
	// is the release run that is not running.
	//
	// # It is also what grain warms a branch's guest cache with
	//
	// The one case the paragraphs above cannot reach is the branch that
	// changes a guest input: its key does not exist in the registry until
	// it lands, so no dispatch on the base branch has filled it and every
	// job on that branch builds its own. Since grain/task-410 grain
	// dispatches these same jobs on *that branch's* commit when it plans
	// a job that resolves a guest nothing has published, once per set of
	// guest inputs, with every job that needs the same guest waiting for
	// that one run (pkg/grit/cache/guestcache.go).
	//
	// Said plainly, because it is the one consequence of naming a job
	// here that does not follow from the paragraphs above: the run grain
	// dispatches then executes *the branch's* copy of the named job, with
	// that dispatch's publishing material, and what it publishes under
	// the content-addressed key is the image the base branch will pull
	// once the branch merges. .grain/workflows/image.yml's own comment
	// calls that a vouching for the branch's guest inputs, and until this
	// existed it took an operator typing `grain ci run -job ...` per
	// branch. A repository that names nothing here is warmed for nothing,
	// which is the behaviour every deployment had before.
	//
	// Job ids as ci.Request.Jobs takes them, BaseBranchCIJobs' own
	// spelling: a bare id where it is unique across the workflows that
	// answer `workflow_dispatch`, and "<workflow path>:<job id>" where
	// it is not.
	PostMergeDispatchJobs []string
	// ReleaseSecrets names the write-only, repository-scoped values made available
	// as mode-0600 files only to trusted release and manually dispatched CI runs.
	// Values live in the encrypted secrets store; this slice is non-secret metadata
	// that tells a job which names it may resolve.
	ReleaseSecrets []string
	// PromptExtension is this repo's own standing instructions for a run
	// working in it, appended *after* Config.PromptExtension for a task
	// that targets this repo -- prompt_extension.go's own doc comment has
	// the composition rule and why it is an append rather than a
	// replacement.
	//
	// Read at dispatch rather than seeded onto the task, unlike
	// DefaultCapabilities above (Config.PromptExtension has why): a task
	// filed today against a repo whose conventions are written down
	// tomorrow is told about them when it runs.
	PromptExtension string
	// SetupCommand is the shell this repo needs run once, in the fresh
	// checkout, before a run's first turn: `make deps`, an `npm ci`, a
	// virtualenv, whatever generates the file the tests will not build
	// without. orchestrator.prepareCheckout runs it after the clone and
	// puts its exit status and a tail of its output into the prompt.
	//
	// A repo without one is the normal case and says nothing here. Prose
	// in PromptExtension above is what a repo had to use instead, and it
	// is a poor substitute: an instruction is something each run has to
	// spend a turn obeying, and one that is not obeyed first is a run
	// that has already read the wrong failure out of a tree that does
	// not build.
	//
	// Read at dispatch, like PromptExtension and for the same reason --
	// a repo whose toolchain changes tomorrow changes it for every task
	// already filed against it, not only for the ones filed after.
	//
	// It is arbitrary shell, run in the sandbox with the same
	// run_command tool the agent's own commands go through. That grants
	// nothing new: the agent runs arbitrary shell in that sandbox
	// anyway, and this is somebody with access to grain's settings
	// choosing what runs there first.
	//
	// That last paragraph is also why this is one of the two settings a
	// repository may declare for itself: a command that grants nothing
	// new is one it makes no difference to keep out of the repository.
	// The repository's own is DeclaredSetupCommand below and both run,
	// this one first (SetupScript).
	SetupCommand string
	// DeclaredPromptExtension and DeclaredSetupCommand are the same two
	// settings as written by the *repository*, in `.grain/settings.yml`,
	// and read back by pkg/repodecl (grain/task-340).
	//
	// # Why they are columns of their own
	//
	// Because the two layers are written by two parties who must not
	// overwrite one another. The fields above are settings boxes a person
	// maintains, and model.Memory's doc comment is careful that such a
	// box "must not be silently rewritten by an agent" -- which is what
	// folding a declared value into them would be, in a file every agent
	// grain dispatches to that repository can edit. Kept apart, the
	// declaration sync owns these two and the settings UI owns those two,
	// an operator can always see which half came from where, and turning
	// the Declarations switch off restores exactly the row an operator
	// wrote.
	//
	// It is also what makes a re-read idempotent: one column holding
	// "operator's text, then the repository's" would append the
	// repository's text to itself on every poll of an unchanged file.
	//
	// # They add and never subtract
	//
	// Composed by RepoPromptExtension and SetupScript below, in that
	// order and by appending. A repository can add standing instructions
	// and add setup steps; it cannot discard what the deployment or an
	// operator said, and there is no spelling here that means "ignore the
	// operator's". That is the same rule DefaultCapabilities,
	// Config.PromptExtension and Config.IgnoreGitHubCI already hold to,
	// for the same reason: a layer able to subtract is a layer able to
	// switch off a control somebody else turned on.
	DeclaredPromptExtension string
	DeclaredSetupCommand    string
	// PortBranches names a repository's paired branches -- for
	// bwsalmon/grain, `main` and `candidate` -- whose merges are each
	// ported to the others: when the merge queue lands a task's pull
	// request on one of them, grain files a port task carrying the same
	// change onto every other one (orchestrator's port.go). Empty, the
	// default, is a repository whose merges stay where they landed.
	//
	// DeclaredPortBranches is the same list as the repository writes it,
	// `port-branches` in `.grain/settings.yml`. The two are unioned
	// (PortTargets) rather than either replacing the other, for the rule
	// the two declared fields above follow: a repository's own layer adds
	// and never subtracts. A port grants nothing new either -- it is the
	// same change, under the same team and capabilities, that somebody
	// already approved, landing through the same merge queue and CI.
	PortBranches         []string
	DeclaredPortBranches []string
	// PortTypes narrows, per direction, which merges are ported: keyed
	// "<from>-><to>" (PortDirection), each names the conventional-commit
	// types (`fix`, `revert`, ...) a change merged onto from must carry
	// to be ported onto to. A direction with no entry ports everything,
	// which is every direction of a repository that says nothing.
	//
	// It is how a release branch cut from another only receives fixes:
	// bwsalmon/grain ports main -> candidate for `fix` and `revert` only,
	// because the daily release cut carries everything else across, and
	// still ports every merge candidate -> main.
	//
	// DeclaredPortTypes is `port-types` in `.grain/settings.yml`. The two
	// compose in PortTypeFilter, where the repository's layer only ever
	// narrows a direction between branches it paired itself.
	PortTypes         map[string][]string
	DeclaredPortTypes map[string][]string
	// Declarations turns on reading this repository's own `.grain`
	// directory for the templates, suites and schedules it declares
	// (repo_declaration.go, grain/task-60). Off by default, and off is
	// the state every deployment was in before the field existed.
	//
	// It is a switch rather than something that simply happens for every
	// repository grain works in, because of what a declaration costs and
	// who writes one. A `.grain/schedules` file is a standing claim on
	// this deployment's agent capacity, written in a repository whose
	// files every run grain dispatches there can rewrite -- so somebody
	// with access to grain's own settings decides once, per repository,
	// that this repository's own word is good enough for that. What a
	// declaration may then say is bounded separately and much more
	// tightly (repo_declaration.go's "What a declaration is, and what it
	// is emphatically not"): the answer to "what may this task touch" is
	// never the repository's, switch or no switch.
	Declarations bool
	// RequireBranchUpToDate makes the merge queue insist that a branch
	// contain its base before it merges -- GitHub's own "Require branches
	// to be up to date before merging", which most repositories leave
	// switched off and which this field is switched off for by default.
	//
	// Off (the default, and the zero value) is the cheap behaviour: a
	// branch that is behind its base but merges cleanly is merged as it
	// stands, on the full-suite verdict it already holds. The required
	// verdict itself does not move an inch either way --
	// orchestrator.grainCIVerdictOf still demands a successful run
	// against the branch's *exact* head -- what changes is that other
	// people's merges stop invalidating that head. A queue that brings
	// every branch up to date as the base moves re-tests all of them on
	// every merge: N open branches and N merges cost on the order of N²
	// full suites, which on this deployment was four twenty-minute suites
	// for one task in ninety minutes (grain/task-198).
	//
	// On is for a repository that would rather spend those suites than
	// merge a change that was never tested against the tree it lands on.
	// The queue then merges the base into a clean-but-behind head itself,
	// one server-side write, and waits for CI on the commit that makes --
	// the same write it already makes to refresh a broken head
	// (orchestrator.refreshStaleHead).
	//
	// What makes the default safe is not that two green branches cannot
	// break each other -- they can, and that is exactly what an
	// up-to-date rule buys -- but that the conflict is caught before
	// anything lands and reported afterwards if it lands anyway.
	//
	// Caught: the merge queue checks whether the change's diff overlaps
	// what has landed since its tested tree, and re-tests it on a
	// throwaway ref when it does (MergeRecheck, grain/task-985, 1023),
	// so the pair that is green apart and broken together fails on a
	// throwaway ref with nothing having reached the base branch at all.
	//
	// Reported: grain runs its own CI on the base branch after every merge
	// (orchestrator.reconcileBaseBranchCI), which is how a red branch is
	// noticed and how a repair is filed for it. That run used to *hold*
	// the queue as well, and this doc used to name the hold as the
	// compensating control; it no longer does either (grain/task-471,
	// orchestrator's merge predicate). A gate whose own failure blocks the
	// only route to fixing it is a deadlock, not a control -- see
	// docs/merge-queue-behind.md for the reasoning that was weighed and
	// what replaced it.
	//
	// Only one of the three costs a full suite per branch per merge, and
	// it is the strict rule.
	RequireBranchUpToDate bool
	// WatchDefaultBranch asks grain to keep its own CI's verdict about
	// this repository's default branch current whether or not grain has
	// ever merged anything into it (grain/task-331). Off by default, and
	// off is the state every deployment was in before the field existed.
	//
	// # What it closes
	//
	// There is one base_branch_ci row per (repository, base branch), and
	// until this the only thing that ever created one was grain
	// performing a merge (orchestrator.recordPostMergeCI). Since
	// grain/task-198 a *tracked* branch is re-tested whenever its tip
	// moves, however it moved -- but a repository whose pull requests are
	// all merged by hand, or one grain has not merged into yet, has no
	// row, so nothing tracks it and its `main` can be red with the queue
	// merging onto it exactly as before. Set here, grain resolves the
	// repository's default branch (github.Client.DefaultBranch) and seeds
	// the row itself, after which the ordinary tip-watching applies.
	//
	// # Why a switch rather than every repository grain CI is on for
	//
	// Because the first cycle after it is turned on starts a full suite
	// on that branch, and a branch that turns out to be red earns a
	// repair task (it used to hold that repository's merge queue too,
	// until grain/task-471). That is the control working as designed, and
	// it is still a bill: doing
	// it for every repository with a config row would spend N suites at
	// once and file N tasks on the strength of a deployment upgrade
	// rather than of anybody's decision -- and the set of repositories
	// with a config row is not the set whose default branch anybody
	// wants suites spent on. So the repositories are named, one tick at a
	// time, by whoever is paying for the CI.
	//
	// It composes with the CI mode rather than replacing it: a
	// repository grain CI is not enabled for (RepoUsesGrainCI, or
	// grainci.RequiredFor) is not seeded however this reads, because
	// there would be no suite to run. ReleaseWorkflowsDisabled does not
	// suppress it -- it decides which event the seeded branch is judged
	// under and nothing more (grain/task-458):
	// orchestrator.baseBranchCIWanted is the one place both are asked.
	WatchDefaultBranch bool
	// MergeOnGreen lets the merge queue merge a pull request here as soon
	// as it is green on its required checks and merges cleanly with its
	// target branch, rather than waiting its turn behind entries ahead of
	// it in the lane whose checks are still running (grain/task-545). Off
	// by default, and off is the strict first-in-first-out lane every
	// repository had before the field existed.
	//
	// Set, an entry ahead in the lane whose CI has not finished
	// (PrPending) no longer closes the lane: it keeps its own place and
	// its waiting_ci stage, and a healthy entry behind it takes the open
	// slot (orchestrator's mergeWalk). Every other gate still
	// applies -- a hold, a red or conflicted entry ahead, the strict
	// up-to-date rule -- so what this trades is order, never a check.
	MergeOnGreen bool
	// BaseRepairBypassesHumanGates lets a base-branch repair pull request
	// here merge on green CI while its target base branch is red, without
	// waiting for a human on sensitive path triggers (such as
	// .grain/workflows/**) or trust-boundary paths (grain/task-401). Off by
	// default unless enabled for this repository.
	BaseRepairBypassesHumanGates bool
	// MergeRecheck is how the merge queue's overlap re-check is applied
	// here (grain/task-985, 1023, docs/merge-queue-behind.md):
	// MergeRecheckOff, MergeRecheckObserve or MergeRecheckEnforce. Empty
	// is enforce -- read it through MergeRecheckModeOf, which fills that
	// in -- so a repository nobody has configured re-checks changes whose
	// diff overlaps what has landed since their tested tree and merges
	// non-overlapping changes on their own green.
	MergeRecheck MergeRecheckMode
	// IgnoreGitHubCI declares that GitHub's check runs are not a source
	// of verdicts for this repository at all: grain CI
	// (.grain/workflows) is its gate. Off by default, and off is the
	// state every deployment was in before the field existed.
	//
	// Set, nothing about a pull request here is taken from GitHub's
	// checks. They are not read (forge.Observe makes no Checks call for
	// such a repository at all), not counted in health, not waited on,
	// not timed by either of the clocks the queue runs on CI, and not
	// named in any sentence the queue or a run's own CI tools write --
	// pull_request_status, base_branch_status and the merge queue's
	// repair comment say the gate is grain CI instead of listing reds an
	// agent then has to explain away.
	//
	// # It is not an exemption, which is what makes it acceptable
	//
	// docs/merge-queue-behind.md's "The option not taken" rejects a
	// per-repository list of base-branch jobs whose failure is not acted
	// on, because that is a standing decision to ignore a verdict: a
	// real regression in a listed job reaches the base branch
	// unremarked, and the list is exactly as stale as whoever last
	// edited it. That reasoning is right, and it is about a different
	// thing.
	//
	// This is a statement about a *provider* rather than about a job: it
	// says which CI system judges this repository, which is either true
	// or false about how the deployment is configured, not a judgement
	// about a known-bad test that rots. Nothing is hidden by it because
	// nothing is reported -- the reads are not made. The section of that
	// document named for this field has the distinction in full, and the
	// state that forced it: GitHub Actions blocked at the account level,
	// every workflow run on every branch rejected before its first step,
	// for days, while grain went on reading and reporting the checks.
	//
	// # It swaps the gate rather than removing it
	//
	// The one thing this must not become is a way to merge with no
	// verdict at all. So the declaration makes grain CI *required*, by
	// the repository's word rather than by the task's grant
	// (orchestrator.grainCIVerdictOf, forge.Input.IgnoreGitHubCI). With
	// GitHub's checks unread, an empty settled check list would
	// otherwise read PrClean and merge on the strength of nobody having
	// looked; instead a commit with no grain CI verdict reads PrPending
	// and reaches the ordinary stall deadline and a person.
	//
	// That also settles which tasks grain CI judges: since 57939261f
	// setting this (or CIMode "grain") is what grants grain-ci to the
	// repository's tasks (RepoUsesGrainCI), so there is no longer a
	// repository that turns this on without its tasks holding grain-ci.
	// What can still leave a commit unjudged is a repository with no
	// .grain/workflows of its own: it merges *nothing* rather than
	// everything, and an operator whose provider has just died is
	// allowed the switch before the replacement is wired up, not after.
	//
	// Composed with Config.IgnoreGitHubCI by union, like
	// DefaultCapabilities and for the same reason: a deployment that has
	// said GitHub is not a verdict source has said it for every
	// repository under it -- an unpaid Actions bill stops an account,
	// not a repository -- and a repo layer that could subtract would let
	// a stale row re-enable a provider the deployment has already
	// declared dead.
	IgnoreGitHubCI bool
	// UnfixableChecks names this repository's checks that will never pass
	// for a pull request grain's agents author, whatever is pushed to the
	// branch. A CLA bot is the case it was written for: restoring an
	// account's Actions budget clears every check that failed because no
	// job could start, and leaves `cla/google` red on every agent-authored
	// pull request in the repository, permanently, because what it
	// asserts is about who signed an agreement rather than about the tree
	// (grain/task-291).
	//
	// # What it does, and the one thing it deliberately does not
	//
	// It does not exempt the check. A pull request with one of these red
	// is still not merged -- forge.HealthFrom reads it exactly as it
	// always did, the queue waits, and a person clears it or nothing
	// merges. What the declaration changes is only what grain *spends*
	// getting there: a repair run dispatched to fix a check that cannot
	// pass is a run that could not have succeeded, and the base merge
	// before it is a write made to reach a green that is not reachable.
	// Named here, those moves are not made and a person is told in
	// seconds instead (orchestrator's forge.Reds, escalateUnfixable).
	//
	// That is the distinction docs/merge-queue-behind.md's "The option
	// not taken" turns on, and it is why this is not the per-repository
	// ignore list that document rejects. The objection there is that
	// listing a job whose failure is not acted on lets a real
	// regression in it reach the base branch unremarked, and that the
	// list is exactly as stale as whoever last edited it. Neither applies
	// to a list that holds the queue just as hard as before: a stale
	// entry here costs a repair that would have been spent anyway on a
	// check that had started passing again, and the queue's own next
	// cycle reads that green and merges.
	//
	// Exact check names, matched case-insensitively and never by
	// substring (forge.Unfixable.Covers). grain CI's own verdict cannot
	// be named here -- it judges one exact commit on the branch, so it is
	// always the branch's own answer, and a deployment able to declare
	// its own runner unfixable would have built a way to merge with no
	// verdict at all.
	UnfixableChecks []string

	// CIMode is the single-select CI mode configured for this repository:
	// "grain" (Use Grain CI), "github" (Use GitHub CI), or "none" (Use No CI).
	CIMode CIMode

	// RepairTemplateID is the ID of the template used to file repair tasks for this repository.
	RepairTemplateID string

	// GitCredential is the credential this repository is cloned and
	// pushed with, ahead of the deployment's git connections
	// (Store.GitConnectionOn). Keyed by the repository's host as well as
	// its owner/name (RepoRef.StoredOwner), so it applies on that host
	// only. It was the default team's team_repo_config.git_credential
	// until schema 23 folded that table in.
	GitCredential string
}

// CIMode names the CI verification mode for a repository.
type CIMode string

const (
	// CIModeGrain requires Grain CI (.grain/workflows) and ignores external GitHub check runs.
	CIModeGrain CIMode = "grain"
	// CIModeGitHub uses external GitHub Actions / check runs to gate PRs.
	CIModeGitHub CIMode = "github"
	// CIModeNone bypasses CI verification for merges in this repository.
	CIModeNone CIMode = "none"
)

// EffectiveCIMode returns the repository's effective CI mode, accounting for legacy IgnoreGitHubCI.
func (c RepoConfig) EffectiveCIMode() CIMode {
	if c.CIMode != "" {
		return c.CIMode
	}
	if c.IgnoreGitHubCI {
		return CIModeGrain
	}
	return CIModeGitHub
}

// RepoUsesGrainCI reports whether the repository is configured to use Grain CI.
// It checks whether repo is the controller-local config repo, or if repo
// configuration enables Grain CI (via CIMode or IgnoreGitHubCI).
func RepoUsesGrainCI(repoCfg *RepoConfig, repo RepoRef) bool {
	if IsConfigRepo(repo) {
		return true
	}
	if repoCfg != nil && repoCfg.CIMode != "" {
		return repoCfg.CIMode == CIModeGrain
	}
	if repoCfg != nil && repoCfg.IgnoreGitHubCI {
		return true
	}
	return false
}

// Empty reports whether this config says nothing at all -- the state a
// repo with no row is in. PutRepoConfig deletes rather than writes such a
// row, so "has a row" and "has something of its own to say" stay the same
// fact and ListRepoConfigs never returns a repo that adds nothing. A
// further field here (base and max_concurrent are docs/data-model.md's
// own remaining two) gains a term in this method and nothing else has to
// change -- PromptExtension, the second, and SetupCommand, the third,
// are exactly that.
func (c RepoConfig) Empty() bool {
	return c.CloneURL == "" && len(c.DefaultCapabilities) == 0 && len(c.CICapabilities) == 0 &&
		c.Work.Empty() && len(c.ReleaseSecrets) == 0 &&
		len(c.BaseBranchCIJobs) == 0 && len(c.PostMergeDispatchJobs) == 0 &&
		c.PromptExtension == "" && c.SetupCommand == "" && !c.Declarations &&
		!c.RequireBranchUpToDate && !c.WatchDefaultBranch && !c.IgnoreGitHubCI && !c.MergeOnGreen && !c.BaseRepairBypassesHumanGates &&
		c.MergeRecheck == "" &&
		c.CIMode == "" &&
		len(c.UnfixableChecks) == 0 &&
		c.DeclaredPromptExtension == "" && c.DeclaredSetupCommand == "" &&
		c.RepairTemplateID == "" && len(c.PortBranches) == 0 && len(c.DeclaredPortBranches) == 0 &&
		len(c.PortTypes) == 0 && len(c.DeclaredPortTypes) == 0 &&
		c.GitCredential == ""
}

// RepoPromptExtension is this repository's whole layer of the prompt
// extension: what an operator typed into grain's settings for it, then
// what the repository declared for itself, appended.
//
// It is the `repo` argument PromptExtensionFor takes, which is what
// makes the repository's own text the third of four layers rather than a
// fourth composition rule nobody would find: deployment, then operator's
// repo layer, then the repository's own, and a task's override still
// replacing all three.
//
// The order is the rule. An operator's text comes first because it is
// the one a person maintains and the one a run should read first if it
// reads no further, and the repository's is appended because a layer
// that could come first -- or replace -- would be an agent-writable file
// deciding what a person's instructions mean.
func (c RepoConfig) RepoPromptExtension() string {
	return joinTrimmed("\n\n", c.PromptExtension, c.DeclaredPromptExtension)
}

// SetupScript is the shell orchestrator.prepareCheckout runs in a fresh
// checkout: the operator's setup command, then the repository's declared
// one, both of them, in that order.
//
// Both rather than either, for the reason the append exists at all: a
// repository must not be able to discard the setup an operator decided
// this repository needs. A declaration that could replace it would be
// the one shell command grain runs before an agent's first turn, chosen
// by a file that agent can rewrite.
//
// Joined as two lines of one script, which is the shape runSetupCommand
// already builds (it prepends its own `cd`) and the shape a multi-line
// setup command already has. `set -e` is deliberately not applied over
// the join, for the same reason it is not applied within one command:
// what a failing first half should do is whoever wrote it's to say, and
// a repository whose declared step must not run after a failed operator
// step writes that condition into its own script.
func (c RepoConfig) SetupScript() string {
	return joinTrimmed("\n", c.SetupCommand, c.DeclaredSetupCommand)
}

// JoinTrimmed is the whole of how a repository's declared layer folds
// into an operator's: every layer trimmed, empty layers dropped, and
// nothing anywhere that lets a later layer remove an earlier one.
func JoinTrimmed(sep string, layers ...string) string {
	var parts []string
	for _, layer := range layers {
		if v := strings.TrimSpace(layer); v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, sep)
}

func joinTrimmed(sep string, layers ...string) string {
	return JoinTrimmed(sep, layers...)
}

// PairedBranches is the whole set of branches this repository ports
// between: the operator's PortBranches and the repository's own
// DeclaredPortBranches, unioned, trimmed and without repeats, in the
// order they were first named.
func (c RepoConfig) PairedBranches() []string {
	var out []string
	for _, b := range append(slices.Clone(c.PortBranches), c.DeclaredPortBranches...) {
		b = strings.TrimSpace(b)
		if b != "" && !slices.Contains(out, b) {
			out = append(out, b)
		}
	}
	return out
}

// PortTargets is where a merge onto branch is ported to: every other
// paired branch, or nothing when branch is not one of them -- which is
// every branch of a repository that pairs none, and a feature branch of
// one that does.
func (c RepoConfig) PortTargets(branch string) []string {
	paired := c.PairedBranches()
	if len(paired) < 2 || !slices.Contains(paired, branch) {
		return nil
	}
	var out []string
	for _, b := range paired {
		if b != branch {
			out = append(out, b)
		}
	}
	return out
}

// PortDirection is the key PortTypes and DeclaredPortTypes file a
// direction under: "main->candidate".
func PortDirection(from, to string) string { return from + "->" + to }

// PortTypeFilter is the conventional-commit types a merge onto from has
// to carry to be ported onto to, or nil when every merge is ported.
//
// The repository's own layer adds and never subtracts (DeclaredPortBranches
// has why), so where the operator pairs both branches itself, only the
// operator's PortTypes can narrow the direction, and a declared list can
// only widen one the operator narrowed. Where the pair is the
// repository's own declaration, the repository narrows its own ports, and
// an operator list for the same direction is unioned with it.
func (c RepoConfig) PortTypeFilter(from, to string) []string {
	dir := PortDirection(from, to)
	operator, declared := c.PortTypes[dir], c.DeclaredPortTypes[dir]
	operatorPaired := slices.Contains(c.PortBranches, from) && slices.Contains(c.PortBranches, to)
	switch {
	case operatorPaired && len(operator) == 0:
		return nil
	case len(operator) == 0 && len(declared) == 0:
		return nil
	}
	var out []string
	for _, t := range append(slices.Clone(operator), declared...) {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// PortTypeDirections is every direction either layer's PortTypes names
// plus every direction between paired branches, sorted, each once: what
// a reader needs in front of them to see which filters are in force.
func (c RepoConfig) PortTypeDirections() [][2]string {
	seen := map[string]bool{}
	var out [][2]string
	add := func(from, to string) {
		if dir := PortDirection(from, to); !seen[dir] {
			seen[dir] = true
			out = append(out, [2]string{from, to})
		}
	}
	paired := c.PairedBranches()
	if len(paired) >= 2 {
		for _, from := range paired {
			for _, to := range paired {
				if from != to {
					add(from, to)
				}
			}
		}
	}
	for _, layer := range []map[string][]string{c.PortTypes, c.DeclaredPortTypes} {
		for dir := range layer {
			if from, to, ok := strings.Cut(dir, "->"); ok {
				add(from, to)
			}
		}
	}
	slices.SortFunc(out, func(a, b [2]string) int {
		return strings.Compare(PortDirection(a[0], a[1]), PortDirection(b[0], b[1]))
	})
	return out
}

// DeclaredPortTypesWarningPrefix begins each PortTypesWarnings entry
// about the repository's own `port-types`, as against the operator's.
const DeclaredPortTypesWarningPrefix = "port-types in .grain/settings.yml"

// PortTypesWarnings is every `port-types` entry that has no effect, one
// sentence each, sorted: a direction naming a branch that is not paired
// (a typo such as main->candidtae, or a release branch nobody paired),
// which nothing will ever port along, and a declared direction between
// branches the operator pairs with no list of its own, which
// PortTypeFilter ignores because a repository cannot narrow an
// operator's pair. Both are accepted, since neither grants anything,
// and both used to be accepted without a word -- while the direction
// somebody meant to filter kept porting every merge.
func (c RepoConfig) PortTypesWarnings() []string {
	paired := c.PairedBranches()
	var out []string
	check := func(layer string, declared bool, types map[string][]string) {
		for dir := range types {
			from, to, ok := strings.Cut(dir, "->")
			if !ok {
				continue
			}
			var unpaired []string
			for _, b := range []string{from, to} {
				if !slices.Contains(paired, b) {
					unpaired = append(unpaired, b)
				}
			}
			switch {
			case len(unpaired) > 0:
				pairs := "no branches are paired"
				if len(paired) > 0 {
					pairs = "the paired branches are " + strings.Join(paired, ", ")
				}
				out = append(out, fmt.Sprintf("%s: %s names %s, which is not a paired branch (%s), "+
					"so the entry has no effect", layer, dir, strings.Join(unpaired, " and "), pairs))
			case declared && len(c.PortTypes[dir]) == 0 &&
				slices.Contains(c.PortBranches, from) && slices.Contains(c.PortBranches, to):
				out = append(out, fmt.Sprintf("%s: %s is ignored: the operator pairs %s and %s "+
					"with no port-types list for this direction, so every merge is ported -- a "+
					"repository's list can widen an operator's list but never narrow an operator's pair",
					layer, dir, from, to))
			}
		}
	}
	check("the operator's port-types", false, c.PortTypes)
	check(DeclaredPortTypesWarningPrefix, true, c.DeclaredPortTypes)
	slices.Sort(out)
	return out
}
