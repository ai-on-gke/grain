// loadtest_helpers_test.go holds the test doubles and bookkeeping
// loadtest_test.go's TestLoadSustainedConcurrency wires together: a
// minimal fake github.Client (this harness never pushes a branch, so it
// never needs more than BranchExists -- see that file's own doc comment
// for why), a fake capability provider that mints and (mostly) revokes a
// resource per dispatch so a load run can prove nothing leaks, a
// randomized agent script generator, and the metrics loadtest_test.go's
// own verdict is built from.
package e2e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// --- fake github.Client -----------------------------------------------

// loadGitHub answers every github.Client method this harness's own
// dispatches can reach honestly (BranchExists, always false: nothing
// here ever pushes -- see loadtest_test.go's own doc comment on why not)
// and refuses every other one loudly. reconcileSync and reconcileReleases
// both read the store first (OpenPullRequestLinks, PendingCandidates) and
// never call the client at all once those come back empty, which they
// always do here -- no dispatch in this file ever records a LinkFixes
// link or a release candidate. A refusal firing at all would mean that
// assumption broke, which is worth failing loudly on rather than quietly
// answering a plausible-looking canned value for a code path this
// harness never meant to exercise.
type loadGitHub struct{}

func newLoadGitHub() *loadGitHub { return &loadGitHub{} }

func (g *loadGitHub) unsupported(method string) error {
	return fmt.Errorf("loadtest: fake github client: %s is not supported -- "+
		"this harness never expected to reach it (see loadGitHub's own doc comment)", method)
}

