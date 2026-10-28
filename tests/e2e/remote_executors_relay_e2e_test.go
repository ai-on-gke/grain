package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/modelexec"
)

// TestRemoteExecutorsPoolAndConcurrencyWalkthrough performs a comprehensive end-to-end
// verification of the Remote Executor Pools and Agent Concurrency subsystem.
func TestRemoteExecutorsPoolAndConcurrencyWalkthrough(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	st := model.New(db)
	if err := st.Init(ctx); err != nil {
		t.Fatalf("store.Init: %v", err)
	}

	raw1, tok1, err := model.GenerateExecutorToken("cluster-node-alpha")
	if err != nil {
		t.Fatalf("GenerateExecutorToken alpha: %v", err)
	}
	created1, err := st.CreateExecutorToken(ctx, tok1)
	if err != nil {
		t.Fatalf("CreateExecutorToken alpha: %v", err)
	}

	raw2, tok2, err := model.GenerateExecutorToken("cluster-node-beta")
	if err != nil {
		t.Fatalf("GenerateExecutorToken beta: %v", err)
	}
	_, err = st.CreateExecutorToken(ctx, tok2)
	if err != nil {
		t.Fatalf("CreateExecutorToken beta: %v", err)
	}

	// 1. Launch Mock Grain Daemon with Modelexec Server
	var mcpInvocations atomic.Int32
	modelexecSrv, err := modelexec.NewServer(modelexec.Config{
		Store: st,
		Wait:  5 * time.Second,
		ServeMCP: func(rwc io.ReadWriteCloser, args []string) error {
			mcpInvocations.Add(1)
			defer rwc.Close()
			dec := json.NewDecoder(rwc)
			enc := json.NewEncoder(rwc)
			for {
				var req map[string]any
				if err := dec.Decode(&req); err != nil {
					return nil
				}
				resp := map[string]any{
					"jsonrpc": "2.0",
					"id":      req["id"],
					"result": map[string]any{
						"status": "tool_executed_successfully",
					},
				}
				if err := enc.Encode(resp); err != nil {
					return err
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("modelexec.NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()
	go func() {
		_ = modelexecSrv.Serve(ctx, ln)
	}()

	// 2. Connect Node Alpha (Concurrency: 2) & Node Beta (Concurrency: 4)
	execAddr := ln.Addr().String()
	nodeAlpha := &modelexec.Daemon{
		Addr:       execAddr,
		Token:      raw1,
		Name:       "worker-alpha",
		InstanceID: "inst-alpha-001",

		Binaries:    map[string]string{"agy": "/bin/sh", "grain": "/bin/sh"},
		Concurrency: 2,
	}
	go func() { _ = nodeAlpha.Run(ctx) }()

	nodeBeta := &modelexec.Daemon{
		Addr:       execAddr,
		Token:      raw2,
		Name:       "worker-beta",
		InstanceID: "inst-beta-002",

		Binaries:    map[string]string{"agy": "/bin/sh", "grain": "/bin/sh"},
		Concurrency: 4,
	}
	go func() { _ = nodeBeta.Run(ctx) }()

	// Wait for both executors to register
	waitForCondition(t, 5*time.Second, func() bool {
		return len(modelexecSrv.Connected()) >= 2
	}, "executors to register with modelexec server")

	// Verify Status details
	statuses := modelexecSrv.Connected()
	if len(statuses) < 2 {
		t.Fatalf("expected at least 2 connected executors, got %d", len(statuses))
	}
	t.Logf("Connected executors: %+v", statuses)

	// 4. Test Least-Loaded Scheduling across Multi-Executor Pool
	// We dispatch 6 tasks concurrently. Node Alpha has concurrency 2, Beta has concurrency 4.
	// Least-loaded scheduling will distribute tasks proportionally across Alpha and Beta.
	const totalTasks = 6
	var wg sync.WaitGroup
	executorsUsed := make([]string, totalTasks)
	workDirs := make([]string, totalTasks)
	taskErrors := make([]error, totalTasks)

	releaseFile := filepath.Join(t.TempDir(), "release.txt")
	taskStarted := make([]chan struct{}, totalTasks)
	for i := range taskStarted {
		taskStarted[i] = make(chan struct{})
	}

	for i := 0; i < totalTasks; i++ {
		wg.Add(1)
		go func(taskIdx int) {
			defer wg.Done()
			if taskIdx > 0 {
				<-taskStarted[taskIdx-1]
			}

			host, err := modelexecSrv.Host(ctx, "agy")
			if err != nil {
				taskErrors[taskIdx] = fmt.Errorf("getting host: %w", err)
				close(taskStarted[taskIdx])
				return
			}
			executorsUsed[taskIdx] = host.Name()

			// Create isolated temp workspace
			wsDir, err := host.MkdirTemp(ctx, fmt.Sprintf("task-ws-%d-*", taskIdx))
			if err != nil {
				taskErrors[taskIdx] = fmt.Errorf("creating workspace: %w", err)
				close(taskStarted[taskIdx])
				return
			}
			workDirs[taskIdx] = wsDir
			defer func() {
				_ = host.RemoveAll(ctx, wsDir)
			}()

			// Place mock credential file
			credPath := filepath.Join(wsDir, "credentials.json")
			if err := host.PlaceFile(ctx, credPath, fmt.Sprintf(`{"task_id": %d}`, taskIdx), "600"); err != nil {
				taskErrors[taskIdx] = fmt.Errorf("placing credentials: %w", err)
				close(taskStarted[taskIdx])
				return
			}

			// Run command inside workspace holding connection so load is observed until all tasks are running
			var startedOnce sync.Once
			signalWriter := &streamWriter{
				onData: func(p []byte) {
					if strings.Contains(string(p), fmt.Sprintf(`"task_id": %d`, taskIdx)) {
						startedOnce.Do(func() {
							close(taskStarted[taskIdx])
						})
					}
				},
			}

			cmdScript := fmt.Sprintf(`cat %s && for i in $(seq 1 100); do if [ -f %s ]; then break; fi; sleep 0.05; done`, credPath, releaseFile)
			out, err := host.Run(ctx, agent.Command{
				Path: "/bin/sh",
				Args: []string{"-c", cmdScript},
			}, signalWriter)
			if err != nil {
				taskErrors[taskIdx] = fmt.Errorf("running command: %w", err)
				return
			}
			if !strings.Contains(out, fmt.Sprintf(`"task_id": %d`, taskIdx)) {
				taskErrors[taskIdx] = fmt.Errorf("unexpected output: %s", out)
				return
			}
		}(i)
	}

	// Wait for all tasks to be actively executing concurrently on executors
	<-taskStarted[totalTasks-1]

	// Release all tasks so they complete
	if err := os.WriteFile(releaseFile, []byte("ok"), 0644); err != nil {
		t.Fatalf("writing release file: %v", err)
	}

	wg.Wait()

	for i, err := range taskErrors {
		if err != nil {
			t.Fatalf("Task %d failed: %v", i, err)
		}
	}

	// Verify workspace isolation
	seenDirs := make(map[string]bool)
	for i, dir := range workDirs {
		if seenDirs[dir] {
			t.Fatalf("Duplicate workspace dir %s across tasks", dir)
		}
		seenDirs[dir] = true
		// Verify cleanup
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Logf("Workspace %s successfully cleaned up by task %d", dir, i)
		}
	}

	// Verify both executors received load
	alphaCount := 0
	betaCount := 0
	for _, name := range executorsUsed {
		if strings.Contains(name, "alpha") {
			alphaCount++
		} else if strings.Contains(name, "beta") {
			betaCount++
		}
	}
	t.Logf("Load distribution: worker-alpha=%d, worker-beta=%d", alphaCount, betaCount)
	if alphaCount == 0 || betaCount == 0 {
		t.Fatalf("Expected load distributed between both executors, got alpha=%d, beta=%d", alphaCount, betaCount)
	}

	// 5. Test Instance Deduplication (duplicate instance ID reconnect)
	dupNode := &modelexec.Daemon{
		Addr:       execAddr,
		Token:      raw1,
		Name:       "worker-alpha-reconnected",
		InstanceID: "inst-alpha-001", // Duplicate instance ID

		Binaries:    map[string]string{"agy": "/bin/sh", "grain": "/bin/sh"},
		Concurrency: 2,
	}
	go func() { _ = dupNode.Run(ctx) }()

	waitForCondition(t, 5*time.Second, func() bool {
		for _, s := range modelexecSrv.Connected() {
			if s.ID == "inst-alpha-001" && s.Host == "worker-alpha-reconnected" {
				return true
			}
		}
		return false
	}, "duplicate instance to replace previous executor session")

	// 6. Test Per-Executor Token Revocation
	// Revoke created1 (tok1). worker-alpha should be disconnected immediately
	// and subsequent reconnection attempts should be rejected.
	if _, err := st.RevokeExecutorToken(ctx, created1.ID); err != nil {
		t.Fatalf("RevokeExecutorToken failed: %v", err)
	}
	modelexecSrv.RevokeToken(created1.ID)

	// Verify worker-alpha is evicted
	waitForCondition(t, 5*time.Second, func() bool {
		for _, s := range modelexecSrv.Connected() {
			if s.ID == "inst-alpha-001" {
				return false
			}
		}
		return true
	}, "worker-alpha to be evicted upon token revocation")

	// Verify worker-beta remains active
	if len(modelexecSrv.Connected()) != 1 {
		t.Fatalf("expected 1 executor connected (worker-beta), got %d", len(modelexecSrv.Connected()))
	}
	if modelexecSrv.Connected()[0].ID != "inst-beta-002" {
		t.Fatalf("expected worker-beta to remain connected, got %s", modelexecSrv.Connected()[0].Host)
	}

	// 7. Verify Network Resilience (Graceful Half-Close on MCP Streams)
	betaHost, err := modelexecSrv.Host(ctx, "agy")
	if err != nil || betaHost == nil {
		t.Fatalf("getting betaHost: %v", err)
	}

	mcpScript := `
python3 -c '
import os, sys, socket, json

sock = os.environ.get("GRAIN_EXECUTOR_SOCK")
if not sock:
    sys.exit("missing sock")

s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sock)
s.sendall(json.dumps(["mcpserver"]).encode("utf-8") + b"\n")

# Send tool request
req = {"jsonrpc": "2.0", "id": 42, "method": "tools/call", "params": {"name": "test_tool"}}
s.sendall(json.dumps(req).encode("utf-8") + b"\n")

# Half-close write end
s.shutdown(socket.SHUT_WR)

resp = b""
while True:
    data = s.recv(1024)
    if not data:
        break
    resp += data
s.close()

if b"tool_executed_successfully" in resp:
    print("MCP_HALF_CLOSE_SUCCESS")
else:
    print("MCP_FAILED:" + resp.decode("utf-8", errors="ignore"))
'
`
	// The tunnel socket is only in the environment of what is started
	// through the executor's grain binary, as `grain mcpserver` is.
	grain, err := betaHost.BinaryPath("grain")
	if err != nil {
		t.Fatalf("BinaryPath(grain): %v", err)
	}
	betaHost.AuthorizeMCPArgs([]string{"mcpserver"})
	out, err := betaHost.Run(ctx, agent.Command{
		Path: grain,
		Args: []string{"-c", mcpScript},
	}, nil)
	if err != nil {
		t.Fatalf("MCP half-close run failed: %v, out: %s", err, out)
	}
	if !strings.Contains(out, "MCP_HALF_CLOSE_SUCCESS") {
		t.Fatalf("MCP half-close test did not succeed: %s", out)
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

type streamWriter struct {
	onData func([]byte)
}

func (w *streamWriter) Write(p []byte) (int, error) {
	if w.onData != nil {
		w.onData(p)
	}
	return len(p), nil
}
