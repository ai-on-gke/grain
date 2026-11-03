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

package orchestrator_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// TestGranuleVMMemoryHeadroomOnARealVM boots a 2 GiB guest the way the
// daemon does (GranuleSandboxes.Acquire) and checks the two things the
// headroom arithmetic is for, on the real VM rather than on createArgs:
// the VM container's cgroup memory.max is guest + max(1 GiB, 25%) -- 3
// GiB here -- and the VMM survives a guest that fills its RAM with page
// cache and then writes a multi-GB file, the workload that OOM-killed it
// at guest + 512 MiB (model.VMMMemoryHeadroomPct).
//
// Opt-in like the rest of the real-VM suite (GRAIN_REAL_VM_TESTS), and
// not in the real-vm job's -run list: the write is several GB of disk
// and minutes of IO a hosted runner has better uses for.
// docs/test-runs/2026-11-03-sandbox-resources.md is the walkthrough that
// ran it.
func TestGranuleVMMemoryHeadroomOnARealVM(t *testing.T) {
	granuleDockerRealTestPrereqs(t)

	granuleDir := buildGranule(t)
	image := buildGranuleGuestImage(t)
	t.Setenv("PATH", granuleDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateDir := t.TempDir()
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir: stateDir,
		// The same flags TestGranuleSandboxesAgainstARealDockerBackedVM
		// passes, for the reasons given there.
		CreateArgs:        []string{"-kontur-image", image, "-guest-port", "22", "-guest-user", "debian", "-disk-mode=overlay"},
		SSHUser:           "debian",
		Workspace:         "/home/debian",
		ReadyTimeout:      3 * time.Minute,
		ReadyPollInterval: time.Second,
	})
	name, err := k.VMNameFor("mem-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := granule.Delete(context.Background(), stateDir, name); err != nil {
			t.Logf("cleaning up real granule VM %q: %v", name, err)
		}
	})

	const guestMB = 2048
	sb, err := k.Acquire(context.Background(), "mem-1", orchestrator.Shape{CPUs: 2, MemoryMB: guestMB, DiskGB: 12})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	tools, err := sb.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var run *mcp.Tool
	for i := range tools {
		if tools[i].Name == "run_command" {
			run = &tools[i]
		}
	}
	if run == nil {
		t.Fatal("no run_command tool")
	}

	container := "granule-vm-" + name
	cgroup := containerCgroupDir(t, container)
	wantMax := int64(orchestrator.GranuleMemoryLimitMB(guestMB, 0)) << 20
	if wantMax != 3<<30 {
		t.Fatalf("GranuleMemoryLimitMB(%d, 0) = %d MiB, want 3072", guestMB, wantMax>>20)
	}
	if got := readCgroupInt(t, filepath.Join(cgroup, "memory.max")); got != wantMax {
		t.Fatalf("%s memory.max = %d, want %d (guest %d MiB + max(1 GiB, 25%%))", container, got, wantMax, guestMB)
	}
	t.Logf("%s memory.max = %d (%d MiB)", container, wantMax, wantMax>>20)

	// Fill the guest's page cache, then write 4 GiB -- twice the guest's
	// RAM, more than the whole cgroup -- and read it back so the host side
	// pages it in through the VMM as well.
	for _, step := range []string{
		"dd if=/dev/zero of=/home/debian/fill bs=1M count=1536 status=none && cat /home/debian/fill >/dev/null && free -m",
		"dd if=/dev/urandom of=/home/debian/big bs=1M count=4096 conv=fsync status=none && free -m",
		"cat /home/debian/big >/dev/null && rm -f /home/debian/fill /home/debian/big && sync && echo survived",
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		res := run.Handler(ctx, map[string]any{"command": step, "timeout": 900000})
		cancel()
		if res.IsError {
			t.Fatalf("guest step %q failed -- was the VMM OOM-killed? (memory.events: %s): %s",
				step, readCgroupFile(t, filepath.Join(cgroup, "memory.events")), res.Text)
		}
		t.Logf("guest step %q:\n%s", step, res.Text)
	}

	state, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}} {{.State.OOMKilled}}", container).CombinedOutput()
	if err != nil || strings.TrimSpace(string(state)) != "true false" {
		t.Fatalf("%s after the write: running/OOMKilled = %q (%v), want \"true false\"", container, state, err)
	}
	events := readCgroupFile(t, filepath.Join(cgroup, "memory.events"))
	if strings.Contains(events, "oom_kill ") && !strings.Contains(events, "oom_kill 0") {
		t.Fatalf("%s memory.events = %q, want no oom_kill", container, events)
	}
	t.Logf("%s memory.events:\n%s", container, events)
	if peak, err := os.ReadFile(filepath.Join(cgroup, "memory.peak")); err == nil {
		t.Logf("%s memory.peak = %s", container, strings.TrimSpace(string(peak)))
	}
}

// containerCgroupDir is the cgroup v2 directory of a docker container's
// init process, read from /proc rather than guessed from the cgroup
// driver's naming.
func containerCgroupDir(t *testing.T, container string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Pid}}", container).Output()
	if err != nil {
		t.Fatalf("docker inspect %s: %v", container, err)
	}
	data, err := os.ReadFile("/proc/" + strings.TrimSpace(string(out)) + "/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok {
			return filepath.Join("/sys/fs/cgroup", path)
		}
	}
	t.Fatalf("no cgroup v2 entry in %q", data)
	return ""
}

func readCgroupFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func readCgroupInt(t *testing.T, path string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(readCgroupFile(t, path), 10, 64)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return n
}
