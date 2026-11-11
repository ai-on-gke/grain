// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build ignore

// Command walk_hostexec_probe drives 8 concurrent HTTP clients polling a live
// `grain daemon` every 1s across 40s while its unix-socket `grain host` is
// SIGSTOPped, recording per-endpoint latency distributions and sampling both
// live daemon socket fds and listener accept-backlog entries in /proc/net/unix
// to verify stale-while-revalidate non-blocking behavior (PR #906).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type sample struct {
	Client   int           `json:"client"`
	Tick     int           `json:"tick"`
	Endpoint string        `json:"endpoint"`
	Status   int           `json:"status"`
	Latency  time.Duration `json:"latencyNs"`
	Ms       float64       `json:"ms"`
}

// inspectUnixProbes returns:
//   - inFlightDaemonFDs: open AF_UNIX stream fds held right now by daemonPID
//     that are waiting on an unaccepted (SIGSTOPped) listener (St == "03", Path == "",
//     Inode in /proc/<daemonPID>/fd -> socket:[ino], minus established IPC sockets)
//   - backlogCumulative: entries in /proc/net/unix bound to sockPath (or pkg/hostd's
//     shortened .h*/s symlink inside filepath.Dir(sockPath)) with Flags == "00000000".
//     Because the SIGSTOPped server never calls accept(), each background dial adds
//     exactly +1 entry to sk_receive_queue and stays queued until SIGCONT.
func inspectUnixProbes(sockPath string, daemonPID int) (inFlight int, backlog int) {
	raw, err := os.ReadFile("/proc/net/unix")
	if err != nil {
		return -1, -1
	}
	daemonInodes := make(map[string]bool)
	if daemonPID > 0 {
		entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", daemonPID))
		if err == nil {
			for _, e := range entries {
				target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", daemonPID, e.Name()))
				if err == nil && strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
					ino := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
					daemonInodes[ino] = true
				}
			}
		}
	}

	sockDir := filepath.Dir(sockPath)
	// First collect all unnamed client stream sockets (Type=0001, St=03, Path="") owned by daemonPID.
	// Note: daemonPID normally holds 0 unnamed AF_UNIX SOCK_STREAM client sockets except active hostd dials.
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		flags := fields[3]
		typ := fields[4]
		st := fields[5]
		ino := fields[6]
		p := ""
		if len(fields) >= 8 {
			p = fields[7]
		}
		if p == "" && typ == "0001" && st == "03" && daemonInodes[ino] {
			inFlight++
		}
		if p != "" && flags != "00010000" {
			if p == sockPath || (sockDir != "" && sockDir != "." && strings.HasPrefix(p, sockDir+"/")) {
				backlog++
			}
		}
	}
	return inFlight, backlog
}

