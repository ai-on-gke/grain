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

package workflows

import (
	"fmt"
	"strings"
)

// The types a JobRun carries that pkg/ci's diagnostics, shared-fault and
// gate code produce. They moved here with JobRun so that the run record
// is neutral (docs/design/grit-split.md, GS-2); the code that fills them
// in stays where it was, and pkg/ci re-exports each under its old name.

// FailureSummary holds high-signal structured diagnostics and test failure information
// extracted from job or step output.
type FailureSummary struct {
	Tests       []TestFailure `json:"tests,omitempty"`
	Diagnostics []Diagnostic  `json:"diagnostics,omitempty"`
	RawErrors   []string      `json:"rawErrors,omitempty"`
}

// TestFailure represents a single failed test case with location and failure reason.
type TestFailure struct {
	Name        string `json:"name"`
	Location    string `json:"location,omitempty"`
	Message     string `json:"message,omitempty"`
	Diff        string `json:"diff,omitempty"`
	Quarantined bool   `json:"quarantined,omitempty"`
}

// Diagnostic represents a compiler, linter, or tool diagnostic at a specific source location.
type Diagnostic struct {
	File     string `json:"file"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Message  string `json:"message"`
	Severity string `json:"severity,omitempty"`
	Rule     string `json:"rule,omitempty"`
}

// Empty reports whether fs contains any failure details.
func (fs *FailureSummary) Empty() bool {
	if fs == nil {
		return true
	}
	return len(fs.Tests) == 0 && len(fs.Diagnostics) == 0 && len(fs.RawErrors) == 0
}

// Render formats the structured failure summary as a concise human-readable block.
func (fs *FailureSummary) Render() string {
	if fs == nil || fs.Empty() {
		return ""
	}
	var b strings.Builder
	b.WriteString("Failure summary:")
	if len(fs.Tests) > 0 {
		b.WriteString("\n  Failed tests:")
		for _, t := range fs.Tests {
			fmt.Fprintf(&b, "\n    • %s", t.Name)
			if t.Quarantined {
				b.WriteString(" [quarantined]")
			}
			if t.Location != "" {
				fmt.Fprintf(&b, " (%s)", t.Location)
			}
			if t.Message != "" {
				lines := strings.Split(t.Message, "\n")
				if len(lines) == 1 {
					fmt.Fprintf(&b, ": %s", lines[0])
				} else {
					for _, l := range lines {
						if strings.TrimSpace(l) != "" {
							fmt.Fprintf(&b, "\n      %s", l)
						}
					}
				}
			}
			if t.Diff != "" {
				b.WriteString("\n      Diff:")
				for _, l := range strings.Split(t.Diff, "\n") {
					if strings.TrimSpace(l) != "" {
						fmt.Fprintf(&b, "\n        %s", l)
					}
				}
			}
		}
	}
	if len(fs.Diagnostics) > 0 {
		b.WriteString("\n  Diagnostics:")
		for _, d := range fs.Diagnostics {
			loc := d.File
			if d.Line > 0 {
				loc = fmt.Sprintf("%s:%d", loc, d.Line)
				if d.Column > 0 {
					loc = fmt.Sprintf("%s:%d", loc, d.Column)
				}
			}
			ruleStr := ""
			if d.Rule != "" {
				ruleStr = fmt.Sprintf(" (%s)", d.Rule)
			}
			sevStr := ""
			if d.Severity != "" && strings.ToLower(d.Severity) != "error" {
				sevStr = fmt.Sprintf("[%s] ", d.Severity)
			}
			fmt.Fprintf(&b, "\n    • %s: %s%s%s", loc, sevStr, d.Message, ruleStr)
		}
	}
	if len(fs.RawErrors) > 0 {
		b.WriteString("\n  Errors:")
		for _, err := range fs.RawErrors {
			lines := strings.Split(err, "\n")
			for i, l := range lines {
				if i == 0 {
					fmt.Fprintf(&b, "\n    • %s", l)
				} else if strings.TrimSpace(l) != "" {
					fmt.Fprintf(&b, "\n      %s", l)
				}
			}
		}
	}
	return b.String()
}

// Signature is one failing job reduced to the things that decide whether
// another branch's failure is the same fault.
type Signature struct {
	Source string
	Repo   string
	// JobID is the workflow's own id for the job, JobName its display
	// name -- the id decides, the name is what a reader is shown.
	JobID   string
	JobName string
	// Step is the failing step's name, empty for a job that failed
	// without one (Kind says which).
	Step string
	// Kind separates "a step exited non-zero" from "grain could not run
	// this job at all", so the two can never be folded together however
	// alike their text is. They are different answers to "why is this
	// red" and send a reader to different places (see ConclusionErrored).
	Kind Conclusion
	// Digest is the fingerprint two failures must match on exactly.
	Digest string
	// Paths are the repo-relative files this failure's output names, in
	// the order it named them -- what an open pull request is matched
	// against.
	Paths []string
}

// WaitCause is the kind of thing holding a queueing job, as against the
// sentence about it. The values are short and fixed so that they can be
// counted: they are what a job's wait is bucketed by while it queues.
type WaitCause string

const (
	// WaitForGrant is a capability reservation -- the pre-sandbox
	// prerequisite Admission acquires, which is scarce for reasons that
	// have nothing to do with how much machine this deployment has.
	WaitForGrant WaitCause = "grant"
	// WaitForGuest is the sandbox guest this job will resolve being built
	// and published by a run of grain's own, so that this job -- and
	// every other job on every branch that needs the same guest -- pulls
	// it instead of building one apiece (guestcache.go). Like
	// WaitForGrant it is nothing to do with how much machine this
	// deployment has: a job held here is held by a ~1GB image that does
	// not exist yet.
	WaitForGuest WaitCause = "guest"
	// WaitForSlot is the count: every sandbox this deployment keeps for
	// CI is held by another job (the max_ci_jobs an operator set).
	WaitForSlot WaitCause = "slot"
	// WaitForRun is the same count one level down: this job's own run is
	// already running as many jobs at once as it may (Engine.Concurrency),
	// so what is holding it is its siblings rather than anybody else's
	// branch. Kept apart from WaitForSlot because the two are read
	// differently -- a suite queueing behind itself is a suite wider than
	// the deployment runs, and a suite queueing behind other runs is a
	// deployment with too little to go round.
	WaitForRun WaitCause = "run"
	// WaitForRoom is the budget: there are slots free, but not enough
	// machine left in CI's share of the host for this job's shape.
	WaitForRoom WaitCause = "budget"
	// WaitForHole is the budget again, from the other side: this job is
	// the one capacity is being held back for, and the gate is keeping
	// smaller jobs out while a hole of its shape forms (ReserveAfter).
	WaitForHole WaitCause = "hole"
	// WaitForPriority is the order: this job fits and could go, and the
	// deployment would rather spend the capacity on something else first.
	WaitForPriority WaitCause = "priority"
)

// Wait is why a job is still queueing: the sentence to show whoever is
// watching, and the stable kind of thing behind it.
//
// The two are separate because only one of them can be added up. A
// reason says how the deployment stands right now -- "2 jobs this
// deployment would rather run first are queued ahead of it", "8 GiB
// free and this job asks for 16 GiB" -- and is a different string a
// minute later, so a job held by one thing for forty minutes would go
// down as a dozen unrelated short waits if the string were all there
// was. The cause does not move, so a job's wait can be attributed to
// whichever kind of thing held it longest (JobRun.QueuedReason).
//
// The zero Wait means "not waiting for anything any more", which is what
// a gate reports when it finally admits the job.
type Wait struct {
	Cause  WaitCause
	Reason string
}
