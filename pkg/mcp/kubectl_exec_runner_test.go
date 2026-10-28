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

package mcp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/kubectlgate"
)

func TestKubectlExecArgsNameTheNamespacePodAndContainer(t *testing.T) {
	r := &KubectlExecRunner{Namespace: "grain", Pod: "grain-sandbox-7-1", Container: "sandbox"}
	got := strings.Join(r.execArgs([]string{"sh", "-c", "echo hi"}, false), " ")
	want := "-n grain exec -c sandbox grain-sandbox-7-1 -- sh -c " + statusTrailerScript + " grain-exec sh -c echo hi"
	if got != want {
		t.Errorf("execArgs = %q, want %q", got, want)
	}
}

func TestKubectlExecArgsPassKubeconfigWhenSet(t *testing.T) {
	r := &KubectlExecRunner{
		Kubeconfig: "/var/lib/grain/kubeconfig",
		Namespace:  "grain",
		Pod:        "grain-sandbox-7-1",
		Container:  "sandbox",
	}
	got := strings.Join(r.execArgs([]string{"sh", "-c", "echo hi"}, false), " ")
	want := "--kubeconfig /var/lib/grain/kubeconfig -n grain exec -c sandbox grain-sandbox-7-1 -- sh -c " + statusTrailerScript + " grain-exec sh -c echo hi"
	if got != want {
		t.Errorf("execArgs = %q, want %q", got, want)
	}
}

func TestKubectlExecArgsAddInteractiveOnlyForStdin(t *testing.T) {
	r := &KubectlExecRunner{Namespace: "grain", Pod: "p"}
	// Without stdin to pipe, -i would give the command a stdin nothing
	// ever writes to instead of the closed one it should see.
	if got := r.execArgs([]string{"true"}, false); contains(got, "-i") {
		t.Errorf("execArgs without stdin = %v, want no -i", got)
	}
	if got := r.execArgs([]string{"true"}, true); !contains(got, "-i") {
		t.Errorf("execArgs with stdin = %v, want -i", got)
	}
	// Never -t: a TTY merges stderr into stdout and translates newlines,
	// and every caller here wants the two streams apart.
	if got := r.execArgs([]string{"true"}, true); contains(got, "-t") {
		t.Errorf("execArgs = %v, want no -t", got)
	}
}

func TestKubectlExecArgsOmitTheContainerWhenUnnamed(t *testing.T) {
	r := &KubectlExecRunner{Namespace: "grain", Pod: "p"}
	// kubectl's own flags, before the "--": the shell after it has a -c
	// of its own.
	got := r.execArgs([]string{"true"}, false)
	for i, a := range got {
		if a == "--" {
			got = got[:i]
			break
		}
	}
	if contains(got, "-c") {
		t.Errorf("kubectl flags = %v, want no -c when no container is named", got)
	}
}

func TestKubectlExecPassesArgvThroughUnquoted(t *testing.T) {
	// argv is the command, not a string to be shell-quoted into one:
	// quoting here as well would double-quote every argument.
	r := &KubectlExecRunner{Namespace: "grain", Pod: "p"}
	args := r.execArgs([]string{"sh", "-c", "a b 'c'"}, false)
	if args[len(args)-1] != "a b 'c'" {
		t.Errorf("last arg = %q, want it verbatim", args[len(args)-1])
	}
}

func TestExecFailedBeforeContainerRecognizesKubectlsOwnFailures(t *testing.T) {
	// Both of these exit 1, exactly as a failing command in the pod
	// does, so the stream is the only thing that tells them apart.
	for _, stderr := range []string{
		"Error from server (NotFound): pods \"grain-sandbox-7-1\" not found\n",
		"error: unable to upgrade connection: container not found\n",
		"Error from server (Forbidden): pods \"p\" is forbidden\n",
		"Unable to connect to the server: dial tcp 136.70.201.174:443: connect: connection timed out\n",
		"E1025 07:40:01.123456     1 websocket.go:297] Unhandled Error\n" +
			"error: Post \"https://136.70.201.174/api/v1/namespaces/grain/pods/p/exec?command=sh&container=sandbox&stderr=true&stdout=true\": dial tcp 136.70.201.174:443: connect: connection timed out\n",
	} {
		if !execFailedBeforeContainer(stderr) {
			t.Errorf("execFailedBeforeContainer(%q) = false, want true", stderr)
		}
	}
	// A command's own stderr is not kubectl's.
	for _, stderr := range []string{"make: *** [all] Error 1\n", "", "go: cannot find module\n"} {
		if execFailedBeforeContainer(stderr) {
			t.Errorf("execFailedBeforeContainer(%q) = true, want false", stderr)
		}
	}
}