func (g *loadGitHub) ListIssues(owner, repo, label string) ([]github.Issue, error) {
	return nil, g.unsupported("ListIssues")
}
func (g *loadGitHub) GetIssue(owner, repo string, number int) (github.Issue, error) {
	return github.Issue{}, g.unsupported("GetIssue")
}
func (g *loadGitHub) AddLabel(owner, repo string, number int, label string) error {
	return g.unsupported("AddLabel")
}
func (g *loadGitHub) RemoveLabel(owner, repo string, number int, label string) error {
	return g.unsupported("RemoveLabel")
}
func (g *loadGitHub) CloseIssue(owner, repo string, number int) error {
	return g.unsupported("CloseIssue")
}
func (g *loadGitHub) ReopenIssue(owner, repo string, number int) error {
	return g.unsupported("ReopenIssue")
}
func (g *loadGitHub) UpdateIssue(owner, repo string, number int, title, body *string) error {
	return g.unsupported("UpdateIssue")
}
func (g *loadGitHub) BranchExists(owner, repo, branch string) (bool, error) {
	return false, nil
}
func (g *loadGitHub) GetBranchHead(owner, repo, branch string) (*github.BranchHead, error) {
	return nil, nil
}
func (g *loadGitHub) CompareCommits(owner, repo, base, head string) ([]github.Commit, error) {
	return nil, g.unsupported("CompareCommits")
}
func (g *loadGitHub) CompareRefs(owner, repo, base, head string) (github.Comparison, error) {
	return github.Comparison{}, g.unsupported("CompareRefs")
}
func (g *loadGitHub) CreateBranch(owner, repo, branch, sha string) error {
	return g.unsupported("CreateBranch")
}
func (g *loadGitHub) UpdateBranch(owner, repo, branch, sha string, force bool) error {
	return g.unsupported("UpdateBranch")
}
func (g *loadGitHub) DeleteBranch(owner, repo, branch string) error {
	return g.unsupported("DeleteBranch")
}
func (g *loadGitHub) CreatePullRequest(owner, repo, head, base, title, body string) (github.PullRequest, error) {
	return github.PullRequest{}, g.unsupported("CreatePullRequest")
}
func (g *loadGitHub) UpdatePullRequestBody(owner, repo string, number int, body string) error {
	return g.unsupported("UpdatePullRequestBody")
}
func (g *loadGitHub) UpdatePullRequestTitle(owner, repo string, number int, title string) error {
	return g.unsupported("UpdatePullRequestTitle")
}
func (g *loadGitHub) ClosePullRequest(owner, repo string, number int) error {
	return g.unsupported("ClosePullRequest")
}
func (g *loadGitHub) FindOpenPullRequestForBranch(owner, repo, branch string) (*github.PullRequest, error) {
	return nil, nil
}
func (g *loadGitHub) MergedPullRequestForCommit(owner, repo, sha string) (*github.PullRequest, error) {
	return nil, g.unsupported("MergedPullRequestForCommit")
}
func (g *loadGitHub) CreateIssue(owner, repo, title, body string, labels []string) (github.Issue, error) {
	return github.Issue{}, g.unsupported("CreateIssue")
}
func (g *loadGitHub) MergePullRequest(owner, repo string, number int, headSHA string) error {
	return g.unsupported("MergePullRequest")
}
func (g *loadGitHub) MergeBranch(owner, repo, base, head, commitMessage string) (github.MergeResult, error) {
	return github.MergeResult{}, g.unsupported("MergeBranch")
}
func (g *loadGitHub) GetPullRequest(owner, repo string, number int) (github.PullRequestDetail, error) {
	return github.PullRequestDetail{}, g.unsupported("GetPullRequest")
}
func (g *loadGitHub) DefaultBranch(owner, repo string) (string, error) {
	return "main", nil
}
func (g *loadGitHub) SetDefaultBranch(owner, repo, branch string) error {
	return g.unsupported("SetDefaultBranch")
}
func (g *loadGitHub) ListReviewComments(owner, repo string, number int) ([]github.ReviewComment, error) {
	return nil, g.unsupported("ListReviewComments")
}
func (g *loadGitHub) ListPullRequestFiles(owner, repo string, number int) ([]string, error) {
	return nil, g.unsupported("ListPullRequestFiles")
}
func (g *loadGitHub) ListPullRequestCommits(owner, repo string, number int) ([]github.Commit, error) {
	return nil, g.unsupported("ListPullRequestCommits")
}
func (g *loadGitHub) ListCheckRuns(owner, repo, ref string) ([]github.CheckRun, error) {
	return nil, g.unsupported("ListCheckRuns")
}
func (g *loadGitHub) ListWorkflowRuns(owner, repo, headSHA string) ([]github.CheckRun, error) {
	return nil, g.unsupported("ListWorkflowRuns")
}
func (g *loadGitHub) FailedJobLogs(owner, repo, headSHA string) ([]github.JobLog, error) {
	return nil, g.unsupported("FailedJobLogs")
}
func (g *loadGitHub) ActionsState(owner, repo, headSHA string) (github.ActionsState, error) {
	return github.ActionsState{}, g.unsupported("ActionsState")
}
func (g *loadGitHub) RecentRunStarts(owner, repo string, limit int) (github.RunStarts, error) {
	return github.RunStarts{}, g.unsupported("RecentRunStarts")
}
func (g *loadGitHub) ListComments(owner, repo string, number int) ([]github.Comment, error) {
	return nil, g.unsupported("ListComments")
}
func (g *loadGitHub) CreateComment(owner, repo string, number int, body string) (int, error) {
	return 0, g.unsupported("CreateComment")
}
func (g *loadGitHub) CreateReview(owner, repo string, number int, body string, comments []github.NewReviewComment) (int, error) {
	return 0, g.unsupported("CreateReview")
}

// --- fake capability provider -------------------------------------------

const loadCapabilityName = "loadtest-resource"

// loadCapability stands in for a real MINT capability (gcpkey,
// geminikey): Materialize mints a resource identified by the run that
// asked for it and remembers it in its own bookkeeping -- never grain's
// store, deliberately, the same as every real Reaper (model.Reaper's own
// doc comment: "consults the resource's own source of truth ... never
// grain's store"). Revoke deletes it back out, except for a configurable
// fraction of calls that fail on purpose, standing in for a real
// provider's own API call timing out or a controller crashing between
// mint and revoke -- both are the failure Reap exists to clean up after
// on its own schedule (cmd/grain daemon.go's own reapCapabilities), which
// is exactly what this file's own TestLoadSustainedConcurrency checks
// actually happens under sustained concurrent dispatch rather than only
// in a single-run unit test.
type loadCapability struct {
	model.BaseCapability // Resolve always Honoured, PromptSection empty -- this fake needs neither.

	revokeFailRate float64
	reapAfter      time.Duration

	mu          sync.Mutex
	outstanding map[string]time.Time // resource -> minted at
	minted      int
	revoked     int
	revokeFails int
	reaped      int
}

