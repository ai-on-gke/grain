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

// Package secretsrepo gives grain's encrypted secrets file a home that
// is not this host's disk: a git repository of its own, holding the
// ciphertext and nothing else.
//
// Until now there were two answers and neither was this one. The file
// used to sit in the state repository, which stopped being safe the
// moment a task could be dispatched at that repository -- the git proxy
// authorizes per repository and streams a packfile it does not parse, so
// a sandbox that may clone the state repository reads every object in
// it, a file deleted three commits ago included (staterepo.HasSecrets,
// and cmd/grain's forbiddenRepos). So it moved out to
// <data-dir>/secrets, where no push carries it anywhere -- correct, and
// it left a deployment whose ciphertext exists on exactly one disk. A
// host rebuilt onto a fresh data directory comes back with its key,
// which every deploy path already knows how to hand over, and with no
// file for that key to open.
//
// A repository of its own is what closes that gap without reopening the
// first one. It is a third place, named separately (Settings, in
// <data-dir>/secrets-repo.json), and it is a repository no task is ever
// dispatched at: cmd/grain adds it to the set the git proxy refuses to
// every sandbox, unconditionally and without asking what it holds, since
// holding grain's ciphertext is the whole of what it is for.
//
// # What is in it, and under what name
//
// One file per deployment, and the name says whose it is: FileName
// renders "grain-<owner>-<repo>.secrets.enc" from the state repository
// this deployment runs on. That is deliberate on both halves. A secrets
// repository is the sort of repository an operator already has --
// somewhere private where other things also keep encrypted material --
// so a file called "secrets.enc" at its root would be a name nobody
// outside grain can place, and one that collides with the next tool to
// want the same obvious name. And an operator running more than one
// grain -- a staging deployment and a production one -- points both at
// the same secrets repository and gets two files rather than two hosts
// overwriting one, because what distinguishes their secrets is exactly
// what distinguishes their state.
//
// # Which copy wins
//
// The file under <data-dir>/secrets is the live one and always wins;
// this repository is a backup of it, restored from only when the live
// one is not there at all. That rule is what makes Sync safe to run on a
// timer without ever consulting a human: grain never overwrites the
// secrets it is running on with something a push from elsewhere left in
// a repository, and the one case where the repository is authoritative
// -- a data directory with no secrets file in it -- is the restore, and
// is unambiguous.
//
// It also makes the history here disposable, which is why Sync resets
// onto the remote rather than merging with it. Every commit is one
// derived file whose source is still on this host, so a remote that has
// moved is taken whole and this deployment's own file written back on
// top of it: two grains sharing a repository never fight over it (their
// files have different names), and a push that failed before somebody
// else's landed is retried on the next tick rather than becoming a
// divergence an operator has to go and resolve. pkg/staterepo refuses to
// merge for the opposite reason -- there, the history *is* the state.
//
// Nothing here decrypts anything, and the private key is not in it: a
// clone of a secrets repository is every secret grain has in a form
// nothing but this deployment's own key file can open, which is the
// property that makes it worth pushing at all.
package secretsrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitrefs"
)

// DefaultBranch is the branch the ciphertext lives on when Config does
// not say otherwise. Named rather than left to git, for staterepo's own
// reason: `git init` takes the branch name from the invoking user's
// configuration, and a repository whose branch depends on who created it
// is one a push cannot find later.
const DefaultBranch = "main"

