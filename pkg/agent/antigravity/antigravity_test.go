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

package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// recordingRunner captures everything Framework.Run hands the subprocess,
// and replays a canned stream back. It is the seam for asserting on how
// agy is invoked -- what testing.go's scriptRunner deliberately does not
// do, since that one is busy actually running tools.
type recordingRunner struct {
	args   []string
	stdin  string
	env    []string
	dir    string
	stdout string
	// err is what the subprocess itself returned, for the failures that
	// only exist as an exit status: an agy that exited non-zero after
	// writing its own account of the run to stdout, or one that died
	// before writing anything at all.
	err error
	// homeAtRun is a copy of the MCP config found in the HOME this run
	// was given, and settingsAtRun a copy of agy's own settings file
	// beside it -- both read while the run is notionally in flight, since
	// Run deletes that directory as it returns and a test cannot look
	// afterwards. settingsAtRun is "" when no settings file was written
	// at all, which is a distinct case from an empty one.
	homeAtRun     string
	settingsAtRun string
	sawSettings   bool
	// hooksAtRun is agy's hooks.json from the same HOME, read on the same
	// terms, and sawHooks whether one was written at all.
	hooksAtRun string
	sawHooks   bool
	// dirEntriesAtRun is what the working directory agy was started in
	// held while the run was in flight, and dirExistedAtRun whether it
	// was there at all -- read for the same reason as the two above, a
	// scratch directory inside the private HOME going away with it.
	dirEntriesAtRun int
	dirExistedAtRun bool
	// calls counts the execs Run made -- one for an ordinary run, more
	// for one the retry loop in resume.go had to repeat.
	calls int
	// onRun, when set, is called with each exec while the run's staged
	// files are still there.
	onRun func(args, env []string, dir string)
}

func (r *recordingRunner) Run(_ context.Context, args []string, stdin string, env []string, dir string, tee io.Writer) (string, error) {
	r.calls++
	r.args, r.stdin, r.env, r.dir = args, stdin, env, dir
	if r.onRun != nil {
		r.onRun(args, env, dir)
	}
	if entries, err := os.ReadDir(dir); err == nil {
		r.dirEntriesAtRun, r.dirExistedAtRun = len(entries), true
	}
	for _, e := range env {
		if home, ok := strings.CutPrefix(e, "HOME="); ok {
			if data, err := os.ReadFile(filepath.Join(home, mcpConfigRelPath)); err == nil {
				r.homeAtRun = string(data)
			}
			if data, err := os.ReadFile(filepath.Join(home, cliSettingsRelPath)); err == nil {
				r.settingsAtRun, r.sawSettings = string(data), true
			}
			if data, err := os.ReadFile(filepath.Join(home, hooksConfigRelPath)); err == nil {
				r.hooksAtRun, r.sawHooks = string(data), true
			}
		}
	}
	if tee != nil {
		io.WriteString(tee, r.stdout)
	}
	return r.stdout, r.err
}

func okStream() string {
	return stream(
		initLine,
		toolActive(0, "write_file", `{"path":"ok.txt"}`),
		toolDone(0, "write_file", "wrote ok.txt"),
		`{"event":"result","result":{"status":"SUCCESS","response":"done"}}`,
	)
}

// TestRunSendsThePromptOverStdinAndNeverInArgv is the discipline v1's
// dispatch.py set and agent/claude kept: untrusted issue content must
// never become a ps-visible argument. agy's other print mode takes the
// prompt as the argument to --print, which is exactly why this package
// uses --input-format stream-json instead.
func TestRunSendsThePromptOverStdinAndNeverInArgv(t *testing.T) {
	const secret = "untrusted-issue-body-that-must-not-reach-argv"
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: secret, SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, a := range r.args {
		if strings.Contains(a, secret) {
			t.Fatalf("prompt reached argv (%q); it must travel over stdin only", a)
		}
	}
	var ev struct {
		Event   string `json:"event"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.stdin)), &ev); err != nil {
		t.Fatalf("stdin was not one JSON user event: %v (%q)", err, r.stdin)
	}
	// Contains rather than equals: Run prepends toolPreamble, which is
	// what keeps a run using grain's tools rather than agy's own. The
	// prompt itself still has to arrive whole and unaltered.
	if ev.Event != "user" || ev.Message.Role != "user" || !strings.Contains(ev.Message.Content, secret) {
		t.Errorf("stdin user event = %+v, want the prompt as a user turn", ev)
	}
	if !strings.Contains(ev.Message.Content, mcp.AgyQualifiedToolName("run_command")) {
		t.Errorf("stdin user event = %+v, want the preamble naming grain's own tools", ev)
	}
	if !argsHave(r.args, "--input-format", "stream-json") {
		t.Errorf("args = %v, want --input-format stream-json (the mode that reads the prompt from stdin)", r.args)
	}
	if argsHave(r.args, "--print", secret) {
		t.Errorf("args = %v, want no --print <prompt>", r.args)
	}
}

// TestRunWritesThisRunsOwnMCPSettingsIntoAPrivateHome is the property
// that stands in for claude's --mcp-config: agy registers MCP servers
// per-user, so two concurrent runs against two sandboxes could otherwise
// only ever see whichever registration was written last.
func TestRunWritesThisRunsOwnMCPSettingsIntoAPrivateHome(t *testing.T) {
	root := t.TempDir()
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: root}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var home string
	for _, e := range r.env {
		if h, ok := strings.CutPrefix(e, "HOME="); ok {
			home = h
		}
	}
	if home == "" {
		t.Fatal("no HOME in the subprocess environment; agy would read the controller's own ~/.gemini")
	}
	if home == os.Getenv("HOME") {
		t.Fatal("HOME was the controller's own; a run must get a private one")
	}
	if r.homeAtRun == "" {
		t.Fatalf("no %s written into the private HOME", mcpConfigRelPath)
	}

	var settings struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(r.homeAtRun), &settings); err != nil {
		t.Fatalf("settings file was not JSON: %v (%q)", err, r.homeAtRun)
	}
	if len(settings.MCPServers) != 1 {
		t.Fatalf("mcpServers = %+v, want exactly one -- the only server this run may see", settings.MCPServers)
	}
	srv := settings.MCPServers[mcpServerName]
	if srv.Command != "/usr/local/bin/grain" {
		t.Errorf("mcp command = %q, want this grain binary", srv.Command)
	}
	if !argsHave(srv.Args, "-sandbox-root", root) || srv.Args[0] != "mcpserver" {
		t.Errorf("mcp args = %v, want the mcpserver subcommand rooted at this run's sandbox", srv.Args)
	}

	// The directory is the run's own, and goes away with it: nothing may
	// outlive a run holding a pointer at that run's sandbox.
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("private HOME %s still exists after Run returned (stat err = %v)", home, err)
	}
}

// wait_for_checks blocks for as long as CI takes, so agy's own cap on a
// single MCP tool call has to be moved out past the longest wait that
// tool will ever do -- otherwise a run that asked to wait out a slow
// build has the call killed under it and is told the tool failed. agy
// takes that cap as a per-server key rather than an environment variable,
// which is the only way this differs from agent/claude's MCP_TOOL_TIMEOUT.
func TestRunRaisesAgysMCPToolTimeoutPastTheLongestWait(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var settings struct {
		MCPServers map[string]struct {
			TimeoutSeconds *int `json:"timeoutSeconds"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(r.homeAtRun), &settings); err != nil {
		t.Fatalf("mcp config was not JSON: %v (%q)", err, r.homeAtRun)
	}
	got := settings.MCPServers[mcpServerName].TimeoutSeconds
	if got == nil {
		t.Fatalf("mcp config = %q, want a timeoutSeconds on this run's server", r.homeAtRun)
	}
	if limit := time.Duration(*got) * time.Second; limit <= mcp.MaxWaitForChecksTimeout {
		t.Errorf("timeoutSeconds = %v, want more than wait_for_checks' own %v maximum",
			limit, mcp.MaxWaitForChecksTimeout)
	}
	// Positive, not the negative agy reads as "no cap at all": a wedged
	// MCP server should still end the call eventually.
	if *got <= 0 {
		t.Errorf("timeoutSeconds = %d, want a finite cap rather than agy's no-timeout value", *got)
	}
}

