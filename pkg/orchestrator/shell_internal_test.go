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

// The VM half of ShellRequest.Output, which the tests beside this file
// cannot reach: a granule sandbox runs its script over a transport rather
// than as a subprocess, so what has to be pinned here is that it uses the
// streaming entry point where its runner has one, falls back where it does
// not, and reports the same text either way.

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
)

// fakeShellRunner is a sandboxRunner that answers with a fixed pair, and
// optionally streams it a piece at a time.
type fakeShellRunner struct {
	stdout, stderr string
	exitCode       int
	// chunks is what a follower is given, in order, when streaming. Empty
	// means stdout then stderr, which is what a real one would deliver.
	chunks []string
	// streamed records whether the streaming entry point was the one used.
	streamed bool
	// argv records the command passed to Run or RunStream.
	argv []string
}

func (r *fakeShellRunner) Run(_ context.Context, argv []string, _ string) (string, string, int) {
	r.argv = argv
	return r.stdout, r.stderr, r.exitCode
}

// streamingShellRunner is fakeShellRunner with RunStream, since a type
// either has the method or does not and the test needs both.
type streamingShellRunner struct{ *fakeShellRunner }

func (r streamingShellRunner) RunStream(_ context.Context, argv []string, _ string, out io.Writer) (string, string, int) {
	r.argv = argv
	r.streamed = true
	chunks := r.chunks
	if len(chunks) == 0 {
		chunks = []string{r.stdout, r.stderr}
	}
	for _, chunk := range chunks {
		if chunk == "" {
			continue
		}
		io.WriteString(out, chunk)
	}
	return r.stdout, r.stderr, r.exitCode
}

func granuleShell(runner sandboxRunner) *granuleSandbox {
	return &granuleSandbox{
		owner:  &GranuleSandboxes{cfg: GranuleConfig{Workspace: "/home/debian/work"}},
		name:   "sandbox-1",
		vmName: "granule-vm-sandbox-1",
		runner: runner,
	}
}

func TestAGranuleSandboxsShellFollowsAScriptThroughAStreamingRunner(t *testing.T) {
	backing := &fakeShellRunner{
		stdout: "building\n", stderr: "warning\n",
		chunks: []string{"building\n", "warning\n", "done\n"},
	}
	sandbox := granuleShell(streamingShellRunner{backing})

	var followed strings.Builder
	out, err := sandbox.RunShell(context.Background(), ShellRequest{Script: "make", Output: &followed})
	if err != nil {
		t.Fatalf("running a script: %v", err)
	}
	if !backing.streamed {
		t.Errorf("a runner that can stream should have been asked to")
	}
	if followed.String() != "building\nwarning\ndone\n" {
		t.Errorf("the follower saw %q", followed.String())
	}
	// What was followed is what is reported: the interleaved log, not
	// stdout-then-stderr rebuilt from the pair -- see RunShell on why.
	if out.Output != followed.String() {
		t.Errorf("followed %q, returned %q", followed.String(), out.Output)
	}
}

func TestAGranuleSandboxsShellStillWorksWithARunnerThatCannotStream(t *testing.T) {
	backing := &fakeShellRunner{stdout: "out\n", stderr: "err\n", exitCode: 2}
	sandbox := granuleShell(backing)

	var followed strings.Builder
	out, err := sandbox.RunShell(context.Background(), ShellRequest{Script: "make", Output: &followed})
	if err != nil {
		t.Fatalf("running a script: %v", err)
	}
	if followed.Len() != 0 {
		t.Errorf("a transport with nothing to say as it goes says nothing: got %q", followed.String())
	}
	if out.Output != "out\nerr\n" || out.ExitCode != 2 {
		t.Errorf("got %q / %d, want %q / 2", out.Output, out.ExitCode, "out\nerr\n")
	}
}

func TestAGranuleSandboxsShellDoesNotStreamWhenNobodyIsFollowing(t *testing.T) {
	backing := &fakeShellRunner{stdout: "out\n"}
	sandbox := granuleShell(streamingShellRunner{backing})

	out, err := sandbox.RunShell(context.Background(), ShellRequest{Script: "make"})
	if err != nil {
		t.Fatalf("running a script: %v", err)
	}
	if backing.streamed {
		t.Errorf("with no Output there is nothing to stream to, so the plain call is the right one")
	}
	if out.Output != "out\n" {
		t.Errorf("output = %q", out.Output)
	}
}

