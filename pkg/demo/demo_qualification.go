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

package demo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// demoQualificationRelease is the release -qualification seeds a
// candidate of.
const demoQualificationRelease = "1.0"

// seedQualificationDemo is `grain demo -qualification`: a release of repo
// whose current candidate is active and whose qualification run is waiting
// for approval, so Releases' "Approve all" can be pressed in a browser
// (docs/test-runs/2026-10-12-rbac-enforcement-sweep.md). Opt-in, because
// the run files a task every other demo spec would have to count around.
//
// The orchestrator cuts a candidate on GitHub and its qualification
// reconciler files the run (reconcileQualifications); a demo has neither,
// so the candidate is marked cut here and the run is created the way that
// reconciler creates one, from a plan that requires approval.
func seedQualificationDemo(ctx context.Context, store *model.Store, repo model.RepoRef) error {
	now := time.Now().UTC()
	tmpl := model.Template{
		ID: "template-qualify", Name: "Qualify", Title: "Qualify the candidate",
		Body: "Run the release checklist against the candidate branch.", CreatedAt: now,
	}
	if err := store.PutTemplate(ctx, tmpl); err != nil {
		return fmt.Errorf("storing the qualification template: %w", err)
	}
	plan := model.QualificationPlan{
		Repo: repo, Configured: true, RequireApproval: true,
		Items: []model.QualificationItem{{TemplateID: tmpl.ID, Repeat: 1}},
	}
	if err := store.PutQualificationPlan(ctx, plan); err != nil {
		return fmt.Errorf("storing the qualification plan: %w", err)
	}
	release, err := store.CreateRelease(ctx, repo, demoQualificationRelease, now)
	if err != nil {
		return fmt.Errorf("creating release %s: %w", demoQualificationRelease, err)
	}
	candidate, err := store.CurrentCandidateForRelease(ctx, release.ID)
	if err != nil {
		return err
	}
	if candidate == nil {
		return fmt.Errorf("release %s has no candidate", demoQualificationRelease)
	}
	if err := store.MarkCandidateCut(ctx, candidate.ID); err != nil {
		return fmt.Errorf("marking the candidate cut: %w", err)
	}
	candidate.Status = model.CandidateActive
	if _, err := store.CreateQualificationRun(ctx, *candidate, plan, now); err != nil {
		return fmt.Errorf("creating the qualification run: %w", err)
	}
	return nil
}

// configureSecretsDemo is `grain demo -secrets-store`: a real secrets
// store (pkg/secrets) in the demo's throwaway directory, so Settings →
// Capabilities' "Other secrets" and a repository's release secrets can be
// set and removed rather than answered "no secrets directory". Opt-in,
// because a demo without one is what the panes' unavailable notes are
// walked against.
func configureSecretsDemo(dir string, cfg ui.Config) (ui.Config, error) {
	path := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(path, 0o700); err != nil {
		return cfg, fmt.Errorf("creating the secrets store: %w", err)
	}
	cfg.Secrets = secrets.New(path)
	return cfg, nil
}
