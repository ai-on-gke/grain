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

package orchestrator

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// A category's own review checks reach the reviewer's brief, under the
// paths that put the change there, as things to report as findings.
func TestMatchListCarriesACategorysReviewChecks(t *testing.T) {
	got := matchList(trustboundary.Classify([]string{"scripts/release-images.sh", "pkg/relay/relay.go"}), true)
	i := strings.Index(got, "**release-machinery**")
	if i < 0 {
		t.Fatalf("no release-machinery entry:\n%s", got)
	}
	machinery := got[i:]
	for _, want := range []string{"`scripts/release-images.sh`", "report it as a finding", "`vX.Y.Z` or `latest`", "RELEASE_MAJOR"} {
		if !strings.Contains(machinery, want) {
			t.Errorf("release-machinery's entry does not say %q:\n%s", want, got)
		}
	}
	if j := strings.Index(got, "**credential-proxy**"); j < 0 || strings.Contains(got[j:i], "report it as a finding") {
		t.Errorf("a category with no review checks was given some:\n%s", got)
	}
}
