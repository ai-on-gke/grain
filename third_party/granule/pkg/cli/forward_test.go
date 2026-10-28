package cli

import (
	"bytes"
	"slices"
	"testing"
)

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
