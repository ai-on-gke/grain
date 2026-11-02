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

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// errUsageLimit is context.Cause's own report, by identity, for a run
// Pause cancelled because the credential it is spending has no
// budget left in its current window -- the third way a run here ends
// without having failed, alongside errTaskClosed and errRunTimedOut, and
// read the same way (errors.Is against context.Cause) by RunDispatch.
var errUsageLimit = errors.New("orchestrator: the agent's usage limit was reached while this run was live")

// How long dispatch waits before trying again, when the provider's own
// answer needs bounding:
//
//   - defaultUsageLimitPause is the wait when the refusal named no reset
//     at all, which is the common shape of a bare 429. Long enough that
//     the deployment is not hammering a provider that just said no,
//     short enough that a limit which has in fact already lifted costs
//     one idle quarter of an hour rather than an evening.
//   - minUsageLimitPause is the floor under a provider that named a very
//     short delay. The point of pausing is to stop the queue walking
//     into the same wall run after run, and a few seconds does not.
//   - maxUsageLimitPause is the ceiling over one that named a very long
//     one -- or over a reset instant this code misread. A day covers the
//     longest window a provider has been seen to name (agy's "Resets in
//     17h32m", task 1908, which a 6h cap met two or three times over).
//     A deployment idling for a wrong day is recoverable; one idling
//     until 2286 because an epoch was parsed in the wrong unit is not
//     something anybody would think to look for.
//
// Retrying once the pause lifts is not a promise the limit is over: a
// window that has not actually reset produces one more refusal, one more
// pause, and no further traffic. That is the property these numbers are
// chosen for, rather than for guessing any particular provider's window.
const (
	defaultUsageLimitPause = 15 * time.Minute
	minUsageLimitPause     = time.Minute
	maxUsageLimitPause     = 24 * time.Hour
)

// PauseScope is what one usage limit belongs to, and so what the pause it
// opens covers: the credential the run that met it was spending.
//
// For a run on one of a team's model connections that is the connection
// owning the key -- the one it borrows from (model.AgentBackend.
// CredentialBackendID) or itself (model.AgentBackend.CredentialOwnerID)
// -- and Connection names it. Teams cannot share connections, so such a
// pause never reaches another team's work, nor the same team's other
// connections on other keys.
//
// A run on no connection -- the deployment-level credentials a
// deployment with no connections configured runs on (the legacy
// single-framework settings), or a framework that cannot say which
// connection it is on -- has nothing narrower to be keyed by than its
// own team and framework, and that is what its pause covers: the
// smallest scope a refusal grain cannot attribute to a connection can
// safely be read as, rather than the whole deployment. A scope naming
// neither a connection nor a framework covers its whole team.
//
// Team and Framework are carried on a connection's scope too, for what
// reads a pause back (Pauses: whose banner it is, what it is labelled).
//
// Model narrows a connection's scope to one model on it: providers limit
// per model as often as per key, so a run refused on one model pauses
// that model on its connection and leaves the connection's other models
// dispatching (Begin). It is the model grain passed the CLI -- the
// resolved one, never a team default left unresolved -- or "auto" and
// its like for a provider-chosen run. A connection's scope with no Model
// covers every model on it: a run whose model grain cannot name, and a
// pause escalated to the whole connection (Begin). Model means nothing
// without a Connection and is dropped from a scope that has none.
type PauseScope struct {
	Connection string
	Model      string
	Team       string
	Framework  string
}

// normalized is s as Pause holds it: the default team for none, and the
// framework's current spelling (model.NormalizeAgentFrameworkName), so a
// legacy "gemini" pauses what that name now means.
func (s PauseScope) normalized() PauseScope {
	s.Connection = strings.TrimSpace(s.Connection)
	s.Team = strings.TrimSpace(s.Team)
	if s.Team == "" {
		s.Team = model.DefaultTeamID
	}
	s.Framework = model.NormalizeAgentFrameworkName(strings.TrimSpace(s.Framework))
	s.Model = normalizePauseModel(s.Model)
	if s.Connection == "" {
		s.Model = ""
	}
	return s
}

