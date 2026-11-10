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
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// GET /api/upgrade carries whatever notices an edition registered
// (upgrade.RegisterNotice), with an upgrader configured or not, and no
// notices key when none is. Not parallel: the registry is the process's.
func TestUpgradeStatusReportsRegisteredNotices(t *testing.T) {
	dir := t.TempDir()
	servers := map[string]*ui.Server{
		"no upgrader": func() *ui.Server { srv, _ := testServer(t); return srv }(),
		"image upgrader": serverWithRealUpgrader(t, &upgrade.ImageConfig{
			Repository: "docker.io/grainai/grain",
			RefFile:    filepath.Join(dir, "image.env"),
			PullCmd:    []string{"true"},
		}, dir),
	}

	for name, srv := range servers {
		got := decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/upgrade", ""))
		if _, ok := got["notices"]; ok {
			t.Errorf("%s: notices = %v with none registered", name, got["notices"])
		}
	}

	unregister := upgrade.RegisterNotice(func() []upgrade.Notice {
		return []upgrade.Notice{
			{
				Kind:    "stale-flag",
				Message: "the unit's -upgrade-image flag names a retired repository",
				Command: "sudo systemctl edit grain",
				DocsURL: "https://example.com/docs/upgrade",
			},
			{Kind: "empty"}, // no message: dropped
		}
	})
	t.Cleanup(unregister)

	for name, srv := range servers {
		got := decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/upgrade", ""))
		notices, _ := got["notices"].([]any)
		if len(notices) != 1 {
			t.Fatalf("%s: notices = %v, want the one with a message", name, got["notices"])
		}
		n, _ := notices[0].(map[string]any)
		if n["kind"] != "stale-flag" ||
			n["message"] != "the unit's -upgrade-image flag names a retired repository" ||
			n["command"] != "sudo systemctl edit grain" ||
			n["docsUrl"] != "https://example.com/docs/upgrade" {
			t.Errorf("%s: notice = %v", name, n)
		}
	}

	unregister()
	for name, srv := range servers {
		got := decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/upgrade", ""))
		if _, ok := got["notices"]; ok {
			t.Errorf("%s: notices = %v after unregistering", name, got["notices"])
		}
	}
}
