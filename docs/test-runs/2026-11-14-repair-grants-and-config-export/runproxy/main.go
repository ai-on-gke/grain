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

// Command runproxy stands in for the dispatch half of a run the walkthrough
// sandbox cannot make (it has no model credential): it opens a stopped
// daemon's store, records a live run of -task on sandbox walk-<task> exactly
// as dispatch does (Store.StartRun), hands that sandbox a token, and serves
// the daemon's own git proxy (daemon.StartGitProxy) over that store. The
// real in-sandbox CLI then reaches it the way a run's does:
//
//	GRAIN_SANDBOX_PROXY=<printed url> GRAIN_SANDBOX_TOKEN=walk-token grain ci list
//
// Everything from the token on -- SandboxNamespaceScope reading the task's
// stored grants, the /cli/ routes, the stored GitHub CI rows -- is the
// production path.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
)

func main() {
	driver := flag.String("db-driver", "sqlite", "sqlite or postgres")
	dsn := flag.String("db-dsn", "", "database DSN (sqlite: <data-dir>/store when empty)")
	dataDir := flag.String("data-dir", "", "the stopped daemon's -data-dir")
	scratch := flag.String("scratch", "", "a directory for the proxy's own token file")
	task := flag.String("task", "", "task whose run to start")
	flag.Parse()
	ctx := context.Background()
	store, _, err := daemon.OpenStore(*driver, *dsn, *dataDir)
	if err != nil {
		log.Fatal(err)
	}
	sandbox := "walk-" + *task
	run := model.Run{ID: fmt.Sprintf("%s-walk-%d", *task, time.Now().Unix()), TaskID: *task, Sandbox: sandbox, Attempt: 1, StartedAt: time.Now().UTC()}
	if err := store.StartRun(ctx, run, model.Limits{}); err != nil {
		log.Fatalf("starting run: %v", err)
	}
	defer func() {
		_ = store.FinishRun(context.Background(), run.ID, time.Now().UTC(), "failed", "walkthrough run ended")
	}()
	if err := os.MkdirAll(filepath.Join(*scratch, "secrets"), 0o700); err != nil {
		log.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{sandbox: "walk-token"})
	if err := os.WriteFile(filepath.Join(*scratch, "secrets", "sandbox-tokens.json"), raw, 0o600); err != nil {
		log.Fatal(err)
	}
	url, _, stop, err := daemon.StartGitProxy(*scratch, store, "127.0.0.1:4230", true, daemon.NewDirect(""), nil)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = stop(context.Background()) }()
	log.Printf("run %s of task %s live on %s; proxy %s", run.ID, *task, sandbox, url)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}
