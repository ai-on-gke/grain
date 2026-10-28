package cli

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type walkthroughClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *walkthroughClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *walkthroughClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// streamingWalkthroughSandbox emits chunks with clock advances
type streamingWalkthroughSandbox struct {
	name        string
	clock       *walkthroughClock
	chunks      []string
	chunkStepCh chan struct{}
	chunkDoneCh chan struct{}
}

func (s *streamingWalkthroughSandbox) Name() string {
	return s.name
}

func (s *streamingWalkthroughSandbox) Release(_ context.Context) error {
	return nil
}

func (s *streamingWalkthroughSandbox) Exec(ctx context.Context, cmd ci.Command) (ci.CommandResult, error) {
	return s.ExecStream(ctx, cmd, nil)
}

func (s *streamingWalkthroughSandbox) ExecStream(ctx context.Context, _ ci.Command, out func(string)) (ci.CommandResult, error) {
	var full strings.Builder
	for _, chunk := range s.chunks {
		if s.chunkStepCh != nil {
			select {
			case <-ctx.Done():
				return ci.CommandResult{Output: full.String(), ExitCode: 1}, ctx.Err()
			case <-s.chunkStepCh:
			}
		}
		if s.clock != nil {
			s.clock.advance(ci.LiveOutputInterval + time.Second)
		}
		full.WriteString(chunk)
		if out != nil {
			out(chunk)
		}
		if s.chunkDoneCh != nil {
			s.chunkDoneCh <- struct{}{}
		}
	}
	return ci.CommandResult{Output: full.String(), ExitCode: 0}, nil
}

type streamingWalkthroughSandboxes struct {
	sandbox *streamingWalkthroughSandbox
}

func (b *streamingWalkthroughSandboxes) Acquire(_ context.Context, _ ci.SandboxRequest) (ci.Sandbox, error) {
	return b.sandbox, nil
}

type testCIRunnerAdapter struct {
	service *ci.Service
	store   ci.RunStore
	repo    string
}

func (r *testCIRunnerAdapter) StartGrainCI(ctx context.Context, jobs []string) (ci.Run, error) {
	return r.service.Start(ctx, ci.Request{Repo: r.repo, Ref: "main", Jobs: jobs})
}

func (r *testCIRunnerAdapter) RetriggerGrainCI(ctx context.Context, workflow, commit string, jobs []string, all bool) (ci.Run, error) {
	return r.service.Start(ctx, ci.Request{Repo: r.repo, Ref: "main", Workflow: workflow, Commit: commit, Jobs: jobs, Recheck: true})
}

func (r *testCIRunnerAdapter) GrainCIRun(_ context.Context, id string) (ci.Run, error) {
	run, ok := r.service.Get(id)
	if !ok {
		return ci.Run{}, fmt.Errorf("no such run: %s", id)
	}
	return run, nil
}

func (r *testCIRunnerAdapter) FindGrainCIRuns(_ context.Context, filter ci.RunFilter, limit int) (ci.RunPage, error) {
	if r.store != nil {
		return r.store.Find(filter, limit), nil
	}
	return ci.RunPage{}, nil
}

func (r *testCIRunnerAdapter) GrainCISharedFaults(_ context.Context) ([]mcp.CIFault, error) {
	return nil, nil
}

func parseCILogResponse(text string) (offset, returned, totalSize, nextOffset int, complete, stillRunning bool, body string) {
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "offset: ") {
			fmt.Sscanf(line, "offset: %d", &offset)
		} else if strings.HasPrefix(line, "returned: ") {
			fmt.Sscanf(line, "returned: %d", &returned)
		} else if strings.HasPrefix(line, "total_size: ") {
			fmt.Sscanf(line, "total_size: %d", &totalSize)
		} else if strings.HasPrefix(line, "next_offset: ") {
			fmt.Sscanf(line, "next_offset: %d", &nextOffset)
		} else if strings.HasPrefix(line, "complete: ") {
			complete = strings.Contains(line, "true")
		} else if strings.HasPrefix(line, "still_running: ") {
			stillRunning = strings.Contains(line, "true")
		}
	}
	if returned > 0 && len(text) >= returned {
		body = text[len(text)-returned:]
	}
	return
}

