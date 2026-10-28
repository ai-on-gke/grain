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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// StandingSandboxSupervisor tracks and supervises running standing sandboxes.
// It starts the sandboxes defined in grain/config, restarts one whose
// definition or resolved secret values change, and restarts one whose
// launch failed, whose instance a liveness probe finds dead (see
// ServingSandboxProber), or that a caller has reported with MarkFailed, on
// the next reconcile.
//
// mu guards active and is only ever held for map reads and writes, never
// across a launch, a release or a probe -- a granule launch boots a VM and
// can take minutes, and GetActive and MarkFailed must not wait for it.
// reconcileMu serializes whole passes instead, so two overlapping
// reconciles cannot both launch an instance for the same name.
type StandingSandboxSupervisor struct {
	reconcileMu sync.Mutex
	mu          sync.Mutex
	active      map[string]*ActiveStandingSandbox
	launcher    func(ctx context.Context, spec ServeSpec) (ServingSandbox, error)
}

// ActiveStandingSandbox represents a currently active/supervised standing sandbox.
type ActiveStandingSandbox struct {
	Def       model.StandingSandbox
	Instance  ServingSandbox
	StartedAt time.Time
	Restarts  int
	LastError string
	Failed    bool
	// Sandbox is the backend sandbox name the instance is launched under
	// (standingSandboxName), which is what a granule VM is named after.
	Sandbox string
	// Failures counts launches in a row that have failed, and NextAttempt
	// is the earliest the next one will be made (standingBackoff). Both
	// are zero while the last launch succeeded.
	Failures    int
	NextAttempt time.Time

	// envDigest is a digest of the environment Instance was launched
	// with, secret values included, so a rotated secret restarts the
	// instance even though its definition -- which names secrets but does
	// not hold them -- is unchanged.
	envDigest string
	// probeMisses counts consecutive failed liveness probes.
	probeMisses   int
	releaseLedger func()
	releaseMeter  func(context.Context)
}

func standingSandboxCommitment(sb model.StandingSandbox, defaultShape model.Commitment, sandboxes Sandboxes) model.Commitment {
	def := defaultShape
	if def.IsZero() && sandboxes != nil {
		if shaper, ok := sandboxes.(interface{ DefaultShape() Shape }); ok {
			ds := shaper.DefaultShape()
			def = model.Commitment{CPUs: ds.CPUs, MemoryMB: ds.MemoryMB, DiskGB: ds.DiskGB}
		}
	}
	if def.IsZero() && sb.CPUs <= 0 && sb.MemoryMB <= 0 && sb.DiskGB <= 0 {
		return model.Commitment{}
	}
	cpus := sb.CPUs
	if cpus <= 0 {
		cpus = def.CPUs
	}
	memMB := sb.MemoryMB
	if memMB <= 0 {
		memMB = def.MemoryMB
	}
	diskGB := sb.DiskGB
	if diskGB <= 0 {
		diskGB = def.DiskGB
	}
	return model.Commitment{CPUs: cpus, MemoryMB: memMB, DiskGB: diskGB}
}

// ServingSandboxProber is a ServingSandbox that can say whether the server
// it was started with is still running. Optional: an instance that does
// not implement it is taken to be alive for as long as it is held.
type ServingSandboxProber interface {
	// Alive returns nil while the served command is running, and an error
	// saying why not once it, or the sandbox holding it, is gone.
	Alive(ctx context.Context) error
}

// standingProbeMisses is how many consecutive failed probes it takes to
// declare an instance dead. More than one, so a single dropped exec into a
// healthy guest does not cost a VM rebuild.
const standingProbeMisses = 2

// standingProbeTimeout bounds one liveness probe.
const standingProbeTimeout = 30 * time.Second

// standingBackoffBase and standingBackoffMax bound how long a standing
// sandbox whose launch keeps failing waits between attempts. A launch on
// granule is a VM create, and a definition that cannot start -- a bad
// image, a secret that does not resolve -- fails the same way every time,
// so retrying it on every reconcile tick (seconds apart) only burns the
// host's time and fills the log.
const (
	standingBackoffBase = 30 * time.Second
	standingBackoffMax  = 15 * time.Minute
)

