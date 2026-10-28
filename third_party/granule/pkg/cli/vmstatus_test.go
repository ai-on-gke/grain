package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// statusFact returns the value "vm status" printed for a fact, so the
// tests can assert on facts rather than on column widths.
func statusFact(t *testing.T, out, key string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == key {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), key))
		}
	}
	t.Fatalf("no %s line in:\n%s", key, out)
	return ""
}

// TestVMStatus_ReportsAReadyGuestWithoutWaiting is what "vm status" is
// for: the same question "vm wait" blocks on, asked once and answered
// now, with the saved spec alongside it.
func TestVMStatus_ReportsAReadyGuestWithoutWaiting(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")
	calls := callLog(t)

	stdout, stderr, err := runVMArgs(t, "status", "web", "--state-dir", stateDir)
	if err != nil {
		t.Fatalf("status error = %v, stderr = %s", err, stderr)
	}
	for key, want := range map[string]string{
		"NAME":      "web",
		"DISK_MODE": "overlay",
		"CONTAINER": "granule-vm-web",
		"STATE":     "running",
		"GUEST":     "ready",
	} {
		if got := statusFact(t, stdout, key); got != want {
			t.Errorf("%s = %q, want %q\n%s", key, got, want, stdout)
		}
	}
	// It has to ask once and return: a status that quietly waited out a
	// boot would be "vm wait" under another name.
	if n := countCalls(calls(), "exec"); n != 1 {
		t.Errorf("%d guest probes, want exactly 1 -- a status must not become a wait", n)
	}
}

// TestVMStatus_UnreachableGuestExitsNonZero is the property a script
// branches on, and the facts have to be printed anyway: they are most
// wanted when the answer is "not up".
func TestVMStatus_UnreachableGuestExitsNonZero(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")
	calls := callLog(t)
	t.Setenv("FAKEDOCKER_PROBE_FAIL", "-1")

	stdout, stderr, err := runVMArgs(t, "status", "web", "--state-dir", stateDir)
	if err == nil {
		t.Fatal("status of a guest that never answered = nil error, want a non-zero exit")
	}
	if got := statusFact(t, stdout, "GUEST"); !strings.HasPrefix(got, "unreachable") {
		t.Errorf("GUEST = %q, want it reported unreachable", got)
	}
	// Why it isn't ready is the whole reason to ask without waiting.
	if !strings.Contains(stdout, "not reachable yet") {
		t.Errorf("stdout = %q, want the probe's own reason in it", stdout)
	}
	if got := statusFact(t, stdout, "STATE"); got != "running" {
		t.Errorf("STATE = %q, want the container still reported as running", got)
	}
	// A guest that is still booting is the ordinary reason to see this,
	// so the command that waits for it is worth naming.
	if !strings.Contains(err.Error()+stderr, "vm wait web") {
		t.Errorf("error = %v, want it to point at the wait", err)
	}
	if n := countCalls(calls(), "exec"); n != 1 {
		t.Errorf("%d probes, want exactly 1: status asks, it doesn't wait", n)
	}
}

// The question "vm status" exists to answer, for a VM that has a
// -ready-file: which of the two conditions is outstanding. A guest that
// answers and has not declared itself ready is a healthy VM with a
// workload that hasn't come up, and reporting it as "unreachable" would
// send whoever asked to the console instead of into the guest.
func TestVMStatus_SaysWhichConditionIsOutstanding(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")
	t.Setenv("FAKEDOCKER_PROBE_UNDECLARED", "1")

	stdout, stderr, err := runVMArgs(t, "status", "web", "--state-dir", stateDir)
	if err == nil {
		t.Fatal("status of a guest that has not declared itself ready = nil error, want a non-zero exit")
	}
	got := statusFact(t, stdout, "GUEST")
	if !strings.Contains(got, "has not declared itself ready") {
		t.Errorf("GUEST = %q, want it to say the declaration is what is outstanding\n%s", got, stdout)
	}
	if strings.Contains(got, "unreachable") {
		t.Errorf("GUEST = %q, want a guest that answered not reported unreachable", got)
	}
	// The path the guest was supposed to create is in the probe's own
	// message, and it is the thing to go and look for in the guest.
	if !strings.Contains(stdout, "/run/granule/ready") {
		t.Errorf("stdout = %q, want the ready file named in it", stdout)
	}
	if !strings.Contains(err.Error()+stderr, "vm exec web") {
		t.Errorf("error = %v, want it to point into the guest rather than at the boot", err)
	}
	if got := statusFact(t, stdout, "STATE"); got != "running" {
		t.Errorf("STATE = %q, want the container still reported as running", got)
	}
}

