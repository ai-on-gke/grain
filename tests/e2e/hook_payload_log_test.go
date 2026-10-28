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

package e2e

// The instrument TestLiveNativeToolsAreDenied uses to see the PreToolUse
// payloads a live agy actually sends, and the test that keeps that
// instrument honest.
//
// # Why a wrapper binary, and what it is for
//
// agy hands each hook `{"toolCall": {"name": ..., "args": ...}}` on stdin
// and reads a decision back, and antigravity.HookDecision matches that
// name against the tools grain withholds. Everything about that denial
// rests on the name arriving in the spelling the deny list is written in
// -- and the shape is not obvious from agy's own documentation, which
// says matchers match *step types* ("lowercasing the step type and
// removing the CORTEX_STEP_TYPE_ prefix"). If a name arrives in a shape
// HookDecision does not recognise, the deny list denies nothing, quietly,
// and the only symptom is a native tool that runs.
//
// A transcript cannot answer this: it reports the tool agy ran, not the
// name it asked the hook about. So the live test points agy's config at
// this wrapper instead of at grain itself. It is grain in every respect
// -- for anything but the hook subcommand it replaces itself with the
// real binary, so the forked MCP server agy spawns is the same process it
// would otherwise have been -- and for the hook it records the payload
// before handing it to the same grain, unchanged, and passes grain's own
// reply back on stdout.
//
// It exits 0 whatever happens, like the subcommand it stands in front of:
// measured against agy 1.1.26, a hook that exits non-zero fails the tool
// call outright, so an instrument that failed loudly would break the very
// runs it is watching.
//
// # Why this file also holds a test of the wrapper
//
// The wrapper only ever runs on the nightly, where a mistake in it looks
// exactly like the regression the nightly exists to catch: grain's tools
// unable to reach the sandbox, or a hook that denies nothing. The test
// below drives it directly -- no agy, no credential -- so a broken
// instrument fails in the pull-request suite, where the person who broke
// it is looking.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
)

// hookPayload is one PreToolUse call as agy sent it: the name grain's
// hook was asked to decide about, and the whole payload, which is the
// only place the arguments (and any field agy adds later) survive.
type hookPayload struct {
	Name string
	Raw  string
}

// buildHookRecordingGrain compiles a stand-in for the grain binary that
// records every hook payload to a file, and returns the two paths: the
// binary to hand antigravity.New, and the log to read afterwards.
func buildHookRecordingGrain(t *testing.T, grainPath string) (wrapperPath, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "hook-payloads.jsonl")
	wrapperPath = filepath.Join(dir, "grain-hook-recorder")

	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(fmt.Sprintf(hookRecorderSource,
		strconv.Quote(grainPath), strconv.Quote(logPath), strconv.Quote(antigravity.HookSubcommand))), 0o600); err != nil {
		t.Fatalf("writing the hook recorder's source: %v", err)
	}
	// Its own module, because `go build` of a lone file wants a module
	// context and this one deliberately lives outside the repository's:
	// it imports nothing but the standard library, and building it inside
	// the repo would make it a package the repo's own tooling has to
	// account for.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module grainhookrecorder\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatalf("writing the hook recorder's go.mod: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", wrapperPath, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the hook recorder: %v\n%s", err, out)
	}
	return wrapperPath, logPath
}

// hookRecorderSource is that stand-in. The three %s are the real grain
// binary, the log to append to, and the subcommand that means "this is a
// hook call" -- all quoted Go string literals.
const hookRecorderSource = `package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"syscall"
)

const (
	grainPath = %s
	logPath   = %s
	hookArg   = %s
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == hookArg {
		payload, _ := io.ReadAll(os.Stdin)
		record(payload)
		cmd := exec.Command(grainPath, os.Args[1:]...)
		cmd.Stdin = bytes.NewReader(payload)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		// Deliberately unchecked: a hook that exits non-zero fails the
		// tool call, so this stands in for grain's own "never fail the
		// run" contract as well as for its decision.
		_ = cmd.Run()
		return
	}
	// Everything else -- the forked MCP server, above all -- becomes the
	// real binary: same pid, same file descriptors, same process group,
	// so nothing about how grain is started or killed changes because a
	// test is watching.
	if err := syscall.Exec(grainPath, append([]string{grainPath}, os.Args[1:]...), os.Environ()); err != nil {
		os.Stderr.WriteString("grain-hook-recorder: " + err.Error() + "\n")
		os.Exit(1)
	}
}

// record appends one payload as a JSON string on a line of its own. A
// JSON string rather than the payload itself, so that a payload
// containing a newline cannot be read back as two calls, and so that a
// line that was torn by two hooks writing at once fails to parse instead
// of parsing as something else.
func record(payload []byte) {
	line, err := json.Marshal(string(payload))
	if err != nil {
		return
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}
`

