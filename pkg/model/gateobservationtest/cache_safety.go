// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gateobservationtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
)

// RunShadowCacheSafety fault-injects persisted rows as can occur across
// versions or through a partial restore. Persisted authority declarations are
// untrusted input; a replay must enforce the same shadow boundary as evaluation.
func RunShadowCacheSafety(t *testing.T, s *model.Store, db *sql.DB) {
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 16, 0, 0, 0, time.UTC)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	id := "shadow-cache-safety"
	must(s.PutTask(ctx, model.Task{ID: id, Title: "Cache safety", Intent: model.IntentAnalyze}))
	view, _, err := s.ObserveTaskGates(ctx, id, now)
	must(err)
	raw, err := json.Marshal(view)
	must(err)
	for _, tc := range []struct {
		name   string
		mutate func(*gates.ObservationView)
	}{
		{"authority", func(v *gates.ObservationView) { v.Authority = "builtins" }},
		{"mode", func(v *gates.ObservationView) { v.Mode = "enforcement" }},
		{"lifecycle-effects", func(v *gates.ObservationView) { v.LifecycleEffects = true }},
		{"action-effects", func(v *gates.ObservationView) { v.ActionEffects = true }},
		{"dispatch-effects", func(v *gates.ObservationView) { v.DispatchEffects = true }},
		{"automatic-cutover", func(v *gates.ObservationView) { v.AutomaticCutover = true }},
		{"required-binding", func(v *gates.ObservationView) { v.Bindings[0].Required = true }},
		{"guard-binding", func(v *gates.ObservationView) { v.Bindings[0].Purpose = "guard" }},
		{"action-binding", func(v *gates.ObservationView) { a := "change.merge"; v.Bindings[0].Action = &a }},
		{"work-output", func(v *gates.ObservationView) { v.Results[0].Result.Work = []struct{}{{}} }},
		{"action-output", func(v *gates.ObservationView) { v.Results[0].Result.Actions = []struct{}{{}} }},
		{"accepted-error", func(v *gates.ObservationView) {
			v.Results[0].Result.Status = "error"
			v.Results[0].Result.Error = &gates.EvaluationError{Code: "unavailable", Message: "old source error", Retryable: true}
			v.Results[0].Disposition = "accepted"
		}},
		{"unsupported-type", func(v *gates.ObservationView) {
			v.Bindings[0].Type = "unregistered"
			v.Results[0].Result.Type = "unregistered"
		}},
		{"unsupported-version", func(v *gates.ObservationView) { v.Bindings[0].Version = 99; v.Results[0].Result.Version = 99 }},
		{"unsupported-config", func(v *gates.ObservationView) {
			v.Bindings[0].Config = json.RawMessage(`{"product":"report","semantics":"current_v1","callback":"not-an-observer"}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var injected gates.ObservationView
			must(json.Unmarshal(raw, &injected))
			tc.mutate(&injected)
			damaged, err := json.Marshal(injected)
			must(err)
			_, err = db.ExecContext(ctx, "UPDATE gate_observation_shadow SET view_json = ? WHERE task_id = ?", string(damaged), id)
			must(err)
			_, err = db.ExecContext(ctx, "UPDATE gate_observation_shadow_history SET view_json = ? WHERE task_id = ?", string(damaged), id)
			must(err)
			before := legacyChecksSnapshot(t, ctx, db)
			restarted := model.New(db)
			wake, commits := restarted.Changed(), restarted.CommitCount()
			replay, diagnostics, err := restarted.ObserveTaskGates(ctx, id, now)
			must(err)
			assertShadowView(t, replay)
			for _, sample := range diagnostics.History {
				assertShadowView(t, &sample.View)
			}
			if diagnostics.Counts["unknown_invalid_history"] == 0 {
				t.Fatal("unsafe historical sample was not explicitly withheld")
			}
			if before != legacyChecksSnapshot(t, ctx, db) || commits != restarted.CommitCount() {
				t.Fatal("cache replay changed legacy state")
			}
			select {
			case <-wake:
				t.Fatal("cache replay woke dispatch")
			default:
			}
		})
	}
	t.Run("old-build-and-malformed-history", func(t *testing.T) {
		var old gates.ObservationView
		must(json.Unmarshal(raw, &old))
		for i := range old.Bindings {
			old.Bindings[i].Origin.Revision = "previous-observer-build"
		}
		oldRaw, err := json.Marshal(old)
		must(err)
		_, err = db.ExecContext(ctx, "UPDATE gate_observation_shadow_history SET view_json = ? WHERE task_id = ?", string(oldRaw), id)
		must(err)
		_, err = db.ExecContext(ctx, "UPDATE gate_observation_shadow SET view_json = ? WHERE task_id = ?", string(raw), id)
		must(err)
		_, err = db.ExecContext(ctx, "UPDATE gate_observation_shadow_history SET view_json = ? WHERE task_id = ? AND generation = (SELECT MAX(generation) FROM gate_observation_shadow_history WHERE task_id = ?)", "not-json", id, id)
		must(err)
		replay, diag, err := s.ObserveTaskGates(ctx, id, now)
		must(err)
		assertShadowView(t, replay)
		if diag.Counts["unknown_invalid_history"] != 1 || len(diag.History) == 0 {
			t.Fatal("invalid newest sample hid safe older history or its gap")
		}
		for _, sample := range diag.History {
			assertShadowView(t, &sample.View)
			if sample.View.Bindings[0].Origin.Revision != "previous-observer-build" {
				t.Fatal("safe old provenance was rewritten or discarded")
			}
		}
	})

}
