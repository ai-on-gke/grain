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

// walk drives §1 items 1, 2, 3 and 6 of docs/test-plans/2026-11-04.md
// over one simulated GitHub, each step running off the previous step's
// real output:
//
//   - pkg/github/githubsim stands in for github.com: a bare repository
//     seeded with this checkout's origin/main and tags, its REST API served
//     over HTTP (githubsim.Sim.Handler) to the shell scripts and through a
//     real github.RESTClient to grain;
//   - scripts/release-from-trunk.sh (train mode, RELEASE_CI_SOURCE=github)
//     and scripts/release-train-actions.sh run unmodified in a scratch
//     clone of that bare repository, reading the sim's REST answers;
//   - grain's own Stage 2 (orchestrator.SyncReleaseQualification) runs on a
//     real sqlite store, with the release-qualification suite and its
//     templates loaded from the checkout's .grain by the "declarations"
//     reconciler, and posts grain/release-qualification and grain/rc-hold
//     to the sim as the App "grain-walkthrough".
//
// Stubbed boundaries: GitHub itself (githubsim), Actions executing
// nightly.yml (the driver concludes the runs release-train-actions.sh
// dispatched), and the agent inside each qualification task (the driver
// writes what a run's finish writes: its closing note as grain's relay,
// its pull request link, its proposals, its scratch branch).
//
// A separate module (replace ../../../..) so the repository's ./... never
// builds it:
//
//	go run . -src <checkout> -dir <scratch> [-out <dir for JSON>]
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

const (
	owner = "ai-on-gke"
	name  = "grain"
	app   = "grain-walkthrough"
)

var stage1Jobs = []string{"go-long", "staterepo-soak", "loadtest", "scalability", "merge-stress", "long-term-test",
	"long-sim", "postgres-stress", "tunnel-stress", "ui-e2e-long", "installer-variants", "vulns", "cmd-grain-stress", "upgrade-test"}

// appTokens is an installation token of the App grain-walkthrough: what
// github.RESTClient.CheckRunApp reads grain's own App from.
type appTokens struct{}

func (appTokens) TokenFor(string, string) *string           { t := "walkthrough-installation-token"; return &t }
func (appTokens) AppSlugFor(string, string) (string, error) { return app, nil }

type bareDeclarations struct{ bare string }

func (d bareDeclarations) Head(_ context.Context, _ model.RepoRef) (string, error) {
	return gitOut(d.bare, "rev-parse", "refs/heads/main")
}

func (d bareDeclarations) Read(_ context.Context, repo model.RepoRef) (model.RepoDeclarations, string, error) {
	dir, err := os.MkdirTemp("", "walk-declarations-")
	if err != nil {
		return model.RepoDeclarations{}, "", err
	}
	defer os.RemoveAll(dir)
	if _, err := gitOut("", "clone", "-q", "--depth", "1", "file://"+d.bare, dir+"/c"); err != nil {
		return model.RepoDeclarations{}, "", err
	}
	commit, err := gitOut(dir+"/c", "rev-parse", "HEAD")
	if err != nil {
		return model.RepoDeclarations{}, "", err
	}
	declared, err := repodecl.Load(os.DirFS(dir+"/c"), repo)
	return declared, commit, err
}

type world struct {
	src, dir, out string
	bare, clone   string
	sim           *githubsim.Sim
	api           string
	client        *github.RESTClient
	store         *model.Store
	ctx           context.Context
	repo          model.RepoRef
	now           time.Time
	step          int
}

func main() {
	src := flag.String("src", "", "the grain checkout")
	dir := flag.String("dir", "", "a scratch directory (emptied)")
	out := flag.String("out", "", "where to write JSON snapshots (default: none)")
	proxy := flag.Bool("proxy", false, "walk items 4 and 5 (the task train through the scoped proxy) instead")
	grainBin := flag.String("grain", "grain", "with -proxy, the grain binary the sandbox runs")
	flag.Parse()
	if *src == "" || *dir == "" {
		log.Fatal("usage: walk -src <checkout> -dir <scratch> [-out <dir>] [-proxy -grain <binary>]")
	}
	if *proxy {
		bin, err := exec.LookPath(*grainBin)
		if err != nil {
			log.Fatal(err)
		}
		bin, _ = filepath.Abs(bin)
		proxyWalk(*src, *dir, *out, bin)
		return
	}
	w, err := setup(*src, *dir, *out)
	if err != nil {
		log.Fatal(err)
	}
	if err := w.walk(); err != nil {
		log.Fatal(err)
	}
}

