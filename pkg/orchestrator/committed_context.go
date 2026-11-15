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

package orchestrator

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ContextReposDir is where prepareCommittedContext clones read-only external
// context repositories inside a sandbox (e.g. /home/debian/repos/<owner>/<repo>),
// detached at each reference's pinned commit SHA.
const ContextReposDir = "repos"

const (
	contextSHAMarker      = "grain-context-sha:"
	contextManifestMarker = "grain-context-manifest-b64:"
)

// ResolvedContextCheckout describes one committed-context reference verified or
// cloned inside a sandbox and surfaced in the agent's prompt.
type ResolvedContextCheckout struct {
	Ref       model.ContextRef
	Repo      model.RepoRef
	Placement string // "target" or "read"
	CloneDir  string // e.g. "work" or "repos/acme/specs"
	RelPath   string // e.g. "work/design/plc/042" or "repos/acme/specs/plc/042"
	LocalPath string // e.g. "/home/debian/work/design/plc/042" or "./work/design/plc/042"
	PinnedSHA string // resolved commit SHA
	Manifest  *model.ContextManifest
}

func resolveSandboxPath(sandboxRoot, relPath string) string {
	cleanRel := strings.TrimPrefix(path.Clean(relPath), "./")
	if strings.TrimSpace(sandboxRoot) != "" {
		return path.Join(sandboxRoot, cleanRel)
	}
	return "./" + cleanRel
}

func manifestCandidates(baseDir, refPath string) []string {
	full := path.Join(baseDir, refPath)
	base := strings.ToLower(path.Base(refPath))
	if base == "context.yml" || base == "context.yaml" {
		return []string{full}
	}
	return []string{
		path.Join(full, "context.yml"),
		path.Join(full, "context.yaml"),
	}
}

func manifestReadSnippet(baseDir, refPath string) string {
	cands := manifestCandidates(baseDir, refPath)
	quoted := make([]string, 0, len(cands))
	for _, c := range cands {
		quoted = append(quoted, shellQuote(c))
	}
	return fmt.Sprintf(`for grain_mf in %s; do
  if [ -f "$grain_mf" ] && [ ! -L "$grain_mf" ]; then
    echo "%s$(head -c 65536 "$grain_mf" | base64 | tr -d '\n\r')"
    break
  fi
done`, strings.Join(quoted, " "), contextManifestMarker)
}

func parseContextCommandOutput(text string) (string, *model.ContextManifest) {
	var sha string
	var manifest *model.ContextManifest
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if val, ok := strings.CutPrefix(trimmed, contextSHAMarker); ok {
			sha = strings.ToLower(strings.TrimSpace(val))
		}
		if b64, ok := strings.CutPrefix(trimmed, contextManifestMarker); ok && b64 != "" {
			if raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64)); err == nil {
				if parsed, err := model.ParseContextManifest(raw); err == nil && parsed != nil {
					manifest = parsed
				}
			}
		}
	}
	return sha, manifest
}