// readHookPayloads reads back what the wrapper recorded. A log that is
// not there at all means no hook ever ran, which is a finding rather than
// an error here -- the caller says what it means.
func readHookPayloads(t *testing.T, logPath string) []hookPayload {
	t.Helper()
	contents, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading the recorded hook payloads: %v", err)
	}
	var payloads []hookPayload
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw string
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Logf("skipping an unreadable hook payload record (%v): %s", err, line)
			continue
		}
		var call struct {
			ToolCall struct {
				Name string `json:"name"`
			} `json:"toolCall"`
		}
		// An unparseable payload keeps its raw form and an empty name:
		// "agy sent the hook something this does not understand" is
		// exactly the answer this instrument exists to surface, and
		// dropping it would hide it.
		_ = json.Unmarshal([]byte(raw), &call)
		payloads = append(payloads, hookPayload{Name: call.ToolCall.Name, Raw: raw})
	}
	return payloads
}

// TestHookPayloadRecorderStandsInForGrain drives the wrapper directly:
// the hook path must record what it was given and still answer with
// grain's own decision, and everything else must be the real binary.
//
// This one needs neither agy nor a credential, so it runs in the ordinary
// suite -- which is the point of it. See this file's header.
func TestHookPayloadRecorderStandsInForGrain(t *testing.T) {
	t.Parallel()
	grainPath := buildGrainBinary(t)
	wrapperPath, logPath := buildHookRecordingGrain(t, grainPath)

	// A denied call: the wrapper must pass grain's deny through byte for
	// byte, since agy reads that reply and nothing else.
	denied := `{"toolCall":{"name":"run_command","args":{"command":"echo hi"}}}`
	gotWrapper, gotStatus := runHookBinary(t, wrapperPath, denied)
	wantGrain, _ := runHookBinary(t, grainPath, denied)
	if gotWrapper != wantGrain {
		t.Errorf("the recorder answered a denied call with %q; grain itself answers %q", gotWrapper, wantGrain)
	}
	if !strings.Contains(gotWrapper, `"deny"`) {
		t.Errorf("a call to agy's own run_command was answered %q, which is not a denial: this test's fixture no longer matches HookDecision", gotWrapper)
	}
	if gotStatus != 0 {
		t.Errorf("the recorder exited %d; a hook that exits non-zero fails the tool call it was asked about", gotStatus)
	}

	// And a call it has no opinion about, where the reply that matters is
	// no reply at all: an empty JSON object is a denial to agy.
	allowed := `{"toolCall":{"name":"mcp_grain-sandbox_run_command","args":{"command":"echo hi"}}}`
	if got, status := runHookBinary(t, wrapperPath, allowed); got != "" || status != 0 {
		t.Errorf("the recorder answered one of grain's own tools with %q (exit %d), want no output at all: "+
			"anything else stops the run's own tools", got, status)
	}

	payloads := readHookPayloads(t, logPath)
	if len(payloads) != 2 {
		t.Fatalf("the recorder logged %d payload(s), want 2: %+v", len(payloads), payloads)
	}
	if payloads[0].Name != "run_command" || payloads[1].Name != "mcp_grain-sandbox_run_command" {
		t.Errorf("the recorder logged %q and %q, want the two names it was sent, in order",
			payloads[0].Name, payloads[1].Name)
	}
	if !strings.Contains(payloads[0].Raw, "echo hi") {
		t.Errorf("the recorded payload %q dropped the call's arguments, which is where a live run's evidence is",
			payloads[0].Raw)
	}

	// Everything that is not a hook call is the real binary, which is
	// what lets agy fork the MCP server through this. A cheap verb
	// stands in for the server itself, which needs a sandbox and a
	// stdio peer to say anything.
	wrapped := runOutput(t, t.TempDir(), wrapperPath, "schema-version")
	direct := runOutput(t, t.TempDir(), grainPath, "schema-version")
	if wrapped != direct {
		t.Errorf("`grain schema-version` through the recorder printed %q, the real binary %q: "+
			"the recorder is not a transparent stand-in, so a live run through it proves nothing",
			wrapped, direct)
	}

	// The subcommand that actually matters, spoken to as agy speaks to
	// it: a live run reaches its sandbox by forking this binary's
	// "mcpserver" over a pipe, so a recorder that broke that would fail
	// the nightly as "the model never used its tools".
	p := startMCPServer(t, wrapperPath, "-sandbox-root", t.TempDir())
	if names := toolNames(t, p); !names["run_command"] {
		t.Errorf("the recorder's mcpserver advertised %v, without grain's own run_command among them", names)
	}
}

// runHookBinary runs one hook call against a binary and returns its
// stdout and exit status.
func runHookBinary(t *testing.T, binary, payload string) (stdout string, exitStatus int) {
	t.Helper()
	cmd := exec.Command(binary, antigravity.HookSubcommand)
	cmd.Stdin = strings.NewReader(payload)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running %s %s: %v\nstderr: %s", binary, antigravity.HookSubcommand, err, errOut.String())
		}
		exitStatus = exit.ExitCode()
	}
	return out.String(), exitStatus
}
