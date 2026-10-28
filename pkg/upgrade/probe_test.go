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

package upgrade

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// probeImageUpgrader is an image-pipeline upgrader whose ref file names
// running, and whose registry answers digest for every tag it is asked
// about (recording which).
func probeImageUpgrader(t *testing.T, running, digest string, lookupErr error) (*Upgrader, *[]string) {
	t.Helper()
	refFile := filepath.Join(t.TempDir(), "image.env")
	if running != "" {
		if err := os.WriteFile(refFile, []byte(imageRefEnvKey+"="+running+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var asked []string
	u := newUpgrader(t, Config{
		Image: &ImageConfig{Repository: "ghcr.io/bwsalmon/grain/grain", RefFile: refFile},
		ManifestDigest: func(_ context.Context, ref string) (string, error) {
			asked = append(asked, ref)
			return digest, lookupErr
		},
	})
	return u, &asked
}

func TestAlreadyRunningComparesTheImageDigestWithoutPulling(t *testing.T) {
	const repo = "ghcr.io/bwsalmon/grain/grain"
	for name, tc := range map[string]struct {
		running, digest string
		err             error
		want            bool
		wantErr         bool
	}{
		"same digest":            {running: repo + ":main@" + testDigest, digest: testDigest, want: true},
		"moved":                  {running: repo + ":main@" + testDigest, digest: "sha256:feed"},
		"unpinned ref":           {running: repo + ":main", digest: testDigest},
		"another repository":     {running: "ghcr.io/someone/else:main@" + testDigest, digest: testDigest},
		"registry unreachable":   {running: repo + ":main@" + testDigest, err: errors.New("dial tcp: refused"), wantErr: true},
		"no digest in answer":    {running: repo + ":main@" + testDigest, digest: "", wantErr: true},
		"no ref file to compare": {running: "", digest: testDigest, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			u, asked := probeImageUpgrader(t, tc.running, tc.digest, tc.err)
			got, err := u.AlreadyRunning(t.Context(), "main")
			if (err != nil) != tc.wantErr {
				t.Fatalf("AlreadyRunning: err = %v, want error %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("AlreadyRunning = %v, want %v", got, tc.want)
			}
			if got && (len(*asked) != 1 || (*asked)[0] != repo+":main") {
				t.Errorf("the registry was asked about %v, want [%s:main]", *asked, repo)
			}
		})
	}
}

func TestAlreadyRunningRefusesABranchItWouldNotUpgradeOnto(t *testing.T) {
	u, asked := probeImageUpgrader(t, "ghcr.io/bwsalmon/grain/grain:main@"+testDigest, testDigest, nil)
	u.cfg.AllowedBranches = []string{"main"}
	if got, err := u.AlreadyRunning(t.Context(), "grain/task-1"); err == nil || got {
		t.Errorf("AlreadyRunning on a branch not allowed = %v, %v; want false and an error", got, err)
	}
	if len(*asked) != 0 {
		t.Errorf("the registry was asked about %v", *asked)
	}
}

// RegistryManifestDigest is a HEAD of the tag's manifest, through the
// same anonymous token challenge a tag list takes, reading the digest
// off the answer's Docker-Content-Digest.
func TestRegistryManifestDigest(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			w.Write([]byte(`{"token":"anon"}`))
		case "/v2/owner/grain/manifests/main":
			if r.Method != http.MethodHead {
				t.Errorf("manifest asked for with %s, want HEAD", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer anon" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="reg",scope="repository:owner/grain:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") {
				t.Errorf("Accept = %q, want the index type among them", r.Header.Get("Accept"))
			}
			w.Header().Set("Docker-Content-Digest", testDigest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	saved := registryClient
	registryClient = srv.Client()
	defer func() { registryClient = saved }()

	host := strings.TrimPrefix(srv.URL, "https://")
	digest, err := RegistryManifestDigest(context.Background(), host+"/owner/grain:main")
	if err != nil {
		t.Fatal(err)
	}
	if digest != testDigest {
		t.Errorf("digest = %q, want %q", digest, testDigest)
	}
	if _, err := RegistryManifestDigest(context.Background(), host+"/owner/grain:gone"); err == nil {
		t.Error("a tag the registry does not have answered a digest")
	}
}

// gitLsRemote reads origin's branch without fetching it.
func TestGitLsRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	origin, clone := filepath.Join(dir, "origin"), filepath.Join(dir, "clone")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet", "-b", "main", origin)
	git("-C", origin, "commit", "--quiet", "--allow-empty", "-m", "one")
	git("clone", "--quiet", origin, clone)
	git("-C", origin, "commit", "--quiet", "--allow-empty", "-m", "two")
	want := git("-C", origin, "rev-parse", "HEAD")

	got, err := gitLsRemote(t.Context(), clone, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("gitLsRemote = %q, want origin's %q (not the clone's own)", got, want)
	}
	if _, err := gitLsRemote(t.Context(), clone, "nope"); err == nil {
		t.Error("a branch origin does not have answered a commit")
	}
}
