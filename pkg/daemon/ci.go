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

package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/actionscache"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/capability/portforward"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/grit/actions"
	"github.com/ai-on-gke/grain/pkg/gritapi"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/policy"
)

// ciReadScopes is the registry of read-only git scopes CI jobs hold
// while they run: this sandbox, this one repository, fetch only
// (gitproxy.ReadScopes). The proxy reads it on every request and the CI
// sandbox adapter below writes it as jobs start and finish.
//
// Package-level for the reason sandboxRecreations is: startGitProxy
// hands it to the proxy and buildCIService hands it to the engine, and
// the two need to name the same registry without either owning the
// other. Empty is the resting state -- a deployment running no CI grants
// nothing through it, and the proxy answers exactly as it did before it
// existed.
var CiReadScopes = gitproxy.NewReadScopes()

// SandboxCapabilities is the registry of capability tools a sandbox may
// call through the proxy's /cap/<server>/mcp (grain cap,
// docs/capabilities-spec.md §5.2.6): filled by each agent run's and CI
// job's capability pass for as long as it is materialized, and read by
// the proxy BuildProxy wires.
var SandboxCapabilities = gitproxy.NewCapRegistry()

// ciWriteScopes is the registry of write git scopes CI jobs hold while they run
// (gitproxy.WriteScopes).
var CiWriteScopes = gitproxy.NewWriteScopes()

// ciSandboxTeams is the registry of the team each CI job holding a
// repository access it declared runs as, whose GitHub credential the
// proxy forwards that job's requests with (gitproxy.SandboxTeams,
// grain/task-1654).
var CiSandboxTeams = gitproxy.NewSandboxTeams()

// liveCI holds the CI service runDaemon builds, for the UI/API server
// that was started before it to reach -- livePullRequests' own reason
// and the same shape. A request that arrives before the reconcile loop
// has got there is told so rather than dereferencing nil; in practice
// only an operator clicking within the first second of a start can.
//
// It holds a ci.Runner, which is all anything reading it asks of CI
// (docs/design/grit-split.md#GS-1). Only this package's own wiring
// reaches past that to the in-process service (embedded).
var LiveCI LiveCIRunner

// LiveCIRunner is LiveCI's type: one ci.Runner, published once the
// reconcile loop has built it and read from any goroutine.
type LiveCIRunner struct {
	held atomic.Pointer[liveCIRunner]
}

type liveCIRunner struct {
	runner ci.Runner
	// service is runner when it is the in-process ci.Service, and nil
	// otherwise.
	service *ci.Service
}

// Store publishes runner, or takes the published one back given nil.
func (l *LiveCIRunner) Store(runner ci.Runner) {
	if runner == nil {
		l.held.Store(nil)
		return
	}
	held := &liveCIRunner{runner: runner}
	held.service, _ = runner.(*ci.Service)
	l.held.Store(held)
}

// Load is the published runner, or nil before there is one.
func (l *LiveCIRunner) Load() ci.Runner {
	if held := l.held.Load(); held != nil {
		return held.runner
	}
	return nil
}

// embedded is the published runner as the in-process service it is when
// this daemon runs CI itself, or nil. It is the builder's own handle,
// for the two things a ci.Runner does not offer and only the process
// running the engine can do: put the namespace store's runs in the
// service's own run store (daemonNamespaceCIStore), and move the job
// cache's epoch when the sandbox image changes (applySandboxImage).
func (l *LiveCIRunner) embedded() *ci.Service {
	if held := l.held.Load(); held != nil {
		return held.service
	}
	return nil
}

// ciGate is the ci.Runner the UI/API server and the cycle are given: whatever liveCI
// holds by the time a request arrives, and an honest refusal until
// runDaemon has put one there.
//
// It is only wired at all when this deployment runs CI (-ci), because
// ui.Config.CI being nil is how the API says "this deployment has no
// grain CI" -- a gate wired unconditionally would have GET /api/ci/runs
// answering `enabled: true` on a daemon that will never run a job.
type CiGate struct{}

func (CiGate) Start(ctx context.Context, req ci.Request) (ci.Run, error) {
	service := LiveCI.Load()
	if service == nil {
		return ci.Run{}, errors.New(
			"this daemon's CI service is not up yet: its reconcile loop has not started " +
				"(or has failed -- check the daemon log)")
	}
	return service.Start(ctx, req)
}

func (CiGate) WorkflowOptions(ctx context.Context, req ci.Request) ([]ci.WorkflowOption, error) {
	service := LiveCI.Load()
	if service == nil {
		return nil, errors.New(
			"this daemon's CI service is not up yet: its reconcile loop has not started " +
				"(or has failed -- check the daemon log)")
	}
	return service.WorkflowOptions(ctx, req)
}

func (CiGate) Get(id string) (ci.Run, bool) {
	if service := LiveCI.Load(); service != nil {
		return service.Get(id)
	}
	return ci.Run{}, false
}

func (CiGate) List() []ci.Run {
	if service := LiveCI.Load(); service != nil {
		return service.List()
	}
	return nil
}

func (CiGate) Find(f ci.RunFilter, limit int) ci.RunPage {
	if service := LiveCI.Load(); service != nil {
		return service.Find(f, limit)
	}
	return ci.RunPage{}
}

func (CiGate) Cancel(id, reason string, by ci.CancelSource) bool {
	if service := LiveCI.Load(); service != nil {
		return service.Cancel(id, reason, by)
	}
	return false
}

// MarkFlake and EvictBaseTransitionFailures complete ci.Runner, and both
// are deliberately answered as a runner that cannot do them: the gate
// never forwarded either (before GS-1 the cycle asked for them with a
// type assertion this gate did not satisfy), so a daemon has never
// marked a recheck's flakes or evicted a base's cached failures through
// it. Forwarding them is a behaviour change of its own, kept out of the
// interface split (docs/design/grit-split.md#GS-1).
func (CiGate) MarkFlake(string, []string, string) bool { return false }

func (CiGate) EvictBaseTransitionFailures(context.Context, string) (int, bool, error) {
	return 0, false, nil
}

// The job cache the engine consults, from outside the daemon. A daemon
// whose service is not up yet has no cache to report on either -- ok is
// false, which the API renders as "this deployment runs every job, every
// time", and is true again a second later once the reconcile loop has
// put a service in liveCI.
func (CiGate) CacheStats(ctx context.Context) (ci.CacheStats, bool, error) {
	if service := LiveCI.Load(); service != nil {
		return service.CacheStats(ctx)
	}
	return ci.CacheStats{}, false, nil
}

func (CiGate) ClearCache(ctx context.Context) (int, bool, error) {
	if service := LiveCI.Load(); service != nil {
		return service.ClearCache(ctx)
	}
	return 0, false, nil
}

// Everything failing jobs have left behind, dropped. There is no
// operator-facing route to this and deliberately so (ci.
// ClearableArtifacts): its one caller is the reclaim behind the disk
// floor, on a deployment that is already holding work back
// (diskReclaim, grain/task-213). A daemon whose service is not up yet
// keeps no artifacts either, and answers ok=false rather than pretending
// to have cleared an empty store.
func (CiGate) ClearArtifacts(ctx context.Context) (int, int64, bool, error) {
	if service := LiveCI.Load(); service != nil {
		return service.ClearArtifacts(ctx)
	}
	return 0, 0, false, nil
}

// What the kept artifacts occupy, from outside the daemon -- the
// reading the System pane's Storage section shows beside the bounds that
// sweep them (grain/task-276). There is no button next to it and
// deliberately so (ClearArtifacts above): this is the half of the
// artifact store an operator is offered, because what it costs a
// deployment is disk and what it holds is irreplaceable.
//
// A daemon whose service is not up yet answers ok=false, exactly as the
// cache above does.
func (CiGate) ArtifactStats(ctx context.Context) (ci.ArtifactStats, bool, error) {
	if service := LiveCI.Load(); service != nil {
		return service.ArtifactStats(ctx)
	}
	return ci.ArtifactStats{}, false, nil
}

// What a run left behind, from outside the daemon. A daemon whose
// service is not up yet keeps no artifacts either -- ok is false, which
// the API renders as "this deployment collects none" -- and is answering
// properly a second later, exactly as the cache above is.
func (CiGate) RunArtifacts(ctx context.Context, runID string) ([]ci.Artifact, bool, error) {
	if service := LiveCI.Load(); service != nil {
		return service.RunArtifacts(ctx, runID)
	}
	return nil, false, nil
}

func (CiGate) OpenArtifact(ctx context.Context, ref ci.ArtifactRef) (io.ReadCloser, ci.Artifact, error) {
	service := LiveCI.Load()
	if service == nil {
		return nil, ci.Artifact{}, errors.New("this daemon's CI service is not up yet")
	}
	return service.OpenArtifact(ctx, ref)
}

func (CiGate) WithArtifacts(ctx context.Context, run ci.Run) ci.Run {
	if service := LiveCI.Load(); service != nil {
		return service.WithArtifacts(ctx, run)
	}
	return run
}

// --- the sandbox adapter -------------------------------------------------

// ciSandboxes is ci.Sandboxes over orchestrator.Sandboxes: it builds a
// job's machine out of whichever backend this deployment runs (a host
// directory or a granule VM), checks the repository out at the commit
// under test, and hands it over ready for a step to run.
//
// Everything it needs beyond the backend is the deployment's git proxy:
// the URL a sandbox clones from, the token store that mints the
// sandbox's own bearer token, and the read-scope registry that makes
// that token worth exactly one fetchable repository (pkg/gitproxy's
// readscope.go).
type CiSandboxes struct {
	// Capabilities builds providers from live deployment settings per job.
	Capabilities func() *model.CapabilityRegistry
	Credentials  model.CredentialResolver
	// Sandboxes is the backend, exactly the one dispatched runs use.
	Sandboxes orchestrator.Sandboxes
	// DefaultShape, when set, returns the deployment's configured default
	// sandbox shape. A CI job's `sandbox:` dimensions are treated as a
	// per-dimension floor ("at least") against this default and against
	// any default shape carried by Sandboxes itself (Shape.AtLeast).
	DefaultShape func() orchestrator.Shape
	// GitRemoteBase is this deployment's git proxy URL (startGitProxy's
	// own answer). Empty means there is nowhere to clone from, which
	// fails the job rather than running its steps against an empty
	// directory.
	GitRemoteBase string
	// MintToken and RevokeToken are gitproxy.SandboxTokenStore's own
	// EnsureToken/Revoke -- the same mint an agent run's sandbox gets,
	// since identity needs nothing new here; what differs is what the
	// token is worth, which is Scopes' business.
	MintToken   func(sandbox string) (string, error)
	RevokeToken func(sandbox string) error
	// Scopes is the read-scope registry the proxy consults. A job's
	// sandbox is registered against the one repository it is testing
	// before anything in it runs git, and dropped when the job ends.
	Scopes *gitproxy.ReadScopes
	// WriteScopes is the write-scope registry the proxy consults.
	WriteScopes *gitproxy.WriteScopes
	// Teams is the registry the proxy reads a job's sandbox in, as the
	// default team's (CiSandbox.registerWithProxy).
	Teams *gitproxy.SandboxTeams
	// TeamGitCredential is model.Store.TeamGitCredential: asked before
	// such a job's checkout, so a team with no credential is told so in
	// one sentence rather than by a failed clone. nil skips the check
	// and leaves the proxy to refuse.
	TeamGitCredential func(ctx context.Context, team, owner, repo string) (string, error)
	// Log is the daemon's own log. nil discards.
	Log    func(format string, args ...any)
	Grants *CiGrantReservations
	// Meter counts each job's sandbox against this deployment's lifetime
	// figures (orchestrator.SandboxMeter, grain/task-315) -- the same
	// counting an agent run's sandbox gets, from the other of the two
	// callers that build one, so that "how many sandboxes, agent and CI"
	// is one question with two rows behind it. The zero value counts
	// nothing, which is what a test wiring this adapter by hand gets.
	Meter orchestrator.SandboxMeter
	// PackageCache returns the package cache URL for a sandbox.
	PackageCache func(ctx context.Context) string
	// PackageCacheCA returns the internal root CA certificate in PEM format.
	PackageCacheCA func(ctx context.Context) []byte
	// PackageCacheDNS returns the IP address of the package cache DNS server.
	PackageCacheDNS func(ctx context.Context) string
	// Policy is the daemon's -policy (Config.policy), which every job is
	// judged against before its sandbox is built (CheckCIJobPolicy). nil,
	// a daemon with no -policy, refuses no job.
	Policy *policy.Policy
	// ActionsCache hands this job the URL of this deployment's Actions
	// cache, scoped to the repository and branch it is testing and to
	// whether it is a trusted release run (ciActionsCacheScope), and the
	// way to give that scope back when the sandbox is released
	// (pkg/cli/actionscache.go, pkg/actionscache).
	//
	// nil, or an empty URL, on a deployment keeping no such cache: the
	// job's act then runs its own cache server in the sandbox, exactly
	// as it did before this existed, and exports into a cache that dies
	// with the machine.
	ActionsCache func(scope actionscache.Scope) (url string, revoke func(), err error)
	// Stopping, when non-nil, is closed when the daemon begins shutting down.
	Stopping <-chan struct{}
}

// ciActionsCacheScope is the corner of the Actions cache a job may use:
// its repository, the branch it is building, and trusted only for a
// release -- the one event a caller cannot start (ci.Request's
// TrustedRelease), so the one whose entries an ordinary branch's CI can
// never have written (pkg/actionscache, issue #2392). A job with no ref
// to name gets an empty ref, which the cache grants nothing for.
func CiActionsCacheScope(req ci.SandboxRequest) actionscache.Scope {
	return actionscache.Scope{
		Repo:    req.Repo,
		Ref:     req.Ref,
		Trusted: req.Event == ci.EventRelease,
	}
}

type CiGrantReservations struct {
	Capabilities func() *model.CapabilityRegistry
	Credentials  model.CredentialResolver
	Log          func(format string, args ...any)
	// Policy is CiSandboxes.Policy, asked before anything a job holds is
	// materialized for it, which is before its sandbox exists.
	Policy *policy.Policy
	// Servers are the attached capability servers a job's capability
	// references are minted through, and Config reads the capability
	// policy they are checked against and the deployment id their
	// attempt_ids carry (ci_capability_pass.go). nil Servers, or Servers
	// answering nil, refuses every reference.
	Servers func() orchestrator.CapabilityServers
	Config  func(context.Context) (*model.Config, error)
	// Ports and PortsURL are the daemon's capability port relay and where
	// a sandbox reaches it (WithCICapabilityPorts), and SetupTimeout
	// bounds each setup command (zero: the orchestrator's default) --
	// what a job's pass is carried into its sandbox with.
	Ports        *portforward.Relay
	PortsURL     string
	SetupTimeout time.Duration
	// CapServers, ProxyURL and ProxyToken carry a job's sandbox-callable
	// capability tools through the sandbox proxy's /cap/<server>/mcp
	// (orchestrator.CIPassOptions); nil CapServers forwards each endpoint
	// over Ports instead.
	CapServers *gitproxy.CapRegistry
	ProxyURL   string
	ProxyToken func(sandbox string) (string, error)
	// Now and Sleep are the clock a pass's retries are timed against;
	// nil is the real one.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error

	mu       sync.Mutex
	prepared map[string]*ciGrantReservation
	// passes are the minted capability passes of jobs between Acquire
	// and its release, by sandbox name. Memory only: a pass carries
	// leased secrets.
	passes map[string]*ciHeldPass
}

type ciGrantReservation struct {
	cc           model.CapabilityContext
	materialized []model.Materialized
	once         sync.Once
	log          func(format string, args ...any)
}

