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

package main

// proxy.go is `walk -proxy`: §1 items 4 and 5. The real
// .grain/templates/release-train.yml, filed as a task by its own
// schedule, runs in a stand-in sandbox whose only way out is grain's
// scoped git proxy (gitproxy.BuildProxy, as the daemon builds it):
//
//   - git clones and pushes go through the proxy to a scratch upstream
//     (git http-backend over the same bare repository githubsim serves);
//   - `grain ci list` / `grain ci checks` are the real grain binary in
//     sandbox mode (GRAIN_SANDBOX_PROXY), answered by the proxy's /cli
//     handlers from a ui.Client whose forge is githubsim;
//   - the task's run is the template body's one command,
//     scripts/release-train-task.sh, and then the pushes a misbehaving
//     run might try by hand.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type proxyWorld struct {
	*world
	grainBin string
	sbx      string // the sandbox's HOME
	proxyURL string
	token    string
	task     model.Task
}

func gitHTTPBackend(projectRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cmd := exec.Command("git", "http-backend")
		cmd.Env = append(os.Environ(), "GIT_PROJECT_ROOT="+projectRoot, "GIT_HTTP_EXPORT_ALL=1",
			"PATH_INFO="+r.URL.Path, "QUERY_STRING="+r.URL.RawQuery, "REQUEST_METHOD="+r.Method,
			"CONTENT_TYPE="+r.Header.Get("Content-Type"), "HTTP_CONTENT_ENCODING="+r.Header.Get("Content-Encoding"),
			"GATEWAY_INTERFACE=CGI/1.1", "SERVER_PROTOCOL=HTTP/1.1", "REMOTE_USER=walkthrough")
		cmd.Stdin = bytes.NewReader(body)
		out, err := cmd.Output()
		if err != nil {
			http.Error(w, fmt.Sprintf("git http-backend: %v", err), http.StatusInternalServerError)
			return
		}
		sep := []byte("\r\n\r\n")
		end := bytes.Index(out, sep)
		if end == -1 {
			sep = []byte("\n\n")
			end = bytes.Index(out, sep)
		}
		status := http.StatusOK
		for _, line := range strings.Split(string(out[:end]), "\n") {
			line = strings.TrimRight(line, "\r")
			key, value, _ := strings.Cut(line, ":")
			value = strings.TrimSpace(value)
			if strings.EqualFold(key, "Status") {
				if code, err := strconv.Atoi(strings.Fields(value)[0]); err == nil {
					status = code
				}
				continue
			}
			if key != "" {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write(out[end+len(sep):])
	}
}

func setupProxy(src, dir, out, grainBin string) (*proxyWorld, error) {
	src, _ = filepath.Abs(src)
	dir, _ = filepath.Abs(dir)
	_ = os.RemoveAll(dir)
	up := filepath.Join(dir, "upstream")
	bare := filepath.Join(up, owner, name+".git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		return nil, err
	}
	if out != "" {
		out, _ = filepath.Abs(out)
		_ = os.MkdirAll(out, 0o755)
	}
	os.Setenv("GIT_AUTHOR_NAME", "walkthrough")
	os.Setenv("GIT_AUTHOR_EMAIL", "walkthrough@example.com")
	os.Setenv("GIT_COMMITTER_NAME", "walkthrough")
	os.Setenv("GIT_COMMITTER_EMAIL", "walkthrough@example.com")
	w := &world{src: src, dir: dir, out: out, bare: bare, repo: model.RepoRef{Owner: owner, Name: name},
		now: time.Now().UTC().Truncate(time.Second)}
	for _, args := range [][]string{
		{"init", "-q", "--bare", "-b", "main", bare},
		{"-C", bare, "config", "http.receivepack", "true"},
		{"-C", bare, "config", "gc.autoDetach", "false"},
		{"-C", bare, "config", "maintenance.autoDetach", "false"},
	} {
		if _, err := gitOut("", args...); err != nil {
			return nil, err
		}
	}
	// main is this branch's HEAD -- the scripts and the template the task
	// runs are this change's -- and the remote's release tags.
	tags, _ := gitOut(src, "tag", "-l", "v*", "latest")
	refs := []string{"HEAD:refs/heads/main"}
	for _, t := range strings.Fields(tags) {
		if !strings.Contains(t, "-") {
			refs = append(refs, "refs/tags/"+t+":refs/tags/"+t)
		}
	}
	if _, err := gitOut(src, append([]string{"push", "-q", bare}, refs...)...); err != nil {
		return nil, err
	}
	backend := httptest.NewServer(gitHTTPBackend(up))
	backendURL, _ := url.Parse(backend.URL)

	w.sim = githubsim.New(owner, name, bare, "main")
	w.sim.CheckRunApp = app
	w.sim.Now = func() time.Time { return w.now }
	w.api = httptest.NewServer(w.sim.Handler()).URL
	w.client = github.NewClient(w.sim, appTokens{})

	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(dir, "data")))
	if err != nil {
		return nil, err
	}
	w.store = model.New(db)
	w.ctx = context.Background()
	if err := w.store.Init(w.ctx); err != nil {
		return nil, err
	}
	if err := w.store.PutRepoConfig(w.ctx, model.RepoConfig{Repo: w.repo, Declarations: true, CIMode: model.CIModeGitHub}); err != nil {
		return nil, err
	}
	if err := w.store.SetTargetRepos(w.ctx, []string{w.repo.String()}); err != nil {
		return nil, err
	}
	for _, r := range orchestrator.Reconcilers() {
		if r.Name == "declarations" {
			if err := r.Reconcile(w.ctx, orchestrator.Deps{Store: w.store, Client: w.client,
				Declarations: bareDeclarations{bare: bare}}, w.now); err != nil {
				return nil, fmt.Errorf("declarations: %w", err)
			}
		}
	}

	// The schedule files the task from the template, as it does every 20
	// minutes; its run is dispatched to sandbox-train.
	sched, err := w.store.GetSchedule(w.ctx, model.RepoDeclaredID(model.DeclaredSchedule, w.repo, "release-train"))
	if err != nil || sched == nil {
		return nil, fmt.Errorf("no release-train schedule: %v", err)
	}
	fired, err := w.store.FireScheduleNow(w.ctx, *sched, w.now, model.RepoRef{})
	if err != nil || fired.TaskID == "" {
		return nil, fmt.Errorf("firing the schedule: %+v %v", fired, err)
	}
	task, err := w.store.GetTask(w.ctx, fired.TaskID)
	if err != nil || task == nil {
		return nil, fmt.Errorf("reading task %s: %v", fired.TaskID, err)
	}
	if err := w.store.StartRun(w.ctx, model.Run{ID: task.ID + "-1", TaskID: task.ID, Sandbox: "sandbox-train", Attempt: 1, StartedAt: w.now},
		model.Limits{}); err != nil {
		return nil, err
	}

	data := filepath.Join(dir, "proxy-data")
	for p, body := range map[string]string{
		"secrets/github/credentials.json": `{"*": "anonymous"}`,
		"secrets/sandbox-tokens.json":     `{"sandbox-train": "walkthrough-sandbox-token"}`,
	} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(data, p)), 0o700)
		if err := os.WriteFile(filepath.Join(data, p), []byte(body), 0o600); err != nil {
			return nil, err
		}
	}
	if err := w.store.UpdateConfig(w.ctx, func(c *model.Config) error { c.GitCredential = "anonymous"; return nil }); err != nil {
		return nil, err
	}
	uic := ui.NewClient(ui.Config{Actions: w.client}, w.store)
	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir: data, Store: w.store, ForwardHost: backendURL.Host, ForwardTLS: false,
		Namespace: &namespace.Server{CIStore: ci.NewRunStore(0), ExternalCI: uic.ExternalCIRuns()},
	})
	if err != nil {
		return nil, err
	}
	p := &proxyWorld{world: w, grainBin: grainBin, sbx: filepath.Join(dir, "sandbox"), token: "walkthrough-sandbox-token", task: *task}
	p.proxyURL = httptest.NewServer(gitproxy.NewHandler(proxy)).URL
	_ = os.MkdirAll(p.sbx, 0o755)
	if err := mcp.ConfigureGitCredentials(p.sbx, p.proxyURL+"/"+owner+"/"+name+".git", p.token, mcp.GitIdentity{}); err != nil {
		return nil, err
	}
	w.say("setup: upstream %s via git http-backend at %s; scoped proxy at %s; githubsim REST at %s", bare, backend.URL, p.proxyURL, w.api)
	w.say("setup: schedule %s filed task %s %q from template %s", sched.ID, task.ID, task.Title, task.TemplateID)
	if push, err := w.store.GitDeclaredPush(w.ctx, "sandbox-train"); err == nil && push != nil {
		b, _ := json.Marshal(push)
		w.say("setup: the push grant sandbox-train's run holds: %s", b)
	}
	return p, nil
}

