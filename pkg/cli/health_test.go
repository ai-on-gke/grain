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

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatchcondition"
	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/mergelogins"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// `grain health` against a real server: a layer's registered counters
// are its text and its -json, and are gone once it unregisters them.
func TestGrainHealthShowsRegisteredCountersUntilUnregistered(t *testing.T) {
	const code = "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"
	store, _ := sqlitetest.Open(t)
	srv := ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("operator@example.com"),
		LocalAPIToken: code,
	}, store)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	const name = "cli test verification"
	unregister := ui.RegisterHealthCounters(name, func() map[string]int64 {
		return map[string]int64{"verifier:admitted": 12, "verifier:host_mismatch": 2}
	})
	t.Cleanup(unregister)

	out, err := runAccessCLI(t, ts.URL, code, "health")
	if err != nil {
		t.Fatalf("grain health: %v", err)
	}
	for _, want := range []string{"counters since the daemon started", name, "verifier:admitted", "12", "verifier:host_mismatch"} {
		if !strings.Contains(out, want) {
			t.Errorf("grain health does not mention %q:\n%s", want, out)
		}
	}
	// Sorted by key, so the same counter is always on the same line.
	if strings.Index(out, "verifier:admitted") > strings.Index(out, "verifier:host_mismatch") {
		t.Errorf("counters are not sorted by key:\n%s", out)
	}

	out, err = runAccessCLI(t, ts.URL, code, "-json", "health")
	if err != nil {
		t.Fatalf("grain -json health: %v", err)
	}
	var got ui.SandboxHealthResponse
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("grain -json health is not JSON: %v\n%s", err, out)
	}
	var found bool
	for _, s := range got.Counters {
		if s.Name == name {
			found = s.Counters["verifier:admitted"] == 12 && s.Counters["verifier:host_mismatch"] == 2
		}
	}
	if !found {
		t.Errorf("grain -json health counters = %+v, want %q with admitted 12, host_mismatch 2", got.Counters, name)
	}

	unregister()
	out, err = runAccessCLI(t, ts.URL, code, "health")
	if err != nil {
		t.Fatalf("grain health after unregister: %v", err)
	}
	if strings.Contains(out, name) {
		t.Errorf("grain health still shows %q after unregister:\n%s", name, out)
	}
}

func TestGrainHealthSurfacesHubFailuresAndScopeConflicts(t *testing.T) {
	const code = "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"
	store, _ := sqlitetest.Open(t)
	srv := ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("operator@example.com"),
		LocalAPIToken: code,
		HubStatus: func(_ context.Context) (model.HubSnapshot, error) {
			return model.HubSnapshot{
				Enabled: true,
				State:   model.HubStateError,
				Config: model.HubConfig{
					URL:   "https://hub.example.com",
					Entry: "grain-west",
				},
				ConsecutiveFailures: 3,
				LastError:           "hub API 503: upstream unavailable",
				ScopeConflicts: []api.ScopeConflict{
					{Repo: "acme/widgets", PathPrefix: "pkg/autoscaler", ConflictingEntry: "grain-east", ConflictingOwner: "east@example.com"},
				},
			}, nil
		},
	}, store)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	out, err := runAccessCLI(t, ts.URL, code, "health")
	if err != nil {
		t.Fatalf("grain health: %v", err)
	}
	for _, want := range []string{
		"hub (grain-west @ https://hub.example.com): error",
		"last error (3 failure(s)): hub API 503: upstream unavailable",
		"scope conflict on acme/widgets/pkg/autoscaler: claimed by grain-east (east@example.com)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("grain health output missing %q:\n%s", want, out)
		}
	}
}

