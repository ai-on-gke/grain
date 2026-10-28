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

package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// A step runner that recorded its exit has ended, so the pid it left on
// disk is not signalled: by the time its sandbox is released the kernel
// may have handed that pid to something else.
func TestKillStepRunnerSparesAFinishedRunnersPID(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	dir := filepath.Join(root, ".grain-ci-step", "a1-s0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "exit"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	killDetachedStepRunners(root)
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the process behind a finished runner's stale pid was killed: %v", err)
	}
}

// The pid file is in the sandbox, where the job can write any pid it
// likes: one that is not the runner the file names -- a process of the
// daemon's, or 1, which KillPID would turn into kill(-1) -- is not
// signalled.
func TestKillStepRunnerSparesAPIDThatIsNotItsRunner(t *testing.T) {
	if _, err := os.Stat("/proc/self/cmdline"); err != nil {
		t.Skip("no /proc to tell a runner from another process")
	}
	root := t.TempDir()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	for i, pid := range []int{cmd.Process.Pid, 1, os.Getpid()} {
		dir := filepath.Join(root, ".grain-ci-step", "a1-s"+strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	killDetachedStepRunners(root)
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("a process a forged pid file named was killed: %v", err)
	}
}

// A runner that is still going when its sandbox is released is killed:
// the check above must not spare the very process it is there for.
func TestKillStepRunnerKillsItsOwnRunner(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".grain-ci-step", "a1-s0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", "sleep 30", "_", dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	killDetachedStepRunners(root)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a live step runner was not killed when its sandbox was released")
	}
}
