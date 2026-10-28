package workflows

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/grit/actions"
	"gopkg.in/yaml.v3"
)

// WorkflowDir is where a repository declares its grain workflows: one
// YAML file per workflow, exactly as `.github/workflows` holds one file
// per GitHub workflow. The directory is `.grain` rather than `.github`
// so a repository can carry both without either reading the other's
// files, and `workflows` under it so `.grain` has room for whatever
// else a repository eventually declares to grain.
const WorkflowDir = ".grain/workflows"

// GitHubWorkflowDir is consulted when a repository's .grain/workflows
// declares no workflow of its own -- because the directory is not there,
// or because nothing in it is a workflow file (LoadDir).
const GitHubWorkflowDir = ".github/workflows"

// RunnerLabel is the one value `runs-on` may take: grain's own sandbox,
// whichever backend the deployment builds those out of (a directory on
// the controller, or a kontur-managed VM -- orchestrator.Sandboxes).
//
// A single legal label rather than no `runs-on` key at all, because a
// file that has to say where it runs is a file whose next version can
// say somewhere else, and because a workflow copied from
// `.github/workflows` fails on this line -- naming the thing that is
// different -- rather than silently running its ubuntu-latest steps on
// something else.
const RunnerLabel = "grain-sandbox"

const GitHubRunnerLabel = "ubuntu-latest"

// Workflow is one file under WorkflowDir: what it is called, what
// events it answers to, and the jobs it declares.
type Workflow struct {
	// Sources retain execution limitations until this workflow is selected.
	// A scheduled image builder must not block unrelated push/PR workflows.
	planningError error
	// actions is set by parseGitHub: this workflow's jobs run under act,
	// whichever directory the file came from.
	//
	// It is a fact about the workflow rather than about its jobs because
	// the jobs are exactly what a planning error takes away
	// (parseForSource clears them and keeps the triggers), and the one
	// thing that had to survive it is this: planning translates a push of
	// a task branch into the pull_request an Actions workflow expects
	// (workflowEvent), and reading the kind off Jobs[0] meant a
	// .github workflow with a definition error in it matched *no* event
	// on any branch but main -- so its error was never reported there,
	// and the run came back green with no jobs rather than red with a
	// message (grain/task-254).
	actions bool
	// Path is the file this was parsed from, relative to the repository
	// root (".grain/workflows/tests.yml"). It is not a field of the
	// format -- Parse fills it -- and it is what every error message
	// and every result row names the workflow by when Name is ambiguous
	// or absent.
	Path string
	// Name is the workflow's own `name:`, GitHub's own key. Empty in the
	// file means Path, exactly as GitHub falls back to the file path for
	// an unnamed workflow.
	Name string
	// On is the set of events this workflow answers to. A workflow with
	// no `on:` at all answers to nothing and is never planned -- which
	// is GitHub's behaviour too, and is the right one: a file that
	// forgot to say when it runs should be visibly dead rather than
	// running on everything.
	On Triggers
	// Env is workflow-level environment, layered under each job's and
	// each step's own (Step.Environment).
	Env map[string]string
	// Jobs are the workflow's jobs, sorted by ID rather than kept in
	// file order. YAML mappings have an order and Go maps do not, and
	// every consumer here -- the plan, the fingerprint, the result rows
	// -- needs one stable answer rather than whichever the parse
	// happened to produce.
	Jobs []Job
}

// PlanningError is the error that keeps this workflow from being
// planned, recorded by a source that parsed its triggers but not its
// jobs (see the field).
func (w Workflow) PlanningError() error { return w.planningError }

// WithPlanningError is w with its jobs taken away and err recorded as
// what any run that selects it fails with.
func (w Workflow) WithPlanningError(err error) Workflow {
	w.planningError = err
	w.Jobs = nil
	return w
}

// isActions reports whether this workflow's jobs are act invocations.
//
// The parser's own flag where there is one, and the jobs themselves
// otherwise: a Workflow built in memory rather than parsed from a file
// -- which is every one a test writes -- carries its kind on its jobs
// and nowhere else.
func (w Workflow) isActions() bool {
	return w.actions || (len(w.Jobs) > 0 && w.Jobs[0].Actions != nil)
}

