//go:build ignore

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

// Command seed writes docs/test-plans/2026-11-14.md §3's heavy Workstreams
// data into a stopped daemon's SQLite store: one workstream whose root has
// a long multi-line goal and 45 merged plus 3 open subtasks spread over
// four repositories, and 2,000 more tasks in proposed-by trees (one 250
// deep, a cycle, a dangling parent, a two-parent task).
//
//	go run ./docs/test-runs/2026-11-14-next-heavy-data/seed/main.go -store /tmp/heavy-ui/data/store
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func main() {
	dir := flag.String("store", "", "the daemon's <data-dir>/store directory")
	wsLast := flag.Bool("ws-last", false, "file the heavy workstream after the 2,000 others, so its subtasks are the newest closed")
	flag.Parse()
	db, err := sqlite.Open(sqlite.DefaultConfig(*dir))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		log.Fatal(err)
	}
	now := time.Now().UTC()
	repos := []model.RepoRef{
		{Owner: "acme", Name: "widgets"}, {Owner: "acme", Name: "api"},
		{Owner: "acme", Name: "docs"}, {Owner: "acme", Name: "infra"},
	}
	n := 0
	put := func(id, title, desc string, repo model.RepoRef, done bool, parents ...string) {
		var links []model.Link
		for _, p := range parents {
			links = append(links, model.Link{Kind: model.LinkProposedBy, Target: p})
		}
		created := now.Add(-48*time.Hour + time.Duration(n)*time.Second)
		n++
		if err := store.PutTask(ctx, model.Task{
			ID: id, Title: title, Body: desc, Target: &repo, CreatedAt: &created,
			Intent:   model.IntentImplement,
			Filing:   model.Filing{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}}, Reason: model.ReasonDirect},
			Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}},
			Links:    links,
			Binding:  model.BindingDirective,
		}); err != nil {
			log.Fatalf("PutTask %s: %v", id, err)
		}
		if done {
			at := created.Add(time.Hour)
			if err := store.Observe(ctx, model.Observation{TaskID: id, ClosedAt: &at, CompletedAt: &at, PrMergedAt: &at}); err != nil {
				log.Fatalf("Observe %s: %v", id, err)
			}
		}
	}

	workstream := func() {
		goal := strings.Repeat("Make every /next/ page hold up under production-sized data: "+
			"thousands of tasks, deep proposal chains and many repositories.\n", 8) +
			"Done when the heavy-data walkthrough finds nothing to fix."
		put("100", "Harden the Workstreams heavy-data views", goal, repos[0], false)
		for i := 0; i < 45; i++ {
			put(fmt.Sprintf("%d", 101+i), fmt.Sprintf("Merged subtask %02d with a reasonably long title that wraps", i+1), "", repos[i%4], true, "100")
		}
		for i := 0; i < 3; i++ {
			put(fmt.Sprintf("%d", 146+i), fmt.Sprintf("Open subtask %d", i+1), "", repos[i%4], false, "100")
		}

	}
	if !*wsLast {
		workstream()
	}
	// 2,000 more: a chain 250 deep, a cycle, a dangling parent, a
	// two-parent task, and 1,740 tasks in 60 small trees.
	put("200", "chain root", "", repos[1], true)
	for i := 1; i < 250; i++ {
		put(fmt.Sprintf("%d", 200+i), fmt.Sprintf("chain link %d", i), "", repos[1], i%7 != 0, fmt.Sprintf("%d", 199+i))
	}
	put("500", "cycle a", "", repos[2], true, "501")
	put("501", "cycle b", "", repos[2], true, "502")
	put("502", "cycle c", "", repos[2], false, "500")
	put("503", "dangling child", "", repos[3], true, "99999")
	put("504", "two parents", "", repos[3], false, "200", "100")
	for tree := 0; tree < 60; tree++ {
		rootID := fmt.Sprintf("%d", 1000+tree*29)
		put(rootID, fmt.Sprintf("tree %d root", tree), "", repos[tree%4], true)
		for k := 1; k < 29; k++ {
			put(fmt.Sprintf("%d", 1000+tree*29+k), fmt.Sprintf("tree %d task %d", tree, k), "", repos[(tree+k)%4], k%3 != 0, fmt.Sprintf("%d", 1000+tree*29+(k-1)/2))
		}
	}
	if *wsLast {
		workstream()
	}
	fmt.Printf("seeded %d tasks\n", n)
}
