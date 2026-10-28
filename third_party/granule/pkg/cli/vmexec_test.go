package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/vmspec"
)

// createDockerVM creates a docker-backend VM in a fresh state directory
// and returns that directory, so the exec tests have something with a
// saved backend to resolve.
func createDockerVM(t *testing.T, name string) string {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	if _, stderr, err := runVMArgs(t, "create", name,
		"--backend", "docker",
		"--disk-mode", "overlay",
		"--state-dir", stateDir,
	); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	return stateDir
}

// dockerCalls returns the docker invocations fakedocker recorded, split
// into their arguments.
func dockerCalls(t *testing.T, logPath string) [][]string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("reading fakedocker log: %v", err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line != "" {
			calls = append(calls, strings.Split(line, "\x1f"))
		}
	}
	return calls
}

// callLog points fakedocker at a log file of this test's own and returns
// a function reading the calls back.
func callLog(t *testing.T) func() [][]string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("FAKEDOCKER_LOG", logPath)
	return func() [][]string { return dockerCalls(t, logPath) }
}

// changingCalls drops the docker calls that only ask the daemon a
// question, leaving the ones that would have started, stopped or removed
// something. That is what a test asserting "nothing happened" is really
// about, and a create asks one such question before it does anything at
// all: what sizing the machine image records for itself (see
// resolveImageSizing), which reads an image's config and changes nothing.
func changingCalls(calls [][]string) [][]string {
	var changing [][]string
	for _, c := range calls {
		if len(c) > 1 && c[0] == "image" && c[1] == "inspect" {
			continue
		}
		changing = append(changing, c)
	}
	return changing
}

// findCall returns the first recorded call whose first argument is verb.
func findCall(t *testing.T, calls [][]string, verb string) []string {
	t.Helper()
	for _, c := range calls {
		if len(c) > 0 && c[0] == verb {
			return c
		}
	}
	t.Fatalf("no %q call in %v", verb, calls)
	return nil
}

// lastCall returns the final recorded call whose first argument is verb.
func lastCall(t *testing.T, calls [][]string, verb string) []string {
	t.Helper()
	for i := len(calls) - 1; i >= 0; i-- {
		if len(calls[i]) > 0 && calls[i][0] == verb {
			return calls[i]
		}
	}
	t.Fatalf("no %q call in %v", verb, calls)
	return nil
}

func TestVMExec_RunsTheCommandInTheGuest(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")
	calls := callLog(t)

	stdout, _, err := runVMStdin(t, strings.NewReader("payload"), "exec", "web",
		"--state-dir", stateDir, "--", "systemctl", "is-active", "nginx")
	if err != nil {
		t.Fatalf("exec error = %v", err)
	}
	if stdout != "payload" {
		t.Errorf("stdout = %q, want the stdin fakedocker echoed back (stdin must reach the guest command)", stdout)
	}

	call := findCall(t, calls(), "exec")
	want := "exec -i granule-vm-web granule exec -- systemctl is-active nginx"
	if got := strings.Join(call, " "); got != want {
		t.Errorf("docker call = %q, want %q", got, want)
	}
}

// TestVMExec_PassesOnTheCommandsExitStatus is the property that makes "vm
// exec" usable in a script: granule exits with the guest command's own
// status, and says nothing of its own about it.
func TestVMExec_PassesOnTheCommandsExitStatus(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")
	t.Setenv("FAKEDOCKER_EXEC_EXIT", "3")

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"vm", "exec", "web", "--state-dir", stateDir, "--", "false"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 3 {
		t.Errorf("exit code = %d, want the guest command's own 3", code)
	}
	if strings.Contains(stderr.String(), "granule:") {
		t.Errorf("stderr = %q, want nothing added over the command's own output", stderr.String())
	}
}

func TestVMExec_UserAndConnectTimeoutReachGranuleExec(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")
	calls := callLog(t)

	if _, _, err := runVMArgs(t, "exec", "web",
		"--state-dir", stateDir,
		"--user", "app",
		"--connect-timeout", "3m",
		"--", "id",
	); err != nil {
		t.Fatalf("exec error = %v", err)
	}

	call := strings.Join(findCall(t, calls(), "exec"), " ")
	for _, want := range []string{"-e GRANULE_EXEC_USER=app", "-e GRANULE_EXEC_CONNECT_TIMEOUT=3m"} {
		if !strings.Contains(call, want) {
			t.Errorf("docker call = %q, want it to contain %q", call, want)
		}
	}
}

