package ui

// The picker listing (OfferedCapabilities, labels.go) and the set of
// capabilities grain actually ships providers for (capabilityCatalog,
// capability_status.go) are two hand-maintained tables with nothing in
// the type system tying them together, and they drifted apart in both
// directions at once: gcp-key and github-sandbox had providers and no
// picker row, so no task on any deployment could be granted either;
// "scratch-repo" had a picker row and no provider, so a task granted it
// failed every dispatch at model.ResolveGrants. These tests are the tie.
//
// They live in package ui rather than ui_test because capabilityCatalog
// and capabilityDisplayNames are unexported -- the catalog is the thing
// worth comparing against, since it reads each provider's own Spec()
// rather than repeating a third list.

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/githubtoken"
	"github.com/ai-on-gke/grain/pkg/model"
)

// capabilityIDs is the picker listing's ids, in table order.
func capabilityIDs() []string {
	var ids []string
	for _, c := range OfferedCapabilities() {
		ids = append(ids, c.ID)
	}
	return ids
}

// catalogIDs is the shipped providers' ids, in catalog order.
func catalogIDs() []string {
	var ids []string
	for _, spec := range capabilityCatalog() {
		ids = append(ids, spec.Name)
	}
	return ids
}

func TestOfferedCapabilitiesCoversEveryShippedCapability(t *testing.T) {
	t.Parallel()
	offered, shipped := capabilityIDs(), catalogIDs()
	for _, id := range shipped {
		// Except the ones no task may hold: those are shipped, granted
		// and reported on, and their absence from the picker is the
		// point rather than the drift this test is about
		// (TestCIOnlyCapabilitiesAreNotOfferedToTasks below).
		if ciOnlyCapability(id) {
			continue
		}
		if !slices.Contains(offered, id) {
			t.Errorf("capability %q has a provider but no OfferedCapabilities row: "+
				"grantsFor and SetCapability reject it as an unknown capability, so no task can ever be granted it "+
				"and its provider is never resolved, never materialized, and places nothing in any sandbox", id)
		}
	}
	for _, id := range offered {
		if !slices.Contains(shipped, id) {
			t.Errorf("capability %q is offered by OfferedCapabilities but grain ships no provider for it: "+
				"a task granted it is refused by model.ResolveGrants (\"no provider is registered\"), "+
				"which prepareCapabilities turns into a failed dispatch", id)
		}
	}
}

// The picker's other half, and the one grain/task-382 is about: a
// capability grain ships *for CI jobs only* must not be a row a human
// can tick on a task. registry-push and package-publish were both rows
// here, with a description ending "Grain CI makes it available only to
// post-merge release workflows" -- which is a sentence, not a gate. The
// gate (SupportsCIEvent) is applied on the CI path alone, so ticking
// package-publish on a task that named a repository resolved perfectly
// well and wrote a live package-write token into the sandbox, on any
// branch, for any task.
func TestCIOnlyCapabilitiesAreNotOfferedToTasks(t *testing.T) {
	t.Parallel()
	offered := capabilityIDs()
	var ciOnly []string
	for _, spec := range capabilityCatalog() {
		if !spec.CIOnly {
			continue
		}
		ciOnly = append(ciOnly, spec.Name)
		if slices.Contains(offered, spec.Name) {
			t.Errorf("capability %q is CI-only and the task picker offers it anyway: a task can be "+
				"granted it, and nothing on the task path applies the event gate that keeps its "+
				"material to releases", spec.Name)
		}
		// Absent from the task picker, and still a capability this
		// deployment configures: a repo grants it to its CI jobs, and
		// Settings has to be able to name it to report on its
		// credential (capabilityDisplayNames).
		if capabilityDisplayName(spec.Name) == "" {
			t.Errorf("CI-only capability %q has no capabilityDisplayNames entry, so Settings shows it "+
				"with a blank name", spec.Name)
		}
	}
	// Vacuously passing would be the whole of this test's failure mode:
	// it is a listing that must stay empty, checked against a listing
	// that must not be.
	if len(ciOnly) == 0 {
		t.Fatal("no capability in the catalog is CI-only, so this test asserts nothing -- " +
			"registry-push and package-publish are supposed to be")
	}
}

// The rule the next event-gated capability needs, rather than the
// precedent of these two: every event a CIEventCapability narrows itself
// to is a CI event, and a task produces none of them -- so a provider
// that implements that interface and is not CIOnly is one whose gate the
// task path would simply not apply (model.CapabilitySpec.CIOnly).
func TestEveryEventGatedCapabilityIsCIOnly(t *testing.T) {
	t.Parallel()
	var gated int
	for _, p := range capabilityProviderCatalog() {
		if _, ok := p.(model.CIEventCapability); !ok {
			continue
		}
		gated++
		if !p.Spec().CIOnly {
			t.Errorf("capability %q gates itself on CI events (SupportsCIEvent) but is not CIOnly: "+
				"a task grant of it is resolved with no event gate at all, which is the one thing "+
				"that gate exists to prevent", p.Spec().Name)
		}
	}
	if gated == 0 {
		t.Fatal("no provider in the catalog implements model.CIEventCapability, so this test asserts nothing")
	}
}

