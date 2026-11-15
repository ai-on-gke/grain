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

// Command simgh serves pkg/github/githubsim's REST API over plain HTTP for a
// real `grain daemon -github-host <addr> -github-insecure-http`, so the
// daemon's GitHub client reads a simulated acme/widgets (default branch main)
// and the jobs of the workflow runs a walkthrough adds:
//
//	go build -o simgh ./docs/test-runs/2026-11-14-repair-grants-and-config-export/simgh/main.go && ./simgh -addr 127.0.0.1:4230 -dir /tmp/sim
//	curl -d '{"ID":9001,"Name":"Tests","Path":".github/workflows/tests.yml","HeadBranch":"main","Event":"push",
//	  "Jobs":[{"Name":"go","Conclusion":"failure","Log":"--- FAIL: TestX"}]}' http://127.0.0.1:4230/_sim/runs
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4230", "listen address")
	dir := flag.String("dir", "", "scratch directory for the bare repository")
	owner := flag.String("owner", "acme", "repository owner")
	name := flag.String("repo", "widgets", "repository name")
	flag.Parse()
	bare := filepath.Join(*dir, *owner, *name+".git")
	sim, err := githubsim.CreateEmptyRepository(*owner, *name, bare)
	if err != nil {
		log.Fatal(err)
	}
	seed := filepath.Join(*dir, "seed")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", seed},
		{"-C", seed, "-c", "user.name=sim", "-c", "user.email=sim@example.com", "commit", "-q", "--allow-empty", "-m", "initial"},
		{"-C", seed, "push", "-q", bare, "main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			log.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	api := sim.Handler()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /_sim/runs", func(w http.ResponseWriter, r *http.Request) {
		var run githubsim.WorkflowRun
		if err := json.NewDecoder(r.Body).Decode(&run); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(sim.AddWorkflowRun(run))
	})
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.RequestURI())
		api.ServeHTTP(w, r)
	}))
	log.Printf("simulated GitHub for %s/%s on http://%s (bare repo %s)", *owner, *name, *addr, bare)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
