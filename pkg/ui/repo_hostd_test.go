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
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A repository names a default hostd (model.RepoConfig.DefaultHostd):
// set, normalized and cleared through its settings, reported on
// RepoDefaults, and a task filed against the repository starts pinned to
// it -- unless the filing or its template names a hostd, either of which
// wins.
func TestRepoDefaultHostdPinsTheTasksFiledAgainstIt(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	registerHostds(t, store, model.Hostd{Name: "gpu-box", Labels: map[string]string{"gpu": "yes", "zone": "b"}}, model.Hostd{Name: "laptop"})
	const repo = "acme/widgets"

	for _, bad := range []string{"Not A Hostd", "nowhere", "gone"} {
		if _, err := c.SetRepoSettings(ctx, repo, ui.SetRepoSettingsRequest{DefaultHostd: ptr(bad)}); err == nil {
			t.Errorf("SetRepoSettings accepted default hostd %q", bad)
		}
	}
	defaults, err := c.SetRepoSettings(ctx, repo, ui.SetRepoSettingsRequest{DefaultHostd: ptr("zone=b, gpu=yes")})
	if err != nil {
		t.Fatalf("setting the repo's default hostd: %v", err)
	}
	if defaults.DefaultHostd != "gpu=yes,zone=b" {
		t.Fatalf("RepoDefaults.defaultHostd = %q, want the selector, labels sorted", defaults.DefaultHostd)
	}
	if read, err := c.RepoDefaults(ctx, repo); err != nil || read.DefaultHostd != "gpu=yes,zone=b" {
		t.Fatalf("RepoDefaults read back %q, %v", read.DefaultHostd, err)
	}

	pinned := func(name string, req ui.CreateTaskRequest, want string) {
		t.Helper()
		req.Approved = true
		if req.Title == "" && req.TemplateID == "" {
			req.Title, req.Description = name, name
		}
		filed, err := c.CreateTask(ctx, req)
		if err != nil {
			t.Fatalf("%s: filing: %v", name, err)
		}
		if got, err := store.TaskHostd(ctx, filed.ID); err != nil || got != want {
			t.Errorf("%s: pinned to %q, %v; want %q", name, got, err, want)
		}
	}
	pinned("filed against the repo", ui.CreateTaskRequest{Repo: repo}, "gpu=yes,zone=b")
	pinned("filed against the default target", ui.CreateTaskRequest{}, "gpu=yes,zone=b")
	pinned("filed against another repo", ui.CreateTaskRequest{Repo: "acme/other"}, "")
	pinned("filed with no repo", ui.CreateTaskRequest{NoRepo: true}, "")
	pinned("filed with its own hostd", ui.CreateTaskRequest{Repo: repo, Hostd: "laptop"}, "laptop")

	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "laptop", Title: "On the laptop", Description: "there", Hostd: "laptop",
	})
	if err != nil {
		t.Fatal(err)
	}
	pinned("filed from a template with a hostd", ui.CreateTaskRequest{TemplateID: tmpl.ID, Repo: repo}, "laptop")
	pinned("filed from that template with its own hostd", ui.CreateTaskRequest{TemplateID: tmpl.ID, Repo: repo, Hostd: "gpu-box"}, "gpu-box")
	plain, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "plain", Title: "Plain", Description: "anywhere"})
	if err != nil {
		t.Fatal(err)
	}
	pinned("filed from a template with no hostd", ui.CreateTaskRequest{TemplateID: plain.ID, Repo: repo}, "gpu=yes,zone=b")

	// A default left naming a hostd that has since been revoked refuses
	// the filing rather than pinning it to something nothing answers.
	if _, err := c.SetRepoSettings(ctx, repo, ui.SetRepoSettingsRequest{DefaultHostd: ptr("laptop")}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeHostd(ctx, "laptop"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "x", Description: "x", Repo: repo}); err == nil {
		t.Error("filed a task against a repo whose default hostd is revoked")
	}

	cleared, err := c.SetRepoSettings(ctx, repo, ui.SetRepoSettingsRequest{DefaultHostd: ptr("")})
	if err != nil || cleared.DefaultHostd != "" {
		t.Fatalf("clearing the repo's default hostd = %q, %v", cleared.DefaultHostd, err)
	}
	pinned("filed after the default is cleared", ui.CreateTaskRequest{Repo: repo}, "")
}
