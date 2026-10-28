package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scriptedRunner is a sandboxRunner that answers each command from a
// queue of canned results and records what it was asked to run, so a
// test can assert both what ensureGuestKVMAccess did and what it did
// *not* do -- the ordinary case being one probe and nothing else.
type scriptedRunner struct {
	replies []runReply
	ran     []string
}

type runReply struct {
	stdout   string
	stderr   string
	exitCode int
}

func (r *scriptedRunner) Run(ctx context.Context, argv []string, stdin string) (string, string, int) {
	r.ran = append(r.ran, argv[len(argv)-1])
	if len(r.replies) == 0 {
		return "", "", 0
	}
	reply := r.replies[0]
	r.replies = r.replies[1:]
	return reply.stdout, reply.stderr, reply.exitCode
}

// probeReply is what nestedVirtProbe prints for one state, in the shape
// parseNestedVirt reads it out of: the guest emits this line among the
// /proc files and the `df` sharing that stream.
func probeReply(state string) runReply {
	return runReply{stdout: nestedVirtPrefix + " " + state + "\n"}
}

func TestEnsureGuestKVMAccessLeavesAWorkingGuestAlone(t *testing.T) {
	t.Parallel()
	// ensureGuestKVMAccess says what it repaired and what it could not,
	// so these claim the log for themselves -- see log_test.go.
	logToTest(t)
	for _, state := range []string{NestedVirtReady, NestedVirtNoDevice, NestedVirtUnsupported, ""} {
		runner := &scriptedRunner{replies: []runReply{probeReply(state)}}
		(&GranuleSandboxes{}).ensureGuestKVMAccess(context.Background(), "g-t1-1", runner)

		if len(runner.ran) != 1 {
			t.Errorf("state %q: ran %d command(s) %q, want only the probe", state, len(runner.ran), runner.ran)
		}
	}
}

func TestEnsureGuestKVMAccessGrantsTheKVMGroupOnADeniedGuest(t *testing.T) {
	t.Parallel()
	logToTest(t)
	runner := &scriptedRunner{replies: []runReply{
		probeReply(NestedVirtDenied),
		{},
		probeReply(NestedVirtReady),
	}}

	(&GranuleSandboxes{}).ensureGuestKVMAccess(context.Background(), "g-t1-1", runner)

	if len(runner.ran) != 3 {
		t.Fatalf("ran %d command(s) %q, want probe, grant, probe", len(runner.ran), runner.ran)
	}
	if runner.ran[1] != guestKVMGrant {
		t.Errorf("second command = %q, want the grant %q", runner.ran[1], guestKVMGrant)
	}
	// The verification has to be its own command: supplementary groups
	// are resolved when a session is created, so the session that ran the
	// grant could never observe it (ensureGuestKVMAccess' own comment).
	if runner.ran[2] != nestedVirtProbe {
		t.Errorf("third command = %q, want a second probe %q", runner.ran[2], nestedVirtProbe)
	}
}

func TestEnsureGuestKVMAccessStopsAtAFailedGrant(t *testing.T) {
	t.Parallel()
	logToTest(t)
	runner := &scriptedRunner{replies: []runReply{
		probeReply(NestedVirtDenied),
		{stderr: "sudo: a password is required", exitCode: 1},
	}}

	(&GranuleSandboxes{}).ensureGuestKVMAccess(context.Background(), "g-t1-1", runner)

	if len(runner.ran) != 2 {
		t.Errorf("ran %d command(s) %q, want the probe and the grant only -- there is nothing to verify after a grant that failed",
			len(runner.ran), runner.ran)
	}
}

