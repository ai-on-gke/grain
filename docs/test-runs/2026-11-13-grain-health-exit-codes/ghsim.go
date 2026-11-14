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

// ghsim serves pkg/github/githubsim over plain HTTP for acme/widgets (4284)
// alongside git-http-backend and walkthrough control routes under /_sim/*
// for reviewed-merge login warnings and GitHub Actions run seeding.
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
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4284", "listen address")
	owner := flag.String("owner", "acme", "repository owner")
	repo := flag.String("repo", "widgets", "repository name")
	bare := flag.String("bare", "", "bare repository backing the sim")
	def := flag.String("default", "main", "default branch")
	flag.Parse()

	sim := githubsim.New(*owner, *repo, *bare, *def)
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
		Env: []string{
			"GIT_PROJECT_ROOT=" + filepath.Dir(*bare),
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}

	var mu sync.Mutex
	var mergeLoginsVar string

	mux := http.NewServeMux()
	mux.HandleFunc("/_sim/merge-logins", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		mergeLoginsVar = strings.TrimSpace(r.URL.Query().Get("value"))
		cur := mergeLoginsVar
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"mergeLogins": cur})
	})
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
	mux.HandleFunc("/_sim/seed-actions", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Runs []struct {
				ID         int64     `json:"id"`
				Name       string    `json:"name"`
				Path       string    `json:"path"`
				HeadBranch string    `json:"headBranch"`
				HeadSHA    string    `json:"headSha"`
				Event      string    `json:"event"`
				Status     string    `json:"status"`
				Conclusion string    `json:"conclusion"`
				CreatedAt  time.Time `json:"createdAt"`
				UpdatedAt  time.Time `json:"updatedAt"`
			} `json:"runs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		var seeded []githubsim.WorkflowRun
		for _, in := range payload.Runs {
			seeded = append(seeded, githubsim.WorkflowRun{
				ID:         in.ID,
				Name:       in.Name,
				Path:       in.Path,
				HeadBranch: in.HeadBranch,
				HeadSHA:    in.HeadSHA,
				Event:      in.Event,
				Status:     in.Status,
				Conclusion: in.Conclusion,
				RunAttempt: 1,
				CreatedAt:  in.CreatedAt,
				UpdatedAt:  in.UpdatedAt,
			})
		}
		sim.WorkflowRuns = seeded
		mu.Unlock()
		fmt.Fprintf(w, `{"seeded":%d}`+"\n", len(seeded))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/"+*owner+"/"+*repo+".git/") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/"+*owner)
			gitBackend.ServeHTTP(w, r2)
			return
		}
		varPrefix := fmt.Sprintf("/repos/%s/%s/actions/variables/GRAIN_MERGE_LOGINS", *owner, *repo)
		if r.Method == http.MethodGet && r.URL.Path == varPrefix {
			mu.Lock()
			v := mergeLoginsVar
			mu.Unlock()
			if v == "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"name":  "GRAIN_MERGE_LOGINS",
				"value": v,
			})
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/users/") {
			login := strings.TrimPrefix(r.URL.Path, "/users/")
			accountType := "User"
			if strings.HasSuffix(login, "[bot]") {
				accountType = "Bot"
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"login": login,
				"type":  accountType,
			})
			return
		}
		mu.Lock()
		defer mu.Unlock()
		baseHandler.ServeHTTP(w, r)
	})

	log.Printf("ghsim: %s/%s (default %s) on %s", *owner, *repo, sim.DefaultBranch, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
