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

package gateobserve

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

type observationComparisons interface {
	CompareRefs(owner, repo, base, head string) (github.Comparison, error)
}

// readReleaseReadiness uses ordinary, scoped read adapters. A missing signature,
// invocation/environment policy or complete asset set remains explicitly unknown.
// In particular it never executes candidate-gate/release-from-trunk or any shell.
func readReleaseReadiness(ctx context.Context, source any, repo model.RepoRef, tags []github.Tag, f *gates.ReleaseFacts) {
	versions := []any{f.SourceVersion}
	defer func() { f.SourceVersion = gates.Digest(versions) }()
	if reader, ok := source.(github.ReleaseTagReader); ok && ctx.Err() == nil {
		refs, err := reader.ListMatchingTags(repo.StoredOwner(), repo.Name, f.Version+"-pending")
		if err == nil && len(refs) <= 16 {
			for _, ref := range refs {
				if ref.Name != f.Version+"-pending" {
					continue
				}
				head, message, err := peelPendingTag(reader, repo, ref)
				if err != nil || len(message) > 8192 {
					continue
				}
				versions = append(versions, ref, head, message)
				if head != f.Head {
					no := false
					f.Provenance = &no
					continue
				}
				for _, line := range strings.Split(message, "\n") {
					if line == "Qualification: PASS" {
						f.QualificationBased = true
					}
					if text, ok := strings.CutPrefix(line, "Soak-Seconds: "); ok {
						seconds, err := strconv.ParseInt(text, 10, 64)
						if err == nil && seconds >= 0 {
							f.RecordedSoakSeconds = &seconds
							f.PredicateReasons["soak"] = "Recorded candidate soak is " + text + " seconds; effective workflow minimum/short-soak answer is not retained."
						}
					}
				}
			}
		}
	}
	comparer, canCompare := source.(observationComparisons)
	heads, canHead := source.(qaObservationHeads)
	if canCompare && canHead && f.Provenance == nil && ctx.Err() == nil {
		main, err := heads.GetBranchHead(repo.StoredOwner(), repo.Name, "main")
		if err == nil && main != nil && main.SHA != "" {
			cmp, err := comparer.CompareRefs(repo.StoredOwner(), repo.Name, main.SHA, f.Head)
			if err == nil {
				versions = append(versions, main.SHA, cmp.Status, cmp.MergeBase)
				if cmp.Status == "behind" || cmp.Status == "identical" {
					yes := true
					f.Provenance = &yes
				}
			}
		}
		// The legacy convergence branch is an alternative ancestry source.
		if f.Provenance == nil && ctx.Err() == nil {
			convergence, err := heads.GetBranchHead(repo.StoredOwner(), repo.Name, "release/"+f.Version)
			if err == nil && convergence != nil && convergence.SHA != "" {
				cmp, err := comparer.CompareRefs(repo.StoredOwner(), repo.Name, convergence.SHA, f.Head)
				if err == nil {
					versions = append(versions, convergence.SHA, cmp.Status, cmp.MergeBase)
					allowed := cmp.Status == "behind" || cmp.Status == "identical"
					f.Provenance = &allowed
				}
			}
		}
	}
	files, canRead := source.(github.FileReader)
	if !canRead || !canCompare || ctx.Err() != nil {
		return
	}
	// Pin policy to the candidate commit. The legacy helper may fall back to
	// main; this observer refuses to turn an unversioned fallback into proof.
	raw, found, err := files.ReadFile(repo.StoredOwner(), repo.Name, f.Head, "scripts/release.conf")
	if err != nil || !found || len(raw) > 8192 {
		return
	}
	major, hasMajor, never := readReleaseConf(observationConf{raw}, repo, f.Head)
	versions = append(versions, gates.Digest(raw))
	f.PolicyVersion = gates.Digest(struct{ Suite, Conf string }{f.PolicyVersion, gates.Digest(raw)})
	released, retracted := map[string]bool{}, map[string]bool{}
	commits := map[string]string{}
	for _, tag := range tags {
		if releaseTagPattern.MatchString(tag.Name) {
			released[tag.Name] = true
			commits[tag.Name] = tag.SHA
		}
		if m := retractedTagPattern.FindStringSubmatch(tag.Name); m != nil {
			retracted[m[1]] = true
		}
	}
	base := newestNotesBase(released, retracted, never, f.Version)
	if base == "" {
		yes := true
		f.BreakingAllowed = &yes
		return
	}
	if commits[base] == "" {
		return
	}
	cmp, err := comparer.CompareRefs(repo.StoredOwner(), repo.Name, commits[base], f.Head)
	if err != nil {
		return
	}
	versions = append(versions, commits[base], cmp)
	hasBreaking := slices.ContainsFunc(cmp.Commits, commitIsBreaking)
	// CompareRefs explicitly caps commits at 250. Negative evidence at that
	// boundary cannot attest the complete range.
	if !hasBreaking && len(cmp.Commits) < 250 && (cmp.Status == "ahead" || cmp.Status == "identical") {
		yes := true
		f.BreakingAllowed = &yes
		return
	}
	if hasBreaking {
		oldMajor, okOld := parseSemverMajor(base)
		newMajor, okNew := parseSemverMajor(f.Version)
		if okOld && okNew && hasMajor && newMajor > oldMajor && major >= newMajor {
			f.PredicateReasons["breaking_change"] = "Breaking changes are allowed by the exact candidate's declared major policy, but effective workflow/environment overrides are not retained."
		} else {
			f.PredicateReasons["breaking_change"] = "Breaking changes require a major-version policy change or a verified person's allow_breaking answer; effective overrides are not retained."
		}
	}
}

type observationConf struct{ raw []byte }

func (c observationConf) ReadFile(_, _, _, path string) ([]byte, bool, error) {
	if path == "scripts/release.conf" {
		return c.raw, true, nil
	}
	return nil, false, nil
}
