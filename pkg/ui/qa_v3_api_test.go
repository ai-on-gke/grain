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

func TestQAV3ProbeAdoptFixtureValidationAndVersionVsVersionResults(t *testing.T) {
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
		t.Fatalf("UpdateConfig: %v", err)
	}

	const tok = "test-qa-v3-token"
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
			raw, mErr := json.Marshal(body)
			if mErr != nil {
				t.Fatalf("marshal: %v", mErr)
			}
			rdr = bytes.NewReader(raw)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req, rErr := http.NewRequestWithContext(ctx, method, ts.URL+path, rdr)
		if rErr != nil {
			t.Fatalf("NewRequest: %v", rErr)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, dErr := http.DefaultClient.Do(req)
		if dErr != nil {
			t.Fatalf("Do(%s %s): %v", method, path, dErr)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// 1. V3 custom check with broken pass fixture must be rejected with 400 at publish.
	badV3YAML := `schema: grain.dev/qa-flow/v3
key: broken-v3
extends: golden-v2@1
label: Broken V3
custom-checks:
  - id: custom:bad-pattern
    kind: pattern
    severity: blocking
    paths: ["pkg/**/*.go"]
    forbid-added: 'panic\('
    message: "do not panic"
    fixtures:
      fail:
        - name: panics
          file: pkg/foo/bar.go
          added-lines: ["panic(1)"]
      pass:
        - name: also panics by mistake
          file: pkg/foo/bar.go
          added-lines: ["panic(2)"]
`
	code, _ := doJSON("POST", "/api/qa/flow-versions", map[string]any{"yaml": badV3YAML})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 on failing pass fixture, got %d", code)
	}

	// 2. Publish golden-v3-enforced extending golden-v3@1.
	enforcedYAML := `schema: grain.dev/qa-flow/v3
key: golden-v3-enforced
extends: golden-v3@1
label: golden v3 enforced
stages:
  s5-merge-triage:
    mode: enforced
    auto-merge-tiers: [T0, T1]
    require-human-tiers: [T2, T3]
`
	code, pubResp := doJSON("POST", "/api/qa/flow-versions", map[string]any{"yaml": enforcedYAML})
	if code != http.StatusCreated {
		t.Fatalf("publish golden-v3-enforced: status=%d body=%+v", code, pubResp)
	}
	enforcedHash, _ := pubResp["hash"].(string)
	if enforcedHash == "" {
		t.Fatalf("expected non-empty hash on publish, got %+v", pubResp)
	}

	// 3. Probe T1, T2, and T3 against golden-v3-enforced@1.
	code, probeT1 := doJSON("POST", "/api/qa/probe", map[string]any{
		"flowHash":     "golden-v3-enforced@1",
		"title":        "Add small helper",
		"files":        []string{"pkg/util/str.go"},
		"nonTestAdded": 12,
	})
	if code != http.StatusOK {
		t.Fatalf("probe T1 status=%d body=%+v", code, probeT1)
	}
	if probeT1["triage"].(map[string]any)["decision"] != "auto_merge" {
		t.Fatalf("expected T1 probe decision=auto_merge, got %+v", probeT1["triage"])
	}

	code, probeT2 := doJSON("POST", "/api/qa/probe", map[string]any{
		"flowHash":     "golden-v3-enforced@1",
		"title":        "Update inbox UI",
		"files":        []string{"ui-ng/src/app/inbox/inbox-page.component.ts"},
		"nonTestAdded": 25,
	})
	if code != http.StatusOK || probeT2["triage"].(map[string]any)["decision"] != "require_human" {
		t.Fatalf("expected T2 probe decision=require_human, status=%d triage=%+v", code, probeT2["triage"])
	}
	if probeT2["triage"].(map[string]any)["enforced"] != true {
		t.Fatalf("expected T2 probe enforced=true, got %+v", probeT2["triage"])
	}

	code, probeT3 := doJSON("POST", "/api/qa/probe", map[string]any{
		"flowHash":     "golden-v3-enforced@1",
		"title":        "Update workflow CI",
		"files":        []string{".github/workflows/ci.yml"},
		"nonTestAdded": 10,
	})
	if code != http.StatusOK || probeT3["triage"].(map[string]any)["blockedByFloor"] != true {
		t.Fatalf("expected T3 trust-boundary probe blockedByFloor=true, status=%d triage=%+v", code, probeT3["triage"])
	}

	// 4. Create a 2-arm version-vs-version experiment (golden-v3 vs golden-v2, no control arm!) with referenceArmId="golden-v2".
	code, expResp := doJSON("POST", "/api/qa/experiments", map[string]any{
		"id":             "v3-vs-v2-test",
		"name":           "Golden V3 vs Golden V2",
		"referenceArmId": "golden-v2",
		"arms": []map[string]any{
			{"id": "golden-v2", "label": "Golden V2", "flowHash": "golden-v2@1", "weight": 50},
			{"id": "golden-v3", "label": "Golden V3", "flowHash": enforcedHash, "weight": 50},
		},
		"scope":      map[string]any{"repos": []string{"acme/grain"}},
		"allocation": map[string]any{"mode": "explicit", "design": "paired", "seed": 42},
		"plan":       map[string]any{"plannedOriginsPerArm": 4, "maxDays": 7},
		"options":    map[string]any{"memory": "frozen"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create version-vs-version experiment: status=%d body=%+v", code, expResp)
	}

	code, startResp := doJSON("POST", "/api/qa/experiments/v3-vs-v2-test/transition", map[string]any{"action": "start"})
	if code != http.StatusOK {
		t.Fatalf("start version-vs-version experiment: status=%d body=%+v", code, startResp)
	}
	pairsRaw, _ := startResp["experiment"].(map[string]any)["allocationPairs"].([]any)
	if len(pairsRaw) != 4 {
		t.Fatalf("expected 4 allocationPairs, got %d", len(pairsRaw))
	}
	p0 := pairsRaw[0].(map[string]any)
	keyA := p0["keyA"].(string)
	keyB := p0["keyB"].(string)

	// Enroll two paired tasks and record S0/S3/S5 check runs.
	now := time.Now().UTC()
	for idx, key := range []string{keyA, keyB} {
		tCode, createdTask := doJSON("POST", "/api/tasks", map[string]any{
			"title":       "V3 bench pair " + string(rune('a'+idx)),
			"description": "Implement V3 feature",
			"repo":        "acme/grain",
			"tags":        []string{"qa-alloc:" + key},
		})
		if tCode != http.StatusCreated && tCode != http.StatusOK {
			t.Fatalf("POST /api/tasks: status=%d body=%+v", tCode, createdTask)
		}
		tid, _ := createdTask["id"].(string)
		tk, gErr := store.GetTask(ctx, tid)
		if gErr != nil || tk == nil {
			t.Fatalf("GetTask(%s): %v", tid, gErr)
		}
		res, _, rErr := resolve.EnsureQAResolutionAtDispatch(ctx, store, *tk, tid+"-1", now)
		if rErr != nil {
			t.Fatalf("EnsureQAResolutionAtDispatch %s: %v", tid, rErr)
		}
		if res.ArmID == "golden-v3" {
			_, _ = store.RecordQACheckRun(ctx, model.QACheckRun{
				TaskID:         tid,
				Stage:          "S0",
				CheckID:        "qa:intent",
				Arm:            model.QAArmTreatment,
				Verdict:        "pass",
				DurationMS:     850,
				TokensIn:       300,
				TokensOut:      120,
				ExperimentID:   "v3-vs-v2-test",
				ArmID:          "golden-v3",
				TriageDecision: "",
			}, nil)
			_, _ = store.RecordQACheckRun(ctx, model.QACheckRun{
				TaskID:         tid,
				Stage:          "S5",
				CheckID:        "qa:merge-triage",
				Arm:            model.QAArmTreatment,
				Verdict:        "pass",
				ExperimentID:   "v3-vs-v2-test",
				ArmID:          "golden-v3",
				BlastTier:      "T1",
				BlastScore:     15,
				TriageDecision: "auto_merge",
			}, nil)
		}
	}

	code, results := doJSON("GET", "/api/qa/experiments/v3-vs-v2-test/results", nil)
	if code != http.StatusOK {
		t.Fatalf("GET results: status=%d body=%+v", code, results)
	}
	if results["referenceArmId"] != "golden-v2" {
		t.Fatalf("expected referenceArmId=golden-v2, got %v", results["referenceArmId"])
	}
	if results["declarationDiff"] == nil {
		t.Fatalf("expected non-nil declarationDiff for golden-v3 vs golden-v2")
	}
	funnel, ok := results["stageFunnel"].([]any)
	if !ok || len(funnel) == 0 {
		t.Fatalf("expected non-empty stageFunnel slice, got %T %+v", results["stageFunnel"], results["stageFunnel"])
	}
	triageRows, ok := results["triageMetrics"].([]any)
	if !ok || len(triageRows) == 0 {
		t.Fatalf("expected non-empty triageMetrics slice, got %T %+v", results["triageMetrics"], results["triageMetrics"])
	}

	// 5. Verify repo-commit adoption endpoint with runtime hook.
	prevHooks := resolve.GetRuntimeHooks()
	t.Cleanup(func() { resolve.SetRuntimeHooks(prevHooks) })
	resolve.SetRuntimeHooks(&resolve.RuntimeHooks{
		ReadRepoFileAtCommit: func(_ context.Context, _, _, _ string) ([]byte, error) {
			return []byte(enforcedYAML), nil
		},
	})
	code, adoptResp := doJSON("POST", "/api/qa/flow-versions/adopt", map[string]any{
		"repo":   "acme/grain",
		"path":   ".grain/qa/flow.yml",
		"commit": "0123456789abcdef0123456789abcdef01234567",
	})
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("adopt status=%d body=%+v", code, adoptResp)
	}

	// Non-hex 40-char commit and paths outside .grain/ must be rejected (400).
	badHexCode, _ := doJSON("POST", "/api/qa/flow-versions/adopt", map[string]any{
		"repo":   "acme/grain",
		"path":   ".grain/qa/flow.yml",
		"commit": "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
	})
	if badHexCode != http.StatusBadRequest {
		t.Fatalf("expected non-hex 40-char commit to return 400, got %d", badHexCode)
	}
	outsideGrainCode, _ := doJSON("POST", "/api/qa/flow-versions/adopt", map[string]any{
		"repo":   "acme/grain",
		"path":   "pkg/ui/server.go",
		"commit": "0123456789abcdef0123456789abcdef01234567",
	})
	if outsideGrainCode != http.StatusBadRequest {
		t.Fatalf("expected non-.grain/ adopt path to return 400, got %d", outsideGrainCode)
	}
}
