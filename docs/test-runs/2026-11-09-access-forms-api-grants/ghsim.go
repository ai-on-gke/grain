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

// ghsim serves pkg/github/githubsim's REST API over plain HTTP for one
// repository whose default branch is not main (acme/widgets on trunk), so
// a real `grain daemon -github-host 127.0.0.1:<port> -github-insecure-http`
// resolves that default branch the way it would on GitHub
// (Config.GitHubDefaultBranches, model.AlignDefaultBases).
//
//	go run ghsim.go -addr 127.0.0.1:4294 -bare /path/widgets.git -default trunk
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4294", "listen address")
	owner := flag.String("owner", "acme", "repository owner")
	repo := flag.String("repo", "widgets", "repository name")
	bare := flag.String("bare", "", "bare repository backing the sim's branches")
	def := flag.String("default", "trunk", "the repository's default branch")
	flag.Parse()
	sim := githubsim.New(*owner, *repo, *bare, *def)
	log.Printf("ghsim: %s/%s (default %s) on %s", *owner, *repo, sim.DefaultBranch, *addr)
	log.Fatal(http.ListenAndServe(*addr, sim.Handler()))
}