// The same distinction in the one word "vm list -status" has room for.
func TestVMList_UndeclaredGuestHasItsOwnWord(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")
	t.Setenv("FAKEDOCKER_PROBE_UNDECLARED", "1")

	stdout, stderr, err := runVMArgs(t, "list", "-status", "--state-dir", stateDir)
	if err != nil {
		t.Fatalf("list error = %v, stderr = %s", err, stderr)
	}
	if !strings.Contains(stdout, "undeclared") {
		t.Errorf("stdout = %q, want the undeclared guest told apart from an unreachable one", stdout)
	}
}

// A VM created before "granule ready" existed keeps the image it was
// created from, and "vm status" has to keep answering for it: the
// fallback probe reports the same word, and the line says which probe
// answered so that nobody reads more into it than it is worth.
func TestVMStatus_ImageWithoutTheReadyMode(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")
	t.Setenv("FAKEDOCKER_NO_READY_MODE", "1")

	stdout, stderr, err := runVMArgs(t, "status", "web", "--state-dir", stateDir)
	if err != nil {
		t.Fatalf("status error = %v, stderr = %s", err, stderr)
	}
	got := statusFact(t, stdout, "GUEST")
	if !strings.HasPrefix(got, "ready") {
		t.Errorf("GUEST = %q, want the guest still reported ready\n%s", got, stdout)
	}
	if !strings.Contains(got, "predates") {
		t.Errorf("GUEST = %q, want it to say the answer came from the fallback probe", got)
	}
	// One word is all "vm list -status" has room for, and the word is
	// the same one: the VM is ready, however it was asked.
	listOut, _, err := runVMArgs(t, "list", "-status", "--state-dir", stateDir)
	if err != nil {
		t.Fatalf("list error = %v", err)
	}
	if !strings.Contains(listOut, "ready") {
		t.Errorf("list stdout = %q, want the VM listed as ready", listOut)
	}
}

// A container that has exited and one that was never created are both
// "no guest to ask", but they want different things done about them, so
// they are reported and explained apart.
func TestVMStatus_ContainerStates(t *testing.T) {
	for _, tc := range []struct {
		name      string
		env       func(t *testing.T)
		wantState string
		wantErr   string
	}{
		{"exited", func(t *testing.T) { t.Setenv("FAKEDOCKER_RUNNING", "false") }, "exited", "docker logs granule-vm-web"},
		{"never started", func(t *testing.T) { t.Setenv("FAKEDOCKER_MISSING", "granule-vm-web") }, "missing", "no container granule-vm-web"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withFakeDocker(t)
			stateDir := createDockerVM(t, "web")
			calls := callLog(t)
			tc.env(t)

			stdout, _, err := runVMArgs(t, "status", "web", "--state-dir", stateDir)
			if err == nil {
				t.Fatal("status of a VM with no running container = nil error, want a non-zero exit")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tc.wantErr)
			}
			if got := statusFact(t, stdout, "STATE"); got != tc.wantState {
				t.Errorf("STATE = %q, want %q", got, tc.wantState)
			}
			// Nothing to probe through: an exec against a container that
			// isn't running reports docker's failure, not the guest's.
			if n := countCalls(calls(), "exec"); n != 0 {
				t.Errorf("%d guest probes against a VM with no running container, want none", n)
			}
		})
	}
}

