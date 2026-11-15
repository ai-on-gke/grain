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

// edges drives the real orchestrator.HostSandboxes CPU sampler over real
// processes for the edge cases of docs/test-plans/2026-11-14.md §5 item 1:
// run directories that are prefixes of each other, a child that cd's to
// /tmp, a double-forked process reparented away from the run, and a burst
// of short-lived reaped children whose parent exits mid-window.
//
//	go run ./docs/test-runs/2026-11-14-agents-cpu/edges.go [-secs 20]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

type scenario struct {
	name string
	// cmds maps sandbox name to the bash command started in its root.
	cmds map[string]string
	want string
}

func main() {
	secs := flag.Int("secs", 20, "seconds each scenario runs")
	flag.Parse()
	base, err := os.MkdirTemp("", "edges-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(base)
	d := fmt.Sprint(*secs)
	// Each command runs under a bash that stays in the run's root, as a
	// real run's agent process does.
	scenarios := []scenario{
		{"prefix dirs", map[string]string{
			"5-1":  "stress-ng --cpu 1 --cpu-method fft --timeout " + d + "s --quiet",
			"5-10": "stress-ng --cpu 2 --cpu-method fft --timeout " + d + "s --quiet",
		}, "5-1 ~1, 5-10 ~2"},
		{"child cds to /tmp", map[string]string{
			"5-1": "sh -c 'cd /tmp && exec stress-ng --cpu 1 --cpu-method fft --timeout " + d + "s --quiet'; true",
		}, "5-1 ~1"},
		{"double fork, cwd kept", map[string]string{
			"5-1": "(setsid stress-ng --cpu 1 --cpu-method fft --timeout " + d + "s --quiet </dev/null >/dev/null 2>&1 &); exit 0",
		}, "5-1 ~1 (cwd still in the run)"},
		{"double fork, cd /tmp", map[string]string{
			"5-1": "(cd /tmp && setsid stress-ng --cpu 1 --cpu-method fft --timeout " + d + "s --quiet </dev/null >/dev/null 2>&1 &); sleep " + d,
		}, "lost: the orphan left both the directory and the tree (the sleep that started it reads ~0)"},
		{"burst of short children, parent exits half way", map[string]string{
			"5-1": "end=$((SECONDS+" + fmt.Sprint(*secs/2) + ")); while [ $SECONDS -lt $end ]; do sh -c 'i=0; while [ $i -lt 3000 ]; do i=$((i+1)); done'; done",
		}, "5-1 ~1 while the loop runs, then absent; never negative or > 1.2"},
	}
	for _, sc := range scenarios {
		run(base, sc, *secs)
	}
}

func run(base string, sc scenario, secs int) {
	fmt.Printf("== %s (want %s)\n", sc.name, sc.want)
	hs := orchestrator.NewHostSandboxes(filepath.Join(base, strings.ReplaceAll(sc.name, " ", "_")))
	ctx := context.Background()
	var procs []*exec.Cmd
	for name, cmd := range sc.cmds {
		sb, err := hs.Acquire(ctx, name, orchestrator.Shape{})
		if err != nil {
			panic(err)
		}
		c := exec.Command("bash", "-c", cmd)
		root, err := sb.(interface{ Root() (string, error) }).Root()
		if err != nil {
			panic(err)
		}
		c.Dir = root
		if err := c.Start(); err != nil {
			panic(err)
		}
		procs = append(procs, c)
		go c.Wait()
	}
	start := time.Now()
	for time.Since(start) < time.Duration(secs)*time.Second {
		time.Sleep(2 * time.Second)
		var parts []string
		for _, h := range hs.Health(ctx) {
			v := "absent"
			if h.CPUCores != nil {
				v = fmt.Sprintf("%.3f/%dms", *h.CPUCores, h.CPUWindow.Milliseconds())
			}
			parts = append(parts, h.Sandbox+"="+v)
		}
		fmt.Printf("  t=%4.1fs %s\n", time.Since(start).Seconds(), strings.Join(parts, " "))
	}
	for _, c := range procs {
		_ = c.Process.Kill()
	}
	exec.Command("pkill", "-f", "^stress-ng").Run()
	time.Sleep(500 * time.Millisecond)
}