// sh runs a command in the sandbox: HOME is the sandbox's, git's only
// credential is the proxy token, and grain is the real binary in sandbox
// mode.
func (p *proxyWorld) sh(label, script string, env ...string) (int, string) {
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = p.sbx
	cmd.Env = append([]string{
		"PATH=" + filepath.Dir(p.grainBin) + ":" + os.Getenv("PATH"), "HOME=" + p.sbx,
		"GRAIN_SANDBOX_PROXY=" + p.proxyURL, "GRAIN_SANDBOX_TOKEN=" + p.token, "GRAIN=" + p.grainBin,
		"RELEASE_NOW=" + strconv.FormatInt(p.now.Unix(), 10),
	}, env...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	status := 0
	if ee, ok := err.(*exec.ExitError); ok {
		status = ee.ExitCode()
	} else if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("--- %s (exit %d): %s\n", label, status, script)
	text := strings.ReplaceAll(buf.String(), p.dir, "<scratch>")
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if l != "" {
			fmt.Println("    | " + l)
		}
	}
	return status, buf.String()
}

func proxyWalk(src, dir, out, grainBin string) {
	p, err := setupProxy(src, dir, out, grainBin)
	if err != nil {
		log.Fatal(err)
	}
	head := p.sha("main")
	p.green("main", head)
	p.sim.WorkflowRuns = append(p.sim.WorkflowRuns, githubsim.WorkflowRun{
		ID: p.nextRunID(), Name: "nightly", Path: ".github/workflows/nightly.yml", HeadBranch: "main", HeadSHA: p.sha("main~1"),
		Event: "schedule", Status: "completed", Conclusion: "success", RunAttempt: 1, CreatedAt: p.now.Add(-20 * time.Hour),
		Jobs: []githubsim.WorkflowJob{{Name: "go-long"}},
	})

	p.section("item 4: the sandbox clones through the proxy and runs the template's one command (tick A: cut)")
	p.sh("clone", "git clone -q "+p.proxyURL+"/"+owner+"/"+name+".git work && git -C work log --oneline -1")
	status, outText := p.sh("release-train-task.sh, tick A", "bash ~/work/scripts/release-train-task.sh 2>&1 | tail -n 40", "FORCE=true")
	rc := "v3.2.0-rc.1"
	expect(status == 0 && strings.Contains(outText, "RELEASE_TRAIN_RESULT=acted") && p.sha(rc) == head && p.sha("refs/heads/candidate") == head,
		"%s and candidate pushed through the proxy on %s", rc, head[:12])
	p.tags()

	p.section("item 4/5: Stage 1 and grain's Stage 2 pass on the rc; tick B reads them with grain ci and stages -pending")
	jobs := []githubsim.WorkflowJob{}
	for _, j := range stage1Jobs {
		jobs = append(jobs, githubsim.WorkflowJob{Name: j})
	}
	// A multi-attempt run: loadtest failed on attempt 1 and passed on 2.
	jobs = append(jobs, githubsim.WorkflowJob{Name: "loadtest", Conclusion: "failure", RunAttempt: 1})
	for i := range jobs[:len(stage1Jobs)] {
		if jobs[i].Name != "loadtest" {
			jobs[i].RunAttempt = 1
		}
	}
	p.sim.WorkflowRuns = append([]githubsim.WorkflowRun{{
		ID: 31337, Name: "nightly", Path: ".github/workflows/nightly.yml", HeadBranch: rc, HeadSHA: head, Event: "push",
		Status: "completed", RunAttempt: 2, CreatedAt: p.now.Add(-3 * time.Hour), UpdatedAt: p.now.Add(-time.Hour), Jobs: jobs,
	}}, p.sim.WorkflowRuns...)
	started := p.now.Add(-50 * time.Minute)
	done := p.now.Add(-10 * time.Minute)
	id, err := p.client.CreateCheckRun(owner, name, github.CheckRunWrite{Name: model.ReleaseQualificationCheckName, HeadSHA: head,
		Status: "in_progress", ExternalID: model.ReleaseQualificationTag(p.repo, rc), StartedAt: &started})
	if err != nil {
		log.Fatal(err)
	}
	if err := p.client.UpdateCheckRun(owner, name, id, github.CheckRunWrite{Conclusion: "success", CompletedAt: &done,
		Output: &github.CheckRunOutput{Title: "Release qualification of " + rc + ": success", Summary: "every item passed"}}); err != nil {
		log.Fatal(err)
	}
	// And a same-named check run another App posted.
	p.sim.CheckRuns[head] = append(p.sim.CheckRuns[head], github.CheckRun{ID: 7001, Name: model.ReleaseQualificationCheckName,
		HeadSHA: head, App: "github-actions", Status: "completed", Conclusion: ptr("failure"), StartedAt: &done, CompletedAt: &done})
	// A run grain knows only from its record of the run's jobs (a webhook
	// it saw), which GitHub no longer lists.
	if err := p.store.PutGitHubCIJob(p.ctx, model.GitHubCIJob{Repo: p.repo.String(), CheckRunID: 555001, RunID: 4242,
		App: model.GitHubActionsApp, Ref: "main", Commit: head, JobName: "go build, vet and test", Conclusion: "success",
		CompletedAt: &done, Via: "webhook", RunURL: "https://github.com/ai-on-gke/grain/actions/runs/4242"}); err != nil {
		log.Fatal(err)
	}
	p.tick(time.Minute)
	status, outText = p.sh("release-train-task.sh, tick B", "bash ~/work/scripts/release-train-task.sh 2>&1 | tail -n 45")
	expect(status == 0 && p.sha("v3.2.0-pending") == head, "v3.2.0-pending pushed through the proxy (it carries #645's breaking change)")
	p.tags()

	p.section("item 5: grain ci checks / ci list --format github|github-jobs against githubsim's own REST answers")
	p.compareCI(head, rc)

	p.section("item 4: pushes outside the train's grant, by hand from the task's sandbox")
	type attempt struct {
		what, refspec string
		allowed       bool
	}
	for _, a := range []attempt{
		{"a patch release tag", head + ":refs/tags/v3.1.2", false},
		{"latest, deleted", ":refs/tags/latest", false},
		{"candidate, deleted", ":refs/heads/candidate", false},
		{"the -pending tag, deleted", ":refs/tags/v3.2.0-pending", false},
		{"a released tag, moved", "+" + head + ":refs/tags/v3.1.0", false},
		{"main, rewound", "+" + p.sha("main~1") + ":refs/heads/main", false},
		{"a retraction", head + ":refs/tags/v3.1.0-retracted", false},
		{"an rc tag, moved", "+" + p.sha("main~1") + ":refs/tags/" + rc, false},
		{"a -discarded tag", head + ":refs/tags/" + rc + "-discarded", true},
		{"the release tag v3.2.0 on the rc's commit", head + ":refs/tags/v3.2.0", true},
		{"latest, force-moved onto it", "+" + head + ":refs/tags/latest", true},
	} {
		st, text := p.sh("push "+a.what, "cd work && git push origin "+a.refspec+" 2>&1")
		refused := st != 0 || strings.Contains(text, "[remote rejected]")
		expect(refused != a.allowed, "%s: %s", a.what, map[bool]string{true: "allowed", false: "refused"}[!refused])
	}
	// The right ref on a commit main does not have.
	p.sh("a commit only the sandbox has", "cd work && git -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m 'not on main' && git push origin HEAD:refs/tags/v3.3.0-rc.1 2>&1")
	p.tags()

	p.section("item 4: a pull request editing the template's push: list is held for a person")
	p.templatePushHold()
}

