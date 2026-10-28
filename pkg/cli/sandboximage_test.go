package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/sandboximage"
)

func TestSandboxImageCommandPrintsDefaultAndBase(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runSandboxImageCmd(nil, &stdout, &stderr); err != nil {
		t.Fatalf("runSandboxImageCmd(nil): %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != daemon.DefaultSandboxImage {
		t.Errorf("grain sandbox-image = %q, want %q", got, daemon.DefaultSandboxImage)
	}

	for _, args := range [][]string{{"-base"}, {"base"}} {
		stdout.Reset()
		stderr.Reset()
		if err := runSandboxImageCmd(args, &stdout, &stderr); err != nil {
			t.Fatalf("runSandboxImageCmd(%v): %v", args, err)
		}
		want := sandboximage.BaseImageFor(daemon.DefaultSandboxImage, "guest-base", sandboximage.DefaultGuestBaseImage)
		if got := strings.TrimSpace(stdout.String()); got != want {
			t.Errorf("grain sandbox-image %v = %q, want %q", args, got, want)
		}
	}
}

func TestSandboxImageContractSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runSandboxImageCmd([]string{"contract"}, &stdout, &stderr); err != nil {
		t.Fatalf("runSandboxImageCmd(contract): %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"Grain sandbox contract:",
		sandboximage.ContractLabel,
		sandboximage.ContractFile,
		"Granule guest contract:",
		sandboximage.GranuleContractLabel,
		"guest-base",
		"k8s-sandbox-base",
		"python3",
		"/usr/local/bin/grain-connect",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("grain sandbox-image contract output missing %q:\n%s", want, out)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if err := runSandboxImageCmd([]string{"contract", "-json"}, &stdout, &stderr); err != nil {
		t.Fatalf("runSandboxImageCmd(contract -json): %v", err)
	}
	var info sandboximage.ContractInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		t.Fatalf("unmarshalling contract JSON: %v\n%s", err, stdout.String())
	}
	if info.Contract != sandboximage.Contract || info.MinimumContract != sandboximage.MinimumContract {
		t.Errorf("contract JSON = %+v, want Contract=%d MinimumContract=%d", info, sandboximage.Contract, sandboximage.MinimumContract)
	}
}

func TestSandboxImageCheckSubcommand(t *testing.T) {
	dir := t.TempDir()
	fakeDocker := filepath.Join(dir, "docker")
	script := `#!/bin/sh
set -eu
case "$*" in
  *"image inspect"*good-k8s*)
    printf '{"%s":"%s"}\n' "` + sandboximage.ContractLabel + `" "` + sandboximage.ContractValue() + `"
    ;;
  *"image inspect"*good-guest*)
    printf '{"%s":"%s","%s":"1"}\n' "` + sandboximage.ContractLabel + `" "` + sandboximage.ContractValue() + `" "` + sandboximage.GranuleContractLabel + `"
    ;;
  *"image inspect"*unlabelled*)
    printf '{}\n'
    ;;
  *"run --rm"*good-k8s*)
    echo "sandbox-contract verify ok: grain=v1"
    ;;
  *)
    echo "unexpected docker args: $*" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := runSandboxImageCmd([]string{"check", "-docker", fakeDocker, "-probe", "example.test/good-k8s:latest"}, &stdout, &stderr); err != nil {
		t.Fatalf("check good-k8s failed: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "OK") || !strings.Contains(stdout.String(), "runtime probe: passed") {
		t.Errorf("unexpected check output:\n%s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if err := runSandboxImageCmd([]string{"check", "-docker", fakeDocker, "-json", "example.test/good-guest:latest"}, &stdout, &stderr); err != nil {
		t.Fatalf("check good-guest failed: %v\nstderr: %s", err, stderr.String())
	}
	var report sandboximage.ImageCheckReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("unmarshalling check report: %v\n%s", err, stdout.String())
	}
	if !report.OK || report.Backend != sandboximage.BackendGranule {
		t.Errorf("good-guest report = %+v, want OK and backend=granule", report)
	}

	stdout.Reset()
	stderr.Reset()
	if err := runSandboxImageCmd([]string{"check", "-docker", fakeDocker, "example.test/unlabelled:latest"}, &stdout, &stderr); err == nil {
		t.Fatalf("expected unlabelled image check to fail, got nil:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "FAILED") {
		t.Errorf("expected FAILED in output:\n%s", stdout.String())
	}
}
