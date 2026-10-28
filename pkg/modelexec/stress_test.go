package modelexec_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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
	"github.com/ai-on-gke/grain/pkg/modelexec"
)

func stressDuration(t *testing.T, defaultDur time.Duration) time.Duration {
	t.Helper()
	if s := os.Getenv("GRAIN_STRESS_DURATION"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			return d
		}
	}
	if s := os.Getenv("STRESS_DURATION"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			return d
		}
	}
	if testing.Short() {
		return 2 * time.Second
	}
	return defaultDur
}

func countOpenFDs() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

// TestModelexecHighConcurrencyStress exercises the complete modelexec stack
// (Server, Relay, Daemon, HostConn, RemoteHost, MCP Tunneling, Git Proxy, Stdout/Stderr Pumps,
// Abrupt Cancellations, Process Group Killings, Dynamic Limits, and Filesystem Ops)
// concurrently under heavy load.
func TestModelexecHighConcurrencyStress(t *testing.T) {
	dur := stressDuration(t, 8*time.Second)
	t.Logf("Running Modelexec high-concurrency stress test for %v", dur)

	startFDs := countOpenFDs()

	const (
		executorToken = "executor-secret-token-200"
	)

	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()

	// 1. Setup Modelexec Server with MCP & Git Proxy tunnels
	var (
		totalMCPCalls      atomic.Int64
		totalGitProxyCalls atomic.Int64
	)

	serveMCP := func(rwc io.ReadWriteCloser, args []string) error {
		defer rwc.Close()
		totalMCPCalls.Add(1)

		dec := json.NewDecoder(rwc)
		enc := json.NewEncoder(rwc)

		var req map[string]any
		if err := dec.Decode(&req); err != nil {
			return err
		}
		val, _ := req["payload"].(string)
		return enc.Encode(map[string]any{
			"status": "ok",
			"echo":   val,
			"args":   args,
		})
	}

	serveGitProxy := func(rwc io.ReadWriteCloser) error {
		defer rwc.Close()
		totalGitProxyCalls.Add(1)
		buf := make([]byte, 1024)
		n, err := rwc.Read(buf)
		if err != nil && err != io.EOF {
			return err
		}
		if n > 0 {
			_, _ = rwc.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK"))
		}
		return nil
	}

	modelexecSrv, err := modelexec.NewServer(modelexec.Config{
		Token:         executorToken,
		Wait:          5 * time.Second,
		ServeMCP:      serveMCP,
		ServeGitProxy: serveGitProxy,
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

	// 2. Start multiple remote executor daemons connecting to the listener
	numDaemons := 4
	for d := 0; d < numDaemons; d++ {
		daemonName := fmt.Sprintf("stress-daemon-%d", d)

		dConfig := &modelexec.Daemon{
			Addr:        ln.Addr().String(),
			Token:       executorToken,
			Name:        daemonName,
			Concurrency: 32,
			Binaries: map[string]string{
				"echo": "/bin/echo",
				"sh":   "/bin/sh",
				"cat":  "/bin/cat",
				// The MCP calls below dial the tunnel socket, which only
				// a process started through the executor's grain shim
				// is told about.
				"grain": "/bin/sh",
			},
		}
		go func(daemon *modelexec.Daemon) {
			_ = daemon.Run(ctx)
		}(dConfig)
	}

	// Wait for at least one host to register
	hostCheckDeadline := time.Now().Add(5 * time.Second)
	var initialHost *modelexec.RemoteHost
	for {
		if time.Now().After(hostCheckDeadline) {
			t.Fatal("timed out waiting for daemons to register with modelexec server")
		}
		initialHost, err = modelexecSrv.Host(ctx, "sh")
		if err == nil && initialHost != nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	var (
		successfulExecs     atomic.Int64
		failedExecs         atomic.Int64
		abruptCancellations atomic.Int64
		abruptKills         atomic.Int64
		fsOpsCompleted      atomic.Int64
		totalBytesPushed    atomic.Int64
		wg                  sync.WaitGroup
	)

	concurrency := 24
	if dur > 10*time.Second {
		concurrency = 48
	}

	// 5. Worker pool generating concurrent workload
	for i := 0; i < concurrency; i++ {
		workerID := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			var counter int64

			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				counter++
				taskType := (workerID + int(counter)) % 5

				host, err := modelexecSrv.Host(ctx, "sh")
				if err != nil || host == nil {
					time.Sleep(20 * time.Millisecond)
					continue
				}

				switch taskType {
				case 0:
					// A. High-Throughput Stdout/Stderr log pumping
					lineCount := 500 + int(counter%500)
					shCmd := fmt.Sprintf("for i in $(seq 1 %d); do echo \"[LOG-STRESS-$i] abcdefghijklmnopqrstuvwxyz0123456789 payload-data\"; done", lineCount)

					var teeBuf bytes.Buffer
					out, err := host.Run(ctx, agent.Command{
						Path: "/bin/sh",
						Args: []string{"-c", shCmd},
					}, &teeBuf)

					if err != nil {
						if ctx.Err() == nil {
							failedExecs.Add(1)
						}
					} else {
						successfulExecs.Add(1)
						totalBytesPushed.Add(int64(len(out)))
						if !strings.Contains(out, fmt.Sprintf("[LOG-STRESS-%d]", lineCount)) {
							t.Errorf("stdout incomplete, missing last line")
						}
					}

				case 1:
					// B. Nested subagent tool execution over GRAIN_EXECUTOR_SOCK unix domain socket
					tokenBytes := make([]byte, 16)
					_, _ = rand.Read(tokenBytes)
					mcpPayload := hex.EncodeToString(tokenBytes)

					mcpScript := fmt.Sprintf(`
cat << 'EOF' > /tmp/mcp_worker_%d.py
import os, sys, socket, json

sock_path = os.environ.get("GRAIN_EXECUTOR_SOCK")
if not sock_path:
    sys.exit("missing GRAIN_EXECUTOR_SOCK")

s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sock_path)
s.sendall(json.dumps(["mcpserver", "-sandbox", "test"]).encode("utf-8") + b"\n")
s.sendall(json.dumps({"payload": "%s"}).encode("utf-8") + b"\n")

buf = b""
while b"\n" not in buf:
    chunk = s.recv(1024)
    if not chunk:
        break
    buf += chunk
s.close()
resp = json.loads(buf.decode("utf-8"))
print("MCP_OUT:" + resp.get("echo", ""))
EOF
python3 /tmp/mcp_worker_%d.py
rm -f /tmp/mcp_worker_%d.py
`, workerID, mcpPayload, workerID, workerID)

					grain, err := host.BinaryPath("grain")
					if err != nil {
						failedExecs.Add(1)
						continue
					}
					out, err := host.Run(ctx, agent.Command{
						Path: grain,
						Args: []string{"-c", mcpScript},
					}, nil)

					if err != nil {
						if ctx.Err() == nil {
							failedExecs.Add(1)
						}
					} else {
						if strings.Contains(out, "MCP_OUT:"+mcpPayload) {
							successfulExecs.Add(1)
						} else {
							failedExecs.Add(1)
						}
					}

				case 2:
					// C. Abrupt Client Cancellation while streaming
					cmdCtx, cmdCancel := context.WithTimeout(ctx, 60*time.Millisecond)
					// Command that would run for 10 seconds if not cancelled
					_, cErr := host.Run(cmdCtx, agent.Command{
						Path: "/bin/sh",
						Args: []string{"-c", "while true; do echo 'pumping...'; sleep 0.01; done"},
					}, nil)
					cmdCancel()

					if cErr != nil {
						abruptCancellations.Add(1)
					}

				case 3:
					// D. Process Group Kill / SIGTERM / SIGKILL simulation
					// Start a pipeline of subprocesses and kill the parent group
					killScript := "sh -c 'sleep 10 & sleep 10 & sleep 10 & wait' & pid=$!; sleep 0.05; kill -9 $pid 2>/dev/null || true"
					_, _ = host.Run(ctx, agent.Command{
						Path: "/bin/sh",
						Args: []string{"-c", killScript},
					}, nil)
					abruptKills.Add(1)

				case 4:
					// E. Concurrent Filesystem Operations (MkdirTemp, WriteFile, ReadTail, RemoveAll)
					tmpDir, err := host.MkdirTemp(ctx, "stress-fs-*")
					if err == nil && tmpDir != "" {
						testFile := filepath.Join(tmpDir, "data.txt")
						fileData := []byte("modelexec-filesystem-stress-test-data-" + hex.EncodeToString(tokenBytesGen(16)))
						if wErr := host.WriteFile(ctx, testFile, fileData, fs.FileMode(0644)); wErr == nil {
							readData, truncated, rErr := host.ReadTail(ctx, testFile, 1024)
							if rErr == nil && !truncated && bytes.Equal(readData, fileData) {
								fsOpsCompleted.Add(1)
							}
						}
						_ = host.RemoveAll(ctx, tmpDir)
					}
				}
			}
		}()
	}

	// 6. Dynamic limits & capacity reconfiguration loop
	wg.Add(1)
	go func() {
		defer wg.Done()
		var step int
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			step++
			limitVal := 10 + (step%5)*8 // cycle between 10, 18, 26, 34, 42
			modelexecSrv.SetLimits([]model.AgentBackend{
				{
					ID:          "executor-stress-daemon-0-sh",
					Framework:   "sh",
					Concurrency: limitVal,
				},
				{
					ID:          "executor-stress-daemon-1-sh",
					Framework:   "sh",
					Concurrency: limitVal,
				},
			})

			time.Sleep(150 * time.Millisecond)
		}
	}()

	wg.Wait()

	t.Logf("Modelexec Stress Completed: %d successful execs, %d failed execs, %d abrupt cancels, %d kills, %d FS ops, %d MCP calls, %d MB pushed",
		successfulExecs.Load(), failedExecs.Load(), abruptCancellations.Load(), abruptKills.Load(),
		fsOpsCompleted.Load(), totalMCPCalls.Load(), totalBytesPushed.Load()/(1024*1024))

	if successfulExecs.Load() == 0 {
		t.Fatalf("Expected successful executions, got 0")
	}

	if startFDs > 0 {
		time.Sleep(500 * time.Millisecond)
		endFDs := countOpenFDs()
		t.Logf("Open FDs: start=%d, end=%d", startFDs, endFDs)
		if endFDs > startFDs+40 {
			t.Errorf("Potential FD leak in modelexec: start=%d, end=%d", startFDs, endFDs)
		}
	}
}

func tokenBytesGen(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}
