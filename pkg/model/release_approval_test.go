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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func TestReleaseApprovalDecisionStoreRoundTrip(t *testing.T) {
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

			repo := model.RepoRef{Host: "git.forge.example", Owner: "acme", Name: "widgets"}
			c1 := strings.Repeat("a", 40)
			c2 := strings.Repeat("b", 40)
			t0 := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

			got, err := store.GetReleaseApprovalDecision(ctx, repo, "v2.6.0", c1)
			if err != nil {
				t.Fatalf("GetReleaseApprovalDecision (empty): %v", err)
			}
			if got != nil {
				t.Fatalf("GetReleaseApprovalDecision (empty) = %+v, want nil", got)
			}

			want := model.ReleaseApprovalDecision{
				Repo:      repo,
				Version:   "v2.6.0",
				Commit:    c1,
				Decision:  "approve",
				Workflow:  "promote-release.yml",
				DecidedBy: "alice@example.com",
				DecidedAt: t0,
			}
			if err := store.PutReleaseApprovalDecision(ctx, want); err != nil {
				t.Fatalf("PutReleaseApprovalDecision: %v", err)
			}
			got, err = store.GetReleaseApprovalDecision(ctx, repo, "v2.6.0", c1)
			if err != nil {
				t.Fatalf("GetReleaseApprovalDecision: %v", err)
			}
			if got == nil || *got != want {
				t.Fatalf("GetReleaseApprovalDecision = %+v, want %+v", got, want)
			}
			if !got.ActiveAt(t0.Add(20 * time.Minute)) {
				t.Errorf("decision should stay active past 15 minutes while within ReleaseApprovalHold")
			}
			if got.ActiveAt(t0.Add(model.ReleaseApprovalHold)) {
				t.Errorf("decision should expire at ReleaseApprovalHold")
			}

			// A different commit of the same version is distinct, and updating
			// the first commit replaces it in place.
			if other, err := store.GetReleaseApprovalDecision(ctx, repo, "v2.6.0", c2); err != nil || other != nil {
				t.Fatalf("GetReleaseApprovalDecision(c2) = %+v, %v; want nil", other, err)
			}
			second := model.ReleaseApprovalDecision{
				Repo:      repo,
				Version:   "v2.6.0",
				Commit:    c2,
				Decision:  "reject",
				Workflow:  "promote-release.yml",
				DecidedBy: "bob@example.com",
				DecidedAt: t0.Add(time.Hour),
			}
			if err := store.PutReleaseApprovalDecision(ctx, second); err != nil {
				t.Fatalf("PutReleaseApprovalDecision(c2): %v", err)
			}
			want.DecidedAt = t0.Add(25 * time.Hour)
			if err := store.PutReleaseApprovalDecision(ctx, want); err != nil {
				t.Fatalf("PutReleaseApprovalDecision(c1 replace): %v", err)
			}
			list, err := store.ListReleaseApprovalDecisions(ctx, repo)
			if err != nil {
				t.Fatalf("ListReleaseApprovalDecisions: %v", err)
			}
			if len(list) != 2 || list[0] != want || list[1] != second {
				t.Fatalf("ListReleaseApprovalDecisions = %+v, want [%+v %+v]", list, want, second)
			}
		})
	}
}