func (r *ciGrantReservation) release(ctx context.Context) {
	r.once.Do(func() {
		for _, materialized := range r.materialized {
			if lease := materialized.Materialization.Lease; lease != nil {
				if err := materialized.Provider.Revoke(ctx, r.cc, *lease); err != nil && r.log != nil {
					r.log("grain ci: releasing pre-sandbox capability %q for %s: %v",
						materialized.Grant.Capability, r.cc.Run.ID, err)
				}
			}
		}
	})
}

func (g *CiGrantReservations) Acquire(ctx context.Context, req ci.SandboxRequest, waiting func(ci.Wait)) (func(), error) {
	if len(req.Capabilities) == 0 {
		return func() {}, nil
	}
	if g == nil || g.Capabilities == nil {
		return nil, fmt.Errorf("CI grant admission has no capability registry")
	}
	if err := ciPolicyRefusal(g.Policy, req, g.Log); err != nil {
		return nil, err
	}
	if err := ciCatalogueRefusal(ctx, g.Config, req, g.Log); err != nil {
		return nil, err
	}
	repo, err := model.ParseRepo(req.Repo)
	if err != nil {
		return nil, err
	}
	refs := ciCapabilityRefs(req.Capabilities)
	var streak ciPassStreak
	reported := ""
	report := func(reason string, err error) {
		if reason == reported {
			return
		}
		if waiting != nil {
			// Cause as well as sentence: a job held for a finite
			// credential and a job held for memory are the same
			// length of wait and two different things to fix, and
			// the cause is what the job's record is attributed to
			// afterwards (ci.WaitForGrant, ci.JobRun.Queued).
			waiting(ci.Wait{Cause: ci.WaitForGrant, Reason: reason})
		}
		// Said once in the daemon log too, with GCP's own words on
		// the end of it: the job row carries the short reason, but a
		// deployment whose CI jobs all stall on the same finite
		// resource is diagnosed from the log.
		if g.Log != nil {
			g.Log("grain ci: %s: %s: %v", req.Name, reason, err)
		}
		reported = reason
	}
	for {
		reg := g.Capabilities()
		var cc model.CapabilityContext
		var resolved []model.GrantResolution
		var err error
		if reg == nil {
			err = &model.ResourceExhaustedError{Err: errors.New("CI capability providers are not configured")}
		} else {
			cc, resolved, err = ResolveCICapabilities(ctx, reg, g.Credentials, repo, req.Name, req.Event, req.TrustedSchedule, req.Capabilities)
		}
		var materialized []model.Materialized
		if err == nil {
			materialized, err = model.MaterializeScarceGrants(ctx, reg, cc, resolved)
		}
		if exhausted, wait := model.ResourceExhausted(err); wait {
			releaseCIMaterialized(ctx, cc, materialized, g.Log)
			resource, capability := exhausted.Resource, exhausted.Capability
			if resource == "" {
				resource = "capability resources"
			}
			if capability == "" {
				capability = "capability"
			}
			report(fmt.Sprintf("waiting for %s (%s grant)", resource, capability), err)
			delay := exhausted.RetryAfter
			if delay <= 0 {
				delay = model.DefaultResourceRetryInterval
			}
			if err := g.wait(ctx, delay); err != nil {
				return nil, err
			}
			cc.Now = time.Now().UTC()
			continue
		}
		if err != nil {
			releaseCIMaterialized(ctx, cc, materialized, g.Log)
			return nil, err
		}
		// The job's capability references, minted last so that a pass
		// that has to wait gives back what the job reserved above rather
		// than holding it idle (§4.2.3).
		if len(refs) > 0 {
			pass, deadline, err := g.mintCIPass(ctx, req, repo, refs)
			if err != nil {
				releaseCIMaterialized(ctx, cc, materialized, g.Log)
				delay, reason, final := streak.failure(err, g.now())
				if final != nil {
					return nil, final
				}
				report(reason, err)
				if err := g.wait(ctx, delay); err != nil {
					return nil, err
				}
				continue
			}
			g.holdPass(req.Name, pass, deadline)
		}
		reservation := &ciGrantReservation{cc: cc, materialized: materialized, log: g.Log}
		g.mu.Lock()
		if g.prepared == nil {
			g.prepared = map[string]*ciGrantReservation{}
		}
		g.prepared[req.Name] = reservation
		g.mu.Unlock()
		return func() {
			g.mu.Lock()
			delete(g.prepared, req.Name)
			g.mu.Unlock()
			cleanupCtx, cancel := context.WithTimeout(context.Background(), ciReleaseTimeout)
			defer cancel()
			reservation.release(cleanupCtx)
			g.releasePass(cleanupCtx, req.Name)
		}, nil
	}
}

func (g *CiGrantReservations) take(name string) *ciGrantReservation {
	g.mu.Lock()
	defer g.mu.Unlock()
	reservation := g.prepared[name]
	delete(g.prepared, name)
	return reservation
}

func releaseCIMaterialized(ctx context.Context, cc model.CapabilityContext, materialized []model.Materialized, logf func(string, ...any)) {
	for _, item := range materialized {
		if lease := item.Materialization.Lease; lease != nil {
			if err := item.Provider.Revoke(ctx, cc, *lease); err != nil && logf != nil {
				logf("grain ci: releasing capability %q for %s: %v", item.Grant.Capability, cc.Run.ID, err)
			}
		}
	}
}

func (s CiSandboxes) log(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

func (s CiSandboxes) defaultShape() orchestrator.Shape {
	var def orchestrator.Shape
	if shaper, ok := s.Sandboxes.(interface{ DefaultShape() orchestrator.Shape }); ok {
		def = shaper.DefaultShape()
	}
	if s.DefaultShape != nil {
		def = def.AtLeast(s.DefaultShape())
	}
	return def
}

// ciGitSafe is what a repo name, ref or commit has to match to be
// interpolated into the checkout script below -- orchestrator's own
// gitSafe, restated here for the reason that one gives: the sandbox is
// where arbitrary shell runs anyway, so this is not a privilege
// boundary, it is the difference between a bad ref failing with git's
// own error and a bad ref producing a command that does something else
// entirely.
var ciGitSafe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// Acquire implements ci.Sandboxes: build the machine, give it the one
// checkout credential (a proxy token scoped to this repository, fetch only),
// clone the commit under test, provision explicit CI grants, and answer
// only once a step could run.
func (s CiSandboxes) Acquire(ctx context.Context, req ci.SandboxRequest) (ci.Sandbox, error) {
	if s.Stopping != nil {
		select {
		case <-s.Stopping:
			return nil, errors.New("this daemon is shutting down")
		default:
		}
	}
	repo, err := model.ParseRepo(req.Repo)
	if err != nil {
		return nil, fmt.Errorf("%q is not a repository this deployment can check out: %w", req.Repo, err)
	}
	if s.GitRemoteBase == "" {
		return nil, errors.New("this daemon runs no git proxy, so a CI job has nothing to clone the repository from")
	}
	for _, v := range []string{repo.Owner, repo.Name} {
		if !ciGitSafe.MatchString(v) {
			return nil, fmt.Errorf("%q is not a usable git name", v)
		}
	}
	checkoutRef := req.Commit
	if checkoutRef == "" {
		checkoutRef = req.Ref
	}
	if checkoutRef != "" && !ciGitSafe.MatchString(checkoutRef) {
		return nil, fmt.Errorf("%q is not a usable git ref", checkoutRef)
	}
	// The owner's policy, before there is a machine: a job it refuses
	// gets no sandbox, no checkout and no scope.
	if err := ciPolicyRefusal(s.Policy, req, s.Log); err != nil {
		return nil, err
	}

	shape := s.jobShape(req)
	// Waiting rather than failing while a Kubernetes cluster has no node
	// with room for the job's pod (orchestrator.ErrClusterFull): a pod
	// that did not fit now is a job to run later, as one the budget is
	// holding back is, not a failed job. The gate admits against the
	// cluster's last reading already (model.Budget.ClusterFit); this
	// covers the race that reading cannot see. A pod that could never
	// fit is not ErrClusterFull, and fails.
	var backing orchestrator.Sandbox
	var counted func(context.Context)
	for waited := false; ; waited = true {
		backing, counted, err = s.Meter.Acquire(ctx, s.Sandboxes, model.SandboxKindCI, req.Name, shape)
		if err == nil || !errors.Is(err, orchestrator.ErrClusterFull) {
			break
		}
		if !waited {
			s.log("grain ci: %s: %v -- waiting for room rather than failing the job", req.Name, err)
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(err, ctx.Err())
		case <-time.After(ciClusterFullRetry):
		}
	}
	if err != nil {
		return nil, err
	}
	sandbox := &CiSandbox{Owner: s, Backing: backing, SandboxName: backing.Name(), counted: counted}
	if s.Grants != nil {
		sandbox.reservation = s.Grants.take(req.Name)
	}
	cache := orchestrator.PackageCacheSource{Base: s.PackageCache, CA: s.PackageCacheCA, DNS: s.PackageCacheDNS}
	if prep := orchestrator.PrepareSandbox(ctx, cache, backing); prep.Err != nil {
		s.log("grain ci: %s: configuring package cache at %s: %v", sandbox.SandboxName, prep.Base, prep.Err)
	}
	// Where this deployment keeps an Actions cache, this job is told
	// where to find its own repository's corner of it -- one file, read
	// by the act invocation grain generates for an Actions job
	// (pkg/grit/actions' ActScript). The grant is recorded on the sandbox
	// before anything can use it and given back by Release.
	//
	// A failure here is a warning and nothing else: a job that could not
	// be told about the cache builds everything from cold, which is what
	// every job did before this existed.
	if s.ActionsCache != nil {
		url, revoke, err := s.ActionsCache(CiActionsCacheScope(req))
		switch {
		case err != nil:
			s.log("grain ci: %s: no Actions cache for this job: %v -- it will build from cold", sandbox.SandboxName, err)
		case strings.TrimSpace(url) != "":
			sandbox.revokeActionsCache = revoke
			if reason := ciActionsCacheUnreachable(ctx, backing, url); reason != "" {
				s.log("grain ci: %s: not using this deployment's Actions cache: %s -- the job's act will keep "+
					"its own cache server and build from cold", sandbox.SandboxName, reason)
				break
			}
			if err := orchestrator.WriteSandboxFile(ctx, backing, actions.CacheURLFile, "0644", url+"\n"); err != nil {
				s.log("grain ci: %s: telling the job where this deployment's Actions cache is: %v", sandbox.SandboxName, err)
			}
		}
	}

	// From here on every failure has a sandbox to destroy, and a scope
	// and a token that must not outlive it.
	if err := sandbox.prepareJob(ctx, repo, checkoutRef, req.Event, req.TrustedSchedule, req.Capabilities, req.ReleaseSecrets); err != nil {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ciReleaseTimeout)
		defer cancel()
		if rerr := sandbox.Release(releaseCtx); rerr != nil {
			s.log("grain ci: releasing %s after a failed checkout: %v", sandbox.SandboxName, rerr)
		}
		return nil, err
	}
	// The pass admission minted for the job's capability references,
	// carried in last, so its setup commands find the checkout there, and
	// before any step runs: ports, placements, setup, then every
	// mcp_endpoint proxied into the sandbox and listed in
	// /home/debian/.grain/mcp.json (docs/capabilities-spec.md §5).
	if err := s.Grants.materializeCIPass(ctx, req.Name, backing); err != nil {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ciReleaseTimeout)
		defer cancel()
		if rerr := sandbox.Release(releaseCtx); rerr != nil {
			s.log("grain ci: releasing %s after its capability grants failed to materialize: %v", sandbox.SandboxName, rerr)
		}
		return nil, err
	}
	return sandbox, nil
}

// Readopt re-claims an in-flight CI job's sandbox across a daemon restart and
// re-wires its git-proxy token, scopes, and shell runner without re-cloning.
func (s CiSandboxes) Readopt(ctx context.Context, req ci.SandboxRequest) (ci.Sandbox, error) {
	if s.Sandboxes == nil {
		return nil, errors.New("this deployment has no sandbox backend")
	}
	repo, err := model.ParseRepo(req.Repo)
	if err != nil {
		return nil, err
	}
	backing, err := orchestrator.ReadoptSandbox(ctx, s.Sandboxes, req.Name)
	if err != nil {
		return nil, fmt.Errorf("sandbox %s could not be readopted from backend %T: %w", req.Name, s.Sandboxes, err)
	}
	sandbox := &CiSandbox{Owner: s, Backing: backing, SandboxName: backing.Name()}
	if err := sandbox.readoptJob(ctx, repo, req.Event, req.TrustedSchedule, req.Capabilities); err != nil {
		return nil, err
	}
	return sandbox, nil
}

// ciActionsCacheProbe asks the cache, from inside the sandbox, for a
// key nothing has ever written. A cache that is there answers 204 and
// the probe exits 0; anything else -- no route, no listener, a
// deployment whose advertised address is wrong for this backend -- is
// the job's own cache calls failing later, and it is better found now.
//
// curl and wget are both tried because a sandbox image is entitled to
// have only one of them, and a machine with neither is reported as
// unreachable rather than assumed fine: the cost of being wrong that
// way is a cold build, and the cost of being wrong the other way is
// every `type=gha` build in the job failing outright (buildkit fails
// the build when its cache exporter cannot reach its server).
const ciActionsCacheProbe = `set -eu
url="$1_apis/artifactcache/cache?keys=grain-ci-probe&version=grain-ci-probe"
if command -v curl >/dev/null 2>&1; then
  exec curl -fsS -m 15 -o /dev/null "$url"
fi
if command -v wget >/dev/null 2>&1; then
  exec wget -q -T 15 -O /dev/null "$url"
fi
echo "this sandbox has neither curl nor wget, so there is no way to check the cache is reachable" >&2
exit 127`

// ciActionsCacheUnreachable answers why this job should not be pointed
// at the cache, and "" when it should be.
func ciActionsCacheUnreachable(ctx context.Context, sandbox orchestrator.Sandbox, url string) string {
	probeCtx, cancel := context.WithTimeout(ctx, ciActionsCacheProbeTimeout)
	defer cancel()
	out, exitCode, err := orchestrator.RunSandboxCommand(probeCtx,
		sandbox, []string{"sh", "-c", ciActionsCacheProbe, "sh", url}, "")
	switch {
	case err != nil:
		return err.Error()
	case exitCode != 0:
		detail := strings.TrimSpace(out)
		if detail == "" {
			detail = fmt.Sprintf("the check exited %d", exitCode)
		}
		return fmt.Sprintf("%s is not reachable from it (%s)", url, detail)
	}
	return ""
}

// ciActionsCacheProbeTimeout bounds that one request. It is short: the
// cache is on the same machine the sandbox already cloned its
// repository from, so anything slower than this is a deployment with a
// problem rather than a busy network.
const ciActionsCacheProbeTimeout = 30 * time.Second

// ciReleaseTimeout bounds the teardown of a sandbox whose preparation
// failed. The engine bounds its own releases (ci.sandboxReleaseTimeout);
// this is the same idea for the window before it has a sandbox to
// release.
const ciReleaseTimeout = 2 * time.Minute

