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

// ghsim serves pkg/github/githubsim over plain HTTP for acme/widgets and
// acme/gadgets with git http-backend and walkthrough control routes under /_sim/*.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4294", "listen address")
	owner := flag.String("owner", "acme", "repository owner")
	repo := flag.String("repo", "widgets", "primary repository name")
	bare := flag.String("bare", "", "bare repository backing the primary sim")
	repo2 := flag.String("repo2", "gadgets", "secondary repository name")
	bare2 := flag.String("bare2", "", "bare repository backing the secondary sim")
	def := flag.String("default", "main", "default branch")
	flag.Parse()

	sim := githubsim.New(*owner, *repo, *bare, *def)
	baseHandler := sim.Handler()

	var sim2 *githubsim.Sim
	var baseHandler2 http.Handler
	if *bare2 != "" {
		sim2 = githubsim.New(*owner, *repo2, *bare2, *def)
		baseHandler2 = sim2.Handler()
	}

	gitPath, err := exec.LookPath("git")
	if err != nil {
		log.Fatalf("git not found: %v", err)
	}
	gitBackend := &cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + filepath.Dir(*bare),
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}

	var mu sync.Mutex
	mux := http.NewServeMux()

	mux.HandleFunc("/_sim/check-runs", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Ref    string            `json:"ref"`
			Checks []github.CheckRun `json:"checks"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		if sim.CheckRuns == nil {
			sim.CheckRuns = map[string][]github.CheckRun{}
		}
		sim.CheckRuns[payload.Ref] = payload.Checks
		mu.Unlock()
		fmt.Fprintln(w, `{"ok":true}`)
	})

	mux.HandleFunc("/_sim/pulls", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pulls":    sim.PullRequests,
			"comments": sim.Comments,
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/"+*owner+"/"+*repo+".git/") ||
			(*bare2 != "" && strings.HasPrefix(r.URL.Path, "/"+*owner+"/"+*repo2+".git/")) {
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/"+*owner)
			gitBackend.ServeHTTP(w, r2)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if sim2 != nil && strings.HasPrefix(r.URL.Path, "/repos/"+*owner+"/"+*repo2) {
			baseHandler2.ServeHTTP(w, r)
			return
		}
		baseHandler.ServeHTTP(w, r)
	})

	log.Printf("ghsim: %s/%s (+%s/%s) default=%s on %s", *owner, *repo, *owner, *repo2, sim.DefaultBranch, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
