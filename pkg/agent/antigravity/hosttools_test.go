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
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A made-up host-tool grant, so that the seam is tested without any real
// environment's: it admits two lookups, run as the host's "lookup
// identity", and the fix for a lapsed credential is `renew-lookups`.
const madeUpGrant = "made-up-lookup"

var madeUpTools = []string{"wiki_search", "doc_lookup"}

const madeUpPreamble = " The exceptions are wiki_search and doc_lookup, which grain records every call to."

func madeUpHint(host string) string { return "lookup credential lapsed: run renew-lookups on " + host }

// registerMadeUpGrant registers the made-up grant until t ends. Tests
// that call it must not be parallel: another test's run would see it.
func registerMadeUpGrant(t *testing.T) {
	t.Helper()
	t.Cleanup(RegisterHostToolGrant(HostToolGrant{
		Capability:    madeUpGrant,
		Tools:         madeUpTools,
		Preamble:      madeUpPreamble,
		HostIdentity:  "lookup identity",
		IsAuthFailure: func(text string) bool { return strings.Contains(text, "renew-lookups") },
		AuthHint:      madeUpHint,
	}))
}

func hookPayload(t *testing.T, name string, args map[string]any) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"toolCall": map[string]any{"name": name, "args": args}})
	if err != nil {
		t.Fatalf("building payload: %v", err)
	}
	return payload
}

func hookVerdict(t *testing.T, reply []byte) string {
	t.Helper()
	if len(reply) == 0 {
		return "no opinion"
	}
	var out struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(reply, &out); err != nil {
		t.Fatalf("hook reply was not JSON: %v (%q)", err, reply)
	}
	return out.Decision
}

// Without the grant, a registered grant's tools are denied like any other
// host tool, and nothing is recorded anywhere.
func TestHookDeniesHostToolsWithoutTheGrant(t *testing.T) {
	registerMadeUpGrant(t)
	record := filepath.Join(t.TempDir(), hostToolCallsFilename)
	for _, opts := range []HookOptions{{}, {RecordPath: record, Grants: []string{"self-debug"}}} {
		for _, tool := range madeUpTools {
			reply := HookDecisionFor(opts, hookPayload(t, tool, map[string]any{"query": "q"}))
			if got := hookVerdict(t, reply); got != "deny" {
				t.Errorf("%s with grants %v: decision = %q, want deny", tool, opts.Grants, got)
			}
			if !strings.Contains(string(reply), "not one of grain's sandbox-bound MCP tools") {
				t.Errorf("%s: reason = %s, want today's denial text", tool, reply)
			}
		}
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Errorf("a denied call left a record behind (stat err %v)", err)
	}
}

// With the grant, the hook command carries it, each of its tools is let
// through and recorded with its task, run, tool and query, and a host
// tool off its list is still denied.
func TestHookAllowsAndRecordsAGrantsHostTools(t *testing.T) {
	registerMadeUpGrant(t)
	record := filepath.Join(t.TempDir(), "grain-run", hostToolCallsFilename)
	args := hookArgs(agent.RunConfig{Grants: []string{madeUpGrant}, TaskID: "3720", RunID: "run-1"},
		filepath.Dir(filepath.Dir(record)))
	if want := []string{"-grant", madeUpGrant, "-record", record, "-task", "3720", "-run", "run-1"}; !slices.Equal(args, want) {
		t.Fatalf("hookArgs = %q, want %q", args, want)
	}
	opts, ignored := ParseHookArgs(args)
	if len(ignored) != 0 {
		t.Fatalf("ParseHookArgs ignored %q of what hookArgs wrote", ignored)
	}

	for _, tool := range madeUpTools {
		reply := HookDecisionFor(opts, hookPayload(t, tool, map[string]any{"query": "about " + tool}))
		if got := hookVerdict(t, reply); got != "no opinion" {
			t.Errorf("%s with the grant: reply = %s, want no opinion (allowed)", tool, reply)
		}
	}
	for _, tool := range []string{"run_command", "write_to_file", "wiki_search_and_write"} {
		if got := hookVerdict(t, HookDecisionFor(opts, hookPayload(t, tool, nil))); got != "deny" {
			t.Errorf("%s with the grant: decision = %q, want deny", tool, got)
		}
	}
	if got := hookVerdict(t, HookDecisionFor(opts, hookPayload(t, mcp.AgyQualifiedToolName("run_command"), nil))); got != "no opinion" {
		t.Errorf("grain's own run_command with the grant: decision = %q, want no opinion", got)
	}

	calls := loadHostToolCalls(record)
	if len(calls) != len(madeUpTools) {
		t.Fatalf("recorded %d calls, want %d: %+v", len(calls), len(madeUpTools), calls)
	}
	for i, tool := range madeUpTools {
		c := calls[i]
		if c.Tool != tool || c.Task != "3720" || c.Run != "run-1" || c.Query != "about "+tool || c.At.IsZero() {
			t.Errorf("record %d = %+v, want tool %s for task 3720, run run-1, with its query", i, c, tool)
		}
	}
}

