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

package demo

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

var _ ui.StateRepoManager = (*externalConfigRepo)(nil)

// The stub reports the repository the way a daemon's state repo does in
// external mode -- through ExternalConfigRepo, which the refusal reads
// first, and through Status, which Settings → Config Repo reads -- and
// refuses everything that would need a working tree.
func TestExternalConfigRepo(t *testing.T) {
	ctx := context.Background()
	r := newExternalConfigRepo(" https://github.com/acme/grain-config.git ", "")
	if remote, branch := r.ExternalConfigRepo(); remote != "https://github.com/acme/grain-config.git" || branch != "main" {
		t.Errorf("ExternalConfigRepo() = %q, %q; want the trimmed remote on main", remote, branch)
	}
	st, err := r.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Available || st.Mode != "external" || st.Remote != "https://github.com/acme/grain-config.git" || st.Branch != "main" {
		t.Errorf("Status() = %+v, want an available external repository on main", st)
	}
	if _, err := r.Sync(ctx); err == nil {
		t.Error("Sync succeeded with no working tree behind it")
	}
	if _, err := r.SetRemote(ctx, ui.StateRepoRemoteRequest{}); err == nil {
		t.Error("SetRemote succeeded with no working tree behind it")
	}
	if _, err := r.Tree(ctx); err == nil {
		t.Error("Tree succeeded with no working tree behind it")
	}
}

// The demo names the external config repository in owner/name form
// (ui.Config.StateRepoRef), as a daemon does: without it /api/config
// carried no configRepo and the repository's own page offered Remove
// (docs/test-runs/2026-11-08-repository-release-memory-ui.md, task 3613).
func TestExternalConfigRepoRef(t *testing.T) {
	for remote, want := range map[string]string{
		"https://github.com/grain/config.git":   "grain/config",
		"https://github.com/acme/grain-config/": "acme/grain-config",
		"git@github.com:acme/cfg.git":           "acme/cfg",
		"http://127.0.0.1:4300/acme/cfg":        "acme/cfg",
	} {
		got, ok := newExternalConfigRepo(remote, "").ref()
		if !ok || got.String() != want {
			t.Errorf("ref(%q) = %v, %v; want %s", remote, got, ok, want)
		}
	}
	for _, remote := range []string{"https://github.com", "https://github.com/onlyowner", "not a remote"} {
		if got, ok := newExternalConfigRepo(remote, "").ref(); ok {
			t.Errorf("ref(%q) = %v, want none", remote, got)
		}
	}
}
