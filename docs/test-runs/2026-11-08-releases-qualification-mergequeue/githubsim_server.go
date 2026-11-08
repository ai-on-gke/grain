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

// githubsim_server serves pkg/github/githubsim over HTTP as a GitHub
// Enterprise host (REST under /api/v3, GraphQL at /api/graphql) so a real
// `grain daemon -github-host 127.0.0.1:PORT -github-insecure-http` can be
// pointed at it, for walkthrough 2026-11-08 §5 item 3. It seeds acme/widgets
// (main, .github/workflows/promote-release.yml, no .grain/workflows) and
// offers loopback control endpoints:
//
//	POST /_tag?name=v1.4.0-pending      annotated tag on main in the bare repo
//	GET  /_dispatches                   every workflow dispatch the sim took
//	GET  /_calls                        every REST call, in order
//
//	go run githubsim_server.go -addr 127.0.0.1:4390 -dir /tmp/s5-item3/sim
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

func git(dir string, args ...string) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=train", "GIT_AUTHOR_EMAIL=train@example.invalid",
		"GIT_COMMITTER_NAME=train", "GIT_COMMITTER_EMAIL=train@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func main() {
	addr := flag.String("addr", "", "listen address (required; the run scripts pass one)")
	dir := flag.String("dir", "/tmp/s5-item3/sim", "where the bare repo lives")
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
		_ = os.MkdirAll(filepath.Join(seed, ".github", "workflows"), 0o755)
		_ = os.WriteFile(filepath.Join(seed, ".github", "workflows", "promote-release.yml"),
			[]byte("name: promote-release\non:\n  workflow_dispatch:\n    inputs:\n      version: {required: true}\njobs:\n  promote:\n    runs-on: ubuntu-latest\n    steps: [{run: echo promote}]\n"), 0o644)
		_ = os.WriteFile(filepath.Join(seed, "README.md"), []byte("widgets\n"), 0o644)
		git(seed, "add", "-A")
		git(seed, "commit", "-q", "-m", "seed")
		git(seed, "push", "-q", bare, "main")
	}
	sim := githubsim.New("acme", "widgets", bare, "main")
	sim.WorkflowFiles = []string{"promote-release.yml"}
	api := sim.Handler()
	mux := http.NewServeMux()
	mux.HandleFunc("/_tag", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		git(*dir, "--git-dir", bare, "tag", "-f", "-a", name, "-m", name+" staged by the train", "main")
		w.WriteHeader(204)
	})
	mux.HandleFunc("/_dispatches", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(sim.Dispatches)
	})
	mux.HandleFunc("/_calls", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(sim.Calls)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/graphql":
			r.URL.Path = "/graphql"
		case strings.HasPrefix(r.URL.Path, "/api/v3/"):
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/v3")
		}
		r.RequestURI = r.URL.RequestURI()
		api.ServeHTTP(w, r)
	})
	log.Printf("githubsim on http://%s (bare %s)", *addr, bare)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
