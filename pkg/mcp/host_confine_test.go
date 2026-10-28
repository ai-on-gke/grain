package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testAccessCode = "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"

// requireConfinement skips a test on a host with no bubblewrap at all,
// and fails it on one where bwrap is installed but cannot confine a
// command: that is a host a single-tenant daemon refuses to start host
// sandboxes on, and a test that skipped there would hide it.
func requireConfinement(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(confineBinary); err != nil {
		t.Skipf("%s not installed: %v", confineBinary, err)
	}
	if err := CheckHostConfinement(context.Background()); err != nil {
		t.Fatalf("CheckHostConfinement: %v", err)
	}
}

func runIn(t *testing.T, tools []Tool, command string) Result {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == "run_command" {
			return tool.Handler(context.Background(), map[string]any{"command": command})
		}
	}
	t.Fatal("no run_command tool")
	return Result{}
}

// A single-tenant daemon's access code is <data-dir>/local-api-token,
// readable by the account a host sandbox's commands run as. With the
// data directory hidden, a run_command cannot read it by its path, by
// another path through /proc, or out of the environment of a process
// outside the sandbox (its token file's name, the way to the
// code) -- and the sandbox's own directory, here
// inside the data directory as -sandbox-dir may well be, is still its
// own (grain/task-4366).
func TestHostSandboxRunCommandCannotReadTheAccessCode(t *testing.T) {
	requireConfinement(t)
	dataDir := t.TempDir()
	token := filepath.Join(dataDir, "local-api-token")
	if err := os.WriteFile(token, []byte(testAccessCode+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dataDir, "sandboxes", "runs", "run-1")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	tools := NewHostSandboxTools(root, []string{dataDir})

	for name, command := range map[string]string{
		"by its path":          "cat " + token,
		"through /proc/root":   fmt.Sprintf("cat /proc/%d/root%s", os.Getpid(), token),
		"by listing data dir":  "ls -la " + dataDir + " && cat " + dataDir + "/*",
		"out of /proc environ": fmt.Sprintf("cat /proc/%d/environ", os.Getpid()),
		"after an umount":      "umount " + dataDir + "; cat " + token,
		"from a nested userns": "unshare -Urm sh -c 'umount " + dataDir + "; cat " + token + "'",
	} {
		t.Run(name, func(t *testing.T) {
			res := runIn(t, tools, command)
			if strings.Contains(res.Text, testAccessCode) {
				t.Fatalf("run_command %q read the access code:\n%s", command, res.Text)
			}
		})
	}

	res := runIn(t, tools, "pwd && echo kept > mine && cat mine")
	if res.IsError || !strings.Contains(res.Text, root) || !strings.Contains(res.Text, "kept") {
		t.Fatalf("the sandbox's own directory is not its own under confinement:\n%s", res.Text)
	}
	if got, err := os.ReadFile(filepath.Join(root, "mine")); err != nil || string(got) != "kept\n" {
		t.Fatalf("a file written in the sandbox = %q, %v; want it on the host", got, err)
	}
	if got, err := os.ReadFile(token); err != nil || !strings.Contains(string(got), testAccessCode) {
		t.Fatalf("the access code file on the host = %q, %v; want it untouched", got, err)
	}
}

// Without a hide list run_command is what it always was: nothing is
// wrapped, so a host with no bubblewrap is unaffected. That is also why
// a single-tenant daemon has to pass one: the same command, unconfined,
// reads the code.
func TestHostSandboxRunCommandWithoutAHideListIsUnconfined(t *testing.T) {
	dataDir := t.TempDir()
	token := filepath.Join(dataDir, "local-api-token")
	if err := os.WriteFile(token, []byte(testAccessCode), 0o600); err != nil {
		t.Fatal(err)
	}
	res := runIn(t, NewSandboxTools(t.TempDir()), "cat "+token)
	if !strings.Contains(res.Text, testAccessCode) {
		t.Fatalf("an unconfined run_command could not read a file its account owns:\n%s", res.Text)
	}
}

// With a hide list and no bubblewrap, run_command refuses rather than
// running the command unconfined.
func TestHostSandboxRunCommandFailsClosedWithoutBubblewrap(t *testing.T) {
	dataDir := t.TempDir()
	token := filepath.Join(dataDir, "local-api-token")
	if err := os.WriteFile(token, []byte(testAccessCode), 0o600); err != nil {
		t.Fatal(err)
	}
	// bash is found by its absolute path below if the refusal ever
	// stopped happening, so an empty PATH cannot make this pass by
	// accident.
	t.Setenv("PATH", t.TempDir())
	res := runIn(t, NewHostSandboxTools(t.TempDir(), []string{dataDir}), "/bin/cat "+token)
	if !res.IsError || strings.Contains(res.Text, testAccessCode) || !strings.Contains(res.Text, "bubblewrap") {
		t.Fatalf("run_command with no bwrap = %+v, want a refusal naming bubblewrap", res)
	}
	if err := CheckHostConfinement(context.Background()); err == nil {
		t.Fatal("CheckHostConfinement with no bwrap = nil, want an error")
	}
}

func TestConfinedArgv(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	token := filepath.Join(base, "elsewhere-token")
	inside := filepath.Join(dataDir, "sb", "run-1")
	outside := filepath.Join(base, "sb", "run-1")
	for _, dir := range []string{inside, outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(token, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	argv := []string{"bash", "-c", "true"}
	tail := func(root string) string { return " --chdir " + root + " -- bash -c true" }
	head := "bwrap --die-with-parent --unshare-pid --dev-bind / / --proc /proc"
	for name, tc := range map[string]struct {
		root string
		hide []string
		want string
	}{
		"root outside": {outside, []string{dataDir, token},
			head + " --tmpfs " + dataDir + " --ro-bind /dev/null " + token + tail(outside)},
		"root inside": {inside, []string{dataDir},
			head + " --tmpfs " + dataDir + " --bind " + inside + " " + inside + tail(inside)},
		"missing path": {outside, []string{filepath.Join(base, "gone")}, head + tail(outside)},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := confinedArgv("bwrap", tc.root, tc.hide, argv)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, " ") != tc.want {
				t.Errorf("confinedArgv =\n  %s\nwant\n  %s", strings.Join(got, " "), tc.want)
			}
		})
	}
	if _, err := confinedArgv("bwrap", inside, []string{inside}, argv); err == nil {
		t.Error("confinedArgv hiding the sandbox root itself = nil error")
	}
}
