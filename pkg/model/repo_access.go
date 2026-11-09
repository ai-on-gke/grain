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

package model

// The repo capability server's grants -- CI read and write access to other
// repositories, and the task-level any-ref push grant -- and the event gate
// that decides which CI runs are handed them. They live apart from task.go
// so that the trust-boundary list (pkg/trustboundary, capabilities-secrets)
// can hold this file without holding every change to tasks.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// GitPushAnyRefCapability is the retired bare name of GitPushAnyRefGrant:
// the grant that lets a task's run push any ref of the repositories it
// may write, not only its own task branch (Store.GitPushAnyRef). It is
// still read as that grant wherever it was not rewritten (a repository's
// own .grain declaration, an older grain/config); schema migration 85
// rewrote the stored rows.
const GitPushAnyRefCapability = "git-push-any-ref"

// RepoCapabilityServerID is the canonical capability_id of the in-process
// repository-access server (design/design/unified-grants.design.md §1, §4),
// answered by grain itself like GrainCapabilityServerID.
const RepoCapabilityServerID = "https://grain.dev/capabilities/repo"

// The repo server's #write method, and the one value of it the git proxy
// honours so far: every branch and tag, force-pushes and deletions
// included (formerly git-push-any-ref).
const (
	RepoWriteMethod    = "write"
	RepoWriteRef       = RepoCapabilityServerID + "#" + RepoWriteMethod
	GitPushAnyRefGrant = RepoWriteRef + `={"force":true,"refs":["refs/heads/*","refs/tags/*"]}`
)

// gitPushAnyRefRefs are the ref patterns GitPushAnyRefGrant names.
var gitPushAnyRefRefs = []string{"refs/heads/*", "refs/tags/*"}

// IsGitPushAnyRefGrant reports whether grant lifts the git proxy's
// one-branch push rule: GitPushAnyRefGrant, recorded or as a grant
// string with its arguments in any order, or the retired bare
// git-push-any-ref. A repo#write grant naming fewer refs, or without
// force, is not it: the proxy does not honour narrower task-level refs
// (design/design/unified-grants.design.md Q3), so it opens nothing rather than
// everything.
func IsGitPushAnyRefGrant(grant string) bool {
	s := strings.TrimSpace(grant)
	if s == GitPushAnyRefCapability {
		return true
	}
	if !IsCapabilityRef(s) {
		return false
	}
	spec, err := CapabilityGrantSpec(s)
	if err != nil || spec.Capability != RepoWriteRef {
		return false
	}
	if force, _ := spec.With["force"].(bool); !force {
		return false
	}
	refs, _ := spec.With["refs"].([]any)
	if len(refs) != len(gitPushAnyRefRefs) {
		return false
	}
	for _, want := range gitPushAnyRefRefs {
		if !slices.Contains(refs, any(want)) {
			return false
		}
	}
	return true
}

// The repo server's #read method: CI read access to the one repository
// its `repo` argument names (formerly repo-read:<repo>).
const (
	RepoReadMethod = "read"
	RepoReadRef    = RepoCapabilityServerID + "#" + RepoReadMethod
)

// The repo server's #credential method: per-task git credential override
// selecting secrets/github/<name> (formerly github-credential:<name>).
const (
	RepoCredentialMethod     = "credential"
	RepoCredentialRef        = RepoCapabilityServerID + "#" + RepoCredentialMethod
	GitCredentialNamePattern = "^[A-Za-z0-9_-]+$"
)

