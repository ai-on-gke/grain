package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func TestMCPStressChild(t *testing.T) {
	if os.Getenv("GRAIN_MCP_STRESS_CHILD") != "1" {
		return
	}
	registry := NewRegistry()
	registry.Register(Tool{
		Name:        "echo",
		Description: "Echo back text",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(_ context.Context, args map[string]any) Result {
			txt, _ := args["text"].(string)
			return Result{Text: txt}
		},
	})
	registry.Register(Tool{
		Name:        "compute_hash",
		Description: "Compute hex representation of payload",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(_ context.Context, args map[string]any) Result {
			data, _ := args["data"].(string)
			return Result{Text: fmt.Sprintf("len=%d", len(data))}
		},
	})
	_ = Serve(context.Background(), registry, bufio.NewReader(os.Stdin), bufio.NewWriter(os.Stdout))
	os.Exit(0)
}

// TestMCPHighConcurrencyToolInvocationAndManagedHTTPStress tests hundreds of concurrent
// tool calls, high-throughput payload blasting, and rapid instance lifecycles.
func TestMCPHighConcurrencyToolInvocationAndManagedHTTPStress(t *testing.T) {
	t.Setenv("GRAIN_MCP_STRESS_CHILD", "1")

	dur := stressDuration(t, 5*time.Second)
	t.Logf("Running MCP stress test for %v", dur)

	startFDs := countOpenFDs()

	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()

	// 1. Start long-lived ManagedHTTP server
	server, err := StartCommandHTTP(ctx, os.Args[0], []string{"-test.run=^TestMCPStressChild$"})
	if err != nil {
		t.Fatalf("StartCommandHTTP: %v", err)
	}
	defer server.Close()

	httpClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        500,
			MaxIdleConnsPerHost: 200,
			IdleConnTimeout:     30 * time.Second,
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		},
		Timeout: 10 * time.Second,
	}

	var (
		totalCalls       atomic.Int64
		failedCalls      atomic.Int64
		totalBytes       atomic.Int64
		abruptCancels    atomic.Int64
		shortLivedSpawns atomic.Int64
		wg               sync.WaitGroup
	)

	concurrency := 40
	if dur > 10*time.Second {
		concurrency = 80
	}

	// 2. High concurrency tool workers blasting parallel calls
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var reqID int64

			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				reqID++
				// Random payload between 1KB and 64KB
				payloadLen := 1024 + (int(reqID*73)%64)*1024
				raw := make([]byte, payloadLen)
				_, _ = rand.Read(raw)
				payloadStr := hex.EncodeToString(raw[:payloadLen/2])

				reqBody, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      reqID,
					"method":  "tools/call",
					"params": map[string]any{
						"name": "echo",
						"arguments": map[string]any{
							"text": payloadStr,
						},
					},
				})

				req, err := http.NewRequestWithContext(ctx, "POST", server.URL, bytes.NewReader(reqBody))
				if err != nil {
					return
				}
				req.Header.Set("Authorization", "Bearer "+server.Token)
				req.Header.Set("Content-Type", "application/json")

				resp, err := httpClient.Do(req)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					failedCalls.Add(1)
					continue
				}

				body, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()

				if err != nil || resp.StatusCode != http.StatusOK {
					if ctx.Err() == nil {
						failedCalls.Add(1)
					}
					continue
				}

				var rpcResp struct {
					Result struct {
						Content []struct {
							Type string `json:"type"`
							Text string `json:"text"`
						} `json:"content"`
					} `json:"result"`
				}
				if err := json.Unmarshal(body, &rpcResp); err == nil && len(rpcResp.Result.Content) > 0 {
					if rpcResp.Result.Content[0].Text == payloadStr {
						totalCalls.Add(1)
						totalBytes.Add(int64(len(payloadStr) * 2))
					} else {
						failedCalls.Add(1)
					}
				} else {
					failedCalls.Add(1)
				}
			}
		}()
	}

	// 3. Short-lived server spawn & abrupt cancellation loop
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			subCtx, subCancel := context.WithTimeout(ctx, 300*time.Millisecond)
			subSrv, err := StartCommandHTTP(subCtx, os.Args[0], []string{"-test.run=^TestMCPStressChild$"})
			if err == nil {
				shortLivedSpawns.Add(1)
				// Make a quick call then abruptly cancel
				reqBody, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      1,
					"method":  "tools/call",
					"params": map[string]any{
						"name": "echo",
						"arguments": map[string]any{
							"text": "quick-burst",
						},
					},
				})
				req, _ := http.NewRequestWithContext(subCtx, "POST", subSrv.URL, bytes.NewReader(reqBody))
				req.Header.Set("Authorization", "Bearer "+subSrv.Token)
				req.Header.Set("Content-Type", "application/json")
				if resp, err := httpClient.Do(req); err == nil {
					_, _ = io.ReadAll(resp.Body)
					_ = resp.Body.Close()
				}
				subCancel()
				subSrv.Close()
				abruptCancels.Add(1)
			} else {
				subCancel()
			}

			time.Sleep(50 * time.Millisecond)
		}
	}()

	// 4. Concurrently blast dialExec stdio stream pumps with rapid cancellations
	execConcurrency := 10
	for i := 0; i < execConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				// Dial a shell script executing a high volume stream
				runCtx, runCancel := context.WithTimeout(ctx, 400*time.Millisecond)
				conn, err := dialExec(runCtx, "sh", []string{"-c", "cat"}, []string{"grain-connect", "/run/grain/lifecycle.sock"})
				if err != nil {
					runCancel()
					continue
				}

				// Blast bytes into stdin while reading
				blastData := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz0123456789\n"), 1024) // 37KB
				go func() {
					_, _ = conn.Write(blastData)
				}()

				buf := make([]byte, 8192)
				for {
					_ = conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
					n, rErr := conn.Read(buf)
					if n > 0 {
						totalBytes.Add(int64(n))
					}
					if rErr != nil {
						break
					}
				}

				_ = conn.Close()
				runCancel()
			}
		}()
	}

	wg.Wait()

	t.Logf("MCP Stress Completed: %d successful tool calls, %d failed calls, %d MB transferred, %d short-lived spawns",
		totalCalls.Load(), failedCalls.Load(), totalBytes.Load()/(1024*1024), shortLivedSpawns.Load())

	if totalCalls.Load() == 0 {
		t.Fatalf("Expected successful tool calls, got 0")
	}

	if startFDs > 0 {
		time.Sleep(200 * time.Millisecond)
		endFDs := countOpenFDs()
		t.Logf("Open FDs: start=%d, end=%d", startFDs, endFDs)
		if endFDs > startFDs+30 {
			t.Errorf("Potential FD leak detected: start=%d, end=%d", startFDs, endFDs)
		}
	}
}
