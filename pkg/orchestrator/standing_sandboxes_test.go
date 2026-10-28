package orchestrator

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

type fakeServingSandbox struct {
	spec     ServeSpec
	released bool
}

func (f *fakeServingSandbox) DialStream(ctx context.Context, argv []string) (net.Conn, error) {
	return nil, errors.New("not implemented in fake")
}

func (f *fakeServingSandbox) Release(ctx context.Context) error {
	f.released = true
	return nil
}

func (f *fakeServingSandbox) Output(ctx context.Context) string {
	return "fake output"
}

func (f *fakeServingSandbox) Name() string {
	return f.spec.Name
}

type fakeCredentialsResolver struct {
	secrets map[string]string
}

func (f *fakeCredentialsResolver) Resolve(ctx context.Context, name string) (string, error) {
	if val, ok := f.secrets[name]; ok {
		return val, nil
	}
	return "", errors.New("secret not found: " + name)
}

func openTestStoreForOrchestrator(t *testing.T) *model.Store {
	t.Helper()
	st, _ := sqlitetest.Open(t)
	return st
}

func TestStandingSandboxSupervisorLifecycle(t *testing.T) {
	ctx := context.Background()
	st := openTestStoreForOrchestrator(t)
	now := time.Now().UTC()

	var mu sync.Mutex
	var launched []*fakeServingSandbox

	launcher := func(ctx context.Context, spec ServeSpec) (ServingSandbox, error) {
		mu.Lock()
		defer mu.Unlock()
		inst := &fakeServingSandbox{spec: spec}
		launched = append(launched, inst)
		return inst, nil
	}

	creds := &fakeCredentialsResolver{
		secrets: map[string]string{
			"db/password": "super-secret-pw",
			"api-key":     "key-12345",
		},
	}

	sup := NewStandingSandboxSupervisor(launcher)
	deps := Deps{
		Store:             st,
		StandingSandboxes: sup,
		Config: Config{
			Credentials: creds,
		},
	}

	// 1. Define a standing sandbox in grain/config (Store)
	sb := model.StandingSandbox{
		Name:          "prod-indexer",
		Image:         "docker.io/grain/indexer:v1.0",
		Command:       "/bin/indexer --port 9000",
		Dir:           "/srv/indexer",
		CPUs:          4,
		MemoryMB:      8192,
		DiskGB:        50,
		NetworkAccess: model.NetworkOpen,
		Enabled:       true,
		Env: map[string]string{
			"INDEXER_ENV": "production",
		},
		Secrets: []model.StandingSandboxSecret{
			{
				SandboxName: "prod-indexer",
				SecretName:  "db/password",
				EnvVar:      "DB_PASSWORD",
			},
			{
				SandboxName: "prod-indexer",
				SecretName:  "api-key",
				EnvVar:      "API_KEY",
			},
		},
	}
	if err := st.PutStandingSandbox(ctx, sb); err != nil {
		t.Fatalf("PutStandingSandbox: %v", err)
	}

	// 2. Initial reconcile (simulates Grain boot / system restart)
	if err := reconcileStandingSandboxes(ctx, deps, now); err != nil {
		t.Fatalf("reconcileStandingSandboxes: %v", err)
	}

	active, ok := sup.GetActive("prod-indexer")
	if !ok {
		t.Fatalf("expected prod-indexer to be active")
	}
	if active.Restarts != 0 {
		t.Errorf("expected 0 restarts, got %d", active.Restarts)
	}
	if active.Failed {
		t.Errorf("expected active to not be failed")
	}

	mu.Lock()
	if len(launched) != 1 {
		t.Fatalf("expected 1 launched sandbox, got %d", len(launched))
	}
	firstLaunch := launched[0]
	mu.Unlock()

	// Verify launcher received properly resolved secrets and Granule OCI image
	if firstLaunch.spec.Image != "docker.io/grain/indexer:v1.0" {
		t.Errorf("got image %q, want docker.io/grain/indexer:v1.0", firstLaunch.spec.Image)
	}
	if firstLaunch.spec.Env["DB_PASSWORD"] != "super-secret-pw" || firstLaunch.spec.Env["API_KEY"] != "key-12345" {
		t.Errorf("secrets not resolved into env: %+v", firstLaunch.spec.Env)
	}
	if firstLaunch.spec.Env["INDEXER_ENV"] != "production" {
		t.Errorf("literal env missing: %+v", firstLaunch.spec.Env)
	}

	// 3. Simulate failure of the sandbox instance
	sup.MarkFailed("prod-indexer", "process crashed with SIGSEGV")

	// Next reconcile cycle should detect failure and restart the sandbox
	now = now.Add(10 * time.Second)
	if err := reconcileStandingSandboxes(ctx, deps, now); err != nil {
		t.Fatalf("reconcileStandingSandboxes on restart: %v", err)
	}

	active, ok = sup.GetActive("prod-indexer")
	if !ok || active.Failed {
		t.Fatalf("expected prod-indexer to be restarted and active, got %+v", active)
	}
	if active.Restarts != 1 {
		t.Errorf("expected 1 restart, got %d", active.Restarts)
	}

	mu.Lock()
	if len(launched) != 2 {
		t.Fatalf("expected 2 launches total, got %d", len(launched))
	}
	if !firstLaunch.released {
		t.Errorf("expected first sandbox instance to be released on failure")
	}
	secondLaunch := launched[1]
	mu.Unlock()

	// 4. Update the definition in grain/config
	sb.Image = "docker.io/grain/indexer:v2.0"
	sb.Env["INDEXER_ENV"] = "production-v2"
	if err := st.PutStandingSandbox(ctx, sb); err != nil {
		t.Fatalf("PutStandingSandbox update: %v", err)
	}

	now = now.Add(10 * time.Second)
	if err := reconcileStandingSandboxes(ctx, deps, now); err != nil {
		t.Fatalf("reconcileStandingSandboxes on update: %v", err)
	}

	mu.Lock()
	if len(launched) != 3 {
		t.Fatalf("expected 3 launches total after update, got %d", len(launched))
	}
	if !secondLaunch.released {
		t.Errorf("expected second sandbox instance to be released on config update")
	}
	thirdLaunch := launched[2]
	mu.Unlock()

	if thirdLaunch.spec.Image != "docker.io/grain/indexer:v2.0" {
		t.Errorf("got image %q after update, want docker.io/grain/indexer:v2.0", thirdLaunch.spec.Image)
	}
	if thirdLaunch.spec.Env["INDEXER_ENV"] != "production-v2" {
		t.Errorf("env not updated: %+v", thirdLaunch.spec.Env)
	}

	// 5. Disable the sandbox
	sb.Enabled = false
	if err := st.PutStandingSandbox(ctx, sb); err != nil {
		t.Fatalf("PutStandingSandbox disable: %v", err)
	}

	now = now.Add(10 * time.Second)
	if err := reconcileStandingSandboxes(ctx, deps, now); err != nil {
		t.Fatalf("reconcileStandingSandboxes on disable: %v", err)
	}

	if _, ok := sup.GetActive("prod-indexer"); ok {
		t.Fatalf("expected prod-indexer to no longer be active in supervisor after being disabled")
	}
	if !thirdLaunch.released {
		t.Errorf("expected third sandbox instance to be released on disable")
	}
}

