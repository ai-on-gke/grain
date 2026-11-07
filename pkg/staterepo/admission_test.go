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

package staterepo_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

func TestAdmissionRoundTripsThroughStateRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)

	cfg := model.DefaultConfig()
	cfg.DefaultAdmission = model.AdmissionBypass
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:             widgets,
		DefaultAdmission: model.AdmissionDurable,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID:        "tmpl-steward",
		Name:      "Steward",
		Title:     "Steward",
		Body:      "Keep watch.",
		Admission: model.AdmissionDurable,
		CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSchedule(ctx, model.Schedule{
		ID:         "sched-bypass",
		Title:      "Bypass check",
		Body:       "Check.",
		Target:     widgets,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 60},
		Admission:  model.AdmissionBypass,
		Enabled:    true,
		NextRunAt:  now,
		CreatedAt:  now,
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	for table, want := range map[string]string{"template": "admission: durable", "schedule": "admission: bypass"} {
		path := staterepo.DumpPath(table)
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s missing %q:\n%s", path, want, raw)
		}
	}

	dst, dstDB := openDB(t)
	if err := staterepo.ImportTables(ctx, dstDB, dir, staterepo.SettingsTables); err != nil {
		t.Fatalf("importing settings tables: %v", err)
	}
	gotCfg, err := dst.GetConfig(ctx)
	if err != nil || gotCfg.DefaultAdmission != model.AdmissionBypass {
		t.Fatalf("imported Config.DefaultAdmission = %v (%v), want bypass", gotCfg.DefaultAdmission, err)
	}
	gotRepo, err := dst.GetRepoConfig(ctx, widgets)
	if err != nil || gotRepo == nil || gotRepo.DefaultAdmission != model.AdmissionDurable {
		t.Fatalf("imported RepoConfig.DefaultAdmission = %+v (%v), want durable", gotRepo, err)
	}
	gotTmpl, err := dst.GetTemplate(ctx, "tmpl-steward")
	if err != nil || gotTmpl == nil || gotTmpl.Admission != model.AdmissionDurable {
		t.Fatalf("imported Template.Admission = %+v (%v), want durable", gotTmpl, err)
	}
	gotSched, err := dst.GetSchedule(ctx, "sched-bypass")
	if err != nil || gotSched == nil || gotSched.Admission != model.AdmissionBypass {
		t.Fatalf("imported Schedule.Admission = %+v (%v), want bypass", gotSched, err)
	}
}

func TestOneReleaseStateRepoImportFoldsRetiredAdmissionGrantsAndWarns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src, srcDB := openDB(t)

	disabled := false
	cfg := model.DefaultConfig()
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Catalogue: &model.CapabilityCatalogue{
			InProcess: map[string]model.CatalogueInProcess{
				"read-issues": {Enabled: &disabled},
			},
		},
	}
	if err := src.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := src.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets}); err != nil {
		t.Fatal(err)
	}
	if err := src.PutTemplate(ctx, model.Template{
		ID:        "tmpl-old",
		Name:      "Old",
		Title:     "Old",
		Body:      "body",
		Grants:    []model.Grant{{Capability: "read-issues", Via: model.GrantByGrain}},
		CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := staterepo.Export(ctx, srcDB, dir); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}

	// Simulate a current-schema grain/config PR still spelling the retired grants.
	capPath := filepath.Join(dir, filepath.FromSlash(staterepo.CapabilitiesPath))
	if err := os.WriteFile(capPath, []byte(`defaults:
  task:
    - priority-dispatch
    - read-issues
  repos:
    acme/widgets:
      task:
        - priority-dispatch
        - durable
in_process:
  priority-dispatch:
    enabled: false
  read-issues:
    enabled: true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	tmplPath := filepath.Join(dir, filepath.FromSlash(staterepo.DumpPath("template")))
	rawTmpl, err := os.ReadFile(tmplPath)
	if err != nil {
		t.Fatal(err)
	}
	updatedTmpl := strings.Replace(string(rawTmpl),
		"      - capability: read-issues\n",
		"      - capability: priority-dispatch\n        via: grain\n        folder: null\n      - capability: read-issues\n", 1)
	if updatedTmpl == string(rawTmpl) {
		t.Fatalf("could not splice priority-dispatch into %s:\n%s", tmplPath, rawTmpl)
	}
	if err := os.WriteFile(tmplPath, []byte(updatedTmpl), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := staterepo.Check(ctx, srcDB, dir, model.SchemaVersion)
	if err != nil {
		t.Fatalf("Check failed on one-release admission grants: %v", err)
	}
	for _, needle := range []string{"in_process.priority-dispatch", "defaults.task", "defaults.repos[acme/widgets].task", "tmpl-old"} {
		if !slices.ContainsFunc(report.Warnings, func(w string) bool { return strings.Contains(w, needle) }) {
			t.Errorf("Check warnings = %v, missing warning mentioning %q", report.Warnings, needle)
		}
	}

	dst, dstDB := openDB(t)
	if err := staterepo.ImportTables(ctx, dstDB, dir, staterepo.SettingsTables); err != nil {
		t.Fatalf("ImportTables: %v", err)
	}
	gotCfg, err := dst.GetConfig(ctx)
	if err != nil || gotCfg.DefaultAdmission != model.AdmissionBypass || !slices.Equal(gotCfg.DefaultCapabilities, []string{"read-issues"}) {
		t.Errorf("Config = DefaultAdmission:%s DefaultCapabilities:%v (%v), want bypass and [read-issues]",
			gotCfg.DefaultAdmission, gotCfg.DefaultCapabilities, err)
	}
	if _, ok := gotCfg.CapabilityPolicy.Catalogue.InProcess["priority-dispatch"]; ok {
		t.Error("imported catalogue kept priority-dispatch")
	}
	gotRepo, err := dst.GetRepoConfig(ctx, widgets)
	if err != nil || gotRepo == nil || gotRepo.DefaultAdmission != model.AdmissionDurable || len(gotRepo.DefaultCapabilities) != 0 {
		t.Errorf("RepoConfig = %+v (%v), want DefaultAdmission=durable and DefaultCapabilities=[]", gotRepo, err)
	}
	gotTmpl, err := dst.GetTemplate(ctx, "tmpl-old")
	if err != nil || gotTmpl == nil || gotTmpl.Admission != model.AdmissionBypass || len(gotTmpl.Grants) != 1 || gotTmpl.Grants[0].Capability != "read-issues" {
		t.Errorf("Template = %+v (%v), want Admission=bypass and [read-issues]", gotTmpl, err)
	}
}
