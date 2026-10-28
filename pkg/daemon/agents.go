package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/agent/antigravity"
	"github.com/bwsalmon/grain/pkg/agent/claude"
	"github.com/bwsalmon/grain/pkg/agent/codex"
	"github.com/bwsalmon/grain/pkg/agent/custom"
	"github.com/bwsalmon/grain/pkg/agent/sessionstore"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/modelexec"
	"github.com/bwsalmon/grain/pkg/modelproxy"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/secrets"
)

// agentFrameworks returns the factory orchestrator.Deps.Framework wants:
// the agent.Framework one dispatch is driven by, chosen per run rather
// than once at startup (bwsalmon/agents#615 built exactly one, here, and
// every run used it).
//
// Two things made per-run construction necessary. A task can now name a
// framework of its own (model.Task.AgentFramework) and override the
// deployment-wide default, so which one a run needs is not known until
// that run comes up; and both frameworks' credentials are settable from
// the UI now (pkg/ui's "Agent frameworks" section, writing the
// secrets.GeminiAPIKeySecret/ClaudeOAuthTokenSecret/OpenAIAPIKeySecret
// entries this reads),
// so the key a run authenticates with is whatever is in the secrets
// database at the moment it is dispatched -- not whatever a file held
// when this process started.
//
// The cost is one client construction per dispatch instead of one per
// process, which is nothing next to the run itself: every constructor
// resolves a binary and a credential and builds a struct.
func backendUnavailableKey(b model.AgentBackend) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s",
		b.ID, b.Framework, strings.Join(b.Models, ","), b.AuthMode, b.CredentialBackendID)
}

// teamHasAgentBackend is orchestrator.Deps.TeamHasAgentBackend for this
// deployment: whether a team has a connection of its own, stored or
// offered by one of its remote executors, that is not disabled -- the
// same pool agentFrameworks picks from, read the same way.
func teamHasAgentBackend(cfg Config, store *model.Store) func(context.Context, string) bool {
	return func(ctx context.Context, team string) bool {
		// The deployment's connections serve every team
		// (model.DeploymentAgentBackends).
		return len(model.DeploymentAgentBackends(DispatchConfig(ctx, store, cfg).AgentBackends, false)) > 0
	}
}

// agentPool is the dispatch side of the connection/model split
// (pkg/model/model_selection.go): a run asks for a model, and the pool
// round-robins across every enabled connection of the run's team that
// advertises it and has a free slot under its Concurrency. A run that
// pinned a connection (model.Task.AgentBackendID) gets that connection,
// enabled or not, and does not advance the rotation.
//
// One pool lives for the life of the daemon: the rotation counters, the
// cool-off of connections that recently refused, and the count of runs in
// flight on each connection are all state across dispatches.
type AgentPool struct {
	cfg         Config
	Store       *model.Store
	secretStore *secrets.Store

	mu   sync.Mutex
	next map[string]int
	// Availability belongs to this effective backend configuration, not
	// merely its stable ID. An operator correcting a connection in
	// Settings must make the edited connection eligible on the next
	// dispatch; keeping the old refusal against its ID would silently
	// ignore that correction until the bounded window expired.
	unavailable map[string]time.Time
	// inflight is the runs each connection (by ID) is carrying, for its
	// Concurrency limit.
	inflight map[string][]*connectionLease
	// Pause, when set, is the usage-limit gate (orchestrator.Pause), keyed
	// by the connection that owns a credential and the model on it: a
	// connection whose key is paused for the model a task would run
	// there -- or paused outright -- is passed over like one cooling off,
	// so the team's work rotates onto its other connections and waits
	// only when it has none left. Its other models are not passed over. It replaces the pool's own cool-off for a usage limit,
	// so an operator lifting the pause makes the connection a candidate
	// again at once.
	Pause *orchestrator.Pause
	// hostdDraining is whether host runs are being held for a draining
	// grain hostd (hostdHold), so the hold is logged when it starts and
	// when it ends rather than every cycle.
	hostdDraining atomic.Bool
}

// pausedLocked reports whether connection b's credential is paused
// after a usage limit (AgentPool.Pause) for a run of modelName -- the
// model the request chose, or b's own (runModelOn) when it chose none.
func (p *AgentPool) pausedLocked(b model.AgentBackend, modelName string, now time.Time) bool {
	return p.Pause.ModelPaused(b.CredentialOwnerID(), runModelOn(b, modelName), model.ConnectionTeam(b), now)
}

// runModelOn is the model a run that chose modelName passes the CLI on
// connection b: modelName itself, or b's own model when it chose none
// (buildOn's resolution, model.AgentBackend.EffectiveModel).
func runModelOn(b model.AgentBackend, modelName string) string {
	if name := strings.TrimSpace(modelName); name != "" {
		return name
	}
	return b.EffectiveModel()
}

// connectionLease is one run's hold on a connection's concurrency. It is
// taken when the pool picks the connection and given back when the run
// it was picked for returns. A lease whose run never started -- a
// dispatch that failed between choosing a connection and running the
// agent -- stops counting after leaseStartGrace, so a setup failure
// cannot keep a slot forever.
type connectionLease struct {
	pool     *AgentPool
	id       string
	taken    time.Time
	started  bool
	released bool
}

const leaseStartGrace = 10 * time.Minute

func (l *connectionLease) start() {
	if l == nil {
		return
	}
	l.pool.mu.Lock()
	l.started = true
	l.pool.mu.Unlock()
}

func (l *connectionLease) Release() {
	if l == nil {
		return
	}
	l.pool.mu.Lock()
	defer l.pool.mu.Unlock()
	if l.released {
		return
	}
	l.released = true
	leases := l.pool.inflight[l.id]
	l.pool.inflight[l.id] = slices.DeleteFunc(leases, func(other *connectionLease) bool { return other == l })
}

func NewAgentPool(cfg Config, store *model.Store, secretStore *secrets.Store) *AgentPool {
	return &AgentPool{
		cfg: cfg, Store: store, secretStore: secretStore,
		next:        map[string]int{},
		unavailable: map[string]time.Time{},
		inflight:    map[string][]*connectionLease{},
	}
}

// busyLocked reports whether connection b has no free slot. Concurrency
// <= 0 is "unset", which defaults to model.DefaultAgentBackendConcurrency
// (4); a positive Concurrency is the most runs it may carry at once.
func (p *AgentPool) busyLocked(b model.AgentBackend, now time.Time) bool {
	return p.inUseLocked(b, now) >= b.EffectiveConcurrency()
}

// inUseLocked is how many of connection b's slots are taken right now:
// runs that have started and not been released, and leases still inside
// their start grace.
func (p *AgentPool) inUseLocked(b model.AgentBackend, now time.Time) int {
	n := 0
	for _, l := range p.inflight[b.ID] {
		if !l.released && (l.started || now.Sub(l.taken) < leaseStartGrace) {
			n++
		}
	}
	return n
}

// agentFrameworks is the pool's Build, for callers that need only that.
func AgentFrameworks(cfg Config, store *model.Store, secretStore *secrets.Store) func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
	return NewAgentPool(cfg, store, secretStore).Build
}

// poolRequest is one choice resolved against the team and the
// connections as they stand: which model is wanted, on which framework,
// and whether a connection was pinned.
type poolRequest struct {
	team string
	// settings is the deployment's model choices -- default model,
	// pinned connection, per-model settings -- as dispatch read them.
	settings  *model.Config
	framework string
	model     string
	// models is the ordered list of models to try for this request, from
	// first to last. For an explicit per-task model it holds that one
	// model; for the deployment's default it holds every model in
	// settings.DefaultModels() -- only those on the task's framework, when
	// it named one.
	models []string
	// modelFrameworks is, for a task that named no framework, each
	// default model's own framework (defaultModelFramework): the list is
	// the deployment's whole answer, so a model on another framework than
	// the deployment's is still a model to fall back to. Nil when the task
	// named a framework, which then restricts every model to it.
	modelFrameworks map[string]string
	// pinnedModel is a model the task itself chose: it runs on that model
	// or waits, and never falls back down the default list.
	pinnedModel bool
	pinned      *model.AgentBackend
	// priority is AgentChoice.Priority: a run with priority dispatch is
	// not held by, and takes no slot of, a connection's concurrency limit.
	priority bool
	// hostdDraining is the frameworks whose CLI would start on the host
	// through a grain hostd that is draining for its own upgrade
	// (AgentPool.hostdDrainingFrameworks), and hostdWhy the sentence a run
	// that has nothing else to go to waits with. Their connections are not
	// candidates until hostd's replacement takes starts.
	hostdDraining map[string]bool
	hostdWhy      string
	// adoptable is, for a durable run (AgentChoice.Durable), the
	// frameworks this daemon can adopt a run of across its own restart
	// (preferAdoptable); nil otherwise.
	adoptable map[string]bool
}

// frameworkFor is the framework a run of m on req is restricted to: m's
// own, for a default model of a task that named no framework, and the
// request's otherwise.
func (r poolRequest) frameworkFor(m string) string {
	if f, ok := r.modelFrameworks[m]; ok {
		return f
	}
	return r.framework
}

// defaultModelFramework is the framework default model m runs on: the
// one framework the deployment's connections serving it are on, or the
// deployment's own framework (fallback) where that is ambiguous -- no
// connection serves it and its name says nothing, or connections on
// several frameworks serve it and the deployment's is one of them.
func defaultModelFramework(backends []model.AgentBackend, team, m, fallback string) string {
	fallback = model.NormalizeAgentFrameworkName(fallback)
	var serving []string
	for _, b := range backends {
		if b.SupportsModel(m) {
			if f := model.NormalizeAgentFrameworkName(b.Framework); !slices.Contains(serving, f) {
				serving = append(serving, f)
			}
		}
	}
	if len(serving) > 1 && slices.Contains(serving, fallback) {
		return fallback
	}
	if f := model.ModelFramework(backends, team, m); f != "" {
		return f
	}
	return fallback
}