func newLoadCapability(revokeFailRate float64, reapAfter time.Duration) *loadCapability {
	return &loadCapability{
		revokeFailRate: revokeFailRate,
		reapAfter:      reapAfter,
		outstanding:    map[string]time.Time{},
	}
}

func (p *loadCapability) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        loadCapabilityName,
		Label:       "Load-test resource",
		Description: "a fake, in-memory MINT capability that exists only to prove nothing leaks under sustained concurrent dispatch",
		Source:      model.GrantByGrain,
		Provision:   model.ProvisionMint,
		MaxLease:    time.Hour,
	}
}

func (p *loadCapability) Materialize(ctx context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	resource := cc.Run.ID
	now := time.Now().UTC()
	p.mu.Lock()
	p.outstanding[resource] = now
	p.minted++
	p.mu.Unlock()
	return model.Materialization{
		Lease: &model.Lease{
			Capability: loadCapabilityName,
			Resource:   resource,
			MintedBy:   model.CredentialRef{Name: "loadtest-minter"},
			IssuedAt:   now,
		},
	}, nil
}

func (p *loadCapability) Revoke(ctx context.Context, cc model.CapabilityContext, lease model.Lease) error {
	if rand.Float64() < p.revokeFailRate {
		p.mu.Lock()
		p.revokeFails++
		p.mu.Unlock()
		return fmt.Errorf("loadtest: simulated revoke failure for %s", lease.Resource)
	}
	p.mu.Lock()
	delete(p.outstanding, lease.Resource)
	p.revoked++
	p.mu.Unlock()
	return nil
}

// Reap deletes everything minted before the caller's shared trailing edge.
func (p *loadCapability) Reap(ctx context.Context, creds model.CredentialResolver, before time.Time) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var deleted []string
	for resource, mintedAt := range p.outstanding {
		if mintedAt.Before(before) {
			deleted = append(deleted, resource)
			delete(p.outstanding, resource)
			p.reaped++
		}
	}
	return deleted, nil
}

type loadCapabilityStats struct{ Minted, Revoked, RevokeFails, Reaped, StillOutstanding int }

func (p *loadCapability) Stats() loadCapabilityStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return loadCapabilityStats{
		Minted: p.minted, Revoked: p.revoked, RevokeFails: p.revokeFails,
		Reaped: p.reaped, StillOutstanding: len(p.outstanding),
	}
}

// --- scripted agent turns ------------------------------------------------

type loadOutcomeKind string

const (
	loadOutcomeClose loadOutcomeKind = "closed"
	loadOutcomeFail  loadOutcomeKind = "failed"
	loadOutcomeAsk   loadOutcomeKind = "asked"
)

// loadGenerator is this file's own randomGenerator (random_test.go),
// trimmed to the three outcomes this harness needs. There is no push
// script: e2e_test.go, random_test.go and pkg/orchestrator/live_test.go
// already exercise the git/pull-request path at scale enough to trust,
// and it is not what bwsalmon/agents#416 asks this file to catch --
// scheduling, sqlite and capability behavior under load, all three of
// which "closed"/"failed"/"asked" already exercise identically to a real
// push (dispatch.Cycle, RunDispatch, capability materialize/revoke,
// FinishRun all run exactly the same regardless of which of the four
// outcomes a dispatch ends in). Every script opens with a real
// `sleep`, a genuine (if brief) subprocess through NewSandboxTools'
// run_command, standing in for the real work a dispatched agent would
// spend most of its own wall time doing.
type loadGenerator struct {
	// rngMu guards every rng access below: orchestrator.RunCycle now runs
	// a tick's dispatches concurrently (bwsalmon/agents#435), and every
	// loadGenerator a tick's Framework factory hands out -- one per
	// dispatch -- shares this same tickerRNG (loadtest_test.go), unlike
	// script/calls/start/reported below, which are this loadGenerator's
	// own and never touched by any other goroutine (one Framework, and so
	// one loadGenerator, is only ever used for one run).
	rngMu   *sync.Mutex
	rng     *rand.Rand
	metrics *loadMetrics

	script   []antigravity.Step
	calls    int
	start    time.Time
	reported bool
}

