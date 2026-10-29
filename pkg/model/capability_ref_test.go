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

package model

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

const testGCP = "https://github.com/bwsalmon/grain/capabilities/gcp"

// listCaller advertises one releasable method, gcp-key, whose account
// argument defaults to staging.
type listCaller struct{}

func (listCaller) ListTools(context.Context) ([]protocol.Tool, error) {
	args := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"account": map[string]any{"type": "string", "default": "staging"},
		},
	}
	return []protocol.Tool{
		{Name: "mint_gcp-key", InputSchema: map[string]any{
			"type": "object", "required": []any{"attempt_id", "deadline", "args"},
			"properties": map[string]any{
				"attempt_id": map[string]any{"type": "string"},
				"deadline":   map[string]any{"type": "string"},
				"args":       args,
			},
		}},
		{Name: "release_gcp-key", InputSchema: map[string]any{
			"type": "object", "required": []any{"attempt_id"},
			"properties": map[string]any{"attempt_id": map[string]any{"type": "string"}},
		}},
	}, nil
}

func (listCaller) CallTool(context.Context, string, map[string]any) ([]byte, error) {
	return nil, errors.New("not used")
}

func (listCaller) Close() error { return nil }

func testLookup(t *testing.T) protocol.Lookup {
	t.Helper()
	srv, err := protocol.NewServer(context.Background(), testGCP, listCaller{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return func(id string) (*protocol.Server, error) {
		if id != testGCP {
			return nil, errors.New("capability " + id + " is not configured in mcpServers")
		}
		return srv, nil
	}
}

func TestNormalizeCapabilityNamesRecordsResolvedRefsWithDefaults(t *testing.T) {
	lookup := testLookup(t)
	got, err := NormalizeCapabilityNames([]string{
		"self-debug",
		testGCP, // shorthand: the only method
		testGCP + `#gcp-key={"account":"staging"}`, // equal once defaulted
		testGCP + `#gcp-key={"account":"prod"}`,
	}, lookup)
	if err != nil {
		t.Fatalf("NormalizeCapabilityNames: %v", err)
	}
	want := []string{
		"self-debug",
		testGCP + `#gcp-key={"account":"staging"}`,
		testGCP + `#gcp-key={"account":"prod"}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NormalizeCapabilityNames = %q, want %q", got, want)
	}

	if _, err := NormalizeCapabilityNames([]string{testGCP + `={"region":"eu"}`}, lookup); err == nil {
		t.Error("an argument the schema does not allow was accepted")
	}
	if _, err := NormalizeCapabilityNames([]string{"https://example.com/caps/unmapped"}, lookup); err == nil ||
		!strings.Contains(err.Error(), "not configured") {
		t.Errorf("an unmapped reference: error = %v, want it refused as not configured", err)
	}
	// Without servers only the syntax is checked, and the grant is kept
	// as written.
	if got, err := NormalizeCapabilityNames([]string{testGCP + `={"account":"x"}`}, nil); err != nil || got[0] != testGCP+`={"account":"x"}` {
		t.Errorf("syntax-only normalize = %q, %v", got, err)
	}
	if _, err := NormalizeCapabilityNames([]string{testGCP + `={not json`}, nil); err == nil {
		t.Error("a malformed argument object was accepted")
	}
}

func TestCapabilityListDecodesNamesRefsAndGrantObjects(t *testing.T) {
	var doc struct {
		Capabilities CapabilityList `yaml:"capabilities"`
	}
	src := `capabilities:
  - self-debug
  - ` + testGCP + `
  - capability: ` + testGCP + `#gcp-key
    with:
      account: infra-staging
`
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	want := CapabilityList{"self-debug", testGCP, testGCP + `#gcp-key={"account":"infra-staging"}`}
	if !reflect.DeepEqual(doc.Capabilities, want) {
		t.Errorf("decoded %q, want %q", doc.Capabilities, want)
	}
	for _, bad := range []string{
		"capabilities:\n  - capability: " + testGCP + "\n    extra: 1\n",
		"capabilities:\n  - capability: self-debug\n    with: {a: 1}\n",
	} {
		if err := yaml.Unmarshal([]byte(bad), &doc); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}

	got, err := CapabilityEntries([]any{"gcp-key", map[string]any{"capability": testGCP, "with": map[string]any{"account": "a"}}})
	if err != nil || !reflect.DeepEqual(got, []string{"gcp-key", testGCP + `={"account":"a"}`}) {
		t.Errorf("CapabilityEntries = %q, %v", got, err)
	}
}

func TestCapabilityListsWithCommasRoundTrip(t *testing.T) {
	for _, list := range [][]string{
		nil,
		{"gcp-key", "self-debug"},
		{"gcp-key", testGCP + `#gcp-key={"a":"1","b":"2"}`},
	} {
		if got := splitCapabilities(joinCapabilities(list)); !reflect.DeepEqual(got, list) {
			t.Errorf("round trip of %q = %q", list, got)
		}
	}
}

func TestReferenceContainmentComparesRecordedArgs(t *testing.T) {
	staging := testGCP + `#gcp-key={"account":"staging"}`
	prod := testGCP + `#gcp-key={"account":"prod"}`
	parent := Task{ID: "1", Grants: []Grant{{Capability: "self-debug"}, {Capability: staging}}}

	covered := Task{Grants: []Grant{{Capability: staging, Via: GrantByLabel}}}
	if !GrantsSubsetOf(covered.Grants, parent.Grants) || len(EscalatedCapabilities(covered, parent)) != 0 {
		t.Errorf("a child holding its parent's recorded grant was treated as escalated")
	}
	other := Task{Grants: []Grant{{Capability: prod, Via: GrantByDefault}}}
	if GrantsSubsetOf(other.Grants, parent.Grants) {
		t.Error("a grant with other args was covered")
	}
	if got := EscalatedCapabilities(other, parent); !reflect.DeepEqual(got, []string{prod}) {
		t.Errorf("EscalatedCapabilities = %q, want %q even for a default", got, prod)
	}
	// A reference never normalized cannot be shown covered.
	raw := Task{Grants: []Grant{{Capability: testGCP, Via: GrantByLabel}}}
	if GrantsSubsetOf(raw.Grants, parent.Grants) {
		t.Error("an unnormalized reference was covered")
	}
}

func TestCapabilityPolicyDeniesUntrustedByDefault(t *testing.T) {
	g := testGCP + `#gcp-key={"account":"staging"}`
	acme := &RepoRef{Owner: "acme", Name: "widgets"}
	var none CapabilityPolicy
	if err := none.Permits(g, CapabilitySubject{Repo: acme, Kind: CapabilityRunTask}); err != nil {
		t.Errorf("a trusted task with no rules was refused: %v", err)
	}
	if err := none.Permits(g, CapabilitySubject{Repo: acme, Kind: CapabilityRunCI, Event: "pull_request", Untrusted: true}); err == nil {
		t.Error("an untrusted pull_request was granted a reference no rule opts in")
	}
	if err := none.Permits("gcp-key", CapabilitySubject{Untrusted: true}); err != nil {
		t.Errorf("an in-process name is not this policy's to judge: %v", err)
	}

	p := CapabilityPolicy{Rules: []CapabilityPolicyRule{
		{Capability: testGCP + "#gcp-key", Repos: []string{"acme/*"}, RunKinds: []string{CapabilityRunTask}},
		{Capability: testGCP + "#*", Events: []string{"pull_request"}, RunKinds: []string{CapabilityRunCI}, AllowUntrusted: true},
	}}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := p.Permits(g, CapabilitySubject{Repo: acme, Kind: CapabilityRunTask}); err != nil {
		t.Errorf("an admitted task was refused: %v", err)
	}
	if err := p.Permits(g, CapabilitySubject{Repo: &RepoRef{Owner: "other", Name: "x"}, Kind: CapabilityRunTask}); err == nil {
		t.Error("a repository no rule admits was granted a named reference")
	}
	if err := p.Permits(g, CapabilitySubject{Repo: acme, Kind: CapabilityRunCI, Event: "pull_request", Untrusted: true}); err != nil {
		t.Errorf("an opted-in untrusted event was refused: %v", err)
	}
	if err := (CapabilityPolicy{Rules: []CapabilityPolicyRule{{Capability: "gcp-key"}}}).Validate(); err == nil {
		t.Error("a rule naming no reference validated")
	}
	text, err := EncodeCapabilityPolicy(p)
	if err != nil {
		t.Fatal(err)
	}
	if back, err := DecodeCapabilityPolicy(text); err != nil || !reflect.DeepEqual(back, p) {
		t.Errorf("policy round trip = %+v, %v", back, err)
	}
}

func TestCapabilityPolicyDenyRuleRefusesWhatItCovers(t *testing.T) {
	g := testGCP + `#gcp-key={"account":"staging"}`
	acme := &RepoRef{Owner: "acme", Name: "widgets"}
	p := CapabilityPolicy{Rules: []CapabilityPolicyRule{
		{Capability: testGCP + "#*", RunKinds: []string{CapabilityRunCI}, Deny: true},
		{Capability: testGCP + "#gcp-key", AllowUntrusted: true},
	}}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := p.Permits(g, CapabilitySubject{Repo: acme, Kind: CapabilityRunCI, Event: "push"}); err == nil {
		t.Error("a CI run a deny rule covers was granted the reference an allow rule also names")
	}
	if err := p.Permits(g, CapabilitySubject{Repo: acme, Kind: CapabilityRunTask}); err != nil {
		t.Errorf("a task the deny rule does not cover was refused: %v", err)
	}
	onlyDeny := CapabilityPolicy{Rules: []CapabilityPolicyRule{{Capability: testGCP + "#gcp-key", Events: []string{"schedule"}, Deny: true}}}
	if err := onlyDeny.Permits(g, CapabilitySubject{Repo: acme, Kind: CapabilityRunCI, Event: "push"}); err != nil {
		t.Errorf("a deny rule restricted a run it does not cover: %v", err)
	}
	if err := onlyDeny.Permits(g, CapabilitySubject{Repo: acme, Kind: CapabilityRunCI, Event: "schedule"}); err == nil {
		t.Error("a scheduled run the deny rule covers was granted")
	}
}

func TestCapabilityPolicyAppliesAcrossAdoptedServerAliases(t *testing.T) {
	const canonical = "https://grain.dev/capabilities/gcp"
	servers := map[string]protocol.ServerConfig{
		canonical: {Command: "grain", Args: []string{"capability", "gcp"}, Builtin: "gcp", Aliases: []string{testGCP}},
	}
	acme := &RepoRef{Owner: "acme", Name: "widgets"}
	other := &RepoRef{Owner: "other", Name: "x"}

	// A rule written for the hand-added id before adoption still restricts
	// and denies grants naming the canonical id after adoption.
	oldAllow := CapabilityPolicy{Rules: []CapabilityPolicyRule{{Capability: testGCP + "#gcp-key", Repos: []string{"acme/*"}}}}
	if err := oldAllow.Permits(canonical+"#gcp-key", CapabilitySubject{Repo: acme, Kind: CapabilityRunTask}, servers); err != nil {
		t.Errorf("allowed repo refused: %v", err)
	}
	if err := oldAllow.Permits(canonical+"#gcp-key", CapabilitySubject{Repo: other, Kind: CapabilityRunTask}, servers); err == nil {
		t.Error("a repo excluded by a rule naming the adopted alias was granted the canonical id")
	}
	oldDeny := CapabilityPolicy{Rules: []CapabilityPolicyRule{{Capability: testGCP + "#*", RunKinds: []string{CapabilityRunCI}, Deny: true}}}
	if err := oldDeny.Permits(canonical+"#gcp-key", CapabilitySubject{Repo: acme, Kind: CapabilityRunCI, Event: "push"}, servers); err == nil {
		t.Error("a CI run denied by a rule naming the adopted alias was granted the canonical id")
	}

	// Conversely, a rule written for the canonical id also governs grants
	// that still name the alias.
	newAllow := CapabilityPolicy{Rules: []CapabilityPolicyRule{{Capability: canonical + "#gcp-key", Repos: []string{"acme/*"}}}}
	if err := newAllow.Permits(testGCP+"#gcp-key", CapabilitySubject{Repo: other, Kind: CapabilityRunTask}, servers); err == nil {
		t.Error("a repo excluded by a rule naming the canonical id was granted the alias")
	}
}

func TestCapabilityPolicyVerdictsByRunKind(t *testing.T) {
	ref := testGCP + "#gcp-key"
	if got := (CapabilityPolicy{}).Verdicts(ref); got != nil {
		t.Errorf("empty policy verdicts = %v, want nil", got)
	}
	denyTasks := CapabilityPolicy{Rules: []CapabilityPolicyRule{
		{Capability: testGCP + "#*", RunKinds: []string{CapabilityRunTask}, Deny: true},
	}}
	want := map[string]CapabilityPolicyVerdict{CapabilityRunTask: CapabilityPolicyDenied}
	if got := denyTasks.Verdicts(ref); !reflect.DeepEqual(got, want) {
		t.Errorf("denyTasks.Verdicts = %v, want %v", got, want)
	}
	allowOnlyCI := CapabilityPolicy{Rules: []CapabilityPolicyRule{
		{Capability: testGCP + "#*", RunKinds: []string{CapabilityRunCI}},
	}}
	wantAllowCI := map[string]CapabilityPolicyVerdict{
		CapabilityRunTask:    CapabilityPolicyDenied,
		CapabilityRunProcess: CapabilityPolicyDenied,
	}
	if got := allowOnlyCI.Verdicts(ref); !reflect.DeepEqual(got, wantAllowCI) {
		t.Errorf("allowOnlyCI.Verdicts = %v, want %v", got, wantAllowCI)
	}
	repoDependent := CapabilityPolicy{Rules: []CapabilityPolicyRule{
		{Capability: testGCP + "#*", RunKinds: []string{CapabilityRunTask}, Repos: []string{"acme/*"}, Deny: true},
		{Capability: testGCP + "#*", Events: []string{"schedule"}, Deny: true},
	}}
	wantRepoDependent := map[string]CapabilityPolicyVerdict{
		CapabilityRunTask: CapabilityPolicyMayBeRefused,
		CapabilityRunCI:   CapabilityPolicyMayBeRefused,
	}
	if got := repoDependent.Verdicts(ref); !reflect.DeepEqual(got, wantRepoDependent) {
		t.Errorf("repoDependent.Verdicts = %v, want %v", got, wantRepoDependent)
	}
}
