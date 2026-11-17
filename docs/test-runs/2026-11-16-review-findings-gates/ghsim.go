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

// ghsim serves pkg/github/githubsim over plain HTTP alongside git-http-backend
// and an ephemeral Grain Hub JWKS/assertion signer so a real grain daemon can
// verify full-access and repo-scoped human callers without mocking UI routes.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/hub"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4294", "listen address")
	owner := flag.String("owner", "acme", "repository owner")
	repo := flag.String("repo", "widgets", "repository name")
	bare := flag.String("bare", "", "bare repository backing the sim")
	def := flag.String("default", "main", "default branch")
	aud := flag.String("audience", "https://walk.example.com", "hub JWT audience")
	flag.Parse()

	sim := githubsim.New(*owner, *repo, *bare, *def)
	sim.Reachable = map[string][]githubsim.ReachableRepo{
		"ghp_walkthrough": {{FullName: *owner + "/" + *repo, Push: true}},
	}
	baseHandler := sim.Handler()

	signer, err := hub.NewEphemeralLocalSigner("walk-1")
	if err != nil {
		log.Fatalf("ephemeral hub signer: %v", err)
	}

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
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		jwks, err := signer.JWKS(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	})
	mux.HandleFunc("/_sim/jwt", func(w http.ResponseWriter, r *http.Request) {
		email := strings.TrimSpace(r.URL.Query().Get("email"))
		if email == "" {
			http.Error(w, "missing email", http.StatusBadRequest)
			return
		}
		now := time.Now().UTC()
		tok, err := signer.SignAssertion(context.Background(), hub.HubClaims{
			Issuer:    hub.HubAssertionIssuer,
			Audience:  *aud,
			Subject:   email,
			Email:     email,
			Kind:      "human",
			IssuedAt:  now.Unix(),
			NotBefore: now.Add(-hub.AssertionNotBeforeSkew).Unix(),
			ExpiresAt: now.Add(4 * time.Minute).Unix(),
			JWTID:     fmt.Sprintf("walk-%d", now.UnixNano()),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(tok))
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
		if strings.HasPrefix(r.URL.Path, "/"+*owner+"/"+*repo+".git/") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/"+*owner)
			gitBackend.ServeHTTP(w, r2)
			return
		}
		baseHandler.ServeHTTP(w, r)
	})

	log.Printf("ghsim listening on %s for %s/%s (%s)", *addr, *owner, *repo, *bare)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