// normalizePauseModel is a model name as a pause compares it: trimmed and
// lower-cased, so "Gemini-3-Pro" and "gemini-3-pro" are one quota.
func normalizePauseModel(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// key is the one window a scope's limits share. A connection's is the
// connection and model -- its team and framework follow from it, and an
// empty model is the whole connection -- and anything else's is its team
// and framework.
func (s PauseScope) key() string {
	if s.Connection != "" {
		return "connection\x00" + s.Connection + "\x00" + s.Model
	}
	return "team\x00" + strings.ToLower(s.Team) + "\x00" + s.Framework
}

// covers reports whether a pause held for s covers a run spending run's
// credential: the same connection's -- on the same model, for a pause
// on one model -- or, for a pause on no connection, a run of the same
// team, on the same framework and on no connection either, or any run of
// that team at all when s names no framework.
//
// A run whose model is unknown is covered only by a pause on its whole
// connection: grain cannot say it would meet a model's limit, and a
// pause it met itself is connection-wide in turn.
func (s PauseScope) covers(run PauseScope) bool {
	if s.Connection != "" {
		return s.Connection == run.Connection && (s.Model == "" || s.Model == run.Model)
	}
	if !strings.EqualFold(s.Team, run.Team) {
		return false
	}
	if s.Framework == "" {
		return true
	}
	return run.Connection == "" && s.Framework == run.Framework
}

// describe names a scope for the log lines an operator reads when a
// pause opens, expires or is lifted.
func (s PauseScope) describe() string {
	if s.Connection != "" && s.Model != "" {
		return fmt.Sprintf("model %s on connection %s (team %s)", s.Model, s.Connection, s.Team)
	}
	if s.Connection != "" {
		return fmt.Sprintf("connection %s (team %s)", s.Connection, s.Team)
	}
	if s.Framework == "" {
		return fmt.Sprintf("team %s", s.Team)
	}
	return fmt.Sprintf("team %s's %s runs on no connection", s.Team, s.Framework)
}

// CredentialConnection is implemented by an agent.Framework that runs on
// one of a team's model connections -- the daemon's pool wraps every
// framework it builds on one (daemon.BackendFramework) -- to say which
// connection's credential it spends and which team that connection
// belongs to. That is what a usage limit it meets pauses (PauseScope).
// A framework that does not implement it, or answers "", is paused by
// its run's team and framework instead.
type CredentialConnection interface {
	CredentialConnection() (connection, team string)
}

// CredentialModel is implemented by a framework on a connection that can
// say which model it passes the CLI -- the resolved model, as the run
// spends it -- which is what a usage limit it meets pauses on that
// connection (PauseScope.Model). A framework that does not implement it,
// or answers "", is paused connection-wide.
type CredentialModel interface {
	CredentialModel() string
}

// CredentialFramework is implemented by a framework built on a connection
// that can say which agent framework that connection runs on.
type CredentialFramework interface {
	CredentialFramework() string
}

// FallbackFramework is implemented by a framework built on a model
// further down the deployment's default list than the first, which can
// say why the earlier ones were passed over ("gemini-3-pro is paused
// until 14:05 UTC"). "" means the run is on the model it preferred.
type FallbackFramework interface {
	FallbackReason() string
}

func runFrameworkFor(framework agent.Framework, task model.Task, cfg Config) string {
	if name := model.NormalizeAgentFrameworkName(task.AgentFramework); name != "" {
		return name
	}
	if cf, ok := framework.(CredentialFramework); ok {
		if name := model.NormalizeAgentFrameworkName(cf.CredentialFramework()); name != "" {
			return name
		}
	}
	return frameworkFor(task, cfg)
}

// runModelFor is the model a run on framework, for task, runs: the
// model of the connection framework was built on, where it names one,
// and the task's own pin where it does not. "" when neither says.
func runModelFor(framework agent.Framework, task model.Task) string {
	if cm, ok := framework.(CredentialModel); ok {
		if m := strings.TrimSpace(cm.CredentialModel()); m != "" {
			return m
		}
	}
	return strings.TrimSpace(task.Model)
}

// runPauseScope is the scope a run on framework, for task, spends:
// framework's own connection, and model on it, where it names one, and
// task's team and framework (frameworkFor) where it does not.
func runPauseScope(framework agent.Framework, task model.Task, cfg Config) PauseScope {
	scope := PauseScope{Team: model.DefaultTeamID, Framework: runFrameworkFor(framework, task, cfg)}
	if cc, ok := framework.(CredentialConnection); ok {
		if connection, team := cc.CredentialConnection(); strings.TrimSpace(connection) != "" {
			scope.Connection = connection
			if strings.TrimSpace(team) != "" {
				scope.Team = team
			}
			if cm, ok := framework.(CredentialModel); ok {
				scope.Model = cm.CredentialModel()
			}
		}
	}
	return scope.normalized()
}

// Pause is the "stop dispatching, there is no budget left" gate: what an
// agent's own usage limit (agent.UsageLimitError) turns into for the
// rest of the deployment.
//
// A usage limit is not a fact about the run that met it. Every other run
// spending the same credential is going to meet the same refusal the
// moment it is dispatched -- each one burning an attempt, a sandbox, and
// a place in its own failure streak on an outage none of them caused. So
// the run that meets one pauses dispatch onto that credential until the
// window resets (Begin), and the runs already in flight on it are
// cancelled rather than left to grind through the same wall (register,
// which is how Begin can reach them at all). Every run ended either way
// records model.PausedOutcome -- its own word rather than "failed" or
// "cancelled", and one that neither task_streak nor Store.FailureStreak
// counts, so an outage does not spend the retry budget of every task
// that happened to be running when it began.
//
// What "the same credential" means is the model connection that owns it
// (PauseScope). It used to be the agent framework (grain/task-104), when
// a deployment held one credential per framework; connections are a
// team's own now, so a framework-wide pause let one team exhausting its
// key stop and cancel every other team's work on the same framework, and
// show them a banner about it. A pause keyed by the credential-owning
// connection covers exactly the runs spending that key -- the
// connection's own, and those of every connection borrowing it -- and so
// never more than one team. Dispatch learns of it from the daemon's
// connection pool, which passes a paused connection over and rotates a
// team's work onto its other connections (ConnectionPaused); pauses on
// no connection gate dispatch here, by team and framework (Coverage).
//
// The other pause a deployment has is nothing to do with this one: an
// operator's own stop (model.PipelinePause) is a store row, is
// deliberate rather than a provider's refusal, and covers the whole
// queue whatever a task runs on -- see reconcileDispatch, where the two
// arrive at dispatch.Cycle as two different options.
//
// Its windows outlive the process once a store is attached (Restore):
// each one is written to the store as it opens or is extended, deleted
// as it expires or is lifted, and read back by the next daemon before it
// dispatches anything. It used to be in-process only, on the theory that
// a restarted daemon would spend one run rediscovering a limit; in
// practice that run cancelled every sibling on the same credential, and
// a deployment upgrading or restarting a few times inside a long
// provider window (grain/task-1259: Antigravity quotas that reset after
// about fourteen hours) kept walking its queue into the same wall. A
// credential an operator has since changed is not held up by a restored
// window: editing a connection in Settings lifts its pauses (pkg/ui).
// Live runs and failure bursts are still in-process only -- they are
// what this daemon is doing, which a restart does end.
//
// The zero value is ready to use, and every method tolerates a nil
// receiver: a caller that wires no Pause (a test, a one-shot cycle) gets
// a deployment that reports a usage limit on the run that met it and
// pauses nothing -- see Config.Pause.
//
// Every method is safe for concurrent callers.
type Pause struct {
	mu sync.Mutex
	// windows is every pause in force, keyed by PauseScope.key. Empty
	// when nothing is paused. A map rather than a single instant because
	// several credentials can be refusing at once, on windows of their
	// own that reset at different times.
	windows map[string]pauseWindow
	// live is every run currently cancellable by a pause, keyed by run
	// ID -- RunDispatch registers its own runCtx's cancel for as long as
	// its agent is running, and no longer.
	live map[string]liveRun
	// bursts is the recent fast, identical failures observeFastFailure
	// is counting towards a refusal nobody has a marker for, keyed by
	// credential and error text (burstKey). See refusalburst.go.
	bursts map[string][]burstFailure
	// store keeps windows across restarts; nil until Restore attaches
	// one, and nil for good in a test or one-shot cycle that never does.
	store PauseStore
	// persistMu serialises writes to store, so a window opening and
	// being lifted at once cannot reach the store in the wrong order
	// (persist re-reads each window under it).
	persistMu sync.Mutex
}

// PauseStore is where a Pause keeps its windows across a restart --
// *model.Store, in a real deployment. Keys are Pause's own, opaque to
// the store.
type PauseStore interface {
	PutUsageLimitPause(ctx context.Context, key string, pause model.UsageLimitPause) error
	DeleteUsageLimitPause(ctx context.Context, key string) error
	ListUsageLimitPauses(ctx context.Context) ([]model.StoredUsageLimitPause, error)
}

// pauseStoreTimeout bounds one store write, so a wedged store cannot
// hold up the run that met a limit or the tick that expired one.
const pauseStoreTimeout = 10 * time.Second

// storeKey is key as the store holds it: PauseScope.key's NUL
// separators, which Postgres TEXT cannot carry, spelled as "|".
func storeKey(key string) string {
	return strings.ReplaceAll(key, "\x00", "|")
}

// Restore attaches store to p and loads the windows it holds that are
// still open at now, so a restarted daemon keeps passing over the
// credentials its predecessor had found spent. Windows already expired
// are deleted from the store rather than restored. Windows p already
// holds -- one opened before Restore was called -- are kept, the later
// instant winning where both name one scope, and written to the store
// too. The daemon calls it once at startup, before its first dispatch.
//
// It reports how many windows it restored. An error reading the store
// leaves p attached and holding only what it held before: the cost is
// the old in-process behaviour, not a daemon that will not start.
func (p *Pause) Restore(ctx context.Context, store PauseStore, now time.Time) (int, error) {
	if p == nil || store == nil {
		return 0, nil
	}
	p.mu.Lock()
	p.store = store
	p.mu.Unlock()
	rows, err := store.ListUsageLimitPauses(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading usage-limit pauses: %w", err)
	}
	var stale []string
	restored := 0
	p.mu.Lock()
	if p.windows == nil {
		p.windows = map[string]pauseWindow{}
	}
	for _, row := range rows {
		v := row.Pause
		scope := PauseScope{Connection: v.Connection, Model: v.Model, Team: v.Team, Framework: v.Framework}.normalized()
		key := scope.key()
		if row.Key != storeKey(key) || !now.Before(v.Until) {
			// Expired, or written under a key this build would not
			// give it: either way the row goes, and an open window is
			// rewritten under its own key below.
			stale = append(stale, row.Key)
		}
		if !now.Before(v.Until) {
			continue
		}
		if current, held := p.windows[key]; held && !v.Until.After(current.until) {
			continue
		}
		var labels []string
		for _, label := range v.Labels {
			if label = normalizePauseModel(label); label != "" && !slices.Contains(labels, label) {
				labels = append(labels, label)
			}
		}
		p.windows[key] = pauseWindow{scope: scope, labels: labels, until: v.Until.UTC(), reason: v.Reason, escalated: v.Escalated}
		restored++
		log.Printf("orchestrator: restored the usage-limit pause on %s until %s (%s); dispatch onto it stays paused across this restart",
			scope.describe(), v.Until.UTC().Format(time.RFC3339), p.windows[key].scopeNote())
	}
	keys := make([]string, 0, len(p.windows))
	for key := range p.windows {
		keys = append(keys, key)
	}
	p.mu.Unlock()

	p.persistMu.Lock()
	for _, key := range stale {
		if err := store.DeleteUsageLimitPause(ctx, key); err != nil {
			log.Printf("orchestrator: deleting the stored usage-limit pause %q: %v", key, err)
		}
	}
	p.persistMu.Unlock()
	sort.Strings(keys)
	p.persist(keys...)
	return restored, nil
}

// persist writes each of keys' windows to the attached store as they
// stand now -- put when p holds it, deleted when it does not -- so the
// store converges on p whatever order the changes behind them happened
// in. It is called after p.mu is released, by whatever changed them.
// A failed write is logged and otherwise ignored: the gate in memory is
// still right, and only a restart before the next write would miss it.
func (p *Pause) persist(keys ...string) {
	if p == nil || len(keys) == 0 {
		return
	}
	p.mu.Lock()
	store := p.store
	p.mu.Unlock()
	if store == nil {
		return
	}
	p.persistMu.Lock()
	defer p.persistMu.Unlock()
	for _, key := range keys {
		p.mu.Lock()
		w, held := p.windows[key]
		p.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), pauseStoreTimeout)
		var err error
		if held {
			err = store.PutUsageLimitPause(ctx, storeKey(key), w.view())
		} else {
			err = store.DeleteUsageLimitPause(ctx, storeKey(key))
		}
		cancel()
		if err != nil {
			log.Printf("orchestrator: recording the usage-limit pause %q in the store: %v -- the store disagrees with this daemon until the next write",
				storeKey(key), err)
		}
	}
}