// ciSandbox is one CI job's machine.
type CiSandbox struct {
	Owner             CiSandboxes
	Backing           orchestrator.Sandbox
	SandboxName       string
	capabilityContext model.CapabilityContext
	materialized      []model.Materialized
	reservation       *ciGrantReservation
	// counted closes this sandbox's entry in the lifetime figures: how
	// long it lived, added to the CI total. orchestrator.SandboxMeter
	// hands it back from Acquire and it is called from Release, once --
	// including the release a failed checkout does below, which is a
	// sandbox that came up, cost its minutes and ran no step.
	counted func(context.Context)
	// revokeActionsCache gives back this job's Actions cache token, so
	// that the URL written into the sandbox is worth nothing the moment
	// the sandbox is gone. nil where this deployment keeps no cache.
	revokeActionsCache func()
	// run is the sandbox's own run_command tool, the transport an
	// agent's tool call goes over.
	Run func(context.Context, map[string]any) mcp.Result
	// shell is orchestrator.SandboxShell where the backend has one,
	// which both real backends do, and is preferred over run for a
	// step's script -- see ciSandbox.Exec.
	shell orchestrator.SandboxShell
}

// jobShape is the sandbox a CI job is built in: what its workflow asks
// for, raised to the deployment's own default shape.
func (s CiSandboxes) jobShape(req ci.SandboxRequest) orchestrator.Shape {
	return orchestrator.Shape{
		CPUs: req.Shape.CPUs, MemoryMB: req.Shape.MemoryMB, DiskGB: req.Shape.DiskGB,
		// CI workflows are expected to be portable from GitHub-hosted
		// runners, including dependency and browser downloads. They receive
		// only explicitly configured CI capability material. Open egress
		// provides dependency-download compatibility.
		NetworkAccess: model.NetworkOpen,
		// The lowest class: on a full cluster a CI job's pod is what an
		// interactive session preempts, and a preempted job is retried
		// as infrastructure (ci.PreemptionReporter).
		Class: orchestrator.SandboxClassCI,
		Life:  req.Timeout,
	}.AtLeast(s.defaultShape())
}

// SandboxPreempted implements ci.PreemptionReporter by asking the
// backend's own sandbox, where it can say
// (orchestrator.SandboxPreemptionReporter).
func (s *CiSandbox) SandboxPreempted(ctx context.Context) (string, bool) {
	if r, ok := s.Backing.(orchestrator.SandboxPreemptionReporter); ok {
		return r.SandboxPreempted(ctx)
	}
	return "", false
}

// SandboxOOM implements ci.OOMReporter by asking the backend's own
// sandbox, where it can say (orchestrator.SandboxOOMReporter).
func (s *CiSandbox) SandboxOOM(ctx context.Context) (string, bool) {
	if r, ok := s.Backing.(orchestrator.SandboxOOMReporter); ok {
		return r.SandboxOOM(ctx)
	}
	return "", false
}

// Name implements ci.Sandbox: what the backend actually called it, which
// is what an operator reading a list of VMs will find.
func (s *CiSandbox) Name() string { return s.SandboxName }

// prepare is everything between "there is a machine" and "a step could
// run": the read scope, the token, git's credentials, and the checkout.
//
// The scope is granted before the token is minted, not after. A token
// that exists while its scope does not is a token that authorizes
// nothing -- which is the safe order -- whereas the reverse would leave
// a window in which the sandbox's own name is registered as fetchable by
// whoever holds a token this deployment has not minted yet.
func (s *CiSandbox) prepare(ctx context.Context, repo model.RepoRef, checkoutRef string) error {
	s.Owner.Scopes.Grant(s.SandboxName, repo)

	if s.Owner.MintToken == nil {
		return errors.New("this daemon mints no sandbox tokens, so a CI job cannot authenticate to the git proxy")
	}
	token, err := s.Owner.MintToken(s.SandboxName)
	if err != nil {
		return fmt.Errorf("minting %s's proxy token: %w", s.SandboxName, err)
	}
	// The placeholder URL is what an agent run's sandbox is configured
	// with too (orchestrator's runOne): what git's credential helper
	// stores is the proxy's host, not a repository, so the path never
	// matters.
	//
	// The identity is left unset, which mcp.GitIdentity.OrDefault
	// resolves to grain's own: a CI job commits nothing, so the only
	// thing this decides is what `git config` in the checkout says, and
	// a deployment's chosen agent identity has nothing to do with a
	// machine that authors no commits.
	if err := s.Backing.ConfigureGitCredentials(ctx,
		s.Owner.GitRemoteBase+"/placeholder/placeholder.git", token, mcp.GitIdentity{}); err != nil {
		return fmt.Errorf("pointing %s's git at the proxy: %w", s.SandboxName, err)
	}

	tools, err := s.Backing.Tools(ctx)
	if err != nil {
		return err
	}
	run, ok := CiRunCommand(tools)
	if !ok {
		return fmt.Errorf("sandbox %s exposes no run_command tool, so there is nothing to run a job's steps with", s.SandboxName)
	}
	s.Run = run
	// Optional, the way orchestrator.SandboxPlacer is: both backends
	// this deployment can run have one, and a Sandbox in a test that
	// does not falls back to run_command above.
	s.shell, _ = s.Backing.(orchestrator.SandboxShell)

	// The checkout goes through the sandbox's own run_command tool
	// rather than exec.Command or SSH, which is what makes it work for
	// either backend -- see orchestrator.prepareCheckout's own doc
	// comment, which takes exactly this route for exactly this reason.
	result := run(ctx, map[string]any{
		"command":     ciCheckoutScript(orchestrator.CloneURL(s.Owner.GitRemoteBase, repo), checkoutRef),
		"description": "checking out the commit under test",
		"timeout":     float64(ciCheckoutTimeout / time.Millisecond),
	})
	if result.IsError || ciExitCode(result) != 0 {
		return fmt.Errorf("checking %s out at %s in %s: %s",
			repo, ciRefDescription(checkoutRef), s.SandboxName, strings.TrimSpace(result.Text))
	}
	return nil
}

func (s *CiSandbox) prepareJob(ctx context.Context, repo model.RepoRef, ref string, event ci.Event, trustedSchedule bool, ids, releaseSecrets []string) error {
	if err := s.registerWithProxy(ctx, repo, event, trustedSchedule, ids); err != nil {
		return err
	}
	if err := s.prepare(ctx, repo, ref); err != nil {
		return err
	}
	if len(ids) > 0 {
		if s.Owner.Capabilities == nil {
			return errors.New("CI capability providers are not configured")
		}
		reg := s.Owner.Capabilities()
		if reg == nil {
			return errors.New("CI capability providers are not configured")
		}
		cc, resolved, err := ResolveCICapabilities(ctx, reg, s.Owner.Credentials, repo, s.SandboxName, event, trustedSchedule, ids)
		if err != nil {
			return err
		}
		s.capabilityContext = cc
		for _, grant := range resolved {
			if grant.Resolution.Refused {
				return fmt.Errorf("CI capability %q refused: %s", grant.Grant.Capability, grant.Resolution.Reason)
			}
		}
		var prepared []model.Materialized
		if s.reservation != nil {
			prepared = s.reservation.materialized
		}
		s.materialized, err = model.CompleteMaterializeGrants(ctx, reg, cc, resolved, prepared)
		if err != nil {
			return fmt.Errorf("materializing CI capabilities: %w", err)
		}
		for _, grant := range resolved {
			if otherRepo, ok := model.RepoReadTarget(grant.Grant.Capability); ok {
				s.Owner.Scopes.Grant(s.SandboxName, otherRepo)
			}
			if otherRepo, ok := model.RepoWriteTarget(grant.Grant.Capability); ok {
				if s.Owner.WriteScopes != nil {
					s.Owner.WriteScopes.Grant(s.SandboxName, otherRepo)
				}
			}
		}
	}
	placements := append([]model.Materialized(nil), s.materialized...)
	if len(releaseSecrets) > 0 {
		if event != ci.EventRelease && event != ci.EventManual {
			return fmt.Errorf("release secrets are not available to %s CI runs", event)
		}
		if s.Owner.Credentials == nil {
			return errors.New("release secrets are configured, but this deployment has no secrets store")
		}
		var files []model.Placement
		for _, name := range releaseSecrets {
			secret, key := ci.ReleaseSecretAddress(repo.String(), name)
			value, err := s.Owner.Credentials.Resolve(ctx, secret+"/"+key)
			if err != nil {
				return fmt.Errorf("resolving release secret %q for %s: %w", name, repo, err)
			}
			files = append(files, model.Placement{
				Side: model.SideSandbox, Path: ci.ReleaseSecretPath(name), Content: value,
			})
		}
		placements = append(placements, model.Materialized{
			Grant:           model.Grant{Capability: "release-secrets"},
			Materialization: model.Materialization{Placements: files},
		})
	}
	placer, _ := s.Backing.(orchestrator.SandboxPlacer)
	var root string
	var err error
	if placer == nil {
		if rooted, ok := s.Backing.(interface{ Root() (string, error) }); ok {
			root, err = rooted.Root()
			if err != nil {
				return fmt.Errorf("finding CI sandbox root: %w", err)
			}
		}
	}
	return orchestrator.ApplyCapabilityPlacements(ctx, root, placer, placements)
}

func (s *CiSandbox) readoptJob(ctx context.Context, repo model.RepoRef, event ci.Event, trustedSchedule bool, ids []string) error {
	if err := s.registerWithProxy(ctx, repo, event, trustedSchedule, ids); err != nil {
		return err
	}
	if s.Owner.Scopes != nil {
		s.Owner.Scopes.Grant(s.SandboxName, repo)
	}
	if s.Owner.MintToken != nil {
		if _, err := s.Owner.MintToken(s.SandboxName); err != nil {
			return fmt.Errorf("minting %s's proxy token on readopt: %w", s.SandboxName, err)
		}
	}
	tools, err := s.Backing.Tools(ctx)
	if err != nil {
		return err
	}
	run, ok := CiRunCommand(tools)
	if !ok {
		return fmt.Errorf("sandbox %s exposes no run_command tool", s.SandboxName)
	}
	s.Run = run
	s.shell, _ = s.Backing.(orchestrator.SandboxShell)
	if len(ids) == 0 {
		return nil
	}
	// The other repositories this job may reach are the ones prepareJob
	// granted it, and no more: resolved through the same event gate, so a
	// push or pull-request run's declared repo-write (release, manual and
	// trusted schedule runs only -- repowrite.Provider) is not handed to
	// its sandbox just because the daemon restarted under it.
	if s.Owner.Capabilities == nil {
		return errors.New("CI capability providers are not configured")
	}
	reg := s.Owner.Capabilities()
	if reg == nil {
		return errors.New("CI capability providers are not configured")
	}
	_, resolved, err := ResolveCICapabilities(ctx, reg, s.Owner.Credentials, repo, s.SandboxName, event, trustedSchedule, ids)
	if err != nil {
		return err
	}
	for _, grant := range resolved {
		if grant.Resolution.Refused {
			return fmt.Errorf("CI capability %q refused: %s", grant.Grant.Capability, grant.Resolution.Reason)
		}
	}
	for _, grant := range resolved {
		if otherRepo, ok := model.RepoReadTarget(grant.Grant.Capability); ok && s.Owner.Scopes != nil {
			s.Owner.Scopes.Grant(s.SandboxName, otherRepo)
		}
		if otherRepo, ok := model.RepoWriteTarget(grant.Grant.Capability); ok && s.Owner.WriteScopes != nil {
			s.Owner.WriteScopes.Grant(s.SandboxName, otherRepo)
		}
	}
	return nil
}

// registerWithProxy registers this job's sandbox with the proxy and,
// when it holds repository access it declared (repo-read:, repo-write:),
// checks that the deployment has a git connection covering every one of
// them, so a job that cannot reach one is told so in one sentence rather
// than by a failed clone (grain/task-1654, grain/task-4622).
//
// The sandbox is registered as the default team's, which since the
// single-tenant reset is the deployment (stage 1 keeps the deployment's
// git connections on that team's row, and stage 4c gave CI runs no team
// of their own).
func (s *CiSandbox) registerWithProxy(ctx context.Context, repo model.RepoRef, event ci.Event, trustedSchedule bool, ids []string) error {
	if ciHoldsRepoAccess(s.Owner.Capabilities, event, trustedSchedule, ids) {
		if s.Owner.TeamGitCredential != nil {
			for _, target := range ciRepoAccessTargets(repo, ids) {
				if _, err := s.Owner.TeamGitCredential(ctx, model.DefaultTeamID, target.Owner, target.Name); err != nil {
					if errors.Is(err, model.ErrNoTeamGitCredential) {
						return errors.New(gitproxy.NoTeamCredentialMessage(model.DefaultTeamID, target.String()))
					}
					return fmt.Errorf("finding the GitHub credential for %s: %w", target, err)
				}
			}
		}
	}
	if s.Owner.Teams != nil {
		s.Owner.Teams.Grant(s.SandboxName, model.DefaultTeamID)
	}
	return nil
}

// ciHoldsRepoAccess reports whether ids hold a repository access this
// event may carry: any `repo-read:`, or a `repo-write:` whose provider's
// event gate admits this run (resolveCICapabilities applies the same
// gate, so an untrusted run's declared write is never granted and is
// not counted here).
func ciHoldsRepoAccess(capabilities func() *model.CapabilityRegistry, event ci.Event, trustedSchedule bool, ids []string) bool {
	var reg *model.CapabilityRegistry
	for _, id := range ids {
		if _, ok := model.RepoReadTarget(id); ok {
			return true
		}
		if _, ok := model.RepoWriteTarget(id); !ok || capabilities == nil {
			continue
		}
		if reg == nil {
			// Asked for only once a job declares write: a job that
			// declares nothing never builds a registry here.
			if reg = capabilities(); reg == nil {
				return false
			}
		}
		provider, ok := reg.Lookup(id)
		if !ok {
			continue
		}
		if events, ok := provider.(model.CIEventCapability); ok && !events.SupportsCIEvent(string(event)) &&
			!ciTrustedScheduleAdmits(provider, event, trustedSchedule) {
			continue
		}
		return true
	}
	return false
}

// ciRepoAccessTargets is the repository under test and every repository
// ids declare access to, once each.
func ciRepoAccessTargets(repo model.RepoRef, ids []string) []model.RepoRef {
	out := []model.RepoRef{repo}
	for _, id := range ids {
		target, ok := model.RepoReadTarget(id)
		if !ok {
			target, ok = model.RepoWriteTarget(id)
		}
		if ok && !slices.ContainsFunc(out, target.Same) {
			out = append(out, target)
		}
	}
	return out
}

func (s *CiSandbox) revokeCapabilities(ctx context.Context) error {
	var errs []error
	for _, m := range s.materialized {
		if model.IsScarceResourceProvider(m.Provider) && s.reservation != nil {
			continue
		}
		if lease := m.Materialization.Lease; lease != nil {
			if err := m.Provider.Revoke(ctx, s.capabilityContext, *lease); err != nil {
				errs = append(errs, fmt.Errorf("revoking CI capability %q: %w", m.Grant.Capability, err))
			}
		}
	}
	if s.reservation != nil {
		s.reservation.release(ctx)
	}
	return errors.Join(errs...)
}

// CiScarceCapability reports whether id names a provider in the current
// registry that reserves a finite external resource
// (model.IsScarceResourceProvider) -- the capabilities ci.Engine hands
// only to a job that declares them (ci.Engine.ExplicitOnlyCapability).
// An id with no provider is not scarce: it is left for admission to
// report as unconfigured, as before.
func CiScarceCapability(capabilities func() *model.CapabilityRegistry) func(string) bool {
	return func(id string) bool {
		if capabilities == nil {
			return false
		}
		reg := capabilities()
		if reg == nil {
			return false
		}
		provider, ok := reg.Lookup(id)
		return ok && model.IsScarceResourceProvider(provider)
	}
}