// TestKubectlExecRunnerReportsRealExitStatus runs against a stand-in
// kubectl, which is enough to prove the one contract remoteRunner's
// callers depend on: a command that ran gets its own status back, and
// one that never ran gets -1.
func TestKubectlExecRunnerReportsRealExitStatus(t *testing.T) {
	dir := t.TempDir()
	kubectl := filepath.Join(dir, "kubectl")
	script := "#!/bin/sh\n" +
		// The command is everything after the "--".
		"case \"$*\" in\n" +
		"  *'grain-exec fail'*) echo 'went wrong' >&2; exit 3 ;;\n" +
		"  *'grain-exec missing'*) echo 'Error from server (NotFound): pods \"p\" not found' >&2; exit 1 ;;\n" +
		"esac\n" +
		"echo ok\n" + fakeStatusTrailerOK
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the fake kubectl: %v", err)
	}
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}

	stdout, _, code := r.Run(context.Background(), []string{"true"}, "")
	if code != 0 || strings.TrimSpace(stdout) != "ok" {
		t.Errorf("Run of a working command = (%q, %d), want (ok, 0)", stdout, code)
	}
	if _, _, code := r.Run(context.Background(), []string{"fail"}, ""); code != 3 {
		t.Errorf("exit code = %d, want the pod command's own 3", code)
	}
	// A pod that is not there did not run the command at all, which is
	// -1 rather than a status the command chose.
	if _, _, code := r.Run(context.Background(), []string{"missing"}, ""); code != -1 {
		t.Errorf("exit code against a missing pod = %d, want -1", code)
	}
}

// TestKubectlExecArgsPutTheTrailerBeyondThrough is why Through is a
// field: the shell that reports the command's status has to run where
// the command does, inside the guest, so that a guest session torn down
// under `granule exec` cannot be answered for by a shell in the pod.
func TestKubectlExecArgsPutTheTrailerBeyondThrough(t *testing.T) {
	r := &KubectlExecRunner{Namespace: "grain", Pod: "p", Through: []string{"granule", "exec", "--"}}
	got := strings.Join(r.execArgs([]string{"uname", "-a"}, false), " ")
	want := "-n grain exec p -- granule exec -- sh -c " + statusTrailerScript + " grain-exec uname -a"
	if got != want {
		t.Errorf("execArgs = %q, want %q", got, want)
	}
}

// passThroughKubectl is a kubectl stand-in that runs the command it was
// handed -- everything after its "--" -- on this machine, so the real
// statusTrailerScript runs as it would in a pod.
func passThroughKubectl(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubectl")
	script := "#!/bin/sh\n" +
		"while [ \"$1\" != -- ]; do shift; done\n" +
		"shift\n" +
		"exec \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the fake kubectl: %v", err)
	}
	return path
}

