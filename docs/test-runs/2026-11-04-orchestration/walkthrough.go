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

// Command walkthrough executes §9 of docs/test-plans/2026-11-04.md
// ("Orchestration, schedules and repositories") against real SQLite stores,
// real bare git repositories, a real `grain daemon` subprocess (item 1),
// real `gitproxy` + `githubsim` + `orchestrator.RunCycle` (items 2-4), and
// `ui.NewServer` serving the embedded frontend (item 5).
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing/fstest"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/durable"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/repodecl"
	"github.com/ai-on-gke/grain/pkg/secretsrepo"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	restore := orchestrator.SetCheckRegistrationWindow(0)
	defer restore()

	mode := flag.String("mode", "fast", "which walkthrough items to run: fast (2,3,4,5), item1, item2, item3, item4, item5, serve, all")
	outDir := flag.String("out", "docs/test-runs/2026-11-04-orchestration", "directory for JSON and log artifacts")
	grainBin := flag.String("grain-bin", "/home/debian/bin/grain", "path to built grain binary")
	serveAddr := flag.String("serve-addr", "127.0.0.1:4291", "listen address in -mode=serve")
	accessCode := flag.String("access-code", "walkthrough-3075-secret-code", "UI access code in -mode=serve")
	flag.Parse()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("creating output dir: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch *mode {
	case "item1":
		must(runItem1(ctx, *outDir, *grainBin))
	case "item2":
		must(runItem2(ctx, *outDir))
	case "item3":
		must(runItem3(ctx, *outDir))
	case "item4":
		must(runItem4(ctx, *outDir))
	case "item5":
		must(runItem5(ctx, *outDir, "", ""))
	case "serve":
		must(runItem5(ctx, *outDir, *serveAddr, *accessCode))
	case "fast":
		must(runItem2(ctx, *outDir))
		must(runItem3(ctx, *outDir))
		must(runItem4(ctx, *outDir))
		must(runItem5(ctx, *outDir, "", ""))
	case "all":
		must(runItem1(ctx, *outDir, *grainBin))
		must(runItem2(ctx, *outDir))
		must(runItem3(ctx, *outDir))
		must(runItem4(ctx, *outDir))
		must(runItem5(ctx, *outDir, "", ""))
	default:
		log.Fatalf("unknown -mode %q", *mode)
	}
}

func must(err error) {
	if err != nil {
		log.Fatalf("FAIL: %v", err)
	}
}

func boolPtr(v bool) *bool    { return &v }
func strPtr(v string) *string { return &v }

func writeArtifact(outDir, name string, data any) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, name), append(b, '\n'), 0o644)
}

func gitCmd(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Walkthrough Tester",
		"GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=Walkthrough Tester",
		"GIT_COMMITTER_EMAIL=tester@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		panic(fmt.Sprintf("git %v in %s failed: %v\n%s", args, dir, err, out))
	}
	return strings.TrimSpace(string(out))
}

func runGitAllowErr(dir string, envExtra []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Walkthrough Tester",
		"GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=Walkthrough Tester",
		"GIT_COMMITTER_EMAIL=tester@example.com",
	)
	cmd.Env = append(cmd.Env, envExtra...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func initBareWithMain(root, owner, name string, files map[string]string) (bareDir, seedDir, headSHA string) {
	bareDir = filepath.Join(root, owner, name+".git")
	if err := os.MkdirAll(filepath.Dir(bareDir), 0o755); err != nil {
		panic(err)
	}
	gitCmd(root, "init", "-q", "--bare", bareDir)
	gitCmd(bareDir, "symbolic-ref", "HEAD", "refs/heads/main")
	gitCmd(bareDir, "config", "http.receivepack", "true")

	seedDir = filepath.Join(root, "seed-"+owner+"-"+name)
	gitCmd(root, "clone", "-q", bareDir, seedDir)
	gitCmd(seedDir, "checkout", "-q", "-b", "main")
	for k, v := range files {
		full := filepath.Join(seedDir, k)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			panic(err)
		}
		if err := os.WriteFile(full, []byte(v), 0o644); err != nil {
			panic(err)
		}
	}
	gitCmd(seedDir, "add", ".")
	gitCmd(seedDir, "commit", "-q", "-m", "initial commit")
	gitCmd(seedDir, "push", "-q", "origin", "main")
	headSHA = gitCmd(seedDir, "rev-parse", "HEAD")
	return bareDir, seedDir, headSHA
}

func pushBranchFiles(bareDir, baseBranch, newBranch string, files map[string]string, msg string) string {
	tmp, err := os.MkdirTemp("", "walk-push-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)
	work := filepath.Join(tmp, "w")
	gitCmd(tmp, "clone", "-q", "-b", baseBranch, bareDir, work)
	if newBranch != baseBranch {
		gitCmd(work, "checkout", "-q", "-b", newBranch)
	}
	for k, v := range files {
		full := filepath.Join(work, k)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			panic(err)
		}
		if err := os.WriteFile(full, []byte(v), 0o644); err != nil {
			panic(err)
		}
	}
	gitCmd(work, "add", ".")
	gitCmd(work, "commit", "-q", "-m", msg)
	gitCmd(work, "push", "-q", "-f", "origin", newBranch)
	return gitCmd(work, "rev-parse", "HEAD")
}

// realGitMergeableTransport wraps githubsim.Sim so that every pull-request read
// computes Mergeable using `git merge-tree --write-tree` against the real bare
// repository (unless freezeMergeable is set to simulate GitHub's stale-cache window).
type realGitMergeableTransport struct {
	mu                sync.Mutex
	sims              map[string]*githubsim.Sim // "owner/name" -> Sim
	freezeMergeable   bool
	failMerge502ForPR int
}

func (t *realGitMergeableTransport) updateMergeableLocked(sim *githubsim.Sim) {
	if t.freezeMergeable {
		return
	}
	for i := range sim.PullRequests {
		pr := &sim.PullRequests[i]
		if pr.State != "open" {
			continue
		}
		baseRef := "refs/heads/" + pr.Base
		headRef := "refs/heads/" + pr.Head
		if pr.HeadRepo != "" && !strings.EqualFold(pr.HeadRepo, sim.Owner+"/"+sim.Repo) {
			forkOwner, _, _ := strings.Cut(pr.HeadRepo, "/")
			if fork, ok := sim.Forks[pr.HeadRepo]; ok && fork != nil {
				remoteRef := "refs/remotes/fork/" + forkOwner + "/" + pr.Head
				_ = exec.Command("git", "--git-dir", sim.BareRepo, "fetch", "--quiet",
					"--end-of-options", fork.BareRepo, "+refs/heads/"+pr.Head+":"+remoteRef).Run()
				headRef = remoteRef
			}
		}
		err := exec.Command("git", "--git-dir", sim.BareRepo, "merge-tree", "--write-tree", baseRef, headRef).Run()
		clean := err == nil
		pr.Mergeable = &clean
	}
}

func (t *realGitMergeableTransport) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if method == "PUT" && t.failMerge502ForPR > 0 && strings.HasSuffix(path, fmt.Sprintf("/pulls/%d/merge", t.failMerge502ForPR)) {
		return github.ApiResponse{Status: 502, Body: []byte(`{"message":"Bad Gateway"}`)}, nil
	}
	for key, sim := range t.sims {
		if strings.Contains(strings.ToLower(path), "/repos/"+strings.ToLower(key)) {
			t.updateMergeableLocked(sim)
			return sim.Request(method, path, headers, body)
		}
	}
	// Fallback to first sim if only one.
	for _, sim := range t.sims {
		t.updateMergeableLocked(sim)
		return sim.Request(method, path, headers, body)
	}
	return github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, nil
}

func openTempStore(ctx context.Context, dir string) (*model.Store, *sql.DB, error) {
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		return nil, nil, err
	}
	store := model.New(db)
	store.SingleTenant = true
	if err := store.Init(ctx); err != nil {
		db.Close()
		return nil, nil, err
	}
	return store, db, nil
}

// ============================================================================
// ITEM 1: git_ref schedule triggers on a REAL `grain daemon` subprocess
// ============================================================================

type item1Record struct {
	Commits          map[string]string `json:"commits"`
	Steps            []map[string]any  `json:"steps"`
	Refusals         map[string]any    `json:"refusals"`
	ScheduleWireJSON map[string]any    `json:"scheduleWireJson"`
	LsRemoteCounts   map[string]int64  `json:"lsRemoteCounts"`
	DaemonLogExcerpt []string          `json:"daemonLogExcerpt"`
}

