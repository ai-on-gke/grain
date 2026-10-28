package mcp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/kubectlgate"
)

const (
	apiServerDialTimeout     = "Unable to connect to the server: dial tcp 136.70.201.174:443: connect: connection timed out"
	apiServerExecPostTimeout = "E1025 07:40:01.123456     1 websocket.go:297] Unhandled Error\n" +
		"error: Post \"https://136.70.201.174:443/api/v1/namespaces/grain/pods/p/exec?command=sh&container=sandbox&stderr=true&stdout=true\": dial tcp 136.70.201.174:443: connect: connection timed out"
)

// fakeStatusTrailerOK is the line statusTrailerScript ends a command
// that exited 0 with, for a kubectl stand-in that does not run the
// command it is handed but has to answer as one that did.
const fakeStatusTrailerOK = "printf '\\036grain-exec-status=0\\n' >&2\n"

// flakyExecKubectl is a kubectl stand-in whose first failures execs
// fail with stderr, exit 1, and whose later ones print "ok".
func flakyExecKubectl(t *testing.T, failures int, stderr string) (path string, count func() int) {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	script := "#!/bin/sh\n" +
		"n=$(cat " + counter + " 2>/dev/null || echo 0)\n" +
		"n=$((n+1))\n" +
		"echo $n > " + counter + "\n" +
		"if [ $n -le " + strconv.Itoa(failures) + " ]; then echo '" + stderr + "' >&2; exit 1; fi\n" +
		"echo ok\n" + fakeStatusTrailerOK
	path = filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, func() int {
		data, _ := os.ReadFile(counter)
		n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		return n
	}
}

func TestKubectlExecRunnerRetriesAnAPIServerDialTimeout(t *testing.T) {
	// The failure that used to error a CI job's whole sandbox setup: one
	// `chmod 600 .gitconfig` whose exec never reached the API server.
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	kubectl, count := flakyExecKubectl(t, 2, apiServerDialTimeout)
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	stdout, stderr, code := r.Run(context.Background(), []string{"chmod", "600", ".gitconfig"}, "")
	if code != 0 || strings.TrimSpace(stdout) != "ok" {
		t.Fatalf("Run = (%q, %q, %d), want ok after the timeouts", stdout, stderr, code)
	}
	if n := count(); n != 3 {
		t.Errorf("kubectl ran %d times, want 3", n)
	}
}

func TestKubectlExecRunnerRetriesAKubeletDialFailureOnUpgrade(t *testing.T) {
	// The API server could not reach the kubelet to start the exec
	// (konnectivity's dial timing out on GKE): the command never ran.
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	kubectl, count := flakyExecKubectl(t, 2, "error: unable to upgrade connection: error dialing backend: dial timeout, backstop")
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	var live bytes.Buffer
	stdout, stderr, code := r.RunStream(context.Background(), []string{"git", "config", "--global", "credential.helper", "store"}, "", &live)
	if code != 0 || strings.TrimSpace(stdout) != "ok" {
		t.Fatalf("RunStream = (%q, %q, %d), want ok after the upgrade failures", stdout, stderr, code)
	}
	if n := count(); n != 3 {
		t.Errorf("kubectl ran %d times, want 3", n)
	}
	if strings.Contains(live.String(), "dialing backend") {
		t.Errorf("live = %q, want the retried failures kept out of it", live.String())
	}
}

func TestKubectlExecRunnerReportsADialTimeoutThatOutlastsItsRetries(t *testing.T) {
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	kubectl, count := flakyExecKubectl(t, 100, apiServerDialTimeout)
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	_, stderr, code := r.Run(context.Background(), []string{"true"}, "")
	if code != -1 || !strings.Contains(stderr, "connection timed out") {
		t.Errorf("Run = (%q, %d), want -1 and the timeout", stderr, code)
	}
	if n := count(); n != kubectlgate.MaxAttempts {
		t.Errorf("kubectl ran %d times, want %d", n, kubectlgate.MaxAttempts)
	}
}

func TestKubectlExecRunnerDoesNotRetryAFailureInThePod(t *testing.T) {
	// The command ran and failed; running it again could do whatever
	// it did a second time.
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	kubectl, count := flakyExecKubectl(t, 100, "dial tcp 10.0.0.1:80: connect: connection timed out")
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	if _, _, code := r.Run(context.Background(), []string{"curl", "http://10.0.0.1"}, ""); code != 1 {
		t.Errorf("exit code = %d, want the pod command's own 1", code)
	}
	if n := count(); n != 1 {
		t.Errorf("kubectl ran %d times, want 1", n)
	}
}

