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

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestQACommandStatusEnableDisableAndFlagPreservation(t *testing.T) {
	useAccessCodeFile(t)
	srvURL, _ := syncTestServer(t)
	seedSettings(t, srvURL, *settingsRequest("60s", 2, "github.example"))

	// 1. Default status is off against a real SQLite-backed ui.Server.
	var err error
	outOff := captureStdout(t, func() {
		err = runCLIWithAccessCode([]string{"-server", srvURL, "qa", "status"})
	})
	if err != nil {
		t.Fatalf("grain qa status: %v", err)
	}
	if !strings.Contains(outOff, "quality control: off") {
		t.Fatalf("expected 'quality control: off' in %q", outOff)
	}

	// 2. Enable with explicit custom flow, 40% rollout, and shadow-control=false.
	outEnable := captureStdout(t, func() {
		err = runCLIWithAccessCode([]string{
			"-server", srvURL, "qa", "enable", "-flow", model.QAFlowRepoCustom, "-rollout", "40", "-shadow-control=false",
		})
	})
	if err != nil {
		t.Fatalf("grain qa enable custom: %v", err)
	}
	if !strings.Contains(outEnable, "enabled (40% PR rollout)") ||
		!strings.Contains(outEnable, "flow:              repo-custom") ||
		!strings.Contains(outEnable, "shadow on control: false") {
		t.Fatalf("unexpected custom enable output: %q", outEnable)
	}

	// 3. Updating only -rollout 75 preserves -flow repo-custom and -shadow-control=false.
	outRolloutOnly := captureStdout(t, func() {
		err = runCLIWithAccessCode([]string{"-server", srvURL, "qa", "enable", "-rollout", "75"})
	})
	if err != nil {
		t.Fatalf("grain qa enable -rollout 75: %v", err)
	}
	if !strings.Contains(outRolloutOnly, "flow:              repo-custom") ||
		!strings.Contains(outRolloutOnly, "rollout:           75%") ||
		!strings.Contains(outRolloutOnly, "shadow on control: false") {
		t.Fatalf("flag clobber detected on partial enable: %q", outRolloutOnly)
	}

	// 4. Disable, then bare `grain qa enable` preserves 75%, repo-custom, and shadow=false.
	outDisableJSON := captureStdout(t, func() {
		err = runCLIWithAccessCode([]string{"-server", srvURL, "--json", "qa", "disable"})
	})
	if err != nil {
		t.Fatalf("grain qa disable --json: %v", err)
	}
	var parsed qaStatusView
	if err := json.Unmarshal([]byte(outDisableJSON), &parsed); err != nil {
		t.Fatalf("unmarshal disable json %q: %v", outDisableJSON, err)
	}
	if parsed.Enabled || parsed.RolloutPct != 75 || parsed.FlowID != model.QAFlowRepoCustom || parsed.ShadowOnControl {
		t.Fatalf("unexpected state after disable: %+v", parsed)
	}

	outReenableJSON := captureStdout(t, func() {
		err = runCLIWithAccessCode([]string{"-server", srvURL, "--json", "qa", "enable"})
	})
	if err != nil {
		t.Fatalf("bare grain qa enable --json: %v", err)
	}
	if err := json.Unmarshal([]byte(outReenableJSON), &parsed); err != nil {
		t.Fatalf("unmarshal re-enable json %q: %v", outReenableJSON, err)
	}
	if !parsed.Enabled || parsed.RolloutPct != 75 || parsed.FlowID != model.QAFlowRepoCustom || parsed.ShadowOnControl {
		t.Fatalf("bare enable clobbered saved settings: %+v", parsed)
	}

	// 5. Invalid -epoch <= 0 and epoch downgrades fail validation instead of silently succeeding.
	if err := runCLIWithAccessCode([]string{"-server", srvURL, "qa", "enable", "-epoch", "0"}); err == nil {
		t.Fatalf("expected -epoch 0 to fail validation")
	}
	if err := runCLIWithAccessCode([]string{"-server", srvURL, "qa", "enable", "-epoch", "-2"}); err == nil {
		t.Fatalf("expected -epoch -2 to fail validation")
	}
}

func TestQAJudgeRunDryRunIncludesClosedExperimentTasksOnly(t *testing.T) {
	useAccessCodeFile(t)
	var listQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/qa/experiments/exp-a":
			_, _ = w.Write([]byte(`{"experiment":{"id":"exp-a","state":"stopped"}}`))
		case "/api/qa/experiments/exp-a/results":
			_, _ = w.Write([]byte(`{"modelMix":{"byArm":{"control":{"gemini-4":1},"golden-v2":{"gemini-4":1}}},"pairByOrigin":{"task:10":"G-01"}}`))
		case "/api/tasks":
			listQuery = r.URL.RawQuery
			if r.URL.Query().Get("limit") != "0" {
				// Simulate >200 tasks truncating closed task 10 when limit != 0.
				_, _ = w.Write([]byte(`[{"id":"20","title":"Other exp","description":"B","state":"open","pullRequest":"https://github.example/o/r/pull/2"}]`))
				return
			}
			_, _ = w.Write([]byte(`[
				{"id":"10","title":"Exp A closed","description":"A","state":"closed","pullRequest":"https://github.example/o/r/pull/1"},
				{"id":"20","title":"Other exp","description":"B","state":"closed","pullRequest":"https://github.example/o/r/pull/2"}
			]`))
		case "/api/tasks/10/qa":
			_, _ = w.Write([]byte(`{"experimentId":"exp-a","armId":"golden-v2"}`))
		case "/api/tasks/20/qa":
			_, _ = w.Write([]byte(`{"experimentId":"exp-other","armId":"golden-v2"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	var runErr error
	rawOut := captureStdout(t, func() {
		runErr = runCLIWithAccessCode([]string{
			"-server", ts.URL, "--json", "qa", "judge", "run",
			"-experiment", "exp-a", "-judge-model", "opus-5.5-high", "-dry-run",
		})
	})
	if runErr != nil {
		t.Fatalf("grain qa judge run -dry-run: %v", runErr)
	}
	if !strings.Contains(listQuery, "limit=0") {
		t.Fatalf("tasks query = %q, want limit=0 so closed tasks beyond 200 are included", listQuery)
	}
	var summary struct {
		Packets int `json:"packets"`
	}
	if err := json.Unmarshal([]byte(rawOut), &summary); err != nil {
		t.Fatalf("unmarshal summary %q: %v", rawOut, err)
	}
	if summary.Packets != 1 {
		t.Fatalf("packets = %d, want 1 (closed exp-a task included, exp-other excluded)", summary.Packets)
	}
}
