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

// The lifetime sandbox figures on the way out (grain/task-315): the two
// surfaces that carry them, and that they carry the same numbers.
//
// The question this was built for -- "how many sandboxes, agent and CI,
// have we used so far?" -- was asked by a human and would as easily be
// asked by an agent, so it has to be answerable from the System pane's
// own endpoint and from the report, with nobody having to know which one
// is authoritative. These tests read both from one deployment and
// compare them.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// countSandboxes writes the counters a deployment would have after one
// long agent run, two CI jobs, and one CI sandbox that never came up.
func countSandboxes(t *testing.T, ctx context.Context, store *model.Store) {
	t.Helper()
	at := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	if err := store.RecordSandboxStart(ctx, model.SandboxKindAgent, true, at); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSandboxFinish(ctx, model.SandboxKindAgent, 90*time.Minute, at.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, lived := range []time.Duration{30 * time.Minute, 30 * time.Minute} {
		if err := store.RecordSandboxStart(ctx, model.SandboxKindCI, true, at); err != nil {
			t.Fatal(err)
		}
		if err := store.RecordSandboxFinish(ctx, model.SandboxKindCI, lived, at.Add(lived)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecordSandboxStart(ctx, model.SandboxKindCI, false, at); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxHealthCarriesTheLifetimeFigures(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	countSandboxes(t, context.Background(), client.Store)
	client.Config.Sandboxes = fakeSandboxHealth{snapshots: []ui.SandboxSnapshot{{Sandbox: "t1-1"}}}

	rec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[ui.SandboxHealthResponse](t, rec)
	if got.Lifetime == nil {
		t.Fatal("no lifetime figures beside the live sandboxes")
	}
	if got.Lifetime.Total.Created != 3 {
		t.Errorf("built = %d, want 3", got.Lifetime.Total.Created)
	}
	if got.Lifetime.Total.Failed != 1 {
		t.Errorf("failed = %d, want 1", got.Lifetime.Total.Failed)
	}
	if want := 2.5; got.Lifetime.Total.Hours != want {
		t.Errorf("hours = %v, want %v (90 minutes of agent and two half-hour CI jobs)",
			got.Lifetime.Total.Hours, want)
	}
	if len(got.Lifetime.Kinds) != 2 {
		t.Fatalf("kinds = %+v, want agent and ci", got.Lifetime.Kinds)
	}
	agent, ci := got.Lifetime.Kinds[0], got.Lifetime.Kinds[1]
	if agent.Kind != model.SandboxKindAgent || ci.Kind != model.SandboxKindCI {
		t.Fatalf("kinds came back as %q, %q; want agent then ci", agent.Kind, ci.Kind)
	}
	// The comparison the hours exist for: the same count of sandboxes
	// costing very different machine time.
	if agent.Hours != 1.5 || ci.Hours != 1 {
		t.Errorf("agent %v h, CI %v h; want 1.5 and 1", agent.Hours, ci.Hours)
	}
	if ci.MeanSeconds != 1800 {
		t.Errorf("mean CI sandbox = %vs, want 1800", ci.MeanSeconds)
	}
	if got.Lifetime.Total.Since.IsZero() {
		t.Error("no date for the figures to be quoted since")
	}
}

func TestMetricsReportCarriesTheSameLifetimeFigures(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	countSandboxes(t, ctx, client.Store)
	client.Config.Sandboxes = fakeSandboxHealth{}

	// Over the endpoint, which is what `grain metrics` reads.
	rec := do(t, srv, http.MethodGet, "/api/metrics", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	report := decode[ui.MetricsReport](t, rec)
	if report.Sandboxes == nil {
		t.Fatal("the report carries no sandbox figures")
	}
	if report.Sandboxes.Total.Created != 3 || report.Sandboxes.Total.Hours != 2.5 {
		t.Errorf("report says %d built and %v hours, want 3 and 2.5",
			report.Sandboxes.Total.Created, report.Sandboxes.Total.Hours)
	}

	// And the System pane's own reading, from the same deployment: an
	// operator and an agent asking the same question in two places must
	// not be told two different numbers.
	pane := decode[ui.SandboxHealthResponse](t, do(t, srv, http.MethodGet, "/api/sandboxes", ""))
	if pane.Lifetime == nil {
		t.Fatal("the System pane carries no sandbox figures")
	}
	if pane.Lifetime.Total != report.Sandboxes.Total {
		t.Errorf("the System pane says %+v and the report says %+v; both read the same rows",
			pane.Lifetime.Total, report.Sandboxes.Total)
	}
}

func TestLifetimeFiguresAreAbsentBeforeAnySandboxIsCounted(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.Sandboxes = fakeSandboxHealth{}

	// A deployment that has counted nothing reports a reading with
	// nothing in it rather than no reading: "no sandbox has been built
	// since counting began" is an answer, and both surfaces say it in
	// their own words.
	got := decode[ui.SandboxHealthResponse](t, do(t, srv, http.MethodGet, "/api/sandboxes", ""))
	if got.Lifetime == nil {
		t.Fatal("no lifetime figures at all")
	}
	if !got.Lifetime.Empty() {
		t.Errorf("lifetime = %+v, want empty", got.Lifetime)
	}
	if len(got.Lifetime.Kinds) != 0 {
		t.Errorf("kinds = %+v, want none: a kind never used is absent rather than nought",
			got.Lifetime.Kinds)
	}
}
