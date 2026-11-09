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

package e2e

// The CI shutdown waiter, end to end -- item U5 of
// docs/test-runs/2026-10-29-grit-backend-ci-shutdown.md, walked live in
// that report's addendum and kept here: a real daemon.Run with embedded
// CI and host sandboxes, a local git http-backend standing in for
// GitHub, and a stub agent whose sandbox runs the real `grain` CLI's
// `grain ci run-job` and `grain ci wait` against its own sandbox proxy.
// Stopping the daemon is cancelling Run's ctx, which is all SIGTERM does
// to it (upgrade_drain_interrupted_test.go).

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// accessDo is an authenticated request with a JSON body, failing t on
// anything but a 2xx.
func accessDo(t *testing.T, method, url, body string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(presentAccessCode(req))
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s: %d %s", method, url, resp.StatusCode, raw)
	}
}

// ciWaitAgent is the claude CLI's stand-in for this walk. Its first run
// pushes a commit to the task's branch and starts `grain ci run-job b`
// and, beside it, `grain ci wait`; a later run resumes both from the run
// ids they answered with. Each command runs in a session of its own, as
// a sandbox's run_command does on a VM the daemon's stop does not
// reach: with host sandboxes the agent is the daemon's child, and the
// interrupted run's process group is stopped with it. Each writes its
// output to <dir>/<cmd>.<attempt> and then touches <dir>/<cmd>.<attempt>.done.
func ciWaitAgent(t *testing.T, grain, sandboxDir string) stubAgent {
	t.Helper()
	agent := newStubAgent(t, 600)
	d := agent.dir
	bg := func(name, args string) string {
		out := filepath.Join(d, name)
		return fmt.Sprintf("setsid sh -c '%s %s > %s 2>&1; touch %s.done' < /dev/null > /dev/null 2>&1 &\n", grain, args, out, out)
	}
	script := `#!/bin/bash
echo start >> ` + filepath.Join(d, "starts") + `
n=$(wc -l < ` + filepath.Join(d, "starts") + `)
# Host sandboxes run the agent in the daemon's directory with its HOME;
# this run's own proxy credential is in its sandbox root. Never fall back
# to the HOME this test runs under, whose credential is not this daemon's.
creds=$(ls -t $(find ` + sandboxDir + ` -maxdepth 4 -name .git-credentials) 2>/dev/null | head -1)
[ -n "$creds" ] || { echo "no sandbox credential under ` + sandboxDir + `" >&2; exit 1; }
export HOME=$(dirname "$creds")
unset GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN GRAIN_SERVER GRAIN_LOCAL_API_TOKEN_FILE
cd "$HOME/work" || { echo "no checkout at $HOME/work" >&2; exit 1; }
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-1","name":"run_command","input":{"command":"grain ci wait"}}]}}'
if [ "$n" = 1 ]; then
  git -c user.email=a@b -c user.name=a commit -q --allow-empty -m change
  git push -q origin HEAD
  ` + bg("runjob.1", "ci run-job b") + `  sleep 2
  ` + bg("wait.1", "ci wait") + `else
  ` + bg("wait.2", `ci wait --resume $(sed -n "s/.*--resume //p" `+filepath.Join(d, "wait.1")+`)`) +
		`  ` + bg("runjob.2", `ci run-job b --resume $(sed -n "s/.*--resume //p" `+filepath.Join(d, "runjob.1")+`)`) + `fi
echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}}'
sleep 600
echo '{"type":"result","result":"done"}'
`
	mustWriteFile(t, filepath.Join(d, "claude"), script)
	return agent
}

func (s stubAgent) output(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		t.Fatalf("reading the agent's %s: %v", name, err)
	}
	return string(raw)
}

func (s stubAgent) done(name string) bool {
	_, err := os.Stat(filepath.Join(s.dir, name+".done"))
	return err == nil
}

var (
	resumeRE = regexp.MustCompile(`grain ci wait --resume (\S+)`)
	creditRE = regexp.MustCompile(`waited (\d+)s; wait credit used: (\d+)s`)
)

