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

package orchestrator

// The serving sandbox: what is run inside the guest to start a server, and
// what a deployment that cannot hold one is told.
//
// No VM here. What is testable without one is the invocation, the image
// override and the refusals -- and those are where this can be wrong in a
// way nobody would notice, since a wrong systemd-run line fails at
// dispatch on a host this suite never runs on. The boot itself needs
// /dev/kvm and is covered by the real-KVM suite.

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestServeArgsRunTheCommandAsATransientUnit(t *testing.T) {
	args := serveArgs(ServeSpec{
		Command: "python3 -m lifecycle.server",
		Dir:     "/opt/grain-lifecycle",
		Env:     map[string]string{"LIFECYCLE_LOG_LEVEL": "info", "GRAIN_LIFECYCLE_TOKEN": "sealed"},
	}, "grain-lifecycle")

	if !slices.Equal(args[:4], []string{"sh", "-c", serveAsExecUser, "sh"}) {
		t.Fatalf("args = %q", args)
	}
	for _, want := range []string{
		"--unit=grain-lifecycle",
		// So a unit that already failed does not block starting one under
		// the same name.
		"--collect",
		"--property=WorkingDirectory=/opt/grain-lifecycle",
		"--setenv=GRAIN_LIFECYCLE_TOKEN=sealed",
		"--setenv=LIFECYCLE_LOG_LEVEL=info",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("args %q is missing %q", args, want)
		}
	}
	// The command goes through a shell because that is what a declaration
	// writes: one string somebody typed, not an argv to split on spaces.
	tail := args[len(args)-3:]
	if tail[0] != "/bin/sh" || tail[1] != "-lc" || tail[2] != "python3 -m lifecycle.server" {
		t.Errorf("the command was not handed to a shell whole: %q", tail)
	}
	// Sorted, so the line a test asserts on and the line an operator reads
	// are the same every time.
	token := slices.Index(args, "--setenv=GRAIN_LIFECYCLE_TOKEN=sealed")
	level := slices.Index(args, "--setenv=LIFECYCLE_LOG_LEVEL=info")
	if token > level {
		t.Errorf("the environment is not in a stable order: %q", args)
	}
}

// A value with a space or a quote in it has to arrive intact, which is why
// the environment is passed as unit settings rather than spliced into the
// shell line.
func TestServeArgsDoNotSpliceTheEnvironmentIntoTheShellLine(t *testing.T) {
	args := serveArgs(ServeSpec{
		Command: "serve",
		Env:     map[string]string{"NOTE": `a value with "quotes" and a space`},
	}, "u")
	if !slices.Contains(args, `--setenv=NOTE=a value with "quotes" and a space`) {
		t.Fatalf("args = %q", args)
	}
	if strings.Contains(args[len(args)-1], "NOTE") {
		t.Errorf("the environment reached the shell line: %q", args[len(args)-1])
	}
}

func TestServeArgsLeaveOutWhatWasNotAskedFor(t *testing.T) {
	args := serveArgs(ServeSpec{Command: "serve"}, DefaultServeUnit)
	for _, unwanted := range []string{"--property=WorkingDirectory=", "--setenv="} {
		for _, arg := range args {
			if strings.HasPrefix(arg, unwanted) {
				t.Errorf("args %q carries an empty %s", args, unwanted)
			}
		}
	}
	if !slices.Contains(args, "--unit="+DefaultServeUnit) {
		t.Errorf("args %q does not name the default unit", args)
	}
}