func main() {
	baseURL := flag.String("base-url", "http://127.0.0.1:4284", "daemon base URL")
	token := flag.String("token", "", "Bearer API token")
	sockPath := flag.String("sock", "", "host unix socket path")
	daemonPID := flag.Int("daemon-pid", 0, "daemon PID for /proc/<pid>/fd inspection")
	clients := flag.Int("clients", 8, "number of concurrent polling clients")
	seconds := flag.Int("seconds", 40, "duration in seconds to poll")
	maxAllowedMs := flag.Float64("max-ms", 200.0, "maximum allowed latency in ms on hostexec endpoints")
	maxSettingsMs := flag.Float64("max-settings-ms", 1200.0, "maximum allowed latency in ms on /api/settings (which samples /proc/stat over 2x100ms)")
	includeHostEntries := flag.Bool("include-host-entries", false, "also poll /api/capability-servers/host-entries")
	flag.Parse()

	endpoints := []string{
		"/api/upgrade",
		"/api/settings",
		"/api/settings/cli-run",
		"/api/sandboxes",
	}
	if *includeHostEntries {
		endpoints = append(endpoints, "/api/capability-servers/host-entries")
	}

	baselineInFlight, baselineBacklog := inspectUnixProbes(*sockPath, *daemonPID)

	var (
		mu              sync.Mutex
		samples         []sample
		maxDeltaInFlght int
		maxStepBacklog  int
		finalBacklog    int
		connTimeline    []string
	)

	stopMonitor := make(chan struct{})
	var monWG sync.WaitGroup
	if *sockPath != "" && *daemonPID > 0 {
		monWG.Add(1)
		go func() {
			defer monWG.Done()
			start := time.Now()
			t := time.NewTicker(150 * time.Millisecond)
			defer t.Stop()
			prevIF, prevBL := -1, baselineBacklog
			for {
				select {
				case <-stopMonitor:
					return
				case <-t.C:
					rawIF, bl := inspectUnixProbes(*sockPath, *daemonPID)
					deltaIF := rawIF - baselineInFlight
					if deltaIF < 0 {
						deltaIF = 0
					}
					stepBL := bl - prevBL
					mu.Lock()
					if deltaIF > maxDeltaInFlght {
						maxDeltaInFlght = deltaIF
					}
					if prevBL >= 0 && stepBL > maxStepBacklog {
						maxStepBacklog = stepBL
					}
					finalBacklog = bl
					if deltaIF != prevIF || bl != prevBL {
						connTimeline = append(connTimeline, fmt.Sprintf("t=%5.2fs in_flight_probes=%d cumulative_backlog=%d",
							time.Since(start).Seconds(), deltaIF, bl))
						prevIF = deltaIF
						prevBL = bl
					}
					mu.Unlock()
				}
			}
		}()
	}

	var wg sync.WaitGroup
	startWall := time.Now()
	for c := 0; c < *clients; c++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()
			hc := &http.Client{Timeout: 10 * time.Second}
			for tick := 0; tick < *seconds; tick++ {
				targetTime := startWall.Add(time.Duration(tick)*time.Second + time.Duration(clientID)*100*time.Millisecond)
				if d := time.Until(targetTime); d > 0 {
					time.Sleep(d)
				}
				for _, ep := range endpoints {
					req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, *baseURL+ep, nil)
					if *token != "" {
						req.Header.Set("Authorization", "Bearer "+*token)
					}
					t0 := time.Now()
					resp, err := hc.Do(req)
					dt := time.Since(t0)
					status := 0
					if err == nil {
						status = resp.StatusCode
						_, _ = io.Copy(io.Discard, resp.Body)
						resp.Body.Close()
					}
					mu.Lock()
					samples = append(samples, sample{
						Client:   clientID,
						Tick:     tick,
						Endpoint: ep,
						Status:   status,
						Latency:  dt,
						Ms:       float64(dt.Microseconds()) / 1000.0,
					})
					mu.Unlock()
				}
			}
		}(c)
	}
	wg.Wait()
	close(stopMonitor)
	monWG.Wait()

	byEP := make(map[string][]float64)
	var slow []sample
	badStatus := 0
	for _, s := range samples {
		byEP[s.Endpoint] = append(byEP[s.Endpoint], s.Ms)
		if s.Status != http.StatusOK {
			badStatus++
		}
		limit := *maxAllowedMs
		if s.Endpoint == "/api/settings" {
			limit = *maxSettingsMs
		}
		if s.Ms > limit {
			slow = append(slow, s)
		}
	}

	totalRefreshDials := finalBacklog - baselineBacklog
	var report bytes.Buffer
	fmt.Fprintf(&report, "Total requests: %d (%d clients x %d ticks x %d endpoints)\n",
		len(samples), *clients, *seconds, len(endpoints))
	fmt.Fprintf(&report, "Non-200 responses: %d\n", badStatus)
	fmt.Fprintf(&report, "Max simultaneous in-flight background probes: %d (max backlog step=%d, total refresh dials=%d)\n",
		maxDeltaInFlght, maxStepBacklog, totalRefreshDials)
	for _, line := range connTimeline {
		fmt.Fprintf(&report, "  %s\n", line)
	}
	for _, ep := range endpoints {
		vals := byEP[ep]
		sort.Float64s(vals)
		if len(vals) == 0 {
			continue
		}
		p50 := vals[len(vals)*50/100]
		p95 := vals[len(vals)*95/100]
		p99 := vals[len(vals)*99/100]
		maxV := vals[len(vals)-1]
		fmt.Fprintf(&report, "Endpoint %-38s count=%4d p50=%6.2fms p95=%6.2fms p99=%6.2fms max=%6.2fms\n",
			ep, len(vals), p50, p95, p99, maxV)
	}
	fmt.Print(report.String())

	if len(slow) > 0 || badStatus > 0 || maxDeltaInFlght > 1 || maxStepBacklog > 1 {
		b, _ := json.MarshalIndent(map[string]any{
			"slowCount":      len(slow),
			"firstSlow":      slow[:min(10, len(slow))],
			"badStatus":      badStatus,
			"maxInFlight":    maxDeltaInFlght,
			"maxStepBacklog": maxStepBacklog,
			"refreshDials":   totalRefreshDials,
		}, "", "  ")
		fmt.Printf("FAILURE DETAILS:\n%s\n", string(b))
		os.Exit(1)
	}
	fmt.Println("PASS: all hostexec endpoints completed < 200ms with <= 1 detached background probe per refresh cycle!")
}
