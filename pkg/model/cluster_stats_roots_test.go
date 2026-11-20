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
	"math/rand/v2"
	"sort"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// legacyDirectionRoot is the per-task walk ClusterStats used before the
// batched resolver (2c9fff5d0, directionRootTaskID): follow the
// lexically smallest proposed-by target, else the task WorkOrigin,
// until a task with neither, a task that does not exist, or a task
// already on the path. It reads each step through GetTask so it is an
// oracle independent of the batched SQL.
func legacyDirectionRoot(ctx context.Context, t *testing.T, store *model.Store, id string, cache map[string]string) string {
	t.Helper()
	if root, ok := cache[id]; ok {
		return root
	}
	seen := map[string]bool{}
	var path []string
	cur := id
	for {
		if root, ok := cache[cur]; ok {
			cur = root
			break
		}
		if seen[cur] {
			break
		}
		seen[cur] = true
		path = append(path, cur)
		task, err := store.GetTask(ctx, cur)
		if err != nil {
			t.Fatalf("GetTask %s: %v", cur, err)
		}
		if task == nil {
			break
		}
		parent := ""
		for _, l := range task.Links {
			if l.Kind == model.LinkProposedBy && (parent == "" || l.Target < parent) {
				parent = l.Target
			}
		}
		next := ""
		switch {
		case parent != "" && parent != cur:
			next = parent
		case task.WorkOrigin.Task() != "" && task.WorkOrigin.Task() != cur:
			next = task.WorkOrigin.Task()
		}
		if next == "" {
			break
		}
		cur = next
	}
	for _, p := range path {
		cache[p] = cur
	}
	return cur
}

// seedHeavyProposedByForest files more than 2,000 tasks whose
// proposed-by links form the shapes the batched resolver has to agree
// with the legacy walk on: a chain deeper than 200, a cycle entered from
// outside it, a dangling parent, a task with two proposed-by links, a
// self link that falls back to WorkOrigin, WorkOrigin-only children and
// a random forest. Every task completes inside [since, since+7d), so
// every one of them is rolled up. It returns the IDs it filed.
func seedHeavyProposedByForest(ctx context.Context, t *testing.T, store *model.Store, since time.Time) []string {
	t.Helper()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	var ids []string
	put := func(id string, origin model.WorkOrigin, parents ...string) {
		t.Helper()
		links := make([]model.Link, 0, len(parents))
		for _, p := range parents {
			links = append(links, model.Link{Kind: model.LinkProposedBy, Target: p})
		}
		created := since.Add(time.Duration(len(ids)) * time.Second)
		if err := store.PutTask(ctx, model.Task{
			ID: id, Title: "task " + id, Target: &repo, CreatedAt: &created,
			Filing:     model.Filing{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}}},
			Links:      links,
			WorkOrigin: origin,
			Binding:    model.BindingDirective,
		}); err != nil {
			t.Fatalf("PutTask %s: %v", id, err)
		}
		done := created.Add(time.Minute)
		if err := store.Observe(ctx, model.Observation{TaskID: id, CompletedAt: &done}); err != nil {
			t.Fatalf("Observe %s: %v", id, err)
		}
		ids = append(ids, id)
	}

	// A chain 250 deep: d-0249 -> ... -> d-0000.
	put("d-0000", "")
	for i := 1; i < 250; i++ {
		put(fmt.Sprintf("d-%04d", i), "", fmt.Sprintf("d-%04d", i-1))
	}
	// A cycle y-a -> y-b -> y-c -> y-a, entered from y-tail -> y-b.
	put("y-a", "", "y-b")
	put("y-b", "", "y-c")
	put("y-c", "", "y-a")
	put("y-tail", "", "y-b")
	// A dangling parent: g-missing is never filed.
	put("g-child", "", "g-missing")
	put("g-grand", "", "g-child")
	// Two proposed-by links: MIN(target) picks m-alpha over m-zeta.
	put("m-alpha", "")
	put("m-zeta", "")
	put("m-two", "", "m-zeta", "m-alpha")
	put("m-two-child", "", "m-two")
	// WorkOrigin only, and a self link that falls back to WorkOrigin.
	put("o-root", "")
	put("o-child", model.TaskOrigin("o-root"))
	put("s-self", model.TaskOrigin("o-root"), "s-self")
	// A random forest of 1,750 tasks in 50 trees, some with two parents.
	rng := rand.New(rand.NewPCG(5728, 2026))
	for tree := 0; tree < 50; tree++ {
		put(fmt.Sprintf("f-%02d-000", tree), "")
		for n := 1; n < 35; n++ {
			p1 := fmt.Sprintf("f-%02d-%03d", tree, rng.IntN(n))
			if p2 := fmt.Sprintf("f-%02d-%03d", tree, rng.IntN(n)); rng.IntN(10) == 0 && p2 != p1 {
				put(fmt.Sprintf("f-%02d-%03d", tree, n), "", p1, p2)
			} else {
				put(fmt.Sprintf("f-%02d-%03d", tree, n), "", p1)
			}
		}
	}
	if len(ids) < 2000 {
		t.Fatalf("seeded %d tasks, want at least 2,000", len(ids))
	}
	return ids
}

