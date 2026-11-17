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
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/qa/resolve"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestQAExperimentLifecycleBlindSaltResultsAndReadoutAPI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	if err := store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.TargetRepos = []string{"acme/grain"}
		return nil
	}); err != nil {
		t.Fatalf("UpdateConfig TargetRepos: %v", err)
	}
	const tok = "test-qa-exp-token"
	srv := ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("alice"),
		LocalAPIToken: tok,
	}, store)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	doJSON := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var rdr *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req, err := http.NewRequestWithContext(ctx, method, ts.URL+path, rdr)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do %s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// 1. Create paired experiment draft.
	code, created := doJSON(http.MethodPost, "/api/qa/experiments", map[string]any{
		"id":         "golden-v2-paired",
		"name":       "golden-v2 vs control paired",
		"hypothesis": "golden-v2 raises blind panel score and mutation kill rate",
		"arms": []map[string]any{
			{"id": "control", "label": "Control", "flowHash": "control", "weight": 50, "baseBranch": "bench/exp/control"},
			{"id": "golden-v2", "label": "golden v2", "flowHash": "golden-v2@1", "weight": 50, "baseBranch": "bench/exp/golden-v2"},
		},
		"allocation": map[string]any{"mode": "explicit", "design": "paired", "seed": 77},
		"plan":       map[string]any{"plannedOriginsPerArm": 4, "maxDays": 7},
		"options":    map[string]any{"shadowOnControl": true, "memory": "frozen"},
	})
	if code != http.StatusCreated {
		t.Fatalf("POST /api/qa/experiments status = %d, body = %+v", code, created)
	}

	// 2. While draft/running, salt and allocationList are hidden, while opaque allocationKeys/allocationPairs exist.
	code, detail := doJSON(http.MethodGet, "/api/qa/experiments/golden-v2-paired", nil)
	if code != http.StatusOK {
		t.Fatalf("GET detail status = %d", code)
	}
	if detail["salt"] != nil || detail["allocationList"] != nil {
		t.Fatalf("expected salt and allocationList to be hidden before stop, got salt=%v list=%v", detail["salt"], detail["allocationList"])
	}
	pairs, ok := detail["allocationPairs"].([]any)
	if !ok || len(pairs) != 4 {
		t.Fatalf("expected 4 allocationPairs, got %+v", detail["allocationPairs"])
	}

	// 3. Start experiment -> transitions to running and enables QA master switch with rolloutPct=0.
	code, _ = doJSON(http.MethodPost, "/api/qa/experiments/golden-v2-paired/transition", map[string]any{
		"action":       "start",
		"dryRunViewed": true,
	})
	if code != http.StatusOK {
		t.Fatalf("start status = %d", code)
	}
	qaCfg, _ := store.ConfigQA(ctx)
	if !qaCfg.Enabled || qaCfg.RolloutPct != 0 {
		t.Fatalf("expected QA.Enabled=true and RolloutPct=0 on start, got %+v", qaCfg)
	}

	// 4. File 4 pairs via HTTP POST /api/tasks (carrying tags: ["qa-alloc:..."]), enroll via dispatch resolver, and verify 8/8 checkedCount + 100% assignment audit on /results.
	// Internal tags (console-session:*, held-for-approval, base-repair:*) must be rejected with 400.
	if badTagCode, _ := doJSON(http.MethodPost, "/api/tasks", map[string]any{
		"title": "Evil tag attempt",
		"repo":  "acme/grain",
		"tags":  []string{"console-session:director"},
	}); badTagCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on internal tag in POST /api/tasks, got %d", badTagCode)
	}
	// Sandboxed agents holding api:tasks:write must never be able to set qa-alloc: tags directly.
	agentRef, _ := model.ParseRepo("acme/grain")
	if err := store.PutTask(ctx, model.Task{
		ID:     "agent-filer-1",
		Intent: model.IntentImplement,
		Title:  "agent filer",
		Target: &agentRef,
		Grants: []model.Grant{{Capability: "api:tasks:write", Via: model.GrantByLabel}},
	}); err != nil {
		t.Fatalf("PutTask agent-filer-1: %v", err)
	}
	agentBody, _ := json.Marshal(map[string]any{
		"title": "Agent spoofed enrollment",
		"repo":  "acme/grain",
		"tags":  []string{"qa-alloc:spoofed"},
	})
	agentReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/api/tasks", bytes.NewReader(agentBody))
	agentReq.Header.Set("Authorization", "Bearer "+tok)
	agentReq.Header.Set(ui.ActingTaskHeader, "agent-filer-1")
	agentReq.Header.Set("Content-Type", "application/json")
	agentResp, err := http.DefaultClient.Do(agentReq)
	if err != nil {
		t.Fatalf("Do agent POST /api/tasks: %v", err)
	}
	_ = agentResp.Body.Close()
	if agentResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 when an agent sets task tags on POST /api/tasks, got %d", agentResp.StatusCode)
	}
	now := time.Now().UTC()
	for i, rawPair := range pairs {
		pm := rawPair.(map[string]any)
		for slot, kField := range []struct{ keyF, baseF string }{{"keyA", "baseA"}, {"keyB", "baseB"}} {
			tCode, createdTask := doJSON(http.MethodPost, "/api/tasks", map[string]any{
				"title":       "Bench task pair " + string(rune('a'+i)) + "-" + string(rune('0'+slot)),
				"description": "Implement parity fix",
				"repo":        "acme/grain",
				"base":        pm[kField.baseF].(string),
				"tags":        []string{"qa-alloc:" + pm[kField.keyF].(string), "bench-spec:G-01"},
			})
			if tCode != http.StatusCreated && tCode != http.StatusOK {
				t.Fatalf("POST /api/tasks status = %d, body = %+v", tCode, createdTask)
			}
			tid, _ := createdTask["id"].(string)
			tk, gErr := store.GetTask(ctx, tid)
			if gErr != nil || tk == nil {
				t.Fatalf("GetTask(%s): %v", tid, gErr)
			}
			if _, _, err := resolve.EnsureQAResolutionAtDispatch(ctx, store, *tk, tid+"-1", now); err != nil {
				t.Fatalf("EnsureQAResolutionAtDispatch: %v", err)
			}
		}
	}

	code, results := doJSON(http.MethodGet, "/api/qa/experiments/golden-v2-paired/results", nil)
	if code != http.StatusOK {
		t.Fatalf("GET results status = %d, body = %+v", code, results)
	}
	audit := results["assignmentAudit"].(map[string]any)
	if audit["passed"] != true || audit["agreementPct"].(float64) != 100.0 || audit["checkedCount"].(float64) != 8 {
		t.Fatalf("expected 8/8 checkedCount and 100%% assignment audit pass, got %+v", audit)
	}

	// 5. Stop experiment and import readout -> salt is revealed and paired forest rows populate.
	code, _ = doJSON(http.MethodPost, "/api/qa/experiments/golden-v2-paired/transition", map[string]any{
		"action":   "stop",
		"decision": "ship",
		"arm":      "golden-v2",
		"reason":   "bench complete",
	})
	if code != http.StatusOK {
		t.Fatalf("stop status = %d", code)
	}

	code, stoppedDetail := doJSON(http.MethodGet, "/api/qa/experiments/golden-v2-paired", nil)
	if code != http.StatusOK || stoppedDetail["salt"] == nil || stoppedDetail["allocationList"] == nil {
		t.Fatalf("expected salt and allocationList revealed after stop, got %+v", stoppedDetail)
	}

	// Arbitrary path outside <dataDir>/qa/readouts must be rejected by safepath confinement.
	badCode, _ := doJSON(http.MethodPost, "/api/qa/experiments/golden-v2-paired/readout", map[string]any{
		"action":     "import",
		"readoutDir": "/etc",
	})
	if badCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on out-of-root readoutDir, got %d", badCode)
	}

	tPass, cPass := true, false
	cScore, tScore := 6.5, 8.5
	cMut, tMut := 0.55, 0.85
	code, _ = doJSON(http.MethodPost, "/api/qa/experiments/golden-v2-paired/readout", map[string]any{
		"action":           "import",
		"model":            "claude-sonnet-4",
		"humanLabelsCount": 20,
		"raterMatrix":      [][]float64{{8.5, 8.4, 8.6}, {6.5, 6.4, 6.6}, {8.8, 8.7, 8.9}, {6.2, 6.1, 6.3}},
		"probe":            map[string]any{"AUC": 0.52},
		"outcomes": []map[string]any{
			{"specKey": "G-01", "armId": "control", "panelScore": cScore, "mutationScore": cMut, "hiddenPassed": cPass},
			{"specKey": "G-01", "armId": "golden-v2", "panelScore": tScore, "mutationScore": tMut, "hiddenPassed": tPass},
			{"specKey": "G-02", "armId": "control", "panelScore": cScore, "mutationScore": cMut, "hiddenPassed": tPass},
			{"specKey": "G-02", "armId": "golden-v2", "panelScore": tScore, "mutationScore": tMut, "hiddenPassed": tPass},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("POST readout status = %d", code)
	}

	code, finalResults := doJSON(http.MethodGet, "/api/qa/experiments/golden-v2-paired/results", nil)
	if code != http.StatusOK {
		t.Fatalf("GET final results status = %d", code)
	}
	forest, _ := finalResults["forestRows"].([]any)
	if len(forest) < 3 {
		t.Fatalf("expected >= 3 paired forest rows after readout import, got %d (%+v)", len(forest), forest)
	}
	rubric, ok := finalResults["rubric"].(map[string]any)
	if !ok || rubric["validated"] != true {
		t.Fatalf("expected validated rubric summary, got %+v", finalResults["rubric"])
	}
}