// A grant name the binary has no registration for admits nothing, and no
// argument names a tool into a registered grant's allowlist.
func TestHookArgsCannotWidenAnAllowlist(t *testing.T) {
	registerMadeUpGrant(t)
	opts, _ := ParseHookArgs([]string{"-grant", madeUpGrant, "-grant", "run_command", "-grant", "unregistered",
		"-record", filepath.Join(t.TempDir(), "r.jsonl")})
	for _, tool := range []string{"run_command", "unregistered"} {
		if got := hookVerdict(t, HookDecisionFor(opts, hookPayload(t, tool, nil))); got != "deny" {
			t.Errorf("%s named as a grant: decision = %q, want deny", tool, got)
		}
	}
	if args := hookArgs(agent.RunConfig{Grants: []string{"unregistered"}}, "/h"); args != nil {
		t.Errorf("hookArgs for an unregistered grant = %q, want none", args)
	}
}

// An allowlisted call grain cannot record is denied, naming its grant:
// host-side calls must never go unrecorded.
func TestHookDeniesAnAllowedCallItCannotRecord(t *testing.T) {
	registerMadeUpGrant(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", filepath.Join(blocker, "under-a-file.jsonl")} {
		opts := HookOptions{Grants: []string{madeUpGrant}, RecordPath: path}
		reply := HookDecisionFor(opts, hookPayload(t, "wiki_search", map[string]any{"query": "q"}))
		if got := hookVerdict(t, reply); got != "deny" {
			t.Errorf("record path %q: decision = %q, want deny", path, got)
		}
		if !strings.Contains(string(reply), "this task's "+madeUpGrant+" grant") {
			t.Errorf("record path %q: reason = %s, want it to name the grant", path, reply)
		}
	}
}

// A run without the grant writes the hooks.json it always has; one with it
// carries the grant into the hook's own command.
func TestHookConfigCarriesTheGrantOnlyWhenHeld(t *testing.T) {
	registerMadeUpGrant(t)
	plain, err := agyHomeFiles("/bin/grain", []string{"mcpserver"}, "", false, hookArgs(agent.RunConfig{TaskID: "1"}, "/h")...)
	if err != nil {
		t.Fatal(err)
	}
	today, err := hookConfigJSON("/bin/grain")
	if err != nil {
		t.Fatal(err)
	}
	if string(plain[hooksConfigRelPath]) != string(today) {
		t.Errorf("hooks.json without the grant = %s, want today's %s", plain[hooksConfigRelPath], today)
	}
	granted, err := agyHomeFiles("/bin/grain", []string{"mcpserver"}, "", false,
		hookArgs(agent.RunConfig{TaskID: "1", Grants: []string{madeUpGrant}}, "/h")...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(granted[hooksConfigRelPath]), "'-grant' '"+madeUpGrant+"'") {
		t.Errorf("hooks.json with the grant = %s, want the grant in the hook command", granted[hooksConfigRelPath])
	}
}

// The preamble carries a grant's own words only for a run holding it,
// and after the explanation of why the built-ins are unavailable, never
// inside it: the walkthrough in
// docs/test-runs/2026-10-15-corp-research-capability.md found an
// exception spliced into the list of built-ins, so the very next sentence
// -- "They do not run in your sandbox ... grain does not see the calls
// ... unrecorded" -- read as being about the tools it had just admitted.
func TestToolPreambleCarriesAGrantsWordsAfterTheBuiltIns(t *testing.T) {
	registerMadeUpGrant(t)
	plain := toolPreamble(agent.RunConfig{})
	if strings.Contains(plain, madeUpPreamble) {
		t.Error("preamble without the grant carries the grant's words")
	}
	if plain != toolPreamble(agent.RunConfig{Grants: []string{"self-debug"}}) {
		t.Error("an unrelated grant changed the preamble")
	}
	cfg := agent.RunConfig{Grants: []string{madeUpGrant}}
	granted := toolPreamble(cfg)
	if hostToolException(cfg) != madeUpPreamble {
		t.Errorf("hostToolException = %q, want the grant's own %q", hostToolException(cfg), madeUpPreamble)
	}
	if got := strings.Replace(granted, madeUpPreamble, "", 1); got != plain {
		t.Errorf("preamble with the grant, less its words, differs from the plain one:\n got %q\nwant %q", got, plain)
	}
	collision := strings.Index(granted, "does what you were asked to do.")
	if collision < 0 {
		t.Fatalf("preamble lost the sentence this test anchors on: %q", granted)
	}
	if i := strings.Index(granted, madeUpPreamble); i < collision {
		t.Errorf("the grant's words are at %d, before the built-ins' explanation ends at %d", i, collision)
	}
}