// resolve turns a task's choice into a poolRequest. What the task said
// wins; the team's own defaults fill in what it did not.
func (p *AgentPool) resolve(ctx context.Context, live Config, choice orchestrator.AgentChoice) (poolRequest, error) {
	req := poolRequest{team: model.DefaultTeamID, framework: choice.Framework, model: strings.TrimSpace(choice.Model), priority: choice.Priority}
	if req.team == "" {
		req.team = model.DefaultTeamID
	}
	backendID := strings.TrimSpace(choice.BackendID)
	// A pin that names no connection is a model choice from before
	// model.Task.Model existed, which a task filed by an older proposer
	// can still carry.
	if backendID != "" && findBackendByID(live.AgentBackends, backendID) == nil && req.model == "" {
		req.model, backendID = backendID, ""
	}
	// And the reverse: a model naming a connection, not served by it as a
	// model, is that connection's pin -- what a proposal's `model` meant
	// before the split (relayProposedTasks reads new ones this way too).
	// Only the team's own: another team's connection id is just a model
	// nothing in this team serves, and waits as one.
	if backendID == "" && req.model != "" {
		if b := findBackendByID(live.AgentBackends, req.model); b != nil && !b.SupportsModel(req.model) {
			backendID, req.model = b.ID, ""
		}
	}
	// The symbolic judgement model (model.JudgementModelName,
	// docs/design/durable-tasks.md#DT-11) is resolved here, where every
	// run's model is chosen: to the deployment's JudgementModel, or the
	// strongest model its connections offer. Nothing resolving it is a
	// task that chose no model -- it runs on the rotation, and
	// orchestrator's reconcileJudgementModel tells somebody.
	if model.IsJudgementModel(req.model) {
		req.model = ""
		if p.Store != nil {
			if stored, err := p.Store.GetConfig(ctx); err == nil && stored != nil {
				if m, ok := model.ResolveJudgementModel(*stored, live.AgentBackends); ok {
					req.model = m
				}
			}
		}
	}
	if req.model != "" {
		req.models = []string{req.model}
		req.pinnedModel = true
	}
	explicitFramework := strings.TrimSpace(req.framework) != ""
	if p.Store != nil {
		// The deployment's model choices (framework, default model,
		// pinned connection, per-model settings) are grain_config's,
		// whichever team the task is for (docs/single-tenant-reset.md,
		// stage 1, 6-ii-b2 and 6-ii-b4).
		stored, err := p.Store.GetConfig(ctx)
		if err == nil && stored != nil {
			settings := stored
			req.settings = settings
			framework := settings.AgentFramework
			// The framework, pinned connection and default model are one
			// answer to "what does this deployment run on", taken whole
			// only by a task that said nothing about any of them.
			if req.framework == "" && backendID == "" && req.model == "" {
				req.framework = framework
				backendID = strings.TrimSpace(settings.AgentBackendID)
				if backendID != "" && findBackendByID(live.AgentBackends, backendID) == nil {
					backendID = ""
				}
			}
			// A task that chose neither a model nor a connection runs on its
			// team's default model list (tried first to last), each model on
			// its own framework, so a paused model falls over to the next
			// one whatever framework that is on -- unless the task named a
			// framework itself, in which case only models on that framework
			// are tried. The deployment's framework only settles a model
			// whose framework is ambiguous (defaultModelFramework).
			if req.model == "" && backendID == "" && settings.DefaultModel != "" {
				want := model.NormalizeAgentFrameworkName(req.framework)
				if !explicitFramework {
					req.modelFrameworks = map[string]string{}
				}
				for _, m := range settings.DefaultModels() {
					if !explicitFramework {
						req.models = append(req.models, m)
						req.modelFrameworks[m] = defaultModelFramework(live.AgentBackends, req.team, m, framework)
						continue
					}
					if mf := model.ModelFramework(live.AgentBackends, req.team, m); want == "" || mf == "" || mf == want {
						req.models = append(req.models, m)
					}
				}
				// The models this task's latest runs failed over off
				// (orchestrator.AgentChoice.AvoidModels) are tried last,
				// so it resumes on the next model of its rotation.
				req.models = avoidLast(req.models, choice.AvoidModels)
				if len(req.models) > 0 {
					req.model = req.models[0]
				}
			}
		}
	}
	// A framework nobody implements is refused as such, rather than as a
	// team with no connection on it.
	if req.framework != "" && !model.ValidAgentFramework(model.NormalizeAgentFrameworkName(req.framework)) {
		return req, fmt.Errorf("unknown agent framework %q: expected %s", req.framework, model.AgentFrameworkNames())
	}
	if backendID != "" {
		backend, err := pinnedBackend(live.AgentBackends, orchestrator.AgentChoice{Framework: req.framework, BackendID: backendID})
		if err != nil {
			return req, err
		}
		// Any of the deployment's connections may be pinned, whichever
		// team saved it (model.DeploymentAgentBackends).
		if req.model != "" && !backend.SupportsModel(req.model) {
			return req, fmt.Errorf("connection %q does not advertise model %q (it serves %s)",
				backend.ID, req.model, strings.Join(backend.SupportedModels(), ", "))
		}
		if req.model == "" {
			req.model = backend.EffectiveModel()
		}
		req.pinned = backend
		return req, nil
	}
	// Nothing chose a model -- not the task, not its team: the rotation
	// spans every enabled connection of the team, each serving the first
	// model it advertises. That is what a deployment configured before
	// models were chosen separately did, and keeps doing with nothing for
	// a human to set.
	if choice.Durable {
		req.preferAdoptable(live.adoptableFrameworks(ctx))
		if len(choice.AvoidModels) > 0 && len(req.models) > 0 {
			req.models = avoidLast(req.models, choice.AvoidModels)
			req.model = req.models[0]
		}
	}
	return req, nil
}

// avoidLast is models with every one of avoid moved, in order, behind
// the rest. Nothing is dropped: a rotation whose every model has failed
// still has one to run on.
func avoidLast(models, avoid []string) []string {
	if len(avoid) == 0 {
		return models
	}
	var first, last []string
	for _, m := range models {
		if slices.Contains(avoid, m) {
			last = append(last, m)
		} else {
			first = append(first, m)
		}
	}
	return append(first, last...)
}

// candidatesLocked is every connection the rotation may use for req
// right now, and what became of the rest. p.mu must be held.
func (p *AgentPool) candidatesLocked(live Config, req poolRequest, now time.Time) ([]model.AgentBackend, backendExclusions) {
	for key, until := range p.unavailable {
		if !now.Before(until) {
			delete(p.unavailable, key)
		}
	}
	want := model.NormalizeAgentFrameworkName(req.frameworkFor(req.model))
	var candidates []model.AgentBackend
	// Each connection that is not a candidate is counted under the first
	// reason that excluded it, so a dispatch that finds nothing can say
	// why.
	var excluded backendExclusions
	for _, backend := range live.AgentBackends {
		bTeam := model.ConnectionTeam(backend)
		switch {
		// No team case: model connections are the deployment's, and serve
		// every team's task (docs/single-tenant-reset.md, stage 1).
		case req.model != "" && !backend.SupportsModel(req.model):
			excluded.model++
			if backend.NoLongerDiscovers(req.model) {
				excluded.vanished++
			}
		case want != "" && model.NormalizeAgentFrameworkName(backend.Framework) != want:
			excluded.framework++
		case backend.Disabled:
			excluded.disabled++
		case req.model == "" && !backend.HasModels():
			// Nothing chose a model and this connection advertises none:
			// grain does not guess one for it.
			excluded.addNoModels(backend.ID)
		case req.hostdDraining[model.NormalizeAgentFrameworkName(backend.Framework)]:
			excluded.hostdDraining++
		case now.Before(p.unavailable[backendUnavailableKey(backend)]),
			p.Pause.ConnectionPaused(backend.CredentialOwnerID(), bTeam, now):
			excluded.coolingOff++
		case p.pausedLocked(backend, req.model, now):
			// Only the model this run would pass the CLI is paused here;
			// the connection's other models are still candidates for
			// other tasks.
			excluded.addPausedModel(runModelOn(backend, req.model))
		case !req.priority && p.busyLocked(backend, now):
			excluded.busy++
			excluded.busyRuns += p.inUseLocked(backend, now)
			excluded.busyLimit += backend.EffectiveConcurrency()
		default:
			candidates = append(candidates, backend)
		}
	}
	return req.adoptableOnly(candidates), excluded
}

// selectCandidateLocked tries req's models in order -- first model first,
// then down the list -- returning the first model that has at least one
// eligible candidate connection right now. When none can run, it returns
// the exclusions across all requested models. p.mu must be held.
func (p *AgentPool) selectCandidateLocked(live Config, req poolRequest, now time.Time) (string, []model.AgentBackend, backendExclusions) {
	if len(req.models) <= 1 {
		candidates, excluded := p.candidatesLocked(live, req, now)
		return req.model, candidates, excluded
	}
	for _, m := range req.models {
		single := req
		single.model = m
		single.models = []string{m}
		if candidates, _ := p.candidatesLocked(live, single, now); len(candidates) > 0 {
			return m, candidates, backendExclusions{}
		}
	}
	return req.model, nil, p.multiModelExclusionsLocked(live, req, now)
}

func (p *AgentPool) multiModelExclusionsLocked(live Config, req poolRequest, now time.Time) backendExclusions {
	for key, until := range p.unavailable {
		if !now.Before(until) {
			delete(p.unavailable, key)
		}
	}
	var excluded backendExclusions
	for _, backend := range live.AgentBackends {
		bTeam := model.ConnectionTeam(backend)
		var served, supported []string
		for _, m := range req.models {
			if backend.SupportsModel(m) {
				served = append(served, m)
				// Each model on its own framework (poolRequest.frameworkFor).
				if want := model.NormalizeAgentFrameworkName(req.frameworkFor(m)); want == "" || model.NormalizeAgentFrameworkName(backend.Framework) == want {
					supported = append(supported, m)
				}
			}
		}
		if len(served) == 0 {
			excluded.model++
			if slices.ContainsFunc(req.models, backend.NoLongerDiscovers) {
				excluded.vanished++
			}
			continue
		}
		if len(supported) == 0 {
			excluded.framework++
			continue
		}
		if backend.Disabled {
			excluded.disabled++
			continue
		}
		if req.hostdDraining[model.NormalizeAgentFrameworkName(backend.Framework)] {
			excluded.hostdDraining++
			continue
		}
		if now.Before(p.unavailable[backendUnavailableKey(backend)]) ||
			p.Pause.ConnectionPaused(backend.CredentialOwnerID(), bTeam, now) {
			excluded.coolingOff++
			continue
		}
		allPaused := true
		for _, m := range supported {
			if !p.pausedLocked(backend, m, now) {
				allPaused = false
				break
			}
		}
		if allPaused {
			excluded.modelPaused++
			for _, m := range supported {
				name := runModelOn(backend, m)
				if !slices.Contains(excluded.pausedModels, name) {
					excluded.pausedModels = append(excluded.pausedModels, name)
				}
			}
			continue
		}
		if !req.priority && p.busyLocked(backend, now) {
			excluded.busy++
			excluded.busyRuns += p.inUseLocked(backend, now)
			excluded.busyLimit += backend.EffectiveConcurrency()
		}
	}
	return excluded
}

