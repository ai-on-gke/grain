package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/staterepo/statecheck"
)

// StateCheckSandboxes is ci.Sandboxes with one job answered by the daemon
// itself: the configuration repository check (ConfigRepoCISource,
// StateCheckBuiltin). Every job a workflow file declares goes on to
// Sandboxes unchanged.
//
// The check is `grain state check`, and the daemon is a grain -- the very
// build whose ability to load the tree is the question -- so it needs no
// machine and no image. What it needs is the tree, and the repository it
// is asked about is the one this daemon keeps a copy of on its own disk
// (CiWorkflowSource.cloneURL): grain/config.
// Acquire makes a throwaway checkout of the commit from that copy, and the
// one step runs statecheck.Run against it in-process.
//
// What the checkout can do to the daemon is what a state import can: it is
// read as rows, through the same code StateManager applies a merge with.
// Symlinks are checked out as plain files, so a tree cannot point a table
// at a file of the host's and have its contents quoted back in an error.
type StateCheckSandboxes struct {
	ci.Sandboxes
	// ConfigRepoDir is CiWorkflowSource's: the local repository the
	// deployment's configuration lives in.
	ConfigRepoDir string
	// TempDir is where checkouts are made; empty means os.TempDir.
	TempDir string
}

func (s StateCheckSandboxes) Acquire(ctx context.Context, req ci.SandboxRequest) (ci.Sandbox, error) {
	switch req.Builtin {
	case "":
		if s.Sandboxes == nil {
			return nil, fmt.Errorf("this deployment has no CI sandboxes to run %s's job in", req.Repo)
		}
		return s.Sandboxes.Acquire(ctx, req)
	case StateCheckBuiltin:
	default:
		return nil, fmt.Errorf("this build of grain runs no builtin CI job called %q", req.Builtin)
	}
	r, err := model.ParseRepo(req.Repo)
	if err != nil {
		return nil, err
	}
	source := s.localRepo(r)
	if source == "" {
		return nil, fmt.Errorf("%s is not a configuration repository this daemon keeps, so it cannot check it", r)
	}
	target := req.Commit
	switch {
	case target != "" && !ciCommitName.MatchString(target):
		return nil, fmt.Errorf("%q is not a commit", target)
	case target == "" && req.Ref != "" && !ciGitSafe.MatchString(req.Ref):
		return nil, fmt.Errorf("%q is not a usable branch name", req.Ref)
	case target == "":
		target = req.Ref
	}
	if target == "" {
		target = "HEAD"
	}

	dir, err := os.MkdirTemp(s.TempDir, "grain-ci-state-check-")
	if err != nil {
		return nil, err
	}
	clone := filepath.Join(dir, "repo")
	if out, err := CiRunGit(ctx, dir, nil, "clone", "--quiet", "--no-checkout", "--", source, clone); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("checking out %s: %w: %s", r, err, strings.TrimSpace(out))
	}
	checkout := []string{"-c", "core.symlinks=false", "checkout", "--quiet", "--detach"}
	if _, err := CiRunGit(ctx, clone, nil, append(checkout, target)...); err != nil {
		// A commit no branch of the copy names any more: fetched by name,
		// the same second arm ciCheckoutScript has.
		if out, err := CiRunGit(ctx, clone, nil, "fetch", "--quiet", "--", "origin", target); err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("checking out %s at %s: %w: %s", r, target, err, strings.TrimSpace(out))
		}
		if out, err := CiRunGit(ctx, clone, nil, append(checkout, "FETCH_HEAD")...); err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("checking out %s at %s: %w: %s", r, target, err, strings.TrimSpace(out))
		}
	}
	name := req.Name
	if name == "" {
		name = "grain-state-check"
	}
	return &stateCheckSandbox{name: name, dir: dir, tree: clone}, nil
}

func (s StateCheckSandboxes) Readopt(ctx context.Context, req ci.SandboxRequest) (ci.Sandbox, error) {
	if req.Builtin != "" {
		return nil, fmt.Errorf("in-process builtin CI job %q cannot be readopted across a restart", req.Builtin)
	}
	if readopter, ok := s.Sandboxes.(interface {
		Readopt(context.Context, ci.SandboxRequest) (ci.Sandbox, error)
	}); ok {
		return readopter.Readopt(ctx, req)
	}
	return nil, fmt.Errorf("CI sandbox backend %T does not support readoption", s.Sandboxes)
}

func localConfigRepoEnabled(configRepoDir string) bool {
	if configRepoDir == "" {
		return false
	}
	settings, err := staterepo.LoadSettings(filepath.Dir(configRepoDir))
	return err == nil && strings.TrimSpace(settings.Remote) == ""
}

// localRepo is the copy of r this daemon keeps, or "" for a repository it
// keeps none of -- cloneURL's local arm, and nothing else, since
// anything else would mean reaching a remote from the daemon to check it.
func (s StateCheckSandboxes) localRepo(r model.RepoRef) string {
	if r.Same(model.ConfigRepo()) && localConfigRepoEnabled(s.ConfigRepoDir) {
		return s.ConfigRepoDir
	}
	return ""
}

// stateCheckSandbox is one checkout, for as long as its job lasts.
type stateCheckSandbox struct {
	name string
	dir  string
	tree string
}

func (s *stateCheckSandbox) Name() string { return s.name }

// Exec runs the state check against the checkout. cmd.Script is the
// step's `run:` as ConfigRepoCISource wrote it, which says what is done
// here but is not a shell script for anything to run. A script that is not a state check is refused rather than run, since
// there is no shell here to run it in.
func (s *stateCheckSandbox) Exec(ctx context.Context, cmd ci.Command) (result ci.CommandResult, err error) {
	if !strings.HasPrefix(strings.TrimSpace(cmd.Script), "grain state check") {
		return ci.CommandResult{ExitCode: 2, Output: "this job is run by the grain daemon itself, which runs " +
			"`grain state check` and nothing else; it has no shell for:\n" + cmd.Script + "\n"}, nil
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "$ %s\n", strings.TrimSpace(cmd.Script))
	fmt.Fprintf(&out, "run in-process by this deployment's own grain (%s), with no image and no sandbox\n", buildIdentity())
	// The daemon's own process: a tree that finds a panic in the importer
	// fails its check rather than taking every other run down with it.
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintf(&out, "error: grain state check panicked: %v\n", p)
			result, err = ci.CommandResult{Output: out.String(), ExitCode: 1}, nil
		}
	}()
	if err := statecheck.Run(ctx, &out, s.tree, statecheck.Options{PreviousAPIScopeSets: s.previousAPIScopeSets(ctx)}); err != nil {
		fmt.Fprintf(&out, "error: %v\n", err)
		return ci.CommandResult{Output: out.String(), ExitCode: 1}, nil
	}
	return ci.CommandResult{Output: out.String()}, nil
}

// previousAPIScopeSets is the API scope sets file as the branch the
// change is against holds it -- the clone's origin/HEAD, which is the
// branch the deployment's own copy has checked out -- for the check's
// diff of effective scopes. A base with no such file declared no sets;
// a clone with no origin/HEAD gets no diff.
func (s *stateCheckSandbox) previousAPIScopeSets(ctx context.Context) []byte {
	if _, err := CiRunGit(ctx, s.tree, nil, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/HEAD"); err != nil {
		return nil
	}
	out, err := CiRunGit(ctx, s.tree, nil, "show", "refs/remotes/origin/HEAD:"+statecheck.APIScopeSetsFile)
	if err != nil {
		return []byte{}
	}
	return []byte(out)
}

func (s *stateCheckSandbox) Release(context.Context) error {
	return os.RemoveAll(s.dir)
}
