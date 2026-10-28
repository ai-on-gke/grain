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

// TestPortMechanicalWalkthrough is docs/test-plans/2026-10-06.md §3,
// steps 1 and 2: the mechanical porter end to end.
//
// pkg/orchestrator/port_mechanical_test.go proves the filing rules with
// a portpick.Porter reaching the sim's bare repository directly, and
// pkg/daemon/porter_test.go proves NewPorter's token and scopes on their
// own. Nothing before this ran the porter the way a deployment does:
// built by daemon.NewPorter, reaching the stand-in GitHub through the
// real git proxy under a token minted for one attempt, inside real
// orchestrator.RunCycle cycles whose merge queue then lands what it
// opened -- and read the proxy's audit log and the merges metrics
// afterwards.
package e2e

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/portpick"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// scriptAgent is the scripted stand-in for a live model session. A task
// it has a script for runs that script on its own branch, cut from its
// base, and pushes; a port task follows portBody's recipe (find the
// merge, `git cherry`, `git cherry-pick -x` with a Backported-from
// footer), resolving a conflict in the merged change's favour the way an
// agent told "do on the target what it did on the source" would.
type scriptAgent struct {
	remote string
	store  *model.Store
	mu     sync.Mutex
	// scripts is the shell each non-port task runs in its checkout,
	// keyed by title.
	scripts map[string]string
}

func (a *scriptAgent) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	task, err := a.store.GetTask(ctx, cfg.TaskID)
	if err != nil || task == nil {
		return nil, fmt.Errorf("scriptAgent: reading %s: %v", cfg.TaskID, err)
	}
	branch := task.BranchName()
	prefix := "git clone -q " + a.remote + " work && cd work && " +
		"git config user.email agent@example.com && git config user.name agent && " +
		"git checkout -q " + task.Base + " && git checkout -q -b " + branch + " && "
	var cmd string
	if m := portTitleRe.FindStringSubmatch(task.Title); m != nil {
		cm := portCherryRe.FindStringSubmatch(task.Body)
		if cm == nil {
			return nil, fmt.Errorf("scriptAgent: %s's body names no `git cherry -v` command:\n%s", task.ID, task.Body)
		}
		cherry := strings.ReplaceAll(cm[1], "<merge>", "$M")
		cmd = prefix +
			"M=$(git log --first-parent --merges --format='%H %s' origin/" + sourceBranchOf(task) +
			" | grep -F 'pull request #" + m[1] + " ' | head -1 | cut -d' ' -f1) && test -n \"$M\" && " +
			"for c in $(" + cherry + " | awk '$1 == \"+\" {print $2}'); do " +
			"{ git cherry-pick -x $c >/dev/null 2>&1 || " +
			"{ git checkout -q --theirs -- . && git add -A && GIT_EDITOR=true git cherry-pick --continue >/dev/null; }; } && " +
			"git commit -q --amend -m \"$(git log -1 --format=%B)\n\nBackported-from: $c\"; done && " +
			"git push -q origin " + branch
	} else {
		a.mu.Lock()
		script, ok := a.scripts[task.Title]
		a.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("scriptAgent: no script for %q", task.Title)
		}
		cmd = prefix + script + " && git push -q origin " + branch
	}
	return antigravity.NewForTest(antigravity.Steps(
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText("pushed "+branch),
	)).Run(ctx, cfg)
}

// commitFile is the shell for one commit writing content to file.
func commitFile(file, content, message string) string {
	return fmt.Sprintf("printf '%%s\\n' %q > %s && git add %s && git commit -q -m %q", content, file, file, message)
}

// recordingCI is greenCI that also remembers every ref it was started
// for -- the port's grain CI run, which the merge queue gates it on.
type recordingCI struct {
	greenCI
	mu   sync.Mutex
	refs []string
}

func (c *recordingCI) Start(ctx context.Context, req ci.Request) (ci.Run, error) {
	c.mu.Lock()
	c.refs = append(c.refs, req.Ref)
	c.mu.Unlock()
	return c.greenCI.Start(ctx, req)
}

func (c *recordingCI) ranOn(branch string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.ContainsFunc(c.refs, func(ref string) bool {
		return ref == branch || ref == "refs/heads/"+branch
	})
}

// portAttempt is one mechanical attempt as the test watched it: the
// token NewPorter minted for it, and whether its Release ran.
type portAttempt struct {
	token    string
	released bool
}

// auditLine is one line of the git proxy's audit log
// (gitproxy.FileAuditLog).
type auditLine struct {
	Sandbox    string `json:"sandbox"`
	Repo       string `json:"repo"`
	Action     string `json:"action"`
	Credential string `json:"credential"`
	Outcome    string `json:"outcome"`
}

func readAudit(t *testing.T, path string) []auditLine {
	t.Helper()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []auditLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l auditLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("audit log line %q: %v", sc.Text(), err)
		}
		out = append(out, l)
	}
	return out
}

