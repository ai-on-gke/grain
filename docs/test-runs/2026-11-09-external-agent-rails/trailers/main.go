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

// trailers prints orchestrator.CommitTrailers -- the trailers a run's
// commits are written with -- for tasks read out of a real daemon's
// SQLite store, one task per line:
//
//	go run trailers/main.go -store /var/tmp/ext-rails/data/store 5 6
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func main() {
	dir := flag.String("store", "", "the daemon's store directory (holding grain.db)")
	flag.Parse()
	db, err := sqlite.Open(sqlite.DefaultConfig(*dir))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	store := model.New(db)
	for _, id := range flag.Args() {
		task, err := store.GetTask(context.Background(), id)
		if err != nil || task == nil {
			log.Fatalf("task %s: %v", id, err)
		}
		fmt.Printf("%s\t%s\n", id, strings.Join(orchestrator.CommitTrailers(*task), " | "))
	}
}
