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

package ui

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// RepoRevResolver resolves a git ref (or "" for the repository's default
// branch) in repo to a full commit SHA when committed context is filed or
// edited. ok is false when this resolver does not serve repo.
type RepoRevResolver interface {
	ResolveRepoRev(ctx context.Context, repo model.RepoRef, rev string) (sha string, ok bool, err error)
}

// ContextRefInput is the structured JSON input form of a committed-context
// reference (`{repo, path, rev}`) on CreateTaskRequest and UpdateTaskRequest.
type ContextRefInput struct {
	Repo string `json:"repo,omitempty"`
	Path string `json:"path"`
	Rev  string `json:"rev,omitempty"`
}

// ContextRefView is one committed-context reference as GET /api/tasks/{id} and
// the task detail page render it: one `{repo, path, rev}` shape across both
// target-repo folders and separate -read context repositories.
type ContextRefView struct {
	Repo      string                 `json:"repo,omitempty"`
	Path      string                 `json:"path"`
	Rev       string                 `json:"rev,omitempty"`
	Placement string                 `json:"placement"` // "target" or "read"
	Manifest  *model.ContextManifest `json:"manifest,omitempty"`
}

func contextRefViews(t model.Task) []ContextRefView {
	refs := t.ContextRefs()
	if len(refs) == 0 {
		return nil
	}
	out := make([]ContextRefView, 0, len(refs))
	for _, ref := range refs {
		repoStr := ""
		if repo, ok := ref.ResolvedRepo(t.Target); ok {
			repoStr = repo.String()
		}
		placement := "target"
		if ref.TargetsOtherRepo(t.Target) {
			placement = "read"
		}
		out = append(out, ContextRefView{
			Repo:      repoStr,
			Path:      ref.Path,
			Rev:       ref.Rev,
			Placement: placement,
			Manifest:  ref.Manifest,
		})
	}
	return out
}

func parseContextRefInput(in ContextRefInput) (model.ContextRef, error) {
	var ref model.ContextRef
	if repoText := strings.TrimSpace(in.Repo); repoText != "" {
		repo, err := model.ParseRepo(repoText)
		if err != nil {
			return model.ContextRef{}, validationErrorf("context repo %q: %v", in.Repo, err)
		}
		ref.Repo = repo
	}
	cleanPath, err := model.CleanContextPath(in.Path)
	if err != nil {
		return model.ContextRef{}, validationErrorf("context path %q: %v", in.Path, err)
	}
	ref.Path = cleanPath
	if revText := strings.TrimSpace(in.Rev); revText != "" {
		cleanRev, err := model.ValidateContextRev(revText)
		if err != nil {
			return model.ContextRef{}, validationErrorf("context rev %q: %v", in.Rev, err)
		}
		ref.Rev = cleanRev
	}
	return ref, nil
}

// derivedFromLinks validates CreateTaskRequest.DerivedFrom into LinkDerivedFrom links.
func derivedFromLinks(derivedFrom []string) ([]model.Link, error) {
	var links []model.Link
	for _, raw := range derivedFrom {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		ref, ok := NormalizePullRequestRef(raw)
		if !ok {
			return nil, validationErrorf("derived-from %q is not a pull request: write owner/name#number or its URL", raw)
		}
		links = append(links, model.Link{Kind: model.LinkDerivedFrom, Target: ref})
	}
	return links, nil
}

