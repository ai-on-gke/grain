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
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/qa"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestQAFlowVersionV4StagesViewAndJSONPublish(t *testing.T) {
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

	const tok = "test-qa-v4-flow-token"
	srv := ui.NewServer(ui.Config{LocalAPIToken: tok}, store)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	doReq := func(method, path, body string) *http.Response {
		t.Helper()
		var bodyReader io.Reader
		if body != "" {
			bodyReader = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, ts.URL+path, bodyReader)
		if err != nil {
			t.Fatalf("NewRequest %s %s: %v", method, path, err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return res
	}

	// 1. GET /api/qa/flow-versions/engineering-workflow-v4@1 returns declarationV4 and 8-stage stagesView.
	res := doReq(http.MethodGet, "/api/qa/flow-versions/engineering-workflow-v4@1", "")
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for engineering-workflow-v4@1, got %d", res.StatusCode)
	}
	var builtInV4 map[string]any
	if err := json.NewDecoder(res.Body).Decode(&builtInV4); err != nil {
		t.Fatalf("decode engineering-workflow-v4@1: %v", err)
	}
	if builtInV4["schemaVer"] != qa.FlowSchemaV4 {
		t.Fatalf("expected schemaVer=%q, got %v", qa.FlowSchemaV4, builtInV4["schemaVer"])
	}
	if builtInV4["declarationV4"] == nil {
		t.Fatalf("expected declarationV4 in response")
	}
	stagesRaw, ok := builtInV4["stagesView"].([]any)
	if !ok || len(stagesRaw) != 8 {
		t.Fatalf("expected 8 stages in stagesView, got %T len=%d", builtInV4["stagesView"], len(stagesRaw))
	}
	for i, sAny := range stagesRaw {
		st, _ := sAny.(map[string]any)
		if int(st["index"].(float64)) != i+1 {
			t.Fatalf("stage %d index=%v", i, st["index"])
		}
		if st["enabled"] != true {
			t.Fatalf("expected stage %d (%v) enabled on engineering-workflow-v4@1", i+1, st["id"])
		}
	}

	// 2. POST /api/qa/flow-versions with raw JSON declaration for grain.dev/qa-flow/v4 preserves V4 fields.
	v4JSONBody := `{
		"declaration": {
			"schema": "grain.dev/qa-flow/v4",
			"key": "studio-custom-v4",
			"label": "Studio Custom V4",
			"extends": "engineering-workflow-v4@1",
			"decompose": {
				"enabled": true,
				"mode": "propose_dag",
				"complexities": ["high"],
				"maxSubtasks": 4,
				"targetDiffLines": 250
			},
			"context": {
				"enabled": true,
				"includeCallers": true,
				"includeMemory": false,
				"maxTokens": 8000,
				"extraPaths": ["pkg/qa/"]
			},
			"readabilityPacks": [
				{"id": "go", "enabled": true, "activation": "always", "customText": "Prefer explicit error returns."}
			],
			"skillSteps": [
				{"id": "step-pr", "label": "PR Checklist", "stage": "pr", "enabled": true, "maxBytes": 512, "text": "Document root cause."}
			],
			"prContract": {
				"enabled": true,
				"mode": "blocking",
				"maxSubjectChars": 68,
				"requiredSections": ["Summary", "Verification"],
				"auditLintSuppressions": true,
				"guidanceText": "Explain verification commands."
			},
			"customChecks": [
				{
					"id": "custom:no-fixme",
					"label": "No FIXME comments",
					"kind": "pattern",
					"severity": "warn",
					"paths": ["pkg/**/*.go"],
					"forbidAdded": "FIXME",
					"message": "Avoid FIXME in production code.",
					"fixtures": {
						"fail": [{"name": "has fixme", "file": "pkg/a.go", "addedLines": ["// FIXME later"]}],
						"pass": [{"name": "clean line", "file": "pkg/a.go", "addedLines": ["// clean"]}]
					}
				}
			]
		}
	}`
	postRes := doReq(http.MethodPost, "/api/qa/flow-versions", v4JSONBody)
	defer postRes.Body.Close()
	if postRes.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(postRes.Body)
		t.Fatalf("expected 201 Created for v4 JSON publish, got %d: %s", postRes.StatusCode, string(b))
	}
	var postPayload struct {
		Hash    string         `json:"hash"`
		Version map[string]any `json:"version"`
	}
	if err := json.NewDecoder(postRes.Body).Decode(&postPayload); err != nil {
		t.Fatalf("decode POST response: %v", err)
	}
	if postPayload.Hash == "" {
		t.Fatalf("expected non-empty hash")
	}
	if postPayload.Version["declarationV4"] == nil {
		t.Fatalf("expected declarationV4 on published version response")
	}

	// Verify GET /api/qa/flow-versions/{hash} hydrates V4 fields and stagesView.
	getPublished := doReq(http.MethodGet, "/api/qa/flow-versions/"+postPayload.Hash, "")
	defer getPublished.Body.Close()
	if getPublished.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on GET published v4, got %d", getPublished.StatusCode)
	}
	var hydrated struct {
		SchemaVer     string                 `json:"schemaVer"`
		DeclarationV4 *qa.FlowDeclarationV4  `json:"declarationV4"`
		Resolved      qa.FlowSpec            `json:"resolved"`
		StagesView    []qa.WorkflowStageView `json:"stagesView"`
	}
	if err := json.NewDecoder(getPublished.Body).Decode(&hydrated); err != nil {
		t.Fatalf("decode hydrated v4: %v", err)
	}
	if hydrated.SchemaVer != qa.FlowSchemaV4 {
		t.Fatalf("hydrated schemaVer=%q, want %q", hydrated.SchemaVer, qa.FlowSchemaV4)
	}
	if hydrated.DeclarationV4 == nil || hydrated.DeclarationV4.Decompose.Mode != "propose_dag" {
		t.Fatalf("expected hydrated DeclarationV4.Decompose.Mode=propose_dag, got %+v", hydrated.DeclarationV4)
	}
	if hydrated.Resolved.PRContract.Mode != "blocking" || hydrated.Resolved.PRContract.MaxSubjectChars != 68 {
		t.Fatalf("expected Resolved.PRContract mode=blocking maxSubjectChars=68, got %+v", hydrated.Resolved.PRContract)
	}
	foundCustom := false
	for _, cc := range hydrated.Resolved.CustomChecks {
		if cc.ID == "custom:no-fixme" {
			foundCustom = true
			break
		}
	}
	if !foundCustom {
		t.Fatalf("expected custom:no-fixme in Resolved.CustomChecks, got %+v", hydrated.Resolved.CustomChecks)
	}
	if len(hydrated.StagesView) != 8 {
		t.Fatalf("expected 8 stages in hydrated StagesView, got %d", len(hydrated.StagesView))
	}

	// 3. POST /api/qa/flow-versions/adopt uses Server.ReadRepoFileAtCommit when configured.
	srv.ReadRepoFileAtCommit = func(ctx context.Context, repo, commit, path string) ([]byte, error) {
		return []byte(`schema: grain.dev/qa-flow/v4
key: repo-adopted-v4
label: Repo Adopted V4
extends: engineering-workflow-v4@1
pr-contract:
  enabled: true
  mode: warn
  max-subject-chars: 70
`), nil
	}
	adoptBody := `{"repo":"acme/kernel","path":".grain/qa-flow.yaml","commit":"0123456789abcdef0123456789abcdef01234567"}`
	adoptRes := doReq(http.MethodPost, "/api/qa/flow-versions/adopt", adoptBody)
	defer adoptRes.Body.Close()
	if adoptRes.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(adoptRes.Body)
		t.Fatalf("expected 201 Created from adopt, got %d: %s", adoptRes.StatusCode, string(b))
	}
}
