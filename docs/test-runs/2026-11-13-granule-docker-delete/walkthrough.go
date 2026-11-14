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

// Command walkthrough executes §4 ("Granule ccdbc4b re-vendor and real-Docker
// concurrent vm delete settlement") of docs/test-plans/2026-11-13-afternoon.md
// against a live Docker Engine (dockerd) and the vendored granule CLI built
// from github.com/gke-labs/granule/cmd/granule.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/granule"
)

const (
	testImageTag = "grain-walkthrough-granule-delete:local"
	vmPrefix     = "wt5486-"
)

const containerInitSource = `package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func main() {
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	files, _ := strconv.Atoi(os.Getenv("OVERLAY_FILES"))
	if files > 0 {
		_ = os.MkdirAll("/work", 0o755)
		buf := make([]byte, 4096)
		for i := 0; i < files; i++ {
			dir := filepath.Join("/work", fmt.Sprintf("d%03d", i%128))
			if i < 128 {
				_ = os.MkdirAll(dir, 0o755)
			}
			_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%06d", i)), buf, 0o644)
		}
	}
	fmt.Println("READY")

	if os.Getenv("IGNORE_TERM") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	<-sigCh
}
`

type harness struct {
	workDir    string
	granuleBin string
	realDocker string
	createdVMs []string
	mu         sync.Mutex
	failures   []string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	realDocker, err := exec.LookPath("docker")
	if err != nil {
		fatalf("locating docker CLI: %v", err)
	}
	workDir, err := os.MkdirTemp("", "granule-docker-walkthrough-*")
	if err != nil {
		fatalf("creating scratch dir: %v", err)
	}

	h := &harness{
		workDir:    workDir,
		granuleBin: filepath.Join(workDir, "granule"),
		realDocker: realDocker,
	}
	defer h.cleanup()

	fmt.Println("=== Building vendored granule CLI and scratch test image ===")
	if err := h.buildGranule(ctx); err != nil {
		fatalf("building granule CLI: %v", err)
	}
	if err := h.buildTestImage(ctx); err != nil {
		fatalf("building scratch test image: %v", err)
	}

	h.runCase("2a-simultaneous: two concurrent Command.Delete calls (simultaneous entry)", h.caseConcurrentCommandDeleteSimultaneous)
	h.runCase("2a-removing: Command.Delete entering while another Command.Delete has container in removing state", h.caseConcurrentCommandDeleteWhileRemoving)
	h.runCase("2b: direct docker rm -f racing Command.Delete (orphan-sweep vs run-release)", h.caseDockerRmRacingCommandDelete)
	h.runCase("2c: concurrent granule.RemoveContainers calls racing docker rm -f", h.caseConcurrentRemoveContainers)
	h.runCase("3a: short ctx timeout on Command.Delete during real dockerd in-progress removal, then uncancelled cleanup", h.caseTimeoutDuringRealRemoval)
	h.runCase("3b: short ctx timeout on Command.Delete via passthrough docker shim while container stays running, then uncancelled cleanup", h.caseTimeoutViaPassthroughShim)

	fmt.Println()
	if len(h.failures) > 0 {
		fmt.Printf("SUMMARY: %d case(s) FAILED:\n", len(h.failures))
		for _, f := range h.failures {
			fmt.Printf("  - %s\n", f)
		}
		os.Exit(1)
	}
	fmt.Println("SUMMARY: ALL CASES PASSED")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(2)
}

func (h *harness) runCase(name string, fn func(context.Context) error) {
	fmt.Printf("\n--- CASE %s ---\n", name)
	start := time.Now()
	if err := fn(context.Background()); err != nil {
		fmt.Printf("RESULT: FAIL (%s): %v\n", time.Since(start).Round(time.Millisecond), err)
		h.failures = append(h.failures, fmt.Sprintf("%s: %v", name, err))
		return
	}
	fmt.Printf("RESULT: PASS (%s)\n", time.Since(start).Round(time.Millisecond))
}