// Config says where the two ends of a sync are.
type Config struct {
	// Dir is the working tree, under the data directory beside the
	// secrets it backs up. It need not exist yet.
	Dir string
	// Remote is the repository the ciphertext is pushed to. Empty is a
	// deployment that has not configured one, which is every deployment
	// until somebody does: Open returns ErrNoRemote rather than a Repo
	// with nowhere to push, because a local git repository holding a copy
	// of a file already on the same disk is not a backup of anything.
	Remote string
	// Branch is the branch it lives on; DefaultBranch if empty.
	Branch string
	// File is what the ciphertext is called inside the repository --
	// FileName's rendering, unless an operator named one themselves.
	File string
	// Source is the encrypted file on this host: secrets.Config.File,
	// under <data-dir>/secrets. It is the copy that wins, and the one a
	// restore writes.
	Source string
	// AuthorName and AuthorEmail identify grain's own commits, given to
	// git explicitly because a deployed host's git config usually names
	// nobody at all -- and an unset user.email is a commit that fails.
	AuthorName  string
	AuthorEmail string
	// Token, if set, is called for the credential to push an https remote
	// with, per operation rather than once: a GitHub App installation
	// token lasts an hour, and a daemon that cached one at startup would
	// stop being able to push a day later.
	Token func(ctx context.Context) (string, error)
}

// ErrNoRemote is Open's answer for a Config naming no remote. It is a
// sentinel rather than a failure to report, because "this deployment has
// not configured a secrets repository" is the ordinary state of a fresh
// install and not something a daemon should log every thirty seconds.
var ErrNoRemote = errors.New("secretsrepo: no remote configured")

// Repo is an opened secrets repository. Build one with Open.
type Repo struct {
	cfg    Config
	branch string
}

