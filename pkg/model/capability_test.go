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
	"strings"
	"testing"
	"time"
)

// --- test capabilities -------------------------------------------------
//
// One provider per Provision, plus one that refuses and one that fails
// during Materialize, standing in for the real gcp-key/self-debug/
// grain-github-<name> providers docs/data-model.md describes -- none of
// which v2 can build yet, since minting a real credential needs a
// controller v2 does not have (see README.md). gemini-key is the one
// exception: capability/geminikey/ is a real MINT provider now, tested
// against its own fakes rather than these. These stand-ins are enough to
// exercise the contract on its own.

type mintCapability struct {
	BaseCapability
	refuse string // non-empty makes Resolve refuse with this reason
}

func (mintCapability) Spec() CapabilitySpec {
	return CapabilitySpec{
		Name: "test-mint", Label: "grain-test-mint",
		Source: GrantByLabel, Provision: ProvisionMint, MaxLease: 24 * time.Hour,
	}
}

func (c mintCapability) Resolve(ctx context.Context, cc CapabilityContext) (Resolution, error) {
	if c.refuse != "" {
		return RefusedBecause(c.refuse), nil
	}
	return Honoured(), nil
}

func (mintCapability) Materialize(ctx context.Context, cc CapabilityContext) (Materialization, error) {
	expires := cc.Now.Add(24 * time.Hour)
	return Materialization{
		Lease: &Lease{
			Capability: "test-mint", Resource: "minted-" + cc.Run.ID,
			MintedBy: CredentialRef{Name: "test-standing-credential"},
			IssuedAt: cc.Now, ExpiresAt: &expires,
		},
		Placements: []Placement{
			{Side: SideSandbox, Path: "/home/debian/.test-mint-key", Content: "sekret"},
		},
	}, nil
}

func (mintCapability) PromptSection(ctx context.Context, cc CapabilityContext, placements []Placement) (string, error) {
	if len(placements) != 1 {
		return "", errors.New("expected exactly one placement")
	}
	return "a test key is at " + placements[0].Path, nil
}

type grantCapability struct{ BaseCapability }

func (grantCapability) Spec() CapabilitySpec {
	return CapabilitySpec{
		Name: "test-grant", Label: "grain-test-grant",
		Source: GrantByLabel, Provision: ProvisionGrant,
	}
}

func (grantCapability) PromptSection(ctx context.Context, cc CapabilityContext, placements []Placement) (string, error) {
	return "you can do the test-only thing", nil
}

type selectCapability struct{ BaseCapability }

func (selectCapability) Spec() CapabilitySpec {
	return CapabilitySpec{
		Name: "test-select", Label: "grain-test-select",
		Source: GrantByLabel, Provision: ProvisionSelect,
	}
}

func (selectCapability) Materialize(ctx context.Context, cc CapabilityContext) (Materialization, error) {
	return Materialization{CredentialOverride: &CredentialRef{Name: "test-scoped-credential"}}, nil
}

type failingMaterializeCapability struct{ BaseCapability }

func (failingMaterializeCapability) Spec() CapabilitySpec {
	return CapabilitySpec{Name: "test-fails", Provision: ProvisionGrant, Source: GrantByLabel}
}

func (failingMaterializeCapability) Materialize(ctx context.Context, cc CapabilityContext) (Materialization, error) {
	return Materialization{}, errors.New("boom")
}

type limitedMaterializeCapability struct {
	BaseCapability
	attempts   int
	succeedOn  int
	retryAfter time.Duration
	times      []time.Time
}

func (c *limitedMaterializeCapability) Spec() CapabilitySpec {
	return CapabilitySpec{Name: "test-limited", Provision: ProvisionMint, Source: GrantByLabel}
}

func (*limitedMaterializeCapability) ScarceResourceProvider() {}