// Availability is orchestrator.Deps.ModelAvailability: "" when choice
// can be dispatched now, or why it has to wait. It reserves nothing --
// Build does that -- so a task it admits can still lose the last slot
// to another one admitted in the same tick, and is then refused by
// Build as busy, the same as a connection that filled up between ticks.
func (p *AgentPool) Availability(ctx context.Context, choice orchestrator.AgentChoice) string {
	live := DispatchConfig(ctx, p.Store, p.cfg)
	if len(live.AgentBackends) == 0 {
		// No connections at all is the single-framework deployment the
		// daemon's own flags configure; Build reports what it lacks.
		framework := choice.Framework
		if framework == "" {
			framework = live.DefaultAgentFramework()
		}
		return p.hostdHold(ctx, live, framework)
	}
	req, err := p.resolve(ctx, live, choice)
	if err != nil {
		// A connection pinned with a model its provider has stopped
		// listing waits for the provider (or the operator) to bring it
		// back, rather than failing at dispatch.
		if b := findBackendByID(live.AgentBackends, strings.TrimSpace(choice.BackendID)); b != nil && b.NoLongerDiscovers(choice.Model) {
			return fmt.Sprintf("connection %q's provider no longer lists model %s", b.ID, strings.TrimSpace(choice.Model))
		}
		// Any other choice that cannot resolve at all is a setup failure
		// worth a diagnosis, not a wait.
		return ""
	}
	if req.pinned != nil {
		// A pinned connection with no model chosen and none of its own
		// to run waits for somebody to set one, rather than running on a
		// model name nobody chose.
		if req.model == "" {
			return noModelsReason([]string{req.pinned.ID})
		}
		// A pin bypasses the rotation, but not a usage-limit pause on the
		// pinned connection's credential for the model it would run:
		// there is nothing else for it to run on, so it waits for the
		// window. No other model is tried in its place -- see
		// orchestrator.Deps.ModelAvailability on the seam a fallback
		// would use.
		owner, team := req.pinned.CredentialOwnerID(), model.ConnectionTeam(*req.pinned)
		pin := fmt.Sprintf(" -- the task is pinned to %s on connection %q; it does not fall back", req.model, req.pinned.ID)
		if p.Pause.ConnectionPaused(owner, team, time.Now()) {
			return fmt.Sprintf("connection %q is paused after a usage limit on its credential", req.pinned.ID) + pin
		}
		if p.Pause.ModelPaused(owner, req.model, team, time.Now()) {
			return fmt.Sprintf("model %s on connection %q is paused after a usage limit", req.model, req.pinned.ID) + pin
		}
		return p.hostdHold(ctx, live, req.pinned.Framework)
	}
	// While hostd drains, the connections that would start their CLI
	// through it are passed over, so the run goes to one running its CLI
	// in the container -- further down the default models, or elsewhere
	// in the rotation -- and waits on hostd only when there is none.
	req.hostdDraining, req.hostdWhy = p.hostdDrainingFrameworks(ctx, live)
	p.mu.Lock()
	_, candidates, excluded := p.selectCandidateLocked(live, req, time.Now())
	p.mu.Unlock()
	if len(candidates) > 0 {
		return ""
	}
	if excluded.hostdDraining > 0 {
		return req.hostdWhy
	}
	reason := waitReason(req, excluded)
	if req.pinnedModel {
		// A model the task chose is the only one it runs on: say so, or
		// a wait while other models have budget reads as a missed
		// fallback.
		reason += fmt.Sprintf(" -- the task is pinned to %s; it does not fall back", req.model)
	}
	return reason
}

// fallbackReasonLocked says why a run on req's default model list came
// down to picked rather than a model earlier in the list -- "gemini-3-pro
// is paused until 14:05 UTC" -- or "" when picked is the first. Only the
// models before picked are named: those are the ones it would have run
// on. p.mu must be held.
func (p *AgentPool) fallbackReasonLocked(live Config, req poolRequest, picked string, now time.Time) string {
	var why []string
	for _, m := range req.models {
		if m == picked {
			break
		}
		want := model.NormalizeAgentFrameworkName(req.frameworkFor(m))
		var until time.Time
		onHost := false
		for _, b := range live.AgentBackends {
			if b.Disabled || !b.SupportsModel(m) || (want != "" && model.NormalizeAgentFrameworkName(b.Framework) != want) {
				continue
			}
			if req.hostdDraining[model.NormalizeAgentFrameworkName(b.Framework)] {
				onHost = true
			}
			// The model is back as soon as any connection serving it is.
			if at, ok := p.Pause.ModelPausedUntil(b.CredentialOwnerID(), runModelOn(b, m), model.ConnectionTeam(b), now); ok && (until.IsZero() || at.Before(until)) {
				until = at
			}
		}
		switch {
		case !until.IsZero():
			why = append(why, fmt.Sprintf("%s is paused until %s", m, until.UTC().Format("15:04 UTC")))
		case onHost:
			why = append(why, fmt.Sprintf("%s runs on the host, and grain hostd is updating", m))
		default:
			why = append(why, fmt.Sprintf("%s has no free connection", m))
		}
	}
	return strings.Join(why, ", and ")
}

// waitReason is the sentence a held task shows: either nothing enabled
// in its team serves the model, or everything that does is busy.
func waitReason(req poolRequest, e backendExclusions) string {
	// With no model chosen the rotation spans every enabled connection
	// of the team, so the team's are the connections that are busy.
	modelLabel := req.model
	if len(req.models) > 1 {
		modelLabel = strings.Join(req.models, ", ")
	}
	serving := " serving " + modelLabel
	if modelLabel == "" {
		serving = ""
	}
	if e.busy+e.coolingOff == 0 && e.modelPaused > 0 {
		return e.pausedModelClause() + " -- the task waits for the pause to lift"
	}
	if e.busy+e.coolingOff+e.modelPaused > 0 {
		var why []string
		if e.busy > 0 {
			why = append(why, e.busyClause())
		}
		if e.coolingOff > 0 {
			why = append(why, fmt.Sprintf("%d cooling off after a usage limit", e.coolingOff))
		}
		if e.modelPaused > 0 {
			why = append(why, e.pausedModelClause())
		}
		return fmt.Sprintf("all connections%s are busy (%s)", serving, strings.Join(why, ", "))
	}
	if modelLabel == "" && len(e.noModels) > 0 {
		return noModelsReason(e.noModels)
	}
	msg := fmt.Sprintf("no enabled connection serves model %s", modelLabel)
	if modelLabel == "" {
		msg = "the deployment has no enabled connection"
	}
	if e.disabled > 0 {
		msg += fmt.Sprintf(" (%d disabled)", e.disabled)
	}
	if e.vanished > 0 {
		msg += fmt.Sprintf(" -- the provider behind %d of its connections no longer lists it", e.vanished)
	}
	return msg
}

// noModelsReason is the sentence a task waits with when the connections
// it would run on advertise no model at all and nothing chose one: no
// discovered list, no pinned one, and no team default.
func noModelsReason(ids []string) string {
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = fmt.Sprintf("%q", id)
	}
	what := "connection " + quoted[0] + " has"
	if len(ids) > 1 {
		what = "connections " + strings.Join(quoted, ", ") + " have"
	}
	return what + " no models -- set one: pick the deployment's default model, or list the models the connection serves (Settings -> Models (/settings/models))"
}

// Build is orchestrator.Deps.Framework.
func (p *AgentPool) Build(ctx context.Context, choice orchestrator.AgentChoice) (agent.Framework, error) {
	secretStore := p.secretStore
	// Re-read here, not closed over from startup: which connections exist
	// and what each team chose are live settings, consumed right here, one
	// row read before a run that will cost minutes.
	live := DispatchConfig(ctx, p.Store, p.cfg)
	var selected *model.AgentBackend
	var lease *connectionLease
	framework := choice.Framework
	var chosenModel string
	var settings *model.Config
	var fallback string

	// Only the team's own connections are candidates, and there is nothing
	// behind them: no deployment-wide connection, framework or default model
	// stands in for a team that has none (grain/task-1601). The orchestrator
	// holds such a team's tasks (orchestrator.Deps.TeamHasAgentBackend), so a
	// dispatch that finds none is a race with its admin removing the last
	// one, and fails naming what is missing.
	req, err := p.resolve(ctx, live, choice)
	if err != nil {
		return nil, err
	}
	settings = req.settings
	chosenModel = req.model
	if req.pinned != nil && req.model == "" {
		return nil, errors.New(noModelsReason([]string{req.pinned.ID}))
	}
	if req.pinned != nil {
		// A task that named a connection gets that connection, and none
		// of the rotation's reasons skip it: it is used while disabled
		// (model.Task.AgentBackendID), and it does not advance rotation.
		selected = req.pinned
	} else {
		// Availability's rule: a draining hostd's connections are not
		// candidates, so a run that may go elsewhere never meets its
		// refusal.
		req.hostdDraining, req.hostdWhy = p.hostdDrainingFrameworks(ctx, live)
		p.mu.Lock()
		now := time.Now()
		pickedModel, candidates, excluded := p.selectCandidateLocked(live, req, now)
		if len(candidates) == 0 {
			p.mu.Unlock()
			targetModel := req.model
			if len(req.models) > 1 {
				targetModel = strings.Join(req.models, ", ")
			}
			wantFramework := req.framework
			if req.modelFrameworks != nil {
				// Each default model was tried on its own framework, so
				// no one framework is what the run asked for.
				wantFramework = ""
			}
			return nil, noBackendError(req.team, wantFramework, targetModel, len(live.AgentBackends), excluded)
		}
		chosenModel = pickedModel
		fallback = p.fallbackReasonLocked(live, req, pickedModel, now)
		key := model.NormalizeAgentFrameworkName(req.frameworkFor(chosenModel)) + "@" + strings.ToLower(req.team) + "/" + chosenModel
		backend := candidates[p.next[key]%len(candidates)]
		p.next[key] = (p.next[key] + 1) % len(candidates)
		p.mu.Unlock()
		selected = &backend
	}
	if !choice.Priority {
		p.mu.Lock()
		lease = &connectionLease{pool: p, id: selected.ID, taken: time.Now()}
		p.inflight[selected.ID] = append(p.inflight[selected.ID], lease)
		p.mu.Unlock()
	}
	built, err := p.buildOn(ctx, live, framework, selected, chosenModel, settings, secretStore)
	if err != nil || built == nil || selected == nil {
		lease.Release()
		return built, err
	}
	key := backendUnavailableKey(*selected)
	return &BackendFramework{Framework: built, Id: selected.ID, Lease: lease,
		Credential: selected.CredentialOwnerID(), Team: model.ConnectionTeam(*selected),
		FrameworkName: model.NormalizeAgentFrameworkName(selected.Framework),
		Model:         runModelOn(*selected, chosenModel),
		LimitPaused:   p.Pause != nil,
		Fallback:      fallback,
		Unavailable: func(_ string, until time.Time) {
			p.mu.Lock()
			p.unavailable[key] = until
			p.mu.Unlock()
		}}, nil
}

