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

// ghsim serves pkg/github/githubsim for acme/widgets plus git-http-backend
// for every acme/<name>.git bare repository under -root, so a task's
// read repositories and extra write targets (acme/docs, acme/gadgets)
// clone and push through grain's git proxy like the target does.
package main

import (
	"flag"
	"log"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4284", "listen address")
	root := flag.String("root", "", "directory holding <name>.git bare repositories for owner acme")
	flag.Parse()

	sim := githubsim.New("acme", "widgets", filepath.Join(*root, "widgets.git"), "main")
	reach := []githubsim.ReachableRepo{}
	for _, n := range []string{"widgets", "docs", "gadgets"} {
		reach = append(reach, githubsim.ReachableRepo{FullName: "acme/" + n, Push: true})
	}
	sim.Reachable = map[string][]githubsim.ReachableRepo{"ghp_walkthrough": reach}
	base := sim.Handler()

	gitPath, err := exec.LookPath("git")
	if err != nil {
		log.Fatalf("git not found: %v", err)
	}
	backend := &cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + *root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	var mu sync.Mutex
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if rest, ok := strings.CutPrefix(r.URL.Path, "/acme/"); ok {
			if name, _, ok := strings.Cut(rest, ".git/"); ok {
				if _, err := os.Stat(filepath.Join(*root, name+".git")); err == nil {
					log.Printf("git %s %s", r.Method, r.URL.String())
					r2 := r.Clone(r.Context())
					r2.URL.Path = "/" + rest
					backend.ServeHTTP(w, r2)
					return
				}
			}
		}
		mu.Lock()
		defer mu.Unlock()
		base.ServeHTTP(w, r)
	})
	log.Printf("ghsim: acme/{widgets,docs,gadgets} under %s on %s", *root, *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