// ResolveCICapabilities resolves a CI job's capabilities. A job belongs
// to no team since the single-tenant reset (stage 4c): its capability
// context names none, which reads as the deployment wherever a team is
// still asked for.
func ResolveCICapabilities(ctx context.Context, reg *model.CapabilityRegistry, credentials model.CredentialResolver,
	repo model.RepoRef, runID string, event ci.Event, trustedSchedule bool, ids []string) (model.CapabilityContext, []model.GrantResolution, error) {
	now := time.Now().UTC()
	cc := model.CapabilityContext{
		Task: model.Task{Target: &repo}, Run: model.Run{ID: runID, Sandbox: runID, StartedAt: now},
		Now: now, Credentials: credentials,
		// This is the CI path, and the only thing that says so:
		// model.ResolveGrants refuses a CI-only capability to every
		// context that does not (model.CapabilitySpec.CIOnly). The
		// event gate just below is what narrows those further, and is
		// still this function's own -- a job, unlike a task, has an
		// event to be judged on.
		CI: true,
	}
	for _, id := range ids {
		// A capability reference has no in-process provider: it is
		// minted through its capability server by the grant admission
		// (CiGrantReservations.Acquire).
		if id == grainci.CapabilityName || model.IsCapabilityRef(id) {
			continue
		}
		provider, ok := reg.LookupFor(id, true)
		if !ok {
			return cc, nil, &model.ResourceExhaustedError{
				Capability: id,
				Err:        fmt.Errorf("CI capability %q has no provider configured", id),
			}
		}
		if !provider.Spec().SupportsCI() {
			return cc, nil, fmt.Errorf("capability %q is not supported for CI", id)
		}
		if events, ok := provider.(model.CIEventCapability); ok && !events.SupportsCIEvent(string(event)) &&
			!ciTrustedScheduleAdmits(provider, event, trustedSchedule) {
			continue
		}
		cc.Task.Grants = append(cc.Task.Grants, model.Grant{Capability: id, Via: model.GrantByDefault})
	}
	if _, ok := reg.Lookup(grainci.CapabilityName); ok {
		cc.Task.Grants = append(cc.Task.Grants, model.Grant{Capability: grainci.CapabilityName, Via: model.GrantByDefault})
	}
	resolved, err := model.ResolveGrants(ctx, reg, cc)
	if err != nil {
		return cc, nil, fmt.Errorf("resolving CI capabilities: %w", err)
	}
	return cc, resolved, nil
}

// ciTrustedScheduleAdmits reports whether a provider whose event gate
// refused this run takes it anyway because it is the schedule run grain
// itself fired (ci.Request.TrustedSchedule) and the provider opted in to
// those (model.TrustedScheduleCapability). A caller's own schedule run
// is never trusted, so it gets the event gate's answer unchanged.
func ciTrustedScheduleAdmits(provider model.CapabilityProvider, event ci.Event, trustedSchedule bool) bool {
	if event != ci.EventSchedule || !trustedSchedule {
		return false
	}
	trusted, ok := provider.(model.TrustedScheduleCapability)
	return ok && trusted.SupportsTrustedSchedule()
}

// CiRepoCapabilities reads only operator-owned CI settings: the
// deployment's own configuration of the repository
// (model.Store.EffectiveRepoConfig), which holds what the default team's
// team_repo_config layered over it until schema 23. Repo spelling is
// case-insensitive,
// including requests coming directly from the MCP CI tool. The capability
// catalogue's deployment-wide CI defaults (settings/capabilities.yml
// defaults.ci) join every repository's own list.
func CiRepoCapabilities(store *model.Store) func(context.Context, string) ([]string, error) {
	return func(ctx context.Context, name string) ([]string, error) {
		eff, err := ciRepoConfig(ctx, store, name)
		if err != nil {
			return eff.CICapabilities, err
		}
		cfg, err := store.GetConfig(ctx)
		if err != nil || cfg == nil || cfg.CapabilityPolicy.Catalogue == nil {
			return eff.CICapabilities, err
		}
		out := append([]string{}, eff.CICapabilities...)
		for _, id := range cfg.CapabilityPolicy.Catalogue.Defaults.CI {
			if !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
		return out, nil
	}
}

// CiRepoReleaseSecrets reads the non-secret names whose values a trusted
// release or explicit manual run may receive, from the same configuration
// CiRepoCapabilities reads. Values remain in the encrypted store until the
// individual job sandbox is being prepared.
func CiRepoReleaseSecrets(store *model.Store) func(context.Context, string) ([]string, error) {
	return func(ctx context.Context, name string) ([]string, error) {
		eff, err := ciRepoConfig(ctx, store, name)
		return eff.ReleaseSecrets, err
	}
}

// CiReleaseRefRefusal is ci.Service.ReleaseRefRefusal for a daemon:
// orchestrator.ReleaseBranchRefusal against this deployment's store and
// GitHub, so the branches a trusted release may run from are the same
// ones whichever caller started it.
func CiReleaseRefRefusal(store *model.Store, client github.Client) func(context.Context, string, string) (string, error) {
	return func(ctx context.Context, name, ref string) (string, error) {
		repo, err := model.ParseRepo(name)
		if err != nil {
			return "", err
		}
		return orchestrator.ReleaseBranchRefusal(ctx, store, client, repo, strings.TrimPrefix(ref, "refs/heads/"))
	}
}

func ciRepoConfig(ctx context.Context, store *model.Store, name string) (model.EffectiveRepoConfig, error) {
	repo, err := model.ParseRepo(name)
	if err != nil {
		return model.EffectiveRepoConfig{}, err
	}
	if store == nil {
		return model.EffectiveRepoConfig{}, nil
	}
	// The stored spelling, found without regard to case, so a request
	// spelling the repository differently reads the same configuration.
	configs, err := store.ListRepoConfigs(ctx)
	if err != nil {
		return model.EffectiveRepoConfig{}, err
	}
	found := false
	for _, cfg := range configs {
		if !cfg.Repo.Same(repo) {
			continue
		}
		if found {
			return model.EffectiveRepoConfig{}, fmt.Errorf("multiple configurations name %s; reconcile repository spelling first", repo)
		}
		repo, found = cfg.Repo, true
	}
	return store.EffectiveRepoConfig(ctx, repo)
}

// ciCheckoutTimeout bounds the clone. Long enough for a large repository
// over a slow link, and clamped by run_command's own ceiling anyway
// (mcp's maxRunCommandTimeout, ten minutes).
const ciCheckoutTimeout = 10 * time.Minute

func ciRefDescription(ref string) string {
	if ref == "" {
		return "its default branch"
	}
	return ref
}

// ciCheckoutScript clones url into the workspace and leaves the commit
// under test checked out, detached -- a CI job has no branch to be on
// and nothing to push, so a detached HEAD is the honest state and one
// nothing can accidentally commit onto.
//
// A commit that the clone did not bring down (a pull request head, most
// obviously) is fetched by name on the second arm rather than failing
// the job, which is the same fallback a `git checkout` of a ref nobody
// has locally needs.
//
// The clone itself is orchestrator.RetryingCloneScript's: a clone the
// sandbox's connection to the git proxy dropped part-way is run again
// rather than erroring the job.
func ciCheckoutScript(url, ref string) string {
	target := "HEAD"
	if ref != "" {
		target = ref
	}
	return fmt.Sprintf(`set -e
rm -rf '%[1]s'
%[5]s
cd '%[1]s'
if ! git checkout --quiet --detach '%[3]s' 2>/dev/null; then
  git fetch --quiet origin '%[3]s'
  git checkout --quiet --detach FETCH_HEAD
fi
git --no-pager log -1 --format='%[4]s%%H'`, orchestrator.CheckoutDir, url, target, ciCommitMarker,
		orchestrator.RetryingCloneScript("'"+url+"'", "'"+orchestrator.CheckoutDir+"'"))
}

// ciCommitMarker prefixes the one line the checkout script prints the
// resolved commit on, so it can be picked back out of run_command's own
// `exit=`/`stdout:`/`stderr:` framing -- the same trick
// orchestrator.commitMarker plays, and the reason nothing here parses
// that framing.
const ciCommitMarker = "grain-ci-commit:"

// Exec implements ci.Sandbox: one step's script, run with bash in the
// checkout, with the step's environment set.
//
// Over orchestrator.SandboxShell where the backend has one, and over the
// sandbox's run_command tool where it does not. The two do the same
// thing; what differs is what they do to a long step. run_command is the
// seam an *agent* reaches a sandbox through, and its bounds are there to
// protect a model's turn: every call is clamped to ten minutes
// (mcp.maxRunCommandTimeout) and every answer cut to a few tens of
// kilobytes (mcp.maxToolResultBytes). Neither bound is survivable for
// CI. This repository's own `go` job is `go test -race -timeout 30m`
// under `timeout-minutes: 45`, so run_command would kill it at ten and
// report a failure the workflow file plainly did not ask for, and the
// log a person opens a failed job to read is exactly the thing the
// output cap throws away. SandboxShell is the same command with the
// caller's own bound and its whole output (pkg/ci truncates it once, in
// the middle, at ci.MaxStepOutput).
//
// It is also the narrower environment. A step's script comes out of a
// workflow file, which is a file an agent can rewrite, so what its shell
// starts with is part of what that file is trusted with: on the host
// backend SandboxShell gives a step a curated few of the daemon's own
// variables (orchestrator.hostShellPassthrough) where run_command similarly
// passes only allowlisted variables (mcp.sandboxPassthrough). The run_command arm below
// is for a Sandbox in a test that implements no shell -- both backends a
// deployment can run have one.
func (s *CiSandbox) Exec(ctx context.Context, cmd ci.Command) (ci.CommandResult, error) {
	return s.exec(ctx, cmd, nil)
}

// ExecStream implements ci.StreamingSandbox: Exec, with each piece of the
// step's output handed over as the sandbox produces it, so a job in flight
// can be followed rather than only read afterwards (pkg/ci's live.go).
//
// Over the shell, which is the only one of this adapter's two transports
// that can stream: a ShellRequest carries a writer and both backends write
// to it as their subprocess prints (orchestrator's shell.go). The
// run_command arm below cannot -- an MCP tool call is one request and one
// answer -- so a sandbox reached that way falls back to reporting its step
// when the step is over, which is what every step did before this existed.
func (s *CiSandbox) ExecStream(ctx context.Context, cmd ci.Command, out func(string)) (ci.CommandResult, error) {
	return s.exec(ctx, cmd, out)
}

// exec is both of the above: out is nil for Exec and the follower's sink
// for ExecStream.
func (s *CiSandbox) exec(ctx context.Context, cmd ci.Command, out func(string)) (ci.CommandResult, error) {
	if s.Run == nil && s.shell == nil {
		return ci.CommandResult{}, fmt.Errorf("sandbox %s was never prepared", s.SandboxName)
	}
	dir := strings.TrimSpace(cmd.Dir)
	if strings.Contains(dir, "'") {
		return ci.CommandResult{}, fmt.Errorf("%q is not a usable working directory", cmd.Dir)
	}
	if s.shell != nil {
		// The checkout, then the step's own working directory inside it
		// -- the join ciStepScript does with two `cd`s, said once here
		// because ShellRequest takes one directory.
		within := orchestrator.CheckoutDir
		if dir != "" {
			within += "/" + dir
		}
		result, err := s.shell.RunShell(ctx, orchestrator.ShellRequest{
			Script: cmd.Script, Dir: within, Env: cmd.Env, Timeout: cmd.Timeout,
			StepID: cmd.StepID,
			Output: ciStepFollower(out),
		})
		if err != nil {
			// The transport, not the command: the machine gone or the
			// connection dropped. That fails the job rather than the
			// step -- see ci.Sandbox.Exec.
			return ci.CommandResult{Output: result.Output}, fmt.Errorf("running a step in %s: %w", s.SandboxName, err)
		}
		return ci.CommandResult{Output: result.Output, ExitCode: result.ExitCode}, nil
	}
	args := map[string]any{
		"command":     ciStepScript(dir, cmd.Env, cmd.Script),
		"description": "running a grain CI step",
	}
	if cmd.Timeout > 0 {
		args["timeout"] = float64(cmd.Timeout / time.Millisecond)
	}
	result := s.Run(ctx, args)
	code, ok := ciExit(result)
	if !ok {
		// No status at all: the tool refused the call rather than the
		// command failing, which is the transport being gone. That fails
		// the job rather than the step -- see ci.Sandbox.Exec.
		return ci.CommandResult{Output: result.Text}, fmt.Errorf("running a step in %s: %s",
			s.SandboxName, strings.TrimSpace(result.Text))
	}
	if code == -1 && strings.Contains(result.Text, mcp.SessionCutOffNotice) {
		// A session that ended before the command said how it ended
		// (mcp.KubectlExecRunner.RunStream): the step neither passed
		// nor failed, the transport dropped it. That too fails the job
		// rather than the step, and is not retried as one -- the step
		// may have done half of what it does.
		return ci.CommandResult{Output: result.Text}, fmt.Errorf("running a step in %s: %s",
			s.SandboxName, mcp.SessionCutOffNotice)
	}
	return ci.CommandResult{Output: result.Text, ExitCode: code}, nil
}

// ciStepFollower adapts pkg/ci's chunk-at-a-time sink to the io.Writer a
// ShellRequest follows with, and is nil for a step nobody is watching --
// which is what tells both backends there is no live copy to make.
func ciStepFollower(out func(string)) io.Writer {
	if out == nil {
		return nil
	}
	return writerFunc(func(p []byte) (int, error) {
		out(string(p))
		return len(p), nil
	})
}

// writerFunc is an io.Writer that is one function, for the adapter above.
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// ciStepScript is one step as bash sees it: the checkout, then the
// step's own working directory, then its environment, then its script.
//
// `set -e` goes in front of the whole thing so a two-line step means
// what it looks like it means -- GitHub's own default shell behaviour,
// and what ci.Command.Script asks a backend for.
func ciStepScript(dir string, env map[string]string, script string) string {
	var b strings.Builder
	b.WriteString("set -e\ncd '")
	b.WriteString(orchestrator.CheckoutDir)
	b.WriteString("'\n")
	if dir != "" {
		b.WriteString("cd '" + dir + "'\n")
	}
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		// A name that is not a shell identifier is dropped rather than
		// written: `export 1x=y` is a syntax error, which would fail
		// every step of the job over one bad key in a workflow file.
		if !ciEnvName.MatchString(name) {
			continue
		}
		b.WriteString("export " + name + "=" + ciShellQuote(env[name]) + "\n")
	}
	b.WriteString(script)
	return b.String()
}

var ciEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func ciShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Release implements ci.Sandbox: the scope goes first, then the machine,
// then the token.
//
// The scope first, on purpose. Releasing a VM can hang on a wedged host
// for the whole of ci.sandboxReleaseTimeout, and a scope that outlives
// its job for two minutes is two minutes in which a token that has
// leaked can still fetch. Dropping it is a map delete that cannot fail.
func (s *CiSandbox) Release(ctx context.Context) error {
	s.Owner.Scopes.Drop(s.SandboxName)
	if s.Owner.WriteScopes != nil {
		s.Owner.WriteScopes.Drop(s.SandboxName)
	}
	s.Owner.Teams.Drop(s.SandboxName)
	if s.revokeActionsCache != nil {
		s.revokeActionsCache()
	}
	capErr := s.revokeCapabilities(ctx)
	err := errors.Join(capErr, s.Backing.Release(ctx))
	// The end of this sandbox's life, counted where the life ends rather
	// than where the job's record is written: a job whose sandbox failed
	// its checkout never gets a JobRun worth reading, and it occupied the
	// machine for as long as it took to find that out.
	if s.counted != nil {
		s.counted(ctx)
	}
	if s.Owner.RevokeToken != nil {
		// Logged rather than joined, for SandboxTokenStore.Revoke's own
		// reason: a leftover entry authorizes nothing once the scope
		// above is gone, so it is upkeep rather than safety.
		if rerr := s.Owner.RevokeToken(s.SandboxName); rerr != nil {
			s.Owner.log("grain ci: revoking %s's sandbox token: %v", s.SandboxName, rerr)
		}
	}
	return err
}