// standingBackoff is the wait after failures launches in a row have
// failed: standingBackoffBase, doubling each time, up to
// standingBackoffMax.
func standingBackoff(failures int) time.Duration {
	d := standingBackoffBase
	for i := 1; i < failures && d < standingBackoffMax; i++ {
		d *= 2
	}
	return min(d, standingBackoffMax)
}

// standingSandboxName is the backend sandbox name a standing sandbox is
// launched under: "s" and eight hex digits of a hash of its declared name.
//
// Not the declared name itself, because a sandbox name has to fit what
// the backend derives from it (ServeSpec.Name): granule names the VM
// VMNamePrefix plus the sandbox name, under netshim's 11-byte limit, which
// leaves nine bytes -- and a standing sandbox declared as "preview-env"
// would fail every launch, after the definition had been accepted. Hashing
// keeps the name stable across restarts and daemons, and leaves the
// declared name free to say what the sandbox is; it is still what the
// supervisor, Sandbox health and the guest's unit ("standing-<name>") go
// by. The leading "s" keeps it out of the namespace agent runs ("<task
// id>-<attempt>", digits and a hyphen) and CI jobs ("c...") name their
// sandboxes in.
func standingSandboxName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "s" + hex.EncodeToString(sum[:4])
}

// NewStandingSandboxSupervisor creates a new supervisor instance.
func NewStandingSandboxSupervisor(launcher func(ctx context.Context, spec ServeSpec) (ServingSandbox, error)) *StandingSandboxSupervisor {
	return &StandingSandboxSupervisor{
		active:   make(map[string]*ActiveStandingSandbox),
		launcher: launcher,
	}
}

var (
	defaultStandingSupervisorMu sync.Mutex
	defaultStandingSupervisor   *StandingSandboxSupervisor
)

func getDefaultStandingSupervisor() *StandingSandboxSupervisor {
	defaultStandingSupervisorMu.Lock()
	defer defaultStandingSupervisorMu.Unlock()
	if defaultStandingSupervisor == nil {
		defaultStandingSupervisor = NewStandingSandboxSupervisor(nil)
	}
	return defaultStandingSupervisor
}

// reconcileStandingSandboxes reconciles the desired state of long-standing sandboxes
// (stored in the grain_config settings table) with the live running sandboxes.
func reconcileStandingSandboxes(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil {
		return nil
	}

	sandboxes, err := deps.Store.ListStandingSandboxes(ctx)
	if err != nil {
		return fmt.Errorf("listing standing sandboxes: %w", err)
	}

	supervisor := deps.StandingSandboxes
	if supervisor == nil {
		supervisor = getDefaultStandingSupervisor()
	}

	return supervisor.Reconcile(ctx, deps, sandboxes, now)
}