func runItem1(ctx context.Context, outDir, grainBin string) error {
	log.Printf("=== ITEM 1: git_ref schedule triggers on real grain daemon ===")
	tmp, err := os.MkdirTemp("", "walk-item1-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	gitRoot := filepath.Join(tmp, "git")
	_, upstreamSeed, commitA := initBareWithMain(gitRoot, "upstream", "core", map[string]string{"VERSION": "1.0.0\n"})
	initBareWithMain(gitRoot, "acme", "edition", map[string]string{"README.md": "edition\n"})
	initBareWithMain(gitRoot, "acme", "secrets-backup", map[string]string{"README.md": "secrets\n"})
	initBareWithMain(gitRoot, "offlist", "external", map[string]string{"README.md": "external\n"})

	// Annotated tag refs/tags/latest -> commitA, plus decoy branch x/refs/tags/latest.
	gitCmd(upstreamSeed, "tag", "-a", "-m", "release 1.0.0", "latest")
	if err := os.WriteFile(filepath.Join(upstreamSeed, "DECOY"), []byte("decoy\n"), 0o644); err != nil {
		return err
	}
	gitCmd(upstreamSeed, "add", "DECOY")
	gitCmd(upstreamSeed, "commit", "-q", "-m", "decoy commit")
	gitCmd(upstreamSeed, "branch", "x/refs/tags/latest")
	gitCmd(upstreamSeed, "reset", "-q", "--hard", commitA)
	gitCmd(upstreamSeed, "push", "-q", "origin", "refs/tags/latest", "x/refs/tags/latest")

	moveTag := func(version string) string {
		if err := os.WriteFile(filepath.Join(upstreamSeed, "VERSION"), []byte(version+"\n"), 0o644); err != nil {
			panic(err)
		}
		gitCmd(upstreamSeed, "add", "VERSION")
		gitCmd(upstreamSeed, "commit", "-q", "-m", "release "+version)
		sha := gitCmd(upstreamSeed, "rev-parse", "HEAD")
		gitCmd(upstreamSeed, "tag", "-f", "-a", "-m", "release "+version, "latest")
		gitCmd(upstreamSeed, "push", "-q", "-f", "origin", "main", "refs/tags/latest")
		return sha
	}

	gitCoreOut, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		return fmt.Errorf("git --exec-path: %w", err)
	}
	gitHTTPBackend := filepath.Join(strings.TrimSpace(string(gitCoreOut)), "git-http-backend")

	var mu sync.Mutex
	lsCounts := map[string]int64{}

	gitSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v3/") {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case strings.HasSuffix(r.URL.Path, "/pulls"), strings.HasSuffix(r.URL.Path, "/issues"), strings.HasSuffix(r.URL.Path, "/runs"):
				_, _ = w.Write([]byte(`[]`))
			case strings.Contains(r.URL.Path, "/actions/runs"):
				_, _ = w.Write([]byte(`{"total_count":0,"workflow_runs":[]}`))
			case strings.Contains(r.URL.Path, "/check-runs"):
				_, _ = w.Write([]byte(`{"total_count":0,"check_runs":[]}`))
			default:
				_, _ = w.Write([]byte(`{"default_branch":"main","full_name":"acme/edition"}`))
			}
			return
		}
		if strings.HasSuffix(r.URL.Path, "/info/refs") {
			repoPath := strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/info/refs"), "/")
			repoPath = strings.TrimSuffix(repoPath, ".git")
			mu.Lock()
			lsCounts[repoPath]++
			mu.Unlock()
		}
		(&cgi.Handler{
			Path: gitHTTPBackend,
			Env: []string{
				"GIT_PROJECT_ROOT=" + gitRoot,
				"GIT_HTTP_EXPORT_ALL=1",
			},
		}).ServeHTTP(w, r)
	}))
	defer gitSrv.Close()
	gitHost := strings.TrimPrefix(gitSrv.URL, "http://")

	dataDir := filepath.Join(tmp, "data")
	sandboxDir := filepath.Join(tmp, "sandboxes")
	_ = os.MkdirAll(dataDir, 0o755)
	_ = os.MkdirAll(sandboxDir, 0o755)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	uiAddr := ln.Addr().String()
	_ = ln.Close()
	baseURL := "http://" + uiAddr

	var logBuf bytes.Buffer
	var logMu sync.Mutex
	logWriter := writerFunc(func(p []byte) (int, error) {
		logMu.Lock()
		defer logMu.Unlock()
		return logBuf.Write(p)
	})

	startDaemon := func() (stopFn func() error, code string) {
		cmd := exec.Command(grainBin, "daemon",
			"-data-dir", dataDir,
			"-sandbox-dir", sandboxDir,
			"-sandbox-port-base=0",
			"-disable-native-sandboxes",
			"-ci=false",
			"-ui-addr", uiAddr,
			"-ui-open=false",
			"-github-host", gitHost,
			"-github-insecure-http",
			"-poll-interval", "1s",
			"-repo", "acme/edition",
			"-repo", "upstream/core",
			"-repo", "acme/secrets-backup",
		)
		cmd.Stdout = logWriter
		cmd.Stderr = logWriter
		if err := cmd.Start(); err != nil {
			panic(fmt.Sprintf("starting grain daemon: %v", err))
		}
		codePath := filepath.Join(dataDir, "local-api-token")
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if raw, err := os.ReadFile(codePath); err == nil && strings.TrimSpace(string(raw)) != "" {
				cand := strings.TrimSpace(string(raw))
				req, _ := http.NewRequest("GET", baseURL+"/api/settings", nil)
				req.Header.Set("Authorization", "Bearer "+cand)
				if resp, err := http.DefaultClient.Do(req); err == nil {
					_ = resp.Body.Close()
					if resp.StatusCode == 200 {
						code = cand
						break
					}
				}
			}
			time.Sleep(150 * time.Millisecond)
		}
		if code == "" {
			logMu.Lock()
			logs := logBuf.String()
			logMu.Unlock()
			_ = cmd.Process.Kill()
			panic("daemon did not become ready; logs:\n" + logs)
		}
		return func() error {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
				return nil
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				return <-done
			}
		}, code
	}

	apiCall := func(code, method, path string, body any) (int, []byte) {
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, baseURL+path, rdr)
		if err != nil {
			panic(err)
		}
		req.Header.Set("Authorization", "Bearer "+code)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}

	readCursorFromSQLite := func(schedID string) string {
		db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(dataDir, "store")))
		if err != nil {
			return "err:" + err.Error()
		}
		defer db.Close()
		store := model.New(db)
		cur, err := store.GetScheduleRefCursor(ctx, schedID)
		if err != nil || cur == nil {
			return ""
		}
		return cur.Commit
	}

	scheduledTasks := func(_ string, schedID string) []map[string]any {
		db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(dataDir, "store")))
		if err != nil {
			panic(fmt.Sprintf("opening sqlite store: %v", err))
		}
		defer db.Close()
		store := model.New(db)
		all, err := store.ListTasks(ctx)
		if err != nil {
			panic(fmt.Sprintf("ListTasks: %v", err))
		}
		wantTag := model.ScheduleFiringTag(schedID)
		var matched []map[string]any
		for _, t := range all {
			for _, tg := range t.Tags {
				if tg == wantTag {
					matched = append(matched, map[string]any{
						"id":    t.ID,
						"title": t.Title,
						"tags":  t.Tags,
					})
					break
				}
			}
		}
		return matched
	}

	rec := item1Record{
		Commits:  map[string]string{"A": commitA},
		Refusals: map[string]any{},
	}

	// Configure secrets repo in dataDir before starting daemon so acme/secrets-backup is in ForbiddenRepos.
	if err := secretsrepo.SaveSettings(dataDir, secretsrepo.Settings{
		Remote: gitSrv.URL + "/acme/secrets-backup.git",
	}); err != nil {
		return fmt.Errorf("saving secretsrepo settings: %w", err)
	}

	stopDaemon, code := startDaemon()
	defer func() {
		if stopDaemon != nil {
			_ = stopDaemon()
		}
	}()

	st, raw := apiCall(code, "PUT", "/api/github-tokens/default", map[string]any{
		"value": "local-walkthrough-token",
	})
	if st != 200 && st != 201 && st != 204 {
		return fmt.Errorf("PUT /api/github-tokens/default status %d: %s", st, raw)
	}
	for _, r := range []string{"acme/edition", "upstream/core", "acme/secrets-backup"} {
		st, raw = apiCall(code, "POST", "/api/repos", map[string]any{"repo": r})
		if st != 200 && st != 201 {
			return fmt.Errorf("POST /api/repos (%s) status %d: %s", r, st, raw)
		}
	}

	// Refusal 1: Repository-declared git_ref schedule watching grain/config (repodecl).
	_, declErr := repodecl.Load(fstest.MapFS{
		".grain/templates/sync.yml": &fstest.MapFile{Data: []byte("name: sync\ntitle: Sync\nbody: Sync.\n")},
		".grain/schedules/sync.yml": &fstest.MapFile{Data: []byte("template: sync\ntrigger:\n  git_ref:\n    repo: grain/config\n    ref: refs/heads/main\n")},
	}, model.RepoRef{Owner: "acme", Name: "edition"})
	if declErr == nil {
		return fmt.Errorf("expected repodecl refusal for grain/config git_ref trigger")
	}
	rec.Refusals["declared_grain_config"] = declErr.Error()
	log.Printf("Refusal (declared grain/config): %s", declErr)

	// Refusal 2: API validation for non-full ref name.
	badRefStatus, badRefBody := apiCall(code, "POST", "/api/schedules", map[string]any{
		"title":       "Bad ref",
		"description": "Watch short ref",
		"repo":        "acme/edition",
		"recurrence": map[string]any{
			"kind":   "git_ref",
			"gitRef": map[string]any{"repo": "upstream/core", "ref": "latest"},
		},
	})
	rec.Refusals["short_ref_api"] = map[string]any{"status": badRefStatus, "body": strings.TrimSpace(string(badRefBody))}

	// Refusal 3: Schedule watching forbidden repo (acme/secrets-backup, the deployment's -secrets-repo)
	// and schedule watching off-target repo (offlist/external).
	forbStatus, forbBody := apiCall(code, "POST", "/api/schedules", map[string]any{
		"title":       "Watch forbidden secrets repo",
		"description": "Attempt to watch secrets-backup",
		"repo":        "acme/edition",
		"recurrence": map[string]any{
			"kind":   "git_ref",
			"gitRef": map[string]any{"repo": "acme/secrets-backup", "ref": "refs/heads/main"},
		},
	})
	offStatus, offBody := apiCall(code, "POST", "/api/schedules", map[string]any{
		"title":       "Watch off-target repo",
		"description": "Attempt to watch offlist/external",
		"repo":        "acme/edition",
		"recurrence": map[string]any{
			"kind":   "git_ref",
			"gitRef": map[string]any{"repo": "offlist/external", "ref": "refs/heads/main"},
		},
	})
	rec.Refusals["create_watch_forbidden_secrets_repo"] = map[string]any{"status": forbStatus, "body": strings.TrimSpace(string(forbBody))}
	rec.Refusals["create_watch_off_target_repo"] = map[string]any{"status": offStatus, "body": strings.TrimSpace(string(offBody))}

	// Create the real schedule watching upstream/core refs/tags/latest (overlap: skip).
	createSt, createRaw := apiCall(code, "POST", "/api/schedules", map[string]any{
		"title":       "Sync upstream latest release",
		"description": "Bring acme/edition up to upstream/core's latest release.",
		"repo":        "acme/edition",
		"overlap":     "skip",
		"recurrence": map[string]any{
			"kind":   "git_ref",
			"gitRef": map[string]any{"repo": "upstream/core", "ref": "refs/tags/latest"},
		},
	})
	if createSt != 200 && createSt != 201 {
		return fmt.Errorf("POST /api/schedules status %d: %s", createSt, createRaw)
	}
	var createdSched map[string]any
	_ = json.Unmarshal(createRaw, &createdSched)
	rec.ScheduleWireJSON = createdSched
	schedID, _ := createdSched["id"].(string)
	log.Printf("Created schedule %s watching upstream/core refs/tags/latest", schedID)

	// Step 1: First look records cursor at commitA and fires 0 tasks.
	time.Sleep(3 * time.Second)
	cursor1 := readCursorFromSQLite(schedID)
	tasks1 := scheduledTasks(code, schedID)
	log.Printf("Step 1 (first look): cursor=%s (want %s), tasks=%d (want 0)", cursor1, commitA, len(tasks1))
	if cursor1 != commitA || len(tasks1) != 0 {
		logMu.Lock()
		logs := logBuf.String()
		logMu.Unlock()
		return fmt.Errorf("step 1 mismatch: cursor=%s want %s, tasks=%d\nlogs:\n%s", cursor1, commitA, len(tasks1), logs)
	}
	rec.Steps = append(rec.Steps, map[string]any{
		"step": "1_first_look", "cursor": cursor1, "tasksFiled": len(tasks1),
	})

	logMu.Lock()
	earlyLogs := logBuf.String()
	logMu.Unlock()
	rec.Refusals["daemon_log_forbidden_check"] = extractMatchingLines(earlyLogs, "secrets-backup", "offlist/external")
	log.Printf("Daemon log refusals: %v", rec.Refusals["daemon_log_forbidden_check"])

	// Step 2: Move refs/tags/latest to commitB; wait out the real 2m poll interval -> fires once!
	commitB := moveTag("1.1.0")
	rec.Commits["B"] = commitB
	log.Printf("Step 2: moved refs/tags/latest to %s; waiting for daemon's 2m git_ref poll...", commitB)
	deadline := time.Now().Add(135 * time.Second)
	var tasks2 []map[string]any
	for time.Now().Before(deadline) {
		tasks2 = scheduledTasks(code, schedID)
		if len(tasks2) > 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	time.Sleep(4 * time.Second)
	tasks2 = scheduledTasks(code, schedID)
	cursor2 := readCursorFromSQLite(schedID)
	if len(tasks2) != 1 || cursor2 != commitB {
		return fmt.Errorf("step 2 mismatch: tasks=%d (want 1), cursor=%s (want %s)", len(tasks2), cursor2, commitB)
	}
	firstTaskID := fmt.Sprint(tasks2[0]["id"])
	_, commentsRaw := apiCall(code, "GET", "/api/tasks/"+firstTaskID+"/comments", nil)
	log.Printf("Step 2 complete: task #%s filed once, cursor=%s", firstTaskID, cursor2)
	rec.Steps = append(rec.Steps, map[string]any{
		"step": "2_move_fires_once", "cursor": cursor2, "tasksFiled": len(tasks2),
		"firstTaskId": firstTaskID, "comments": json.RawMessage(commentsRaw),
	})

	// Step 3: Move refs/tags/latest to commitC while task #1 is still open -> held; then close #1 -> fires!
	commitC := moveTag("1.2.0")
	rec.Commits["C"] = commitC
	log.Printf("Step 3a: moved refs/tags/latest to %s while task #%s is open; waiting 125s for next poll...", commitC, firstTaskID)
	time.Sleep(125 * time.Second)
	tasks3Held := scheduledTasks(code, schedID)
	cursor3Held := readCursorFromSQLite(schedID)
	log.Printf("Step 3a (while open): tasks=%d (want 1), cursor=%s (want still %s)", len(tasks3Held), cursor3Held, commitB)
	if len(tasks3Held) != 1 || cursor3Held != commitB {
		return fmt.Errorf("step 3a mismatch: tasks=%d (want 1), cursor=%s (want %s)", len(tasks3Held), cursor3Held, commitB)
	}

	closeSt, closeBody := apiCall(code, "POST", "/api/tasks/"+firstTaskID+"/close", map[string]any{"reason": "done syncing 1.1.0"})
	if closeSt != 200 {
		return fmt.Errorf("closing task %s: %d %s", firstTaskID, closeSt, closeBody)
	}
	log.Printf("Step 3b: closed task #%s; waiting for next poll to fire held move...", firstTaskID)
	deadline = time.Now().Add(135 * time.Second)
	var tasks3After []map[string]any
	for time.Now().Before(deadline) {
		tasks3After = scheduledTasks(code, schedID)
		if len(tasks3After) >= 2 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	cursor3After := readCursorFromSQLite(schedID)
	if len(tasks3After) != 2 || cursor3After != commitC {
		return fmt.Errorf("step 3b mismatch: tasks=%d (want 2), cursor=%s (want %s)", len(tasks3After), cursor3After, commitC)
	}
	log.Printf("Step 3b complete: second task filed after close, cursor=%s", cursor3After)
	rec.Steps = append(rec.Steps, map[string]any{
		"step":           "3_held_while_open_then_fires_after_close",
		"whileOpenTasks": len(tasks3Held), "whileOpenCursor": cursor3Held,
		"afterCloseTasks": len(tasks3After), "afterCloseCursor": cursor3After,
	})

	for _, t := range tasks3After {
		id := fmt.Sprint(t["id"])
		if id != firstTaskID {
			apiCall(code, "POST", "/api/tasks/"+id+"/close", map[string]any{"reason": "done syncing 1.2.0"})
		}
	}

	// Step 4a: Restart grain daemon with tag unchanged at commitC -> neither re-fires!
	log.Printf("Step 4a: restarting grain daemon with tag still at %s...", commitC)
	if err := stopDaemon(); err != nil {
		return fmt.Errorf("stopping daemon (4a): %w", err)
	}
	stopDaemon = nil
	mu.Lock()
	lsBeforeRestart := lsCounts["upstream/core"]
	mu.Unlock()

	stopDaemon, code = startDaemon()
	time.Sleep(4 * time.Second)
	mu.Lock()
	lsAfterRestart := lsCounts["upstream/core"]
	mu.Unlock()
	tasks4a := scheduledTasks(code, schedID)
	cursor4a := readCursorFromSQLite(schedID)
	log.Printf("Step 4a (restart unchanged): ls-remote %d->%d, tasks=%d (want 2), cursor=%s",
		lsBeforeRestart, lsAfterRestart, len(tasks4a), cursor4a)
	if len(tasks4a) != 2 || cursor4a != commitC {
		return fmt.Errorf("step 4a mismatch: tasks=%d (want 2), cursor=%s", len(tasks4a), cursor4a)
	}

	// Step 4b: Stop daemon, move tag to commitD while daemon is down, start daemon -> nor misses!
	log.Printf("Step 4b: stopping daemon, moving tag while down, restarting...")
	if err := stopDaemon(); err != nil {
		return fmt.Errorf("stopping daemon (4b): %w", err)
	}
	stopDaemon = nil
	commitD := moveTag("1.3.0")
	rec.Commits["D"] = commitD

	stopDaemon, code = startDaemon()
	deadline = time.Now().Add(15 * time.Second)
	var tasks4b []map[string]any
	for time.Now().Before(deadline) {
		tasks4b = scheduledTasks(code, schedID)
		if len(tasks4b) >= 3 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	cursor4b := readCursorFromSQLite(schedID)
	if len(tasks4b) != 3 || cursor4b != commitD {
		return fmt.Errorf("step 4b mismatch: tasks=%d (want 3), cursor=%s (want %s)", len(tasks4b), cursor4b, commitD)
	}
	log.Printf("Step 4b complete: move while down fired immediately on restart (tasks=%d, cursor=%s)", len(tasks4b), cursor4b)
	rec.Steps = append(rec.Steps, map[string]any{
		"step":                    "4_restart_neither_refires_nor_misses",
		"afterUnchangedRestart":   len(tasks4a),
		"afterOfflineMoveRestart": len(tasks4b),
		"finalCursor":             cursor4b,
	})

	_ = stopDaemon()
	stopDaemon = nil

	mu.Lock()
	rec.LsRemoteCounts = map[string]int64{}
	for k, v := range lsCounts {
		rec.LsRemoteCounts[k] = v
	}
	mu.Unlock()
	logMu.Lock()
	rec.DaemonLogExcerpt = extractMatchingLines(logBuf.String(),
		"starts watching", "fired:", "previous firing is still open", "secrets-backup", "target repo list")
	logMu.Unlock()

	return writeArtifact(outDir, "item1-git-ref.json", rec)
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func extractMatchingLines(s string, needles ...string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		for _, n := range needles {
			if strings.Contains(line, n) {
				out = append(out, strings.TrimSpace(line))
				break
			}
		}
	}
	return out
}

// ============================================================================
// ITEM 2: Fork-to-upstream (`upstream_repo`) over githubsim + real gitproxy
// ============================================================================

type fnProvider struct {
	run func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error)
}

func (p fnProvider) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	return p.run(ctx, cfg)
}