// List is a sorted copy: changing what it returned changes nothing tracked.
func TestStandingSandboxListIsASortedCopy(t *testing.T) {
	sup := NewStandingSandboxSupervisor(func(ctx context.Context, spec ServeSpec) (ServingSandbox, error) {
		if spec.Unit == "standing-b-broken" {
			return nil, errors.New("no capacity")
		}
		return &fakeServingSandbox{spec: spec}, nil
	})
	desired := []model.StandingSandbox{
		{Name: "c-svc", Image: "img-c", Enabled: true},
		{Name: "a-svc", Image: "img-a", Enabled: true},
		{Name: "b-broken", Image: "img-b", Enabled: true},
	}
	if err := sup.Reconcile(context.Background(), Deps{}, desired, time.Now()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := sup.List()
	var names []string
	for _, active := range got {
		names = append(names, active.Def.Name)
	}
	if want := []string{"a-svc", "b-broken", "c-svc"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("List names = %v, want %v", names, want)
	}
	if !got[1].Failed || got[1].LastError != "no capacity" {
		t.Errorf("b-broken = failed %v, lastError %q; want failed with the launch error", got[1].Failed, got[1].LastError)
	}
	got[0].Failed = true
	if active, _ := sup.GetActive("a-svc"); active.Failed {
		t.Error("changing List's result changed the tracked entry")
	}
}

func TestStandingSandboxSystemRestart(t *testing.T) {
	ctx := context.Background()
	st := openTestStoreForOrchestrator(t)
	now := time.Now().UTC()

	// Store already contains an enabled standing sandbox
	if err := st.PutStandingSandbox(ctx, model.StandingSandbox{
		Name:    "worker-1",
		Image:   "docker.io/grain/worker:v1",
		Command: "run-worker",
		Enabled: true,
	}); err != nil {
		t.Fatalf("PutStandingSandbox: %v", err)
	}

	var launched []ServeSpec
	launcher := func(ctx context.Context, spec ServeSpec) (ServingSandbox, error) {
		launched = append(launched, spec)
		return &fakeServingSandbox{spec: spec}, nil
	}

	// Fresh supervisor simulates newly booted daemon
	sup := NewStandingSandboxSupervisor(launcher)
	deps := Deps{
		Store:             st,
		StandingSandboxes: sup,
	}

	if err := reconcileStandingSandboxes(ctx, deps, now); err != nil {
		t.Fatalf("reconcileStandingSandboxes on fresh supervisor: %v", err)
	}

	if len(launched) != 1 {
		t.Fatalf("expected 1 sandbox started on boot, got %d", len(launched))
	}
	if launched[0].Name != standingSandboxName("worker-1") || launched[0].Unit != "standing-worker-1" || launched[0].Image != "docker.io/grain/worker:v1" {
		t.Errorf("launched spec mismatch: %+v", launched[0])
	}
}

// probedServingSandbox is a fake whose liveness probe a test controls.
type probedServingSandbox struct {
	fakeServingSandbox
	mu   sync.Mutex
	dead bool
}

func (p *probedServingSandbox) Alive(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead {
		return errors.New("unit is not active")
	}
	return nil
}

type ctxRecordingSandbox struct {
	fakeServingSandbox
	releaseErr error
}

func (c *ctxRecordingSandbox) Release(ctx context.Context) error {
	c.releaseErr = ctx.Err()
	c.released = true
	return nil
}

// An instance that dies after a good launch is found by the liveness
// probe and restarted -- but only after more than one miss in a row, so
// one dropped exec does not cost a VM.
func TestStandingSandboxProbeRestartsADeadInstance(t *testing.T) {
	ctx := context.Background()
	var launched []*probedServingSandbox
	sup := NewStandingSandboxSupervisor(func(ctx context.Context, spec ServeSpec) (ServingSandbox, error) {
		inst := &probedServingSandbox{fakeServingSandbox: fakeServingSandbox{spec: spec}}
		launched = append(launched, inst)
		return inst, nil
	})
	desired := []model.StandingSandbox{{Name: "svc", Image: "img", Command: "serve", Enabled: true}}
	reconcile := func() {
		t.Helper()
		if err := sup.Reconcile(ctx, Deps{}, desired, time.Now()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}

	reconcile()
	reconcile()
	if len(launched) != 1 {
		t.Fatalf("a live instance was relaunched: %d launches", len(launched))
	}

	launched[0].mu.Lock()
	launched[0].dead = true
	launched[0].mu.Unlock()
	reconcile()
	if len(launched) != 1 {
		t.Fatalf("one failed probe restarted the instance")
	}
	reconcile()
	if len(launched) != 2 {
		t.Fatalf("expected a restart after %d failed probes, got %d launches", standingProbeMisses, len(launched))
	}
	if !launched[0].released {
		t.Error("the dead instance was not released")
	}
	active, _ := sup.GetActive("svc")
	if active.Restarts != 1 || active.Failed {
		t.Errorf("after restart: %+v", active)
	}
}

// A first launch that fails is retried as the failure it was, and the
// restart that brings it up is counted. It used to leave no environment
// digest behind, so every retry was read as a secret rotation ("secret
// values changed; restarting instance", logged each tick against a
// definition with no secrets at all) and Restarts stayed at 0 once the
// instance came up -- seen on a real daemon in
// docs/test-runs/2026-10-04-agent-resilience-standing-sandboxes.md.
func TestStandingSandboxFailedFirstLaunchIsRetriedAsAFailure(t *testing.T) {
	ctx := context.Background()
	launches := 0
	sup := NewStandingSandboxSupervisor(func(ctx context.Context, spec ServeSpec) (ServingSandbox, error) {
		launches++
		if launches == 1 {
			return nil, errors.New("granule VM name is over the limit")
		}
		return &fakeServingSandbox{spec: spec}, nil
	})
	desired := []model.StandingSandbox{{Name: "svc", Image: "img", Command: "serve", Enabled: true,
		Env: map[string]string{"MODE": "walk"}}}
	// Each reconcile an hour after the last, past the failed launch's
	// backoff (NextAttempt), so the retry is due.
	now := time.Now()
	reconcile := func() {
		t.Helper()
		if err := sup.Reconcile(ctx, Deps{}, desired, now); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		now = now.Add(time.Hour)
	}

	reconcile()
	active, _ := sup.GetActive("svc")
	if !active.Failed || active.LastError == "" {
		t.Fatalf("after a failed first launch: %+v, want it failed with the error", active)
	}
	reconcile()
	if launches != 2 {
		t.Fatalf("launches = %d, want the failed one retried once", launches)
	}
	active, _ = sup.GetActive("svc")
	if active.Failed || active.Restarts != 1 {
		t.Errorf("after the retry: failed=%v restarts=%d, want running with 1 restart", active.Failed, active.Restarts)
	}
	reconcile()
	if launches != 2 {
		t.Errorf("launches = %d, want the running instance kept", launches)
	}
}

// A rotated secret value restarts the instance, though the definition --
// which only names the secret -- has not changed.
func TestStandingSandboxRestartsOnSecretRotation(t *testing.T) {
	ctx := context.Background()
	var launched []*fakeServingSandbox
	sup := NewStandingSandboxSupervisor(func(ctx context.Context, spec ServeSpec) (ServingSandbox, error) {
		inst := &fakeServingSandbox{spec: spec}
		launched = append(launched, inst)
		return inst, nil
	})
	creds := &fakeCredentialsResolver{secrets: map[string]string{"tok": "before"}}
	deps := Deps{Config: Config{Credentials: creds}}
	desired := []model.StandingSandbox{{
		Name: "svc", Image: "img", Command: "serve", Enabled: true,
		Secrets: []model.StandingSandboxSecret{{SandboxName: "svc", SecretName: "tok", EnvVar: "TOKEN"}},
	}}

	for i := 0; i < 2; i++ {
		if err := sup.Reconcile(ctx, deps, desired, time.Now()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	if len(launched) != 1 {
		t.Fatalf("unchanged secret relaunched the instance: %d launches", len(launched))
	}

	creds.secrets["tok"] = "rotated"
	if err := sup.Reconcile(ctx, deps, desired, time.Now()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(launched) != 2 || launched[1].spec.Env["TOKEN"] != "rotated" {
		t.Fatalf("rotation not picked up: %d launches", len(launched))
	}
	if !launched[0].released {
		t.Error("instance running on the old value was not released")
	}

	// A secret that cannot be resolved for now leaves the running
	// instance alone rather than tearing it down.
	delete(creds.secrets, "tok")
	if err := sup.Reconcile(ctx, deps, desired, time.Now()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(launched) != 2 || launched[1].released {
		t.Fatal("an unresolvable secret stopped the running instance")
	}
}

// Releases happen on a context detached from the reconcile's, so a
// shutting-down daemon still deletes the VM.
func TestStandingSandboxReleasesOnACancelledContext(t *testing.T) {
	inst := &ctxRecordingSandbox{}
	sup := NewStandingSandboxSupervisor(func(ctx context.Context, spec ServeSpec) (ServingSandbox, error) {
		return inst, nil
	})
	desired := []model.StandingSandbox{{Name: "svc", Image: "img", Command: "serve", Enabled: true}}
	if err := sup.Reconcile(context.Background(), Deps{}, desired, time.Now()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sup.Reconcile(ctx, Deps{}, nil, time.Now()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !inst.released || inst.releaseErr != nil {
		t.Fatalf("release ran on a cancelled ctx (released=%v, ctx err=%v)", inst.released, inst.releaseErr)
	}
}

// GetActive, List and MarkFailed do not wait for a launch in progress.
func TestStandingSandboxLaunchDoesNotHoldTheLock(t *testing.T) {
	entered := make(chan struct{})
	unblock := make(chan struct{})
	sup := NewStandingSandboxSupervisor(func(ctx context.Context, spec ServeSpec) (ServingSandbox, error) {
		if spec.Unit == "standing-slow" {
			close(entered)
			<-unblock
		}
		return &fakeServingSandbox{spec: spec}, nil
	})
	desired := []model.StandingSandbox{
		{Name: "fast", Image: "img", Command: "serve", Enabled: true},
		{Name: "slow", Image: "img", Command: "serve", Enabled: true},
	}
	done := make(chan error)
	go func() { done <- sup.Reconcile(context.Background(), Deps{}, desired, time.Now()) }()
	<-entered

	got := make(chan bool)
	go func() {
		_, ok := sup.GetActive("fast")
		listed := sup.List()
		sup.MarkFailed("fast", "probe")
		got <- ok && len(listed) == 1 && listed[0].Def.Name == "fast"
	}()
	select {
	case ok := <-got:
		if !ok {
			t.Error("the launch already finished was not recorded before the next began")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GetActive or List blocked behind a launch")
	}
	close(unblock)
	if err := <-done; err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

// A standing sandbox is launched under a short name derived from its
// declared one, so a name a granule VM could never carry ("g-" plus nine
// bytes at most) still starts: grain/task-1936 found "walk-standing"
// failing every reconcile with "over the 11-byte limit".
func TestStandingSandboxNameFitsAGranuleVM(t *testing.T) {
	k := &GranuleSandboxes{}
	seen := map[string]string{}
	for _, name := range []string{"walk-standing", "a", "preview-env", "a.very.long_standing-sandbox.name.indeed"} {
		got := standingSandboxName(name)
		if got != standingSandboxName(name) {
			t.Errorf("standingSandboxName(%q) is not stable", name)
		}
		if _, err := k.VMNameFor(got); err != nil {
			t.Errorf("standingSandboxName(%q) = %q: %v", name, got, err)
		}
		if other, dup := seen[got]; dup {
			t.Errorf("standingSandboxName(%q) = standingSandboxName(%q) = %q", name, other, got)
		}
		seen[got] = name
	}

	var specs []ServeSpec
	sup := NewStandingSandboxSupervisor(func(ctx context.Context, spec ServeSpec) (ServingSandbox, error) {
		specs = append(specs, spec)
		return &fakeServingSandbox{spec: spec}, nil
	})
	desired := []model.StandingSandbox{{Name: "walk-standing", Image: "img", Enabled: true}}
	if err := sup.Reconcile(context.Background(), Deps{}, desired, time.Now()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(specs) != 1 || specs[0].Name != standingSandboxName("walk-standing") || specs[0].Unit != "standing-walk-standing" {
		t.Fatalf("launched %+v; want the derived sandbox name and the declared name's unit", specs)
	}
	active, ok := sup.GetActive("walk-standing")
	if !ok || active.Sandbox != specs[0].Name {
		t.Errorf("GetActive = %+v, want it tracked by its declared name with Sandbox %q", active, specs[0].Name)
	}
}

// A launch that keeps failing is retried on a doubling backoff rather
// than on every reconcile; a good launch clears it, and a changed
// definition is tried at once.
func TestStandingSandboxBacksOffAFailingLaunch(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	failing := true
	sup := NewStandingSandboxSupervisor(func(ctx context.Context, spec ServeSpec) (ServingSandbox, error) {
		attempts++
		if failing {
			return nil, errors.New("no capacity")
		}
		return &fakeServingSandbox{spec: spec}, nil
	})
	desired := []model.StandingSandbox{{Name: "svc", Image: "img", Enabled: true}}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	reconcile := func() {
		t.Helper()
		if err := sup.Reconcile(ctx, Deps{}, desired, now); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}

	reconcile()
	active, _ := sup.GetActive("svc")
	if attempts != 1 || active.Failures != 1 || !active.NextAttempt.Equal(now.Add(standingBackoffBase)) {
		t.Fatalf("after one failure: attempts %d, %+v; want NextAttempt %s", attempts, active, now.Add(standingBackoffBase))
	}

	// Reconcile ticks inside the backoff do not launch.
	now = now.Add(10 * time.Second)
	reconcile()
	now = now.Add(10 * time.Second)
	reconcile()
	if attempts != 1 {
		t.Fatalf("launched %d times inside the backoff, want 1", attempts)
	}

	// Once it runs out, one more attempt -- and twice the wait after it.
	now = now.Add(10 * time.Second)
	reconcile()
	active, _ = sup.GetActive("svc")
	if attempts != 2 || active.Failures != 2 || !active.NextAttempt.Equal(now.Add(2*standingBackoffBase)) {
		t.Fatalf("after the second failure: attempts %d, %+v", attempts, active)
	}

	// A changed definition is tried straight away.
	desired[0].Image = "img2"
	now = now.Add(time.Second)
	reconcile()
	active, _ = sup.GetActive("svc")
	if attempts != 3 || active.Failures != 1 {
		t.Fatalf("after a definition change: attempts %d, failures %d; want an immediate attempt, counted afresh", attempts, active.Failures)
	}

	// A good launch clears the backoff.
	failing = false
	now = active.NextAttempt
	reconcile()
	active, _ = sup.GetActive("svc")
	if attempts != 4 || active.Failed || active.Failures != 0 || !active.NextAttempt.IsZero() || active.Restarts != 1 {
		t.Fatalf("after a good launch: attempts %d, %+v", attempts, active)
	}
}

func TestStandingBackoffIsCapped(t *testing.T) {
	for failures, want := range map[int]time.Duration{
		1: standingBackoffBase, 2: 2 * standingBackoffBase, 3: 4 * standingBackoffBase,
		10: standingBackoffMax, 1000: standingBackoffMax,
	} {
		if got := standingBackoff(failures); got != want {
			t.Errorf("standingBackoff(%d) = %s, want %s", failures, got, want)
		}
	}
}
