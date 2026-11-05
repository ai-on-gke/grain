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

// Command walkthrough executes §5 ("The workstation CLI") of
// docs/test-plans/2026-11-04.md against live processes:
//   - a real `grain daemon` set up with `grain init workstation`, backed by
//     two simulated GitHub repositories (acme/widgets on grain CI, acme/other
//     on GitHub Actions) and host sandboxes;
//   - `grain demo -github-sim -fake-ci -scheduled-workflows -merge-queue-states -git-proxy`;
//   - `tests/iap/iapsim.go` in front of `grain demo -ui-iap-*` for the IAP
//     sign-in redirect; and
//   - a self-signed TLS proxy in front of the daemon for TLS-mismatch checks.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type CmdRecord struct {
	Section      string `json:"section"`
	Label        string `json:"label"`
	Command      string `json:"command"`
	ExpectedExit int    `json:"expectedExit"`
	ObservedExit int    `json:"observedExit"`
	Pass         bool   `json:"pass"`
	Stdout       string `json:"stdout"`
	Stderr       string `json:"stderr"`
	JSONValid    *bool  `json:"jsonValid,omitempty"`
	Notes        string `json:"notes,omitempty"`
}

type FSParityRecord struct {
	Name          string `json:"name"`
	SandboxCmd    string `json:"sandboxCmd"`
	ExternalCmd   string `json:"externalCmd"`
	SandboxBytes  int    `json:"sandboxBytes"`
	ExternalBytes int    `json:"externalBytes"`
	ByteIdentical bool   `json:"byteIdentical"`
	SandboxExit   int    `json:"sandboxExit"`
	ExternalExit  int    `json:"externalExit"`
	Preview       string `json:"preview"`
}

type SuiteReport struct {
	GeneratedAt string           `json:"generatedAt"`
	GrainBinary string           `json:"grainBinary"`
	Commands    []CmdRecord      `json:"commands"`
	FSParity    []FSParityRecord `json:"fsParity"`
	Findings    []string         `json:"findings"`
}

type runner struct {
	grainBin string
	homeDir  string
	report   SuiteReport
}