// TestRunPointsAGranuleRunAtItsVMInsteadOfALocalRoot is the other half of
// mcpServerArgs, and the case where there is no local directory to name.
func TestRunPointsAGranuleRunAtItsVMInsteadOfALocalRoot(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain", WithGranuleSSH("agent", "/keys/exec", "/workspace"))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", GranuleVM: "grain-run-7"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var settings struct {
		MCPServers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(r.homeAtRun), &settings); err != nil {
		t.Fatalf("settings file was not JSON: %v", err)
	}
	args := settings.MCPServers[mcpServerName].Args
	for _, want := range [][2]string{
		{"-granule-vm", "grain-run-7"}, {"-ssh-user", "agent"},
		{"-exec-key", "/keys/exec"}, {"-workspace", "/workspace"},
	} {
		if !argsHave(args, want[0], want[1]) {
			t.Errorf("mcp args = %v, want %s %s", args, want[0], want[1])
		}
	}
	if argsHave(r.args, "--add-dir", "") {
		t.Errorf("args = %v, want no --add-dir for a granule run: there is no local path to name", r.args)
	}
}

// The forked mcpserver has to be told which repo and branch it may read
// CI for, or pull_request_status has nothing to answer with -- the same
// contract agent/claude's own test of this asserts, since the two
// frameworks build these arguments separately.
func TestRunPassesTheRunsOwnRepoAndBranchToTheMCPServer(t *testing.T) {
	mcpArgs := func(t *testing.T, f *Framework, cfg agent.RunConfig, r *recordingRunner) []string {
		t.Helper()
		if _, err := f.Run(context.Background(), cfg); err != nil {
			t.Fatalf("Run: %v", err)
		}
		var settings struct {
			MCPServers map[string]struct {
				Args []string `json:"args"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(r.homeAtRun), &settings); err != nil {
			t.Fatalf("settings file was not JSON: %v", err)
		}
		return settings.MCPServers[mcpServerName].Args
	}

	t.Run("configured", func(t *testing.T) {
		r := &recordingRunner{stdout: okStream()}
		f := newFramework(r, "/usr/local/bin/grain", WithGitHubAccess("/data", "github.example", true))
		args := mcpArgs(t, f, agent.RunConfig{
			Prompt: "go", SandboxRoot: t.TempDir(),
			Repo: "acme/widgets", Branch: "grain/task-9",
			GitCredential: "grain-team-acme",
		}, r)
		for _, want := range [][2]string{
			{"-data-dir", "/data"}, {"-pr-repo", "acme/widgets"},
			{"-pr-branch", "grain/task-9"}, {"-github-host", "github.example"},
			// The credential the branch is pushed with, by name, so the
			// subprocess reads GitHub as the same account rather than
			// off a ladder that may name nothing (agent.RunConfig.GitCredential).
			{"-pr-credential", "grain-team-acme"},
		} {
			if !argsHave(args, want[0], want[1]) {
				t.Errorf("mcp args = %v, want %s %s", args, want[0], want[1])
			}
		}
		// The flag a local mock deployment lives or dies by: dropped, the
		// forked mcpserver speaks HTTPS to a githubsim serving plain
		// HTTP and every pull_request_status answer becomes a TLS error.
		if !slices.Contains(args, "-github-insecure-http") {
			t.Errorf("mcp args = %v, want -github-insecure-http passed through", args)
		}
		// Not a repair, so the flag that turns base_branch_status's
		// conclusion around is absent (agent.RunConfig.Repair).
		if slices.Contains(args, "-pr-repair") {
			t.Errorf("mcp args = %v, want no -pr-repair for an ordinary run", args)
		}
	})

	t.Run("a repair run", func(t *testing.T) {
		r := &recordingRunner{stdout: okStream()}
		f := newFramework(r, "/usr/local/bin/grain", WithGitHubAccess("/data", "github.com", false))
		args := mcpArgs(t, f, agent.RunConfig{
			Prompt: "go", SandboxRoot: t.TempDir(),
			Repo: "acme/widgets", Branch: "grain/task-9", Repair: true,
		}, r)
		if !slices.Contains(args, "-pr-repair") {
			t.Errorf("mcp args = %v, want -pr-repair for a merge queue repair", args)
		}
	})

	// A task with no repo attached is a real case, and half the flags
	// would make mcpserver warn about a misconfiguration that is not one.
	t.Run("no repo on the run", func(t *testing.T) {
		r := &recordingRunner{stdout: okStream()}
		f := newFramework(r, "/usr/local/bin/grain", WithGitHubAccess("/data", "github.com", false))
		args := mcpArgs(t, f, agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir(), GitCredential: "grain-team-acme"}, r)
		// By name alone, not name-and-value: a flag is passed with a
		// real value after it, so asking whether "-pr-repo" is followed
		// by the empty string is a question nothing could ever answer
		// yes to, wired or not.
		for _, unwanted := range []string{"-data-dir", "-pr-repo", "-pr-branch", "-pr-credential", "-github-host"} {
			if slices.Contains(args, unwanted) {
				t.Errorf("mcp args = %v, want no %s for a run with no repo", args, unwanted)
			}
		}
	})

	// And a deployment that never called WithGitHubAccess has no
	// credential to read GitHub with, so naming the repo would only
	// produce a warning per run.
	t.Run("no github access on the framework", func(t *testing.T) {
		r := &recordingRunner{stdout: okStream()}
		f := newFramework(r, "/usr/local/bin/grain")
		args := mcpArgs(t, f, agent.RunConfig{
			Prompt: "go", SandboxRoot: t.TempDir(),
			Repo: "acme/widgets", Branch: "grain/task-9",
		}, r)
		if slices.Contains(args, "-pr-repo") {
			t.Errorf("mcp args = %v, want no -pr-repo without WithGitHubAccess", args)
		}
	})
}

// The run's own wall-clock deadline reaches the forked mcpserver too,
// which is what lets its tool results tell the run how long it has left
// (mcp.Registry.AnnounceDeadline). Asserted here as well as in
// agent/claude's own test, since each framework builds these arguments
// separately.
func TestRunPassesTheRunsDeadlineToTheMCPServer(t *testing.T) {
	mcpArgs := func(t *testing.T, ctx context.Context) []string {
		t.Helper()
		r := &recordingRunner{stdout: okStream()}
		f := newFramework(r, "/usr/local/bin/grain")
		if _, err := f.Run(ctx, agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		var settings struct {
			MCPServers map[string]struct {
				Args []string `json:"args"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(r.homeAtRun), &settings); err != nil {
			t.Fatalf("settings file was not JSON: %v", err)
		}
		return settings.MCPServers[mcpServerName].Args
	}

	ctx, cancel := context.WithDeadline(context.Background(),
		time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	defer cancel()
	if args := mcpArgs(t, ctx); !argsHave(args, "-run-deadline", "2026-03-04T05:06:07Z") {
		t.Errorf("mcp args = %v, want -run-deadline carrying the ctx's own deadline", args)
	}

	// No deadline on the ctx, no flag: a run bounded some other way is
	// not told a moment nobody set.
	if args := mcpArgs(t, context.Background()); slices.Contains(args, "-run-deadline") {
		t.Errorf("mcp args = %v, want no -run-deadline for a ctx that has none", args)
	}
}

// TestRunRejectsAGranuleVMWithoutSSHConfig keeps the failure at
// construction time rather than letting a forked mcpserver fail to reach
// anything.
func TestRunRejectsAGranuleVMWithoutSSHConfig(t *testing.T) {
	f := newFramework(&recordingRunner{stdout: okStream()}, "/usr/local/bin/grain")
	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", GranuleVM: "grain-run-7"})
	if err == nil {
		t.Fatal("Run err = nil for a granule VM with no WithGranuleSSH, want an error")
	}
	if result != nil {
		t.Errorf("Run result = %+v, want nil: this run never started", result)
	}
}

// TestRunRequiresASandbox is the same guard for a RunConfig naming
// neither a root nor a VM.
func TestRunRequiresASandbox(t *testing.T) {
	f := newFramework(&recordingRunner{stdout: okStream()}, "/usr/local/bin/grain")
	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go"}); err == nil {
		t.Fatal("Run err = nil with no sandbox at all, want an error")
	}
}

// TestRunPassesTheAPIKeyByEnvironmentNotArgv is WithAPIKey's own reason
// for existing: a credential in argv is a credential in `ps`.
func TestRunPassesTheAPIKeyByEnvironmentNotArgv(t *testing.T) {
	const key = "AIza-not-a-real-key"
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain", WithAPIKey(key))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, a := range r.args {
		if strings.Contains(a, key) {
			t.Fatalf("API key reached argv (%q)", a)
		}
	}
	var saw bool
	for _, e := range r.env {
		if e == "GEMINI_API_KEY="+key {
			saw = true
		}
	}
	if !saw {
		t.Errorf("env = %v, want GEMINI_API_KEY passed to the subprocess", r.env)
	}
}