// A host-directory sandbox's git must not read the *host's* system git
// config. Pinning HOME to the sandbox only redirects the global config;
// /etc/gitconfig -- or, on macOS, the gitconfig Apple's Command Line
// Tools ship at
// /Library/Developer/CommandLineTools/usr/share/git-core/gitconfig -- is
// still read, and what is in it is the host operator's, not this
// sandbox's. The case that bites is a credential helper: git appends
// helpers rather than replacing them, so a system `helper = osxkeychain`
// runs beside the `store` mcp.GitCredentialFiles writes and is handed the
// sandbox's gitproxy token to file away in the operator's own keychain.
//
// Asserted against hostShellEnv directly rather than only through a
// script, because this is a value grain sets rather than one it passes
// through from the daemon: a test that only watched a script's
// environment would still pass with the line deleted, on a machine where
// the daemon happens to export GIT_CONFIG_NOSYSTEM itself.
func TestAHostSandboxsShellEnvironmentIgnoresTheHostsSystemGitConfig(t *testing.T) {
	// Deliberately not set in this process's environment: what is pinned
	// is that grain supplies it, not that it survives the allowlist.
	var found string
	env := hostShellEnv("/sandboxes/shell-1")
	for _, entry := range env {
		if name, value, _ := strings.Cut(entry, "="); name == "GIT_CONFIG_NOSYSTEM" {
			found = value
		}
	}
	if found != "1" {
		t.Errorf("GIT_CONFIG_NOSYSTEM = %q, want \"1\" -- git in this sandbox would read the host's system config; the environment was %q",
			found, env)
	}
}

// A CI step on a VM finds a capability grant's sandbox commands by name,
// as run_command does and as a step on every other backend does: the
// script puts protocol.SandboxBinDir first on the guest's own PATH before
// anything of the step's, so a step's own PATH in its env still wins.
func TestAGranuleSandboxsShellPutsSandboxCommandsOnPath(t *testing.T) {
	backing := &fakeShellRunner{}
	if _, err := granuleShell(backing).RunShell(context.Background(), ShellRequest{Script: "deploy --dry-run", Env: map[string]string{"A": "b"}}); err != nil {
		t.Fatal(err)
	}
	if len(backing.argv) != 3 || backing.argv[0] != "bash" {
		t.Fatalf("argv = %q, want bash -c <script>", backing.argv)
	}
	want := "PATH=/home/debian/.grain/bin:$PATH; export PATH\nset -e\ncd '/home/debian/work'\nexport A='b'\ndeploy --dry-run"
	if got := backing.argv[2]; got != want {
		t.Errorf("script = %q, want %q", got, want)
	}
}

func TestAGranuleSandboxsShellRoundsFractionalSecondTimeoutsUp(t *testing.T) {
	backing := &fakeShellRunner{}
	sandbox := granuleShell(backing)

	_, err := sandbox.RunShell(context.Background(), ShellRequest{
		Script:  "true",
		Timeout: 10*time.Second + 200*time.Millisecond,
	})
	if err != nil {
		t.Fatalf("running a script: %v", err)
	}
	if len(backing.argv) < 3 || !strings.HasPrefix(backing.argv[2], "timeout --kill-after=10s 11 ") {
		t.Fatalf("argv = %q, want guest timeout rounded up to 11s so it never fires before the host deadline", backing.argv)
	}
}

// A repository a host sandbox's script creates -- a checkout, a scratch
// repository -- is one the daemon may read back with the release image's
// git, which cannot open reftable, so grain pins the files format there
// whatever the sandbox's own git config would choose (pkg/gitrefs).
func TestAHostSandboxsShellEnvironmentPinsTheFilesRefFormat(t *testing.T) {
	var found string
	for _, entry := range hostShellEnv("/sandboxes/shell-1") {
		if name, value, _ := strings.Cut(entry, "="); name == "GIT_DEFAULT_REF_FORMAT" {
			found = value
		}
	}
	if found != "files" {
		t.Errorf("GIT_DEFAULT_REF_FORMAT = %q, want \"files\"", found)
	}
}

