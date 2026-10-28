//go:build linux

package intree

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/hostscriptcli"
)

// hostScriptSandboxImage stands in for a sandbox image with no grain in
// it: Debian with curl, as grain's own sandbox images are, and dash as
// /bin/sh.
const hostScriptSandboxImage = "buildpack-deps:bookworm-curl"

// A grant works inside a container sandbox whose image has no grain of
// its own: its setup installs the stand-in grain on the PATH run_command
// sets, and `grain host-script <name>` called directly and from a
// repository script carries argv, streams and exit status exactly.
// Skipped where docker or the image is not at hand.
func TestHostScriptCommandWorksInAContainerSandbox(t *testing.T) {
	if exec.Command("docker", "image", "inspect", hostScriptSandboxImage).Run() != nil {
		t.Skipf("docker or %s is not available", hostScriptSandboxImage)
	}
	f := newHostScriptFixture(t, script("echoargs", `printf '%s\0' "$@"; echo to-stderr >&2; exit 3`, "usage", "echoargs ARGS..."))
	const attempt = "e1-42-1-0"
	resp := mint(t, f.ps, "host-script", attempt, map[string]any{"script": "echoargs"})
	descriptors, bin, repo := t.TempDir(), t.TempDir(), t.TempDir()
	for _, pl := range resp.Placements {
		if filepath.Dir(pl.Path) != hostscriptcli.Dir {
			t.Fatalf("placement %s is outside %s", pl.Path, hostscriptcli.Dir)
		}
		// World-readable here only because the container's uid is not
		// this test's.
		if err := os.WriteFile(filepath.Join(descriptors, filepath.Base(pl.Path)), []byte(pl.Content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(bin, 0o777); err != nil {
		t.Fatal(err)
	}
	// A repository's test script calls the command by name.
	if err := os.WriteFile(filepath.Join(repo, "check.sh"), []byte("#!/bin/sh\ngrain host-script echoargs \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	docker := func(shell string, args ...string) *exec.Cmd {
		// The grant's setup, then run_command's own prefix (pkg/mcp
		// sshRunCommandTool).
		argv := []string{"run", "--rm", "--network", "host", "--user", "1000:1000",
			"-v", descriptors + ":" + hostscriptcli.Dir + ":ro",
			"-v", bin + ":" + HostScriptBinDir,
			"-v", repo + ":/repo:ro",
			hostScriptSandboxImage, "bash", "-c", resp.Setup + " && PATH=" + HostScriptBinDir + ":$PATH; export PATH; " + shell, "bash"}
		return exec.Command("docker", append(argv, args...)...)
	}
	args := []string{"two words", "$(id)", "`id`", "; id", "a\nb", "--", ""}
	want := strings.Join(args, "\x00") + "\x00"
	for name, shell := range map[string]string{
		"run_command": `grain host-script echoargs "$@"`,
		"repo script": `/repo/check.sh "$@"`,
	} {
		t.Run(name, func(t *testing.T) {
			cmd := docker(shell, args...)
			var out, errb bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errb
			err := cmd.Run()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 3 {
				t.Fatalf("exit %v, stderr %q: want the script's own status 3", err, errb.String())
			}
			if out.String() != want || !strings.Contains(errb.String(), "to-stderr") {
				t.Errorf("stdout %q stderr %q, want %q and to-stderr", out.String(), errb.String(), want)
			}
		})
	}
	help, err := docker("grain host-script echoargs --grain-help").Output()
	if err != nil || !strings.Contains(string(help), "echoargs ARGS...") {
		t.Errorf("--grain-help: %q %v", help, err)
	}
}