func setup(src, dir, out string) (*world, error) {
	src, _ = filepath.Abs(src)
	dir, _ = filepath.Abs(dir)
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "home"), 0o755); err != nil {
		return nil, err
	}
	if out != "" {
		out, _ = filepath.Abs(out)
		_ = os.MkdirAll(out, 0o755)
	}
	os.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	os.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	os.Setenv("GIT_AUTHOR_NAME", "walkthrough")
	os.Setenv("GIT_AUTHOR_EMAIL", "walkthrough@example.com")
	os.Setenv("GIT_COMMITTER_NAME", "walkthrough")
	os.Setenv("GIT_COMMITTER_EMAIL", "walkthrough@example.com")
	w := &world{src: src, dir: dir, out: out, bare: filepath.Join(dir, "origin.git"), clone: filepath.Join(dir, "clone"),
		repo: model.RepoRef{Owner: owner, Name: name}, now: time.Now().UTC().Truncate(time.Second)}
	if _, err := gitOut("", "init", "-q", "--bare", "-b", "main", w.bare); err != nil {
		return nil, err
	}
	// Released tags only (v3.0.0 .. v3.1.1 and latest): the walk owns
	// v3.2.0 from its first rc, so the real remote's own v3.2.0-rc.1 and
	// earlier rc tags are left out.
	tags, err := gitOut(src, "tag", "-l", "v*", "latest")
	if err != nil {
		return nil, err
	}
	refs := []string{"refs/remotes/origin/main:refs/heads/main"}
	var seeded []string
	for _, t := range strings.Fields(tags) {
		if strings.Contains(t, "-") {
			continue
		}
		seeded = append(seeded, t)
		refs = append(refs, "refs/tags/"+t+":refs/tags/"+t)
	}
	tags = strings.Join(seeded, " ")
	if _, err := gitOut(src, append([]string{"push", "-q", w.bare}, refs...)...); err != nil {
		return nil, err
	}
	if _, err := gitOut("", "clone", "-q", w.bare, w.clone); err != nil {
		return nil, err
	}
	w.sim = githubsim.New(owner, name, w.bare, "main")
	w.sim.CheckRunApp = app
	w.sim.Now = func() time.Time { return w.now }
	srv := httptest.NewServer(w.sim.Handler())
	w.api = srv.URL
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
	if err := w.store.PutRepoConfig(w.ctx, model.RepoConfig{Repo: w.repo, Declarations: true}); err != nil {
		return nil, err
	}
	for _, r := range orchestrator.Reconcilers() {
		if r.Name == "declarations" {
			if err := r.Reconcile(w.ctx, orchestrator.Deps{Store: w.store, Client: w.client,
				Declarations: bareDeclarations{bare: w.bare}}, w.now); err != nil {
				return nil, fmt.Errorf("declarations: %w", err)
			}
		}
	}
	suite, err := w.store.GetSuite(w.ctx, model.RepoDeclaredID(model.DeclaredSuite, w.repo, model.ReleaseQualificationSuiteStem))
	if err != nil || suite == nil {
		return nil, fmt.Errorf("the release-qualification suite was not declared: %v", err)
	}
	var items []string
	for _, it := range suite.Items {
		items = append(items, it.TemplateID)
	}
	head, _ := gitOut(w.bare, "rev-parse", "main")
	w.say("setup: githubsim %s/%s at %s, bare %s, main %s; tags %s", owner, name, w.api, w.bare, head[:12], strings.Join(strings.Fields(tags), " "))
	w.say("setup: declared suite %s, mode %s, max passes %d, items %s", suite.ID, suite.Mode, suite.MaxPasses, strings.Join(items, ", "))
	return w, nil
}

func (w *world) say(format string, args ...any) {
	fmt.Printf("[%s] %s\n", w.now.Format("15:04:05"), fmt.Sprintf(format, args...))
}

func (w *world) section(title string) {
	w.step++
	fmt.Printf("\n=== %d. %s\n", w.step, title)
}

func (w *world) tick(d time.Duration) { w.now = w.now.Add(d) }

func gitOut(dir string, args ...string) (string, error) {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(string(out)), nil
}

func (w *world) sha(ref string) string {
	s, err := gitOut(w.bare, "rev-parse", ref+"^{commit}")
	if err != nil {
		return ""
	}
	return s
}

// commit puts a new commit on main in the bare repository: a fix landing.
func (w *world) commit(msg string) string {
	tree, _ := gitOut(w.bare, "rev-parse", "main^{tree}")
	parent := w.sha("main")
	c, err := gitOut(w.bare, "commit-tree", tree, "-p", parent, "-m", msg)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := gitOut(w.bare, "update-ref", "refs/heads/main", c); err != nil {
		log.Fatal(err)
	}
	return c
}

