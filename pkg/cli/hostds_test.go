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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// `grain hostds register` sends the owner the remote hostd will connect
// as and writes the token it is answered with to -token-file; `grain
// hostds set-owner` patches only the owner.
func TestHostdsRegisterAndSetOwner(t *testing.T) {
	var created ui.CreateHostdRequest
	var patched map[string]any
	var patchedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/hostds":
			_ = json.NewDecoder(r.Body).Decode(&created)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(ui.IssuedHostdToken{
				Hostd: model.Hostd{Name: created.Name, Owner: strings.ToLower(created.Owner)},
				Token: "ghd_secret",
			})
		case r.Method == http.MethodPatch:
			patchedPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&patched)
			_ = json.NewEncoder(w).Encode(model.Hostd{Name: "ws-1", Owner: "hostd@proj.iam.gserviceaccount.com"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := ui.NewHTTPClient(srv.URL)
	ctx := context.Background()

	tokenFile := filepath.Join(t.TempDir(), "hostd.token")
	// A token file left world-readable from before is narrowed to 0600.
	if err := os.WriteFile(tokenFile, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdHostds(ctx, c, &printer{json: true}, []string{"register", "-owner", "Alice@Example.com", "-label", "os=linux", "-token-file", tokenFile, "ws-1"}); err != nil {
		t.Fatal(err)
	}
	if created.Name != "ws-1" || created.Owner != "Alice@Example.com" || created.Labels["os"] != "linux" {
		t.Fatalf("POST /api/hostds = %+v", created)
	}
	got, err := os.ReadFile(tokenFile)
	if err != nil || strings.TrimSpace(string(got)) != "ghd_secret" {
		t.Fatalf("-token-file = %q, %v", got, err)
	}
	if fi, _ := os.Stat(tokenFile); fi.Mode().Perm() != 0o600 {
		t.Errorf("-token-file mode = %v, want 0600", fi.Mode().Perm())
	}

	if err := cmdHostds(ctx, c, &printer{json: true}, []string{"set-owner", "ws-1", "hostd@proj.iam.gserviceaccount.com"}); err != nil {
		t.Fatal(err)
	}
	if patchedPath != "/api/hostds/ws-1" || patched["owner"] != "hostd@proj.iam.gserviceaccount.com" || patched["labels"] != nil {
		t.Fatalf("PATCH %s %v, want only the owner", patchedPath, patched)
	}
	if err := cmdHostds(ctx, c, &printer{}, []string{"set-owner", "ws-1"}); err == nil {
		t.Error("set-owner with no owner argument succeeded")
	}
}
