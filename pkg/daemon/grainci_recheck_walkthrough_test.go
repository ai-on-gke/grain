package daemon

// docs/test-plans/2026-10-25.md §6 step 3, driven end to end: an agent's
// wait_for_checks and retrigger_ci (pkg/mcp) over the REST hop a sandbox
// run makes (DaemonGrainCI, ui.HTTPClient) to a real pkg/ui server whose
// grain CI is a real ci.Service -- a workflow on disk, an engine, a
// DirCache -- with jobs that run on a fake machine this test controls.
// The report is docs/test-runs/2026-10-25-ci-recheck-retry-cache.md.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/ui"
)

const recheckWalkthroughWorkflow = `name: tests
on: [push, pull_request]
jobs:
  go:
    name: go
    cache: true
    steps:
      - name: go test
        run: echo GO_STEP
  e2e:
    name: e2e
    cache: true
    steps:
      - name: e2e
        run: echo E2E_STEP
`

const recheckWalkthroughCommit = "abcdef0123456789abcdef0123456789abcdef01"

// pinnedSource is a workflow directory whose branch is always at one
// commit, as a forge's source would answer for a branch nobody pushes to.
type pinnedSource struct{ ci.FSSource }

func (s pinnedSource) Workflows(ctx context.Context, repo, ref string) ([]ci.Workflow, string, error) {
	workflows, _, err := s.FSSource.Workflows(ctx, repo, ref)
	return workflows, recheckWalkthroughCommit, err
}

// recheckMachine is every job's sandbox. go always passes. e2e fails the
// first time it runs and passes after that -- but, while hold is open, a
// later execution waits for it to close (or for its run to be cancelled)
// first, so a recheck can be kept in flight for as long as the test needs.
type recheckMachine struct {
	goRuns  atomic.Int32
	e2eRuns atomic.Int32
	hold    chan struct{}
}

func (m *recheckMachine) Acquire(context.Context, ci.SandboxRequest) (ci.Sandbox, error) {
	return m, nil
}
func (m *recheckMachine) Name() string                  { return "fake" }
func (m *recheckMachine) Release(context.Context) error { return nil }

func (m *recheckMachine) Exec(ctx context.Context, cmd ci.Command) (ci.CommandResult, error) {
	switch {
	case strings.Contains(cmd.Script, "GO_STEP"):
		m.goRuns.Add(1)
		return ci.CommandResult{Output: "ok\n"}, nil
	case strings.Contains(cmd.Script, "E2E_STEP"):
		if m.e2eRuns.Add(1) == 1 {
			return ci.CommandResult{Output: "--- FAIL: TestCheckout\n", ExitCode: 1}, nil
		}
		select {
		case <-m.hold:
		case <-ctx.Done():
			return ci.CommandResult{ExitCode: 1}, ctx.Err()
		}
		return ci.CommandResult{Output: "--- PASS: TestCheckout\n"}, nil
	}
	return ci.CommandResult{}, nil
}

// countStarts counts POST /api/ci/runs, which is how both tools ask.
type countStarts struct {
	next   http.Handler
	starts atomic.Int32
}

func (c *countStarts) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && r.URL.Path == "/api/ci/runs" {
		defer c.starts.Add(1)
	}
	c.next.ServeHTTP(w, r)
}

