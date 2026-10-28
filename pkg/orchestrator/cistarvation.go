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
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/forge"
)

// Grain CI starvation, read off the stall clock and the CI service.
//
// The merge queue used to answer a grain CI stall itself, once per head
// commit -- extending the deadline or starting a fresh run (the old
// retryStalledCI) -- before a person was asked. That is now
// system:pipeline-steward's call: the daemon only detects the stall
// (forge.ChecksStalled, Observation.MergeQueueStalledAt) and hands it to a
// person (escalateStalledChecks), and what is here is the read-only half
// that says whether the stall is the deployment's capacity rather than the
// pull request.

// ciStarvedEntries is how many pull requests stalled at the same time
// count as grain CI being short of machines deployment-wide, rather than
// one pull request being stuck.
const ciStarvedEntries = 3

// ciStarvedJobs and ciStarvedJobWait are the same sign read off the CI
// service itself, which is what the `ci_queueing` metrics are measured
// from: at least this many jobs in live runs, each waiting at least this
// long for a machine without being admitted.
const (
	ciStarvedJobs    = 3
	ciStarvedJobWait = 30 * time.Minute
)

// ciStarvation reports whether grain CI is short of machines across the
// whole deployment, and since when. runner may be nil, in which case only
// the stall count is read.
func ciStarvation(runner ci.Runner, now time.Time) (time.Time, bool) {
	var since time.Time
	if n, oldest := forge.StalledChecks(now); n >= ciStarvedEntries {
		since = oldest
	}
	if runner != nil {
		waiting, oldest := 0, time.Time{}
		for _, run := range runner.List() {
			if run.Conclusion.Done() {
				continue
			}
			for _, j := range run.Jobs {
				if j.StartedAt != nil || j.Conclusion.Done() || j.ActivityAt == nil ||
					now.Sub(*j.ActivityAt) < ciStarvedJobWait {
					continue
				}
				waiting++
				if oldest.IsZero() || j.ActivityAt.Before(oldest) {
					oldest = *j.ActivityAt
				}
			}
		}
		if waiting >= ciStarvedJobs && (since.IsZero() || oldest.Before(since)) {
			since = oldest
		}
	}
	return since, !since.IsZero()
}

// ciStarvationSentence is how a comment or a row names starvation:
// "grain CI has been short of machines for 3h".
func ciStarvationSentence(since, now time.Time) string {
	d := now.Sub(since)
	if d < time.Minute {
		d = time.Minute
	}
	return "grain CI has been short of machines for " + humanDuration(d.Round(time.Minute))
}

// ciRunProgressed reports whether any job of run has got a machine in this
// run (a cached job did its work in an earlier one), or the run has
// already concluded and only the verdict is still to be collected.
func ciRunProgressed(run ci.Run) bool {
	if run.Conclusion.Done() {
		return true
	}
	for _, j := range run.Jobs {
		if !j.Cached && j.StartedAt != nil {
			return true
		}
	}
	return false
}

func capitalize(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}