func runItem2(ctx context.Context, outDir string) error {
	log.Printf("=== ITEM 2: Fork-to-upstream (upstream_repo) over githubsim + gitproxy ===")
	tmp, err := os.MkdirTemp("", "walk-item2-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	gitRoot := filepath.Join(tmp, "git")
	upstreamBare, _, initialSHA := initBareWithMain(gitRoot, "gke-labs", "kube-agents", map[string]string{
		"README.md": "kube-agents upstream\n",
	})
	forkBare := filepath.Join(gitRoot, "sackheads", "kube-agents.git")
	_ = os.MkdirAll(filepath.Dir(forkBare), 0o755)
	gitCmd(gitRoot, "clone", "-q", "--bare", upstreamBare, forkBare)
	gitCmd(forkBare, "config", "http.receivepack", "true")

	upstreamSim := githubsim.New("gke-labs", "kube-agents", upstreamBare, "main")
	forkSim := githubsim.New("sackheads", "kube-agents", forkBare, "main")
	upstreamSim.Forks = map[string]*githubsim.Sim{
		"sackheads/kube-agents": forkSim,
	}

	transport := &realGitMergeableTransport{
		sims: map[string]*githubsim.Sim{
			"gke-labs/kube-agents":  upstreamSim,
			"sackheads/kube-agents": forkSim,
		},
	}
	ghClient := github.NewClient(transport, nil)

	// Upstream git HTTP backend behind gitproxy.
	gitCoreOut, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		return fmt.Errorf("git --exec-path: %w", err)
	}
	gitHTTPBackendPath := filepath.Join(strings.TrimSpace(string(gitCoreOut)), "git-http-backend")
	gitBackendHandler := &cgi.Handler{
		Path: gitHTTPBackendPath,
		Env: []string{
			"GIT_PROJECT_ROOT=" + gitRoot,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}
	gitUpstreamSrv := httptest.NewServer(&githubsim.GitUpstream{
		Backend: gitBackendHandler,
		Tokens: map[string]githubsim.GitAccess{
			"upstream-bot-token": {
				Read:  []string{"gke-labs/kube-agents", "sackheads/kube-agents"},
				Write: []string{"gke-labs/kube-agents", "sackheads/kube-agents"},
			},
		},
	})
	defer gitUpstreamSrv.Close()
	forwardHost := strings.TrimPrefix(gitUpstreamSrv.URL, "http://")

	dataDir := filepath.Join(tmp, "data")
	_ = os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o700)
	_ = os.WriteFile(filepath.Join(dataDir, "secrets", "github", "credentials.json"), []byte(`{"*": "bot"}`), 0o600)
	_ = os.WriteFile(filepath.Join(dataDir, "secrets", "github", "bot.token"), []byte("upstream-bot-token"), 0o600)

	store, db, err := openTempStore(ctx, dataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.GitCredential = "bot"
		return nil
	}); err != nil {
		return err
	}

	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir:      dataDir,
		Store:        store,
		ForwardHost:  forwardHost,
		ForwardTLS:   false,
		SingleTenant: true,
	})
	if err != nil {
		return fmt.Errorf("BuildProxy: %w", err)
	}
	proxySrv := httptest.NewServer(gitproxy.NewHandler(proxy))
	defer proxySrv.Close()

	uiClient := ui.NewClient(ui.Config{
		Actor:        model.Principal{Kind: model.PrincipalHuman, ID: "operator"},
		Capabilities: ui.OfferedCapabilities(),
	}, store)

	// Set upstream_repo through ui.Client (same code path as PUT /api/repos/sackheads/kube-agents/settings).
	forkRef := model.RepoRef{Owner: "sackheads", Name: "kube-agents"}
	upstreamRef := model.RepoRef{Owner: "gke-labs", Name: "kube-agents"}
	upStr := upstreamRef.String()
	ciMode := string(model.CIModeGitHub)
	updatedRepoCfg, err := uiClient.SetRepoSettings(ctx, forkRef.String(), ui.SetRepoSettingsRequest{
		UpstreamRepo: &upStr,
		CIMode:       &ciMode,
	})
	if err != nil {
		return fmt.Errorf("SetRepoSettings(upstreamRepo): %w", err)
	}
	log.Printf("Configured %s -> upstreamRepo=%q", forkRef, updatedRepoCfg.UpstreamRepo)

	created, err := uiClient.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "feat: add fork-to-upstream controller loop",
		Description: "Add controller.go through the fork workflow.",
		Repo:        forkRef.String(),
		AutoMerge:   true,
	})
	if err != nil {
		return fmt.Errorf("CreateTask: %w", err)
	}
	if created.State == model.StateProposed {
		_ = uiClient.Approve(ctx, created.ID)
	}

	var resultLog = map[string]any{
		"initialUpstreamSHA": initialSHA,
		"repoSettings":       updatedRepoCfg,
	}

	sandboxRoot := filepath.Join(tmp, "sandboxes")
	_ = os.MkdirAll(sandboxRoot, 0o755)
	tokens := gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "secrets", "sandbox-tokens.json"))

	provider := fnProvider{run: func(ctx context.Context, rc agent.RunConfig) (*agent.Result, error) {
		resultLog["runConfigUpstreamRepo"] = rc.UpstreamRepo
		resultLog["promptMentionsFork"] = strings.Contains(rc.Prompt, "sackheads/kube-agents is a fork push repository for upstream gke-labs/kube-agents")

		sbxName := filepath.Base(rc.SandboxRoot)
		runs, _ := store.Runs(ctx, rc.TaskID)
		if len(runs) > 0 {
			sbxName = runs[len(runs)-1].Sandbox
		}
		tok, err := tokens.EnsureToken(sbxName)
		if err != nil {
			return nil, fmt.Errorf("EnsureToken(%s): %w", sbxName, err)
		}
		if err := mcp.ConfigureGitCredentials(rc.SandboxRoot, proxySrv.URL+"/placeholder/placeholder.git", tok, mcp.GitIdentity{}); err != nil {
			return nil, fmt.Errorf("ConfigureGitCredentials: %w", err)
		}
		sbxEnv := []string{"HOME=" + rc.SandboxRoot}
		forkURL := proxySrv.URL + "/sackheads/kube-agents.git"
		upstreamURL := proxySrv.URL + "/gke-labs/kube-agents.git"

		work := filepath.Join(rc.SandboxRoot, "work")
		if out, err := runGitAllowErr(rc.SandboxRoot, sbxEnv, "clone", "-q", forkURL, work); err != nil {
			return nil, fmt.Errorf("clone fork via proxy: %v (%s)", err, out)
		}
		if out, err := runGitAllowErr(work, sbxEnv, "remote", "add", "upstream", upstreamURL); err != nil {
			return nil, fmt.Errorf("remote add upstream: %v (%s)", err, out)
		}
		// 1. Verify read access to upstream works through gitproxy.
		fetchOut, fetchErr := runGitAllowErr(work, sbxEnv, "fetch", "upstream", "main")
		if fetchErr != nil {
			return nil, fmt.Errorf("expected fetch upstream to succeed, got: %v (%s)", fetchErr, fetchOut)
		}
		resultLog["fetchUpstreamSucceeded"] = true

		// 2. Create commit on task branch.
		tObj, _ := store.GetTask(ctx, rc.TaskID)
		branch := tObj.BranchName()
		gitCmd(work, "checkout", "-q", "-b", branch)
		_ = os.WriteFile(filepath.Join(work, "controller.go"), []byte("package main\n\nfunc RunController() {}\n"), 0o644)
		gitCmd(work, "add", "controller.go")
		gitCmd(work, "commit", "-q", "-m", "feat: add controller loop\n\nAdds RunController via fork workflow.")

		// 3. Attempt direct push to upstream -> MUST BE REFUSED BY GITPROXY!
		pushUpOut, pushUpErr := runGitAllowErr(work, sbxEnv, "push", "upstream", "HEAD:refs/heads/"+branch)
		if pushUpErr == nil {
			return nil, fmt.Errorf("SECURITY BUG: direct push to upstream succeeded through gitproxy!")
		}
		resultLog["pushToUpstreamRefusal"] = pushUpOut
		log.Printf("Direct push to upstream refused as expected: %s", pushUpOut)

		// 4. Push to origin (the fork) -> MUST SUCCEED!
		pushForkOut, pushForkErr := runGitAllowErr(work, sbxEnv, "push", "origin", "HEAD:refs/heads/"+branch)
		if pushForkErr != nil {
			return nil, fmt.Errorf("push to fork failed: %v (%s)", pushForkErr, pushForkOut)
		}
		pushedSHA := gitCmd(work, "rev-parse", "HEAD")
		resultLog["pushToForkSucceeded"] = true
		resultLog["pushedBranch"] = branch
		resultLog["headSHA"] = pushedSHA

		// Upstream CI starts in_progress on push so Cycle 1 opens PR #9000 and holds it in the merge queue.
		transport.mu.Lock()
		upstreamSim.CheckRuns[pushedSHA] = []github.CheckRun{
			{ID: 500, Name: "Tests / unit", Status: "in_progress"},
		}
		transport.mu.Unlock()

		return &agent.Result{
			FinalText:  "Implemented controller loop and pushed to fork.",
			Transcript: "pushed branch to fork",
		}, nil
	}}

	registry := model.NewCapabilityRegistry(durable.New())
	deps := orchestrator.Deps{
		Store:      store,
		Client:     ghClient,
		Sandboxes:  orchestrator.NewHostSandboxes(sandboxRoot),
		Framework:  orchestrator.StaticFramework(provider),
		Config:     orchestrator.Config{Capabilities: registry},
		MaxWorkers: 1,
	}

	now := time.Date(2026, 11, 4, 14, 0, 0, 0, time.UTC)
	// Cycle 1: dispatches task, pushes to fork, finishes and opens PR #9000 on upstream (held in queue while CI is in_progress)!
	if err := orchestrator.RunCycle(ctx, deps, now); err != nil {
		return fmt.Errorf("Item 2 Cycle 1: %w", err)
	}

	taskAfterC1, _ := store.GetTask(ctx, created.ID)
	prRef := model.TaskPullRequest(*taskAfterC1)
	if prRef == nil || !prRef.Repo.Same(upstreamRef) {
		return fmt.Errorf("expected PR on %s, got %+v", upstreamRef, prRef)
	}
	log.Printf("Cycle 1 opened PR %s#%d (state=%q, headRepo=%q, head=%q)",
		prRef.Repo, prRef.Number, upstreamSim.PullRequests[0].State, upstreamSim.PullRequests[0].HeadRepo, upstreamSim.PullRequests[0].Head)
	resultLog["cycle1PR"] = map[string]any{
		"repo":     prRef.Repo.String(),
		"number":   prRef.Number,
		"state":    upstreamSim.PullRequests[0].State,
		"head":     upstreamSim.PullRequests[0].Head,
		"headRepo": upstreamSim.PullRequests[0].HeadRepo,
	}

	// Test pull_request_status MCP tool reading upstream PR + upstream CI + upstream Actions job log!
	branch := taskAfterC1.BranchName()
	headSHA := fmt.Sprint(resultLog["headSHA"])
	transport.mu.Lock()
	upstreamSim.CheckRuns[headSHA] = []github.CheckRun{
		{ID: 501, Name: "Tests / unit", Status: "completed", Conclusion: strPtr("failure"), HTMLURL: "https://example.test/runs/501"},
	}
	upstreamSim.AddWorkflowRun(githubsim.WorkflowRun{
		ID:         5010,
		Name:       "Tests",
		HeadBranch: branch,
		HeadSHA:    headSHA,
		HeadRepo:   forkRef.String(),
		Event:      "pull_request",
		Status:     "completed",
		Conclusion: "failure",
		Jobs: []githubsim.WorkflowJob{
			{Name: "Tests / unit", Conclusion: "failure", Log: "=== RUN   TestController\n    controller_test.go:19: expected ready\n--- FAIL: TestController\n"},
		},
	})
	transport.mu.Unlock()

	prTools := mcp.NewPullRequestTools(ghClient, nil, mcp.PullRequestScope{
		Owner:         forkRef.Owner,
		Repo:          forkRef.Name,
		Branch:        branch,
		UpstreamOwner: upstreamRef.Owner,
		UpstreamRepo:  upstreamRef.Name,
	})
	var prStatusTool mcp.Tool
	for _, tl := range prTools {
		if tl.Name == "pull_request_status" {
			prStatusTool = tl
			break
		}
	}
	redRes := prStatusTool.Handler(ctx, map[string]any{})
	resultLog["pullRequestStatusRed"] = redRes.Text
	log.Printf("pull_request_status (red upstream check):\n%s", redRes.Text)
	if !strings.Contains(redRes.Text, "gke-labs/kube-agents/pull/") || !strings.Contains(redRes.Text, "Tests / unit") {
		return fmt.Errorf("pull_request_status did not report upstream PR and check:\n%s", redRes.Text)
	}

	// Turn upstream CI green!
	transport.mu.Lock()
	upstreamSim.WorkflowRuns = nil
	upstreamSim.CheckRuns[headSHA] = []github.CheckRun{
		{ID: 502, Name: "Tests / unit", Status: "completed", Conclusion: strPtr("success")},
	}
	transport.mu.Unlock()
	greenRes := prStatusTool.Handler(ctx, map[string]any{})
	resultLog["pullRequestStatusGreen"] = greenRes.Text

	// Also advance upstream/main with a non-conflicting commit BEFORE merge queue runs,
	// AND test with requireBranchUpToDate=true to see if fork refresh works or breaks!
	// First, test standard merge queue landing the fork PR into upstream!
	now = now.Add(30 * time.Second)
	if err := orchestrator.RunCycle(ctx, deps, now); err != nil {
		return fmt.Errorf("Item 2 Cycle 2 (merge queue): %w", err)
	}
	stAfterC2, _ := store.State(ctx, created.ID)
	obsAfterC2, _ := store.GetObservation(ctx, created.ID)
	upstreamMainAfter := gitCmd(upstreamBare, "rev-parse", "refs/heads/main")
	log.Printf("Cycle 2: task state=%s, prMergedAt=%v, upstream/main=%s (initial=%s)",
		stAfterC2, obsAfterC2.PrMergedAt, upstreamMainAfter, initialSHA)
	if stAfterC2 != model.StateClosed || obsAfterC2.PrMergedAt == nil || upstreamMainAfter == initialSHA {
		return fmt.Errorf("expected fork PR to merge and close task; state=%s, prMergedAt=%v", stAfterC2, obsAfterC2.PrMergedAt)
	}
	resultLog["cycle2Merged"] = map[string]any{
		"taskState":       stAfterC2,
		"prMergedAt":      obsAfterC2.PrMergedAt,
		"upstreamMainSHA": upstreamMainAfter,
	}

	// Bonus edge check: Fork PR when upstream/main has RequireBranchUpToDate=true and moved ahead!
	reqUpToDate := true
	_, _ = uiClient.SetRepoSettings(ctx, upstreamRef.String(), ui.SetRepoSettingsRequest{
		RequireBranchUpToDate: &reqUpToDate,
	})
	_, _ = uiClient.SetRepoSettings(ctx, forkRef.String(), ui.SetRepoSettingsRequest{
		RequireBranchUpToDate: &reqUpToDate,
	})
	task2, err := uiClient.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "feat: second fork change behind upstream main",
		Description: "Test merge queue branch refresh on a fork PR.",
		Repo:        forkRef.String(),
		AutoMerge:   true,
	})
	if err != nil {
		return err
	}
	if task2.State == model.StateProposed {
		_ = uiClient.Approve(ctx, task2.ID)
	}
	branch2 := "grain/fork-task-2"
	t2Obj, _ := store.GetTask(ctx, task2.ID)
	branch2 = t2Obj.BranchName()
	sha2 := pushBranchFiles(forkBare, "main", branch2, map[string]string{"second.go": "package main\n"}, "feat: second change")
	// Advance upstream/main with an unrelated file so branch2 is behind upstream/main!
	pushBranchFiles(upstreamBare, "main", "main", map[string]string{"UPSTREAM_DOC.md": "moved ahead\n"}, "docs: upstream advance")
	// Mark task2 completed with open PR on upstream.
	doneAt := now.Add(time.Minute)
	_ = store.Observe(ctx, model.Observation{TaskID: task2.ID, CompletedAt: &doneAt})
	if _, err := orchestrator.OpenPullRequestForTask(ctx, store, ghClient, *t2Obj); err != nil {
		return fmt.Errorf("OpenPullRequestForTask(task2): %w", err)
	}
	transport.mu.Lock()
	upstreamSim.CheckRuns[sha2] = []github.CheckRun{{ID: 601, Name: "Tests / unit", Status: "completed", Conclusion: strPtr("success")}}
	transport.mu.Unlock()

	errBehind := orchestrator.SyncPullRequests(ctx, store, ghClient, doneAt.Add(10*time.Second))
	t2State, _ := store.State(ctx, task2.ID)
	t2Comments, _ := store.Comments(ctx, task2.ID)
	resultLog["behindUpstreamWithRequireUpToDate"] = map[string]any{
		"syncErr":       fmt.Sprint(errBehind),
		"task2State":    t2State,
		"commentsCount": len(t2Comments),
	}
	log.Printf("Item 2 behind-upstream check: syncErr=%v, task2State=%s, comments=%d", errBehind, t2State, len(t2Comments))

	// Also verify staterepo export includes upstream_repo!
	exportDir := filepath.Join(tmp, "state-export")
	if err := staterepo.Export(ctx, db, exportDir); err != nil {
		return fmt.Errorf("staterepo.Export: %w", err)
	}
	repoCfgDump, _ := os.ReadFile(filepath.Join(exportDir, "repos", "sackheads", "kube-agents.yml"))
	if len(repoCfgDump) == 0 {
		// Try tables/repo_config.json if flat dump format
		repoCfgDump, _ = os.ReadFile(filepath.Join(exportDir, "tables", "repo_config.json"))
	}
	resultLog["stateExportContainsUpstreamRepo"] = strings.Contains(string(repoCfgDump), "gke-labs/kube-agents")

	return writeArtifact(outDir, "item2-fork-upstream.json", resultLog)
}

