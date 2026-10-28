package demo

// The observability demo is the one executable fixture behind
// ui/e2e/observability.spec.js. Ordinary `grain demo` deliberately has no
// orchestrator or CI engine; this opt-in scenario gives the real UI server a
// real CI service, a real on-disk job cache, and real host-directory
// sandboxes. Its three /api/e2e controls only pace that lifecycle so the
// browser can observe each state instead of racing a short shell command.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const observabilityCommit = "0123456789abcdef0123456789abcdef01234567"

func configureObservabilityDemo(ctx context.Context, dir string, store *model.Store, cfg ui.Config) (ui.Config, *demoObservabilityBackend, error) {
	workflow, err := ci.Parse(".grain/workflows/observability.yml", []byte(`name: Observability
on: workflow_dispatch
jobs:
  observe:
    steps:
      - name: Wait for the browser
        run: |
          until test -f .finish; do
            sleep 0.1
          done
          printf 'observed through the sandbox\n'
`))
	if err != nil {
		return cfg, nil, err
	}
	cache, err := ci.OpenDirCache(filepath.Join(dir, "ci-cache"), ci.DirCacheOptions{})
	if err != nil {
		return cfg, nil, err
	}
	sandboxDir := filepath.Join(dir, "sandboxes")
	if err := os.MkdirAll(sandboxDir, 0o755); err != nil {
		return cfg, nil, err
	}
	demoLedger := &model.Ledger{}
	demoBudget := model.NewBudget(model.Commitment{CPUs: 4, MemoryMB: 16384}, 2048, model.DefaultCPUOvercommit, model.DefaultMemoryOvercommit)
	backend := newDemoObservabilityBackend(sandboxDir, demoLedger)
	service := &ci.Service{
		Source: ci.SourceFunc(func(context.Context, string, string) ([]ci.Workflow, string, error) {
			return []ci.Workflow{workflow}, observabilityCommit, nil
		}),
		Engine: &ci.Engine{
			Sandboxes:   demoObservabilityCISandboxes{backend: backend},
			Cache:       cache,
			Concurrency: 1,
		},
	}
	cfg.CI = service
	cfg.Sandboxes = daemon.SandboxHealthAdapter{
		Inner: backend,
		Limits: func() model.Limits {
			return model.Limits{
				Workers:    4,
				Budget:     demoBudget,
				AgentShape: model.Commitment{CPUs: 2, MemoryMB: 2048},
				Ledger:     demoLedger,
			}
		},
	}
	cfg.HostStats = func() (ui.HostPressure, error) {
		return ui.HostPressure{
			CPUs:           4,
			LoadAverage1:   0.45,
			LoadAverage5:   0.55,
			LoadAverage15:  0.60,
			CPUUtilization: 0.15,
			MemoryUsedMB:   4096,
			MemoryTotalMB:  16384,
			SwapUsedMB:     0,
			SwapTotalMB:    4096,
		}, nil
	}
	cfg.SandboxHostCapacity = func() (ui.HostCapacity, error) {
		return ui.HostCapacity{CPUs: 4, MemoryMB: 16384}, nil
	}
	cfg.Overcommit = func() model.OvercommitState {
		c, _ := store.GetConfig(ctx)
		r := c.SandboxOvercommit.Resolved()
		cpuRatio, memRatio := c.SandboxCPUOvercommit, c.SandboxMemoryOvercommit
		if r.Mode == model.OvercommitAuto {
			cpuRatio, memRatio = 2.75, 1.25
		}
		return model.OvercommitState{
			Mode:             r.Mode,
			Settings:         r,
			CPUOvercommit:    cpuRatio,
			MemoryOvercommit: memRatio,
			Last: &model.OvercommitSignals{
				CPUSome:    8.4,
				MemorySome: 1.9,
			},
			Adjustments: []model.OvercommitAdjustment{
				{
					At:        time.Date(2026, 10, 24, 9, 20, 0, 0, time.UTC),
					Dimension: "cpu",
					From:      2.5,
					To:        2.75,
					Reason:    "CPU PSI 8.4% under 12.5% calm band for 4 samples",
				},
			},
		}
	}
	cfg.PackageCache = newDemoPackageCache()
	cfg.ActionsCache = newDemoActionsCache()
	// The operations panes the rest of this scenario is watched through.
	// A real secrets store in the throwaway directory, so the Admin
	// secrets panel sets, lists and deletes through the real handlers;
	// a fixed `top` snapshot, for the same reason HostStats above is
	// fixed (a demo has no host of its own worth reading); and power
	// hooks that refuse rather than act, so the Power tab shows the
	// buttons a deployment that can restart itself does without this
	// process ever being able to power-cycle the machine it runs on.
	cfg.Secrets = secrets.New(filepath.Join(dir, "secrets-store"))
	cfg.HostTop = func(_ context.Context, lines int) ([]string, error) {
		top := demoTopLines
		if lines > 0 && lines < len(top) {
			top = top[:lines]
		}
		return top, nil
	}
	cfg.Restart = refuseDemoPower("restart itself")
	cfg.Reboot = refuseDemoPower("reboot its host")
	cfg.Shutdown = refuseDemoPower("shut its host down")

	stored, err := store.GetConfig(ctx)
	if err != nil {
		return cfg, nil, err
	}
	if stored == nil {
		defaults := model.DefaultConfig()
		stored = &defaults
	}
	stored.SandboxOvercommit = model.OvercommitSettings{Mode: model.OvercommitAuto}
	if err := store.PutConfig(ctx, *stored); err != nil {
		return cfg, nil, err
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:   repo,
		CIMode: model.CIModeGrain,
	}); err != nil {
		return cfg, nil, err
	}
	return cfg, backend, nil
}

