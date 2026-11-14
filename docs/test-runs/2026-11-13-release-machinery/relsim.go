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

// relsim serves pkg/github/githubsim over HTTP (REST at / and under
// /api/v3, GraphQL at /api/graphql) for a real
// `grain daemon -github-host 127.0.0.1:PORT -github-insecure-http`, for
// walkthrough 2026-11-13 §9 items 1 and 5. It is the 2026-11-11 §7
// trainsim.go with different seeds: acme/widgets carries the release
// manager's own declarations, copied from this checkout
// (.grain/schedules/release-manager{,-patch}.yml and
// .grain/templates/release-manager.yml), a release v1.0.0 and a pending
// v1.1.0 (tag v1.1.0-pending), and it offers loopback control endpoints:
//
//	POST /_commit?msg=feat:+x            a new commit on main (prints its sha)
//	POST /_tag?name=v1.1.0-rc.1[&ref=X]  an annotated tag (default main)
//	POST /_run?ref=R&sha=S&conclusion=C&event=E&jobs=a:success;b:failure&workflow=nightly.yml
//	                                     a concluded workflow run with those jobs
//	POST /_green?sha=S                   tests.yml and release.yml green on S
//	GET  /_calls                         every REST call, in order
//	GET  /_dispatches                    every workflow_dispatch, as JSON
//
// and serves the bare repository over git's smart HTTP (git http-backend).
//
//	go run relsim.go -addr 127.0.0.1:4450 -dir /tmp/s9/sim -repo-root .
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

func git(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=train", "GIT_AUTHOR_EMAIL=train@example.invalid",
		"GIT_COMMITTER_NAME=train", "GIT_COMMITTER_EMAIL=train@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func copyFile(src, dst string) {
	b, err := os.ReadFile(src)
	if err != nil {
		log.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		log.Fatal(err)
	}
}

