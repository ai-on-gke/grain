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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	grainusage "github.com/ai-on-gke/grain/pkg/usage"
)

func TestParseBudget(t *testing.T) {
	b, err := parseBudget("claude-1/opus=2_000_000/5h")
	if err != nil || b != (model.TokenBudget{Connection: "claude-1", Model: "opus", Tokens: 2000000, Window: "5h"}) {
		t.Fatalf("parseBudget = %+v, %v", b, err)
	}
	if b, err := parseBudget("agy=10/168h"); err != nil || b.Model != "" || b.Connection != "agy" {
		t.Fatalf("connection-wide budget = %+v, %v", b, err)
	}
	for _, bad := range []string{"claude-1", "claude-1=10", "claude-1=lots/5h"} {
		if _, err := parseBudget(bad); err == nil {
			t.Errorf("parseBudget(%q) accepted it", bad)
		}
	}
}

// `grain usage` prints the per-connection table and the advisory, and a
// setting flag PUTs the settings it read back with that one change.
func TestGrainUsageShowsAndSets(t *testing.T) {
	sustain := 1.6
	reset := time.Date(2026, 11, 5, 17, 0, 0, 0, time.UTC)
	five := 1
	resp := ui.UsageResponse{Report: grainusage.Report{
		Mode: model.UsageModeAdvisory, MaxWorkers: 6, EffectiveWorkers: 6, SustainableWorkers: &five,
		Scopes: []grainusage.Scope{{Connection: "c1", ConnectionName: "cloudtop-antigravity", Model: "opus-5.5-high",
			Used: 400000, Limit: 2000000, LimitSource: "configured", Remaining: 1600000, ResetAt: &reset,
			BurnPerHour: 100000, SustainableWorkers: &sustain}},
		Advisories: []string{"at this rate cloudtop-antigravity's opus-5.5-high budget runs out in ~16h; sustainable ≈ 1.6 workers"},
	}}
	var put *model.UsageSettings
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/usage":
		case r.Method == http.MethodPut && r.URL.Path == "/api/usage/settings":
			put = &model.UsageSettings{}
			if err := json.NewDecoder(r.Body).Decode(put); err != nil {
				t.Errorf("decoding PUT: %v", err)
			}
			resp.Settings = *put
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	out := captureStdout(t, func() {
		if err := runCLI([]string{"-server", srv.URL, "usage"}); err != nil {
			t.Errorf("grain usage: %v", err)
		}
	})
	for _, want := range []string{"cloudtop-antigravity", "opus-5.5-high", "400k", "2.0M", "1.6M", "1.6", "advisory: at this rate"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if put != nil {
		t.Fatal("a plain `grain usage` changed settings")
	}
	captureStdout(t, func() {
		if err := runCLI([]string{"-server", srv.URL, "usage", "-mode", "auto", "-min-workers", "2", "-budget", "c1/opus-5.5-high=2000000/5h"}); err != nil {
			t.Errorf("grain usage -mode auto: %v", err)
		}
	})
	if put == nil || put.Mode != "auto" || put.MinWorkers != 2 || len(put.Budgets) != 1 || put.Budgets[0].Tokens != 2000000 {
		t.Fatalf("PUT settings = %+v", put)
	}
	if err := runCLI([]string{"-server", srv.URL, "usage", "-mode", "sometimes"}); err == nil {
		t.Fatal("grain usage accepted an unknown mode")
	}
}

// Under -mode off the report carries no estimate at all, so the header
// must not explain the missing figure as "a connection in use has no
// known budget" -- the 2026-11-10 walkthrough's deployment had one.
func TestGrainUsageOffSaysNotEstimated(t *testing.T) {
	resp := ui.UsageResponse{
		Report:   grainusage.Report{Mode: model.UsageModeOff, MaxWorkers: 4, EffectiveWorkers: 4},
		Settings: model.UsageSettings{Mode: model.UsageModeOff, Budgets: []model.TokenBudget{{Connection: "c1", Tokens: 1000, Window: "5h"}}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	out := captureStdout(t, func() {
		if err := runCLI([]string{"-server", srv.URL, "usage"}); err != nil {
			t.Errorf("grain usage: %v", err)
		}
	})
	if !strings.Contains(out, "sustainable: not estimated (mode off)") || strings.Contains(out, "no known budget") {
		t.Fatalf("off-mode header:\n%s", out)
	}
}

// A malformed -budget, a bad window and an unknown mode are the command
// being wrong: exit 2, as the daemon's 400 for the same settings is.
func TestGrainUsageMalformedSettingsExitTwo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("%s %s reached the daemon", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ui.UsageResponse{})
	}))
	t.Cleanup(srv.Close)
	for _, args := range [][]string{
		{"-budget", "claude-1=abc/5h"},
		{"-budget", "claude-1=1000/-5h"},
		{"-budget", "claude-1=-10/5h"},
		{"-mode", "sometimes"},
	} {
		err := runCLI(append([]string{"-server", srv.URL, "usage"}, args...))
		if code, _, _ := classifyError(err); code != exitUsage {
			t.Errorf("grain usage %v: exit %d (%v), want %d", args, code, err, exitUsage)
		}
	}
}