// resolveContextLinksAndReads parses, validates, checks/auto-grants -read
// access, and resolves git refs to commit SHAs for a task's committed-context
// references (from Designs and/or Context).
func (c *Client) resolveContextLinksAndReads(
	ctx context.Context,
	designs []string,
	structured []ContextRefInput,
	target *model.RepoRef,
	reads []model.RepoRef,
	cfg *model.Config,
	existingLinks []model.Link,
) ([]model.Link, []model.RepoRef, error) {
	type rawEntry struct {
		label string
		ref   model.ContextRef
	}
	var entries []rawEntry
	for _, raw := range designs {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		ref, err := model.ParseContextRef(raw)
		if err != nil {
			return nil, nil, validationErrorf("design %q is not a valid committed-context reference ([owner/name:]path[@rev]): %v", raw, err)
		}
		entries = append(entries, rawEntry{label: raw, ref: ref})
	}
	for _, item := range structured {
		if strings.TrimSpace(item.Repo) == "" && strings.TrimSpace(item.Path) == "" && strings.TrimSpace(item.Rev) == "" {
			continue
		}
		ref, err := parseContextRefInput(item)
		if err != nil {
			return nil, nil, err
		}
		entries = append(entries, rawEntry{label: ref.StringRelativeTo(target), ref: ref})
	}
	if len(entries) == 0 {
		return nil, reads, nil
	}

	outReads := slices.Clone(reads)
	var designLinks []model.Link
	var resolvedRefs []model.ContextRef

	for _, entry := range entries {
		ref := entry.ref
		if !ref.Repo.IsZero() {
			canon, err := c.parseRepo(ref.Repo.String())
			if err != nil {
				return nil, nil, err
			}
			ref.Repo = canon
			if target != nil && ref.Repo.Same(*target) {
				ref.Repo = *target
			}
		}

		if ref.TargetsOtherRepo(target) {
			if model.IsConfigRepo(ref.Repo) {
				return nil, nil, validationErrorf("committed context %q cannot read the internal configuration repository %s", entry.label, ref.Repo)
			}
			if !slices.ContainsFunc(outReads, ref.Repo.Same) {
				if cfg != nil && model.ContextReadAutoGranted(cfg.AutoGrantContextReads, ref.Repo) {
					outReads = append(outReads, ref.Repo)
				} else {
					targetDesc := "no target repository"
					if target != nil {
						targetDesc = target.String()
					}
					return nil, nil, validationErrorf(
						"committed context %q is in %s (task target: %s): grant read access with -read %s, or add %s to autoGrantContextReads",
						entry.label, ref.Repo, targetDesc, ref.Repo, ref.Repo,
					)
				}
			}
		}

		resolvedRepo, hasRepo := ref.ResolvedRepo(target)
		if ref.Rev != "" && !model.IsFullCommitSHA(ref.Rev) {
			if !hasRepo {
				return nil, nil, validationErrorf("design %q names revision %q without a repository: qualify it as owner/name:%s@%s or set a target repository",
					entry.label, ref.Rev, ref.Path, ref.Rev)
			}
			resolved := false
			if c.Config.RepoRevs != nil {
				sha, ok, err := c.Config.RepoRevs.ResolveRepoRev(ctx, resolvedRepo, ref.Rev)
				if err != nil {
					return nil, nil, validationErrorf("resolving %s@%s: %v", resolvedRepo, ref.Rev, err)
				}
				if ok {
					if sha == "" {
						return nil, nil, validationErrorf("revision %q was not found in %s", ref.Rev, resolvedRepo)
					}
					ref.Rev = strings.ToLower(sha)
					resolved = true
				}
			}
			if !resolved && !model.IsCommitSHA(ref.Rev) {
				return nil, nil, validationErrorf("cannot resolve symbolic ref %q in %s: no repository revision resolver is configured; specify a commit SHA", ref.Rev, resolvedRepo)
			}
		} else if ref.Rev == "" && hasRepo && c.Config.RepoRevs != nil {
			sha, ok, err := c.Config.RepoRevs.ResolveRepoRev(ctx, resolvedRepo, "")
			if err != nil && ref.TargetsOtherRepo(target) {
				return nil, nil, validationErrorf("resolving default revision of %s: %v", resolvedRepo, err)
			}
			if err == nil && ok {
				if sha == "" && ref.TargetsOtherRepo(target) {
					return nil, nil, validationErrorf("could not resolve default revision of context repository %s", resolvedRepo)
				}
				if sha != "" {
					ref.Rev = strings.ToLower(sha)
				}
			}
		}

		formatted := ref.StringRelativeTo(target)
		if !slices.ContainsFunc(designLinks, func(l model.Link) bool { return l.Target == formatted }) {
			designLinks = append(designLinks, model.Link{Kind: model.LinkDesign, Target: formatted})
			resolvedRefs = append(resolvedRefs, ref)
		}
	}

	// Preserve existing discovered manifests that still match one of the refs.
	if len(existingLinks) > 0 {
		oldTask := model.Task{Target: target, Links: existingLinks}
		oldRefs := oldTask.ContextRefs()
		probe := model.Task{Target: target, Links: designLinks}
		for _, newRef := range resolvedRefs {
			for _, oldRef := range oldRefs {
				if oldRef.Manifest == nil || oldRef.Path != newRef.Path {
					continue
				}
				oldRepo, oldOK := oldRef.ResolvedRepo(target)
				newRepo, newOK := newRef.ResolvedRepo(target)
				if oldOK == newOK && (!oldOK || oldRepo.Same(newRepo)) && oldRef.Rev == newRef.Rev {
					model.SetTaskContextManifest(&probe, newRef, oldRef.Manifest)
					break
				}
			}
		}
		designLinks = probe.Links
	}

	return designLinks, outReads, nil
}

