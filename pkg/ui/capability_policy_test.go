package ui_test

import (
	"context"
	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const usageWorkflow = `name: deploy
on:
  push:
    branches: [main]
jobs:
  ship:
    capabilities:
      - ` + configCap + `#key
    steps:
      - run: echo ship
`

// grantFixtures stores the server, and an open task, a closed task, a
// template and a declared workflow each granting one of its methods.
func grantFixtures(t *testing.T, client *ui.Client) {
	t.Helper()
	ctx := context.Background()
	store := client.Store
	if err := store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.MCPServers = map[string]protocol.ServerConfig{configCap: {Command: "grain", Args: []string{"capability", "cloud"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	grant := []model.Grant{{Capability: configCap + `#key={"account":"ci"}`}}
	for _, task := range []model.Task{
		{ID: "11", Intent: model.IntentImplement, Title: "open work", Target: &repo, Grants: grant},
		{ID: "12", Intent: model.IntentImplement, Title: "closed work", Target: &repo, Grants: grant},
	} {
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.Close(ctx, "12", ui.CloseOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, model.Template{ID: "tpl-1", Name: "nightly key", Title: "rotate", Target: &repo, Grants: grant}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoWorkflows(ctx, repo, []model.RepoWorkflow{{Repo: repo, Path: ".grain/workflows/deploy.yml", Text: usageWorkflow}}); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityServerUsageListsEveryOpenHolder(t *testing.T) {
	_, hc, client, _ := configTestServer(t, &fakeProber{})
	grantFixtures(t, client)
	all, err := hc.CapabilityServerUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var usage []ui.CapabilityServerUsage
	for _, u := range all {
		if !strings.HasPrefix(u.ID, intree.CanonicalIDPrefix) {
			usage = append(usage, u)
		}
	}
	if len(usage) != 1 || usage[0].ID != configCap {
		t.Fatalf("usage = %+v, want the one server", usage)
	}
	var got []string
	for _, h := range usage[0].Holders {
		got = append(got, h.Kind+":"+h.ID)
	}
	want := "task:11 template:tpl-1 workflow:acme/widgets:.grain/workflows/deploy.yml#ship"
	if strings.Join(got, " ") != want {
		t.Errorf("holders = %v, want %s (a closed task holds nothing)", got, want)
	}
}

func TestCapabilityPolicyPreviewNamesNewlyDeniedGrantsAndSaveStoresIt(t *testing.T) {
	_, hc, client, _ := configTestServer(t, &fakeProber{})
	grantFixtures(t, client)
	ctx := context.Background()
	denyCI := model.CapabilityPolicy{Rules: []model.CapabilityPolicyRule{
		{Capability: configCap + "#*", RunKinds: []string{model.CapabilityRunCI}, Deny: true},
	}}
	preview, err := hc.PreviewCapabilityPolicy(ctx, denyCI)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Denied) != 1 || preview.Denied[0].Kind != "workflow" || !strings.Contains(preview.Denied[0].Reason, "denies") {
		t.Fatalf("preview = %+v, want only the workflow job denied", preview)
	}

	// Restricting the method to one other repository refuses the task
	// and the template too.
	elsewhere := model.CapabilityPolicy{Rules: []model.CapabilityPolicyRule{{Capability: configCap + "#key", Repos: []string{"other/*"}}}}
	if preview, err = hc.PreviewCapabilityPolicy(ctx, elsewhere); err != nil || len(preview.Denied) != 3 {
		t.Fatalf("preview = %+v, %v, want task, template and workflow denied", preview, err)
	}

	if _, err := hc.PreviewCapabilityPolicy(ctx, model.CapabilityPolicy{Rules: []model.CapabilityPolicyRule{{Capability: "gcp-key"}}}); err == nil {
		t.Error("a rule naming no reference was previewed")
	}
	if _, err := hc.SetCapabilityPolicy(ctx, model.CapabilityPolicy{Rules: []model.CapabilityPolicyRule{{Capability: configCap, RunKinds: []string{"robot"}}}}); err == nil {
		t.Error("an invalid run kind was saved")
	}
	saved, err := hc.SetCapabilityPolicy(ctx, denyCI)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Policy.Rules) != 1 || !saved.Policy.Rules[0].Deny {
		t.Fatalf("saved = %+v", saved)
	}
	cfg, err := client.Store.GetConfig(ctx)
	if err != nil || len(cfg.CapabilityPolicy.Rules) != 1 {
		t.Fatalf("stored policy = %+v, %v", cfg, err)
	}
	// Once saved, the same policy denies nothing new.
	if preview, err = hc.PreviewCapabilityPolicy(ctx, denyCI); err != nil || len(preview.Denied) != 0 {
		t.Errorf("preview of the stored policy = %+v, %v", preview, err)
	}
}

func TestCapabilityServerRenameMovesTheEntryWithItsConfig(t *testing.T) {
	_, hc, client, _ := configTestServer(t, &fakeProber{})
	ctx := context.Background()
	if err := client.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.MCPServers = map[string]protocol.ServerConfig{
			configCap:                                {Command: "grain", Args: []string{"capability", "cloud"}, Config: map[string]any{"project": "p1", "token": "$secret:cloud/token"}},
			"https://capabilities.example.com/taken": {Command: "other"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	const renamed = "https://capabilities.example.com/cloud-v2"
	moved := protocol.ServerConfig{Command: "grain", Args: []string{"capability", "cloud"}, Config: map[string]any{"project": "p2", "token": "$secret:cloud/token"}}
	// The new id has no status of its own yet: the old one's schema
	// checks the config.
	list, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: renamed, RenameFrom: configCap, Config: moved})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range own(list) {
		ids = append(ids, s.ID)
	}
	if strings.Join(ids, " ") != "https://capabilities.example.com/cloud-v2 https://capabilities.example.com/taken" {
		t.Fatalf("after rename = %v", ids)
	}
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: "https://capabilities.example.com/taken", RenameFrom: renamed, Config: moved}); err == nil {
		t.Error("a rename onto an existing entry was saved")
	}
}