// Reconcile synchronizes the supervisor's running instances with the desired definitions.
//
// It works from a snapshot of the tracked instances taken under mu, does
// every launch, release and probe with mu released, and re-takes mu only
// to record each result.
func (s *StandingSandboxSupervisor) Reconcile(ctx context.Context, deps Deps, desired []model.StandingSandbox, now time.Time) error {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()

	desiredMap := make(map[string]model.StandingSandbox, len(desired))
	for _, sb := range desired {
		desiredMap[sb.Name] = sb
	}

	// Only Reconcile adds or removes entries, and reconcileMu keeps it to
	// one pass at a time, so the snapshot's set of names stays current for
	// the whole pass; only MarkFailed can change an entry behind it, and a
	// failure it reports after the snapshot is acted on next pass.
	s.mu.Lock()
	snapshot := make(map[string]ActiveStandingSandbox, len(s.active))
	for name, active := range s.active {
		snapshot[name] = *active
	}
	s.mu.Unlock()

	// 1. Stop and release any instances that are no longer in desired set or have been disabled.
	for name, active := range snapshot {
		sb, exists := desiredMap[name]
		if !exists || !sb.Enabled {
			log.Printf("orchestrator: standing sandbox %q is disabled or removed; stopping instance", name)
			s.release(ctx, name, active)
			s.mu.Lock()
			delete(s.active, name)
			s.mu.Unlock()
		}
	}

	// 2. Ensure all enabled desired sandboxes are running and healthy.
	for _, sb := range desired {
		if !sb.Enabled {
			continue
		}

		env, digest, envErr := resolveStandingEnv(ctx, deps, sb)
		active, running := snapshot[sb.Name]
		if !running {
			// Sandbox is not running (e.g. system boot or new definition) -> Start it.
			log.Printf("orchestrator: starting standing sandbox %q (image: %s)", sb.Name, sb.Image)
			s.start(ctx, deps, sb, env, digest, envErr, now, ActiveStandingSandbox{}, false)
			continue
		}

		// Sandbox is tracked. Check if its definition, or the secret
		// values it was started with, have changed. A secret that cannot
		// be resolved right now is not a reason to stop an instance that
		// is running on the value it already has.
		if !definitionsEqual(active.Def, sb) || (envErr == nil && digest != active.envDigest) {
			if definitionsEqual(active.Def, sb) {
				log.Printf("orchestrator: standing sandbox %q secret values changed; restarting instance", sb.Name)
			} else {
				log.Printf("orchestrator: standing sandbox %q definition updated; restarting instance", sb.Name)
			}
			s.release(ctx, sb.Name, active)
			// A changed definition is tried at once, whatever backoff the
			// old one had earned: it may well be the fix.
			active.Failures = 0
			active.NextAttempt = time.Time{}
			s.start(ctx, deps, sb, env, digest, envErr, now, active, false)
			continue
		}
		if envErr != nil {
			log.Printf("orchestrator: standing sandbox %q: %v; keeping the running instance", sb.Name, envErr)
		}

		// Check if the running sandbox failed, either as reported or as
		// its liveness probe finds it.
		if !active.Failed && active.Instance != nil {
			if dead := s.probe(ctx, sb.Name, active.Instance); dead != "" {
				active.Failed = true
				active.LastError = dead
			}
		}
		if active.Failed {
			if active.Instance == nil && now.Before(active.NextAttempt) {
				// The last launch failed and its backoff has not run out.
				continue
			}
			log.Printf("orchestrator: standing sandbox %q failed (%s); attempting restart", sb.Name, active.LastError)
			s.release(ctx, sb.Name, active)
			// The digest the instance last ran with is kept if the secrets
			// cannot be resolved now; start records the failure either way.
			s.start(ctx, deps, sb, env, digest, envErr, now, active, true)
		}
	}

	if deps.Ledger != nil {
		s.mu.Lock()
		liveStanding := make(map[string]model.Commitment, len(s.active))
		for _, active := range s.active {
			if active.Instance != nil && !active.Failed {
				liveStanding[active.Sandbox] = standingSandboxCommitment(active.Def, deps.AgentShape, deps.Sandboxes)
			}
		}
		s.mu.Unlock()
		deps.Ledger.Reserve(model.PoolStanding, liveStanding)
	}

	return nil
}

// start launches sb and records the outcome over prev, the entry it
// replaces (zero for a sandbox that was not tracked). A restart of a failed
// instance counts towards Restarts; a restart for a changed definition does
// not. A failed launch counts towards Failures and sets NextAttempt, which
// Reconcile waits for before trying again; a good one clears both.
func (s *StandingSandboxSupervisor) start(ctx context.Context, deps Deps, sb model.StandingSandbox, env map[string]string, digest string, envErr error, now time.Time, prev ActiveStandingSandbox, afterFailure bool) {
	next := prev
	next.Def = sb
	next.Sandbox = standingSandboxName(sb.Name)
	next.Instance = nil
	next.releaseLedger = nil
	next.releaseMeter = nil
	next.probeMisses = 0

	var inst ServingSandbox
	var releaseLedger func()
	var releaseMeter func(context.Context)
	err := envErr
	if err == nil {
		inst, releaseLedger, releaseMeter, err = s.launch(ctx, deps, sb, env)
	}
	if err != nil {
		log.Printf("orchestrator: error starting standing sandbox %q: %v", sb.Name, err)
		if prev.Def.Name == "" {
			next.StartedAt = now
		}
		next.LastError = err.Error()
		next.Failed = true
		if envErr == nil {
			// The environment this attempt was made with, so the next
			// reconcile does not take the digest of a sandbox that has never
			// launched ("") for rotated secrets and relaunch at once -- which
			// is how a failing launch used to be retried on every tick. A
			// secret that really does change still gets a prompt retry.
			next.envDigest = digest
		}
		next.Failures++
		next.NextAttempt = now.Add(standingBackoff(next.Failures))
		log.Printf("orchestrator: standing sandbox %q: %d failed launch(es) in a row; next attempt at %s",
			sb.Name, next.Failures, next.NextAttempt.Format(time.RFC3339))
	} else {
		next.Instance = inst
		next.releaseLedger = releaseLedger
		next.releaseMeter = releaseMeter
		next.StartedAt = now
		next.LastError = ""
		next.Failed = false
		next.envDigest = digest
		next.Failures = 0
		next.NextAttempt = time.Time{}
		if afterFailure {
			next.Restarts++
		}
	}

	s.mu.Lock()
	s.active[sb.Name] = &next
	s.mu.Unlock()
}

