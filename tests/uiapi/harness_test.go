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

package uiapi

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/demo/demotest"
)

// The harness itself: a seeded demo, its named rows, a restart onto other
// flags, and the CLI against it.
func TestDemotestSeedsRestartsAndRunsTheCLI(t *testing.T) {
	t.Parallel()
	srv := demotest.Start(t, demotest.Options{})
	wantDriver := os.Getenv("GRAIN_E2E_DB_DRIVER")
	if wantDriver == "" {
		wantDriver = "sqlite"
	}
	if srv.DBDriver != wantDriver {
		t.Errorf("store is on %q, want %q", srv.DBDriver, wantDriver)
	}
	if srv.Seed.ParkedQuestionTask == "" {
		t.Fatalf("no seeded task titled %q among %v", demotest.ParkedQuestionTitle, srv.Seed.Tasks)
	}
	for _, name := range []string{"Board", "Timeline", "Report", "Tree"} {
		if srv.Seed.Boards[name] == "" {
			t.Errorf("no seeded board %q among %v", name, srv.Seed.Boards)
		}
	}

	// Without the access code the demo signs nobody in.
	anon := demotest.Request(t, http.DefaultClient, http.MethodGet, srv.URL+"/api/tasks", nil)
	if anon.Status != http.StatusUnauthorized {
		t.Errorf("GET /api/tasks with no access code = %d, want 401: %s", anon.Status, anon.Body)
	}

	// The CLI an operator runs, against this server.
	out := demotest.CLI(t, srv, "show", srv.Seed.ParkedQuestionTask)
	if !strings.Contains(out, demotest.ParkedQuestionTitle) {
		t.Errorf("grain show %s does not name the task:\n%s", srv.Seed.ParkedQuestionTask, out)
	}

	code, before := srv.AccessCode, srv.URL
	srv.Restart("-github-sim")
	if srv.URL == before {
		t.Errorf("Restart kept the URL %s", before)
	}
	if srv.AccessCode != code {
		t.Errorf("Restart changed the access code")
	}
	// -github-sim is on now: its branches are listed.
	srv.Do(t, http.MethodGet, "/api/repos/acme/widgets/github-branches", nil).WantStatus(t, http.StatusOK)
}
