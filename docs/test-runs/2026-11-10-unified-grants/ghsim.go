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

// ghsim serves git smart HTTP (git http-backend) and pkg/github/githubsim's
// REST API over plain HTTP for one or more repositories under -root:
//
//	go build -o /var/tmp/ghsim docs/test-runs/2026-11-10-unified-grants/ghsim.go
//	/var/tmp/ghsim -addr 127.0.0.1:4280 -root /var/tmp/repos -repo acme/widgets=main -repo acme/lib=main
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/cgi"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

type repoList []string

func (r *repoList) String() string     { return strings.Join(*r, ",") }
func (r *repoList) Set(v string) error { *r = append(*r, v); return nil }

func main() {
	addr := flag.String("addr", "127.0.0.1:4280", "listen address")
	root := flag.String("root", "", "directory containing <owner>/<name>.git bare repos")
	var repos repoList
	flag.Var(&repos, "repo", "owner/name[=defaultBranch] (repeatable)")
	flag.Parse()

	sims := map[string]http.Handler{}
	var fallback http.Handler
	for _, spec := range repos {
		name, def, ok := strings.Cut(spec, "=")
		if !ok || def == "" {
			def = "main"
		}
		owner, repo, ok := strings.Cut(name, "/")
		if !ok {
			log.Fatalf("invalid -repo %q: want owner/name[=branch]", spec)
		}
		bare := filepath.Join(*root, owner, repo+".git")
		sim := githubsim.New(owner, repo, bare, def)
		h := sim.Handler()
		sims[strings.ToLower(owner+"/"+repo)] = h
		if fallback == nil {
			fallback = h
		}
		log.Printf("ghsim: %s/%s (default %s) -> %s", owner, repo, def, bare)
	}
	if fallback == nil {
		log.Fatal("ghsim: at least one -repo is required")
	}

	gitCGI := &cgi.Handler{
		Path: "/usr/bin/git",
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + *root,
			"GIT_HTTP_EXPORT_ALL=1",
			"REMOTE_USER=walk",
		},
	}

	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ".git/") || strings.HasSuffix(r.URL.Path, ".git") {
			gitCGI.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v3/") || r.URL.Path == "/api/v3" {
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/v3")
			if r2.URL.Path == "" {
				r2.URL.Path = "/"
			}
			r2.RequestURI = r2.URL.RequestURI()
			r = r2
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) >= 3 && parts[0] == "repos" {
			key := strings.ToLower(parts[1] + "/" + parts[2])
			if h, ok := sims[key]; ok {
				h.ServeHTTP(w, r)
				return
			}
			http.Error(w, fmt.Sprintf(`{"message":"Not Found: %s"}`, key), http.StatusNotFound)
			return
		}
		fallback.ServeHTTP(w, r)
	})
	log.Printf("ghsim listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