// buildOn builds the framework one run is driven by: selected's
// framework, authentication and the model chosen for it, with the
// deployment's own settings for that model (model.Config.SettingFor) -- or, with
// no connection selected, the daemon's own single framework.
func (p *AgentPool) buildOn(ctx context.Context, live Config, framework string, selected *model.AgentBackend, chosenModel string, settings *model.Config, secretStore *secrets.Store) (agent.Framework, error) {
	// effort is the chosen model's own per-model setting, "Model default"
	// (an empty effort, which passes none) included. Nothing
	// deployment-wide stands behind it (docs/connections-and-models.md).
	var effort string
	if selected != nil && chosenModel == "" {
		chosenModel = selected.EffectiveModel()
	}
	if settings != nil && chosenModel != "" {
		setting, _ := settings.LookupModelSetting(chosenModel)
		effort = setting.Effort
	}
	if selected != nil {
		backend := *selected
		framework = backend.Framework
		live.AgentBackendID = backend.ID
		live.agentCredentialBackendID = backend.CredentialBackendID
		live = withConnectionCLIPath(live, backend)
		live.agyTurnTimeout, live.agyRetryCutoff = backend.TurnLimits()
		live.agyConnection = backend.Name
		if live.agyConnection == "" {
			live.agyConnection = backend.ID
		}
		live.AgentAuthMode = backend.AuthMode
		live.codexAuthMode = backend.AuthMode
		live.ClaudeAuthMode = backend.AuthMode
		if backend.CredentialBackendID != "" {
			for _, source := range live.AgentBackends {
				if source.ID == backend.CredentialBackendID {
					live.AgentAuthMode = source.AuthMode
					live.codexAuthMode = source.AuthMode
					live.ClaudeAuthMode = source.AuthMode
					break
				}
			}
		}
		selected = &backend
	} else if framework == "" {
		framework = live.DefaultAgentFramework()
	}
	// The model and effort a framework is asked for are the ones chosen
	// for this run and nothing else: "" is the provider's own default.
	live.GeminiModel, live.GeminiEffort = "", ""
	live.ClaudeModel = ""
	live.CodexModel, live.CodexEffort = "", ""
	switch model.NormalizeAgentFrameworkName(framework) {
	case model.AgentFrameworkAntigravity:
		live.GeminiModel, live.GeminiEffort = chosenModel, effort
	case model.AgentFrameworkClaude:
		live.ClaudeModel = chosenModel
	case model.AgentFrameworkCodex:
		live.CodexModel, live.CodexEffort = chosenModel, effort
	}
	// Normalized so a task or a config row still carrying the legacy
	// "gemini" spelling dispatches onto the framework that name now
	// means, rather than falling into the error below.
	switch model.NormalizeAgentFrameworkName(framework) {
	case model.AgentFrameworkCustom:
		if selected == nil {
			return nil, errors.New("custom agent framework requires a configured backend and command (Settings -> Models)")
		}
		// A custom command is handed the model and effort chosen for
		// this run (docs/custom-agent-backends.md).
		return buildCustomFramework(live, *selected, chosenModel, effort, secretStore)
	case model.AgentFrameworkClaude, model.AgentFrameworkCodex, model.AgentFrameworkAntigravity:
		var built agent.Framework
		var err error
		switch model.NormalizeAgentFrameworkName(framework) {
		case model.AgentFrameworkClaude:
			built, err = BuildClaudeFramework(ctx, live, secretStore)
		case model.AgentFrameworkCodex:
			built, err = buildCodexFramework(ctx, live, secretStore)
		default:
			built, err = BuildAntigravityFramework(ctx, live, secretStore)
		}
		return built, err
	default:
		// Unreachable through the UI (ui.UpdateSettings and
		// ui.CreateTask both validate against the same vocabulary),
		// so this is a store written by hand or by a newer build --
		// worth naming rather than silently running the default.
		return nil, fmt.Errorf("unknown agent framework %q: expected %s",
			framework, model.AgentFrameworkNames())
	}
}

// backendExclusions counts, per reason, the configured backends a
// rotation dispatch passed over.
type backendExclusions struct {
	disabled, framework, model, coolingOff, busy int
	// hostdDraining counts the connections passed over because their CLI
	// runs on the host and grain hostd is draining (poolRequest.hostdDraining).
	hostdDraining int
	// busyRuns and busyLimit are the runs the busy connections are
	// carrying and the concurrency they allow between them, so the
	// sentence a held task shows says the limit that is full rather than
	// leaving the count of connections to read as one.
	busyRuns, busyLimit int
	// vanished counts the connections, among model's, that advertise
	// what their provider reports and whose provider does not report the
	// model now (model.AgentBackend.NoLongerDiscovers).
	vanished int
	// noModels is the connections that advertise no model at all, for a
	// run that chose none (model.AgentBackend.HasModels).
	noModels []string
	// modelPaused counts the connections passed over because the model
	// the run would pass there is paused after a usage limit
	// (orchestrator.Pause.ModelPaused), and pausedModels is which models
	// those were, each once.
	modelPaused  int
	pausedModels []string
}

func (e *backendExclusions) addNoModels(id string) { e.noModels = append(e.noModels, id) }

func (e *backendExclusions) addPausedModel(name string) {
	e.modelPaused++
	if !slices.Contains(e.pausedModels, name) {
		e.pausedModels = append(e.pausedModels, name)
	}
}

// pausedModelClause says which models are paused on how many
// connections: "model gemini-3-pro is paused after a usage limit on 1
// connection".
func (e backendExclusions) pausedModelClause() string {
	what := "model " + strings.Join(e.pausedModels, ", ") + " is"
	if len(e.pausedModels) > 1 {
		what = "models " + strings.Join(e.pausedModels, ", ") + " are"
	}
	where := "1 connection"
	if e.modelPaused != 1 {
		where = fmt.Sprintf("%d connections", e.modelPaused)
	}
	return fmt.Sprintf("%s paused after a usage limit on %s", what, where)
}

// busyClause says which connections are full and at what limit:
// "1 connection at its concurrency limit (3 of 3 runs)". The leading
// number counts connections, never runs.
func (e backendExclusions) busyClause() string {
	what := "1 connection at its concurrency limit"
	if e.busy != 1 {
		what = fmt.Sprintf("%d connections at their concurrency limits", e.busy)
	}
	return fmt.Sprintf("%s (%d of %d runs)", what, e.busyRuns, e.busyLimit)
}

// noBackendError is the setup failure of a dispatch that found no
// backend to rotate onto. It names what the run asked for -- its team
// always, since backends are strictly team-scoped, and a framework or
// model only when one was asked for -- and what became of every
// configured backend, because the fix (enable one, assign one to this
// team, wait out a cool-off) depends entirely on which it was.
func noBackendError(team, framework, targetModel string, configured int, e backendExclusions) error {
	var want []string
	if framework != "" {
		want = append(want, fmt.Sprintf("framework %q", framework))
	}
	if targetModel != "" {
		want = append(want, fmt.Sprintf("model %q", targetModel))
	}
	var why []string
	if e.disabled > 0 {
		why = append(why, fmt.Sprintf("%d disabled", e.disabled))
	}
	if e.framework > 0 {
		why = append(why, fmt.Sprintf("%d on another framework", e.framework))
	}
	if e.model > 0 {
		why = append(why, fmt.Sprintf("%d not serving that model", e.model))
	}
	if len(e.noModels) > 0 {
		why = append(why, fmt.Sprintf("%d with no models (set one)", len(e.noModels)))
	}
	if e.hostdDraining > 0 {
		why = append(why, fmt.Sprintf("%d running its CLI on the host while grain hostd updates", e.hostdDraining))
	}
	if e.coolingOff > 0 {
		why = append(why, fmt.Sprintf("%d cooling off after a recent refusal", e.coolingOff))
	}
	if e.modelPaused > 0 {
		why = append(why, e.pausedModelClause())
	}
	if e.busy > 0 {
		why = append(why, e.busyClause())
	}
	detail := fmt.Sprintf("of %d configured", configured)
	if len(why) > 0 {
		detail += ", " + strings.Join(why, ", ")
	}
	msg := fmt.Sprintf("no configured agent backend is free: %s", detail)
	if len(want) > 0 {
		msg = fmt.Sprintf("no configured agent backend matches %s: %s",
			strings.Join(want, ", "), detail)
	}
	// A team runs only on its own connections, with nothing behind them
	// (grain/task-1601), so whatever else is configured, the fix is the
	// team's.
	msg += " -- add one in Settings -> Models (/settings/models)"
	return errors.New(msg)
}

func findBackendByID(backends []model.AgentBackend, id string) *model.AgentBackend {
	for _, backend := range backends {
		if backend.ID == id {
			return &backend
		}
	}
	return nil
}

// pinnedBackend is the model a task named by hand
// (model.Task.AgentBackendID), looked up in the pool as it stands right
// now.
//
// Two ways it refuses, and both are worth a setup-failed run that says
// so rather than a quiet fallback onto the rotation: the id names
// nothing, because the model it named has since been removed in
// Settings; and the task also named a framework that is not the one that
// model runs. ui.CreateTask keeps the second pair consistent when a task
// is filed, so reaching it means the pool was edited underneath a queued
// task -- which is exactly the case where dispatching onto "some other
// model of ours" would be the wrong guess.
//
// Disabled is deliberately not consulted. A model out of the rotation is
// still a model this deployment has, and naming one is how it gets work.
func pinnedBackend(backends []model.AgentBackend, choice orchestrator.AgentChoice) (*model.AgentBackend, error) {
	for _, backend := range backends {
		if backend.ID != choice.BackendID {
			continue
		}
		if want := model.NormalizeAgentFrameworkName(choice.Framework); want != "" &&
			want != model.NormalizeAgentFrameworkName(backend.Framework) {
			return nil, fmt.Errorf("task names model %q, which runs on %s, and framework %q",
				backend.ID, model.NormalizeAgentFrameworkName(backend.Framework), want)
		}
		return &backend, nil
	}
	return nil, fmt.Errorf("no configured model has id %q (Settings -> Models)", choice.BackendID)
}

