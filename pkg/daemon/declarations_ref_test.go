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
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// RepoDeclarationSource.Ref is what a git_ref schedule watches with: an
// annotated tag reads as the commit it points at, a ref whose name only
// ends the same way is not the ref, and a ref that is not there is "".
func TestRepoDeclarationSourceRefPeelsTagsAndMatchesExactly(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	gitRoot := t.TempDir()
	repoDir := filepath.Join(gitRoot, "upstream", "core.git")
	gitIn(t, gitRoot, "init", "-q", "--bare", repoDir)
	gitIn(t, repoDir, "symbolic-ref", "HEAD", "refs/heads/main")

	seed := filepath.Join(t.TempDir(), "seed")
	gitIn(t, gitRoot, "clone", "-q", repoDir, seed)
	gitIn(t, seed, "config", "user.email", "test@example.com")
	gitIn(t, seed, "config", "user.name", "test")
	gitIn(t, seed, "checkout", "-q", "-b", "main")
	commitFile := func(name string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(seed, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, seed, "add", ".")
		gitIn(t, seed, "commit", "-q", "-m", "add "+name)
		out, err := exec.Command("git", "-C", seed, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	first := commitFile("one")
	gitIn(t, seed, "tag", "-a", "-m", "release", "latest")
	// A branch whose name ends in the tag's whole ref name: ls-remote's
	// pattern matches it too, and it must not be read as the tag.
	second := commitFile("two")
	gitIn(t, seed, "branch", "x/refs/tags/latest")
	gitIn(t, seed, "push", "-q", "origin", "main", "x/refs/tags/latest", "refs/tags/latest")

	const token = "secret-ref-token"
	upstream := httptest.NewServer(gitHTTPBackendWithAuth(gitRoot, token))
	defer upstream.Close()
	src := RepoDeclarationSource{Clone: CiWorkflowSource{
		GitHubHost:   strings.TrimPrefix(upstream.URL, "http://"),
		InsecureHTTP: true,
		TempDir:      t.TempDir(),
		TeamCredential: func(context.Context, model.RepoRef) (string, error) {
			return token, nil
		},
	}}
	ctx := context.Background()
	repo := model.RepoRef{Owner: "upstream", Name: "core"}

	got, err := src.Ref(ctx, repo, "refs/tags/latest")
	if err != nil {
		t.Fatalf("Ref(refs/tags/latest): %v", err)
	}
	if got != first {
		t.Errorf("Ref(refs/tags/latest) = %q, want the tagged commit %q (not the tag object, nor %q)", got, first, second)
	}
	if got, err := src.Ref(ctx, repo, "refs/heads/main"); err != nil || got != second {
		t.Errorf("Ref(refs/heads/main) = %q, %v; want %q", got, err, second)
	}
	if got, err := src.Ref(ctx, repo, "refs/tags/nope"); err != nil || got != "" {
		t.Errorf("Ref(refs/tags/nope) = %q, %v; want \"\" and no error", got, err)
	}
	if _, err := src.Ref(ctx, repo, "--upload-pack=evil"); err == nil {
		t.Error("Ref accepted something that is not a full ref name")
	}
}
