package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// blockingCISandboxes hands out sandboxes whose every command blocks
// until the run is detached, and records each readopt request.
type blockingCISandboxes struct {
	mu        sync.Mutex
	acquired  []ci.SandboxRequest
	readopted []ci.SandboxRequest
}

func (b *blockingCISandboxes) Acquire(_ context.Context, req ci.SandboxRequest) (ci.Sandbox, error) {
	b.mu.Lock()
	b.acquired = append(b.acquired, req)
	b.mu.Unlock()
	return blockingCISandbox{name: req.Name}, nil
}

func (b *blockingCISandboxes) Readopt(_ context.Context, req ci.SandboxRequest) (ci.Sandbox, error) {
	b.mu.Lock()
	b.readopted = append(b.readopted, req)
	b.mu.Unlock()
	return nil, errors.New("recorded, not readopted")
}

type blockingCISandbox struct{ name string }

func (s blockingCISandbox) Name() string { return s.name }
func (s blockingCISandbox) Exec(ctx context.Context, _ ci.Command) (ci.CommandResult, error) {
	<-ctx.Done()
	return ci.CommandResult{ExitCode: -1}, ctx.Err()
}
func (s blockingCISandbox) Release(context.Context) error { return nil }

// A job's sandbox readopted across a restart is re-granted what runJob
// acquired it with -- the job's own narrowed capabilities -- and not the
// repository's whole set: the whole set would hand it repository scopes
// and explicit-only capabilities it never declared, and drop the
// repo-read: it did.
func TestReadoptRequestCarriesOnlyTheJobsOwnCapabilities(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	workflowYAML := `name: tests
on: push
jobs:
  narrow:
    capabilities: [repo-read:acme/other]
    steps:
      - run: sleep 60
`
	_, commitSHA, source := setupTestCIRepo(t, workflowYAML)
	provided := []string{"repo-write:acme/secret", "scarce-key", "plain"}
	explicitOnly := func(id string) bool { return id == "scarce-key" }

	store1, closeStore1 := openTestStore(t, dataDir)
	defer closeStore1()
	sb1 := &blockingCISandboxes{}
	svc1 := &ci.Service{
		Source:       source,
		Engine:       &ci.Engine{Sandboxes: sb1, ExplicitOnlyCapability: explicitOnly},
		Runs:         ci.NewStoredRunStore(store1, ci.StoredRunStoreOptions{}),
		PlanDir:      filepath.Join(dataDir, "state", "ci-plans"),
		Capabilities: func(context.Context, string) ([]string, error) { return provided, nil },
	}
	started, err := svc1.Start(ctx, ci.Request{Repo: "acme/widgets", Ref: "main", Commit: commitSHA, Event: ci.EventPush})
	if err != nil {
		t.Fatalf("starting CI run: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, _ := svc1.Get(started.ID)
		if len(r.Jobs) == 1 && r.Jobs[0].Sandbox != "" && len(r.Jobs[0].Steps) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the job's step to start; run=%+v", r)
		}
		time.Sleep(20 * time.Millisecond)
	}
	svc1.Detach()
	closeStore1()

	sb1.mu.Lock()
	if len(sb1.acquired) != 1 {
		t.Fatalf("acquired %d sandboxes, want 1", len(sb1.acquired))
	}
	want := slices.Clone(sb1.acquired[0].Capabilities)
	sb1.mu.Unlock()
	slices.Sort(want)
	if !slices.Equal(want, []string{"repo-read:acme/other"}) {
		t.Fatalf("Acquire was handed %v; this test assumes runJob's narrowing gives [repo-read:acme/other]", want)
	}

	store2, closeStore2 := openTestStore(t, dataDir)
	defer closeStore2()
	sb2 := &blockingCISandboxes{}
	svc2 := &ci.Service{
		Source:       source,
		Engine:       &ci.Engine{Sandboxes: sb2, ExplicitOnlyCapability: explicitOnly},
		Runs:         ci.NewStoredRunStore(store2, ci.StoredRunStoreOptions{}),
		PlanDir:      filepath.Join(dataDir, "state", "ci-plans"),
		Capabilities: func(context.Context, string) ([]string, error) { return provided, nil },
	}
	AdoptCIRuns(ctx, svc2)

	sb2.mu.Lock()
	defer sb2.mu.Unlock()
	if len(sb2.readopted) != 1 {
		t.Fatalf("readopted %d sandboxes, want 1", len(sb2.readopted))
	}
	got := slices.Clone(sb2.readopted[0].Capabilities)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("readopt was handed capabilities %v, want the job's own %v (what Acquire was handed)", got, want)
	}
}