func TestPortMechanicalWalkthrough(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	const owner, repoName = "acme", "gadgets"
	repo := model.RepoRef{Owner: owner, Name: repoName}

	// The repository: main declaring the pair, candidate cut from it.
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", "-q", bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	mustWriteFile(t, filepath.Join(seed, "NOTES.md"), "seed\n")
	mustWriteFile(t, filepath.Join(seed, ".grain", "settings.yml"), "port-branches: [main, candidate]\n")
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "commit", "-q", "-m", "initial commit")
	run(t, seed, "git", "push", "-q", "origin", "main", "main:candidate")
	git := func(args ...string) string {
		t.Helper()
		return strings.TrimSpace(runOutput(t, upstream, "git", append([]string{"--git-dir", bare}, args...)...))
	}
	// pushTo commits file onto branch of the remote directly, the way a
	// human's push (or an earlier hand port) would arrive.
	pushTo := func(branch, from, file, content, message string) string {
		t.Helper()
		dir := t.TempDir()
		run(t, dir, "git", "clone", "-q", bare, "w")
		wd := filepath.Join(dir, "w")
		run(t, wd, "git", "config", "user.email", "human@example.com")
		run(t, wd, "git", "config", "user.name", "human")
		run(t, wd, "git", "checkout", "-q", "-B", branch, "origin/"+from)
		mustWriteFile(t, filepath.Join(wd, file), content)
		run(t, wd, "git", "add", "-A")
		run(t, wd, "git", "commit", "-q", "-m", message)
		run(t, wd, "git", "push", "-q", "origin", "HEAD:refs/heads/"+branch)
		return strings.TrimSpace(runOutput(t, wd, "git", "rev-parse", "HEAD"))
	}

	// The one external boundary stubbed: GitHub, as githubsim's REST API
	// over the bare repository, which git http-backend serves.
	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)
	remote := "http://" + githubHost + "/" + owner + "/" + repoName + ".git"
	client := github.NewClient(sim, nil)

	// The store, opened once for the whole walkthrough: the git proxy
	// reads the same one the orchestrator writes, as in a deployment.
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitCredential = "anonymous"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, Declarations: true}); err != nil {
		t.Fatal(err)
	}

	// The real git proxy, wired as daemon.StartGitProxy wires it: the
	// CI scope registries NewPorter grants into, in front of the model.
	dataDir := t.TempDir()
	mustWriteFile(t, filepath.Join(dataDir, "secrets", "github", "credentials.json"), `{"*": "anonymous"}`)
	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir: dataDir, Store: store, ForwardHost: githubHost, ForwardTLS: false,
		ReadScopes: daemon.CiReadScopes, WriteScopes: daemon.CiWriteScopes, SandboxTeams: daemon.CiSandboxTeams,
	})
	if err != nil {
		t.Fatal(err)
	}
	// What the daemon does at startup: the ladder above becomes the
	// deployment's git connections, which is all the proxy reads.
	if err := store.UpdateConfig(context.Background(), func(t *model.Config) error {
		t.GitConnections = append(t.GitConnections, model.TeamGitConnection{Credential: "anonymous"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	proxyURL := newTestServer(t, gitproxy.NewHandler(proxy))
	auditPath := filepath.Join(dataDir, "state", "git-proxy", "audit.log")
	tokens := gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "secrets", "sandbox-tokens.json"))

	// The porter a deployment builds, watched rather than replaced: the
	// wrapper records each attempt's token and its release, and passes
	// everything through.
	porter, ok := daemon.NewPorter(t.TempDir(), proxyURL, store, tokens).(*portpick.Porter)
	if !ok || porter == nil {
		t.Fatalf("daemon.NewPorter with a proxy and a token store returned %T", porter)
	}
	var attempts []*portAttempt
	minted := porter.Remote
	porter.Remote = func(ctx context.Context, r model.RepoRef, task string) (portpick.Remote, error) {
		got, err := minted(ctx, r, task)
		if err != nil {
			return got, err
		}
		if !strings.HasPrefix(got.URL, proxyURL+"/") {
			t.Errorf("porter remote %q is not the git proxy %s", got.URL, proxyURL)
		}
		a := &portAttempt{token: got.Token}
		attempts = append(attempts, a)
		release := got.Release
		got.Release = func() {
			release()
			a.released = true
		}
		return got, nil
	}

	agentStub := &scriptAgent{remote: remote, store: store, scripts: map[string]string{}}
	ciStub := &recordingCI{greenCI: greenCI{runs: map[string]ci.Run{}}}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: credentialed(t, remote), MaxWorkers: 1,
		Declarations: bareDeclarations{t: t, bare: bare},
		CI:           ciStub,
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			return agentStub, nil
		},
		Config: orchestrator.Config{Porter: porter},
	}
	at := baseTime
	cycle := func() {
		t.Helper()
		at = at.Add(10 * time.Minute)
		if err := orchestrator.RunCycle(ctx, deps, at); err != nil {
			t.Fatalf("RunCycle at %s: %v", at, err)
		}
	}
	state := func(id string) model.State {
		t.Helper()
		st, err := store.State(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	task := func(id string) model.Task {
		t.Helper()
		got, err := store.GetTask(ctx, id)
		if err != nil || got == nil {
			t.Fatalf("reading %s: %v %v", id, got, err)
		}
		return *got
	}
	land := func(id string, beforeMerge func()) {
		t.Helper()
		cycle()
		if beforeMerge != nil {
			beforeMerge()
		}
		for i := 0; i < 4; i++ {
			for _, pr := range sim.openPullRequests() {
				sim.markPullRequestClean(pr.Number)
			}
			cycle()
			if state(id) == model.StateClosed {
				return
			}
		}
		t.Fatalf("%s never merged: state %q, task %+v, pull requests %+v", id, state(id), task(id), sim.sim.PullRequests)
	}
	portsOf := func(source string) []model.Task {
		t.Helper()
		all, err := store.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []model.Task
		for _, tk := range all {
			if slices.Contains(tk.Links, model.Link{Kind: model.LinkPortOf, Target: source}) {
				out = append(out, tk)
			}
		}
		return out
	}
	onlyPort := func(source string) model.Task {
		t.Helper()
		ports := portsOf(source)
		if len(ports) != 1 {
			t.Fatalf("ports of %s = %d, want 1: %+v", source, len(ports), ports)
		}
		return ports[0]
	}
	file := func(title, script string) string {
		t.Helper()
		id, err := store.NewTaskID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		orderKey, err := store.OrderKeyForNewTask(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		now := at
		if err := store.PutTask(ctx, model.Task{
			ID: id, Title: title, Body: "body of " + title,
			Intent:   model.IntentImplement,
			Origin:   model.Origin{Attribution: model.Attribution{Actor: human("alice")}, Reason: model.ReasonDirect},
			Approval: &model.Attribution{Actor: human("alice")}, ApprovedAt: &now,
			Target: &repo, Base: "main", AutoMerge: true,
			OrderKey: orderKey, CreatedAt: &now,
		}); err != nil {
			t.Fatal(err)
		}
		agentStub.mu.Lock()
		agentStub.scripts[title] = script
		agentStub.mu.Unlock()
		return id
	}
	remoteHas := func(branch string) bool {
		t.Helper()
		return git("ls-remote", "--heads", bare, "refs/heads/"+branch) != ""
	}
	// withToken runs git against the proxy as the holder of token.
	withToken := func(token string, args ...string) (string, error) {
		auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		cmd := exec.Command("git", append([]string{"-c", "http.extraHeader=Authorization: Basic " + auth}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		cmd.Dir = t.TempDir()
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	proxyRemote := proxyURL + "/" + owner + "/" + repoName + ".git"
	auditSeen := 0
	// checkAttempt is what every mechanical attempt must leave behind: one
	// attempt, released; its identity in the audit log, every request it
	// made forwarded with the team's credential; nothing left granted to
	// that identity; and a token that no longer reaches the repository.
	checkAttempt := func(label string, wantPush bool) string {
		t.Helper()
		if len(attempts) == 0 {
			t.Fatalf("%s: the porter was never asked", label)
		}
		a := attempts[len(attempts)-1]
		if !a.released {
			t.Errorf("%s: the attempt's proxy identity was never released", label)
		}
		lines := readAudit(t, auditPath)
		fresh := lines[auditSeen:]
		auditSeen = len(lines)
		identity := ""
		var actions []string
		for _, l := range fresh {
			if !strings.HasPrefix(l.Sandbox, "port-") {
				continue
			}
			if identity == "" {
				identity = l.Sandbox
			} else if l.Sandbox != identity {
				t.Errorf("%s: two port identities in one attempt: %s and %s", label, identity, l.Sandbox)
			}
			if l.Repo != owner+"/"+repoName {
				t.Errorf("%s: %s reached %s", label, l.Sandbox, l.Repo)
			}
			if strings.HasPrefix(l.Outcome, "allowed: ") {
				// The ref grant a push's check records before it is
				// forwarded (gitproxy.CIRefUpdates): the porter's write
				// scope is a CI job's.
				if l.Outcome != "allowed: repo-write (create)" && l.Outcome != "allowed: repo-write (update)" {
					t.Errorf("%s: audit line %+v, want its branch allowed by repo-write", label, l)
				}
				continue
			}
			if !strings.HasPrefix(l.Outcome, "forwarded") || l.Credential != "anonymous" {
				t.Errorf("%s: audit line %+v, want forwarded with the team's credential", label, l)
			}
			actions = append(actions, l.Action)
		}
		if identity == "" {
			t.Fatalf("%s: the proxy's audit log names no port-… identity: %+v", label, fresh)
		}
		if len(identity) != len("port-")+12 {
			t.Errorf("%s: identity %q is not port-<12 hex>", label, identity)
		}
		if got := slices.Contains(actions, "git-receive-pack"); got != wantPush {
			t.Errorf("%s: pushed = %v, want %v (actions %v)", label, got, wantPush, actions)
		}
		if !slices.Contains(actions, "git-upload-pack") {
			t.Errorf("%s: never fetched through the proxy (actions %v)", label, actions)
		}
		if repos := daemon.CiWriteScopes.Repos(identity); len(repos) != 0 {
			t.Errorf("%s: %s still holds write to %v after its attempt", label, identity, repos)
		}
		if team, ok := daemon.CiSandboxTeams.Team(identity); ok {
			t.Errorf("%s: %s is still registered as team %q", label, identity, team)
		}
		// A later fetch with the same token must fail (401 unauthenticated,
		// where git prompts for a username or fails with terminal prompts disabled),
		// and is never recorded under the identity in the audit log (or forwarded).
		if out, err := withToken(a.token, "ls-remote", "--heads", proxyRemote); err == nil {
			t.Errorf("%s: %s's token still fetches after its attempt:\n%s", label, identity, out)
		}
		after := readAudit(t, auditPath)[auditSeen:]
		auditSeen += len(after)
		for _, l := range after {
			if l.Sandbox == identity {
				t.Errorf("%s: audit log still attributed request to released identity %s: %+v", label, identity, l)
			}
			if strings.HasPrefix(l.Outcome, "forwarded") {
				t.Errorf("%s: the released token's request was forwarded: %+v", label, l)
			}
		}
		return identity
	}

	// The declaration sync reads port-branches off main.
	cycle()
	if rc, err := store.GetRepoConfig(ctx, repo); err != nil || rc == nil || !slices.Equal(rc.PortTargets("main"), []string{"candidate"}) {
		t.Fatalf("repo config after the declaration sync: %+v %v", rc, err)
	}

	// --- 1. A clean one-commit change: ported by grain, through the proxy.
	clean := file("add a gadget", commitFile("GADGET.md", "a gadget", "feat: add a gadget"))
	land(clean, nil)
	port := onlyPort(clean)
	if !slices.Contains(port.Tags, model.TagMechanicalPort) {
		t.Fatalf("clean port tags = %v, want %s; body:\n%s", port.Tags, model.TagMechanicalPort, port.Body)
	}
	if port.Base != "candidate" || port.Approval == nil {
		t.Errorf("clean port base %q approval %+v", port.Base, port.Approval)
	}
	if st := state(port.ID); st != model.StateCompleted {
		t.Errorf("clean port state = %q, want completed with no agent run", st)
	}
	if runs, _ := store.Runs(ctx, port.ID); len(runs) != 0 {
		t.Errorf("clean port has %d runs, want none", len(runs))
	}
	pr := model.TaskPullRequest(port)
	if pr == nil {
		t.Fatalf("clean port links = %+v, want its pull request", port.Links)
	}
	opened := sim.prByNumber(pr.Number)
	if opened.Base != "candidate" || opened.Head != port.BranchName() {
		t.Fatalf("clean port pull request = %+v, want %s onto candidate", opened, port.BranchName())
	}
	msg := git("log", "-1", "--format=%B", "refs/heads/"+port.BranchName())
	cleanHead := git("rev-parse", "refs/heads/"+model.BranchName(clean))
	for _, want := range []string{"feat: add a gadget", "Backported-from: " + cleanHead, "(cherry picked from commit " + cleanHead + ")"} {
		if !strings.Contains(msg, want) {
			t.Errorf("clean port commit lacks %q:\n%s", want, msg)
		}
	}
	if committer := git("log", "-1", "--format=%cn <%ce>", "refs/heads/"+port.BranchName()); committer != "grain <grain@users.noreply.github.com>" {
		t.Errorf("clean port committer = %q", committer)
	}
	checkAttempt("clean port", true)
	land(port.ID, nil)
	if !ciStub.ranOn(port.BranchName()) {
		t.Errorf("grain CI never ran on the mechanical port's branch %s (runs on %v)", port.BranchName(), ciStub.refs)
	}
	git("cat-file", "-e", "candidate:GADGET.md")
	if back := portsOf(port.ID); len(back) != 0 {
		t.Fatalf("the mechanical port's merge onto candidate filed %d ports back: %+v", len(back), back)
	}

	// --- 2. Three commits: one already on candidate by patch id, one named
	// by a Backported-from footer there. Only the third is picked.
	pushTo("candidate", "candidate", "ALPHA.md", "alpha\n", "alpha, carried by hand")
	three := file("three commits", strings.Join([]string{
		commitFile("ALPHA.md", "alpha", "feat: alpha"),
		commitFile("BETA.md", "beta", "feat: beta"),
		commitFile("GAMMA.md", "gamma", "feat: gamma"),
	}, " && "))
	var alpha, beta, gamma string
	land(three, func() {
		shas := strings.Fields(git("log", "--reverse", "--format=%H", "candidate..refs/heads/"+model.BranchName(three), "--", "ALPHA.md", "BETA.md", "GAMMA.md"))
		if len(shas) != 3 {
			t.Fatalf("three-commit branch carries %v", shas)
		}
		alpha, beta, gamma = shas[0], shas[1], shas[2]
		pushTo("candidate", "candidate", "BETA.md", "beta, adapted for candidate\n",
			"feat: beta, adapted\n\nBackported-from: "+beta)
	})
	port = onlyPort(three)
	if !slices.Contains(port.Tags, model.TagMechanicalPort) {
		t.Fatalf("three-commit port is not mechanical: tags %v, body:\n%s", port.Tags, port.Body)
	}
	picked := strings.Split(git("log", "--format=%s", "candidate..refs/heads/"+port.BranchName()), "\n")
	if !slices.Equal(picked, []string{"feat: gamma"}) {
		t.Errorf("three-commit port picked %q, want only feat: gamma", picked)
	}
	for _, want := range []string{"cherry-picked 1 commit(s)", gamma[:12], "Left out " + alpha[:12], "Left out " + beta[:12]} {
		if !strings.Contains(port.Body, want) {
			t.Errorf("three-commit port body lacks %q:\n%s", want, port.Body)
		}
	}
	checkAttempt("three-commit port", true)
	land(port.ID, nil)
	if got := git("show", "candidate:BETA.md"); got != "beta, adapted for candidate" {
		t.Errorf("candidate's BETA.md = %q: the footer-named commit was picked over the hand port", got)
	}

	// --- 3. A conflict: falls back to an agent port task naming the
	// commit and the path, and nothing is pushed.
	pushTo("candidate", "candidate", "NOTES.md", "candidate's notes\n", "docs: candidate's own notes")
	conflict := file("rewrite the notes", commitFile("NOTES.md", "main's notes", "docs: main's notes"))
	land(conflict, nil)
	port = onlyPort(conflict)
	conflictHead := git("rev-parse", "refs/heads/"+model.BranchName(conflict))
	if slices.Contains(port.Tags, model.TagMechanicalPort) {
		t.Errorf("conflicting port is tagged %s", model.TagMechanicalPort)
	}
	for _, want := range []string{"stopped on a conflict", conflictHead, "`NOTES.md`", "Nothing was pushed"} {
		if !strings.Contains(port.Body, want) {
			t.Errorf("conflicting port body lacks %q:\n%s", want, port.Body)
		}
	}
	if st := state(port.ID); st != model.StateQueued {
		t.Errorf("conflicting port state = %q, want queued for an agent", st)
	}
	if remoteHas(port.BranchName()) {
		t.Errorf("a conflicting port pushed %s", port.BranchName())
	}
	checkAttempt("conflicting port", false)
	land(port.ID, nil)
	if runs, _ := store.Runs(ctx, port.ID); len(runs) == 0 {
		t.Errorf("the conflicting port landed with no agent run")
	}

	// --- 4. The port's branch already exists: refused, never forced.
	stray := map[string]string{}
	preexisting := file("add a widget rack", commitFile("RACK.md", "a rack", "feat: add a widget rack"))
	land(preexisting, func() {
		// The port's id is the next the store hands out after this
		// cycle's own; a stray branch for each of the next few covers it.
		next, err := store.NewTaskID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := strconv.Atoi(next)
		for i := 1; i <= 4; i++ {
			b := model.BranchName(strconv.Itoa(n + i))
			stray[b] = pushTo(b, "candidate", "STRAY.md", "somebody else's\n", "wip: somebody else's branch")
		}
	})
	port = onlyPort(preexisting)
	strayHead, ok := stray[port.BranchName()]
	if !ok {
		t.Fatalf("port %s's branch %s is none of the strays %v", port.ID, port.BranchName(), stray)
	}
	if slices.Contains(port.Tags, model.TagMechanicalPort) {
		t.Errorf("a port whose branch already existed is tagged %s", model.TagMechanicalPort)
	}
	for _, want := range []string{"could not finish it", "already exists", "nothing was pushed"} {
		if !strings.Contains(port.Body, want) {
			t.Errorf("refused port body lacks %q:\n%s", want, port.Body)
		}
	}
	if got := git("rev-parse", "refs/heads/"+port.BranchName()); got != strayHead {
		t.Errorf("the porter moved %s from %s to %s", port.BranchName(), strayHead, got)
	}
	checkAttempt("pre-existing branch", false)
	// Somebody deletes the stale branch; the agent then makes the port.
	for b := range stray {
		git("update-ref", "-d", "refs/heads/"+b)
	}
	land(port.ID, nil)

	// --- 5. Every attempt was its own identity.
	if len(attempts) != 4 {
		t.Errorf("mechanical attempts = %d, want 4", len(attempts))
	}
	for i, a := range attempts {
		for _, b := range attempts[i+1:] {
			if a.token == b.token {
				t.Errorf("two attempts shared a proxy token")
			}
		}
	}

	// --- 6. The merges metrics, as GET /api/metrics?section=merges reads
	// them: two mechanical ports, two agent ports, none repaired. A
	// merge's time is GitHub's merged_at, which githubsim stamps from the
	// wall clock rather than the cycles' clock, so the window spans both.
	report, err := ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice")}, store).Metrics(ctx, ui.MetricsRequest{
		Since: baseTime.Add(-time.Hour), Until: time.Now().Add(time.Hour), Section: "merges",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := report.Merges.Ports, (ui.MetricsMergePorts{Mechanical: 2, Agent: 2, Repaired: 0, MechanicalRate: 0.5}); got != want {
		t.Errorf("merges ports = %+v, want %+v", got, want)
	}
}

// prByNumber is the sim's pull request number n.
func (s *syncedSim) prByNumber(n int) githubsim.PullRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pr := range s.sim.PullRequests {
		if pr.Number == n {
			return pr
		}
	}
	return githubsim.PullRequest{}
}

// TestReadOnlyPushIsRefusedWithAReasonGitPrints is §3 step 5: a sandbox
// that may only read a repository runs a real `git push` through the
// real git proxy, credentialed the way a sandbox is
// (mcp.ConfigureGitCredentials), and what git prints names the reason --
// not the "RPC failed; HTTP 403" a refused git-receive-pack POST used to
// end in. The read scope is a CI job's (daemon.CiReadScopes), the
// registry grain CI grants a `repo-read:` job into.
func TestReadOnlyPushIsRefusedWithAReasonGitPrints(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	const owner, repoName = "acme", "readonly"
	repo := model.RepoRef{Owner: owner, Name: repoName}
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", "-q", bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	mustWriteFile(t, filepath.Join(seed, "README.md"), "seed\n")
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "commit", "-q", "-m", "initial commit")
	run(t, seed, "git", "push", "-q", "origin", "main")
	before := strings.TrimSpace(runOutput(t, upstream, "git", "--git-dir", bare, "rev-parse", "main"))

	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	mustWriteFile(t, filepath.Join(dataDir, "secrets", "github", "credentials.json"), `{"*": "anonymous"}`)
	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir: dataDir, Store: store, ForwardHost: mustHost(t, newTestServer(t, gitHTTPBackend(upstream))),
		ReadScopes: daemon.CiReadScopes, WriteScopes: daemon.CiWriteScopes, SandboxTeams: daemon.CiSandboxTeams,
	})
	if err != nil {
		t.Fatal(err)
	}
	// What the daemon does at startup: the ladder above becomes the
	// deployment's git connections, which is all the proxy reads.
	if err := store.UpdateConfig(context.Background(), func(t *model.Config) error {
		t.GitConnections = append(t.GitConnections, model.TeamGitConnection{Credential: "anonymous"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	proxyURL := newTestServer(t, gitproxy.NewHandler(proxy))
	tokens := gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "secrets", "sandbox-tokens.json"))

	sandbox := "ci-job-readonly-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	token, err := tokens.EnsureToken(sandbox)
	if err != nil {
		t.Fatal(err)
	}
	daemon.CiReadScopes.Grant(sandbox, repo)
	t.Cleanup(func() { daemon.CiReadScopes.Drop(sandbox) })
	home := t.TempDir()
	if err := mcp.ConfigureGitCredentials(home, proxyURL+"/placeholder/placeholder.git", token, mcp.GitIdentity{}); err != nil {
		t.Fatal(err)
	}
	gitAs := func(dir string, args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "HOME="+home, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
			"GIT_AUTHOR_NAME=ci", "GIT_AUTHOR_EMAIL=ci@example.com", "GIT_COMMITTER_NAME=ci", "GIT_COMMITTER_EMAIL=ci@example.com")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// It may fetch...
	if out, err := gitAs(home, "clone", "-q", proxyURL+"/"+owner+"/"+repoName+".git", "work"); err != nil {
		t.Fatalf("a read-scoped clone failed: %v\n%s", err, out)
	}
	work := filepath.Join(home, "work")
	mustWriteFile(t, filepath.Join(work, "CHANGE.md"), "change\n")
	if out, err := gitAs(work, "add", "-A"); err != nil {
		t.Fatal(out)
	}
	if out, err := gitAs(work, "commit", "-q", "-m", "a change"); err != nil {
		t.Fatal(out)
	}
	// ...but not push, and git says why.
	out, err := gitAs(work, "push", "origin", "HEAD:refs/heads/ci-change")
	t.Logf("git push as a read-only sandbox:\n%s", out)
	if err == nil {
		t.Fatalf("a read-scoped push succeeded:\n%s", out)
	}
	if want := gitproxy.ReadOnlyPushReason(owner, repoName); !strings.Contains(out, want) {
		t.Errorf("git push printed no reason; want %q in:\n%s", want, out)
	}
	if strings.Contains(out, "RPC failed") {
		t.Errorf("git push ended in a bare RPC failure:\n%s", out)
	}
	if got := strings.TrimSpace(runOutput(t, upstream, "git", "--git-dir", bare, "for-each-ref", "--format=%(refname)", "refs/heads/ci-change")); got != "" {
		t.Errorf("the refused push created %s", got)
	}
	if got := strings.TrimSpace(runOutput(t, upstream, "git", "--git-dir", bare, "rev-parse", "main")); got != before {
		t.Errorf("main moved from %s to %s", before, got)
	}
	lines := readAudit(t, filepath.Join(dataDir, "state", "git-proxy", "audit.log"))
	if len(lines) == 0 || lines[len(lines)-1].Outcome != "denied: push to read-only repository" || lines[len(lines)-1].Sandbox != sandbox {
		t.Errorf("audit log's last entry = %+v, want %s's push denied as read-only", lines[len(lines)-1:], sandbox)
	}
	// The credential git stored is still there: a 403 is not a 401, so
	// git does not erase it, and the next fetch still works.
	if out, err := gitAs(work, "fetch", "-q", "origin"); err != nil {
		t.Errorf("a fetch after the refused push failed: %v\n%s", err, out)
	}
}

// commitMessage is the shell for one commit writing content to file,
// whose message is paragraphs, each its own -m (a footer is a paragraph).
func commitMessage(file, content string, paragraphs ...string) string {
	cmd := fmt.Sprintf("printf '%%s\\n' %q > %s && git add %s && git commit -q", content, file, file)
	for _, p := range paragraphs {
		cmd += fmt.Sprintf(" -m %q", p)
	}
	return cmd
}

// TestPortTypesWalkthrough is docs/test-plans/2026-10-11.md §1: the
// per-direction `port-types` filter end to end. A paired-branch
// repository declares `port-types: {main->candidate: [fix, revert]}` in
// its own .grain/settings.yml, the real declaration sync reads it into
// RepoConfig, and the real merge queue lands each pull request through
// orchestrator.RunCycle, whose close-out (filePorts/decidePort) files a
// port task to the other branch or logs why it does not. Then the
// operator's own column (RepoConfig.PortTypes) is set beside the
// declaration, and malformed declarations are pushed for the sync to
// report.
//
// GitHub is the one stub (githubsim over a bare repository, served by git
// http-backend); CI is greenCI and the agent is scriptAgent, as in
// TestPortMechanicalWalkthrough. Not t.Parallel: it reads the skip lines
// off the global logger (captureLog).
func TestPortTypesWalkthrough(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	logs := captureLog(t)
	const owner, repoName = "acme", "porttypes"
	repo := model.RepoRef{Owner: owner, Name: repoName}
	const settings = "port-branches: [main, candidate]\n" +
		"port-types:\n  main->candidate: [fix, revert]\n"

	// main declaring the pair and the filter, with one feature candidate
	// is cut with (so reverting it on main has something to revert on
	// candidate too).
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", "-q", bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	mustWriteFile(t, filepath.Join(seed, "NOTES.md"), "seed\n")
	mustWriteFile(t, filepath.Join(seed, ".grain", "settings.yml"), settings)
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "commit", "-q", "-m", "initial commit")
	mustWriteFile(t, filepath.Join(seed, "LEGACY.md"), "legacy\n")
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "commit", "-q", "-m", "feat: add legacy")
	run(t, seed, "git", "push", "-q", "origin", "main", "main:candidate")
	git := func(args ...string) string {
		t.Helper()
		return strings.TrimSpace(runOutput(t, upstream, "git", append([]string{"--git-dir", bare}, args...)...))
	}
	pushTo := func(branch, file, content, message string) string {
		t.Helper()
		dir := t.TempDir()
		run(t, dir, "git", "clone", "-q", bare, "w")
		wd := filepath.Join(dir, "w")
		run(t, wd, "git", "config", "user.email", "human@example.com")
		run(t, wd, "git", "config", "user.name", "human")
		run(t, wd, "git", "checkout", "-q", "-B", branch, "origin/"+branch)
		mustWriteFile(t, filepath.Join(wd, file), content)
		run(t, wd, "git", "add", "-A")
		run(t, wd, "git", "commit", "-q", "-m", message)
		run(t, wd, "git", "push", "-q", "origin", "HEAD:refs/heads/"+branch)
		return strings.TrimSpace(runOutput(t, wd, "git", "rev-parse", "HEAD"))
	}

	// The one external boundary stubbed: GitHub.
	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)
	remote := "http://" + githubHost + "/" + owner + "/" + repoName + ".git"
	client := github.NewClient(sim, nil)

	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, Declarations: true}); err != nil {
		t.Fatal(err)
	}
	agentStub := &scriptAgent{remote: remote, store: store, scripts: map[string]string{}}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: credentialed(t, remote), MaxWorkers: 1,
		Declarations: bareDeclarations{t: t, bare: bare},
		CI:           &greenCI{runs: map[string]ci.Run{}},
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			return agentStub, nil
		},
	}
	at := baseTime
	cycle := func() {
		t.Helper()
		at = at.Add(10 * time.Minute)
		if err := orchestrator.RunCycle(ctx, deps, at); err != nil {
			t.Fatalf("RunCycle at %s: %v", at, err)
		}
	}
	state := func(id string) model.State {
		t.Helper()
		st, err := store.State(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	land := func(id string) {
		t.Helper()
		cycle()
		for i := 0; i < 8; i++ {
			for _, pr := range sim.openPullRequests() {
				sim.markPullRequestClean(pr.Number)
			}
			cycle()
			if state(id) == model.StateClosed {
				return
			}
		}
		got, _ := store.GetTask(ctx, id)
		comments, _ := store.Comments(ctx, id)
		runs, _ := store.Runs(ctx, id)
		t.Fatalf("%s never merged: state %q, task %+v, runs %+v, comments %+v\nlog:\n%s", id, state(id), got, runs, comments, logs.String())
	}
	portsOf := func(source string) []model.Task {
		t.Helper()
		all, err := store.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []model.Task
		for _, tk := range all {
			if slices.Contains(tk.Links, model.Link{Kind: model.LinkPortOf, Target: source}) {
				out = append(out, tk)
			}
		}
		return out
	}
	file := func(base, title, script string) string {
		t.Helper()
		id, err := store.NewTaskID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		orderKey, err := store.OrderKeyForNewTask(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		now := at
		if err := store.PutTask(ctx, model.Task{
			ID: id, Title: title, Body: "body of " + title,
			Intent:   model.IntentImplement,
			Origin:   model.Origin{Attribution: model.Attribution{Actor: human("alice")}, Reason: model.ReasonDirect},
			Approval: &model.Attribution{Actor: human("alice")}, ApprovedAt: &now,
			Target: &repo, Base: base, AutoMerge: true,
			OrderKey: orderKey, CreatedAt: &now,
		}); err != nil {
			t.Fatal(err)
		}
		agentStub.mu.Lock()
		agentStub.scripts[title] = script
		agentStub.mu.Unlock()
		return id
	}
	rc := func() model.RepoConfig {
		t.Helper()
		got, err := store.GetRepoConfig(ctx, repo)
		if err != nil || got == nil {
			t.Fatalf("repo config: %+v %v", got, err)
		}
		return *got
	}
	// skipLine is filePorts' log line for task id, or "" if it logged none.
	skipLine := func(id string) string {
		for _, l := range strings.Split(logs.String(), "\n") {
			if strings.Contains(l, "orchestrator: not porting ") && strings.Contains(l, "(task "+id+")") {
				return l[strings.Index(l, "orchestrator: "):]
			}
		}
		return ""
	}
	typeSkip := "`candidate`: not a fix/revert change and no `Port: yes` footer (`main` ports only those types to `candidate`); " +
		"it reaches `candidate` some other way, such as the next release cut"
	// skipNotes is what the merge queue said on task id about not porting
	// it (filePorts' notePortSkips): the log line's reason, on the task.
	skipNotes := func(id string) []string {
		t.Helper()
		comments, err := store.Comments(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range comments {
			if c.Author.Actor.ID == "merge-queue" && strings.Contains(c.Body, "but grain did not port it there") {
				out = append(out, c.Body)
			}
		}
		return out
	}
	// expect lands a change and checks whether it was ported onto to, and
	// the skip line it logged, and the one comment it left on the task,
	// if it was not. Every skip this walkthrough makes -- port-types,
	// a revert of what to lacks, `Port: no` -- is one somebody reading
	// the merged task needs told.
	expect := func(label, id, to string, wantPort bool, wantSkip string) {
		t.Helper()
		land(id)
		ports := portsOf(id)
		switch {
		case wantPort && (len(ports) != 1 || ports[0].Base != to):
			t.Errorf("%s: ports = %+v, want one onto %s; logged %q", label, ports, to, skipLine(id))
		case !wantPort && len(ports) != 0:
			t.Errorf("%s: ported (%+v), want no port", label, ports)
		}
		line := skipLine(id)
		if wantSkip == "" && line != "" {
			t.Errorf("%s: logged %q, want no skip", label, line)
		}
		if wantSkip != "" && !strings.HasSuffix(line, wantSkip) {
			t.Errorf("%s: skip line = %q, want it to end %q", label, line, wantSkip)
		}
		notes := skipNotes(id)
		if wantSkip == "" && len(notes) != 0 {
			t.Errorf("%s: noted %q on the task, want no skip", label, notes)
		}
		if wantSkip != "" {
			why := strings.TrimPrefix(wantSkip, "`"+to+"`: ")
			want := "which `" + owner + "/" + repoName + "` keeps paired with `" + to + "`, but grain did not port it there: " + why + "."
			if len(notes) != 1 || !strings.HasPrefix(notes[0], "Merged ") || !strings.HasSuffix(notes[0], want) {
				t.Errorf("%s: noted %q on the task, want one comment ending %q", label, notes, want)
			}
		}
		t.Logf("%s: ports %d; %s; noted %q", label, len(ports), line, notes)
	}

	// --- The declaration sync reads the filter off main.
	cycle()
	if got := rc(); !slices.Equal(got.PortTypeFilter("main", "candidate"), []string{"fix", "revert"}) ||
		got.PortTypeFilter("candidate", "main") != nil {
		t.Fatalf("after the declaration sync: main->candidate %v, candidate->main %v (config %+v)",
			got.PortTypeFilter("main", "candidate"), got.PortTypeFilter("candidate", "main"), got)
	}

	// A fix made on candidate directly: the commit a later port on main
	// names in its Backported-from footer.
	hotfix := pushTo("candidate", "HOTFIX.md", "hotfix", "fix: a hotfix made on candidate")

	// --- Merges onto main, through the real queue.
	expect("only feat:", file("main", "only feat", commitMessage("ALPHA.md", "alpha", "feat: add alpha")),
		"candidate", false, typeSkip)
	expect("feat: + fix(scope):", file("main", "feat and fix",
		commitMessage("BETA.md", "beta", "feat: add beta")+" && "+commitMessage("BETA2.md", "beta fixed", "fix(beta): mend beta")),
		"candidate", true, "")
	expect("feat: with Port: yes", file("main", "feat port yes", commitMessage("GAMMA.md", "gamma", "feat: add gamma", "Port: yes")),
		"candidate", true, "")
	expect("fix: with Port: no", file("main", "fix port no", commitMessage("DELTA.md", "delta", "fix: mend delta", "Port: no")),
		"candidate", false, "`candidate`: the change carries a `Port: no` footer")
	expect(`Revert "feat: ..."`, file("main", "revert legacy",
		"git revert --no-edit $(git log -1 --format=%H --grep='^feat: add legacy$' HEAD)"),
		"candidate", true, "")
	// A revert of a feature the filter kept off candidate is not ported:
	// candidate has nothing to revert (portSkip's revertsAbsent).
	revertUnported := file("main", "revert alpha",
		"git revert --no-edit $(git log -1 --format=%H --grep='^feat: add alpha$' HEAD)")
	expect(`Revert "feat: ..." of a feature candidate lacks`, revertUnported, "candidate", false,
		"`candidate`: a revert of a change `candidate` does not have, and no `Port: yes` footer "+
			"(`main` ports only fix/revert changes to `candidate`); there is nothing on `candidate` to revert")
	if got := git("ls-tree", "--name-only", "candidate", "ALPHA.md"); got != "" {
		t.Errorf("candidate has %s, which the only-feat change should not have brought", got)
	}

	// --- docs/test-plans/2026-10-13.md §9 steps 3-4: the same revert
	// made by grain's own revert_task (mcp.GitTaskReverter, which reverts
	// the task's merge commit with -m 1 and writes Reverts-Commit), a
	// merge-commit revert by git, and reverts of fixes that were ported,
	// which are ported in turn.
	revertSkip := "`candidate`: a revert of a change `candidate` does not have, and no `Port: yes` footer " +
		"(`main` ports only fix/revert changes to `candidate`); there is nothing on `candidate` to revert"
	mergeOf := func(id string) string {
		t.Helper()
		sha := git("log", "-1", "--merges", "--first-parent", "--format=%H", "--fixed-strings",
			"--grep="+model.BranchName(id)+"\n", "main")
		if sha == "" {
			sha = git("log", "-1", "--merges", "--first-parent", "--format=%H", "--fixed-strings", "--grep="+model.BranchName(id), "main")
		}
		if sha == "" {
			t.Fatalf("no merge of %s on main", id)
		}
		return sha
	}
	// revertTask runs revert_task's reverter for task id on a clone of
	// main, pushes the result as a scratch branch and returns the script
	// a task runs to take that commit as its own change.
	revertTask := func(id string) string {
		t.Helper()
		dir := t.TempDir()
		run(t, dir, "git", "clone", "-q", bare, "w")
		wd := filepath.Join(dir, "w")
		run(t, wd, "git", "config", "user.email", "agent@example.com")
		run(t, wd, "git", "config", "user.name", "agent")
		res, err := mcp.NewGitTaskReverter(wd).RevertTask(ctx, mcp.RevertTaskRequest{
			TaskID: id, Reason: "walkthrough: undo task " + id})
		if err != nil {
			t.Fatalf("revert_task %s: %v", id, err)
		}
		// The task's merge commit, not some other task's whose id
		// starts with the same digits.
		if want := mergeOf(id); res.CommitSHA != want {
			t.Errorf("revert_task %s reverted %s, not the task's merge commit %s", id, res.CommitSHA, want)
		}
		scratch := "scratch/revert-" + id
		run(t, wd, "git", "push", "-q", "origin", "HEAD:refs/heads/"+scratch)
		t.Logf("revert_task %s: reverted %s as %s:\n%s", id, res.CommitSHA, res.NewCommitSHA,
			runOutput(t, wd, "git", "log", "-1", "--format=%B", res.NewCommitSHA))
		return "git reset -q --hard origin/" + scratch
	}
	landPort := func(source string) {
		t.Helper()
		ports := portsOf(source)
		if len(ports) != 1 {
			t.Fatalf("%s: ports %+v, want one", source, ports)
		}
		land(ports[0].ID)
	}

	featMu := file("main", "feat mu", commitMessage("MU.md", "mu", "feat: add mu"))
	expect("feat: mu", featMu, "candidate", false, typeSkip)
	expect("revert_task of feat: mu", file("main", "revert_task mu", revertTask(featMu)), "candidate", false, revertSkip)

	featNu := file("main", "feat nu", commitMessage("NU.md", "nu", "feat: add nu"))
	expect("feat: nu", featNu, "candidate", false, typeSkip)
	expect("git revert -m 1 of feat: nu's merge commit", file("main", "revert merge nu",
		"git revert --no-edit -m 1 "+mergeOf(featNu)), "candidate", false, revertSkip)

	fixXi := file("main", "fix xi", commitMessage("XI.md", "xi", "fix: mend xi"))
	expect("fix: xi", fixXi, "candidate", true, "")
	landPort(fixXi)
	expect(`Revert "fix: xi" (ported)`, file("main", "revert xi",
		"git revert --no-edit $(git log -1 --format=%H --grep='^fix: mend xi$' HEAD)"), "candidate", true, "")

	fixOmicron := file("main", "fix omicron", commitMessage("OMICRON.md", "omicron", "fix: mend omicron"))
	expect("fix: omicron", fixOmicron, "candidate", true, "")
	landPort(fixOmicron)
	expect("revert_task of fix: omicron (ported, merge commit)", file("main", "revert_task omicron", revertTask(fixOmicron)),
		"candidate", true, "")
	expect("fix!:", file("main", "fix bang", commitMessage("EPSILON.md", "epsilon", "fix!: mend epsilon incompatibly")),
		"candidate", true, "")
	expect("only fix: is a port", file("main", "feat and ported fix",
		commitMessage("ZETA.md", "zeta", "feat: add zeta")+" && "+
			commitMessage("HOTFIX.md", "hotfix", "fix: a hotfix made on candidate", "Backported-from: "+hotfix)),
		"candidate", false, typeSkip)
	// candidate -> main has no entry: a feature merged on candidate is
	// still ported to main.
	expect("feat: on candidate", file("candidate", "feat on candidate", commitMessage("ETA.md", "eta", "feat: add eta on candidate")),
		"main", true, "")

	// --- The operator layer. grain has no HTTP route for the operator's
	// port columns (only the grain/config state repository writes them),
	// so they are written to the store the way that import does.
	op := rc()
	op.PortBranches = []string{"main", "candidate"}
	op.PortTypes = map[string][]string{model.PortDirection("main", "candidate"): {"fix"}}
	if err := store.PutRepoConfig(ctx, op); err != nil {
		t.Fatal(err)
	}
	// The operator pairs the branches and ports only fixes; the
	// repository's [fix, revert] widens that to reverts as well...
	if got := rc().PortTypeFilter("main", "candidate"); !slices.Equal(got, []string{"fix", "revert"}) {
		t.Errorf("operator [fix] + declared [fix, revert] = %v, want [fix revert]", got)
	}
	expect("revert: under operator [fix]", file("main", "revert type", commitMessage("THETA.md", "theta", "revert: drop theta")),
		"candidate", true, "")
	expect("feat: under operator [fix]", file("main", "feat under operator", commitMessage("IOTA.md", "iota", "feat: add iota")),
		"candidate", false, typeSkip)
	// ...but on a pair the operator configured with no list, the
	// declaration cannot narrow it: every merge is ported.
	op = rc()
	op.PortTypes = nil
	if err := store.PutRepoConfig(ctx, op); err != nil {
		t.Fatal(err)
	}
	if got := rc().PortTypeFilter("main", "candidate"); got != nil {
		t.Errorf("operator pair with no list + declared [fix, revert] = %v, want nil (no narrowing)", got)
	}
	expect("feat: under an operator pair", file("main", "feat under operator pair", commitMessage("KAPPA.md", "kappa", "feat: add kappa")),
		"candidate", true, "")
	// ...and the declaration says it is ignored rather than reading as
	// accepted.
	if w := rc().PortTypesWarnings(); len(w) != 1 || !strings.Contains(w[0], "main->candidate is ignored") {
		t.Errorf("operator pair with no list: port-types warnings = %q, want the declared main->candidate named as ignored", w)
	}
	op = rc()
	op.PortBranches, op.PortTypes = nil, nil
	if err := store.PutRepoConfig(ctx, op); err != nil {
		t.Fatal(err)
	}

	// --- Malformed declarations: what the sync reports, and what it keeps.
	declare := func(label, body string) model.RepoConfig {
		t.Helper()
		pushTo("main", ".grain/settings.yml", "port-branches: [main, candidate]\n"+body, "chore: declare "+label)
		// A declaration that does not parse fails the cycle's sync step,
		// which RunCycle reports alongside running everything else.
		at = at.Add(10 * time.Minute)
		if err := orchestrator.RunCycle(ctx, deps, at); err != nil {
			t.Logf("%s: RunCycle: %v", label, err)
		}
		sync, err := store.GetRepoDeclarationSync(ctx, repo)
		if err != nil || sync == nil {
			t.Fatalf("%s: declaration sync %+v %v", label, sync, err)
		}
		got := rc()
		t.Logf("%s: sync error %q; declared port-types %v", label, sync.LastError, got.DeclaredPortTypes)
		return got
	}
	want := map[string][]string{"main->candidate": {"fix", "revert"}}
	for _, bad := range []struct{ label, body, err string }{
		{"main-candidate", "port-types:\n  main-candidate: [fix, revert]\n", `"main-candidate" is not a direction`},
		{"an empty list", "port-types:\n  main->candidate: []\n", "main->candidate names no types"},
	} {
		got := declare(bad.label, bad.body)
		sync, _ := store.GetRepoDeclarationSync(ctx, repo)
		if !strings.Contains(sync.LastError, bad.err) {
			t.Errorf("%s: sync error %q, want it to say %q", bad.label, sync.LastError, bad.err)
		}
		if !reflect.DeepEqual(got.DeclaredPortTypes, want) {
			t.Errorf("%s: declared port-types became %v, want the last good %v kept", bad.label, got.DeclaredPortTypes, want)
		}
	}
	got := declare("an unknown branch", "port-types:\n  main->release: [fix]\n")
	if sync, _ := store.GetRepoDeclarationSync(ctx, repo); sync.LastError != "" {
		t.Errorf("an unknown branch: sync error %q", sync.LastError)
	}
	if !reflect.DeepEqual(got.DeclaredPortTypes, map[string][]string{"main->release": {"fix"}}) || got.PortTypeFilter("main", "candidate") != nil {
		t.Errorf("an unknown branch: declared %v, main->candidate filter %v", got.DeclaredPortTypes, got.PortTypeFilter("main", "candidate"))
	}
	// Accepted, since it grants nothing, but named as having no effect.
	if w := got.PortTypesWarnings(); len(w) != 1 || !strings.Contains(w[0], "main->release names release, which is not a paired branch") {
		t.Errorf("an unknown branch: port-types warnings = %q, want main->release named as unpaired", w)
	}
}

// TestGitHTTPBackendServesAGzippedRequest is the regression test for the
// stand-in GitHub TestPortTypesWalkthrough first tripped over: once a
// repository has enough refs, git gzips its upload-pack request, and
// gitHTTPBackend handed http-backend the compressed body without saying
// so -- "protocol error: bad line length character", an HTTP 500 and a
// failed clone.
func TestGitHTTPBackendServesAGzippedRequest(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, "acme", "many-refs.git"), "main")
	parent := t.TempDir()
	run(t, parent, "git", "clone", "-q", bare, "seed")
	seed := filepath.Join(parent, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	refspecs := []string{"main"}
	for i := 0; i < 40; i++ {
		mustWriteFile(t, filepath.Join(seed, "FILE.md"), fmt.Sprintf("%d\n", i))
		run(t, seed, "git", "add", "-A")
		run(t, seed, "git", "commit", "-q", "-m", fmt.Sprintf("commit %d", i))
		refspecs = append(refspecs, fmt.Sprintf("HEAD:refs/heads/branch-%d", i))
	}
	run(t, seed, "git", append([]string{"push", "-q", "origin"}, refspecs...)...)

	url := newTestServer(t, gitHTTPBackend(upstream)) + "/acme/many-refs.git"
	cmd := exec.Command("git", "-c", "protocol.version=2", "clone", "--mirror", "-q", url, filepath.Join(t.TempDir(), "mirror"))
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_TRACE_CURL=1", "GIT_TRACE_CURL_NO_DATA=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cloning 41 refs through gitHTTPBackend: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Content-Encoding: gzip") {
		t.Errorf("git sent no gzipped request, so this proves nothing; trace:\n%s", out)
	}
}
