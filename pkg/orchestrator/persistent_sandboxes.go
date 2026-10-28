package orchestrator

// Persistent sandboxes: staging environments and long-lived services
// (model.PersistentSandbox) that grain keeps running.
//
// Each enabled definition gets one goroutine for as long as it is
// enabled, and that goroutine is the whole of the supervision: acquire a
// sandbox from the deployment's own backend, check the repository out in
// it, run the command, and when the command returns -- cleanly or not --
// or the sandbox under it dies, record why and do it again, backing off
// on a crash loop. The reconcile step only starts, stops and replaces
// those goroutines as the definitions change, so a reconcile tick never
// waits on a VM boot.
//
// A persistent sandbox is acquired straight from Deps.Sandboxes rather
// than through dispatch, so it never holds one of the max_concurrent_runs
// slots task attempts are dispatched into; its VM is reported on its own
// in the Sandboxes view instead (model.PersistentSandboxStatus).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Persistent sandbox states, as PersistentSandboxStatus.State reports them.
const (
	PersistentStarting   = "starting"
	PersistentRunning    = "running"
	PersistentRestarting = "restarting"
	PersistentStopped    = "stopped"
)

// persistentBackoffBase and persistentBackoffMax bound the wait between
// restarts of a command that keeps exiting; persistentStableAfter is how
// long a command has to stay up for its next exit to be treated as a
// first one again.
var (
	persistentBackoffBase = time.Second
	persistentBackoffMax  = 5 * time.Minute
	persistentStableAfter = 2 * time.Minute
)

// persistentLogBytes is how much of a command's output is kept per
// sandbox, and persistentEventCount how many exit events.
const (
	persistentLogBytes   = 64 << 10
	persistentEventCount = 20
)

// persistentCheckout is the checkout's directory, relative to the
// sandbox's workspace.
const persistentCheckout = "repo"

// PersistentBackoff is the wait before the restart that follows failures
// rapid exits in a row: persistentBackoffBase doubling, up to
// persistentBackoffMax. Zero failures is no wait.
func PersistentBackoff(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	d := persistentBackoffBase
	for i := 1; i < failures && d < persistentBackoffMax; i++ {
		d *= 2
	}
	return min(d, persistentBackoffMax)
}

// persistentSandboxName is the backend name a persistent sandbox is
// acquired under: "p" and eight hex digits, for standingSandboxName's
// reason (granule's nine-byte budget), and out of the namespaces runs,
// CI jobs and standing sandboxes use.
func persistentSandboxName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "p" + hex.EncodeToString(sum[:4])
}

// SandboxPortDialer is a Sandbox that can open a TCP connection to a port
// something inside it listens on: a host directory's own loopback, or a
// guest reached by exec'ing a forwarder in it. It is what a persistent
// sandbox's host port and UI sub-path are forwarded through.
type SandboxPortDialer interface {
	DialPort(ctx context.Context, port int) (net.Conn, error)
}

// DialPort implements SandboxPortDialer: a host directory shares this
// host's network, so its port is this host's loopback.
func (s *hostSandbox) DialPort(ctx context.Context, port int) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
}

// guestDialScript connects stdio to 127.0.0.1:$1 inside a guest: grain's
// own forwarder where the image carries it, nc where it does not.
const guestDialScript = `if command -v grain-connect >/dev/null 2>&1; then exec grain-connect "127.0.0.1:$1"; fi; exec nc 127.0.0.1 "$1"`

// DialPort implements SandboxPortDialer for a VM, the way a serving
// sandbox is dialled: one exec per connection, its stdio the stream.
func (s *granuleSandbox) DialPort(ctx context.Context, port int) (net.Conn, error) {
	return dialGuestPort(ctx, s.runner, s.name, port)
}

// DialPort implements SandboxPortDialer for a Kubernetes granule pod.
func (s *k8sGranuleSandbox) DialPort(ctx context.Context, port int) (net.Conn, error) {
	return dialGuestPort(ctx, s.runner, s.name, port)
}

