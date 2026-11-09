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

// walksim <addr> <root>: local GitHub smart-HTTP + multi-repo githubsim +
// GitHub App sandbox-org API stub for the 2026-11-08 §3 walkthrough.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

type sandboxEvent struct {
	At     string   `json:"at"`
	Action string   `json:"action"`
	Repo   string   `json:"repo,omitempty"`
	Repos  []string `json:"repos,omitempty"`
}

func initBareRepo(root, owner, name string, branches ...string) string {
	bare := filepath.Join(root, owner, name+".git")
	if _, err := os.Stat(bare); err == nil {
		return bare
	}
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		log.Fatalf("mkdir %s: %v", bare, err)
	}
	work, err := os.MkdirTemp("", "walksim-seed-*")
	if err != nil {
		log.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(work)

	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Walk Sim",
			"GIT_AUTHOR_EMAIL=walk@example.com",
			"GIT_COMMITTER_NAME=Walk Sim",
			"GIT_COMMITTER_EMAIL=walk@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("", "init", "--bare", "--initial-branch=main", bare)
	run(bare, "config", "http.receivepack", "true")
	run("", "init", "--initial-branch=main", work)
	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("# "+owner+"/"+name+"\n"), 0o644)
	run(work, "add", "README.md")
	run(work, "commit", "-m", "initial commit")
	run(work, "push", bare, "HEAD:refs/heads/main")
	for _, br := range branches {
		if br != "" && br != "main" {
			run(work, "push", bare, "HEAD:refs/heads/"+br)
		}
	}
	return bare
}

func main() {
	if len(os.Args) < 3 {
		log.Fatalf("usage: walksim <addr> <root> [key-out]")
	}
	addr, root := os.Args[1], os.Args[2]
	if err := os.MkdirAll(root, 0o755); err != nil {
		log.Fatal(err)
	}
	if len(os.Args) >= 4 {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			log.Fatal(err)
		}
		pemBytes := pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(key),
		})
		if err := os.WriteFile(os.Args[3], pemBytes, 0o600); err != nil {
			log.Fatal(err)
		}
	}

	var mu sync.Mutex
	sims := map[string]*githubsim.Sim{}
	getSim := func(owner, name string) *githubsim.Sim {
		key := strings.ToLower(owner + "/" + name)
		mu.Lock()
		defer mu.Unlock()
		if s, ok := sims[key]; ok {
			return s
		}
		bare := initBareRepo(root, owner, name, "main", "release", "develop")
		s := githubsim.New(owner, name, bare, "main")
		sims[key] = s
		return s
	}
	for _, r := range [][2]string{
		{"acme", "app"},
		{"acme", "widgets"},
		{"acme", "docs"},
		{"acme", "site"},
	} {
		_ = getSim(r[0], r[1])
	}

	sandboxRepos := map[string]time.Time{}
	var events []sandboxEvent

	gitCGI := &cgi.Handler{
		Path: "/usr/bin/git",
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=walk"},
	}

	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ".git/") {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
			if len(parts) >= 2 && strings.HasSuffix(parts[1], ".git") {
				owner, name := parts[0], strings.TrimSuffix(parts[1], ".git")
				if owner != "sandbox-org" {
					_ = getSim(owner, name)
				}
			}
			gitCGI.ServeHTTP(w, r)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/api/v3")
		reqURI := strings.TrimPrefix(r.URL.RequestURI(), "/api/v3")

		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/app/installations"):
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				http.Error(w, `{"message":"missing app JWT"}`, http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 77, "account": map[string]any{"login": "sandbox-org"}},
			})
			return

		case r.Method == http.MethodPost && path == "/app/installations/77/access_tokens":
			var req struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			events = append(events, sandboxEvent{
				At:     time.Now().UTC().Format(time.RFC3339),
				Action: "MINT_TOKEN",
				Repos:  req.Repositories,
			})
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs-walk-sandbox-token",
				"expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			})
			return

		case r.Method == http.MethodPost && path == "/orgs/sandbox-org/repos":
			var req struct {
				Name    string `json:"name"`
				Private bool   `json:"private"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
				http.Error(w, `{"message":"invalid repo create"}`, http.StatusBadRequest)
				return
			}
			initBareRepo(root, "sandbox-org", req.Name, "main")
			mu.Lock()
			sandboxRepos[req.Name] = time.Now().UTC().Truncate(time.Second)
			events = append(events, sandboxEvent{
				At:     time.Now().UTC().Format(time.RFC3339),
				Action: "CREATE_REPO",
				Repo:   req.Name,
			})
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":      req.Name,
				"full_name": "sandbox-org/" + req.Name,
			})
			return

		case r.Method == http.MethodGet && path == "/orgs/sandbox-org/repos":
			type item struct {
				Name      string    `json:"name"`
				CreatedAt time.Time `json:"created_at"`
			}
			mu.Lock()
			list := make([]item, 0, len(sandboxRepos))
			for name, created := range sandboxRepos {
				list = append(list, item{Name: name, CreatedAt: created})
			}
			mu.Unlock()
			sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(list)
			return

		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/repos/sandbox-org/"):
			name := strings.TrimPrefix(path, "/repos/sandbox-org/")
			mu.Lock()
			_, exists := sandboxRepos[name]
			delete(sandboxRepos, name)
			events = append(events, sandboxEvent{
				At:     time.Now().UTC().Format(time.RFC3339),
				Action: "DELETE_REPO",
				Repo:   name,
			})
			mu.Unlock()
			_ = os.RemoveAll(filepath.Join(root, "sandbox-org", name+".git"))
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return

		case r.Method == http.MethodPost && path == "/sim/seed-sandbox-repo":
			name := r.URL.Query().Get("name")
			age, err := time.ParseDuration(r.URL.Query().Get("age"))
			if err != nil || name == "" {
				http.Error(w, "want ?name=...&age=...", http.StatusBadRequest)
				return
			}
			mu.Lock()
			sandboxRepos[name] = time.Now().UTC().Add(-age).Truncate(time.Second)
			events = append(events, sandboxEvent{
				At:     time.Now().UTC().Format(time.RFC3339),
				Action: "SEED_REPO",
				Repo:   fmt.Sprintf("%s (age=%s)", name, age),
			})
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return

		case r.Method == http.MethodGet && path == "/sim/sandbox-state":
			mu.Lock()
			live := make([]string, 0, len(sandboxRepos))
			for name := range sandboxRepos {
				live = append(live, name)
			}
			sort.Strings(live)
			evCopy := append([]sandboxEvent(nil), events...)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"live":   live,
				"events": evCopy,
			})
			return
		}

		// Route /repos/<owner>/<name>... to that repository's githubsim.Sim.
		owner, name := "acme", "app"
		if strings.HasPrefix(path, "/repos/") {
			parts := strings.Split(strings.TrimPrefix(path, "/repos/"), "/")
			if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
				owner, name = parts[0], parts[1]
			}
		}
		sim := getSim(owner, name)
		defer func() {
			if v := recover(); v != nil {
				http.Error(w, fmt.Sprintf(`{"message":"githubsim: unhandled %s %s: %v"}`, r.Method, reqURI, v), http.StatusNotImplemented)
			}
		}()
		body, _ := io.ReadAll(r.Body)
		headers := map[string]string{}
		for k := range r.Header {
			headers[k] = r.Header.Get(k)
		}
		resp, err := sim.Request(r.Method, reqURI, headers, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		status := resp.Status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(resp.Body)
	})

	log.Printf("walksim listening on http://%s (root=%s)", addr, root)
	log.Fatal(http.ListenAndServe(addr, mux))
}