// ciRunCommand picks the run_command handler out of the tools a sandbox
// exposes, so a job's steps go through the same handler an agent's own
// tool call would -- no second path into the sandbox to keep in step
// with the first (orchestrator.runCommandTool, which is that package's
// unexported own).
func CiRunCommand(tools []mcp.Tool) (func(context.Context, map[string]any) mcp.Result, bool) {
	for _, t := range tools {
		if t.Name == "run_command" && t.Handler != nil {
			return t.Handler, true
		}
	}
	return nil, false
}

// ciExit reads the status off a run_command answer, whose first line is
// `exit=N` on both transports (mcp.formatRunCommandResult). The second
// return is false where there is no such line at all, which is what a
// tool refusing the call outright looks like.
func ciExit(result mcp.Result) (int, bool) {
	first, _, _ := strings.Cut(result.Text, "\n")
	rest, ok := strings.CutPrefix(first, "exit=")
	if !ok {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil {
		return 0, false
	}
	return code, true
}

// ciExitCode is ciExit for the callers that treat an unreadable status
// as a failure, which is what an unreadable status means when the thing
// being asked about is grain's own checkout script.
func ciExitCode(result mcp.Result) int {
	if code, ok := ciExit(result); ok {
		return code
	}
	return -1
}

// --- where workflows come from -------------------------------------------

// ciWorkflowSource is ci.Source over a shallow clone made on this host:
// given a repository and a branch, it reads that branch's
// `.grain/workflows` and reports the commit it read them at.
//
// It runs here rather than in a sandbox because of *when* it is asked. A
// run's workflows are read synchronously, on the request that asked for
// the run (ci.Service.Start's own doc comment on why parsing is not
// deferred), and building a whole sandbox to read one directory would
// turn an HTTP request into a VM boot. What it reads is a repository
// this deployment already holds a credential for, into a temporary
// directory it deletes, and nothing it reads reaches a job's sandbox --
// the job clones the repository itself, through the proxy, under a scope
// that can only fetch.
type CiWorkflowSource struct {
	// ConfigRepoDir is the controller-local grain/config repository. It
	// has no upstream forge URL to clone, so workflow discovery reads its
	// branch directly from the repository this daemon owns.
	ConfigRepoDir string
	// Credentials is this deployment's credential ladder. A repository
	// no entry covers is cloned anonymously, which is right for a public
	// one and fails with git's own error for anything else.
	Credentials *gitproxy.CredentialSet
	// GitHubHost and InsecureHTTP name the far end -- empty means
	// github.com over TLS, and a test or a simulator says otherwise, the
	// same two knobs the proxy's own forwarder takes.
	GitHubHost   string
	InsecureHTTP bool
	// Forbidden is the set of repositories no sandbox may reach
	// (gitproxy.ForbiddenSet). Checked here as well as in the proxy: a
	// job against one of them would fail at its own checkout anyway, and
	// refusing on the request that asked says so to somebody who can
	// read it, without this host cloning that repository in the
	// meantime.
	Forbidden *gitproxy.ForbiddenSet
	// TempDir is where clones are made, "" meaning the system's own.
	TempDir string

	// TeamGitCredential is model.Store.TeamGitCredential: the
	// deployment's git connection for a repository.
	TeamGitCredential func(ctx context.Context, team, owner, repo string) (string, error)

	// TeamGitCredentialOn is model.Store.TeamGitCredentialOn, taking
	// the repository's git host as well.
	TeamGitCredentialOn func(ctx context.Context, team, host, owner, repo string) (string, error)

	// TeamCredential resolves a token for a repository directly (e.g. in tests
	// or via a custom resolver callback).
	TeamCredential func(ctx context.Context, repo model.RepoRef) (string, error)
}

// ciCloneTimeout bounds the read. A workflow directory is small but the
// clone that carries it is a whole branch, and a request that has been
// waiting three minutes for one has its answer either way.
const ciCloneTimeout = 3 * time.Minute

// Workflows implements ci.Source.
func (s CiWorkflowSource) Workflows(ctx context.Context, repo, ref string) ([]ci.Workflow, string, error) {
	workflows, commit, _, err := s.WorkflowsAndTree(ctx, repo, ref)
	return workflows, commit, err
}

// WorkflowsAndTree implements ci.TreeSource: the same clone, read twice.
//
// This is the one source in the deployment that has a checkout in front of
// it, so it is the one that can say what the commit contains -- which is
// what keys a job on the files it declared rather than on the commit
// (pkg/ci/paths.go). It is read out of the clone this function already
// made, in one more git command against a local repository, rather than
// from a second clone: the clone is the expensive part and it is already
// paid for.
func (s CiWorkflowSource) WorkflowsAndTree(ctx context.Context, repo, ref string) ([]ci.Workflow, string, *ci.Tree, error) {
	return s.workflowsAndTree(ctx, repo, ref)
}

func (s CiWorkflowSource) workflowsAndTree(ctx context.Context, repo, ref string) ([]ci.Workflow, string, *ci.Tree, error) {
	r, err := model.ParseRepo(repo)
	if err != nil {
		return nil, "", nil, err
	}
	for _, forbidden := range s.Forbidden.Repos() {
		if strings.EqualFold(forbidden.Owner, r.Owner) && strings.EqualFold(forbidden.Name, r.Name) {
			return nil, "", nil, fmt.Errorf("%s: %s", r, gitproxy.ForbiddenReason)
		}
	}
	if ref != "" && !ciGitSafe.MatchString(ref) {
		return nil, "", nil, fmt.Errorf("%q is not a usable branch name", ref)
	}

	ctx, cancel := context.WithTimeout(ctx, ciCloneTimeout)
	defer cancel()

	dir, err := os.MkdirTemp(s.TempDir, "grain-ci-workflows-")
	if err != nil {
		return nil, "", nil, err
	}
	defer os.RemoveAll(dir)

	clone := filepath.Join(dir, "repo")
	args := []string{"clone", "--quiet", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, s.cloneURL(r), clone)
	if out, err := s.clone(ctx, dir, r, args...); err != nil {
		return nil, "", nil, fmt.Errorf("reading %s's workflows: %w: %s", r, err, strings.TrimSpace(out))
	}
	// Local, and so with no credential in front of it: what was fetched
	// is already on this disk.
	commit, err := CiRunGit(ctx, clone, nil, "rev-parse", "HEAD")
	if err != nil {
		return nil, "", nil, fmt.Errorf("resolving %s at %s: %w", r, ciRefDescription(ref), err)
	}
	workflows, _, err := ci.LoadDir(os.DirFS(clone))
	if err != nil {
		return workflows, strings.TrimSpace(commit), nil, err
	}
	tree, err := ciTree(ctx, clone)
	if err != nil {
		// A tree nobody could read is not a reason to refuse the run:
		// every job falls back to keying on the commit, which is what it
		// did before `paths:` existed, and the run is correct and merely
		// slower. Saying so out loud matters more than failing here would
		// -- a deployment silently paying for the whole suite on every
		// push is the thing this was built to stop.
		log.Printf("ci: %s at %s: reading the tree to key jobs on their declared paths: %v "+
			"(every job keys on the commit instead, so nothing is wrong and nothing is cached across commits)",
			r, ciRefDescription(ref), err)
		tree = nil
	}
	return workflows, strings.TrimSpace(commit), tree, nil
}

// ciCommitName is a commit as a caller may pin one: hex, abbreviated or
// whole.
var ciCommitName = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// OnBranch implements ci.BranchSource: whether commit is in ref's
// history, read from a clone of the branch's commits and nothing else.
//
// Asked only for a commit that is not the branch's tip, so the cost --
// a second clone, though of commit objects alone (--filter=tree:0) --
// is paid by the rare run pinned to something older, not by every run.
// The answer is a scan of `git rev-list` rather than `merge-base
// --is-ancestor`: naming a commit the clone does not hold would send a
// partial clone back to the remote to fetch it, and the commit this is
// asked about is exactly the one that must not be fetched to find out.
// GIT_NO_LAZY_FETCH says the same to any git new enough to read it.
func (s CiWorkflowSource) OnBranch(ctx context.Context, repo, ref, commit string) (bool, error) {
	r, err := model.ParseRepo(repo)
	if err != nil {
		return false, err
	}
	if !ciCommitName.MatchString(commit) {
		return false, nil
	}
	if ref != "" && !ciGitSafe.MatchString(ref) {
		return false, fmt.Errorf("%q is not a usable branch name", ref)
	}

	ctx, cancel := context.WithTimeout(ctx, ciCloneTimeout)
	defer cancel()
	dir, err := os.MkdirTemp(s.TempDir, "grain-ci-history-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(dir)

	clone := filepath.Join(dir, "repo.git")
	args := []string{"clone", "--quiet", "--bare", "--single-branch", "--filter=tree:0"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, s.cloneURL(r), clone)
	if out, err := s.clone(ctx, dir, r, args...); err != nil {
		return false, fmt.Errorf("reading %s's history: %w: %s", r, err, strings.TrimSpace(out))
	}
	out, err := CiRunGit(ctx, clone, []string{"GIT_NO_LAZY_FETCH=1"}, "rev-list", "HEAD")
	if err != nil {
		return false, fmt.Errorf("listing %s's history at %s: %w", r, ciRefDescription(ref), err)
	}
	commit = strings.ToLower(commit)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), commit) {
			return true, nil
		}
	}
	return false, nil
}

// ciTree is what one commit holds, as ci.Tree wants it: every file, and a
// hash of its content.
//
// `git ls-tree -r -z HEAD` against the clone that is already on this disk
// -- no network, and the object ids are already computed, so this is a
// read of the index rather than a hash of a working tree. -z is not
// optional: without it git quotes any path with a space or a non-ASCII
// byte in it, and a quoted path is one that would match no `paths:`
// pattern anybody wrote.
//
// The mode travels with the object id, because a script losing its execute
// bit changes what a job does without changing a byte of its content, and
// the whole promise of a content key is that everything which could change
// the answer is in it (ci.fingerprintVersion).
func ciTree(ctx context.Context, clone string) (*ci.Tree, error) {
	out, err := CiRunGit(ctx, clone, nil, "ls-tree", "-r", "-z", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(out))
	}
	tree := &ci.Tree{}
	for _, entry := range strings.Split(out, "\x00") {
		if entry == "" {
			continue
		}
		// "<mode> SP <type> SP <object> TAB <path>"
		meta, path, ok := strings.Cut(entry, "\t")
		if !ok {
			return nil, fmt.Errorf("git ls-tree printed a line with no path in it: %q", entry)
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			return nil, fmt.Errorf("git ls-tree printed %q, which is not <mode> <type> <object>", meta)
		}
		tree.Files = append(tree.Files, ci.TreeFile{Path: path, Blob: fields[0] + ":" + fields[2]})
	}
	return tree, nil
}

func (s CiWorkflowSource) cloneURL(repo model.RepoRef) string {
	if s.ConfigRepoDir != "" && repo.Same(model.ConfigRepo()) {
		return s.ConfigRepoDir
	}
	host := s.GitHubHost
	if host == "" {
		host = "github.com"
	}
	scheme := "https://"
	if s.InsecureHTTP {
		scheme = "http://"
	}
	if other := repo.OffDefaultHost(); other != "" {
		// A repository on another git host is fetched from that host.
		host, scheme = other, "https://"
	}
	return scheme + host + "/" + repo.Owner + "/" + repo.Name + ".git"
}

// clone runs the one git command here that talks to a remote, with this
// deployment's credential for repo in front of it.
//
// The token goes into a temporary askpass script rather than into the
// URL or onto the command line: anything in argv is readable by any
// other process on this host through `ps`, and a token baked into a
// remote URL persists in the clone's own .git/config. That is
// pkg/staterepo's gitAuthed's argument, applied here to a clone nobody
// keeps -- and the script lives beside the clone rather than in it, so
// nothing this writes can end up in a working tree grain then reads
// workflows out of.
//
// A repository the ladder covers with nothing is cloned anonymously,
// which is right for a public one and fails with git's own error for
// anything else.
//
// A repository whose credential only its host's own tool carries
// (gitproxy.AmbientCommandSource.ForgeViaCommand) is not cloned from
// https://<host> at all -- the front end redirects that to its login
// page -- but
// through gitproxy.ServerGitRemoteFor's loopback route, the same upstream
// hop the git proxy makes for that host's pushes: every argument naming
// cloneURL(repo) is swapped for the route's URL.
//
// A per-team configuration repository's name (grain/config-<team>) is
// refused before anything is run, as the git proxy refuses it: it names
// nothing, and cloning it from a forge's grain/ owner with this
// deployment's credential is the one thing it must not turn into.
func (s CiWorkflowSource) clone(ctx context.Context, dir string, repo model.RepoRef, args ...string) (string, error) {
	if gitproxy.RetiredConfigRepo(repo.Host, repo.Owner, repo.Name) {
		return "", errors.New(gitproxy.RetiredConfigRepoReason(repo.Owner, repo.Name))
	}
	if repo.Same(model.ConfigRepo()) {
		if !localConfigRepoEnabled(s.ConfigRepoDir) {
			return "", errors.New("local config repository is disabled when an external config repository is configured")
		}
		return CiRunGit(ctx, dir, nil, args...)
	}
	cred, found := s.gitCredential(ctx, repo)
	if found && s.Credentials != nil {
		remote, err := s.Credentials.ServerGitRemoteFor(ctx, cred, repo.GitHost(), repo.Owner, repo.Name)
		if err != nil {
			return "", err
		}
		if remote != nil {
			defer remote.Close()
			direct := s.cloneURL(repo)
			routed := make([]string, len(args))
			for i, a := range args {
				if a == direct {
					a = remote.URL
				}
				routed[i] = a
			}
			out, err := CiRunGit(ctx, dir, nil, routed...)
			if err != nil && remote.Err() != nil && !strings.Contains(out, remote.Err().Error()) {
				if strings.TrimSpace(out) != "" {
					out = remote.Err().Error() + "\n" + out
				} else {
					out = remote.Err().Error()
				}
			}
			return out, err
		}
	}
	var token string
	if found && cred.Token != nil {
		token = *cred.Token
	}
	if token == "" {
		return CiRunGit(ctx, dir, nil, args...)
	}
	askpass := filepath.Join(dir, "askpass.sh")
	body := "#!/bin/sh\ncase \"$1\" in\n*[Uu]sername*) printf '%s' 'x-access-token' ;;\n*) printf '%s' " +
		ciShellQuote(token) + " ;;\nesac\n"
	if err := os.WriteFile(askpass, []byte(body), 0o700); err != nil {
		return "", err
	}
	defer os.Remove(askpass)
	return CiRunGit(ctx, dir, []string{
		"GIT_ASKPASS=" + askpass,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=",
	}, args...)
}

// gitCredential is the credential repo is cloned with here:
// TeamCredential's, then the deployment's git connection for it. The
// credentials.json ladder is not asked (docs/single-tenant-reset.md,
// stage 1).
func (s CiWorkflowSource) gitCredential(ctx context.Context, repo model.RepoRef) (gitproxy.Credential, bool) {
	if s.TeamCredential != nil {
		if t, err := s.TeamCredential(ctx, repo); err == nil && t != "" {
			return gitproxy.Credential{Token: &t}, true
		}
	}
	if s.TeamGitCredential != nil || s.TeamGitCredentialOn != nil {
		return s.resolveTeamGitCredential(ctx, repo)
	}
	return gitproxy.Credential{}, false
}