// Settings' Capabilities tab and the per-task picker name the same
// capabilities twice (capability_status.go's own note on why the
// duplication is accepted). Two views disagreeing about what a
// capability is called is the cheap half of the same drift.
func TestOfferedCapabilitiesNamesMatchSettingsDisplayNames(t *testing.T) {
	t.Parallel()
	for _, c := range OfferedCapabilities() {
		if c.Description == "" {
			t.Errorf("capability %q has no description for the picker to show", c.ID)
		}
		want := capabilityDisplayName(c.ID)
		if want == "" {
			t.Errorf("capability %q has no capabilityDisplayNames entry, so Settings shows it with a blank name", c.ID)
			continue
		}
		if c.Name != want {
			t.Errorf("capability %q: picker name %q, Settings name %q -- one capability, two names", c.ID, c.Name, want)
		}
	}
}

// The third drift of the same kind, and the one grain/task-172 is about:
// a capability that resolves a standing credential -- one an operator
// pasted once, which every later mint authenticates as -- but ships no
// way to test it. Settings can only ever say **Ready** for such a
// capability, meaning "the secret is set", while the key inside it may
// have been deleted or rotated away at the far end months ago. Every
// capability with a Requires entry is one that can fail that way, so
// every one of them owes a model.CredentialChecker.
func TestEveryCapabilityWithAStandingCredentialCanBeChecked(t *testing.T) {
	t.Parallel()
	for _, p := range capabilityProviderCatalog() {
		spec := p.Spec()
		if len(spec.Requires) == 0 {
			continue
		}
		if !capabilityCheckable(spec.Name) {
			t.Errorf("capability %q resolves the standing credential(s) %v but implements no "+
				"model.CredentialChecker: Settings can report it Ready and nothing on any pane "+
				"can see whether what is in those secrets still works", spec.Name, spec.Requires)
		}
	}
}

// The same bar, held to the one listing on this pane that is not
// capabilityProviderCatalog: a named GitHub token (githubTokenStatuses).
// Those rows are deliberately not in the catalog -- which tokens exist
// is an operator's own files, not a property of this build -- so the
// test above cannot see them, and for a while nothing could: they
// reported Ready by construction, and a token revoked or rotated at
// GitHub's end went stale with nothing on any pane able to say so
// (grain/task-189).
//
// Two halves, because the drift can happen in either: the provider grain
// builds for a named token has to implement model.CredentialChecker, and
// the row this package builds for that same token has to report itself
// Checkable, or the pane offers no button to reach the check through.
func TestEveryNamedGitHubTokenRowCanBeChecked(t *testing.T) {
	t.Parallel()
	if _, ok := any(githubtoken.New("release-bot", githubtoken.Config{})).(model.CredentialChecker); !ok {
		t.Fatal("githubtoken.Provider implements no model.CredentialChecker: a named token's row can " +
			"only ever say Ready, and nothing on any pane can see whether GitHub still accepts it")
	}
	c := &Client{Config: Config{
		Capabilities:     append(OfferedCapabilities(), GitHubTokenCapabilities([]string{"release-bot"})...),
		CapabilityChecks: unreachedChecker{},
	}}
	var rows int
	for _, status := range c.capabilityStatuses(model.Config{}, nil) {
		if _, ok := model.GitCredentialName(status.ID); !ok {
			continue
		}
		rows++
		if !status.Checkable {
			t.Errorf("named token %q reports Checkable false, so Settings offers no way to test a "+
				"credential it reports Ready by construction", status.ID)
		}
	}
	if rows != 1 {
		t.Fatalf("built %d named-token rows, want one -- the listing this test is about is missing", rows)
	}
}

// unreachedChecker satisfies Config.CapabilityChecks without being a
// place a check could actually go: what the tests above need from it is
// that this deployment *has* one (the nil-means-unavailable contract),
// not what it would answer.
type unreachedChecker struct{}

func (unreachedChecker) CheckCapability(ctx context.Context, id string) (CapabilityCheckResult, error) {
	return CapabilityCheckResult{}, errors.New("no check should have been made")
}

// The other direction: a capability with nothing standing behind it must
// not offer a check, since there would be nothing for one to authenticate
// as and the button would only ever produce a confusing failure.
func TestCapabilitiesWithNoCredentialOfferNoCheck(t *testing.T) {
	t.Parallel()
	for _, p := range capabilityProviderCatalog() {
		spec := p.Spec()
		if len(spec.Requires) == 0 && capabilityCheckable(spec.Name) {
			t.Errorf("capability %q needs no credential but ships a credential check", spec.Name)
		}
	}
}
