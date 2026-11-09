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

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

// SuiteQualification is a repo-declared suite's `qualification:` stanza:
// what makes the release-qualification reconciler
// (orchestrator/releasequalification.go) run the suite as a release gate,
// and the four things that gate is otherwise hard-wired to.
//
//	qualification:
//	  check: grain/edition-qualification             # default grain/release-qualification
//	  candidate: synced-release                      # default rc-tags
//	  stage1: { status: grain/edition-qualification } # or { workflow: nightly } (default)
//	  upstream: github.com/ai-on-gke/grain           # synced-release only; this is the default
//
// A suite whose file has no stanza is a release gate only when its stem is
// ReleaseQualificationSuiteStem, and then it is one with every default --
// exactly what that suite was before the stanza existed.
//
// The stanza is the declaration's, kept beside its provenance
// (`repo_declaration_qualification`) rather than on the suite row, the way
// a template's push set is: only the repository's default branch writes
// it, and no settings export carries it.
type SuiteQualification struct {
	// Check is the check run the verdict is posted as on the candidate's
	// commit.
	Check string
	// Candidate is how the candidate commit is found.
	Candidate QualificationCandidate
	// Stage1Workflow is the workflow run (by name) that must be green on
	// the candidate before Stage 2 starts; Stage1Status the commit-status
	// context that must be. Exactly one is set once defaults are applied.
	Stage1Workflow string
	Stage1Status   string
	// Upstream is the Go module whose release a synced-release candidate's
	// go.mod pins. Its repository, whose tags name the releases, is the
	// owner and name the path's second and third elements give.
	Upstream string
}

// QualificationCandidate is how a qualification suite's candidate commit
// is found.
type QualificationCandidate string

const (
	// CandidateRCTags is the newest `vX.Y.Z-rc.N` tag of the repository
	// that has been neither discarded nor promoted (playbook "Lifecycle").
	CandidateRCTags QualificationCandidate = "rc-tags"
	// CandidateSyncedRelease is, for a repository that builds on another
	// module rather than cutting candidates of its own, the newest commit
	// on its default branch that changed go.mod to pin a release
	// (`vX.Y.Z`) of Upstream the repository has not tagged itself yet.
	CandidateSyncedRelease QualificationCandidate = "synced-release"
)

// CoreModulePath is grain's own module, the default Upstream.
const CoreModulePath = "github.com/ai-on-gke/grain"

// WithDefaults is q with every field it leaves blank filled in.
func (q SuiteQualification) WithDefaults() SuiteQualification {
	q.Check = strings.TrimSpace(q.Check)
	if q.Check == "" {
		q.Check = ReleaseQualificationCheckName
	}
	if q.Candidate == "" {
		q.Candidate = CandidateRCTags
	}
	q.Stage1Workflow, q.Stage1Status = strings.TrimSpace(q.Stage1Workflow), strings.TrimSpace(q.Stage1Status)
	if q.Stage1Workflow == "" && q.Stage1Status == "" {
		q.Stage1Workflow = ReleaseQualificationStage1Workflow
	}
	q.Upstream = strings.TrimSpace(q.Upstream)
	if q.Upstream == "" && q.Candidate == CandidateSyncedRelease {
		q.Upstream = CoreModulePath
	}
	return q
}

// Validate reports what is wrong with a stanza as a repository wrote it.
func (q SuiteQualification) Validate() error {
	switch q.Candidate {
	case "", CandidateRCTags, CandidateSyncedRelease:
	default:
		return fmt.Errorf("qualification.candidate is %q or %q, got %q", CandidateRCTags, CandidateSyncedRelease, q.Candidate)
	}
	if q.Stage1Workflow != "" && q.Stage1Status != "" {
		return fmt.Errorf("qualification.stage1 is one of `workflow:` or `status:`, not both")
	}
	if strings.ContainsAny(q.Check, "\n\r") || strings.ContainsAny(q.Stage1Status, "\n\r") ||
		strings.ContainsAny(q.Stage1Workflow, "\n\r") {
		return fmt.Errorf("qualification names are one line each")
	}
	if q.Upstream != "" {
		if q.Candidate != CandidateSyncedRelease {
			return fmt.Errorf("qualification.upstream is only read with `candidate: %s`", CandidateSyncedRelease)
		}
		if _, err := q.UpstreamRepo(); err != nil {
			return err
		}
	}
	return nil
}

// UpstreamRepo is the repository whose tags name Upstream's releases.
func (q SuiteQualification) UpstreamRepo() (RepoRef, error) {
	path := q.WithDefaults().Upstream
	if err := module.CheckPath(path); err != nil {
		return RepoRef{}, fmt.Errorf("qualification.upstream %q is not a module path: %w", path, err)
	}
	parts := strings.Split(path, "/")
	if len(parts) < 3 {
		return RepoRef{}, fmt.Errorf("qualification.upstream %q does not name a repository (host/owner/name)", path)
	}
	return RepoRef{Owner: parts[1], Name: parts[2]}, nil
}

