package dockervm

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/guestexec"
)

// execCall returns the single "docker exec" invocation recorded, failing
// the test if there isn't exactly one.
func execCall(t *testing.T, calls [][]string) []string {
	t.Helper()
	var found [][]string
	for _, c := range calls {
		if len(c) > 0 && c[0] == "exec" {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("got %d docker exec calls, want 1: %v", len(found), calls)
	}
	return found[0]
}

func TestExec_RunsGranuleExecInTheVMContainer(t *testing.T) {
	d, calls := testDocker(t)

	var stdout bytes.Buffer
	code, err := Exec(context.Background(), d, "web", ExecOptions{
		Command: []string{"systemctl", "is-active", "nginx"},
		Stdin:   strings.NewReader("hello"),
		Stdout:  &stdout,
		Stderr:  io.Discard,
	})
	if err != nil {
		t.Fatalf("Exec error = %v", err)
	}
	if code != 0 {
		t.Errorf("Exec status = %d, want 0", code)
	}

	call := execCall(t, calls())
	want := []string{"exec", "-i", "granule-vm-web", "granule", "exec", "--", "systemctl", "is-active", "nginx"}
	if strings.Join(call, " ") != strings.Join(want, " ") {
		t.Errorf("docker call = %v, want %v", call, want)
	}
	// fakedocker's "exec" copies stdin to stdout, so this is the session's
	// stdin proxying (the README's Flow 4) reaching the guest command.
	if stdout.String() != "hello" {
		t.Errorf("stdout = %q, want the stdin that was passed through", stdout.String())
	}
}

func TestExec_TTYAndEnv(t *testing.T) {
	d, calls := testDocker(t)

	if _, err := Exec(context.Background(), d, "web", ExecOptions{
		TTY:    true,
		Env:    []string{"GRANULE_EXEC_USER=app", "GRANULE_EXEC_CONNECT_TIMEOUT=3m"},
		Stdout: io.Discard,
		Stderr: io.Discard,
	}); err != nil {
		t.Fatalf("Exec error = %v", err)
	}

	call := execCall(t, calls())
	// No command and no "--": that is what asks "granule exec" for an
	// interactive login shell rather than for one command.
	want := []string{"exec", "-i", "-t", "-e", "GRANULE_EXEC_USER=app", "-e", "GRANULE_EXEC_CONNECT_TIMEOUT=3m", "granule-vm-web", "granule", "exec"}
	if strings.Join(call, " ") != strings.Join(want, " ") {
		t.Errorf("docker call = %v, want %v", call, want)
	}
}

// TestExec_ReportsTheCommandsOwnStatus covers the difference "vm exec"
// exists to preserve: a guest command that exits non-zero is a status to
// pass on, not an error.
func TestExec_ReportsTheCommandsOwnStatus(t *testing.T) {
	d, _ := testDocker(t)
	t.Setenv("FAKEDOCKER_EXEC_EXIT", "42")

	code, err := Exec(context.Background(), d, "web", ExecOptions{
		Command: []string{"false"},
		Stdout:  io.Discard,
		Stderr:  io.Discard,
	})
	if err != nil {
		t.Fatalf("Exec error = %v, want the status reported instead", err)
	}
	if code != 42 {
		t.Errorf("Exec status = %d, want 42", code)
	}
}

func TestExec_VMNotRunning(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  func(t *testing.T)
	}{
		{"no such container", func(t *testing.T) { t.Setenv("FAKEDOCKER_MISSING", "granule-vm-web") }},
		{"container exited", func(t *testing.T) { t.Setenv("FAKEDOCKER_RUNNING", "false") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, calls := testDocker(t)
			tc.env(t)

			_, err := Exec(context.Background(), d, "web", ExecOptions{
				Command: []string{"true"},
				Stdout:  io.Discard,
				Stderr:  io.Discard,
			})
			if err == nil {
				t.Fatal("Exec error = nil, want one naming the VM")
			}
			if !strings.Contains(err.Error(), "granule-vm-web") {
				t.Errorf("error = %v, want it to name the container", err)
			}
			// Nothing was exec'd: docker's own 125/126/127 must not be
			// mistaken for the guest command's status.
			for _, c := range calls() {
				if len(c) > 0 && c[0] == "exec" {
					t.Errorf("docker exec was attempted against a VM that isn't running: %v", c)
				}
			}
		})
	}
}

