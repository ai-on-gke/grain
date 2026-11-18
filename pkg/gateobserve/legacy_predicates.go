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
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/qa"
	"github.com/ai-on-gke/grain/pkg/releasepolicy"
	"regexp"
)

func repoDeclaredSuite(id string) (model.RepoRef, string, bool) {
	return releasepolicy.RepoDeclaredSuite(id)
}
func parseQualificationAttempt(extID, baseTag string) (int, bool) {
	return releasepolicy.ParseQualificationAttempt(extID, baseTag)
}
func latestAttemptCheckRun(runs []github.CheckRun, name, app, baseTag string) (int, *github.CheckRun) {
	return releasepolicy.LatestAttemptCheckRun(runs, name, app, baseTag)
}
func peelPendingTag(tags github.ReleaseTagReader, repo model.RepoRef, tag github.TagRef) (commit, message string, err error) {
	return releasepolicy.PeelPendingTag(tags, repo, tag)
}
func readReleaseConf(files github.FileReader, repo model.RepoRef, commit string) (major int, hasMajor bool, never map[string]bool) {
	return releasepolicy.ReadReleaseConf(files, repo, commit)
}
func newestNotesBase(released, retracted, never map[string]bool, target string) string {
	return releasepolicy.NewestNotesBase(released, retracted, never, target)
}
func parseSemverParts(v string) ([3]int, bool) { return releasepolicy.ParseSemverParts(v) }
func parseSemverMajor(v string) (int, bool)    { return releasepolicy.ParseSemverMajor(v) }
func semverLess(a, b string) bool              { return releasepolicy.SemverLess(a, b) }
func commitIsBreaking(c github.Commit) bool    { return releasepolicy.CommitIsBreaking(c) }
func countTaskLinks(t model.Task, kind model.LinkKind) int {
	n := 0
	for _, l := range t.Links {
		if l.Kind == kind {
			n++
		}
	}
	return n
}
func hasTaskLink(t model.Task, kind model.LinkKind) bool { return countTaskLinks(t, kind) > 0 }
func maxS4RepairRounds(f qa.FlowSpec) int                { return max(0, min(f.MaxS4RepairRounds, 3)) }

var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
var retractedTagPattern = regexp.MustCompile(`^(v[0-9]+\.[0-9]+\.[0-9]+)-retracted$`)
