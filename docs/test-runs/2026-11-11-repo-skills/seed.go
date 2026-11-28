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

// seed writes, before a fresh daemon's first boot, the one thing the
// repo-skills walkthrough cannot set through the API: a git credential
// ("bot", the simulator's token) and a git connection for acme/widgets, so
// a dispatched run can clone from and push to ghsim.go.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func main() {
	dataDir := flag.String("data-dir", "", "grain daemon -data-dir")
	token := flag.String("token", "tok-walkthrough", "simulated GitHub token")
	flag.Parse()
	if *dataDir == "" {
		log.Fatal("-data-dir is required")
	}
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(*dataDir, "store")))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		log.Fatalf("store.Init: %v", err)
	}
	ghCredDir := filepath.Join(*dataDir, "secrets", "github")
	if err := os.MkdirAll(ghCredDir, 0o700); err != nil {
		log.Fatal(err)
	}
	credSet, err := gitproxy.LoadCredentialSet(ghCredDir)
	if err != nil {
		log.Fatal(err)
	}
	if err := credSet.SetToken("bot", *token); err != nil {
		log.Fatal(err)
	}
	if err := store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.GitCredential = "bot"
		cfg.GitConnections = []model.TeamGitConnection{{Credential: "bot", Scope: "acme/widgets"}}
		return nil
	}); err != nil {
		log.Fatalf("UpdateConfig: %v", err)
	}
}
