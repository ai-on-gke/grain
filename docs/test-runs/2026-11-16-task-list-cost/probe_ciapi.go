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

//go:build ignore

// probe_ciapi.go probes GET /api/tasks/summaries through a real ciapi.Endpoint
// wired in front of ui.Server over the seeded PostgreSQL or SQLite store (§2
// item 3 / §10 CI API token).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/ciapi"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func main() {
	pgURL := flag.String("postgres-url", "", "PostgreSQL URL")
	sqliteDir := flag.String("sqlite-dir", "", "SQLite data directory")
	label := flag.String("label", "store", "label for output")
	flag.Parse()

	ctx := context.Background()
	var store *model.Store
	var closeFn func() error
	switch {
	case *pgURL != "":
		db, err := postgres.Open(postgres.Config{URL: *pgURL})
		if err != nil {
			log.Fatalf("open postgres: %v", err)
		}
		store, closeFn = model.New(db), db.Close
	case *sqliteDir != "":
		db, err := sqlite.Open(sqlite.DefaultConfig(*sqliteDir))
		if err != nil {
			log.Fatalf("open sqlite: %v", err)
		}
		store, closeFn = model.New(db), db.Close
	default:
		log.Fatal("pass -postgres-url or -sqlite-dir")
	}
	defer func() { _ = closeFn() }()

	uiSrv := ui.NewServer(ui.Config{Actor: ui.DefaultActor("admin@example.com")}, store)
	gate := ciapi.NewEndpoint()
	gate.Log = func(string, ...any) {}
	gate.SetUpstream(uiSrv)
	ts := httptest.NewServer(gate)
	defer ts.Close()
	gate.SetURL(ts.URL)

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, readTok, err := gate.Tokens.Mint(ciapi.Grant{
		Repo:    widgets,
		RunID:   "ci-walk-1",
		Expires: time.Now().Add(time.Hour),
		Scopes:  []model.APIScope{"tasks:read"},
	})
	if err != nil {
		log.Fatalf("mint read token: %v", err)
	}
	_, ciOnlyTok, err := gate.Tokens.Mint(ciapi.Grant{
		Repo:    widgets,
		RunID:   "ci-walk-2",
		Expires: time.Now().Add(time.Hour),
		Scopes:  []model.APIScope{"ci:read"},
	})
	if err != nil {
		log.Fatalf("mint ci-only token: %v", err)
	}

	httpClient := ts.Client()
	do := func(tok, path string, extraHeaders map[string]string) (int, http.Header, []byte) {
		req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if err != nil {
			log.Fatalf("new request %s: %v", path, err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			log.Fatalf("do %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, b
	}

	fmt.Printf("=== CI API token GET /api/tasks/summaries probes (%s) ===\n", *label)

	// 1. Token lacking tasks:read -> 403
	code, _, body := do(ciOnlyTok, "/api/tasks/summaries?ids=4997", nil)
	if code != http.StatusForbidden {
		log.Fatalf("ci-only token: got %d (%s), want 403", code, body)
	}
	fmt.Println("  PASS [ci-api ci-only]: 403 Forbidden")

	// 2. Mixed readable (4997), foreign (4998), no-repo (4999), missing (999999)
	code, hdr, body := do(readTok, "/api/tasks/summaries?ids=4997,4998,4999,999999", nil)
	if code != http.StatusOK {
		log.Fatalf("mixed ids: got %d (%s), want 200", code, body)
	}
	var parsed struct {
		Tasks map[string]json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		log.Fatalf("decode mixed: %v", err)
	}
	if len(parsed.Tasks) != 1 || parsed.Tasks["4997"] == nil {
		log.Fatalf("mixed ids returned keys %v, want only 4997", parsed.Tasks)
	}
	if strings.Contains(string(body), "Secret body") || strings.Contains(string(body), "No-repo body") {
		log.Fatalf("mixed ids leaked foreign/no-repo body!")
	}
	fmt.Printf("  PASS [ci-api widgets]: mixed ids -> keys=[4997], ETag=%s\n", hdr.Get("ETag"))

	// 3. Identical omission for foreign (4998), no-repo (4999), missing (999999)
	cF, hF, bF := do(readTok, "/api/tasks/summaries?ids=4998", nil)
	cN, hN, bN := do(readTok, "/api/tasks/summaries?ids=4999", nil)
	cM, hM, bM := do(readTok, "/api/tasks/summaries?ids=999999", nil)
	if cF != 200 || cN != 200 || cM != 200 {
		log.Fatalf("omission statuses: foreign=%d norepo=%d missing=%d, want all 200", cF, cN, cM)
	}
	if string(bF) != string(bM) || string(bN) != string(bM) {
		log.Fatalf("omission bodies diverged: foreign=%q norepo=%q missing=%q", bF, bN, bM)
	}
	if hF.Get("ETag") != hM.Get("ETag") || hN.Get("ETag") != hM.Get("ETag") {
		log.Fatalf("omission ETags diverged: foreign=%q norepo=%q missing=%q", hF.Get("ETag"), hN.Get("ETag"), hM.Get("ETag"))
	}
	fmt.Printf("  PASS [ci-api widgets]: identical omission foreign(4998)==no-repo(4999)==missing(999999) -> 200 %s ETag=%s\n",
		strings.TrimSpace(string(bM)), hM.Get("ETag"))

	// 4. 50 unique + dup -> 200; 51 unique -> 400; malformed -> 400
	var ids50, ids51 []string
	for i := 1; i <= 51; i++ {
		if i <= 50 {
			ids50 = append(ids50, fmt.Sprint(i))
		}
		ids51 = append(ids51, fmt.Sprint(i))
	}
	if c, _, b := do(readTok, "/api/tasks/summaries?ids="+strings.Join(ids50, ",")+",1&comments_limit=1&attempts_limit=1", nil); c != 200 {
		log.Fatalf("50+dup: got %d (%s), want 200", c, b)
	}
	if c, _, b := do(readTok, "/api/tasks/summaries?ids="+strings.Join(ids51, ","), nil); c != 400 {
		log.Fatalf("51 unique: got %d (%s), want 400", c, b)
	}
	for _, bq := range []string{"", "ids=", "ids=bad/id", "ids=../1", "ids=4997,,4998", "ids=-1", "ids=4997&comments_limit=0"} {
		p := "/api/tasks/summaries"
		if bq != "" {
			p += "?" + bq
		}
		if c, _, b := do(readTok, p, nil); c != 400 {
			log.Fatalf("malformed %q: got %d (%s), want 400", bq, c, b)
		}
	}

	// 5. ?repo= handling: foreign repo -> 403 at CI proxy; own repo -> 200 (rewritten job repo)
	if c, _, b := do(readTok, "/api/tasks/summaries?ids=4997&repo=acme/secret", nil); c != 403 {
		log.Fatalf("foreign ?repo=: got %d (%s), want 403", c, b)
	}
	if c, _, b := do(readTok, "/api/tasks/summaries?ids=4997&repo=acme/widgets", nil); c != 200 {
		log.Fatalf("own ?repo=: got %d (%s), want 200", c, b)
	}
	fmt.Println("  PASS [ci-api widgets]: 50+dup=200, 51=400, 7 malformed=400, foreign ?repo==403, own ?repo==200")

	// 6. ETag changes when 4997 changes, stays unchanged when 4998 (foreign) changes.
	_, h1, _ := do(readTok, "/api/tasks/summaries?ids=4997,4998&comments_limit=1&attempts_limit=1", nil)
	et1 := h1.Get("ETag")
	if et1 == "" {
		log.Fatal("missing ETag on CI API summaries response")
	}
	attr := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "admin@example.com"}}
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    "4998",
		Author:    attr,
		Body:      "ci-api probe foreign mutation",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		log.Fatalf("add comment 4998: %v", err)
	}
	_, hForeign, _ := do(readTok, "/api/tasks/summaries?ids=4997,4998&comments_limit=1&attempts_limit=1", nil)
	if hForeign.Get("ETag") != et1 {
		log.Fatalf("CI API ETag changed on foreign task mutation: %s -> %s", et1, hForeign.Get("ETag"))
	}
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    "4997",
		Author:    attr,
		Body:      "ci-api probe readable mutation",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		log.Fatalf("add comment 4997: %v", err)
	}
	_, h2, _ := do(readTok, "/api/tasks/summaries?ids=4997,4998&comments_limit=1&attempts_limit=1", nil)
	et2 := h2.Get("ETag")
	if et2 == "" || et2 == et1 {
		log.Fatalf("CI API ETag did not change after readable mutation: %s vs %s", et1, et2)
	}
	// Check whether If-None-Match works through CI API proxy too (diagnostic, not fatal if proxy omits header).
	c304, _, _ := do(readTok, "/api/tasks/summaries?ids=4997,4998&comments_limit=1&attempts_limit=1", map[string]string{"If-None-Match": et2})
	fmt.Printf("  PASS [ci-api widgets]: ETag=%s (unchanged after foreign 4998 write) -> %s (after 4997 write); If-None-Match status=%d\n", et1, et2, c304)
}