// TestRunLeavesAgysOwnAPIRetryAlone is resume.go's "there is nothing of
// agy's to bound" written as an assertion: this package passes no retry
// bound, in the argv or in the environment, and would have to start doing
// so for that section to be out of date.
//
// It is deliberately a search for a *name* rather than for one flag,
// because agy has none today -- the point is to notice the day this
// package invents one, since a bound grain sets is a decision that has to
// be argued in that header rather than added quietly. The env entries Run
// builds are appended after os.Environ() (hostRunner.Run), so an operator
// who has some future knob set on the controller keeps it either way.
func TestRunLeavesAgysOwnAPIRetryAlone(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Flags and variable *names* only. A value carrying the string is
	// ordinary -- a sandbox path under this very test's temp directory
	// does, since the test is named after what it is looking for.
	for _, a := range r.args {
		if strings.HasPrefix(a, "-") && strings.Contains(strings.ToLower(a), "retr") {
			t.Errorf("args = %v, want no retry bound of grain's own in agy's argv: see resume.go's "+
				"\"A refusal reaches this loop immediately\", which says there is nothing there to bound", r.args)
		}
	}
	for _, e := range r.env {
		name, _, _ := strings.Cut(e, "=")
		if strings.Contains(strings.ToLower(name), "retr") {
			t.Errorf("env = %v, want no retry bound of grain's own in agy's environment: see resume.go's "+
				"\"A refusal reaches this loop immediately\", which says there is nothing there to bound", r.env)
		}
	}
}

// TestRunAsksAgyToUseTheAPIKeyItIsGiven is the failure this pair of
// files was written for: agy reads GEMINI_API_KEY only for a session
// whose settings name the gemini model provider, and a run without that
// setting ignores the key, falls through to its interactive browser
// login and dies with "authentication required. Run 'agy' to log in,
// then retry" -- with a prompt on stdin there is no terminal to log in
// at. Passing the key is therefore only half of authenticating with it.
func TestRunAsksAgyToUseTheAPIKeyItIsGiven(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain", WithAPIKey("AIza-not-a-real-key"))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !r.sawSettings {
		t.Fatalf("no %s written into the private HOME; agy would ignore GEMINI_API_KEY and ask for a browser login", cliSettingsRelPath)
	}
	var settings struct {
		ModelProvider string `json:"modelProvider"`
	}
	if err := json.Unmarshal([]byte(r.settingsAtRun), &settings); err != nil {
		t.Fatalf("agy settings were not JSON: %v (%q)", err, r.settingsAtRun)
	}
	if settings.ModelProvider != "gemini" {
		t.Errorf("modelProvider = %q, want %q -- the one value that makes agy authenticate from GEMINI_API_KEY",
			settings.ModelProvider, "gemini")
	}
}

// The other side of it: a deployment that configured no key at all means
// agy to authenticate however its own environment says, and pinning the
// provider for it would break that rather than fix anything. The settings
// file is still written -- it carries this run's permission rules too --
// so what must be absent is the setting, not the file.
func TestRunLeavesAgysProviderAloneWithoutAKey(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !r.sawSettings {
		t.Fatalf("no %s written into the private HOME; this run's permission rules go there", cliSettingsRelPath)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(r.settingsAtRun), &settings); err != nil {
		t.Fatalf("agy settings were not JSON: %v (%q)", err, r.settingsAtRun)
	}
	if _, ok := settings["modelProvider"]; ok {
		t.Errorf("modelProvider = %v for a run with no API key, want it left unset so agy authenticates as its own environment says",
			settings["modelProvider"])
	}
}

// TestRunUsesAgy12PermissionResources verifies every rule uses AGY 1.2's
// action(target) grammar. Bare eager-tool names were accepted by 1.1.x
// but are rejected by 1.2 as invalid grant strings.
func TestRunUsesAgy12PermissionResources(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain", WithAPIKey("AIza-not-a-real-key"))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var settings struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(r.settingsAtRun), &settings); err != nil {
		t.Fatalf("agy settings were not JSON: %v (%q)", err, r.settingsAtRun)
	}

	for _, resource := range deniedNativeResources {
		if !slices.Contains(settings.Permissions.Deny, resource) {
			t.Errorf("deny = %v, want native resource %q denied", settings.Permissions.Deny, resource)
		}
	}
	for _, tool := range []string{"run_command", "write_file", "comment_on_issue"} {
		want := agyMCPPermission(tool)
		if !slices.Contains(settings.Permissions.Allow, want) {
			t.Errorf("allow = %v, want grain's MCP resource %q allowed", settings.Permissions.Allow, want)
		}
	}
	for decision, rules := range map[string][]string{
		"allow": settings.Permissions.Allow,
		"deny":  settings.Permissions.Deny,
	} {
		for _, rule := range rules {
			if !strings.Contains(rule, "(") || !strings.HasSuffix(rule, ")") {
				t.Errorf("%s rule %q is not an AGY 1.2 action(target) resource", decision, rule)
			}
			if strings.HasPrefix(rule, mcp.AgyQualifiedToolName("")) {
				t.Errorf("%s rule %q uses the eager roster name, which AGY 1.2 rejects as an invalid grant", decision, rule)
			}
		}
	}
}

