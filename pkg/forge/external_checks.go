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

package forge

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

// DefaultExternalCheckStallThreshold is how long a third-party status check
// (such as cla/google) may sit pending on the same head commit with no state
// change and no new check runs before the merge walk nudges it once, and how
// long after that nudge before it raises pipeline.merge-stalled to the
// pipeline steward.
const DefaultExternalCheckStallThreshold = 30 * time.Minute

var externalChecks = sightings{
	window: DefaultExternalCheckStallThreshold,
	seen:   map[string]sighting{},
}

// SetExternalCheckStallThreshold replaces DefaultExternalCheckStallThreshold
// and returns a function that restores the previous value. Zero disables
// external-check nudging and escalation.
func SetExternalCheckStallThreshold(d time.Duration) func() {
	return externalChecks.setWindow(d)
}

// ForgetExternalChecks drops ref's external-check stall sighting.
func ForgetExternalChecks(ref Ref) {
	externalChecks.forget(ref)
}

// IsExternalCheck reports whether c is a check posted by a third-party app
// rather than grain's own CI/state check or an in-repository GitHub Actions
// workflow.
func IsExternalCheck(c github.CheckRun, requiredCheck string) bool {
	if c.App == "" {
		return false
	}
	if strings.EqualFold(c.App, "github-actions") || strings.Contains(c.HTMLURL, "/actions/runs/") {
		return false
	}
	if strings.EqualFold(c.App, "grain") {
		return false
	}
	if requiredCheck != "" && c.Name == requiredCheck {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(c.Name))
	if lower == "grain" || lower == "grain state check" || lower == "grain-state-check" ||
		strings.HasPrefix(lower, "grain/") || strings.HasPrefix(lower, "grain-") ||
		strings.HasPrefix(lower, "grain ci") {
		return false
	}
	return true
}

// ExternalChecksStalledWithin inspects a pending pull request's gated checks
// and reports any unfinished third-party checks when they are the only checks
// holding the pull request pending, how long the head has gone with no state
// change and no new check runs, and whether that wait has reached the external
// stall threshold (shortened by timeout when set).
func ExternalChecksStalledWithin(
	ref Ref,
	headSHA string,
	checks []github.CheckRun,
	requiredCheck string,
	grain GrainCI,
	now time.Time,
	timeout time.Duration,
) (pending []github.CheckRun, waited time.Duration, stalled bool, threshold time.Duration) {
	if headSHA == "" || (grain.Required && !grain.Passed) {
		externalChecks.forget(ref)
		return nil, 0, false, 0
	}
	if grain.Passed {
		checks = FilterActionsBudgetFailures(checks)
	}
	if requiredCheck != "" && !hasCheck(checks, requiredCheck) {
		externalChecks.forget(ref)
		return nil, 0, false, 0
	}
	for _, c := range checks {
		if c.Status != "completed" {
			if !IsExternalCheck(c, requiredCheck) {
				externalChecks.forget(ref)
				return nil, 0, false, 0
			}
			pending = append(pending, c)
			continue
		}
		if c.Conclusion == nil {
			externalChecks.forget(ref)
			return nil, 0, false, 0
		}
		switch *c.Conclusion {
		case "failure", "timed_out", "startup_failure", "cancelled", "stale", "action_required":
			externalChecks.forget(ref)
			return nil, 0, false, 0
		}
	}
	if len(pending) == 0 {
		externalChecks.forget(ref)
		return nil, 0, false, 0
	}

	keySHA := headSHA + "\x00" + externalProgressKey(checks, grain)
	first, window := externalChecks.observe(ref, keySHA, now)
	if now.After(first) {
		waited = now.Sub(first)
	}
	if window <= 0 {
		return pending, waited, false, 0
	}
	if timeout > 0 && timeout < window {
		window = timeout
	}
	return pending, waited, !now.Before(first.Add(window)), window
}

func externalProgressKey(checks []github.CheckRun, grain GrainCI) string {
	parts := make([]string, 0, len(checks))
	for _, c := range checks {
		conc := ""
		if c.Conclusion != nil {
			conc = *c.Conclusion
		}
		parts = append(parts, fmt.Sprintf("%d|%s|%s|%s", c.ID, c.Name, c.Status, conc))
	}
	sort.Strings(parts)
	return fmt.Sprintf("%s;%+v", strings.Join(parts, ","), grain)
}
