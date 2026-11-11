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

package model_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
)

// docs/test-runs/2026-11-10-agent-api-paging.md, F7: GET
// /api/access/history read the newest entries of every surface and kept
// the access ones, so it answered nothing once enough other changes were
// newer. ConfigAuditOfSurface matches the surface before the limit, on
// both drivers.
func TestConfigAuditOfSurfaceMatchesBeforeTheLimit(t *testing.T) {
	t.Parallel()
	backends := []struct {
		name string
		open func(t *testing.T) *model.Store
	}{
		{name: "sqlite", open: openTestStore},
		{name: "postgres", open: func(t *testing.T) *model.Store {
			st, _ := postgrestest.Open(t)
			return st
		}},
	}
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := b.open(t)
			by := model.Principal{Kind: model.PrincipalHuman, ID: "owner@example.com"}
			base := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
			for i := range 3 {
				if _, err := store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
					At: base.Add(time.Duration(i) * time.Second), By: by,
					Surface: "access", Summary: fmt.Sprintf("allowed user:member-%d@example.com", i),
				}); err != nil {
					t.Fatal(err)
				}
			}
			for i := range 20 {
				if _, err := store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
					At: base.Add(time.Hour + time.Duration(i)*time.Second), By: by,
					Surface: "capability-servers", Summary: fmt.Sprintf("PUT cap-%03d", i),
				}); err != nil {
					t.Fatal(err)
				}
			}
			if all, err := store.ConfigAudit(ctx, 10); err != nil || len(all) != 10 || all[0].Surface != "capability-servers" {
				t.Fatalf("ConfigAudit(10) = %d entries (%v), want the 10 newest of any surface", len(all), err)
			}
			got, err := store.ConfigAuditOfSurface(ctx, "access", 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 3 {
				t.Fatalf("ConfigAuditOfSurface(access, 10) = %d entries, want all 3 access changes", len(got))
			}
			for i, e := range got {
				want := fmt.Sprintf("allowed user:member-%d@example.com", 2-i)
				if e.Surface != "access" || e.Summary != want {
					t.Errorf("entry %d = %s %q, want access %q (newest first)", i, e.Surface, e.Summary, want)
				}
			}
			if two, err := store.ConfigAuditOfSurface(ctx, "access", 2); err != nil || len(two) != 2 {
				t.Errorf("ConfigAuditOfSurface(access, 2) = %d entries (%v), want 2", len(two), err)
			}
			if every, err := store.ConfigAuditOfSurface(ctx, "access", 0); err != nil || len(every) != 3 {
				t.Errorf("ConfigAuditOfSurface(access, 0) = %d entries (%v), want every one (3)", len(every), err)
			}
			if _, err := store.ConfigAuditOfSurface(ctx, " ", 10); err == nil {
				t.Errorf("ConfigAuditOfSurface with no surface should be refused")
			}
		})
	}
}
