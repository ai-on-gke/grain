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

package ui_test

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestSetRepoIgnoreGitHubCIStoresTheSwitch(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	// Off is the default, and a repo with no row of its own is in it.
	read, err := c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if read.IgnoreGitHubCI {
		t.Fatal("ignoreGitHubCI = true for a repo nobody has configured")
	}

	got, err := c.SetRepoIgnoreGitHubCI(ctx, "acme/widgets", true)
	if err != nil {
		t.Fatalf("turning ignore-github-ci on: %v", err)
	}
	if !got.IgnoreGitHubCI {
		t.Fatal("ignoreGitHubCI = false after turning it on")
	}
	if read, err = c.RepoDefaults(ctx, "acme/widgets"); err != nil {
		t.Fatal(err)
	}
	if !read.IgnoreGitHubCI {
		t.Fatal("ignoreGitHubCI = false on a re-read, want the stored switch")
	}

	if got, err = c.SetRepoIgnoreGitHubCI(ctx, "acme/widgets", false); err != nil {
		t.Fatalf("turning ignore-github-ci off: %v", err)
	}
	if got.IgnoreGitHubCI {
		t.Fatal("ignoreGitHubCI = true after turning it off")
	}
}

func TestIgnoreGitHubCISurvivesTheOtherRepoSaves(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	if _, err := c.SetRepoIgnoreGitHubCI(ctx, "acme/widgets", true); err != nil {
		t.Fatal(err)
	}
	got, err := c.SetRepoSetupCommand(ctx, "acme/widgets", "make deps")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IgnoreGitHubCI {
		t.Fatal("ignoreGitHubCI = false after saving a setup command, want it untouched")
	}
	if got, err = c.SetRepoDeclarations(ctx, "acme/widgets", true); err != nil {
		t.Fatal(err)
	}
	if !got.IgnoreGitHubCI {
		t.Fatal("ignoreGitHubCI = false after saving declarations, want it untouched")
	}

	if got, err = c.SetRepoIgnoreGitHubCI(ctx, "acme/widgets", false); err != nil {
		t.Fatal(err)
	}
	if got.SetupCommand != "make deps" || !got.Declarations {
		t.Fatalf("defaults = %+v after flipping ignore-github-ci, want the rest untouched", got)
	}
}

func TestRepoIgnoreGitHubCIGetAndPut(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/ignore-github-ci",
		`{"ignoreGitHubCI":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200: %s", rec.Code, rec.Body)
	}
	saved := decode[ui.RepoDefaults](t, rec)
	if saved.Repo != "acme/widgets" || !saved.IgnoreGitHubCI {
		t.Fatalf("put response = %+v, want acme/widgets with ignore-github-ci on", saved)
	}

	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/ignore-github-ci", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoDefaults](t, rec); !reflect.DeepEqual(got, saved) {
		t.Fatalf("get = %+v, want %+v", got, saved)
	}
	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/capabilities", "")
	if got := decode[ui.RepoDefaults](t, rec); !got.IgnoreGitHubCI {
		t.Fatal("the capabilities route reports ignoreGitHubCI = false, want the stored switch")
	}

	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/ignore-github-ci",
		`{"ignoreGitHubCI":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoDefaults](t, rec); got.IgnoreGitHubCI {
		t.Fatal("ignoreGitHubCI = true after turning it off")
	}
}
