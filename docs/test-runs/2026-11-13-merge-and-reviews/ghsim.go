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
// small walkthrough control routes under /_sim/* to inject git clone failures,
// update commit check-runs, and seed GitHub Actions workflow runs.
//
// For docs/test-plans/2026-11-13.md §6 it also stands in for GitHub's merge
// attribution: -merge-logins maps each token (the Authorization header) to
// the login GitHub would record as merged_by, and GET /_sim/merges lists
// every merge request with the token that made it.
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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	mergeLogins := flag.String("merge-logins", "", "comma-separated token=login pairs: the merged_by a merge made with each token is recorded as")
	flag.Parse()
	loginFor := map[string]string{}
	for _, kv := range strings.Split(*mergeLogins, ",") {
		if tok, login, ok := strings.Cut(strings.TrimSpace(kv), "="); ok {
			loginFor[tok] = login
		}
	}
	type mergeRecord struct {
		Path  string    `json:"path"`
		Token string    `json:"token"`
		Login string    `json:"login"`
		At    time.Time `json:"at"`
	}
	var merges []mergeRecord

	sim := githubsim.New(*owner, *repo, *bare, *def)
	sim.RecordMergeCommits = true
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
	var failGitRemaining atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/_sim/fail-git", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.ParseInt(r.URL.Query().Get("n"), 10, 64)
		failGitRemaining.Store(n)
		fmt.Fprintf(w, `{"failGit":%d}`+"\n", n)
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
				Jobs       []struct {
					Name        string `json:"name"`
					Conclusion  string `json:"conclusion"`
					FailingStep string `json:"failingStep"`
					Log         string `json:"log"`
				} `json:"jobs"`
			} `json:"runs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		var seeded []githubsim.WorkflowRun
		for _, in := range payload.Runs {
			wr := githubsim.WorkflowRun{
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
			}
			for _, j := range in.Jobs {
				wr.Jobs = append(wr.Jobs, githubsim.WorkflowJob{
					Name:        j.Name,
					Conclusion:  j.Conclusion,
					FailingStep: j.FailingStep,
					Log:         j.Log,
				})
			}
			seeded = append(seeded, wr)
		}
		sim.WorkflowRuns = seeded
		mu.Unlock()
		fmt.Fprintf(w, `{"seeded":%d}`+"\n", len(seeded))
	})
	mux.HandleFunc("/_sim/merges", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(merges)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/"+*owner+"/"+*repo+".git/") {
			for {
				cur := failGitRemaining.Load()
				if cur <= 0 {
					break
				}
				if failGitRemaining.CompareAndSwap(cur, cur-1) {
					http.Error(w, "simulated git upstream failure", http.StatusInternalServerError)
					return
				}
			}
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/"+*owner)
			gitBackend.ServeHTTP(w, r2)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		// GitHub works mergeability out by itself; the sim leaves it
		// unset, which the merge queue reads as "not yet known".
		yes := true
		for i := range sim.PullRequests {
			if sim.PullRequests[i].Mergeable == nil {
				sim.PullRequests[i].Mergeable = &yes
			}
		}
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/merge") && strings.Contains(r.URL.Path, "/pulls/") {
			auth := r.Header.Get("Authorization")
			tok := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(auth, "token "), "Bearer "))
			login, ok := loginFor[tok]
			if !ok {
				login = "unknown-token"
			}
			sim.MergeAs = login
			merges = append(merges, mergeRecord{Path: r.URL.Path, Token: tok, Login: login, At: time.Now().UTC()})
			log.Printf("ghsim: merge %s with token %q recorded as %s", r.URL.Path, tok, login)
		}
		baseHandler.ServeHTTP(w, r)
	})

	log.Printf("ghsim: %s/%s (default %s) on %s", *owner, *repo, sim.DefaultBranch, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
