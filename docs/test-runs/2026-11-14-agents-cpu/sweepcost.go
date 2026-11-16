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

// sweepcost times one sysstat.ProcessTreeCPU sweep of the host's real
// /proc (docs/test-plans/2026-11-14.md §5 item 3), after starting -extra
// idle processes so the host holds about the count the plan names.
//
//	go run ./docs/test-runs/2026-11-14-agents-cpu/sweepcost.go -extra 450 -n 50
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"time"

	"github.com/ai-on-gke/grain/pkg/sysstat"
)

func main() {
	extra := flag.Int("extra", 450, "idle processes to start first")
	n := flag.Int("n", 50, "sweeps to time")
	flag.Parse()
	dir, _ := os.MkdirTemp("", "sweep-")
	defer os.RemoveAll(dir)
	var procs []*exec.Cmd
	for i := 0; i < *extra; i++ {
		c := exec.Command("sleep", "600")
		c.Dir = dir
		if err := c.Start(); err != nil {
			panic(err)
		}
		procs = append(procs, c)
	}
	defer func() {
		for _, c := range procs {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
	}()
	entries, _ := os.ReadDir("/proc")
	total := 0
	for _, e := range entries {
		if e.Name()[0] >= '0' && e.Name()[0] <= '9' {
			total++
		}
	}
	roots := map[string]string{"5-1": dir, "5-10": dir + "0", "6-1": "/nonexistent/6-1"}
	var took []time.Duration
	for i := 0; i < *n; i++ {
		t0 := time.Now()
		got, err := sysstat.ProcessTreeCPU(roots)
		if err != nil {
			panic(err)
		}
		took = append(took, time.Since(t0))
		if i == 0 {
			fmt.Printf("processes on host: %d; attributed roots: %v\n", total, got)
		}
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	fmt.Printf("sweeps=%d p50=%v p90=%v max=%v\n", *n, took[*n/2], took[*n*9/10], took[*n-1])
}
