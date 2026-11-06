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

// `grain hostds register` prints the install lines and note the daemon
// rendered after the token, and `grain hostds install` prints them again
// from GET /api/hostds/{name}/install; the token is printed once, never
// as part of them.
func TestHostdsRegisterAndInstallPrintTheDaemonsLines(t *testing.T) {
	inst := &ui.HostdInstall{
		Lines: []string{"git clone https://git.example.com/grain-hostd", "./grain-hostd/install.sh --name ws-1"},
		Note:  "Run these as yourself; paste the token when asked.",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/hostds":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(ui.IssuedHostdToken{Hostd: model.Hostd{Name: "ws-1", Owner: "alice@example.com"}, Token: "ghd_secret", Install: inst})
		case r.Method == http.MethodGet && r.URL.Path == "/api/hostds/ws-1/install":
			_ = json.NewEncoder(w).Encode(inst)
		case r.Method == http.MethodGet && r.URL.Path == "/api/hostds/old/install":
			_ = json.NewEncoder(w).Encode(ui.HostdInstall{Release: "v1.0.0"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := ui.NewHTTPClient(srv.URL)
	ctx := context.Background()

	out, err := captureStdoutCLI(t, func() error {
		return cmdHostds(ctx, c, &printer{}, []string{"register", "-owner", "alice@example.com", "ws-1"})
	})
	if err != nil {
		t.Fatal(err)
	}
	tok := strings.Index(out, "ghd_secret")
	if tok < 0 || strings.Count(out, "ghd_secret") != 1 {
		t.Fatalf("register printed %q, want the token once", out)
	}
	for _, want := range append(append([]string{}, inst.Lines...), inst.Note) {
		i := strings.Index(out, want)
		if i < 0 || i < tok {
			t.Errorf("register printed %q, want %q after the token", out, want)
		}
	}

	out, err = captureStdoutCLI(t, func() error {
		return cmdHostds(ctx, c, &printer{}, []string{"install", "ws-1"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "ghd_") || !strings.Contains(out, "    "+inst.Lines[0]+"\n    "+inst.Lines[1]+"\n") || !strings.Contains(out, inst.Note) {
		t.Errorf("install printed %q, want the lines as one block and the note", out)
	}

	if _, err := captureStdoutCLI(t, func() error {
		return cmdHostds(ctx, c, &printer{}, []string{"install", "old"})
	}); err == nil {
		t.Error("install against a daemon that sends no lines succeeded")
	}
	if err := cmdHostds(ctx, c, &printer{}, []string{"install"}); err == nil {
		t.Error("install with no name succeeded")
	}
}

// `grain hostds delete` sends DELETE /api/hostds/<name> and passes the
// daemon's refusal of a live hostd through; `grain hostds
// delete-revoked` posts the bulk action.
func TestHostdsDelete(t *testing.T) {
	var deleted []string
	bulk := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/hostds/live-1":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "hostd live-1 is not revoked: revoke it first, which disconnects it, then delete it"})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/hostds/"):
			name := strings.TrimPrefix(r.URL.Path, "/api/hostds/")
			deleted = append(deleted, name)
			_ = json.NewEncoder(w).Encode(ui.DeletedHostd{Name: name, ClearedPauses: []string{"agy@hostd/" + name}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/hostds/delete-revoked":
			bulk = true
			_ = json.NewEncoder(w).Encode(ui.DeleteRevokedHostdsResult{
				Deleted: []ui.DeletedHostd{{Name: "old-1"}},
				Skipped: []ui.SkippedHostd{{Name: "old-2", Reason: "still named"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := ui.NewHTTPClient(srv.URL)
	ctx := context.Background()

	err := cmdHostds(ctx, c, &printer{}, []string{"delete", "live-1"})
	if err == nil || !strings.Contains(err.Error(), "revoke it first") {
		t.Errorf("deleting a live hostd: err = %v, want the daemon's refusal", err)
	}
	if err := cmdHostds(ctx, c, &printer{json: true}, []string{"delete", "old-1"}); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "old-1" {
		t.Errorf("DELETE requests = %v, want old-1", deleted)
	}
	if err := cmdHostds(ctx, c, &printer{}, []string{"delete"}); err == nil {
		t.Error("delete with no name succeeded")
	}
	if err := cmdHostds(ctx, c, &printer{}, []string{"delete-revoked"}); err != nil || !bulk {
		t.Errorf("delete-revoked: err = %v, posted = %v", err, bulk)
	}
}