// ============================================================================
// ITEM 3: Five merges per lane on real git content, conflict after 3rd, store=1
// ============================================================================

func queueCompletedPR(ctx context.Context, store *model.Store, ghClient github.Client, sim *githubsim.Sim,
	repo model.RepoRef, id, title string, files map[string]string, now time.Time) (model.Task, string, int, error) {
	task := model.Task{
		ID:        id,
		Title:     title,
		Body:      title,
		Target:    &repo,
		AutoMerge: true,
		CreatedAt: &now,
		Origin:    model.Origin{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}}},
		Binding:   model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		return model.Task{}, "", 0, err
	}
	branch := task.BranchName()
	sha := pushBranchFiles(sim.BareRepo, "main", branch, files, title)
	sim.CheckRuns[sha] = []github.CheckRun{{ID: 1, Name: "Tests", Status: "completed", Conclusion: strPtr("success")}}
	sim.CheckRuns[branch] = sim.CheckRuns[sha]
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &now}); err != nil {
		return model.Task{}, "", 0, err
	}
	st, err := orchestrator.OpenPullRequestForTask(ctx, store, ghClient, task)
	if err != nil {
		return model.Task{}, "", 0, err
	}
	got, _ := store.GetTask(ctx, task.ID)
	return *got, branch, st.PullRequest.Number, nil
}