// pauseWindow is one pause: what it covers, when that may be dispatched
// again, and what the provider said when it refused (kept for the log
// line and for Pauses' callers).
//
// labels are the models grain passed runs this window's model refused,
// where they were not the model itself: a provider-chosen "auto" run the
// CLI reported was refused on gemini-3-pro holds a window on gemini-3-pro
// that covers "auto" runs too, since the provider is choosing the model
// it has just said is spent. escalated is why a pause on a connection
// covers all of it rather than one model, "" for a pause that never
// named a model.
type pauseWindow struct {
	scope     PauseScope
	labels    []string
	until     time.Time
	reason    string
	escalated string
}

// coversRun is w.scope.covers(run), widened to the model labels w also
// holds.
func (w pauseWindow) coversRun(run PauseScope) bool {
	if w.scope.covers(run) {
		return true
	}
	if w.scope.Connection == "" || w.scope.Connection != run.Connection || run.Model == "" {
		return false
	}
	for _, label := range w.labels {
		if label == run.Model {
			return true
		}
	}
	return false
}

// liveRun is one registered run: the cancellation a pause reaches it
// through, and the credential it is spending -- which is what decides
// whether a given pause covers it at all.
type liveRun struct {
	scope  PauseScope
	cancel context.CancelCauseFunc
}

