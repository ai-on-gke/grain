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

// sim.go serves multi-repo githubsim over plain HTTP (acme/widgets, acme/docs,
// acme/gadgets) alongside git-http-backend and a local ES256 IAP key/minting
// endpoint (/_sim/iap-keys, /_sim/mint-iap?email=...) so the real grain daemon
// verifies real signed JWT assertions for scoped RBAC walkthrough users.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
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

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/golang-jwt/jwt/v5"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4294", "listen address")
	reposDir := flag.String("repos-dir", "", "directory holding widgets.git, docs.git, gadgets.git")
	iapAud := flag.String("iap-aud", "walk-iap-aud", "IAP JWT audience")
	iapIss := flag.String("iap-iss", "https://walk-iap.example", "IAP JWT issuer")
	flag.Parse()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatalf("ecdsa key: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		log.Fatalf("marshal pub: %v", err)
	}
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	const kid = "walk-es256-1"

	repoNames := []string{"widgets", "docs", "gadgets"}
	reachable := make([]githubsim.ReachableRepo, 0, len(repoNames))
	for _, r := range repoNames {
		reachable = append(reachable, githubsim.ReachableRepo{
			FullName: "acme/" + r,
			Push:     true,
		})
	}
	sims := map[string]*githubsim.Sim{}
	handlers := map[string]http.Handler{}
	for _, r := range repoNames {
		bare := filepath.Join(*reposDir, r+".git")
		sim := githubsim.New("acme", r, bare, "main")
		sim.Reachable = map[string][]githubsim.ReachableRepo{
			"ghp_walkthrough": reachable,
		}
		sims[r] = sim
		handlers[r] = sim.Handler()
	}

	gitPath, err := exec.LookPath("git")
	if err != nil {
		log.Fatalf("git not found: %v", err)
	}
	gitBackend := &cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + *reposDir,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}

	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/_sim/iap-keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{kid: pubPEM})
	})
	mux.HandleFunc("/_sim/mint-iap", func(w http.ResponseWriter, r *http.Request) {
		email := strings.TrimSpace(r.URL.Query().Get("email"))
		if email == "" {
			http.Error(w, "email required", http.StatusBadRequest)
			return
		}
		now := time.Now()
		claims := jwt.MapClaims{
			"iss":   *iapIss,
			"aud":   *iapAud,
			"email": email,
			"sub":   "accounts.google.com:walk-" + email,
			"iat":   now.Unix(),
			"exp":   now.Add(2 * time.Hour).Unix(),
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		tok.Header["kid"] = kid
		signed, err := tok.SignedString(priv)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, signed)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		for _, name := range repoNames {
			if strings.HasPrefix(r.URL.Path, "/acme/"+name+".git/") {
				r2 := r.Clone(r.Context())
				r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/acme")
				gitBackend.ServeHTTP(w, r2)
				return
			}
		}
		apiPath := strings.TrimPrefix(r.URL.Path, "/api/v3")
		if r.Method == http.MethodGet && apiPath == "/user" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"login": "grain-bot",
				"name":  "Grain Bot",
				"email": "bot@example.com",
				"type":  "User",
			})
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(apiPath, "/users/") {
			login := strings.TrimPrefix(apiPath, "/users/")
			accountType := "User"
			if strings.HasSuffix(login, "[bot]") {
				accountType = "Bot"
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"login": login, "type": accountType})
			return
		}
		mu.Lock()
		defer mu.Unlock()
		rw := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		handled := false
		for _, name := range repoNames {
			if strings.HasPrefix(apiPath, "/repos/acme/"+name) {
				handlers[name].ServeHTTP(rw, r)
				handled = true
				break
			}
		}
		if !handled {
			handlers["widgets"].ServeHTTP(rw, r)
		}
		if rw.code >= 500 {
			log.Printf("walk-sim %d %s %s", rw.code, r.Method, r.URL.RequestURI())
		}
	})

	log.Printf("walk-sim listening on %s (repos: %v, iap-aud: %s)", *addr, repoNames, *iapAud)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}
