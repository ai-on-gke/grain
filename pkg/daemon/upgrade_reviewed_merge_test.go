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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/reviewedmerge"
)

// TestReviewedMergeOnACommandBackedHostReadsThroughItsTool: with no
// -upgrade-reviewed-merge-token-file, a host/owner/name repository on a
// host only a host tool reaches (the made-up forgecli source,
// gitproxytest) is read through `forgecli api` against that host's
// /api/v3, and the policy keeps the repository whole.
func TestReviewedMergeOnACommandBackedHostReadsThroughItsTool(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, "forgecli")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Header.Get("X-Fake-ForgeCLI-Host")+" "+r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v3/repos/experimental/grain/git/commits/abc":
			fmt.Fprint(w, `{"parents":[{"sha":"p1"}]}`)
		case "/api/v3/repos/experimental/grain/compare/p1...abc":
			fmt.Fprint(w, `{"status":"ahead","merge_base_commit":{"sha":"p1"}}`)
		case "/api/v3/repos/experimental/grain/commits/abc/pulls":
			fmt.Fprint(w, `[]`)
		case "/api/v3/repos/experimental/grain/pulls/7/reviews":
			fmt.Fprint(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(fakeForgeCLIUpstreamEnv, srv.URL)

	repo := depotTestHost + "/experimental/grain"
	rc := upgradeReviewedMerge(Config{UpgradeReviewedMergeRepo: repo, UpgradeReviewedMergeLogins: []string{"grain-bot"}})
	if rc == nil {
		t.Fatal("no reviewed-merge check")
	}
	if rc.Policy.Repo != repo {
		t.Fatalf("policy repo = %q, want %q", rc.Policy.Repo, repo)
	}
	if rest, ok := rc.Forge.(reviewedmerge.REST); !ok || rest.Client == nil {
		t.Fatalf("forge = %#v, want REST over forgecli", rc.Forge)
	}
	ctx := context.Background()
	if parent, err := rc.Forge.FirstParent(ctx, repo, "abc"); err != nil || parent != "p1" {
		t.Fatalf("FirstParent = %q, %v", parent, err)
	}
	if cmp, err := rc.Compare(ctx, repo, "p1", "abc"); err != nil || cmp.Status != "ahead" {
		t.Fatalf("Compare = %+v, %v", cmp, err)
	}
	if pulls, err := rc.Forge.PullsForCommit(ctx, repo, "abc"); err != nil || len(pulls) != 0 {
		t.Fatalf("PullsForCommit = %+v, %v", pulls, err)
	}
	if reviews, err := rc.Forge.Reviews(ctx, repo, 7); err != nil || len(reviews) != 0 {
		t.Fatalf("Reviews = %+v, %v", reviews, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 4 {
		t.Fatalf("asked = %q, want four reads through forgecli", asked)
	}
	for _, a := range asked {
		if !strings.HasPrefix(a, depotTestHost+" ") {
			t.Errorf("%q was not sent by forgecli for %s", a, depotTestHost)
		}
	}
}

// TestReviewedMergeWithATokenFileOrOnGitHubUsesPlainHTTPS: a token file,
// or a host no command-backed source claims, keeps the plain client.
func TestReviewedMergeWithATokenFileOrOnGitHubUsesPlainHTTPS(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []Config{
		{UpgradeReviewedMergeRepo: depotTestHost + "/experimental/grain", UpgradeReviewedMergeTokenFile: tokenFile},
		{UpgradeReviewedMergeRepo: "example/grain"},
		{UpgradeReviewedMergeRepo: "git.example.test/example/grain"},
	} {
		rest, ok := upgradeReviewedMerge(cfg).Forge.(reviewedmerge.REST)
		if !ok || rest.Client != nil {
			t.Errorf("%+v: forge = %#v, want plain HTTPS", cfg.UpgradeReviewedMergeRepo, rest)
		}
		if cfg.UpgradeReviewedMergeTokenFile != "" && rest.Token != "tok" {
			t.Errorf("token = %q", rest.Token)
		}
	}
}
