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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestSuiteQualificationDefaultsAreTheReleaseQualificationSuites(t *testing.T) {
	got := model.SuiteQualification{}.WithDefaults()
	want := model.SuiteQualification{
		Check:          model.ReleaseQualificationCheckName,
		Candidate:      model.CandidateRCTags,
		Stage1Workflow: model.ReleaseQualificationStage1Workflow,
	}
	if got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
	synced := model.SuiteQualification{Candidate: model.CandidateSyncedRelease, Stage1Status: "ci/edition"}.WithDefaults()
	if synced.Upstream != model.CoreModulePath || synced.Stage1Workflow != "" || synced.Stage1Status != "ci/edition" {
		t.Fatalf("synced-release defaults = %+v", synced)
	}
	if repo, err := synced.UpstreamRepo(); err != nil || repo != (model.RepoRef{Owner: "ai-on-gke", Name: "grain"}) {
		t.Fatalf("UpstreamRepo = %v, %v", repo, err)
	}
	if got := synced.Stage1Name(); got != "ci/edition" {
		t.Errorf("Stage1Name = %q, want the status context", got)
	}
}

func TestSuiteQualificationValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		q    model.SuiteQualification
		bad  string
	}{
		{name: "empty", q: model.SuiteQualification{}},
		{name: "synced", q: model.SuiteQualification{Candidate: model.CandidateSyncedRelease, Upstream: "example.com/acme/core"}},
		{name: "unknown candidate", q: model.SuiteQualification{Candidate: "nightly"}, bad: "candidate"},
		{name: "both stage1s", q: model.SuiteQualification{Stage1Workflow: "nightly", Stage1Status: "ci"}, bad: "not both"},
		{name: "upstream with rc tags", q: model.SuiteQualification{Upstream: "example.com/acme/core"}, bad: "only read"},
		{name: "upstream not a repo", q: model.SuiteQualification{Candidate: model.CandidateSyncedRelease, Upstream: "example.com/core"}, bad: "repository"},
		{name: "two-line check", q: model.SuiteQualification{Check: "a\nb"}, bad: "one line"},
	} {
		err := tc.q.Validate()
		switch {
		case tc.bad == "" && err != nil:
			t.Errorf("%s: Validate = %v, want nil", tc.name, err)
		case tc.bad != "" && (err == nil || !strings.Contains(err.Error(), tc.bad)):
			t.Errorf("%s: Validate = %v, want an error about %q", tc.name, err, tc.bad)
		}
	}
}

// The default stem's tag is the one runs were always tagged with, so a
// candidate mid-qualification across the upgrade keeps its attempts.
func TestQualificationTagOfTheDefaultSuiteIsUnchanged(t *testing.T) {
	repo := model.RepoRef{Owner: "Acme", Name: "Widgets"}
	if got, want := model.QualificationTag(model.ReleaseQualificationSuiteStem, repo, "v1.0.0-rc.1"),
		model.ReleaseQualificationTag(repo, "v1.0.0-rc.1"); got != want {
		t.Fatalf("QualificationTag = %q, want %q", got, want)
	}
	if got := model.QualificationTag("edition-qualification", repo, "v1.0.0@abc"); got != "edition-qualification:acme/widgets@v1.0.0@abc" {
		t.Fatalf("QualificationTag = %q", got)
	}
	base := model.ReleaseQualificationTag(repo, "v1.0.0-rc.1")
	if got := model.QualificationAttemptTag(base, 1); got != base {
		t.Fatalf("QualificationAttemptTag(1) = %q, want %q", got, base)
	}
	if got := model.QualificationAttemptTag(base, 2); got != base+"#2" {
		t.Fatalf("QualificationAttemptTag(2) = %q, want %q", got, base+"#2")
	}
}