// TestRunPutsGrainInFrontOfEveryToolCall: the hooks.json in the private
// HOME points a PreToolUse hook at this very binary, which is the one
// place agy documents as able to block a tool call outright.
func TestRunPutsGrainInFrontOfEveryToolCall(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/opt/grain/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !r.sawHooks {
		t.Fatalf("no %s written into the private HOME; nothing then stands between the model and agy's own tools", hooksConfigRelPath)
	}
	var hooks map[string]struct {
		PreToolUse []struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"PreToolUse"`
	}
	if err := json.Unmarshal([]byte(r.hooksAtRun), &hooks); err != nil {
		t.Fatalf("hook config was not JSON: %v (%q)", err, r.hooksAtRun)
	}
	groups := hooks[HookName].PreToolUse
	if len(groups) != 1 || len(groups[0].Hooks) != 1 {
		t.Fatalf("PreToolUse = %+v, want exactly one handler in one group", groups)
	}
	if groups[0].Matcher != "*" {
		t.Errorf("matcher = %q, want %q -- HookDecision reads the name out of the payload, so the hook has to see every call",
			groups[0].Matcher, "*")
	}
	cmd := groups[0].Hooks[0].Command
	if !strings.Contains(cmd, "/opt/grain/bin/grain") || !strings.Contains(cmd, HookSubcommand) {
		t.Errorf("hook command = %q, want this binary's own path and %q", cmd, HookSubcommand)
	}
}

// TestHookDecisionAllowsOnlyGrainsExactTools is the policy itself. The
// hook must fail closed as AGY adds native tools, while leaving every
// exact tool published by grain's private MCP server alone.
func TestHookDecisionAllowsOnlyGrainsExactTools(t *testing.T) {
	reply := func(name string) []byte {
		payload, err := json.Marshal(map[string]any{
			"toolCall": map[string]any{"name": name, "args": map[string]any{"CommandLine": "ls"}},
			"stepIdx":  3,
		})
		if err != nil {
			t.Fatalf("building payload: %v", err)
		}
		return HookDecision(payload)
	}
	decision := func(payload []byte) string {
		var out struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		if err := json.Unmarshal(payload, &out); err != nil {
			t.Fatalf("hook reply was not JSON: %v (%q)", err, payload)
		}
		if out.Reason == "" {
			t.Errorf("denial carries no reason; AGY shows it to the model")
		}
		return out.Decision
	}

	// Grain's exact MCP tools, including names whose verbs collide with
	// AGY's native tools, are the complete allowlist.
	for _, tool := range publishedTools() {
		name := mcp.AgyQualifiedToolName(tool)
		if got := reply(name); len(got) != 0 {
			t.Errorf("HookDecision(%q) = %s, want no decision for the tool that reaches the sandbox", name, got)
		}
	}

	// Known native tools and names introduced after this code was written
	// are denied alike. A prefix alone is insufficient: only a tool grain
	// actually publishes is allowed.
	denied := append([]string(nil), withheldNativeTools...)
	denied = append(denied,
		"browser_click_element",
		"finish",
		"call_mcp_tool",
		"",
		"run_command ",
		mcp.AgyQualifiedToolName("future_unpublished_tool"),
	)
	for _, name := range denied {
		if got := decision(reply(name)); got != "deny" {
			t.Errorf("decision(%q) = %q, want deny", name, got)
		}
	}

	// A payload the hook cannot identify must fail closed too.
	for _, junk := range [][]byte{nil, []byte(""), []byte("not json"), []byte(`{"toolCall":7}`)} {
		if got := decision(HookDecision(junk)); got != "deny" {
			t.Errorf("HookDecision(%q) = %q, want deny for an unreadable payload", junk, got)
		}
	}
}

// TestHookDenialPointsAtToolsThatExist: the reason travels back to the
// model, so every tool it names has to be one this run actually holds.
// Prefixing the denied tool's own name is what this guards against --
// grain has no write_to_file, so "use mcp_grain-sandbox_write_to_file"
// sent a live model looking for a tool that is not there.
func TestHookDenialPointsAtToolsThatExist(t *testing.T) {
	published := map[string]bool{}
	for _, tool := range publishedTools() {
		published[mcp.AgyQualifiedToolName(tool)] = true
	}
	prefix := mcp.AgyQualifiedToolName("")

	for _, native := range withheldNativeTools {
		payload, err := json.Marshal(map[string]any{"toolCall": map[string]any{"name": native}})
		if err != nil {
			t.Fatalf("building payload: %v", err)
		}
		var out struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(HookDecision(payload), &out); err != nil {
			t.Fatalf("hook reply for %q was not JSON: %v", native, err)
		}
		for _, word := range strings.FieldsFunc(out.Reason, func(r rune) bool { return r == ' ' || r == ',' || r == '.' }) {
			if !strings.HasPrefix(word, prefix) || word == prefix+"*" {
				continue
			}
			if !published[word] {
				t.Errorf("denying %q suggests %q, which this run does not have; name tools from the constructor rather than from the denied name",
					native, word)
			}
		}
	}
}

// TestRunClearsAnAmbientGoogleAPIKey: the subprocess inherits the
// controller's environment, and agy prefers GOOGLE_API_KEY over the
// GEMINI_API_KEY grain passes when both are set. An unrelated key
// exported on the controller would otherwise become the credential every
// run bills against.
func TestRunClearsAnAmbientGoogleAPIKey(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain", WithAPIKey("AIza-not-a-real-key"))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !slices.Contains(r.env, "GOOGLE_API_KEY=") {
		t.Errorf("env = %v, want GOOGLE_API_KEY cleared so grain's own key is the one agy uses", r.env)
	}
}

// TestAgyConfigPathsAreTheOnesAgyReads pins the two paths in agy's HOME
// this package depends on. They are asserted as literals rather than
// against the vars themselves because that is the whole content of the
// dependency: written anywhere else -- ~/.gemini/settings.json, where
// Gemini CLI kept both -- neither file is an error, it is silently
// ignored, and a run gets no grain tools and no working credential. With
// no --strict-mcp-config to refuse on, agy would say nothing about it
// either.
func TestAgyConfigPathsAreTheOnesAgyReads(t *testing.T) {
	if want := filepath.Join(".gemini", "config", "mcp_config.json"); mcpConfigRelPath != want {
		t.Errorf("mcpConfigRelPath = %q, want %q (what `agy mcp list` reads)", mcpConfigRelPath, want)
	}
	if want := filepath.Join(".gemini", "antigravity-cli", "settings.json"); cliSettingsRelPath != want {
		t.Errorf("cliSettingsRelPath = %q, want %q (agy's own settings file)", cliSettingsRelPath, want)
	}
}

// TestRunNamesAModelAndAnEffort: a model selection is two values to agy,
// and it refuses a bare family name before the run starts ("--model
// gemini-3.1-pro requires --effort (available: low, high)"). So a model
// a deployment names by its family is passed with the default effort
// beside it.
func TestRunNamesAModelAndAnEffort(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain", WithModel("gemini-3.1-pro"))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !argsHave(r.args, "--model", "gemini-3.1-pro") {
		t.Errorf("args = %v, want --model gemini-3.1-pro", r.args)
	}
	if !argsHave(r.args, "--effort", DefaultEffort) {
		t.Errorf("args = %v, want --effort %s: agy refuses a bare family name without one", r.args, DefaultEffort)
	}
	if !slices.Contains(Efforts(), DefaultEffort) {
		t.Errorf("DefaultEffort = %q, want one of %v (agy's own --effort vocabulary)", DefaultEffort, Efforts())
	}
}

// TestRunWithNoModelLeavesAgyToItsOwnDefault: grain compiles in no model
// name, so a framework nobody named a model for asks agy for none -- and,
// since the effort pairs with a model, for no effort either. Nor does
// "auto", agy's own "pick one for me", take one.
func TestRunWithNoModelLeavesAgyToItsOwnDefault(t *testing.T) {
	for _, model := range []string{"", "auto"} {
		r := &recordingRunner{stdout: okStream()}
		f := newFramework(r, "/usr/local/bin/grain", WithModel(model), WithEffort("high"))
		if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
			t.Fatalf("Run(%q): %v", model, err)
		}
		if model == "" && slices.Contains(r.args, "--model") {
			t.Errorf("args = %v, want no --model when none is named", r.args)
		}
		if slices.Contains(r.args, "--effort") {
			t.Errorf("model %q: args = %v, want no --effort", model, r.args)
		}
		if got := f.Effort(); got != "" {
			t.Errorf("model %q: Effort() = %q, want empty", model, got)
		}
	}
}

// TestWithEffortIsWhatAgyIsAskedFor: the effort is a setting of its own
// (`grain settings -gemini-effort`), so the value a deployment chose has
// to be the one on the command line.
func TestWithEffortIsWhatAgyIsAskedFor(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain", WithModel("gemini-3.8-flash"), WithEffort("medium"))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !argsHave(r.args, "--model", "gemini-3.8-flash") || !argsHave(r.args, "--effort", "medium") {
		t.Errorf("args = %v, want --model gemini-3.8-flash --effort medium", r.args)
	}
}

// TestRunDropsEffortForAModelThatNamesOne is the compatibility half of
// the pair above, and the reason it is not simply "pass both, always":
// `agy models` prints only the suffixed spelling, so a deployment that
// named a model before the effort was a setting of its own has
// gemini-3.1-pro-high stored -- and agy refuses that name alongside an
// --effort that disagrees with it ("--model gemini-3.1-pro-high
// conflicts with --effort=low"), which would fail every run of a
// deployment that had changed neither setting.
func TestRunDropsEffortForAModelThatNamesOne(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain", WithModel("gemini-3.1-pro-high"), WithEffort("low"))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !argsHave(r.args, "--model", "gemini-3.1-pro-high") {
		t.Errorf("args = %v, want the stored model passed through untouched", r.args)
	}
	if slices.Contains(r.args, "--effort") {
		t.Errorf("args = %v, want no --effort: agy rejects one alongside a model whose name carries its own", r.args)
	}
}

// TestRunDropsEffortForAuto: agy picks the model, and its effort, for
// --model auto, and refuses an --effort beside it.
func TestRunDropsEffortForAuto(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain", WithModel("auto"), WithEffort("high"))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !argsHave(r.args, "--model", "auto") {
		t.Errorf("args = %v, want --model auto", r.args)
	}
	if slices.Contains(r.args, "--effort") {
		t.Errorf("args = %v, want no --effort: agy rejects one alongside --model auto", r.args)
	}
}

// TestWithoutEffortPassesNone: empty means "say nothing about the
// effort", which is what leaves agy's own answer in place -- the same
// thing an empty model does, and what a stored row written before this
// column existed reads back as.
func TestWithoutEffortPassesNone(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain", WithEffort(""))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if slices.Contains(r.args, "--effort") {
		t.Errorf("args = %v, want no --effort at all", r.args)
	}
}

// TestRunOverridesAgysPermissionPromptsByDefault pins the flag every
// dispatch runs under, and the one seam that drops it.
//
// The default is the compatibility property: agy's own permission mode is
// request-review, while existing unattended runs historically used this
// override. WithAmbientPermissions is the explicit way to leave decisions to
// permissionRules instead; TestLiveRunWithAmbientPermissions proves with a
// real agy that grain's allowlisted tools still work in that mode.
func TestRunOverridesAgysPermissionPromptsByDefault(t *testing.T) {
	// One sandbox root for both runs below: --add-dir names it, and two
	// TempDirs would make the two command lines differ for a reason that
	// has nothing to do with the flag under test.
	root := t.TempDir()
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: root}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !slices.Contains(r.args, "--dangerously-skip-permissions") {
		t.Errorf("args = %v, want --dangerously-skip-permissions: without it agy runs in request-review mode, "+
			"where a headless run soft-denies every call that needs a confirmation", r.args)
	}

	// And the probe's own Framework, which is the same run with that one
	// argument gone. Everything else about the command line has to be
	// untouched, or the live measurement it drives would be measuring
	// some other run than the one a dispatch gets.
	probe := &recordingRunner{stdout: okStream()}
	pf := newFramework(probe, "/usr/local/bin/grain", WithAmbientPermissions())
	if _, err := pf.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: root}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if slices.Contains(probe.args, "--dangerously-skip-permissions") {
		t.Errorf("args = %v, want the override dropped for WithAmbientPermissions", probe.args)
	}
	var want []string
	for _, a := range r.args {
		if a != "--dangerously-skip-permissions" {
			want = append(want, a)
		}
	}
	// Both execs must capture diagnostics, but their private paths are
	// intentionally different. Normalize only that value for comparison.
	probeArgs := slices.Clone(probe.args)
	for i, a := range want {
		if a == "--log-file" && i+1 < len(want) {
			want[i+1] = "<private-log>"
		}
	}
	for i, a := range probeArgs {
		if a == "--log-file" && i+1 < len(probeArgs) {
			probeArgs[i+1] = "<private-log>"
		}
	}
	if !slices.Equal(probeArgs, want) {
		t.Errorf("args without the override = %v, want %v: the probe differs from a dispatch in that flag alone",
			probe.args, want)
	}
}

// TestRunMirrorsTheRawStreamToTranscriptPath is what LiveTranscriptDir
// reads: the bytes on disk while a run is still going, not a narrative
// assembled once it ends.
func TestRunMirrorsTheRawStreamToTranscriptPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-1")
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), TranscriptPath: path,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the mirrored transcript: %v", err)
	}
	if string(data) != okStream() {
		t.Errorf("mirrored transcript = %q, want the raw stream verbatim", data)
	}
	if got := PartialTranscript(string(data)); !strings.Contains(got, "wrote ok.txt") {
		t.Errorf("PartialTranscript of the mirror = %q, want the run's own narrative", got)
	}
}

// The mirror is a run's raw tool output, so nobody but the daemon's own
// user may read it.
func TestRunCreatesTheTranscriptOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-1")
	f := newFramework(&recordingRunner{stdout: okStream()}, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), TranscriptPath: path,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat transcript: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("transcript mode = %o, want 600", got)
	}
}

// TestRunPassesAPrintTimeoutPastGrainsOwnDeadline pins the flag whose
// absence ended every dispatched run five minutes in: agy's print mode
// caps a whole non-interactive run at 5m by default and kills it with
// "timeout waiting for response". The value has to sit past the deadline
// grain will cancel the run at, so that grain is always what stops a run
// and the reason it stopped is always reported.
func TestRunPassesAPrintTimeoutPastGrainsOwnDeadline(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	if _, err := f.Run(ctx, agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, ok := flagValueFor(r.args, "--print-timeout")
	if !ok {
		t.Fatalf("args = %v, want --print-timeout among them; without it agy stops the run at 5m", r.args)
	}
	d, err := time.ParseDuration(got)
	if err != nil {
		t.Fatalf("--print-timeout %q is not a duration agy would parse: %v", got, err)
	}
	if d <= 90*time.Minute {
		t.Errorf("--print-timeout = %s, want more than the run's own 90m deadline", d)
	}
}

// A run with no deadline at all -- a test, or a caller that means it to
// take as long as it takes -- still needs a cap, since agy's flag has no
// "never" value and its default is five minutes.
func TestRunPassesAPrintTimeoutWithNoDeadline(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := flagValueFor(r.args, "--print-timeout")
	d, err := time.ParseDuration(got)
	if err != nil {
		t.Fatalf("--print-timeout %q is not a duration agy would parse: %v", got, err)
	}
	if d < time.Hour {
		t.Errorf("--print-timeout = %s, want a cap no ordinary run could reach", d)
	}
}

// A granule run has no local sandbox path to start agy in, and used to be
// left in whatever directory the daemon itself was started in -- which is
// where agy's own native file tools would have written. It gets an empty
// scratch directory instead.
func TestGranuleRunStartsAgyInAnEmptyScratchDirectory(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain", WithGranuleSSH("debian", "", "/work"))

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", GranuleVM: "vm-1",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.dir == "" {
		t.Fatal("agy was started with no working directory; it would inherit the daemon's own")
	}
	if !r.dirExistedAtRun {
		t.Fatalf("the working directory agy was given (%s) did not exist", r.dir)
	}
	if r.dirEntriesAtRun != 0 {
		t.Errorf("working directory %s held %d entries, want an empty scratch directory",
			r.dir, r.dirEntriesAtRun)
	}
}

// flagValueFor reads the value that follows name in an argument list.
func flagValueFor(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// TestVerifyToolRosterNotesARunThatCannotReachGrain covers the one
// conclusion agy's init roster actually supports: a roster with neither
// the call_mcp_tool dispatcher nor any eagerly registered grain tool is a
// run that cannot touch its sandbox or report back, and whoever reads the
// run is told so.
func TestVerifyToolRosterNotesARunThatCannotReachGrain(t *testing.T) {
	capture := stream(
		`{"event":"init","init":{"tools":["run_command","view_file"]}}`,
		`{"event":"result","result":{"status":"SUCCESS","response":"done"}}`,
	)
	if note := verifyToolRoster(capture); note == "" {
		t.Fatal("verifyToolRoster = \"\", want a note for a roster with no route to grain's tools")
	}

	r := &recordingRunner{stdout: capture}
	f := newFramework(r, "/usr/local/bin/grain")
	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.Transcript, "call_mcp_tool") {
		t.Errorf("Transcript = %q, want the missing bridge named in it", result.Transcript)
	}
}

// TestVerifyToolRosterAcceptsAgysOwnNativeRoster is the other side, and
// the reason the check had to be rewritten: a real agy advertises its own
// 57 native tools and no grain tool at all, so the old "which of these
// did grain not publish?" reading flagged every one of them on every
// single run. What that roster does carry is a way through to grain --
// call_mcp_tool, or an eagerly registered mcp_grain-sandbox_* name -- and
// either one means there is nothing to report.
func TestVerifyToolRosterAcceptsAgysOwnNativeRoster(t *testing.T) {
	for name, roster := range map[string][]string{
		"the lazy dispatcher": {"run_command", "view_file", "call_mcp_tool"},
		"an eager grain tool": {"run_command", mcp.AgyQualifiedToolName("ask_question")},
	} {
		t.Run(name, func(t *testing.T) {
			tools, err := json.Marshal(roster)
			if err != nil {
				t.Fatal(err)
			}
			capture := stream(
				`{"event":"init","init":{"tools":`+string(tools)+`}}`,
				`{"event":"result","result":{"status":"SUCCESS","response":"done"}}`,
			)
			if note := verifyToolRoster(capture); note != "" {
				t.Errorf("verifyToolRoster = %q, want none for a roster that reaches grain", note)
			}
		})
	}
}

// A capture with no init event at all -- a run killed before agy spoke --
// supports no conclusion either way, and must not produce a note that
// reads like a misconfiguration.
func TestVerifyToolRosterSaysNothingWithoutARoster(t *testing.T) {
	if note := verifyToolRoster(stream(
		`{"event":"result","result":{"status":"SUCCESS","response":"done"}}`,
	)); note != "" {
		t.Errorf("verifyToolRoster = %q, want none when there is no roster to read", note)
	}
}

// The daemon's address and the run's task id are what the forked
// mcpserver needs to offer open_pull_request at all, and they only travel
// as its own arguments -- the same pair agent/claude passes, since both
// fork the identical server.
func TestRunPassesTheGrainServerAndTaskToTheMCPServer(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain", WithGrainServer("http://127.0.0.1:8420"))

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "x", SandboxRoot: t.TempDir(), TaskID: "t1",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	args := settingsArgs(t, r.homeAtRun)
	if !argsHave(args, "-server", "http://127.0.0.1:8420") || !argsHave(args, "-task", "t1") {
		t.Errorf("mcpserver args = %v, want -server and -task among them", args)
	}
}

// Both or neither: one without the other names a question with nowhere
// to send it, and mcpserver rejects either half on its own.
func TestRunOmitsTheGrainServerWithoutATaskID(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain", WithGrainServer("http://127.0.0.1:8420"))

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "x", SandboxRoot: t.TempDir(),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	args := settingsArgs(t, r.homeAtRun)
	if argsHave(args, "-server", "") || argsHave(args, "-task", "") {
		t.Errorf("mcpserver args = %v, want neither -server nor -task", args)
	}
}

// open_pull_request is published like every other tool, and being on
// this list is what decides whether agy registers it as a tool the model
// can see at all: everything named here is asked for eagerly, and
// everything left off is reachable only through agy's dispatcher, if the
// model thinks to look for it.
func TestPublishedToolsNamesOpenPullRequest(t *testing.T) {
	names := publishedTools()
	for _, n := range names {
		if n == "open_pull_request" {
			return
		}
	}
	t.Errorf("publishedTools() = %v, want open_pull_request among them", names)
}

// The names agy is asked to register eagerly, and that HookDecision
// admits, are bare -- the spelling that subcommand registers them under.
// A name arriving here already carrying claude's own prefix is qualified
// twice by eagerToolNames and becomes one nothing offers and the hook
// denies, which is what the self-debug task readers were published as
// until agent.PublishedToolNames became the single list.
func TestPublishedToolsAreBareAndCoverTheGrantedTools(t *testing.T) {
	names := publishedTools()
	for _, n := range names {
		if strings.HasPrefix(n, "mcp_") {
			t.Errorf("publishedTools() = %v, want bare names; %q is already qualified", names, n)
		}
	}
	for _, want := range []string{"read_grain_task", "get_ci_log", "list_ci_runs", "read_deployment_health"} {
		if !slices.Contains(names, want) {
			t.Errorf("publishedTools() = %v, want %s among them", names, want)
		}
	}
}

// TestRunAsksForEveryGrainToolEagerly pins the config key that decides
// whether a run has grain's tools at all. Without it agy loads them
// lazily: they appear in no roster, the model reaches for agy's own
// native tools instead -- which run on the controller, not in the sandbox
// -- and what does get called is recorded under the dispatcher's name
// rather than the tool's.
func TestRunAsksForEveryGrainToolEagerly(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var config struct {
		MCPServers map[string]struct {
			Tools map[string]struct {
				Eager bool `json:"eager"`
			} `json:"tools"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(r.homeAtRun), &config); err != nil {
		t.Fatalf("mcp config was not JSON: %v (%s)", err, r.homeAtRun)
	}
	tools := config.MCPServers[mcpServerName].Tools
	for _, name := range publishedTools() {
		if !tools[name].Eager {
			t.Errorf("mcp config asks for %q lazily; every published tool must be eager", name)
		}
	}
}

