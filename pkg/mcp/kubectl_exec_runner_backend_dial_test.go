package mcp

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/kubectlgate"
)

// kubeletDialTimeout is the API server failing to reach the pod's kubelet
// through its exec tunnel, exactly as task 831's runs 831-33 to 831-35
// reported it: kubectl reached the API server, the command never started.
const kubeletDialTimeout = "error: unable to upgrade connection: error dialing backend: dial timeout, backstop"

func TestKubectlExecRunnerRetriesAKubeletDialTimeout(t *testing.T) {
	// The setup write that ended run 831-35 setup-failed.
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	kubectl, count := flakyExecKubectl(t, kubectlgate.MaxAttempts-1, kubeletDialTimeout)
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	stdout, stderr, code := r.Run(context.Background(), []string{"sh", "-c", "cat > .git-credentials"}, "secret")
	if code != 0 || strings.TrimSpace(stdout) != "ok" {
		t.Fatalf("Run = (%q, %q, %d), want ok after the dial timeouts", stdout, stderr, code)
	}
	if n := count(); n != kubectlgate.MaxAttempts {
		t.Errorf("kubectl ran %d times, want %d", n, kubectlgate.MaxAttempts)
	}
}

func TestKubectlExecRunnerStreamRetriesAKubeletDialTimeoutBehindAKlogLine(t *testing.T) {
	// An agent's run_command: retried without the person or the agent
	// seeing the failures a retry cleared.
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	klogged := "E1026 18:45:02.123456 1 v2.go:105] EOF\n" + kubeletDialTimeout
	kubectl, count := flakyExecKubectl(t, kubectlgate.MaxAttempts-1, klogged)
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	var out bytes.Buffer
	stdout, stderr, code := r.RunStream(context.Background(), []string{"go", "test", "./..."}, "", &out)
	if code != 0 || strings.TrimSpace(stdout) != "ok" || stderr != "" {
		t.Fatalf("RunStream = (%q, %q, %d), want (\"ok\\n\", \"\", 0)", stdout, stderr, code)
	}
	if n := count(); n != kubectlgate.MaxAttempts {
		t.Errorf("kubectl ran %d times, want %d", n, kubectlgate.MaxAttempts)
	}
	if got := out.String(); got != "ok\n" {
		t.Errorf("live out = %q, want %q without the dial errors", got, "ok\n")
	}
}

func TestKubectlExecRunnerReportsAKubeletDialTimeoutThatOutlastsItsRetries(t *testing.T) {
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	kubectl, count := flakyExecKubectl(t, 100, kubeletDialTimeout)
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	_, stderr, code := r.Run(context.Background(), []string{"true"}, "")
	if code != -1 || !strings.Contains(stderr, "error dialing backend") {
		t.Errorf("Run = (%q, %d), want -1 and the dial failure", stderr, code)
	}
	if n := count(); n != kubectlgate.MaxAttempts {
		t.Errorf("kubectl ran %d times, want %d", n, kubectlgate.MaxAttempts)
	}
}

func TestKubectlExecRunnerDoesNotRetryAKubeletDialFailureAfterTheCommandStarted(t *testing.T) {
	// The command had already printed something, so it ran: the same
	// words from kubectl afterwards are a session cut off part-way, and
	// running it again could do what it did a second time.
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	kubectl, count := cutOffKubectl(t, kubeletDialTimeout+"\n", 1)
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	var out bytes.Buffer
	_, stderr, code := r.RunStream(context.Background(), []string{"git", "push"}, "", &out)
	if code != -1 || !strings.Contains(stderr, SessionCutOffNotice) {
		t.Errorf("RunStream = (%q, %d), want -1 and the cut-off notice", stderr, code)
	}
	if n := count(); n != 1 {
		t.Errorf("kubectl ran %d times, want 1", n)
	}
}

func TestKubectlExecRunnerDoesNotRetryAnUpgradeTheServerRefused(t *testing.T) {
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	kubectl, count := flakyExecKubectl(t, 100, "error: unable to upgrade connection: container not found (sandbox)")
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	if _, _, code := r.Run(context.Background(), []string{"true"}, ""); code != -1 {
		t.Errorf("exit code = %d, want -1: the command never ran", code)
	}
	if n := count(); n != 1 {
		t.Errorf("kubectl ran %d times, want 1", n)
	}
}