func (r *runner) execGrain(section, label, displayCmd string, expectedExit int, checkJSON bool, extraEnv []string, args ...string) CmdRecord {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.grainBin, args...)
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + r.homeDir,
		"XDG_CONFIG_HOME=" + filepath.Join(r.homeDir, ".config"),
		"GRAIN_CLI_ENV=",
		"GRAIN_DATA_DIR=",
		"GRAIN_SANDBOX_CLI=0",
	}
	env = append(env, extraEnv...)
	cmd.Env = env
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
	}
	rec := CmdRecord{
		Section:      section,
		Label:        label,
		Command:      displayCmd,
		ExpectedExit: expectedExit,
		ObservedExit: code,
		Pass:         code == expectedExit,
		Stdout:       outBuf.String(),
		Stderr:       errBuf.String(),
	}
	if checkJSON {
		valid := false
		// Per #XC-13: on error (except target_failed exit 1 where stdout carries
		// the verdict), stderr carries one JSON {"error": ...} object and stdout
		// must be empty. On success (or target_failed), stdout carries valid JSON.
		outTrim := strings.TrimSpace(rec.Stdout)
		errTrim := strings.TrimSpace(rec.Stderr)
		var js any
		switch {
		case code == 0:
			valid = outTrim != "" && json.Unmarshal([]byte(outTrim), &js) == nil
		case code == 1 && outTrim != "" && errTrim == "":
			valid = json.Unmarshal([]byte(outTrim), &js) == nil
		default:
			var errObj struct {
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			valid = outTrim == "" && errTrim != "" &&
				json.Unmarshal([]byte(errTrim), &errObj) == nil &&
				errObj.Error != nil && errObj.Error.Code != ""
			if !valid {
				rec.Notes = fmt.Sprintf("json check failed: stdout=%q stderr=%q", outTrim, errTrim)
			}
		}
		rec.JSONValid = &valid
		if !valid {
			rec.Pass = false
		}
	}
	r.report.Commands = append(r.report.Commands, rec)
	status := "PASS"
	if !rec.Pass {
		status = "FAIL"
		r.report.Findings = append(r.report.Findings, fmt.Sprintf("[%s] %s: expected exit %d, got %d (notes=%s, stderr=%q, stdout=%q)",
			section, label, expectedExit, code, rec.Notes, firstLine(rec.Stderr), firstLine(rec.Stdout)))
	}
	log.Printf("%s [%s] %s: want=%d got=%d", status, section, label, expectedExit, code)
	return rec
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

func mustRun(dir, name string, args ...string) string {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func freeAddr() string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

type multiRepoSim struct {
	mu     sync.Mutex
	sims   map[string]*githubsim.Sim
	defSim *githubsim.Sim
}

func (m *multiRepoSim) Request(method, reqURI string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	clean := strings.TrimPrefix(reqURI, "/api/v3")
	if clean == "" {
		clean = "/"
	}
	if clean == "/user" {
		return github.ApiResponse{Status: http.StatusOK, Body: []byte(`{"login":"operator","name":"Operator","email":"operator@example.com"}`)}, nil
	}
	if strings.Contains(clean, "/git/matching-refs/tags/") {
		return github.ApiResponse{Status: http.StatusOK, Body: []byte("[]")}, nil
	}
	for full, sim := range m.sims {
		if strings.HasPrefix(clean, "/repos/"+full+"/") || clean == "/repos/"+full ||
			strings.HasPrefix(clean, "/repos/"+full+"?") {
			return sim.Request(method, clean, headers, body)
		}
	}
	if strings.HasPrefix(clean, "/graphql") || strings.HasPrefix(reqURI, "/api/graphql") {
		for full, sim := range m.sims {
			parts := strings.SplitN(full, "/", 2)
			if bytes.Contains(body, []byte(parts[1])) {
				return sim.Request(method, "/graphql", headers, body)
			}
		}
	}
	return m.defSim.Request(method, clean, headers, body)
}

func setupGitRepo(gitRoot, owner, name, workflowYAML string, branches map[string]map[string]string) (string, *githubsim.Sim) {
	bare := filepath.Join(gitRoot, owner, name+".git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		log.Fatal(err)
	}
	mustRun(bare, "git", "init", "--bare", "--initial-branch=main")
	mustRun(bare, "git", "config", "http.receivepack", "true")

	workParent, err := os.MkdirTemp("", "seed-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(workParent)
	mustRun(workParent, "git", "clone", "-q", bare, "seed")
	seed := filepath.Join(workParent, "seed")
	mustRun(seed, "git", "config", "user.name", "seed")
	mustRun(seed, "git", "config", "user.email", "seed@example.com")
	mustWrite(filepath.Join(seed, "README.md"), "# "+owner+"/"+name+"\n")
	if workflowYAML != "" {
		mustWrite(filepath.Join(seed, ".grain", "workflows", "tests.yml"), workflowYAML)
	}
	mustWrite(filepath.Join(seed, "MODE.txt"), "pass\n")
	mustRun(seed, "git", "add", "-A")
	mustRun(seed, "git", "commit", "-q", "-m", "initial commit")
	mustRun(seed, "git", "push", "-q", "origin", "main")

	for br, files := range branches {
		mustRun(seed, "git", "checkout", "-q", "-B", br, "main")
		for p, content := range files {
			mustWrite(filepath.Join(seed, p), content)
		}
		mustRun(seed, "git", "add", "-A")
		mustRun(seed, "git", "commit", "--allow-empty", "-q", "-m", "branch "+br)
		mustRun(seed, "git", "push", "-q", "origin", br)
	}
	mustRun(seed, "git", "checkout", "-q", "main")
	sim := githubsim.New(owner, name, bare, "main")
	return bare, sim
}

func mustWrite(path, content string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		log.Fatal(err)
	}
}

func startGitHubHost(m *multiRepoSim, gitRoot, ghToken string) *httptest.Server {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		log.Fatal(err)
	}
	upstream := &githubsim.GitUpstream{
		Tokens: map[string]githubsim.GitAccess{
			ghToken: {Read: []string{"acme/widgets", "acme/other"}, Write: []string{"acme/widgets", "acme/other"}},
		},
		Public: []string{"acme/widgets", "acme/other"},
		Backend: &cgi.Handler{
			Path: gitBin,
			Args: []string{"http-backend"},
			Env: []string{
				"GIT_PROJECT_ROOT=" + gitRoot,
				"GIT_HTTP_EXPORT_ALL=1",
			},
		},
	}
	api := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		headers := map[string]string{}
		for k := range r.Header {
			headers[k] = r.Header.Get(k)
		}
		resp, err := m.Request(r.Method, r.URL.RequestURI(), headers, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(resp.Status)
		_, _ = w.Write(resp.Body)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", api)
	mux.HandleFunc("/api/v3/", api)
	mux.HandleFunc("/graphql", api)
	mux.HandleFunc("/api/graphql", api)
	mux.HandleFunc("/user/repos", api)
	mux.HandleFunc("/installation/repositories", api)
	mux.Handle("/", upstream)
	return httptest.NewServer(mux)
}

func writeFakeAgyScript(path, shellBody, finalText string) {
	const conv = "walkthrough-conv"
	mk := func(v map[string]any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	events := []string{
		mk(map[string]any{"event": "init", "conversation_id": conv, "init": map[string]any{"tools": []string{}, "permission_mode": "always-proceed"}}),
		mk(map[string]any{"event": "step_update", "step_update": map[string]any{"conversation_id": conv, "step_index": 0, "step_type": "agent_response", "state": "ACTIVE", "text_delta": finalText}}),
		mk(map[string]any{"event": "step_update", "step_update": map[string]any{"conversation_id": conv, "step_index": 0, "step_type": "agent_response", "state": "DONE"}}),
		mk(map[string]any{"event": "result", "result": map[string]any{"conversation_id": conv, "status": "SUCCESS", "response": finalText, "num_turns": 1}}),
	}
	var sb strings.Builder
	sb.WriteString("#!/usr/bin/env bash\nset -euo pipefail\n")
	sb.WriteString("for arg in \"$@\"; do case \"$arg\" in --version) echo 'fake agy'; exit 0;; models) exit 0;; esac; done\n")
	sb.WriteString("IFS= read -r _ || true\n")
	sb.WriteString(shellBody + "\n")
	for _, ev := range events {
		sb.WriteString("printf '%s\\n' '" + strings.ReplaceAll(ev, "'", `'\''`) + "'\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o755); err != nil {
		log.Fatal(err)
	}
}

func main() {
	grainBin := flag.String("grain", "/home/debian/bin/grain", "path to built grain binary")
	repoRoot := flag.String("repo-root", "/home/debian/work", "grain checkout root")
	outDir := flag.String("out", "/tmp/walkthrough-out", "directory for artifacts and results")
	flag.Parse()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatal(err)
	}
	workTmp, err := os.MkdirTemp("", "grain-cli-walk-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(workTmp)

	cliHome := filepath.Join(workTmp, "cli-home")
	if err := os.MkdirAll(cliHome, 0o700); err != nil {
		log.Fatal(err)
	}
	r := &runner{
		grainBin: *grainBin,
		homeDir:  cliHome,
		report: SuiteReport{
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			GrainBinary: *grainBin,
		},
	}

	// -------------------------------------------------------------------------
	// 1. Set up two simulated GitHub repos + real `grain daemon`
	// -------------------------------------------------------------------------
	const ghToken = "ghp-walkthrough-sim-token"
	gitRoot := filepath.Join(workTmp, "gitroot")
	widgetsWorkflow := `name: tests
on:
  push:
    branches: ["**"]
  pull_request:
jobs:
  lint:
    name: lint
    steps:
      - name: Check formatting
        run: |
          echo "lint line 1: checking style"
          echo "lint line 2: clean"
  unit:
    name: unit
    steps:
      - name: Test
        run: |
          mode=$(cat MODE.txt)
          echo "=== RUN   TestWidgetParse"
          echo "unit log alpha line 0123456789"
          if [ "$mode" = "fail" ]; then
            echo "    widget_test.go:19: unexpected header"
            echo "--- FAIL: TestWidgetParse (0.01s)"
            echo "FAIL"
            exit 1
          fi
          if [ "$mode" = "slow" ]; then
            echo "sleeping step part 1"
            sleep 2
            echo "sleeping step part 2"
            sleep 12
          fi
          if [ "$mode" = "grow" ]; then
            echo "grow line 1"
            sleep 2
            echo "grow line 2"
            sleep 2
            echo "grow line 3 final"
          fi
          echo "--- PASS: TestWidgetParse (0.01s)"
          echo "PASS"
`
	widgetsBare, simWidgets := setupGitRepo(gitRoot, "acme", "widgets", widgetsWorkflow, map[string]map[string]string{
		"feat/green": {"MODE.txt": "pass\n"},
		"feat/red":   {"MODE.txt": "fail\n"},
		"feat/slow":  {"MODE.txt": "slow\n"},
		"feat/grow":  {"MODE.txt": "grow\n"},
		"feat/mq-w":  {"MODE.txt": "pass\n", "MQ.txt": "widgets-queued\n"},
	})
	otherBare, simOther := setupGitRepo(gitRoot, "acme", "other", "", map[string]map[string]string{
		"feat/green":     {"MODE.txt": "pass\n"},
		"feat/red":       {"MODE.txt": "fail\n"},
		"feat/cancelled": {"MODE.txt": "cancel\n"},
		"feat/empty":     {"MODE.txt": "empty\n"},
		"feat/mq-o":      {"MODE.txt": "pass\n", "MQ.txt": "other-queued\n"},
	})
	_ = widgetsBare

	reachable := []githubsim.ReachableRepo{
		{FullName: "acme/widgets", Push: true},
		{FullName: "acme/other", Push: true},
	}
	simWidgets.Reachable = map[string][]githubsim.ReachableRepo{ghToken: reachable}
	simOther.Reachable = map[string][]githubsim.ReachableRepo{ghToken: reachable}

	shaFor := func(bare, ref string) string {
		return strings.TrimSpace(mustRun(bare, "git", "rev-parse", ref))
	}
	tBool := true
	now := time.Now().UTC()

	// Seed PRs and GitHub Actions runs on acme/other (ci_mode: github) and PRs on acme/widgets.
	simWidgets.PullRequests = []githubsim.PullRequest{
		{Number: 501, Title: "Widgets green PR", Head: "feat/green", Base: "main", State: "open", Mergeable: &tBool, CreatedAt: now, HTMLURL: "https://github.com/acme/widgets/pull/501"},
		{Number: 502, Title: "Widgets merge-queue PR", Head: "feat/mq-w", Base: "main", State: "open", Mergeable: &tBool, CreatedAt: now, HTMLURL: "https://github.com/acme/widgets/pull/502"},
	}
	otherGreenSHA := shaFor(otherBare, "feat/green")
	otherRedSHA := shaFor(otherBare, "feat/red")
	otherCancSHA := shaFor(otherBare, "feat/cancelled")
	otherEmptySHA := shaFor(otherBare, "feat/empty")
	otherMQSHA := shaFor(otherBare, "feat/mq-o")

	simOther.PullRequests = []githubsim.PullRequest{
		{Number: 9001, Title: "Other Actions green PR", Head: "feat/green", Base: "main", State: "open", Mergeable: &tBool, CreatedAt: now, HTMLURL: "https://github.com/acme/other/pull/9001"},
		{Number: 9002, Title: "Other Actions red PR", Head: "feat/red", Base: "main", State: "open", Mergeable: &tBool, CreatedAt: now, HTMLURL: "https://github.com/acme/other/pull/9002"},
		{Number: 9003, Title: "Other merge-queue PR", Head: "feat/mq-o", Base: "main", State: "open", Mergeable: &tBool, CreatedAt: now, HTMLURL: "https://github.com/acme/other/pull/9003"},
	}
	strPtr := func(s string) *string { return &s }
	simOther.CheckRuns = map[string][]github.CheckRun{
		otherGreenSHA: {{ID: 1, Name: "build", Status: "completed", Conclusion: strPtr("success"), App: model.GitHubActionsApp}},
		otherRedSHA:   {{ID: 2, Name: "build", Status: "completed", Conclusion: strPtr("failure"), App: model.GitHubActionsApp}},
		otherMQSHA:    {{ID: 3, Name: "build", Status: "in_progress", App: model.GitHubActionsApp}},
	}
	simOther.WorkflowRuns = []githubsim.WorkflowRun{
		{
			ID: 101, Name: "CI", Path: ".github/workflows/ci.yml",
			HeadBranch: "feat/green", HeadSHA: otherGreenSHA, Event: "pull_request",
			Status: "completed", Conclusion: "success", RunAttempt: 1, CreatedAt: now.Add(-3 * time.Minute),
			Jobs: []githubsim.WorkflowJob{{Name: "build", Conclusion: "success", Log: "building...\nPASS\n"}},
		},
		{
			ID: 102, Name: "CI", Path: ".github/workflows/ci.yml",
			HeadBranch: "feat/red", HeadSHA: otherRedSHA, Event: "pull_request",
			Status: "completed", Conclusion: "failure", RunAttempt: 1, CreatedAt: now.Add(-2 * time.Minute),
			Jobs: []githubsim.WorkflowJob{
				{Name: "lint", Conclusion: "success", Log: "lint ok\n"},
				{Name: "build", Conclusion: "failure", FailingStep: "Run tests", Log: "--- FAIL: TestOtherAction (0.02s)\nFAIL\n"},
			},
		},
		{
			ID: 103, Name: "CI", Path: ".github/workflows/ci.yml",
			HeadBranch: "feat/cancelled", HeadSHA: otherCancSHA, Event: "push",
			Status: "completed", Conclusion: "cancelled", RunAttempt: 1, CreatedAt: now.Add(-1 * time.Minute),
			Jobs: []githubsim.WorkflowJob{{Name: "build", Conclusion: "cancelled", Log: "cancelled\n"}},
		},
		{
			ID: 104, Name: "CI", Path: ".github/workflows/ci.yml",
			HeadBranch: "feat/empty", HeadSHA: otherEmptySHA, Event: "push",
			Status: "completed", Conclusion: "success", RunAttempt: 1, CreatedAt: now.Add(-30 * time.Second),
			Jobs: nil,
		},
	}

	multi := &multiRepoSim{
		sims:   map[string]*githubsim.Sim{"acme/widgets": simWidgets, "acme/other": simOther},
		defSim: simWidgets,
	}
	ghSrv := startGitHubHost(multi, gitRoot, ghToken)
	defer ghSrv.Close()
	ghHost := strings.TrimPrefix(ghSrv.URL, "http://")

	daemonHome := filepath.Join(workTmp, "daemon-home")
	if err := os.MkdirAll(daemonHome, 0o700); err != nil {
		log.Fatal(err)
	}
	ghTokFile := filepath.Join(daemonHome, "gh-token")
	mustWrite(ghTokFile, ghToken+"\n")
	daemonAddr := freeAddr()
	daemonURL := "http://" + daemonAddr
	daemonEnv := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + daemonHome,
		"XDG_CONFIG_HOME=" + filepath.Join(daemonHome, ".config"),
		"GRAIN_DATA_DIR=",
		"GRAIN_SANDBOX_CLI=0",
	}
	initCmd := exec.Command(*grainBin, "init", "workstation", "-non-interactive",
		"-repo", "acme/widgets", "-sandbox-backend", "host", "-model-backend", "claude",
		"-github-token-file", ghTokFile, "-owner", "operator@example.com",
		"-ui-addr", daemonAddr, "-start-service=false")
	initCmd.Env = daemonEnv
	if out, err := initCmd.CombinedOutput(); err != nil {
		log.Fatalf("grain init workstation: %v\n%s", err, out)
	}
	dataDir := filepath.Join(daemonHome, ".grain")
	if err := staterepo.SaveSettings(dataDir, staterepo.Settings{DirectConfigWrites: true}); err != nil {
		log.Fatal(err)
	}
	var daemonLogs bytes.Buffer
	daemonCmd := exec.Command(filepath.Join(dataDir, "daemon.sh"),
		"-github-host", ghHost, "-github-insecure-http", "-sandbox-port-base=0", "-poll-interval", "1s")
	daemonCmd.Env = daemonEnv
	daemonCmd.Stdout = &daemonLogs
	daemonCmd.Stderr = &daemonLogs
	daemonCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := daemonCmd.Start(); err != nil {
		log.Fatal(err)
	}
	defer func() {
		_ = syscall.Kill(-daemonCmd.Process.Pid, syscall.SIGTERM)
		_ = daemonCmd.Wait()
	}()

	ctx := context.Background()
	opClient := ui.NewHTTPClient(daemonURL)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if time.Now().After(deadline) {
			log.Fatalf("daemon did not become ready:\n%s", daemonLogs.String())
		}
		if b, err := os.ReadFile(ui.LocalAPITokenPath(dataDir)); err == nil && strings.TrimSpace(string(b)) != "" {
			opClient.Token = strings.TrimSpace(string(b))
			if _, err := opClient.GetSettings(ctx); err == nil {
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	opTok := opClient.Token
	log.Printf("Live grain daemon ready at %s", daemonURL)

	// Seed PR-linked in-review tasks (101..105) and memory:// rows into the
	// initialized WAL SQLite store now that daemon.sh has imported state-repo.
	{
		seedDB, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(dataDir, "store")))
		if err != nil {
			log.Fatalf("opening store for seed: %v", err)
		}
		seedStore := model.New(seedDB)
		putSeedTask := func(id, owner, name, branch, prRef, title string, autoMerge bool) {
			target := model.RepoRef{Owner: owner, Name: name}
			ts := now
			tk := model.Task{
				ID:         id,
				Intent:     model.IntentImplement,
				Title:      title,
				Body:       title,
				Target:     &target,
				Base:       "main",
				Branch:     branch,
				Approval:   &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"}},
				ApprovedAt: &ts,
				AutoMerge:  autoMerge,
				Links:      []model.Link{{Kind: model.LinkFixes, Target: prRef}},
				CreatedAt:  &ts,
			}
			if err := seedStore.PutTask(ctx, tk); err != nil {
				log.Fatalf("PutTask %s: %v", id, err)
			}
			if err := seedStore.ObserveField(ctx, id, ts, func(o *model.Observation) {
				o.CompletedAt = &ts
			}); err != nil {
				log.Fatalf("ObserveField %s: %v", id, err)
			}
		}
		putSeedTask("101", "acme", "widgets", "feat/green", "acme/widgets#501", "Track widgets#501", false)
		putSeedTask("102", "acme", "widgets", "feat/mq-w", "acme/widgets#502", "Track widgets#502 in merge queue", true)
		putSeedTask("103", "acme", "other", "feat/green", "acme/other#9001", "Track other#9001", false)
		putSeedTask("104", "acme", "other", "feat/red", "acme/other#9002", "Track other#9002", false)
		putSeedTask("105", "acme", "other", "feat/mq-o", "acme/other#9003", "Track other#9003 in merge queue", true)

		byOp := model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"}
		wRef := model.RepoRef{Owner: "acme", Name: "widgets"}
		oRef := model.RepoRef{Owner: "acme", Name: "other"}
		for _, m := range []model.Memory{
			{Repo: nil, Text: "# Global Memory\nShared deployment note.\n", UpdatedAt: now, UpdatedBy: byOp},
			{Repo: &wRef, Text: "# Widgets Memory\nWidgets repo summary line.\n", UpdatedAt: now, UpdatedBy: byOp},
			{Repo: &oRef, Text: "# Other Memory\nSecret other-repo memory note.\n", UpdatedAt: now, UpdatedBy: byOp},
		} {
			if err := seedStore.PutMemory(ctx, m); err != nil {
				log.Fatalf("PutMemory: %v", err)
			}
		}
		for _, d := range []model.MemoryDocument{
			{Repo: &wRef, Name: "arch", Summary: "Widgets architecture", Text: "# Widgets Architecture\nWidgets detailed doc.\n", UpdatedAt: now, UpdatedBy: byOp},
			{Repo: &oRef, Name: "notes", Summary: "Other confidential notes", Text: "# Other Subdoc\nConfidential other subdoc.\n", UpdatedAt: now, UpdatedBy: byOp},
		} {
			if err := seedStore.PutMemoryDocument(ctx, d); err != nil {
				log.Fatalf("PutMemoryDocument: %v", err)
			}
		}
		_ = seedDB.Close()
	}

	// Enable externalAgentApi + githubHost + attach acme/other on GitHub Actions CI + acme/widgets on grain CI.
	extAPI := true
	insecureGH := true
	if _, err := opClient.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		ExternalAgentAPI:   &extAPI,
		GitHubHost:         &ghHost,
		GitHubInsecureHTTP: &insecureGH,
	}); err != nil {
		log.Fatalf("UpdateSettings: %v", err)
	}
	if _, err := opClient.AddTargetRepo(ctx, "acme/widgets"); err != nil {
		log.Fatalf("AddTargetRepo acme/widgets: %v", err)
	}
	if _, err := opClient.AddTargetRepo(ctx, "acme/other"); err != nil {
		log.Fatalf("AddTargetRepo acme/other: %v", err)
	}
	if _, err := opClient.SetDeploymentRepoGitConnection(ctx, "acme/other", "default"); err != nil {
		log.Printf("SetDeploymentRepoGitConnection acme/other (optional): %v", err)
	}
	if _, err := opClient.SetRepoCIMode(ctx, "acme/widgets", string(model.CIModeGrain)); err != nil {
		log.Fatalf("SetRepoCIMode widgets: %v", err)
	}
	if _, err := opClient.SetRepoCIMode(ctx, "acme/other", string(model.CIModeGitHub)); err != nil {
		log.Fatalf("SetRepoCIMode other: %v", err)
	}

	// Create a task for item 2 comment test; PR-linked tasks 101..105 were pre-seeded above.
	tComment, err := opClient.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Comment flag order test task", Description: "Used for item 2 comment test.",
		Repo: "acme/widgets", HoldForApproval: true,
	})
	if err != nil {
		log.Fatalf("CreateTask comment: %v", err)
	}
	// Let the daemon's 1s reconcile cycle sync pull request observations & merge queue state.
	time.Sleep(2500 * time.Millisecond)

	// Start real grain CI runs on acme/widgets: green, red, cancelled, slow!
	startRun := func(ref string) ci.Run {
		sha := shaFor(widgetsBare, ref)
		run, err := opClient.StartCIRun(ctx, ui.CIRunRequest{
			Repo: "acme/widgets", Ref: ref, Commit: sha, Event: string(ci.EventPullRequest), Recheck: true,
		})
		if err != nil {
			log.Fatalf("StartCIRun %s: %v", ref, err)
		}
		return run
	}
	greenRun := startRun("feat/green")
	redRun := startRun("feat/red")
	cancelTarget := startRun("feat/slow")
	time.Sleep(500 * time.Millisecond)
	if _, err := opClient.CancelCIRun(ctx, cancelTarget.ID, "walkthrough test"); err != nil {
		log.Fatalf("CancelCIRun: %v", err)
	}

	// Wait for greenRun and redRun to finish on the daemon's real host CI runner.
	waitDone := func(id string) ci.Run {
		dl := time.Now().Add(45 * time.Second)
		for time.Now().Before(dl) {
			got, err := opClient.CIRun(ctx, id)
			if err == nil && got.Conclusion.Done() {
				return got
			}
			time.Sleep(250 * time.Millisecond)
		}
		log.Fatalf("CI run %s did not finish in 45s", id)
		return ci.Run{}
	}
	greenRun = waitDone(greenRun.ID)
	redRun = waitDone(redRun.ID)
	cancelTarget = waitDone(cancelTarget.ID)
	log.Printf("Grain CI runs settled: green=%s(%s) red=%s(%s) cancelled=%s(%s)",
		greenRun.ID, greenRun.Conclusion, redRun.ID, redRun.Conclusion, cancelTarget.ID, cancelTarget.Conclusion)

	// -------------------------------------------------------------------------
	// 2. Start `grain demo` + `iapsim` + TLS mismatch server
	// -------------------------------------------------------------------------
	demoAddr := freeAddr()
	demoURL := "http://" + demoAddr
	demoAccessFile := filepath.Join(workTmp, "demo-access-code")
	var demoLogs bytes.Buffer
	demoCmd := exec.Command(*grainBin, "demo", "-open=false", "-addr", demoAddr,
		"-github-sim", "-fake-ci", "-scheduled-workflows", "-merge-queue-states", "-git-proxy",
		"-reconcile", "2s", "-access-code-file", demoAccessFile)
	demoCmd.Env = append(daemonEnv, "TMPDIR="+workTmp)
	demoCmd.Stdout = &demoLogs
	demoCmd.Stderr = &demoLogs
	if err := demoCmd.Start(); err != nil {
		log.Fatal(err)
	}
	defer func() { _ = demoCmd.Process.Kill(); _ = demoCmd.Wait() }()
	for i := 0; i < 100; i++ {
		if resp, err := http.Get(demoURL + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	demoTokBytes, err := os.ReadFile(demoAccessFile)
	if err != nil {
		log.Fatalf("reading demo access code: %v\n%s", err, demoLogs.String())
	}
	demoTok := strings.TrimSpace(string(demoTokBytes))

	// Build and start iapsim + IAP-configured demo for item 1 (exit 3 on IAP sign-in HTML page).
	iapSimBin := filepath.Join(workTmp, "iapsim")
	mustRun(*repoRoot, "go", "build", "-o", iapSimBin, "./tests/iap/iapsim.go")
	iapIssuerAddr := freeAddr()
	iapProxyAddr := freeAddr()
	iapDemoAddr := freeAddr()
	const iapAud = "/projects/123/global/backendServices/456"
	const iapIss = "https://iap.test.local"
	iapSimCmd := exec.Command(iapSimBin,
		"-issuer", iapIssuerAddr, "-proxy", iapProxyAddr,
		"-upstream", "http://"+iapDemoAddr, "-aud", iapAud, "-iss", iapIss)
	if err := iapSimCmd.Start(); err != nil {
		log.Fatal(err)
	}
	defer func() { _ = iapSimCmd.Process.Kill(); _ = iapSimCmd.Wait() }()

	iapDemoAccess := filepath.Join(workTmp, "iap-demo-access")
	iapDemoCmd := exec.Command(*grainBin, "demo", "-open=false", "-addr", iapDemoAddr,
		"-as", "owner@example.com", "-access-code-file", iapDemoAccess,
		"-ui-iap-audience", iapAud, "-ui-iap-issuer", iapIss,
		"-ui-iap-keys-url", "http://"+iapIssuerAddr+"/jwks")
	iapDemoCmd.Env = append(daemonEnv, "TMPDIR="+workTmp)
	if err := iapDemoCmd.Start(); err != nil {
		log.Fatal(err)
	}
	defer func() { _ = iapDemoCmd.Process.Kill(); _ = iapDemoCmd.Wait() }()
	for i := 0; i < 100; i++ {
		if resp, err := http.Get("http://" + iapDemoAddr + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Self-signed TLS reverse proxy in front of the live daemon (for TLS mismatch checks).
	daemonTargetURL, _ := url.Parse(daemonURL)
	tlsProxy := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(daemonTargetURL))
	defer tlsProxy.Close()

	// Closed loopback port for wrong-port check.
	unusedPortAddr := freeAddr()

	// Mint scoped gma_ tokens on the live daemon via POST /api/external-agents/tokens!
	mintToken := func(name string, scopes []string, scopeSet string, repos []string, expiresAt string) string {
		body := map[string]any{
			"name":          name,
			"scopes":        scopes,
			"scopeSet":      scopeSet,
			"repos":         repos,
			"rateLimit":     100000,
			"maxConcurrent": 100,
		}
		if expiresAt != "" {
			body["expiresAt"] = expiresAt
		}
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, daemonURL+"/api/external-agents/tokens", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+opTok)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Fatalf("mint %s: %v", name, err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode >= 300 {
			log.Fatalf("mint %s: %d %s", name, res.StatusCode, string(b))
		}
		var out struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal(b, &out)
		return out.Token
	}

	tokCIWidgets := mintToken("ci-widgets", []string{"ci:read"}, "", []string{"acme/widgets"}, "")
	tokMemWidgets := mintToken("mem-widgets", []string{"memory:read", "tasks:read"}, "", []string{"acme/widgets"}, "")
	tokMemBoth := mintToken("mem-both", []string{"memory:read"}, "", []string{"acme/widgets", "acme/other"}, "")
	tokExtDev := mintToken("ext-dev", nil, "grain/external-dev", nil, "")
	tokDeployNoSrc := mintToken("deploy-nosrc", []string{"deployment:read"}, "", nil, "")
	tokDeployWithSrc := mintToken("deploy-src", []string{"deployment:read", "deployment:src"}, "", nil, "")
	tokExpired := mintToken("expired-tok", []string{"tasks:read"}, "", nil, time.Now().UTC().Add(1200*time.Millisecond).Format(time.RFC3339Nano))
	time.Sleep(1500 * time.Millisecond)

	// =========================================================================
	// ITEM 1: Exit-code table 0..5 against live daemon + -json error validation
	// =========================================================================
	srvEnv := []string{"GRAIN_SERVER=" + daemonURL, "GRAIN_TOKEN=" + opTok}

	// Code 0: success
	r.execGrain("1-exit-codes", "exit 0: grain list (text)", "grain list", 0, false, srvEnv, "list")
	r.execGrain("1-exit-codes", "exit 0: grain list -json", "grain list -json", 0, true, srvEnv, "list", "-json")
	r.execGrain("1-exit-codes", "exit 0: grain ci wait <green>", "grain ci wait "+greenRun.ID, 0, false, srvEnv, "ci", "wait", greenRun.ID)

	// Stage >20,000 tiny files under /usr/local/share/grain/src/walkcap_fixture so
	// src:// walk-cap truncation (maxSrcWalkFiles = 20000) is exercised live with 0 matches.
	walkCapDir := "/usr/local/share/grain/src/walkcap_fixture"
	if err := os.MkdirAll(walkCapDir, 0o755); err == nil {
		defer os.RemoveAll(walkCapDir)
		for d := 0; d < 21; d++ {
			sub := filepath.Join(walkCapDir, fmt.Sprintf("d%02d", d))
			_ = os.MkdirAll(sub, 0o755)
			for i := 0; i < 1000; i++ {
				_ = os.WriteFile(filepath.Join(sub, fmt.Sprintf("f%03d.txt", i)), []byte("ok\n"), 0o644)
			}
		}
	}

	// Code 1: red run, cancelled run, truncated grep, 5xx
	r.execGrain("1-exit-codes", "exit 1: grain ci wait <red>", "grain ci wait "+redRun.ID, 1, false, srvEnv, "ci", "wait", redRun.ID)
	r.execGrain("1-exit-codes", "exit 1: grain ci wait <red> -json", "grain ci wait "+redRun.ID+" -json", 1, true, srvEnv, "ci", "wait", redRun.ID, "-json")
	r.execGrain("1-exit-codes", "exit 1: grain ci wait <cancelled>", "grain ci wait "+cancelTarget.ID, 1, false, srvEnv, "ci", "wait", cancelTarget.ID)
	r.execGrain("1-exit-codes", "exit 1: truncated walk grep with 0 matches", "grain fs grep __never_occurs_walk_cap__ src://walkcap_fixture", 1, false, srvEnv, "fs", "grep", "__never_occurs_walk_cap__", "src://walkcap_fixture")
	r.execGrain("1-exit-codes", "exit 1: truncated walk grep -json", "grain fs grep __never_occurs_walk_cap__ src://walkcap_fixture -json", 1, true, srvEnv, "fs", "grep", "__never_occurs_walk_cap__", "src://walkcap_fixture", "-json")
	// 5xx server error check against a stub 503 endpoint to verify 5xx -> exit 1 + valid JSON:
	srv503 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"the service is temporarily unavailable"}`))
	}))
	defer srv503.Close()
	r.execGrain("1-exit-codes", "exit 1: 503 server error -json", "grain -server <503-stub> list -json", 1, true, nil, "-server", srv503.URL, "-token", "tok", "list", "-json")

	// Code 2: bad flag, stray arg, unknown cmd, 404 task id, 404 run id, missing fs subcommand
	r.execGrain("1-exit-codes", "exit 2: bad flag", "grain list -no-such-flag", 2, false, srvEnv, "list", "-no-such-flag")
	r.execGrain("1-exit-codes", "exit 2: bad flag -json", "grain list -no-such-flag -json", 2, true, srvEnv, "list", "-no-such-flag", "-json")
	r.execGrain("1-exit-codes", "exit 2: stray positional arg", "grain list strayword", 2, false, srvEnv, "list", "strayword")
	r.execGrain("1-exit-codes", "exit 2: unknown command -json", "grain nosuchverb -json", 2, true, srvEnv, "nosuchverb", "-json")
	r.execGrain("1-exit-codes", "exit 2: 404 task id", "grain get 999999", 2, false, srvEnv, "get", "999999")
	r.execGrain("1-exit-codes", "exit 2: 404 task id -json", "grain get 999999 -json", 2, true, srvEnv, "get", "999999", "-json")
	r.execGrain("1-exit-codes", "exit 2: 404 CI run id -json", "grain ci show no-such-run -json", 2, true, srvEnv, "ci", "show", "no-such-run", "-json")
	r.execGrain("1-exit-codes", "exit 2: missing fs subcommand -json", "grain fs -json", 2, true, srvEnv, "fs", "-json")

	// Code 3: bad token, expired gma_ token, 403 forbidden scope, IAP sign-in HTML page
	r.execGrain("1-exit-codes", "exit 3: bad token", "grain -token gma_invalid list", 3, false, []string{"GRAIN_SERVER=" + daemonURL}, "-token", "gma_invalid", "list")
	r.execGrain("1-exit-codes", "exit 3: bad token -json", "grain -token gma_invalid list -json", 3, true, []string{"GRAIN_SERVER=" + daemonURL}, "-token", "gma_invalid", "list", "-json")
	r.execGrain("1-exit-codes", "exit 3: expired gma_ token -json", "grain -token <expired-gma> list -json", 3, true, []string{"GRAIN_SERVER=" + daemonURL}, "-token", tokExpired, "list", "-json")
	r.execGrain("1-exit-codes", "exit 3: 403 forbidden scope -json", "grain -token <ci:read> create -title x -json", 3, true, []string{"GRAIN_SERVER=" + daemonURL}, "-token", tokCIWidgets, "create", "-title", "x", "-json")
	r.execGrain("1-exit-codes", "exit 3: IAP unauthenticated /api/ (iapsim 401)", "grain -server http://"+iapProxyAddr+" list", 3, false, nil, "-server", "http://"+iapProxyAddr, "list")
	r.execGrain("1-exit-codes", "exit 3: IAP unauthenticated /api/ (iapsim 401) -json", "grain -server http://"+iapProxyAddr+" list -json", 3, true, nil, "-server", "http://"+iapProxyAddr, "list", "-json")
	iapHTMLRedirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "http://"+iapProxyAddr+"/_iapsim/login?next="+url.QueryEscape(req.URL.RequestURI()), http.StatusFound)
	}))
	defer iapHTMLRedirect.Close()
	r.execGrain("1-exit-codes", "exit 3: IAP HTML sign-in page redirect", "grain -server "+iapHTMLRedirect.URL+" list", 3, false, nil, "-server", iapHTMLRedirect.URL, "list")
	r.execGrain("1-exit-codes", "exit 3: IAP HTML sign-in page redirect -json", "grain -server "+iapHTMLRedirect.URL+" list -json", 3, true, nil, "-server", iapHTMLRedirect.URL, "list", "-json")

	// Code 4: wrong port, TLS mismatch (HTTPS->HTTP, HTTPS untrusted cert, HTTP->HTTPS)
	r.execGrain("1-exit-codes", "exit 4: wrong port", "grain -server http://"+unusedPortAddr+" -connect-timeout 600ms list", 4, false, nil, "-server", "http://"+unusedPortAddr, "-connect-timeout", "600ms", "-token", "tok", "list")
	r.execGrain("1-exit-codes", "exit 4: wrong port -json", "grain -server http://"+unusedPortAddr+" -connect-timeout 600ms list -json", 4, true, nil, "-server", "http://"+unusedPortAddr, "-connect-timeout", "600ms", "-token", "tok", "list", "-json")
	r.execGrain("1-exit-codes", "exit 4: TLS mismatch (https client to http daemon)", "grain -server https://"+daemonAddr+" -connect-timeout 1s -token <op> list", 4, false, nil, "-server", "https://"+daemonAddr, "-connect-timeout", "1s", "-token", opTok, "list")
	r.execGrain("1-exit-codes", "exit 4: TLS mismatch (https client to http daemon) -json", "grain -server https://"+daemonAddr+" -connect-timeout 1s -token <op> list -json", 4, true, nil, "-server", "https://"+daemonAddr, "-connect-timeout", "1s", "-token", opTok, "list", "-json")
	r.execGrain("1-exit-codes", "exit 4: TLS mismatch (untrusted self-signed cert)", "grain -server "+tlsProxy.URL+" -connect-timeout 1s -token <op> list", 4, false, nil, "-server", tlsProxy.URL, "-connect-timeout", "1s", "-token", opTok, "list")
	r.execGrain("1-exit-codes", "exit 4: TLS mismatch (untrusted self-signed cert) -json", "grain -server "+tlsProxy.URL+" -connect-timeout 1s -token <op> list -json", 4, true, nil, "-server", tlsProxy.URL, "-connect-timeout", "1s", "-token", opTok, "list", "-json")
	httpToTLS := "http://" + strings.TrimPrefix(tlsProxy.URL, "https://")
	r.execGrain("1-exit-codes", "exit 4: TLS mismatch (http client to https server)", "grain -server "+httpToTLS+" -connect-timeout 1s -token <op> list", 4, false, nil, "-server", httpToTLS, "-connect-timeout", "1s", "-token", opTok, "list")
	r.execGrain("1-exit-codes", "exit 4: TLS mismatch (http client to https server) -json", "grain -server "+httpToTLS+" -connect-timeout 1s -token <op> list -json", 4, true, nil, "-server", httpToTLS, "-connect-timeout", "1s", "-token", opTok, "list", "-json")

	// Code 5: ci wait -timeout 5s on a slow run
	slowRun := startRun("feat/slow")
	r.execGrain("1-exit-codes", "exit 5: ci wait -timeout 5s on slow run", "grain ci wait "+slowRun.ID+" -timeout 5s", 5, false, srvEnv, "ci", "wait", slowRun.ID, "-timeout", "5s")
	slowRun2 := startRun("feat/slow")
	r.execGrain("1-exit-codes", "exit 5: ci wait -timeout 5s -json on slow run", "grain ci wait "+slowRun2.ID+" -timeout 5s -json", 5, false, srvEnv, "ci", "wait", slowRun2.ID, "-timeout", "5s", "-json")
	_, _ = opClient.CancelCIRun(ctx, slowRun.ID, "walkthrough cleanup")
	_, _ = opClient.CancelCIRun(ctx, slowRun2.ID, "walkthrough cleanup")

	// =========================================================================
	// ITEM 2: Flags after the verb & free-text body containing -as
	// =========================================================================
	r.execGrain("2-flag-positions", "grain list --json", "grain list --json", 0, true, srvEnv, "list", "--json")
	r.execGrain("2-flag-positions", "grain ci show <id> -json", "grain ci show "+greenRun.ID+" -json", 0, true, srvEnv, "ci", "show", greenRun.ID, "-json")
	r.execGrain("2-flag-positions", "grain comment <id> \"use -as carefully\"", fmt.Sprintf("grain comment %s \"use -as carefully\"", tComment.ID), 0, false, srvEnv, "comment", tComment.ID, "use -as carefully")
	r.execGrain("2-flag-positions", "grain comment <id> use -as carefully (unquoted words)", fmt.Sprintf("grain comment %s use -as carefully", tComment.ID), 0, false, srvEnv, "comment", tComment.ID, "use", "-as", "carefully")
	getRec := r.execGrain("2-flag-positions", "verify stored comment bodies", "grain get "+tComment.ID+" -json", 0, true, srvEnv, "get", tComment.ID, "-json")
	if strings.Count(getRec.Stdout, "use -as carefully") < 2 {
		r.report.Findings = append(r.report.Findings, fmt.Sprintf("[2-flag-positions] comment body did not preserve 'use -as carefully' twice: %s", getRec.Stdout))
	}

	// =========================================================================
	// ITEM 3: grain fs with scoped tokens + sandbox CLI byte-for-byte parity
	// =========================================================================
	ciWidEnv := []string{"GRAIN_SERVER=" + daemonURL, "GRAIN_TOKEN=" + tokCIWidgets}
	memWidEnv := []string{"GRAIN_SERVER=" + daemonURL, "GRAIN_TOKEN=" + tokMemWidgets}
	memBothEnv := []string{"GRAIN_SERVER=" + daemonURL, "GRAIN_TOKEN=" + tokMemBoth}
	extDevEnv := []string{"GRAIN_SERVER=" + daemonURL, "GRAIN_TOKEN=" + tokExtDev}
	depNoSrcEnv := []string{"GRAIN_SERVER=" + daemonURL, "GRAIN_TOKEN=" + tokDeployNoSrc}
	depSrcEnv := []string{"GRAIN_SERVER=" + daemonURL, "GRAIN_TOKEN=" + tokDeployWithSrc}

	// Repo-limited ci:read:
	r.execGrain("3-grain-fs", "ci:read@acme/widgets reading own repo CI log", "grain fs cat ci://acme/widgets/"+redRun.ID+"/unit/Test.log", 0, false, ciWidEnv, "fs", "cat", "ci://acme/widgets/"+redRun.ID+"/unit/Test.log")
	r.execGrain("3-grain-fs", "ci:read@acme/widgets reading other repo CI (ls)", "grain fs ls ci://acme/other", 3, false, ciWidEnv, "fs", "ls", "ci://acme/other")
	r.execGrain("3-grain-fs", "ci:read@acme/widgets reading other repo CI (cat) -json", "grain fs cat ci://acme/other/run-x/log -json", 3, true, ciWidEnv, "fs", "cat", "ci://acme/other/run-x/log", "-json")
	lsCIRoot := r.execGrain("3-grain-fs", "ci:read@acme/widgets ls ci:// narrows to acme/widgets", "grain fs ls ci://", 0, false, ciWidEnv, "fs", "ls", "ci://")
	if strings.Contains(lsCIRoot.Stdout, "other") {
		r.report.Findings = append(r.report.Findings, "[3-grain-fs] ci:read@acme/widgets ls ci:// leaked acme/other: "+lsCIRoot.Stdout)
	}

	// Repo-limited memory:read:
	r.execGrain("3-grain-fs", "memory:read@acme/widgets reading own repo memory", "grain fs cat memory://repos/acme/widgets.md", 0, false, memWidEnv, "fs", "cat", "memory://repos/acme/widgets.md")
	r.execGrain("3-grain-fs", "memory:read@acme/widgets reading own repo subdoc", "grain fs cat memory://repos/acme/widgets/arch.md", 0, false, memWidEnv, "fs", "cat", "memory://repos/acme/widgets/arch.md")
	r.execGrain("3-grain-fs", "memory:read@acme/widgets reading other repo memory (refused)", "grain fs cat memory://repos/acme/other.md", 3, false, memWidEnv, "fs", "cat", "memory://repos/acme/other.md")
	r.execGrain("3-grain-fs", "memory:read@acme/widgets reading other repo subdoc (refused)", "grain fs cat memory://repos/acme/other/notes.md", 3, false, memWidEnv, "fs", "cat", "memory://repos/acme/other/notes.md")
	// Probe memory:// root / ls / grep with repo-limited memory:read!
	memLsRepos := r.execGrain("3-grain-fs", "memory:read@acme/widgets ls memory://repos/acme", "grain fs ls memory://repos/acme", 0, false, memWidEnv, "fs", "ls", "memory://repos/acme")
	if strings.Contains(memLsRepos.Stdout, "other") {
		r.report.Findings = append(r.report.Findings, "[3-grain-fs] memory:read@acme/widgets ls memory://repos/acme leaked other: "+memLsRepos.Stdout)
	}
	memGrep := r.execGrain("3-grain-fs", "memory:read@acme/widgets grep across memory://", "grain fs grep . memory://", 0, false, memWidEnv, "fs", "grep", ".", "memory://")
	if strings.Contains(memGrep.Stdout, "other") || strings.Contains(memGrep.Stdout, "Confidential") {
		r.report.Findings = append(r.report.Findings, "[3-grain-fs] memory:read@acme/widgets grep memory:// leaked other: "+memGrep.Stdout)
	}
	memBothLs := r.execGrain("3-grain-fs", "memory:read@widgets+other ls memory://repos/acme", "grain fs ls memory://repos/acme", 0, false, memBothEnv, "fs", "ls", "memory://repos/acme")
	if !strings.Contains(memBothLs.Stdout, "widgets") || !strings.Contains(memBothLs.Stdout, "other") {
		r.report.Findings = append(r.report.Findings, fmt.Sprintf("[3-grain-fs] memory:read@widgets+other ls memory://repos/acme missing allowed repos: got %q stderr=%q", memBothLs.Stdout, memBothLs.Stderr))
	}

	// src:// refused to gma_ without deployment:src, allowed with deployment:src:
	r.execGrain("3-grain-fs", "gma_ grain/external-dev refused on src://", "grain fs ls src://", 3, false, extDevEnv, "fs", "ls", "src://")
	r.execGrain("3-grain-fs", "gma_ deployment:read (no deployment:src) refused on src:// -json", "grain fs cat src://README.md -json", 3, true, depNoSrcEnv, "fs", "cat", "src://README.md", "-json")
	r.execGrain("3-grain-fs", "gma_ deployment:read+deployment:src allowed on src://", "grain fs cat src://README.md --limit 64", 0, false, depSrcEnv, "fs", "cat", "src://README.md", "--limit", "64")

	// Byte-for-byte comparison between real sandboxed task CLI (`grain ls|read|grep`) and workstation `grain fs`:
	sbOutDir := filepath.Join(workTmp, "sandbox-captures")
	if err := os.MkdirAll(sbOutDir, 0o755); err != nil {
		log.Fatal(err)
	}
	growRun := startRun("feat/grow")
	redUnitURI := "ci://acme/widgets/" + redRun.ID + "/unit/Test.log"
	growURI := "ci://acme/widgets/" + growRun.ID + "/unit/Test.log"

	fakeAgentPath := filepath.Join(workTmp, "fake-agy-sandbox-fs")
	sbScript := fmt.Sprintf(`
HOME="$PWD"
export HOME
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_SANDBOX_CLI
BIN=%q
OUT=%q
"$BIN" ls ci://acme/widgets/"%s" > "$OUT/ls.bin"
"$BIN" read "%s?tail=45" > "$OUT/inline_tail.bin"
"$BIN" read "%s" --tail 45 > "$OUT/flag_tail.bin"
"$BIN" read "%s" --offset 8 --limit 32 > "$OUT/offset_limit.bin"
"$BIN" grep "FAIL" ci://acme/widgets/"%s" > "$OUT/grep_cmd.bin"
"$BIN" read "ci://acme/widgets/%s?grep=FAIL" > "$OUT/inline_grep.bin"
"$BIN" read "%s?follow=1" > "$OUT/follow.bin"
cp "$OUT/offset_limit.bin" "$OUT/range.bin"
touch "$OUT/done"
`, *grainBin, sbOutDir, redRun.ID, redUnitURI, redUnitURI, redUnitURI, redRun.ID, redRun.ID, growURI)
	writeFakeAgyScript(fakeAgentPath, sbScript, "Captured sandbox CLI namespace outputs.")

	backends := []model.AgentBackend{{
		ID: "fake", Name: "fake agy", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, CLIPath: fakeAgentPath, Models: []string{"fake-model"},
	}}
	if _, err := opClient.UpdateSettings(ctx, ui.UpdateSettingsRequest{AgentBackends: &backends}); err != nil {
		log.Fatalf("configuring fake agy: %v", err)
	}
	sbTask, err := opClient.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Sandbox CLI fs parity probe", Description: "Run sandbox CLI against /fs/.",
		Repo: "acme/widgets", AgentFramework: model.AgentFrameworkAntigravity,
	})
	if err != nil {
		log.Fatalf("creating sandbox probe task: %v", err)
	}
	if err := opClient.Approve(ctx, sbTask.ID); err != nil {
		log.Fatalf("approving sandbox probe task: %v", err)
	}
	// Simultaneously follow growURI from the workstation CLI while growRun is still growing!
	var extFollowOut bytes.Buffer
	extFollowDone := make(chan int, 1)
	go func() {
		cmd := exec.Command(*grainBin, "-server", daemonURL, "-token", opTok, "fs", "cat", "--follow", growURI)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + cliHome, "GRAIN_SANDBOX_CLI=0"}
		cmd.Stdout = &extFollowOut
		err := cmd.Run()
		code := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		extFollowDone <- code
	}()

	sbDeadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(sbDeadline) {
		if _, err := os.Stat(filepath.Join(sbOutDir, "done")); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(sbOutDir, "done")); err != nil {
		tState, _ := opClient.GetTask(ctx, sbTask.ID)
		log.Fatalf("sandbox probe task did not finish (state=%v):\n%s", tState, daemonLogs.String())
	}
	extFollowExit := <-extFollowDone

	checkParity := func(name, sbFile, sbCmd, extCmdDisplay string, extBytes []byte, extExit int) {
		sbBytes, err := os.ReadFile(filepath.Join(sbOutDir, sbFile))
		if err != nil {
			log.Fatalf("reading %s: %v", sbFile, err)
		}
		eq := bytes.Equal(sbBytes, extBytes) && extExit == 0
		rec := FSParityRecord{
			Name: name, SandboxCmd: sbCmd, ExternalCmd: extCmdDisplay,
			SandboxBytes: len(sbBytes), ExternalBytes: len(extBytes),
			ByteIdentical: eq, SandboxExit: 0, ExternalExit: extExit,
			Preview: firstLine(string(extBytes)),
		}
		r.report.FSParity = append(r.report.FSParity, rec)
		if !eq {
			r.report.Findings = append(r.report.Findings, fmt.Sprintf("[3-fs-parity] %s mismatch: sb(%d)=%q ext(%d,exit=%d)=%q",
				name, len(sbBytes), string(sbBytes), len(extBytes), extExit, string(extBytes)))
		}
	}

	extLs := r.execGrain("3-fs-parity", "parity ls", "grain fs ls ci://acme/widgets/"+redRun.ID, 0, false, srvEnv, "fs", "ls", "ci://acme/widgets/"+redRun.ID)
	checkParity("ls", "ls.bin", "grain ls ci://acme/widgets/"+redRun.ID, "grain fs ls ci://acme/widgets/"+redRun.ID, []byte(extLs.Stdout), extLs.ObservedExit)

	extInlineTail := r.execGrain("3-fs-parity", "parity ?tail", "grain fs cat '"+redUnitURI+"?tail=45'", 0, false, srvEnv, "fs", "cat", redUnitURI+"?tail=45")
	checkParity("?tail inline", "inline_tail.bin", "grain read '"+redUnitURI+"?tail=45'", "grain fs cat '"+redUnitURI+"?tail=45'", []byte(extInlineTail.Stdout), extInlineTail.ObservedExit)

	extFlagTail := r.execGrain("3-fs-parity", "parity --tail (via read alias)", "grain fs read "+redUnitURI+" --tail 45", 0, false, srvEnv, "fs", "read", redUnitURI, "--tail", "45")
	checkParity("--tail flag (read)", "flag_tail.bin", "grain read "+redUnitURI+" --tail 45", "grain fs read "+redUnitURI+" --tail 45", []byte(extFlagTail.Stdout), extFlagTail.ObservedExit)

	extOffLim := r.execGrain("3-fs-parity", "parity --offset/--limit", "grain fs cat "+redUnitURI+" --offset 8 --limit 32", 0, false, srvEnv, "fs", "cat", redUnitURI, "--offset", "8", "--limit", "32")
	checkParity("--offset/--limit", "offset_limit.bin", "grain read "+redUnitURI+" --offset 8 --limit 32", "grain fs cat "+redUnitURI+" --offset 8 --limit 32", []byte(extOffLim.Stdout), extOffLim.ObservedExit)

	extGrep := r.execGrain("3-fs-parity", "parity grep", "grain fs grep FAIL ci://acme/widgets/"+redRun.ID, 0, false, srvEnv, "fs", "grep", "FAIL", "ci://acme/widgets/"+redRun.ID)
	checkParity("grep subcommand", "grep_cmd.bin", "grain grep FAIL ci://acme/widgets/"+redRun.ID, "grain fs grep FAIL ci://acme/widgets/"+redRun.ID, []byte(extGrep.Stdout), extGrep.ObservedExit)

	extInlineGrep := r.execGrain("3-fs-parity", "parity ?grep inline", "grain fs cat 'ci://acme/widgets/"+redRun.ID+"?grep=FAIL'", 0, false, srvEnv, "fs", "cat", "ci://acme/widgets/"+redRun.ID+"?grep=FAIL")
	checkParity("?grep inline", "inline_grep.bin", "grain read 'ci://acme/widgets/"+redRun.ID+"?grep=FAIL'", "grain fs cat 'ci://acme/widgets/"+redRun.ID+"?grep=FAIL'", []byte(extInlineGrep.Stdout), extInlineGrep.ObservedExit)

	checkParity("?follow / --follow on growing log", "follow.bin", "grain read '"+growURI+"?follow=1'", "grain fs cat --follow "+growURI, extFollowOut.Bytes(), extFollowExit)

	// HTTP Range: bytes=8-39 on /api/fs/ vs sandbox proxy /fs/
	rangeReq, _ := http.NewRequest(http.MethodGet, daemonURL+"/api/fs/ci/acme/widgets/"+redRun.ID+"/unit/Test.log", nil)
	rangeReq.Header.Set("Authorization", "Bearer "+opTok)
	rangeReq.Header.Set("Range", "bytes=8-39")
	rangeRes, err := http.DefaultClient.Do(rangeReq)
	if err != nil {
		log.Fatal(err)
	}
	rangeBody, _ := io.ReadAll(rangeRes.Body)
	rangeRes.Body.Close()
	rangeExit := 0
	if rangeRes.StatusCode != http.StatusPartialContent {
		rangeExit = 1
	}
	checkParity("HTTP Range: bytes=8-39 (206 Partial Content)", "range.bin", "GET /fs/... Range: bytes=8-39", "GET /api/fs/... Range: bytes=8-39", rangeBody, rangeExit)

	// =========================================================================
	// ITEM 4: External `grain ci wait`, `retrigger`, and `test-history`
	// =========================================================================
	// 4a. Against githubsim Actions runs on acme/other (ci_mode: github):
	r.execGrain("4-ci-actions", "ci wait gha green", "grain ci wait gha:acme:other:101", 0, false, srvEnv, "ci", "wait", "gha:acme:other:101")
	r.execGrain("4-ci-actions", "ci wait gha green -json", "grain ci wait gha:acme:other:101 -json", 0, true, srvEnv, "ci", "wait", "gha:acme:other:101", "-json")
	r.execGrain("4-ci-actions", "ci wait gha red", "grain ci wait gha:acme:other:102", 1, false, srvEnv, "ci", "wait", "gha:acme:other:102")
	r.execGrain("4-ci-actions", "ci wait gha red -json", "grain ci wait gha:acme:other:102 -json", 1, true, srvEnv, "ci", "wait", "gha:acme:other:102", "-json")
	r.execGrain("4-ci-actions", "ci wait gha cancelled", "grain ci wait gha:acme:other:103", 1, false, srvEnv, "ci", "wait", "gha:acme:other:103")
	// Missing -jobs name on Actions run (with jobs vs 0 jobs) - must NOT exit 0 (green)!
	recMissAct1 := r.execGrain("4-ci-actions", "ci wait gha missing -jobs (run with jobs)", "grain ci wait gha:acme:other:101 -jobs nonexistent", 2, false, srvEnv, "ci", "wait", "gha:acme:other:101", "-jobs", "nonexistent")
	recMissAct0 := r.execGrain("4-ci-actions", "ci wait gha missing -jobs (run with 0 jobs)", "grain ci wait gha:acme:other:104 -jobs nonexistent", 2, false, srvEnv, "ci", "wait", "gha:acme:other:104", "-jobs", "nonexistent")
	if recMissAct1.ObservedExit != recMissAct0.ObservedExit {
		r.report.Findings = append(r.report.Findings, fmt.Sprintf("[4-ci-actions] inconsistent exit code for missing -jobs: run with jobs exited %d, run with 0 jobs exited %d", recMissAct1.ObservedExit, recMissAct0.ObservedExit))
	}
	// -pr and -ref resolution on githubsim Actions runs:
	r.execGrain("4-ci-actions", "ci wait -pr acme/other#9001 (Actions green)", "grain ci wait -pr acme/other#9001", 0, false, srvEnv, "ci", "wait", "-pr", "acme/other#9001")
	r.execGrain("4-ci-actions", "ci wait -pr URL acme/other/pull/9002 (Actions red)", "grain ci wait -pr https://github.com/acme/other/pull/9002", 1, false, srvEnv, "ci", "wait", "-pr", "https://github.com/acme/other/pull/9002")
	r.execGrain("4-ci-actions", "ci wait -ref feat/green -repo acme/other (Actions green)", "grain ci wait -ref feat/green -repo acme/other", 0, false, srvEnv, "ci", "wait", "-ref", "feat/green", "-repo", "acme/other")
	r.execGrain("4-ci-actions", "ci wait -ref feat/red -repo acme/other (Actions red)", "grain ci wait -ref feat/red -repo acme/other", 1, false, srvEnv, "ci", "wait", "-ref", "feat/red", "-repo", "acme/other")
	r.execGrain("4-ci-actions", "ci retrigger refused on Actions run", "grain ci retrigger gha:acme:other:102", 2, false, srvEnv, "ci", "retrigger", "gha:acme:other:102")

	// 4b. Against fake-ci runs on `grain demo -github-sim -fake-ci -scheduled-workflows`:
	demoEnv := []string{"GRAIN_SERVER=" + demoURL, "GRAIN_TOKEN=" + demoTok}
	demoClient := ui.NewHTTPClient(demoURL)
	demoClient.Token = demoTok
	if _, err := demoClient.SetRepoCIMode(ctx, "acme/widgets", string(model.CIModeGrain)); err != nil {
		log.Fatalf("demo SetRepoCIMode: %v", err)
	}
	startFakeRun := func(ref string) ci.Run {
		run, err := demoClient.StartCIRun(ctx, ui.CIRunRequest{Repo: "acme/widgets", Ref: ref, Event: "pull_request", Recheck: true})
		if err != nil {
			log.Fatalf("demo StartCIRun %s: %v", ref, err)
		}
		return run
	}
	finishFakeRun := func(id, conclusion string) {
		b, _ := json.Marshal(map[string]any{"conclusion": conclusion})
		res, err := http.Post(demoURL+"/api/e2e/fake-ci/runs/"+id+"/finish", "application/json", bytes.NewReader(b))
		if err != nil || res.StatusCode >= 300 {
			log.Fatalf("finishFakeRun %s: err=%v status=%v", id, err, res)
		}
		res.Body.Close()
	}
	fkGreen := startFakeRun("main")
	finishFakeRun(fkGreen.ID, "success")
	fkRed := startFakeRun("release/1.0")
	finishFakeRun(fkRed.ID, "failure")
	fkCancel := startFakeRun("main")
	_, _ = demoClient.CancelCIRun(ctx, fkCancel.ID, "walkthrough test")

	r.execGrain("4-ci-fakeci", "ci wait fake-ci green", "grain ci wait "+fkGreen.ID, 0, false, demoEnv, "ci", "wait", fkGreen.ID)
	r.execGrain("4-ci-fakeci", "ci wait fake-ci red", "grain ci wait "+fkRed.ID, 1, false, demoEnv, "ci", "wait", fkRed.ID)
	r.execGrain("4-ci-fakeci", "ci wait fake-ci cancelled", "grain ci wait "+fkCancel.ID, 1, false, demoEnv, "ci", "wait", fkCancel.ID)
	r.execGrain("4-ci-fakeci", "ci wait fake-ci missing -jobs", "grain ci wait "+fkGreen.ID+" -jobs no-such-job", 2, false, demoEnv, "ci", "wait", fkGreen.ID, "-jobs", "no-such-job")
	r.execGrain("4-ci-fakeci", "ci wait fake-ci -ref release/1.0 -repo acme/widgets", "grain ci wait -ref release/1.0 -repo acme/widgets", 1, false, demoEnv, "ci", "wait", "-ref", "release/1.0", "-repo", "acme/widgets")
	// Demo seeds open PR acme/widgets#104 on task 8 (grain/task-8-...): start and finish a fake-ci run on task 8's branch for -pr!
	demoTask8, err := demoClient.GetTask(ctx, "8")
	if err == nil && demoTask8.Branch != "" && demoTask8.PullRequest != "" {
		fkPR := startFakeRun(demoTask8.Branch)
		finishFakeRun(fkPR.ID, "success")
		r.execGrain("4-ci-fakeci", "ci wait fake-ci -pr "+demoTask8.PullRequest, "grain ci wait -pr "+demoTask8.PullRequest, 0, false, demoEnv, "ci", "wait", "-pr", demoTask8.PullRequest)
	}

	// 4c. Grain CI `-pr`, `-ref`, `retrigger` (reruns only failed jobs once), and `test-history`:
	r.execGrain("4-ci-grain", "ci wait grain -pr acme/widgets#501", "grain ci wait -pr acme/widgets#501", 0, false, srvEnv, "ci", "wait", "-pr", "acme/widgets#501")
	r.execGrain("4-ci-grain", "ci wait grain -ref feat/red -repo acme/widgets", "grain ci wait -ref feat/red -repo acme/widgets", 1, false, srvEnv, "ci", "wait", "-ref", "feat/red", "-repo", "acme/widgets")
	r.execGrain("4-ci-grain", "ci wait grain selective -jobs lint on red run (lint passed!)", "grain ci wait "+redRun.ID+" -jobs lint", 0, false, srvEnv, "ci", "wait", redRun.ID, "-jobs", "lint")
	r.execGrain("4-ci-grain", "ci wait grain missing -jobs", "grain ci wait "+greenRun.ID+" -jobs nonexistent", 2, false, srvEnv, "ci", "wait", greenRun.ID, "-jobs", "nonexistent")

	// Retrigger redRun (lint=success, unit=failure) -> must rerun ONLY unit once!
	retrigRec := r.execGrain("4-ci-grain", "ci retrigger <red-run> -json", "grain ci retrigger "+redRun.ID+" -json", 0, true, srvEnv, "ci", "retrigger", redRun.ID, "-json")
	var retrigRun ci.Run
	if json.Unmarshal([]byte(retrigRec.Stdout), &retrigRun) == nil && retrigRun.ID != "" {
		settled := waitDone(retrigRun.ID)
		var jobIDs []string
		for _, j := range settled.Jobs {
			jobIDs = append(jobIDs, j.JobID)
		}
		if !slices.Equal(jobIDs, []string{"unit"}) {
			r.report.Findings = append(r.report.Findings, fmt.Sprintf("[4-ci-grain] retrigger expected only [unit], got jobs %v", jobIDs))
		}
	}
	r.execGrain("4-ci-grain", "ci retrigger <green-run> refused (no failed jobs)", "grain ci retrigger "+greenRun.ID, 2, false, srvEnv, "ci", "retrigger", greenRun.ID)

	// test-history across acme/widgets runs (greenRun passed TestWidgetParse, redRun & retrigRun failed TestWidgetParse):
	thText := r.execGrain("4-ci-grain", "ci test-history TestWidgetParse (text)", "grain ci test-history TestWidgetParse -repo acme/widgets", 0, false, srvEnv, "ci", "test-history", "TestWidgetParse", "-repo", "acme/widgets")
	r.execGrain("4-ci-grain", "ci test-history TestWidgetParse -json", "grain ci test-history TestWidgetParse -repo acme/widgets -json", 0, true, srvEnv, "ci", "test-history", "TestWidgetParse", "-repo", "acme/widgets", "-json")
	if !strings.Contains(thText.Stdout, "TestWidgetParse") {
		r.report.Findings = append(r.report.Findings, "[4-ci-grain] test-history output missing TestWidgetParse: "+thText.Stdout)
	}

	// =========================================================================
	// ITEM 5: `grain merge-queue` as operator and one-repo ci:read, text & -json
	// =========================================================================
	// Wait a reconcile tick so adopted PRs appear in pipeline/merge-queue state:
	time.Sleep(2500 * time.Millisecond)
	mqOpText := r.execGrain("5-merge-queue", "operator merge-queue (text)", "grain merge-queue", 0, false, srvEnv, "merge-queue")
	mqOpJSON := r.execGrain("5-merge-queue", "operator merge-queue -json", "grain merge-queue -json", 0, true, srvEnv, "merge-queue", "-json")
	mqScopedText := r.execGrain("5-merge-queue", "ci:read@acme/widgets merge-queue (text)", "grain merge-queue", 0, false, ciWidEnv, "merge-queue")
	mqScopedJSON := r.execGrain("5-merge-queue", "ci:read@acme/widgets merge-queue -json", "grain merge-queue -json", 0, true, ciWidEnv, "merge-queue", "-json")
	mqScopedWiden := r.execGrain("5-merge-queue", "ci:read@acme/widgets merge-queue -repo acme/other (cannot widen)", "grain merge-queue -repo acme/other -json", 0, true, ciWidEnv, "merge-queue", "-repo", "acme/other", "-json")
	if strings.Contains(mqScopedText.Stdout, "acme/other") || strings.Contains(mqScopedJSON.Stdout, "acme/other") ||
		strings.Contains(mqScopedWiden.Stdout, "9003") || strings.Contains(mqScopedWiden.Stdout, "105") || strings.Contains(mqScopedWiden.Stdout, "waiting_ci") {
		r.report.Findings = append(r.report.Findings, "[5-merge-queue] ci:read@acme/widgets leaked acme/other in merge-queue!")
	}
	_ = mqOpText
	_ = mqOpJSON
	// Also check `grain pr status` against the live daemon (PullRequestGate):
	r.execGrain("4-ci-grain", "pr status acme/widgets#501 on live daemon", "grain pr status acme/widgets#501", 0, false, srvEnv, "pr", "status", "acme/widgets#501")
	// Also check `grain fs` on `grain demo -git-proxy`:
	r.execGrain("5-merge-queue", "grain demo -git-proxy fs ls ci://", "grain -server <demo> fs ls ci://", 0, false, demoEnv, "fs", "ls", "ci://")
	r.execGrain("5-merge-queue", "grain demo -merge-queue-states merge-queue", "grain -server <demo> merge-queue", 0, false, demoEnv, "merge-queue")

	// =========================================================================
	// ITEM 6: `grain health` on the demo (text ordering and -json)
	// =========================================================================
	hText := r.execGrain("6-health", "grain health on demo (text)", "grain health", 0, false, demoEnv, "health")
	r.execGrain("6-health", "grain health on demo -json", "grain health -json", 0, true, demoEnv, "health", "-json")
	// Verify counter keys in text output are sorted:
	var seenKeys []string
	for _, line := range strings.Split(hText.Stdout, "\n") {
		if strings.HasPrefix(line, "  ") && !strings.Contains(line, "(none yet)") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				seenKeys = append(seenKeys, fields[0])
			}
		}
	}
	if len(seenKeys) == 0 || !slices.IsSorted(seenKeys) {
		r.report.Findings = append(r.report.Findings, fmt.Sprintf("[6-health] health counter keys not sorted or empty: %v", seenKeys))
	}

	// Write JSON report to outDir!
	reportBytes, _ := json.MarshalIndent(r.report, "", "  ")
	if err := os.WriteFile(filepath.Join(*outDir, "results.json"), reportBytes, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("Walkthrough finished: %d commands run, %d fs-parity checks, %d findings.",
		len(r.report.Commands), len(r.report.FSParity), len(r.report.Findings))
	for _, f := range r.report.Findings {
		fmt.Println("FINDING:", f)
	}
}