// A daemon stopped under a task blocked in grain ci wait and grain ci
// run-job answers both at once with the run to resume -- rather than
// leaving them to the 10s server bound or a dropped connection -- and the
// next daemon, having adopted the run's jobs, carries the resumed waits
// on to the verdict. The run-job, superseded by the full run the wait
// started, follows it and is answered by its own job; and the two waits,
// blocked side by side, are credited for the time they waited once.
func TestACIWaitAcrossADaemonRestartResumesToTheVerdict(t *testing.T) {
	if testing.Short() {
		t.Skip("walks a CI run across a daemon restart")
	}
	grain := buildGrainCLI(t)
	dataDir, sandboxDir := t.TempDir(), t.TempDir()
	agent := ciWaitAgent(t, grain, sandboxDir)

	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, "acme", "widgets.git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seed := filepath.Join(t.TempDir(), "seed")
	run(t, upstream, "git", "clone", "-q", bare, seed)
	// Each job runs until the test opens its gate, after the restart,
	// rather than for a fixed time: on a loaded runner the walk up to the
	// stop can take longer than any fixed job would, and a job that
	// finished first would answer the waits before the daemon stopped
	// under them. The jobs are host sandbox steps, so they see the
	// test's own directory. They also give up once that directory is
	// gone, so a step runner the test leaves behind does not outlive it.
	gateDir := t.TempDir()
	gate := filepath.Join(gateDir, "release")
	t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o644) })
	step := fmt.Sprintf(`until [ -e %s ] || [ ! -d %s ]; do sleep 0.1; done`, gate, gateDir)
	mustWriteFile(t, filepath.Join(seed, ".grain", "workflows", "ci.yml"), `name: ci
on: [push]
jobs:
  a:
    capabilities: []
    steps:
      - run: '`+step+`'
  b:
    capabilities: []
    steps:
      - run: '`+step+`'
`)
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "-c", "user.email=s@e", "-c", "user.name=s", "commit", "-q", "-m", "workflow")
	run(t, seed, "git", "push", "-q", "origin", "HEAD:main")
	githost := httptest.NewServer(gitHTTPBackend(upstream))
	t.Cleanup(githost.Close)

	// daemonConfig's own, with the git host set before the config row is
	// seeded from it: the stored row, not the flag, is what a start reads.
	cfg := daemon.Config{
		DataDir: dataDir, SandboxDir: sandboxDir,
		UiAddr:     "127.0.0.1:" + strconv.Itoa(freeLoopbackPort(t)),
		ClaudePath: filepath.Join(agent.dir, "claude"),
		MaxWorkers: 2, PollInterval: time.Second,
		GithubHost: strings.TrimPrefix(githost.URL, "http://"), GithubInsecureHTTP: true,
		// Embedded CI, whose jobs run in host sandboxes: `grain daemon
		// -sandbox-backend host`, as the walk ran it.
		Ci: true, HostSandboxes: true,
	}
	seedConnections(t, cfg, nil,
		[]model.AgentBackend{{ID: "claude", Framework: model.AgentFrameworkClaude, Models: []string{"claude-sonnet-5"}}},
		map[string]string{"claude": "sk-ant-oat01-stub"})

	// The repository on grain CI with a git token, which the daemon takes
	// up at its next start.
	setup := startDaemon(t, cfg)
	accessDo(t, http.MethodPost, setup.base+"/api/repos", `{"repo":"acme/widgets"}`)
	accessDo(t, http.MethodPut, setup.base+"/api/repos/acme/widgets/ci-mode", `{"ciMode":"grain"}`)
	accessDo(t, http.MethodPut, setup.base+"/api/github-tokens/default", `{"value":"ghp_stub"}`)
	setup.stop(t)

	first := startDaemon(t, cfg)
	c := ui.NewHTTPClient(first.base)
	if _, err := c.CreateTask(context.Background(), ui.CreateTaskRequest{
		Title: "wait across a restart", Description: "the stub agent waits on CI",
		Approved: true, Repo: "acme/widgets",
	}); err != nil {
		t.Fatalf("filing the task: %v", err)
	}
	waitUntil(t, "the agent's grain ci wait to start", 90*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(agent.dir, "wait.1"))
		return err == nil
	})
	started := time.Now()
	// The wait.1 file exists as soon as the shell redirects into it, not
	// once grain ci wait has started the full run that supersedes the
	// run-job's; on a loaded runner the stop below could land between the
	// two, and the run-job would answer with its own run rather than
	// following the full one. So wait for the supersession itself.
	//
	// Neither this wait nor the next is what the walk is about, and the
	// jobs hold until the gate opens, so both are bounded generously: on a
	// go e2e runner shared with tests/deploy under -race, starting the
	// full run and its two sandboxes took longer than the 10s and 12s
	// these were once held to.
	var supersededRunID string
	waitUntil(t, "the full run to supersede the run-job's", 90*time.Second, func() bool {
		runs, _, err := c.ListCIRuns(context.Background(), "acme/widgets")
		if err != nil {
			return false
		}
		for _, r := range runs {
			if id, ok := ci.SupersedingRun(r); ok {
				supersededRunID = id
				return true
			}
		}
		return false
	})
	waitStopStart := time.Now()
	waitUntil(t, "the superseding run's jobs to start their detached step runners", 90*time.Second, func() bool {
		runs, _, err := c.ListCIRuns(context.Background(), "acme/widgets")
		if err != nil {
			return false
		}
		for _, r := range runs {
			if r.ID != supersededRunID {
				continue
			}
			startedJobs := 0
			for _, j := range r.Jobs {
				if j.Sandbox == "" {
					continue
				}
				matches, _ := filepath.Glob(filepath.Join(daemon.HostSandboxRunsDir(cfg), j.Sandbox, ".grain-ci-step", "*", "pid"))
				for _, pidPath := range matches {
					if pidBytes, err := os.ReadFile(pidPath); err == nil && strings.TrimSpace(string(pidBytes)) != "" {
						startedJobs++
						break
					}
				}
			}
			return startedJobs >= 2
		}
		return false
	})
	if rem := 4*time.Second - time.Since(waitStopStart); rem > 0 {
		time.Sleep(rem)
	}
	if agent.done("wait.1") || agent.done("runjob.1") {
		t.Fatalf("a wait ended before the daemon stopped: wait %q, run-job %q",
			agent.output(t, "wait.1"), agent.output(t, "runjob.1"))
	}

	// (1) Both answer "running" with the run to resume, at once.
	stopped := time.Now()
	first.stop(t)
	waitUntil(t, "both waits to answer the shutdown", 5*time.Second, func() bool {
		return agent.done("wait.1") && agent.done("runjob.1")
	})
	answered := time.Now()
	t.Logf("both waits answered %s after the stop began", answered.Sub(stopped).Round(time.Millisecond))
	wait1, runjob1 := agent.output(t, "wait.1"), agent.output(t, "runjob.1")
	m := resumeRE.FindStringSubmatch(wait1)
	if !strings.Contains(wait1, "is still running") || m == nil {
		t.Fatalf("grain ci wait across the stop answered %q, want still running with a run to resume", wait1)
	}
	runID := m[1]
	if r := resumeRE.FindStringSubmatch(runjob1); r == nil || r[1] != runID {
		t.Errorf("grain ci run-job b across the stop answered %q, want it following the full run %s", runjob1, runID)
	}

	// (3) The interrupted run's ledger holds its two overlapping waits
	// once: no more than the time from the first (run-job, started 2s
	// before the wait) starting to their answer. Counted per wait it would
	// be about twice that. Each wait is credited up to the moment it
	// answers, and on a loaded runner that comes seconds after the stop
	// began, so the bound runs to the answer rather than to the stop.
	store := readStore(t, dataDir)
	credits, err := store.RunWaitCredits(context.Background())
	if err != nil || len(credits) != 1 {
		t.Fatalf("wait credit after the stop = %+v, %v; want the interrupted run's", credits, err)
	}
	if limit := answered.Sub(started) + 2*time.Second + time.Second; credits[0].Blocked > limit {
		t.Errorf("interrupted run %s blocked %s for two overlapping waits, want no more than ~%s",
			credits[0].RunID, credits[0].Blocked, limit)
	}

	// (2) The next start re-runs the task, whose resumed waits report the
	// verdict once the adopted jobs finish.
	cfg.UiAddr = strings.TrimPrefix(first.base, "http://")
	startDaemon(t, cfg)
	// The jobs finish once both resumed waits have been started against
	// the restarted daemon, so it is the adopted jobs' verdict they carry.
	// The two are left blocked side by side for a while first, so that
	// their overlap being credited once is told apart from it being
	// credited per wait by more than the seconds each is rounded to, or
	// than the spread between their starts on a loaded runner.
	waitUntil(t, "the agent's resumed waits to start", 90*time.Second, func() bool {
		_, werr := os.Stat(filepath.Join(agent.dir, "wait.2"))
		_, rerr := os.Stat(filepath.Join(agent.dir, "runjob.2"))
		return werr == nil && rerr == nil
	})
	time.Sleep(6 * time.Second)
	if agent.done("wait.2") || agent.done("runjob.2") {
		t.Fatalf("a resumed wait answered before the jobs finished: wait %q, run-job %q",
			agent.output(t, "wait.2"), agent.output(t, "runjob.2"))
	}
	mustWriteFile(t, gate, "")
	waitUntil(t, "the resumed waits to report", 3*time.Minute, func() bool {
		return agent.done("wait.2") && agent.done("runjob.2")
	})
	wait2, runjob2 := agent.output(t, "wait.2"), agent.output(t, "runjob.2")
	if !strings.Contains(wait2, "grain CI passed") || !strings.Contains(wait2, "run "+runID+",") || !strings.Contains(wait2, "): a, b") {
		t.Errorf("resumed grain ci wait answered %q, want run %s passed with a and b", wait2, runID)
	}
	if !strings.Contains(runjob2, "grain CI passed") || !strings.Contains(runjob2, "): b\n") {
		t.Errorf("resumed grain ci run-job b answered %q, want b passed", runjob2)
	}
	// Credited once, the run's credit is the span the two waits covered
	// together: short of the sum of what each waited by their overlap,
	// several seconds here. Credited per wait it would be that sum. Each
	// figure is rounded to the second, hence the margin.
	waited := map[string]int{}
	used := map[string]int{}
	for name, out := range map[string]string{"wait": wait2, "run-job": runjob2} {
		if m := creditRE.FindStringSubmatch(out); m != nil {
			waited[name], _ = strconv.Atoi(m[1])
			used[name], _ = strconv.Atoi(m[2])
		}
	}
	if len(used) == 2 {
		t.Logf("resumed waits: the wait waited %ds and reports %ds of credit used, the run-job %ds and %ds",
			waited["wait"], used["wait"], waited["run-job"], used["run-job"])
		both := waited["wait"] + waited["run-job"]
		for name, u := range used {
			if u+2 > both {
				t.Errorf("resumed %s reports %ds of credit used where the wait and the run-job waited %ds and %ds side by side, want their overlap counted once",
					name, u, waited["wait"], waited["run-job"])
			}
		}
	}
}
