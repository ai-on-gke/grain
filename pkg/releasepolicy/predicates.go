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

// Package releasepolicy holds existing pure release predicates shared by legacy
// services and observation readers. It has no gate evaluator or effect clients.
package releasepolicy

import (
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"regexp"
	"strconv"
	"strings"
)

var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
var retractedTagPattern = regexp.MustCompile(`^(v[0-9]+\.[0-9]+\.[0-9]+)-retracted$`)
var releaseMajorConfPattern = regexp.MustCompile(`^RELEASE_MAJOR="?([0-9]+)"?[ \t]*(?:#.*)?$`)
var releaseNeverPromoteQuotedPattern = regexp.MustCompile(`^RELEASE_NEVER_PROMOTE="([^"]*)"[ \t]*(?:#.*)?$`)
var releaseNeverPromoteBarePattern = regexp.MustCompile(`^RELEASE_NEVER_PROMOTE=([^"#\r\n]*?)[ \t]*(?:#.*)?$`)
var breakingSubjectPattern = regexp.MustCompile(`^[A-Za-z]+(\([^)]*\))?!: `)
var mergePullRequestPattern = regexp.MustCompile(`^Merge pull request #[0-9]+ `)
var breakingTrailerPattern = regexp.MustCompile(`^BREAKING[ -]CHANGE:`)

func RepoDeclaredSuite(id string) (model.RepoRef, string, bool) {
	prefix := model.RepoDeclaredPrefix + string(model.DeclaredSuite) + ":"
	if !strings.HasPrefix(id, prefix) {
		return model.RepoRef{}, "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(id, prefix), ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return model.RepoRef{}, "", false
	}
	return model.RepoRef{Owner: parts[0], Name: parts[1]}, parts[2], true
}

func ParseQualificationAttempt(extID, baseTag string) (int, bool) {
	if extID == baseTag {
		return 1, true
	}
	rest, ok := strings.CutPrefix(extID, baseTag+"#")
	if !ok || rest == "" {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 2 || strconv.Itoa(n) != rest {
		return 0, false
	}
	return n, true
}

func LatestAttemptCheckRun(runs []github.CheckRun, name, app, baseTag string) (int, *github.CheckRun) {
	bestAttempt := 1
	var out *github.CheckRun
	for i := range runs {
		r := &runs[i]
		if r.Name != name || r.App != app {
			continue
		}
		attempt, ok := ParseQualificationAttempt(r.ExternalID, baseTag)
		if !ok {
			continue
		}
		if out == nil || attempt > bestAttempt || (attempt == bestAttempt && r.ID > out.ID) {
			bestAttempt = attempt
			out = r
		}
	}
	return bestAttempt, out
}

func PeelPendingTag(tags github.ReleaseTagReader, repo model.RepoRef, tag github.TagRef) (commit, message string, err error) {
	if tag.ObjectType != "tag" {
		return tag.ObjectSHA, "", nil
	}
	at, err := tags.GetAnnotatedTag(repo.StoredOwner(), repo.Name, tag.ObjectSHA)
	if err != nil {
		return "", "", err
	}
	if at == nil {
		return tag.ObjectSHA, "", nil
	}
	return at.Commit, at.Message, nil
}

func ReadReleaseConf(files github.FileReader, repo model.RepoRef, commit string) (major int, hasMajor bool, never map[string]bool) {
	never = map[string]bool{}
	if files == nil {
		return 0, false, never
	}
	var raw []byte
	var ok bool
	if commit != "" {
		raw, ok, _ = files.ReadFile(repo.StoredOwner(), repo.Name, commit, "scripts/release.conf")
	}
	if !ok {
		raw, ok, _ = files.ReadFile(repo.StoredOwner(), repo.Name, "main", "scripts/release.conf")
	}
	if !ok {
		return 0, false, never
	}
	var neverRaw string
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		l := strings.TrimSpace(line)
		if m := releaseMajorConfPattern.FindStringSubmatch(l); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				major, hasMajor = n, true
			}
		} else if m := releaseNeverPromoteQuotedPattern.FindStringSubmatch(l); m != nil {
			neverRaw = m[1]
		} else if m := releaseNeverPromoteBarePattern.FindStringSubmatch(l); m != nil {
			neverRaw = m[1]
		}
	}
	for _, tag := range strings.Fields(neverRaw) {
		never[tag] = true
	}
	return major, hasMajor, never
}

func NewestNotesBase(released, retracted, never map[string]bool, target string) string {
	best := ""
	for v := range released {
		if retracted[v] || never[v] || !SemverLess(v, target) {
			continue
		}
		if best == "" || SemverLess(best, v) {
			best = v
		}
	}
	return best
}

func ParseSemverParts(v string) ([3]int, bool) {
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func ParseSemverMajor(v string) (int, bool) {
	parts, ok := ParseSemverParts(v)
	return parts[0], ok
}

func SemverLess(a, b string) bool {
	pa, oka := ParseSemverParts(a)
	pb, okb := ParseSemverParts(b)
	if !oka || !okb {
		return a < b
	}
	for i := range 3 {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func CommitIsBreaking(c github.Commit) bool {
	msg := strings.ReplaceAll(c.Message, "\r\n", "\n")
	subject, rest, _ := strings.Cut(msg, "\n")
	if breakingSubjectPattern.MatchString(subject) {
		return true
	}
	if mergePullRequestPattern.MatchString(subject) {
		for _, line := range strings.Split(rest, "\n") {
			t := strings.TrimSpace(line)
			if t == "" {
				continue
			}
			if breakingSubjectPattern.MatchString(t) {
				return true
			}
			break
		}
	}
	for _, line := range strings.Split(msg, "\n") {
		if breakingTrailerPattern.MatchString(line) {
			return true
		}
	}
	return false
}
