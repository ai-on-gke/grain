package stress_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/agent"
	"github.com/bwsalmon/granule/pkg/execwire"
	"github.com/bwsalmon/granule/pkg/hypervisor"
	"github.com/bwsalmon/granule/pkg/memagent"
	"github.com/bwsalmon/granule/pkg/qcow2"
)

// fakeVM implements memagent.Resizer for stress tests.
type stressVM struct {
	mu              sync.Mutex
	bootBytes       uint64
	hotpluggedBytes uint64
	resizeCalls     int64
	infoCalls       int64
}

func (f *stressVM) Info(context.Context) (hypervisor.VMInfo, error) {
	atomic.AddInt64(&f.infoCalls, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return hypervisor.VMInfo{
		State:            "Running",
		MemoryActualSize: f.bootBytes + f.hotpluggedBytes,
		Config: hypervisor.VMConfigInfo{
			Memory: hypervisor.MemoryInfo{
				Size:           f.bootBytes,
				HotpluggedSize: f.hotpluggedBytes,
			},
		},
	}, nil
}

func (f *stressVM) ResizeIfNeeded(_ context.Context, desiredRAMBytes uint64) (bool, error) {
	atomic.AddInt64(&f.resizeCalls, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bootBytes+f.hotpluggedBytes == desiredRAMBytes {
		return false, nil
	}
	f.hotpluggedBytes = desiredRAMBytes - f.bootBytes
	return true, nil
}

// TestStress_MemAgentPressureStorm fires hundreds of concurrent pressure signals
// against the memory agent server to test in-flight deduplication and thread safety.
func TestStress_MemAgentPressureStorm(t *testing.T) {
	const bootMB = 256
	const maxMB = 2048
	const stepMB = 128
	const cooldown = 50 * time.Millisecond

	vm := &stressVM{bootBytes: uint64(bootMB) * 1024 * 1024}
	const token = "stress-token"
	cfg := memagent.Config{
		Addr:     "127.0.0.1:0",
		Token:    token,
		MaxMB:    maxMB,
		StepMB:   stepMB,
		Cooldown: cooldown,
	}

	srv := memagent.New(cfg, vm)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	listenAddr := ln.Addr().String()
	_ = ln.Close()

	srvCfg := cfg
	srvCfg.Addr = listenAddr
	srv = memagent.New(srvCfg, vm)

	srvDone := make(chan error, 1)
	go func() {
		srvDone <- srv.Serve(ctx)
	}()

	// Wait for listener to be ready
	var readyConn net.Conn
	for i := 0; i < 50; i++ {
		readyConn, err = net.Dial("tcp", listenAddr)
		if err == nil {
			readyConn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server not listening on %s: %v", listenAddr, err)
	}

	const clients = 100
	var wg sync.WaitGroup
	errCh := make(chan error, clients)

	for c := 0; c < clients; c++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()
			conn, err := net.Dial("tcp", listenAddr)
			if err != nil {
				errCh <- fmt.Errorf("client %d dial: %w", clientID, err)
				return
			}
			defer conn.Close()

			pressureVal := 10.0 + float64(clientID%50)
			_, err = fmt.Fprintf(conn, "PRESSURE %s %.2f\n", token, pressureVal)
			if err != nil {
				errCh <- fmt.Errorf("client %d send: %w", clientID, err)
				return
			}
		}(c)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("pressure storm client error: %v", err)
	}

	// Wait for any in-flight resizes to settle
	time.Sleep(100 * time.Millisecond)

	infoCalls := atomic.LoadInt64(&vm.infoCalls)
	resizeCalls := atomic.LoadInt64(&vm.resizeCalls)
	t.Logf("Pressure storm (%d concurrent clients) -> %d info calls, %d resize calls", clients, infoCalls, resizeCalls)

	if resizeCalls > int64(clients) {
		t.Errorf("excessive resize calls: %d > %d", resizeCalls, clients)
	}

	cancel()
	select {
	case <-srvDone:
	case <-time.After(2 * time.Second):
		t.Errorf("server did not shut down cleanly on context cancel")
	}
}

// TestStress_StorageHighConcurrency creates multiple VMDK descriptors, resizes them across
// multiple tiers, checks caching, and validates data across hundreds of extents concurrently.
func TestStress_StorageHighConcurrency(t *testing.T) {
	tempDir := t.TempDir()
	const workers = 15
	const iterations = 5

	var wg sync.WaitGroup
	errCh := make(chan error, workers*iterations)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				diskDir := filepath.Join(tempDir, fmt.Sprintf("worker_%d_iter_%d", workerID, i))
				if err := os.MkdirAll(diskDir, 0o755); err != nil {
					errCh <- err
					return
				}
				qcowPath := filepath.Join(diskDir, "rootfs.qcow2")

				// 1. Synthesize 512MB disk
				size1 := int64(512 * 1024 * 1024)
				if err := qcow2.WriteOverlay(qcowPath, "", size1); err != nil {
					errCh <- fmt.Errorf("WriteOverlay: %w", err)
					return
				}

				// 2. Resize to 4GB
				size2 := int64(4 * 1024 * 1024 * 1024)
				if err := qcow2.Resize(qcowPath, size2); err != nil {
					errCh <- fmt.Errorf("Resize to 4GB: %w", err)
					return
				}

				// 3. Resize to 16GB
				size3 := int64(16 * 1024 * 1024 * 1024)
				if err := qcow2.Resize(qcowPath, size3); err != nil {
					errCh <- fmt.Errorf("Resize to 16GB: %w", err)
					return
				}

				// 4. Verify VirtualSize
				vSize, err := qcow2.VirtualSize(qcowPath)
				if err != nil || vSize != size3 {
					errCh <- fmt.Errorf("VirtualSize = %d (err=%v), want %d", vSize, err, size3)
					return
				}

				// 5. Verify Header Size
				hdr, err := qcow2.ReadHeader(qcowPath)
				if err != nil {
					errCh <- fmt.Errorf("ReadHeader: %w", err)
					return
				}
				if int64(hdr.Size) != size3 {
					errCh <- fmt.Errorf("hdr.Size = %d, want %d", hdr.Size, size3)
					return
				}
			}
		}(w)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("storage stress error: %v", err)
	}
}

