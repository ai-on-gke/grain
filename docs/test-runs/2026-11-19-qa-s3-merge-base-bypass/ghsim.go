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

// ghsim serves pkg/github/githubsim over plain HTTP for acme/widgets and
// acme/secondary alongside git-http-backend, an ES256 Hub JWKS issuer for
// direct and delegated human JWT assertions, and /_sim/* walkthrough controls.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
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
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/webauth"
	"github.com/golang-jwt/jwt/v5"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4286", "listen address")
	owner := flag.String("owner", "acme", "repository owner")
	repo := flag.String("repo", "widgets", "primary repository name")
	bare := flag.String("bare", "", "bare repository backing the primary repo")
	secondaryRepo := flag.String("secondary-repo", "secondary", "secondary repository name")
	bareSecondary := flag.String("bare-secondary", "", "bare repository backing the secondary repo")
	def := flag.String("default", "main", "default branch")
	hubIssuer := flag.String("hub-issuer", "https://hub.example.test", "Hub JWT issuer")
	hubAudience := flag.String("hub-audience", "https://grain.example.test", "Hub JWT audience")
	flag.Parse()

	sim := githubsim.New(*owner, *repo, *bare, *def)
	reachable := []githubsim.ReachableRepo{{FullName: *owner + "/" + *repo, Push: true}}
	var simSec *githubsim.Sim
	var secHandler http.Handler
	if *bareSecondary != "" {
		simSec = githubsim.New(*owner, *secondaryRepo, *bareSecondary, *def)
		reachable = append(reachable, githubsim.ReachableRepo{FullName: *owner + "/" + *secondaryRepo, Push: true})
		simSec.Reachable = map[string][]githubsim.ReachableRepo{"ghp_walkthrough": reachable}
		secHandler = simSec.Handler()
	}
	sim.Reachable = map[string][]githubsim.ReachableRepo{
		"ghp_walkthrough": reachable,
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

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	byteLen := (ecKey.PublicKey.Curve.Params().BitSize + 7) / 8
	xBytes := ecKey.PublicKey.X.FillBytes(make([]byte, byteLen))
	yBytes := ecKey.PublicKey.Y.FillBytes(make([]byte, byteLen))
	jwksPayload := map[string]any{
		"keys": []map[string]string{
			{
				"kty": "EC",
				"crv": "P-256",
				"alg": "ES256",
				"use": "sig",
				"kid": "walk-key-1",
				"x":   base64.RawURLEncoding.EncodeToString(xBytes),
				"y":   base64.RawURLEncoding.EncodeToString(yBytes),
			},
		},
	}

	var mu sync.Mutex
	var jtiSeq int

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwksPayload)
	})
	mux.HandleFunc("/_sim/mint-jwt", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Sub       string `json:"sub"`
			Kind      string `json:"kind"`
			Delegate  string `json:"delegate"`
			App       string `json:"app"`
			Grant     string `json:"grant"`
			Access    string `json:"access"`
			RequestID string `json:"requestId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if payload.Kind == "" {
			payload.Kind = string(webauth.PrincipalHuman)
		}
		mu.Lock()
		jtiSeq++
		seq := jtiSeq
		mu.Unlock()
		now := time.Now().Truncate(time.Second)
		claims := ui.HubClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    *hubIssuer,
				Subject:   payload.Sub,
				Audience:  jwt.ClaimStrings{*hubAudience},
				IssuedAt:  jwt.NewNumericDate(now.Add(-5 * time.Second)),
				NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
				ExpiresAt: jwt.NewNumericDate(now.Add(2 * time.Minute)),
				ID:        fmt.Sprintf("walk-jti-%d", seq),
			},
			Kind:      webauth.PrincipalKind(payload.Kind),
			RequestID: fmt.Sprintf("walk-rid-%d", seq),
		}
		if payload.Delegate != "" {
			actObj := map[string]any{
				"sub":  payload.Delegate,
				"kind": "service",
			}
			if payload.App != "" {
				actObj["app"] = payload.App
			}
			if payload.Grant != "" {
				actObj["grant"] = payload.Grant
			}
			if payload.Access != "" {
				actObj["access"] = payload.Access
			}
			b, _ := json.Marshal(actObj)
			claims.Act = b
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		tok.Header["kid"] = "walk-key-1"
		signed, err := tok.SignedString(ecKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": signed})
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

	mux.HandleFunc("/_sim/seed-actions", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Append bool `json:"append"`
			Runs   []struct {
				ID         int64     `json:"id"`
				Name       string    `json:"name"`
				Path       string    `json:"path"`
				HeadBranch string    `json:"headBranch"`
				HeadSHA    string    `json:"headSha"`
				Event      string    `json:"event"`
				Status     string    `json:"status"`
				Conclusion string    `json:"conclusion"`
				CreatedAt  time.Time `json:"createdAt"`
				UpdatedAt  time.Time `json:"updatedAt"`
				Jobs       []struct {
					ID         int64  `json:"id"`
					Name       string `json:"name"`
					Status     string `json:"status"`
					Conclusion string `json:"conclusion"`
					Log        string `json:"log"`
				} `json:"jobs"`
			} `json:"runs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		var seeded []githubsim.WorkflowRun
		if payload.Append {
			seeded = append(seeded, sim.WorkflowRuns...)
		}
		for _, in := range payload.Runs {
			var jobs []githubsim.WorkflowJob
			for _, j := range in.Jobs {
				jobs = append(jobs, githubsim.WorkflowJob{
					Name:       j.Name,
					Status:     j.Status,
					Conclusion: j.Conclusion,
					Log:        j.Log,
				})
			}
			created := in.CreatedAt
			if created.IsZero() {
				created = time.Now().UTC()
			}
			updated := in.UpdatedAt
			if updated.IsZero() {
				updated = created
			}
			seeded = append(seeded, githubsim.WorkflowRun{
				ID:         in.ID,
				Name:       in.Name,
				Path:       in.Path,
				HeadBranch: in.HeadBranch,
				HeadSHA:    in.HeadSHA,
				Event:      in.Event,
				Status:     in.Status,
				Conclusion: in.Conclusion,
				RunAttempt: 1,
				CreatedAt:  created,
				UpdatedAt:  updated,
				Jobs:       jobs,
			})
		}
		sim.WorkflowRuns = seeded
		mu.Unlock()
		fmt.Fprintf(w, `{"seeded":%d}`+"\n", len(seeded))
	})

	mux.HandleFunc("/_sim/reruns", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		out := map[string]any{
			"rerunFailedJobs": sim.RerunFailedJobs,
			"rerunRuns":       sim.RerunRuns,
			"rerunJobs":       sim.RerunJobs,
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("/_sim/prs", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		var list []map[string]any
		for _, pr := range sim.PullRequests {
			list = append(list, map[string]any{
				"number": pr.Number,
				"title":  pr.Title,
				"head":   pr.Head,
				"base":   pr.Base,
				"state":  pr.State,
				"merged": pr.Merged,
			})
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
	})

	mux.HandleFunc("/_sim/branches", func(w http.ResponseWriter, _ *http.Request) {
		out, err := exec.Command("git", "--git-dir="+*bare, "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads").CombinedOutput()
		if err != nil {
			http.Error(w, string(out), http.StatusInternalServerError)
			return
		}
		branches := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				branches[fields[0]] = fields[1]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(branches)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/"+*owner+"/"+*repo+".git/") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/"+*owner)
			gitBackend.ServeHTTP(w, r2)
			return
		}
		if *bareSecondary != "" && strings.HasPrefix(r.URL.Path, "/"+*owner+"/"+*secondaryRepo+".git/") {
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
		if secHandler != nil && strings.HasPrefix(r.URL.Path, "/repos/"+*owner+"/"+*secondaryRepo) {
			mu.Lock()
			defer mu.Unlock()
			secHandler.ServeHTTP(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		baseHandler.ServeHTTP(w, r)
	})

	log.Printf("ghsim: %s/%s + %s/%s (default %s) on %s (JWKS issuer=%s)", *owner, *repo, *owner, *secondaryRepo, sim.DefaultBranch, *addr, *hubIssuer)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