// DialPort implements SandboxPortDialer for a Kubernetes sandbox pod.
func (s *k8sSandbox) DialPort(ctx context.Context, port int) (net.Conn, error) {
	return dialGuestPort(ctx, s.runner, s.name, port)
}

// DialPort implements SandboxPortDialer for a Docker sandbox container.
func (s *containerSandbox) DialPort(ctx context.Context, port int) (net.Conn, error) {
	return dialGuestPort(ctx, s.runner, s.name, port)
}

func dialGuestPort(ctx context.Context, runner sandboxRunner, name string, port int) (net.Conn, error) {
	dialer, ok := runner.(streamRunner)
	if !ok {
		return nil, fmt.Errorf("orchestrator: sandbox %q's transport cannot open a connection into the guest", name)
	}
	return dialer.DialStream(ctx, []string{"sh", "-c", guestDialScript, "sh", fmt.Sprint(port)})
}

// PersistentPortForwarder binds the host ports persistent sandboxes ask
// for (pkg/tunnel/hostport's Forwarder). Sync is given the whole wanted
// set every time and opens, keeps or closes listeners to match it.
type PersistentPortForwarder interface {
	Sync(ports map[int]func(ctx context.Context) (net.Conn, error)) map[int]error
}

// PersistentSandboxSupervisor keeps every enabled persistent sandbox
// running. The zero value is not usable; NewPersistentSandboxSupervisor.
type PersistentSandboxSupervisor struct {
	// Ports binds host ports; nil binds none, and a sandbox asking for
	// one says so in its LastError.
	Ports PersistentPortForwarder
	// ReservedPorts are the ports grain itself listens on, which no
	// persistent sandbox may take.
	ReservedPorts []int
	// RepoURL, when set, is the clone URL for a repository, overriding
	// the git proxy address built from Config.GitRemoteBase -- for tests
	// cloning a local repository, and deployments with no proxy.
	RepoURL func(repo string) string
	// GrantRead and DropRead give and take back a sandbox's fetch-only
	// git proxy scope on its one repository (gitproxy.ReadScopes, the
	// registry CI jobs use). Nil grants nothing.
	GrantRead func(sandbox string, repo model.RepoRef)
	DropRead  func(sandbox string)
	// Head and Verdict, when set, replace how the refresh policy reads a
	// ref's head and grain CI's verdict on a commit (persistentDeps'
	// head and verdict) -- for tests.
	Head    func(ctx context.Context, repo, ref string) (string, error)
	Verdict func(ctx context.Context, repo, branch, commit string) string

	mu      sync.Mutex
	entries map[string]*persistentEntry
	// held are the sandboxes an operator stopped: kept stopped until
	// started again, or until this daemon restarts.
	held map[string]bool
	// portErrs is the last Sync's answer, by host port.
	portErrs map[int]error
	ledger   *model.Ledger
}

// NewPersistentSandboxSupervisor builds an empty supervisor.
func NewPersistentSandboxSupervisor() *PersistentSandboxSupervisor {
	return &PersistentSandboxSupervisor{
		entries: make(map[string]*persistentEntry),
		held:    make(map[string]bool),
	}
}

// persistentEntry is one supervised sandbox: its goroutine's controls and
// everything it reports. Fields after mu are guarded by it.
type persistentEntry struct {
	def    model.PersistentSandbox
	cancel context.CancelFunc
	done   chan struct{}
	// ctl carries operator requests to the goroutine: "restart" or
	// "rebuild". Buffered by one; a request made while one is pending is
	// the same request.
	ctl  chan string
	logs *ringBuffer

	mu          sync.Mutex
	state       string
	sandbox     Sandbox
	startedAt   time.Time
	restarts    int
	lastError   string
	nextAttempt time.Time
	events      []model.PersistentSandboxEvent
	cmdCancel   context.CancelFunc
	// commit is the commit the checkout is at, as the last checkout
	// reported it.
	commit string
	// target is the commit the next checkout moves to instead of Ref's
	// fetched tip: a refresh's to-sha, kept for good under after_green so
	// that no restart moves past the last green commit.
	target string
	// refresh is the refresh policy's own state (persistent_refresh.go).
	refresh persistentRefreshState
	// packageCache is PrepareSandbox's summary for the sandbox held now.
	packageCache string
}