func runItem3(ctx context.Context, outDir string) error {
	log.Printf("=== ITEM 3: Five merges per lane on real git content ===")
	tmp, err := os.MkdirTemp("", "walk-item3-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	out := map[string]any{}
	now := time.Date(2026, 11, 4, 15, 0, 0, 0, time.UTC)

	// --- Scenario 3a: 6 clean PRs on real git content -> 5 merge in Cycle 1, 1 in Cycle 2 ---
	{
		dir3a := filepath.Join(tmp, "3a")
		bare, _, _ := initBareWithMain(filepath.Join(dir3a, "git"), "acme", "widgets", map[string]string{"README.md": "base\n"})
		sim := githubsim.New("acme", "widgets", bare, "main")
		tr := &realGitMergeableTransport{sims: map[string]*githubsim.Sim{"acme/widgets": sim}}
		ghClient := github.NewClient(tr, nil)
		store, db, err := openTempStore(ctx, filepath.Join(dir3a, "db"))
		if err != nil {
			return err
		}
		defer db.Close()
		cfg, _ := store.GetConfig(ctx)
		maxPerRepo := model.DefaultMaxMergesPerRepo
		if cfg != nil && cfg.MaxMergesPerRepo > 0 {
			maxPerRepo = cfg.MaxMergesPerRepo
		}
		out["defaultMaxMergesPerRepo"] = maxPerRepo

		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		var ids []string
		for i := 1; i <= 6; i++ {
			id := fmt.Sprintf("30%d", i)
			ids = append(ids, id)
			fname := fmt.Sprintf("clean_%d.txt", i)
			if _, _, _, err := queueCompletedPR(ctx, store, ghClient, sim, repo, id,
				fmt.Sprintf("feat: add %s", fname), map[string]string{fname: fmt.Sprintf("content %d\n", i)},
				now.Add(time.Duration(i)*time.Second)); err != nil {
				return err
			}
		}

		// Run Cycle 1 (using RunCycle with real store + client -> runs SyncPullRequests with config.MaxMergesPerRepo == 5).
		deps := orchestrator.Deps{Store: store, Client: ghClient}
		if err := orchestrator.RunCycle(ctx, deps, now.Add(time.Minute)); err != nil {
			return fmt.Errorf("3a Cycle 1: %w", err)
		}
		c1Merged := 0
		c1States := map[string]string{}
		for _, id := range ids {
			st, _ := store.State(ctx, id)
			c1States[id] = string(st)
			if st == model.StateClosed {
				c1Merged++
			}
		}
		log.Printf("3a Cycle 1: merged=%d/6 (want 5), states=%v", c1Merged, c1States)
		if c1Merged != 5 {
			return fmt.Errorf("3a Cycle 1 merged %d PRs, want 5", c1Merged)
		}

		if err := orchestrator.RunCycle(ctx, deps, now.Add(2*time.Minute)); err != nil {
			return fmt.Errorf("3a Cycle 2: %w", err)
		}
		c2Merged := 0
		for _, id := range ids {
			if st, _ := store.State(ctx, id); st == model.StateClosed {
				c2Merged++
			}
		}
		log.Printf("3a Cycle 2: merged=%d/6 (want 6)", c2Merged)
		if c2Merged != 6 {
			return fmt.Errorf("3a Cycle 2 merged %d PRs, want 6", c2Merged)
		}
		out["scenario3a_sixCleanPRs"] = map[string]any{
			"cycle1Merged": c1Merged,
			"cycle1States": c1States,
			"cycle2Merged": c2Merged,
		}
	}

	// --- Scenario 3b: Conflict after the 3rd on real git content! ---
	// Let's test BOTH:
	//   (i) freezeMergeable=true (GitHub's cached mergeable=true before the 3rd merge -> PUT /pulls/4/merge fails 405 on real git conflict!)
	//   (ii) freezeMergeable=false (GitHub's re-read after the 3rd merge immediately reports mergeable=false on PR #4!)
	for _, sub := range []struct {
		name   string
		freeze bool
	}{
		{"putMerge405ConflictAfterThird", true},
		{"reReadMergeableFalseAfterThird", false},
	} {
		dir3b := filepath.Join(tmp, "3b-"+sub.name)
		bare, _, _ := initBareWithMain(filepath.Join(dir3b, "git"), "acme", "conflict-lane", map[string]string{
			"shared.txt": "line 1: original\n",
		})
		sim := githubsim.New("acme", "conflict-lane", bare, "main")
		tr := &realGitMergeableTransport{sims: map[string]*githubsim.Sim{"acme/conflict-lane": sim}}
		ghClient := github.NewClient(tr, nil)
		store, db, err := openTempStore(ctx, filepath.Join(dir3b, "db"))
		if err != nil {
			return err
		}
		defer db.Close()
		repo := model.RepoRef{Owner: "acme", Name: "conflict-lane"}

		specs := []struct {
			id    string
			files map[string]string
		}{
			{"401", map[string]string{"clean1.txt": "one\n"}},
			{"402", map[string]string{"clean2.txt": "two\n"}},
			{"403", map[string]string{"shared.txt": "line 1: modified by PR 3\n"}},
			{"404", map[string]string{"shared.txt": "line 1: conflicting edit from PR 4\n"}},
			{"405", map[string]string{"clean5.txt": "five\n"}},
		}
		for idx, sp := range specs {
			if _, _, _, err := queueCompletedPR(ctx, store, ghClient, sim, repo, sp.id,
				"change "+sp.id, sp.files, now.Add(time.Duration(idx)*time.Second)); err != nil {
				return err
			}
		}
		if sub.freeze {
			// Prime Mergeable=true against initial main, then freeze so PUT /pulls/4/merge hits real git conflict!
			tr.mu.Lock()
			tr.updateMergeableLocked(sim)
			tr.freezeMergeable = true
			tr.mu.Unlock()
		}

		deps := orchestrator.Deps{Store: store, Client: ghClient}
		c1Err := orchestrator.RunCycle(ctx, deps, now.Add(time.Minute))
		states := map[string]string{}
		prMerged := map[string]bool{}
		for idx, sp := range specs {
			st, _ := store.State(ctx, sp.id)
			states[sp.id] = string(st)
			prMerged[sp.id] = sim.PullRequests[idx].Merged
		}
		log.Printf("3b (%s) Cycle 1: err=%v, prMerged=%v, states=%v", sub.name, c1Err, prMerged, states)
		out["scenario3b_"+sub.name] = map[string]any{
			"cycle1Err": fmt.Sprint(c1Err),
			"prMerged":  prMerged,
			"states":    states,
		}
	}

	// --- Scenario 3b-iii: 502 merge error on 4th PR closes the lane for the 5th PR! ---
	{
		dir3b502 := filepath.Join(tmp, "3b-502")
		bare, _, _ := initBareWithMain(filepath.Join(dir3b502, "git"), "acme", "error-lane", map[string]string{"README.md": "base\n"})
		sim := githubsim.New("acme", "error-lane", bare, "main")
		tr := &realGitMergeableTransport{sims: map[string]*githubsim.Sim{"acme/error-lane": sim}}
		ghClient := github.NewClient(tr, nil)
		store, db, err := openTempStore(ctx, filepath.Join(dir3b502, "db"))
		if err != nil {
			return err
		}
		defer db.Close()
		repo := model.RepoRef{Owner: "acme", Name: "error-lane"}
		var pr4Num int
		for i := 1; i <= 5; i++ {
			id := fmt.Sprintf("45%d", i)
			fname := fmt.Sprintf("f_%d.txt", i)
			_, _, prNum, err := queueCompletedPR(ctx, store, ghClient, sim, repo, id,
				"add "+fname, map[string]string{fname: "ok\n"}, now.Add(time.Duration(i)*time.Second))
			if err != nil {
				return err
			}
			if i == 4 {
				pr4Num = prNum
			}
		}
		tr.failMerge502ForPR = pr4Num
		deps := orchestrator.Deps{Store: store, Client: ghClient}
		c1Err := orchestrator.RunCycle(ctx, deps, now.Add(time.Minute))
		prMergedC1 := map[string]bool{}
		for i := 1; i <= 5; i++ {
			id := fmt.Sprintf("45%d", i)
			prMergedC1[id] = sim.PullRequests[i-1].Merged
		}
		log.Printf("3b (server502AfterThirdClosesLane) Cycle 1: err=%v, prMerged=%v", c1Err, prMergedC1)
		if !prMergedC1["451"] || !prMergedC1["452"] || !prMergedC1["453"] || prMergedC1["454"] || prMergedC1["455"] {
			return fmt.Errorf("3b 502 lane close mismatch: %v", prMergedC1)
		}
		tr.failMerge502ForPR = 0
		_ = orchestrator.RunCycle(ctx, deps, now.Add(2*time.Minute))
		prMergedC2 := map[string]bool{}
		for i := 1; i <= 5; i++ {
			id := fmt.Sprintf("45%d", i)
			prMergedC2[id] = sim.PullRequests[i-1].Merged
		}
		out["scenario3b_server502AfterThirdClosesLane"] = map[string]any{
			"cycle1Err":      fmt.Sprint(c1Err),
			"cycle1PRMerged": prMergedC1,
			"cycle2PRMerged": prMergedC2,
		}
	}

	// --- Scenario 3c: Store holding `1` keeps `1` (both PutConfig=1 and staterepo round-trip of 1) ---
	{
		dir3c := filepath.Join(tmp, "3c")
		bare, _, _ := initBareWithMain(filepath.Join(dir3c, "git"), "acme", "single-lane", map[string]string{"README.md": "base\n"})
		sim := githubsim.New("acme", "single-lane", bare, "main")
		tr := &realGitMergeableTransport{sims: map[string]*githubsim.Sim{"acme/single-lane": sim}}
		ghClient := github.NewClient(tr, nil)

		// Create store1 with MaxMergesPerRepo = 1, export via staterepo, import into store2!
		store1, db1, err := openTempStore(ctx, filepath.Join(dir3c, "db1"))
		if err != nil {
			return err
		}
		defer db1.Close()
		cfg1, _ := store1.GetConfig(ctx)
		if cfg1 == nil {
			cfg1 = &model.Config{}
		}
		cfg1.MaxMergesPerRepo = 1
		if err := store1.PutConfig(ctx, *cfg1); err != nil {
			return err
		}
		expDir := filepath.Join(dir3c, "export")
		if err := staterepo.Export(ctx, db1, expDir); err != nil {
			return err
		}

		store2, db2, err := openTempStore(ctx, filepath.Join(dir3c, "db2"))
		if err != nil {
			return err
		}
		defer db2.Close()
		if err := staterepo.Import(ctx, db2, expDir); err != nil {
			return err
		}
		cfg2, _ := store2.GetConfig(ctx)
		if cfg2.MaxMergesPerRepo != 1 {
			return fmt.Errorf("3c: imported MaxMergesPerRepo = %d, want 1", cfg2.MaxMergesPerRepo)
		}

		repo := model.RepoRef{Owner: "acme", Name: "single-lane"}
		for i := 1; i <= 3; i++ {
			id := fmt.Sprintf("50%d", i)
			fname := fmt.Sprintf("single_%d.txt", i)
			if _, _, _, err := queueCompletedPR(ctx, store2, ghClient, sim, repo, id,
				"add "+fname, map[string]string{fname: "x\n"}, now.Add(time.Duration(i)*time.Second)); err != nil {
				return err
			}
		}

		deps := orchestrator.Deps{Store: store2, Client: ghClient}
		if err := orchestrator.RunCycle(ctx, deps, now.Add(time.Minute)); err != nil {
			return fmt.Errorf("3c Cycle 1: %w", err)
		}
		mergedC1 := 0
		for _, pr := range sim.PullRequests {
			if pr.Merged {
				mergedC1++
			}
		}
		log.Printf("3c (store holding 1): merged in Cycle 1 = %d/3 (want 1)", mergedC1)
		if mergedC1 != 1 {
			return fmt.Errorf("3c: merged %d in Cycle 1, want 1", mergedC1)
		}
		out["scenario3c_storeHolding1"] = map[string]any{
			"keptValue":     cfg2.MaxMergesPerRepo,
			"cycle1Merged":  mergedC1,
			"totalQueuedPR": 3,
		}
	}

	return writeArtifact(outDir, "item3-five-per-lane.json", out)
}

// ============================================================================
// ITEM 4: resume_later {ci: my-branch}, until-merged wait on closed task with
// open PR, and auto-merge hand-back across restart/import
// ============================================================================

type walkWaiter struct {
	client *ui.Client
	taskID string
}

func (w walkWaiter) WaitOn(ctx context.Context, ids []string, park bool) ([]model.WaitTarget, error) {
	return w.client.WaitOn(ctx, w.taskID, ids, park)
}

func (w walkWaiter) ParkUntil(ctx context.Context, until time.Time) (time.Time, error) {
	return w.client.ParkUntil(ctx, w.taskID, until)
}

func (w walkWaiter) ResumeLater(ctx context.Context, when model.ResumeOn) (model.ResumeOn, error) {
	return w.client.ResumeLater(ctx, w.taskID, when)
}

func runItem4(ctx context.Context, outDir string) error {
	log.Printf("=== ITEM 4: resume_later {ci: my-branch}, until-merged wait, auto-merge hand-back ===")
	tmp, err := os.MkdirTemp("", "walk-item4-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	out := map[string]any{}
	now := time.Date(2026, 11, 4, 16, 0, 0, 0, time.UTC)

	// --- Scenario 4a: `resume_later {ci: my-branch}` on an Actions repo in githubsim ---
	{
		dir4a := filepath.Join(tmp, "4a")
		bare, _, _ := initBareWithMain(filepath.Join(dir4a, "git"), "acme", "actions-app", map[string]string{"main.go": "package main\n"})
		sim := githubsim.New("acme", "actions-app", bare, "main")
		tr := &realGitMergeableTransport{sims: map[string]*githubsim.Sim{"acme/actions-app": sim}}
		ghClient := github.NewClient(tr, nil)
		store, db, err := openTempStore(ctx, filepath.Join(dir4a, "db"))
		if err != nil {
			return err
		}
		defer db.Close()

		repo := model.RepoRef{Owner: "acme", Name: "actions-app"}
		_ = store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub})
		uiClient := ui.NewClient(ui.Config{
			Actor:              model.Principal{Kind: model.PrincipalHuman, ID: "operator"},
			Capabilities:       ui.OfferedCapabilities(),
			Actions:            ghClient,
			GitHubPullRequests: ghClient,
		}, store)
		uiClient.Now = func() time.Time { return now }

		created, err := uiClient.CreateTask(ctx, ui.CreateTaskRequest{
			Title:       "fix: wait for GitHub Actions via resume_later",
			Description: "Push branch, suspend on ci: my-branch, resume on verdict.",
			Repo:        repo.String(),
		})
		if err != nil {
			return err
		}
		if created.State == model.StateProposed {
			_ = uiClient.Approve(ctx, created.ID)
		}

		attempts := 0
		var attempt2Prompt string
		var resumeToolReply string
		var pushedBranch, pushedSHA string
		runStart := now

		provider := fnProvider{run: func(ctx context.Context, rc agent.RunConfig) (*agent.Result, error) {
			attempts++
			if attempts == 1 {
				tObj, _ := store.GetTask(ctx, rc.TaskID)
				pushedBranch = tObj.BranchName()
				pushedSHA = pushBranchFiles(bare, "main", pushedBranch, map[string]string{"fix.go": "package main\n"}, "fix: attempt 1")
				// Seed an in-progress GitHub Actions workflow run in githubsim!
				started := runStart.Add(2 * time.Second)
				sim.AddWorkflowRun(githubsim.WorkflowRun{
					ID:         9101,
					Name:       "Tests",
					Path:       ".github/workflows/tests.yml",
					Event:      "push",
					HeadBranch: pushedBranch,
					HeadSHA:    pushedSHA,
					Status:     "in_progress",
					CreatedAt:  started,
				})
				tools := mcp.NewResumeLaterTools(walkWaiter{client: uiClient, taskID: rc.TaskID}, nil)
				callArgs := map[string]any{
					"reason": "pushed fix.go; waiting for Tests workflow verdict",
					"when":   map[string]any{"ci": "my-branch"},
				}
				res := tools[0].Handler(ctx, callArgs)
				resumeToolReply = res.Text
				return &agent.Result{
					FinalText:  "Suspending until CI finishes.",
					Transcript: res.Text,
					ToolCalls: []agent.ToolCall{{
						Name:      "resume_later",
						Arguments: callArgs,
						Text:      res.Text,
					}},
				}, nil
			}
			attempt2Prompt = rc.Prompt
			return &agent.Result{FinalText: "CI passed; work complete.", Transcript: "done"}, nil
		}}

		sbxDir := filepath.Join(dir4a, "sbx")
		_ = os.MkdirAll(sbxDir, 0o755)
		deps := orchestrator.Deps{
			Store:      store,
			Client:     ghClient,
			Sandboxes:  orchestrator.NewHostSandboxes(sbxDir),
			Framework:  orchestrator.StaticFramework(provider),
			MaxWorkers: 1,
		}

		// Cycle 1: Attempt 1 runs, pushes branch, calls resume_later {ci: my-branch}.
		if err := orchestrator.RunCycle(ctx, deps, now); err != nil {
			return fmt.Errorf("4a Cycle 1: %w", err)
		}
		stAfterC1, _ := store.State(ctx, created.ID)

		// Cycle 2: Actions run 9101 is still in_progress -> must stay suspended!
		if err := orchestrator.RunCycle(ctx, deps, now.Add(30*time.Second)); err != nil {
			return fmt.Errorf("4a Cycle 2: %w", err)
		}
		stWhileInProgress, _ := store.State(ctx, created.ID)
		attemptsWhileInProgress := attempts

		// Conclude Actions run 9101 in githubsim!
		sim.AddWorkflowRun(githubsim.WorkflowRun{
			ID:         9101,
			Name:       "Tests",
			Path:       ".github/workflows/tests.yml",
			Event:      "push",
			HeadBranch: pushedBranch,
			HeadSHA:    pushedSHA,
			Status:     "completed",
			Conclusion: "failure",
			CreatedAt:  now.Add(2 * time.Second),
		})

		// Cycle 3: Actions run concluded -> wakes task and dispatches Attempt 2!
		if err := orchestrator.RunCycle(ctx, deps, now.Add(60*time.Second)); err != nil {
			return fmt.Errorf("4a Cycle 3: %w", err)
		}
		stAfterWake, _ := store.State(ctx, created.ID)
		log.Printf("4a: stAfterC1=%s, whileInProgress=%s (attempts=%d), afterVerdict: attempts=%d state=%s",
			stAfterC1, stWhileInProgress, attemptsWhileInProgress, attempts, stAfterWake)
		if attemptsWhileInProgress != 1 || attempts != 2 {
			return fmt.Errorf("4a: expected 1 attempt while in_progress and 2 after verdict, got %d and %d",
				attemptsWhileInProgress, attempts)
		}
		out["scenario4a_resumeLaterActions"] = map[string]any{
			"resumeToolReply":         resumeToolReply,
			"stateAfterSuspend":       stAfterC1,
			"stateWhileInProgress":    stWhileInProgress,
			"attemptsWhileInProgress": attemptsWhileInProgress,
			"attemptsAfterVerdict":    attempts,
			"stateAfterAttempt2":      stAfterWake,
			"attempt2PromptHasReason": strings.Contains(attempt2Prompt, "waiting for Tests workflow verdict"),
		}
	}

	// --- Scenario 4b: Until-merged wait on a closed task with an open PR ---
	{
		dir4b := filepath.Join(tmp, "4b")
		bare, _, _ := initBareWithMain(filepath.Join(dir4b, "git"), "acme", "wait-app", map[string]string{"README.md": "wait\n"})
		sim := githubsim.New("acme", "wait-app", bare, "main")
		tr := &realGitMergeableTransport{sims: map[string]*githubsim.Sim{"acme/wait-app": sim}}
		ghClient := github.NewClient(tr, nil)
		store, db, err := openTempStore(ctx, filepath.Join(dir4b, "db"))
		if err != nil {
			return err
		}
		defer db.Close()
		uiClient := ui.NewClient(ui.Config{
			Actor:              model.Principal{Kind: model.PrincipalHuman, ID: "operator"},
			Capabilities:       ui.OfferedCapabilities(),
			Actions:            ghClient,
			GitHubPullRequests: ghClient,
		}, store)
		uiClient.Now = func() time.Time { return now }
		repo := model.RepoRef{Owner: "acme", Name: "wait-app"}

		// Task A has an open PR on githubsim, and Task A itself is CLOSED while its PR stays open!
		taskA, _, prNumA, err := queueCompletedPR(ctx, store, ghClient, sim, repo, "601", "feat: prerequisite change",
			map[string]string{"prereq.txt": "ready\n"}, now)
		if err != nil {
			return err
		}
		// Turn off AutoMerge on A and close Task A without merging PR #1!
		taskA.AutoMerge = false
		_ = store.PutTask(ctx, taskA)
		closedAt := now.Add(5 * time.Second)
		if err := store.Observe(ctx, model.Observation{TaskID: taskA.ID, ClosedAt: &closedAt}); err != nil {
			return err
		}

		// Task B parks waiting on Task A with until: "merged".
		createdB, err := uiClient.CreateTask(ctx, ui.CreateTaskRequest{
			Title:       "feat: follow-up needing prerequisite merged",
			Description: "Wait for 601 until merged.",
			Repo:        repo.String(),
		})
		if err != nil {
			return err
		}
		if createdB.State == model.StateProposed {
			_ = uiClient.Approve(ctx, createdB.ID)
		}

		bAttempts := 0
		var bAttempt2Prompt string
		var waitToolReply string
		provider := fnProvider{run: func(ctx context.Context, rc agent.RunConfig) (*agent.Result, error) {
			if rc.TaskID != createdB.ID {
				return &agent.Result{FinalText: "noop"}, nil
			}
			bAttempts++
			if bAttempts == 1 {
				waitTools := mcp.NewWaitForTaskTools(walkWaiter{client: uiClient, taskID: rc.TaskID}, nil)
				waitArgs := map[string]any{
					"task_id": taskA.ID,
					"until":   "merged",
					"park":    true,
				}
				res := waitTools[0].Handler(ctx, waitArgs)
				waitToolReply = res.Text
				return &agent.Result{
					FinalText:  "Parking until 601 merges.",
					Transcript: res.Text,
					ToolCalls: []agent.ToolCall{{
						Name:      "wait_for_task",
						Arguments: waitArgs,
						Text:      res.Text,
					}},
				}, nil
			}
			bAttempt2Prompt = rc.Prompt
			return &agent.Result{FinalText: "601 merged; proceeding!", Transcript: "done"}, nil
		}}

		sbxDir := filepath.Join(dir4b, "sbx")
		_ = os.MkdirAll(sbxDir, 0o755)
		deps := orchestrator.Deps{
			Store:      store,
			Client:     ghClient,
			Sandboxes:  orchestrator.NewHostSandboxes(sbxDir),
			Framework:  orchestrator.StaticFramework(provider),
			MaxWorkers: 1,
		}

		// Cycle 1: B dispatches and calls wait_for_task(task_id=601, until="merged", park=true).
		if err := orchestrator.RunCycle(ctx, deps, now.Add(10*time.Second)); err != nil {
			return fmt.Errorf("4b Cycle 1: %w", err)
		}
		// Cycle 2: Task A is CLOSED, PR #1 is still OPEN -> B MUST HOLD!
		if err := orchestrator.RunCycle(ctx, deps, now.Add(20*time.Second)); err != nil {
			return fmt.Errorf("4b Cycle 2: %w", err)
		}
		stBWhileOpenPR, _ := store.State(ctx, createdB.ID)
		attemptsWhileOpenPR := bAttempts
		log.Printf("4b while A closed & PR #%d open: B state=%s, bAttempts=%d (want 1) | waitReply=%q",
			prNumA, stBWhileOpenPR, attemptsWhileOpenPR, waitToolReply)
		if attemptsWhileOpenPR != 1 {
			return fmt.Errorf("4b BUG: B woke while closed task A's PR was still open! bAttempts=%d", attemptsWhileOpenPR)
		}

		// Now merge PR #1 on githubsim!
		prHeadSHA := sim.PullRequests[0].Head
		// Resolve actual branch tip SHA for MergePullRequest.
		prDetail, err := ghClient.GetPullRequest("acme", "wait-app", prNumA)
		if err != nil {
			return err
		}
		prHeadSHA = prDetail.HeadSHA
		if err := ghClient.MergePullRequest("acme", "wait-app", prNumA, prHeadSHA); err != nil {
			return fmt.Errorf("merging PR #%d: %w", prNumA, err)
		}

		// Cycle 3 (and Cycle 4 if closedprs observes in one step before dispatch, or same cycle!):
		if err := orchestrator.RunCycle(ctx, deps, now.Add(40*time.Second)); err != nil {
			return fmt.Errorf("4b Cycle 3: %w", err)
		}
		if bAttempts == 1 {
			// Check if reconcileClosedPRs runs after dispatch in Reconcilers() order -> next cycle dispatches!
			if err := orchestrator.RunCycle(ctx, deps, now.Add(50*time.Second)); err != nil {
				return fmt.Errorf("4b Cycle 4: %w", err)
			}
		}
		stBAfterMerge, _ := store.State(ctx, createdB.ID)
		log.Printf("4b after PR #%d merged: bAttempts=%d (want 2), B state=%s", prNumA, bAttempts, stBAfterMerge)
		if bAttempts != 2 {
			return fmt.Errorf("4b: expected B to wake after closed task A's PR merged, bAttempts=%d", bAttempts)
		}
		out["scenario4b_untilMergedOnClosedTaskWithOpenPR"] = map[string]any{
			"waitToolReply":       waitToolReply,
			"stateWhilePROpen":    stBWhileOpenPR,
			"attemptsWhilePROpen": attemptsWhileOpenPR,
			"attemptsAfterMerge":  bAttempts,
			"stateAfterMerge":     stBAfterMerge,
			"promptMentionsWait":  strings.Contains(bAttempt2Prompt, "601"),
		}
	}

	// --- Scenario 4c: Auto-merge hand-back when hold clears (across restart & staterepo import, only with clearance) ---
	{
		hasLink := func(t model.Task, k model.LinkKind) bool {
			for _, l := range t.Links {
				if l.Kind == k {
					return true
				}
			}
			return false
		}
		dir4c := filepath.Join(tmp, "4c")
		bare, _, _ := initBareWithMain(filepath.Join(dir4c, "git"), "acme", "sec-app", map[string]string{
			"pkg/gitproxy/proxy.go": "package gitproxy\n",
		})
		sim := githubsim.New("acme", "sec-app", bare, "main")
		tr := &realGitMergeableTransport{sims: map[string]*githubsim.Sim{"acme/sec-app": sim}}
		ghClient := github.NewClient(tr, nil)
		store1, db1, err := openTempStore(ctx, filepath.Join(dir4c, "db1"))
		if err != nil {
			return err
		}
		defer db1.Close()

		uiClient1 := ui.NewClient(ui.Config{
			Actor:        model.Principal{Kind: model.PrincipalHuman, ID: "operator"},
			Capabilities: ui.OfferedCapabilities(),
		}, store1)
		repo := model.RepoRef{Owner: "acme", Name: "sec-app"}

		createdC, err := uiClient1.CreateTask(ctx, ui.CreateTaskRequest{
			Title:       "fix(gitproxy): add doc comment on proxy.go",
			Description: "Touch trust-boundary file pkg/gitproxy/proxy.go with auto_merge: true.",
			Repo:        repo.String(),
			AutoMerge:   true,
		})
		if err != nil {
			return err
		}
		if createdC.State == model.StateProposed {
			_ = uiClient1.Approve(ctx, createdC.ID)
		}

		sbxDir := filepath.Join(dir4c, "sbx")
		_ = os.MkdirAll(sbxDir, 0o755)
		makeProvider := func(st *model.Store) fnProvider {
			return fnProvider{run: func(ctx context.Context, rc agent.RunConfig) (*agent.Result, error) {
				if rc.TaskID == createdC.ID {
					tObj, _ := st.GetTask(ctx, rc.TaskID)
					br := tObj.BranchName()
					sha := pushBranchFiles(bare, "main", br, map[string]string{
						"pkg/gitproxy/proxy.go": "package gitproxy\n\n// tweak\n",
					}, "fix(gitproxy): add comment")
					sim.CheckRuns[sha] = []github.CheckRun{{ID: 701, Name: "Tests", Status: "completed", Conclusion: strPtr("success")}}
					sim.CheckRuns[br] = sim.CheckRuns[sha]
					return &agent.Result{FinalText: "Updated pkg/gitproxy/proxy.go.", Transcript: "pushed"}, nil
				}
				// Automated security review task: finish clean with no findings!
				return &agent.Result{FinalText: "no findings", Transcript: "clean"}, nil
			}}
		}

		deps1 := orchestrator.Deps{
			Store:      store1,
			Client:     ghClient,
			Sandboxes:  orchestrator.NewHostSandboxes(sbxDir),
			Framework:  orchestrator.StaticFramework(makeProvider(store1)),
			MaxWorkers: 2,
		}

		// Cycle 1: Task C runs, pushes pkg/gitproxy/proxy.go, opens PR -> classifier puts LinkSecurityHold and takes AutoMerge!
		if err := orchestrator.RunCycle(ctx, deps1, now); err != nil {
			return fmt.Errorf("4c Cycle 1: %w", err)
		}
		heldTask, _ := store1.GetTask(ctx, createdC.ID)
		heldHasSecurityLink := hasLink(*heldTask, model.LinkSecurityHold)
		log.Printf("4c after Cycle 1 (held): AutoMerge=%v, hasSecurityHold=%v, links=%v",
			heldTask.AutoMerge, heldHasSecurityLink, heldTask.Links)
		if heldTask.AutoMerge || !heldHasSecurityLink {
			return fmt.Errorf("4c: expected AutoMerge=false and LinkSecurityHold under security hold, got AutoMerge=%v links=%v",
				heldTask.AutoMerge, heldTask.Links)
		}

		// Export state repo from db1 and import into brand-new db2 (simulating daemon restart + state import)!
		expDir := filepath.Join(dir4c, "state-export")
		if err := staterepo.Export(ctx, db1, expDir); err != nil {
			return fmt.Errorf("4c staterepo.Export: %w", err)
		}
		store2, db2, err := openTempStore(ctx, filepath.Join(dir4c, "db2"))
		if err != nil {
			return err
		}
		defer db2.Close()
		if err := staterepo.Import(ctx, db2, expDir); err != nil {
			return fmt.Errorf("4c staterepo.Import: %w", err)
		}
		importedHeld, _ := store2.GetTask(ctx, createdC.ID)
		importedHasHold := hasLink(*importedHeld, model.LinkSecurityHold)
		if importedHeld.AutoMerge || !importedHasHold {
			return fmt.Errorf("4c after import: AutoMerge=%v hasHold=%v, want false/true",
				importedHeld.AutoMerge, importedHasHold)
		}

		// Run cycles on imported store2: security review task runs & ends clean -> hold lifts -> AutoMerge handed back -> PR merges!
		deps2 := orchestrator.Deps{
			Store:      store2,
			Client:     ghClient,
			Sandboxes:  orchestrator.NewHostSandboxes(sbxDir),
			Framework:  orchestrator.StaticFramework(makeProvider(store2)),
			MaxWorkers: 2,
		}
		var autoMergeRestored bool
		for step := 1; step <= 6; step++ {
			if err := orchestrator.RunCycle(ctx, deps2, now.Add(time.Duration(step*15)*time.Second)); err != nil {
				return fmt.Errorf("4c imported Cycle %d: %w", step, err)
			}
			cur, _ := store2.GetTask(ctx, createdC.ID)
			if cur.AutoMerge {
				autoMergeRestored = true
			}
			if st, _ := store2.State(ctx, createdC.ID); st == model.StateClosed && sim.PullRequests[0].Merged {
				autoMergeRestored = cur.AutoMerge
				break
			}
		}
		finalTaskC, _ := store2.GetTask(ctx, createdC.ID)
		finalStateC, _ := store2.State(ctx, createdC.ID)
		log.Printf("4c after clean review on imported store: AutoMerge=%v, state=%s, prMerged=%v",
			finalTaskC.AutoMerge, finalStateC, sim.PullRequests[0].Merged)
		if !autoMergeRestored || !sim.PullRequests[0].Merged || finalStateC != model.StateClosed {
			return fmt.Errorf("4c: expected AutoMerge handed back and PR merged; AutoMerge=%v prMerged=%v state=%s",
				finalTaskC.AutoMerge, sim.PullRequests[0].Merged, finalStateC)
		}

		// Negative guard (#609 / 8d5402b2): dropping hold WITHOUT clearance (e.g. retargeting repo) keeps AutoMerge=false!
		otherRepo := model.RepoRef{Owner: "acme", Name: "other"}
		taskNeg := model.Task{
			ID:        "699",
			Title:     "held task retargeted",
			Body:      "test",
			Target:    &repo,
			AutoMerge: false,
			CreatedAt: &now,
			Origin:    model.Origin{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}}},
			Binding:   model.BindingDirective,
			Links: []model.Link{
				{Kind: model.LinkSecurityHold, Target: "v1"},
				{Kind: model.LinkTrustBoundaryCleared, Target: "deadbeef@v1"},
			},
		}
		if err := store2.PutTask(ctx, taskNeg); err != nil {
			return err
		}
		afterHoldNeg, _ := store2.GetTask(ctx, "699")
		// Drop both hold and clearance (as retargeting does).
		afterHoldNeg.Target = &otherRepo
		afterHoldNeg.Links = nil
		if err := store2.PutTask(ctx, *afterHoldNeg); err != nil {
			return err
		}
		afterDropNoClearance, _ := store2.GetTask(ctx, "699")
		if afterDropNoClearance.AutoMerge {
			return fmt.Errorf("4c SECURITY BUG: AutoMerge restored when clearance was dropped alongside hold!")
		}

		out["scenario4c_autoMergeHandBack"] = map[string]any{
			"underHoldAutoMerge":               heldTask.AutoMerge,
			"underHoldHasSecurityLink":         heldHasSecurityLink,
			"survivedExportImport":             importedHasHold && !importedHeld.AutoMerge,
			"restoredAndMergedAfterClean":      sim.PullRequests[0].Merged && finalTaskC.AutoMerge,
			"withheldWhenClearanceAlsoDropped": !afterDropNoClearance.AutoMerge,
		}
	}

	return writeArtifact(outDir, "item4-waits-and-handback.json", out)
}

