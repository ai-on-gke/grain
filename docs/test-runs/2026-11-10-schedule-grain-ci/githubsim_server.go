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

// githubsim_server serves githubsim REST endpoints plus real `git http-backend`
// bare repositories on a single loopback HTTP listener for a real `grain daemon`
// started with `-github-host 127.0.0.1:<port> -github-insecure-http`.
//
// Inspection endpoints under `/_sim/`:
//   - GET  /_sim/state      -> JSON summary of check runs, commit statuses, PRs, and write requests
//   - POST /_sim/reset-log  -> clears recorded request/write logs between steps
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

var (
	gitSmartRE  = regexp.MustCompile(`^/([^/]+)/([^/]+?)(?:\.git)?/(info/refs|git-upload-pack|git-receive-pack)$`)
	repoRouteRE = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)(?:/|$)`)
	statusRE    = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/statuses/([^/]+)$`)
	checkRunsRE = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/check-runs(?:/|$)`)
)

type recordedWrite struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	Body   string `json:"body,omitempty"`
}

type multiForge struct {
	mu           sync.Mutex
	reposDir     string
	sims         map[string]*githubsim.Sim
	handlers     map[string]http.Handler
	gitUpstream  *githubsim.GitUpstream
	statusWrites []recordedWrite
	checkWrites  []recordedWrite
	allMutations []recordedWrite
}

func main() {
	addr := flag.String("addr", "127.0.0.1:4250", "loopback listen address")
	reposDir := flag.String("repos-dir", "", "directory containing bare <owner>/<repo>.git repositories")
	reposList := flag.String("repos", "acme/widgets,acme/other", "comma-separated owner/name repositories to serve")
	token := flag.String("token", "sim-token", "accepted GitHub token")
	flag.Parse()

	if *reposDir == "" {
		log.Fatal("-repos-dir is required")
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		log.Fatalf("finding git: %v", err)
	}

	mf := &multiForge{
		reposDir: *reposDir,
		sims:     map[string]*githubsim.Sim{},
		handlers: map[string]http.Handler{},
	}

	var names []string
	for _, raw := range strings.Split(*reposList, ",") {
		full := strings.TrimSpace(raw)
		if full == "" {
			continue
		}
		parts := strings.SplitN(full, "/", 2)
		if len(parts) != 2 {
			log.Fatalf("invalid repo %q", full)
		}
		bare := filepath.Join(*reposDir, parts[0], parts[1]+".git")
		var sim *githubsim.Sim
		if _, statErr := os.Stat(filepath.Join(bare, "HEAD")); statErr == nil {
			sim = githubsim.New(parts[0], parts[1], bare, "main")
			_ = exec.Command(gitBin, "-C", bare, "config", "http.receivepack", "true").Run()
		} else {
			var createErr error
			sim, createErr = githubsim.CreateEmptyRepository(parts[0], parts[1], bare)
			if createErr != nil {
				log.Fatalf("creating bare repo %s: %v", full, createErr)
			}
		}
		sim.PullRequestBase = githubsim.UniquePullRequestBase()
		key := strings.ToLower(full)
		mf.sims[key] = sim
		mf.handlers[key] = sim.Handler()
		names = append(names, full)
	}

	mf.gitUpstream = &githubsim.GitUpstream{
		Tokens: map[string]githubsim.GitAccess{
			*token:      {Read: names, Write: names},
			"anonymous": {Read: names, Write: names},
		},
		Public: names,
		Backend: &cgi.Handler{
			Path: gitBin,
			Args: []string{"http-backend"},
			Env: []string{
				"GIT_PROJECT_ROOT=" + *reposDir,
				"GIT_HTTP_EXPORT_ALL=1",
			},
		},
	}

	log.Printf("githubsim_server listening on http://%s for %v (repos-dir=%s)", *addr, names, *reposDir)
	log.Fatal(http.ListenAndServe(*addr, mf))
}

type statusCapture struct {
	http.ResponseWriter
	code int
}

func (sc *statusCapture) WriteHeader(code int) {
	sc.code = code
	sc.ResponseWriter.WriteHeader(code)
}

func (mf *multiForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/_sim/state" && r.Method == http.MethodGet {
		mf.serveState(w)
		return
	}
	if r.URL.Path == "/_sim/reset-log" && r.Method == http.MethodPost {
		mf.mu.Lock()
		mf.statusWrites = nil
		mf.checkWrites = nil
		mf.allMutations = nil
		mf.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}` + "\n"))
		return
	}

	// Route git smart-HTTP requests directly to GitUpstream + git http-backend.
	// Ensure path carries .git so git http-backend finds <owner>/<name>.git on disk.
	if m := gitSmartRE.FindStringSubmatch(r.URL.Path); m != nil {
		r2 := r.Clone(r.Context())
		r2.URL.Path = fmt.Sprintf("/%s/%s.git/%s", m[1], m[2], m[3])
		mf.gitUpstream.ServeHTTP(w, r2)
		return
	}

	// Strip optional /api/v3 prefix used by ReleaseHostClient.
	cleanPath := strings.TrimPrefix(r.URL.Path, "/api/v3")
	if cleanPath == "" {
		cleanPath = "/"
	}

	// Merged /user/repos and /installation/repositories across all configured Sims.
	if r.Method == http.MethodGet && (cleanPath == "/user/repos" || cleanPath == "/installation/repositories") {
		mf.serveRepoList(w, cleanPath)
		return
	}

	var bodyCopy []byte
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Body != nil {
		bodyCopy, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(bodyCopy)))
	}

	// Intercept commit status writes if any caller ever sends POST /repos/{o}/{r}/statuses/{sha}.
	if m := statusRE.FindStringSubmatch(cleanPath); m != nil && r.Method == http.MethodPost {
		mf.mu.Lock()
		rec := recordedWrite{Method: r.Method, Path: cleanPath, Status: http.StatusCreated, Body: string(bodyCopy)}
		mf.statusWrites = append(mf.statusWrites, rec)
		mf.allMutations = append(mf.allMutations, rec)
		mf.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"state":"success"}` + "\n"))
		return
	}

	var target http.Handler
	if m := repoRouteRE.FindStringSubmatch(cleanPath); m != nil {
		key := strings.ToLower(m[1] + "/" + m[2])
		target = mf.handlers[key]
	} else {
		for _, h := range mf.handlers {
			target = h
			break
		}
	}
	if target == nil {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}

	r2 := r.Clone(r.Context())
	r2.URL.Path = cleanPath
	r2.URL.RawPath = ""
	sc := &statusCapture{ResponseWriter: w, code: http.StatusOK}
	target.ServeHTTP(sc, r2)

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		rec := recordedWrite{Method: r.Method, Path: cleanPath, Status: sc.code, Body: truncate(string(bodyCopy), 512)}
		mf.mu.Lock()
		mf.allMutations = append(mf.allMutations, rec)
		if checkRunsRE.MatchString(cleanPath) {
			mf.checkWrites = append(mf.checkWrites, rec)
		}
		mf.mu.Unlock()
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func (mf *multiForge) serveRepoList(w http.ResponseWriter, cleanPath string) {
	var repos []map[string]any
	for _, sim := range mf.sims {
		full := sim.Owner + "/" + sim.Repo
		repos = append(repos, map[string]any{
			"full_name":      full,
			"name":           sim.Repo,
			"default_branch": "main",
			"private":        true,
			"owner":          map[string]any{"login": sim.Owner},
			"permissions":    map[string]any{"admin": true, "push": true, "pull": true},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	if cleanPath == "/installation/repositories" {
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(repos), "repositories": repos})
		return
	}
	_ = json.NewEncoder(w).Encode(repos)
}

func (mf *multiForge) serveState(w http.ResponseWriter) {
	mf.mu.Lock()
	defer mf.mu.Unlock()

	reposOut := map[string]any{}
	for key, sim := range mf.sims {
		reposOut[key] = map[string]any{
			"checkRuns":    sim.CheckRuns,
			"pullRequests": sim.PullRequests,
		}
	}
	out := map[string]any{
		"repos":        reposOut,
		"statusWrites": mf.statusWrites,
		"checkWrites":  mf.checkWrites,
		"allMutations": mf.allMutations,
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