// TeamGitCredentialLookups is model.Store.TeamGitCredential and
// TeamGitCredentialOn as grain CI and repository declarations ask them:
// for the team named, or -- on a single-tenant store
// (model.Store.SingleTenant), whose one tenant is the default team -- for
// the default team whatever team is named, the same way the store
// resolves every task's credential there.
//
// Either way the answer is the deployment's git connection
// (model.Store.GitConnectionOn): the team named chooses nothing.
func TeamGitCredentialLookups(store *model.Store) (
	byTeam func(ctx context.Context, team, owner, repo string) (string, error),
	onHost func(ctx context.Context, team, host, owner, repo string) (string, error)) {
	return func(ctx context.Context, _, owner, repo string) (string, error) {
			return store.GitConnectionOn(ctx, "", owner, repo)
		}, func(ctx context.Context, _, host, owner, repo string) (string, error) {
			return store.GitConnectionOn(ctx, host, owner, repo)
		}
}

func (s CiWorkflowSource) resolveTeamGitCredential(ctx context.Context, repo model.RepoRef) (gitproxy.Credential, bool) {
	offDefault := repo.OffDefaultHost()
	var credName string
	var credErr error
	switch {
	case offDefault != "":
		if s.TeamGitCredentialOn == nil {
			return gitproxy.Credential{}, false
		}
		credName, credErr = s.TeamGitCredentialOn(ctx, model.DefaultTeamID, offDefault, repo.Owner, repo.Name)
	case s.TeamGitCredentialOn != nil:
		credName, credErr = s.TeamGitCredentialOn(ctx, model.DefaultTeamID, "", repo.Owner, repo.Name)
		if (credErr != nil || credName == "") && s.GitHubHost != "" {
			credName, credErr = s.TeamGitCredentialOn(ctx, model.DefaultTeamID, s.GitHubHost, repo.Owner, repo.Name)
		}
	case s.TeamGitCredential != nil:
		credName, credErr = s.TeamGitCredential(ctx, model.DefaultTeamID, repo.Owner, repo.Name)
	}
	if credErr != nil || credName == "" || s.Credentials == nil {
		return gitproxy.Credential{}, false
	}
	if offDefault != "" {
		if cred, ok := s.Credentials.GetForRepoOn(credName, offDefault, repo.Owner, repo.Name); ok && (cred.Token != nil || cred.SSH != nil) {
			return cred, true
		}
		return gitproxy.Credential{}, false
	}
	if s.GitHubHost != "" {
		if cred, ok := s.Credentials.GetForRepoOn(credName, s.GitHubHost, repo.Owner, repo.Name); ok && (cred.Token != nil || cred.SSH != nil) {
			return cred, true
		}
	}
	if cred, ok := s.Credentials.GetForRepo(credName, repo.Owner, repo.Name); ok && (cred.Token != nil || cred.SSH != nil) {
		return cred, true
	}
	return gitproxy.Credential{}, false
}

// ciRunGit runs one git command in dir and reports what it printed.
//
// GIT_TERMINAL_PROMPT=0 is not optional: without it, a git that cannot
// authenticate falls through to a terminal prompt and hangs this daemon
// on a request rather than failing it.
func CiRunGit(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w", args[0], err)
	}
	return string(out), nil
}

// configRepoCISource supplies the validation Grain itself requires for its
// controller-local configuration repository. It is code rather than a file in
// that repository because the check is what decides whether a proposed config
// tree is safe to merge; a branch must not be able to remove the check that
// judges it. A repo_workflow at the same path still wins through ci.Layered,
// giving an operator the same deliberate override available for every other
// deployment-declared workflow.
//
// The check is a builtin job (ci.Job.Builtin, StateCheckSandboxes): the
// daemon runs `grain state check` itself, in-process, against a checkout
// of the commit made from its own local copy of the repository. It used
// to `docker pull` the image of the build this deployment runs and run
// that in a sandbox, which made every change to these repositories
// depend on the registry still holding that one tag -- and twice a
// retention cleanup deleted it while the build was still deployed, and
// every config change went red for a reason none of them could fix
// (grain:sha-adea62d, grain/task-3894; grain:sha-7781f61, task 4720).
// The code that has to be able to load the tree is the code that is
// running, so that is what checks it.
type ConfigRepoCISource struct{}

const ConfigRepoCIWorkflowPath = ".grain/workflows/grain-config.yml"

// StateCheckBuiltin is the ci.Job.Builtin of the configuration
// repository check, which StateCheckSandboxes answers.
const StateCheckBuiltin = "grain-state-check"

// stateCheckBuildEnv is on the state check step so that the build
// doing the checking is part of the job's fingerprint: a different build
// is a different job and misses the cache.
const stateCheckBuildEnv = "GRAIN_STATE_CHECK_BUILD"

func (s ConfigRepoCISource) Workflows(ctx context.Context, repo, _ string) ([]ci.Workflow, string, error) {
	r, err := model.ParseRepo(repo)
	if err != nil {
		return nil, "", err
	}
	args := "state check /state"
	env := map[string]string{
		// The build doing the checking, because the answer is that
		// build's: an upgrade must not replay a verdict an older one gave.
		stateCheckBuildEnv: buildIdentity(),
	}
	if !r.Same(model.ConfigRepo()) {
		return nil, "", nil
	}
	// The script is what the step is shown as having run, and it is what
	// StateCheckSandboxes runs: `grain state check` with these arguments,
	// in the daemon. It is never handed to a shell.
	body := `name: grain config

on:
  push:
    branches: ["**"]
  workflow_dispatch:

jobs:
  check:
    name: grain can load this
    timeout-minutes: 20
    steps:
      - name: Validate configuration
        run: grain ` + args + `
`
	workflows, err := ci.ParseDeclared([]ci.DeclaredWorkflow{{Path: ConfigRepoCIWorkflowPath, Text: body}})
	if err != nil {
		return nil, "", err
	}
	for i := range workflows {
		for j := range workflows[i].Jobs {
			workflows[i].Jobs[j].Builtin = StateCheckBuiltin
			for k := range workflows[i].Jobs[j].Steps {
				workflows[i].Jobs[j].Steps[k].Env = env
			}
		}
	}
	return workflows, "", nil
}

// ciDeclaredSource is ci.Source over what this deployment declares for
// a repository itself -- the repo_workflow rows an operator edits on the
// repo's own page (ui.SetRepoWorkflows, model.RepoWorkflow).
//
// It reads the store and clones nothing, which is what makes it the
// answer for a repository grain does not control: there is no
// `.grain/workflows` in somebody else's tree and no way to put one
// there, and this needs neither.
func ciDeclaredSource(store *model.Store) ci.Source {
	return ci.DeclaredSource{
		Declared: func(ctx context.Context, repo string) ([]ci.DeclaredWorkflow, error) {
			r, err := model.ParseRepo(repo)
			if err != nil {
				return nil, err
			}
			stored, err := store.GetRepoWorkflows(ctx, r)
			if err != nil {
				return nil, err
			}
			out := make([]ci.DeclaredWorkflow, 0, len(stored))
			for _, w := range stored {
				out = append(out, ci.DeclaredWorkflow{Path: w.Path, Text: w.Text})
			}
			return out, nil
		},
	}
}

// --- what a run is worth -------------------------------------------------

// ciQueueHeadsTTL is how long a reading of the merge queue's heads is
// reused. The gate asks per waiting job per attempt -- a second apiece
// against a deployment with a dozen jobs queueing -- and the queue itself
// moves once a reconcile cycle, so re-reading the whole of it every time
// would be a database scan per second to answer a question whose answer
// changes every half minute at best.
const ciQueueHeadsTTL = 5 * time.Second

// ciMergeQueuePriority is how this deployment ranks one CI run against
// another at the job gate: the change at the front of its repository's
// merge queue for its base branch goes first, because its verdict is the
// one that unblocks everything queued behind it (ci.PriorityMergeHead,
// grain/task-316).
//
// The heads come from orchestrator.MergeQueueHeads, so this is grain's own
// queue order rather than a second opinion about it. Everything else --
// what the number means, how age eventually beats it, what happens to a
// job that does not fit -- is pkg/ci's, which is told a number and nothing
// about merge queues.
func CiMergeQueuePriority(store *model.Store) func(ci.Request) ci.Priority {
	heads := &ciQueueHeads{store: store}
	return heads.rank
}

// ciQueueHeads is the cached reading, shared by every job queueing at the
// gate.
type ciQueueHeads struct {
	store *model.Store

	mu    sync.Mutex
	at    time.Time
	heads map[string]map[string]bool
	// repairs and repairsAt are readRepairs' own cached reading.
	repairs   map[string]map[string]bool
	repairsAt time.Time
	// failed is set when the last read did not answer, so that a database
	// that has stopped answering is one log line per TTL rather than one
	// per waiting job per second.
	failed bool
}

func (h *ciQueueHeads) rank(req ci.Request) ci.Priority {
	return max(req.Priority, h.queueRank(req))
}

// queueRank is rank without the request's own floor: what the merge
// queue and the open repairs say this run is worth.
//
// The floor is what a trunk release gate asks for (ci.PriorityReleaseGate,
// set by the orchestrator on the default branch's post-merge and release
// runs and on its release-gate schedules), and it ranks above a queue head
// and below a repair. Being at or above it is what lets a run's jobs into
// the budget's repair reserve and has a hole held for them at once
// (ci.Priority.MaySpendReserve, ReservesAtOnce): a branch with an open
// repair still ranks as one, since rank takes the larger of the two.
func (h *ciQueueHeads) queueRank(req ci.Request) ci.Priority {
	if h.store == nil {
		return ci.PriorityNormal
	}
	branch := strings.TrimPrefix(req.Ref, "refs/heads/")
	if branch == "" || req.Repo == "" {
		return ci.PriorityNormal
	}
	if h.readRepairs()[req.Repo][branch] {
		return ci.PriorityRepair
	}
	if h.read()[req.Repo][branch] {
		return ci.PriorityMergeHead
	}
	return ci.PriorityNormal
}

// readRepairs is the refs whose CI a red base branch's repair is waiting
// on, as of at most ciQueueHeadsTTL ago (ci.PriorityRepair,
// grain/task-488): each base branch with an open repair, whose own run
// is what confirms it green, and the branch of each repair that has a
// run live -- which dispatch admits one of per base at a time, so a flood
// of repairs does not become a flood of reserve-spending CI. A read that
// fails ranks nothing as a repair, which is what the gate did before.
func (h *ciQueueHeads) readRepairs() map[string]map[string]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.repairsAt.IsZero() && time.Since(h.repairsAt) < ciQueueHeadsTTL {
		return h.repairs
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h.repairsAt = time.Now()
	h.repairs = repairRefs(ctx, h.store)
	return h.repairs
}

// repairRefs is readRepairs' own read, repo -> branch.
func repairRefs(ctx context.Context, store *model.Store) map[string]map[string]bool {
	tags, err := store.BaseRepairBases(ctx)
	if err != nil || len(tags) == 0 {
		return nil
	}
	runs, err := store.LiveRuns(ctx)
	if err != nil {
		return nil
	}
	live := map[string]bool{}
	for _, r := range runs {
		live[r.TaskID] = true
	}
	out := map[string]map[string]bool{}
	add := func(repo, branch string) {
		if out[repo] == nil {
			out[repo] = map[string]bool{}
		}
		out[repo][branch] = true
	}
	for id, tag := range tags {
		if st, err := store.State(ctx, id); err != nil || st == model.StateClosed {
			continue
		}
		repo, base, ok := model.ParseBaseRepairBranchTag(tag)
		if !ok {
			continue
		}
		add(repo.String(), base)
		if live[id] {
			if task, err := store.GetTask(ctx, id); err == nil && task != nil {
				add(repo.String(), task.BranchName())
			}
		}
	}
	return out
}

// read is the heads as of at most ciQueueHeadsTTL ago. A read that fails
// leaves every run ranked the same, which is what this deployment did
// before it had an order at all: the queue's own reconciler is what acts
// on a store it cannot read, and a CI gate is not the place to escalate
// one.
func (h *ciQueueHeads) read() map[string]map[string]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failed && time.Since(h.at) < ciQueueHeadsTTL {
		return h.heads
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	heads, err := orchestrator.MergeQueueHeads(ctx, h.store)
	h.at = time.Now()
	if err != nil {
		if !h.failed {
			h.failed = true
			log.Printf("ci: cannot tell which changes are at the front of the merge queue, "+
				"so every run is ranked the same at the job gate: %v", err)
		}
		h.heads = nil
		return nil
	}
	h.failed = false
	h.heads = heads
	return heads
}

// ciStuckJobs is job id -> run id, for exactly as long as the Jobs gate
// might still ask SharedGate.Stuck about that job: filed the moment a
// job joins the gate's queue (ci.Engine.JobQueued) and forgotten the
// moment its Acquire call returns, admitted or not (ci.Engine.JobDequeued)
// -- both wired at buildCIService below. It exists because a gate's own
// waiter id (gateWaiter.id, "job-42") means nothing outside pkg/ci; this
// is where it is given a run to mean something about.
type ciStuckJobs struct {
	mu  sync.Mutex
	run map[string]string
}

func (j *ciStuckJobs) queued(jobID, runID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.run == nil {
		j.run = map[string]string{}
	}
	j.run[jobID] = runID
}

func (j *ciStuckJobs) dequeued(jobID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.run, jobID)
}

func (j *ciStuckJobs) runOf(jobID string) (string, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	runID, ok := j.run[jobID]
	return runID, ok
}

// ciJobStuck is ci.SharedGate.Stuck for this deployment: whether jobID
// is queueing behind capacity that cannot ever be released, because
// every live sandbox on the host -- not merely the part of it standing
// in this job's way, but every one of them -- is a task's own agent run
// waiting for the very CI run jobID belongs to.
//
// Conservative on purpose, because this is how a gate is made to
// overcommit a host rather than merely to wait longer, and a predicate
// that called ordinary queueing "stuck" would do that routinely instead
// of only when nothing else can ever free the room:
//
//   - a live CI job -- this job's own siblings included -- always
//     answers false. It holds a pool other than PoolAgent, it is making
//     progress on its own, and it will release on its own; a job merely
//     queued behind other CI jobs is an ordinary wait, not a cycle.
//   - an agent run whose task is not waiting on this job's run answers
//     false too, whatever else is true: that sandbox may go on holding
//     its capacity for an hour and release it for a reason that has
//     nothing to do with this job, which is still "wait and see" rather
//     than "nothing will ever move".
//   - one holder of either kind among several is enough to answer
//     false for all of them: this is an all-or-nothing read of the whole
//     ledger, not a search for the one holder actually in this job's way,
//     because a true answer here lets the host run over its budget and
//     that trade is only right when no release is coming from anywhere.
func ciJobStuck(store *model.Store, limits func() model.Limits, jobs *ciStuckJobs, jobID string) (bool, string) {
	if store == nil || jobs == nil {
		return false, ""
	}
	runID, ok := jobs.runOf(jobID)
	if !ok {
		return false, ""
	}
	ledger := limits().Ledger
	holders := ledger.Holders()
	if len(holders) == 0 {
		return false, ""
	}
	waitingRuns, err := store.AgentRunsAwaitingCIRun(context.Background(), runID)
	if err != nil {
		log.Printf("ci: job %s: reading which agent runs are waiting on run %s: %v -- "+
			"treating it as an ordinary wait rather than a stuck one", jobID, runID, err)
		return false, ""
	}
	if len(waitingRuns) == 0 {
		return false, ""
	}
	waiting := make(map[string]bool, len(waitingRuns))
	for _, id := range waitingRuns {
		waiting[id] = true
	}
	for _, h := range holders {
		if h.Pool != model.PoolAgent || !waiting[h.ID] {
			return false, ""
		}
	}
	return true, fmt.Sprintf(
		"every sandbox this deployment is holding right now is a task's own agent run waiting for CI "+
			"run %s to answer it, and %s is one of that run's jobs -- no release is coming from any of "+
			"them until it runs", runID, jobID)
}