// demoTopLines is the observability demo's `top` snapshot: a header and
// the processes a grain host is usually running.
var demoTopLines = []string{
	"PID  USER      PR  NI    VIRT    RES    SHR S  %CPU  %MEM     TIME+ COMMAND",
	"1    root      20   0   21.4M   9.2M   7.1M S   0.0   0.1   0:01.20 systemd",
	"4242 debian    20   0  712.0M 128.0M  42.0M S   2.1   1.8   0:15.42 grain-daemon",
	"5120 debian    20   0   45.2M  18.4M  12.1M S   0.5   0.2   0:02.10 node",
}

// refuseDemoPower is a ui.Config power hook that never acts: the demo
// offers the Power tab's buttons, and pressing one is an error that says
// why nothing happened.
func refuseDemoPower(what string) func(context.Context) error {
	return func(context.Context) error {
		return fmt.Errorf("grain demo does not %s: nothing here is real", what)
	}
}

// demoObservabilityCISandboxes adapts the real HostSandboxes backend without a
// git checkout. The fixture's workflow is supplied by SourceFunc and needs no
// repository contents, but its step still runs through SandboxShell inside the
// backend-owned directory, exactly as an ordinary CI step does.
type demoObservabilityCISandboxes struct {
	backend *demoObservabilityBackend
}

func (d demoObservabilityCISandboxes) Acquire(ctx context.Context, req ci.SandboxRequest) (ci.Sandbox, error) {
	backing, err := d.backend.Acquire(ctx, req.Name, orchestrator.Shape{})
	if err != nil {
		return nil, err
	}
	rooted, ok := backing.(interface{ Root() (string, error) })
	if !ok {
		_ = backing.Release(context.Background())
		return nil, fmt.Errorf("observability sandbox %s has no host root", backing.Name())
	}
	root, err := rooted.Root()
	if err != nil {
		_ = backing.Release(context.Background())
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, orchestrator.CheckoutDir), 0o755); err != nil {
		_ = backing.Release(context.Background())
		return nil, err
	}
	shell, ok := backing.(orchestrator.SandboxShell)
	if !ok {
		_ = backing.Release(context.Background())
		return nil, fmt.Errorf("observability sandbox %s has no shell", backing.Name())
	}
	return &demoObservabilityCISandbox{backing: backing, shell: shell}, nil
}

type demoObservabilityCISandbox struct {
	backing orchestrator.Sandbox
	shell   orchestrator.SandboxShell
}

func (s *demoObservabilityCISandbox) Name() string { return s.backing.Name() }

func (s *demoObservabilityCISandbox) Exec(ctx context.Context, cmd ci.Command) (ci.CommandResult, error) {
	dir := orchestrator.CheckoutDir
	if cmd.Dir != "" {
		dir = filepath.Join(dir, cmd.Dir)
	}
	result, err := s.shell.RunShell(ctx, orchestrator.ShellRequest{
		Script:  cmd.Script,
		Dir:     dir,
		Env:     cmd.Env,
		Timeout: cmd.Timeout,
	})
	return ci.CommandResult{Output: result.Output, ExitCode: result.ExitCode}, err
}

func (s *demoObservabilityCISandbox) Release(ctx context.Context) error {
	return s.backing.Release(ctx)
}

// demoObservabilityBackend enriches HostSandboxes' real lifecycle with
// deterministic per-sandbox resource readings. HostSandboxes cannot report
// per-directory pressure (all directories share the host); the stable,
// deliberately distant series here let the browser prove that histories are
// paired by sandbox identity when a row is inserted ahead of another.
type demoObservabilityBackend struct {
	inner  *orchestrator.HostSandboxes
	ledger *model.Ledger

	mu       sync.Mutex
	roots    map[string]string
	other    orchestrator.Sandbox
	polls    int
	acquires int
}

