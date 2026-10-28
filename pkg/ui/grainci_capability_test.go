package ui_test

// Task 2015: grain-ci is granted by a repository's CI Mode ("Use Grain
// CI"), never attached by hand. 6172e1205 had put it back in the picker,
// where it was accepted everywhere and then did nothing -- a repo's or
// the deployment's default set carrying it turned nothing on, and a task
// filed against a github-mode repo with -capability grain-ci was filed
// holding no grain-ci at all, silently. These hold the other answer:
// refused by name, with the switch that works.

import (
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// refusesAsGrainCI reports whether err is grain's refusal of a
// hand-attached grain-ci: the CI Mode named, and not "unknown
// capability", which is a lie about a capability this build ships.
func refusesAsGrainCI(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "Use Grain CI") && !strings.Contains(err.Error(), "unknown capability")
}

func TestGrainCIIsNotOfferedAndIsRefusedByName(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	repo := "acme/widgets"

	if slices.ContainsFunc(ui.OfferedCapabilities(), func(c ui.Capability) bool { return c.ID == grainci.CapabilityName }) {
		t.Errorf("the task capability picker offers %q", grainci.CapabilityName)
	}
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		DefaultCapabilities: &[]string{grainci.CapabilityName},
	}); !refusesAsGrainCI(err) {
		t.Errorf("UpdateSettings: err = %v, want the CI Mode refusal", err)
	}
	if _, err := c.SetRepoDefaultCapabilities(ctx, repo, []string{grainci.CapabilityName}); !refusesAsGrainCI(err) {
		t.Errorf("SetRepoDefaultCapabilities: err = %v, want the CI Mode refusal", err)
	}
	// `grain create -repo acme/widgets -capability grain-ci` on a
	// github-mode repo: the case that used to be filed holding nothing.
	if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "try grain CI", Approved: true, Repo: repo,
		Capabilities: &[]string{grainci.CapabilityName},
	}); !refusesAsGrainCI(err) {
		t.Errorf("CreateTask: err = %v, want the CI Mode refusal", err)
	}
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "plain", Approved: true, Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetCapability(ctx, task.ID, grainci.CapabilityName, true); !refusesAsGrainCI(err) {
		t.Errorf("SetCapability: err = %v, want the CI Mode refusal", err)
	}
}

// What the refusal points at does work: a task filed against a repo
// whose CI Mode is grain holds grain-ci without anybody naming it, even
// when the browser sends its picker as an explicit, empty list.
func TestGrainCIModeGrantsGrainCIToTheRepositorysTasks(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	for _, caps := range []*[]string{nil, {}} {
		task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
			Title: "gated", Approved: true, Repo: repo.String(), Capabilities: caps,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(task.Capabilities, grainci.CapabilityName) {
			t.Errorf("capabilities = %v (picker %v), want grain-ci from the CI Mode", task.Capabilities, caps)
		}
	}
}

// A default set written before grain-ci was refused there still holds
// it. That must not fail every task filed against the repo, and must not
// turn anything on either: the CI Mode alone decides.
func TestLegacyGrainCIInADefaultSetIsIgnored(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGitHub, DefaultCapabilities: []string{grainci.CapabilityName},
	}); err != nil {
		t.Fatal(err)
	}
	putDefaultCapabilities(t, ctx, store, grainci.CapabilityName)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "legacy", Approved: true, Repo: repo.String()})
	if err != nil {
		t.Fatalf("CreateTask on a repo with a legacy grain-ci default: %v", err)
	}
	if slices.Contains(task.Capabilities, grainci.CapabilityName) {
		t.Errorf("capabilities = %v: a legacy default turned grain CI on for a github-mode repo", task.Capabilities)
	}
}