type BackendFramework struct {
	agent.Framework
	Id          string
	Unavailable func(string, time.Time)
	// lease is this run's slot under the connection's Concurrency,
	// given back when the run returns.
	Lease *connectionLease
	// Credential is the connection whose key this run spends -- Id's
	// own, or the one Id borrows (model.AgentBackend.CredentialOwnerID)
	// -- and Team is Id's team. Together they are what a usage limit met
	// here pauses (CredentialConnection, orchestrator.PauseScope).
	Credential, Team string
	// FrameworkName is the normalized agent framework of the selected
	// connection, so a task that did not pick a framework of its own is
	// recorded under the framework of whichever fallback model ran it.
	FrameworkName string
	// Model is the model this run passes the CLI, as resolved for the
	// connection -- what a usage limit met here pauses on it
	// (CredentialModel). "" when it is not known, which pauses the whole
	// connection.
	Model string
	// LimitPaused is whether a usage limit is the orchestrator's Pause's
	// to hold (AgentPool.Pause), rather than the pool's own cool-off.
	LimitPaused bool
	// Fallback is why this run is not on a model earlier in the
	// deployment's default list (AgentPool.fallbackReasonLocked) -- "" on
	// the first model, a pin, or a list of one.
	Fallback string
}

// CredentialConnection is orchestrator.CredentialConnection: the
// connection whose credential this run spends, and its team.
func (f *BackendFramework) CredentialConnection() (connection, team string) {
	if f.Credential != "" {
		return f.Credential, f.Team
	}
	return f.Id, f.Team
}

// CredentialFramework is orchestrator.CredentialFramework: the framework of
// the connection this run was built on.
func (f *BackendFramework) CredentialFramework() string { return f.FrameworkName }

// CredentialModel is orchestrator.CredentialModel: the model this run
// spends its connection's credential on.
func (f *BackendFramework) CredentialModel() string { return f.Model }

// FallbackReason is orchestrator.FallbackFramework: why this run is not
// on a model earlier in the deployment's default list, or "".
func (f *BackendFramework) FallbackReason() string { return f.Fallback }

// Release gives back this run's connection lease, if it holds one.
func (f *BackendFramework) Release() {
	if f == nil {
		return
	}
	f.Lease.Release()
}

func (f *BackendFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.Lease.start()
	defer f.Lease.Release()
	result, err := f.Framework.Run(ctx, cfg)
	var limit *agent.UsageLimitError
	if errors.As(err, &limit) {
		copy := *limit
		copy.Framework = f.Id
		if !f.LimitPaused {
			until, ok := copy.ResumeAt(time.Now())
			if !ok {
				until = time.Now().Add(15 * time.Minute)
			}
			f.Unavailable(f.Id, until)
		}
		return result, &copy
	}
	var incompat *agent.ModelIncompatibilityError
	if errors.As(err, &incompat) {
		copy := *incompat
		f.Unavailable(f.Id, time.Now().Add(15*time.Minute))
		return result, &copy
	}
	return result, err
}

// ParseResult is agent.ResultParser for the framework it wraps, which is
// how a run another daemon started is read when this one adopts it
// (orchestrator.ResumeAdoptedRun): the adopting daemon chooses a
// connection for it as for any run, so the framework it gets back is
// this wrapper. Parsing is the adopted run's end, so it gives the lease
// back as Run does. Unwrap says whether there is a parser underneath.
func (f *BackendFramework) ParseResult(stdout string, runErr error) (*agent.Result, error) {
	defer f.Lease.Release()
	parser, ok := f.Framework.(agent.ResultParser)
	if !ok {
		return nil, fmt.Errorf("agent framework %T cannot read a run host's spool", f.Framework)
	}
	return parser.ParseResult(stdout, runErr)
}

// ParseAttempts is agent.AttemptsParser for the framework it wraps, and
// ParseResult of the last attempt alone for one that is not.
func (f *BackendFramework) ParseAttempts(earlier, stdout string, runErr error) (*agent.Result, error) {
	parser, ok := f.Framework.(agent.AttemptsParser)
	if !ok {
		return f.ParseResult(stdout, runErr)
	}
	defer f.Lease.Release()
	return parser.ParseAttempts(earlier, stdout, runErr)
}

// Unwrap is the framework this one wraps.
func (f *BackendFramework) Unwrap() agent.Framework { return f.Framework }

func (f *BackendFramework) CanOpenPullRequest() bool {
	pr, ok := f.Framework.(agent.PullRequestFramework)
	return ok && pr.CanOpenPullRequest()
}

// TerminalCommand preserves the optional interactive-terminal capability of
// the selected backend. Wrapping pooled frameworks for quota attribution must
// not make a Claude, Codex, or Antigravity backend look less capable than the
// same framework selected through the legacy single-backend settings.
func (f *BackendFramework) TerminalCommand(ctx context.Context, cfg agent.RunConfig) (*agent.TerminalCommand, error) {
	terminal, ok := f.Framework.(agent.TerminalFramework)
	if !ok {
		return nil, fmt.Errorf("agent backend %q does not support an interactive terminal", f.Id)
	}
	return terminal.TerminalCommand(ctx, cfg)
}

// dispatchConfig is cfg with every store-backed setting a dispatch
// actually consults folded in from grain_config as it stands right now
// -- which agent.Framework to drive a run with, and which model that
// framework is asked for.
//
// Read per dispatch rather than cached at startup for the same reason
// the framework itself is built per dispatch (agentFrameworks' own doc
// comment): these are live choices an operator makes in Settings and
// expects the next run to honour. A store that cannot be read, or has no
// row yet, leaves cfg exactly as the flags parsed it -- which is the
// value that seeded that row in the first place.
func DispatchConfig(ctx context.Context, store *model.Store, cfg Config) Config {
	live := cfg
	if store != nil {
		if stored, err := store.GetConfig(ctx); err == nil && stored != nil {
			live = cfg.withLiveModelConfig(*stored)
			if cfg.executorServer != nil {
				cfg.executorServer.SetLimits(stored.AgentBackends)
			}
		}
	}
	if cfg.executorServer != nil {
		live.AgentBackends = append(slices.Clone(live.AgentBackends), cfg.executorServer.AgentBackends()...)
	}
	return live
}

// defaultAgentFramework is the deployment-wide setting a task that named
// no framework of its own is dispatched with: this config's own agent
// framework with model.Config.AgentFramework's "empty means antigravity"
// applied. Called on a dispatchConfig result, so what it answers is the
// setting as it stands now rather than as it stood at startup.
func (c Config) DefaultAgentFramework() string {
	return model.NormalizeAgentFramework(c.AgentFramework)
}

func findExecutorHost(cfg Config, binary string) (*modelexec.RemoteHost, error) {
	if cfg.executorServer == nil {
		return nil, nil
	}
	if strings.HasPrefix(cfg.AgentBackendID, "executor-") {
		host := cfg.executorServer.ConnectedHost(binary)
		if host == nil {
			return nil, fmt.Errorf("remote executor for backend %q offering %q is not connected", cfg.AgentBackendID, binary)
		}
		return host, nil
	}
	return nil, nil
}

// buildAntigravityFramework builds the framework a run driven by
// agent/antigravity uses: Google's Antigravity CLI (agy) as a subprocess,
// authenticating with the same Gemini API key the in-process runtime it
// replaced ran as. Unlike that runtime it needs a binary on the host, so
// this has the same shape buildClaudeFramework does -- resolve the CLI,
// resolve the credential, fail with something an operator can act on if
// either is missing.
func BuildAntigravityFramework(ctx context.Context, cfg Config, secretStore *secrets.Store) (agent.Framework, error) {
	remoteHost, err := findExecutorHost(cfg, "agy")
	if err != nil {
		return nil, err
	}
	agyPath := cfg.AgyPath
	var hostdHost agent.Host
	if remoteHost == nil {
		if r := cfg.ResolveCLIRun(ctx, model.AgentFrameworkAntigravity); r.Via == ViaHostd {
			// agy forks grain's mcpserver and hook commands by this binary's
			// own path, so on the bare machine that path has to hold a grain
			// of this daemon's version too -- the installer's to put there
			// (docs/design/cli-run-location.md).
			if hostdHost, err = cfg.cliHostdHost(r); err != nil {
				return nil, err
			}
			agyPath = r.HostPath
			if agyPath == "" {
				agyPath = "agy"
			}
		}
	}
	if remoteHost == nil && hostdHost == nil {
		if err := checkConnectionCLI(cfg); err != nil {
			return nil, err
		}
	}
	if remoteHost != nil {
		if path, err := remoteHost.BinaryPath("agy"); err == nil {
			agyPath = path
		}
	} else if agyPath == "" {
		resolved, err := exec.LookPath("agy")
		if err != nil {
			// Named as an install, not as a lookup failure, for the same
			// reason buildClaudeFramework does: an operator who has just
			// switched frameworks in Settings (where nothing mentions a
			// CLI) reads a bare "executable file not found in $PATH" as
			// grain being broken rather than as a host missing a package.
			return nil, fmt.Errorf("the Antigravity CLI (agy) is not installed: %w -- "+
				"the deployment image carries one (Dockerfile), so this is either an image "+
				"built without it or a grain running outside one; deploy an image that has it, "+
				"or point -agy-path at an existing copy", err)
		}
		agyPath = resolved
	}
	// Checked here, so a task dispatched with no key ends as a
	// setup-failed run naming what is missing (orchestrator.runOne's own
	// guard) rather than as an agy subprocess failing to authenticate
	// later with a message that says nothing about where grain reads
	// credentials from.
	var apiKey string
	if cfg.AgentAuthMode != model.AntigravityAuthAmbient && remoteHost == nil {
		var err error
		apiKey, err = backendCredential(ctx, cfg, secretStore, secrets.GeminiAPIKeySecret, cfg.GeminiAPIKeyFile)
		if err != nil {
			return nil, fmt.Errorf("reading the Gemini API key: %w", err)
		}
	}
	if apiKey == "" && cfg.AgentAuthMode != model.AntigravityAuthAmbient && remoteHost == nil {
		if cfg.AgentBackendID != "" {
			return nil, errors.New("this connection has no Gemini API key of its own: set one on it in Settings -> Models (/settings/models)")
		}
		return nil, errors.New("no Gemini API key is configured: set one in the UI " +
			"(Settings -> Models), or point -gemini-api-key-file at a file holding one")
	}
	var grainBinaryPath string
	if remoteHost != nil {
		if p, err := remoteHost.BinaryPath("grain"); err == nil {
			grainBinaryPath = p
		} else {
			grainBinaryPath = "grain"
		}
	} else {
		resolved, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolving this binary's own path, for agy's MCP settings: %w", err)
		}
		grainBinaryPath = resolved
	}
	// Resolved again inside the Run rather than closing over the key just
	// read, exactly as buildClaudeFramework does with its token: a key
	// replaced in Settings between this construction and the subprocess
	// actually starting is the one that should be used, and re-reading it
	// is one SQLite row.
	opts := []antigravity.Option{
		antigravity.WithModel(cfg.GeminiModel),
		antigravity.WithEffort(cfg.GeminiEffort),
		// The controller's own GitHub credential ladder, so a run can
		// call pull_request_status and see CI's verdict on the commits
		// it pushed. Not a per-run secret and not a sandbox one: the
		// forked mcpserver reading it runs here, on the controller.
		antigravity.WithGitHubAccess(cfg.DataDir, cfg.GithubHost, cfg.GithubInsecureHTTP),
		// The connection's own model-turn limits, defaults where it
		// sets none (turndeadline.go).
		antigravity.WithTurnLimits(cfg.agyTurnTimeout, cfg.agyRetryCutoff, cfg.agyConnection),
	}
	if remoteHost != nil {
		opts = append(opts, antigravity.WithHost(remoteHost))
	} else if hostdHost != nil {
		opts = append(opts, antigravity.WithHost(hostdHost))
	}
	if cfg.AgentAuthMode != model.AntigravityAuthAmbient && remoteHost == nil {
		opts = append(opts, antigravity.WithAPIKeyFunc(func(ctx context.Context) (string, error) {
			return backendCredential(ctx, cfg, secretStore, secrets.GeminiAPIKeySecret, cfg.GeminiAPIKeyFile)
		}))
	}
	if dir := agentSessionsDir(cfg.DataDir); dir != "" {
		// The per-task HOME that lets a run pick up the conversation the
		// task's last run left -- what agent/claude has always had for
		// free, its sessions living in the controller's own ~/.claude.
		// See agentSessionsDir, and pkg/agent/sessionstore for what is
		// kept there and what bounds it.
		opts = append(opts, antigravity.WithSessionStore(sessionstore.New(dir)))
	}
	if cfg.GranuleSandboxes {
		// Only meaningful with -granule-sandboxes, exactly as for
		// agent/claude: a run dispatched onto a plain
		// orchestrator.HostSandboxes directory reaches it through
		// RunConfig.SandboxRoot instead.
		opts = append(opts, antigravity.WithGranuleSSH(cfg.GranuleSSHUser, cfg.GranuleExecKey, cfg.GranuleWorkspace))
	}
	if url := daemonServerURL(cfg.UiAddr); url != "" {
		opts = append(opts, antigravity.WithGrainServer(url))
	}
	return antigravity.New(agyPath, grainBinaryPath, opts...), nil
}