func newLoadGenerator(rngMu *sync.Mutex, rng *rand.Rand, metrics *loadMetrics) *loadGenerator {
	return &loadGenerator{rngMu: rngMu, rng: rng, metrics: metrics}
}

// Next's own timing -- start at the first call, reported once the script
// is exhausted -- is this file's only window onto how long a
// dispatch actually ran in wall-clock terms: model.Run's own
// StartedAt/FinishedAt (task_run) are both stamped with RunCycle's own
// tick-start `now` (dispatch.Cycle and RunDispatch never call time.Now()
// themselves), so they read as zero-duration regardless of how long a
// run actually took and cannot answer whether two dispatches actually
// overlapped. See loadtest_test.go's own reportConcurrency for what this
// is measured for.
// Next implements antigravity.Script.
func (g *loadGenerator) Next(string) (antigravity.Step, bool) {
	if g.script == nil {
		g.start = time.Now()
		g.rngMu.Lock()
		workSeconds := 0.01 + g.rng.Float64()*0.04
		r := g.rng.Float64()
		g.rngMu.Unlock()
		work := toolCall("run_command", map[string]any{"command": fmt.Sprintf("sleep %.3f", workSeconds)})
		switch {
		case r < 0.5:
			g.script = []antigravity.Step{
				work,
				toolCall("comment_on_issue", map[string]any{"comment": "done -- nothing to push, just an answer"}),
				finalText("closed out"),
			}
			g.metrics.recordOutcome(loadOutcomeClose)
		case r < 0.75:
			g.script = []antigravity.Step{
				work,
				toolCall("run_command", map[string]any{"command": "echo simulated load-test failure >&2; exit 1"}),
				finalText("could not complete the task"),
			}
			g.metrics.recordOutcome(loadOutcomeFail)
		default:
			g.script = []antigravity.Step{
				work,
				toolCall("ask_question", map[string]any{"question": "need direction to continue"}),
				finalText("waiting on a reply"),
			}
			g.metrics.recordOutcome(loadOutcomeAsk)
		}
	}
	if g.calls >= len(g.script) {
		g.calls++
		return antigravity.Step{}, false
	}
	step := g.script[g.calls]
	g.calls++
	if g.calls == len(g.script) && !g.reported {
		// The framework never calls Next again once a step carries no
		// tool call (the case above is dead in practice for that
		// reason) -- this handing back the script's own last entry, a
		// plain finalText, already happens after every tool call this
		// dispatch made (including the real `sleep`) has finished, so
		// this is the last moment this generator ever sees, not the
		// branch above.
		g.metrics.recordDispatchSpan(g.start, time.Now())
		g.reported = true
	}
	return step, true
}

// --- metrics --------------------------------------------------------------

// loadMetrics is this file's own equivalent of tests/loadtest.py's
// HostSample/Sampler/evaluate -- gathered from every goroutine this test
// runs (the RunCycle-ticking loop and every external writer), so every
// method here is safe for concurrent use.
type loadMetrics struct {
	mu sync.Mutex

	filed int
	// readySince/resolved together compute how long a task actually
	// waited between becoming dispatchable and leaving the ready queue
	// (dispatched, or otherwise resolved) -- this file's own scheduling-
	// starvation signal, sampled from outside RunCycle the same way a
	// real operator watching the store, not RunCycle's own internals,
	// would have to.
	readySince map[string]time.Time
	resolved   map[string]bool
	readyWait  []time.Duration
	maxReady   int

	writeLat  []time.Duration
	writeErrs int
	writeOps  int

	// How long a tick took is not here: RunCycle measures that itself,
	// into the orchestrator.CycleTimes ring loadtest_test.go hands its
	// Deps (reportCycles). All this counts is how many ticks were driven,
	// which is what reportCycles checks that ring against.
	ticks int

	outcomes map[loadOutcomeKind]int

	spans []dispatchSpan
}

// dispatchSpan is one dispatch's own observed wall-clock window, from
// loadGenerator's own doc comment on why this, not task_run, is what
// reportConcurrency measures overlap from.
type dispatchSpan struct{ start, end time.Time }

func newLoadMetrics() *loadMetrics {
	return &loadMetrics{
		readySince: map[string]time.Time{},
		resolved:   map[string]bool{},
		outcomes:   map[loadOutcomeKind]int{},
	}
}

func (m *loadMetrics) taskFiled() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.filed++
}