// Job is one job of a workflow: a unit that gets a sandbox of its own,
// runs its steps in order inside it, and is the granularity everything
// else here works at -- parallelism, caching, and the pass/fail a
// reader actually looks at.
type Job struct {
	// ID is the key the job is declared under (`jobs.<id>`), the name
	// `needs` refers to it by, and the name its cache key is derived
	// from.
	ID string
	// Name is the job's own `name:`, for a human. Empty means ID.
	Name string
	// Needs are the IDs of jobs in this same workflow that must finish
	// successfully before this one starts. Cross-workflow `needs` is not
	// a thing: two workflows are two independent units of work, exactly
	// as on GitHub.
	Needs []string
	// Env is job-level environment, layered over the workflow's and
	// under each step's.
	Env map[string]string
	// Sandbox is how big a machine this job asked for. The zero value
	// asks for nothing in particular, which is what most jobs should
	// say: the deployment's own default shape is then what they get.
	Sandbox SandboxSpec
	// TimeoutMinutes bounds the whole job, steps and sandbox setup
	// alike. Zero means DefaultJobTimeout rather than "no bound": a CI
	// job that can hang forever is one that eventually holds every
	// sandbox a deployment has, which is a worse failure than the one
	// the timeout cuts short.
	TimeoutMinutes int
	// Cache controls whether a completed result may stand in for this job
	// on a later identical run. It defaults to true. Release jobs and other
	// jobs whose purpose is an external side effect set `cache: false` so a
	// new matching run always executes them.
	Cache bool
	// Artifacts are the files this job is worth keeping when it does not
	// succeed: `artifacts:` in the file, GitHub's actions/upload-artifact
	// said as a property of the job rather than as a step, since a grain
	// job's sandbox is destroyed the moment the job ends and a step
	// cannot outlive it. See ArtifactSpec, and artifacts.go for why this
	// is failure-only.
	//
	// It is part of the job's fingerprint, like everything else declared
	// here: adding an artifact to a job whose failure is already cached
	// is a different job, so the next run of it actually runs and
	// actually collects.
	//
	// The json tag is the only one on this struct and is load-bearing for
	// exactly that reason. jobFingerprint hashes a Job through
	// encoding/json, which writes a nil slice as `null` rather than
	// omitting it, so a new field with no tag would change the key of
	// every job in every deployment -- a fleet-wide cache miss, and a
	// full suite re-run on every open branch, to add a key that most jobs
	// do not use. `omitempty` keeps a job that declares no artifacts
	// hashing exactly as it did.
	Artifacts []ArtifactSpec `json:"artifacts,omitempty"`
	// Paths and PathsIgnore are the files this job's answer depends on:
	// `paths:` and `paths-ignore:` in the file, GitHub's own two keys,
	// GitHub's own glob syntax and GitHub's own `!`, said of a *job*
	// rather than of a workflow's trigger. Declaring either keys the job
	// on the content of the files that match rather than on the commit,
	// so a commit that touched nothing this job reads is a cache hit
	// rather than eight minutes of sandbox. A job declares one or the
	// other; both together is refused, as it is on GitHub.
	//
	// Both empty is the default and means the whole tree, which is what
	// every job did before this key existed. paths.go's doc comment is
	// the whole of what a declaration promises and what defends it --
	// read it before adding one, because a job that reads a file it did
	// not declare is a green tick over code nobody tested.
	//
	// json tags for the reason Artifacts carries one: a Job is hashed
	// through encoding/json, and `omitempty` keeps a job that declares
	// neither hashing as it did.
	Paths       []string `json:"paths,omitempty"`
	PathsIgnore []string `json:"pathsIgnore,omitempty"`
	// GuestCache names the files whose content decides which sandbox
	// guest this job resolves: `guest-cache:` in the file, the same glob
	// syntax `paths:` takes. A job that declares it is one grain may warm
	// the guest cache for before letting it start -- see guestcache.go,
	// which is the whole of what declaring it does and what it costs.
	//
	// It is deliberately not `paths:`. The two select files for opposite
	// purposes: `paths:` says which files decide this job's *answer*, so
	// that a commit touching none of them is a cache hit, and this says
	// which files decide which ~1GB image the job has to have in front of
	// it before it can run at all. A job can want both -- the KVM suite's
	// answer depends on the whole tree, and its guest depends on eleven
	// paths -- and folding them into one key would make every change to a
	// guest input a miss on every job that reads one.
	//
	// Empty is the default and means grain warms nothing for this job,
	// which is what every job did before this key existed.
	//
	// json tag for the reason Artifacts carries one: a Job is hashed
	// through encoding/json, and `omitempty` keeps a job that declares no
	// guest inputs hashing exactly as it did -- so adding this key to the
	// format misses no cache entry anywhere.
	GuestCache []string `json:"guestCache,omitempty"`
	// Capabilities and Secrets are what this job cannot do its work
	// without: `capabilities:` and `secrets:` in the file, capability
	// ids and release secret names respectively.
	//
	// They are *requirements*, and requirements.go is the whole of what
	// that means and why it is not the same thing as a grant. In one
	// line: writing `gcp-key` here does not produce a key, it states
	// that this job's steps cannot run without one, and a job whose
	// requirement this deployment does not meet for this repository does
	// not run and says so.
	//
	// Declared on the job rather than on the workflow or the repository
	// because the finer the declaration the more of the suite survives a
	// missing credential -- "this job needs `gcp-key`" leaves the other
	// six running, and "this repository needs `gcp-key`" cannot.
	//
	// Empty is the default and means two things that are worth keeping
	// apart: this job asserts nothing about what it needs, and it is
	// therefore handed everything the deployment provides this
	// repository, exactly as every job was before these keys existed.
	// `capabilities: []` is the other answer, and NoCapabilities is
	// where it is kept.
	//
	// json tags for the reason Artifacts carries one: a Job is hashed
	// through encoding/json, and `omitempty` keeps a job that declares
	// neither hashing exactly as it did -- so adding these keys to the
	// format misses no cache entry anywhere.
	Capabilities []string `json:"capabilities,omitempty"`
	Secrets      []string `json:"secrets,omitempty"`
	// NoCapabilities is `capabilities: []` in the file: the key present
	// and its list empty. It is the job saying it needs none of what the
	// deployment provides this repository, and it is given none of it --
	// where an absent key (Capabilities empty, this false) still means
	// the whole provided set (jobCapabilities). Without it a job had no
	// way to opt out, and every job in a suite minted every finite
	// credential the repository was granted whether it used it or not.
	//
	// json tag and omitempty for the reason Capabilities has them: a job
	// that does not write `capabilities: []` hashes exactly as it did.
	NoCapabilities bool `json:"noCapabilities,omitempty"`
	// OptionalCapabilities is `optional-capabilities:` in the file:
	// capability ids this job uses when it has them and runs without
	// when it does not (requirements.go's "Optional capabilities"). One
	// is placed only when this deployment provides it for this
	// repository; one it does not provide -- including a name this build
	// has never heard of -- is simply absent, never a reason to refuse
	// the job. Declaring one narrows the job exactly as declaring a
	// requirement does, so a job still gets only what it named.
	//
	// json tag and omitempty for the reason Capabilities has them: a job
	// declaring none hashes exactly as it did before the key existed.
	OptionalCapabilities []string `json:"optionalCapabilities,omitempty"`
	// Retry enables automatic retry for flaky or failed steps on this job.
	Retry bool `json:"retry,omitempty"`
	// Retries bounds the retry attempts count (bounded to at most 1).
	Retries int `json:"retries,omitempty"`
	// RetryPolicy is the name of a retry policy for this job.
	RetryPolicy string `json:"retryPolicy,omitempty"`
	// Builtin names a job the deployment runs itself rather than in a
	// sandbox (SandboxRequest.Builtin): the Sandboxes implementation it
	// is handed to answers Acquire with a machine of its own making, and
	// each step's script is what that machine does rather than a shell
	// script for one. It is never read from a workflow file -- no key in
	// the format sets it -- so only a workflow grain writes in code
	// (pkg/daemon's ConfigRepoCISource) can ask for one.
	//
	// json tag for the reason Artifacts carries one: `omitempty` keeps
	// every other job hashing exactly as it did.
	Builtin string `json:"builtin,omitempty"`
	// Steps are the job's steps, in file order, which is the one place
	// here where file order is the meaning rather than an accident.
	Steps []Step
	// Actions is non-nil for a job read directly from .github/workflows.
	Actions *ActionsJob
}