func BuildClaudeFramework(ctx context.Context, cfg Config, secretStore *secrets.Store) (agent.Framework, error) {
	remoteHost, err := findExecutorHost(cfg, "claude")
	if err != nil {
		return nil, err
	}
	claudePath := cfg.ClaudePath
	// A CLI set to run on the host from a daemon in a container runs
	// through grain hostd (hostexec.go, clirun.go), which decides the
	// binary, so nothing about it is checked or resolved here.
	//
	// detached is the same hostd, through the container-equivalent entry
	// ("claude-detached"): the image's claude, with the container's HOME
	// and the deployment's token, forking its mcpserver on the host from
	// the grain the installer extracted to this binary's own path, so
	// that nothing of the run is a child of this process and the run
	// outlives it (docs/non-disruptive-upgrade.md).
	var container agent.Host
	detached := false
	if remoteHost == nil && cfg.RunHost == nil {
		if r := cfg.ResolveCLIRun(ctx, model.AgentFrameworkClaude); r.Via == ViaHostd {
			if container, err = cfg.cliHostdHost(r); err != nil {
				return nil, err
			}
			claudePath = "claude"
			if r.Location == model.CLIRunDetached {
				detached = true
				if r.HostPath != "" {
					claudePath = r.HostPath
				}
			}
		}
	}
	if remoteHost == nil && cfg.RunHost == nil && container == nil {
		// Not under a run host: the run pod, not this one, is where
		// the path has to exist, exactly as for -claude-path.
		if err := checkConnectionCLI(cfg); err != nil {
			return nil, err
		}
	}
	if remoteHost != nil {
		if path, err := remoteHost.BinaryPath("claude"); err == nil {
			claudePath = path
		}
	} else if claudePath == "" {
		resolved, err := exec.LookPath("claude")
		if err != nil {
			// Named as an install, not as a lookup failure. Both agent
			// frameworks need a binary, and an operator who has just
			// switched frameworks in Settings (where nothing mentions a
			// CLI at all) reads a bare "executable file not found in
			// $PATH" as grain being broken rather than as something
			// missing an install. The deployment image carries both
			// (Dockerfile), so on a real deployment this means an
			// image built without it -- not a host to go install
			// anything on.
			return nil, fmt.Errorf("the claude CLI is not installed: %w -- "+
				"the deployment image carries one (Dockerfile), so this is either an image "+
				"built without it or a grain running outside one; deploy an image that has it, "+
				"or point -claude-path at an existing copy", err)
		}
		claudePath = resolved
	}
	// Checked here, so a task dispatched with no token ends as a
	// setup-failed run naming what is missing (orchestrator.runOne's own
	// guard), rather than as a claude subprocess failing to authenticate
	// several minutes later with a message about credentials that says
	// nothing about where grain reads them from.
	isVertex := cfg.ClaudeAuthMode == model.ClaudeAuthVertex || cfg.AgentAuthMode == model.ClaudeAuthVertex || os.Getenv("CLAUDE_CODE_USE_VERTEX") == "1"
	if !isVertex && cfg.ClaudeAuthMode != model.AntigravityAuthAmbient && remoteHost == nil {
		token, err := backendCredential(ctx, cfg, secretStore, secrets.ClaudeOAuthTokenSecret, cfg.ClaudeOAuthTokenFile)
		if err != nil {
			return nil, fmt.Errorf("reading the Claude Code OAuth token: %w", err)
		}
		if token == "" {
			if cfg.AgentBackendID != "" {
				return nil, errors.New("this connection has no Claude Code OAuth token of its own: set one on it in Settings -> Models (/settings/models)")
			}
			return nil, errors.New("no Claude Code OAuth token is configured: set one in the UI " +
				"(Settings -> Models), or point -claude-oauth-token-file at a file holding one")
		}
	}
	var grainBinaryPath string
	if remoteHost != nil {
		if p, err := remoteHost.BinaryPath("grain"); err == nil {
			grainBinaryPath = p
		} else {
			grainBinaryPath = "grain"
		}
	} else if cfg.RunHost != nil {
		// The CLI, and the mcpserver its --mcp-config starts, run in the
		// run host's image, not beside this process: this binary's own
		// path is that image's grain only on a deployment that happens
		// to install it at the same place. The hand run (docs/test-runs/
		// 2026-10-15-upgrade-adopts-live-claude-run.md) had the daemon at
		// another path, and every run started with no tools at all.
		grainBinaryPath = cfg.RunHost.Grain
	} else {
		resolved, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolving this binary's own path, for claude's --mcp-config: %w", err)
		}
		grainBinaryPath = resolved
	}
	// Resolved again inside the Run rather than closing over the token
	// just read: a token replaced in Settings between this construction
	// and the subprocess actually starting is the one that should be
	// used, and re-reading it is one SQLite row.
	opts := []claude.Option{
		claude.WithModel(cfg.ClaudeModel),
		// The controller's own GitHub credential ladder -- see the
		// identical line in buildAntigravityFramework.
		claude.WithGitHubAccess(cfg.DataDir, cfg.GithubHost, cfg.GithubInsecureHTTP),
	}
	if remoteHost != nil {
		opts = append(opts, claude.WithHost(remoteHost))
	} else if cfg.RunHost != nil {
		// Only claude for now: its recommended login is a long-lived
		// setup-token that never refreshes, so run hosts of any number
		// and any generation can hold it at once. codex refreshes its
		// shared auth.json itself, which is not safe across processes
		// (docs/codex-authentication.md, "Several processes, one
		// login"), and stays under LocalHost until grain renews that
		// login ahead of expiry on its own.
		opts = append(opts, claude.WithHost(cfg.RunHost))
	} else if container != nil && detached {
		// The mcpserver is claude's own child on the host, as it is in
		// the container: the daemon's sandbox access is all at the same
		// paths there (the granule exec key and the kubeconfig in the data
		// directory, kubectl and docker on the host), and an mcpserver
		// served from this process would end with it.
		opts = append(opts, claude.WithHost(container))
	} else if container != nil {
		// The mcpserver stays here, behind grain's own HTTP endpoint,
		// rather than becoming the container's child: it keeps this
		// daemon's sandbox access and GitHub credentials, and the
		// container is given neither.
		opts = append(opts, claude.WithHost(container), claude.WithHostMCP())
	}
	if isVertex {
		opts = append(opts, claude.WithVertex(true))
	}
	if cfg.ClaudeAuthMode != model.AntigravityAuthAmbient && remoteHost == nil {
		opts = append(opts, claude.WithOAuthTokenFunc(func(ctx context.Context) (string, error) {
			return backendCredential(ctx, cfg, secretStore, secrets.ClaudeOAuthTokenSecret, cfg.ClaudeOAuthTokenFile)
		}))
	}
	if cfg.GranuleSandboxes {
		// Only meaningful with -granule-sandboxes: a run dispatched
		// onto a plain orchestrator.HostSandboxes directory reaches it
		// through RunConfig.SandboxRoot instead (RunDispatch, via
		// cycle.go's own rootedSandbox/vmNamedSandbox split), which
		// needs none of this.
		opts = append(opts, claude.WithGranuleSSH(cfg.GranuleSSHUser, cfg.GranuleExecKey, cfg.GranuleWorkspace))
	}
	if url := daemonServerURL(cfg.UiAddr); url != "" {
		if cfg.RunHost != nil && remoteHost == nil && cfg.RunHostDaemonURL != "" {
			// A run pod is not this pod: its mcpserver reaches the
			// daemon at the Service, which outlives this process.
			url = cfg.RunHostDaemonURL
		}
		opts = append(opts, claude.WithGrainServer(url))
	}
	return claude.New(claudePath, grainBinaryPath, opts...), nil
}