func TestWaitReady_RetriesUntilTheGuestAnswers(t *testing.T) {
	d, calls := testDocker(t)
	t.Setenv("FAKEDOCKER_PROBE_FAIL", "2")

	if err := WaitReady(context.Background(), d, "web", 30*time.Second); err != nil {
		t.Fatalf("WaitReady error = %v", err)
	}

	execs := 0
	for _, c := range calls() {
		if len(c) > 0 && c[0] == "exec" {
			execs++
			// The probe is "granule ready", not a hand-rolled command of
			// this package's own: what counts as ready is decided in one
			// place, guest-side, and shared with the container readiness
			// probe a pod spec runs.
			if got := strings.Join(c, " "); !strings.HasSuffix(got, "granule ready -timeout 0") {
				t.Errorf("readiness probe ran %q, want it to end in %q", got, "granule ready -timeout 0")
			}
		}
	}
	if execs != 3 {
		t.Errorf("got %d readiness probes, want 3 (two refused, then one that answered)", execs)
	}
}

// TestWaitReady_Sub50msDetection verifies that adaptive polling in WaitReady
// detects guest readiness in sub-50ms (initial 10ms backoff interval) instead
// of incurring a fixed 2-second polling quantization delay.
func TestWaitReady_Sub50msDetection(t *testing.T) {
	d, _ := testDocker(t)
	// Fail 1 probe: probe 1 (0ms) fails, WaitReady sleeps initial 10ms, probe 2 (10ms) succeeds.
	t.Setenv("FAKEDOCKER_PROBE_FAIL", "1")

	start := time.Now()
	if err := WaitReady(context.Background(), d, "web", 5*time.Second); err != nil {
		t.Fatalf("WaitReady error = %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Errorf("WaitReady took %v to detect readiness after 1 failed probe, want sub-50ms", elapsed)
	}
}

// A VM keeps the image it was created from, so an image without a
// "ready" mode is an ordinary VM that predates the mode -- and the wait
// still has to work against it, with the probe it used to use. Anything
// else takes "vm wait" away from exactly the VMs whose owner has not
// upgraded yet.
func TestWaitReady_FallsBackForAnImageWithoutTheReadyMode(t *testing.T) {
	d, calls := testDocker(t)
	t.Setenv("FAKEDOCKER_NO_READY_MODE", "1")

	if err := WaitReady(context.Background(), d, "web", 30*time.Second); err != nil {
		t.Fatalf("WaitReady error = %v, want the fallback probe to answer", err)
	}

	var probes []string
	for _, c := range calls() {
		if len(c) > 0 && c[0] == "exec" {
			probes = append(probes, strings.Join(c, " "))
		}
	}
	if len(probes) != 2 {
		t.Fatalf("got %d probes (%v), want the ready mode tried once and the fallback once", len(probes), probes)
	}
	if !strings.HasSuffix(probes[1], "granule exec -- true") {
		t.Errorf("fallback probe ran %q, want the pre-\"granule ready\" %q", probes[1], "granule exec -- true")
	}
	// One dial per probe, the same bound "-timeout 0" puts on the mode
	// this stands in for: an old "granule exec" left to its own default
	// spends 30 seconds on a guest that is not up, which is longer than
	// most of the timeouts the loop around it is given.
	if !strings.Contains(probes[1], "-e GRANULE_EXEC_CONNECT_TIMEOUT=0") {
		t.Errorf("fallback probe ran %q, want it bounded to a single dial", probes[1])
	}
}

// The fallback is still a poll: an old image's guest boots at the same
// speed as a new one's, so a VM that is not up yet must be retried
// rather than reported as an image problem.
func TestWaitReady_FallbackRetriesUntilTheGuestAnswers(t *testing.T) {
	d, _ := testDocker(t)
	t.Setenv("FAKEDOCKER_NO_READY_MODE", "1")
	t.Setenv("FAKEDOCKER_PROBE_FAIL", "1")

	if err := WaitReady(context.Background(), d, "web", 30*time.Second); err != nil {
		t.Fatalf("WaitReady error = %v", err)
	}
}

// A container with neither mode is the one thing no waiting fixes: there
// is no probe left to fall back to, so it is reported at once and named
// for what it is rather than looking like a guest that never booted.
func TestWaitReady_ImageWithNeitherProbeMode(t *testing.T) {
	d, _ := testDocker(t)
	t.Setenv("FAKEDOCKER_NO_READY_MODE", "1")
	t.Setenv("FAKEDOCKER_NO_EXEC_MODE", "1")

	start := time.Now()
	err := WaitReady(context.Background(), d, "web", time.Hour)
	if err == nil {
		t.Fatal("WaitReady error = nil, want one")
	}
	if !strings.Contains(err.Error(), "no \"granule ready\" mode") {
		t.Errorf("error = %v, want it to name the missing mode", err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("took %v to give up on something no wait can fix", elapsed)
	}
}

// What the fallback must not do is answer a smaller question than the
// probe it stands in for: a VM created with a -ready-file is not ready
// until the guest has created it, whatever its image is old enough to
// have missed.
func TestInspect_FallbackChecksTheGuestsDeclaration(t *testing.T) {
	d, calls := testDocker(t)
	t.Setenv("FAKEDOCKER_NO_READY_MODE", "1")
	t.Setenv("FAKEDOCKER_CONTAINER_ENV", "GRANULE_EXEC_USER=root,GRANULE_READY_FILE=/run/granule/ready")
	t.Setenv("FAKEDOCKER_PROBE_UNDECLARED", "1")

	st, err := Inspect(context.Background(), d, "web")
	if err != nil {
		t.Fatalf("Inspect error = %v, want the status reported instead", err)
	}
	if st.Ready {
		t.Errorf("status = %+v, want a guest that has not declared itself ready", st)
	}
	if !st.Reachable || !st.Fallback {
		t.Errorf("status = %+v, want a reachable guest probed through the fallback", st)
	}
	if !strings.Contains(st.Detail, "has not declared itself ready") || !strings.Contains(st.Detail, "/run/granule/ready") {
		t.Errorf("Detail = %q, want it to name the declaration and the file", st.Detail)
	}

	var last string
	for _, c := range calls() {
		if len(c) > 0 && c[0] == "exec" {
			last = strings.Join(c, " ")
		}
	}
	if !strings.HasSuffix(last, "granule exec -- test -e /run/granule/ready") {
		t.Errorf("last probe ran %q, want the declaration checked by path", last)
	}
}

// And the same VM once its guest has created the file: ready, by both
// conditions, through an image that knows about neither.
func TestInspect_FallbackReadyOnceTheGuestHasDeclared(t *testing.T) {
	d, _ := testDocker(t)
	t.Setenv("FAKEDOCKER_NO_READY_MODE", "1")
	t.Setenv("FAKEDOCKER_CONTAINER_ENV", "GRANULE_READY_FILE=/run/granule/ready")

	st, err := Inspect(context.Background(), d, "web")
	if err != nil {
		t.Fatalf("Inspect error = %v", err)
	}
	if !st.Ready || !st.Reachable {
		t.Errorf("status = %+v, want a ready guest", st)
	}
	if !st.Fallback {
		t.Errorf("status = %+v, want the answer marked as the fallback's", st)
	}
}

// A VM on a current image must not be reported as if it had been probed
// the old way -- the flag is what "vm status" says how much its answer
// is worth by.
func TestInspect_ReadyModeIsNotReportedAsAFallback(t *testing.T) {
	d, _ := testDocker(t)

	st, err := Inspect(context.Background(), d, "web")
	if err != nil {
		t.Fatalf("Inspect error = %v", err)
	}
	if st.Fallback {
		t.Errorf("status = %+v, want Fallback false for an image with the mode", st)
	}
}

// TestInspect_ReportsReadinessWithoutWaiting is the question "vm wait"
// can only answer by blocking: asked once, answered now, whichever way.
func TestInspect_ReportsReadinessWithoutWaiting(t *testing.T) {
	d, _ := testDocker(t)

	st, err := Inspect(context.Background(), d, "web")
	if err != nil {
		t.Fatalf("Inspect error = %v", err)
	}
	if st.Container != "granule-vm-web" {
		t.Errorf("Container = %q, want granule-vm-web", st.Container)
	}
	if !st.Running() || !st.Ready {
		t.Errorf("status = %+v, want a running container with a guest that answered", st)
	}
}

func TestInspect_NotReadyCarriesTheProbesOwnReason(t *testing.T) {
	d, _ := testDocker(t)
	t.Setenv("FAKEDOCKER_PROBE_FAIL", "-1")

	st, err := Inspect(context.Background(), d, "web")
	// A guest that isn't up is a status, not a failure to ask.
	if err != nil {
		t.Fatalf("Inspect error = %v, want the status reported instead", err)
	}
	if !st.Running() || st.Ready {
		t.Errorf("status = %+v, want a running container whose guest has not answered", st)
	}
	if !strings.Contains(st.Detail, "not reachable yet") {
		t.Errorf("Detail = %q, want the probe's own message", st.Detail)
	}
}

// TestInspect_AnUndeclaredGuestIsToldApartFromAnUnreachableOne is the
// distinction the second readiness condition exists to make reportable:
// the VM booted and its agent answers, and the guest has not said its
// workload is up. Everything a caller would do about that differs from
// what it would do about a guest that never answered, so Status carries
// the two apart rather than as one "not ready".
func TestInspect_AnUndeclaredGuestIsToldApartFromAnUnreachableOne(t *testing.T) {
	d, _ := testDocker(t)
	t.Setenv("FAKEDOCKER_PROBE_UNDECLARED", "1")

	st, err := Inspect(context.Background(), d, "web")
	if err != nil {
		t.Fatalf("Inspect error = %v, want the status reported instead", err)
	}
	if !st.Running() || st.Ready {
		t.Errorf("status = %+v, want a running container whose guest is not ready", st)
	}
	if !st.Reachable {
		t.Errorf("status = %+v, want Reachable: the probe exited %d, which is the guest answering", st, guestexec.ExitNotDeclared)
	}
	if !strings.Contains(st.Detail, "has not declared itself ready") {
		t.Errorf("Detail = %q, want the probe's own message", st.Detail)
	}
}

// The other side of it: an ordinary failed probe must not be read as a
// guest that answered, or every still-booting VM would be reported as a
// workload that hasn't come up.
func TestInspect_AnUnreachableGuestIsNotReportedAsReachable(t *testing.T) {
	d, _ := testDocker(t)
	t.Setenv("FAKEDOCKER_PROBE_FAIL", "-1")

	st, err := Inspect(context.Background(), d, "web")
	if err != nil {
		t.Fatalf("Inspect error = %v", err)
	}
	if st.Reachable {
		t.Errorf("status = %+v, want Reachable false for a probe that failed on the dial", st)
	}
}

// A wait that runs out against a guest that answers has to say which of
// the two conditions it ran out on: "did not become reachable" would
// send whoever reads it to a console with nothing wrong in it.
func TestWaitReady_TimeoutOnAnUndeclaredGuestSaysWhichConditionItIs(t *testing.T) {
	d, _ := testDocker(t)
	t.Setenv("FAKEDOCKER_PROBE_UNDECLARED", "1")

	err := WaitReady(context.Background(), d, "web", time.Nanosecond)
	if err == nil {
		t.Fatal("WaitReady error = nil, want a timeout")
	}
	if !strings.Contains(err.Error(), "declare itself ready") {
		t.Errorf("error = %v, want it to say the declaration is what is outstanding", err)
	}
	if strings.Contains(err.Error(), "become reachable") {
		t.Errorf("error = %v, want it not to blame the boot for a guest that answered", err)
	}
}

func TestInspect_ContainerNotRunning(t *testing.T) {
	d, calls := testDocker(t)
	t.Setenv("FAKEDOCKER_MISSING", "granule-vm-web")

	st, err := Inspect(context.Background(), d, "web")
	if err != nil {
		t.Fatalf("Inspect error = %v", err)
	}
	if st.Running() || st.Ready {
		t.Errorf("status = %+v, want neither running nor ready", st)
	}
	// Nothing to probe through: an exec against a container that isn't
	// there reports docker's failure, not the guest's.
	for _, c := range calls() {
		if len(c) > 0 && c[0] == "exec" {
			t.Errorf("probed a container that isn't running: %v", c)
		}
	}
}

// Each probe has to give up dialling well inside the wait's own budget:
// left to retry for the 30 seconds "granule exec" gives a dial by
// default, one probe outlasts any shorter timeout the caller asked for
// and delays the container-exited check with it. "-timeout 0" is what
// keeps that from happening (see readyProbeArgs), so it is asserted
// rather than left to the argv comparison above.
func TestWaitReady_ProbesGiveUpDiallingBeforeTheWaitDoes(t *testing.T) {
	d, calls := testDocker(t)

	if err := WaitReady(context.Background(), d, "web", 30*time.Second); err != nil {
		t.Fatalf("WaitReady error = %v", err)
	}

	call := execCall(t, calls())
	if !slices.Contains(call, "-timeout") || call[len(call)-1] != "0" {
		t.Errorf("readiness probe ran %v, want it to ask for a single attempt (-timeout 0)", call)
	}
}

// TestWaitReady_ContainerExited covers the failure worth failing fast on:
// a VM whose container died is not going to become reachable, however
// long the timeout is.
func TestWaitReady_ContainerExited(t *testing.T) {
	d, _ := testDocker(t)
	t.Setenv("FAKEDOCKER_PROBE_FAIL", "-1")
	t.Setenv("FAKEDOCKER_RUNNING", "false")

	err := WaitReady(context.Background(), d, "web", time.Hour)
	if err == nil {
		t.Fatal("WaitReady error = nil, want one")
	}
	if !strings.Contains(err.Error(), "exited before its guest became reachable") {
		t.Errorf("error = %v, want it to say the container exited", err)
	}
}

func TestWaitReady_Timeout(t *testing.T) {
	d, _ := testDocker(t)
	t.Setenv("FAKEDOCKER_PROBE_FAIL", "-1")

	err := WaitReady(context.Background(), d, "web", time.Nanosecond)
	if err == nil {
		t.Fatal("WaitReady error = nil, want a timeout")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %v, want a timeout", err)
	}
}