// register makes runID, which spends scope's credential, cancellable by
// a pause covering it, and returns the function that stops it being so
// -- which RunDispatch calls the moment its framework returns.
//
// A run registering while a pause that covers it is already in force is
// cancelled on the spot. That is not the ordinary path -- dispatch does
// not start a run on a paused credential -- but a run dispatched by the
// cycle that was already in flight when the pause began has nowhere else
// to be caught, and it would otherwise spend a whole agent run
// discovering the limit for itself.
func (p *Pause) register(runID string, scope PauseScope, now time.Time, cancel context.CancelCauseFunc) func() {
	if p == nil {
		return func() {}
	}
	scope = scope.normalized()
	p.mu.Lock()
	if p.live == nil {
		p.live = map[string]liveRun{}
	}
	p.live[runID] = liveRun{scope: scope, cancel: cancel}
	paused := false
	for _, w := range p.windows {
		if now.Before(w.until) && w.coversRun(scope) {
			paused = true
			break
		}
	}
	p.mu.Unlock()

	if paused {
		cancel(errUsageLimit)
	}
	return func() {
		p.mu.Lock()
		delete(p.live, runID)
		p.mu.Unlock()
	}
}

// Begin records that a run spending scope's credential met limit at
// now: dispatch onto that credential is paused until the window resets,
// and every run in flight on it is cancelled. It returns the instant
// dispatch onto it resumes, which is what the caller records in the
// run's own detail. BeginWindow is the same, reporting the whole pause
// it left in force.
func (p *Pause) Begin(now time.Time, scope PauseScope, limit *agent.UsageLimitError) time.Time {
	return p.BeginWindow(now, scope, limit).Until
}