// Open prepares the repository, cloning it if this host has not seen it
// before and `git init`ing a working tree for it otherwise.
//
// A remote that exists but is empty -- which is what "create a private
// repository and point grain at it" produces -- is not a special case:
// the clone succeeds with no commits, and the first Sync makes the one
// that matters.
func Open(ctx context.Context, cfg Config) (*Repo, error) {
	if cfg.Dir == "" {
		return nil, errors.New("secretsrepo: no directory configured")
	}
	if cfg.Remote == "" {
		return nil, ErrNoRemote
	}
	if cfg.File == "" {
		return nil, errors.New("secretsrepo: no file name configured")
	}
	// Settings.File is an operator's free text, and the one thing it must
	// not be able to say is "somewhere else on this host": a name that
	// climbed out of the working tree would have grain writing ciphertext
	// over a file nobody asked it to touch, and committing whatever was
	// there instead.
	if clean := filepath.Clean(cfg.File); filepath.IsAbs(cfg.File) || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("secretsrepo: %q names a file outside the repository", cfg.File)
	}
	if cfg.Source == "" {
		return nil, errors.New("secretsrepo: no source file configured")
	}
	cfg.Branch = strings.TrimSpace(cfg.Branch)
	if cfg.Branch == "" {
		cfg.Branch = DefaultBranch
	}
	if err := ValidateBranch(cfg.Branch); err != nil {
		return nil, err
	}
	if cfg.AuthorName == "" {
		cfg.AuthorName = "grain"
	}
	if cfg.AuthorEmail == "" {
		cfg.AuthorEmail = "grain@localhost"
	}
	r := &Repo{cfg: cfg, branch: cfg.Branch}

	exists, err := isRepo(cfg.Dir)
	if err != nil {
		return nil, err
	}
	// A repository in a ref format this git cannot read (reftable, made
	// by a newer host git) fails every command below with git's bare
	// "unknown repository extension"; say what it is and how to convert
	// it instead.
	if exists {
		if err := gitrefs.Check(ctx, cfg.Dir); err != nil {
			return nil, fmt.Errorf("secretsrepo: %w", err)
		}
	}
	// A working tree cloned from a different repository is thrown away
	// rather than repointed. Everything in it is a cache -- one derived
	// file whose source is still under <data-dir>/secrets, and a history
	// this package treats as disposable anyway -- and what it may also
	// hold is other deployments' files, fetched from the repository this
	// one used to share. Repointing would push those into whatever
	// repository the operator has just named, which is the one outcome
	// worth a re-clone to avoid.
	if exists && cfg.Remote != "" {
		if current, err := r.git(ctx, "config", "--get", "remote.origin.url"); err == nil &&
			strings.TrimSpace(current) != "" && strings.TrimSpace(current) != cfg.Remote {
			if err := os.RemoveAll(cfg.Dir); err != nil {
				return nil, fmt.Errorf("secretsrepo: clearing %s to clone %s into it: %w", cfg.Dir, cfg.Remote, err)
			}
			exists = false
		}
	}
	if !exists {
		if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
			return nil, fmt.Errorf("secretsrepo: preparing %s: %w", cfg.Dir, err)
		}
		empty, err := isEmptyDir(cfg.Dir)
		if err != nil {
			return nil, err
		}
		if empty {
			if err := r.clone(ctx); err != nil {
				return nil, err
			}
		} else if _, err := r.git(ctx, "init", "--initial-branch="+r.branch, "--", cfg.Dir); err != nil {
			// A directory that already holds something cannot be cloned
			// into (`git clone` refuses a non-empty destination), and
			// there is nothing to lose by initing instead: Sync's fetch
			// resets a repository with no commits onto the remote's
			// branch, which brings down exactly what a clone would have.
			return nil, err
		}
	}
	if err := r.configure(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// Remote returns the repository the ciphertext is pushed to.
func (r *Repo) Remote() string { return r.cfg.Remote }

// File returns what the ciphertext is called inside the repository.
func (r *Repo) File() string { return r.cfg.File }

// path is where that file sits in this working tree.
func (r *Repo) path() string { return filepath.Join(r.cfg.Dir, r.cfg.File) }

// Result says what one Sync did, for a caller that reports it: to a
// terminal in `grain state status`, or to the journal at startup, where
// "the secrets on this host were restored from the repository" is a
// sentence an operator should not have to infer.
type Result struct {
	// Restored is a data directory that had no secrets file and now has
	// the one the repository was holding. The rebuilt host.
	Restored bool
	// Committed is this host's ciphertext written into the repository
	// because it differed from what was there, or was not there at all.
	Committed bool
	// Pushed is that commit, or one an earlier failed push left behind,
	// reaching the remote.
	Pushed bool
}

// Sync reconciles the two copies, in the one direction each case allows,
// and reports what it did.
//
//   - The remote is taken first, whole: fetch and reset onto it. Every
//     commit here is a derived file, so there is never anything local
//     worth merging, and taking the remote's history rather than merging
//     with it is what stops two deployments sharing one repository from
//     ever diverging.
//   - A data directory with no secrets file, and a repository holding
//     one, is the restore: the file is written back beside the key,
//     which is the case this whole package exists for.
//   - Otherwise this host's file is the answer and the repository is
//     brought up to it: written, committed and pushed when the bytes
//     differ from what the reset left in the tree.
//
// A fetch that could not reach the remote is not fatal: the local
// history is used as it stands, the commit is still made, and the push
// either works or reports why -- and the next tick tries again.
func (r *Repo) Sync(ctx context.Context) (Result, error) {
	var out Result
	fetched, fetchErr := r.fetch(ctx)

	source, err := readFile(r.cfg.Source)
	if err != nil {
		return out, errors.Join(fetchErr, err)
	}
	stored, err := readFile(r.path())
	if err != nil {
		return out, errors.Join(fetchErr, err)
	}

	if source == nil {
		if stored == nil {
			// Nothing on either side: a deployment that has not written a
			// secret yet. Nothing to back up and nothing to restore.
			return out, fetchErr
		}
		if err := r.restore(stored); err != nil {
			return out, errors.Join(fetchErr, err)
		}
		out.Restored = true
		return out, fetchErr
	}

	if !bytes.Equal(source, stored) {
		// Settings.File may name a directory to sit the file in, for a
		// secrets repository with a layout of its own; on the first sync
		// into a fresh clone that directory is not there yet.
		if err := os.MkdirAll(filepath.Dir(r.path()), 0o700); err != nil {
			return out, errors.Join(fetchErr, fmt.Errorf("secretsrepo: preparing %s: %w", filepath.Dir(r.path()), err))
		}
		if err := os.WriteFile(r.path(), source, 0o600); err != nil {
			return out, errors.Join(fetchErr, fmt.Errorf("secretsrepo: writing %s: %w", r.path(), err))
		}
		committed, err := r.commit(ctx)
		if err != nil {
			return out, errors.Join(fetchErr, err)
		}
		out.Committed = committed
	}

	// Pushed on the strength of what the remote is missing rather than of
	// what this cycle committed: a commit whose push failed while nothing
	// else changed would otherwise sit here forever, since every later
	// cycle writes identical bytes and commits nothing.
	ahead, known := r.aheadOfRemote(ctx, fetched)
	if known && !ahead {
		return out, fetchErr
	}
	if err := r.push(ctx); err != nil {
		return out, errors.Join(fetchErr, err)
	}
	out.Pushed = true
	return out, fetchErr
}

// restore writes the repository's copy back to where grain reads its
// secrets from, with the permissions the file would have had if this
// host had written it itself.
func (r *Repo) restore(stored []byte) error {
	dir := filepath.Dir(r.cfg.Source)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("secretsrepo: preparing %s: %w", dir, err)
	}
	if err := os.WriteFile(r.cfg.Source, stored, 0o600); err != nil {
		return fmt.Errorf("secretsrepo: restoring %s: %w", r.cfg.Source, err)
	}
	return nil
}

