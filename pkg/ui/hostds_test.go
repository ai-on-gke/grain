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
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The hostd registry's routes: a created hostd's token is shown once and
// names it, a rotation retires the old token, revoking retires them all,
// and the co-located hostd can be neither given a token nor revoked.
func TestHostdRegistryRoutes(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	if err := client.Store.EnsureLocalHostd(ctx); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/hostds", `{"name":"ws-1","owner":"Alice@Example.com","labels":{"os":"linux"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/hostds = %d %s", rec.Code, rec.Body.String())
	}
	var issued ui.IssuedHostdToken
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(issued.Token, model.HostdTokenPrefix) || issued.Hostd.Owner != "alice@example.com" {
		t.Fatalf("issued = %+v", issued)
	}
	if h, _ := client.Store.HostdForToken(ctx, issued.Token); h == nil || h.Name != "ws-1" {
		t.Fatalf("the issued token names %+v, want ws-1", h)
	}

	for _, bad := range []string{`{"name":"Bad Name"}`, `{"name":"local"}`, `{"name":"ws-1"}`, `{"name":"ws-2","owner":"nobody"}`} {
		if rec := do(t, srv, http.MethodPost, "/api/hostds", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("POST /api/hostds %s = %d, want 400", bad, rec.Code)
		}
	}

	rec = do(t, srv, http.MethodGet, "/api/hostds", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), issued.Token) {
		t.Fatalf("GET /api/hostds = %d %s, want the list without the token", rec.Code, rec.Body.String())
	}
	var listed []ui.HostdView
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Name != model.LocalHostd || listed[1].Name != "ws-1" || listed[1].TokenPrefix == "" {
		t.Errorf("listed = %+v, want local and ws-1 with its token's prefix", listed)
	}

	rec = do(t, srv, http.MethodPatch, "/api/hostds/ws-1", `{"owner":"bob@example.com","labels":{"gpu":"yes"}}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "bob@example.com") || !strings.Contains(rec.Body.String(), `"gpu":"yes"`) {
		t.Errorf("PATCH /api/hostds/ws-1 = %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, srv, http.MethodPost, "/api/hostds/ws-1/rotate", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate = %d %s", rec.Code, rec.Body.String())
	}
	var rotated ui.IssuedHostdToken
	if err := json.Unmarshal(rec.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if h, _ := client.Store.HostdForToken(ctx, issued.Token); h != nil {
		t.Error("the token before the rotation still names a hostd")
	}
	if h, _ := client.Store.HostdForToken(ctx, rotated.Token); h == nil || h.Name != "ws-1" {
		t.Errorf("the rotated token names %+v, want ws-1", h)
	}

	if rec := do(t, srv, http.MethodPost, "/api/hostds/ws-1/revoke", ""); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body.String())
	}
	if h, _ := client.Store.HostdForToken(ctx, rotated.Token); h != nil {
		t.Error("a revoked hostd's token still names it")
	}
	if rec := do(t, srv, http.MethodPost, "/api/hostds/ws-1/rotate", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("rotating a revoked hostd = %d, want 400", rec.Code)
	}
	for _, path := range []string{"/api/hostds/local/revoke", "/api/hostds/local/rotate"} {
		if rec := do(t, srv, http.MethodPost, path, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400", path, rec.Code)
		}
	}
	if rec := do(t, srv, http.MethodPost, "/api/hostds/nope/revoke", ""); rec.Code != http.StatusNotFound {
		t.Errorf("revoking an unknown hostd = %d, want 404", rec.Code)
	}
}
