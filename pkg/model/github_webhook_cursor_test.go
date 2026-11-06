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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func TestGitHubWebhookCursorStoreRoundTrip(t *testing.T) {
	t.Parallel()

	backends := []struct {
		name string
		open func(t *testing.T) *model.Store
	}{
		{
			name: "sqlite",
			open: func(t *testing.T) *model.Store {
				db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				st := model.New(db)
				if err := st.Init(context.Background()); err != nil {
					t.Fatal(err)
				}
				return st
			},
		},
		{
			name: "postgres",
			open: func(t *testing.T) *model.Store {
				st, _ := postgrestest.Open(t)
				return st
			},
		},
	}

	for _, b := range backends {
		b := b
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := b.open(t)

			cur, err := store.GetGitHubWebhookCursor(ctx)
			if err != nil {
				t.Fatalf("GetGitHubWebhookCursor (empty): %v", err)
			}
			if cur != nil {
				t.Fatalf("GetGitHubWebhookCursor (empty) = %+v, want nil", cur)
			}

			t1 := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
			if err := store.SetGitHubWebhookCursor(ctx, 42, t1); err != nil {
				t.Fatalf("SetGitHubWebhookCursor(42): %v", err)
			}
			cur, err = store.GetGitHubWebhookCursor(ctx)
			if err != nil {
				t.Fatalf("GetGitHubWebhookCursor(42): %v", err)
			}
			if cur == nil || cur.LastAckedSeq != 42 || !cur.UpdatedAt.Equal(t1) {
				t.Fatalf("GetGitHubWebhookCursor = %+v, want seq=42 updatedAt=%v", cur, t1)
			}

			// Verify 64-bit sequence numbers past int32 round-trip cleanly.
			const bigSeq uint64 = 1 << 40
			t2 := t1.Add(5 * time.Minute)
			if err := store.SetGitHubWebhookCursor(ctx, bigSeq, t2); err != nil {
				t.Fatalf("SetGitHubWebhookCursor(%d): %v", bigSeq, err)
			}
			cur, err = store.GetGitHubWebhookCursor(ctx)
			if err != nil {
				t.Fatalf("GetGitHubWebhookCursor(%d): %v", bigSeq, err)
			}
			if cur == nil || cur.LastAckedSeq != bigSeq || !cur.UpdatedAt.Equal(t2) {
				t.Fatalf("GetGitHubWebhookCursor = %+v, want seq=%d updatedAt=%v", cur, bigSeq, t2)
			}
		})
	}
}