func TestVMStatus_UnknownVM(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")

	_, _, err := runVMArgs(t, "status", "nosuch", "--state-dir", stateDir)
	if err == nil {
		t.Fatal("status of an unknown VM = nil error, want one")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want it to say the VM wasn't found", err)
	}
}

// TestVMList_StatusIsOptIn: a plain listing is a read of the state
// directory and nothing else, which is what keeps it usable with no
// docker daemon around at all.
func TestVMList_StatusIsOptIn(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")
	calls := callLog(t)

	stdout, stderr, err := runVMArgs(t, "list", "--state-dir", stateDir)
	if err != nil {
		t.Fatalf("list error = %v, stderr = %s", err, stderr)
	}
	if strings.Contains(stdout, "STATUS") {
		t.Errorf("stdout = %q, want no STATUS column without -status", stdout)
	}
	if n := len(calls()); n != 0 {
		t.Errorf("%d docker calls for a plain listing, want none: %v", n, calls())
	}
}

func TestVMList_StatusColumn(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")
	callLog(t)

	stdout, stderr, err := runVMArgs(t, "list", "--state-dir", stateDir, "--status")
	if err != nil {
		t.Fatalf("list -status error = %v, stderr = %s", err, stderr)
	}
	if !strings.Contains(stdout, "STATUS") {
		t.Errorf("stdout = %q, want a STATUS column", stdout)
	}
	if !strings.Contains(stdout, "ready") {
		t.Errorf("stdout = %q, want the reachable guest reported ready", stdout)
	}

	t.Setenv("FAKEDOCKER_PROBE_FAIL", "-1")
	stdout, _, err = runVMArgs(t, "list", "--state-dir", stateDir, "--status")
	if err != nil {
		t.Fatalf("list -status error = %v", err)
	}
	// A VM that isn't up is a row like any other: listing every VM is
	// still what this command does, so it reports rather than fails.
	if !strings.Contains(stdout, "unreachable") {
		t.Errorf("stdout = %q, want the guest that never answered reported unreachable", stdout)
	}
}

func TestVMList_StatusMultipleVMs(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	for _, name := range []string{"vm1", "vm2", "vm3"} {
		if _, stderr, err := runVMArgs(t, "create", name,
			"--backend", "docker",
			"--disk-mode", "overlay",
			"--state-dir", stateDir,
		); err != nil {
			t.Fatalf("create %s error = %v, stderr = %s", name, err, stderr)
		}
	}

	stdout, stderr, err := runVMArgs(t, "list", "--state-dir", stateDir, "--status")
	if err != nil {
		t.Fatalf("list -status error = %v, stderr = %s", err, stderr)
	}
	for _, name := range []string{"vm1", "vm2", "vm3"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("stdout = %q, want %s in output", stdout, name)
		}
	}
	if strings.Count(stdout, "ready") != 3 {
		t.Errorf("stdout = %q, want 3 ready statuses", stdout)
	}
}

// createPublishedVM creates a VM with the grant an EXPOSE needs to mean
// anything ("-publish-declared"), against whatever the fake docker's
// FAKEDOCKER_EXPOSED_PORTS says its image declares.
func createPublishedVM(t *testing.T, name string) string {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	if _, stderr, err := runVMArgs(t, "create", name,
		"--disk-mode", "overlay",
		"--publish-declared",
		"--state-dir", stateDir,
	); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	return stateDir
}