type ActionsJob struct {
	WorkflowPath string
	// Event is filled while planning. A task branch arrives at Grain as a
	// push, but an Actions workflow commonly spells that pre-merge check as
	// pull_request.
	Event Event
	// WorkflowText is the workflow file this job's act invocation is
	// given, for a job that declares `needs`: the file as committed, with
	// this job's own `needs:` commented out, written into the sandbox and
	// pointed at instead of the one in the checkout.
	//
	// It is here because act resolves `needs` itself -- `act --job image`
	// runs everything `image` needs before it, in that one invocation --
	// and grain has already ordered the jobs and run them. See
	// actions_needs.go, which is the whole of the argument.
	//
	// Empty for a job that needs nothing, which is every job an Actions
	// workflow had before this was read at all. The json tag is what keeps
	// that promise cheap: an ActionsJob is hashed into its job's
	// fingerprint (jobFingerprint.Job), and `omitempty` leaves the key of
	// every job that declares no `needs` exactly where it was rather than
	// missing the cache for the whole fleet.
	WorkflowText string `json:",omitempty"`
}

// DisplayName is what a reader should be shown for this job: its own
// `name:` where it has one, its ID otherwise.
func (j Job) DisplayName() string {
	if j.Name != "" {
		return j.Name
	}
	return j.ID
}