// --- putting it together -------------------------------------------------

// buildCIService is the whole of grain CI for one deployment: where
// workflows are read from, what a job runs in, and how many jobs run at
// once. Called from runDaemon once the git proxy is up, since every part
// of a job's checkout goes through it.
//
// limits is what makes "how much" a shared answer rather than a second
// opinion (grain/task-296, grain/task-28). It used to be cfg.maxWorkers --
// a bound borrowed from dispatch's own, so a deployment sized for N
// sandboxes could hold N agent runs and N CI jobs at once, and more than
// that again with several CI runs in flight, since ci.Engine.Concurrency
// bounds a run rather than an engine. Then it was a *count* of CI jobs
// plus a share of the machine carved out for CI by reserving the rest for
// agents. It is now the one thing both sides share: the deployment's
// sandbox budget and the live ledger every sandbox on this host is charged
// to, so the gate below admits a job only while the machine has room for
// its shape counting every agent run in flight, and dispatch admits a run
// only while it has room counting every CI job in flight.
//
// It is a func rather than a value because the budget's three settings are
// editable in Settings: a deployment resized under a running daemon is
// honoured by the next job that asks (liveConfig.limits).
// retention is the other live answer beside limits: how long this
// deployment keeps what CI recorded (liveConfig.ciRetention). bounds is
// the third: what its job cache and its collected artifacts may occupy
// on the data disk (liveConfig.ciStorageBounds). Funcs for the same
// reason, and nil is allowed for either -- a test wiring an engine gets
// grain's own defaults rather than having to know them.
func BuildCIService(ctx context.Context, cfg Config, limits func() model.Limits, retention func() ci.StoredRunRetention,
	bounds func() ci.StorageBounds, store *model.Store,
	sandboxes orchestrator.Sandboxes,
	credentials *gitproxy.CredentialSet, proxyURL string, tokens *gitproxy.SandboxTokenStore,
	configCheckImage func() string, forbidden *gitproxy.ForbiddenSet,
	capabilities func() *model.CapabilityRegistry, capabilityCredentials model.CredentialResolver,
	packageCache func(context.Context) string,
	packageCacheCA func(context.Context) []byte,
	packageCacheDNS func(context.Context) string,
	actionsCache func(actionscache.Scope) (string, func(), error)) *ci.Service {

	var teamGitCredential func(context.Context, string, string, string) (string, error)
	var teamGitCredentialOn func(context.Context, string, string, string, string) (string, error)
	if store != nil {
		teamGitCredential, teamGitCredentialOn = TeamGitCredentialLookups(store)
	}
	// What a job is refused for lacking: the deployment's credential for
	// the repositories it declared. A single-tenant daemon refuses none --
	// the proxy sends its one tenant's jobs out on the daemon's own
	// ladder when the default team has no connection for them.
	jobTeamGitCredential := teamGitCredential
	if store != nil && store.SingleTenant {
		jobTeamGitCredential = nil
	}
	// stuckJobs is what lets the Jobs gate's Stuck hook, below, ask about
	// a job it is about to refuse: which run it belongs to, so it can
	// check that run's own tasks against the ledger (ciJobStuck).
	stuckJobs := &ciStuckJobs{}
	grantReservations := &CiGrantReservations{
		Capabilities: capabilities, Credentials: capabilityCredentials, Log: log.Printf,
		Policy: cfg.policy,
	}
	if store != nil {
		grantReservations.Config = store.GetConfig
	}
	if tokens != nil && proxyURL != "" {
		grantReservations.CapServers, grantReservations.ProxyURL, grantReservations.ProxyToken = SandboxCapabilities, proxyURL, tokens.EnsureToken
	}
	jobSandboxes := CiSandboxes{
		Capabilities: capabilities, Credentials: capabilityCredentials, Sandboxes: sandboxes,
		DefaultShape: func() orchestrator.Shape {
			if limits == nil {
				return orchestrator.Shape{}
			}
			s := limits().AgentShape
			return orchestrator.Shape{CPUs: s.CPUs, MemoryMB: s.MemoryMB}
		},
		GitRemoteBase: proxyURL, MintToken: tokens.EnsureToken, RevokeToken: tokens.Revoke,
		Scopes: CiReadScopes, WriteScopes: CiWriteScopes, Teams: CiSandboxTeams, Log: log.Printf, Grants: grantReservations,
		TeamGitCredential: jobTeamGitCredential,
		Policy:            cfg.policy,
		PackageCache:      packageCache,
		PackageCacheCA:    packageCacheCA,
		PackageCacheDNS:   packageCacheDNS,
		ActionsCache:      actionsCache,
		Stopping:          cfg.shutdownStarted,
		// Counted into the same two rows dispatch's own sandboxes are
		// counted into, out of the same store this service already holds
		// for its run history.
		Meter: orchestrator.MeterFor(store),
	}
	service := &ci.Service{
		Runs:           buildCIRunStore(ctx, store, retention),
		PlanDir:        filepath.Join(cfg.DataDir, "state", "ci-plans"),
		Capabilities:   CiRepoCapabilities(store),
		ReleaseSecrets: CiRepoReleaseSecrets(store),
		DeployedImages: func() []string { return deployedImages(configCheckImage) },
		SandboxImage: func() string {
			if setter, ok := sandboxes.(orchestrator.SandboxImageSetter); ok {
				if img := setter.CurrentImage(); img != "" {
					return img
				}
			}
			return activeSandboxImageFor(cfg)
		},
		Source: ci.Layered{
			CiWorkflowSource{
				ConfigRepoDir:       StateRepoDir(cfg.DataDir),
				Credentials:         credentials,
				GitHubHost:          cfg.GithubHost,
				InsecureHTTP:        cfg.GithubInsecureHTTP,
				Forbidden:           forbidden,
				TempDir:             filepath.Join(cfg.DataDir, "state"),
				TeamGitCredential:   teamGitCredential,
				TeamGitCredentialOn: teamGitCredentialOn,
			},
			ConfigRepoCISource{},
			ciDeclaredSource(store),
		},
		Engine: &ci.Engine{
			// A job sandbox found OOM-killed is retried once and is a
			// decrease signal for auto overcommit, as an agent's is.
			NoteOOM:   overcommitNoteOOM(cfg.Overcommit),
			Cache:     CiJobCache(cfg, bounds),
			Artifacts: CiArtifactStore(cfg, bounds),
			// The configuration repositories' check is run by this
			// daemon, from its own copy of them; every other job gets a
			// sandbox.
			Sandboxes: StateCheckSandboxes{
				Sandboxes:     jobSandboxes,
				ConfigRepoDir: StateRepoDir(cfg.DataDir),
				TempDir:       filepath.Join(cfg.DataDir, "state"),
			},
			Admission: grantReservations,
			// A scarce credential (a GCP service-account key, a Gemini
			// API key) is minted at admission, once per job that holds
			// it, so it goes only to the jobs that declare it rather
			// than to every job of a suite that declares nothing
			// (grain/task-281).
			ExplicitOnlyCapability: CiScarceCapability(capabilities),
			// ConcurrencyLimit bounds one run, Jobs bounds the whole host.
			// By default no per-run cap (model.Limits.CIJobsPerRun) is
			// set, so this returns 0 and all ready jobs in a run proceed
			// directly to Engine.Jobs, where the deployment's sandbox
			// budget governs concurrency. When an operator sets
			// CIJobsPerRun, it caps one run's width.
			ConcurrencyLimit: func() int {
				if limits == nil {
					return 0
				}
				return limits().CIJobsPerRun
			},
			// What lets the Jobs gate's own Stuck hook, below, ask about
			// a job by run rather than by its own meaningless id --
			// ciStuckJobs' whole reason to exist.
			JobQueued:   stuckJobs.queued,
			JobDequeued: stuckJobs.dequeued,
			// And which of the runs queueing for those sandboxes matters
			// most: the change at the front of a merge queue, whose
			// verdict is what lets everything behind it move
			// (grain/task-316). Asked per queueing job per attempt, so a
			// job that has been waiting half an hour is ranked on the
			// queue as it stands rather than as it was when the run
			// started.
			Priority: CiMergeQueuePriority(store),
			Jobs: &ci.SharedGate{
				// No Limit: a deployment-wide ceiling on CI job sandboxes is
				// exactly what grain/task-28 removed, and no team has one of
				// its own any more. What bounds the deployment is the budget
				// below.
				// And the half a count cannot say: what those jobs weigh.
				// The deployment's whole sandbox budget, shared with every
				// agent run rather than divided with them, so a job asking
				// for 16 GiB on a full host waits for memory instead of
				// taking the host into swap (model.Budget, grain/task-173).
				// Re-read per attempt so a deployment resized under a
				// running daemon is honoured by the next job rather than at
				// the next restart -- and so that the live disk reading
				// folded in by limitsWithDisk is this second's.
				Budget: func() model.Budget { return limits().Budget },
				// And where the shapes already spent are counted: the one
				// ledger every sandbox on this host is charged to, agent runs
				// included. A job is tested and charged against it under the
				// ledger's own lock, so a dispatch happening in the same
				// instant cannot be given the same capacity.
				Ledger: func() *model.Ledger { return limits().Ledger },
				// What a job's `sandbox:` dimensions are floored against:
				// the backend builds the sandbox at least as large in
				// each dimension as both the job's ask and the
				// deployment's default shape, so each dimension is
				// charged max(want, Default()).
				Default: func() model.Commitment { return limits().AgentShape },
				// And how the gate orders whatever those two are holding
				// back: how long a job queues to gain a step of rank, and
				// how long the budget has to have been refusing one before
				// capacity is held for it rather than spent on smaller
				// jobs (model.Limits.CIAgeBoost and CIReserveAfter,
				// grain/task-325). Both funcs rather than values for the
				// reason the two above are -- they are asked for on every
				// attempt, so a job that has been queueing for half an
				// hour, which is exactly the job an operator shortening a
				// window is trying to rescue, is ordered by the window as
				// it stands now.
				//
				// Zero travels through as zero: what an unconfigured
				// deployment gets is ci.DefaultAgeBoost and
				// ci.DefaultReserveAfter, resolved in pkg/ci beside the
				// rule they belong to rather than guessed at here.
				AgeBoost:     func() time.Duration { return limits().CIAgeBoost },
				ReserveAfter: func() time.Duration { return limits().CIReserveAfter },
				// The deadlock Sole does not reach: a job this deployment
				// is refusing because an agent sandbox is holding the
				// room it needs, where that sandbox's own task is
				// waiting for this very job's run to answer it. No
				// release is coming from either side then, so this runs
				// the job anyway rather than waiting out a cycle
				// (grain/task-41; ciJobStuck has the detail and the
				// cases it deliberately leaves waiting).
				Stuck: func(jobID string) (bool, string) {
					return ciJobStuck(store, limits, stuckJobs, jobID)
				},
				Wake: wakeCycle,
				Log:  log.Printf,
			},
			Stopping: cfg.shutdownStarted,
			Log:      log.Printf,
		},
		Stopping: cfg.shutdownStarted,
		Log:      log.Printf,
	}
	// The warmer is set after the service rather than inside it because
	// it needs the service: warming a guest is starting a CI run and
	// watching it, and the engine that would run that job is the one this
	// hangs off (ci.GuestWarmer). Nothing here is circular at run time --
	// a warm run's own jobs never ask for a warm, which the warmer
	// enforces by remembering the runs it started.
	service.Engine.GuestCache = &ci.GuestWarmer{
		Jobs:  CiCacheFillingJobs(store, sandboxGuestImage(cfg)),
		Start: service.Start,
		Get:   service.Get,
		Log:   log.Printf,
	}
	adopted, lost := AdoptCIRuns(ctx, service)
	if cfg.runHostUpgrade != nil && (len(adopted) > 0 || len(lost) > 0) {
		cfg.runHostUpgrade.setCIAdoption(adopted, lost)
	}
	return service
}