// settingsArgs pulls the grain-sandbox server's own argument list out of
// the settings file a run's private HOME was given.
func settingsArgs(t *testing.T, settingsJSON string) []string {
	t.Helper()
	var settings struct {
		MCPServers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(settingsJSON), &settings); err != nil {
		t.Fatalf("settings file was not JSON: %v (%s)", err, settingsJSON)
	}
	return settings.MCPServers[mcpServerName].Args
}

func argsHave(args []string, name, value string) bool {
	for i, a := range args {
		if a == name && i+1 < len(args) && (value == "" || args[i+1] == value) {
			return true
		}
	}
	return false
}

// See the matching test in pkg/agent/claude: with `granule run` generating
// a keypair per guest, no exec key is the normal configuration, and an
// empty "-exec-key" is worse than none at all.
func TestRunOmitsExecKeyWhenUnset(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newFramework(r, "/usr/local/bin/grain", WithGranuleSSH("agent", "", "/workspace"))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", GranuleVM: "grain-run-7"}); err != nil {
		t.Fatalf("a granule run with no exec key should work, got: %v", err)
	}
	var settings struct {
		MCPServers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(r.homeAtRun), &settings); err != nil {
		t.Fatalf("settings file was not JSON: %v", err)
	}
	for _, arg := range settings.MCPServers[mcpServerName].Args {
		if arg == "-exec-key" {
			t.Errorf("args = %v, want no -exec-key when none is configured", settings.MCPServers[mcpServerName].Args)
		}
	}
}