// BeginWindow is Begin, reporting the pause the limit left in force --
// which can be wider than scope: see below.
//
// A credential already paused keeps the later of the two instants. Two
// runs meeting the same limit within a second of each other is the
// ordinary case -- they were in flight together -- and the second must
// not be able to shorten the wait the first established, while a genuine
// second refusal naming a later reset should extend it. Two *different*
// credentials meeting their own limits keep two windows, neither of
// which says anything about the other.
//
// A run on a connection whose model is known pauses that model on that
// connection, and nothing else: the connection's other models keep
// dispatching, and only its runs on the same model are cancelled. The
// model is the one the provider's error named where it named one
// (agent.UsageLimitError.Model -- the concrete model an "auto" run was
// refused on), and the one grain passed the CLI otherwise. The pause
// escalates to the whole connection, and cancels every run on it, when
// that is what the evidence says:
//
//   - the provider said the refusal is account- or key-wide
//     (agent.LimitScopeAccount: credits, billing, an organisation quota);
//   - or a second, different model on the same connection met a limit
//     while the first model's pause is still open -- two of a key's
//     models spent at once is read as the key being spent.
//
// The escalated pause sits beside the model pauses it grew from rather
// than replacing them, so lifting the connection's by hand leaves each
// model's own window to run out as it would have.
func (p *Pause) BeginWindow(now time.Time, scope PauseScope, limit *agent.UsageLimitError) model.UsageLimitPause {
	if p == nil {
		return model.UsageLimitPause{}
	}
	scope = scope.normalized()
	until := resumeAt(now, limit)
	reason := ""
	if limit != nil {
		reason = limit.Error()
	}
	label := ""
	if named := normalizePauseModel(limitModel(limit)); scope.Model != "" && named != "" && named != scope.Model {
		label, scope.Model = scope.Model, named
	}

	p.mu.Lock()
	if p.windows == nil {
		p.windows = map[string]pauseWindow{}
	}
	escalated := ""
	if scope.Connection != "" && scope.Model != "" {
		if limit != nil && limit.Scope == agent.LimitScopeAccount {
			escalated = "the provider's refusal is account-wide"
		} else if other := p.otherModelPausedLocked(scope, now); other != "" {
			escalated = fmt.Sprintf("a second model (%s) met a usage limit while %s's pause was still open", scope.Model, other)
		}
		if escalated != "" {
			scope.Model, label = "", ""
		}
	}
	key := scope.key()
	current, held := p.windows[key]
	extended := !held || until.After(current.until)
	window := current
	if extended {
		window = pauseWindow{scope: scope, labels: current.labels, until: until, reason: reason, escalated: escalated}
		if held && escalated == "" {
			window.escalated = current.escalated
		}
	} else {
		until = current.until
	}
	if label != "" && !slices.Contains(window.labels, label) {
		window.labels = append(window.labels, label)
	}
	p.windows[key] = window
	defer p.persist(key)
	cancels := make(map[string]context.CancelCauseFunc, len(p.live))
	for runID, run := range p.live {
		if window.coversRun(run.scope) {
			cancels[runID] = run.cancel
		}
	}
	p.mu.Unlock()

	cancelled := make([]string, 0, len(cancels))
	for runID, cancel := range cancels {
		cancelled = append(cancelled, runID)
		cancel(errUsageLimit)
	}
	sort.Strings(cancelled)
	if extended {
		log.Printf("orchestrator: %s -- dispatch onto %s paused until %s (%s); cancelling %d live run(s) %v",
			reason, scope.describe(), until.Format(time.RFC3339), window.scopeNote(), len(cancelled), cancelled)
	}
	return window.view()
}

