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
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// serverSecret is the capability-server-<hash> secret SetCapabilityServer
// keeps a config field's value under for id.
func serverSecret(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "capability-server-" + hex.EncodeToString(sum[:4])
}

type listedSecret struct {
	Name              string   `json:"name"`
	Keys              []string `json:"keys"`
	CapabilityServers []string `json:"capabilityServers"`
	Orphaned          bool     `json:"orphaned"`
}

func listSecrets(t *testing.T, srv *ui.Server) map[string]listedSecret {
	t.Helper()
	rec := do(t, srv, http.MethodGet, "/api/secrets", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/secrets: %d %s", rec.Code, rec.Body)
	}
	got := decode[struct {
		Secrets []listedSecret `json:"secrets"`
	}](t, rec)
	out := map[string]listedSecret{}
	for _, s := range got.Secrets {
		out[s.Name] = s
	}
	return out
}

// Task 2263: a capability server's own secrets are listed with the server
// that uses them, one grain made for a server nothing uses any more is
// marked orphaned, and removing a server deletes the ones it alone used.
func TestSecretsListCapabilityServerOwnersAndRemoveCleansUp(t *testing.T) {
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	store := secrets.New(t.TempDir())
	client.Config.Secrets = store
	ctx := context.Background()

	const a, b = "https://example.com/caps/a", "https://example.com/caps/b"
	aSecret := serverSecret(a)
	aManaged, _, _ := strings.Cut(protocol.ManagedSecretName(a, "token"), "/")
	const leftover = "capability-server-deadbeef"
	for _, s := range [][2]string{{aSecret, "secret_values"}, {aManaged, "token"}, {leftover, "secret_values"}, {"buildkite", "token"}} {
		if err := store.Set(s[0], s[1], []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	for id, env := range map[string]map[string]string{
		a: {"VALUES": "$secret:" + aSecret + "/secret_values"},
		b: {"SHARED": "$secret:" + aSecret + "/secret_values", "CI": "$secret:buildkite/token"},
	} {
		if err := client.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: id, Config: protocol.ServerConfig{Command: "cap", Env: env}}); err != nil {
			t.Fatalf("SetCapabilityServer %s: %v", id, err)
		}
	}

	got := listSecrets(t, srv)
	if s := got[aSecret]; !slices.Equal(s.CapabilityServers, []string{a, b}) || s.Orphaned {
		t.Errorf("%s = %+v, want used by both servers", aSecret, s)
	}
	if s := got[aManaged]; !slices.Equal(s.CapabilityServers, []string{a}) || s.Orphaned {
		t.Errorf("%s = %+v, want a's managed secret", aManaged, s)
	}
	if s := got["buildkite"]; !slices.Equal(s.CapabilityServers, []string{b}) || s.Orphaned {
		t.Errorf("buildkite = %+v, want used by b", s)
	}
	if s := got[leftover]; len(s.CapabilityServers) != 0 || !s.Orphaned {
		t.Errorf("%s = %+v, want orphaned", leftover, s)
	}

	// b still uses a's config secret, so only a's managed one goes.
	if err := client.RemoveCapabilityServer(ctx, a); err != nil {
		t.Fatal(err)
	}
	got = listSecrets(t, srv)
	if _, ok := got[aManaged]; ok {
		t.Errorf("removing %s left its managed secret %s", a, aManaged)
	}
	if s, ok := got[aSecret]; !ok || !slices.Equal(s.CapabilityServers, []string{b}) {
		t.Errorf("%s = %+v (present %v), want kept for b", aSecret, s, ok)
	}

	// An operator's own secret outlives the server that used it; grain's
	// own goes with the last one.
	if err := client.RemoveCapabilityServer(ctx, b); err != nil {
		t.Fatal(err)
	}
	got = listSecrets(t, srv)
	if _, ok := got[aSecret]; ok {
		t.Errorf("removing the last server using %s left it", aSecret)
	}
	if s, ok := got["buildkite"]; !ok || len(s.CapabilityServers) != 0 || s.Orphaned {
		t.Errorf("buildkite = %+v (present %v), want kept, unclaimed and not orphaned", s, ok)
	}
	if s, ok := got[leftover]; !ok || !s.Orphaned {
		t.Errorf("%s = %+v (present %v), want still listed as orphaned", leftover, s, ok)
	}
}
