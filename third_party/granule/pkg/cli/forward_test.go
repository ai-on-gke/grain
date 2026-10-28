package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/guestforward"
)

func TestForwardHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"forward", "-h"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("forward -h exit = %d", code)
	}
	if !strings.Contains(stderr.String(), "-remote") || !strings.Contains(stderr.String(), "pod/<name>") {
		t.Fatalf("forward -h does not describe the flags and targets:\n%s", stderr.String())
	}
	var top bytes.Buffer
	Run(context.Background(), []string{"-h"}, strings.NewReader(""), &top, &top)
	if !strings.Contains(top.String(), "granule forward") {
		t.Fatal("top-level usage does not mention granule forward")
	}
}

func TestForwardArgumentErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"forward"}, "a target is required"},
		{[]string{"forward", "pod/x"}, "nothing to forward"},
		{[]string{"forward", "-R", "1=2"}, "a target is required"},
		{[]string{"forward", "pod/x", "-R", "9728"}, "want LISTEN=TARGET"},
		{[]string{"forward", "pod/x", "-L", "nope=7000"}, "address"},
		{[]string{"forward", "container/x", "-n", "ns", "-R", "1=2"}, "only apply to pod/ targets"},
		{[]string{"forward", "pod/", "-R", "1=2"}, "needs a pod name"},
		{[]string{"forward", "no-such-vm", "-state-dir", "/nonexistent", "-R", "1=2"}, "not found"},
	} {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), tc.args, strings.NewReader(""), &stdout, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), tc.want) {
			t.Errorf("%v: exit %d, stderr %q; want an error containing %q", tc.args, code, stderr.String(), tc.want)
		}
	}
}

// A pod target that kubectl cannot reach fails with kubectl's own
// reason rather than hanging or a bare EOF.
func TestForwardPodTransportFailure(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no kubectl at all
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"forward", "pod/x", "-R", "1=2", "-ready-timeout", "5s"}, strings.NewReader(""), &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "kubectl") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestWriteBindingsJSON(t *testing.T) {
	var out bytes.Buffer
	in := []guestforward.Binding{
		{Direction: "local", Listen: "127.0.0.1:41234", Target: "7000"},
		{Direction: "remote", Listen: "127.0.0.1:9728", Target: "9728"},
	}
	if err := writeBindings(&out, in, true); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("want exactly one line, got %q", out.String())
	}
	var got struct{ Bindings []guestforward.Binding }
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Bindings) != 2 || got.Bindings[0] != in[0] || got.Bindings[1] != in[1] {
		t.Fatalf("round trip = %+v", got.Bindings)
	}
}

func TestParseForwardCLIArgsAndExecCommand(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	got, err := parseForwardCLIArgs([]string{"-R", "9728:127.0.0.1:9728", "-L", "8080:127.0.0.1:80", "sbx-1"}, &stderr)
	if err != nil {
		t.Fatalf("parseForwardCLIArgs (docker): %v", err)
	}
	_, argv := got.execCommand()
	wantDocker := []string{"exec", "-i", "granule-vm-sbx-1", "granule", "forward", "-L", "127.0.0.1:8080:127.0.0.1:80", "-R", "127.0.0.1:9728:127.0.0.1:9728"}
	if !slices.Equal(argv, wantDocker) {
		t.Errorf("docker argv = %v, want %v", argv, wantDocker)
	}

	gotKube, err := parseForwardCLIArgs([]string{
		"-kube",
		"-kubeconfig", "/var/lib/grain/kubeconfig",
		"-n", "grain",
		"-c", "sandbox",
		"-R", "127.0.0.1:9728:127.0.0.1:9728",
		"grain-granule-7-1",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseForwardCLIArgs (kube): %v", err)
	}
	bin, kubeArgv := gotKube.execCommand()
	if bin != "kubectl" {
		t.Errorf("kube bin = %q, want kubectl", bin)
	}
	wantKube := []string{
		"--kubeconfig", "/var/lib/grain/kubeconfig",
		"-n", "grain",
		"exec", "-i",
		"-c", "sandbox",
		"grain-granule-7-1",
		"--",
		"granule", "forward",
		"-R", "127.0.0.1:9728:127.0.0.1:9728",
	}
	if !slices.Equal(kubeArgv, wantKube) {
		t.Errorf("kube argv = %v, want %v", kubeArgv, wantKube)
	}
}