// probe asks inst whether it is still serving. It returns why it is dead
// once standingProbeMisses probes in a row have failed, and "" otherwise.
// The miss count is kept on the tracked entry, and only for the instance
// probed: one replaced since the snapshot is not charged for it.
func (s *StandingSandboxSupervisor) probe(ctx context.Context, name string, inst ServingSandbox) string {
	prober, ok := inst.(ServingSandboxProber)
	if !ok {
		return ""
	}
	probeCtx, cancel := context.WithTimeout(ctx, standingProbeTimeout)
	err := prober.Alive(probeCtx)
	cancel()
	if err != nil && ctx.Err() != nil {
		// Shutting down, not a dead instance.
		return ""
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	active, ok := s.active[name]
	if !ok || active.Instance != inst {
		return ""
	}
	if err == nil {
		active.probeMisses = 0
		return ""
	}
	active.probeMisses++
	log.Printf("orchestrator: standing sandbox %q liveness probe failed (%d/%d): %v", name, active.probeMisses, standingProbeMisses, err)
	if active.probeMisses < standingProbeMisses {
		return ""
	}
	return "liveness probe: " + err.Error()
}

// release gives back inst on a context detached from the reconcile's: a
// granule Release execs `granule vm delete`, which on a cancelled ctx (the
// daemon shutting down) would never run and would leave the VM behind until
// ReapOrphans at the next start. A failed release is logged rather than
// dropped, since nothing else will say the VM is still up.
func (s *StandingSandboxSupervisor) release(ctx context.Context, name string, active ActiveStandingSandbox) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
	defer cancel()
	if active.Instance != nil {
		if err := active.Instance.Release(releaseCtx); err != nil {
			log.Printf("orchestrator: releasing standing sandbox %q: %v", name, err)
		}
	}
	if active.releaseMeter != nil {
		active.releaseMeter(releaseCtx)
	}
	if active.releaseLedger != nil {
		active.releaseLedger()
	}
}

// MarkFailed marks a standing sandbox instance as failed, prompting the supervisor to restart it.
func (s *StandingSandboxSupervisor) MarkFailed(name string, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if active, ok := s.active[name]; ok {
		active.Failed = true
		active.LastError = reason
	}
}

// GetActive returns a copy of active standing sandbox status by name.
func (s *StandingSandboxSupervisor) GetActive(name string) (*ActiveStandingSandbox, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active, ok := s.active[name]
	if !ok {
		return nil, false
	}
	cp := *active
	return &cp, true
}

// List returns a copy of every tracked standing sandbox, sorted by name.
// Like GetActive it holds mu only for the copy, so it never waits on a
// launch, release or probe in progress.
func (s *StandingSandboxSupervisor) List() []ActiveStandingSandbox {
	s.mu.Lock()
	out := make([]ActiveStandingSandbox, 0, len(s.active))
	for _, active := range s.active {
		out = append(out, *active)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Def.Name < out[j].Def.Name })
	return out
}