// limitModel is the model limit's own error names, "" for none.
func limitModel(limit *agent.UsageLimitError) string {
	if limit == nil {
		return ""
	}
	return limit.Model
}

// otherModelPausedLocked is the model of a pause still open at now on
// scope's connection on a model other than scope's own, "" for none --
// the evidence BeginWindow escalates on. p.mu must be held.
func (p *Pause) otherModelPausedLocked(scope PauseScope, now time.Time) string {
	var others []string
	for _, w := range p.windows {
		if now.Before(w.until) && w.scope.Connection == scope.Connection && w.scope.Model != "" && w.scope.Model != scope.Model &&
			!slices.Contains(w.labels, scope.Model) {
			others = append(others, w.scope.Model)
		}
	}
	if len(others) == 0 {
		return ""
	}
	sort.Strings(others)
	return others[0]
}

// scopeNote is what a log line says of w's reach: model-scoped, or
// escalated to its connection and why.
func (w pauseWindow) scopeNote() string {
	switch {
	case w.scope.Connection == "":
		return "no connection: scoped to team and framework"
	case w.scope.Model != "":
		return "model-scoped: the connection's other models keep dispatching"
	case w.escalated != "":
		return "escalated to the whole connection: " + w.escalated
	default:
		return "connection-wide: the run's model is unknown"
	}
}

// Blocked reports which runs are gated at now -- see Coverage, which is
// what a caller asks "is this task's credential paused?" of. A pause
// whose instant has passed is cleared here, by the first caller to ask
// after it expired, which is what makes resuming a thing that happens on
// its own rather than something a later failure has to notice.
//
// The reconciler that calls this is level-triggered like every other
// (Reconciler's own doc comment), so "has the window reset yet?" is
// answered by asking again next tick and not by a timer this would
// otherwise have to own.
func (p *Pause) Blocked(now time.Time) Coverage {
	if p == nil {
		return Coverage{}
	}
	// Deferred before the unlock below, so it runs after it: the
	// expired windows' rows are deleted once p.mu is released.
	var expired []string
	defer func() { p.persist(expired...) }()
	p.mu.Lock()
	defer p.mu.Unlock()
	var covered Coverage
	for key, w := range p.windows {
		if !now.Before(w.until) {
			log.Printf("orchestrator: the usage-limit pause on %s set until %s (%s) has expired; dispatch onto it resumes",
				w.scope.describe(), w.until.Format(time.RFC3339), w.scopeNote())
			delete(p.windows, key)
			expired = append(expired, key)
			continue
		}
		covered.windows = append(covered.windows, w)
	}
	return covered
}

// Coverage is which runs the pauses in force at one instant cover: one
// tick's answer to "may this task be dispatched?", taken once (Blocked)
// and then asked of every candidate without holding the lock again.
//
// The zero value covers nothing, which is what an unpaused deployment --
// and a nil Pause -- reports.
type Coverage struct {
	windows []pauseWindow
}

// Any reports whether anything at all is paused. A caller with work to
// do only when something is (reconcileDispatch's per-task lookup) asks
// this first.
func (c Coverage) Any() bool { return len(c.windows) > 0 }

// Covers reports whether a run spending run's credential is gated.
func (c Coverage) Covers(run PauseScope) bool {
	run = run.normalized()
	for _, w := range c.windows {
		if w.coversRun(run) {
			return true
		}
	}
	return false
}

