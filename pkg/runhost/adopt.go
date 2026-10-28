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

package runhost

import (
	"fmt"
	"slices"
	"time"
)

// The adoption pass: at daemon startup, the run-host listing is what
// tells a live task_run row the previous daemon abandoned from one whose
// work is still going (docs/non-disruptive-upgrade.md, "Run hosts never
// touch the store"). Decide is the whole decision, with no I/O, so every
// combination of row, run host, protocol and spool state has one answer
// held by a table test (adopt_test.go).

// Claim is one live task_run row, as the adoption pass sees it.
type Claim struct {
	Run  string
	Task string
	// LastSeen is when a daemon last advanced this run's spool cursor:
	// the start of the gap the adoption credits as wait time. Zero when
	// no cursor was ever written.
	LastSeen time.Time
}

// Adoption is a live row matched to a run host that is resumed.
type Adoption struct {
	Claim Claim
	Host  Info
	// Gap is how long no daemon was reading the spool, credited to the
	// run as wait time so the restart is not charged to its budget. The
	// deadline itself is not reset.
	Gap time.Duration
}

// Release is a run host to destroy, with what the journal says about it.
type Release struct {
	Host   Info
	Reason string
}

// Ending is a live row to finish as model.InterruptedOutcome, with the
// reason the listing or the spool gave rather than a guess.
type Ending struct {
	Claim  Claim
	Reason string
}

// Plan is what the adoption pass does.
type Plan struct {
	Adopt   []Adoption
	Release []Release
	Finish  []Ending
}

// SpoolState is what a run host's spool says, read by the caller.
type SpoolState struct {
	// Exited is true when exit.json is there: the CLI finished, and the
	// spool is final.
	Exited bool
}

// Decide matches rows to run hosts. spool reports a listed run host's
// spool state; now is the startup's clock.
func Decide(rows []Claim, hosts []Info, spool func(Info) SpoolState, now time.Time) Plan {
	var plan Plan
	byRun := map[string][]Info{}
	for _, h := range hosts {
		byRun[h.Labels.Run] = append(byRun[h.Labels.Run], h)
	}
	claimed := map[string]bool{}
	for _, row := range rows {
		claimed[row.Run] = true
		candidates := byRun[row.Run]
		if len(candidates) == 0 {
			plan.Finish = append(plan.Finish, Ending{Claim: row, Reason: "no run host is left for this run: " +
				"it was never started, or it was removed while no daemon was running"})
			continue
		}
		// One run host per row. Should there ever be more (a create the
		// previous daemon retried), keep a running one and free the rest.
		slices.SortStableFunc(candidates, func(a, b Info) int {
			switch {
			case a.Running == b.Running:
				return 0
			case a.Running:
				return -1
			}
			return 1
		})
		h := candidates[0]
		for _, extra := range candidates[1:] {
			plan.Release = append(plan.Release, Release{Host: extra,
				Reason: fmt.Sprintf("a second run host for run %s; %s is the one adopted", row.Run, h.Name)})
		}
		if !Supported(h.Labels.Protocol) {
			plan.Finish = append(plan.Finish, Ending{Claim: row, Reason: fmt.Sprintf(
				"its run host %s speaks run-host protocol %d, which this build (protocol %d) cannot adopt; "+
					"the conversation stays in the session store for the next dispatch",
				h.Name, h.Labels.Protocol, Protocol)})
			plan.Release = append(plan.Release, Release{Host: h, Reason: fmt.Sprintf("unsupported run-host protocol %d", h.Labels.Protocol)})
			continue
		}
		state := spool(h)
		if !h.Running && !state.Exited {
			plan.Finish = append(plan.Finish, Ending{Claim: row, Reason: fmt.Sprintf(
				"its run host %s stopped without recording how its agent ended (killed, or the machine restarted)", h.Name)})
			plan.Release = append(plan.Release, Release{Host: h, Reason: "stopped without an exit record"})
			continue
		}
		if h.Running && !h.Labels.Deadline.IsZero() && now.After(h.Labels.Deadline) {
			plan.Finish = append(plan.Finish, Ending{Claim: row, Reason: fmt.Sprintf(
				"its run host %s outlived its own deadline (%s) while no daemon was watching it",
				h.Name, h.Labels.Deadline.UTC().Format(time.RFC3339))})
			plan.Release = append(plan.Release, Release{Host: h, Reason: "past its deadline label"})
			continue
		}
		a := Adoption{Claim: row, Host: h}
		if !row.LastSeen.IsZero() && now.After(row.LastSeen) {
			a.Gap = now.Sub(row.LastSeen)
		}
		plan.Adopt = append(plan.Adopt, a)
	}
	for _, h := range hosts {
		if !claimed[h.Labels.Run] {
			plan.Release = append(plan.Release, Release{Host: h, Reason: "no live run claims it"})
		}
	}
	return plan
}