func prepareCommittedContext(ctx context.Context, tools []mcp.Tool, remoteBase string, task model.Task, out *checkout, reused bool) error {
	refs := task.ContextRefs()
	if len(refs) == 0 {
		return nil
	}
	run, ok := runCommandTool(tools)
	if !ok {
		return nil
	}
	proxyBase := proxyBaseWithoutCredentials(remoteBase)
	clonedRevs := map[string]string{}

	resolved := make([]ResolvedContextCheckout, 0, len(refs))
	for _, ref := range refs {
		cleanPath, err := model.CleanContextPath(ref.Path)
		if err != nil {
			return fmt.Errorf("orchestrator: invalid committed-context path %q: %w", ref.Path, err)
		}
		ref.Path = cleanPath
		if ref.Rev != "" {
			cleanRev, err := model.ValidateContextRev(ref.Rev)
			if err != nil {
				return fmt.Errorf("orchestrator: invalid committed-context revision %q: %w", ref.Rev, err)
			}
			ref.Rev = cleanRev
		}

		if !ref.TargetsOtherRepo(task.Target) {
			if task.Target == nil || out.Dir == "" {
				continue
			}
			qWork := shellQuote(out.Dir)
			var script string
			if ref.Rev != "" {
				qRev := shellQuote(ref.Rev)
				qRevCommit := shellQuote(ref.Rev + "^{commit}")
				script = fmt.Sprintf(`set -e
if ! git -C %[1]s cat-file -e %[2]s 2>/dev/null; then
  git -C %[1]s fetch --quiet origin %[3]s 2>/dev/null || true
fi
if ! grain_pinned=$(git -C %[1]s rev-parse --verify --quiet %[2]s); then
  echo "grain-context-missing-rev" >&2
  exit 41
fi
if ! git -C %[1]s merge-base --is-ancestor "$grain_pinned" HEAD; then
  echo "grain-context-not-ancestor:$grain_pinned" >&2
  exit 42
fi
echo "%[4]s$grain_pinned"
%[5]s`, qWork, qRevCommit, qRev, contextSHAMarker, manifestReadSnippet(out.Dir, ref.Path))
			} else {
				script = fmt.Sprintf(`set -e
grain_pinned=$(git -C %[1]s rev-parse --verify --quiet HEAD 2>/dev/null || true)
echo "%[2]s$grain_pinned"
%[3]s`, qWork, contextSHAMarker, manifestReadSnippet(out.Dir, ref.Path))
			}
			res := run(ctx, map[string]any{"command": script})
			if res.IsError {
				if strings.Contains(res.Text, "grain-context-missing-rev") || strings.Contains(res.Text, "grain-context-not-ancestor:") {
					return fmt.Errorf("orchestrator: target checkout %s does not contain committed-context revision %s for %s",
						*task.Target, ref.Rev, ref.Path)
				}
				return fmt.Errorf("orchestrator: verifying committed context %s in %s: %s",
					ref.Path, *task.Target, strings.TrimSpace(res.Text))
			}
			sha, manifest := parseContextCommandOutput(res.Text)
			if sha == "" {
				sha = ref.Rev
			}
			if manifest == nil {
				manifest = ref.Manifest
			}
			relPath := path.Join(out.Dir, ref.Path)
			resolved = append(resolved, ResolvedContextCheckout{
				Ref:       ref,
				Repo:      *task.Target,
				Placement: "target",
				CloneDir:  out.Dir,
				RelPath:   relPath,
				LocalPath: resolveSandboxPath(out.Root, relPath),
				PinnedSHA: sha,
				Manifest:  manifest,
			})
			continue
		}

		// External context repository: must be covered by task.Reads and checked out detached at ref.Rev.
		if !slices.ContainsFunc(task.Reads, ref.Repo.Same) {
			return fmt.Errorf("orchestrator: committed context %s:%s requires -read %s", ref.Repo, ref.Path, ref.Repo)
		}
		if proxyBase == "" {
			return fmt.Errorf("orchestrator: cannot clone committed-context repository %s without a git proxy", ref.Repo)
		}
		for _, v := range []string{ref.Repo.Owner, ref.Repo.Name} {
			if !gitSafe.MatchString(v) {
				return fmt.Errorf("orchestrator: %q is not a usable git name", v)
			}
		}

		cloneDir := path.Join(ContextReposDir, ref.Repo.String())
		revKey := strings.ToLower(strings.TrimSpace(ref.Rev))
		if prevRev, exists := clonedRevs[cloneDir]; exists && prevRev != revKey {
			suffix := revKey
			if len(suffix) > 12 {
				suffix = suffix[:12]
			}
			if suffix == "" {
				suffix = "head"
			}
			cloneDir = cloneDir + "@" + suffix
		}
		_, sameClonePrepared := clonedRevs[cloneDir]
		clonedRevs[cloneDir] = revKey

		qCloneDir := shellQuote(cloneDir)
		qParentDir := shellQuote(path.Dir(cloneDir))
		qCloneURL := shellQuote(CloneURL(proxyBase, ref.Repo))

		var cloneStep string
		if sameClonePrepared {
			cloneStep = ""
		} else if reused {
			cloneStep = fmt.Sprintf(`mkdir -p %[1]s
if [ ! -d %[2]s/.git ]; then
  rm -rf %[2]s
  %[3]s
fi
git -C %[2]s remote set-url --push origin grain-read-only
`, qParentDir, qCloneDir, RetryingCloneScript(qCloneURL, qCloneDir))
		} else {
			cloneStep = fmt.Sprintf(`mkdir -p %[1]s
rm -rf %[2]s
%[3]s
git -C %[2]s remote set-url --push origin grain-read-only
`, qParentDir, qCloneDir, RetryingCloneScript(qCloneURL, qCloneDir))
		}

		var checkoutStep string
		if sameClonePrepared {
			checkoutStep = fmt.Sprintf(`grain_pinned=$(git -C %[1]s rev-parse --verify HEAD)
echo "%[2]s$grain_pinned"`, qCloneDir, contextSHAMarker)
		} else if ref.Rev != "" {
			qRev := shellQuote(ref.Rev)
			qRevCommit := shellQuote(ref.Rev + "^{commit}")
			checkoutStep = fmt.Sprintf(`if ! git -C %[1]s cat-file -e %[2]s 2>/dev/null; then
  git -C %[1]s fetch --quiet origin %[3]s
fi
grain_pinned=$(git -C %[1]s rev-parse --verify %[2]s)
git -C %[1]s checkout --quiet --detach --force "$grain_pinned"
echo "%[4]s$grain_pinned"`, qCloneDir, qRevCommit, qRev, contextSHAMarker)
		} else {
			checkoutStep = fmt.Sprintf(`git -C %[1]s fetch --quiet origin
grain_pinned=$(git -C %[1]s rev-parse --verify --quiet 'refs/remotes/origin/HEAD^{commit}' 2>/dev/null || git -C %[1]s rev-parse --verify HEAD)
git -C %[1]s checkout --quiet --detach --force "$grain_pinned"
echo "%[2]s$grain_pinned"`, qCloneDir, contextSHAMarker)
		}

		script := fmt.Sprintf("set -e\n%s%s\n%s", cloneStep, checkoutStep, manifestReadSnippet(cloneDir, ref.Path))
		res := run(ctx, map[string]any{"command": script})
		if res.IsError {
			err := fmt.Errorf("orchestrator: checking out committed context %s:%s@%s: %s",
				ref.Repo, ref.Path, ref.Rev, strings.TrimSpace(res.Text))
			if said, ok := gitproxy.LoginLapsed(res.Text); ok {
				return &hostLoginLapsedError{Repo: ref.Repo, Said: said, err: err}
			}
			if gitproxy.IsAccessRefusal(res.Text) {
				return &accessRefusedError{Repo: ref.Repo, Output: strings.TrimSpace(res.Text), err: err}
			}
			return err
		}
		sha, manifest := parseContextCommandOutput(res.Text)
		if sha == "" {
			sha = ref.Rev
		}
		if manifest == nil {
			manifest = ref.Manifest
		}
		relPath := path.Join(cloneDir, ref.Path)
		resolved = append(resolved, ResolvedContextCheckout{
			Ref:       ref,
			Repo:      ref.Repo,
			Placement: "read",
			CloneDir:  cloneDir,
			RelPath:   relPath,
			LocalPath: resolveSandboxPath(out.Root, relPath),
			PinnedSHA: sha,
			Manifest:  manifest,
		})
	}

	out.Context = resolved
	return nil
}

