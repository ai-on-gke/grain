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

package studio_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ai-on-gke/grain/grain-plc/pkg/grainclient"
	"github.com/ai-on-gke/grain/grain-plc/pkg/perf"
	"github.com/ai-on-gke/grain/grain-plc/pkg/playbook"
	"github.com/ai-on-gke/grain/grain-plc/pkg/plcseams"
	"github.com/ai-on-gke/grain/grain-plc/pkg/studio"
)

const studioTestRev = "0123456789abcdef0123456789abcdef01234567"

func TestStudioServerUIRollupAndPlaybookLaunch(t *testing.T) {
	defer plcseams.ResetForTest()

	var taskSeq int64
	var createdReqs []grainclient.CreateTaskRequest

	daemonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/tasks":
			var req grainclient.CreateTaskRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			req.FiledVia = r.Header.Get(plcseams.HeaderClient)
			createdReqs = append(createdReqs, req)
			id := fmt.Sprintf("%d", atomic.AddInt64(&taskSeq, 1))
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(grainclient.Task{
				ID:          id,
				Title:       req.Title,
				State:       "queued",
				Repo:        req.Repo,
				ExternalRef: req.ExternalRef,
				DependsOn:   req.DependsOn,
				Context:     req.Context,
				Reads:       req.Reads,
				FiledBy:     r.Header.Get(plcseams.HeaderOnBehalfOf),
				FiledVia:    req.FiledVia,
			})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.EscapedPath(), "/api/refs/"):
			_ = json.NewEncoder(w).Encode(grainclient.RefSummary{
				ExternalRef: "plc:proj/alpha",
				Counts:      grainclient.RefRollupCounts{Total: 2, Active: 2},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer daemonSrv.Close()

	registrySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/directory":
			_ = json.NewEncoder(w).Encode(grainclient.DirectoryResponse{
				Entries: []grainclient.DirectoryEntry{
					{
						Name:  "widgets",
						Kind:  grainclient.EntryKindInstance,
						URL:   daemonSrv.URL,
						Owner: "alice@example.com",
					},
				},
			})
		case "/api/route":
			cand := grainclient.RouteCandidate{
				Name:  "widgets",
				Kind:  grainclient.EntryKindInstance,
				URL:   daemonSrv.URL,
				Owner: "alice@example.com",
			}
			_ = json.NewEncoder(w).Encode(grainclient.RouteResponse{
				Repo:       "acme/widgets",
				Primary:    &cand,
				Candidates: []grainclient.RouteCandidate{cand},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer registrySrv.Close()

	recorder := perf.NewRecorder(64)
	srv := studio.NewServer(studio.Config{
		Client: grainclient.New(grainclient.Config{
			RegistryURL: registrySrv.URL,
			ClientName:  grainclient.ClientPLCStudio,
			Perf:        recorder,
		}),
		Perf:        recorder,
		RequireAuth: true,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Static UI index.html and app.js are served without requiring API auth.
	uiResp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	uiBytes, _ := io.ReadAll(uiResp.Body)
	uiResp.Body.Close()
	if uiResp.StatusCode != http.StatusOK || !strings.Contains(string(uiBytes), "Grain PLC Studio") {
		t.Fatalf("GET / status=%d body=%q", uiResp.StatusCode, string(uiBytes))
	}

	// 2. Unauthenticated API call is rejected when RequireAuth=true.
	unauthResp, err := http.Get(ts.URL + "/api/studio/directory")
	if err != nil {
		t.Fatalf("GET /api/studio/directory: %v", err)
	}
	unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 on unauthenticated request, got %d", unauthResp.StatusCode)
	}

	// 3. Launch playbook with authenticated header; step 2 DependsOn is mapped to step 1's created task ID.
	launchBody, _ := json.Marshal(studio.LaunchPlaybookRequest{
		PlanParams: playbook.PlanParams{
			ProjectSlug: "alpha-launch",
			ExternalRef: "plc:proj/alpha",
			TargetRepo:  "acme/widgets",
			Placement:   playbook.PlacementRead,
			ContextRev:  studioTestRev,
		},
	})
	launchReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/studio/playbooks/plc-feature-launch/launch", bytes.NewReader(launchBody))
	launchReq.Header.Set("Content-Type", "application/json")
	launchReq.Header.Set(plcseams.HeaderOnBehalfOf, "alice@example.com")
	launchResp, err := http.DefaultClient.Do(launchReq)
	if err != nil {
		t.Fatalf("POST launch: %v", err)
	}
	defer launchResp.Body.Close()
	if launchResp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(launchResp.Body)
		t.Fatalf("POST launch status = %d: %s", launchResp.StatusCode, string(raw))
	}

	var launched studio.LaunchPlaybookResponse
	if err := json.NewDecoder(launchResp.Body).Decode(&launched); err != nil {
		t.Fatalf("decode LaunchPlaybookResponse: %v", err)
	}
	if len(launched.Created) != 2 || launched.Instance != "widgets" {
		t.Fatalf("launched = %+v", launched)
	}
	if len(createdReqs) != 2 || len(createdReqs[1].DependsOn) != 1 || createdReqs[1].DependsOn[0] != "1" {
		t.Fatalf("expected step 2 DependsOn mapped to task ID '1', got %+v", createdReqs[1].DependsOn)
	}
	if createdReqs[0].Context[0].Repo != "acme/widgets-read" {
		t.Fatalf("expected -read context repo, got %+v", createdReqs[0].Context)
	}
}
