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

// ghsim serves pkg/github/githubsim's REST API over plain HTTP for
// acme/widgets (on trunk) plus a 305-repository Reachable inventory and an
// HTTP MCP gchat-usage stub for docs/test-plans/2026-11-10.md §6.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4283", "listen address")
	bare := flag.String("bare", "", "bare repository backing acme/widgets")
	token := flag.String("token", "tok-walkthrough", "simulated GitHub token")
	flag.Parse()

	sim := githubsim.New("acme", "widgets", *bare, "trunk")
	owners := []string{"acme", "gke-labs", "widgets-org"}
	repos := make([]githubsim.ReachableRepo, 0, 305)
	repos = append(repos,
		githubsim.ReachableRepo{FullName: "acme/widgets", Push: true},
		githubsim.ReachableRepo{FullName: "other/thing", Push: true},
		githubsim.ReachableRepo{FullName: "forge.example.test/ops/tools", Push: true},
		githubsim.ReachableRepo{FullName: "forge.example.test/ops/readonly", Push: false},
	)
	for i := 0; i < 301; i++ {
		owner := owners[i%len(owners)]
		repos = append(repos, githubsim.ReachableRepo{
			FullName: fmt.Sprintf("%s/service-%03d-component", owner, i),
			Push:     i%5 != 0,
		})
	}
	sim.Reachable = map[string][]githubsim.ReachableRepo{
		*token: repos,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp/gchat", serveGChatMCP)
	simHandler := sim.Handler()
	// Smart-HTTP git for acme/widgets, so routes that clone the repository
	// (GET /api/ci/workflows reads .grain/workflows at a ref) can be walked.
	var gitBackend http.Handler
	if gitPath, err := exec.LookPath("git"); err == nil && *bare != "" {
		gitBackend = &cgi.Handler{
			Path: gitPath,
			Root: "/acme",
			Args: []string{"http-backend"},
			Env:  []string{"GIT_PROJECT_ROOT=" + filepath.Dir(*bare), "GIT_HTTP_EXPORT_ALL=1"},
		}
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if gitBackend != nil && strings.HasPrefix(r.URL.Path, "/acme/widgets.git/") {
			gitBackend.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/repos/") && !strings.HasPrefix(r.URL.Path, "/repos/acme/widgets") {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
			if len(parts) >= 2 {
				full := parts[0] + "/" + parts[1]
				w.Header().Set("Content-Type", "application/json")
				if len(parts) == 2 {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"full_name":      full,
						"default_branch": "trunk",
					})
					return
				}
				if len(parts) == 4 && parts[2] == "branches" {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"name":   parts[3],
						"commit": map[string]any{"sha": "0000000000000000000000000000000000000001"},
					})
					return
				}
				if len(parts) >= 3 && parts[2] == "commits" {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"sha":        "0000000000000000000000000000000000000001",
						"check_runs": []any{},
						"statuses":   []any{},
					})
					return
				}
			}
		}
		simHandler.ServeHTTP(w, r)
	})

	log.Printf("ghsim: acme/widgets (%d reachable repos) + /mcp/gchat on %s", len(repos), *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func serveGChatMCP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	reply := func(res any) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  res,
		})
	}
	switch req.Method {
	case "initialize":
		reply(map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "gchat-stub", "version": "1.0.0"},
		})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		reply(map[string]any{
			"tools": []map[string]any{
				{
					"name":        intree.GChatUsageTool,
					"description": "List Google Chat space usage rows.",
					"inputSchema": map[string]any{"type": "object"},
				},
			},
		})
	case "tools/call":
		rows := make([]intree.GChatUsage, 0, 260)
		for i := 0; i < 260; i++ {
			transport := "webhook"
			if i%3 == 0 {
				transport = "bot-api"
			}
			rows = append(rows, intree.GChatUsage{
				Space:     fmt.Sprintf("spaces/AAAA-%03d", i),
				Transport: transport,
			})
		}
		raw, _ := json.Marshal(rows)
		reply(map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": string(raw)},
			},
		})
	default:
		reply(map[string]any{})
	}
}
