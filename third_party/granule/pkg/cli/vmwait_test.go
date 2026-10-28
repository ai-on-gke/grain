package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/vmspec"
)

// countCalls returns how many of the recorded docker invocations start
// with verb.
func countCalls(calls [][]string, verb string) int {
	n := 0
	for _, c := range calls {
		if len(c) > 0 && c[0] == verb {
			n++
		}
	}
	return n
}

// TestVMWait_BlocksUntilTheGuestAnswers is what callers used to write a
// shell loop around "granule exec -- true" for: the VM's containers are
// started well before its guest can answer anything, and this is the wait
// for that difference.
func TestVMWait_BlocksUntilTheGuestAnswers(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")
	calls := callLog(t)
	// One refused probe, then an answer: a guest that is still booting.
	t.Setenv("FAKEDOCKER_PROBE_FAIL", "1")

	stdout, stderr, err := runVMArgs(t, "wait", "web", "--state-dir", stateDir)
	if err != nil {
		t.Fatalf("wait error = %v, stderr = %s", err, stderr)
	}
	if !strings.Contains(stdout, "ready") {
		t.Errorf("stdout = %q, want it to say the VM became ready", stdout)
	}
	if n := countCalls(calls(), "exec"); n < 2 {
		t.Errorf("%d probes, want the wait to have retried the one that was refused", n)
	}
}

// TestVMWait_TimesOutNamingTheContainer covers the other end: a guest
// that never answers has to fail with something a caller can act on, not
// hang.
func TestVMWait_TimesOutNamingTheContainer(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")
	callLog(t)
	t.Setenv("FAKEDOCKER_PROBE_FAIL", "-1")

	_, _, err := runVMArgs(t, "wait", "web", "--state-dir", stateDir, "--timeout", "1ms")
	if err == nil {
		t.Fatal("wait on a guest that never answers = nil error, want a timeout")
	}
	if !strings.Contains(err.Error(), "granule-vm-web") {
		t.Errorf("error = %v, want it to name the container to read logs from", err)
	}
}

func TestVMWait_UnknownVM(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")

	_, _, err := runVMArgs(t, "wait", "nosuch", "--state-dir", stateDir)
	if err == nil {
		t.Fatal("wait on an unknown VM = nil error, want one")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want it to say the VM wasn't found", err)
	}
}

// TestVMCreate_WaitBlocksForTheGuest is the same wait folded into the
// create, which is what makes a create followed by an exec a script
// rather than a poll loop.
func TestVMCreate_WaitBlocksForTheGuest(t *testing.T) {
	withFakeDocker(t)
	calls := callLog(t)
	t.Setenv("FAKEDOCKER_PROBE_FAIL", "1")
	stateDir := filepath.Join(t.TempDir(), "state")

	stdout, stderr, err := runVMArgs(t, "create", "web",
		"--backend", "docker",
		"--state-dir", stateDir,
		"--wait",
	)
	if err != nil {
		t.Fatalf("create -wait error = %v, stderr = %s", err, stderr)
	}
	if !strings.Contains(stdout, "ready") {
		t.Errorf("stdout = %q, want it to say the VM became ready", stdout)
	}
	if n := countCalls(calls(), "exec"); n < 2 {
		t.Errorf("%d probes, want -wait to have retried the one that was refused", n)
	}
	// The VM is a real one either way: -wait only decides when the
	// command returns.
	if _, err := vmspec.Load(stateDir, "web"); err != nil {
		t.Errorf("saved state not found after create -wait: %v", err)
	}
}