func TestChunkedLiveCIStepLogsStorageAndStreamingWalkthrough(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(dir, "grain.db")))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	defer db.Close()

	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("initializing store: %v", err)
	}

	clock := &walkthroughClock{now: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)}

	var testChunks []string
	for i := 1; i <= 6; i++ {
		var chunkBuilder strings.Builder
		for j := 1; j <= 10; j++ {
			lineNum := (i-1)*10 + j
			fmt.Fprintf(&chunkBuilder, "Log line %04d - step stream chunk %d\n", lineNum, i)
		}
		testChunks = append(testChunks, chunkBuilder.String())
	}
	fullExpectedOutput := strings.Join(testChunks, "")

	stepCh := make(chan struct{})
	doneCh := make(chan struct{})

	sandbox := &streamingWalkthroughSandbox{
		name:        "high-throughput-sb",
		clock:       clock,
		chunks:      testChunks,
		chunkStepCh: stepCh,
		chunkDoneCh: doneCh,
	}

	workflow := ci.Workflow{
		Path: ".grain/workflows/ci.yml",
		On:   ci.Triggers{Manual: true, Push: &ci.BranchTrigger{}},
		Jobs: []ci.Job{{
			ID:   "high-throughput-log",
			Name: "High Throughput Log Job",
			Steps: []ci.Step{{
				Name: "stream-logs",
				Run:  "echo 'streaming logs'",
			}},
		}},
	}

	storedRuns := ci.NewStoredRunStore(store, ci.StoredRunStoreOptions{
		Now: clock.Now,
	})
	service := &ci.Service{
		Source: ci.SourceFunc(func(context.Context, string, string) ([]ci.Workflow, string, error) {
			return []ci.Workflow{workflow}, "commit-1234567890", nil
		}),
		Engine: &ci.Engine{
			Sandboxes: &streamingWalkthroughSandboxes{sandbox: sandbox},
			Now:       clock.Now,
		},
		Runs: storedRuns,
		Now:  clock.Now,
	}

	uiCfg := ui.Config{
		CI:           service,
		Capabilities: ui.OfferedCapabilities(),
	}
	srv := ui.NewServer(withAccessCode(uiCfg), store)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	req := ci.Request{
		Repo:  "acme/widgets",
		Ref:   "main",
		Event: ci.EventManual,
	}
	run, err := service.Start(ctx, req)
	if err != nil {
		t.Fatalf("starting CI run: %v", err)
	}
	t.Logf("Started live CI run ID: %s", run.ID)

	runnerAdapter := &testCIRunnerAdapter{
		service: service,
		store:   storedRuns,
		repo:    "acme/widgets",
	}

	waitTools := mcp.NewGrainCIWaitTools(runnerAdapter, mcp.CIRepoScope{Repo: "acme/widgets"})
	var getCILogTool mcp.Tool
	for _, tool := range waitTools {
		if tool.Name == "get_ci_log" {
			getCILogTool = tool
			break
		}
	}
	if getCILogTool.Name == "" {
		t.Fatal("get_ci_log tool not found")
	}

	var accumulatedStream strings.Builder
	currentOffset := 0

	// Emit chunks 1 through 5 while step remains in flight
	for chunkIdx := 1; chunkIdx <= 5; chunkIdx++ {
		stepCh <- struct{}{}
		<-doneCh

		// Wait for async storage update (initial ci_step insertion on chunk 1,
		// and ci_step_log_chunk flush on chunks 2..5).
		expectedChunks := chunkIdx - 1
		var chunkRowCount int
		var dbStepOutput string
		stepDeadline := time.Now().Add(daemonWaitLimit)
		for {
			stepErr := db.QueryRowContext(ctx,
				"SELECT `output` FROM `ci_step` WHERE `run_id` = ? AND `job_index` = 0 AND `step_index` = 0",
				run.ID).Scan(&dbStepOutput)
			chunkErr := db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM `ci_step_log_chunk` WHERE `run_id` = ?", run.ID).Scan(&chunkRowCount)
			if stepErr == nil && chunkErr == nil && chunkRowCount >= expectedChunks {
				break
			}
			if !time.Now().Before(stepDeadline) {
				if stepErr != nil {
					t.Fatalf("reading ci_step after chunk %d: %v", chunkIdx, stepErr)
				}
				if chunkErr != nil {
					t.Fatalf("querying chunk count after chunk %d: %v", chunkIdx, chunkErr)
				}
				t.Fatalf("waiting for chunk %d: ci_step_log_chunk row count = %d, want >= %d", chunkIdx, chunkRowCount, expectedChunks)
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Logf("After emitting chunk %d: ci_step_log_chunk row count = %d", chunkIdx, chunkRowCount)

		// Base ci_step.output in database remains empty while chunks are appended
		if dbStepOutput != "" {
			t.Errorf("ci_step.output = %q, want empty before run conclusion", dbStepOutput)
		}

		// Read progressive log stream via MCP get_ci_log
		res := getCILogTool.Handler(ctx, map[string]any{
			"run_id": run.ID,
			"job":    "high-throughput-log",
			"step":   "stream-logs",
			"offset": float64(currentOffset),
			"size":   float64(4096),
		})
		if res.IsError {
			t.Fatalf("get_ci_log offset %d failed: %s", currentOffset, res.Text)
		}

		_, retReturned, _, retNext, retComplete, retRunning, body := parseCILogResponse(res.Text)
		t.Logf("get_ci_log chunk %d: returned=%d, next_offset=%d, complete=%v, running=%v",
			chunkIdx, retReturned, retNext, retComplete, retRunning)

		// If logs have begun publishing (after first interval), accumulate streamed bytes
		if chunkRowCount > 0 && body != "" {
			accumulatedStream.WriteString(body)
			currentOffset = retNext
		}

		// Tail query verification
		tailRes := getCILogTool.Handler(ctx, map[string]any{
			"run_id": run.ID,
			"job":    "high-throughput-log",
			"step":   "stream-logs",
			"tail":   float64(128),
		})
		if tailRes.IsError {
			t.Errorf("get_ci_log tail failed: %s", tailRes.Text)
		}

		// HTTP endpoint verification
		httpResp, err := accessGet(fmt.Sprintf("%s/api/ci/runs/%s", ts.URL, run.ID))
		if err != nil {
			t.Errorf("HTTP GET /api/ci/runs/%s failed: %v", run.ID, err)
		} else {
			if httpResp.StatusCode != http.StatusOK {
				t.Errorf("HTTP GET status = %d, want 200", httpResp.StatusCode)
			}
			httpResp.Body.Close()
		}
	}

	// Emit final chunk (chunk 6) to complete the step and conclude the run
	stepCh <- struct{}{}
	<-doneCh

	// Wait for run conclusion
	deadline := time.Now().Add(daemonWaitLimit)
	for time.Now().Before(deadline) {
		r, ok := service.Get(run.ID)
		if ok && r.Conclusion.Done() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	finalRun, ok := service.Get(run.ID)
	if !ok || finalRun.Conclusion != ci.ConclusionSuccess {
		t.Fatalf("final run conclusion = %v, want success", finalRun.Conclusion)
	}

	// Read remaining output to end
	res := getCILogTool.Handler(ctx, map[string]any{
		"run_id": run.ID,
		"job":    "high-throughput-log",
		"step":   "stream-logs",
		"offset": float64(currentOffset),
		"size":   float64(4096),
	})
	if res.IsError {
		t.Fatalf("final get_ci_log read failed: %s", res.Text)
	}
	_, _, _, _, retComplete, _, body := parseCILogResponse(res.Text)
	if !retComplete {
		t.Errorf("expected complete: true on concluded run, got false")
	}
	if body != "" {
		accumulatedStream.WriteString(body)
	}

	if strings.TrimSpace(accumulatedStream.String()) != strings.TrimSpace(fullExpectedOutput) {
		t.Errorf("accumulated live stream mismatch! got len %d, want len %d",
			len(strings.TrimSpace(accumulatedStream.String())), len(strings.TrimSpace(fullExpectedOutput)))
	}

	// Verify all chunks cleaned up in database post-conclusion
	var remainingChunks int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `ci_step_log_chunk` WHERE `run_id` = ?", run.ID).Scan(&remainingChunks); err != nil {
		t.Fatalf("querying remaining chunks: %v", err)
	}
	if remainingChunks != 0 {
		t.Errorf("ci_step_log_chunk remaining after conclusion = %d, want 0", remainingChunks)
	}

	// Verify consolidated full log stored in ci_step
	var finalDBOutput string
	if err := db.QueryRowContext(ctx,
		"SELECT `output` FROM `ci_step` WHERE `run_id` = ? AND `job_index` = 0 AND `step_index` = 0",
		run.ID).Scan(&finalDBOutput); err != nil {
		t.Fatalf("reading finalized ci_step: %v", err)
	}
	if finalDBOutput != fullExpectedOutput {
		t.Errorf("finalized ci_step output mismatch! len %d, want %d", len(finalDBOutput), len(fullExpectedOutput))
	}

	// Test reading past the end
	pastRes := getCILogTool.Handler(ctx, map[string]any{
		"run_id": run.ID,
		"job":    "high-throughput-log",
		"step":   "stream-logs",
		"offset": float64(len(fullExpectedOutput) + 500),
		"size":   float64(1024),
	})
	if pastRes.IsError {
		t.Errorf("get_ci_log past end returned error: %s", pastRes.Text)
	}
	if !strings.Contains(pastRes.Text, "returned: 0") || !strings.Contains(pastRes.Text, "complete: true") {
		t.Errorf("get_ci_log past end response invalid: %s", pastRes.Text)
	}

	// Test retention pruning
	sweep, err := store.PruneCIStepOutput(ctx, model.CIStepOutputBounds{
		MaxBytes: 1,
	})
	if err != nil {
		t.Fatalf("PruneCIStepOutput failed: %v", err)
	}
	t.Logf("PruneCIStepOutput swept %d runs, %d steps, %d bytes", sweep.Runs, sweep.Steps, sweep.Bytes)

	if err := db.QueryRowContext(ctx,
		"SELECT `output` FROM `ci_step` WHERE `run_id` = ? AND `job_index` = 0 AND `step_index` = 0",
		run.ID).Scan(&finalDBOutput); err != nil {
		t.Fatalf("reading pruned ci_step: %v", err)
	}
	if finalDBOutput != "" {
		t.Errorf("pruned ci_step output = %q, want empty string", finalDBOutput)
	}
}