type directionTally struct {
	completed, spawned int
}

// TestClusterStatsDirectionRootsMatchLegacyWalkOnHeavyData compares the
// batched direction-root resolver against the pre-batching per-task walk
// on both stores. The two agree on every task once the legacy walk is
// run in the same order the batched one now uses (ascending task ID):
// the only deliberate difference is that the old rollup ranged over a Go
// map, so which member of a proposed-by cycle became the root changed
// from call to call. The batched resolver is deterministic.
func TestClusterStatsDirectionRootsMatchLegacyWalkOnHeavyData(t *testing.T) {
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	until := since.AddDate(0, 0, 7)
	stores := map[string]func(t *testing.T) *model.Store{
		"sqlite": func(t *testing.T) *model.Store {
			db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
			if err != nil {
				t.Fatalf("opening sqlite: %v", err)
			}
			t.Cleanup(func() { db.Close() })
			store := model.New(db)
			if err := store.Init(context.Background()); err != nil {
				t.Fatalf("Init: %v", err)
			}
			return store
		},
		"postgres": func(t *testing.T) *model.Store {
			store, _ := postgrestest.Open(t)
			return store
		},
	}
	for name, openStore := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := openStore(t)
			if store == nil {
				return
			}
			ids := seedHeavyProposedByForest(ctx, t, store, since)
			sort.Strings(ids)

			cache := map[string]string{}
			want := map[string]directionTally{}
			for _, id := range ids {
				root := legacyDirectionRoot(ctx, t, store, id, cache)
				d := want[root]
				d.completed++
				if id != root {
					d.spawned++
				}
				want[root] = d
			}

			stats, err := store.ClusterStats(ctx, since, until)
			if err != nil {
				t.Fatalf("ClusterStats: %v", err)
			}
			got := map[string]directionTally{}
			for _, d := range stats.Directions {
				got[d.TaskID] = directionTally{completed: d.TasksCompleted, spawned: d.TasksSpawned}
			}
			for root, w := range want {
				if got[root] != w {
					t.Errorf("direction %s = %+v, legacy walk says %+v", root, got[root], w)
				}
			}
			for root := range got {
				if _, ok := want[root]; !ok {
					t.Errorf("direction %s = %+v, legacy walk has no such root", root, got[root])
				}
			}

			// Spot-check the shapes by name.
			for _, c := range []struct {
				root string
				want directionTally
			}{
				{"d-0000", directionTally{completed: 250, spawned: 249}},
				// Ascending order reaches y-a first, so the whole cycle and
				// the task entering it roll up under y-a.
				{"y-a", directionTally{completed: 4, spawned: 3}},
				// The dangling parent is the root of the two tasks under it,
				// though it is no task of its own.
				{"g-missing", directionTally{completed: 2, spawned: 2}},
				{"m-alpha", directionTally{completed: 3, spawned: 2}},
				{"m-zeta", directionTally{completed: 1, spawned: 0}},
				{"o-root", directionTally{completed: 3, spawned: 2}},
			} {
				if got[c.root] != c.want {
					t.Errorf("direction %s = %+v, want %+v", c.root, got[c.root], c.want)
				}
			}
			for _, d := range stats.Directions {
				if d.TaskID == "g-missing" && d.Title != "" {
					t.Errorf("dangling root title = %q, want empty", d.Title)
				}
			}

			// The deliberate difference: the legacy walk's cycle root
			// depended on visiting order. Started at y-tail it picks y-b.
			if root := legacyDirectionRoot(ctx, t, store, "y-tail", map[string]string{}); root != "y-b" {
				t.Errorf("legacy walk from y-tail = %s, want y-b (order-dependent cycle root)", root)
			}
			again, err := store.ClusterStats(ctx, since, until)
			if err != nil {
				t.Fatalf("ClusterStats again: %v", err)
			}
			for i := range again.Directions {
				if again.Directions[i].TaskID != stats.Directions[i].TaskID {
					t.Fatalf("direction %d = %s then %s, want a stable order", i, stats.Directions[i].TaskID, again.Directions[i].TaskID)
				}
			}
		})
	}
}