func TestGoModPin(t *testing.T) {
	const core = "example.com/acme/core"
	for _, tc := range []struct {
		name, gomod, want string
		ok                bool
	}{
		{name: "required", gomod: "module example.com/acme/edition\n\ngo 1.26\n\nrequire (\n\texample.com/acme/core v1.4.0\n\tgolang.org/x/mod v0.41.0\n)\n", want: "v1.4.0", ok: true},
		{name: "pseudo", gomod: "module m\n\nrequire example.com/acme/core v1.4.1-0.20261105120000-0123456789ab\n", want: "v1.4.1-0.20261105120000-0123456789ab", ok: true},
		{name: "replaced by a version", gomod: "module m\n\nrequire example.com/acme/core v1.3.0\n\nreplace example.com/acme/core => example.com/acme/core v1.4.0\n", want: "v1.4.0", ok: true},
		{name: "replaced by a directory", gomod: "module m\n\nrequire example.com/acme/core v1.3.0\n\nreplace example.com/acme/core => ../core\n"},
		{name: "replaced by a fork", gomod: "module m\n\nrequire example.com/acme/core v1.3.0\n\nreplace example.com/acme/core => example.com/fork/core v1.4.0\n"},
		{name: "not required", gomod: "module m\n\nrequire golang.org/x/mod v0.41.0\n"},
		{name: "not go.mod", gomod: "{{{"},
	} {
		got, ok := model.GoModPin([]byte(tc.gomod), core)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: GoModPin = %q, %v; want %q, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestPinnedRelease(t *testing.T) {
	releases := map[string]string{
		"v1.3.0":      strings.Repeat("3", 40),
		"v1.4.0":      "0123456789ab" + strings.Repeat("0", 28),
		"v1.4.0-rc.1": "fedcba987654" + strings.Repeat("0", 28),
	}
	for _, tc := range []struct {
		version, want string
		ok            bool
	}{
		{version: "v1.4.0", want: "v1.4.0", ok: true},
		{version: "v1.4.1-0.20261105120000-0123456789ab", want: "v1.4.0", ok: true},
		{version: "v1.4.0-rc.1"},
		{version: "v1.4.0-rc.1.0.20261105120000-fedcba987654"},
		{version: "v1.4.1-0.20261106120000-aaaaaaaaaaaa"},
	} {
		got, ok := model.PinnedRelease(tc.version, releases)
		if got != tc.want || ok != tc.ok {
			t.Errorf("PinnedRelease(%s) = %q, %v; want %q, %v", tc.version, got, ok, tc.want, tc.ok)
		}
	}
}

// The stanza is the declaration's: written by the sync beside the suite,
// replaced by the next, and gone with the suite's file.
func TestSyncRepoDeclarationsKeepsTheQualificationStanza(t *testing.T) {
	store, ctx := open(t)
	repo := model.RepoRef{Owner: "acme", Name: "edition"}
	tmplID := model.RepoDeclaredID(model.DeclaredTemplate, repo, "smoke")
	suiteID := model.RepoDeclaredID(model.DeclaredSuite, repo, "edition-qualification")
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	sync := func(q *model.SuiteQualification, withSuite bool) {
		t.Helper()
		declared := model.RepoDeclarations{Templates: []model.DeclaredTemplateFile{{
			Path:     model.TemplateDeclarationDir + "/smoke.yml",
			Template: model.Template{ID: tmplID, Name: "smoke", Title: "Smoke", Body: "Smoke it."},
		}}}
		if withSuite {
			declared.Suites = []model.DeclaredSuiteFile{{
				Path: model.SuiteDeclarationDir + "/edition-qualification.yml",
				Suite: model.Suite{ID: suiteID, Name: "Edition qualification",
					Items: []model.SuiteItem{{TemplateID: tmplID}}, Mode: model.SuiteUntilClean, MaxPasses: 1},
				Qualification: q,
			}}
		}
		if _, err := store.SyncRepoDeclarations(ctx, repo, strings.Repeat("a", 40), declared, now, time.UTC); err != nil {
			t.Fatalf("SyncRepoDeclarations: %v", err)
		}
	}
	stanzas := func() map[string]model.SuiteQualification {
		t.Helper()
		got, err := store.SuiteQualifications(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	want := model.SuiteQualification{Check: "grain/edition-qualification", Candidate: model.CandidateSyncedRelease,
		Stage1Status: "grain/edition-qualification"}
	sync(&want, true)
	if got := stanzas(); len(got) != 1 || got[suiteID] != want {
		t.Fatalf("after the first sync = %+v, want %s: %+v", got, suiteID, want)
	}
	sync(nil, true)
	if got := stanzas(); len(got) != 0 {
		t.Fatalf("after the stanza was removed = %+v, want none", got)
	}
	sync(&want, true)
	sync(nil, false)
	if got := stanzas(); len(got) != 0 {
		t.Fatalf("after the suite was withdrawn = %+v, want none", got)
	}
}