func (h *harness) trackVM(vmName string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.createdVMs = append(h.createdVMs, vmName)
}

func (h *harness) cleanup() {
	h.mu.Lock()
	vms := append([]string(nil), h.createdVMs...)
	h.mu.Unlock()

	for _, vm := range vms {
		pod := granule.PodName(vm)
		_ = exec.Command(h.realDocker, "rm", "-f", pod, pod+"-netns").Run()
	}
	// Sweep any leftover containers with our walkthrough label just in case.
	out, err := exec.Command(h.realDocker, "ps", "-aq", "--filter", "label=walkthrough.grain.dev/task=5486").Output()
	if err == nil {
		ids := strings.Fields(string(out))
		if len(ids) > 0 {
			args := append([]string{"rm", "-f"}, ids...)
			_ = exec.Command(h.realDocker, args...).Run()
		}
	}
	_ = exec.Command(h.realDocker, "rmi", "-f", testImageTag).Run()
	_ = os.RemoveAll(h.workDir)
}

func (h *harness) buildGranule(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-o", h.granuleBin, "github.com/gke-labs/granule/cmd/granule")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("built vendored granule CLI at %s\n", h.granuleBin)
	return nil
}

func (h *harness) buildTestImage(ctx context.Context) error {
	imgDir := filepath.Join(h.workDir, "img")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		return err
	}
	initSrc := filepath.Join(imgDir, "init.go")
	if err := os.WriteFile(initSrc, []byte(containerInitSource), 0o644); err != nil {
		return err
	}
	initBin := filepath.Join(imgDir, "init")
	buildCmd := exec.CommandContext(ctx, "go", "build", "-o", initBin, initSrc)
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("compiling container init: %w: %s", err, strings.TrimSpace(string(out)))
	}
	dockerfile := "FROM scratch\nCOPY init /init\nENTRYPOINT [\"/init\"]\n"
	if err := os.WriteFile(filepath.Join(imgDir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, h.realDocker, "build", "-q", "-t", testImageTag, imgDir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker build: %w: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("built local test image %s (%s)\n", testImageTag, strings.TrimSpace(string(out)))
	return nil
}

// startVMContainer provisions a real dockervm-shaped container (granule-vm-<name>,
// labelled with granule.VMLabel) and optionally writes a saved VM spec into stateDir.
func (h *harness) startVMContainer(ctx context.Context, stateDir, vmName string, overlayFiles int, ignoreTerm bool, graceSec int, writeState bool) error {
	h.trackVM(vmName)
	pod := granule.PodName(vmName)
	_ = exec.CommandContext(ctx, h.realDocker, "rm", "-f", pod, pod+"-netns").Run()

	ign := "0"
	if ignoreTerm {
		ign = "1"
	}
	args := []string{
		"run", "-d",
		"--name", pod,
		"--label", granule.VMLabel + "=" + vmName,
		"--label", "walkthrough.grain.dev/task=5486",
		"-e", fmt.Sprintf("OVERLAY_FILES=%d", overlayFiles),
		"-e", "IGNORE_TERM=" + ign,
		testImageTag,
	}
	out, err := exec.CommandContext(ctx, h.realDocker, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker run %s: %w: %s", pod, err, strings.TrimSpace(string(out)))
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		logs, _ := exec.CommandContext(ctx, h.realDocker, "logs", pod).CombinedOutput()
		if strings.Contains(string(logs), "READY") {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s READY log (logs=%q)", pod, string(logs))
		}
		time.Sleep(25 * time.Millisecond)
	}

	if writeState {
		if err := os.MkdirAll(stateDir, 0o755); err != nil {
			return err
		}
		spec := map[string]any{
			"name":                          vmName,
			"cpus":                          2,
			"memoryMB":                      2048,
			"konturImage":                   testImageTag,
			"terminationGracePeriodSeconds": graceSec,
		}
		raw, err := json.MarshalIndent(spec, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(stateDir, vmName+".json"), raw, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// installLoggingPassthroughDocker installs a transparent wrapper around real
// docker that forwards every invocation unchanged while recording each command,
// exit code, and stderr line to logFile so the test can prove real dockerd
// emitted "removal of container ... is already in progress".
func (h *harness) installLoggingPassthroughDocker(dir, logFile string, inspectHoldFile string) (string, error) {
	wrapper := filepath.Join(dir, "docker-passthrough.sh")
	script := fmt.Sprintf(`#!/bin/sh
if [ -n %[3]q ] && [ "$1" = "inspect" ]; then
  while [ -f %[3]q ]; do
    sleep 0.02
  done
fi
errfile=$(mktemp "%[1]s/derr.XXXXXX")
%[2]q "$@" 2>"$errfile"
rc=$?
errtxt=$(cat "$errfile")
rm -f "$errfile"
printf 'cmd=[docker %%s] rc=%%d stderr=[%%s]\n' "$*" "$rc" "$errtxt" >> %[4]q
if [ -n "$errtxt" ]; then
  printf '%%s\n' "$errtxt" >&2
fi
exit $rc
`, dir, h.realDocker, inspectHoldFile, logFile)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		return "", err
	}
	return wrapper, nil
}

func (h *harness) inspectContainer(ctx context.Context, pod string) (status string, exitCode int, stderr string) {
	cmd := exec.CommandContext(ctx, h.realDocker, "inspect", "-f", "{{.State.Status}}", pod)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	rc := 0
	if err != nil {
		rc = 1
		if ee, ok := err.(*exec.ExitError); ok {
			rc = ee.ExitCode()
		}
	}
	return strings.TrimSpace(outBuf.String()), rc, strings.TrimSpace(errBuf.String())
}

func (h *harness) waitForRemovingState(ctx context.Context, pod string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, rc, stderr := h.inspectContainer(ctx, pod)
		if rc == 0 && st == "removing" {
			return st, nil
		}
		if rc != 0 {
			return "", fmt.Errorf("container %s vanished before 'removing' state was observed (rc=%d stderr=%q)", pod, rc, stderr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	st, rc, stderr := h.inspectContainer(ctx, pod)
	return "", fmt.Errorf("timed out waiting for %s to enter 'removing' state (last status=%q rc=%d stderr=%q)", pod, st, rc, stderr)
}

func verifyGoneAndClean(h *harness, ctx context.Context, stateDir, vmName string) error {
	pod := granule.PodName(vmName)
	st, rc, stderr := h.inspectContainer(ctx, pod)
	fmt.Printf("post-delete docker inspect %s: rc=%d status=%q stderr=%q\n", pod, rc, st, stderr)
	if rc == 0 {
		return fmt.Errorf("container %s still exists with status %q", pod, st)
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading stateDir %s: %w", stateDir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	fmt.Printf("post-delete stateDir entries: %v (granule.Exists=%v)\n", names, granule.Exists(stateDir, vmName))
	if len(names) != 0 {
		return fmt.Errorf("orphaned files remain in stateDir %s: %v", stateDir, names)
	}
	if granule.Exists(stateDir, vmName) {
		return fmt.Errorf("granule.Exists(%s, %q) = true, want false", stateDir, vmName)
	}
	return nil
}

// Case 2a (simultaneous): Two concurrent Command.Delete calls launched at the
// same instant against a VM with 15,000 overlay files and gracePeriod=1s. Both
// stop the container and race into `docker rm -f` inside real dockerd.
func (h *harness) caseConcurrentCommandDeleteSimultaneous(ctx context.Context) error {
	caseDir, err := os.MkdirTemp(h.workDir, "case2a-sim-*")
	if err != nil {
		return err
	}
	stateDir := filepath.Join(caseDir, "state")
	logFile := filepath.Join(caseDir, "docker.log")
	wrapper, err := h.installLoggingPassthroughDocker(caseDir, logFile, "")
	if err != nil {
		return err
	}
	os.Setenv("GRANULE_DOCKER", wrapper)
	defer os.Unsetenv("GRANULE_DOCKER")

	vmName := vmPrefix + "2a-sim"
	if err := h.startVMContainer(ctx, stateDir, vmName, 15000, false, 1, true); err != nil {
		return err
	}

	cmd := granule.Command{h.granuleBin}
	startBarrier := make(chan struct{})
	errs := make([]error, 2)
	durs := make([]time.Duration, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-startBarrier
			t0 := time.Now()
			errs[idx] = cmd.Delete(ctx, stateDir, vmName)
			durs[idx] = time.Since(t0).Round(time.Millisecond)
		}(i)
	}
	close(startBarrier)
	wg.Wait()

	logBytes, _ := os.ReadFile(logFile)
	fmt.Printf("caller[0]: dur=%s err=%v\n", durs[0], errs[0])
	fmt.Printf("caller[1]: dur=%s err=%v\n", durs[1], errs[1])
	fmt.Printf("passthrough docker log:\n%s", string(logBytes))

	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("Command.Delete caller[%d] failed: %w", i, err)
		}
	}
	return verifyGoneAndClean(h, ctx, stateDir, vmName)
}

// Case 2a (while removing): Command.Delete #1 initiates teardown on a running
// container that ignores SIGTERM (gracePeriod=1s, 15,000 overlay files); once
// dockerd transitions the container into `removing`, Command.Delete #2 runs and
// deterministically hits "removal of container ... is already in progress".
func (h *harness) caseConcurrentCommandDeleteWhileRemoving(ctx context.Context) error {
	caseDir, err := os.MkdirTemp(h.workDir, "case2a-rem-*")
	if err != nil {
		return err
	}
	stateDir := filepath.Join(caseDir, "state")
	logFile := filepath.Join(caseDir, "docker.log")
	wrapper, err := h.installLoggingPassthroughDocker(caseDir, logFile, "")
	if err != nil {
		return err
	}
	os.Setenv("GRANULE_DOCKER", wrapper)
	defer os.Unsetenv("GRANULE_DOCKER")

	vmName := vmPrefix + "2a-rem"
	// Stop the container first so Command.Delete #1 goes straight into the ~400ms
	// overlay2 unlink inside `docker rm -f`, where dockerd sets status="removing".
	if err := h.startVMContainer(ctx, stateDir, vmName, 20000, false, 1, true); err != nil {
		return err
	}
	pod := granule.PodName(vmName)
	if out, err := exec.CommandContext(ctx, h.realDocker, "stop", "-t", "1", pod).CombinedOutput(); err != nil {
		return fmt.Errorf("pre-stopping %s: %w: %s", pod, err, string(out))
	}

	cmd := granule.Command{h.granuleBin}
	var err1, err2 error
	var dur1, dur2 time.Duration
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		t0 := time.Now()
		err1 = cmd.Delete(ctx, stateDir, vmName)
		dur1 = time.Since(t0).Round(time.Millisecond)
	}()

	st, waitErr := h.waitForRemovingState(ctx, pod, 5*time.Second)
	if waitErr != nil {
		<-done1
		return fmt.Errorf("waiting for removing state: %w (caller1 err=%v)", waitErr, err1)
	}
	fmt.Printf("observed real dockerd status=%q on %s; launching second Command.Delete\n", st, pod)

	t0 := time.Now()
	err2 = cmd.Delete(ctx, stateDir, vmName)
	dur2 = time.Since(t0).Round(time.Millisecond)
	<-done1

	logBytes, _ := os.ReadFile(logFile)
	fmt.Printf("caller[1] (initiator): dur=%s err=%v\n", dur1, err1)
	fmt.Printf("caller[2] (racer):     dur=%s err=%v\n", dur2, err2)
	fmt.Printf("passthrough docker log:\n%s", string(logBytes))

	if !strings.Contains(string(logBytes), "is already in progress") {
		return fmt.Errorf("expected real dockerd to emit 'removal of container ... is already in progress', log:\n%s", string(logBytes))
	}
	if err1 != nil {
		return fmt.Errorf("initiator Command.Delete failed: %w", err1)
	}
	if err2 != nil {
		return fmt.Errorf("racing Command.Delete failed: %w", err2)
	}
	return verifyGoneAndClean(h, ctx, stateDir, vmName)
}

// Case 2b: Direct `docker rm -f <container>` (orphan-sweep / external teardown)
// racing `granule.Command.Delete` (run release), which alone owns clearing the
// saved state file in stateDir.
func (h *harness) caseDockerRmRacingCommandDelete(ctx context.Context) error {
	caseDir, err := os.MkdirTemp(h.workDir, "case2b-*")
	if err != nil {
		return err
	}
	stateDir := filepath.Join(caseDir, "state")
	logFile := filepath.Join(caseDir, "docker.log")
	wrapper, err := h.installLoggingPassthroughDocker(caseDir, logFile, "")
	if err != nil {
		return err
	}
	os.Setenv("GRANULE_DOCKER", wrapper)
	defer os.Unsetenv("GRANULE_DOCKER")

	vmName := vmPrefix + "2b-race"
	if err := h.startVMContainer(ctx, stateDir, vmName, 10000, true, 1, true); err != nil {
		return err
	}
	pod := granule.PodName(vmName)

	var rmErr error
	var rmDur time.Duration
	var rmStderr bytes.Buffer
	rmDone := make(chan struct{})
	go func() {
		defer close(rmDone)
		t0 := time.Now()
		c := exec.CommandContext(ctx, h.realDocker, "rm", "-f", pod)
		c.Stderr = &rmStderr
		rmErr = c.Run()
		rmDur = time.Since(t0).Round(time.Millisecond)
	}()

	st, waitErr := h.waitForRemovingState(ctx, pod, 5*time.Second)
	if waitErr != nil {
		<-rmDone
		return fmt.Errorf("waiting for removing state: %w (docker rm err=%v stderr=%q)", waitErr, rmErr, rmStderr.String())
	}
	fmt.Printf("observed real dockerd status=%q on %s; launching granule.Command.Delete\n", st, pod)

	cmd := granule.Command{h.granuleBin}
	t0 := time.Now()
	delErr := cmd.Delete(ctx, stateDir, vmName)
	delDur := time.Since(t0).Round(time.Millisecond)
	<-rmDone

	logBytes, _ := os.ReadFile(logFile)
	fmt.Printf("direct docker rm -f:   dur=%s err=%v stderr=%q\n", rmDur, rmErr, strings.TrimSpace(rmStderr.String()))
	fmt.Printf("granule.Command.Delete: dur=%s err=%v\n", delDur, delErr)
	fmt.Printf("passthrough docker log:\n%s", string(logBytes))

	if !strings.Contains(string(logBytes), "is already in progress") {
		return fmt.Errorf("expected real dockerd to emit 'removal of container ... is already in progress', log:\n%s", string(logBytes))
	}
	if rmErr != nil {
		return fmt.Errorf("direct docker rm -f failed: %w (%s)", rmErr, strings.TrimSpace(rmStderr.String()))
	}
	if delErr != nil {
		entries, _ := os.ReadDir(stateDir)
		var leftover []string
		for _, e := range entries {
			leftover = append(leftover, e.Name())
		}
		return fmt.Errorf("granule.Command.Delete failed: %w (leftover stateDir files=%v)", delErr, leftover)
	}
	return verifyGoneAndClean(h, ctx, stateDir, vmName)
}

// Case 2c: Concurrent `granule.RemoveContainers` calls racing `docker rm -f`
// (stateless orphan container cleanup).
func (h *harness) caseConcurrentRemoveContainers(ctx context.Context) error {
	caseDir, err := os.MkdirTemp(h.workDir, "case2c-*")
	if err != nil {
		return err
	}
	stateDir := filepath.Join(caseDir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}

	vmName := vmPrefix + "2c-rm"
	if err := h.startVMContainer(ctx, stateDir, vmName, 10000, true, 1, false); err != nil {
		return err
	}
	pod := granule.PodName(vmName)

	// Start a direct `docker rm -f` (the caller that blocks until removal settles)
	// and race 4 concurrent `granule.RemoveContainers` calls while `dockerd` holds
	// the container in `removing`.
	var directErr error
	var directDur time.Duration
	directDone := make(chan struct{})
	go func() {
		defer close(directDone)
		t0 := time.Now()
		directErr = exec.CommandContext(ctx, h.realDocker, "rm", "-f", pod).Run()
		directDur = time.Since(t0).Round(time.Millisecond)
	}()

	st, waitErr := h.waitForRemovingState(ctx, pod, 5*time.Second)
	if waitErr != nil {
		<-directDone
		return fmt.Errorf("waiting for removing state: %w", waitErr)
	}
	fmt.Printf("observed real dockerd status=%q on %s; launching 4 concurrent RemoveContainers calls\n", st, pod)

	const racers = 4
	rcErrs := make([]error, racers)
	rcDurs := make([]time.Duration, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			t0 := time.Now()
			rcErrs[idx] = granule.RemoveContainers(ctx, vmName)
			rcDurs[idx] = time.Since(t0).Round(time.Millisecond)
		}(i)
	}
	wg.Wait()
	<-directDone

	fmt.Printf("direct docker rm -f (waiting caller): dur=%s err=%v\n", directDur, directErr)
	for i := range racers {
		fmt.Printf("RemoveContainers[%d]: dur=%s err=%v\n", i, rcDurs[i], rcErrs[i])
	}

	if directErr != nil {
		return fmt.Errorf("direct docker rm -f failed: %w", directErr)
	}
	for i, err := range rcErrs {
		if err != nil {
			return fmt.Errorf("RemoveContainers[%d] failed: %w", i, err)
		}
	}
	return verifyGoneAndClean(h, ctx, stateDir, vmName)
}

// Case 3a: Short context.WithTimeout (80ms) on Command.Delete during a genuine
// ~3s in-progress removal in real dockerd (no artificial sleep/shim).
func (h *harness) caseTimeoutDuringRealRemoval(ctx context.Context) error {
	caseDir, err := os.MkdirTemp(h.workDir, "case3a-*")
	if err != nil {
		return err
	}
	stateDir := filepath.Join(caseDir, "state")

	vmName := vmPrefix + "3a-timeout"
	if err := h.startVMContainer(ctx, stateDir, vmName, 12000, true, 1, true); err != nil {
		return err
	}
	pod := granule.PodName(vmName)

	rmDone := make(chan error, 1)
	go func() {
		rmDone <- exec.CommandContext(ctx, h.realDocker, "rm", "-f", pod).Run()
	}()

	if _, err := h.waitForRemovingState(ctx, pod, 5*time.Second); err != nil {
		<-rmDone
		return err
	}

	cmd := granule.Command{h.granuleBin}
	shortCtx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	shortErr := cmd.Delete(shortCtx, stateDir, vmName)
	shortDur := time.Since(t0).Round(time.Millisecond)

	stMid, rcMid, _ := h.inspectContainer(ctx, pod)
	stateStillExists := granule.Exists(stateDir, vmName)
	fmt.Printf("short Command.Delete (80ms): dur=%s err=%v (isDeadlineExceeded=%v)\n",
		shortDur, shortErr, errors.Is(shortErr, context.DeadlineExceeded))
	fmt.Printf("immediately after timeout: container inspect rc=%d status=%q, savedStateExists=%v\n",
		rcMid, stMid, stateStillExists)

	if shortErr == nil {
		<-rmDone
		return fmt.Errorf("short Command.Delete returned nil on unsettled removal, want non-nil error")
	}
	if !errors.Is(shortErr, context.DeadlineExceeded) {
		<-rmDone
		return fmt.Errorf("short Command.Delete error %q does not wrap context.DeadlineExceeded", shortErr)
	}
	if rcMid != 0 {
		<-rmDone
		return fmt.Errorf("container %s vanished before short timeout check completed; increase overlay size", pod)
	}
	if !stateStillExists {
		<-rmDone
		return fmt.Errorf("saved VM state was wiped prematurely while container %s still existed (status=%q)", pod, stMid)
	}

	// Follow-up uncancelled Command.Delete waits out the remaining real removal and clears saved state.
	t1 := time.Now()
	cleanErr := cmd.Delete(ctx, stateDir, vmName)
	cleanDur := time.Since(t1).Round(time.Millisecond)
	bgRmErr := <-rmDone
	fmt.Printf("follow-up uncancelled Command.Delete: dur=%s err=%v (bg docker rm err=%v)\n", cleanDur, cleanErr, bgRmErr)
	if cleanErr != nil {
		return fmt.Errorf("follow-up Command.Delete failed: %w", cleanErr)
	}
	return verifyGoneAndClean(h, ctx, stateDir, vmName)
}

// Case 3b: Short context.WithTimeout (80ms) on Command.Delete via a thin
// passthrough shim around real /usr/bin/docker that holds `inspect` while a
// container removal is in progress, then releases `inspect` for the follow-up
// uncancelled Command.Delete.
func (h *harness) caseTimeoutViaPassthroughShim(ctx context.Context) error {
	caseDir, err := os.MkdirTemp(h.workDir, "case3b-*")
	if err != nil {
		return err
	}
	stateDir := filepath.Join(caseDir, "state")
	logFile := filepath.Join(caseDir, "docker.log")
	holdFile := filepath.Join(caseDir, "hold-inspect")
	if err := os.WriteFile(holdFile, []byte("hold"), 0o644); err != nil {
		return err
	}
	wrapper, err := h.installLoggingPassthroughDocker(caseDir, logFile, holdFile)
	if err != nil {
		return err
	}
	os.Setenv("GRANULE_DOCKER", wrapper)
	defer os.Unsetenv("GRANULE_DOCKER")

	vmName := vmPrefix + "3b-shim"
	if err := h.startVMContainer(ctx, stateDir, vmName, 10000, true, 1, true); err != nil {
		return err
	}
	pod := granule.PodName(vmName)

	rmDone := make(chan error, 1)
	go func() {
		rmDone <- exec.CommandContext(ctx, h.realDocker, "rm", "-f", pod).Run()
	}()
	if _, err := h.waitForRemovingState(ctx, pod, 5*time.Second); err != nil {
		_ = os.Remove(holdFile)
		<-rmDone
		return err
	}

	cmd := granule.Command{h.granuleBin}
	shortCtx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	shortErr := cmd.Delete(shortCtx, stateDir, vmName)
	shortDur := time.Since(t0).Round(time.Millisecond)

	statePreserved := granule.Exists(stateDir, vmName)
	fmt.Printf("shim short Command.Delete (80ms): dur=%s err=%v (isDeadlineExceeded=%v), savedStateExists=%v\n",
		shortDur, shortErr, errors.Is(shortErr, context.DeadlineExceeded), statePreserved)

	// Release hold so follow-up uncancelled Command.Delete runs cleanly against real docker.
	_ = os.Remove(holdFile)

	if shortErr == nil {
		<-rmDone
		return fmt.Errorf("short Command.Delete returned nil while inspect was held, want non-nil error")
	}
	if !errors.Is(shortErr, context.DeadlineExceeded) {
		<-rmDone
		return fmt.Errorf("short Command.Delete error %q does not wrap context.DeadlineExceeded", shortErr)
	}
	if !statePreserved {
		<-rmDone
		return fmt.Errorf("saved VM state was deleted despite timed-out Command.Delete")
	}

	cleanErr := cmd.Delete(ctx, stateDir, vmName)
	<-rmDone
	fmt.Printf("shim follow-up Command.Delete: err=%v\n", cleanErr)
	if cleanErr != nil {
		return fmt.Errorf("follow-up Command.Delete failed: %w", cleanErr)
	}
	return verifyGoneAndClean(h, ctx, stateDir, vmName)
}