// Timeout is TimeoutMinutes as a duration, with zero resolved to
// DefaultJobTimeout -- see the field's own doc comment for why zero is
// not "unbounded".
func (j Job) Timeout() time.Duration {
	if j.TimeoutMinutes <= 0 {
		return DefaultJobTimeout
	}
	return time.Duration(j.TimeoutMinutes) * time.Minute
}

// DefaultJobTimeout is how long a job with no `timeout-minutes` of its
// own gets. GitHub's own default for a job is 360 minutes; grain's is a
// tenth of that, because a grain sandbox is a resource a deployment has
// very few of (model.Config.MaxWorkers) and a six-hour hung job on a
// two-sandbox deployment is an outage rather than a slow build.
const DefaultJobTimeout = 36 * time.Minute

// Step is one shell script run inside the job's sandbox.
type Step struct {
	// Name is the step's own `name:`, for a human reading the log.
	// Empty means the first line of Run, the way GitHub renders an
	// unnamed step.
	Name string
	// Run is the script, run with bash, from the job's workspace (or
	// WorkingDirectory under it).
	Run string
	// Env is step-level environment, layered over the job's and the
	// workflow's.
	Env map[string]string
	// WorkingDirectory is where Run is run, relative to the workspace
	// the repository was checked out into. Empty means the workspace
	// itself.
	WorkingDirectory string
	// ContinueOnError records the step's failure and carries on with
	// the next one, leaving the job successful -- GitHub's own
	// `continue-on-error`, and the same warning applies: a step nobody
	// fails over is a step nobody reads.
	ContinueOnError bool
	// TimeoutMinutes bounds this step alone. Zero means the job's own
	// remaining time is the only bound -- except on an infrastructure
	// step, where it is that step's whole bound and the job's remaining
	// time is not consulted at all (see Infrastructure).
	TimeoutMinutes int
	// Infrastructure says this step does not test the tree: it gets the
	// job the environment its other steps need. Resolving a
	// content-addressed image out of a registry, pulling a base layer,
	// installing a pinned tool, building what a cache should have held --
	// work whose success says something about this deployment and nothing
	// at all about the commit.
	//
	// It exists to keep one invariant, which is worth stating as a
	// sentence because everything below is a consequence of it: **a cache
	// miss may cost time and must never cost a verdict.** A cache is an
	// optimisation; a miss is supposed to be slower and otherwise
	// identical. What happened instead is grain/task-464's own history: a
	// branch that changed a guest input missed `guest:inputs-<key>`, its
	// "Resolve the sandbox guest" step built the ~1GB guest inline in a
	// nested VM, that ran past the job's bound, and the job was recorded
	// as a *failure* -- a red verdict on a branch nothing was wrong with.
	// The merge queue filed repairs against those branches, the
	// base-branch gate read the same verdicts and held every merge in the
	// repository, and grain/task-427 finally deleted the `real KVM
	// sandboxes` job from `.grain/workflows/tests.yml` to stop the noise.
	// A cache decided what got tested.
	//
	// Declaring it changes two things, and only these two:
	//
	//   - **A step declared here that does not succeed concludes the job
	//     ConclusionErrored rather than ConclusionFailure.** That is the
	//     whole of the fix: errored is grain's existing word for "we did
	//     not get an answer" -- it is not a statement about the tree, it
	//     does not make a base branch Broken (model.BaseBranchCI.Broken
	//     covers `failure` alone), it earns no repair task against the
	//     branch, and it is not written to the job cache (cacheable), so
	//     the next run samples the commit again instead of replaying a
	//     verdict a cold cache invented.
	//   - **Its time is not charged to the job's `timeout-minutes`.** A
	//     job's bound is meant to bound the work the job does, not the
	//     environment it waits for. An infrastructure step is bounded by
	//     its own `timeout-minutes` where it declares one and by the
	//     job's own bound -- a fresh one, of the same length -- where it
	//     does not, and the time it spends is given back to the job
	//     afterwards (jobClock). So a job whose cache was cold has the
	//     same time to run its tests in as one whose cache was warm, and
	//     the miss can no longer expire a bound it was never being
	//     measured against.
	//
	// What it does *not* do is make the step optional. A job still stops
	// at the first step that does not succeed, and the steps after an
	// infrastructure step that failed are skipped exactly as they are
	// after any other -- because a job that could not get its environment
	// has not tested anything and must not be allowed to look as though
	// it had. `continue-on-error:` is still how a step says its failure
	// does not stop the job, and it still wins: a step that says both
	// carries on, and the job is not errored by it.
	//
	// Use it for a step whose failure is genuinely never the tree's
	// fault. `npm ci` is the near miss worth naming: a registry that will
	// not answer is this deployment's problem, but a lockfile that does
	// not resolve is the commit's, the exit code is the same either way,
	// and a step that cannot tell them apart would hide real breakage
	// behind an "errored" nobody blames. Declare the fetch that can only
	// be infrastructure, not the step that is sometimes a verdict.
	//
	// That sentence has a worked example now, and it is worth reading
	// before declaring this on anything that both fetches and checks:
	// grain/task-498 split `npm ci` in this repository's own
	// .grain/workflows/tests.yml into scripts/warm-npm-cache.sh, which
	// fills the cache and is the step that may fail this way, and an
	// install with `npm_config_offline` set, which cannot reach a
	// network and so can only fail on the tree. What it costs is one
	// more install and a script that has to decide, when the fetch did
	// not work, whether the registry or the lockfile was to blame --
	// which it settles on npm's own error code and a ping, defaulting to
	// the tree whenever it cannot tell. That default is the shape of
	// every such decision: being wrong about infrastructure costs a red
	// tick somebody looks at, being wrong about the tree costs a green
	// one nobody does.
	//
	// json tag for the reason Job.Artifacts carries one: a Job -- steps
	// and all -- is hashed through encoding/json into its cache key
	// (jobFingerprint), and `omitempty` keeps every step that does not
	// declare this hashing exactly as it did, so adding this key to the
	// format misses no cache entry anywhere.
	Infrastructure bool `json:"infrastructure,omitempty"`
}

