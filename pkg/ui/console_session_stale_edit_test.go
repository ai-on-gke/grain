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
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// staleDirectorServer is a deployment whose saved director row predates
// the shipped Tier 1 grants, so its pane offers Adopt shipped defaults.
func staleDirectorServer(t *testing.T) (*ui.Server, *ui.Client) {
	t.Helper()
	client, store, ctx := testClient(t)
	if err := store.PutConfig(ctx, model.Config{
		ApprovedByDefault: true,
		ConsoleSessions: []model.ConsoleSession{{
			ID:    model.DirectorSessionID,
			Title: "Director",
			Capabilities: []string{
				"api:self-debug",
				model.GrainSourceGrant,
				model.GrainJournalGrant,
				"api:deployment:read",
				model.GrainHealthGrant,
				model.BootstrapPlaybooksGrant,
				"api:tasks:admin",
			},
			NetworkAccess: model.NetworkOpen,
			Writes:        []model.WriteTarget{{Repo: model.ConfigRepo()}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	return ui.NewServerWithClient(client), client
}

func directorRow(t *testing.T, srv *ui.Server) ui.ConsoleSessionSetting {
	t.Helper()
	rec := do(t, srv, http.MethodGet, "/api/console-sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/console-sessions: status = %d: %s", rec.Code, rec.Body)
	}
	for _, s := range decode[ui.ConsoleSessionSettingsResponse](t, rec).Sessions {
		if s.ID == model.DirectorSessionID {
			return s
		}
	}
	t.Fatal("GET /api/console-sessions: no director row")
	return ui.ConsoleSessionSetting{}
}

func putDirector(t *testing.T, srv *ui.Server, body map[string]any) (int, string) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, srv, http.MethodPut, "/api/console-sessions/"+model.DirectorSessionID, string(b))
	return rec.Code, rec.Body.String()
}

// The 2026-11-07 director agent API walkthrough, finding 4: tab A reads
// the director while it lacks the Tier 1 grants, tab B grants it
// something, and tab A takes Adopt shipped defaults. When the pane sent
// tab A's held set plus the missing defaults, tab B's grant was gone.
// adoptShippedDefaults is resolved against the row as it is when the
// request is served, so tab B's grant stays.
func TestAdoptShippedDefaultsFromAStaleTabKeepsAGrantMadeSince(t *testing.T) {
	t.Parallel()
	srv, _ := staleDirectorServer(t)
	const tabBGrant = "api:tasks:read"

	tabA := directorRow(t, srv)
	if len(tabA.MissingShippedDefaults) == 0 {
		t.Fatal("the saved director lacks no shipped defaults, so there is nothing to adopt")
	}
	if slices.Contains(tabA.MissingShippedDefaults, tabBGrant) || slices.Contains(tabA.Capabilities, tabBGrant) {
		t.Fatalf("%s is already held or a shipped default (%v / %v); the test needs a grant only tab B makes",
			tabBGrant, tabA.Capabilities, tabA.MissingShippedDefaults)
	}

	tabB := directorRow(t, srv)
	if code, body := putDirector(t, srv, map[string]any{
		"capabilities":       append(slices.Clone(tabB.Capabilities), tabBGrant),
		"expectCapabilities": tabB.Capabilities,
	}); code != http.StatusOK {
		t.Fatalf("tab B's grant: status = %d: %s", code, body)
	}

	if code, body := putDirector(t, srv, map[string]any{"adoptShippedDefaults": true}); code != http.StatusOK {
		t.Fatalf("tab A's adopt: status = %d: %s", code, body)
	}

	after := directorRow(t, srv)
	if !slices.Contains(after.Capabilities, model.CanonicalGrantKey(tabBGrant)) {
		t.Errorf("after tab A adopted shipped defaults, Capabilities = %v: tab B's %s was lost", after.Capabilities, tabBGrant)
	}
	for _, want := range tabA.MissingShippedDefaults {
		if !slices.Contains(after.Capabilities, want) {
			t.Errorf("after adopting, Capabilities = %v, missing shipped default %s", after.Capabilities, want)
		}
	}
	for _, kept := range tabA.Capabilities {
		if !slices.Contains(after.Capabilities, model.CanonicalGrantKey(kept)) {
			t.Errorf("after adopting, Capabilities = %v, lost %s the row already held", after.Capabilities, kept)
		}
	}
	if len(after.MissingShippedDefaults) != 0 {
		t.Errorf("after adopting, MissingShippedDefaults = %v, want none", after.MissingShippedDefaults)
	}

	// Adopting again, with nothing left to adopt, changes nothing.
	if code, body := putDirector(t, srv, map[string]any{"adoptShippedDefaults": true}); code != http.StatusOK {
		t.Fatalf("adopting again: status = %d: %s", code, body)
	}
	if again := directorRow(t, srv); !slices.Equal(again.Capabilities, after.Capabilities) {
		t.Errorf("adopting with nothing missing moved Capabilities from %v to %v", after.Capabilities, again.Capabilities)
	}
}

// A tick box saves the whole list too. Sent with the held set it was
// computed from, a save from a tab that read the row before somebody
// else changed its grants is refused as stale -- changing nothing --
// rather than silently putting the old set back.
func TestConsoleSessionCapabilitiesSaveFromAStaleCopyIsRefused(t *testing.T) {
	t.Parallel()
	srv, _ := staleDirectorServer(t)

	tabA := directorRow(t, srv)
	tabB := directorRow(t, srv)
	if code, body := putDirector(t, srv, map[string]any{
		"capabilities":       append(slices.Clone(tabB.Capabilities), "api:tasks:read"),
		"expectCapabilities": tabB.Capabilities,
	}); code != http.StatusOK {
		t.Fatalf("tab B's grant: status = %d: %s", code, body)
	}
	moved := directorRow(t, srv)

	// Tab A unticks .../api#deployment:read from the copy it read first.
	staleNext := slices.DeleteFunc(slices.Clone(tabA.Capabilities), func(c string) bool { return c == model.APIRef("deployment:read") })
	b, _ := json.Marshal(map[string]any{"capabilities": staleNext, "expectCapabilities": tabA.Capabilities})
	wantStale(t, do(t, srv, http.MethodPut, "/api/console-sessions/"+model.DirectorSessionID, string(b)))
	if now := directorRow(t, srv); !slices.Equal(now.Capabilities, moved.Capabilities) {
		t.Errorf("a refused stale save moved Capabilities from %v to %v", moved.Capabilities, now.Capabilities)
	}

	// Re-read, the same change goes through and keeps tab B's grant.
	// Order is not part of the precondition.
	fresh := directorRow(t, srv)
	reversed := slices.Clone(fresh.Capabilities)
	slices.Reverse(reversed)
	next := slices.DeleteFunc(slices.Clone(fresh.Capabilities), func(c string) bool { return c == model.APIRef("deployment:read") })
	if code, body := putDirector(t, srv, map[string]any{"capabilities": next, "expectCapabilities": reversed}); code != http.StatusOK {
		t.Fatalf("save from a fresh copy: status = %d: %s", code, body)
	}
	after := directorRow(t, srv)
	if slices.Contains(after.Capabilities, model.APIRef("deployment:read")) || !slices.Contains(after.Capabilities, model.APIRef("tasks:read")) {
		t.Errorf("after the fresh save, Capabilities = %v, want %s gone and %s kept", after.Capabilities, model.APIRef("deployment:read"), model.APIRef("tasks:read"))
	}

	// Without expectCapabilities a whole-list save is applied as before,
	// so a script that sets the list outright is not asked to read first.
	if code, body := putDirector(t, srv, map[string]any{"capabilities": tabA.Capabilities}); code != http.StatusOK {
		t.Fatalf("save without a precondition: status = %d: %s", code, body)
	}
}

func TestConsoleSessionCapabilityPreconditionsRefuseMeaninglessCombinations(t *testing.T) {
	t.Parallel()
	srv, _ := staleDirectorServer(t)
	held := directorRow(t, srv).Capabilities
	for name, body := range map[string]map[string]any{
		"adopt with capabilities":         {"adoptShippedDefaults": true, "capabilities": held},
		"expectCapabilities on its own":   {"expectCapabilities": held},
		"expectCapabilities with a title": {"expectCapabilities": held, "title": "Director"},
	} {
		if code, resp := putDirector(t, srv, body); code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", name, code, resp)
		}
	}
	if now := directorRow(t, srv).Capabilities; !slices.Equal(now, held) {
		t.Errorf("refused requests moved Capabilities from %v to %v", held, now)
	}
}