func main() {
	addr := flag.String("addr", "", "listen address (required)")
	dir := flag.String("dir", "/tmp/s9/sim", "where the bare repo lives")
	root := flag.String("repo-root", ".", "the grain checkout the train's declarations are copied from")
	flag.Parse()
	if *addr == "" {
		log.Fatal("-addr is required")
	}
	bare := filepath.Join(*dir, "acme", "widgets.git")
	seed := filepath.Join(*dir, "seed")
	if _, err := os.Stat(bare); err != nil {
		_ = os.MkdirAll(seed, 0o755)
		git(*dir, "init", "-q", "--bare", "--initial-branch=main", bare)
		git(seed, "init", "-q", "--initial-branch=main")
		for _, f := range []string{".grain/schedules/release-manager.yml", ".grain/schedules/release-manager-patch.yml",
			".grain/templates/release-manager.yml"} {
			copyFile(filepath.Join(*root, f), filepath.Join(seed, f))
		}
		for _, wf := range []string{"nightly", "tests", "release", "release-train-assets", "promote-release"} {
			_ = os.MkdirAll(filepath.Join(seed, ".github", "workflows"), 0o755)
			_ = os.WriteFile(filepath.Join(seed, ".github", "workflows", wf+".yml"),
				[]byte("name: "+wf+"\non: [push]\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps: [{run: echo "+wf+"}]\n"), 0o644)
		}
		_ = os.WriteFile(filepath.Join(seed, "README.md"), []byte("widgets\n"), 0o644)
		git(seed, "add", "-A")
		git(seed, "commit", "-q", "-m", "chore: seed")
		git(seed, "tag", "-a", "v1.0.0", "-m", "Release v1.0.0")
		git(seed, "commit", "-q", "--allow-empty", "-m", "feat: the next release")
		git(seed, "tag", "-a", "v1.1.0-pending", "-m", "v1.1.0 pending")
		git(seed, "push", "-q", bare, "main", "v1.0.0", "v1.1.0-pending")
	}
	sim := githubsim.New("acme", "widgets", bare, "main")
	sim.WorkflowFiles = []string{"nightly.yml", "tests.yml", "release.yml", "release-train-assets.yml", "promote-release.yml"}
	api := sim.Handler()
	var mu sync.Mutex
	nextID := int64(9000)
	mux := http.NewServeMux()
	mux.HandleFunc("/_commit", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		msg := r.URL.Query().Get("msg")
		git(seed, "pull", "-q", bare, "main")
		f, _ := os.OpenFile(filepath.Join(seed, "changes.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		fmt.Fprintln(f, msg)
		f.Close()
		git(seed, "add", "-A")
		git(seed, "commit", "-q", "-m", msg)
		git(seed, "push", "-q", bare, "main")
		fmt.Fprintln(w, git(seed, "rev-parse", "HEAD"))
	})
	mux.HandleFunc("/_tag", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		name, ref := r.URL.Query().Get("name"), r.URL.Query().Get("ref")
		if ref == "" {
			ref = "main"
		}
		git(*dir, "--git-dir", bare, "tag", "-f", "-a", name, "-m", name, ref)
		fmt.Fprintln(w, git(*dir, "--git-dir", bare, "rev-parse", name+"^{commit}"))
	})
	mux.HandleFunc("/_untag", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		git(*dir, "--git-dir", bare, "tag", "-d", r.URL.Query().Get("name"))
		w.WriteHeader(204)
	})
	addRun := func(wf, ref, sha, event, conclusion string, jobs []githubsim.WorkflowJob) int64 {
		nextID++
		now := time.Now().UTC()
		sim.WorkflowRuns = append([]githubsim.WorkflowRun{{
			ID: nextID, Name: strings.TrimSuffix(wf, ".yml"), Path: ".github/workflows/" + wf, HeadBranch: ref, HeadSHA: sha,
			HeadRepo: "acme/widgets", Event: event, Status: "completed", Conclusion: conclusion, RunAttempt: 1,
			HTMLURL:   fmt.Sprintf("https://github.example/acme/widgets/actions/runs/%d", nextID),
			CreatedAt: now.Add(-time.Minute), UpdatedAt: now, Jobs: jobs,
		}}, sim.WorkflowRuns...)
		return nextID
	}
	mux.HandleFunc("/_run", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		q := r.URL.Query()
		wf := q.Get("workflow")
		if wf == "" {
			wf = "nightly.yml"
		}
		event := q.Get("event")
		if event == "" {
			event = "push"
		}
		sha := q.Get("sha")
		if sha == "" {
			sha = git(*dir, "--git-dir", bare, "rev-parse", q.Get("ref")+"^{commit}")
		}
		var jobs []githubsim.WorkflowJob
		for _, spec := range strings.Split(q.Get("jobs"), ";") {
			name, concl, ok := strings.Cut(spec, ":")
			if !ok {
				continue
			}
			job := githubsim.WorkflowJob{Name: name, Conclusion: concl}
			if concl == "failure" {
				job.Log = name + ": FAIL TestSomething (walkthrough)\n"
			}
			jobs = append(jobs, job)
		}
		id := addRun(wf, q.Get("ref"), sha, event, q.Get("conclusion"), jobs)
		fmt.Fprintln(w, id)
	})
	mux.HandleFunc("/_green", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		sha := r.URL.Query().Get("sha")
		for _, wf := range []string{"tests.yml", "release.yml"} {
			addRun(wf, "main", sha, "push", "success", []githubsim.WorkflowJob{{Name: strings.TrimSuffix(wf, ".yml"), Conclusion: "success"}})
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/_calls", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range sim.Calls {
			fmt.Fprintf(w, "%+v\n", c)
		}
	})
	mux.HandleFunc("/_dispatches", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sim.Dispatches)
	})
	// The daemon reads declarations (and clones) over git's smart HTTP.
	gitHTTP := &cgi.Handler{Path: "/usr/bin/git", Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + *dir, "GIT_HTTP_EXPORT_ALL=1"}}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ".git/") {
			gitHTTP.ServeHTTP(w, r)
			return
		}
		switch {
		case r.URL.Path == "/api/graphql":
			r.URL.Path = "/graphql"
		case strings.HasPrefix(r.URL.Path, "/api/v3/"):
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/v3")
		}
		r.RequestURI = r.URL.RequestURI()
		mu.Lock()
		defer mu.Unlock()
		api.ServeHTTP(w, r)
	})
	log.Printf("trainsim on http://%s (bare %s)", *addr, bare)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