func persistCommittedContextManifests(ctx context.Context, store *model.Store, task *model.Task, resolved []ResolvedContextCheckout) {
	if store == nil || task == nil || len(resolved) == 0 {
		return
	}
	needsWrite := false
	for _, item := range resolved {
		if item.Manifest != nil && item.Ref.Manifest == nil {
			needsWrite = true
			break
		}
		if model.IsFullCommitSHA(item.PinnedSHA) && item.Ref.Rev != "" && item.Ref.Rev != item.PinnedSHA {
			needsWrite = true
			break
		}
	}
	if !needsWrite {
		return
	}
	_ = store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		for _, item := range resolved {
			ref := item.Ref
			if item.Ref.Rev != "" && model.IsFullCommitSHA(item.PinnedSHA) {
				ref.Rev = item.PinnedSHA
			}
			model.SetTaskContextManifest(t, ref, item.Manifest)
		}
		return nil
	})
	if updated, err := store.GetTask(ctx, task.ID); err == nil && updated != nil {
		*task = *updated
	}
}

func synthesizeCommittedContext(task model.Task, checkoutDir, sandboxRoot string) []ResolvedContextCheckout {
	refs := task.ContextRefs()
	if len(refs) == 0 {
		return nil
	}
	dir := strings.TrimPrefix(strings.TrimSpace(checkoutDir), "/")
	if dir == "" {
		dir = CheckoutDir
	}
	out := make([]ResolvedContextCheckout, 0, len(refs))
	for _, ref := range refs {
		if !ref.TargetsOtherRepo(task.Target) {
			var repo model.RepoRef
			if task.Target != nil {
				repo = *task.Target
			}
			relPath := path.Join(dir, ref.Path)
			out = append(out, ResolvedContextCheckout{
				Ref:       ref,
				Repo:      repo,
				Placement: "target",
				CloneDir:  dir,
				RelPath:   relPath,
				LocalPath: resolveSandboxPath(sandboxRoot, relPath),
				PinnedSHA: ref.Rev,
				Manifest:  ref.Manifest,
			})
			continue
		}
		cloneDir := path.Join(ContextReposDir, ref.Repo.String())
		relPath := path.Join(cloneDir, ref.Path)
		out = append(out, ResolvedContextCheckout{
			Ref:       ref,
			Repo:      ref.Repo,
			Placement: "read",
			CloneDir:  cloneDir,
			RelPath:   relPath,
			LocalPath: resolveSandboxPath(sandboxRoot, relPath),
			PinnedSHA: ref.Rev,
			Manifest:  ref.Manifest,
		})
	}
	return out
}