// AdoptCIRuns inspects unconcluded CI runs in service.Runs before ReapOrphans
// runs: for each run, it attempts to readopt any in-flight job's sandbox and
// verify its in-sandbox detached step runner, then resumes the run in place via
// service.Adopt. Runs that cannot be resumed are finished as interrupted via
// RecoverInterruptedExcept.
func AdoptCIRuns(ctx context.Context, service *ci.Service) (adopted, lost []orchestrator.AdoptedCIJobNote) {
	if service == nil || service.Runs == nil || service.Engine == nil || service.IsStopping() {
		return nil, nil
	}
	lister, ok := service.Runs.(interface {
		LiveRuns(context.Context) ([]ci.Run, error)
		RecoverInterruptedExcept(context.Context, time.Time, []string) (int, error)
	})
	if !ok {
		return nil, nil
	}
	liveRuns, err := lister.LiveRuns(ctx)
	if err != nil {
		// Nothing can be adopted, but what the previous process left in
		// flight is still resolved as it was before adoption existed: a
		// run left unconcluded is one Start answers every later request
		// for the same plan with (RunStore.Live), so its branch would
		// never be put through CI again.
		log.Printf("ci: listing live runs for adoption, so adopting none: %v", err)
		if stamped, rerr := lister.RecoverInterruptedExcept(ctx, time.Now().UTC(), nil); rerr != nil {
			log.Printf("ci: resolving runs a previous process left in flight: %v", rerr)
		} else if stamped > 0 {
			log.Printf("ci: %d run(s) were still going when this deployment last stopped; "+
				"they are recorded as interrupted and their branches will be put through CI again", stamped)
		}
		return nil, nil
	}
	if len(liveRuns) == 0 {
		return nil, nil
	}
	readopter, _ := service.Engine.Sandboxes.(interface {
		Readopt(context.Context, ci.SandboxRequest) (ci.Sandbox, error)
	})

	var adoptedRunIDs []string
	for _, run := range liveRuns {
		if service.IsStopping() {
			break
		}
		plan, err := service.ReconstructPlan(ctx, run)
		if err != nil {
			log.Printf("ci: run %s (%s@%s): cannot load or reconstruct plan for adoption: %v", run.ID, run.Repo, run.Ref, err)
			for _, j := range run.Jobs {
				if !j.Conclusion.Done() && j.StartedAt != nil {
					lost = append(lost, orchestrator.AdoptedCIJobNote{
						Run: run.ID, Job: j.JobID, Repo: run.Repo, Ref: run.Ref, Sandbox: j.Sandbox,
						Reason: fmt.Sprintf("plan unavailable (%v)", err),
					})
				}
			}
			continue
		}
		plannedByJobID := make(map[string]ci.PlannedJob, len(plan.Jobs))
		for _, pj := range plan.Jobs {
			plannedByJobID[pj.Job.ID] = pj
		}

		adoptedJobs := map[string]ci.AdoptedJob{}
		var runAdoptedNotes, runLostNotes []orchestrator.AdoptedCIJobNote
		canAdoptRun := true

		for i, j := range run.Jobs {
			if (j.Conclusion == ci.ConclusionErrored || j.Conclusion == ci.ConclusionFailure) && ci.JobFailedOnDaemonService(j) {
				if pj, hasPlanned := plannedByJobID[j.JobID]; hasPlanned {
					run.Jobs[i] = ci.NewJobRun(pj)
					service.Runs.Put(run)
				}
				continue
			}
			if j.Conclusion.Done() || j.StartedAt == nil {
				continue
			}
			pj, hasPlanned := plannedByJobID[j.JobID]
			if !hasPlanned {
				canAdoptRun = false
				runLostNotes = append(runLostNotes, orchestrator.AdoptedCIJobNote{
					Run: run.ID, Job: j.JobID, Repo: run.Repo, Ref: run.Ref, Sandbox: j.Sandbox,
					Reason: "job missing from reconstructed plan",
				})
				break
			}
			if j.Sandbox == "" {
				// Queued / preparing before a sandbox was recorded: Engine.Resume
				// will start this job fresh in the resumed run.
				continue
			}
			if readopter == nil {
				canAdoptRun = false
				runLostNotes = append(runLostNotes, orchestrator.AdoptedCIJobNote{
					Run: run.ID, Job: j.JobID, Repo: run.Repo, Ref: run.Ref, Sandbox: j.Sandbox,
					Reason: "sandbox backend does not support readoption",
				})
				break
			}
			sbReq := ci.SandboxRequest{
				Name:            j.Sandbox,
				Repo:            run.Repo,
				Ref:             run.Ref,
				Commit:          run.Commit,
				Event:           run.Event,
				TrustedSchedule: plan.Request.TrustedSchedule,
				// Narrowed to this job exactly as runJob narrowed the
				// request its sandbox was acquired with: the run's whole
				// set would hand a readopted job repository scopes (and
				// explicit-only capabilities) it never declared, and drop
				// the repo-read:/repo-write: it did.
				Capabilities:   service.Engine.JobCapabilities(pj.Job, plan.Request.Capabilities),
				ReleaseSecrets: ci.JobSecrets(pj.Job, plan.Request.ReleaseSecrets),
				Builtin:        pj.Job.Builtin,
				Shape:          pj.Job.Sandbox,
				Timeout:        pj.Job.Timeout(),
			}
			sb, err := readopter.Readopt(ctx, sbReq)
			if err != nil {
				canAdoptRun = false
				runLostNotes = append(runLostNotes, orchestrator.AdoptedCIJobNote{
					Run: run.ID, Job: j.JobID, Repo: run.Repo, Ref: run.Ref, Sandbox: j.Sandbox,
					Reason: fmt.Sprintf("sandbox %s gone (%v)", j.Sandbox, err),
				})
				break
			}
			// Find the step that was in flight (or about to start) and check its
			// detached step runner directory if a step was already started.
			inFlightStepIdx := -1
			for si, st := range j.Steps {
				if !st.Conclusion.Done() && st.StartedAt != nil {
					inFlightStepIdx = si
					break
				}
			}
			if inFlightStepIdx >= 0 {
				attempt := j.RetryCount + 1
				stepID := ci.StepIDFor(attempt, inFlightStepIdx)
				var backing orchestrator.Sandbox
				if csb, ok := sb.(*CiSandbox); ok {
					backing = csb.Backing
				}
				if backing != nil {
					hasRunner, rerr := orchestrator.HasStepRunner(ctx, backing, stepID)
					if rerr != nil || !hasRunner {
						_ = sb.Release(ctx)
						canAdoptRun = false
						reason := fmt.Sprintf("step runner %s missing in sandbox %s", stepID, j.Sandbox)
						if rerr != nil {
							reason = fmt.Sprintf("checking step runner %s in sandbox %s: %v", stepID, j.Sandbox, rerr)
						}
						runLostNotes = append(runLostNotes, orchestrator.AdoptedCIJobNote{
							Run: run.ID, Job: j.JobID, Repo: run.Repo, Ref: run.Ref, Sandbox: j.Sandbox,
							Reason: reason,
						})
						break
					}
				}
			}
			adoptedJobs[pj.ID] = ci.AdoptedJob{
				Sandbox: sb,
				Job:     j,
			}
			runAdoptedNotes = append(runAdoptedNotes, orchestrator.AdoptedCIJobNote{
				Run: run.ID, Job: j.JobID, Repo: run.Repo, Ref: run.Ref, Sandbox: j.Sandbox,
			})
		}

		if !canAdoptRun {
			for _, aj := range adoptedJobs {
				if aj.Sandbox != nil {
					_ = aj.Sandbox.Release(ctx)
				}
			}
			lost = append(lost, runLostNotes...)
			continue
		}
		if err := service.Adopt(ctx, run, plan, adoptedJobs); err != nil {
			for _, aj := range adoptedJobs {
				if aj.Sandbox != nil {
					_ = aj.Sandbox.Release(ctx)
				}
			}
			for _, note := range runAdoptedNotes {
				note.Reason = err.Error()
				lost = append(lost, note)
			}
			continue
		}
		adoptedRunIDs = append(adoptedRunIDs, run.ID)
		adopted = append(adopted, runAdoptedNotes...)
	}

	stamped, err := lister.RecoverInterruptedExcept(ctx, time.Now().UTC(), adoptedRunIDs)
	switch {
	case err != nil:
		log.Printf("ci: resolving runs a previous process left in flight: %v", err)
	case len(adopted) > 0 || len(lost) > 0:
		log.Printf("ci: adoption: %d job(s) adopted across %d run(s), %d lost (%d run(s) marked interrupted)",
			len(adopted), len(adoptedRunIDs), len(lost), stamped)
	case stamped > 0:
		log.Printf("ci: %d run(s) were still going when this deployment last stopped; "+
			"they are recorded as interrupted and their branches will be put through CI again", stamped)
	}
	return adopted, lost
}

// ciCacheFillingJobs is which of a repository's jobs grain may dispatch
// to fill a build input its own CI then reads -- today the
// content-addressed sandbox guest cache, and nothing else
// (pkg/grit/cache/guestcache.go, and model.RepoConfig.PostMergeDispatchJobs for
// what naming one says).
//
// It is the same operator-owned list the post-merge dispatch reads, and
// deliberately so: the question "which of this repository's jobs are
// grain's to dispatch, and fill caches rather than ship releases" has one
// answer, and a second setting saying the same thing about the same job
// is a second thing to keep in step. What differs is the ref it is
// dispatched on -- there, the base branch after a merge; here, the branch
// whose own guest inputs nothing has published -- and guestcache.go's
// "What this trusts" is the whole of what that difference means.
//
// The same list in the same sense includes the one entry grain adds to
// it rather than reading: the guest cache job, for the repository
// publishing the guest this deployment's own sandboxes are built from,
// while its releases are off (model.EffectivePostMergeDispatchJobs,
// grain/task-457). Reading the stored field instead would leave that
// deployment warming its base branch and not its branches -- the two
// halves of one answer disagreeing about the same repository, which is
// the second setting this function's whole point is to avoid.
//
// A repository that names none, and is not that one, gets no warming --
// the behaviour every deployment had before this existed.
func CiCacheFillingJobs(store *model.Store, guestImage string) func(repo string) []string {
	if store == nil {
		return nil
	}
	return func(name string) []string {
		repo, err := model.ParseRepo(name)
		if err != nil {
			return nil
		}
		// Background rather than a caller's context: this is asked from a
		// goroutine watching a build, and a read of one row is not worth
		// threading a cancellation through.
		cfg, err := store.GetRepoConfig(context.Background(), repo)
		if err != nil {
			log.Printf("ci: reading %s's cache-filling jobs: %v (nothing is warmed, so a job that "+
				"needs a sandbox guest resolves its own)", repo, err)
			return nil
		}
		if cfg == nil {
			return nil
		}
		return model.EffectivePostMergeDispatchJobs(*cfg, guestImage)
	}
}

// buildCIRunStore is where this deployment's runs are kept: rows in the
// same database as everything else grain records about itself
// (model.CIRun), rather than the map a daemon used to lose on every
// restart.
//
// Two things happen here that only a daemon can do, which is why the
// wiring is not inside pkg/ci. The runs a *previous* process left in
// flight are resolved before anything reads one back
// (RecoverInterrupted: a row saying "no conclusion" means "still going",
// and nothing is going any more), and a failure to do that is logged
// rather than fatal -- a deployment whose CI history cannot be tidied
// should still run CI, and every unresolved run costs at most the extra
// run orchestrator.collectCIRun already starts when it cannot find one.
func buildCIRunStore(ctx context.Context, store *model.Store, retention func() ci.StoredRunRetention) ci.RunStore {
	if store == nil {
		// Nowhere to write, which is a test wiring an engine and nothing
		// else -- runDaemon always has a store, and a deployment without
		// one has no tasks to run CI for either. In memory, exactly as
		// every deployment did before these runs had a schema.
		return ci.NewRunStore(0)
	}
	runs := ci.NewStoredRunStore(store, ci.StoredRunStoreOptions{Retention: retention, Log: log.Printf})
	// The policy as resolved rather than as configured, the same way the
	// job cache and the artifact store each report their own bounds: one
	// line that answers "how long will the evidence still be here" for an
	// operator who does not know which of four settings the pane holds it
	// in.
	keep := ci.DefaultStoredRunRetention()
	if retention != nil {
		keep = retention()
	}
	log.Printf("ci: %s -- raise or lower any of it in Settings, where it takes effect without a restart", keep.Describe())
	return runs
}

// ciCacheDir is where a deployment keeps its CI job cache: beside the
// transcripts and the git proxy's audit log, under the data directory's
// `state` -- grain's own working files, as against `state-repo`, which
// is the configuration a human proposes changes to.
//
// A cache entry does not belong in either the store or that repository:
// it holds a job's whole output, it is worthless to anybody reading a
// diff, and losing the lot costs a re-run and nothing else. That is also
// why nothing here backs it up and why a redeploy onto a fresh data
// directory simply starts cold.
func ciCacheDir(dataDir string) string { return filepath.Join(dataDir, "state", "ci-cache") }

// CiJobCache is the cache the engine consults before it builds a
// sandbox: one file per fingerprint, bounded by age and by disk
// (ci.DirCache).
//
// A cache that cannot be opened is not a reason to refuse to run CI --
// it is a reason to run every job, which is exactly what this deployment
// did before the cache existed. So the failure is logged, loudly enough
// that an operator who wonders why nothing is ever cached can find it,
// and nil is handed to the engine.
//
// The bounds are not read here at all: bounds is handed to the cache as
// a provider it calls on every sweep, every Lookup and every Save, so a
// ceiling moved in the Settings pane binds the next sweep rather than the
// next restart (model.Config.CICacheMB, grain/task-317). nil is grain's
// own defaults, which is what a test that wires a cache and no
// configuration wants.
//
// Emptying it, as against resizing it, is still `grain ci cache clear`
// and the button beside it in the CI pane: a lowered ceiling is applied
// by the next sweep, and an operator who needs the disk back now should
// not have to wait five minutes for it.
func CiJobCache(cfg Config, bounds func() ci.StorageBounds) ci.Cache {
	dir := ciCacheDir(cfg.DataDir)
	cache, err := ci.OpenDirCache(dir, ci.DirCacheOptions{
		Bounds: bounds,
		Log:    log.Printf,
	})
	if err != nil {
		log.Printf("ci: no job cache at %s (%v) -- every job will run, every time", dir, err)
		return nil
	}
	// The bounds as resolved rather than as stored: a setting left blank
	// reads back as the default here, so this one line answers "what is
	// this cache actually sized at" without an operator having to know
	// whether the number came from the row or from grain itself. What it
	// cannot claim any more is that these are the bounds for the life of
	// the process -- they are the bounds as this line was printed.
	if stats, err := cache.Stats(context.Background()); err == nil {
		log.Printf("ci: job results are cached in %s for up to %s, within %d bytes and %d entries "+
			"(a single result past %d bytes is not kept) -- `grain ci cache` reports it, "+
			"`grain ci cache clear` empties it, and the bounds are editable in Settings",
			dir, stats.MaxAge, stats.MaxBytes, stats.MaxEntries, stats.MaxEntryBytes)
	} else {
		log.Printf("ci: job results are cached in %s", dir)
	}
	return cache
}

// ciArtifactDir is where a deployment keeps what failing jobs left
// behind: beside the job cache, under the data directory's `state`, and
// out of the store and the state repository for the same reasons a cache
// entry is (ciCacheDir above says them; a trace bundle is a stronger
// case for every one of them, being megabytes of binary).
func ciArtifactDir(dataDir string) string {
	return filepath.Join(dataDir, "state", "ci-artifacts")
}

// ciArtifactStore is where a job's declared files go when it does not
// succeed: one directory per run, bounded by age and by disk
// (ci.DirArtifacts).
//
// A store that cannot be opened is not a reason to refuse to run CI. It
// is a reason to keep nothing, which is exactly where every deployment
// was before this existed -- a red job's own log and no trace beside it
// -- so the failure is logged, loudly enough that an operator wondering
// why a download is never offered can find it, and nil goes to the
// engine.
// bounds is the same provider the job cache is given and is read the same
// way -- on every Save, including the per-artifact cap a job's sandbox is
// told before it copies anything out, and on every sweep.
func CiArtifactStore(cfg Config, bounds func() ci.StorageBounds) ci.ArtifactStore {
	dir := ciArtifactDir(cfg.DataDir)
	store, err := ci.OpenDirArtifacts(dir, ci.DirArtifactsOptions{Bounds: bounds, Log: log.Printf})
	if err != nil {
		log.Printf("ci: no artifact store at %s (%v) -- a failing job's declared files will not be kept", dir, err)
		return nil
	}
	// The bounds as resolved rather than as passed, exactly as the cache
	// reports its own: this one line answers "what is this actually sized
	// at" for an operator who does not know which flags the unit file
	// mentions.
	if stats, err := store.Stats(context.Background()); err == nil {
		log.Printf("ci: a failing job's declared artifacts are kept in %s for up to %s, within %d bytes "+
			"(%d per run, and a single artifact past %d bytes is not kept) -- the bounds are editable "+
			"in Settings",
			dir, stats.MaxAge, stats.MaxBytes, stats.MaxRunBytes, stats.MaxArtifactBytes)
	} else {
		log.Printf("ci: a failing job's declared artifacts are kept in %s", dir)
	}
	return store
}

// deployedImages is what a release run is told this deployment is
// running (ci.Request.DeployedImages): the build's own stamped image and
// the image its config repository's check pulls, which differ only when
// an operator has set "checkImage". cmd/ghcr-prune keeps the tags of
// both, since either one going missing fails every check of that
// repository for a reason no change to it can fix.
func deployedImages(configCheckImage func() string) []string {
	images := []string{DefaultGrainImage}
	if configCheckImage != nil {
		if configured := strings.TrimSpace(configCheckImage()); configured != "" && configured != DefaultGrainImage {
			images = append(images, configured)
		}
	}
	return images
}

// ciClusterFullRetry is how long a CI job whose pod the cluster had no
// room for waits before asking again.
var ciClusterFullRetry = 15 * time.Second

// BuildGritBackendRunner constructs a `ci.BackendRunner` connected to a remote
// `grit serve` endpoint (`cfg.GritURL`) and starts its background `/v1/events`
// consumer loop.
func BuildGritBackendRunner(ctx context.Context, cfg Config, retention func() ci.StoredRunRetention, store *model.Store) *ci.BackendRunner {
	stateDir := filepath.Join(cfg.DataDir, "state")
	cursor := ci.NewFileCursorStore(filepath.Join(stateDir, "grit-cursor.json"))
	client := gritapi.NewClient(gritapi.ClientConfig{
		BaseURL:     cfg.GritURL,
		ClientToken: cfg.GritClientToken,
		Cursor:      cursor,
	})
	runner := ci.NewBackendRunner(ci.BackendRunnerConfig{
		Backend:  client,
		Store:    buildCIRunStore(ctx, store, retention),
		Cursor:   cursor,
		StateDir: stateDir,
	})
	go runner.RunConsumer(ctx, 250*time.Millisecond)
	return runner
}
