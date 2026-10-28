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
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// GET /api/config carries the install notice the daemon found at start,
// for the banner, and nothing when there is none.
func TestConfigReportsTheInstallNotice(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)

	got := decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/config", ""))
	if _, ok := got["installNotice"]; ok {
		t.Fatalf("installNotice = %v with no notice configured", got["installNotice"])
	}

	client.Config.InstallNotice = func() *ui.InstallNotice { return nil }
	got = decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/config", ""))
	if _, ok := got["installNotice"]; ok {
		t.Fatalf("installNotice = %v for a nil notice", got["installNotice"])
	}

	client.Config.InstallNotice = func() *ui.InstallNotice {
		return &ui.InstallNotice{
			Kind:         "host-mode",
			Message:      "this install is still host mode",
			Command:      "sudo ./scripts/setup.sh",
			AffectedCLIs: []string{"claude: no host binary"},
			DocsURL:      "https://example.com/docs",
		}
	}
	got = decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/config", ""))
	n, ok := got["installNotice"].(map[string]any)
	if !ok {
		t.Fatalf("installNotice = %v, want an object", got["installNotice"])
	}
	if n["kind"] != "host-mode" || n["message"] != "this install is still host mode" ||
		n["command"] != "sudo ./scripts/setup.sh" || n["docsUrl"] != "https://example.com/docs" {
		t.Errorf("installNotice = %v", n)
	}
	if clis, _ := n["affectedClis"].([]any); len(clis) != 1 || clis[0] != "claude: no host binary" {
		t.Errorf("affectedClis = %v", n["affectedClis"])
	}
}