func TestK8sAndContainerSandboxesImplementSandboxShellAndStreamOutput(t *testing.T) {
	cases := []struct {
		name  string
		build func(runner sandboxRunner) Sandbox
	}{
		{
			name: "k8sGranuleSandbox",
			build: func(runner sandboxRunner) Sandbox {
				return &k8sGranuleSandbox{
					owner:   &K8sGranuleSandboxes{cfg: K8sGranuleConfig{Workspace: "/home/debian"}},
					name:    "p12345678",
					podName: "grain-granule-p12345678",
					runner:  runner,
				}
			},
		},
		{
			name: "k8sSandbox",
			build: func(runner sandboxRunner) Sandbox {
				return &k8sSandbox{
					owner:   &K8sSandboxes{cfg: K8sConfig{Workspace: "/home/debian"}},
					name:    "p12345678",
					podName: "grain-sandbox-p12345678",
					runner:  runner,
				}
			},
		},
		{
			name: "containerSandbox",
			build: func(runner sandboxRunner) Sandbox {
				return &containerSandbox{
					owner:     &ContainerSandboxes{cfg: ContainerConfig{Workspace: "/home/debian"}},
					name:      "p12345678",
					container: "grain-sandbox-p12345678",
					runner:    runner,
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backing := &fakeShellRunner{
				stdout: "step 1\n",
				stderr: "warn\n",
				chunks: []string{"step 1\n", "warn\n", "step 2\n"},
			}
			sb := tc.build(streamingShellRunner{backing})
			shell, ok := sb.(SandboxShell)
			if !ok {
				t.Fatalf("%s does not implement SandboxShell", tc.name)
			}

			var followed strings.Builder
			res, err := shell.RunShell(context.Background(), ShellRequest{
				Script:  "make serve",
				Dir:     "repo",
				Env:     map[string]string{"PORT": "4280"},
				Timeout: 5*time.Second + 300*time.Millisecond,
				Output:  &followed,
			})
			if err != nil {
				t.Fatalf("RunShell: %v", err)
			}
			if !backing.streamed {
				t.Errorf("%s did not use streamingRunner", tc.name)
			}
			if res.Output != "step 1\nwarn\nstep 2\n" || followed.String() != res.Output {
				t.Errorf("output = %q (followed %q), want %q", res.Output, followed.String(), "step 1\nwarn\nstep 2\n")
			}
			if len(backing.argv) != 3 || backing.argv[0] != "bash" || backing.argv[1] != "-c" {
				t.Fatalf("argv = %q, want bash -c <script>", backing.argv)
			}
			wantInner := "PATH=/home/debian/.grain/bin:$PATH; export PATH\nset -e\ncd '/home/debian/repo'\nexport PORT='4280'\nmake serve"
			wantOuter := "timeout --kill-after=10s 6 bash -c " + shellQuote(wantInner) + "; exit $?"
			if backing.argv[2] != wantOuter {
				t.Errorf("script = %q, want %q", backing.argv[2], wantOuter)
			}
		})
	}
}

func TestRunGuestShellReturnsTransportErrorOnExitCodeMinusOne(t *testing.T) {
	const dialTimeoutStderr = "Unable to connect to the server: dial tcp 136.70.201.174:443: connect: connection timed out\n"
	for _, tc := range []struct {
		name    string
		stdout  string
		stderr  string
		chunks  []string
		wantErr string
		wantOut string
	}{
		{
			name:    "session_cut_off_notice",
			stdout:  "half a step\n",
			stderr:  mcp.SessionCutOffNotice + "\n",
			chunks:  []string{"half a step\n"},
			wantErr: mcp.SessionCutOffNotice,
			wantOut: "half a step\n" + mcp.SessionCutOffNotice + "\n",
		},
		{
			name:    "pre_container_kubectl_exec_failure",
			stderr:  dialTimeoutStderr,
			wantErr: "connection timed out",
			wantOut: dialTimeoutStderr,
		},
		{
			name:    "pre_container_kubectl_exec_failure_unstreamed_stderr",
			stderr:  dialTimeoutStderr,
			chunks:  []string{""},
			wantErr: "connection timed out",
			wantOut: dialTimeoutStderr,
		},
	} {
		for _, stream := range []bool{false, true} {
			mode := "Run"
			if stream {
				mode = "RunStream"
			}
			t.Run(tc.name+"_"+mode, func(t *testing.T) {
				backing := &fakeShellRunner{
					stdout:   tc.stdout,
					stderr:   tc.stderr,
					chunks:   tc.chunks,
					exitCode: -1,
				}
				sb := granuleShell(streamingShellRunner{backing})
				req := ShellRequest{Script: "go test ./..."}
				var followed strings.Builder
				if stream {
					req.Output = &followed
				}
				res, err := sb.RunShell(context.Background(), req)
				if err == nil {
					t.Fatalf("RunShell returned nil error on exitCode -1, want transport error containing %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("RunShell err = %q, want substring %q", err.Error(), tc.wantErr)
				}
				if res.ExitCode != -1 {
					t.Errorf("ExitCode = %d, want -1", res.ExitCode)
				}
				if res.Output != tc.wantOut {
					t.Errorf("Output = %q, want %q", res.Output, tc.wantOut)
				}
			})
		}
	}
}

func TestRemoteSandboxesImplementSandboxShellStreamLiveAndHonorTimeoutsBeyondTenMinutes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		workspace string
		what      string
		make      func(runner sandboxRunner) Sandbox
	}{
		{
			name:      "granule",
			workspace: "/home/debian/work",
			what:      `granule VM "granule-vm-sandbox-1"`,
			make: func(runner sandboxRunner) Sandbox {
				return granuleShell(runner)
			},
		},
		{
			name:      "k8s-granule",
			workspace: "/home/debian/work",
			what:      `granule sandbox pod "grain-granule-sandbox-1"`,
			make: func(runner sandboxRunner) Sandbox {
				return &k8sGranuleSandbox{
					owner:   NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule", Workspace: "/home/debian/work"}),
					name:    "sandbox-1",
					podName: "grain-granule-sandbox-1",
					runner:  runner,
				}
			},
		},
		{
			name:      "k8s",
			workspace: "/home/agent",
			what:      `sandbox pod "grain-sandbox-sandbox-1"`,
			make: func(runner sandboxRunner) Sandbox {
				return &k8sSandbox{
					owner:   NewK8sSandboxes(K8sConfig{Image: "example/sandbox", Workspace: "/home/agent"}),
					name:    "sandbox-1",
					podName: "grain-sandbox-sandbox-1",
					runner:  runner,
				}
			},
		},
		{
			name:      "container",
			workspace: "/home/agent",
			what:      `sandbox container "grain-sandbox-sandbox-1"`,
			make: func(runner sandboxRunner) Sandbox {
				return &containerSandbox{
					owner:     NewContainerSandboxes(ContainerConfig{Image: "example/sandbox", Workspace: "/home/agent"}),
					name:      "sandbox-1",
					container: "grain-sandbox-sandbox-1",
					runner:    runner,
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			backing := &fakeShellRunner{
				stdout: "step line 1\n",
				stderr: "step warning\n",
				chunks: []string{"step line 1\n", "step warning\n", "step done\n"},
			}
			sb := tc.make(streamingShellRunner{backing})
			shell, ok := sb.(SandboxShell)
			if !ok {
				t.Fatalf("%s sandbox does not implement SandboxShell", tc.name)
			}

			var followed strings.Builder
			res, err := shell.RunShell(context.Background(), ShellRequest{
				Script:  "npx playwright test",
				Dir:     "work/ui",
				Env:     map[string]string{"CI": "true"},
				Timeout: 25 * time.Minute,
				Output:  &followed,
			})
			if err != nil {
				t.Fatalf("RunShell: %v", err)
			}
			if !backing.streamed {
				t.Errorf("%s sandbox did not stream through RunStream", tc.name)
			}
			wantOut := "step line 1\nstep warning\nstep done\n"
			if followed.String() != wantOut || res.Output != wantOut || res.ExitCode != 0 {
				t.Errorf("RunShell = %+v, followed = %q; want %q and exit 0", res, followed.String(), wantOut)
			}
			if len(backing.argv) != 3 || backing.argv[0] != "bash" || backing.argv[1] != "-c" {
				t.Fatalf("argv = %q, want [bash -c <script>]", backing.argv)
			}
			script := backing.argv[2]
			// Must bound the command at the requested 25m (1500s), never
			// clamping to mcp.maxRunCommandTimeout (600s).
			if !strings.HasPrefix(script, "timeout --kill-after=10s 1500 bash -c ") {
				t.Errorf("script = %q, want 1500s timeout wrapper", script)
			}
			if strings.Contains(script, " 600 ") {
				t.Errorf("script = %q, unexpectedly clamped to 600s", script)
			}
			wantDir := tc.workspace + "/work/ui"
			if !strings.Contains(script, wantDir) || !strings.Contains(script, "npx playwright test") {
				t.Errorf("script = %q, want working dir %q and command", script, wantDir)
			}

			// Transport error on cut-off session names the specific sandbox.
			cutoff := &fakeShellRunner{
				stdout:   "partial\n" + mcp.SessionCutOffNotice,
				exitCode: -1,
			}
			_, err = tc.make(streamingShellRunner{cutoff}).(SandboxShell).RunShell(context.Background(), ShellRequest{
				Script: "make test",
				Output: &followed,
			})
			if err == nil || !strings.Contains(err.Error(), tc.what) {
				t.Errorf("cut-off RunShell error = %v, want transport error naming %s", err, tc.what)
			}
		})
	}
}