// ValidGitCredentialName reports whether name is a valid named git credential
// stem under secrets/github (matching gitproxy.ValidCredentialName and
// githubtoken.ValidName): non-empty [A-Za-z0-9_-]+ and not "anonymous".
func ValidGitCredentialName(name string) bool {
	if name == "" || name == "anonymous" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// RepoCredentialGrant is the recorded grant selecting the named git credential:
// https://grain.dev/capabilities/repo#credential={"credential":"<name>"}.
func RepoCredentialGrant(name string) string {
	data, err := json.Marshal(map[string]any{"credential": name})
	if err != nil {
		panic(fmt.Sprintf("model: encoding %s's credential: %v", RepoCredentialRef, err))
	}
	return RepoCredentialRef + "=" + string(data)
}

// ParseRepoCredentialGrant returns the credential name a repo#credential grant
// selects, and false for any other capability string (including malformed
// repo#credential arguments).
func ParseRepoCredentialGrant(grant string) (string, bool) {
	s := strings.TrimSpace(grant)
	if !IsCapabilityRef(s) || CapabilityGrantRef(s) != RepoCredentialRef {
		return "", false
	}
	spec, err := CapabilityGrantSpec(s)
	if err != nil || spec.Capability != RepoCredentialRef || len(spec.With) != 1 {
		return "", false
	}
	name, ok := spec.With["credential"].(string)
	if !ok || !ValidGitCredentialName(name) {
		return "", false
	}
	return name, true
}

// RepoReadCapabilityPrefix and RepoWriteCapabilityPrefix are the retired
// bare spellings of CI repository access, repo-read:<owner>/<repo> and
// repo-write:<owner>/<repo>. They are read as RepoReadGrant and
// RepoWriteGrant everywhere -- indefinitely, since a repository's own
// .grain/workflows job `capabilities:` lives in somebody else's
// repository and nothing here rewrites it -- and schema migration 88
// rewrote the stored rows.
const (
	RepoReadCapabilityPrefix  = "repo-read:"
	RepoWriteCapabilityPrefix = "repo-write:"
)

// RepoReadGrant is the recorded grant of CI read access to repo:
// https://grain.dev/capabilities/repo#read={"repo":"<repo>"}.
func RepoReadGrant(repo RepoRef) string { return repoAccessGrant(RepoReadRef, repo) }

// RepoWriteGrant is the recorded grant of CI write (push) access to repo:
// https://grain.dev/capabilities/repo#write={"repo":"<repo>"}. It is not
// GitPushAnyRefGrant, the other value #write takes, which widens a
// task's own pushes rather than naming a repository.
func RepoWriteGrant(repo RepoRef) string { return repoAccessGrant(RepoWriteRef, repo) }

func repoAccessGrant(ref string, repo RepoRef) string {
	data, err := json.Marshal(map[string]any{"repo": repo.String()})
	if err != nil {
		panic(fmt.Sprintf("model: encoding %s's repo: %v", ref, err))
	}
	return ref + "=" + string(data)
}

// RepoReadCapability is the capability id standing for "grant CI read
// access to repo": RepoReadGrant.
func RepoReadCapability(repo RepoRef) string { return RepoReadGrant(repo) }

// RepoReadTarget returns the repository a grant of CI read access names
// -- RepoReadGrant, or the retired repo-read:<repo> -- and false for any
// other capability id.
func RepoReadTarget(capability string) (RepoRef, bool) {
	return repoAccessTarget(capability, RepoReadCapabilityPrefix, RepoReadRef)
}

// RepoWriteCapability is the capability id standing for "grant CI write
// access to repo": RepoWriteGrant.
func RepoWriteCapability(repo RepoRef) string { return RepoWriteGrant(repo) }

// RepoWriteTarget returns the repository a grant of CI write access
// names -- RepoWriteGrant, or the retired repo-write:<repo> -- and false
// for any other capability id, GitPushAnyRefGrant among them.
func RepoWriteTarget(capability string) (RepoRef, bool) {
	return repoAccessTarget(capability, RepoWriteCapabilityPrefix, RepoWriteRef)
}

// repoAccessTarget reads capability as legacyPrefix<repo>, or as ref with
// exactly one argument, repo.
func repoAccessTarget(capability, legacyPrefix, ref string) (RepoRef, bool) {
	s := strings.TrimSpace(capability)
	name, ok := strings.CutPrefix(s, legacyPrefix)
	if !ok {
		if !IsCapabilityRef(s) || CapabilityGrantRef(s) != ref {
			return RepoRef{}, false
		}
		spec, err := CapabilityGrantSpec(s)
		if err != nil || len(spec.With) != 1 {
			return RepoRef{}, false
		}
		name, ok = spec.With["repo"].(string)
		if !ok {
			return RepoRef{}, false
		}
	}
	if name == "" {
		return RepoRef{}, false
	}
	repo, err := ParseRepo(name)
	if err != nil {
		return RepoRef{}, false
	}
	return repo, true
}

// IsRepoAccessGrant reports whether capability grants a CI job read or
// write access to another repository (RepoReadTarget, RepoWriteTarget).
//
// These are what the retired reporead and repowrite providers decided,
// carried by the reference itself now that no provider stands behind it:
// such a grant is CI-only (CapabilitySpec.CIOnly) -- a task holding one is
// refused it (ResolveGrants, CIOnlyRefusal) -- and RepoAccessAdmitsCIEvent
// is its event gate.
func IsRepoAccessGrant(capability string) bool {
	if _, ok := RepoReadTarget(capability); ok {
		return true
	}
	_, ok := RepoWriteTarget(capability)
	return ok
}

// RepoAccessAdmitsCIEvent reports whether a CI job run on event is
// handed the repository access capability grants. Read access goes to
// every event. Write access is the old repo-write provider's gate: a
// post-merge release or an operator's own workflow_dispatch, and the
// schedule run grain itself fired from a workflow's cron (trustedSchedule,
// ci.Request.TrustedSchedule) -- that cron was written into a workflow on
// the default branch whose job declared the grant, and runs as the
// schedule's team, whose GitHub credential the push goes out with
// (grain/task-1654), the same assurance a workflow_dispatch somebody
// typed carries. A caller's own `-event schedule` run is refused, as is a
// push or pull-request run. False for any other capability.
func RepoAccessAdmitsCIEvent(capability, event string, trustedSchedule bool) bool {
	if _, ok := RepoReadTarget(capability); ok {
		return true
	}
	if _, ok := RepoWriteTarget(capability); !ok {
		return false
	}
	switch event {
	case "release", "workflow_dispatch":
		return true
	case "schedule":
		return trustedSchedule
	}
	return false
}

// RepoAccessSpellings is every spelling of capability's repository
// access -- the recorded reference and the retired bare name -- with
// capability itself first, so a rule written in either matches a grant
// held in the other. Just capability for anything else.
func RepoAccessSpellings(capability string) []string {
	out := []string{capability}
	add := func(s string) {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if repo, ok := RepoReadTarget(capability); ok {
		add(RepoReadGrant(repo))
		add(RepoReadCapabilityPrefix + repo.String())
	} else if repo, ok := RepoWriteTarget(capability); ok {
		add(RepoWriteGrant(repo))
		add(RepoWriteCapabilityPrefix + repo.String())
	}
	return out
}