// DisplayName is what a reader should be shown for this step: its own
// `name:`, or the first line of its script.
func (s Step) DisplayName() string {
	if s.Name != "" {
		return s.Name
	}
	line, _, _ := strings.Cut(strings.TrimSpace(s.Run), "\n")
	return line
}

// SandboxSpec is how big a sandbox a job asked for -- the same three
// dimensions orchestrator.Shape carries, named here in the file's own
// vocabulary (`cpus`, `memory-mb`, `disk-gb`) rather than in Go's, and
// carried as a type of this package's own so pkg/ci needs no import of
// pkg/orchestrator (which would run the wrong way: the orchestrator is
// what will eventually drive this, not the other way round).
type SandboxSpec struct {
	CPUs     int
	MemoryMB int
	DiskGB   int
}

// IsZero reports whether a job asked for no particular size, which is
// what leaving `sandbox:` out entirely produces.
func (s SandboxSpec) IsZero() bool { return s.CPUs == 0 && s.MemoryMB == 0 && s.DiskGB == 0 }

// Event is what a CI run is answering to -- GitHub's own event names,
// because a workflow's `on:` block is written in them.
type Event string

const (
	// EventPush is a branch that moved: the everyday trigger, and the
	// one a grain run pushing its branch produces.
	EventPush Event = "push"
	// EventPullRequest is a pull request opened or updated against a
	// base branch. grain's own merge queue is the eventual consumer:
	// "has this branch's head passed the target repo's own checks" is
	// the question it currently has to ask GitHub.
	EventPullRequest Event = "pull_request"
	// EventRelease is a release run: normally the destination branch after
	// Grain observes a pull request merge, or a branch an operator explicitly
	// chooses through the repository's Run release action. Unlike an ordinary
	// push, either origin is trusted before release-only material enters a job.
	EventRelease Event = "release"
	// EventManual is `workflow_dispatch` -- somebody asked for this run
	// by hand, through the API or the CLI. Spelled with GitHub's key in
	// the file and with a shorter name in Go, because "manual" is what
	// it means and `workflow_dispatch` is only what Actions calls it.
	EventManual Event = "workflow_dispatch"
	// EventSchedule is a run of a workflow's own `on: schedule` cron,
	// started by the task schedule that cron was translated into
	// (cron.go, model.WorkflowReviewTemplateID). There is no cron in this
	// package: what fires at 02:11 is one of grain's ordinary schedules,
	// and this is the event it starts the run under, which is what makes
	// a workflow that says nothing but `schedule:` have any jobs to run
	// at all.
	EventSchedule Event = "schedule"
)

