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

package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// A deployment whose -github-host is its own forge (GitHub Enterprise, or
// the walkthrough's loopback double) reads an external config repository's
// pull requests from that forge, so an approved merge verifies there as it
// does on github.com. Before, every remote off github.com had no Reviews
// at all, and every approved or grain-merged commit was held with "grain
// cannot read this remote's pull requests"
// (docs/test-runs/2026-11-11-external-config-verify.md, F1).
func TestRequireVerifiedReadsReviewsFromTheDeploymentsForge(t *testing.T) {
	merge, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/grain-config/commits/" + merge + "/pulls":
			fmt.Fprintf(w, `[{"number":7,"merged_at":"2026-11-11T20:00:00Z","merge_commit_sha":%q,"base":{"ref":"main"}}]`, merge)
		case "/repos/acme/grain-config/pulls/7":
			fmt.Fprintf(w, `{"number":7,"merged":true,"merge_commit_sha":%q,"user":{"login":"bob"},"head":{"sha":%q}}`, merge, head)
		case "/repos/acme/grain-config/pulls/7/reviews":
			fmt.Fprintf(w, `[{"state":"APPROVED","commit_id":%q,"author_association":"MEMBER","user":{"login":"carol"}}]`, head)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	prev := stateRepoForge.Load()
	t.Cleanup(func() { stateRepoForge.Store(prev) })

	dir := t.TempDir()
	settings := staterepo.Settings{Remote: "http://" + host + "/acme/grain-config.git", RequireVerified: true}

	setStateRepoForge(host, true)
	v := stateRepoVerifier(dir, settings)
	if v == nil || v.Reviews == nil {
		t.Fatalf("stateRepoVerifier on the deployment's own forge = %+v, want one that reads its pull requests", v)
	}
	review, err := v.Reviews.MergedReview(context.Background(), merge)
	if err != nil || review == nil || review.Number != 7 || !reflect.DeepEqual(review.Approvers, []string{"carol"}) {
		t.Fatalf("MergedReview = %+v, %v; want #7 approved by carol", review, err)
	}
	// What the State pane's forgeReviews reports.
	if forgeReviewTransport(settings.Remote) == nil {
		t.Errorf("forgeReviewTransport(%s) = nil, want the deployment's forge", settings.Remote)
	}

	// Nothing is asked of a host the operator did not name, nor over
	// plain HTTP the operator did not allow.
	for _, c := range []struct {
		name, forge string
		insecure    bool
		remote      string
	}{
		{"default forge", "github.com", false, settings.Remote},
		{"another host", "forge.example.com", true, settings.Remote},
		{"plain http not allowed", host, false, settings.Remote},
		{"credentials in the URL", host, true, "http://x:y@" + host + "/acme/grain-config.git"},
		{"ssh remote", host, true, "ssh://git@" + host + "/acme/grain-config.git"},
	} {
		setStateRepoForge(c.forge, c.insecure)
		s := settings
		s.Remote = c.remote
		if v := stateRepoVerifier(dir, s); v == nil || v.Reviews != nil {
			t.Errorf("%s: stateRepoVerifier = %+v, want one without Reviews", c.name, v)
		}
	}
	// github.com itself is unchanged.
	setStateRepoForge(host, true)
	if v := stateRepoVerifier(dir, staterepo.Settings{Remote: "https://github.com/acme/grain-config.git", RequireVerified: true}); v == nil || v.Reviews == nil {
		t.Errorf("github.com remote: stateRepoVerifier = %+v, want Reviews", v)
	}
}