// validateExistingContextReads verifies that every non-target committed-context
// reference on task still has its repository in task.Reads (auto-granting if
// cfg.AutoGrantContextReads covers it).
func validateExistingContextReads(task *model.Task, cfg *model.Config) error {
	for _, ref := range task.ContextRefs() {
		if !ref.TargetsOtherRepo(task.Target) {
			continue
		}
		if slices.ContainsFunc(task.Reads, ref.Repo.Same) {
			continue
		}
		if cfg != nil && model.ContextReadAutoGranted(cfg.AutoGrantContextReads, ref.Repo) {
			task.Reads = append(task.Reads, ref.Repo)
			continue
		}
		return validationErrorf("committed context %q requires read access to %s: keep %s in reads or update the task's context references",
			ref.StringRelativeTo(task.Target), ref.Repo, ref.Repo)
	}
	return nil
}

func contextReposInBody(key string, raw json.RawMessage) []string {
	var out []string
	switch key {
	case "designs":
		var items []string
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
		for _, item := range items {
			if ref, err := model.ParseContextRef(item); err == nil && !ref.Repo.IsZero() {
				out = append(out, ref.Repo.String())
			}
		}
	case "context":
		var items []ContextRefInput
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
		for _, item := range items {
			if r := strings.TrimSpace(item.Repo); r != "" {
				out = append(out, r)
			}
		}
	}
	return out
}

// NormalizePullRequestRef reads a pull request named as owner/name#number,
// host/owner/name#number or https://host/owner/name/pull/number, and
// answers it as owner/name#number (host/owner/name#number off the default
// github.com), the form model.LinkDerivedFrom stores.
func NormalizePullRequestRef(raw string) (string, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", false
	}
	if strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://") {
		u, err := url.Parse(text)
		if err != nil {
			return "", false
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 4 || (parts[2] != "pull" && parts[2] != "pulls") {
			return "", false
		}
		if _, err := strconv.Atoi(parts[3]); err != nil {
			return "", false
		}
		repo := parts[0] + "/" + parts[1]
		if host := strings.ToLower(u.Host); host != "" && host != "github.com" && host != "www.github.com" {
			repo = host + "/" + repo
		}
		return repo + "#" + parts[3], true
	}
	repo, num, ok := strings.Cut(text, "#")
	if !ok || strings.Count(repo, "/") < 1 || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") {
		return "", false
	}
	if _, err := strconv.Atoi(num); err != nil {
		return "", false
	}
	repo = strings.TrimPrefix(strings.TrimPrefix(repo, "github.com/"), "www.github.com/")
	return repo + "#" + num, true
}