// Triggers is a workflow's `on:` block: which events run it, and for
// the branch-shaped ones, which branches.
type Triggers struct {
	Push        *BranchTrigger
	PullRequest *BranchTrigger
	Release     *BranchTrigger
	Manual      bool
	// Schedule is the workflow's `on: schedule` list, one entry per cron,
	// each already translated into the task-schedule cadence it means
	// (ScheduleTrigger, cron.go). Empty for a workflow with no cron -- and
	// empty for a `.github/workflows` file whatever it says, because
	// GitHub's own scheduler already runs those and a second one would
	// double every nightly (parseGitHub).
	//
	// It carries no branch filter because a cron has none: GitHub runs a
	// scheduled workflow on the default branch, and the task schedule
	// translated out of this one runs it on whichever branch that schedule
	// names (model.Schedule.Base).
	Schedule []ScheduleTrigger
}

// Any reports whether this workflow answers to any event at all.
func (t Triggers) Any() bool {
	return t.Push != nil || t.PullRequest != nil || t.Release != nil || t.Manual ||
		len(t.Schedule) > 0
}

// BranchTrigger is the branch filter on one event: `branches` and
// `branches-ignore`, GitHub's own two keys with GitHub's own precedence
// (ignore wins) and a subset of its own glob syntax (Match).
type BranchTrigger struct {
	Branches       []string
	BranchesIgnore []string
}

// Matches reports whether branch passes this filter. An empty filter
// matches every branch, which is what `on: push:` with no keys under it
// means on GitHub too.
func (b *BranchTrigger) Matches(branch string) bool {
	if b == nil {
		return false
	}
	branch = strings.TrimPrefix(branch, "refs/heads/")
	for _, pattern := range b.BranchesIgnore {
		if matchBranch(pattern, branch) {
			return false
		}
	}
	if len(b.Branches) == 0 {
		return true
	}
	for _, pattern := range b.Branches {
		if matchBranch(pattern, branch) {
			return true
		}
	}
	return false
}

// matchBranch is GitHub's branch-filter globbing -- matchGlob, which a
// job's `paths:` filter now shares (paths.go). The two wildcards it
// supports and why it is not a regexp are that function's own comment.
func matchBranch(pattern, branch string) bool { return matchGlob(pattern, branch) }

// Describe is an `on:` block written back out in one line -- "push on
// main, pull_request on any branch" -- for the message a run that
// matched no workflow carries (unmatchedReason).
//
// It is the file's own vocabulary rather than this package's structs,
// because the person reading it has the file open: what they have to
// compare against is the branch and the event their run was dispatched
// under, and a line naming both is what lets them see which of the two
// is the one that does not line up.
func (t Triggers) Describe() string {
	if !t.Any() {
		return "nothing -- it declares no `on:` this deployment runs workflows for"
	}
	var parts []string
	for _, ev := range []struct {
		event   Event
		trigger *BranchTrigger
	}{
		{EventPush, t.Push},
		{EventPullRequest, t.PullRequest},
		{EventRelease, t.Release},
	} {
		if ev.trigger != nil {
			parts = append(parts, fmt.Sprintf("%s %s", ev.event, ev.trigger.describeBranches()))
		}
	}
	if t.Manual {
		parts = append(parts, string(EventManual))
	}
	return strings.Join(parts, ", ")
}