// fetch brings the remote's branch down and resets the working tree onto
// it, reporting whether it got that far.
//
// Reset rather than merge, and the argument is in the package comment:
// the one file this tree holds is derived from a file this host still
// has, so the remote's history can be taken whole and this deployment's
// own copy written back on top of it. Which also means a working tree
// that was ahead loses nothing -- the difference is noticed again
// immediately, and committed again on the same cycle.
func (r *Repo) fetch(ctx context.Context) (bool, error) {
	if _, err := r.gitAuthed(ctx, "fetch", "--quiet", "--end-of-options", "origin", r.branch); err != nil {
		// A remote with no such branch yet is nothing to fetch rather than
		// a failure to fetch: an empty repository somebody has just
		// created, whose first commit is the one this sync is about to
		// make.
		if r.remoteBranchMissing(ctx) {
			return false, r.startBranch(ctx)
		}
		return false, fmt.Errorf("secretsrepo: fetching %s from %s: %w", r.branch, r.cfg.Remote, err)
	}
	if _, err := r.git(ctx, "checkout", "-q", "-B", r.branch, "FETCH_HEAD"); err != nil {
		return false, fmt.Errorf("secretsrepo: resetting %s onto origin/%s: %w", r.cfg.Dir, r.branch, err)
	}
	return true, nil
}

// startBranch puts the working tree on the configured branch when the
// remote does not have it, which is the only case where creating it is
// this deployment's job.
//
// Open already covers the empty repository -- a clone with no commits is
// checked out onto the configured branch there. What it cannot cover is
// the other shape of "the branch does not exist yet": a secrets
// repository that already holds commits on some *other* branch, which is
// what an operator has the moment they create the repository with a
// README, or point a second deployment at a branch of its own. The clone
// lands on that repository's default branch, and a commit made there is
// one `git push origin <branch>` cannot find -- git answers "src refspec
// <branch> does not match any", and the ciphertext never leaves the
// host.
//
// Branched from whatever the clone left checked out rather than started
// as an orphan: a branch off the repository's own default is what a
// person creating this branch by hand would get, and an orphan history
// in a repository an operator also keeps other things in is a surprise
// grain has no reason to spring.
func (r *Repo) startBranch(ctx context.Context) error {
	if head, err := r.currentBranch(ctx); err == nil && head == r.branch {
		return nil
	}
	if _, err := r.git(ctx, "checkout", "-q", "-B", r.branch); err != nil {
		return fmt.Errorf("secretsrepo: starting branch %s in %s: %w", r.branch, r.cfg.Dir, err)
	}
	return nil
}