// TestVMStatus_ReportsPublishedPorts is the question this command could
// not answer before: "vm create" prints the host ports docker assigned
// once, and afterwards they live only on the container -- so asking
// later meant knowing granule's container naming well enough to run
// "docker port granule-vm-web" by hand.
func TestVMStatus_ReportsPublishedPorts(t *testing.T) {
	withFakeDocker(t)
	t.Setenv("FAKEDOCKER_EXPOSED_PORTS", "8080/tcp")
	t.Setenv("FAKEDOCKER_PORTS", "8080/tcp -> 0.0.0.0:32768,8080/tcp -> [::]:32768")
	stateDir := createPublishedVM(t, "web")

	stdout, stderr, err := runVMArgs(t, "status", "web", "--state-dir", stateDir)
	if err != nil {
		t.Fatalf("status error = %v, stderr = %s", err, stderr)
	}

	// The binding address is part of the answer, and so is what it
	// means: a port bound to every interface is reachable from every
	// network this host is on, which is worth saying in words.
	if got := statusFact(t, stdout, "PORTS"); got != "8080/tcp -> 0.0.0.0:32768 (every interface)" {
		t.Errorf("PORTS = %q, want the mapping docker holds and who can reach it\n%s", got, stdout)
	}
	// Every mapping, not just the first: a guest port bound on IPv4 and
	// on IPv6 is two facts about who can reach it.
	if !strings.Contains(stdout, "[::]:32768") {
		t.Errorf("stdout = %q, want both bindings reported", stdout)
	}

	// And the same fact collapsed into the column "vm list -status" has
	// room for, with the two bindings of one host port said once.
	listOut, _, err := runVMArgs(t, "list", "-status", "--state-dir", stateDir)
	if err != nil {
		t.Fatalf("list error = %v", err)
	}
	if !strings.Contains(listOut, "PORTS") || !strings.Contains(listOut, "32768->8080/tcp") {
		t.Errorf("list stdout = %q, want a PORTS column naming the host port", listOut)
	}
	if strings.Count(listOut, "32768->8080/tcp") != 1 {
		t.Errorf("list stdout = %q, want one entry per guest port", listOut)
	}
}

// The confusion the grant invites: a VM that publishes nothing because
// nobody granted its image's declaration looks exactly like one whose
// image declares nothing. They want completely different things done
// about them, so the PORTS line says which it is.
func TestVMStatus_PortsSayWhichNothingItIs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared string
		granted  bool
		want     []string
	}{
		{
			name:     "declared but not granted",
			declared: "8080/tcp",
			want:     []string{"8080/tcp", "-publish-declared"},
		},
		{
			name:    "nothing declared",
			granted: true,
			want:    []string{"declares no ports"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withFakeDocker(t)
			t.Setenv("FAKEDOCKER_EXPOSED_PORTS", tc.declared)
			t.Setenv("FAKEDOCKER_PORTS", "")
			var stateDir string
			if tc.granted {
				stateDir = createPublishedVM(t, "web")
			} else {
				stateDir = createDockerVM(t, "web")
			}

			stdout, stderr, err := runVMArgs(t, "status", "web", "--state-dir", stateDir)
			if err != nil {
				t.Fatalf("status error = %v, stderr = %s", err, stderr)
			}
			got := statusFact(t, stdout, "PORTS")
			if !strings.HasPrefix(got, "none") {
				t.Errorf("PORTS = %q, want it to start by saying nothing is published", got)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("PORTS = %q, want it to mention %q", got, want)
				}
			}
		})
	}
}

// A VM with no container at all publishes nothing, and reporting that is
// not a reason for "vm status" to fail: the rest of the facts are what
// whoever asked came for.
func TestVMStatus_PortsOfAMissingContainer(t *testing.T) {
	withFakeDocker(t)
	stateDir := createDockerVM(t, "web")
	t.Setenv("FAKEDOCKER_MISSING", "granule-vm-web")

	stdout, _, err := runVMArgs(t, "status", "web", "--state-dir", stateDir)
	if err == nil {
		t.Fatal("status of a VM with no container = nil error, want a non-zero exit")
	}
	if got := statusFact(t, stdout, "PORTS"); !strings.HasPrefix(got, "none") {
		t.Errorf("PORTS = %q, want nothing published reported for a container that isn't there\n%s", got, stdout)
	}
}
