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

// ghsim serves pkg/github/githubsim's REST API plus git smart-HTTP
// (git-http-backend, receive-pack enabled) over plain HTTP for
// acme/widgets, so a real `grain daemon` clones and pushes through its
// git proxy. It also answers the GitHub App installation-token exchange
// (POST /app/installations/{id}/access_tokens), which githubsim does not,
// and counts the mints at GET /_sim/mints so a walkthrough can see App
// tokens being minted from a migrated key. GET /_sim/git-auth counts the
// Authorization scheme of every git request (never the value).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/cgi"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

var (
	gitHTTPPath  = regexp.MustCompile(`^/[^/]+/[^/]+(\.git)?/(info/refs|git-upload-pack|git-receive-pack)$`)
	appTokenPath = regexp.MustCompile(`^(/api/v3)?/app/installations/([0-9]+)/access_tokens$`)
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4274", "listen address")
	gitRoot := flag.String("git-root", "", "directory containing acme/widgets.git")
	def := flag.String("default", "main", "default branch name")
	flag.Parse()

	sim := githubsim.New("acme", "widgets", *gitRoot+"/acme/widgets.git", *def)
	simH := sim.Handler()
	gitCGI := &cgi.Handler{
		Path: "/usr/lib/git-core/git-http-backend",
		Env:  []string{"GIT_PROJECT_ROOT=" + *gitRoot, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=grain"},
	}

	var mu sync.Mutex
	mints := 0
	gitAuth := map[string]int{}

	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_sim/mints":
			mu.Lock()
			fmt.Fprintln(w, mints)
			mu.Unlock()
			return
		case r.URL.Path == "/_sim/git-auth":
			mu.Lock()
			_ = json.NewEncoder(w).Encode(gitAuth)
			mu.Unlock()
			return
		case r.Method == http.MethodPost && appTokenPath.MatchString(r.URL.Path):
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				http.Error(w, `{"message":"A JSON web token could not be decoded"}`, http.StatusUnauthorized)
				return
			}
			mu.Lock()
			mints++
			n := mints
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"token":      fmt.Sprintf("ghs_simulated_installation_token_%d", n),
				"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
			return
		case gitHTTPPath.MatchString(r.URL.Path):
			scheme, _, _ := strings.Cut(r.Header.Get("Authorization"), " ")
			if scheme == "" {
				scheme = "none"
			}
			mu.Lock()
			gitAuth[scheme]++
			mu.Unlock()
			r2 := r.Clone(r.Context())
			r2.Header.Del("Authorization")
			gitCGI.ServeHTTP(w, r2)
			return
		}
		simH.ServeHTTP(w, r)
	})
	log.Printf("ghsim: acme/widgets (default %s, git-root %s) on %s", *def, *gitRoot, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