func newDemoObservabilityBackend(dir string, ledger *model.Ledger) *demoObservabilityBackend {
	return &demoObservabilityBackend{
		inner:  orchestrator.NewHostSandboxes(dir),
		ledger: ledger,
		roots:  map[string]string{},
	}
}

// ClusterFree is a fixed reading of a three-node cluster, so the health
// pane's budget panel shows the cluster's free capacity beside the
// budget the way a Kubernetes deployment's does (grain/task-805) --
// deliberately tighter than the budget, which is the disagreement the
// line exists to show.
func (d *demoObservabilityBackend) ClusterFree(context.Context) k8s.FreeSummary {
	node := func(cpu float64, memGiB int64) k8s.Requests {
		return k8s.Requests{MilliCPU: int64(cpu * 1000), MemoryBytes: memGiB << 30, DiskBytes: 200 << 30}
	}
	return k8s.FreeSummary{
		Known: true, Nodes: 3,
		Allocatable: node(95.5*3, 360*3),
		Free:        node(6.5, 40),
		LargestFree: node(3.5, 20), LargestNode: "gke-pool-1-node-b",
		Pending: 1, ReadAt: time.Now(),
	}
}

func (d *demoObservabilityBackend) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	backing, err := d.inner.Acquire(ctx, name, shape)
	if err != nil {
		return nil, err
	}
	root, err := backing.(interface{ Root() (string, error) }).Root()
	if err != nil {
		_ = backing.Release(context.Background())
		return nil, err
	}
	observed := &demoObservedSandbox{Sandbox: backing, shell: backing.(orchestrator.SandboxShell), owner: d, root: root}
	d.mu.Lock()
	d.roots[name] = root
	d.acquires++
	d.mu.Unlock()
	if d.ledger != nil {
		pool := model.PoolAgent
		if strings.HasPrefix(name, "c") {
			pool = model.PoolCI
		}
		d.ledger.Charge(pool, name, model.Commitment{CPUs: 2, MemoryMB: 2048})
	}
	return observed, nil
}

func (d *demoObservabilityBackend) LiveSandboxes() []orchestrator.LiveSandbox {
	return d.inner.LiveSandboxes()
}

func (d *demoObservabilityBackend) SandboxCount() int {
	return d.inner.SandboxCount()
}

func (d *demoObservabilityBackend) Health(ctx context.Context) []orchestrator.SandboxHealth {
	snapshots := d.inner.Health(ctx)
	d.mu.Lock()
	d.polls++
	poll := d.polls
	d.mu.Unlock()
	for i := range snapshots {
		s := &snapshots[i]
		s.MemoryTotalMB = 1000
		s.DiskTotalMB = 2048
		if strings.HasPrefix(s.Sandbox, "c") {
			s.LoadAverage = fmt.Sprintf("1.%02d 0.50 0.25", poll%100)
			s.MemoryUsedMB = 100 + poll
			s.DiskUsedMB = 200 + poll
		} else {
			s.LoadAverage = fmt.Sprintf("9.%02d 4.50 2.25", poll%100)
			s.MemoryUsedMB = 800 + poll
			s.DiskUsedMB = 1200 + poll
		}
	}
	return snapshots
}