// describeBranches is one trigger's branch filter in words, and reads
// "on any branch" for the empty filter that matches every one of them --
// the difference between a workflow that answers a branch and one that
// answers all of them is usually the whole of why a run matched nothing.
func (b *BranchTrigger) describeBranches() string {
	if b == nil {
		return "never"
	}
	var parts []string
	if len(b.Branches) > 0 {
		parts = append(parts, "on "+strings.Join(b.Branches, ", "))
	} else {
		parts = append(parts, "on any branch")
	}
	if len(b.BranchesIgnore) > 0 {
		parts = append(parts, "except "+strings.Join(b.BranchesIgnore, ", "))
	}
	return strings.Join(parts, " ")
}

// Matches reports whether this workflow answers to event on branch.
func (t Triggers) Matches(event Event, branch string) bool {
	switch event {
	case EventPush:
		return t.Push.Matches(branch)
	case EventPullRequest:
		return t.PullRequest.Matches(branch)
	case EventRelease:
		return t.Release.Matches(branch)
	case EventManual:
		return t.Manual
	case EventSchedule:
		// No branch filter, for ScheduleTrigger's own reason: a cron says
		// when, and the schedule translated out of it says where.
		return len(t.Schedule) > 0
	default:
		return false
	}
}

// nativeWorkflowPath reports whether filePath is one of grain's own
// workflow files rather than a borrowed GitHub Actions one -- which is
// exactly "it is not under .github/workflows", since an Actions-shaped file
// may perfectly well live in WorkflowDir (Parse dispatches on `runs-on:`,
// not on the directory) and a file grain was handed the text of directly
// has no directory at all.
//
// It decides one thing: whether a cron in this file is grain's to run
// (rawOn.triggers).
func nativeWorkflowPath(filePath string) bool {
	clean := path.Clean(filePath)
	return clean != GitHubWorkflowDir && !strings.HasPrefix(clean, GitHubWorkflowDir+"/")
}

// JobIDPattern is what a job key may look like -- GitHub's own rule,
// which is also the rule that keeps a job ID usable as part of a
// sandbox name and of a URL path.
var JobIDPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// Parse reads one workflow file. filePath is what the workflow will be
// known by (and named in every error this returns); data is the file.
//
// Every key the format does not define is an error rather than a
// shrug -- see the package doc on `uses:`. So is every semantic
// mistake this can see without leaving the file: a job that needs a job
// that is not there, a `needs` cycle, a step with no script, a
// `runs-on` naming anything but RunnerLabel. What it cannot see from
// one file it does not guess at: whether the *repository* has a
// workflow of the same name twice is Load's business, since one file
// cannot know.
func Parse(filePath string, data []byte) (Workflow, error) {
	// Both workflow directories use GitHub Actions syntax. During the
	// in-repository extraction, accept the former private shape only for
	// old callers that do not declare runs-on; the two controlled installs
	// have already moved and this branch can disappear with their fixtures.
	if strings.HasPrefix(path.Clean(filePath), GitHubWorkflowDir+"/") || declaresRunsOn(data) {
		return parseGitHub(filePath, data)
	}
	return parseGrain(filePath, data)
}

// declaresRunsOn reports whether any job in data has a `runs-on` key --
// read off the parsed document rather than the text, so that a comment
// explaining why a job has no `runs-on:` does not send the whole file to
// the Actions path (granule's e2e-ipv6.yml said exactly that, and every
// run of it failed at planning). A file that does not parse falls back to
// the textual check, so it is reported by the same parser as before.
func declaresRunsOn(data []byte) bool {
	doc, err := documentNode(data)
	if err != nil {
		return bytes.Contains(data, []byte("runs-on:"))
	}
	jobs := mappingValue(doc, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return false
	}
	for i := 1; i < len(jobs.Content); i += 2 {
		if key, _ := mappingEntry(jobs.Content[i], "runs-on"); key != nil {
			return true
		}
	}
	return false
}