func (c *limitedMaterializeCapability) Materialize(ctx context.Context, cc CapabilityContext) (Materialization, error) {
	c.attempts++
	c.times = append(c.times, cc.Now)
	if c.attempts < c.succeedOn {
		return Materialization{}, &ResourceExhaustedError{
			Resource: "test keys", RetryAfter: c.retryAfter, Err: errors.New("none free"),
		}
	}
	return Materialization{Lease: &Lease{Capability: "test-limited", Resource: "key-1", IssuedAt: cc.Now}}, nil
}

// --- fixtures ------------------------------------------------------------

func capTask(grants ...string) Task {
	t := Task{
		ID: "cap-task", Intent: IntentImplement, Title: "t",
		Origin: Origin{
			Attribution: Attribution{Actor: Principal{PrincipalHuman, "someone"}},
			Reason:      ReasonDirect,
		},
		Binding: BindingDirective,
	}
	for _, g := range grants {
		t.Grants = append(t.Grants, Grant{Capability: g, Via: GrantByLabel})
	}
	return t
}

func capContext(t Task) CapabilityContext {
	return CapabilityContext{
		Task: t,
		Run:  Run{ID: "cap-task-1", TaskID: t.ID},
		Now:  time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC),
	}
}

// --- registry --------------------------------------------------------

func TestRegistryOrdersProvidersByRegistrationNotByName(t *testing.T) {
	reg := NewCapabilityRegistry(selectCapability{}, mintCapability{}, grantCapability{})
	var names []string
	for _, p := range reg.Providers() {
		names = append(names, p.Spec().Name)
	}
	want := []string{"test-select", "test-mint", "test-grant"}
	if len(names) != len(want) {
		t.Fatalf("Providers() = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("Providers() = %v, want %v", names, want)
		}
	}
}

func TestRegistryReregisteringReplacesInPlace(t *testing.T) {
	reg := NewCapabilityRegistry(mintCapability{}, grantCapability{})
	reg.Register(mintCapability{refuse: "now refuses"})

	names := make([]string, 0, 2)
	for _, p := range reg.Providers() {
		names = append(names, p.Spec().Name)
	}
	if got, want := names, []string{"test-mint", "test-grant"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("re-registering moved position: %v", got)
	}

	p, ok := reg.Lookup("test-mint")
	if !ok {
		t.Fatal("test-mint should still be registered")
	}
	res, err := p.Resolve(context.Background(), CapabilityContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refused {
		t.Fatal("the second registration should have replaced the first, so this should refuse")
	}
}

// --- resolve -----------------------------------------------------------

func TestResolveGrantsHonoursAndRefuses(t *testing.T) {
	reg := NewCapabilityRegistry(mintCapability{}, grantCapability{}, selectCapability{})
	task := capTask("test-grant", "test-mint", "test-select", "test-unregistered")
	resolved, err := ResolveGrants(context.Background(), reg, capContext(task))
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 4 {
		t.Fatalf("got %d resolutions, want 4: %+v", len(resolved), resolved)
	}

	// Registry order (mint, grant, select), then unregistered names
	// sorted -- not the order the grants were listed on the task.
	wantOrder := []string{"test-mint", "test-grant", "test-select", "test-unregistered"}
	for i, r := range resolved {
		if r.Grant.Capability != wantOrder[i] {
			t.Fatalf("resolution %d = %s, want %s", i, r.Grant.Capability, wantOrder[i])
		}
	}
	for _, r := range resolved[:3] {
		if r.Resolution.Refused {
			t.Errorf("%s should be honoured, got refused: %s", r.Grant.Capability, r.Resolution.Reason)
		}
	}
	last := resolved[3]
	if !last.Resolution.Refused || last.Resolution.Reason == "" {
		t.Errorf("test-unregistered should be refused with a human-facing reason, got %+v", last.Resolution)
	}
}

func TestResolveGrantsRefusalIsReported(t *testing.T) {
	reg := NewCapabilityRegistry(mintCapability{refuse: "this deployment has no test-mint config"})
	task := capTask("test-mint")
	resolved, err := ResolveGrants(context.Background(), reg, capContext(task))
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || !resolved[0].Resolution.Refused {
		t.Fatalf("got %+v, want one refused resolution", resolved)
	}
	if resolved[0].Resolution.Reason != "this deployment has no test-mint config" {
		t.Errorf("reason = %q, want the refusal's own text verbatim", resolved[0].Resolution.Reason)
	}
}

func TestUngrantedProvidersAreNotResolved(t *testing.T) {
	// A registered provider the task never asked for should not have
	// Resolve called on it at all -- only what the task actually holds.
	reg := NewCapabilityRegistry(grantCapability{}, mintCapability{})
	task := capTask("test-grant")
	resolved, err := ResolveGrants(context.Background(), reg, capContext(task))
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].Grant.Capability != "test-grant" {
		t.Fatalf("got %+v, want only test-grant resolved", resolved)
	}
}