func TestKubectlExecRunnerTakesTheStatusFromTheTrailer(t *testing.T) {
	r := &KubectlExecRunner{Kubectl: passThroughKubectl(t), Namespace: "grain", Pod: "p"}
	stdout, stderr, code := r.Run(context.Background(), []string{"sh", "-c", "echo out; printf 'no newline' >&2; exit 3"}, "")
	if code != 3 || stdout != "out\n" {
		t.Errorf("Run = (%q, %d), want (out, 3)", stdout, code)
	}
	// The trailer is taken back out, leaving the command's stderr
	// exactly as it wrote it.
	if stderr != "no newline" {
		t.Errorf("stderr = %q, want the command's own %q", stderr, "no newline")
	}
	stdout, stderr, code = r.Run(context.Background(), []string{"cat"}, "piped in")
	if code != 0 || stdout != "piped in" || stderr != "" {
		t.Errorf("Run of cat = (%q, %q, %d), want stdin through and exit 0", stdout, stderr, code)
	}
	var live bytes.Buffer
	_, stderr, code = r.RunStream(context.Background(), []string{"sh", "-c", "echo warn >&2"}, "", &live)
	if code != 0 || stderr != "warn\n" || live.String() != "warn\n" {
		t.Errorf("RunStream = (%q, %d), live %q; want the trailer in neither", stderr, code, live.String())
	}
}

// cutOffKubectl is a kubectl stand-in for a session torn down mid-command:
// it prints what the command had printed so far, then exits as kubectl
// does when the exec's stream closes with no status on it -- 0, unless
// exit says otherwise -- with no trailer, because the command never got
// as far as finishing.
func cutOffKubectl(t *testing.T, stderr string, exit int) (path string, count func() int) {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	script := "#!/bin/sh\n" +
		"echo x >> " + counter + "\n" +
		"echo 'go build ./cmd/grain'\n" +
		"printf '%s' '" + stderr + "' >&2\n" +
		"exit " + strconv.Itoa(exit) + "\n"
	path = filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the fake kubectl: %v", err)
	}
	return path, func() int {
		data, _ := os.ReadFile(counter)
		return strings.Count(string(data), "x")
	}
}

func TestKubectlExecRunnerReportsASessionCutOffMidCommandAsAFailure(t *testing.T) {
	kubectl, _ := cutOffKubectl(t, "compiling\n", 0)
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	var live bytes.Buffer
	stdout, stderr, code := r.RunStream(context.Background(), []string{"make", "build"}, "", &live)
	if code != -1 {
		t.Fatalf("exit code = %d, want -1: kubectl's 0 came with no status from the command", code)
	}
	if stdout != "go build ./cmd/grain\n" || !strings.HasPrefix(stderr, "compiling\n") {
		t.Errorf("Run = (%q, %q), want the partial output kept", stdout, stderr)
	}
	if !strings.Contains(stderr, SessionCutOffNotice) {
		t.Errorf("stderr = %q, want it to say the session was cut off", stderr)
	}
	if !strings.Contains(live.String(), "compiling") {
		t.Errorf("live = %q, want the partial output followed live", live.String())
	}
}

func TestKubectlExecRunnerReportsKubectlLosingTheStreamAsAFailure(t *testing.T) {
	// kubectl's own complaint after the command had started: its 1 is
	// not the command's.
	kubectl, _ := cutOffKubectl(t, "compiling\nerror: error reading from error stream: connection reset by peer\n", 1)
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	if _, stderr, code := r.Run(context.Background(), []string{"make", "build"}, ""); code != -1 || !strings.Contains(stderr, SessionCutOffNotice) {
		t.Errorf("Run = (%q, %d), want -1 and the cut-off notice", stderr, code)
	}
}

func TestKubectlExecRunnerDoesNotRetryASessionCutOffMidCommand(t *testing.T) {
	// Even when what the command had printed reads like the dial
	// timeout Run does retry: the command started, and may have done
	// half of what it does.
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	kubectl, count := cutOffKubectl(t, apiServerDialTimeout+"\n", 0)
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	if _, _, code := r.Run(context.Background(), []string{"curl", "http://10.0.0.1"}, ""); code != -1 {
		t.Errorf("exit code = %d, want -1", code)
	}
	if n := count(); n != 1 {
		t.Errorf("kubectl ran %d times, want 1", n)
	}
}

