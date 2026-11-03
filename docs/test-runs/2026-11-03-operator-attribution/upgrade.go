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

// Item 4 of docs/test-plans/2026-11-03.md §3: open a store a schema-85
// grain wrote (75331e14's `grain demo`, its directory kept by killing
// it) with this tree's store, which migrates it the way a daemon's
// restart does, then print what the task API says about the rows written
// before sources existed.
//
//	go run docs/test-runs/2026-11-03-operator-attribution/upgrade.go DIR TASK
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func main() {
	dir, id := os.Args[1], os.Args[2]
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		log.Fatal(err)
	}
	var version int
	if err := db.QueryRowContext(ctx, "SELECT MAX(`version`) FROM `grain_schema`").Scan(&version); err != nil {
		log.Fatal(err)
	}
	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("owner@example.com")}, store)
	detail, err := client.GetTask(ctx, id)
	if err != nil {
		log.Fatal(err)
	}
	type comment struct{ Author, AuthorKind, AuthorLabel, Body string }
	type step struct{ State, By, ByLabel, Reason, Detail string }
	out := struct {
		Schema      int
		Author      string
		AuthorKind  string
		AuthorLabel string
		Comments    []comment
		Transitions []step
	}{Schema: version, Author: detail.Author, AuthorKind: detail.AuthorKind, AuthorLabel: detail.AuthorLabel}
	for _, c := range detail.Comments {
		out.Comments = append(out.Comments, comment{c.Author, c.AuthorKind, c.AuthorLabel, c.Body})
	}
	for _, tr := range detail.Transitions {
		out.Transitions = append(out.Transitions, step{string(tr.State), tr.By, tr.ByLabel, tr.Reason, tr.Detail})
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}
