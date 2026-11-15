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

	"github.com/ai-on-gke/grain/pkg/model"
)

// The security review prompt keeps reviews cheap: it sends the reviewer to the
// change's own CI check statuses rather than re-running test suites or waiting
// on CI, while still permitting a narrowly targeted test to confirm a finding
// or check a pushed fix.
func TestSecurityReviewTemplateCarriesTestingAndCIGuidance(t *testing.T) {
	t.Parallel()
	tmpl := model.SystemTemplate(model.SecurityReviewTemplateID, model.RepoRef{})
	if tmpl == nil {
		t.Fatal("this build ships no security review template")
	}
	for _, want := range []string{
		"Testing and CI",
		"GitHub Actions or grain CI run is the test signal",
		"grain pr open",
		"check statuses",
		"grain ci checks <sha>",
		"Do not run `go test ./...`",
		"-race",
		"CI re-runs on the new head and the merge queue gates on it",
		"Do not call `grain ci wait`",
		"grain will not merge a head whose CI has not passed",
		"narrowly targeted test (`go test ./pkg/x -run TestName`)",
		"closing note",
		"`go build ./...` or `go vet`",
	} {
		if !strings.Contains(tmpl.Body, want) {
			t.Errorf("security review template body missing %q:\n%s", want, tmpl.Body)
		}
	}
}

// Security reviews work in place on the reviewed task's own branch and never
// run grain CI of their own; orchestrator.newReview attaches the narrowest
// repo-scoped read-only api:ci-read@<repo> grant per target repository instead
// of the run-triggering grain#ci grant.
func TestSecurityReviewTemplateDoesNotGrantGrainCI(t *testing.T) {
	t.Parallel()
	tmpl := model.SystemTemplate(model.SecurityReviewTemplateID, model.RepoRef{})
	if tmpl == nil {
		t.Fatal("this build ships no security review template")
	}
	if model.HasGrainCI(capabilityNames(tmpl.Grants)) {
		t.Errorf("security review template grants = %+v, want no grain#ci grant", tmpl.Grants)
	}
}
