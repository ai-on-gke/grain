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

package model_test

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestParseAndFormatContextRef(t *testing.T) {
	t.Parallel()
	target := &model.RepoRef{Owner: "acme", Name: "widgets"}

	cases := []struct {
		raw      string
		wantRepo model.RepoRef
		wantPath string
		wantRev  string
		wantRel  string
	}{
		{
			raw:      "design/spec/external-cli/external-cli.spec.md",
			wantPath: "design/spec/external-cli/external-cli.spec.md",
			wantRel:  "design/spec/external-cli/external-cli.spec.md",
		},
		{
			raw:      "./design/plc/042/@ABCDEF1234567890",
			wantPath: "design/plc/042",
			wantRev:  "abcdef1234567890",
			wantRel:  "design/plc/042@abcdef1234567890",
		},
		{
			raw:      "acme/widgets:design/plc/042@main",
			wantRepo: model.RepoRef{Owner: "acme", Name: "widgets"},
			wantPath: "design/plc/042",
			wantRev:  "main",
			wantRel:  "design/plc/042@main",
		},
		{
			raw:      "acme/context:specs/plc/042@0123456789abcdef0123456789abcdef01234567",
			wantRepo: model.RepoRef{Owner: "acme", Name: "context"},
			wantPath: "specs/plc/042",
			wantRev:  "0123456789abcdef0123456789abcdef01234567",
			wantRel:  "acme/context:specs/plc/042@0123456789abcdef0123456789abcdef01234567",
		},
		{
			raw:      "git.example.com:8443/acme/context:specs/plc/042@release/v1",
			wantRepo: model.RepoRef{Host: "git.example.com:8443", Owner: "acme", Name: "context"},
			wantPath: "specs/plc/042",
			wantRev:  "release/v1",
			wantRel:  "git.example.com:8443/acme/context:specs/plc/042@release/v1",
		},
		{
			raw:      "git@github.com:acme/context:specs/plc/042",
			wantRepo: model.RepoRef{Owner: "acme", Name: "context"},
			wantPath: "specs/plc/042",
			wantRel:  "acme/context:specs/plc/042",
		},
	}
	for _, tc := range cases {
		got, err := model.ParseContextRef(tc.raw)
		if err != nil {
			t.Fatalf("ParseContextRef(%q): %v", tc.raw, err)
		}
		if got.Repo != tc.wantRepo || got.Path != tc.wantPath || got.Rev != tc.wantRev {
			t.Fatalf("ParseContextRef(%q) = %+v, want repo=%+v path=%q rev=%q", tc.raw, got, tc.wantRepo, tc.wantPath, tc.wantRev)
		}
		if rel := got.StringRelativeTo(target); rel != tc.wantRel {
			t.Fatalf("(%q).StringRelativeTo(target) = %q, want %q", tc.raw, rel, tc.wantRel)
		}
	}

	reject := []string{
		"",
		"   ",
		"/etc/passwd",
		"design/../secret",
		"design/plc/042@",
		"design/plc/042@-upload-pack=evil",
		"design/plc/042@HEAD~1",
		"design/plc/042@a..b",
		"badrepo:design/plc/042",
		":design/plc/042",
		"acme/context:./",
		"acme/context:-flag/file",
		"design/plc\n042",
	}
	for _, raw := range reject {
		if got, err := model.ParseContextRef(raw); err == nil {
			t.Errorf("ParseContextRef(%q) = %+v, want error", raw, got)
		}
	}
}

func TestParseContextManifestAndTaskAttachment(t *testing.T) {
	t.Parallel()
	manifestYAML := []byte(`
title: Checkout Flow Spec
description: Approved product brief and HTML wireframe
files:
  - path: brief.md
    kind: product-brief
    title: Product Brief
    source_url: https://specs.example.com/plc/042
  - path: mocks/checkout.html
    kind: mock-html
    title: Checkout Wireframe
`)
	m, err := model.ParseContextManifest(manifestYAML)
	if err != nil {
		t.Fatalf("ParseContextManifest: %v", err)
	}
	if m.Title != "Checkout Flow Spec" || len(m.Files) != 2 || m.Files[0].SourceURL != "https://specs.example.com/plc/042" {
		t.Fatalf("manifest = %+v", m)
	}

	if _, err := model.ParseContextManifest([]byte("files:\n  - path: ../escape.md\n")); err == nil {
		t.Fatal("expected traversal path in context.yml to fail")
	}
	if _, err := model.ParseContextManifest([]byte("files:\n  - path: ok.md\n    source_url: javascript:alert(1)\n")); err == nil {
		t.Fatal("expected non-http(s) source_url in context.yml to fail")
	}

	target := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := model.Task{
		Target: &target,
		Links:  []model.Link{{Kind: model.LinkDesign, Target: "design/plc/042@0123456"}},
	}
	fullSHA := "0123456789abcdef0123456789abcdef01234567"
	changed := model.SetTaskContextManifest(&task, model.ContextRef{Path: "design/plc/042", Rev: fullSHA}, m)
	if !changed {
		t.Fatal("expected SetTaskContextManifest to report change")
	}
	refs := task.ContextRefs()
	if len(refs) != 1 || refs[0].Rev != fullSHA || refs[0].Manifest == nil || refs[0].Manifest.Title != "Checkout Flow Spec" {
		t.Fatalf("ContextRefs() = %+v", refs)
	}
}

