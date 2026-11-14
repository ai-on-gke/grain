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

// ghsim serves pkg/github/githubsim over plain HTTP for acme/widgets with
// git-http-backend, recorded merge commits, a reachable bot token and one
// seeded issue (for the github#read-issues authority probe).
package main

import (
	"flag"
	"log"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4285", "listen address")
	owner := flag.String("owner", "acme", "repository owner")
	repo := flag.String("repo", "widgets", "repository name")
	bare := flag.String("bare", "", "bare repository backing the sim")
	def := flag.String("default", "main", "default branch")
	flag.Parse()

	sim := githubsim.New(*owner, *repo, *bare, *def)
	sim.RecordMergeCommits = true
	sim.MergeAs = "alice"
	sim.Reachable = map[string][]githubsim.ReachableRepo{
		"ghp_walkthrough": {{FullName: *owner + "/" + *repo, Push: true}},
	}
	baseHandler := sim.Handler()

	gitPath, err := exec.LookPath("git")
	if err != nil {
		log.Fatalf("git not found: %v", err)
	}
	gitBackend := &cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + filepath.Dir(*bare), "GIT_HTTP_EXPORT_ALL=1"},
	}

	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/"+*owner+"/"+*repo+".git/") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r2.URL.Path, "/"+*owner)
			gitBackend.ServeHTTP(w, r2)
			return
		}
		clean := strings.TrimPrefix(r.URL.Path, "/api/v3")
		if r.Method == http.MethodGet && clean == "/user" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"login":"walk-bot","name":"Walk Bot","email":"walk-bot@example.com"}` + "\n"))
			return
		}
		if r.Method == http.MethodGet && clean == "/repos/"+*owner+"/"+*repo+"/issues" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"number":42,"title":"Walkthrough release note issue","state":"open","body":"Seeded issue.","user":{"login":"alice"}}]` + "\n"))
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if clean != r.URL.Path {
			r2 := r.Clone(r.Context())
			r2.URL.Path = clean
			baseHandler.ServeHTTP(w, r2)
			return
		}
		baseHandler.ServeHTTP(w, r)
	})

	log.Printf("ghsim: %s/%s (default %s) on %s", *owner, *repo, sim.DefaultBranch, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