// ============================================================================
// ITEM 5: Direction rollups across windows + System -> Metrics server
// ============================================================================

func runItem5(ctx context.Context, outDir, serveAddr, accessCode string) error {
	log.Printf("=== ITEM 5: Direction rollups & System -> Metrics ===")
	tmp, err := os.MkdirTemp("", "walk-item5-*")
	if err != nil {
		return err
	}
	if serveAddr == "" {
		defer os.RemoveAll(tmp)
	}

	store, db, err := openTempStore(ctx, filepath.Join(tmp, "db"))
	if err != nil {
		return err
	}
	defer db.Close()

	// Reference clock: Wednesday 2026-11-04 15:00:00 UTC
	// - today:      2026-11-04 [2026-11-04 00:00Z, 2026-11-05 00:00Z)
	// - yesterday:  2026-11-03 [2026-11-03 00:00Z, 2026-11-04 00:00Z)
	// - this-week:  2026-W45   [2026-11-02 00:00Z (Mon), 2026-11-09 00:00Z)
	// - last-week:  2026-W44   [2026-10-26 00:00Z (Mon), 2026-11-02 00:00Z)
	// - this-month: 2026-11    [2026-11-01 00:00Z, 2026-12-01 00:00Z)
	// - last-month: 2026-10    [2026-10-01 00:00Z, 2026-11-01 00:00Z)
	refNow := time.Date(2026, 11, 4, 15, 0, 0, 0, time.UTC)

	rCore := model.RepoRef{Owner: "acme", Name: "widgets"}
	rDocs := model.RepoRef{Owner: "acme", Name: "docs"}
	alice := model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}
	bob := model.Principal{Kind: model.PrincipalHuman, ID: "bob@example.com"}
	schedActor := model.Principal{Kind: model.PrincipalAutomation, ID: "schedule:sched-ref-1"}

	intPtr := func(v int) *int { return &v }
	timePtr := func(t time.Time) *time.Time { return &t }

	type seededRun struct {
		ID         string
		TaskID     string
		Sandbox    string
		Attempt    int
		StartedAt  time.Time
		FinishedAt *time.Time
		Outcome    string
	}
	seedTask := func(t model.Task, obs model.Observation, runs []seededRun) error {
		if err := store.PutTask(ctx, t); err != nil {
			return fmt.Errorf("PutTask(%s): %w", t.ID, err)
		}
		for _, r := range runs {
			mr := model.Run{
				ID: r.ID, TaskID: r.TaskID, Sandbox: r.Sandbox,
				Attempt: r.Attempt, StartedAt: r.StartedAt,
			}
			if err := store.StartRun(ctx, mr, model.Limits{Workers: 4}); err != nil {
				return fmt.Errorf("StartRun(%s): %w", r.ID, err)
			}
			if r.FinishedAt != nil {
				if err := store.FinishRun(ctx, r.ID, *r.FinishedAt, r.Outcome, ""); err != nil {
					return fmt.Errorf("FinishRun(%s): %w", r.ID, err)
				}
			}
		}
		obs.TaskID = t.ID
		if err := store.Observe(ctx, obs); err != nil {
			return fmt.Errorf("Observe(%s): %w", t.ID, err)
		}
		return nil
	}

	// Direction 1 (root #100, filed in Oct / W44: 2026-10-28, spawning #101 on Sun Nov 1 (W44 + Nov!),
	// #102 on Tue Nov 3 (yesterday, W45), and grandchild #103 -> #102 on Wed Nov 4 (today, W45)!).
	tOct28 := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	tNov01 := time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC) // W44 AND 2026-11!
	tNov03 := time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC) // yesterday, W45, 2026-11
	tNov04 := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)  // today, W45, 2026-11

	must(seedTask(
		model.Task{
			ID: "100", Title: "Migrate storage layer to connection pooling", Body: "Root direction 1.",
			Target: &rCore, CreatedAt: &tOct28,
			Origin:  model.Origin{Attribution: model.Attribution{Actor: alice}},
			Binding: model.BindingDirective,
			Links:   []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#10"}},
		},
		model.Observation{
			CompletedAt: timePtr(tOct28.Add(30 * time.Minute)),
			PrMergedAt:  timePtr(tOct28.Add(45 * time.Minute)),
			PrAdditions: intPtr(180), PrDeletions: intPtr(40), PrChangedFiles: intPtr(4),
			ClosedAt: timePtr(tOct28.Add(45 * time.Minute)),
		},
		[]seededRun{{
			ID: "r-100-1", TaskID: "100", Sandbox: "sbx-100", Attempt: 1,
			StartedAt: tOct28.Add(5 * time.Minute), FinishedAt: timePtr(tOct28.Add(35 * time.Minute)),
			Outcome: "succeeded",
		}},
	))

	// Child #101 proposed by #100, landed on Sunday 2026-11-01 (in W44 AND in 2026-11!).
	must(seedTask(
		model.Task{
			ID: "101", Title: "Add idle pool reaper metrics", Body: "Child 1 of 100.",
			Target: &rCore, CreatedAt: &tNov01,
			Origin:     model.Origin{Attribution: model.Attribution{Actor: alice, OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: "100"}}},
			WorkOrigin: model.TaskOrigin("100"),
			Binding:    model.BindingDirective,
			Links: []model.Link{
				{Kind: model.LinkProposedBy, Target: "100"},
				{Kind: model.LinkFixes, Target: "acme/widgets#11"},
			},
		},
		model.Observation{
			CompletedAt: timePtr(tNov01.Add(45 * time.Minute)),
			PrMergedAt:  timePtr(tNov01.Add(60 * time.Minute)),
			PrAdditions: intPtr(95), PrDeletions: intPtr(12), PrChangedFiles: intPtr(2),
			ClosedAt: timePtr(tNov01.Add(60 * time.Minute)),
		},
		[]seededRun{{
			ID: "r-101-1", TaskID: "101", Sandbox: "sbx-101", Attempt: 1,
			StartedAt: tNov01.Add(5 * time.Minute), FinishedAt: timePtr(tNov01.Add(50 * time.Minute)),
			Outcome: "succeeded",
		}},
	))

	// Child #102 proposed by #100, landed yesterday 2026-11-03 (W45, 2026-11).
	must(seedTask(
		model.Task{
			ID: "102", Title: "Document connection pool sizing flags", Body: "Child 2 of 100.",
			Target: &rDocs, CreatedAt: &tNov03,
			Origin:     model.Origin{Attribution: model.Attribution{Actor: alice}},
			WorkOrigin: model.TaskOrigin("100"),
			Binding:    model.BindingDirective,
			Links: []model.Link{
				{Kind: model.LinkProposedBy, Target: "100"},
				{Kind: model.LinkFixes, Target: "acme/docs#4"},
			},
		},
		model.Observation{
			CompletedAt: timePtr(tNov03.Add(25 * time.Minute)),
			PrMergedAt:  timePtr(tNov03.Add(40 * time.Minute)),
			PrAdditions: intPtr(64), PrDeletions: intPtr(8), PrChangedFiles: intPtr(1),
			ClosedAt: timePtr(tNov03.Add(40 * time.Minute)),
		},
		[]seededRun{
			{
				ID: "r-102-1", TaskID: "102", Sandbox: "sbx-102a", Attempt: 1,
				StartedAt: tNov03.Add(2 * time.Minute), FinishedAt: timePtr(tNov03.Add(14 * time.Minute)),
				Outcome: "failed",
			},
			{
				ID: "r-102-2", TaskID: "102", Sandbox: "sbx-102b", Attempt: 2,
				StartedAt: tNov03.Add(15 * time.Minute), FinishedAt: timePtr(tNov03.Add(33 * time.Minute)),
				Outcome: "succeeded",
			},
		},
	))

	// Grandchild #103 proposed by #102 (rolls all the way up to root #100!), landed today 2026-11-04!
	must(seedTask(
		model.Task{
			ID: "103", Title: "Fix pool timeout default in example config", Body: "Grandchild of 100 via 102.",
			Target: &rCore, CreatedAt: &tNov04,
			Origin:     model.Origin{Attribution: model.Attribution{Actor: alice}},
			WorkOrigin: model.TaskOrigin("100"),
			Binding:    model.BindingDirective,
			Links: []model.Link{
				{Kind: model.LinkProposedBy, Target: "102"},
				{Kind: model.LinkFixes, Target: "acme/widgets#15"},
			},
		},
		model.Observation{
			CompletedAt: timePtr(tNov04.Add(35 * time.Minute)),
			PrMergedAt:  timePtr(tNov04.Add(50 * time.Minute)),
			PrAdditions: intPtr(22), PrDeletions: intPtr(5), PrChangedFiles: intPtr(1),
			ClosedAt: timePtr(tNov04.Add(50 * time.Minute)),
		},
		[]seededRun{{
			ID: "r-103-1", TaskID: "103", Sandbox: "sbx-103", Attempt: 1,
			StartedAt: tNov04.Add(5 * time.Minute), FinishedAt: timePtr(tNov04.Add(35 * time.Minute)),
			Outcome: "succeeded",
		}},
	))

	// Direction 2 (root #200, schedule-triggered today 2026-11-04, spawning #201 today).
	tNov04b := time.Date(2026, 11, 4, 11, 0, 0, 0, time.UTC)
	must(seedTask(
		model.Task{
			ID: "200", Title: "Sync upstream v1.3.0 release tag", Body: "Scheduled sync.",
			Target: &rCore, CreatedAt: &tNov04b,
			Origin:  model.Origin{Attribution: model.Attribution{Actor: schedActor}},
			Binding: model.BindingDirective,
			Links:   []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#16"}},
		},
		model.Observation{
			CompletedAt: timePtr(tNov04b.Add(20 * time.Minute)),
			PrMergedAt:  timePtr(tNov04b.Add(30 * time.Minute)),
			PrAdditions: intPtr(48), PrDeletions: intPtr(19), PrChangedFiles: intPtr(3),
			ClosedAt: timePtr(tNov04b.Add(30 * time.Minute)),
		},
		[]seededRun{{
			ID: "r-200-1", TaskID: "200", Sandbox: "sbx-200", Attempt: 1,
			StartedAt: tNov04b.Add(2 * time.Minute), FinishedAt: timePtr(tNov04b.Add(20 * time.Minute)),
			Outcome: "succeeded",
		}},
	))
	tNov04b25 := tNov04b.Add(25 * time.Minute)
	must(seedTask(
		model.Task{
			ID: "201", Title: "Update changelog for v1.3.0 sync", Body: "Follow-up from 200.",
			Target: &rDocs, CreatedAt: &tNov04b25,
			Origin:  model.Origin{Attribution: model.Attribution{Actor: bob}},
			Binding: model.BindingDirective,
			Links:   []model.Link{{Kind: model.LinkProposedBy, Target: "200"}},
		},
		model.Observation{
			CompletedAt: timePtr(tNov04b.Add(50 * time.Minute)),
			ClosedAt:    timePtr(tNov04b.Add(50 * time.Minute)),
		},
		[]seededRun{{
			ID: "r-201-1", TaskID: "201", Sandbox: "sbx-201", Attempt: 1,
			StartedAt: tNov04b.Add(30 * time.Minute), FinishedAt: timePtr(tNov04b.Add(48 * time.Minute)),
			Outcome: "succeeded",
		}},
	))

	// Direction 3 (root #300, repo-less investigation completed today with no PR).
	tNov04b60 := tNov04b.Add(60 * time.Minute)
	must(seedTask(
		model.Task{
			ID: "300", Title: "Audit weekly flake budget across suites", Body: "Repo-less report.",
			CreatedAt: &tNov04b60,
			Origin:    model.Origin{Attribution: model.Attribution{Actor: bob}},
			Binding:   model.BindingDirective,
		},
		model.Observation{
			CompletedAt: timePtr(tNov04b.Add(75 * time.Minute)),
			ClosedAt:    timePtr(tNov04b.Add(75 * time.Minute)),
		},
		[]seededRun{{
			ID: "r-300-1", TaskID: "300", Sandbox: "sbx-300", Attempt: 1,
			StartedAt: tNov04b.Add(63 * time.Minute), FinishedAt: timePtr(tNov04b.Add(75 * time.Minute)),
			Outcome: "succeeded",
		}},
	))

	// Seed a published weekly release note for 2026-W44 and a git_ref schedule so the UI has rich data.
	_ = store.PutReleaseNote(ctx, model.ReleaseNote{
		Period:    "2026-W44",
		Title:     "Connection pooling foundation and reaper metrics",
		Text:      "Landed the core connection pool migration (#100) and Sunday's idle reaper metrics (#101).",
		CreatedAt: tNov03,
		UpdatedAt: tNov03,
		UpdatedBy: model.Principal{Kind: model.PrincipalAutomation, ID: "release-notes"},
	})
	_ = store.PutSchedule(ctx, model.Schedule{
		ID:      "sched-git-ref-demo",
		Title:   "Sync upstream latest release",
		Body:    "Watch upstream/core refs/tags/latest.",
		Target:  rCore,
		Overlap: model.OverlapSkip,
		Enabled: true,
		Recurrence: model.Recurrence{
			Kind:   model.RecurrenceGitRef,
			GitRef: &model.GitRefTrigger{Repo: "upstream/core", Ref: "refs/tags/latest"},
		},
		NextRunAt: refNow.Add(720 * time.Hour),
		LastRunAt: &tNov04b,
		CreatedAt: tOct28,
	})

	cfg, _ := store.GetConfig(ctx)
	if cfg == nil {
		cfg = &model.Config{}
	}
	cfg.TargetRepos = []string{"acme/widgets", "acme/docs", "upstream/core"}
	_ = store.PutConfig(ctx, *cfg)

	token := accessCode
	if token == "" {
		token = "test-item5-token"
	}
	client := ui.NewClient(ui.Config{
		Actor:               alice,
		DefaultTarget:       &rCore,
		Capabilities:        ui.OfferedCapabilities(),
		SandboxShapeIgnored: true,
		LocalAPIToken:       token,
	}, store)
	client.Now = func() time.Time { return refNow }
	handler := ui.NewServerWithClient(client)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	windows := []string{
		"",
		"today",
		"yesterday",
		"2026-11-04",
		"2026-11-03",
		"this-week",
		"last-week",
		"2026-W45",
		"2026-W44",
		"2026-W43",
		"this-month",
		"last-month",
		"2026-11",
		"2026-10",
	}

	windowResults := map[string]any{}
	allowedRoots := map[string]bool{"100": true, "200": true, "300": true}

	for _, w := range windows {
		q := ""
		key := w
		if w == "" {
			key = "(default_empty)"
		} else {
			q = "?period=" + w
		}
		req, _ := http.NewRequest("GET", srv.URL+"/api/release-notes/stats"+q, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("GET /api/release-notes/stats%s returned %d: %s", q, resp.StatusCode, raw)
		}
		var st ui.ClusterStats
		if err := json.Unmarshal(raw, &st); err != nil {
			return err
		}

		// Verify direction totals equal top-level totals!
		var sumPRMerged, sumPRMeasured, sumAdded, sumDeleted, sumCompleted, sumFiled, sumRuns, sumRunsSucc int
		var sumHours float64
		for _, d := range st.Directions {
			if !allowedRoots[d.TaskID] {
				return fmt.Errorf("window %q attributed work to non-root task #%s (expected only 100, 200, 300)", key, d.TaskID)
			}
			sumPRMerged += d.PullRequestsMerged
			sumPRMeasured += d.PullRequestsMeasured
			sumAdded += d.LinesAdded
			sumDeleted += d.LinesDeleted
			sumCompleted += d.TasksCompleted
			sumFiled += d.TasksFiled
			sumRuns += d.Runs
			sumRunsSucc += d.RunsSucceeded
			sumHours += d.AgentHours
		}
		if sumPRMerged != st.PullRequestsMerged ||
			sumPRMeasured != st.PullRequestsMeasured ||
			sumAdded != st.LinesAdded ||
			sumDeleted != st.LinesDeleted ||
			sumCompleted != st.TasksCompleted ||
			sumFiled != st.TasksFiled ||
			sumRuns != st.Runs ||
			sumRunsSucc != st.RunsSucceeded ||
			math.Abs(sumHours-st.AgentHours) > 1e-9 {
			return fmt.Errorf("window %q totals mismatch: top=%+v vs sum(directions)=(pr=%d/%d +%d -%d comp=%d filed=%d runs=%d/%d hrs=%f)",
				key, st, sumPRMerged, sumPRMeasured, sumAdded, sumDeleted, sumCompleted, sumFiled, sumRuns, sumRunsSucc, sumHours)
		}
		log.Printf("Window %-15s -> period=%-10s PRs=%d (+%d -%d) completed=%d filed=%d runs=%d directions=%d",
			key, st.Period, st.PullRequestsMerged, st.LinesAdded, st.LinesDeleted, st.TasksCompleted, st.TasksFiled, st.Runs, len(st.Directions))
		windowResults[key] = st
	}

	// Probe deployment TimeZone handling on GET /api/release-notes/stats?period=today!
	// At 2026-11-04 02:30:00 UTC, in America/Los_Angeles (UTC-8) the local date is still 2026-11-03!
	tzCfg, _ := store.GetConfig(ctx)
	tzCfg.TimeZone = "America/Los_Angeles"
	_ = store.PutConfig(ctx, *tzCfg)
	laClock := time.Date(2026, 11, 4, 2, 30, 0, 0, time.UTC)
	tzClient := ui.NewClient(ui.Config{Actor: alice}, store)
	tzClient.Now = func() time.Time { return laClock }
	tzStats, err := tzClient.ClusterStats(ctx, "today")
	if err != nil {
		return err
	}
	log.Printf("TimeZone probe (Config.TimeZone=America/Los_Angeles at 2026-11-04T02:30Z): period=%q (LA date is 2026-11-03)", tzStats.Period)
	// Restore UTC config for screenshots/API consistency.
	tzCfg.TimeZone = "UTC"
	_ = store.PutConfig(ctx, *tzCfg)

	// Invalid period check.
	badReq, _ := http.NewRequest("GET", srv.URL+"/api/release-notes/stats?period=bogus-window", nil)
	badReq.Header.Set("Authorization", "Bearer "+token)
	badResp, _ := http.DefaultClient.Do(badReq)
	badBody, _ := io.ReadAll(badResp.Body)
	_ = badResp.Body.Close()

	if err := writeArtifact(outDir, "item5-direction-rollups.json", map[string]any{
		"referenceClockUTC": refNow,
		"windows":           windowResults,
		"timezoneProbeLA": map[string]any{
			"clockUTC":       laClock,
			"configTimeZone": "America/Los_Angeles",
			"expectedPeriod": "2026-11-03",
			"actualPeriod":   tzStats.Period,
			"since":          tzStats.Since,
			"until":          tzStats.Until,
		},
		"invalidPeriodResponse": map[string]any{
			"status": badResp.StatusCode,
			"body":   strings.TrimSpace(string(badBody)),
		},
	}); err != nil {
		return err
	}

	if serveAddr != "" {
		httpSrv := &http.Server{Addr: serveAddr, Handler: handler}
		go func() {
			<-ctx.Done()
			_ = httpSrv.Close()
		}()
		log.Printf("Serving seeded UI + Metrics on http://%s (access code: %s)", serveAddr, token)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return err
		}
	}
	return nil
}