func committedContextSection(task model.Task, checkoutDir, sandboxRoot string, resolved []ResolvedContextCheckout) string {
	items := resolved
	if len(items) == 0 {
		items = synthesizeCommittedContext(task, checkoutDir, sandboxRoot)
	}
	if len(items) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\n### Committed context\n\n")
	b.WriteString("Read the committed specification context below and hold your work to it:")
	for _, item := range items {
		localPath := item.LocalPath
		if localPath == "" || (strings.HasPrefix(localPath, "./") && strings.TrimSpace(sandboxRoot) != "") {
			localPath = resolveSandboxPath(sandboxRoot, item.RelPath)
		}
		pin := "HEAD"
		if item.PinnedSHA != "" {
			pin = item.PinnedSHA
		}
		if item.Placement == "read" {
			cloneLocal := resolveSandboxPath(sandboxRoot, item.CloneDir)
			fmt.Fprintf(&b, "\n- `%s` (`%s` path `%s`, cloned read-only at `%s` detached at `%s` — do not switch branches or push)",
				localPath, item.Repo, item.Ref.Path, cloneLocal, pin)
		} else if !item.Repo.IsZero() {
			fmt.Fprintf(&b, "\n- `%s` (`%s` path `%s`, pinned at `%s`)",
				localPath, item.Repo, item.Ref.Path, pin)
		} else {
			fmt.Fprintf(&b, "\n- `%s` (path `%s`, pinned at `%s`)",
				localPath, item.Ref.Path, pin)
		}
		if m := item.Manifest; m != nil {
			switch {
			case m.Title != "" && m.Description != "":
				fmt.Fprintf(&b, "\n  Manifest: **%s** — %s", m.Title, m.Description)
			case m.Title != "":
				fmt.Fprintf(&b, "\n  Manifest: **%s**", m.Title)
			case m.Description != "":
				fmt.Fprintf(&b, "\n  Manifest: %s", m.Description)
			}
			for _, f := range m.Files {
				filePath := path.Join(localPath, f.Path)
				label := fmt.Sprintf("`%s`", filePath)
				if f.Kind != "" {
					label += fmt.Sprintf(" (%s)", f.Kind)
				}
				if f.Title != "" {
					label += " — " + f.Title
				}
				if f.SourceURL != "" {
					label += fmt.Sprintf(" [source: %s]", f.SourceURL)
				}
				fmt.Fprintf(&b, "\n  - %s", label)
			}
		}
	}
	return b.String()
}
