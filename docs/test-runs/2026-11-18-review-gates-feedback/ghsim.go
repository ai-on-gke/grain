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
// update commit check-runs, seed GitHub Actions workflow runs, refuse the next
// draft reviews with GitHub's one-pending-review 422, stand in for a person
// submitting a draft (seeding its inline comments as review threads), resolve
// a thread, open a pull request from a fork, and dump what CreateReview
// recorded. Adapted from
// docs/test-runs/2026-11-13-proposal-admission/ghsim.go.
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
	flag.Parse()

	sim := githubsim.New(*owner, *repo, *bare, *def)
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
	var failReviewRemaining atomic.Int64
	var refusedReviews atomic.Int64

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
	mux.HandleFunc("/_sim/fail-review", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.ParseInt(r.URL.Query().Get("n"), 10, 64)
		failReviewRemaining.Store(n)
		fmt.Fprintf(w, `{"failReview":%d}`+"\n", n)
	})
	// refuse-reviews sets githubsim's own RefuseReviews (PR #1808): the
	// next n create-review calls answer GitHub's 422 "one pending review".
	mux.HandleFunc("/_sim/refuse-reviews", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		mu.Lock()
		sim.RefuseReviews = n
		mu.Unlock()
		fmt.Fprintf(w, `{"refuseReviews":%d}`+"\n", n)
	})
	mux.HandleFunc("/_sim/reviews", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reviews": sim.Reviews, "reviewComments": sim.ReviewComments,
			"resolvedThreads": sim.ResolvedThreads, "refused": refusedReviews.Load(),
		})
	})
	mux.HandleFunc("/_sim/open-fork-pr", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Title    string `json:"title"`
			Body     string `json:"body"`
			Head     string `json:"head"`
			HeadRepo string `json:"headRepo"`
			Author   string `json:"author"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		base := sim.PullRequestBase
		if base == 0 {
			base = 9000
		}
		number := base + len(sim.PullRequests)
		sim.PullRequests = append(sim.PullRequests, githubsim.PullRequest{
			Number: number, Title: payload.Title, Body: payload.Body, Head: payload.Head,
			HeadRepo: payload.HeadRepo, Base: sim.DefaultBranch, State: "open", Author: payload.Author,
			HTMLURL: fmt.Sprintf("https://github.com/%s/%s/pull/%d", *owner, *repo, number), CreatedAt: time.Now(),
		})
		fmt.Fprintf(w, `{"number":%d}`+"\n", number)
	})
	mux.HandleFunc("/_sim/resolve", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if sim.ResolvedThreads == nil {
			sim.ResolvedThreads = map[string]bool{}
		}
		sim.ResolvedThreads[r.URL.Query().Get("thread")] = true
		fmt.Fprintln(w, `{"ok":true}`)
	})
	mux.HandleFunc("/_sim/submit-review-comment", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Number    int    `json:"number"`
			User      string `json:"user"`
			Path      string `json:"path"`
			Line      int    `json:"line"`
			Body      string `json:"body"`
			InReplyTo int    `json:"inReplyTo"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if sim.ReviewComments == nil {
			sim.ReviewComments = map[int][]github.ReviewComment{}
		}
		id := 50000
		for _, cs := range sim.ReviewComments {
			for _, c := range cs {
				if c.ID >= id {
					id = c.ID + 1
				}
			}
		}
		line := payload.Line
		sim.ReviewComments[payload.Number] = append(sim.ReviewComments[payload.Number], github.ReviewComment{
			ID: id, User: payload.User, Path: payload.Path, Line: &line, Body: payload.Body, InReplyTo: payload.InReplyTo,
		})
		fmt.Fprintf(w, `{"id":%d}`+"\n", id)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reviews") && strings.Contains(r.URL.Path, "/pulls/") {
			for {
				cur := failReviewRemaining.Load()
				if cur <= 0 {
					break
				}
				if failReviewRemaining.CompareAndSwap(cur, cur-1) {
					refusedReviews.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnprocessableEntity)
					fmt.Fprintln(w, `{"message":"Unprocessable Entity","errors":["User can only have one pending review per pull request"]}`)
					return
				}
			}
		}
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
		baseHandler.ServeHTTP(w, r)
	})

	log.Printf("ghsim: %s/%s (default %s) on %s", *owner, *repo, sim.DefaultBranch, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
