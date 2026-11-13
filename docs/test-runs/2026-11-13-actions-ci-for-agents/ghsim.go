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
// update commit check-runs, seed GitHub Actions workflow runs, read back the
// re-runs grain asked for, and read a log of every API request with its
// status (so a poll answered 304 can be counted).
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
	var reqMu sync.Mutex
	var reqLog []loggedRequest
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
				HeadRepo   string    `json:"headRepo"`
				RunAttempt int       `json:"runAttempt"`
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
				HeadRepo:   in.HeadRepo,
				RunAttempt: max(in.RunAttempt, 1),
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
	mux.HandleFunc("/_sim/reruns", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rerunFailedJobs": sim.RerunFailedJobs,
			"rerunRuns":       sim.RerunRuns,
			"rerunJobs":       sim.RerunJobs,
		})
	})
	mux.HandleFunc("/_sim/requests", func(w http.ResponseWriter, r *http.Request) {
		reqMu.Lock()
		defer reqMu.Unlock()
		if r.Method == http.MethodDelete {
			reqLog = nil
		}
		_ = json.NewEncoder(w).Encode(reqLog)
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
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		baseHandler.ServeHTTP(rec, r)
		reqMu.Lock()
		reqLog = append(reqLog, loggedRequest{At: time.Now().UTC(), Method: r.Method, Path: r.URL.RequestURI(), Status: rec.status,
			Conditional: r.Header.Get("If-None-Match") != ""})
		reqMu.Unlock()
	})

	log.Printf("ghsim: %s/%s (default %s) on %s", *owner, *repo, sim.DefaultBranch, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

type loggedRequest struct {
	At          time.Time `json:"at"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	Status      int       `json:"status"`
	Conditional bool      `json:"conditional,omitempty"`
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