// pausedUntilWords is the part of Reason's sentence that says it is a
// pause, which usageLimitWait reads back off the quota wait gatedByPause
// records with it.
const pausedUntilWords = " is paused until "

// Reason returns a human-readable explanation when at least one active window
// in c covers run, or "" when run is not covered. When multiple windows match,
// the one with the latest expiry wins.
func (c Coverage) Reason(run PauseScope) string {
	run = run.normalized()
	var matched *pauseWindow
	for i := range c.windows {
		w := &c.windows[i]
		if !w.coversRun(run) {
			continue
		}
		if matched == nil || w.until.After(matched.until) {
			matched = w
		}
	}
	if matched == nil {
		return ""
	}
	pause := fmt.Sprintf("dispatch onto %s%s%s",
		matched.scope.describe(), pausedUntilWords, matched.until.UTC().Format(time.RFC3339))
	if matched.reason == "" {
		return pause
	}
	return matched.reason + "; " + pause
}

// ModelPaused reports whether a run on connection, of team -- whose
// credential connection owns, as model.AgentBackend.CredentialOwnerID
// names it -- on modelName is gated at now: a pause on that model there,
// or on the whole connection. It is what the daemon's connection pool
// asks of each candidate, with the model the task would run there, so a
// team's work rotates onto its connections whose keys still have budget
// for that model, and waits only when none has
// (daemon.AgentPool.Availability). An empty modelName asks only about
// pauses on the whole connection (ConnectionPaused).
//
// Like Until, and unlike Blocked, it expires nothing: the pool asking
// must not be able to open the gate out from under the reconcile loop
// that owns it, so a window that has passed simply covers nothing.
func (p *Pause) ModelPaused(connection, modelName, team string, now time.Time) bool {
	_, paused := p.ModelPausedUntil(connection, modelName, team, now)
	return paused
}

// ModelPausedUntil is ModelPaused, and when the run it asks about may
// next be dispatched: the latest end of the windows covering it.
func (p *Pause) ModelPausedUntil(connection, modelName, team string, now time.Time) (time.Time, bool) {
	if p == nil || strings.TrimSpace(connection) == "" {
		return time.Time{}, false
	}
	run := PauseScope{Connection: connection, Model: modelName, Team: team}.normalized()
	p.mu.Lock()
	defer p.mu.Unlock()
	var until time.Time
	for _, w := range p.windows {
		if now.Before(w.until) && w.coversRun(run) && w.until.After(until) {
			until = w.until
		}
	}
	return until, !until.IsZero()
}

// ConnectionPaused reports whether connection, of team, is paused as a
// whole at now: a pause escalated to it (BeginWindow), or one met by a
// run whose model grain could not name. A pause on one of its models is
// ModelPaused's question, not this one.
func (p *Pause) ConnectionPaused(connection, team string, now time.Time) bool {
	return p.ModelPaused(connection, "", team, now)
}

// Lift clears every pause in force before its window has run out, and
// reports whether there was one to clear. It is a system admin's "lift
// all" (LiftWhere is the per-connection and per-model lift a team admin
// has).
func (p *Pause) Lift() bool {
	return len(p.LiftWhere(func(model.UsageLimitPause) bool { return true })) > 0
}

// LiftWhere clears every pause in force that match accepts, before its
// window has run out, and reports the ones it cleared. It is the
// operator's own override: someone who has just topped a plan up, or
// moved a team onto another connection, is holding information this
// process cannot have -- the credential behind the refusal is not the
// credential the next run would spend -- and has no reason to sit out
// the remainder of a window that no longer applies (grain/task-132, over
// the daemon's own DELETE /api/pause, which decides who may lift which
// pause and hands that decision in as match).
//
// If a limit is in fact still in force, the run that follows meets it
// again and shuts that credential's gate again, which costs one attempt
// and is the same self-correcting shape as a window that expires into one
// that has not really reset.
//
// It stops dispatch being gated and nothing more. Runs Begin cancelled
// stay cancelled -- they are over, and their attempts are recorded as
// model.PausedOutcome, which no streak counts -- so what a lift buys is
// the next tick dispatching rather than skipping.
func (p *Pause) LiftWhere(match func(model.UsageLimitPause) bool) []model.UsageLimitPause {
	if p == nil || match == nil {
		return nil
	}
	// Deferred before the unlock below, so the lifted windows' rows are
	// deleted once p.mu is released.
	var keys []string
	defer func() { p.persist(keys...) }()
	p.mu.Lock()
	defer p.mu.Unlock()
	var lifted []model.UsageLimitPause
	for key, w := range p.windows {
		view := w.view()
		if !match(view) {
			continue
		}
		log.Printf("orchestrator: the usage-limit pause on %s set until %s (%s) was lifted by hand; dispatch onto it resumes",
			w.scope.describe(), w.until.Format(time.RFC3339), w.scopeNote())
		delete(p.windows, key)
		keys = append(keys, key)
		lifted = append(lifted, view)
	}
	sortPauses(lifted)
	return lifted
}