func TestVMShell_AsksForAnInteractiveSession(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")
	calls := callLog(t)

	// No terminal on stdin here, so no -t: a shell fed a script over a
	// pipe is still a shell, and docker refuses -t on a pipe.
	if _, _, err := runVMArgs(t, "shell", "web", "--state-dir", stateDir); err != nil {
		t.Fatalf("shell error = %v", err)
	}
	call := findCall(t, calls(), "exec")
	want := "exec -i granule-vm-web granule exec"
	if got := strings.Join(call, " "); got != want {
		t.Errorf("docker call = %q, want %q (no command, so granule exec opens a login shell)", got, want)
	}
}

func TestVMShell_RejectsACommand(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")

	_, _, err := runVMArgs(t, "shell", "web", "--state-dir", stateDir, "--", "uname")
	if err == nil {
		t.Fatal("shell with a command = nil error, want it pointed at \"vm exec\"")
	}
	if !strings.Contains(err.Error(), "vm exec") {
		t.Errorf("error = %v, want it to name \"vm exec\"", err)
	}
}

// TestVMExec_TTYWithoutATerminal covers the mistake docker reports as
// "the input device is not a TTY": say so before shelling out, and say
// what to do about it.
func TestVMExec_TTYWithoutATerminal(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")

	_, _, err := runVMArgs(t, "exec", "web", "--state-dir", stateDir, "-it", "--", "bash")
	if err == nil {
		t.Fatal("exec -it without a terminal = nil error, want one")
	}
	if !strings.Contains(err.Error(), "terminal") {
		t.Errorf("error = %v, want it to explain the missing terminal", err)
	}
}

func TestVMExec_UnknownVM(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")

	_, _, err := runVMArgs(t, "exec", "nope", "--state-dir", stateDir, "--", "true")
	if err == nil {
		t.Fatal("exec of an unknown VM = nil error, want one")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want it to say the VM wasn't found", err)
	}
}

// TestVMRun_CreatesRunsAndDeletes is the whole of the README's Flow 1 in
// one command: the VM is created, waited for, used once and removed, and
// only the guest command's output lands on stdout.
func TestVMRun_CreatesRunsAndDeletes(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	// No -kontur-image and no -disk-mode: a one-off VM on the docker
	// backend takes both from the defaults now, which is what makes this
	// the one-command form the README's Flow 1 shows.
	stdout, stderr, err := runVMStdin(t, strings.NewReader("in"), "run", "oneoff",
		"--state-dir", stateDir,
		"--", "uname", "-a")
	if err != nil {
		t.Fatalf("run error = %v, stderr = %s", err, stderr)
	}
	if stdout != "in" {
		t.Errorf("stdout = %q, want only what the guest command wrote (fakedocker echoes stdin)", stdout)
	}

	var verbs []string
	for _, c := range calls() {
		verbs = append(verbs, c[0])
	}
	joined := strings.Join(verbs, ",")
	for _, want := range []string{"run", "exec", "stop", "rm"} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker calls = %v, want a %q among them", verbs, want)
		}
	}
	// The VM is gone: no saved state, and nothing for a later create to
	// collide with.
	if _, err := vmspec.Load(stateDir, "oneoff"); err == nil {
		t.Error("saved state still present after \"vm run\"")
	}

	// A one-off VM has to be able to boot, so it gets an overlay disk --
	// which is now what "vm create" defaults to as well.
	overlay := false
	for _, c := range calls() {
		if c[0] == "run" && strings.Contains(strings.Join(c, " "), "CHV_DISK_MODE=overlay") {
			overlay = true
		}
	}
	if !overlay {
		t.Error("no docker run asked for CHV_DISK_MODE=overlay")
	}

	// And the image it ran is the locally built one, not the registry
	// reference the static-pod backend needs.
	if call := strings.Join(findCall(t, calls(), "run"), " "); !strings.Contains(call, vmspec.DockerImage) {
		t.Errorf("docker run = %q, want it to use the default %q", call, vmspec.DockerImage)
	}
}

