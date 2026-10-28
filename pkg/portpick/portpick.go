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

// Package portpick makes a port mechanically: the commits of a merged
// pull request cherry-picked onto the other branch of a pair, by grain
// itself on its own host, with no sandbox and no model behind it.
//
// It is the first attempt at a port, not the only one. The orchestrator
// (pkg/orchestrator/port.go) asks it first and files the agent port task
// it always filed when this comes back with anything but a clean result:
// a conflict, a git failure, a credential it cannot get. So everything
// here prefers giving up to guessing.
//
// It follows the steps the agent port task is given (orchestrator's
// portBody) and writes what they write -- `git cherry-pick -x`, plus a
// `Backported-from:` footer -- so a mechanical port and an agent's look
// the same to everything that reads them afterwards: the merge queue's
// port decision, the nightly triage's `git cherry`.
//
// # What it may touch
//
// One ref on the remote: refs/heads/<Request.Branch>, created, never
// moved. It checks the branch is not there first, and pushes without a
// `+` refspec or --force, so git itself refuses anything but a create or
// a fast-forward. It never pushes to the source or target branch (a
// request naming either as its own branch is refused). Locally it works
// in a cache repository of its own under Porter.Dir, one per repository,
// and in a throwaway worktree of it that is removed when it returns.
package portpick

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitrefs"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Remote is how one repository is reached: a URL git can fetch from and
// push to, and the credential it does so with. The token is handed to git
// through the environment (an http.extraHeader set by GIT_CONFIG_*), never
// on a command line or in a URL, where a process listing or an error
// message would show it.
type Remote struct {
	URL   string
	Token string
	// Release, if set, is called once the attempt is done with the
	// remote: where a credential minted for this one attempt (the
	// daemon's git-proxy identity) is revoked.
	Release func()
}

// Request is one port to make.
type Request struct {
	Repo model.RepoRef
	// Source is the branch the pull request merged onto, and Target the
	// branch it is being ported to.
	Source, Target string
	// PullRequest is the merged pull request's number, for messages.
	PullRequest int
	// Head is the merged pull request's head commit.
	Head string
	// Commits are the pull request's own commits Target lacks, as GitHub
	// lists them (orchestrator's lackedBy): what is ported, less anything
	// git finds already there by content or by footer.
	Commits []string
	// Branch is the port's own branch: the only ref this pushes.
	Branch string
	// CredentialTask is the task whose git credential the port is made
	// with -- the merged change's own, which the port inherits.
	CredentialTask string
}

// Outcome is how a port attempt came out.
type Outcome string

const (
	// Picked: every commit applied cleanly and Branch was pushed.
	Picked Outcome = "picked"
	// NothingToPort: every commit is already on Target, by content or by
	// a footer naming it. Nothing was pushed.
	NothingToPort Outcome = "nothing"
	// Conflicted: a commit did not apply. Nothing was pushed.
	Conflicted Outcome = "conflicted"
)

// Pick is one commit ported: the original and the commit made of it.
type Pick struct {
	Source string
	Ported string
}

// Dropped is one commit left out, and why.
type Dropped struct {
	Commit string
	Why    string
}

// Conflict is the commit that did not apply and the paths it conflicted
// on.
type Conflict struct {
	Commit  string
	Subject string
	Paths   []string
}

// Result is what a port attempt did.
type Result struct {
	Outcome  Outcome
	Picked   []Pick
	Dropped  []Dropped
	Conflict *Conflict
	// Head is Branch's tip as pushed, for Picked.
	Head string
}

// DefaultTimeout bounds one port attempt, fetch included. The first
// fetch of a repository fills its cache and is the slow one; after that
// a fetch is what has merged since.
const DefaultTimeout = 5 * time.Minute

// Porter makes ports. The zero value is not usable: Dir and Remote are
// required.
type Porter struct {
	// Dir holds one cache repository per repository ported in.
	Dir string
	// Remote resolves how to reach repo with the credential of task.
	Remote func(ctx context.Context, repo model.RepoRef, task string) (Remote, error)
	// Committer is the identity the ported commits are committed as; the
	// author stays the original's, as `git cherry-pick` keeps it.
	CommitterName, CommitterEmail string
	// Timeout bounds one attempt; zero is DefaultTimeout.
	Timeout time.Duration

	mu    sync.Mutex
	repos map[string]*sync.Mutex
}

