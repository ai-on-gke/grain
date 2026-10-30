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

package ci

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/version"
)

// Service is grain CI as something a daemon holds: a Source to read a
// repository's workflows from, an Engine to run them with, and the
// runs it has started, so an API can ask for one by name after the
// request that started it has gone.
//
// Start is asynchronous on purpose. A CI run takes minutes and an HTTP
// request should not; the caller gets a run id and reads the result
// back with Get, exactly as it would from GitHub's own checks API.
type Service struct {
	// Source is where workflows come from. Required.
	Source Source
	// Capabilities reads operator-owned repo configuration before planning.
	// Any capability list supplied by the caller is replaced, including when nil.
	Capabilities func(context.Context, string) ([]string, error)
	// ReleaseSecrets reads the names of repository-scoped custom secrets. The
	// caller cannot supply them, and Service only attaches them to trusted
	// release or explicit manual-dispatch events.
	ReleaseSecrets func(context.Context, string) ([]string, error)
	// ReleaseRefRefusal says why a trusted release may not run from ref
	// in repo -- empty when it may. A release reads its workflows at ref
	// and is handed the repository's release secrets and publishing
	// capabilities, so ref has to be a branch the repository has
	// reviewed into (its default, paired or release branches) and never
	// a task's branch (grain/task-949). Start asks it of every
	// TrustedRelease request on a named ref and refuses the run on a
	// refusal or an error (checkReleaseRef). nil asks nothing, for a
	// Service with no repository settings to consult.
	ReleaseRefRefusal func(ctx context.Context, repo, ref string) (string, error)
	// DeployedImages names the grain images this deployment runs, for a
	// release run's Request.DeployedImages. nil hands a release job none.
	DeployedImages func() []string
	// SandboxImage returns the OCI image this deployment creates job
	// sandboxes from right now, for Request.SandboxImage. nil leaves it
	// empty (host sandboxes, or tests with no image configured).
	SandboxImage func() string
	// Engine runs them. Required.
	Engine *Engine
	// Runs is where results are kept; nil means an in-memory one is made
	// on first use with DefaultRunHistory. A daemon hands in a
	// StoredRunStore instead, so its runs survive it.
	Runs RunStore
	// MaxRunTime bounds a whole run, however long its jobs' own bounds
	// would add up to. Zero means DefaultMaxRunTime.
	MaxRunTime time.Duration
	// Now is the clock, for tests. nil means time.Now.
	Now func() time.Time
	// Build is which build of grain is running these runs, for tests.
	// nil reads this process's own stamp (version.Get), which is what a
	// daemon wants and what nobody has to remember to wire -- see
	// ci.Build on why a run carries one at all.
	Build func() version.Info
	// NewID generates run ids. nil means a random one.
	NewID func() string
	// Log is where a run's progress is written; nil discards.
	Log func(format string, args ...any)
	// PlanDir is where in-flight run plans are persisted on disk so a
	// restarted daemon can resume an adopted run without re-fetching its
	// workflow tree. Empty disables disk persistence (ReconstructPlan can
	// still re-plan from Source).
	PlanDir string
	// Stopping, when non-nil, is closed when the hosting daemon begins
	// shutting down.
	Stopping <-chan struct{}

	mu       sync.Mutex
	runs     RunStore
	cancels  map[string]context.CancelFunc
	detaches map[string]context.CancelFunc
	wg       sync.WaitGroup
	// cancelled is what Cancel was told about each run it stopped -- who
	// asked and what they said -- kept here because the record in the
	// store is replaced wholesale when the run unwinds (Start's
	// goroutine) and both have to outlive that.
	cancelled map[string]cancellation
}

// cancellation is one Cancel call's own account of itself, held until the
// run it stopped has finished unwinding and the finished record can be
// stamped with it.
type cancellation struct {
	by     CancelSource
	reason string
}

// WorkflowOption is one workflow an operator may choose for a request.
// The path is its stable identity; name is the human-facing label.
type WorkflowOption struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// WorkflowOptions reads the workflows at req's repository and ref and
// returns the ones which match its event. It makes manual workflow discovery
// use the same source and trigger matching as Start rather than maintaining a
// second approximation in the UI.
func (s *Service) WorkflowOptions(ctx context.Context, req Request) ([]WorkflowOption, error) {
	if s.Source == nil {
		return nil, fmt.Errorf("ci: this deployment has no CI source wired")
	}
	req = req.Normalize()
	if err := req.Validate(); err != nil {
		return nil, err
	}
	workflows, _, err := s.Source.Workflows(ctx, req.Repo, req.Ref)
	if err != nil {
		return nil, fmt.Errorf("ci: reading %s's workflows at %s: %w", req.Repo, req.Ref, err)
	}
	options := make([]WorkflowOption, 0, len(workflows))
	for _, workflow := range workflows {
		if _, matches := WorkflowEvent(workflow, req); matches {
			options = append(options, WorkflowOption{Path: workflow.Path, Name: workflow.Name})
		}
	}
	return options, nil
}