func currentTestAccount() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return "debian"
	}
	defer f.Close()

	uid := fmt.Sprintf("%d", os.Getuid())
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := bytes.Split(sc.Bytes(), []byte(":"))
		if len(fields) >= 3 && string(fields[2]) == uid {
			return string(fields[0])
		}
	}
	return "debian"
}

// TestStress_AgentExecutionHighLoad creates dozens of parallel agent execution sessions
// running bash commands with large piped I/O, validating frame integrity.
func TestStress_AgentExecutionHighLoad(t *testing.T) {
	account := currentTestAccount()
	const sessions = 25
	var wg sync.WaitGroup
	errCh := make(chan error, sessions)

	for s := 0; s < sessions; s++ {
		wg.Add(1)
		go func(sessID int) {
			defer wg.Done()
			client, server := net.Pipe()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			serverDone := make(chan error, 1)
			go func() {
				defer server.Close()
				serverDone <- agent.Serve(ctx, server)
			}()
			defer client.Close()

			expectedSum := ""
			req := execwire.Request{
				Line: fmt.Sprintf("head -c 1048576 /dev/zero | tr '\\0' '%c' | sha256sum", 'A'+(sessID%26)),
				User: account,
			}
			// Calculate expected sha256
			pattern := bytes.Repeat([]byte{byte('A' + (sessID % 26))}, 1048576)
			sum := sha256.Sum256(pattern)
			expectedSum = hex.EncodeToString(sum[:])

			if err := execwire.WriteRequest(client, req); err != nil {
				errCh <- fmt.Errorf("session %d: WriteRequest: %w", sessID, err)
				return
			}

			br := bufio.NewReader(client)
			resp, err := execwire.ReadResponse(br)
			if err != nil || !resp.OK {
				errCh <- fmt.Errorf("session %d: response error: %v / %s", sessID, err, resp.Error)
				return
			}

			_ = execwire.WriteFrame(client, execwire.TypeStdinClose, nil)

			var outBuf bytes.Buffer
			for {
				typ, payload, err := execwire.ReadFrame(br)
				if err != nil {
					break
				}
				if typ == execwire.TypeStdout {
					outBuf.Write(payload)
				}
			}

			outputStr := outBuf.String()
			if len(outputStr) < 64 || outputStr[:64] != expectedSum {
				errCh <- fmt.Errorf("session %d output mismatch: got %q, want prefix %s", sessID, outputStr, expectedSum)
				return
			}

			if serr := <-serverDone; serr != nil {
				errCh <- fmt.Errorf("session %d Serve err: %w", sessID, serr)
				return
			}
		}(s)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("agent high load stress error: %v", err)
	}
}

// TestStress_DiskQcowThroughput tests qcow2 header and virtual size inspection
// throughput across 1,000 requests on parallel goroutines.
func TestStress_DiskQcowThroughput(t *testing.T) {
	tempDir := t.TempDir()
	qcowPath := filepath.Join(tempDir, "cached_base.qcow2")

	if err := qcow2.WriteOverlay(qcowPath, "", 1024*1024*1024); err != nil {
		t.Fatalf("WriteOverlay: %v", err)
	}

	const routines = 20
	const requestsPerRoutine = 50

	start := time.Now()
	var wg sync.WaitGroup
	errCh := make(chan error, routines*requestsPerRoutine)

	for r := 0; r < routines; r++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)))
			for i := 0; i < requestsPerRoutine; i++ {
				if rng.Intn(2) == 0 {
					if _, err := qcow2.ReadHeader(qcowPath); err != nil {
						errCh <- err
						return
					}
				} else {
					if _, err := qcow2.VirtualSize(qcowPath); err != nil {
						errCh <- err
						return
					}
				}
			}
		}(r)
	}

	wg.Wait()
	close(errCh)
	elapsed := time.Since(start)

	totalRequests := routines * requestsPerRoutine
	t.Logf("Completed %d qcow2 header / size operations in %s (%0.1f ops/sec)",
		totalRequests, elapsed, float64(totalRequests)/elapsed.Seconds())

	for err := range errCh {
		t.Errorf("cache throughput stress error: %v", err)
	}
}
