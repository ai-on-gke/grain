package ui_test

// The API surface of the merge queue's overlap re-check mode
// (model.RepoConfig.MergeRecheck, grain/task-985). What the queue does
// with it is pinned in pkg/orchestrator.

import (
	"reflect"
	"testing"

	"github.com/bwsalmon/grain/pkg/forge"
	"github.com/bwsalmon/grain/pkg/ui"
)

func TestSetRepoSettingsAppliesCapabilitiesAndUnfixableChecks(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	defaultCaps := []string{"gcp-key", "gcp-key"}
	ciCaps := []string{"gemini-key", "package-publish", "package-publish", "repo-read:acme/tools"}
	unfixable := []string{" cla/google ", "CLA/Google", "license/check"}

	got, err := c.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{
		DefaultCapabilities: &defaultCaps,
		CICapabilities:      &ciCaps,
		UnfixableChecks:     &unfixable,
	})
	if err != nil {
		t.Fatalf("SetRepoSettings: %v", err)
	}
	if !reflect.DeepEqual(got.DefaultCapabilities, []string{"gcp-key"}) {
		t.Errorf("defaultCapabilities = %v, want [gcp-key]", got.DefaultCapabilities)
	}
	if !reflect.DeepEqual(got.CICapabilities, []string{"gemini-key", "package-publish", "repo-read:acme/tools"}) {
		t.Errorf("ciCapabilities = %v, want [gemini-key package-publish repo-read:acme/tools]", got.CICapabilities)
	}
	if !reflect.DeepEqual(got.UnfixableChecks, []string{"cla/google", "license/check"}) {
		t.Errorf("unfixableChecks = %v, want [cla/google license/check]", got.UnfixableChecks)
	}

	// Narrowing ciCapabilities via SetRepoSettings keeps other fields intact.
	narrowCI := []string{"gemini-key", "package-publish"}
	got, err = c.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{
		CICapabilities: &narrowCI,
	})
	if err != nil {
		t.Fatalf("SetRepoSettings narrowing ciCapabilities: %v", err)
	}
	if !reflect.DeepEqual(got.CICapabilities, []string{"gemini-key", "package-publish"}) {
		t.Errorf("ciCapabilities after narrowing = %v, want [gemini-key package-publish]", got.CICapabilities)
	}
	if !reflect.DeepEqual(got.DefaultCapabilities, []string{"gcp-key"}) {
		t.Errorf("defaultCapabilities = %v, want [gcp-key] preserved", got.DefaultCapabilities)
	}
	if !reflect.DeepEqual(got.UnfixableChecks, []string{"cla/google", "license/check"}) {
		t.Errorf("unfixableChecks = %v, want [cla/google license/check] preserved", got.UnfixableChecks)
	}

	// Validation rules match SetRepoDefaultCapabilities, SetRepoCICapabilities, and SetRepoUnfixableChecks.
	badDefault := []string{"package-publish"}
	if _, err := c.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{DefaultCapabilities: &badDefault}); err == nil {
		t.Error("SetRepoSettings accepted CI-only capability in defaultCapabilities")
	}
	badCI := []string{"self-debug"}
	if _, err := c.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{CICapabilities: &badCI}); err == nil {
		t.Error("SetRepoSettings accepted non-CI capability in ciCapabilities")
	}
	badUnfixable := []string{forge.GrainCICheckName}
	if _, err := c.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{UnfixableChecks: &badUnfixable}); err == nil {
		t.Error("SetRepoSettings accepted grain CI check in unfixableChecks")
	}
	commaUnfixable := []string{"a,b"}
	if _, err := c.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{UnfixableChecks: &commaUnfixable}); err == nil {
		t.Error("SetRepoSettings accepted comma in unfixableChecks")
	}
}

func TestSetRepoSettingsStoresTheMergeRecheckMode(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	read, err := c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if read.MergeRecheck != "enforce" {
		t.Fatalf("mergeRecheck = %q for a repo nobody has configured, want enforce", read.MergeRecheck)
	}

	observe := "Observe"
	got, err := c.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{MergeRecheck: &observe})
	if err != nil {
		t.Fatalf("setting observe: %v", err)
	}
	if got.MergeRecheck != "observe" {
		t.Fatalf("mergeRecheck = %q after setting observe", got.MergeRecheck)
	}
	if got, err = c.SetRepoSetupCommand(ctx, "acme/widgets", "make all"); err != nil || got.MergeRecheck != "observe" {
		t.Fatalf("mergeRecheck = %q (%v) after saving a setup command, want it untouched", got.MergeRecheck, err)
	}

	bogus := "sometimes"
	if _, err := c.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{MergeRecheck: &bogus}); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
	reset := ""
	if got, err = c.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{MergeRecheck: &reset}); err != nil ||
		got.MergeRecheck != "enforce" {
		t.Fatalf("mergeRecheck = %q (%v) after resetting, want enforce", got.MergeRecheck, err)
	}
}