// DefaultMaxRunTime is the ceiling on one whole run. It is not the sum
// of its jobs' timeouts and is not meant to be: it is the backstop for
// a backend that has stopped answering in a way no individual job
// notices.
const DefaultMaxRunTime = 2 * time.Hour

func (s *Service) store() RunStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Runs != nil {
		return s.Runs
	}
	if s.runs == nil {
		s.runs = NewRunStore(0)
	}
	return s.runs
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// instant is s.now() as a Run records one -- see Engine.instant.
func (s *Service) instant() *time.Time {
	t := s.now()
	return &t
}

// build is the stamp to put on a run this service starts.
//
// The Service stamps it rather than the Engine, though it is the Engine
// that does the work: both are the same process, one of them is what a
// deployment wires up and hands requests to, and a single writer is what
// keeps the stamp on a run whose record the finished result replaces
// wholesale (Start's goroutine).
func (s *Service) build() *Build {
	if s.Build != nil {
		return BuildOf(s.Build())
	}
	return BuildOf(version.Get())
}

// vouchForCommit withholds every capability and release secret from a
// run whose pinned commit is not the branch's.
//
// A job checks out req.Commit, not req.Ref, and fetches it by name when
// the clone did not bring it down (cmd/grain's ciCheckoutScript) --
// which GitHub answers for any commit it holds for the repository,
// including the head of a pull request opened from a fork by somebody
// with no write access at all. The capabilities a repository grants
// its CI are granted to the trees on its own branches, the same line
// GitHub draws between pull_request and pull_request_target. So a
// commit the source cannot place on req.Ref runs, but runs as a fork's
// pull request would: with nothing, and a job that declared a need says
// so loudly rather than running without it (unmetRequirements). A job
// that declared nothing gets nothing either: jobCapabilities hands such
// a job everything the request carries, which here is nothing.
//
// The tip itself is trusted without asking further, and so is a source
// with no notion of a commit (resolved == ""), which is a local tree
// or grain's own declarations rather than anything a stranger can push
// to. Anything else costs one history read, and fails closed: a source
// that cannot answer, or errs, leaves the commit untrusted.
func (s *Service) vouchForCommit(ctx context.Context, req Request, resolved string) Request {
	if req.Commit == "" || resolved == "" || sameCommit(req.Commit, resolved) {
		return req
	}
	on, err := commitOnBranch(ctx, s.Source, req.Repo, req.Ref, req.Commit)
	if err != nil {
		s.log("ci: %s: is %s on %s? %v (treating it as not)", req.Repo, req.Commit, ciRefOrDefault(req.Ref), err)
	}
	if on && err == nil {
		return req
	}
	req.Capabilities, req.ReleaseSecrets = nil, nil
	req.TrustedRelease, req.TrustedSchedule = false, false
	req = req.WithUntrustedCommit(fmt.Sprintf("this run withholds every capability and release secret: its commit %s "+
		"is not on %s's %s, so nothing says it is a tree the repository trusts "+
		"(a pull request's head from a fork is fetchable by commit) -- run the branch rather than a pinned commit",
		req.Commit, req.Repo, ciRefOrDefault(req.Ref)))
	s.log("ci: %s: commit %s is not on %s: withholding capabilities and release secrets",
		req.Repo, req.Commit, ciRefOrDefault(req.Ref))
	return req
}

// checkReleaseRef refuses a trusted release whose ref is not one a
// trusted release may run from (Service.ReleaseRefRefusal).
//
// It is vouchForCommit's rule applied to the branch rather than to the
// commit: the workflows a release runs are read at Ref, so a release of
// a branch nobody reviewed into is that branch's author's code with the
// repository's release secrets, its publishing capabilities and the
// trusted actions cache (actionscache's TrustedRelease scope) all within
// reach. Refused rather than run untrusted: a release with its trust
// taken away is no release at all (Request.Validate), and saying so to
// whoever asked is better than a run that fails for a reason it does not
// name. The default branch (Ref "") is trusted without asking, and so is
// a request a person vouched for by hand (Request.ReleaseRefVouched). An
// error asking fails closed.
func (s *Service) checkReleaseRef(ctx context.Context, req Request) error {
	if !req.TrustedRelease || req.ReleaseRefVouched || req.Ref == "" || s.ReleaseRefRefusal == nil {
		return nil
	}
	refusal, err := s.ReleaseRefRefusal(ctx, req.Repo, req.Ref)
	if err != nil {
		return fmt.Errorf("ci: refusing a trusted release of %s's %s: could not tell whether it is a branch "+
			"releases may run from: %w", req.Repo, req.Ref, err)
	}
	if refusal == "" {
		return nil
	}
	s.log("ci: %s: refusing a trusted release of %s: %s", req.Repo, req.Ref, refusal)
	return fmt.Errorf("ci: refusing a trusted release of %s's %s, which would run its workflows with the "+
		"repository's release secrets: %s", req.Repo, req.Ref, refusal)
}