// The prompt's half of open_pull_request: orchestrator.BuildPrompt names
// the tool only for a run that really has it, and this is the answer it
// asks for (agent.PullRequestFramework). It tracks WithGrainServer alone,
// since that is the half of -server/-task a Framework holds -- the other
// half, a task id, comes with every dispatch.
func TestCanOpenPullRequestTracksWithGrainServer(t *testing.T) {
	with := New("agy", "/usr/local/bin/grain", WithGrainServer("http://127.0.0.1:8420"))
	if !with.CanOpenPullRequest() {
		t.Error("CanOpenPullRequest() = false for a Framework built WithGrainServer")
	}
	without := New("agy", "/usr/local/bin/grain")
	if without.CanOpenPullRequest() {
		t.Error("CanOpenPullRequest() = true for a Framework with no daemon to ask -- " +
			"its runs' mcpserver registers no open_pull_request at all")
	}
}

// TestToolPreambleNamesEveryToolThatReachesTheSandbox pins the half of
// the preamble the model has to act on: the tools that do the work are
// named, in agy's own spelling for them, and they are read off the
// constructor rather than out of the prose -- a tool added to
// mcp.NewSandboxTools that the prompt never mentions is one the model
// has no reason to look for.
func TestToolPreambleNamesEveryToolThatReachesTheSandbox(t *testing.T) {
	preamble := toolPreamble(agent.RunConfig{SandboxRoot: "/w"})
	t.Log(preamble)

	for _, tool := range mcp.NewSandboxTools("") {
		name := mcp.AgyQualifiedToolName(tool.Name)
		if !strings.Contains(preamble, name) {
			t.Errorf("preamble = %q, want %s named in it", preamble, name)
		}
	}
}

