package k8s

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/kubectlgate"
)

func TestCreateAndApplyPodSkipClientSideValidation(t *testing.T) {
	// Validation is what downloads /openapi/v2 before every apply: one
	// more API round trip per pod, for a manifest this package rendered
	// itself, and the one a busy control plane timed out first.
	kubectl, calls := fakeKubectl(t, nil)
	c := Client{Kubectl: kubectl, Namespace: "grain"}
	if err := c.Create(context.Background(), "p",
		PodShape{CPUs: 1, MemoryMB: 1024, DiskGB: 10},
		PodOptions{Image: "example/sandbox"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := c.ApplyPod(context.Background(), "q", []byte(`{"kind":"Pod"}`)); err != nil {
		t.Fatalf("ApplyPod: %v", err)
	}
	got := calls()
	if len(got) != 2 {
		t.Fatalf("calls = %q, want two applies", got)
	}
	for _, call := range got {
		if !strings.HasSuffix(call, "-n grain apply --validate=false -f -") {
			t.Errorf("call = %q, want `apply --validate=false -f -`", call)
		}
	}
}

// flakyKubectl writes a kubectl stand-in that fails its first failures
// calls with stderr, exit 1, and succeeds after that, printing "ok". It
// returns the script and a count of the calls made.
func flakyKubectl(t *testing.T, failures int, stderr string) (path string, count func() int) {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	script := "#!/bin/sh\n" +
		"n=$(cat " + counter + " 2>/dev/null || echo 0)\n" +
		"n=$((n+1))\n" +
		"echo $n > " + counter + "\n" +
		"if [ $n -le " + strconv.Itoa(failures) + " ]; then echo '" + stderr + "' >&2; exit 1; fi\n" +
		"printf ok\n"
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

const dialTimeout = "Unable to connect to the server: dial tcp 136.70.201.174:443: connect: connection timed out"

func TestRunRetriesAnAPIServerDialTimeout(t *testing.T) {
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	kubectl, count := flakyKubectl(t, 2, dialTimeout)
	c := Client{Kubectl: kubectl}
	if err := c.ApplyPod(context.Background(), "p", []byte(`{}`)); err != nil {
		t.Fatalf("ApplyPod after two dial timeouts: %v", err)
	}
	if n := count(); n != 3 {
		t.Errorf("kubectl ran %d times, want 3 (two timeouts and the one that got through)", n)
	}
}

func TestRunGivesUpOnDialTimeoutsAfterMaxAttempts(t *testing.T) {
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	kubectl, count := flakyKubectl(t, 100, dialTimeout)
	c := Client{Kubectl: kubectl}
	err := c.Delete(context.Background(), "p", false)
	if err == nil || !strings.Contains(err.Error(), "connection timed out") {
		t.Fatalf("Delete = %v, want the dial timeout reported", err)
	}
	if n := count(); n != kubectlgate.MaxAttempts {
		t.Errorf("kubectl ran %d times, want %d", n, kubectlgate.MaxAttempts)
	}
}

func TestRunDoesNotRetryWhatTheServerAnswered(t *testing.T) {
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	kubectl, count := flakyKubectl(t, 100, `Error from server (Forbidden): pods is forbidden`)
	c := Client{Kubectl: kubectl}
	if err := c.ApplyPod(context.Background(), "p", []byte(`{}`)); err == nil {
		t.Fatal("ApplyPod succeeded against a forbidding server")
	}
	if n := count(); n != 1 {
		t.Errorf("kubectl ran %d times, want 1: a refusal is an answer", n)
	}
}

func TestClientCallsQueueAtTheGate(t *testing.T) {
	// A kubectl that records how many copies of itself are running at
	// once, by holding a lock directory per running copy.
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
	c := Client{Kubectl: kubectl, Gate: kubectlgate.New(2, time.Hour)}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// What Status makes of the empty output is beside the
			// point; only how many ran at once matters here.
			_, _ = c.Status(context.Background(), "p")
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
			t.Fatalf("saw %d kubectls running at once through a gate of 2", n)
		}
	}
}
