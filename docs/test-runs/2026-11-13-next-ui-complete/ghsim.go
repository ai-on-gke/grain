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

// ghsim serves pkg/github/githubsim over plain HTTP for five connected
// repositories under acme/* (widgets, api, docs, infra, mobile), backed by
// bare git repositories with git http-backend so branches can be pushed and
// compared while /user/repos and /installation/repositories advertise all five.
package main

import (
	"flag"
	"log"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4294", "listen address")
	owner := flag.String("owner", "acme", "repository owner")
	reposFlag := flag.String("repos", "widgets,api,docs,infra,mobile", "comma-separated repo names")
	baresDir := flag.String("bares-dir", "", "directory containing <repo>.git bare repos")
	token := flag.String("token", "ghp_walkthrough", "credential token advertised in /user/repos")
	def := flag.String("default", "main", "default branch")
	flag.Parse()

	if *baresDir == "" {
		log.Fatal("-bares-dir is required")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		log.Fatalf("git not found: %v", err)
	}

	names := strings.Split(*reposFlag, ",")
	var reachable []githubsim.ReachableRepo
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		reachable = append(reachable, githubsim.ReachableRepo{
			FullName: *owner + "/" + name,
			Push:     true,
		})
	}

	sims := map[string]http.Handler{}
	var primary http.Handler
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		bare := *baresDir + "/" + name + ".git"
		sim := githubsim.New(*owner, name, bare, *def)
		sim.Reachable = map[string][]githubsim.ReachableRepo{
			*token: reachable,
		}
		h := sim.Handler()
		sims[strings.ToLower(*owner+"/"+name)] = h
		if primary == nil {
			primary = h
		}
	}

	gitBackend := &cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + *baresDir,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}

	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/"+*owner+"/") && strings.Contains(p, ".git/") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(p, "/"+*owner)
			gitBackend.ServeHTTP(w, r2)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if rest, ok := strings.CutPrefix(p, "/repos/"); ok {
			parts := strings.SplitN(rest, "/", 3)
			if len(parts) >= 2 {
				key := strings.ToLower(parts[0] + "/" + parts[1])
				if h, found := sims[key]; found {
					h.ServeHTTP(w, r)
					return
				}
				http.NotFound(w, r)
				return
			}
		}
		primary.ServeHTTP(w, r)
	})

	log.Printf("ghsim: %d repos under %s/* on %s", len(sims), *owner, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