func parseGrain(filePath string, data []byte) (Workflow, error) {
	var raw rawWorkflow
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return Workflow{}, fmt.Errorf("ci: %s: %w", filePath, unwrapYAML(err))
	}
	// A second document in one file is a second workflow with no name
	// of its own -- nothing here would run it, so say so rather than
	// dropping it.
	var extra rawWorkflow
	if err := dec.Decode(&extra); err == nil {
		return Workflow{}, fmt.Errorf("ci: %s: a workflow file holds one workflow, and this one has a second YAML document in it", filePath)
	}

	w := Workflow{
		Path: filePath,
		Name: strings.TrimSpace(raw.Name),
		Env:  raw.Env,
	}
	if w.Name == "" {
		w.Name = filePath
	}
	triggers, err := raw.On.triggers(nativeWorkflowPath(filePath))
	if err != nil {
		return Workflow{}, fmt.Errorf("ci: %s: on: %w", filePath, err)
	}
	w.On = triggers

	if len(raw.Jobs) == 0 {
		return Workflow{}, fmt.Errorf("ci: %s: a workflow declares at least one job", filePath)
	}
	ids := make([]string, 0, len(raw.Jobs))
	for id := range raw.Jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		job, err := raw.Jobs[id].job(id)
		if err != nil {
			return Workflow{}, fmt.Errorf("ci: %s: jobs.%s: %w", filePath, id, err)
		}
		w.Jobs = append(w.Jobs, job)
	}
	if err := validateNeeds(w); err != nil {
		return Workflow{}, fmt.Errorf("ci: %s: %w", filePath, err)
	}
	return w, nil
}

// actionsJobScript delegates one Actions job to act on the sandbox host.
// The sandbox VM remains the security and resource boundary and retains its
// Docker daemon, KVM device and network namespace.
//
// workflowText is ActionsJob.WorkflowText: empty for a job that needs
// nothing, in which case act reads the workflow out of the checkout and
// this is the script it has always been; otherwise the pruned copy is
// written into the sandbox first and act is pointed at that
// (actions_needs.go).
func actionsJobScript(workflowPath, jobID string, actionsEvent Event, workflowText string) string {
	if workflowText == "" {
		return actions.ActScript(workflowPath, jobID, string(actionsEvent))
	}
	return actions.ActScriptForText(workflowPath, jobID, string(actionsEvent), workflowText)
}

// validateNeeds checks every `needs` names a job of this same workflow,
// and that following them never comes back to where it started. A cycle
// is caught here, at parse, rather than at the moment the engine finds
// it has nothing left it can start: a workflow whose jobs can never all
// run is a broken file, and the person who can fix it is reading the
// error from the file's own path.
func validateNeeds(w Workflow) error {
	byID := make(map[string]Job, len(w.Jobs))
	for _, j := range w.Jobs {
		byID[j.ID] = j
	}
	for _, j := range w.Jobs {
		for _, need := range j.Needs {
			if _, ok := byID[need]; !ok {
				return fmt.Errorf("jobs.%s: needs %q, which is not a job in this workflow", j.ID, need)
			}
			if need == j.ID {
				return fmt.Errorf("jobs.%s: needs itself", j.ID)
			}
		}
	}
	// Plain iterative depth-first search over the needs edges, with the
	// path being walked kept in `onPath` -- an edge back onto it is the
	// cycle.
	state := make(map[string]int, len(byID)) // 0 unseen, 1 on the path, 2 done
	var walk func(id string, path []string) error
	walk = func(id string, path []string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("jobs.%s: needs form a cycle (%s)", id, strings.Join(append(path, id), " -> "))
		case 2:
			return nil
		}
		state[id] = 1
		for _, need := range byID[id].Needs {
			if err := walk(need, append(path, id)); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for _, j := range w.Jobs {
		if err := walk(j.ID, nil); err != nil {
			return err
		}
	}
	return nil
}

// unwrapYAML turns yaml.v3's own multi-line TypeError into something a
// one-line error message can hold, and leaves everything else alone.
// The library reports every unknown field of a document in one error
// with an embedded newline per line of it, which reads badly wherever a
// grain error is rendered on one line (a CLI row, a JSON field, a task
// comment).
//
// Its other job is explainUnknownFields: a line about a key the format
// does not define is rewritten out of Go's vocabulary into the
// format's, because that error is read by whoever wrote the workflow
// and "type ci.rawStep" is no help to them (see yaml.go's note on it).
func unwrapYAML(err error) error {
	var typeErr *yaml.TypeError
	if !errors.As(err, &typeErr) {
		return err
	}
	lines, rewrote := explainUnknownFields(typeErr.Errors)
	joined := strings.Join(lines, "; ")
	if rewrote {
		joined += " (" + unknownFieldNote + ")"
	}
	return fmt.Errorf("%s", joined)
}

// RequiredCapabilities is the capability ids this job's steps cannot do
// their work without -- `capabilities:` in the workflow file.
func (j Job) RequiredCapabilities() []string { return slices.Clone(j.Capabilities) }