// TestToolPreambleTellsARunWhichOfItsOwnToolsNotToUse is the other half,
// and the one agy gives grain no switch for: its native tools cannot be
// withheld, so the prompt has to name them. Naming them matters more than
// stating the rule, because the rule ("anything without grain's prefix")
// leaves the model to work out which of the 57 tools in front of it that
// covers, and the two rosters share names -- so the collision is called
// out too.
func TestToolPreambleTellsARunWhichOfItsOwnToolsNotToUse(t *testing.T) {
	preamble := toolPreamble(agent.RunConfig{SandboxRoot: "/w"})

	if !strings.Contains(preamble, strings.Join(withheldNativeTools, ", ")) {
		t.Errorf("preamble = %q, want agy's own tools named in it: %s",
			preamble, strings.Join(withheldNativeTools, ", "))
	}
	if !strings.Contains(preamble, "unavailable") {
		t.Errorf("preamble = %q, want it to say what to do about them, not just that they exist", preamble)
	}
	// The rule that covers the tools this list does not name.
	if !strings.Contains(preamble, "anything else whose name does not begin "+mcp.AgyQualifiedToolName("")) {
		t.Errorf("preamble = %q, want the rule that covers the rest of agy's roster", preamble)
	}
	// agy's own run_command and grain's are different tools on different
	// machines. A model that misses that picks the wrong one while
	// believing it picked the right one.
	if !strings.Contains(preamble, "collide") {
		t.Errorf("preamble = %q, want the shared tool names called out", preamble)
	}
}

// A granule run's sandbox is a VM the forked mcpserver reaches over SSH,
// so "your sandbox" and "the machine hosting this session" are two
// different computers rather than two directories -- worth saying, since
// that is the run where a native tool call lands nowhere near the work.
func TestToolPreambleInstructsFullCommentOnIssue(t *testing.T) {
	preamble := toolPreamble(agent.RunConfig{SandboxRoot: "/w"})
	if !strings.Contains(preamble, mcp.AgyQualifiedToolName("comment_on_issue")) {
		t.Errorf("preamble = %q, want comment_on_issue named", preamble)
	}
	if !strings.Contains(preamble, "never truncate") {
		t.Errorf("preamble = %q, want instruction not to truncate comment_on_issue", preamble)
	}
}

func TestToolPreambleSaysAGranuleSandboxIsAnotherMachine(t *testing.T) {
	preamble := toolPreamble(agent.RunConfig{GranuleVM: "vm-1"})
	if !strings.Contains(preamble, "separate virtual machine") {
		t.Errorf("preamble = %q, want a granule run told its sandbox is another machine", preamble)
	}
}

