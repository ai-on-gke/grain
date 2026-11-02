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

package demo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func newHostExecutorDemoServer(t *testing.T, executor string) func(method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runCfg, err := demoCLIRunConfig(ctx, executor)
	if err != nil {
		t.Fatalf("demoCLIRunConfig(%q): %v", executor, err)
	}
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	storedCfg := model.DefaultConfig()
	capabilityServers, err := configureCapabilityServerDemo(ctx, &storedCfg, func(string) (string, bool) {
		return "", false
	}, store, demoCapabilityHostd(runCfg))
	if err != nil {
		t.Fatalf("configureCapabilityServerDemo(%q): %v", executor, err)
	}
	if err := store.PutConfig(ctx, storedCfg); err != nil {
		t.Fatalf("storing initial config: %v", err)
	}
	cfg := ui.Config{
		Actor:             ui.DefaultActor("demo-operator"),
		DefaultTarget:     &repo,
		Capabilities:      append(ui.OfferedCapabilities(), ui.APICapabilities()...),
		CapabilityServers: capabilityServers,
		HostExecutor:      func(ctx context.Context) any { return runCfg.HostExecutorStatus(ctx) },
		Wake: func() {
			if stored, err := store.GetConfig(ctx); err == nil && stored != nil {
				capabilityServers.Update(stored.MCPServers)
			}
		},
	}
	srv := ui.NewServer(withAccessCode(cfg), store)
	return func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:40000"
		presentAccessCode(req)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
}

func TestDemoHostExecutorCapabilityServers(t *testing.T) {
	t.Parallel()

	t.Run("no-hostd", func(t *testing.T) {
		t.Parallel()
		for _, executor := range []string{"", "kubernetes", "none"} {
			do := newHostExecutorDemoServer(t, executor)
			rec := do(http.MethodGet, "/api/capability-servers/host-entries", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("executor=%q GET host-entries: %d %s", executor, rec.Code, rec.Body)
			}
			var got ui.CapabilityServerHostEntries
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("executor=%q decoding host-entries: %v", executor, err)
			}
			if got.Available || !strings.Contains(got.Reason, "no grain hostd beside it") {
				t.Errorf("executor=%q host-entries = %+v, want unavailable with no-hostd reason", executor, got)
			}
		}
	})

	t.Run("hostd-unreachable", func(t *testing.T) {
		t.Parallel()
		for _, executor := range []string{"hostd-down", "hostd-remote-down"} {
			do := newHostExecutorDemoServer(t, executor)
			rec := do(http.MethodGet, "/api/capability-servers/host-entries", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("executor=%q GET host-entries: %d %s", executor, rec.Code, rec.Body)
			}
			var got ui.CapabilityServerHostEntries
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("executor=%q decoding host-entries: %v", executor, err)
			}
			if got.Available || !strings.Contains(got.Reason, "asking grain hostd what it offers") {
				t.Errorf("executor=%q host-entries = %+v, want unavailable with hostd query error", executor, got)
			}
		}
	})

	for _, executor := range []string{"hostd", "hostd-remote"} {
		t.Run(executor, func(t *testing.T) {
			t.Parallel()
			if runtime.GOOS != "linux" {
				t.Skip("grain hostd runs on Linux only")
			}
			if os.Geteuid() == 0 {
				t.Skip("grain hostd refuses to run as root")
			}
			do := newHostExecutorDemoServer(t, executor)
			rec := do(http.MethodGet, "/api/capability-servers/host-entries", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("GET host-entries: %d %s", rec.Code, rec.Body)
			}
			var got ui.CapabilityServerHostEntries
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decoding host-entries: %v", err)
			}
			if !got.Available || !slices.Equal(got.Entries, []string{"corp-search"}) {
				t.Fatalf("host-entries = %+v, want Available=true Entries=[corp-search]", got)
			}
			if len(got.Missing) == 0 {
				t.Errorf("host-entries Missing is empty; demo hostd omits grain's built-in entries so missing-entry notes can be exercised")
			}

			// A run: host server naming the demo hostd's corp-search entry is accepted.
			rec = do(http.MethodPut, "/api/capability-servers",
				`{"id":"https://capabilities.demo.grain.example/corp-search","config":{"command":"corp-search","run":"host"}}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("PUT /api/capability-servers (corp-search): %d %s", rec.Code, rec.Body)
			}

			// One naming an entry the allowlist lacks is refused naming corp-search.
			rec = do(http.MethodPut, "/api/capability-servers",
				`{"id":"https://capabilities.demo.grain.example/other","config":{"command":"unlisted-cap","run":"host"}}`)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "corp-search") {
				t.Errorf("PUT /api/capability-servers (unlisted-cap): %d %s, want 400 naming corp-search", rec.Code, rec.Body)
			}
		})
	}
}