// green records tests.yml and release.yml as passed on branch at sha:
// what makes a commit known-good to candidate-gate.sh select-lkg.
func (w *world) green(branch, sha string) {
	for _, wf := range []string{"tests", "release"} {
		w.sim.WorkflowRuns = append([]githubsim.WorkflowRun{{
			ID: w.nextRunID(), Name: wf, Path: ".github/workflows/" + wf + ".yml", HeadBranch: branch, HeadSHA: sha,
			Event: "push", Status: "completed", Conclusion: "success", RunAttempt: 1, CreatedAt: w.now.Add(-30 * time.Minute),
			Jobs: []githubsim.WorkflowJob{{Name: wf}},
		}}, w.sim.WorkflowRuns...)
	}
}

func (w *world) nextRunID() int64 {
	id := int64(20000)
	for i, r := range w.sim.WorkflowRuns {
		if r.ID <= 0 {
			w.sim.WorkflowRuns[i].ID = int64(9500 + i)
		}
		id = max(id, w.sim.WorkflowRuns[i].ID+1)
	}
	return id
}

// run runs a shell script in the scratch clone and returns its exit
// status and the outputs it wrote to RELEASE_TRAIN_OUTPUT.
func (w *world) run(label, script string, env []string, args ...string) (int, string) {
	outFile := filepath.Join(w.dir, "train.out")
	_ = os.WriteFile(outFile, nil, 0o644)
	base := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(w.dir, "home"),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"RELEASE_CI_SOURCE=github", "RELEASE_GITHUB_API_URL=" + w.api, "RELEASE_GITHUB_REPO=" + owner + "/" + name,
		"GITHUB_TOKEN=walkthrough-actions-token", "RELEASE_QUALIFICATION_APP=" + app,
		"RELEASE_IMAGES=off", "RELEASE_NOTES_TASK=off", "RELEASE_NOTIFY=off", "RELEASE_PUSH_GRANT=train",
		"RELEASE_TRAIN_OUTPUT=" + outFile, "RELEASE_NOW=" + strconv.FormatInt(w.now.Unix(), 10),
	}
	cmd := exec.Command("bash", append([]string{filepath.Join(w.src, "scripts", script)}, args...)...)
	cmd.Dir = w.clone
	cmd.Env = append(base, env...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	status := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		status = ee.ExitCode()
	} else if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("--- %s: %s %s %s (exit %d)\n", label, strings.Join(env, " "), script, strings.Join(args, " "), status)
	for _, l := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if l != "" {
			fmt.Println("    | " + l)
		}
	}
	outs, _ := os.ReadFile(outFile)
	return status, string(outs)
}

func (w *world) train(label string, env ...string) (int, string) {
	return w.run(label, "release-from-trunk.sh", env)
}

func (w *world) actions(label, outputs string, env ...string) int {
	f := filepath.Join(w.dir, "actions.in")
	_ = os.WriteFile(f, []byte(outputs), 0o644)
	status, _ := w.run(label, "release-train-actions.sh", env, f)
	return status
}

func (w *world) reconcile() {
	if err := orchestrator.SyncReleaseQualification(w.ctx, w.store, w.client, w.now); err != nil {
		w.say("reconcile: SyncReleaseQualification: %v", err)
		return
	}
	w.say("reconcile: SyncReleaseQualification ran")
}

func (w *world) tags() {
	t, _ := gitOut(w.bare, "for-each-ref", "--format=%(refname:short)=%(objectname:short)", "refs/tags/v3.2*", "refs/tags/v3.3*", "refs/tags/latest", "refs/heads/candidate", "refs/heads/release")
	w.say("refs: %s", strings.ReplaceAll(t, "\n", " "))
}

func (w *world) tagMessage(tag string) {
	msg, _ := gitOut(w.bare, "for-each-ref", "--format=%(contents)", "refs/tags/"+tag)
	fmt.Printf("    tag %s (%s):\n", tag, w.sha(tag)[:12])
	for _, l := range strings.Split(msg, "\n") {
		fmt.Println("      > " + l)
	}
}

// checks prints every check run on sha, as the sim holds them.
func (w *world) checks(label, sha string) {
	runs := w.sim.CheckRuns[sha]
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	w.say("check runs on %s (%s): %d", sha[:12], label, len(runs))
	for _, r := range runs {
		c := "-"
		if r.Conclusion != nil {
			c = *r.Conclusion
		}
		fmt.Printf("    #%d %-28s app=%-18s status=%-11s conclusion=%-8s external_id=%s\n", r.ID, r.Name, r.App, r.Status, c, r.ExternalID)
		if r.FailureReason != "" {
			fmt.Printf("        title: %s\n", r.FailureReason)
		}
		for _, l := range strings.Split(r.Output, "\n") {
			if l != "" {
				fmt.Printf("        | %s\n", l)
			}
		}
	}
	w.dump("checks-"+label, runs)
}

