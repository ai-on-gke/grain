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

package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// The "agy-tool-hook" subcommand is the whole of what agy sees of
// grain's native-tool denial: agy runs this binary before every tool
// call a run makes, hands it the call on stdin, and reads the decision
// back off stdout. antigravity.HookDecision holds the policy and has its
// own tests; what has never been covered is this end of the pipe -- the
// bytes the subcommand actually writes, and the fact that it writes none
// at all when it has no opinion.
//
// That is the part that broke. The first version of this answered a call
// it had no opinion about with `{}`, on the reading that an object
// carrying no decision carries no opinion; agy reads a decision-less
// object as a deny, so a run so configured was refused *every* tool call
// it made, grain's own included, and could do nothing but say why
// (commit 3601f158, and antigravity.noOpinion for the measured
// contract). Nothing in the Go suite noticed: HookDecision's own tests
// asserted on the value it returned, and the subcommand that writes it
// to a file descriptor had no test at all.
//
// So these assert on the bytes, and deliberately on emptiness rather
// than on "not a deny": zero bytes is the one reply agy takes as "no
// opinion", and any JSON at all -- including valid, empty JSON -- is
// something else.
func TestAgyToolHookWritesNothingWhenItHasNoOpinion(t *testing.T) {
	for _, tt := range []struct {
		name    string
		payload string
	}{
		// Grain's own run_command shares a name with the AGY tool
		// of the same name and is the one call a mistaken deny would hurt
		// most: it is the only tool that reaches the run's sandbox.
		{"grain's own tool of a withheld name",
			`{"toolCall":{"name":"` + mcp.AgyQualifiedToolName("run_command") + `","args":{}}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr := runAgyToolHook(t, nil, tt.payload)
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing at all: agy reads any other reply -- "+
					"an empty JSON object included -- as a decision, and a decision-less "+
					"object as a deny", stdout)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want nothing for an ordinary call", stderr)
			}
		})
	}
}

// A tool this package withholds is denied, with a reason the model can
// act on. The name is taken from antigravity's own list rather than
// written out here, so this cannot drift from what the hook actually
// refuses.
func TestAgyToolHookDeniesAWithheldNativeTool(t *testing.T) {
	const tool = "write_to_file"
	if !antigravity.IsWithheldNativeTool(tool) {
		t.Fatalf("%s is no longer a withheld native tool; pick another for this test", tool)
	}

	stdout, _ := runAgyToolHook(t, nil, `{"toolCall":{"name":"`+tool+`","args":{"path":"/etc/passwd"}}}`)

	var decision struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(stdout), &decision); err != nil {
		t.Fatalf("stdout = %q, want the JSON decision agy reads back: %v", stdout, err)
	}
	if decision.Decision != "deny" {
		t.Errorf("decision = %q, want %q -- this is the one word agy documents as a hard block",
			decision.Decision, "deny")
	}
	// The reason reaches the model, so it has to name something the run
	// can actually call instead of the tool it was refused.
	if decision.Reason == "" {
		t.Error("reason is empty; the model is told what it may not do and nothing about what to do")
	}
}

// Argument handling, which is only worth a test because of what it must
// not cost: agy invokes this with no arguments, but a hook command that
// exits non-zero fails the tool call, so even a misuse has to leave the
// call alone rather than take the run's tools down with it.
func TestAgyToolHookStillAnswersWhenGivenArguments(t *testing.T) {
	stdout, stderr := runAgyToolHook(t, []string{"unexpected"}, `{"toolCall":{"name":"`+mcp.AgyQualifiedToolName("run_command")+`"}}`)
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing at all", stdout)
	}
	if stderr == "" {
		t.Error("stderr is empty; a misuse should say so somewhere a human can find it")
	}
}

// runAgyToolHook drives the subcommand exactly as agy does -- a payload
// on stdin, the reply on stdout -- and returns both of the streams it
// wrote. os.Pipe rather than a buffer because the subcommand reads and
// writes the process's own file descriptors, which is the thing under
// test; the same swap cmd/grain's other output tests make
// (schemaversion_test.go).
func runAgyToolHook(t *testing.T, args []string, payload string) (stdout, stderr string) {
	t.Helper()

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	// Written and closed before the hook runs: a payload this small fits
	// in the pipe buffer, so nothing here can block on a reader.
	if _, err := io.WriteString(inW, payload); err != nil {
		t.Fatalf("writing the payload: %v", err)
	}
	inW.Close()

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = inR, outW, errW
	agyToolHook(args)
	os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr

	outW.Close()
	errW.Close()
	inR.Close()

	out, err := io.ReadAll(outR)
	if err != nil {
		t.Fatalf("reading the hook's stdout: %v", err)
	}
	errOut, err := io.ReadAll(errR)
	if err != nil {
		t.Fatalf("reading the hook's stderr: %v", err)
	}
	return string(out), string(errOut)
}

// The subcommand as a host-tool grant's run's hooks.json starts it, with
// a made-up grant registered the way a linked package registers its own
// from init(): the allowlisted host tool goes through (no output) and
// lands in the record, and run_command is still denied. The real grant
// this binary links is tested where it lives (google/capability).
func TestAgyToolHookWithAHostToolGrant(t *testing.T) {
	t.Cleanup(antigravity.RegisterHostToolGrant(antigravity.HostToolGrant{
		Capability: "made-up-lookup", Tools: []string{"wiki_search"},
	}))
	record := filepath.Join(t.TempDir(), "host-tool-calls.jsonl")
	args := []string{"-grant", "made-up-lookup", "-record", record, "-task", "7", "-run", "r"}

	if stdout, stderr := runAgyToolHook(t, args, `{"toolCall":{"name":"wiki_search","args":{"query":"depot auth"}}}`); stdout != "" || stderr != "" {
		t.Errorf("wiki_search: stdout %q, stderr %q, want neither", stdout, stderr)
	}
	if stdout, _ := runAgyToolHook(t, args, `{"toolCall":{"name":"run_command","args":{"CommandLine":"id"}}}`); !strings.Contains(stdout, `"deny"`) {
		t.Errorf("run_command: stdout %q, want a deny", stdout)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("reading the record: %v", err)
	}
	if got := strings.Count(string(data), "\n"); got != 1 || !strings.Contains(string(data), `"query":"depot auth"`) {
		t.Errorf("record = %s, want the one wiki_search call with its query", data)
	}
}
