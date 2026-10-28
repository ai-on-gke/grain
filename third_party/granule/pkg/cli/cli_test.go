package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/dockercli"
)

func TestRun_NoArgsPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), nil, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "granule vm create") {
		t.Errorf("usage not printed to stderr:\n%s", stderr.String())
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"bogus"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `unknown command "bogus"`) {
		t.Errorf("stderr = %q, want it to name the unknown command", stderr.String())
	}
}

func TestRun_Help(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"-h"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "granule vm create") {
		t.Errorf("usage not printed to stdout:\n%s", stdout.String())
	}
}

// A subcommand's own -h is a question that has been answered, not a
// failure: the flag package has already printed that command's flags, so
// finishing with "granule: flag: help requested" and a non-zero status
// on top of them makes a satisfied request read as a mistake.
//
// Every subcommand goes through the same one place in Run, so every one
// of them is checked here -- including the ones whose name argument the
// help request stands in for (see helpName).
func TestRun_SubcommandHelpExitsZeroAndPrintsTheFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		flag string // a flag only this command's own listing carries
	}{
		{[]string{"vm", "create", "-h"}, "-backend"},
		{[]string{"vm", "run", "-h"}, "-keep-on-failure"},
		{[]string{"vm", "exec", "-h"}, "-connect-timeout"},
		{[]string{"vm", "shell", "-h"}, "-connect-timeout"},
		{[]string{"vm", "wait", "-h"}, "-timeout"},
		{[]string{"vm", "status", "-h"}, "-timeout"},
		{[]string{"vm", "update", "-h"}, "-memory-mb"},
		{[]string{"vm", "delete", "-h"}, "-static-pod-path"},
		{[]string{"vm", "list", "-h"}, "-state-dir"},
		{[]string{"guest", "build", "-h"}, "-setup"},
		{[]string{"guest", "pack", "-h"}, "-packer-image"},
		{[]string{"guest", "rebase", "-h"}, "-boot"},
		{[]string{"guest", "verify", "-h"}, "-checks"},
		{[]string{"granulefile", "check", "-h"}, "-level"},
		{[]string{"granulefile", "build", "-h"}, "-guest-image"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), tc.args, strings.NewReader(""), &stdout, &stderr)
			if code != 0 {
				t.Errorf("exit code = %d, want 0 for a help request", code)
			}
			out := stdout.String() + stderr.String()
			if !strings.Contains(out, tc.flag) {
				t.Errorf("the flags were not printed (looked for %s):\n%s", tc.flag, out)
			}
			if strings.Contains(out, "granule: flag") {
				t.Errorf("a help request was reported as an error:\n%s", out)
			}
		})
	}
}

// A flag where the VM name belongs used to be taken as the name itself:
// "granule vm create -state-dir /tmp/vms" created a VM called
// "-state-dir" in the *default* state directory and said nothing about
// it. Refused now, naming the argument that is wrong.
func TestRun_AFlagIsNotAVMName(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"vm", "create", "-state-dir", dir}, strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code = 0, want a failure for a missing VM name")
	}
	if !strings.Contains(stderr.String(), `"-state-dir"`) {
		t.Errorf("stderr = %q, want it to name the argument that is not a VM name", stderr.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the state directory holds %v; nothing should have been created", entries)
	}
}

func TestRun_VMErrorSetsExitCode1(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"vm", "delete"}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit code = %d, want 1 (vm delete with no name should fail)", code)
	}
}

// TestRun_DockerFlagChoosesTheContainerCLI: "granule -docker <cli>"
// drives that CLI for the whole command, the same as $GRANULE_DOCKER --
// here a VM's create goes to fakedocker, which is on no PATH.
func TestRun_DockerFlagChoosesTheContainerCLI(t *testing.T) {
	for name, form := range map[string][]string{
		"separate": {"-docker", fakedockerPath},
		"equals":   {"--docker=" + fakedockerPath},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(dockercli.EnvBinary, "/no/such/container-cli")
			calls := callLog(t)
			stateDir := filepath.Join(t.TempDir(), "state")

			args := append(append([]string{}, form...), "vm", "create", "web", "--disk-mode", "overlay", "--state-dir", stateDir)
			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != 0 {
				t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
			}
			if got := os.Getenv(dockercli.EnvBinary); got != fakedockerPath {
				t.Errorf("GRANULE_DOCKER = %q, want -docker to have set it to %q", got, fakedockerPath)
			}
			findCall(t, calls(), "run")
		})
	}
}

func TestRun_DockerFlagNeedsAValue(t *testing.T) {
	for _, args := range [][]string{{"-docker"}, {"-docker="}} {
		var stdout, stderr bytes.Buffer
		if code := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != 2 {
			t.Errorf("%q: exit code = %d, want 2", args, code)
		}
	}
}