// resolveStandingEnv builds sb's environment, its secrets resolved out of
// Grain's secret store, and a digest of the result to compare against the
// one a running instance was started with.
func resolveStandingEnv(ctx context.Context, deps Deps, sb model.StandingSandbox) (map[string]string, string, error) {
	env := make(map[string]string, len(sb.Env)+len(sb.Secrets))
	for k, v := range sb.Env {
		env[k] = v
	}

	if len(sb.Secrets) > 0 {
		creds := deps.Config.Credentials
		if creds == nil {
			return nil, "", fmt.Errorf("credentials resolver required to resolve secrets for standing sandbox %q", sb.Name)
		}
		for _, sec := range sb.Secrets {
			val, err := creds.Resolve(ctx, sec.SecretName)
			if err != nil {
				return nil, "", fmt.Errorf("resolving secret %q for standing sandbox %q: %w", sec.SecretName, sb.Name, err)
			}
			if sec.EnvVar != "" {
				env[sec.EnvVar] = val
			}
		}
	}

	return env, envDigest(env), nil
}

// envDigest hashes env in a fixed order. Only the digest is kept, so the
// supervisor never holds a secret value longer than one launch.
func envDigest(env map[string]string) string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		fmt.Fprintf(h, "%d:%s%d:%s", len(name), name, len(env[name]), env[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *StandingSandboxSupervisor) launch(ctx context.Context, deps Deps, sb model.StandingSandbox, env map[string]string) (ServingSandbox, func(), func(context.Context), error) {
	cmd := sb.Command
	if cmd == "" {
		cmd = "sleep infinity"
	}

	shape := Shape{
		CPUs:          sb.CPUs,
		MemoryMB:      sb.MemoryMB,
		DiskGB:        sb.DiskGB,
		NetworkAccess: sb.NetworkAccess,
	}

	name := standingSandboxName(sb.Name)
	spec := ServeSpec{
		Name:    name,
		Image:   sb.Image,
		Shape:   shape,
		Command: cmd,
		Dir:     sb.Dir,
		Env:     env,
		Unit:    "standing-" + sb.Name,
		Prepare: func(ctx context.Context, sandbox Sandbox) {
			prep := PrepareSandbox(ctx, deps.Config.PackageCacheSource(), sandbox)
			log.Printf("orchestrator: standing sandbox %q: its sandbox %s", sb.Name, prep.Summary())
		},
	}

	want := standingSandboxCommitment(sb, deps.AgentShape, deps.Sandboxes)
	var releaseLedger func()
	if deps.Budget.Known() {
		verdict, release := deps.Ledger.Admit(deps.Budget, model.PoolStanding, name, want)
		if !verdict.OK {
			return nil, nil, nil, fmt.Errorf("sandbox budget refused standing sandbox %q: %s", sb.Name, verdict.Reason)
		}
		releaseLedger = release
	} else {
		releaseLedger = deps.Ledger.Charge(model.PoolStanding, name, want)
	}

	if s.launcher != nil {
		inst, err := s.launcher(ctx, spec)
		if err != nil {
			if releaseLedger != nil {
				releaseLedger()
			}
			return nil, nil, nil, err
		}
		return inst, releaseLedger, func(context.Context) {}, nil
	}

	if serving, ok := deps.Sandboxes.(ServingSandboxes); ok {
		inst, releaseMeter, err := MeterFor(deps.Store).Serve(ctx, serving, model.SandboxKindAgent, spec)
		if err != nil {
			if releaseLedger != nil {
				releaseLedger()
			}
			return nil, nil, nil, err
		}
		return inst, releaseLedger, releaseMeter, nil
	}

	if releaseLedger != nil {
		releaseLedger()
	}
	return nil, nil, nil, fmt.Errorf("sandboxes backend does not support serving standing sandboxes: %w", CannotServe(deps.Sandboxes))
}

func definitionsEqual(a, b model.StandingSandbox) bool {
	if a.Name != b.Name || a.Image != b.Image || a.Command != b.Command || a.Dir != b.Dir {
		return false
	}
	if a.CPUs != b.CPUs || a.MemoryMB != b.MemoryMB || a.DiskGB != b.DiskGB || a.NetworkAccess != b.NetworkAccess || a.Enabled != b.Enabled {
		return false
	}
	if !reflect.DeepEqual(a.Env, b.Env) {
		return false
	}
	if !reflect.DeepEqual(a.Secrets, b.Secrets) {
		return false
	}
	return true
}