func (d *demoObservabilityBackend) finishCI() error {
	d.mu.Lock()
	roots := make([]string, 0, len(d.roots))
	for name, root := range d.roots {
		if strings.HasPrefix(name, "c") {
			roots = append(roots, root)
		}
	}
	d.mu.Unlock()
	if len(roots) == 0 {
		return fmt.Errorf("no CI sandbox is live")
	}
	for _, root := range roots {
		if err := os.WriteFile(filepath.Join(root, orchestrator.CheckoutDir, ".finish"), []byte("finish\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (d *demoObservabilityBackend) addOther(ctx context.Context) error {
	d.mu.Lock()
	if d.other != nil {
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()
	other, err := d.Acquire(ctx, "1-identity-check", orchestrator.Shape{})
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.other = other
	d.mu.Unlock()
	return nil
}

func (d *demoObservabilityBackend) releaseOther(ctx context.Context) error {
	d.mu.Lock()
	other := d.other
	d.other = nil
	d.mu.Unlock()
	if other == nil {
		return nil
	}
	return other.Release(ctx)
}

func (d *demoObservabilityBackend) acquireCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.acquires
}

type demoObservedSandbox struct {
	orchestrator.Sandbox
	shell orchestrator.SandboxShell
	owner *demoObservabilityBackend
	root  string
	once  sync.Once
	err   error
}

func (s *demoObservedSandbox) Root() (string, error) { return s.root, nil }

func (s *demoObservedSandbox) RunShell(ctx context.Context, req orchestrator.ShellRequest) (orchestrator.ShellResult, error) {
	return s.shell.RunShell(ctx, req)
}

func (s *demoObservedSandbox) Release(ctx context.Context) error {
	s.once.Do(func() {
		s.owner.mu.Lock()
		delete(s.owner.roots, s.Name())
		s.owner.mu.Unlock()
		if s.owner.ledger != nil {
			pool := model.PoolAgent
			if strings.HasPrefix(s.Name(), "c") {
				pool = model.PoolCI
			}
			s.owner.ledger.Release(pool, s.Name())
		}
		s.err = s.Sandbox.Release(ctx)
	})
	return s.err
}

func observabilityDemoHandler(app http.Handler, backend *demoObservabilityBackend) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/e2e/ci/finish":
			err = backend.finishCI()
		case r.Method == http.MethodPost && r.URL.Path == "/api/e2e/sandboxes/other":
			err = backend.addOther(r.Context())
		case r.Method == http.MethodDelete && r.URL.Path == "/api/e2e/sandboxes/other":
			err = backend.releaseOther(r.Context())
		case r.Method == http.MethodGet && r.URL.Path == "/api/e2e/sandboxes/acquires":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int{"acquires": backend.acquireCount()})
			return
		default:
			app.ServeHTTP(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

type demoPackageCache struct {
	mu    sync.Mutex
	stats []ui.PackageCacheStats
}

func newDemoPackageCache() *demoPackageCache {
	return &demoPackageCache{
		stats: []ui.PackageCacheStats{
			{Kind: "apt", Entries: 412, Bytes: 3 * (1 << 30)},
			{Kind: "npm", Entries: 128, Bytes: 450 * (1 << 20)},
			{Kind: "pypi", Entries: 85, Bytes: 220 * (1 << 20)},
			{Kind: "go", Entries: 64, Bytes: 180 * (1 << 20)},
			{Kind: "docker", Entries: 12, Bytes: 1200 * (1 << 20)},
			{Kind: "playwright", Entries: 3, Bytes: 350 * (1 << 20)},
		},
	}
}

func (d *demoPackageCache) PackageCacheStats() ([]ui.PackageCacheStats, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res := make([]ui.PackageCacheStats, len(d.stats))
	copy(res, d.stats)
	return res, nil
}

func (d *demoPackageCache) PackageCacheEntryCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	total := 0
	for _, s := range d.stats {
		total += s.Entries
	}
	return total
}

func (d *demoPackageCache) FlushPackageCache(kind string) (int, int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var freedEntries int
	var freedBytes int64
	for i := range d.stats {
		if kind == "" || d.stats[i].Kind == kind {
			freedEntries += d.stats[i].Entries
			freedBytes += d.stats[i].Bytes
			d.stats[i].Entries = 0
			d.stats[i].Bytes = 0
		}
	}
	return freedEntries, freedBytes, nil
}

type demoActionsCache struct {
	mu       sync.Mutex
	repos    []ui.ActionsCacheRepoStats
	maxBytes int64
}

func newDemoActionsCache() *demoActionsCache {
	return &demoActionsCache{
		maxBytes: 20 * (1 << 30),
		repos: []ui.ActionsCacheRepoStats{
			{Repo: "acme/widgets", Bytes: 2 * (1 << 30)},
			{Repo: "acme/gadgets", Bytes: 1 * (1 << 30)},
		},
	}
}

func (d *demoActionsCache) ActionsCacheStats() (int, int64, int64, []ui.ActionsCacheRepoStats) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var totalBytes int64
	var entries int
	for _, r := range d.repos {
		totalBytes += r.Bytes
		if r.Bytes > 0 {
			entries += 10
		}
	}
	res := make([]ui.ActionsCacheRepoStats, len(d.repos))
	copy(res, d.repos)
	return entries, totalBytes, d.maxBytes, res
}

func (d *demoActionsCache) ActionsCacheEntryCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	var entries int
	for _, r := range d.repos {
		if r.Bytes > 0 {
			entries += 10
		}
	}
	return entries
}

func (d *demoActionsCache) FlushActionsCache(repo string) (int, int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var freedEntries int
	var freedBytes int64
	for i := range d.repos {
		if repo == "" || d.repos[i].Repo == repo {
			if d.repos[i].Bytes > 0 {
				freedEntries += 10
			}
			freedBytes += d.repos[i].Bytes
			d.repos[i].Bytes = 0
		}
	}
	return freedEntries, freedBytes
}