// buildCodexFramework builds the framework a run driven by agent/codex
// uses: OpenAI's codex CLI as a subprocess, authenticating with an
// OpenAI API key or a shared ChatGPT login file. Same shape as the two
// above -- resolve the CLI, resolve the credential, fail with something
// an operator can act on if either is missing -- because a deployment
// hits exactly the same two ways of not being set up.
// managedCodexAuthFile is the UI-owned subscription login. It lives in
// the host-only secrets directory already mounted into container deployments.
// A separately supplied -codex-auth-file remains a fallback.
func ManagedCodexAuthFile(dataDir string) string {
	return filepath.Join(SecretsDir(dataDir), "codex-auth.json")
}

// codexLoginFiles is every subscription login this daemon's codex runs
// can share: each backend's own, the UI-managed shared one, and an
// operator's -codex-auth-file. The login refresher renews each of them
// ahead of the CLI (codex.Refresher, docs/codex-authentication.md).
func codexLoginFiles(cfg Config) []string {
	managed := ManagedCodexAuthFile(cfg.DataDir)
	files := []string{managed}
	if backends, err := filepath.Glob(filepath.Join(filepath.Dir(managed), "codex-auth-*.json")); err == nil {
		files = append(files, backends...)
	}
	if cfg.CodexAuthFile != "" {
		files = append(files, cfg.CodexAuthFile)
	}
	return files
}

func configuredCodexAuthFile(cfg Config) string {
	managed := ManagedCodexAuthFile(cfg.DataDir)
	backendID := cfg.AgentBackendID
	if cfg.agentCredentialBackendID != "" {
		backendID = cfg.agentCredentialBackendID
	}
	if path := codex.BackendAuthFile(managed, backendID); path != "" {
		if _, err := os.Stat(path); err == nil || !os.IsNotExist(err) {
			return path
		}
	}
	if backendID != "" {
		// A connection's login is its own, like its key (backendCredential):
		// the shared one is the deployment's, which MigrateAgentBackendKeys
		// copied into every connection that ran on it.
		return ""
	}
	if _, err := os.Stat(managed); err == nil || !os.IsNotExist(err) {
		return managed
	}
	return cfg.CodexAuthFile
}

func buildCodexFramework(ctx context.Context, cfg Config, secretStore *secrets.Store) (agent.Framework, error) {
	remoteHost, err := findExecutorHost(cfg, "codex")
	if err != nil {
		return nil, err
	}
	codexPath := cfg.CodexPath
	// See BuildClaudeFramework. codex's MCP server is grain's own HTTP
	// endpoint on this machine whichever way it runs.
	var container agent.Host
	if remoteHost == nil {
		if r := cfg.ResolveCLIRun(ctx, model.AgentFrameworkCodex); r.Via == ViaHostd {
			if container, err = cfg.cliHostdHost(r); err != nil {
				return nil, err
			}
			codexPath = "codex"
		}
	}
	if remoteHost == nil && container == nil {
		if err := checkConnectionCLI(cfg); err != nil {
			return nil, err
		}
	}
	if remoteHost != nil {
		if path, err := remoteHost.BinaryPath("codex"); err == nil {
			codexPath = path
		}
	} else if codexPath == "" {
		resolved, err := exec.LookPath("codex")
		if err != nil {
			// Named as an install, not as a lookup failure -- see
			// buildClaudeFramework's own comment on why a bare
			// "executable file not found in $PATH" reads as grain being
			// broken to the operator who has just switched frameworks in
			// Settings.
			return nil, fmt.Errorf("the codex CLI is not installed: %w -- "+
				"the deployment image carries one (Dockerfile), so this is either an image "+
				"built without it or a grain running outside one; deploy an image that has it, "+
				"or point -codex-path at an existing copy", err)
		}
		codexPath = resolved
	}
	// Checked here, so a task dispatched with no key ends as a
	// setup-failed run naming what is missing (orchestrator.runOne's own
	// guard) rather than as a codex subprocess failing to authenticate
	// later with a message that says nothing about where grain reads
	// credentials from.
	var apiKey string
	if cfg.codexAuthMode != "" && cfg.codexAuthMode != model.CodexAuthAPIKey && cfg.codexAuthMode != model.CodexAuthSubscription && cfg.codexAuthMode != model.AntigravityAuthAmbient {
		return nil, fmt.Errorf("unknown Codex authentication mode %q", cfg.codexAuthMode)
	}
	if cfg.codexAuthMode != model.CodexAuthSubscription && cfg.codexAuthMode != model.AntigravityAuthAmbient && remoteHost == nil {
		var err error
		apiKey, err = backendCredential(ctx, cfg, secretStore, secrets.OpenAIAPIKeySecret, cfg.OpenAIAPIKeyFile)
		if err != nil {
			return nil, fmt.Errorf("reading the OpenAI API key: %w", err)
		}
		if apiKey == "" && cfg.codexAuthMode == model.CodexAuthAPIKey {
			return nil, errors.New("no OpenAI API key is configured for this backend (Settings -> Models)")
		}
	}
	// The subscription login: auth.json is a file codex rewrites in
	// place whenever its OAuth tokens rotate, which is why a run links
	// to it rather than copying it.
	authFile := configuredCodexAuthFile(cfg)
	if apiKey == "" && cfg.codexAuthMode != model.AntigravityAuthAmbient && remoteHost == nil {
		if authFile == "" && cfg.AgentBackendID != "" {
			return nil, errors.New("this connection has no Codex credential of its own: upload auth.json " +
				"or set an OpenAI API key on it in Settings -> Models (/settings/models)")
		}
		if authFile == "" {
			return nil, errors.New("no Codex credential is configured: upload auth.json or set an OpenAI API key " +
				"in the UI (Settings -> Models), point -openai-api-key-file at a file holding a key, " +
				"or point -codex-auth-file at auth.json from `codex login`")
		}
		if err := codex.ValidateAuthFile(authFile); err != nil {
			return nil, err
		}
	}
	// The mcpserver behind codex's endpoint is grain's own child, so
	// this is the binary that forks it.
	var grainBinaryPath string
	if remoteHost != nil {
		if p, err := remoteHost.BinaryPath("grain"); err == nil {
			grainBinaryPath = p
		} else {
			grainBinaryPath = "grain"
		}
	} else {
		resolved, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolving this binary's own path, for codex's MCP config: %w", err)
		}
		grainBinaryPath = resolved
	}
	// Resolved again inside the Run rather than closing over the key
	// just read, exactly as the two frameworks above do: a key replaced
	// in Settings between this construction and the subprocess actually
	// starting is the one that should be used, and re-reading it is one
	// SQLite row.
	opts := []codex.Option{
		codex.WithModel(cfg.CodexModel),
		codex.WithEffort(cfg.CodexEffort),
		// The controller's own GitHub credential ladder -- see the
		// identical line in buildAntigravityFramework.
		codex.WithGitHubAccess(cfg.DataDir, cfg.GithubHost, cfg.GithubInsecureHTTP),
	}
	if container != nil {
		opts = append(opts, codex.WithHost(container))
	}
	if remoteHost != nil {
		opts = append(opts, codex.WithHost(remoteHost))
		if remoteHost.CodexAuthFile() != "" {
			opts = append(opts, codex.WithAuthFile(remoteHost.CodexAuthFile()))
		}
	} else if apiKey != "" {
		opts = append(opts, codex.WithAPIKeyFunc(func(ctx context.Context) (string, error) {
			return backendCredential(ctx, cfg, secretStore, secrets.OpenAIAPIKeySecret, cfg.OpenAIAPIKeyFile)
		}))
	} else {
		opts = append(opts, codex.WithAuthFile(authFile))
	}
	if dir := agentSessionsDir(cfg.DataDir); dir != "" {
		// The per-task CODEX_HOME that lets a run pick up the thread the
		// task's last run left -- see the identical line in
		// buildAntigravityFramework, and agentSessionsDir.
		opts = append(opts, codex.WithSessionStore(sessionstore.New(dir)))
	}
	if cfg.GranuleSandboxes {
		opts = append(opts, codex.WithGranuleSSH(cfg.GranuleSSHUser, cfg.GranuleExecKey, cfg.GranuleWorkspace))
	}
	// The mcpserver that calls grain back is the child of grain's own
	// endpoint, in this container, so loopback is right for it.
	if url := daemonServerURL(cfg.UiAddr); url != "" {
		opts = append(opts, codex.WithGrainServer(url))
	}
	return codex.New(codexPath, grainBinaryPath, opts...), nil
}

func buildCustomFramework(cfg Config, backend model.AgentBackend, runModel, effort string, secretStore *secrets.Store) (agent.Framework, error) {
	grainBinaryPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("custom agent backend: locate grain binary: %w", err)
	}
	return custom.New(custom.Config{
		Command: backend.Command, StateRepoDir: StateRepoDir(cfg.DataDir),
		Model: runModel, Effort: effort,
		ModelProxy: func(ctx context.Context, run agent.RunConfig) (string, string, func(), error) {
			return CustomBackendModelProxy(ctx, backend, runModel, run)
		},
		MCPServer: func(ctx context.Context, run agent.RunConfig) (custom.Command, error) {
			args, err := CustomMCPServerArgs(ctx, cfg, run)
			if err != nil {
				return custom.Command{}, err
			}
			return custom.Command{Command: grainBinaryPath, Args: args}, nil
		},
	})
}

// customBackendModelProxy resolves the model proxy URL and mints a short-lived
// token for one custom backend run, matching the built-in frameworks' model proxy
// integration.
//
// release revokes that token, and the framework calls it as the run ends:
// the proxy's registry would otherwise keep honouring it until its lease
// ran out -- the run's deadline, or DefaultLease for a run that has none
// -- long after the backend that held it had exited.
//
// A backend whose vendor customBackendVendor cannot name gets empty
// fields rather than a token for whichever proxy happens to be running:
// that would hand it a vendor it never asked for.
//
// The token forwards with the deployment's connection of that vendor
// (modelproxy.Endpoint.ConnectionFor), as every model proxy token does; a
// deployment with none gets a token every request on which is refused
// saying so.
func CustomBackendModelProxy(ctx context.Context, backend model.AgentBackend, runModel string, run agent.RunConfig) (url, token string, release func(), err error) {
	vendor, ok := customBackendVendor(backend, runModel)
	if !ok {
		return "", "", nil, nil
	}
	endpoint := ModelProxies[vendor.Name]
	if endpoint == nil || endpoint.URL() == "" {
		return "", "", nil, nil
	}
	owner := run.TaskID
	if owner == "" {
		owner = "custom"
	}
	// Held to the run's own deadline, credit included, as a backstop for
	// a run whose release never comes (the daemon dying mid-run).
	var until time.Time
	if !run.Deadline.IsZero() {
		until = run.Deadline.Add(orchestrator.RunWaitCreditCap)
	}
	connection, err := endpoint.ConnectionFor(ctx, "")
	if err != nil {
		return "", "", nil, fmt.Errorf("choosing the proxy's connection: %w", err)
	}
	id, token, err := endpoint.MintUntil(owner, connection, until)
	if err != nil {
		return "", "", nil, fmt.Errorf("minting proxy token: %w", err)
	}
	return endpoint.URL() + vendor.BaseURLSuffix, token, func() { endpoint.Revoke(id) }, nil
}