// view is w as model.UsageLimitPause carries it out of this package.
func (w pauseWindow) view() model.UsageLimitPause {
	return model.UsageLimitPause{
		Connection: w.scope.Connection,
		Model:      w.scope.Model,
		Labels:     slices.Clone(w.labels),
		Team:       w.scope.Team,
		Framework:  w.scope.Framework,
		Until:      w.until.UTC(),
		Reason:     w.reason,
		Escalated:  w.escalated,
	}
}

// Pauses is every pause in force at now, sorted by team and then by
// connection -- what an operator's banner needs to say *whose*
// credential has stopped and which team's work that holds, rather than
// implying the whole deployment has stopped (ui.AgentPauseStatus). The
// UI shows each viewer only the ones for teams they can see.
//
// Like Until, and unlike Blocked, it expires nothing: a UI poll must not
// be able to open the gate out from under the reconcile loop that owns
// it, so a window that has passed is simply left out of the answer.
func (p *Pause) Pauses(now time.Time) []model.UsageLimitPause {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []model.UsageLimitPause
	for _, w := range p.windows {
		if now.Before(w.until) {
			out = append(out, w.view())
		}
	}
	sortPauses(out)
	return out
}

func sortPauses(pauses []model.UsageLimitPause) {
	sort.Slice(pauses, func(i, j int) bool {
		a, b := pauses[i], pauses[j]
		if !strings.EqualFold(a.Team, b.Team) {
			return strings.ToLower(a.Team) < strings.ToLower(b.Team)
		}
		if a.Connection != b.Connection {
			return a.Connection < b.Connection
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		return a.Framework < b.Framework
	})
}

// Until is the instant the last pause in force lifts, and that pause's
// own reason -- for a caller that wants to show the current state rather
// than act on it, and for a test asserting on what Begin recorded. A
// zero time means nothing is paused; an instant already in the past
// means a pause nothing has asked about since it expired. Which
// credentials are covered, and whose they are, is Pauses' question.
func (p *Pause) Until() (time.Time, string) {
	if p == nil {
		return time.Time{}, ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var until time.Time
	var reason string
	for _, w := range p.windows {
		if w.until.After(until) {
			until, reason = w.until, w.reason
		}
	}
	return until, reason
}

// frameworkFor is which agent framework a task's runs are driven by, as
// a name: the task's own override where it made one
// (model.Task.AgentFramework), and the deployment's default where it did
// not (Config.AgentFramework, refreshed from the store each cycle by
// RunCycle).
//
// It is the same resolution Deps.Framework performs when it *builds* the
// framework -- cmd/grain's agentFrameworks, whose own default comes from
// the same grain_config row and the same -agent-framework flag behind it
// -- spelled out here because this package needs the name without
// building anything: which pause covers a run that is on no connection (runPauseScope), and
// which ready tasks a pause covers (reconcileDispatch).
//
// The two answers can differ for the length of one tick, if the stored
// default is changed between a cycle's config read and a dispatch's own.
// Nothing is lost either way: a task dispatched onto a framework that
// turns out to be paused meets the limit and pauses it again, and one
// passed over that need not have been is dispatched by the next tick.
func frameworkFor(task model.Task, cfg Config) string {
	if name := model.NormalizeAgentFrameworkName(task.AgentFramework); name != "" {
		return name
	}
	return model.NormalizeAgentFramework(cfg.AgentFramework)
}

// resumeAt turns a provider's own account of when it will serve again
// into the instant this deployment will actually try again, bounded by
// the three constants above. A nil limit -- a caller with no detail at
// all -- is read the same way as a refusal that named no reset.
func resumeAt(now time.Time, limit *agent.UsageLimitError) time.Time {
	until := now.Add(defaultUsageLimitPause)
	if limit != nil {
		if named, ok := limit.ResumeAt(now); ok {
			until = named
		}
	}
	if floor := now.Add(minUsageLimitPause); until.Before(floor) {
		until = floor
	}
	if ceiling := now.Add(maxUsageLimitPause); until.After(ceiling) {
		until = ceiling
	}
	return until.UTC()
}