func (e *persistentEntry) event(kind, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("orchestrator: persistent sandbox %q: %s", e.def.Name, msg)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, model.PersistentSandboxEvent{At: time.Now(), Kind: kind, Message: msg})
	if len(e.events) > persistentEventCount {
		e.events = append([]model.PersistentSandboxEvent(nil), e.events[len(e.events)-persistentEventCount:]...)
	}
}

func (e *persistentEntry) set(fn func(e *persistentEntry)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn(e)
}

// persistentDeps is the part of Deps a supervising goroutine keeps: it
// outlives the reconcile that started it, so it is given values rather
// than the Deps of one tick.
type persistentDeps struct {
	sandboxes  Sandboxes
	meter      SandboxMeter
	remoteBase string
	mint       func(sandbox string) (string, error)
	revoke     func(sandbox string) error
	// head reads a ref's head from the forge, "" where it cannot say
	// (no client, a ref that is not a branch); the refresh policy then
	// asks the sandbox's own remote (persistentLsRemote).
	head func(ctx context.Context, repo, ref string) (string, error)
	// verdict is grain CI's conclusion on commit of repo's branch, ""
	// while there is none (persistentCIVerdict).
	verdict func(ctx context.Context, repo, branch, commit string) string
	// cache is the deployment's package cache, which every sandbox built
	// here is pointed at before anything runs in it (PrepareSandbox).
	cache PackageCacheSource
}