// Recorded calls reach the daemon log and the tool census even when the
// transcript lost them, the note names the grant and what its calls ran
// as, and an auth failure carries the grant's hint.
func TestReportHostToolCalls(t *testing.T) {
	registerMadeUpGrant(t)
	r := &agent.Result{ToolCalls: []agent.ToolCall{
		{Name: "wiki_search", IsError: true, Text: "credential lapsed: please renew-lookups"},
	}}
	calls := []HostToolCall{
		{Tool: "wiki_search", Query: "a", Host: "ctl-1"},
		{Tool: "doc_lookup", Query: "b", Host: "ctl-1"},
	}
	notes := reportHostToolCalls(r, calls, "fallback")
	if len(r.ToolCalls) != 2 || r.ToolCalls[1].Name != "doc_lookup" {
		t.Errorf("tool calls = %+v, want the doc_lookup the transcript missed added", r.ToolCalls)
	}
	want := []string{
		"grain: 2 host tool call(s) made under the " + madeUpGrant + " grant, on ctl-1, as that host's lookup identity",
		"grain: wiki_search failed on auth -- " + madeUpHint("ctl-1"),
	}
	if !slices.Equal(notes, want) {
		t.Errorf("notes = %q, want %q", notes, want)
	}
	if notes := reportHostToolCalls(&agent.Result{}, nil, "h"); len(notes) != 0 {
		t.Errorf("a run with no host calls got notes %q", notes)
	}
}

func TestRegisterHostToolGrantRefusesADuplicate(t *testing.T) {
	registerMadeUpGrant(t)
	defer func() {
		if recover() == nil {
			t.Error("registering the same grant twice did not panic")
		}
	}()
	RegisterHostToolGrant(HostToolGrant{Capability: madeUpGrant, Tools: []string{"x"}})
}

// A host-tool grant registered for a capability server method is held by a
// run holding that method -- recorded, bare, or under the retired bare name
// that is an alias of it -- and one registered under the retired bare name
// is held by a run holding the method; in each case the hook command carries
// the grant as registered and the hook reads it back to admit the tools.
func TestHostToolGrantIsMatchedByMethod(t *testing.T) {
	const (
		ref   = "https://grain.example/capabilities/made-up#lookup"
		grant = ref + "={}"
	)
	t.Cleanup(model.RegisterLegacyGrantAlias(madeUpGrant, grant))
	for _, registered := range []string{grant, madeUpGrant} {
		t.Run(registered, func(t *testing.T) {
			t.Cleanup(RegisterHostToolGrant(HostToolGrant{Capability: registered, Tools: madeUpTools}))
			for _, held := range []string{grant, ref, madeUpGrant} {
				args := hookArgs(agent.RunConfig{Grants: []string{"self-debug", held}}, "/h")
				if len(args) < 2 || args[0] != "-grant" || args[1] != registered {
					t.Errorf("holding %s: hookArgs = %q, want -grant %s", held, args, registered)
					continue
				}
				opts, ignored := ParseHookArgs(args)
				if len(ignored) != 0 {
					t.Fatalf("ParseHookArgs ignored %q", ignored)
				}
				if _, ok := opts.allowsHostTool(madeUpTools[0]); !ok {
					t.Errorf("holding %s: the hook does not admit %s from %q", held, madeUpTools[0], args)
				}
			}
			if args := hookArgs(agent.RunConfig{Grants: []string{"https://grain.example/capabilities/made-up#other={}"}}, "/h"); args != nil {
				t.Errorf("another method of the same server opened the host tools: %q", args)
			}
		})
	}
}
