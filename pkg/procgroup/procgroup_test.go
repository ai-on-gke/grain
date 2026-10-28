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

//go:build !windows

package procgroup

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestPrepareKillsAChildProcessTooOnCancel proves the whole point of this
// package: exec.CommandContext's own default Cancel only signals the one
// process it started, so a shell command that backgrounds a child of its
// own would otherwise survive its parent's cancellation. Prepare puts the
// command in its own process group and kills that group instead, so both
// the shell and whatever it backgrounded die together.
func TestPrepareKillsAChildProcessTooOnCancel(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	marker := t.TempDir() + "/still-running"

	ctx, cancel := context.WithCancel(context.Background())
	// The backgrounded `sleep 30 & wait` loop writing marker in a
	// tight loop would be more direct, but a single long sleep,
	// backgrounded so bash itself returns control to its own shell
	// while the child lives on, is enough to prove the child was
	// never reached by a plain single-process kill.
	cmd := exec.CommandContext(ctx, "bash", "-c",
		"sleep 30 & echo $! > "+marker+".pid; wait")
	Prepare(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting command: %v", err)
	}

	// Give the backgrounded sleep a moment to actually start and write
	// its pid file before cancelling.
	var childPID int
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(marker + ".pid")
		if err == nil && len(data) > 0 {
			if _, scanErr := parsePID(string(data), &childPID); scanErr == nil && childPID > 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("backgrounded sleep never wrote a pid file in time")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	_ = cmd.Wait()

	// The child's own process group leader (this test's cmd) is gone;
	// signal 0 against the child's pid tells us whether it's still
	// alive without actually sending it a real signal.
	deadline = time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(childPID, 0)
		if err == syscall.ESRCH {
			return // the backgrounded child is gone too -- Prepare worked.
		}
		if time.Now().After(deadline) {
			t.Fatalf("backgrounded child pid %d is still alive after cancelling its parent -- Prepare did not reach it", childPID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// parsePID is a tiny fmt.Sscanf stand-in kept local to avoid pulling in
// fmt just for one integer parse in a test.
func parsePID(s string, out *int) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	*out = n
	if n == 0 {
		return 0, os.ErrInvalid
	}
	return 1, nil
}

func TestKillGuardsAndTerminates(t *testing.T) {
	if err := Kill(nil); err != nil {
		t.Errorf("Kill(nil) = %v, want nil", err)
	}
	if err := Kill(&exec.Cmd{}); err != nil {
		t.Errorf("Kill(&exec.Cmd{}) = %v, want nil", err)
	}
	if err := Kill(&exec.Cmd{Process: &os.Process{Pid: 0}}); err != nil {
		t.Errorf("Kill(pid 0) = %v, want nil", err)
	}
	if err := Kill(&exec.Cmd{Process: &os.Process{Pid: -1}}); err != nil {
		t.Errorf("Kill(pid -1) = %v, want nil", err)
	}

	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not installed")
	}
	cmd := exec.CommandContext(context.Background(), "sleep", "30")
	Prepare(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting command: %v", err)
	}
	pid := cmd.Process.Pid
	if err := Kill(cmd); err != nil {
		t.Errorf("Kill(running cmd) = %v, want nil", err)
	}
	_ = cmd.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d still alive after Kill", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