func (w *world) notifications(label string) {
	ns, err := w.store.ListNotifications(w.ctx, model.NotificationFilter{Limit: 100})
	if err != nil {
		log.Fatal(err)
	}
	w.say("notifications (%s): %d", label, len(ns))
	for i := len(ns) - 1; i >= 0; i-- {
		n := ns[i]
		state := "open"
		if n.ClearedAt != nil {
			state = "cleared"
		}
		fmt.Printf("    [%s] %s (%s, %s) %s\n", state, n.Subject, n.Severity, n.RaisedBy, n.Title)
		for _, l := range strings.Split(n.Body, "\n") {
			if l != "" {
				fmt.Printf("        | %s\n", l)
			}
		}
	}
	w.dump("notifications-"+label, ns)
}

func (w *world) dump(name string, v any) {
	if w.out == "" {
		return
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	_ = os.WriteFile(filepath.Join(w.out, fmt.Sprintf("%02d-%s.json", w.step, name)), append(b, '\n'), 0o644)
}

// items is every qualification task filed for rcTag across all Stage 2
// attempts, by template, oldest attempt first.
func (w *world) items(rcTag string) map[string][]string {
	baseTag := model.ReleaseQualificationTag(w.repo, rcTag)
	var runs []model.SuiteRun
	for attempt := 1; ; attempt++ {
		rs, err := w.store.SuiteRunsTagged(w.ctx, model.QualificationAttemptTag(baseTag, attempt))
		if err != nil {
			log.Fatal(err)
		}
		if len(rs) == 0 {
			break
		}
		runs = append(runs, rs...)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	out := map[string][]string{}
	for _, r := range runs {
		for _, t := range r.Tasks {
			out[t.TemplateID] = append(out[t.TemplateID], t.TaskID)
		}
	}
	return out
}

func (w *world) showItems(rcTag string) {
	its := w.items(rcTag)
	keys := make([]string, 0, len(its))
	for k := range its {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	w.say("qualification tasks for %s:", rcTag)
	for _, k := range keys {
		var parts []string
		for _, id := range its[k] {
			st, _ := w.store.State(w.ctx, id)
			t, _ := w.store.GetTask(w.ctx, id)
			title := ""
			if t != nil {
				title = fmt.Sprintf(" %q prio %d", t.Title, t.Priority)
			}
			parts = append(parts, fmt.Sprintf("%s [%s]%s", id, st, title))
		}
		fmt.Printf("    %-28s %s\n", strings.TrimPrefix(k, "repo:template:"+owner+":"+name+":"), strings.Join(parts, "; "))
	}
}

func (w *world) latest(rcTag, item string) string {
	ids := w.items(rcTag)[model.RepoDeclaredID(model.DeclaredTemplate, w.repo, item)]
	if len(ids) == 0 {
		log.Fatalf("no task for %s of %s", item, rcTag)
	}
	return ids[len(ids)-1]
}

func ownRun(taskID string) model.Attribution {
	return model.Attribution{
		Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
		OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: taskID},
	}
}

func footer(verdict, tag, sha string) string {
	return fmt.Sprintf("Walked the template's steps.\n\nQualification-Verdict: %s\nQualified-Tag: %s\nQualified-Commit: %s\n", verdict, tag, sha)
}

// finish is the scripted agent's end of a task: the closing note grain
// relays for the run, and the task completed.
func (w *world) finish(taskID, note string) {
	if _, err := w.store.AddComment(w.ctx, model.Comment{TaskID: taskID, Author: ownRun(taskID), Body: note, CreatedAt: w.now}); err != nil {
		log.Fatal(err)
	}
	at := w.now
	if err := w.store.Observe(w.ctx, model.Observation{TaskID: taskID, CompletedAt: &at}); err != nil {
		log.Fatal(err)
	}
	first := strings.SplitN(strings.TrimSpace(note[strings.Index(note, "Qualification-Verdict"):]), "\n", 2)[0]
	w.say("agent: %s finished: %s", taskID, first)
}

// openPR is a run that pushed a branch and had grain open a pull request
// for it: a real pull request on the sim, and the task's fixes link.
func (w *world) openPR(taskID string) {
	branch := "grain/" + taskID
	c := w.commit("walkthrough: a change from " + taskID)
	_, _ = gitOut(w.bare, "update-ref", "refs/heads/"+branch, c)
	_, _ = gitOut(w.bare, "update-ref", "refs/heads/main", c+"~1")
	pr, err := w.client.CreatePullRequest(owner, name, branch, "main", "Change from "+taskID, "body")
	if err != nil {
		log.Fatal(err)
	}
	t, _ := w.store.GetTask(w.ctx, taskID)
	t.Links = append(t.Links, model.Link{Kind: model.LinkFixes, Target: fmt.Sprintf("%s/%s#%d", owner, name, pr.Number)})
	if err := w.store.PutTask(w.ctx, *t); err != nil {
		log.Fatal(err)
	}
	w.say("agent: %s pushed %s and grain opened %s/%s#%d", taskID, branch, owner, name, pr.Number)
}

// propose is a run proposing a follow-up task on base.
func (w *world) propose(parent, base, title string) string {
	id, err := w.store.NewTaskID(w.ctx)
	if err != nil {
		log.Fatal(err)
	}
	at := w.now
	repo := w.repo
	if err := w.store.PutTask(w.ctx, model.Task{
		ID: id, Title: title, Body: "follow-up", Target: &repo, Base: base,
		Links:     []model.Link{{Kind: model.LinkProposedBy, Target: parent}},
		CreatedAt: &at,
	}); err != nil {
		log.Fatal(err)
	}
	w.say("agent: %s proposed task %s on %s: %q", parent, id, base, title)
	return id
}

func (w *world) fileTask(title string) string {
	id, err := w.store.NewTaskID(w.ctx)
	if err != nil {
		log.Fatal(err)
	}
	at := w.now
	repo := w.repo
	if err := w.store.PutTask(w.ctx, model.Task{ID: id, Title: title, Body: "fix", Target: &repo, Base: "main", CreatedAt: &at}); err != nil {
		log.Fatal(err)
	}
	w.say("person: filed task %s %q", id, title)
	return id
}

func (w *world) closeTask(id string) {
	at := w.now
	if err := w.store.ObserveField(w.ctx, id, at, func(o *model.Observation) { o.ClosedAt = &at }); err != nil {
		log.Fatal(err)
	}
	w.say("task %s closed (its fix landed)", id)
}

// nightlyRun returns the newest nightly.yml run on the rc tag.
func (w *world) nightlyRun(rc string) *githubsim.WorkflowRun {
	for i := range w.sim.WorkflowRuns {
		r := &w.sim.WorkflowRuns[i]
		if r.HeadBranch == rc && strings.HasSuffix(r.Path, "nightly.yml") {
			return r
		}
	}
	return nil
}

// conclude is Actions running a dispatched nightly.yml run to its end:
// the jobs it was asked for (all 14 when inputs.jobs is empty), each
// passing unless named in failing.
func (w *world) conclude(rc string, failing ...string) {
	r := w.nightlyRun(rc)
	if r == nil {
		log.Fatalf("no nightly.yml run on %s to conclude", rc)
	}
	jobs := stage1Jobs
	for _, d := range w.sim.Dispatches {
		if d.RunID == r.ID && d.Inputs["jobs"] != "" {
			jobs = strings.Split(d.Inputs["jobs"], ",")
		}
	}
	r.Jobs = nil
	conclusion := "success"
	for _, j := range jobs {
		job := githubsim.WorkflowJob{Name: j, Conclusion: "success"}
		for _, f := range failing {
			if f == j {
				job.Conclusion, conclusion = "failure", "failure"
			}
		}
		r.Jobs = append(r.Jobs, job)
	}
	r.Status, r.Conclusion, r.UpdatedAt = "completed", conclusion, w.now
	w.say("actions: nightly.yml run %d on %s (%s) concluded %s: jobs %s", r.ID, rc, r.Event, conclusion, strings.Join(jobs, ","))
}

func (w *world) dispatches(since int) int {
	for _, d := range w.sim.Dispatches[since:] {
		w.say("githubsim: workflow_dispatch %s ref=%s inputs=%v -> queued run %d", d.Workflow, d.Ref, d.Inputs, d.RunID)
	}
	if len(w.sim.Dispatches) == since {
		w.say("githubsim: no workflow_dispatch")
	}
	return len(w.sim.Dispatches)
}

func expect(cond bool, format string, args ...any) {
	if cond {
		fmt.Printf("    OK   %s\n", fmt.Sprintf(format, args...))
		return
	}
	fmt.Printf("    FAIL %s\n", fmt.Sprintf(format, args...))
}

func outputHas(outs, kv string) bool {
	for _, l := range strings.Split(outs, "\n") {
		if l == kv {
			return true
		}
	}
	return false
}