var (
	shaPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	// portedFooter is the orchestrator's own (port.go): the footer a port
	// writes, or the line `git cherry-pick -x` writes.
	portedFooter = regexp.MustCompile(`(?mi)^[ \t]*(?:Backported-from:[ \t]*([0-9a-f]{7,40})|\(cherry picked from commit ([0-9a-f]{7,40})\))`)
)

// Port makes req's port, or says why it did not. An error is a port that
// could not be attempted or could not finish -- nothing to say about the
// change itself -- and the caller falls back to an agent for it exactly
// as it does for a conflict.
func (p *Porter) Port(ctx context.Context, req Request) (Result, error) {
	if err := validate(req); err != nil {
		return Result{}, err
	}
	if p.Dir == "" || p.Remote == nil {
		return Result{}, errors.New("portpick: no cache directory or remote configured")
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	remote, err := p.Remote(ctx, req.Repo, req.CredentialTask)
	if err != nil {
		return Result{}, fmt.Errorf("portpick: reaching %s: %w", req.Repo, err)
	}
	if remote.Release != nil {
		defer remote.Release()
	}
	lock := p.repoLock(req.Repo.String())
	lock.Lock()
	defer lock.Unlock()

	g := &gitRunner{remote: remote, name: p.CommitterName, email: p.CommitterEmail}
	g.dir = filepath.Join(p.Dir, safeName(req.Repo.Owner), safeName(req.Repo.Name)+".git")
	if err := g.ensureCache(ctx); err != nil {
		return Result{}, err
	}
	// Both branches of the pair, into refs of this cache's own: the
	// target to port onto, and the source, which the merged head is
	// reachable from.
	targetRef := "refs/portpick/" + req.Target
	sourceRef := "refs/portpick/" + req.Source
	if _, err := g.run(ctx, "", "fetch", "--no-tags", "--quiet", "--end-of-options", remote.URL,
		"+refs/heads/"+req.Target+":"+targetRef,
		"+refs/heads/"+req.Source+":"+sourceRef); err != nil {
		return Result{}, fmt.Errorf("portpick: fetching %s and %s of %s: %w", req.Target, req.Source, req.Repo, err)
	}
	if _, err := g.run(ctx, "", "cat-file", "-e", "--end-of-options", req.Head+"^{commit}"); err != nil {
		return Result{}, fmt.Errorf("portpick: %s's head %s is not reachable from %s", req.Repo, req.Head, req.Source)
	}

	todo, dropped, err := g.pending(ctx, req, targetRef)
	if err != nil {
		return Result{}, err
	}
	res := Result{Dropped: dropped}
	if len(todo) == 0 {
		res.Outcome = NothingToPort
		return res, nil
	}

	// The branch must not exist yet: this creates it and touches nothing
	// that was already there.
	if out, err := g.run(ctx, "", "ls-remote", "--heads", "--end-of-options", remote.URL, "refs/heads/"+req.Branch); err != nil {
		return Result{}, fmt.Errorf("portpick: checking for %s on %s: %w", req.Branch, req.Repo, err)
	} else if strings.TrimSpace(out) != "" {
		return Result{}, fmt.Errorf("portpick: %s already exists on %s; not touching it", req.Branch, req.Repo)
	}

	work, err := os.MkdirTemp(p.Dir, "worktree-")
	if err != nil {
		return Result{}, fmt.Errorf("portpick: %w", err)
	}
	defer func() {
		// A fresh context: the attempt's own may be what ran out.
		cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
		defer ccancel()
		_, _ = g.run(cctx, "", "worktree", "remove", "--force", "--end-of-options", work)
		_ = os.RemoveAll(work)
		_, _ = g.run(cctx, "", "worktree", "prune")
	}()
	if _, err := g.run(ctx, "", "worktree", "add", "--detach", "--quiet", "--end-of-options", work, targetRef); err != nil {
		return Result{}, fmt.Errorf("portpick: checking out %s: %w", req.Target, err)
	}

	for _, sha := range todo {
		ported, conflict, empty, err := g.pick(ctx, work, sha)
		if err != nil {
			return Result{}, err
		}
		if conflict != nil {
			res.Outcome = Conflicted
			res.Conflict = conflict
			res.Picked = nil
			return res, nil
		}
		if empty {
			res.Dropped = append(res.Dropped, Dropped{Commit: sha, Why: "applies as no change on " + req.Target})
			continue
		}
		res.Picked = append(res.Picked, Pick{Source: sha, Ported: ported})
	}
	if len(res.Picked) == 0 {
		res.Outcome = NothingToPort
		return res, nil
	}
	head, err := g.run(ctx, work, "rev-parse", "HEAD")
	if err != nil {
		return Result{}, fmt.Errorf("portpick: %w", err)
	}
	res.Head = strings.TrimSpace(head)
	// No `+`, no --force: a create, which git refuses to turn into a
	// rewrite of anything already there.
	if _, err := g.run(ctx, work, "push", "--quiet", "--no-verify", "--end-of-options", remote.URL,
		res.Head+":refs/heads/"+req.Branch); err != nil {
		return Result{}, fmt.Errorf("portpick: pushing %s to %s: %w", req.Branch, req.Repo, err)
	}
	res.Outcome = Picked
	return res, nil
}

// pending is which of req.Commits still have to be ported onto target,
// oldest first, and the ones dropped: those `git cherry` finds on target
// under another commit (same patch-id), and those a commit on target
// names in a Backported-from or cherry-picked-from footer.
func (g *gitRunner) pending(ctx context.Context, req Request, targetRef string) ([]string, []Dropped, error) {
	want := make(map[string]bool, len(req.Commits))
	for _, c := range req.Commits {
		want[c] = true
	}
	// Oldest first, in the order the change was made, which is the order
	// they have to be applied in.
	out, err := g.run(ctx, "", "rev-list", "--reverse", "--topo-order", "--no-merges", "--end-of-options", targetRef+".."+req.Head)
	if err != nil {
		return nil, nil, fmt.Errorf("portpick: listing what %s lacks: %w", req.Target, err)
	}
	var ordered []string
	for _, sha := range strings.Fields(out) {
		if want[sha] {
			ordered = append(ordered, sha)
		}
	}
	if len(ordered) == 0 {
		return nil, nil, nil
	}
	cherry, err := g.run(ctx, "", "cherry", "--end-of-options", targetRef, req.Head)
	if err != nil {
		return nil, nil, fmt.Errorf("portpick: git cherry against %s: %w", req.Target, err)
	}
	equivalent := map[string]bool{}
	for _, line := range strings.Split(cherry, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "-" {
			equivalent[f[1]] = true
		}
	}
	var todo []string
	var dropped []Dropped
	for _, sha := range ordered {
		if equivalent[sha] {
			dropped = append(dropped, Dropped{Commit: sha, Why: "already on " + req.Target + " under another commit (same patch-id)"})
			continue
		}
		named, err := g.namedByFooter(ctx, targetRef, sha)
		if err != nil {
			return nil, nil, err
		}
		if named != "" {
			dropped = append(dropped, Dropped{Commit: sha, Why: fmt.Sprintf("%s's %s says it is already a port of it", req.Target, named)})
			continue
		}
		todo = append(todo, sha)
	}
	return todo, dropped, nil
}

// namedByFooter is the commit on target whose port footer names sha, or
// "" if none does.
func (g *gitRunner) namedByFooter(ctx context.Context, targetRef, sha string) (string, error) {
	out, err := g.run(ctx, "", "log", "--format=%H%x00%B%x01", "--fixed-strings", "--grep="+sha[:7], "--end-of-options", targetRef, "--")
	if err != nil {
		return "", fmt.Errorf("portpick: searching %s for ports of %s: %w", targetRef, sha, err)
	}
	for _, rec := range strings.Split(out, "\x01") {
		commit, body, ok := strings.Cut(strings.TrimLeft(rec, "\n"), "\x00")
		if !ok {
			continue
		}
		for _, m := range portedFooter.FindAllStringSubmatch(body, -1) {
			if named := m[1] + m[2]; strings.HasPrefix(sha, strings.ToLower(named)) {
				return commit, nil
			}
		}
	}
	return "", nil
}

// pick cherry-picks sha onto the worktree's HEAD with -x and adds the
// Backported-from footer. A conflict is aborted and reported; a pick
// that changes nothing is skipped and reported as empty.
func (g *gitRunner) pick(ctx context.Context, work, sha string) (string, *Conflict, bool, error) {
	if _, err := g.run(ctx, work, "cherry-pick", "-x", "--allow-empty-message", "--end-of-options", sha); err != nil {
		unmerged, _ := g.run(ctx, work, "diff", "--name-only", "--diff-filter=U")
		paths := strings.Fields(unmerged)
		if len(paths) == 0 {
			// No conflicted path: either the pick came out empty, which
			// git stops on rather than committing, or it failed outright.
			status, serr := g.run(ctx, work, "status", "--porcelain")
			if serr == nil && strings.TrimSpace(status) == "" {
				if _, err := g.run(ctx, work, "cherry-pick", "--skip"); err != nil {
					_, _ = g.run(ctx, work, "cherry-pick", "--abort")
					return "", nil, false, fmt.Errorf("portpick: skipping empty pick of %s: %w", sha, err)
				}
				return "", nil, true, nil
			}
			_, _ = g.run(ctx, work, "cherry-pick", "--abort")
			return "", nil, false, fmt.Errorf("portpick: cherry-picking %s: %w", sha, err)
		}
		subject, _ := g.run(ctx, "", "log", "-1", "--format=%s", "--end-of-options", sha, "--")
		_, _ = g.run(ctx, work, "cherry-pick", "--abort")
		return "", &Conflict{Commit: sha, Subject: strings.TrimSpace(subject), Paths: paths}, false, nil
	}
	msg, err := g.run(ctx, work, "log", "-1", "--format=%B", "HEAD")
	if err != nil {
		return "", nil, false, fmt.Errorf("portpick: %w", err)
	}
	msg = strings.TrimRight(msg, "\n") + "\nBackported-from: " + sha + "\n"
	if _, err := g.runInput(ctx, work, msg, "commit", "--amend", "--quiet", "--no-verify", "--allow-empty-message", "--cleanup=verbatim", "-F", "-"); err != nil {
		return "", nil, false, fmt.Errorf("portpick: adding the Backported-from footer to the pick of %s: %w", sha, err)
	}
	head, err := g.run(ctx, work, "rev-parse", "HEAD")
	if err != nil {
		return "", nil, false, fmt.Errorf("portpick: %w", err)
	}
	return strings.TrimSpace(head), nil, false, nil
}

func validate(req Request) error {
	if req.Repo.Owner == "" || req.Repo.Name == "" {
		return errors.New("portpick: no repository")
	}
	if !shaPattern.MatchString(req.Head) {
		return fmt.Errorf("portpick: head %q is not a full commit sha", req.Head)
	}
	if len(req.Commits) == 0 {
		return errors.New("portpick: no commits to port")
	}
	for _, c := range req.Commits {
		if !shaPattern.MatchString(c) {
			return fmt.Errorf("portpick: commit %q is not a full commit sha", c)
		}
	}
	for _, b := range []string{req.Source, req.Target, req.Branch} {
		if !validBranch(b) {
			return fmt.Errorf("portpick: %q is not a branch name", b)
		}
	}
	if req.Branch == req.Source || req.Branch == req.Target {
		return fmt.Errorf("portpick: refusing to push the port to %s, one of the pair itself", req.Branch)
	}
	return nil
}

// validBranch is a branch name git accepts that cannot be read as an
// option or a refspec: no leading dash, no colon, no plus, no glob, no
// whitespace -- which git check-ref-format refuses too, apart from the
// leading dash and plus, refused here.
var branchPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/-]*$`)

func validBranch(b string) bool {
	return branchPattern.MatchString(b) && !strings.Contains(b, "..") && !strings.HasSuffix(b, ".lock") &&
		!strings.HasSuffix(b, "/") && !strings.Contains(b, "//") && !strings.HasSuffix(b, ".")
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func safeName(s string) string {
	s = unsafeChars.ReplaceAllString(s, "_")
	if s == "" || s == "." || s == ".." {
		return "_"
	}
	return s
}

func (p *Porter) repoLock(key string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.repos == nil {
		p.repos = map[string]*sync.Mutex{}
	}
	l, ok := p.repos[key]
	if !ok {
		l = &sync.Mutex{}
		p.repos[key] = l
	}
	return l
}

// gitRunner runs git against one cache repository with the remote's
// credential, and nothing of the host's own git configuration: no
// system or global config, no hooks, no prompts.
type gitRunner struct {
	dir         string
	remote      Remote
	name, email string
}

func (g *gitRunner) ensureCache(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(g.dir, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(g.dir, 0o700); err != nil {
		return fmt.Errorf("portpick: %w", err)
	}
	if _, err := g.run(ctx, "", "init", "--bare", "--quiet", "--end-of-options", g.dir); err != nil {
		return fmt.Errorf("portpick: creating the cache at %s: %w", g.dir, err)
	}
	return nil
}

func (g *gitRunner) run(ctx context.Context, work string, args ...string) (string, error) {
	return g.runInput(ctx, work, "", args...)
}

func (g *gitRunner) runInput(ctx context.Context, work, input string, args ...string) (string, error) {
	full := []string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false"}
	if work != "" {
		full = append(full, "-C", work)
	} else {
		full = append(full, "--git-dir", g.dir)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = g.env()
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %v: %s", args[0], err, g.redact(strings.TrimSpace(stderr.String())))
	}
	return stdout.String(), nil
}

func (g *gitRunner) env() []string {
	name, email := g.name, g.email
	if name == "" {
		name = "grain"
	}
	if email == "" {
		email = "grain@users.noreply.github.com"
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		gitrefs.EnvEntry(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"LC_ALL=C",
		"GIT_COMMITTER_NAME=" + name,
		"GIT_COMMITTER_EMAIL=" + email,
	}
	if g.remote.Token != "" {
		auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + g.remote.Token))
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+auth)
	} else {
		env = append(env, "GIT_CONFIG_COUNT=0")
	}
	return env
}

// redact keeps the credential out of an error, in case git ever echoes
// the header it was given.
func (g *gitRunner) redact(s string) string {
	if g.remote.Token == "" {
		return s
	}
	s = strings.ReplaceAll(s, g.remote.Token, "[redacted]")
	auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + g.remote.Token))
	return strings.ReplaceAll(s, auth, "[redacted]")
}

// Summary is res in a sentence or two of markdown, for the comments and
// the log the orchestrator writes about it.
func (res Result) Summary(target string) string {
	var b strings.Builder
	switch res.Outcome {
	case Picked:
		fmt.Fprintf(&b, "cherry-picked %d commit(s) onto `%s`:", len(res.Picked), target)
		for _, p := range res.Picked {
			fmt.Fprintf(&b, " %s -> %s;", short(p.Source), short(p.Ported))
		}
	case NothingToPort:
		fmt.Fprintf(&b, "nothing left to port onto `%s`", target)
	case Conflicted:
		if c := res.Conflict; c != nil {
			fmt.Fprintf(&b, "%s (%q) conflicts on `%s` in %s", c.Commit, c.Subject, target, strings.Join(quoted(c.Paths), ", "))
		}
	}
	for _, d := range res.Dropped {
		fmt.Fprintf(&b, " Left out %s: %s.", short(d.Commit), d.Why)
	}
	return strings.TrimSpace(strings.TrimSuffix(b.String(), ";"))
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func quoted(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = "`" + p + "`"
	}
	return out
}