func customBackendVendor(backend model.AgentBackend, runModel string) (modelproxy.Vendor, bool) {
	name := strings.ToLower(runModel)
	if name == "" {
		name = strings.ToLower(backend.Name)
	}
	if name == "" {
		name = strings.ToLower(backend.ID)
	}
	for _, vendor := range modelproxy.Vendors() {
		if strings.Contains(name, vendor.Name) {
			return vendor, true
		}
	}
	if strings.Contains(name, "gpt") || strings.Contains(name, "codex") || strings.Contains(name, "openai") || strings.HasPrefix(name, "o1") || strings.HasPrefix(name, "o3") || strings.HasPrefix(name, "o4") {
		return modelproxy.OpenAI, true
	}
	if strings.Contains(name, "sonnet") || strings.Contains(name, "opus") || strings.Contains(name, "haiku") || strings.Contains(name, "anthropic") {
		return modelproxy.Claude, true
	}
	if strings.Contains(name, "flash") || strings.Contains(name, "google") {
		return modelproxy.Gemini, true
	}
	return modelproxy.Vendor{}, false
}

// customMCPServerArgs gives an external backend the same Grain tool server
// the built-in agent CLIs receive. The command owns the transport-specific
// translation into its agent SDK or CLI.
func CustomMCPServerArgs(ctx context.Context, cfg Config, run agent.RunConfig) ([]string, error) {
	var args []string
	switch {
	case run.SandboxRoot != "":
		args = []string{"mcpserver", "-sandbox-root", run.SandboxRoot}
	case run.GranuleVM != "":
		if cfg.GranuleSSHUser == "" || cfg.GranuleWorkspace == "" {
			return nil, errors.New("custom agent backend: granule run requires SSH user and workspace")
		}
		args = []string{"mcpserver", "-granule-vm", run.GranuleVM,
			"-ssh-user", cfg.GranuleSSHUser, "-workspace", cfg.GranuleWorkspace}
		if cfg.GranuleExecKey != "" {
			args = append(args, "-exec-key", cfg.GranuleExecKey)
		}
	case run.Container != "":
		args = []string{"mcpserver", "-container", run.Container}
	default:
		return nil, errors.New("custom agent backend: sandbox root, granule VM, or container is required")
	}
	if run.Repo != "" && run.Branch != "" {
		args = append(args, "-data-dir", cfg.DataDir, "-pr-repo", run.Repo, "-pr-branch", run.Branch)
		if run.Base != "" {
			args = append(args, "-pr-base", run.Base)
		}
		if run.Repair {
			args = append(args, "-pr-repair")
		}
		if run.GitHubCIIgnored {
			args = append(args, "-pr-ignore-github-ci")
		}
		if run.BaseIncident != "" {
			args = append(args, "-pr-base-incident", run.BaseIncident)
		}
		if run.PullRequestNumber > 0 {
			args = append(args, "-pr-number", fmt.Sprintf("%d", run.PullRequestNumber))
		}
		if run.GitCredential != "" {
			args = append(args, "-pr-credential", run.GitCredential)
		}
		if cfg.GithubHost != "" {
			args = append(args, "-github-host", cfg.GithubHost)
		}
		if cfg.GithubInsecureHTTP {
			args = append(args, "-github-insecure-http")
		}
	}
	if server := daemonServerURL(cfg.UiAddr); server != "" && run.TaskID != "" {
		args = append(args, "-server", server, "-task", run.TaskID)
	}
	args = append(args, agent.GrantArgs(run)...)
	args = append(args, agent.ProposeRepoArgs(run)...)
	args = append(args, agent.SandboxEnvArgs(run)...)
	args = append(args, agent.InteractiveArgs(run)...)
	args = append(args, agent.AddendaArgs(run)...)
	args = append(args, agent.RunDeadlineArgs(ctx, run)...)
	return args, nil
}

// daemonServerURL is the base URL this same process's own UI/API server
// is reachable at, for a run's forked mcpserver to ask it to open that
// run's pull request (agent/claude's and agent/antigravity's
// WithGrainServer). It is derived from -ui-addr rather than configured
// separately: the two are the same server, and a second flag naming the
// same thing is a second thing to get wrong.
//
// A host-less address (":8420", what a deployment serving on every
// interface passes) becomes loopback, which is both correct and the only
// address worth using here: the asker is a process on this very host.
//
// "" -- no UI/API server at all (-ui-addr emptied out), or one bound to
// port 0, whose real port is only known to the listener rather than to
// this string -- means runs simply get no open_pull_request tool, which
// is the same deployment they had before it existed.
func daemonServerURL(uiAddr string) string {
	if uiAddr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(uiAddr)
	if err != nil || port == "" || port == "0" {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// backendCredential is AgentCredential for a caller with no connection;
// for a connection it is that connection's own key and nothing else.
func backendCredential(ctx context.Context, cfg Config, secretStore *secrets.Store, secret, file string) (string, error) {
	backendID := cfg.AgentBackendID
	if cfg.agentCredentialBackendID != "" {
		backendID = cfg.agentCredentialBackendID
	}
	if backendID == "" {
		return AgentCredential(ctx, secretStore, secret, file)
	}
	// A connection runs on its own key (or the one it borrows by
	// credentialBackendId) and on nothing else: the deployment-wide key
	// it once fell back to is grain's own, not the team's
	// (grain/task-1656). MigrateAgentBackendKeys copied it into every
	// connection that relied on it.
	if secretStore == nil {
		return "", nil
	}
	name := secrets.AgentBackendCredentialSecret(backendID)
	value, err := secretStore.Resolve(ctx, name)
	switch {
	case err == nil:
		return strings.TrimSpace(value), nil
	case errors.Is(err, secrets.ErrNoSecret):
		return "", nil
	default:
		return "", fmt.Errorf("reading %s: %w", name, err)
	}
}

// withConnectionCLIPath points cfg at backend's own CLI, when it names
// one (model.AgentBackend.CLIPath): the precedence is the connection's
// path, then the deployment's -claude-path / -codex-path / -agy-path,
// then the daemon's PATH, and this is the first step of it. A remote
// executor still runs the binary it offers (findExecutorHost): the path
// is one on the daemon's machine, not on the executor's.
func withConnectionCLIPath(cfg Config, backend model.AgentBackend) Config {
	path := strings.TrimSpace(backend.CLIPath)
	if path == "" {
		return cfg
	}
	switch model.AgentCLIBinary(backend.Framework) {
	case "claude":
		cfg.ClaudePath = path
	case "codex":
		cfg.CodexPath = path
	case "agy":
		cfg.AgyPath = path
	default:
		return cfg
	}
	cfg.connectionCLIPath = path
	return cfg
}

// checkConnectionCLI refuses a connection whose own CLI path names no
// binary this daemon can run, naming the connection rather than the
// deployment flag the generic "not installed" message would point at.
// The path was checked when it was saved (pkg/ui's CheckCLIPath), so
// this is a binary removed or replaced since.
func checkConnectionCLI(cfg Config) error {
	if cfg.connectionCLIPath == "" {
		return nil
	}
	if _, err := exec.LookPath(cfg.connectionCLIPath); err != nil {
		return fmt.Errorf("connection %q's CLI path %q cannot be run here: %w -- "+
			"fix or clear it in Settings -> Models (/settings/models)", cfg.AgentBackendID, cfg.connectionCLIPath, err)
	}
	return nil
}

func credentialConfigForBackend(cfg Config, backendID string) Config {
	cfg.AgentBackendID = backendID
	for _, backend := range cfg.AgentBackends {
		if backend.ID != backendID {
			continue
		}
		cfg.AgentAuthMode = backend.AuthMode
		cfg.codexAuthMode = backend.AuthMode
		cfg.ClaudeAuthMode = backend.AuthMode
		cfg.agentCredentialBackendID = backend.CredentialBackendID
		cfg = withConnectionCLIPath(cfg, backend)
		if backend.CredentialBackendID != "" {
			for _, source := range cfg.AgentBackends {
				if source.ID == backend.CredentialBackendID {
					cfg.AgentAuthMode = source.AuthMode
					cfg.codexAuthMode = source.AuthMode
					cfg.ClaudeAuthMode = source.AuthMode
					break
				}
			}
		}
		break
	}
	return cfg
}

// AgentCredential reads the credential one agent framework runs as: the
// secrets database first -- the one the UI writes (pkg/ui's
// handleSetAgentKey), so a key pasted into Settings takes effect on the
// next dispatch with no restart and no file to place -- and only then
// the startup flag naming a file, which is how a deployment seeded one
// before the UI could (scripts/setup.sh's own seed_secret) and how a
// bare `grain daemon` run outside a deployment still can.
//
// "" with no error means this deployment has neither, which is a state
// the daemon now runs in perfectly happily: the UI is up, and that is
// where the missing key gets set. A secret that is simply absent is not
// an error either -- secrets.Store.Resolve reports one as such, and
// every deployment that has only ever used the file has exactly that.
func AgentCredential(ctx context.Context, secretStore *secrets.Store, secret, file string) (string, error) {
	if secretStore != nil {
		value, err := secretStore.Resolve(ctx, secret)
		switch {
		case err == nil:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed, nil
			}
		case !errors.Is(err, secrets.ErrNoSecret):
			log.Printf("agent credential: reading %s: %v", secret, err)
		}
	}
	if file == "" {
		return "", nil
	}
	value, err := readTrimmedFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		// A path that names nothing is the same "nothing configured this
		// way" as an unset flag: scripts/setup.sh passes
		// -gemini-api-key-file unconditionally, pointing at a file it
		// only writes once a key exists to write.
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return value, nil
}