// "vm run" is "vm create" plus a command and a teardown, so the machine
// image has the same say over the sizing it was not given -- which matters
// most here, of all the ways to start a VM: a one-off run is how a machine
// is tried out, and it answering with granule's sizing rather than its own
// makes the machine look like it was built wrong.
func TestVMRun_HonoursTheMachinesOwnSizing(t *testing.T) {
	withFakeDocker(t)
	t.Setenv("FAKEDOCKER_IMAGE_ENV", "CHV_CPUS=2,CHV_MEMORY_MB=1024")
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "run", "oneoff", "--state-dir", stateDir, "--", "true"); err != nil {
		t.Fatalf("run error = %v, stderr = %s", err, stderr)
	}
	call := strings.Join(findCall(t, calls(), "run"), " ")
	if !strings.Contains(call, "CHV_MEMORY_MB=1024") {
		t.Errorf("VM container did not get the machine's own memory size:\n%s", call)
	}
}

func TestVMRun_PassesOnTheCommandsExitStatusAndStillDeletes(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Setenv("FAKEDOCKER_EXEC_EXIT", "7")

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"vm", "run", "oneoff", "--state-dir", stateDir, "--", "false"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 7 {
		t.Errorf("exit code = %d, want the guest command's own 7", code)
	}
	if _, err := vmspec.Load(stateDir, "oneoff"); err == nil {
		t.Error("a non-zero command left the VM behind; it is a result, not a failure")
	}
}

// TestVMRun_UnreachableGuestDeletesTheVM: a VM that never becomes
// reachable is still a VM, and leaving it behind leaks a container, a tap
// and an overlay per run.
func TestVMRun_UnreachableGuestDeletesTheVM(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Setenv("FAKEDOCKER_PROBE_FAIL", "-1")
	t.Setenv("FAKEDOCKER_RUNNING", "false")

	_, _, err := runVMArgs(t, "run", "oneoff", "--state-dir", stateDir, "--", "true")
	if err == nil {
		t.Fatal("run against an unreachable guest = nil error, want one")
	}
	if _, loadErr := vmspec.Load(stateDir, "oneoff"); loadErr == nil {
		t.Error("the VM was left behind after a failed run")
	}

	// ... unless asked to keep it, which is how the console of a guest
	// that never came up gets looked at.
	t.Setenv("FAKEDOCKER_LOG", filepath.Join(t.TempDir(), "keep.log"))
	if _, _, err := runVMArgs(t, "run", "keeper", "--state-dir", stateDir,
		"--keep-on-failure", "--", "true"); err == nil {
		t.Fatal("run with -keep-on-failure = nil error, want one")
	}
	if _, loadErr := vmspec.Load(stateDir, "keeper"); loadErr != nil {
		t.Errorf("-keep-on-failure removed the VM anyway: %v", loadErr)
	}
}

func TestVMRun_NeedsACommand(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")

	_, _, err := runVMArgs(t, "run", "oneoff", "--state-dir", stateDir)
	if err == nil {
		t.Fatal("run with no command = nil error, want one")
	}
	if !strings.Contains(err.Error(), "command is required") {
		t.Errorf("error = %v, want it to ask for a command", err)
	}
	// Nothing was created on the way to that error.
	if _, loadErr := vmspec.Load(stateDir, "oneoff"); loadErr == nil {
		t.Error("a VM was created despite the missing command")
	}
}

// TestVMRun_RefusesTheStaticPodBackend: the backend that wrote a
// manifest for a standalone kubelet is gone, and a one-off VM that was
// asked for under it is one this command will not create -- so it says
// so, and points at where a cluster deployment is documented instead.
func TestVMRun_RefusesTheStaticPodBackend(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")

	_, _, err := runVMArgs(t, "run", "oneoff", "--backend", "static-pod",
		"--state-dir", stateDir, "--", "true")
	if err == nil {
		t.Fatal("run -backend static-pod = nil error, want one")
	}
	if !strings.Contains(err.Error(), "deploy/k8s") {
		t.Errorf("error = %v, want it to explain the backend and point at the cluster docs", err)
	}
	// Nothing was created on the way to that error.
	if _, loadErr := vmspec.Load(stateDir, "oneoff"); loadErr == nil {
		t.Error("a VM was created despite the refused backend")
	}
}