// taskBecameReady records when id first became dispatchable -- at
// creation for a task filed already approved, or at approval for one
// that started out proposed.
func (m *loadMetrics) taskBecameReady(id string, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.readySince[id]; !ok {
		m.readySince[id] = at
	}
}

// observeReadyTasks is called once per RunCycle tick with the store's own
// current ready list: any task this metrics object previously saw become
// ready that is no longer in it has left the queue -- dispatched, closed,
// or otherwise resolved -- and how long that took is recorded.
func (m *loadMetrics) observeReadyTasks(ready []string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(ready) > m.maxReady {
		m.maxReady = len(ready)
	}
	stillReady := make(map[string]bool, len(ready))
	for _, id := range ready {
		stillReady[id] = true
	}
	for id, since := range m.readySince {
		if m.resolved[id] || stillReady[id] {
			continue
		}
		m.readyWait = append(m.readyWait, now.Sub(since))
		m.resolved[id] = true
	}
}

func (m *loadMetrics) recordWrite(d time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writeOps++
	m.writeLat = append(m.writeLat, d)
	if err != nil {
		m.writeErrs++
	}
}

func (m *loadMetrics) tickRan() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ticks++
}

func (m *loadMetrics) tickCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ticks
}

func (m *loadMetrics) recordOutcome(k loadOutcomeKind) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outcomes[k]++
}

func (m *loadMetrics) recordDispatchSpan(start, end time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spans = append(m.spans, dispatchSpan{start: start, end: end})
}

// concurrencyRatio answers, empirically, the question this whole file
// exists to ask about scheduling: with several slots free and several
// tasks ready, did dispatched runs actually overlap in wall-clock time,
// or did they run one at a time regardless of how many slots were free?
// sumDurations over unionSpan is 1.0 if nothing ever overlapped and grows
// with real concurrency.
func (m *loadMetrics) concurrencyRatio() (ratio float64, n int) {
	m.mu.Lock()
	spans := append([]dispatchSpan(nil), m.spans...)
	m.mu.Unlock()

	if len(spans) == 0 {
		return 1, 0
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start.Before(spans[j].start) })

	var sumDurations time.Duration
	for _, sp := range spans {
		sumDurations += sp.end.Sub(sp.start)
	}
	var unionSpan time.Duration
	curStart, curEnd := spans[0].start, spans[0].end
	for _, sp := range spans[1:] {
		if sp.start.After(curEnd) {
			unionSpan += curEnd.Sub(curStart)
			curStart, curEnd = sp.start, sp.end
			continue
		}
		if sp.end.After(curEnd) {
			curEnd = sp.end
		}
	}
	unionSpan += curEnd.Sub(curStart)
	if unionSpan <= 0 {
		return 1, len(spans)
	}
	return float64(sumDurations) / float64(unionSpan), len(spans)
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

// durationStats is min/p50/p95/max over a copy of samples, sorted here so
// no caller has to remember to.
type durationStats struct {
	Min, P50, P95, Max time.Duration
	N                  int
}

func statsOf(samples []time.Duration) durationStats {
	if len(samples) == 0 {
		return durationStats{}
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return durationStats{
		Min: sorted[0], P50: percentile(sorted, 0.5), P95: percentile(sorted, 0.95),
		Max: sorted[len(sorted)-1], N: len(sorted),
	}
}

// snapshot is an immutable copy of everything report needs, taken under
// the lock once so formatting it never races a still-running goroutine.
type loadMetricsSnapshot struct {
	filed               int
	readyWait           durationStats
	maxReady            int
	stillUnresolved     int
	write               durationStats
	writeErrs, writeOps int
	outcomes            map[loadOutcomeKind]int
}

func (m *loadMetrics) snapshot() loadMetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	outcomes := make(map[loadOutcomeKind]int, len(m.outcomes))
	for k, v := range m.outcomes {
		outcomes[k] = v
	}
	return loadMetricsSnapshot{
		filed:           m.filed,
		readyWait:       statsOf(m.readyWait),
		maxReady:        m.maxReady,
		stillUnresolved: len(m.readySince) - len(m.resolved),
		write:           statsOf(m.writeLat),
		writeErrs:       m.writeErrs,
		writeOps:        m.writeOps,
		outcomes:        outcomes,
	}
}