// commit stages the one file and commits it, reporting whether there was
// anything to commit.
func (r *Repo) commit(ctx context.Context) (bool, error) {
	if _, err := r.git(ctx, "add", "--", r.cfg.File); err != nil {
		return false, err
	}
	status, err := r.git(ctx, "status", "--porcelain", "--", r.cfg.File)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(status) == "" {
		return false, nil
	}
	// The message names the file rather than describing the change,
	// because there is nothing about the change that can be described:
	// the contents are ciphertext, and a reader of this history is
	// entitled to know which deployment's secrets moved and nothing more.
	if _, err := r.git(ctx, "commit", "--quiet", "-m", "Update "+r.cfg.File); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Repo) push(ctx context.Context) error {
	if empty, _ := r.isEmpty(ctx); empty {
		return nil
	}
	if _, err := r.gitAuthed(ctx, "push", "--quiet", "--end-of-options", "origin", r.branch); err != nil {
		return fmt.Errorf("secretsrepo: pushing %s to %s: %w", r.branch, r.cfg.Remote, err)
	}
	return nil
}

// aheadOfRemote reports whether this working tree holds a commit the
// remote's branch does not, and whether that could be told at all.
//
// fetched says whether this cycle actually reached the remote: if it did
// not, nothing is known and the caller pushes anyway, which is what
// tries the network again and reports its own failure. If it did,
// FETCH_HEAD is where the remote was a moment ago and the question is a
// local one.
func (r *Repo) aheadOfRemote(ctx context.Context, fetched bool) (ahead, known bool) {
	if !fetched {
		return false, false
	}
	if empty, _ := r.isEmpty(ctx); empty {
		return false, true
	}
	if _, err := r.git(ctx, "merge-base", "--is-ancestor", "HEAD", "FETCH_HEAD"); err != nil {
		return true, true
	}
	return false, true
}

func (r *Repo) clone(ctx context.Context) error {
	// --branch is left off: an empty remote has no branch to ask for, and
	// asking for one that does not exist is a clone that fails.
	if _, err := r.gitAuthed(ctx, "clone", "--quiet", "--", r.cfg.Remote, r.cfg.Dir); err != nil {
		return fmt.Errorf("secretsrepo: cloning %s: %w", r.cfg.Remote, err)
	}
	if head, err := r.currentBranch(ctx); err == nil && head != r.branch {
		if empty, _ := r.isEmpty(ctx); empty {
			_, _ = r.git(ctx, "checkout", "-q", "-b", r.branch)
		}
	}
	return nil
}

// configure writes grain's identity and the remote into the repository's
// own config, so every later command inherits them.
func (r *Repo) configure(ctx context.Context) error {
	for _, kv := range [][2]string{
		{"user.name", r.cfg.AuthorName},
		{"user.email", r.cfg.AuthorEmail},
		// Signing is the operator's business, and a host with
		// commit.gpgsign set globally would break every automated commit
		// with a passphrase prompt.
		{"commit.gpgsign", "false"},
	} {
		if _, err := r.git(ctx, "config", "--", kv[0], kv[1]); err != nil {
			return err
		}
	}
	return r.setRemote(ctx)
}

// setRemote makes sure origin names Config.Remote. A working tree
// cloned from a different repository never reaches here -- Open throws
// it away and clones afresh -- so this is what points a tree that has no
// origin yet (one Open had to init rather than clone) at the remote.
func (r *Repo) setRemote(ctx context.Context) error {
	current, err := r.git(ctx, "remote")
	if err != nil {
		return err
	}
	has := false
	for _, name := range strings.Fields(current) {
		if name == "origin" {
			has = true
		}
	}
	if has {
		_, err = r.git(ctx, "remote", "set-url", "--", "origin", r.cfg.Remote)
	} else {
		_, err = r.git(ctx, "remote", "add", "--", "origin", r.cfg.Remote)
	}
	return err
}

func (r *Repo) isEmpty(ctx context.Context) (bool, error) {
	if _, err := r.git(ctx, "rev-parse", "--verify", "HEAD"); err != nil {
		return true, nil
	}
	return false, nil
}