// compareCI puts the sandbox's grain ci answers beside the sim's own.
func (p *proxyWorld) compareCI(head, rc string) {
	sim := func(path string) map[string]any {
		resp, err := http.Get(p.api + "/repos/" + owner + "/" + name + "/" + path)
		if err != nil {
			log.Fatal(err)
		}
		defer resp.Body.Close()
		var v map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&v)
		return v
	}
	grain := func(label, args string) map[string]any {
		_, text := p.sh(label, "grain "+args+" > ~/out.json; echo exit=$?; head -c 600 ~/out.json; echo")
		_ = text
		b, _ := os.ReadFile(filepath.Join(p.sbx, "out.json"))
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			fmt.Printf("    (not JSON: %v)\n", err)
		}
		p.dump(strings.ReplaceAll(label, " ", "-"), v)
		return v
	}

	// Check runs.
	g := grain("grain ci checks", "ci checks "+head+" --repo "+owner+"/"+name+" --name grain/release-qualification --format github")
	s := sim("commits/" + head + "/check-runs?check_name=grain%2Frelease-qualification&filter=all")
	p.dump("githubsim-check-runs", s)
	gr, _ := g["check_runs"].([]any)
	sr, _ := s["check_runs"].([]any)
	expect(len(gr) == len(sr) && len(gr) == 2, "grain ci checks lists %d check runs, githubsim %d", len(gr), len(sr))
	expect(g["grain_app"] == app, "grain_app is %v", g["grain_app"])
	for i := range min(len(gr), len(sr)) {
		a, b := gr[i].(map[string]any), sr[i].(map[string]any)
		for _, k := range []string{"id", "name", "head_sha", "status", "conclusion", "external_id", "started_at", "completed_at"} {
			if fmt.Sprint(a[k]) != fmt.Sprint(b[k]) {
				fmt.Printf("    DIFF check run %v %s: grain %v, githubsim %v\n", b["id"], k, a[k], b[k])
			}
		}
		if fmt.Sprint(a["app"]) != fmt.Sprint(b["app"]) {
			fmt.Printf("    DIFF check run %v app: grain %v, githubsim %v\n", b["id"], a["app"], b["app"])
		}
	}

	// Workflow runs on main.
	g = grain("grain ci list github", "ci list --repo "+owner+"/"+name+" --ref main --provider github-actions --format github --limit 500")
	s = sim("actions/runs?branch=main&per_page=100")
	p.dump("githubsim-runs-main", s)
	gw, _ := g["workflow_runs"].([]any)
	sw, _ := s["workflow_runs"].([]any)
	fmt.Printf("    grain listed %d workflow runs on main, githubsim %d; grain_note: %v\n", len(gw), len(sw), g["grain_note"])
	byID := map[string]map[string]any{}
	for _, r := range sw {
		m := r.(map[string]any)
		byID[fmt.Sprint(m["id"])] = m
	}
	for _, r := range gw {
		a := r.(map[string]any)
		b, ok := byID[fmt.Sprint(a["id"])]
		if !ok {
			fmt.Printf("    DIFF grain listed run %v that githubsim does not have\n", a["id"])
			continue
		}
		for _, k := range []string{"name", "path", "head_branch", "head_sha", "event", "status", "conclusion", "run_attempt", "run_started_at", "updated_at"} {
			if fmt.Sprint(a[k]) != fmt.Sprint(b[k]) {
				fmt.Printf("    DIFF run %v %s: grain %v, githubsim %v\n", a["id"], k, a[k], b[k])
			}
		}
		ar, _ := a["repository"].(map[string]any)
		br, _ := b["repository"].(map[string]any)
		if fmt.Sprint(ar["full_name"]) != fmt.Sprint(br["full_name"]) {
			fmt.Printf("    DIFF run %v repository: grain %v, githubsim %v\n", a["id"], ar, br)
		}
	}
	note, _ := g["grain_note"].(string)
	expect(strings.Contains(note, "left out"), "the run grain knows only from its job record is counted in grain_note, not listed: %q", note)
	for _, r := range gw {
		expect(fmt.Sprint(r.(map[string]any)["id"]) != "4242", "run 4242 (job record only) is not invented")
	}

	// The jobs of every attempt of nightly.yml on the rc.
	g = grain("grain ci list github-jobs", "ci list --repo "+owner+"/"+name+" --ref "+rc+" --workflow nightly.yml --format github-jobs")
	s = sim("actions/runs/31337/jobs?filter=all&per_page=100")
	p.dump("githubsim-jobs-31337", s)
	gj, _ := g["jobs"].([]any)
	sj, _ := s["jobs"].([]any)
	expect(len(gj) == len(sj) && len(gj) == 15, "grain ci list --format github-jobs lists %d jobs, githubsim %d (14 + loadtest's first attempt)", len(gj), len(sj))
	for i := range min(len(gj), len(sj)) {
		a, b := gj[i].(map[string]any), sj[i].(map[string]any)
		for _, k := range []string{"id", "run_id", "run_attempt", "workflow_name", "head_sha", "name", "status", "conclusion"} {
			if fmt.Sprint(a[k]) != fmt.Sprint(b[k]) {
				fmt.Printf("    DIFF job %v %s: grain %v, githubsim %v\n", b["id"], k, a[k], b[k])
			}
		}
	}

	// The text forms.
	p.sh("grain ci checks (text)", "grain ci checks "+head+" --repo "+owner+"/"+name)
}