// A capability server out of use is what `grain health` leads with: the
// health response has listed them since #647, but the printer never did,
// so a deployment with a server down was told "no counters"
// (docs/test-runs/2026-11-04-capabilities.md, F7).
func TestGrainHealthListsCapabilityServersOutOfUse(t *testing.T) {
	out := captureStdout(t, func() {
		(&printer{}).health(ui.SandboxHealthResponse{CapabilityServers: []ui.CapabilityServerDown{
			{ID: "https://walk.example/caps/cm", State: "failed", Reason: "initialize: mcp: capability server closed its output: EOF"},
			{ID: "https://grain.dev/capabilities/gcp", State: "needs-config", ConfigRefused: true, Reason: "gcp: permission set org: roles/owner is not grantable"},
			{ID: "https://walk.example/caps/corp", State: "needs-config", Waiting: "a renewed host credential", Reason: "configure: the host credential expired"},
		}})
	})
	for _, want := range []string{
		"FAILING: capability servers out of use",
		"https://walk.example/caps/cm (failed): initialize: mcp: capability server closed its output: EOF",
		"https://grain.dev/capabilities/gcp (refused its saved config): gcp: permission set org: roles/owner is not grantable",
		"https://walk.example/caps/corp (waiting for a renewed host credential, retried every minute): configure: the host credential expired",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("grain health output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "no counters") {
		t.Errorf("grain health says there is nothing to report with a server down:\n%s", out)
	}
}

func TestGrainHealthListsTargetRepoHostWarnings(t *testing.T) {
	out := captureStdout(t, func() {
		(&printer{}).health(ui.SandboxHealthResponse{
			TargetRepoHostWarnings: []ui.TargetRepoHostMismatch{{
				Repo:       "experimental/grain",
				ResolvesTo: "github.com",
				Candidates: []string{"git.forge.example/experimental/grain"},
				Message:    "this entry resolves to github.com; did you mean git.forge.example/experimental/grain?",
			}},
		})
	})
	for _, want := range []string{
		"WARNING: target repositories missing their git host:",
		"experimental/grain: this entry resolves to github.com; did you mean git.forge.example/experimental/grain?",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("grain health output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "no counters") {
		t.Errorf("grain health says there is nothing to report when target repo host warnings exist:\n%s", out)
	}
}

func TestGrainHealthNamesSchedulesThatStoppedRunning(t *testing.T) {
	last := time.Date(2026, 10, 28, 7, 37, 0, 0, time.UTC)
	out := captureStdout(t, func() {
		(&printer{}).health(ui.SandboxHealthResponse{StaleSchedules: []model.StaleSchedule{{
			ID: "repo:acme/widgets:workflow.live-agent", Repo: model.RepoRef{Owner: "acme", Name: "widgets"},
			Workflow: "live-agent", Cadence: 24 * time.Hour, LastRunAt: &last,
			Reason: "this repository uses github CI mode, so .grain/workflows/live-agent.yml's cron is dropped at every firing",
		}}})
	})
	for _, want := range []string{
		"FAILING: schedules with no run in more than twice their cadence",
		"repo:acme/widgets:workflow.live-agent (workflow live-agent) on acme/widgets: every 24h0m0s, last ran 2026-10-28T07:37:00Z",
		"why: this repository uses github CI mode",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("grain health output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "no counters") {
		t.Errorf("grain health says there is nothing to report with a schedule gone quiet:\n%s", out)
	}
}

// Declarations a repository merged that this daemon would not read
// (grain/task-4392): the files, lines and messages, and what is in force.
func TestGrainHealthNamesRefusedDeclarations(t *testing.T) {
	out := captureStdout(t, func() {
		(&printer{}).health(ui.SandboxHealthResponse{RefusedDeclarations: []ui.RefusedDeclarations{{
			Repo: "acme/widgets", InForce: "101381a0b1c2d3e4", HeldBack: true,
			Error: ".grain/schedules/release-train.yml: yaml: unmarshal errors:\n  line 16: cannot unmarshal !!map into string",
		}}})
	})
	for _, want := range []string{
		"FAILING: repository declarations not read in full:",
		"acme/widgets (files held back for a newer grain; in force: 101381a0b1c2)",
		"    .grain/schedules/release-train.yml: yaml: unmarshal errors:",
		"line 16: cannot unmarshal !!map into string",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("grain health output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "no counters") {
		t.Errorf("grain health says there is nothing to report:\n%s", out)
	}
}

// `grain health` exits 1 whenever it prints a FAILING section, text or
// -json, so a monitoring script can tell a failing deployment by status
// (docs/test-runs/2026-11-11-schedule-groups.md, F4); a WARNING alone
// leaves it at 0.
func TestGrainHealthExitsOneWhenACheckIsFailing(t *testing.T) {
	last := time.Date(2026, 11, 8, 7, 37, 0, 0, time.UTC)
	stale := ui.SandboxHealthResponse{StaleSchedules: []model.StaleSchedule{{
		ID: "repo:schedule:acme:widgets:workflow.live", Repo: model.RepoRef{Owner: "acme", Name: "widgets"},
		Cadence: 24 * time.Hour, LastRunAt: &last,
	}}}
	warning := ui.SandboxHealthResponse{MergeLoginWarnings: []mergelogins.Warning{{
		Repo: "acme/widgets", Login: "octocat", Message: "octocat is a user account",
	}}}
	for _, tc := range []struct {
		name     string
		health   ui.SandboxHealthResponse
		args     []string
		wantExit int
		wantOut  string
	}{
		{"stale schedule", stale, []string{"health"}, exitFailed, "FAILING: schedules with no run"},
		{"stale schedule -json", stale, []string{"-json", "health"}, exitFailed, `"repo:schedule:acme:widgets:workflow.live"`},
		{"dispatch blocked", ui.SandboxHealthResponse{DispatchBlocked: []ui.DispatchCondition{{Condition: dispatchcondition.Condition{Name: "hosts", Error: "every host is disconnected"}}}}, []string{"health"}, exitFailed, "FAILING: dispatch is blocked"},
		{"capability server down", ui.SandboxHealthResponse{CapabilityServers: []ui.CapabilityServerDown{{ID: "https://walk.example/caps/cm", State: "failed"}}}, []string{"health"}, exitFailed, "FAILING: capability servers out of use"},
		{"refused declarations", ui.SandboxHealthResponse{RefusedDeclarations: []ui.RefusedDeclarations{{Repo: "acme/widgets", Error: "bad yaml"}}}, []string{"health"}, exitFailed, "FAILING: repository declarations"},
		{"warning only", warning, []string{"health"}, exitOK, "WARNING: reviewed-merge logins"},
		{"healthy", ui.SandboxHealthResponse{}, []string{"health"}, exitOK, "no counters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/sandboxes" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(tc.health)
			}))
			defer ts.Close()
			out, err := runAccessCLI(t, ts.URL, "token", tc.args...)
			if exit, _, _ := classifyError(err); exit != tc.wantExit {
				t.Errorf("grain %s exit = %d (err %v), want %d", strings.Join(tc.args, " "), exit, err, tc.wantExit)
			}
			if !strings.Contains(out, tc.wantOut) {
				t.Errorf("grain %s output missing %q:\n%s", strings.Join(tc.args, " "), tc.wantOut, out)
			}
		})
	}
}
