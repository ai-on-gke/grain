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

// ghsim serves one or more repositories via pkg/github/githubsim over plain
// HTTP alongside git-http-backend and /_sim/seed-actions (with per-attempt
// WorkflowJob logs and conclusions) for the §10 walkthrough.
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

type repoSpec struct {
	owner string
	name  string
	bare  string
	sim   *githubsim.Sim
	h     http.Handler
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:4294", "listen address")
	def := flag.String("default", "main", "default branch")
	var specs multiFlag
	flag.Var(&specs, "repo", "owner/name=/path/to/bare.git (repeatable)")
	flag.Parse()

	if len(specs) == 0 {
		log.Fatal("at least one -repo owner/name=/path/to/bare.git is required")
	}

	gitPath, err := exec.LookPath("git")
	if err != nil {
		log.Fatalf("git not found: %v", err)
	}

	var reachable []githubsim.ReachableRepo
	repos := map[string]*repoSpec{}
	var first *repoSpec
	for _, raw := range specs {
		full, bare, ok := strings.Cut(raw, "=")
		if !ok {
			log.Fatalf("bad -repo %q (want owner/name=/path/to/bare.git)", raw)
		}
		owner, name, ok := strings.Cut(full, "/")
		if !ok {
			log.Fatalf("bad repo %q (want owner/name)", full)
		}
		s := githubsim.New(owner, name, bare, *def)
		rs := &repoSpec{owner: owner, name: name, bare: bare, sim: s, h: s.Handler()}
		key := strings.ToLower(owner + "/" + name)
		repos[key] = rs
		if first == nil {
			first = rs
		}
		reachable = append(reachable, githubsim.ReachableRepo{FullName: owner + "/" + name, Push: true})
	}
	for _, rs := range repos {
		rs.sim.Reachable = map[string][]githubsim.ReachableRepo{
			"ghp_walkthrough": reachable,
		}
	}

	var mu sync.Mutex
	mux := http.NewServeMux()

	mux.HandleFunc("/_sim/seed-actions", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Repo string `json:"repo"`
			Runs []struct {
				ID         int64     `json:"id"`
				Name       string    `json:"name"`
				Path       string    `json:"path"`
				HeadBranch string    `json:"headBranch"`
				HeadSHA    string    `json:"headSha"`
				Event      string    `json:"event"`
				Status     string    `json:"status"`
				Conclusion string    `json:"conclusion"`
				RunAttempt int       `json:"runAttempt"`
				CreatedAt  time.Time `json:"createdAt"`
				UpdatedAt  time.Time `json:"updatedAt"`
				Jobs       []struct {
					Name          string `json:"name"`
					Status        string `json:"status"`
					Conclusion    string `json:"conclusion"`
					RunAttempt    int    `json:"runAttempt"`
					Log           string `json:"log"`
					FailingStep   string `json:"failingStep"`
					FailureReason string `json:"failureReason"`
					NeverStarted  bool   `json:"neverStarted"`
				} `json:"jobs"`
			} `json:"runs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		target := first
		if strings.TrimSpace(payload.Repo) != "" {
			var ok bool
			target, ok = repos[strings.ToLower(strings.TrimSpace(payload.Repo))]
			if !ok {
				http.Error(w, "unknown repo "+payload.Repo, http.StatusNotFound)
				return
			}
		}
		mu.Lock()
		var seeded []githubsim.WorkflowRun
		for _, in := range payload.Runs {
			att := in.RunAttempt
			if att == 0 {
				att = 1
			}
			var jobs []githubsim.WorkflowJob
			for _, j := range in.Jobs {
				jobs = append(jobs, githubsim.WorkflowJob{
					Name:          j.Name,
					Status:        j.Status,
					Conclusion:    j.Conclusion,
					RunAttempt:    j.RunAttempt,
					Log:           j.Log,
					FailingStep:   j.FailingStep,
					FailureReason: j.FailureReason,
					NeverStarted:  j.NeverStarted,
				})
			}
			seeded = append(seeded, githubsim.WorkflowRun{
				ID:         in.ID,
				Name:       in.Name,
				Path:       in.Path,
				HeadBranch: in.HeadBranch,
				HeadSHA:    in.HeadSHA,
				Event:      in.Event,
				Status:     in.Status,
				Conclusion: in.Conclusion,
				RunAttempt: att,
				CreatedAt:  in.CreatedAt,
				UpdatedAt:  in.UpdatedAt,
				Jobs:       jobs,
			})
		}
		target.sim.WorkflowRuns = seeded
		mu.Unlock()
		fmt.Fprintf(w, `{"repo":%q,"seeded":%d}`+"\n", target.owner+"/"+target.name, len(seeded))
	})

	mux.HandleFunc("/_sim/check-runs", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Repo   string            `json:"repo"`
			Ref    string            `json:"ref"`
			Checks []github.CheckRun `json:"checks"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		target := first
		if strings.TrimSpace(payload.Repo) != "" {
			var ok bool
			target, ok = repos[strings.ToLower(strings.TrimSpace(payload.Repo))]
			if !ok {
				http.Error(w, "unknown repo "+payload.Repo, http.StatusNotFound)
				return
			}
		}
		mu.Lock()
		if target.sim.CheckRuns == nil {
			target.sim.CheckRuns = map[string][]github.CheckRun{}
		}
		target.sim.CheckRuns[payload.Ref] = payload.Checks
		mu.Unlock()
		fmt.Fprintln(w, `{"ok":true}`)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Git HTTP backend for /<owner>/<name>.git/...
		for _, rs := range repos {
			prefix := "/" + rs.owner + "/" + rs.name + ".git/"
			if strings.HasPrefix(r.URL.Path, prefix) {
				gitBackend := &cgi.Handler{
					Path: gitPath,
					Args: []string{"http-backend"},
					Env: []string{
						"GIT_PROJECT_ROOT=" + filepath.Dir(rs.bare),
						"GIT_HTTP_EXPORT_ALL=1",
					},
				}
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/" + filepath.Base(rs.bare) + strings.TrimPrefix(r.URL.Path, "/"+rs.owner+"/"+rs.name+".git")
				gitBackend.ServeHTTP(w, r2)
				return
			}
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/users/") {
			login := strings.TrimPrefix(r.URL.Path, "/users/")
			accountType := "User"
			if strings.HasSuffix(login, "[bot]") {
				accountType = "Bot"
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"login": login, "type": accountType})
			return
		}
		// Route /repos/{owner}/{name}/... to the matching sim.
		target := first
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
			if len(parts) >= 2 {
				if rs, ok := repos[strings.ToLower(parts[0]+"/"+parts[1])]; ok {
					target = rs
				} else {
					http.NotFound(w, r)
					return
				}
			}
		}
		if strings.Contains(r.URL.Path, "/actions/runs") || strings.HasSuffix(r.URL.Path, "/tags") {
			log.Printf("ghsim req: %s %s", r.Method, r.URL.RequestURI())
		}
		mu.Lock()
		defer mu.Unlock()
		target.h.ServeHTTP(w, r)
	})

	var names []string
	for _, rs := range repos {
		names = append(names, rs.owner+"/"+rs.name)
	}
	log.Printf("ghsim: serving %v (default %s) on %s", names, *def, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
