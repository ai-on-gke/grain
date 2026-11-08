// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package demo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A standing session's grants can be edited on a fresh demo: every
// session holds the built-in config-map's bootstrap grant, and the edit
// re-checks each grant against the registry, so the registry has to
// carry the config-map server the way a daemon's does (task 3586).
func TestDemoStandingSessionGrantsCanBeEdited(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	store.SingleTenant = true
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := ui.Config{
		Actor:         ui.DefaultActor("demo-operator"),
		DefaultTarget: &repo,
		Capabilities:  append(ui.OfferedCapabilities(), ui.APICapabilities()...),
	}
	storedCfg := model.DefaultConfig()
	capabilityServers, err := configureCapabilityServerDemo(ctx, &storedCfg, func(string) (string, bool) {
		return "", false
	}, store, nil)
	if err != nil {
		t.Fatalf("configureCapabilityServerDemo: %v", err)
	}
	cfg.CapabilityServers = capabilityServers
	if err := store.PutConfig(ctx, storedCfg); err != nil {
		t.Fatalf("storing initial config: %v", err)
	}

	srv := ui.NewServer(withAccessCode(cfg), store)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:40000"
		presentAccessCode(req)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	// The config-map server is attached, not left pending.
	configMap := intree.CanonicalID("config-map")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := capabilityServers.Lookup(configMap); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("Lookup(%s): %v", configMap, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// seedDemo files the director session's task, as the demo does: only
	// a session with an open task has its grants re-checked on an edit.
	if err := seedDemo(ctx, store, cfg); err != nil {
		t.Fatalf("seedDemo: %v", err)
	}
	rec := do(http.MethodGet, "/api/console-sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/console-sessions: %d %s", rec.Code, rec.Body)
	}
	session := directorSession(t, rec)
	if session.TaskID == "" {
		t.Fatalf("GET /api/console-sessions = %s, want the director session with its task", rec.Body)
	}
	if !slices.ContainsFunc(session.Capabilities, func(c string) bool { return strings.HasPrefix(c, configMap) }) {
		t.Fatalf("session capabilities = %v, want a %s grant", session.Capabilities, configMap)
	}
	removed := model.APIRef("capabilities:detach")
	if !slices.Contains(session.Capabilities, removed) {
		t.Fatalf("session capabilities = %v, want %s", session.Capabilities, removed)
	}
	kept := slices.DeleteFunc(slices.Clone(session.Capabilities), func(c string) bool { return c == removed })
	body, _ := json.Marshal(map[string]any{"capabilities": kept})
	rec = do(http.MethodPut, "/api/console-sessions/"+session.ID, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/console-sessions/%s without %s: %d %s", session.ID, removed, rec.Code, rec.Body)
	}
	if saved := directorSession(t, rec); slices.Contains(saved.Capabilities, removed) {
		t.Errorf("saved capabilities = %v, still hold %s", saved.Capabilities, removed)
	}
}

// consoleSessionJSON is the part of a session the test reads.
type consoleSessionJSON struct {
	ID           string   `json:"id"`
	TaskID       string   `json:"taskId"`
	Capabilities []string `json:"capabilities"`
}

// directorSession is the director session in a console sessions
// response.
func directorSession(t *testing.T, rec *httptest.ResponseRecorder) consoleSessionJSON {
	t.Helper()
	var list struct {
		Sessions []consoleSessionJSON `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decoding sessions: %v (%s)", err, rec.Body)
	}
	for _, s := range list.Sessions {
		if s.ID == model.DirectorSessionID {
			return s
		}
	}
	t.Fatalf("console sessions = %s, missing %s", rec.Body, model.DirectorSessionID)
	return consoleSessionJSON{}
}