// --- materialize ---------------------------------------------------------

func TestMaterializeGrantsSkipsRefused(t *testing.T) {
	reg := NewCapabilityRegistry(mintCapability{refuse: "no"}, grantCapability{})
	task := capTask("test-mint", "test-grant")
	cc := capContext(task)
	resolved, err := ResolveGrants(context.Background(), reg, cc)
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := MaterializeGrants(context.Background(), reg, cc, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if len(materialized) != 1 || materialized[0].Grant.Capability != "test-grant" {
		t.Fatalf("got %+v, want only test-grant materialized", materialized)
	}
}

func TestMintMaterializationCarriesLeaseAndPlacement(t *testing.T) {
	reg := NewCapabilityRegistry(mintCapability{})
	task := capTask("test-mint")
	cc := capContext(task)
	resolved, err := ResolveGrants(context.Background(), reg, cc)
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := MaterializeGrants(context.Background(), reg, cc, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if len(materialized) != 1 {
		t.Fatalf("got %d materializations, want 1", len(materialized))
	}
	m := materialized[0].Materialization
	if m.Lease == nil || m.Lease.Resource != "minted-"+cc.Run.ID {
		t.Fatalf("lease = %+v, want one naming the run", m.Lease)
	}
	if len(m.Placements) != 1 {
		t.Fatalf("placements = %+v, want exactly one", m.Placements)
	}
	p := m.Placements[0]
	if p.Side != SideSandbox {
		t.Errorf("placement side = %s, want sandbox", p.Side)
	}
	if p.EffectiveMode() != "600" {
		t.Errorf("effective mode = %s, want the safe default 600", p.EffectiveMode())
	}
	if m.CredentialOverride != nil {
		t.Error("a MINT capability should not set a credential override")
	}
}

func TestSelectMaterializationSetsOverrideAndNothingElse(t *testing.T) {
	reg := NewCapabilityRegistry(selectCapability{})
	task := capTask("test-select")
	cc := capContext(task)
	resolved, err := ResolveGrants(context.Background(), reg, cc)
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := MaterializeGrants(context.Background(), reg, cc, resolved)
	if err != nil {
		t.Fatal(err)
	}
	m := materialized[0].Materialization
	if m.CredentialOverride == nil || m.CredentialOverride.Name != "test-scoped-credential" {
		t.Fatalf("credential override = %+v, want test-scoped-credential", m.CredentialOverride)
	}
	if m.Lease != nil {
		t.Error("a SELECT capability mints nothing, so it should carry no lease")
	}
	if len(m.Placements) != 0 {
		t.Error("a SELECT capability writes nothing into the sandbox")
	}
}

func TestMaterializeGrantsStopsOnFailureButReturnsWhatSucceeded(t *testing.T) {
	reg := NewCapabilityRegistry(grantCapability{}, failingMaterializeCapability{}, mintCapability{})
	task := capTask("test-grant", "test-fails", "test-mint")
	cc := capContext(task)
	resolved, err := ResolveGrants(context.Background(), reg, cc)
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := MaterializeGrants(context.Background(), reg, cc, resolved)
	if err == nil {
		t.Fatal("expected an error from the failing capability")
	}
	// "A failed materialize means no dispatch": what came before it in
	// registration order is still reported, but test-mint -- which comes
	// after test-fails -- must never have had Materialize called.
	if len(materialized) != 1 || materialized[0].Grant.Capability != "test-grant" {
		t.Fatalf("got %+v, want only test-grant materialized before the failure", materialized)
	}
}

func TestScarceMaterializationReturnsExhaustionAndCompletesWithoutMintingTwice(t *testing.T) {
	limited := &limitedMaterializeCapability{succeedOn: 3, retryAfter: time.Millisecond}
	earlier := &countingMaterializeCapability{name: "test-earlier"}
	reg := NewCapabilityRegistry(earlier, limited)
	task := capTask("test-earlier", "test-limited")
	cc := capContext(task)
	resolved, err := ResolveGrants(context.Background(), reg, cc)
	if err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt < 3; attempt++ {
		materialized, err := MaterializeScarceGrants(context.Background(), reg, cc, resolved)
		if _, exhausted := ResourceExhausted(err); !exhausted || len(materialized) != 0 {
			t.Fatalf("scarce attempt %d = (%+v, %v), want an exhausted result and no reservation", attempt, materialized, err)
		}
		cc.Now = cc.Now.Add(time.Second)
	}
	reserved, err := MaterializeScarceGrants(context.Background(), reg, cc, resolved)
	if err != nil || len(reserved) != 1 {
		t.Fatalf("successful scarce reservation = (%+v, %v), want one", reserved, err)
	}
	materialized, err := CompleteMaterializeGrants(context.Background(), reg, cc, resolved, reserved)
	if err != nil {
		t.Fatalf("CompleteMaterializeGrants: %v", err)
	}
	if limited.attempts != 3 {
		t.Errorf("limited capability attempts = %d, want 3", limited.attempts)
	}
	if earlier.attempts != 1 {
		t.Errorf("ordinary capability attempts = %d, want 1 after admission", earlier.attempts)
	}
	if len(materialized) != 2 {
		t.Fatalf("materialized = %+v, want both grants", materialized)
	}
	if len(limited.times) != 3 || !limited.times[1].After(limited.times[0]) {
		t.Errorf("retry times = %v, want retries dated after the original attempt", limited.times)
	}
}

func TestMaterializeGrantsReturnsResourceExhaustionToItsAdmissionCaller(t *testing.T) {
	limited := &limitedMaterializeCapability{succeedOn: 100, retryAfter: time.Hour}
	reg := NewCapabilityRegistry(limited)
	task := capTask("test-limited")
	cc := capContext(task)
	resolved, err := ResolveGrants(context.Background(), reg, cc)
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := MaterializeGrants(context.Background(), reg, cc, resolved)
	exhausted, ok := ResourceExhausted(err)
	if !ok || exhausted.Capability != "test-limited" {
		t.Fatalf("MaterializeGrants error = %v, want test-limited resource exhaustion", err)
	}
	if len(materialized) != 0 || limited.attempts != 1 {
		t.Fatalf("materialized = %+v, attempts = %d; want one non-blocking attempt", materialized, limited.attempts)
	}
}

type countingMaterializeCapability struct {
	BaseCapability
	name     string
	attempts int
}

func (c *countingMaterializeCapability) Spec() CapabilitySpec {
	return CapabilitySpec{Name: c.name, Provision: ProvisionGrant, Source: GrantByLabel}
}

func (c *countingMaterializeCapability) Materialize(context.Context, CapabilityContext) (Materialization, error) {
	c.attempts++
	return Materialization{}, nil
}

// --- prompt sections -----------------------------------------------------

func TestPromptSectionsOnlyCoverMaterializedGrants(t *testing.T) {
	reg := NewCapabilityRegistry(mintCapability{}, grantCapability{})
	task := capTask("test-mint", "test-grant")
	cc := capContext(task)
	resolved, err := ResolveGrants(context.Background(), reg, cc)
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := MaterializeGrants(context.Background(), reg, cc, resolved)
	if err != nil {
		t.Fatal(err)
	}
	sections, err := PromptSections(context.Background(), cc, materialized)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 2 {
		t.Fatalf("got %d prompt sections, want 2: %v", len(sections), sections)
	}
	if sections[0] != "a test key is at /home/debian/.test-mint-key" {
		t.Errorf("mint prompt section = %q", sections[0])
	}
	if sections[1] != "you can do the test-only thing" {
		t.Errorf("grant prompt section = %q", sections[1])
	}
}

func TestPromptSectionNeverSeesPlacementContent(t *testing.T) {
	// mintCapability.PromptSection only receives placements, whose
	// Content field carries the material -- this asserts the contract's
	// own signature keeps that true structurally: PromptSection cannot
	// name a placement's Content because it is never given the
	// Materialization, only the []Placement slice from it. This test
	// exercises that by hand rather than by static shape alone.
	c := mintCapability{}
	cc := capContext(capTask("test-mint"))
	text, err := c.PromptSection(context.Background(), cc, []Placement{
		{Side: SideSandbox, Path: "/home/debian/.test-mint-key", Content: "sekret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if containsSecret(text) {
		t.Fatalf("prompt section leaked material: %q", text)
	}
}

func containsSecret(s string) bool {
	for i := 0; i+len("sekret") <= len(s); i++ {
		if s[i:i+len("sekret")] == "sekret" {
			return true
		}
	}
	return false
}

// --- base capability -----------------------------------------------------

func TestBaseCapabilityDefaults(t *testing.T) {
	c := BaseCapability{}
	cc := capContext(capTask("test-bare"))

	res, err := c.Resolve(context.Background(), cc)
	if err != nil || res.Refused {
		t.Fatalf("BaseCapability.Resolve should default to Honoured, got %+v, err %v", res, err)
	}
	m, err := c.Materialize(context.Background(), cc)
	if err != nil || m.Lease != nil || len(m.Placements) != 0 || m.CredentialOverride != nil {
		t.Fatalf("BaseCapability.Materialize should default to nothing, got %+v, err %v", m, err)
	}
	text, err := c.PromptSection(context.Background(), cc, nil)
	if err != nil || text != "" {
		t.Fatalf("BaseCapability.PromptSection should default to no text, got %q, err %v", text, err)
	}
	if err := c.Revoke(context.Background(), cc, Lease{}); err != nil {
		t.Fatalf("BaseCapability.Revoke should default to a no-op, got %v", err)
	}
}

func TestCapabilityRegistryDynamicProvider(t *testing.T) {
	reg := NewCapabilityRegistry(grantCapability{})
	reg.RegisterDynamic(func(name string) (CapabilityProvider, bool) {
		if strings.HasPrefix(name, "dynamic-") {
			return &countingMaterializeCapability{name: name}, true
		}
		return nil, false
	})

	// Static lookup
	if _, ok := reg.Lookup("test-grant"); !ok {
		t.Error("Lookup(test-grant) = false, want true")
	}

	// Dynamic lookup
	p, ok := reg.Lookup("dynamic-foo")
	if !ok || p.Spec().Name != "dynamic-foo" {
		t.Fatalf("Lookup(dynamic-foo) = %v, %v", p, ok)
	}

	// Dynamic resolve
	task := capTask("dynamic-foo")
	cc := capContext(task)
	resolved, err := ResolveGrants(context.Background(), reg, cc)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].Resolution.Refused {
		t.Fatalf("ResolveGrants(dynamic-foo) = %+v", resolved)
	}
}
