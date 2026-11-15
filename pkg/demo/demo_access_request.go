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
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// openDemoDB opens the demo's throwaway database in dir: SQLite by default, or
// PostgreSQL when driver is "postgres" (starting an embedded server in dir
// when dsn is empty, or opening a fresh schema on dsn so concurrent Playwright
// workers never share tables).
func openDemoDB(ctx context.Context, dir, driver, dsn string) (*sql.DB, string, func(), error) {
	if driver == "" {
		driver = "sqlite"
	}
	switch driver {
	case "sqlite":
		if strings.TrimSpace(dsn) != "" {
			return nil, "", nil, fmt.Errorf("grain demo: -db-dsn only applies with -db-driver=postgres")
		}
		db, err := sqlite.Open(sqlite.DefaultConfig(dir))
		if err != nil {
			return nil, "", nil, fmt.Errorf("grain demo: opening the throwaway store: %v", err)
		}
		return db, "sqlite", func() { _ = db.Close() }, nil
	case "postgres":
		if strings.TrimSpace(dsn) == "" {
			pgCfg, stop, err := postgres.EnsureRunning(ctx, dir, postgres.WithoutCrashSafety())
			if err != nil {
				return nil, "", nil, fmt.Errorf("grain demo: starting throwaway postgres: %v", err)
			}
			db, err := postgres.Open(pgCfg)
			if err != nil {
				_ = stop()
				return nil, "", nil, fmt.Errorf("grain demo: opening throwaway postgres: %v", err)
			}
			return db, "postgres", func() {
				_ = db.Close()
				_ = stop()
			}, nil
		}
		adminCfg := postgres.DefaultConfig(dsn)
		admin, err := postgres.Open(adminCfg)
		if err != nil {
			return nil, "", nil, fmt.Errorf("grain demo: connecting to postgres: %v", err)
		}
		var randBytes [6]byte
		if _, err := rand.Read(randBytes[:]); err != nil {
			_ = admin.Close()
			return nil, "", nil, fmt.Errorf("grain demo: generating schema name: %v", err)
		}
		schema := "grain_demo_" + hex.EncodeToString(randBytes[:])
		if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)); err != nil {
			_ = admin.Close()
			return nil, "", nil, fmt.Errorf("grain demo: creating postgres schema %s: %v", schema, err)
		}
		scopedCfg := adminCfg
		scopedCfg.SearchPath = schema
		db, err := postgres.Open(scopedCfg)
		if err != nil {
			_, _ = admin.ExecContext(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, schema))
			_ = admin.Close()
			return nil, "", nil, fmt.Errorf("grain demo: opening postgres schema %s: %v", schema, err)
		}
		return db, "postgres", func() {
			_ = db.Close()
			_, _ = admin.ExecContext(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, schema))
			_ = admin.Close()
		}, nil
	default:
		return nil, "", nil, fmt.Errorf("grain demo: -db-driver must be \"sqlite\" or \"postgres\" (got %q)", driver)
	}
}

// accessRequestDemoHandler serves POST /api/e2e/tasks/{id}/pending-access to
// loopback callers, parking task id on a real model.PendingAccessRequest in
// store (just as a finished run's request_access call does) so e2e specs can
// exercise PendingAccess.jsx and POST /api/tasks/{id}/access-request/grant
// end-to-end against the real store.
func accessRequestDemoHandler(app http.Handler, store *model.Store) http.Handler {
	const prefix, suffix = "/api/e2e/tasks/", "/pending-access"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, suffix) {
			app.ServeHTTP(w, r)
			return
		}
		if !loopbackRequest(r) {
			http.Error(w, "the pending-access hook only answers loopback callers", http.StatusForbidden)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), suffix)
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Summary string `json:"summary"`
			Reason  string `json:"reason"`
			Items   any    `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "the body is {\"summary\": ..., \"reason\": ..., \"items\": [...]}", http.StatusBadRequest)
			return
		}
		// The items are checked the way a run's request_access call is
		// (model.ParseAccessRequestItems), so the card is only ever shown
		// a request a real run could have parked on.
		items, err := model.ParseAccessRequestItems(body.Items)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		task, err := store.GetTask(ctx, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if task == nil {
			http.NotFound(w, r)
			return
		}
		summary := strings.TrimSpace(body.Summary)
		if summary == "" {
			summary = "Grant requested access"
		}
		now := time.Now().UTC()
		commentID, err := store.AddComment(ctx, model.Comment{
			TaskID:    id,
			Body:      "**This run asked for more access:** " + summary,
			Summary:   summary,
			CreatedAt: now,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := store.ObserveField(ctx, id, now, func(o *model.Observation) {
			o.PendingQuestionCommentID = &commentID
			o.PendingAccess = &model.PendingAccessRequest{
				Summary:   summary,
				Reason:    strings.TrimSpace(body.Reason),
				Items:     items,
				CreatedAt: now,
			}
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}\n"))
	})
}