func persistentSandboxCommitment(sb model.PersistentSandbox, defaultShape model.Commitment, sandboxes Sandboxes) model.Commitment {
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

// reconcilePersistentSandboxes is the "persistent-sandboxes" reconcile
// step.
func reconcilePersistentSandboxes(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil || deps.PersistentSandboxes == nil {
		return nil
	}
	list, err := deps.Store.ListPersistentSandboxes(ctx)
	if err != nil {
		return fmt.Errorf("listing persistent sandboxes: %w", err)
	}
	deps.PersistentSandboxes.Reconcile(ctx, deps, list)
	return nil
}

// Reconcile starts a goroutine for every enabled definition that has
// none, replaces the goroutine of every definition that changed, stops
// those of definitions removed or disabled, and rebinds host ports.
//
// Enabled persistent sandboxes reserve their configured CPU and memory
// share up front in deps.Ledger (PoolPersistent). When the deployment's
// budget is already full, a newly enabled persistent sandbox waits for
// room rather than pushing committed capacity past the budget cap.
func (s *PersistentSandboxSupervisor) Reconcile(ctx context.Context, deps Deps, desired []model.PersistentSandbox) {
	pd := persistentDeps{
		sandboxes:  deps.Sandboxes,
		meter:      MeterFor(deps.Store),
		remoteBase: deps.Config.GitRemoteBase,
		mint:       deps.MintSandboxToken,
		revoke:     deps.RevokeSandboxToken,
		cache:      deps.Config.PackageCacheSource(),
		head:       persistentForgeHead(deps),
		verdict:    persistentCIVerdict(deps),
	}
	if s.Head != nil {
		pd.head = s.Head
	}
	if s.Verdict != nil {
		pd.verdict = s.Verdict
	}
	wanted := make(map[string]model.PersistentSandbox, len(desired))
	for _, sb := range desired {
		if err := model.ValidatePersistentSandbox(sb); err != nil {
			log.Printf("orchestrator: %v; not running it", err)
			continue
		}
		wanted[sb.Name] = sb
	}

	s.mu.Lock()
	if deps.Ledger != nil {
		s.ledger = deps.Ledger
	}
	var stop []*persistentEntry
	for name, e := range s.entries {
		sb, ok := wanted[name]
		if !ok || !sb.Enabled || s.held[name] || !reflect.DeepEqual(sb, e.def) {
			stop = append(stop, e)
			delete(s.entries, name)
		}
	}
	for name := range s.held {
		if _, ok := wanted[name]; !ok {
			delete(s.held, name)
		}
	}
	s.mu.Unlock()
	for _, e := range stop {
		e.stop()
	}

	reserved := make(map[string]model.Commitment, len(wanted))
	s.mu.Lock()
	for name, e := range s.entries {
		id := persistentSandboxName(name)
		reserved[id] = persistentSandboxCommitment(e.def, deps.AgentShape, deps.Sandboxes)
	}
	if deps.Ledger != nil {
		deps.Ledger.Reserve(model.PoolPersistent, reserved)
	}
	names := make([]string, 0, len(wanted))
	for name := range wanted {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sb := wanted[name]
		if !sb.Enabled || s.held[name] || s.entries[name] != nil {
			continue
		}
		id := persistentSandboxName(name)
		want := persistentSandboxCommitment(sb, deps.AgentShape, deps.Sandboxes)
		if deps.Budget.Known() && deps.Ledger != nil {
			verdict, _ := deps.Ledger.Admit(deps.Budget, model.PoolPersistent, id, want)
			if !verdict.OK {
				log.Printf("orchestrator: persistent sandbox %q waiting for sandbox budget: %s", name, verdict.Reason)
				continue
			}
		}
		reserved[id] = want
		s.entries[name] = s.launch(ctx, pd, sb)
	}
	if deps.Ledger != nil {
		deps.Ledger.Reserve(model.PoolPersistent, reserved)
	}
	s.mu.Unlock()

	s.syncPorts(desired)
}

// syncPorts binds every enabled sandbox's host port that collides with
// nothing, and records why any other was not bound.
func (s *PersistentSandboxSupervisor) syncPorts(desired []model.PersistentSandbox) {
	if s.Ports == nil {
		return
	}
	reserved := make(map[int]bool, len(s.ReservedPorts))
	for _, p := range s.ReservedPorts {
		reserved[p] = true
	}
	ports := make(map[int]func(ctx context.Context) (net.Conn, error))
	errs := make(map[int]error)
	for _, sb := range desired {
		if sb.HostPort == 0 || !sb.Enabled || model.ValidatePersistentSandbox(sb) != nil {
			continue
		}
		switch {
		case reserved[sb.HostPort]:
			errs[sb.HostPort] = fmt.Errorf("host port %d is one grain itself listens on", sb.HostPort)
		case ports[sb.HostPort] != nil:
			errs[sb.HostPort] = fmt.Errorf("host port %d is claimed by more than one persistent sandbox", sb.HostPort)
			delete(ports, sb.HostPort)
		case errs[sb.HostPort] == nil:
			name := sb.Name
			ports[sb.HostPort] = func(ctx context.Context) (net.Conn, error) { return s.Dial(ctx, name) }
		}
	}
	for port, err := range s.Ports.Sync(ports) {
		errs[port] = err
	}
	s.mu.Lock()
	s.portErrs = errs
	s.mu.Unlock()
}

// launch starts sb's goroutine. Called with s.mu held.
func (s *PersistentSandboxSupervisor) launch(ctx context.Context, pd persistentDeps, sb model.PersistentSandbox) *persistentEntry {
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e := &persistentEntry{
		def:    sb,
		cancel: cancel,
		done:   make(chan struct{}),
		ctl:    make(chan string, 1),
		logs:   newRingBuffer(persistentLogBytes),
		state:  PersistentStarting,
	}
	watching := make(chan struct{})
	go func() {
		defer close(e.done)
		defer func() { <-watching }()
		s.supervise(runCtx, pd, e)
	}()
	go func() {
		defer close(watching)
		s.watchRefresh(runCtx, pd, e)
	}()
	return e
}

// stop ends e's goroutine and waits for it to give its sandbox back.
func (e *persistentEntry) stop() {
	e.cancel()
	<-e.done
}

// supervise is one sandbox's whole life: (re)build, run, record, back
// off, repeat, until ctx ends.
func (s *PersistentSandboxSupervisor) supervise(ctx context.Context, pd persistentDeps, e *persistentEntry) {
	name := persistentSandboxName(e.def.Name)
	var sandbox Sandbox
	var sandboxCounted func(context.Context)
	release := func() {
		if sandbox == nil {
			return
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), acquireCleanupTimeout)
		if err := sandbox.Release(rctx); err != nil {
			log.Printf("orchestrator: releasing persistent sandbox %q: %v", e.def.Name, err)
		}
		if sandboxCounted != nil {
			sandboxCounted(rctx)
			sandboxCounted = nil
		}
		cancel()
		if s.DropRead != nil {
			s.DropRead(name)
		}
		if pd.revoke != nil {
			_ = pd.revoke(name)
		}
		sandbox = nil
		e.set(func(e *persistentEntry) { e.sandbox, e.packageCache = nil, "" })
	}
	defer func() {
		release()
		e.set(func(e *persistentEntry) { e.state = PersistentStopped })
	}()

	failures := 0
	fresh := true
	for ctx.Err() == nil {
		if sandbox == nil {
			sb, counted, err := s.build(ctx, pd, e, name)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				failures++
				e.event("error", "building the sandbox: %v", err)
				e.set(func(e *persistentEntry) { e.lastError = err.Error() })
				if !s.wait(ctx, e, failures) {
					return
				}
				continue
			}
			sandbox, sandboxCounted, fresh = sb, counted, true
			e.set(func(e *persistentEntry) { e.sandbox = sb })
		}
		shell := sandbox.(SandboxShell)
		e.mu.Lock()
		pending := e.target != "" && e.target != e.commit
		e.mu.Unlock()
		if !fresh && (e.def.AutoUpdate || pending) {
			if err := s.checkout(ctx, shell, e, pd, false); err != nil && ctx.Err() == nil {
				e.event("error", "updating the checkout: %v", err)
			}
		}
		fresh = false

		cmdCtx, cmdCancel := context.WithCancel(ctx)
		started := time.Now()
		e.set(func(e *persistentEntry) {
			e.state = PersistentRunning
			e.startedAt = started
			e.nextAttempt = time.Time{}
			e.cmdCancel = cmdCancel
		})
		e.event("start", "running %q", e.def.Command)
		res, err := shell.RunShell(cmdCtx, ShellRequest{
			Script: persistentCommandScript(e.def),
			Dir:    persistentDir(e.def),
			Env:    e.def.Env,
			Output: e.logs,
		})
		interrupted := cmdCtx.Err() != nil
		cmdCancel()
		if ctx.Err() != nil {
			return
		}
		up := time.Since(started)
		if up >= persistentStableAfter {
			failures = 0
		}

		var request string
		select {
		case request = <-e.ctl:
		default:
		}
		switch {
		case request == "rebuild":
			e.event("rebuild", "rebuilding the sandbox from a fresh VM and a fresh clone")
			release()
			failures = 0
		case request == "refresh" && interrupted:
			// The refresh event was recorded when it was asked for.
			failures = 0
		case interrupted:
			e.event("restart", "restarted by an operator")
			failures = 0
		case err != nil:
			// The transport failing is the sandbox gone: rebuild it.
			failures++
			e.event("sandbox-exit", "the sandbox stopped answering after %s: %v", up.Round(time.Second), err)
			e.set(func(e *persistentEntry) { e.lastError = err.Error() })
			release()
			e.noteCrash(up)
		default:
			failures++
			e.event("exit", "the command exited with status %d after %s", res.ExitCode, up.Round(time.Second))
			e.set(func(e *persistentEntry) { e.lastError = fmt.Sprintf("exited with status %d", res.ExitCode) })
			e.noteCrash(up)
		}
		e.set(func(e *persistentEntry) {
			e.restarts++
			e.state = PersistentRestarting
			e.cmdCancel = nil
		})
		if !s.wait(ctx, e, failures) {
			return
		}
	}
}

