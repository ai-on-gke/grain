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

// ghsim serves pkg/github/githubsim over plain HTTP for acme/widgets and a
// fork alice/widgets (with an open fork PR alice:fix-branch -> main) alongside
// git-http-backend and walkthrough control routes under /_sim/* for merge
// refusal injection so a real grain daemon produces genuine merge_failed tasks.
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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

var gitHTTPPath = regexp.MustCompile(`^/[^/]+/[^/]+\.git/(info/refs|git-upload-pack|git-receive-pack)$`)

func main() {
	addr := flag.String("addr", "127.0.0.1:4294", "listen address")
	gitRoot := flag.String("git-root", "", "directory containing acme/widgets.git and alice/widgets.git")
	def := flag.String("default", "main", "default branch")
	flag.Parse()

	widgetsBare := filepath.Join(*gitRoot, "acme", "widgets.git")
	forkBare := filepath.Join(*gitRoot, "alice", "widgets.git")

	sim := githubsim.New("acme", "widgets", widgetsBare, *def)
	forkSim := githubsim.New("alice", "widgets", forkBare, *def)
	sim.Forks = map[string]*githubsim.Sim{
		"alice":         forkSim,
		"alice/widgets": forkSim,
	}
	sim.Reachable = map[string][]githubsim.ReachableRepo{
		"ghp_walkthrough": {{FullName: "acme/widgets", Push: true}},
	}
	// Seed an open fork pull request headed by alice:fix-branch -> main so
	// New task fork-head resolution works end to end against real ghsim.
	mergeableTrue := true
	sim.PullRequests = append(sim.PullRequests, githubsim.PullRequest{
		Number:              99,
		Title:               "Alice's fork fix",
		Body:                "Seeded fork PR for §5 walkthrough.",
		Head:                "fix-branch",
		HeadRepo:            "alice/widgets",
		MaintainerCanModify: true,
		Base:                *def,
		State:               "open",
		Mergeable:           &mergeableTrue,
		Author:              "alice",
		HTMLURL:             fmt.Sprintf("http://%s/acme/widgets/pull/99", *addr),
		CreatedAt:           time.Now().UTC(),
	})

	baseHandler := sim.Handler()

	gitPath, err := exec.LookPath("git")
	if err != nil {
		log.Fatalf("git not found: %v", err)
	}
	gitBackend := &cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + *gitRoot,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}

	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/_sim/refuse-merges", func(w http.ResponseWriter, r *http.Request) {
		val := r.URL.Query().Get("value")
		mu.Lock()
		sim.RefuseMerges = val
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"refuseMerges": val})
	})
	mux.HandleFunc("/_sim/prs", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		prs := append([]githubsim.PullRequest(nil), sim.PullRequests...)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(prs)
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
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if gitHTTPPath.MatchString(r.URL.Path) {
			r2 := r.Clone(r.Context())
			r2.Header.Del("Authorization")
			gitBackend.ServeHTTP(w, r2)
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
		// Mark newly created PRs mergeable=true (simulating GitHub completing
		// its async mergeability computation) so the live merge queue proceeds
		// straight to attempting the merge.
		for i := range sim.PullRequests {
			if sim.PullRequests[i].Mergeable == nil {
				t := true
				sim.PullRequests[i].Mergeable = &t
			}
		}
	})

	log.Printf("ghsim: acme/widgets + fork alice/widgets (default %s) on %s", sim.DefaultBranch, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