func TestWalkthroughWaitForChecksFollowsASupersedingRecheckToTheCombinedVerdict(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("waits out one wait_for_checks poll interval")
	}
	ctx := context.Background()
	root := t.TempDir()
	tree := filepath.Join(root, "tree")
	if err := os.MkdirAll(filepath.Join(tree, ".grain", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, ".grain", "workflows", "tests.yml"), []byte(recheckWalkthroughWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	cache, err := ci.OpenDirCache(filepath.Join(root, "cache"), ci.DirCacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	machine := &recheckMachine{hold: make(chan struct{})}
	var idMu sync.Mutex
	n := 0
	svc := &ci.Service{
		Source: pinnedSource{ci.FSSource{Root: tree}},
		Engine: &ci.Engine{Sandboxes: machine, Cache: cache, Concurrency: 4, Log: t.Logf},
		NewID: func() string {
			idMu.Lock()
			defer idMu.Unlock()
			n++
			return fmt.Sprintf("run-%d", n)
		},
		Log: t.Logf,
	}

	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(root, "data")))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTask(ctx, model.Task{
		ID: "7", Intent: model.IntentImplement, Title: "test it", Target: &repo,
		Grants: []model.Grant{{Capability: "grain-ci"}},
	}); err != nil {
		t.Fatal(err)
	}
	counted := &countStarts{next: ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("operator"),
		Capabilities:  ui.OfferedCapabilities(),
		LocalAPIToken: testAccessCode,
		CI:            svc,
	}, store)}
	srv := httptest.NewServer(counted)
	t.Cleanup(srv.Close)
	tools := map[string]mcp.Tool{}
	for _, tool := range mcp.NewGrainCIWaitTools(DaemonGrainCI{Client: accessCodeClient(srv.URL), TaskID: "7"},
		mcp.CIRepoScope{Repo: repo.String()}) {
		tools[tool.Name] = tool
	}
	call := func(name string, args map[string]any) string {
		t.Helper()
		res := tools[name].Handler(ctx, args)
		if res.IsError {
			t.Fatalf("%s: %s", name, res.Text)
		}
		return res.Text
	}

	// The complete suite: go passes, e2e fails.
	first := call("wait_for_checks", map[string]any{})
	if !strings.Contains(first, "run-1") || !strings.Contains(first, "failure") {
		t.Fatalf("first wait_for_checks = %s, want run-1 failing", first)
	}

	// retrigger_ci re-runs only e2e, uncached, and the run is held.
	if text := call("retrigger_ci", map[string]any{}); !strings.Contains(text, "run-2") {
		t.Fatalf("retrigger_ci = %s, want run-2", text)
	}
	recheck, _ := svc.Get("run-2")
	if !recheck.Recheck || len(recheck.Jobs) != 1 || recheck.Jobs[0].JobID != "e2e" {
		t.Fatalf("run-2 = %+v, want an uncached recheck of e2e alone", recheck)
	}

	// A full-suite wait joins it rather than starting a run.
	before := counted.starts.Load()
	waited := make(chan string, 1)
	go func() { waited <- call("wait_for_checks", map[string]any{}) }()
	for deadline := time.Now().Add(10 * time.Second); counted.starts.Load() == before; {
		if time.Now().After(deadline) {
			t.Fatal("wait_for_checks never asked for its run")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // past its first read of run-2

	// The same retrigger_ci again is the same plan, and grain CI hands
	// back the recheck already running rather than starting a twin
	// (ci.Service.Start's Live). Asking for every job is a different one,
	// and supersedes the first recheck while the wait is on it; the new
	// recheck is let through.
	if text := call("retrigger_ci", map[string]any{}); !strings.Contains(text, "run-2") {
		t.Fatalf("identical retrigger_ci = %s, want the live run-2 back", text)
	}
	if text := call("retrigger_ci", map[string]any{"jobs": []any{"*"}}); !strings.Contains(text, "run-3") {
		t.Fatalf("retrigger_ci of every job = %s, want run-3", text)
	}
	close(machine.hold)

	var joined string
	select {
	case joined = <-waited:
	case <-time.After(2 * time.Minute):
		t.Fatal("wait_for_checks never answered")
	}
	superseded, _ := svc.Get("run-2")
	if superseded.Conclusion != ci.ConclusionCancelled || !strings.Contains(superseded.CancelReason, "run-3") {
		t.Fatalf("run-2 = %s (%q), want it superseded by run-3", superseded.Conclusion, superseded.CancelReason)
	}
	if !strings.Contains(joined, "run-3") || !strings.Contains(joined, "success") || strings.Contains(joined, "cancelled") {
		t.Fatalf("joined wait_for_checks = %s\nwant the superseding recheck run-3's success, not run-2's cancellation", joined)
	}
	if n := svc.List(); len(n) != 3 {
		t.Fatalf("grain CI holds %d runs, want 3: the wait must start none of its own", len(n))
	}

	// And the next full-suite wait is the combined verdict, every job's
	// newest result read from the cache rather than run again.
	combined := call("wait_for_checks", map[string]any{})
	if !strings.Contains(combined, "run-4") || !strings.Contains(combined, "success") {
		t.Fatalf("combined wait_for_checks = %s, want run-4 passing", combined)
	}
	run4, _ := svc.Get("run-4")
	for _, job := range run4.Jobs {
		if job.Conclusion != ci.ConclusionSuccess || !job.Cached {
			t.Errorf("run-4 job %s = %s (cached %v), want a cached pass", job.JobID, job.Conclusion, job.Cached)
		}
	}
	if g, e := machine.goRuns.Load(), machine.e2eRuns.Load(); g != 2 || e != 3 {
		t.Errorf("go ran %d times and e2e %d, want 2 (run-1, run-3) and 3 (run-1, the superseded run-2, run-3)", g, e)
	}
}