func TestKubectlExecRunnerDoesNotRetryAnInPodCommandThatSaysItCouldNotConnect(t *testing.T) {
	// A `kubectl get` run inside the pod against a cluster it cannot
	// reach opens its stderr exactly as the outer kubectl would, but the
	// status trailer says it ran: running it again is not a retry of a
	// failed exec.
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	script := "#!/bin/sh\n" +
		"n=$(cat " + counter + " 2>/dev/null || echo 0)\n" +
		"echo $((n+1)) > " + counter + "\n" +
		"echo '" + apiServerDialTimeout + "' >&2\n" +
		"printf '\\036grain-exec-status=1\\n' >&2\n" +
		"exit 1\n"
	kubectl := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	var out bytes.Buffer
	if _, _, code := r.RunStream(context.Background(), []string{"kubectl", "get", "pods"}, "", &out); code != 1 {
		t.Errorf("exit code = %d, want the pod command's own 1", code)
	}
	data, _ := os.ReadFile(counter)
	if n := strings.TrimSpace(string(data)); n != "1" {
		t.Errorf("kubectl ran %s times, want 1", n)
	}
	if !strings.Contains(out.String(), "Unable to connect") {
		t.Errorf("live out = %q, want the command's own stderr", out.String())
	}
}

func TestKubectlExecRunnerStreamRetriesTransientAPIServerDialTimeouts(t *testing.T) {
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	for _, tc := range []struct {
		name   string
		stderr string
	}{
		{name: "unable_to_connect", stderr: apiServerDialTimeout},
		{name: "post_exec_dial_timeout", stderr: apiServerExecPostTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kubectl, count := flakyExecKubectl(t, kubectlgate.MaxAttempts-1, tc.stderr)
			r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
			var out bytes.Buffer
			stdout, stderr, code := r.RunStream(context.Background(), []string{"true"}, "", &out)
			if code != 0 || strings.TrimSpace(stdout) != "ok" || stderr != "" {
				t.Fatalf("RunStream = (%q, %q, %d), want (\"ok\\n\", \"\", 0)", stdout, stderr, code)
			}
			if n := count(); n != kubectlgate.MaxAttempts {
				t.Errorf("kubectl ran %d times, want %d", n, kubectlgate.MaxAttempts)
			}
			if got := out.String(); got != "ok\n" {
				t.Errorf("live out = %q, want %q without transient dial errors", got, "ok\n")
			}
		})
	}
}

func TestKubectlExecRunnerStreamReportsADialTimeoutThatOutlastsItsRetries(t *testing.T) {
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	kubectl, count := flakyExecKubectl(t, 100, apiServerDialTimeout)
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	var out bytes.Buffer
	_, stderr, code := r.RunStream(context.Background(), []string{"true"}, "", &out)
	if code != -1 || !strings.Contains(stderr, "connection timed out") {
		t.Errorf("RunStream = (%q, %d), want -1 and the timeout", stderr, code)
	}
	if n := count(); n != kubectlgate.MaxAttempts {
		t.Errorf("kubectl ran %d times, want %d", n, kubectlgate.MaxAttempts)
	}
	if !strings.Contains(out.String(), "connection timed out") {
		t.Errorf("live out = %q, want the final dial error flushed to out", out.String())
	}
}

func TestKubectlExecRunnerStreamDoesNotRetryASessionCutOffMidCommand(t *testing.T) {
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	kubectl, count := cutOffKubectl(t, apiServerDialTimeout+"\n", 0)
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	var out bytes.Buffer
	_, stderr, code := r.RunStream(context.Background(), []string{"curl", "http://10.0.0.1"}, "", &out)
	if code != -1 || !strings.Contains(stderr, SessionCutOffNotice) {
		t.Errorf("RunStream = (%q, %d), want -1 and the cut-off notice", stderr, code)
	}
	if n := count(); n != 1 {
		t.Errorf("kubectl ran %d times, want 1", n)
	}
}

func TestKubectlExecRunnerQueuesAtTheGate(t *testing.T) {
	dir := t.TempDir()
	running := filepath.Join(dir, "running")
	if err := os.Mkdir(running, 0o755); err != nil {
		t.Fatal(err)
	}
	peakLog := filepath.Join(dir, "peak")
	script := "#!/bin/sh\n" +
		"me=" + running + "/$$\n" +
		"mkdir $me\n" +
		"ls " + running + " | wc -l >> " + peakLog + "\n" +
		"sleep 0.1\n" +
		"rmdir $me\n"
	kubectl := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	gate := kubectlgate.New(2, time.Hour)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p", Gate: gate}
			r.Run(context.Background(), []string{"true"}, "")
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(peakLog)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(data))
	if len(lines) != 8 {
		t.Fatalf("kubectl ran %d times, want 8", len(lines))
	}
	for _, l := range lines {
		if n, _ := strconv.Atoi(l); n > 2 {
			t.Fatalf("saw %d kubectl execs running at once through a gate of 2", n)
		}
	}
}
