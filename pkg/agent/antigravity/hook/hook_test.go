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

package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func payload(t *testing.T, name string, args map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"toolCall": map[string]any{"name": name, "args": args}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func verdict(t *testing.T, reply []byte) string {
	t.Helper()
	if len(reply) == 0 {
		return "no opinion"
	}
	var r struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(reply, &r); err != nil {
		t.Fatalf("reply %q: %v", reply, err)
	}
	return r.Decision
}

func TestDecision(t *testing.T) {
	for name, want := range map[string]string{
		"mcp_grain-sandbox_run_command":      "no opinion",
		"mcp_grain-sandbox_comment_on_issue": "no opinion",
		"exit_plan_mode":                     "no opinion",
		"run_command":                        "deny",
		"enter_plan_mode":                    "deny",
		"schedule":                           "deny",
		"mcp_grain-sandbox_not_a_tool":       "deny",
		"":                                   "deny",
	} {
		if got := verdict(t, Decision(payload(t, name, nil))); got != want {
			t.Errorf("Decision(%q) = %s, want %s", name, got, want)
		}
	}
	if got := verdict(t, Decision([]byte("not json"))); got != "deny" {
		t.Errorf("Decision(malformed) = %s, want deny", got)
	}
}

func TestCapabilityToolsAreAdmittedByArgument(t *testing.T) {
	opts, ignored := ParseArgs([]string{FlagCapabilityTool, "acme/lookup", "-bogus"})
	if len(ignored) != 1 || ignored[0] != "-bogus" {
		t.Errorf("ParseArgs ignored %q, want [-bogus]", ignored)
	}
	if got := verdict(t, DecisionFor(opts, payload(t, "mcp_acme_lookup", nil))); got != "no opinion" {
		t.Errorf("a named capability tool: %s, want no opinion", got)
	}
	if got := verdict(t, DecisionFor(opts, payload(t, "mcp_acme_other", nil))); got != "deny" {
		t.Errorf("an unnamed capability tool: %s, want deny", got)
	}
}

// TestHostToolGrantWithoutACanonicalizer is the hook as grain-agentd runs
// it: no pkg/model, so no canonicalizer, and the grant matched as the
// daemon wrote it into the hook's arguments -- the registered Capability.
func TestHostToolGrantWithoutACanonicalizer(t *testing.T) {
	const grant = "https://grain.example/capabilities/made-up#lookup={}"
	t.Cleanup(RegisterHostToolGrant(HostToolGrant{Capability: grant, Tools: []string{"wiki_search"}}))
	record := filepath.Join(t.TempDir(), "calls", HostToolCallsFilename)

	opts, ignored := ParseArgs([]string{FlagGrant, grant, FlagRecord, record, FlagTask, "7", FlagRun, "r1"})
	if len(ignored) != 0 {
		t.Fatalf("ParseArgs ignored %q", ignored)
	}
	if got := verdict(t, DecisionFor(opts, payload(t, "wiki_search", map[string]any{"query": "q"}))); got != "no opinion" {
		t.Fatalf("a held host tool: %s, want no opinion", got)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var call HostToolCall
	if err := json.Unmarshal(bytes.TrimSpace(data), &call); err != nil || call.Tool != "wiki_search" || call.Task != "7" || call.Run != "r1" || call.Query != "q" {
		t.Errorf("recorded %q (%v)", data, err)
	}
	// The method's bare reference holds it too: matched by method.
	if held := HeldHostToolGrants([]string{"https://grain.example/capabilities/made-up#lookup"}); len(held) != 1 {
		t.Errorf("HeldHostToolGrants(bare ref) = %v, want the grant", held)
	}
	// Without the grant, the host tool is denied.
	if got := verdict(t, DecisionFor(Options{RecordPath: record}, payload(t, "wiki_search", nil))); got != "deny" {
		t.Errorf("a host tool without the grant: %s, want deny", got)
	}
}

func TestSetGrantCanonicalizer(t *testing.T) {
	const grant = "https://grain.example/capabilities/made-up#alias={}"
	t.Cleanup(RegisterHostToolGrant(HostToolGrant{Capability: grant, Tools: []string{"wiki_alias"}}))
	if held := HeldHostToolGrants([]string{"made-up-alias"}); len(held) != 0 {
		t.Fatalf("a retired name held %v with no canonicalizer", held)
	}
	SetGrantCanonicalizer(func(s string) string {
		if s == "made-up-alias" {
			return grant
		}
		return s
	})
	t.Cleanup(func() { SetGrantCanonicalizer(nil) })
	if held := HeldHostToolGrants([]string{"made-up-alias"}); len(held) != 1 {
		t.Errorf("a retired name, canonicalized, held %v", held)
	}
}

func TestRun(t *testing.T) {
	var out, errOut bytes.Buffer
	Run([]string{"-unknown"}, bytes.NewReader(payload(t, "run_command", nil)), &out, &errOut)
	if verdict(t, out.Bytes()) != "deny" {
		t.Errorf("Run wrote %q, want a deny", out.String())
	}
	if !strings.Contains(errOut.String(), "ignoring unrecognized arguments") {
		t.Errorf("Run's stderr = %q, want the ignored argument named", errOut.String())
	}
	out.Reset()
	Run(nil, bytes.NewReader(payload(t, "mcp_grain-sandbox_read_file", nil)), &out, &errOut)
	if out.Len() != 0 {
		t.Errorf("Run wrote %q for a grain tool, want nothing", out.String())
	}
}
