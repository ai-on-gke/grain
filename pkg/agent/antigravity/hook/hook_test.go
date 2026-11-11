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

	"github.com/ai-on-gke/grain/pkg/agent/hookpolicy"
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

func TestPolicyFileReplacesCompiledInRosterAndGrants(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, HostToolCallsFilename)
	policyPath := filepath.Join(dir, hookpolicy.Filename)
	raw, err := hookpolicy.Marshal(hookpolicy.File{
		Version: hookpolicy.Version,
		Tool: &hookpolicy.ToolPolicy{
			AllowedTools:             []string{"mcp_grain-sandbox_future_tool", "exit_plan_mode"},
			AllowedPatterns:          []string{"mcp_wildcard_*"},
			CapabilityToolPrefix:     "mcp_",
			CapabilityServerPrefixes: []string{"mcp_open_server_"},
			HostToolGrants: []hookpolicy.HostToolGrant{
				// Never registered via RegisterHostToolGrant in this binary.
				{Capability: "https://grain.example/capabilities/unlinked#search={}", Tools: []string{"unlinked_search"}},
			},
			RecordPath: record,
			TaskID:     "42",
			RunID:      "run-9",
			DenyReasons: map[string]string{
				"custom_trap": "do not call custom_trap in headless mode",
			},
			DenyPatterns: []hookpolicy.DenyPattern{
				{Pattern: "legacy_*", Reason: "legacy tools are retired"},
			},
			DefaultDenySuffix: "is blocked by staged policy.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	opts, ignored := ParseArgs([]string{FlagPolicy, policyPath, FlagCapabilityTool, "cap/read_item"})
	if len(ignored) != 0 {
		t.Fatalf("ParseArgs ignored %q", ignored)
	}

	// Tools in the staged policy are admitted even though PublishedTools knows none of them.
	for _, allowed := range []string{
		"mcp_grain-sandbox_future_tool",
		"mcp_wildcard_anything",
		"mcp_open_server_ping",
		"mcp_cap_read_item",
		"unlinked_search",
	} {
		if got := verdict(t, DecisionFor(opts, payload(t, allowed, map[string]any{"query": "q1"}))); got != "no opinion" {
			t.Errorf("DecisionFor(%q) = %s, want no opinion", allowed, got)
		}
	}

	// A tool in compiled-in PublishedTools that is absent from the staged policy is denied:
	// the hook evaluates only the staged data when the file is present.
	reply := DecisionFor(opts, payload(t, "mcp_grain-sandbox_run_command", nil))
	if got := verdict(t, reply); got != "deny" || !strings.Contains(string(reply), "is blocked by staged policy.") {
		t.Errorf("omitted compiled-in tool: reply = %s, want policy denial", reply)
	}

	// Custom exact and pattern deny reasons come from the policy file.
	trapReply := DecisionFor(opts, payload(t, "custom_trap", nil))
	if !strings.Contains(string(trapReply), "do not call custom_trap in headless mode") {
		t.Errorf("custom_trap reply = %s", trapReply)
	}
	patReply := DecisionFor(opts, payload(t, "legacy_exec", nil))
	if !strings.Contains(string(patReply), "legacy tools are retired") {
		t.Errorf("legacy_exec reply = %s", patReply)
	}

	// Unlinked host-tool grant was recorded to RecordPath from the policy file.
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var call HostToolCall
	if err := json.Unmarshal(bytes.TrimSpace(data), &call); err != nil || call.Tool != "unlinked_search" || call.Task != "42" || call.Run != "run-9" || call.Query != "q1" {
		t.Errorf("recorded call = %q (%v)", data, err)
	}

	// Corrupt policy file fails closed rather than falling open or falling back silently.
	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := verdict(t, DecisionFor(Options{PolicyPath: corrupt}, payload(t, "mcp_grain-sandbox_run_command", nil))); got != "deny" {
		t.Errorf("corrupt policy = %s, want deny", got)
	}

	// Absent policy file falls back to compiled-in PublishedTools.
	missing := filepath.Join(dir, "absent.json")
	if got := verdict(t, DecisionFor(Options{PolicyPath: missing}, payload(t, "mcp_grain-sandbox_run_command", nil))); got != "no opinion" {
		t.Errorf("absent policy fallback = %s, want no opinion", got)
	}
}

// A staged host-tool grant admits only the exact tool names it lists, as a
// compiled-in HostToolGrant always has: a '*' in one is a literal byte, not a
// pattern that opens every host tool behind it.
func TestStagedHostToolGrantsMatchExactly(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, HostToolCallsFilename)
	policyPath := filepath.Join(dir, hookpolicy.Filename)
	raw, err := hookpolicy.Marshal(hookpolicy.File{Tool: &hookpolicy.ToolPolicy{
		HostToolGrants: []hookpolicy.HostToolGrant{
			{Capability: "https://grain.example/capabilities/wide#search={}", Tools: []string{"host_*", "exact_lookup"}},
		},
		RecordPath: record,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	opts := Options{PolicyPath: policyPath}
	for _, name := range []string{"host_exec", "host_", "exact_lookup2", ""} {
		if got := verdict(t, DecisionFor(opts, payload(t, name, nil))); got != "deny" {
			t.Errorf("DecisionFor(%q) = %s, want deny", name, got)
		}
	}
	if got := verdict(t, DecisionFor(opts, payload(t, "exact_lookup", nil))); got != "no opinion" {
		t.Errorf("DecisionFor(exact_lookup) = %s, want no opinion", got)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.TrimSpace(string(data)), "\n") + 1; n != 1 || !strings.Contains(string(data), `"exact_lookup"`) {
		t.Errorf("recorded calls = %q, want exactly the one exact_lookup", data)
	}
}
