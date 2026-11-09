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

// simhost <addr> <root> <owner> <name>: the walk's GitHub for a real
// daemon. Git smart-HTTP paths (<owner>/<name>.git/...) go to `git
// http-backend` over <root>, as 2026-11-03's githost did; every other path
// is the REST API, answered by pkg/github/githubsim over the same bare
// repository -- the simulator `grain demo -github-sim` uses.
//
//	go run docs/test-runs/2026-11-04-capabilities/simhost.go 127.0.0.1:4250 /home/debian/walk/repos acme widgets
package main

import (
	"io"
	"log"
	"net/http"
	"net/http/cgi"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

func main() {
	addr, root, owner, name := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	sim := githubsim.New(owner, name, filepath.Join(root, owner, name+".git"), "main")
	git := &cgi.Handler{Path: "/usr/bin/git", Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=walk"}}
	log.Fatal(http.ListenAndServe(addr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ".git/") {
			log.Printf("git %s %s", r.Method, r.URL.Path)
			git.ServeHTTP(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		headers := map[string]string{}
		for k := range r.Header {
			headers[k] = r.Header.Get(k)
		}
		path := strings.TrimPrefix(r.URL.RequestURI(), "/api/v3")
		resp, err := sim.Request(r.Method, path, headers, body)
		if err != nil {
			log.Printf("api %s %s: %v", r.Method, path, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		log.Printf("api %s %s -> %d", r.Method, path, resp.Status)
		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(resp.Status)
		w.Write(resp.Body)
	})))
}
