package gitproxy

// Which refs a CI job holding repo-write may update.
//
// A release workflow (.grain/workflows/release-train.yml,
// docs/design/release-from-trunk.md §6) pushes three kinds of ref: a
// release or release-candidate tag, refs/tags/v*, created once; the
// `latest` tag, moved on every promotion; and refs/heads/candidate, the
// staging pointer, force-moved onto each cut. All three are allowed, the
// two moving refs force-pushes included. What no CI job may do is
// rewrite trunk: an update of a protected branch (CIProtectedBranches)
// is let through only where the push shows it is a fast-forward, and a
// deletion of one is refused outright. Every other ref is the job's
// declaration to push, as it was before these rules.
//
// The question is answered here rather than by a workflow's own
// allowlist because a workflow file is a file any branch can rewrite:
// the rules have to hold whatever the job's YAML says.

import (
	"fmt"
	"sort"
	"strings"
)

// CIProtectedBranches are the branches a CI job may only fast-forward.
var CIProtectedBranches = map[string]bool{
	"refs/heads/main": true,
}

// ciRefVia is what the audit log names as having allowed a CI job's
// update (RefGrant.Via).
const ciRefVia = "repo-write"

// CIRefUpdates returns why a CI job with write access to owner/repo may
// not make updates, or "" when it may make all of them. check is the
// push in hand, for the fast-forward a protected branch's update must
// show; nil shows none, so every such update is refused.
func CIRefUpdates(check *pushCheck, owner, repo string, updates []RefUpdate) string {
	var problems []string
	var grants []RefGrant
	for _, u := range updates {
		kind, problem := ciRefUpdate(check, u)
		if problem != "" {
			problems = append(problems, problem)
			continue
		}
		grants = append(grants, RefGrant{Ref: u.Ref, Kind: kind, Via: ciRefVia})
	}
	if len(problems) > 0 {
		return fmt.Sprintf("refusing to update %s/%s: %s. A CI job's repo-write grant may create and move tags "+
			"(refs/tags/v*, a force-moved refs/tags/latest) and other branches (a force-moved refs/heads/candidate), "+
			"but may only fast-forward %s", owner, repo, strings.Join(problems, "; "), protectedList())
	}
	check.grant(grants...)
	return ""
}

// ciRefUpdate is one update's kind when CIRefUpdates allows it, or why
// not.
func ciRefUpdate(check *pushCheck, u RefUpdate) (kind, problem string) {
	kind = updateKind(u)
	if !CIProtectedBranches[u.Ref] {
		if kind == "update" && strings.HasPrefix(u.Ref, "refs/tags/") {
			// Moving a tag is always a force-push; say so in the audit line.
			kind = "force"
		}
		return kind, ""
	}
	switch kind {
	case "create":
		return kind, ""
	case "delete":
		return "", fmt.Sprintf("%s may not be deleted from CI", u.Ref)
	}
	var why string
	if check != nil {
		var ok bool
		if ok, why = check.fastForward(u.New, u.Old); ok {
			return kind, ""
		}
	}
	if why != "" {
		why = " (asking the repository for the commits it already has: " + why + ")"
	}
	return "", fmt.Sprintf("%s: could not show %s is a fast-forward of %s%s, and a force-push to it is refused from CI",
		u.Ref, shortCommit(u.New), shortCommit(u.Old), why)
}

func protectedList() string {
	refs := make([]string, 0, len(CIProtectedBranches))
	for ref := range CIProtectedBranches {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return strings.Join(refs, ", ")
}