func TestKubectlExecRunnerReportsAGuestSessionCutOffUnderThrough(t *testing.T) {
	// `granule exec` ending with nothing to say for the guest's command
	// (here: printing part of it and exiting 0) is a cut-off session
	// too, which only holds because the trailer runs beyond Through.
	dir := t.TempDir()
	granule := filepath.Join(dir, "granule")
	if err := os.WriteFile(granule, []byte("#!/bin/sh\necho partial\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &KubectlExecRunner{Kubectl: passThroughKubectl(t), Namespace: "grain", Pod: "p", Through: []string{granule, "exec", "--"}}
	var live bytes.Buffer
	stdout, stderr, code := r.RunStream(context.Background(), []string{"make", "build"}, "", &live)
	if code != -1 || stdout != "partial\n" || !strings.Contains(stderr, SessionCutOffNotice) {
		t.Errorf("RunStream = (%q, %q, %d), want -1 and the cut-off notice", stdout, stderr, code)
	}
	if !strings.Contains(live.String(), SessionCutOffNotice) {
		t.Errorf("RunStream live output = %q, want the cut-off notice streamed to the live writer", live.String())
	}
}

func TestStatusTrailerFindsATrailerArrivingInPieces(t *testing.T) {
	var got bytes.Buffer
	tr := &statusTrailer{to: &got}
	for _, chunk := range []string{"a\x1e", "b\n", "warn\n\x1egrain-ex", "ec-stat", "us=4", "2\n"} {
		if _, err := tr.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	tr.flush()
	if !tr.seen || tr.status != 42 {
		t.Errorf("trailer = (%v, %d), want (true, 42)", tr.seen, tr.status)
	}
	// A stray record separator is ordinary output, passed on.
	if got.String() != "a\x1eb\nwarn\n" {
		t.Errorf("passed on %q, want %q", got.String(), "a\x1eb\nwarn\n")
	}
}

func TestStatusTrailerPassesOnAnUnfinishedTrailer(t *testing.T) {
	var got bytes.Buffer
	tr := &statusTrailer{to: &got}
	tr.Write([]byte("out\x1egrain-exec-sta"))
	tr.flush()
	if tr.seen || got.String() != "out\x1egrain-exec-sta" {
		t.Errorf("trailer seen %v, passed on %q; want no trailer and every byte back", tr.seen, got.String())
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestContainerExecRunnerUser(t *testing.T) {
	r := &ContainerExecRunner{Container: "c"}
	if got := strings.Join(r.execArgs([]string{"id"}, true), " "); got != "exec -i c id" {
		t.Errorf("execArgs = %q, want no -u when User is empty", got)
	}
	r.User = "0"
	if got := strings.Join(r.execArgs([]string{"id"}, true), " "); got != "exec -i -u 0 c id" {
		t.Errorf("execArgs = %q, want -u 0 before the container", got)
	}
}

func TestKubectlDialArgsIncludeThroughWithoutStatusTrailer(t *testing.T) {
	r := &KubectlExecRunner{
		Namespace: "grain",
		Pod:       "grain-granule-7-1",
		Container: "sandbox",
		Through:   []string{"granule", "exec", "--"},
	}
	got := strings.Join(r.dialArgs([]string{"sh", "-c", "exec grain-connect 127.0.0.1:4280"}), " ")
	want := "-n grain exec -i -c sandbox grain-granule-7-1 -- granule exec -- sh -c exec grain-connect 127.0.0.1:4280"
	if got != want {
		t.Errorf("dialArgs = %q, want %q", got, want)
	}
}

func TestContainerExecRunnerStreamsBothStreamsToAFollower(t *testing.T) {
	writeLocalDockerShim(t)
	r := &ContainerExecRunner{Container: "c"}
	var followed strings.Builder
	stdout, stderr, code := r.RunStream(context.Background(), []string{"sh", "-c", "echo out; echo err >&2"}, "", &followed)
	if code != 0 || stdout != "out\n" || stderr != "err\n" {
		t.Fatalf("RunStream = (%q, %q, %d), want (\"out\\n\", \"err\\n\", 0)", stdout, stderr, code)
	}
	if !strings.Contains(followed.String(), "out\n") || !strings.Contains(followed.String(), "err\n") {
		t.Errorf("followed = %q, want both stdout and stderr", followed.String())
	}
}
