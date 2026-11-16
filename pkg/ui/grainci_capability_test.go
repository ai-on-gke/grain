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

package ui_test

// Task 2015: grain-ci is granted by a repository's CI Mode ("Use Grain
// CI"), never attached by hand. 6172e1205 had put it back in the picker,
// where it was accepted everywhere and then did nothing -- a repo's or
// the deployment's default set carrying it turned nothing on, and a task
// filed against a github-mode repo with -capability grain-ci was filed
// holding no grain-ci at all, silently. These hold the other answer:
// refused by name, with the switch that works.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
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

	if slices.ContainsFunc(ui.OfferedCapabilities(), func(c ui.Capability) bool { return c.ID == grainci.Grant }) {
		t.Errorf("the task capability picker offers %q", grainci.Grant)
	}
	// Both spellings, and above all the recorded one: grain#ci is a
	// capability reference, which every picker otherwise takes as
	// grantable (ui.Client.capabilityByID), so the canonical name is
	// the one a hand-attached grant would arrive as.
	for _, name := range []string{grainci.Grant, model.GrainCIRef} {
		if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
			DefaultCapabilities: &[]string{name},
		}); !refusesAsGrainCI(err) {
			t.Errorf("UpdateSettings: %s: err = %v, want the CI Mode refusal", name, err)
		}
		if _, err := c.SetRepoDefaultCapabilities(ctx, repo, []string{name}); !refusesAsGrainCI(err) {
			t.Errorf("SetRepoDefaultCapabilities: %s: err = %v, want the CI Mode refusal", name, err)
		}
		// `grain create -repo acme/widgets -capability grain-ci` on a
		// github-mode repo: the case that used to be filed holding nothing.
		if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{
			Title: "try grain CI", Approved: true, Repo: repo,
			Capabilities: &[]string{name},
		}); !refusesAsGrainCI(err) {
			t.Errorf("CreateTask: %s: err = %v, want the CI Mode refusal", name, err)
		}
		task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "plain", Approved: true, Repo: repo})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.SetCapability(ctx, task.ID, name, true); !refusesAsGrainCI(err) {
			t.Errorf("SetCapability: %s: err = %v, want the CI Mode refusal", name, err)
		}
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
		if !slices.Contains(task.Capabilities, grainci.Grant) {
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
		Repo: repo, CIMode: model.CIModeGitHub, DefaultCapabilities: []string{grainci.Grant},
	}); err != nil {
		t.Fatal(err)
	}
	putDefaultCapabilities(t, ctx, store, grainci.Grant)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "legacy", Approved: true, Repo: repo.String()})
	if err != nil {
		t.Fatalf("CreateTask on a repo with a legacy grain-ci default: %v", err)
	}
	if slices.Contains(task.Capabilities, grainci.Grant) {
		t.Errorf("capabilities = %v: a legacy default turned grain CI on for a github-mode repo", task.Capabilities)
	}
}

// A standing console session has no target repository and cannot attach
// grain#ci by hand, yet its proposals against a grain-CI repository
// receive grain#ci by default; that default must not hold the proposal
// unapproved or block an agent's RunNow approval.
func TestSessionQueuesProposalAgainstGrainCIRepoWithoutHoldingGrainCI(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	sessions, err := c.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		ID:            "ops-chat",
		Title:         "Ops Chat",
		TasksApproved: true,
		Writes:        []ui.WriteTarget{{Repo: repo.String()}},
	})
	if err != nil {
		t.Fatalf("CreateConsoleSession: %v", err)
	}
	var sessionTaskID string
	for _, s := range sessions {
		if s.ID == "ops-chat" {
			sessionTaskID = s.TaskID
		}
	}
	if sessionTaskID == "" {
		t.Fatalf("created session missing taskId in %+v", sessions)
	}

	filed, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "follow-up on grain CI repo", Description: "d",
		Repo: repo.String(), ProposedBy: sessionTaskID,
	})
	if err != nil {
		t.Fatalf("CreateTask from session: %v", err)
	}
	if filed.State != model.StateQueued {
		t.Errorf("proposal state = %s, want %s (body: %q)", filed.State, model.StateQueued, filed.Description)
	}
	if !slices.Contains(filed.Capabilities, grainci.Grant) {
		t.Errorf("proposal capabilities = %v, want %s", filed.Capabilities, grainci.Grant)
	}

	unapproved, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "held on grain CI repo", Description: "d",
		Repo: repo.String(), ProposedBy: sessionTaskID, HoldForApproval: true,
	})
	if err != nil {
		t.Fatalf("CreateTask held: %v", err)
	}
	asSession := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: sessionTaskID})
	if err := c.RunNow(asSession, unapproved.ID); err != nil {
		t.Errorf("RunNow by session on grain-CI task failed: %v", err)
	}
}

// Regression for task 3060: filing a task from .grain/templates/release-train.yml
// (or any .grain template that still writes `capabilities: [grain-ci]`) on a
// ci_mode: github repository must succeed without CreateTask refusing a copied
// grain#ci grant, and must hand the task repo-scoped api:ci-read@<repo>.
func TestDeclaredTemplateWithCIReadOrLegacyGrainCIFilesOnGitHubModeRepo(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	repo := model.GrainSourceRepo()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}
	trainYAML, err := os.ReadFile(filepath.Join("..", "..", ".grain", "templates", "release-train.yml"))
	if err != nil {
		t.Fatal(err)
	}
	declared, err := repodecl.Load(fstest.MapFS{
		".grain/templates/release-train.yml": {Data: trainYAML},
		".grain/templates/grain-ci-ref.yml":  {Data: []byte("title: grain#ci reader\ncapabilities: [\"https://grain.dev/capabilities/grain#ci\"]\nbody: read CI\n")},
	}, repo)
	if err != nil {
		t.Fatalf("repodecl.Load: %v", err)
	}
	now := time.Date(2026, 11, 7, 5, 0, 0, 0, time.UTC)
	if _, err := store.SyncRepoDeclarations(ctx, repo, strings.Repeat("a", 40), declared, now, time.UTC); err != nil {
		t.Fatalf("SyncRepoDeclarations: %v", err)
	}
	wantCap := model.APIGrantString("ci-read", repo.String())
	for _, stem := range []string{"release-train", "grain-ci-ref"} {
		tmplID := model.RepoDeclaredID(model.DeclaredTemplate, repo, stem)
		task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
			TemplateID: tmplID,
			Repo:       repo.String(),
			Approved:   true,
		})
		if err != nil {
			t.Fatalf("CreateTask from %s on ci_mode: github repo: %v", stem, err)
		}
		if !slices.Contains(task.Capabilities, wantCap) {
			t.Errorf("%s task capabilities = %v, want %q", stem, task.Capabilities, wantCap)
		}
		if slices.Contains(task.Capabilities, grainci.Grant) {
			t.Errorf("%s task capabilities = %v, must not hold %q on ci_mode: github", stem, task.Capabilities, grainci.Grant)
		}
	}
}