// wait sleeps out the backoff failures has earned, returning early (true)
// for an operator request and false once ctx ends.
func (s *PersistentSandboxSupervisor) wait(ctx context.Context, e *persistentEntry, failures int) bool {
	d := PersistentBackoff(failures)
	e.set(func(e *persistentEntry) {
		e.state = PersistentRestarting
		if d > 0 {
			e.nextAttempt = time.Now().Add(d)
		}
	})
	if d == 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	case req := <-e.ctl:
		// Put it back for the loop to act on after this wait.
		if req == "rebuild" {
			select {
			case e.ctl <- req:
			default:
			}
		}
		return true
	}
}

// build acquires a sandbox, points it at the deployment's package cache
// and its git at the proxy, and checks the repository out in it.
func (s *PersistentSandboxSupervisor) build(ctx context.Context, pd persistentDeps, e *persistentEntry, name string) (Sandbox, func(context.Context), error) {
	if pd.sandboxes == nil {
		return nil, nil, errors.New("this deployment has no sandbox backend")
	}
	e.set(func(e *persistentEntry) { e.state = PersistentStarting })
	e.event("build", "acquiring sandbox %s", name)
	sb, counted, err := pd.meter.Acquire(ctx, pd.sandboxes, model.SandboxKindAgent, name, Shape{
		CPUs: e.def.CPUs, MemoryMB: e.def.MemoryMB, DiskGB: e.def.DiskGB, NetworkAccess: e.def.NetworkAccess,
		Class: SandboxClassAgent,
	})
	if err != nil {
		return nil, nil, err
	}
	releaseOnFail := func() {
		rctx := context.WithoutCancel(ctx)
		_ = sb.Release(rctx)
		if counted != nil {
			counted(rctx)
		}
	}
	shell, ok := sb.(SandboxShell)
	if !ok {
		releaseOnFail()
		return nil, nil, errors.New("this deployment's sandbox backend cannot run a command outside an agent run")
	}
	// Before the checkout and the command, exactly as a task's dispatch
	// does (applyPackageCache): on a deployment whose sandboxes reach the
	// registries only through the cache, its CA and its resolver, a
	// sandbox built without them cannot fetch a package at all.
	prep := PrepareSandbox(ctx, pd.cache, sb)
	summary := "its sandbox " + prep.Summary()
	e.set(func(e *persistentEntry) { e.packageCache = summary })
	e.event("package-cache", "%s", summary)
	if s.GrantRead != nil {
		owner, repo, _ := strings.Cut(e.def.Repo, "/")
		s.GrantRead(name, model.RepoRef{Owner: owner, Name: repo})
	}
	if pd.mint != nil && s.RepoURL == nil {
		token, err := pd.mint(name)
		if err == nil {
			err = sb.ConfigureGitCredentials(ctx, pd.remoteBase+"/placeholder/placeholder.git", token, mcp.GitIdentity{})
		}
		if err != nil {
			releaseOnFail()
			return nil, nil, fmt.Errorf("configuring git credentials: %w", err)
		}
	}
	s.pinGreenHead(ctx, pd, e)
	if err := s.checkout(ctx, shell, e, pd, true); err != nil {
		releaseOnFail()
		return nil, nil, err
	}
	return sb, counted, nil
}

