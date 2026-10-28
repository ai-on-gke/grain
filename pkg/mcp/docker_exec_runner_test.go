package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeFakeDockerExec installs a shell script named "docker" on PATH (a
// temp directory prepended ahead of the real PATH, restored on cleanup)
// that records each of its own arguments on its own line and then behaves
// as the script body given -- enough to prove DockerExecRunner's own
// invocation and exit-code handling without a docker daemon anywhere.
func writeFakeDockerExec(t *testing.T, body string) (argsFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake docker script is POSIX shell only")
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args.txt")
	script := fmt.Sprintf(`#!/bin/sh
: > %q
for arg in "$@"; do
  printf '%%s\n' "$arg" >> %q
done
%s
`, argsFile, argsFile, body)
	path := filepath.Join(dir, "docker")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func readArgs(t *testing.T, argsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// The guest command has to arrive as a real argv after "granule exec --",
// not shell-quoted into one string the way an `ssh host <command>` call
// would have to quote it: "granule exec" does that join itself (see Run's
// own doc comment), so quoting here too would double-quote every argument
// that needed it.
func TestDockerExecRunnerPassesArgvThroughUnquoted(t *testing.T) {
	argsFile := writeFakeDockerExec(t, "exit 0")

	r := &DockerExecRunner{
		Container:      "granule-vm-sandbox-1",
		User:           "debian",
		KeyPath:        "/images/granule_id_ed25519",
		ConnectTimeout: 90 * time.Second,
	}
	if _, _, code := r.Run(context.Background(), []string{"sh", "-c", "echo hello world"}, ""); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	got := readArgs(t, argsFile)
	want := []string{
		"exec",
		"-e", "GRANULE_EXEC_USER=debian",
		"-e", "GRANULE_EXEC_KEY=/images/granule_id_ed25519",
		"-e", "GRANULE_EXEC_CONNECT_TIMEOUT=1m30s",
		"granule-vm-sandbox-1", "granule", "exec", "--",
		"sh", "-c", "echo hello world",
	}
	if len(got) != len(want) {
		t.Fatalf("docker args = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("docker arg %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// Every one of the three -e flags is omitted when its field is unset, so
// an unset field leaves guestexec's own default in place rather than
// overriding it with an empty value.
func TestDockerExecRunnerOmitsUnsetEnvFlags(t *testing.T) {
	argsFile := writeFakeDockerExec(t, "exit 0")

	r := &DockerExecRunner{Container: "granule-vm-sandbox-1"}
	r.Run(context.Background(), []string{"true"}, "")

	got := strings.Join(readArgs(t, argsFile), " ")
	if strings.Contains(got, "GRANULE_EXEC_") {
		t.Errorf("docker args = %q, want no GRANULE_EXEC_* flags for a zero-valued runner", got)
	}
	if want := "exec granule-vm-sandbox-1 granule exec -- true"; got != want {
		t.Errorf("docker args = %q, want %q", got, want)
	}
}

// -i is what gives the exec'd process a stdin to read at all, so it has
// to be there exactly when there is stdin to pipe -- and not otherwise.
func TestDockerExecRunnerPassesStdinOnlyWhenThereIsSome(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stdin string
		wantI bool
	}{
		{name: "with stdin", stdin: "payload", wantI: true},
		{name: "without stdin", stdin: "", wantI: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argsFile := writeFakeDockerExec(t, "cat")

			r := &DockerExecRunner{Container: "granule-vm-sandbox-1"}
			stdout, _, code := r.Run(context.Background(), []string{"cat"}, tc.stdin)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			if gotI := readArgs(t, argsFile)[1] == "-i"; gotI != tc.wantI {
				t.Errorf("-i present = %v, want %v (args %q)", gotI, tc.wantI, readArgs(t, argsFile))
			}
			if stdout != tc.stdin {
				t.Errorf("stdout = %q, want the piped stdin %q", stdout, tc.stdin)
			}
		})
	}
}

// A guest command's own non-zero exit has to arrive intact rather than
// being flattened into the -1 that means "it never ran" -- including 1
// itself, which is exactly the status both of the pre-guest failures
// below also exit with.
func TestDockerExecRunnerReportsTheGuestCommandsOwnExitCode(t *testing.T) {
	for _, code := range []int{1, 2, 127} {
		t.Run(fmt.Sprintf("exit %d", code), func(t *testing.T) {
			writeFakeDockerExec(t, fmt.Sprintf("echo 'no such file' >&2; exit %d", code))

			r := &DockerExecRunner{Container: "granule-vm-sandbox-1"}
			_, stderr, got := r.Run(context.Background(), []string{"false"}, "")
			if got != code {
				t.Errorf("exit code = %d, want %d (stderr %q)", got, code, stderr)
			}
		})
	}
}

// The two failures that happen before the guest command ever runs both
// exit 1, so only their stderr tells them apart from a guest command that
// exited 1 on its own -- and both have to come back as -1, the report Run
// documents for a guest command that never ran at all.
func TestDockerExecRunnerReportsMinusOneWhenTheGuestCommandNeverRan(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stderr string
	}{
		{
			name:   "granule could not reach the guest",
			stderr: "granule: exec: dialing 169.254.100.2:22: connection refused",
		},
		{
			name:   "no such container",
			stderr: "Error: No such container: granule-vm-sandbox-1",
		},
		{
			name:   "docker daemon not answering",
			stderr: "Error response from daemon: dial unix /var/run/docker.sock: connect: no such file or directory",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeFakeDockerExec(t, fmt.Sprintf("printf '%%s\\n' %q >&2; exit 1", tc.stderr))

			r := &DockerExecRunner{Container: "granule-vm-sandbox-1"}
			_, stderr, code := r.Run(context.Background(), []string{"true"}, "")
			if code != -1 {
				t.Errorf("exit code = %d, want -1", code)
			}
			if !strings.Contains(stderr, tc.stderr) {
				t.Errorf("stderr = %q, want it to carry %q", stderr, tc.stderr)
			}
		})
	}
}

// DockerExecRunner has to satisfy the same interface NewSSHSandboxTools
// and ConfigureGitCredentialsOverSSH take, since serving those two call
// sites is the whole point of it.
func TestDockerExecRunnerIsARemoteRunner(t *testing.T) {
	var _ remoteRunner = &DockerExecRunner{}
}

// The live copy grain's own CI follows a running step with: both streams,
// in the order the guest produced them, written as they arrive -- while
// the pair Run returns stays as it was, because the exit classification
// reads stderr on its own.
func TestDockerExecRunnerStreamsBothStreamsToAFollower(t *testing.T) {
	writeFakeDockerExec(t, "echo out; echo err >&2; echo more; exit 0")

	r := &DockerExecRunner{Container: "granule-vm-sandbox-1"}
	var followed strings.Builder
	stdout, stderr, code := r.RunStream(context.Background(), []string{"true"}, "", &followed)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (%s / %s)", code, stdout, stderr)
	}
	if stdout != "out\nmore\n" || stderr != "err\n" {
		t.Errorf("the streams are still reported apart: stdout %q, stderr %q", stdout, stderr)
	}
	for _, want := range []string{"out", "err", "more"} {
		if !strings.Contains(followed.String(), want) {
			t.Errorf("the follower should see every stream: %q is missing from %q", want, followed.String())
		}
	}
}

// A runner nobody is following behaves exactly as it always did, which is
// what Run being RunStream with a nil writer has to mean.
func TestDockerExecRunnerWithNoFollowerIsUnchanged(t *testing.T) {
	writeFakeDockerExec(t, "echo out; echo err >&2; exit 3")

	r := &DockerExecRunner{Container: "granule-vm-sandbox-1"}
	stdout, stderr, code := r.Run(context.Background(), []string{"true"}, "")
	if stdout != "out\n" || stderr != "err\n" || code != 3 {
		t.Errorf("got %q / %q / %d, want %q / %q / 3", stdout, stderr, code, "out\n", "err\n")
	}
}