// TestGuestKVMGrantScript runs the grant itself under a real shell,
// against stub getent/sudo/usermod/id binaries, because everything that
// can be wrong with it is shell rather than Go: whether the account name
// reaches usermod, and whether a guest with no kvm group fails instead of
// granting membership of something /dev/kvm is not owned by.
func TestGuestKVMGrantScript(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the grant is a POSIX shell command run inside a Linux guest")
	}

	for _, tc := range []struct {
		name     string
		kvmGroup bool
		wantExit int
	}{
		{name: "grants the account the kvm group", kvmGroup: true},
		{name: "fails on a guest with no kvm group", wantExit: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			record := filepath.Join(dir, "usermod.args")
			writeStub(t, dir, "id", "echo sandbox-account\n")
			writeStub(t, dir, "usermod", "echo \"$@\" > "+record+"\n")
			writeStub(t, dir, "sudo", "shift; exec \"$@\"\n") // drops the -n
			getent := "exit 2\n"
			if tc.kvmGroup {
				getent = "echo kvm:x:103:\n"
			}
			writeStub(t, dir, "getent", getent)

			cmd := exec.Command("sh", "-c", guestKVMGrant)
			cmd.Env = append(os.Environ(), "PATH="+dir)
			out, err := cmd.CombinedOutput()

			if got := cmd.ProcessState.ExitCode(); got != tc.wantExit {
				t.Fatalf("exit code = %d (%v), want %d -- output: %s", got, err, tc.wantExit, out)
			}
			if !tc.kvmGroup {
				if !strings.Contains(string(out), "no kvm group") {
					t.Errorf("output = %q, want it to say the guest has no kvm group", out)
				}
				return
			}
			args, readErr := os.ReadFile(record)
			if readErr != nil {
				t.Fatalf("usermod was never run: %v (output: %s)", readErr, out)
			}
			if strings.TrimSpace(string(args)) != "-aG kvm sandbox-account" {
				t.Errorf("usermod args = %q, want %q", strings.TrimSpace(string(args)), "-aG kvm sandbox-account")
			}
		})
	}
}

// writeStub installs a one-line shell script named name in dir, which
// the test puts on PATH in place of the real binary.
func writeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("writing %s stub: %v", name, err)
	}
}

func TestEnsureGuestTmpfs(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{replies: []runReply{{}}}
	(&GranuleSandboxes{}).ensureGuestTmpfs(context.Background(), "g-t1-1", runner)
	if len(runner.ran) != 1 || runner.ran[0] != guestTmpfsMount {
		t.Fatalf("ensureGuestTmpfs ran %q, want [%q]", runner.ran, guestTmpfsMount)
	}
}

func TestGuestTmpfsMountScript(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("guestTmpfsMount is a POSIX shell command run inside a Linux guest")
	}

	for _, tc := range []struct {
		name      string
		fstype    string
		wantMount bool
	}{
		{name: "mounts tmpfs when /tmp is on ext4", fstype: "ext4", wantMount: true},
		{name: "mounts tmpfs when findmnt fails", fstype: "", wantMount: true},
		{name: "skips mount when /tmp is already tmpfs", fstype: "tmpfs", wantMount: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			record := filepath.Join(dir, "mount.args")
			if tc.fstype == "" {
				writeStub(t, dir, "findmnt", "exit 1\n")
			} else {
				writeStub(t, dir, "findmnt", "echo "+tc.fstype+"\n")
			}
			writeStub(t, dir, "sudo", "shift; exec \"$@\"\n") // drops -n
			writeStub(t, dir, "mount", "echo \"$@\" > "+record+"\n")

			cmd := exec.Command("sh", "-c", guestTmpfsMount)
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("guestTmpfsMount failed: %v\n%s", err, out)
			}

			args, readErr := os.ReadFile(record)
			if !tc.wantMount {
				if readErr == nil {
					t.Fatalf("mount was run when /tmp was already tmpfs: %q", string(args))
				}
				return
			}
			if readErr != nil {
				t.Fatalf("mount was not run: %v (output: %s)", readErr, out)
			}
			want := "-t tmpfs -o mode=1777,nosuid,nodev,size=50% tmpfs /tmp"
			if got := strings.TrimSpace(string(args)); got != want {
				t.Errorf("mount args = %q, want %q", got, want)
			}
		})
	}
}