// checkout clones the repository on a fresh sandbox (or fetches it again
// on a restart, for AutoUpdate) and checks Ref out detached.
func (s *PersistentSandboxSupervisor) checkout(ctx context.Context, shell SandboxShell, e *persistentEntry, pd persistentDeps, fresh bool) error {
	url := ""
	switch {
	case s.RepoURL != nil:
		url = s.RepoURL(e.def.Repo)
	case pd.remoteBase != "":
		url = strings.TrimSuffix(pd.remoteBase, "/") + "/" + e.def.Repo + ".git"
	default:
		url = "https://github.com/" + e.def.Repo + ".git"
	}
	ref := e.def.Ref
	if ref == "" {
		ref = "HEAD"
	}
	// A target is checked out by its own sha rather than as FETCH_HEAD,
	// so a push landing between the verdict and this fetch cannot slip
	// an unverified commit in; it is fetched by sha only if the ref's
	// history no longer has it.
	e.mu.Lock()
	target := e.target
	e.mu.Unlock()
	want := "FETCH_HEAD"
	if target != "" {
		want = target
	}
	script := fmt.Sprintf(`mkdir -p %[1]s
cd %[1]s
if [ ! -d .git ]; then git init -q . && git remote add origin %[2]s; fi
git remote set-url origin %[2]s
git fetch -q origin %[3]s
if [ %[4]s != FETCH_HEAD ] && ! git cat-file -e %[4]s^{commit} 2>/dev/null; then git fetch -q origin %[4]s; fi
git checkout -q --detach %[4]s
git log -1 --format='checked out %%h %%s'
git rev-parse HEAD`, persistentCheckout, shellQuote(url), shellQuote(ref), shellQuote(want))
	res, err := shell.RunShell(ctx, ShellRequest{Script: script, Timeout: 10 * time.Minute})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("checking out %s at %s: exit %d: %s", e.def.Repo, want, res.ExitCode, strings.TrimSpace(res.Output))
	}
	out := strings.TrimSpace(res.Output)
	said, commit := out, ""
	if i := strings.LastIndex(out, "\n"); i >= 0 {
		said, commit = strings.TrimSpace(out[:i]), strings.TrimSpace(out[i+1:])
	}
	e.set(func(e *persistentEntry) {
		e.commit = commit
		// Only after_green stays on the commit it was refreshed to; any
		// other policy's next restart fetches the ref as it always has.
		if policy, _ := persistentRefreshPolicy(e.def); policy != model.PersistentRefreshAfterGreen {
			e.target = ""
		}
	})
	e.event("checkout", "%s", said)
	return nil
}

