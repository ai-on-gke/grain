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

// mock_actions serves a local GitHub REST + Actions + git-http-backend server
// on 127.0.0.1:4284 for the 2026-11-19 §8 walkthrough:
//   - Run 42 (completed): 3 jobs (build, test/unit, lint) whose per-job logs
//     return 200 with 0 bytes after a signed redirect, while the run zip holds
//     1_build.txt, test_unit/2_run.txt + 1_setup.txt, and 3_lint.txt.
//   - Run 43 (in_progress): 1 in_progress job returning 200 with 0 bytes
//     (must NOT trigger a run-zip fetch).
//   - Run 44 (completed): 1 job ("heavy") whose run zip holds 20 step files
//     inflating to 5 MiB each (100 MiB total) to verify the 64 MiB shared cap.
//   - Run 45 (completed): 2 jobs ("build", "lint") whose job logs and run zip
//     both fail (configurable: 404 or empty 200) to verify dual-failure errors
//     and actionsLiveTTL negative caching.
package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

func buildZip(files [][2]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f[0])
		if err != nil {
			panic(err)
		}
		if _, err := io.WriteString(w, f[1]); err != nil {
			panic(err)
		}
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func buildBombZip(trailingNewline bool) []byte {
	const stepSize = 5 << 20 // 5 MiB per step, 20 steps = 100 MiB total
	chunk := strings.Repeat("A", stepSize)
	if trailingNewline {
		chunk = strings.Repeat("A", stepSize-1) + "\n"
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 1; i <= 20; i++ {
		w, err := zw.Create(fmt.Sprintf("heavy/%d_step.txt", i))
		if err != nil {
			panic(err)
		}
		if _, err := io.WriteString(w, chunk); err != nil {
			panic(err)
		}
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func main() {
	addr := flag.String("addr", "127.0.0.1:4284", "listen address")
	owner := flag.String("owner", "acme", "repository owner")
	repo := flag.String("repo", "widgets", "repository name")
	bare := flag.String("bare", "", "bare repository path")
	flag.Parse()

	sim := githubsim.New(*owner, *repo, *bare, "main")
	sim.Reachable = map[string][]githubsim.ReachableRepo{
		"ghp_walkthrough": {{FullName: *owner + "/" + *repo, Push: true}},
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

	zip42 := buildZip([][2]string{
		{"1_build.txt", "build step ok\nartifact built\n"},
		{"test_unit/2_run.txt", "FAIL TestWidget (0.01s)\nexpected 42, got 0\n"},
		{"test_unit/1_setup.txt", "setup env ok\ndb ready\n"},
		{"3_lint.txt", "lint clean\n0 issues\n"},
	})
	zip44NoNL := buildBombZip(false)
	zip44WithNL := buildBombZip(true)

	var (
		mu              sync.Mutex
		counts          = map[string]int{}
		run45Mode       = "empty_and_404" // or "empty_and_empty"
		run44TrailingNL = false
	)
	inc := func(key string) {
		mu.Lock()
		counts[key]++
		mu.Unlock()
	}

	now := time.Now().UTC().Truncate(time.Second)
	sha := "1111111111111111111111111111111111111111"

	makeRun := func(id int64, name, status, conclusion string) map[string]any {
		return map[string]any{
			"id":          id,
			"name":        name,
			"path":        ".github/workflows/ci.yml",
			"head_branch": "main",
			"head_sha":    sha,
			"event":       "push",
			"status":      status,
			"conclusion":  conclusion,
			"run_attempt": 1,
			"created_at":  now.Add(-time.Duration(50-id) * time.Minute),
			"updated_at":  now.Add(-time.Duration(50-id) * time.Minute),
			"html_url":    fmt.Sprintf("http://%s/%s/%s/actions/runs/%d", *addr, *owner, *repo, id),
		}
	}

	runs := map[int64]map[string]any{
		42: makeRun(42, "CI fallback", "completed", "failure"),
		43: makeRun(43, "CI live", "in_progress", ""),
		44: makeRun(44, "CI heavy zip", "completed", "success"),
		45: makeRun(45, "CI dual fail", "completed", "failure"),
	}

	makeJob := func(runID, jobID int64, name, status, conclusion string, steps []map[string]any) map[string]any {
		j := map[string]any{
			"id":            jobID,
			"run_id":        runID,
			"run_attempt":   1,
			"workflow_name": runs[runID]["name"],
			"head_sha":      sha,
			"name":          name,
			"status":        status,
			"conclusion":    conclusion,
			"created_at":    now.Add(-10 * time.Minute),
			"started_at":    now.Add(-9 * time.Minute),
			"html_url":      fmt.Sprintf("http://%s/%s/%s/actions/runs/%d/job/%d", *addr, *owner, *repo, runID, jobID),
			"steps":         steps,
		}
		if status == "completed" {
			j["completed_at"] = now.Add(-8 * time.Minute)
		}
		return j
	}

	jobsByRun := map[int64][]map[string]any{
		42: {
			makeJob(42, 701, "build", "completed", "success", []map[string]any{
				{"number": 1, "name": "Build", "status": "completed", "conclusion": "success"},
			}),
			makeJob(42, 702, "test/unit", "completed", "failure", []map[string]any{
				{"number": 1, "name": "Setup", "status": "completed", "conclusion": "success"},
				{"number": 2, "name": "Run", "status": "completed", "conclusion": "failure"},
			}),
			makeJob(42, 703, "lint", "completed", "success", []map[string]any{
				{"number": 1, "name": "Lint", "status": "completed", "conclusion": "success"},
			}),
		},
		43: {
			makeJob(43, 801, "build", "in_progress", "", []map[string]any{
				{"number": 1, "name": "Compile", "status": "in_progress", "conclusion": ""},
			}),
		},
		44: {
			makeJob(44, 901, "heavy", "completed", "success", []map[string]any{
				{"number": 1, "name": "Heavy steps", "status": "completed", "conclusion": "success"},
			}),
		},
		45: {
			makeJob(45, 951, "build", "completed", "failure", []map[string]any{
				{"number": 1, "name": "Build", "status": "completed", "conclusion": "failure"},
			}),
			makeJob(45, 952, "lint", "completed", "failure", []map[string]any{
				{"number": 1, "name": "Lint", "status": "completed", "conclusion": "failure"},
			}),
		},
	}

	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/_mock/counts", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cp := make(map[string]int, len(counts))
		for k, v := range counts {
			cp[k] = v
		}
		mu.Unlock()
		writeJSON(w, cp)
	})
	mux.HandleFunc("/_mock/reset", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counts = map[string]int{}
		mu.Unlock()
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/_mock/config", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if v := r.URL.Query().Get("run45Mode"); v != "" {
			run45Mode = v
		}
		if v := r.URL.Query().Get("run44TrailingNL"); v != "" {
			run44TrailingNL = v == "1" || v == "true"
		}
		resp := map[string]any{"run45Mode": run45Mode, "run44TrailingNL": run44TrailingNL}
		mu.Unlock()
		writeJSON(w, resp)
	})

	// Blob endpoints simulating signed redirects (note localhost vs 127.0.0.1 host).
	mux.HandleFunc("/_blob/job/", func(w http.ResponseWriter, r *http.Request) {
		inc(r.URL.Path)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Server", "mock-blobstore")
		w.WriteHeader(http.StatusOK) // 0 bytes!
	})
	mux.HandleFunc("/_blob/run/", func(w http.ResponseWriter, r *http.Request) {
		inc(r.URL.Path)
		idStr := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/_blob/run/"), ".zip")
		runID, _ := strconv.ParseInt(idStr, 10, 64)
		switch runID {
		case 42:
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Server", "mock-blobstore")
			_, _ = w.Write(zip42)
		case 44:
			mu.Lock()
			useNL := run44TrailingNL
			mu.Unlock()
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Server", "mock-blobstore")
			if useNL {
				_, _ = w.Write(zip44WithNL)
			} else {
				_, _ = w.Write(zip44NoNL)
			}
		case 45:
			mu.Lock()
			mode := run45Mode
			mu.Unlock()
			w.Header().Set("Server", "mock-blobstore")
			if mode == "empty_and_empty" {
				w.Header().Set("Content-Type", "application/zip")
				w.WriteHeader(http.StatusOK) // 0 bytes!
				return
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	})

	prefix := fmt.Sprintf("/repos/%s/%s", *owner, *repo)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/"+*owner+"/"+*repo+".git/") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/"+*owner)
			gitBackend.ServeHTTP(w, r2)
			return
		}
		if r.URL.Path == prefix+"/actions/runs" && r.Method == http.MethodGet {
			inc(r.URL.Path)
			writeJSON(w, map[string]any{
				"total_count":   4,
				"workflow_runs": []map[string]any{runs[45], runs[44], runs[43], runs[42]},
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, prefix+"/actions/runs/") {
			rest := strings.TrimPrefix(r.URL.Path, prefix+"/actions/runs/")
			parts := strings.Split(rest, "/")
			runID, err := strconv.ParseInt(parts[0], 10, 64)
			if err == nil {
				if len(parts) == 1 {
					inc(r.URL.Path)
					if run, ok := runs[runID]; ok {
						writeJSON(w, run)
						return
					}
					http.NotFound(w, r)
					return
				}
				if len(parts) == 2 && parts[1] == "jobs" {
					inc(r.URL.Path)
					if jobs, ok := jobsByRun[runID]; ok {
						writeJSON(w, map[string]any{"total_count": len(jobs), "jobs": jobs})
						return
					}
					http.NotFound(w, r)
					return
				}
				if len(parts) == 2 && parts[1] == "artifacts" {
					inc(r.URL.Path)
					writeJSON(w, map[string]any{"total_count": 0, "artifacts": []any{}})
					return
				}
				if len(parts) == 2 && parts[1] == "logs" {
					inc(r.URL.Path)
					mu.Lock()
					mode := run45Mode
					mu.Unlock()
					if runID == 45 && mode == "empty_and_404" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusNotFound)
						_, _ = io.WriteString(w, `{"message":"Not Found"}`)
						return
					}
					blobHost := strings.Replace(*addr, "127.0.0.1", "localhost", 1)
					http.Redirect(w, r, fmt.Sprintf("http://%s/_blob/run/%d.zip?sig=RUN_BLOB_SECRET_%d", blobHost, runID, runID), http.StatusFound)
					return
				}
			}
		}
		if strings.HasPrefix(r.URL.Path, prefix+"/actions/jobs/") && strings.HasSuffix(r.URL.Path, "/logs") {
			inc(r.URL.Path)
			idStr := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix+"/actions/jobs/"), "/logs")
			blobHost := strings.Replace(*addr, "127.0.0.1", "localhost", 1)
			http.Redirect(w, r, fmt.Sprintf("http://%s/_blob/job/%s?sig=JOB_BLOB_SECRET_%s", blobHost, idStr, idStr), http.StatusFound)
			return
		}
		if r.URL.Path == "/user" && r.Method == http.MethodGet {
			writeJSON(w, map[string]string{"login": "walk-bot"})
			return
		}
		baseHandler.ServeHTTP(w, r)
	})

	log.Printf("mock_actions: %s/%s on %s (zip42=%dB, zip44=%dB)", *owner, *repo, *addr, len(zip42), len(zip44NoNL))
	log.Fatal(http.ListenAndServe(*addr, mux))
}