// Stage1Name is what Stage 1 is called in a check run's summary: the
// workflow's name, or the status's context.
func (q SuiteQualification) Stage1Name() string {
	q = q.WithDefaults()
	if q.Stage1Status != "" {
		return q.Stage1Status
	}
	return q.Stage1Workflow
}

// QualificationTag is the SuiteRunOptions.Tag every suite run the
// reconciler starts for one candidate of the suite named stem carries.
// For ReleaseQualificationSuiteStem it is ReleaseQualificationTag.
func QualificationTag(stem string, repo RepoRef, candidate string) string {
	return stem + ":" + strings.ToLower(repo.Owner) + "/" + strings.ToLower(repo.Name) + "@" + candidate
}

// QualificationAttemptTag is the SuiteRunOptions.Tag and check-run
// ExternalID of attempt (1-based) of a candidate whose first Stage 2
// attempt is tagged baseTag (QualificationTag). Attempt 1 keeps baseTag
// unchanged so single-attempt candidates and existing check runs match as
// they are; retries after an infrastructure stall are baseTag#2, #3, ...
func QualificationAttemptTag(baseTag string, attempt int) string {
	if attempt <= 1 {
		return baseTag
	}
	return fmt.Sprintf("%s#%d", baseTag, attempt)
}

// GoModPin is the version go.mod requires module at, after any replace
// of module by another version of itself. ok is false when go.mod does
// not require module, replaces it with anything but a version of the
// same module, or does not parse.
func GoModPin(gomod []byte, modulePath string) (version string, ok bool) {
	// Parse rather than ParseLax: ParseLax drops replace directives, which
	// are the main module's own and decide what it builds against.
	f, err := modfile.Parse("go.mod", gomod, nil)
	if err != nil {
		return "", false
	}
	for _, r := range f.Require {
		if r.Mod.Path == modulePath {
			version, ok = r.Mod.Version, true
		}
	}
	if !ok {
		return "", false
	}
	for _, r := range f.Replace {
		if r.Old.Path != modulePath || (r.Old.Version != "" && r.Old.Version != version) {
			continue
		}
		if r.New.Path != modulePath || r.New.Version == "" {
			return "", false
		}
		version = r.New.Version
	}
	return version, true
}

// PinnedRelease is the release (`vX.Y.Z`) a go.mod pin of a module names:
// the version itself when it is one, or, for a pseudo-version, the
// release tag in releases (tag name -> commit) on the commit it pins --
// the newest, if one commit carries more than one. ok is false for a pin
// of anything else: a prerelease, or a commit no release was cut from.
func PinnedRelease(version string, releases map[string]string) (string, bool) {
	if _, ok := ParseReleaseTag(version); ok {
		return version, true
	}
	if !module.IsPseudoVersion(version) {
		return "", false
	}
	rev, err := module.PseudoVersionRev(version)
	if err != nil || rev == "" {
		return "", false
	}
	var best RCTag
	name, found := "", false
	for tag, sha := range releases {
		v, ok := ParseReleaseTag(tag)
		if !ok || !strings.HasPrefix(strings.ToLower(sha), strings.ToLower(rev)) {
			continue
		}
		if !found || best.Less(v) {
			best, name, found = v, tag, true
		}
	}
	return name, found
}

// putDeclarationQualification replaces one declared suite's stanza with
// what the file read at this sync says: none at all when q is nil.
func putDeclarationQualification(ctx context.Context, tx *sql.Tx, id string, q *SuiteQualification) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM `repo_declaration_qualification` WHERE `id` = ?", id); err != nil {
		return fmt.Errorf("replacing %s's qualification: %w", id, err)
	}
	if q == nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO `repo_declaration_qualification` (`id`,`check_name`,`candidate`,`stage1_workflow`,`stage1_status`,`upstream`) VALUES (?,?,?,?,?,?)",
		id, q.Check, string(q.Candidate), q.Stage1Workflow, q.Stage1Status, q.Upstream); err != nil {
		return fmt.Errorf("writing %s's qualification: %w", id, err)
	}
	return nil
}

// SuiteQualifications is every declared suite's `qualification:` stanza
// as its file wrote it (defaults not applied), by suite id. A suite with
// no stanza has no entry.
func (s *Store) SuiteQualifications(ctx context.Context) (map[string]SuiteQualification, error) {
	out := map[string]SuiteQualification{}
	err := each(ctx, s.db,
		"SELECT `id`,`check_name`,`candidate`,`stage1_workflow`,`stage1_status`,`upstream` FROM `repo_declaration_qualification` ORDER BY `id`",
		nil, func(rows *sql.Rows) error {
			var id, candidate string
			var q SuiteQualification
			if err := rows.Scan(&id, &q.Check, &candidate, &q.Stage1Workflow, &q.Stage1Status, &q.Upstream); err != nil {
				return err
			}
			q.Candidate = QualificationCandidate(candidate)
			out[id] = q
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading declared qualification stanzas: %w", err)
	}
	return out, nil
}