// persistentDir is the command's working directory, relative to the
// sandbox workspace.
func persistentDir(def model.PersistentSandbox) string {
	if d := strings.Trim(def.Dir, "/"); d != "" {
		return persistentCheckout + "/" + d
	}
	return persistentCheckout
}

// persistentCommandScript runs the command so that a restart can find
// and end the one before it, which on a VM outlives the exec that was
// interrupted: its pid is kept in a file, and anything still running
// under it is killed first.
func persistentCommandScript(def model.PersistentSandbox) string {
	pidfile := "/tmp/grain-persistent-" + def.Name + ".pid"
	return fmt.Sprintf(`if [ -f %[1]s ]; then old=$(cat %[1]s); pkill -TERM -P "$old" 2>/dev/null || true; kill -TERM "$old" 2>/dev/null || true; fi
echo $$ > %[1]s
exec bash -c %[2]s`, pidfile, shellQuote(def.Command))
}

// Dial opens a connection to name's sandbox port.
func (s *PersistentSandboxSupervisor) Dial(ctx context.Context, name string) (net.Conn, error) {
	s.mu.Lock()
	e := s.entries[name]
	s.mu.Unlock()
	if e == nil {
		return nil, fmt.Errorf("persistent sandbox %q is not running", name)
	}
	e.mu.Lock()
	sb, port := e.sandbox, e.def.SandboxPort
	e.mu.Unlock()
	if port == 0 {
		return nil, fmt.Errorf("persistent sandbox %q exposes no port", name)
	}
	if sb == nil {
		return nil, fmt.Errorf("persistent sandbox %q is starting", name)
	}
	dialer, ok := sb.(SandboxPortDialer)
	if !ok {
		return nil, fmt.Errorf("persistent sandbox %q: this deployment's sandbox backend cannot forward a port", name)
	}
	return dialer.DialPort(ctx, port)
}

// StripPrefix reports whether name's UI sub-path is stripped before
// forwarding, and whether name is served under the UI at all.
func (s *PersistentSandboxSupervisor) StripPrefix(name string) (strip, served bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[name]
	if e == nil || e.def.UISubpath == "" {
		return false, false
	}
	return e.def.StripPrefix, true
}

