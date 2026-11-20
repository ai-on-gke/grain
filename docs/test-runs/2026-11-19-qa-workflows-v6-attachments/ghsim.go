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

// ghsim serves pkg/github/githubsim over plain HTTP for acme/widgets (4284)
// alongside an optionally auth-enforced git-http-backend and walkthrough
// inspection routes under /_sim/* for docs/test-plans/2026-11-19.md §2.
package main

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

type gitReqRecord struct {
	At        string `json:"at"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Query     string `json:"query,omitempty"`
	Authed    bool   `json:"authed"`
	Status    int    `json:"status"`
	UserAgent string `json:"userAgent,omitempty"`
}

func checkGitAuth(r *http.Request, expectedToken string) bool {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth == "" {
		return false
	}
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		tok := strings.TrimSpace(auth[len("bearer "):])
		return subtle.ConstantTimeCompare([]byte(tok), []byte(expectedToken)) == 1
	}
	if strings.HasPrefix(strings.ToLower(auth), "basic ") {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(auth[len("basic "):]))
		if err != nil {
			return false
		}
		user, pass, ok := strings.Cut(string(raw), ":")
		if !ok {
			return false
		}
		return subtle.ConstantTimeCompare([]byte(pass), []byte(expectedToken)) == 1 ||
			subtle.ConstantTimeCompare([]byte(user), []byte(expectedToken)) == 1
	}
	return false
}

func main() {
	addr := flag.String("addr", "127.0.0.1:4284", "listen address")
	owner := flag.String("owner", "acme", "repository owner")
	repo := flag.String("repo", "widgets", "repository name")
	bare := flag.String("bare", "", "bare repository backing the sim")
	def := flag.String("default", "main", "default branch")
	token := flag.String("token", "ghp_walkthrough", "expected git/API token")
	requireGitAuth := flag.Bool("require-git-auth", true, "reject unauthenticated git HTTP requests with 401")
	flag.Parse()

	sim := githubsim.New(*owner, *repo, *bare, *def)
	sim.Reachable = map[string][]githubsim.ReachableRepo{
		*token: {{FullName: *owner + "/" + *repo, Push: true}},
	}
	baseHandler := sim.Handler()

	gitPath, err := exec.LookPath("git")
	if err != nil {
		log.Fatalf("git not found: %v", err)
	}
	gitBackend := &cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + filepath.Dir(*bare),
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}

	var mu sync.Mutex
	enforceGitAuth := *requireGitAuth
	var gitReqs []gitReqRecord

	mux := http.NewServeMux()
	mux.HandleFunc("/_sim/require-git-auth", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if v := strings.TrimSpace(r.URL.Query().Get("value")); v != "" {
			enforceGitAuth = v == "true" || v == "1"
		}
		cur := enforceGitAuth
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"requireGitAuth": cur})
	})
	mux.HandleFunc("/_sim/git-requests", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.Method == http.MethodDelete {
			gitReqs = nil
		}
		cp := make([]gitReqRecord, len(gitReqs))
		copy(cp, gitReqs)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cp)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/"+*owner+"/"+*repo+".git/") {
			authed := checkGitAuth(r, *token)
			mu.Lock()
			mustAuth := enforceGitAuth
			mu.Unlock()
			status := http.StatusOK
			if mustAuth && !authed {
				status = http.StatusUnauthorized
				mu.Lock()
				gitReqs = append(gitReqs, gitReqRecord{
					At:        time.Now().UTC().Format(time.RFC3339Nano),
					Method:    r.Method,
					Path:      r.URL.Path,
					Query:     r.URL.RawQuery,
					Authed:    false,
					Status:    status,
					UserAgent: r.UserAgent(),
				})
				mu.Unlock()
				w.Header().Set("WWW-Authenticate", `Basic realm="ghsim"`)
				http.Error(w, "authentication required for git clone/fetch", http.StatusUnauthorized)
				return
			}
			mu.Lock()
			gitReqs = append(gitReqs, gitReqRecord{
				At:        time.Now().UTC().Format(time.RFC3339Nano),
				Method:    r.Method,
				Path:      r.URL.Path,
				Query:     r.URL.RawQuery,
				Authed:    authed,
				Status:    status,
				UserAgent: r.UserAgent(),
			})
			mu.Unlock()
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/"+*owner)
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
	})

	log.Printf("ghsim: %s/%s (default %s, requireGitAuth=%v) on %s", *owner, *repo, sim.DefaultBranch, *requireGitAuth, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