// templatePushHold is a task that widened release-train.yml's push list
// and pushed it: grain opens its pull request and holds it for a person.
func (p *proxyWorld) templatePushHold() {
	for _, tc := range []struct{ what, from, to string }{
		{"widens the push list with refs/tags/v*", "  - refs/tags/v*-discarded\n", "  - refs/tags/v*-discarded\n  - refs/tags/v*\n"},
		{"rewords the body only", "Run exactly this, once, and nothing else:", "Run exactly this command, once, and nothing else:"},
	} {
		id, _ := p.store.NewTaskID(p.ctx)
		human := model.Principal{Kind: model.PrincipalHuman, ID: "walkthrough@example.com"}
		repo := p.repo
		task := model.Task{ID: id, Intent: model.IntentImplement, Title: "Edit the release train template: " + tc.what, Body: "edit",
			Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Approval: &model.Attribution{Actor: human}, Target: &repo, Binding: model.BindingDirective, AutoMerge: true}
		if err := p.store.PutTask(p.ctx, task); err != nil {
			log.Fatal(err)
		}
		// The run's push: one commit editing the template, on the task's branch.
		wd := filepath.Join(p.dir, "edit-"+id)
		if _, err := gitOut("", "clone", "-q", p.bare, wd); err != nil {
			log.Fatal(err)
		}
		f := filepath.Join(wd, ".grain", "templates", "release-train.yml")
		b, _ := os.ReadFile(f)
		if !strings.Contains(string(b), tc.from) {
			log.Fatalf("release-train.yml has no %q", tc.from)
		}
		_ = os.WriteFile(f, []byte(strings.Replace(string(b), tc.from, tc.to, 1)), 0o644)
		for _, args := range [][]string{{"commit", "-qam", "chore(release): " + tc.what}, {"push", "-q", "origin", "HEAD:refs/heads/" + task.BranchName()}} {
			if _, err := gitOut(wd, args...); err != nil {
				log.Fatal(err)
			}
		}
		result := &agent.Result{ToolCalls: []agent.ToolCall{{Name: "run_command", Text: "pushed"}}}
		if err := orchestrator.ProcessResult(p.ctx, p.store, p.client, task, result, id+"-1", p.now); err != nil {
			log.Fatalf("ProcessResult: %v", err)
		}
		got, _ := p.store.GetTask(p.ctx, id)
		links := map[model.LinkKind]string{}
		for _, l := range got.Links {
			links[l.Kind] = l.Target
		}
		p.say("task %s (%s): auto-merge %v, pull request %q, human-only hold %q, security review %q",
			id, tc.what, got.AutoMerge, links[model.LinkFixes], links[model.LinkHumanOnlyHold], links[model.LinkSecurityReviewTask])
		comments, _ := p.store.Comments(p.ctx, id)
		for _, c := range comments {
			if strings.Contains(c.Body, "release-train.yml") || strings.Contains(strings.ToLower(c.Body), "person") {
				for _, l := range strings.Split(c.Body, "\n") {
					if strings.TrimSpace(l) != "" {
						fmt.Println("        | " + l)
					}
				}
			}
		}
		if review := links[model.LinkSecurityReviewTask]; review != "" {
			done := p.now.Add(30 * time.Minute)
			_ = p.store.Observe(p.ctx, model.Observation{TaskID: review, CompletedAt: &done})
			_ = orchestrator.SyncSecurityHolds(p.ctx, p.store, p.client, p.now.Add(time.Hour))
			got, _ = p.store.GetTask(p.ctx, id)
			p.say("task %s after its security review completed clean: auto-merge %v", id, got.AutoMerge)
		}
		if strings.HasPrefix(tc.what, "widens") {
			expect(links[model.LinkHumanOnlyHold] != "" && !got.AutoMerge, "a push list change waits for a person, even after a clean review")
		}
	}
}