func (r *Repo) currentBranch(ctx context.Context) (string, error) {
	out, err := r.git(ctx, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ValidateBranch checks that branch is a valid git branch name. An empty or
// whitespace-only string is allowed (meaning DefaultBranch).
func ValidateBranch(branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" || branch == DefaultBranch {
		return nil
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("secretsrepo: invalid branch name %q", branch)
	}
	cmd := exec.Command("git", "check-ref-format", "--branch", branch)
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != branch {
		return fmt.Errorf("secretsrepo: invalid branch name %q", branch)
	}
	return nil
}

func (r *Repo) remoteBranchMissing(ctx context.Context) bool {
	out, err := r.gitAuthed(ctx, "ls-remote", "--heads", "--end-of-options", "origin", "refs/heads/"+r.branch)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == ""
}

// readFile reads a file that is allowed not to be there, which both
// copies are: a deployment that has never set a secret has no source
// file, and a repository nothing has pushed to has no stored one.
func readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("secretsrepo: reading %s: %w", path, err)
	}
	return data, nil
}

func isRepo(dir string) (bool, error) {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err == nil {
		return info.IsDir() || info.Mode().IsRegular(), nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("secretsrepo: inspecting %s: %w", dir, err)
}

func isEmptyDir(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("secretsrepo: inspecting %s: %w", dir, err)
	}
	return len(entries) == 0, nil
}

func (r *Repo) git(ctx context.Context, args ...string) (string, error) {
	return r.run(ctx, nil, args...)
}

// gitAuthed runs a git command that talks to the remote, with whatever
// credential Config.Token yields.
//
// The token goes into a temporary askpass script rather than into the
// URL or onto the command line: anything in argv is readable by any
// other process on the host through `ps`, and a token baked into a
// remote URL persists in .git/config.
func (r *Repo) gitAuthed(ctx context.Context, args ...string) (string, error) {
	if r.cfg.Token == nil || !strings.HasPrefix(r.cfg.Remote, "http") {
		return r.run(ctx, nil, args...)
	}
	token, err := r.cfg.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("secretsrepo: no credential for %s: %w", r.cfg.Remote, err)
	}
	if token == "" {
		return r.run(ctx, nil, args...)
	}
	dir, err := os.MkdirTemp("", "grain-secretsrepo-askpass-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "askpass.sh")
	// git asks twice: once for the username, once for the password. The
	// prompt it passes as $1 is what tells them apart.
	body := "#!/bin/sh\ncase \"$1\" in\n*[Uu]sername*) printf '%s' 'x-access-token' ;;\n*) printf '%s' " +
		shellQuote(token) + " ;;\nesac\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		return "", err
	}
	env := []string{
		"GIT_ASKPASS=" + script,
		// Without this, a git that cannot use the askpass falls through to
		// a terminal prompt and hangs the daemon forever instead of
		// failing.
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=",
	}
	return r.run(ctx, env, args...)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (r *Repo) run(ctx context.Context, extraEnv []string, args ...string) (string, error) {
	// A git operation against a remote can hang on a network that
	// half-answers; every call gets a ceiling so a sync cannot wedge the
	// daemon's timer.
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	// clone and init name their own destination and must not run inside a
	// directory that is not a repository yet.
	if args[0] != "clone" && args[0] != "init" {
		cmd.Dir = r.cfg.Dir
	}
	cmd.Env = append(os.Environ(), extraEnv...)
	if args[0] == "clone" || args[0] == "init" {
		// The files ref backend whatever git this is and whatever its
		// config defaults to: a reftable repository made by a newer git
		// is one an older one cannot open at all (task 410). Older gits
		// ignore the variable and only ever make files repositories.
		cmd.Env = append(cmd.Env, "GIT_DEFAULT_REF_FORMAT=files")
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s",
			strings.Join(redact(args), " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// redact keeps a remote URL's userinfo out of an error message, for the
// case where an operator pasted a URL with a token in it.
func redact(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if at := strings.Index(a, "@"); at > 0 && strings.Contains(a[:at], "://") {
			scheme := a[:strings.Index(a, "://")+3]
			out[i] = scheme + "***@" + a[at+1:]
			continue
		}
		out[i] = a
	}
	return out
}

const gitTimeout = 2 * time.Minute