// joinNames is prose, not JSON: a model reads "a, b and c" as an
// instruction and ["a","b","c"] as a data structure that wandered into
// one.
func TestJoinNamesReadsAsProse(t *testing.T) {
	for names, want := range map[string]string{
		"":      "",
		"a":     "a",
		"a,b":   "a and b",
		"a,b,c": "a, b and c",
	} {
		var parts []string
		if names != "" {
			parts = strings.Split(names, ",")
		}
		if got := joinNames(parts); got != want {
			t.Errorf("joinNames(%v) = %q, want %q", parts, got, want)
		}
	}
}

// TestRunReportsAgysOwnErrorAlongsideItsExitStatus pins the shape a real
// failed run has: agy writes what went wrong into its terminal result
// event and *then* exits 1 with nothing at all on stderr. Reporting only
// the exit status left the daemon's log saying "running agy: exit status
// 1 (stderr: )", and since the stream-json capture is removed when the
// run finishes, the sentence dropped here was the last copy of the only
// account of the failure anywhere.
func TestRunReportsAgysOwnErrorAlongsideItsExitStatus(t *testing.T) {
	exitErr := errors.New("exit status 1 (stderr: )")
	r := &recordingRunner{
		stdout: stream(
			initLine,
			`{"event":"result","result":{"status":"ERROR","response":"",`+
				`"error":"There was a network issue connecting to the server, please try again."}}`,
		),
		err: exitErr,
	}
	f := newFramework(r, "/usr/local/bin/grain")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run err = nil, want the failure")
	}
	if !strings.Contains(err.Error(), "There was a network issue") {
		t.Errorf("Run err = %q, want agy's own account of why the run failed", err)
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("Run err = %q, want the exit status kept alongside it", err)
	}
	if !errors.Is(err, exitErr) {
		t.Errorf("Run err = %q, want the subprocess's own error still wrapped in it", err)
	}
	// agent.Framework's contract, unchanged by any of this: a run that
	// pushed a branch before the network gave out still comes back with
	// what it did.
	if result == nil {
		t.Error("Run returned no Result at all, want the partial one alongside the failure")
	}
}

// An agy that died before saying anything -- a missing binary, a signal --
// has left nothing but its exit status, and that is what Run reports:
// there is no stream sentence to lead with.
func TestRunReportsTheExitStatusAloneWhenAgySaidNothing(t *testing.T) {
	r := &recordingRunner{stdout: "", err: errors.New("fork/exec agy: no such file or directory")}
	f := newFramework(r, "/usr/local/bin/grain")

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run err = nil, want the failure")
	}
	if !strings.Contains(err.Error(), "antigravity: running agy: fork/exec agy") {
		t.Errorf("Run err = %q, want the subprocess's own failure named", err)
	}
}

func TestUnconfinedHostSettingsAndHooks(t *testing.T) {
	// 1. Confined (LocalHost / default):
	confinedFiles, err := agyHomeFiles("/bin/grain", []string{"mcpserver"}, "", false)
	if err != nil {
		t.Fatalf("agyHomeFiles: %v", err)
	}
	if _, ok := confinedFiles[hooksConfigRelPath]; !ok {
		t.Errorf("confined agyHomeFiles missing %s", hooksConfigRelPath)
	}
	var confinedSettings struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(confinedFiles[cliSettingsRelPath], &confinedSettings); err != nil {
		t.Fatalf("unmarshal confined settings: %v", err)
	}
	if len(confinedSettings.Permissions.Deny) == 0 {
		t.Errorf("confined settings missing deny rules")
	}

	// 2. Unconfined (e.g. RemoteHost):
	unconfinedFiles, err := agyHomeFiles("/bin/grain", []string{"mcpserver"}, "", true)
	if err != nil {
		t.Fatalf("agyHomeFiles: %v", err)
	}
	if _, ok := unconfinedFiles[hooksConfigRelPath]; ok {
		t.Errorf("unconfined agyHomeFiles should not write %s", hooksConfigRelPath)
	}
	var unconfinedSettings struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(unconfinedFiles[cliSettingsRelPath], &unconfinedSettings); err != nil {
		t.Fatalf("unmarshal unconfined settings: %v", err)
	}
	if len(unconfinedSettings.Permissions.Deny) != 0 {
		t.Errorf("unconfined settings should have empty deny rules, got %v", unconfinedSettings.Permissions.Deny)
	}
	if len(unconfinedSettings.Permissions.Allow) == 0 {
		t.Errorf("unconfined settings missing allow rules for MCP tools")
	}
}

// An adopting daemon reads a host run's stream with ParseResult: the
// whole stream parses as Run would have, a run that ended badly with no
// result comes back as a failure beside what it did, and a run Run left
// detached returns at once without a result.
func TestParseResultReadsAnAdoptedHostRunsStream(t *testing.T) {
	f := newFramework(&recordingRunner{}, "/usr/local/bin/grain")
	result, err := f.ParseResult(okStream(), nil)
	if err != nil || result == nil || len(result.ToolCalls) != 1 {
		t.Fatalf("ParseResult(ok) = %+v, %v", result, err)
	}
	cut := stream(initLine, toolActive(0, "write_file", `{"path":"ok.txt"}`), toolDone(0, "write_file", "wrote ok.txt"))
	result, err = f.ParseResult(cut, errors.New("exit status 1"))
	if err == nil || result == nil || len(result.ToolCalls) != 1 {
		t.Fatalf("ParseResult(cut off) = %+v, %v; want the tool call beside an error", result, err)
	}

	detached := newFramework(&recordingRunner{err: agent.ErrDetached}, "/usr/local/bin/grain")
	result, err = detached.Run(context.Background(), agent.RunConfig{Prompt: "p", SandboxRoot: t.TempDir()})
	if !errors.Is(err, agent.ErrDetached) || result != nil {
		t.Fatalf("Run on a detached host = %+v, %v; want ErrDetached and no result", result, err)
	}
}

// TestHookLetsPlanModeEndAndExplainsTheTimers: agy's exit_plan_mode is a
// plan-mode handoff rather than a host tool, so denying it trapped a run
// in plan mode (design/design/sandbox-first-agent-surface.design.md §5);
// enter_plan_mode and schedule are refused with a reason that says what
// to do instead.
func TestHookLetsPlanModeEndAndExplainsTheTimers(t *testing.T) {
	call := func(name string) []byte {
		payload, err := json.Marshal(map[string]any{"toolCall": map[string]any{"name": name}})
		if err != nil {
			t.Fatal(err)
		}
		return HookDecision(payload)
	}
	if got := call("exit_plan_mode"); len(got) != 0 {
		t.Errorf("HookDecision(exit_plan_mode) = %s, want no decision", got)
	}
	for name, want := range map[string]string{"enter_plan_mode": "Plan mode is off", "schedule": "grain ci wait"} {
		var out struct{ Decision, Reason string }
		if err := json.Unmarshal(call(name), &out); err != nil {
			t.Fatal(err)
		}
		if out.Decision != "deny" || !strings.Contains(out.Reason, want) {
			t.Errorf("HookDecision(%s) = %+v, want a denial mentioning %q", name, out, want)
		}
	}
	if pre := toolPreamble(agent.RunConfig{}); !strings.Contains(pre, "Never call exit_plan_mode, enter_plan_mode or schedule") {
		t.Errorf("toolPreamble does not warn off plan mode and timers: %q", pre)
	}
}
