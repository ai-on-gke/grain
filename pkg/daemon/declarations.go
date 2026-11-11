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

package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

// repoDeclarationSource reads a repository's own `.grain` declarations
// (grain/task-60) the way this daemon already reads its `.grain/workflows`:
// on this host, with the credential this deployment holds for that
// repository, into a temporary directory it deletes.
//
// It is a thin layer over ciWorkflowSource rather than a second cloner,
// and shares that type's whole argument for why the clone happens here
// -- the credential is here, the answer is wanted in the time a
// reconciler tick takes, and building a sandbox to read three
// directories would be a VM boot per poll. Nothing it reads reaches a
// sandbox: what comes out of it is rows in grain's own store.
type RepoDeclarationSource struct {
	// clone carries the credential ladder, the forge address and the
	// forbidden set. Its Workflows method is not used here; its
	// cloneURL and clone are.
	Clone CiWorkflowSource
}

// declarationTimeout bounds one repository's read. The same three
// minutes ciCloneTimeout allows, for the same clone -- a reconciler with
// several repositories to get through would rather fail one than spend a
// cycle on it.
const declarationTimeout = 3 * time.Minute

// Head implements orchestrator.DeclarationSource: the commit at the tip
// of repo's default branch, without cloning it.
//
// `git ls-remote <url> HEAD` is the whole of it. HEAD rather than a
// branch name because the default branch is the remote's own answer and
// not something this deployment should have to be told a second time --
// the clone below reads the same ref by asking for no branch at all, so
// the two cannot drift apart.
func (s RepoDeclarationSource) Head(ctx context.Context, repo model.RepoRef) (string, error) {
	if err := s.allowed(repo); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, declarationTimeout)
	defer cancel()

	dir, err := ciMkdirTemp(s.Clone.TempDir, "grain-declarations-head-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	out, err := s.Clone.clone(ctx, dir, repo, "ls-remote", "--", s.Clone.cloneURL(repo), "HEAD")
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(out))
	}
	for _, line := range strings.Split(out, "\n") {
		sha, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && strings.TrimSpace(ref) == "HEAD" && ciCommitName.MatchString(sha) {
			return sha, nil
		}
	}
	// An empty repository, which has a default branch with nothing
	// on it. Not an error: it declares nothing, and saying so lets
	// the reconciler read it once and then leave it alone.
	return "", nil
}

// Ref implements orchestrator.RefSource: the commit ref of repo peels to,
// read with `git ls-remote` and the same credential Head uses, or "" when
// the remote has no such ref. An annotated tag is advertised twice -- the
// tag object, and its target as "<ref>^{}" -- and the target is the
// answer, so moving a tag's message without moving its commit is not a
// move. Patterns given to ls-remote match from the end of a ref's name,
// so only the line naming ref exactly is read.
func (s RepoDeclarationSource) Ref(ctx context.Context, repo model.RepoRef, ref string) (string, error) {
	if err := s.allowed(repo); err != nil {
		return "", err
	}
	if !strings.HasPrefix(ref, "refs/") || strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("%q is not a full ref name", ref)
	}
	ctx, cancel := context.WithTimeout(ctx, declarationTimeout)
	defer cancel()

	dir, err := ciMkdirTemp(s.Clone.TempDir, "grain-ref-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	out, err := s.Clone.clone(ctx, dir, repo, "ls-remote", "--", s.Clone.cloneURL(repo), ref, ref+"^{}")
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(out))
	}
	return peeledRef(out, ref), nil
}

// peeledRef reads ref's commit out of `git ls-remote` output: its peeled
// "<ref>^{}" line when the remote sent one, and its own line otherwise.
func peeledRef(out, ref string) string {
	direct := ""
	for _, line := range strings.Split(out, "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || !ciCommitName.MatchString(sha) {
			continue
		}
		switch strings.TrimSpace(name) {
		case ref + "^{}":
			return sha
		case ref:
			direct = sha
		}
	}
	return direct
}

// Read implements orchestrator.DeclarationSource.
func (s RepoDeclarationSource) Read(ctx context.Context, repo model.RepoRef) (model.RepoDeclarations, string, error) {
	if err := s.allowed(repo); err != nil {
		return model.RepoDeclarations{}, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, declarationTimeout)
	defer cancel()

	dir, err := ciMkdirTemp(s.Clone.TempDir, "grain-declarations-")
	if err != nil {
		return model.RepoDeclarations{}, "", err
	}
	defer os.RemoveAll(dir)

	checkout := filepath.Join(dir, "repo")
	out, err := s.Clone.clone(ctx, dir, repo,
		"clone", "--quiet", "--depth", "1", "--", s.Clone.cloneURL(repo), checkout)
	if err != nil {
		return model.RepoDeclarations{}, "", fmt.Errorf("%w: %s", err, strings.TrimSpace(out))
	}
	commit, err := CiRunGit(ctx, checkout, nil, "rev-parse", "HEAD")
	if err != nil {
		return model.RepoDeclarations{}, "", err
	}
	// Forward-compatible: a file written for a newer grain than this one
	// is held back, its last good version kept, rather than refusing
	// everything else the repository declares (docs/declarations.md).
	declared, err := repodecl.LoadForwardCompatible(os.DirFS(checkout), repo)
	if err != nil {
		return model.RepoDeclarations{}, "", model.DeclarationRefusal{Err: err}
	}
	return declared, strings.TrimSpace(commit), nil
}

// allowed refuses a repository no sandbox may reach, for
// ciWorkflowSource.Workflows' own reason: grain has no business cloning
// a repository an operator has put out of bounds, and saying so is
// better than a poll that quietly does it every few minutes.
func (s RepoDeclarationSource) allowed(repo model.RepoRef) error {
	for _, forbidden := range s.Clone.Forbidden.Repos() {
		if strings.EqualFold(forbidden.Owner, repo.Owner) && strings.EqualFold(forbidden.Name, repo.Name) {
			return fmt.Errorf("%s: %s", repo, gitproxy.ForbiddenReason)
		}
	}
	return nil
}