func ciRefOrDefault(ref string) string {
	if ref == "" {
		return "default branch"
	}
	return "branch " + ref
}

func (s *Service) newID() string {
	if s.NewID != nil {
		return s.NewID()
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any platform grain runs on, and a
		// run id that collides is a result served for the wrong run.
		panic("ci: generating a run id: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

func (s *Service) log(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

// Start plans a run for req and begins it, returning the run as it
// stands the moment it was started: an id, the jobs it is about to run,
// and no conclusion yet.
//
// Two things happen before it returns, and both are synchronous on
// purpose, because both are things the caller can still be told about:
// the repository's workflows are read and parsed (so an invalid workflow
// becomes a visible failed run tied to this commit), and the plan is built
// (so the caller is handed the list of jobs rather than an empty run to
// poll).
//
// A request whose plan matches a run already in flight is answered with
// that run instead of a second one. That is the first piece of "the
// same run is not repeated", and the cheap piece: it needs no storage,
// no expiry policy and no trust in a previous result, because the run
// it hands back is one that is still going. The other piece -- work
// that already finished, in some earlier run -- is the Engine's Cache,
// job by job: a run started here still gets an id and a record, and
// the jobs it did not have to run say so (JobRun.Cached). planKey's own
// doc comment says why there is no whole-run shortcut above that.
func (s *Service) Start(ctx context.Context, req Request) (Run, error) {
	if s.Source == nil || s.Engine == nil {
		return Run{}, fmt.Errorf("ci: this deployment has no CI engine wired")
	}
	req = req.Normalize()
	if err := req.Validate(); err != nil {
		return Run{}, err
	}

	if err := s.checkReleaseRef(ctx, req); err != nil {
		return Run{}, err
	}
	req.Capabilities = nil
	req.ReleaseSecrets = nil
	if !req.WithholdEventGated && s.Capabilities != nil {
		ids, err := s.Capabilities(ctx, req.Repo)
		if err != nil {
			return Run{}, fmt.Errorf("ci: reading %s's capabilities: %w", req.Repo, err)
		}
		req.Capabilities = ids
	}
	if req.ReleaseSecretsEligible() {
		if s.ReleaseSecrets != nil {
			names, err := s.ReleaseSecrets(ctx, req.Repo)
			if err != nil {
				return Run{}, fmt.Errorf("ci: reading %s's release secrets: %w", req.Repo, err)
			}
			req.ReleaseSecrets = names
		}
	}
	req.DeployedImages = nil
	if req.Event == EventRelease && s.DeployedImages != nil {
		req.DeployedImages = s.DeployedImages()
	}
	req.SandboxImage = ""
	if s.SandboxImage != nil {
		req.SandboxImage = s.SandboxImage()
	}
	req = req.Normalize()

	// The tree comes back with the workflows rather than being fetched
	// again: it is the content of the very commit those workflows were
	// read at, and it is what lets a job keyed on its declared files skip
	// work this deployment has already done (paths.go). A source with
	// nothing to say hands back nil, and every job keys on the commit.
	workflows, commit, tree, err := workflowsAndTree(ctx, s.Source, req.Repo, req.Ref)
	if req.Commit == "" {
		req.Commit = commit
	}
	if err != nil {
		if isWorkflowDefinitionError(err) {
			return s.recordWorkflowFailure(req, err), nil
		}
		return Run{}, fmt.Errorf("ci: reading %s's workflows at %s: %w", req.Repo, req.Ref, err)
	}
	req = s.vouchForCommit(ctx, req, commit)
	if gatesOnBase(req) {
		if err := s.checkBaseGate(ctx, req, workflows); err != nil {
			if isWorkflowDefinitionError(err) {
				return s.recordWorkflowFailure(req, err), nil
			}
			return Run{}, err
		}
	}
	plan, err := PlanRunIn(req, workflows, tree)
	if err != nil {
		if isWorkflowDefinitionError(err) {
			return s.recordWorkflowFailure(req, err), nil
		}
		return Run{}, err
	}

	if live, ok := s.store().Live(plan.Key); ok {
		s.log("ci: %s@%s: already running as %s", req.Repo, req.Ref, live.ID)
		return live, nil
	}

	run := Run{
		ID:          s.newID(),
		Key:         plan.Key,
		Repo:        req.Repo,
		Ref:         req.Ref,
		Commit:      req.Commit,
		Event:       req.Event,
		RequestedBy: req.RequestedBy,
		Recheck:     req.Recheck,
		StartedAt:   s.instant(),
		Build:       s.build(),
	}
	for _, pj := range plan.Jobs {
		run.Jobs = append(run.Jobs, newJobRun(pj))
	}
	s.store().Put(run)
	s.savePlan(run.ID, plan)

	s.launchRun(ctx, run, plan, nil, false)
	return run, nil
}

func (s *Service) launchRun(ctx context.Context, run Run, plan Plan, adopted map[string]AdoptedJob, isResume bool) {
	maxRunTime := s.MaxRunTime
	if maxRunTime <= 0 {
		maxRunTime = DefaultMaxRunTime
	}
	remaining := maxRunTime
	if isResume && run.StartedAt != nil {
		remaining = maxRunTime - s.now().Sub(*run.StartedAt)
		if remaining <= 0 {
			remaining = time.Minute
		}
	}
	if s.Stopping != nil && s.Engine != nil && s.Engine.Stopping == nil {
		s.Engine.Stopping = s.Stopping
	}
	baseCtx, detach := WithDetach(context.WithoutCancel(ctx))
	runCtx, cancel := context.WithTimeout(baseCtx, remaining)
	s.mu.Lock()
	if s.cancels == nil {
		s.cancels = map[string]context.CancelFunc{}
	}
	if s.detaches == nil {
		s.detaches = map[string]context.CancelFunc{}
	}
	s.cancels[run.ID] = cancel
	s.detaches[run.ID] = detach
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		defer cancel()
		defer func() {
			s.mu.Lock()
			delete(s.cancels, run.ID)
			delete(s.detaches, run.ID)
			delete(s.cancelled, run.ID)
			s.mu.Unlock()
		}()
		observe := func(job JobRun) {
			s.store().Update(run.ID, func(r *Run) {
				for i := range r.Jobs {
					if r.Jobs[i].WorkflowPath == job.WorkflowPath && r.Jobs[i].JobID == job.JobID {
						r.Jobs[i] = job
						return
					}
				}
			})
		}
		var finished Run
		if isResume {
			finished = s.Engine.Resume(runCtx, run, plan, adopted, observe)
		} else {
			finished = s.Engine.Run(runCtx, run.ID, plan, observe)
		}
		if isDetaching(runCtx) && !finished.Interrupted && finished.Conclusion != ConclusionSuccess {
			return
		}
		s.removePlan(run.ID)
		finished.ID, finished.StartedAt, finished.Build = run.ID, run.StartedAt, run.Build
		if !finished.Interrupted {
			asked := s.cancellationOf(run.ID)
			finished.CancelReason, finished.CancelledBy = asked.reason, asked.by
		}
		s.store().Put(finished)
		s.log("ci: run %s: %s@%s: %s", run.ID, run.Repo, run.Ref, finished.Conclusion)
	}()
}

// IsStopping reports whether the service's hosting daemon has begun shutting down.
func (s *Service) IsStopping() bool {
	if s == nil {
		return false
	}
	if s.Stopping != nil {
		select {
		case <-s.Stopping:
			return true
		default:
		}
	}
	if s.Engine != nil && s.Engine.Stopping != nil {
		select {
		case <-s.Engine.Stopping:
			return true
		default:
		}
	}
	return false
}

// Adopt resumes an unconcluded run across a daemon restart, reattaching to
// the in-flight jobs in adopted and scheduling any remaining jobs in plan.
func (s *Service) Adopt(ctx context.Context, run Run, plan Plan, adopted map[string]AdoptedJob) error {
	if s == nil || s.Engine == nil {
		return fmt.Errorf("ci: this deployment has no CI engine wired")
	}
	if s.IsStopping() {
		return nil
	}
	s.mu.Lock()
	_, alreadyLive := s.cancels[run.ID]
	s.mu.Unlock()
	if alreadyLive {
		return nil
	}
	s.savePlan(run.ID, plan)
	s.launchRun(ctx, run, plan, adopted, true)
	return nil
}

// Detach detaches all in-flight runs without cancelling them or releasing
// their sandboxes, and waits for their goroutines to unwind so the store is
// left with unconcluded run and job records for the next daemon to readopt.
func (s *Service) Detach() {
	if s == nil {
		return
	}
	s.mu.Lock()
	fns := make([]context.CancelFunc, 0, len(s.detaches))
	for _, fn := range s.detaches {
		fns = append(fns, fn)
	}
	s.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
	s.wg.Wait()
}

type persistedPlan struct {
	Plan                   Plan     `json:"plan"`
	Capabilities           []string `json:"capabilities,omitempty"`
	TrustedRelease         bool     `json:"trusted_release,omitempty"`
	TrustedSchedule        bool     `json:"trusted_schedule,omitempty"`
	ReleaseSecrets         []string `json:"release_secrets,omitempty"`
	WithholdEventGated     bool     `json:"withhold_event_gated,omitempty"`
	WithholdReleaseSecrets bool     `json:"withhold_release_secrets,omitempty"`
	WithholdReason         string   `json:"withhold_reason,omitempty"`
	ReleaseRefVouched      bool     `json:"release_ref_vouched,omitempty"`
	LifecycleImage         string   `json:"lifecycle_image,omitempty"`
	DeployedImages         []string `json:"deployed_images,omitempty"`
	SandboxImage           string   `json:"sandbox_image,omitempty"`
	Priority               Priority `json:"priority,omitempty"`
	GateOnBase             bool     `json:"gate_on_base,omitempty"`
	BaseRef                string   `json:"base_ref,omitempty"`
	Recheck                bool     `json:"recheck,omitempty"`
	UntrustedCommit        string   `json:"untrusted_commit,omitempty"`
}

func (s *Service) planPath(runID string) string {
	if s == nil || s.PlanDir == "" || runID == "" {
		return ""
	}
	return filepath.Join(s.PlanDir, filepath.Base(runID)+".json")
}

func (s *Service) savePlan(runID string, plan Plan) {
	p := s.planPath(runID)
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	req := plan.Request
	wrapped := persistedPlan{
		Plan:                   plan,
		Capabilities:           req.Capabilities,
		TrustedRelease:         req.TrustedRelease,
		TrustedSchedule:        req.TrustedSchedule,
		ReleaseSecrets:         req.ReleaseSecrets,
		WithholdEventGated:     req.WithholdEventGated,
		WithholdReleaseSecrets: req.WithholdReleaseSecrets,
		WithholdReason:         req.WithholdReason,
		ReleaseRefVouched:      req.ReleaseRefVouched,
		LifecycleImage:         req.LifecycleImage,
		DeployedImages:         req.DeployedImages,
		SandboxImage:           req.SandboxImage,
		Priority:               req.Priority,
		GateOnBase:             req.GateOnBase,
		BaseRef:                req.BaseRef,
		Recheck:                req.Recheck,
		UntrustedCommit:        req.UntrustedCommit(),
	}
	data, err := json.Marshal(wrapped)
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

func (s *Service) loadPlan(runID string) (Plan, bool) {
	p := s.planPath(runID)
	if p == "" {
		return Plan{}, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Plan{}, false
	}
	var wrapped persistedPlan
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return Plan{}, false
	}
	plan := wrapped.Plan
	plan.Request.Capabilities = wrapped.Capabilities
	plan.Request.TrustedRelease = wrapped.TrustedRelease
	plan.Request.TrustedSchedule = wrapped.TrustedSchedule
	plan.Request.ReleaseSecrets = wrapped.ReleaseSecrets
	plan.Request.WithholdEventGated = wrapped.WithholdEventGated
	plan.Request.WithholdReleaseSecrets = wrapped.WithholdReleaseSecrets
	plan.Request.WithholdReason = wrapped.WithholdReason
	plan.Request.ReleaseRefVouched = wrapped.ReleaseRefVouched
	plan.Request.LifecycleImage = wrapped.LifecycleImage
	plan.Request.DeployedImages = wrapped.DeployedImages
	plan.Request.SandboxImage = wrapped.SandboxImage
	plan.Request.Priority = wrapped.Priority
	plan.Request.GateOnBase = wrapped.GateOnBase
	plan.Request.BaseRef = wrapped.BaseRef
	plan.Request.Recheck = wrapped.Recheck
	plan.Request = plan.Request.WithUntrustedCommit(wrapped.UntrustedCommit)
	return plan, true
}

func (s *Service) removePlan(runID string) {
	if p := s.planPath(runID); p != "" {
		_ = os.Remove(p)
	}
}

// ReconstructPlan loads a persisted Plan for run from PlanDir, or re-plans it
// from Source if no plan file was persisted (for example, on the first upgrade
// from a daemon build before PlanDir existed).
//
// Re-planning from Source re-derives only what Start would grant anybody:
// a run record does not say whether Start trusted a release or a schedule,
// whether its caller vouched for a release's ref, or whether grain withheld
// release secrets from it, so a release, schedule or manual run with no
// persisted plan is refused here (and recorded as interrupted, to be run
// again by whoever started it) rather than re-planned with trust it may
// never have had. A push or pull-request run is re-planned the way Start
// plans one: workflows read at the branch, and capabilities withheld from
// a pinned commit the branch does not hold (vouchForCommit).
func (s *Service) ReconstructPlan(ctx context.Context, run Run) (Plan, error) {
	if plan, ok := s.loadPlan(run.ID); ok && len(plan.Jobs) > 0 {
		return plan, nil
	}
	if s == nil || s.Source == nil {
		return Plan{}, fmt.Errorf("ci: cannot reconstruct plan for run %s without a workflow source", run.ID)
	}
	switch run.Event {
	case EventPush, EventPullRequest:
	default:
		return Plan{}, fmt.Errorf("ci: run %s is a %s run with no persisted plan, and the trust Start gave it cannot be re-derived from its record", run.ID, run.Event)
	}
	req := Request{
		Repo:        run.Repo,
		Ref:         run.Ref,
		Commit:      run.Commit,
		Event:       run.Event,
		RequestedBy: run.RequestedBy,
		Recheck:     run.Recheck,
	}
	if s.Capabilities != nil {
		ids, err := s.Capabilities(ctx, req.Repo)
		if err != nil {
			return Plan{}, fmt.Errorf("ci: reading %s's capabilities: %w", req.Repo, err)
		}
		req.Capabilities = ids
	}
	if s.SandboxImage != nil {
		req.SandboxImage = s.SandboxImage()
	}
	req = req.Normalize()
	workflows, commit, tree, err := workflowsAndTree(ctx, s.Source, req.Repo, req.Ref)
	if err != nil {
		return Plan{}, err
	}
	req = s.vouchForCommit(ctx, req, commit)
	plan, err := PlanRunIn(req, workflows, tree)
	if err != nil {
		return Plan{}, err
	}
	// Engine.Resume concludes the run on plan.Jobs alone, so a job the
	// record holds and the re-plan does not -- the branch moved and its
	// workflows dropped it, or the run was started for a subset of jobs
	// this full re-plan widens -- would leave the verdict without it
	// (a cached failure included). Only the very job set Start planned
	// is resumed; anything else is recorded as interrupted, as before.
	if len(run.Jobs) > 0 && !sameJobSet(run.Jobs, plan.Jobs) {
		return Plan{}, fmt.Errorf("ci: run %s's re-planned jobs are not the jobs it was started with", run.ID)
	}
	return plan, nil
}

// sameJobSet reports whether recorded and planned name exactly the same
// jobs, each by its workflow path and job id.
func sameJobSet(recorded []JobRun, planned []PlannedJob) bool {
	if len(recorded) != len(planned) {
		return false
	}
	want := make(map[string]bool, len(planned))
	for _, pj := range planned {
		want[pj.WorkflowPath+"\x00"+pj.Job.ID] = true
	}
	for _, j := range recorded {
		k := j.WorkflowPath + "\x00" + j.JobID
		if !want[k] {
			return false
		}
		delete(want, k)
	}
	return len(want) == 0
}

// recordWorkflowFailure turns a deterministic workflow-definition error into
// a durable verdict. Returning the completed run lets manual callers see it
// immediately and lets the merge reconciler record it against the task's head
// instead of logging the same error on every cycle with no visible CI run.
func (s *Service) recordWorkflowFailure(req Request, err error) Run {
	now := s.now()
	run := Run{
		ID: s.newID(),
		Key: HashOf("workflow-error", struct {
			Repo, Ref, Commit string
			Event             Event
			Error             string
		}{req.Repo, req.Ref, req.Commit, req.Event, err.Error()}),
		Repo:        req.Repo,
		Ref:         req.Ref,
		Commit:      req.Commit,
		Event:       req.Event,
		RequestedBy: req.RequestedBy,
		StartedAt:   &now,
		FinishedAt:  &now,
		Conclusion:  ConclusionFailure,
		Error:       err.Error(),
		// Stamped like any other run: a verdict this build reached about
		// a workflow it could not read is exactly the kind of run whose
		// reader wants to know which build reached it.
		Build: s.build(),
	}
	s.store().Put(run)
	s.log("ci: run %s: %s@%s: workflow configuration failed: %s", run.ID, run.Repo, run.Ref, run.Error)
	return run
}

// Get returns one run by id.
func (s *Service) Get(id string) (Run, bool) { return s.store().Get(id) }

// MarkFlake records that the named jobs of run id failed and then passed
// an uncached recheck of the same tree (rechecks, the recheck's run id):
// each is marked JobRun.Flake, which is what QueryTestHistory reads as a
// flake rather than a failure, with a warning saying which run cleared
// it. jobs are plan-wide ids ("<workflow path>:<job id>") or bare job
// ids. It reports whether the run was still held to mark.
func (s *Service) MarkFlake(id string, jobs []string, rechecks string) bool {
	if _, ok := s.store().Get(id); !ok {
		return false
	}
	want := map[string]bool{}
	for _, j := range jobs {
		want[j] = true
	}
	s.store().Update(id, func(run *Run) {
		for i := range run.Jobs {
			job := &run.Jobs[i]
			if job.Conclusion == ConclusionSuccess || !(want[job.JobID] || want[job.WorkflowPath+":"+job.JobID]) {
				continue
			}
			job.Flake = true
			job.Warning = fmt.Sprintf("flaky: failed here and passed an uncached recheck of the same tree (run %s)", rechecks)
		}
	})
	return true
}

// List returns the runs this service still holds, newest first.
func (s *Service) List() []Run { return s.store().List() }

// Find returns the runs matching f, in the order f asks for and at most
// limit of them, asked of everything this service's store holds rather
// than of the page List answers with (RunStore.Find).
func (s *Service) Find(f RunFilter, limit int) RunPage { return s.store().Find(f, limit) }

// CacheStats is what this service's job cache is holding, and ok is
// whether there is one to ask: a deployment can be running CI with no
// cache at all (the engine's Cache is nil, or the directory could not be
// opened and every job runs), and a cache can be one that does not
// answer this question (InspectableCache). Both are "there is nothing
// behind this", which a reader is told rather than shown as an empty
// cache.
func (s *Service) CacheStats(ctx context.Context) (CacheStats, bool, error) {
	cache, ok := s.inspectableCache()
	if !ok {
		return CacheStats{}, false, nil
	}
	stats, err := cache.Stats(ctx)
	if err != nil {
		return CacheStats{}, true, err
	}
	return stats, true, nil
}

// ClearCache empties the job cache, returning how many entries it
// removed and whether there was a cache to empty at all.
//
// Nothing is stopped or waited for. A run in flight that has already had
// a hit keeps its result -- the answer was correct when it was served
// and clearing does not make it wrong -- and a job that looks after this
// simply misses and runs, which is the whole of what clearing costs.
func (s *Service) ClearCache(ctx context.Context) (int, bool, error) {
	cache, ok := s.inspectableCache()
	if !ok {
		return 0, false, nil
	}
	// The cache logs the clearing itself, with the directory in it, so
	// there is nothing to repeat here.
	removed, err := cache.Clear(ctx)
	return removed, true, err
}

// EvictBaseTransitionFailures purges cached negative verdicts tied to baseCommit
// from the job cache, returning how many entries were removed and whether there was
// a cache to purge.
func (s *Service) EvictBaseTransitionFailures(ctx context.Context, baseCommit string) (int, bool, error) {
	cache, ok := s.inspectableCache()
	if !ok {
		return 0, false, nil
	}
	if evictor, ok := cache.(BaseTransitionCacheEvictor); ok {
		n, err := evictor.PurgeNegativeEntries(ctx, baseCommit)
		return n, true, err
	}
	return 0, true, nil
}

// ClearArtifacts drops everything failing jobs have left behind,
// returning how many artifacts went, how many bytes that freed, and
// whether there was a store to empty at all (grain/task-213).
//
// The three-state answer ClearCache gives, and the same "nothing is
// stopped or waited for": a run in flight goes on collecting, and its
// own artifacts land after this in a directory this has already been
// through. What is gone is gone -- see ClearableArtifacts for why this
// is reached for by a deployment reclaiming disk and by nothing else.
func (s *Service) ClearArtifacts(ctx context.Context) (int, int64, bool, error) {
	store, ok := s.artifacts()
	if !ok {
		return 0, 0, false, nil
	}
	clearable, ok := store.(ClearableArtifacts)
	if !ok {
		return 0, 0, false, nil
	}
	// The store logs the clearing itself, with the directory in it, so
	// there is nothing to repeat here.
	removed, freed, err := clearable.Clear(ctx)
	return removed, freed, true, err
}

// ArtifactStats is what this service's artifact store is holding, and ok
// is whether there is one to ask -- CacheStats' own three-state answer,
// for the same reason: a deployment that keeps no artifacts at all and
// one whose store is empty are different facts, and a pane that showed
// both as "0 B" would be telling an operator hunting for disk that there
// is nothing here to find.
//
// Read-only, and that is the whole of what this deployment offers over
// artifacts: they are the only copy of what a failing job produced
// (InspectableArtifacts).
func (s *Service) ArtifactStats(ctx context.Context) (ArtifactStats, bool, error) {
	store, ok := s.artifacts()
	if !ok {
		return ArtifactStats{}, false, nil
	}
	inspectable, ok := store.(InspectableArtifacts)
	if !ok {
		return ArtifactStats{}, false, nil
	}
	stats, err := inspectable.Stats(ctx)
	if err != nil {
		return ArtifactStats{}, true, err
	}
	return stats, true, nil
}

// RunArtifacts is what one run has left to download, and ok is whether
// this deployment keeps artifacts at all -- the three-state answer
// CacheStats gives, and for the same reason: "this deployment collects
// nothing" and "this run kept nothing" send a reader to different
// places.
//
// It answers from the store rather than from the run's own record,
// because the record is what was true when the run finished and the
// store is what is true now (JobRun.Artifacts says more about why).
func (s *Service) RunArtifacts(ctx context.Context, runID string) ([]Artifact, bool, error) {
	store, ok := s.artifacts()
	if !ok {
		return nil, false, nil
	}
	artifacts, err := store.List(ctx, runID)
	if err != nil {
		return nil, true, err
	}
	return artifacts, true, nil
}

// OpenArtifact hands back one artifact's bytes for whatever is serving a
// download. The caller closes the reader.
func (s *Service) OpenArtifact(ctx context.Context, ref ArtifactRef) (io.ReadCloser, Artifact, error) {
	store, ok := s.artifacts()
	if !ok {
		return nil, Artifact{}, fmt.Errorf("ci: this deployment keeps no CI artifacts")
	}
	return store.Open(ctx, ref)
}

// WithArtifacts fills a run's jobs in with what is actually still on
// disk, so that whatever hands a run to a reader is offering downloads
// that exist. A run read back out of the database after a restart has no
// artifacts recorded on it at all (JobRun.Artifacts is not a stored
// column); a run whose files have been swept has them recorded and
// nothing behind them. Both come out of here right.
//
// A store that cannot be read leaves the run exactly as it was: a CI run
// nobody can read because its artifact directory is broken would be a
// poor trade for a feature whose whole purpose is to make a failure
// easier to read.
func (s *Service) WithArtifacts(ctx context.Context, run Run) Run {
	artifacts, ok, err := s.RunArtifacts(ctx, run.ID)
	if !ok || err != nil {
		if err != nil {
			s.log("ci: run %s: reading its artifacts: %v", run.ID, err)
		}
		return run
	}
	byJob := make(map[string][]Artifact, len(artifacts))
	for _, a := range artifacts {
		byJob[a.Job] = append(byJob[a.Job], a)
	}
	jobs := make([]JobRun, len(run.Jobs))
	copy(jobs, run.Jobs)
	for i := range jobs {
		// The notes from the run itself are kept for the declarations
		// that produced no file -- "280MB, over the 32MB an artifact may
		// be" is the answer somebody is looking for -- and everything
		// that claims to be downloadable comes from the store.
		var merged []Artifact
		for _, a := range jobs[i].Artifacts {
			if !a.Stored {
				merged = append(merged, a)
			}
		}
		merged = append(merged, byJob[jobs[i].JobID]...)
		jobs[i].Artifacts = merged
	}
	run.Jobs = jobs
	return run
}

// artifacts is this service's artifact store, and whether there is one.
// It lives on the Engine -- that is what collects them -- and is reached
// through here for the same reason inspectableCache is: an API asks the
// service, not the engine.
func (s *Service) artifacts() (ArtifactStore, bool) {
	if s.Engine == nil || s.Engine.Artifacts == nil {
		return nil, false
	}
	return s.Engine.Artifacts, true
}

func (s *Service) inspectableCache() (InspectableCache, bool) {
	if s.Engine == nil {
		return nil, false
	}
	cache, ok := s.Engine.Cache.(InspectableCache)
	return cache, ok
}

// Cancel ends a run that is still going. It reports whether there was
// one to cancel; the run's own record turns cancelled once its jobs
// notice, which is the same path a shutdown takes.
//
// reason is an optional note about why, recorded on the run and kept
// through the run's own ending: a person stopping a run is the only
// thing that knows why it was stopped, and everything downstream of a
// cancellation -- the verdict written on the task, the sentence an agent
// waiting on wait_for_checks is given -- otherwise has nothing to say
// beyond the word "cancelled" (Run.CancelReason). Empty is the ordinary
// case and says nothing extra anywhere.
//
// by is who asked, and it is not optional in the same way: it decides
// whether anything ever starts another run for this commit
// (Run.CancelledBy, orchestrator.reconcileTaskCI). Grain's own callers
// pass CancelBySystem; only the route a person reaches passes
// CancelByOperator.
//
// Both are written on the record immediately as well as kept, so a
// reader polling the run while its jobs are still giving up sees them
// rather than waiting for the conclusion to catch up.
func (s *Service) Cancel(id, reason string, by CancelSource) bool {
	reason = strings.TrimSpace(reason)
	asked := cancellation{by: by, reason: reason}
	s.mu.Lock()
	cancel, ok := s.cancels[id]
	if ok && asked != (cancellation{}) {
		if s.cancelled == nil {
			s.cancelled = map[string]cancellation{}
		}
		s.cancelled[id] = asked
	}
	s.mu.Unlock()
	if !ok {
		return false
	}
	if asked != (cancellation{}) {
		s.store().Update(id, func(r *Run) { r.CancelReason, r.CancelledBy = asked.reason, asked.by })
	}
	cancel()
	return true
}

// cancellationOf is what Cancel was told about this run, zero for a run
// that ended for any other reason -- a timeout, a daemon stopping under
// it, or no cancellation at all.
func (s *Service) cancellationOf(id string) cancellation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelled[id]
}