// The caller's terminal type has to be put on the docker exec by hand
// for a terminal session: docker's own default for "exec -t" is a flat
// "xterm" no matter what is on this screen, so that -- and not the
// caller's TERM -- is what "granule exec" would forward into the guest.
func TestExecEnv_ATerminalSessionCarriesTheCallersTerminalType(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")

	got := strings.Join((execSettings{tty: true}).execEnv(), " ")
	if !strings.Contains(got, "TERM=xterm-256color") {
		t.Errorf("exec environment = %q, want the caller's own TERM in it", got)
	}
	// Without a terminal there is nothing to forward: the guest session
	// has no pty either, and "dumb" is the right answer for one that
	// hasn't (see pkg/agent).
	if got := (execSettings{}).execEnv(); len(got) != 0 {
		t.Errorf("exec environment = %q for a session with no terminal, want nothing", got)
	}
}

func TestStatusError(t *testing.T) {
	if err := statusError(0); err != nil {
		t.Errorf("statusError(0) = %v, want nil", err)
	}
	var status *exitStatusError
	if err := statusError(5); !errors.As(err, &status) || status.code != 5 {
		t.Errorf("statusError(5) = %v, want an exitStatusError carrying 5", err)
	}
}

// -w and -e belong to the guest command rather than to the session, so
// they travel as "granule exec"'s own flags -- next to, and distinct
// from, the -e docker exec takes for GRANULE_EXEC_USER and friends.
func TestVMExec_WorkdirAndEnvReachTheGuestCommand(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")
	calls := callLog(t)

	if _, _, err := runVMArgs(t, "exec", "web",
		"--state-dir", stateDir,
		"-w", "/src",
		"-e", "GOFLAGS=-mod=vendor",
		"--env", "CI=1",
		"--", "go", "build", "./...",
	); err != nil {
		t.Fatalf("exec error = %v", err)
	}

	call := strings.Join(findCall(t, calls(), "exec"), " ")
	want := "exec -i granule-vm-web granule exec -w /src -e GOFLAGS=-mod=vendor -e CI=1 -- go build ./..."
	if call != want {
		t.Errorf("docker call = %q, want %q", call, want)
	}
}

// A mistyped -e is caught here rather than in the guest, where the
// message would come back wrapped in a container name.
func TestVMExec_RejectsAnEnvEntryThatIsNotKeyValue(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")

	_, _, err := runVMArgs(t, "exec", "web", "--state-dir", stateDir, "-e", "GOFLAGS", "--", "true")
	if err == nil {
		t.Fatal("exec -e with a bare variable name = nil error, want one")
	}
	if !strings.Contains(err.Error(), "KEY=value") {
		t.Errorf("error = %v, want it to say what the flag takes", err)
	}
}

// "vm run" takes the same two, since a one-off VM exists to run one
// command and that command wants the same directory and environment any
// other one does.
func TestVMRun_WorkdirAndEnvReachTheGuestCommand(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "run", "oneoff",
		"--state-dir", stateDir,
		"-w", "/src",
		"-e", "GOFLAGS=-mod=vendor",
		"--", "go", "test", "./...",
	); err != nil {
		t.Fatalf("run error = %v, stderr = %s", err, stderr)
	}

	// The last exec, not the first: the readiness wait runs "granule exec
	// -- true" of its own before the command this asked for.
	call := strings.Join(lastCall(t, calls(), "exec"), " ")
	if !strings.Contains(call, "granule exec -w /src -e GOFLAGS=-mod=vendor -- go test ./...") {
		t.Errorf("docker call = %q, want the workdir and environment on the guest command", call)
	}
}

// And a mistyped one is refused before a VM is created for it, not after
// the whole boot-and-wait.
func TestVMRun_RejectsABadEnvEntryBeforeCreatingAnything(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	_, _, err := runVMArgs(t, "run", "oneoff", "--state-dir", stateDir, "-e", "GOFLAGS", "--", "true")
	if err == nil {
		t.Fatal("run -e with a bare variable name = nil error, want one")
	}
	if len(calls()) != 0 {
		t.Errorf("docker was called %v before the flag was rejected", calls())
	}
}