// A serving sandbox's image is assembled per repository digest, so the
// deployment-wide resolver has no answer for it -- and must not be asked,
// since its answer is a different guest and resolving it can block on a
// build.
func TestAServingSandboxesImageWinsOverTheDeploymentsOwn(t *testing.T) {
	asked := false
	cfg := GranuleConfig{
		CreateArgs:   []string{granuleImageFlag, "ghcr.io/acme/stock:v1", "-cpus", "2"},
		SandboxImage: func(context.Context) (string, error) { asked = true; return "derived:local", nil },
	}

	args, err := cfg.createArgsWithImage(context.Background(), Shape{}, "grain-lifecycle-guest:abc")
	if err != nil {
		t.Fatal(err)
	}
	if asked {
		t.Error("it asked the deployment's resolver for an image it had already been given")
	}
	if i := slices.Index(args, granuleImageFlag); i < 0 || args[i+1] != "grain-lifecycle-guest:abc" {
		t.Errorf("args = %q", args)
	}
	// And with no override it is exactly what it was before: the
	// deployment's own, resolved per create.
	args, err = cfg.createArgsWithImage(context.Background(), Shape{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !asked {
		t.Error("with no override, the deployment's resolver was not asked")
	}
	if i := slices.Index(args, granuleImageFlag); i < 0 || args[i+1] != "derived:local" {
		t.Errorf("args = %q", args)
	}
}

// Both are refused before a VM is created, because either one produces a
// sandbox that cannot do the job and a boot is minutes.
func TestServeRefusesWhatItCannotServe(t *testing.T) {
	k := NewGranuleSandboxes(GranuleConfig{})
	if _, err := k.Serve(context.Background(), ServeSpec{Name: "s", Command: "serve"}); err == nil {
		t.Error("it served without being told which image to boot")
	} else if !strings.Contains(err.Error(), "what the caller pinned") {
		t.Errorf("%v does not say why the image is required", err)
	}
	if _, err := k.Serve(context.Background(), ServeSpec{Name: "s", Image: "i"}); err == nil {
		t.Error("it served nothing")
	}
}

// The network is not the caller's to choose: a declaration cannot ask for
// one and neither can a caller here, because it is the property the whole
// arrangement rests on.
func TestServeShapeWithholdsTheNetwork(t *testing.T) {
	shape := ServeShape(1, 1024)
	if shape.NetworkAccess != model.NetworkNone {
		t.Errorf("network = %q", shape.NetworkAccess)
	}
	if shape.CPUs != 1 || shape.MemoryMB != 1024 {
		t.Errorf("shape = %+v", shape)
	}
}

// A deployment whose backend cannot hold one is told which backend it has
// and why that backend cannot, rather than being told about an interface.
func TestCannotServeNamesTheReason(t *testing.T) {
	for name, backend := range map[string]Sandboxes{
		"container":  &ContainerSandboxes{},
		"kubernetes": &K8sSandboxes{},
		"host":       &HostSandboxes{},
	} {
		err := CannotServe(backend)
		if err == nil {
			t.Fatalf("%s: no reason", name)
		}
		if !strings.Contains(err.Error(), "serving sandbox") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := CannotServe(&ContainerSandboxes{}); !strings.Contains(got.Error(), "NetworkAccess none") {
		t.Errorf("the container backend's reason is its own: %v", got)
	}
	if got := CannotServe(&K8sSandboxes{}); !strings.Contains(got.Error(), "NetworkPolicy") {
		t.Errorf("the k8s backend's reason should say what it does do: %v", got)
	}
}

// The granule backend is the one that can, and says so by implementing it.
func TestOnlyTheGranuleBackendServes(t *testing.T) {
	if _, ok := any(NewGranuleSandboxes(GranuleConfig{})).(ServingSandboxes); !ok {
		t.Error("the granule backend does not implement ServingSandboxes")
	}
	for name, backend := range map[string]Sandboxes{
		"container":  &ContainerSandboxes{},
		"kubernetes": &K8sSandboxes{},
		"host":       &HostSandboxes{},
	} {
		if _, ok := any(backend).(ServingSandboxes); ok {
			t.Errorf("the %s backend claims it can serve; CannotServe says why it cannot", name)
		}
	}
}

// A transport that can only run a command to completion cannot reach
// anything served inside the guest, and is refused before the server is
// started rather than at the first dial.
func TestServeNeedsATransportThatCanOpenAConnection(t *testing.T) {
	var _ streamRunner = (*dialableRunner)(nil)
	if _, ok := any(&plainRunner{}).(streamRunner); ok {
		t.Fatal("the plain runner claims it can stream")
	}
}

type plainRunner struct{}

func (*plainRunner) Run(context.Context, []string, string) (string, string, int) { return "", "", 0 }

type dialableRunner struct{ plainRunner }

func (*dialableRunner) DialStream(context.Context, []string) (net.Conn, error) {
	return nil, errors.New("not here")
}

// firstLine is what a failed start reports, and stderr wins because
// systemd-run puts some refusals on stdout and a traceback on stderr.
func TestFirstLineTakesTheMostUsefulStream(t *testing.T) {
	if got := firstLine("boom\nand more\n", "stdout"); got != "boom" {
		t.Errorf("got %q", got)
	}
	if got := firstLine("  \n", "only stdout"); got != "only stdout" {
		t.Errorf("got %q", got)
	}
	if got := firstLine("", ""); got != "(it said nothing)" {
		t.Errorf("got %q", got)
	}
}

type exitRunner struct {
	code int
	argv []string
}

func (r *exitRunner) Run(_ context.Context, argv []string, _ string) (string, string, int) {
	r.argv = argv
	return "", "inactive", r.code
}

// What the guest's shell makes of serveArgs, run here against a fake
// systemd-run and sudo: systemd-run is reached through sudo when the exec
// is not root (an unprivileged one is refused with "Access denied"), runs
// the command as the exec's own account, and gets every other argument
// intact and in order.
func TestServeArgsStartTheUnitAsRootAndTheCommandAsTheExecUser(t *testing.T) {
	bin := t.TempDir()
	for name, script := range map[string]string{
		"systemd-run": "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n",
		"sudo":        "#!/bin/sh\n[ \"$1\" = -n ] || exit 9\nshift\necho SUDO\nexec \"$@\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	args := serveArgs(ServeSpec{Command: "serve --flag 'x y'", Env: map[string]string{"NOTE": "a b"}}, "u")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running %q: %v", args, err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")

	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	if os.Geteuid() != 0 {
		want = append(want, "SUDO")
	}
	want = append(want, "--uid="+me.Username, "--unit=u", "--collect", "--quiet", "--setenv=NOTE=a b",
		"/bin/sh", "-lc", "serve --flag 'x y'")
	if !slices.Equal(got, want) {
		t.Errorf("systemd-run was run as\n  %q\nwant\n  %q", got, want)
	}
}

// The journal of a system unit is not readable by grain's guest account,
// so Output reads it as root.
func TestServingSandboxOutputReadsTheJournalAsRoot(t *testing.T) {
	runner := &exitRunner{}
	served := &servingSandbox{sandbox: &granuleSandbox{name: "sb", runner: runner}, unit: "standing-x"}
	served.Output(context.Background())
	if len(runner.argv) < 5 || !slices.Equal(runner.argv[:5], []string{"sh", "-c", asGuestRoot, "sh", "journalctl"}) {
		t.Errorf("Output ran %q", runner.argv)
	}
}

// Alive is systemd's view of the served unit: active is alive, anything
// else -- a unit that exited, or an exec that never reached the guest --
// is not.
func TestServingSandboxAliveAsksSystemdAboutItsUnit(t *testing.T) {
	runner := &exitRunner{}
	served := &servingSandbox{sandbox: &granuleSandbox{name: "sb", runner: runner}, unit: "standing-x"}
	if err := served.Alive(context.Background()); err != nil {
		t.Fatalf("Alive on an active unit: %v", err)
	}
	if got := strings.Join(runner.argv, " "); got != "systemctl is-active standing-x" {
		t.Errorf("probe ran %q", got)
	}
	runner.code = 3
	if err := served.Alive(context.Background()); err == nil {
		t.Fatal("Alive on an inactive unit returned nil")
	}
}