// List reports every supervised and every stopped sandbox, by name.
func (s *PersistentSandboxSupervisor) List(defs []model.PersistentSandbox) []model.PersistentSandboxStatus {
	s.mu.Lock()
	entries := make(map[string]*persistentEntry, len(s.entries))
	for k, v := range s.entries {
		entries[k] = v
	}
	portErrs := s.portErrs
	s.mu.Unlock()
	out := make([]model.PersistentSandboxStatus, 0, len(defs))
	for _, def := range defs {
		st := model.PersistentSandboxStatus{
			Name: def.Name, Repo: def.Repo, Ref: def.Ref, Command: def.Command, Enabled: def.Enabled,
			State: PersistentStopped, Sandbox: persistentSandboxName(def.Name),
			SandboxPort: def.SandboxPort, HostPort: def.HostPort, UISubpath: def.UISubpath,
			CPUs: def.CPUs, MemoryMB: def.MemoryMB, DiskGB: def.DiskGB,
			Refresh: def.Refresh,
		}
		if st.Refresh == "" {
			st.Refresh = model.PersistentRefreshManual
		}
		if e := entries[def.Name]; e != nil {
			e.mu.Lock()
			st.State, st.StartedAt, st.Restarts = e.state, e.startedAt, e.restarts
			st.LastError, st.NextAttempt = e.lastError, e.nextAttempt
			st.PackageCache = e.packageCache
			st.Events = append([]model.PersistentSandboxEvent(nil), e.events...)
			st.Commit = e.commit
			if r := e.refresh.last; r != nil {
				last := *r
				st.LastRefresh = &last
			}
			e.mu.Unlock()
		}
		if err := portErrs[def.HostPort]; def.HostPort != 0 && err != nil {
			st.LastError = strings.TrimSpace(st.LastError + "; " + err.Error())
			st.LastError = strings.TrimPrefix(st.LastError, "; ")
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Logs is the tail of name's command output.
func (s *PersistentSandboxSupervisor) Logs(name string) (string, bool) {
	s.mu.Lock()
	e := s.entries[name]
	s.mu.Unlock()
	if e == nil {
		return "", false
	}
	return e.logs.String(), true
}

// ErrPersistentNotRunning is returned by a control on a sandbox that is
// not running.
var ErrPersistentNotRunning = errors.New("persistent sandbox is not running")

// Restart ends name's command; the supervisor starts it again at once.
func (s *PersistentSandboxSupervisor) Restart(name string) error { return s.control(name, "restart") }

// Rebuild throws name's sandbox away and builds it again from a fresh VM
// and a fresh clone.
func (s *PersistentSandboxSupervisor) Rebuild(name string) error { return s.control(name, "rebuild") }

func (s *PersistentSandboxSupervisor) control(name, req string) error {
	s.mu.Lock()
	e := s.entries[name]
	s.mu.Unlock()
	if e == nil {
		return ErrPersistentNotRunning
	}
	e.request(req)
	return nil
}

// request hands req to e's goroutine and ends the command it is running.
func (e *persistentEntry) request(req string) {
	select {
	case e.ctl <- req:
	default:
	}
	e.mu.Lock()
	cancel := e.cmdCancel
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Stop stops name and keeps it stopped until Start, or until the daemon
// restarts (a restart starts every enabled sandbox).
func (s *PersistentSandboxSupervisor) Stop(name string) {
	s.mu.Lock()
	s.held[name] = true
	e := s.entries[name]
	delete(s.entries, name)
	ledger := s.ledger
	s.mu.Unlock()
	if e != nil {
		e.event("stop", "stopped by an operator")
		e.stop()
	}
	if ledger != nil {
		ledger.Release(model.PoolPersistent, persistentSandboxName(name))
	}
}

// Start undoes Stop; the next reconcile starts the sandbox again.
func (s *PersistentSandboxSupervisor) Start(name string) {
	s.mu.Lock()
	delete(s.held, name)
	s.mu.Unlock()
}

// Close stops every sandbox, giving each one's VM back.
func (s *PersistentSandboxSupervisor) Close() {
	s.mu.Lock()
	entries := s.entries
	s.entries = make(map[string]*persistentEntry)
	ledger := s.ledger
	s.mu.Unlock()
	for _, e := range entries {
		e.stop()
	}
	if ledger != nil {
		ledger.Reserve(model.PoolPersistent, nil)
	}
}

// ringBuffer keeps the last max bytes written to it.
type ringBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newRingBuffer(max int) *ringBuffer { return &ringBuffer{max: max} }

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if over := len(r.buf) - r.max; over > 0 {
		r.buf = append([]byte(nil), r.buf[over:]...)
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}