func TestInheritContextAcrossSameAndCrossRepoChildren(t *testing.T) {
	t.Parallel()
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	specs := model.RepoRef{Owner: "acme", Name: "specs"}
	docs := model.RepoRef{Owner: "acme", Name: "docs"}
	sha1 := "1111111111111111111111111111111111111111"
	sha2 := "2222222222222222222222222222222222222222"

	parent := model.Task{
		Target: &widgets,
		Reads:  []model.RepoRef{specs},
		Links: []model.Link{
			{Kind: model.LinkDesign, Target: "design/plc/042@" + sha1},
			{Kind: model.LinkDesign, Target: "acme/specs:plc/shared@" + sha2},
		},
	}
	model.SetTaskContextManifest(&parent, model.ContextRef{Path: "design/plc/042", Rev: sha1}, &model.ContextManifest{
		Title: "PLC 042",
		Files: []model.ContextManifestFile{{Path: "spec.md", Kind: "design-doc"}},
	})

	// Same-repo review child inherits both refs and gets -read acme/specs (not acme/widgets).
	review := model.Task{Target: &widgets}
	model.InheritContext(&review, parent)
	if len(review.Reads) != 1 || !review.Reads[0].Same(specs) {
		t.Fatalf("same-repo child Reads = %v, want [%s]", review.Reads, specs)
	}
	reviewRefs := review.ContextRefs()
	if len(reviewRefs) != 2 || reviewRefs[0].Manifest == nil || reviewRefs[0].Manifest.Title != "PLC 042" {
		t.Fatalf("same-repo child ContextRefs = %+v", reviewRefs)
	}

	// Cross-repo child qualifies parent's target-repo context and grants -read for both acme/widgets and acme/specs.
	cross := model.Task{Target: &docs}
	model.InheritContext(&cross, parent)
	if ungranted := model.UngrantedReads(cross, parent); len(ungranted) != 0 {
		t.Fatalf("cross-repo inherited reads should be permitted by parent: ungranted=%v", ungranted)
	}
	if len(cross.Reads) != 2 {
		t.Fatalf("cross-repo child Reads = %v, want 2 repos", cross.Reads)
	}
	crossRefs := cross.ContextRefs()
	if len(crossRefs) != 2 || crossRefs[0].StringRelativeTo(cross.Target) != "acme/widgets:design/plc/042@"+sha1 {
		t.Fatalf("cross-repo child ContextRefs = %+v", crossRefs)
	}

	// Child filed with its own context keeps only its own.
	explicit := model.Task{
		Target: &widgets,
		Links:  []model.Link{{Kind: model.LinkDesign, Target: "design/other@3333333"}},
	}
	model.InheritContext(&explicit, parent)
	if refs := explicit.ContextRefs(); len(refs) != 1 || refs[0].Path != "design/other" {
		t.Fatalf("explicit child ContextRefs = %+v, want only its own", refs)
	}
}

func TestConfigAutoGrantContextReadsRoundTrip(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()

	normalized, err := model.NormalizeAutoGrantContextReads([]string{" acme/specs ", "ACME/specs"})
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized) != 1 || normalized[0] != "acme/specs" {
		t.Fatalf("normalized = %v", normalized)
	}
	if !model.ContextReadAutoGranted(normalized, model.RepoRef{Owner: "Acme", Name: "Specs"}) {
		t.Fatal("expected case-insensitive repo match")
	}
	if model.ContextReadAutoGranted(normalized, model.RepoRef{Owner: "acme", Name: "other"}) {
		t.Fatal("unexpected match for non-configured repo")
	}

	cfg := model.DefaultConfig()
	cfg.AutoGrantContextReads = normalized
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if len(got.AutoGrantContextReads) != 1 || got.AutoGrantContextReads[0] != "acme/specs" {
		t.Fatalf("GetConfig().AutoGrantContextReads = %v, want [acme/specs]", got.AutoGrantContextReads)
	}
}
