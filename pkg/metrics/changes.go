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

package metrics

// When this deployment's own parameters changed, as a series to line the
// rest of the report up against.
//
// Every other section here measures what the deployment did. This one
// measures what was done *to* it, and it is what makes the others worth
// keeping: a tuning agent's whole job is "we changed X, did Y improve",
// and until this there was nothing anywhere that joined a settings change
// to the numbers around it. An operator dropping max_ci_jobs from 10 to 7
// on a Sunday evening left a record in exactly one place -- a commit in
// the state repository -- and nothing read it (grain/task-495).
//
// It keeps this package's rules. Nothing is stored: the events are read
// out of a history that already exists, on demand, the same way every
// number here is derived from rows that already exist
// (staterepo.SettingsChanges). It is windowed on the moment the change
// happened, which for an event with no duration is the only moment it
// has. And what was never recorded is skipped: a deployment with no state
// repository to read has Available false rather than an empty series that
// reads as "nothing has ever been changed".
//
// # What it is for, concretely
//
// Two things, and both are joins the consumer makes rather than anything
// computed here.
//
// Buckets is the series: how many parameters moved during each slice of
// the window, on the same slices Throughput and CIQueueing are sliced
// into, so a reader looking at a run-time series that steps down halfway
// across the window can see in the same picture that something was
// changed halfway across the window. That is the correlation the nightly
// job exists to make, and the alignment is the whole of what makes it
// possible.
//
// Events is the detail behind it, newest first: which file, which key,
// what it went from and to where the value is a dial
// (staterepo.SettingsField on why a string value is named and not
// quoted). A tuning agent reads the newest few of these, then asks for
// two windows either side of the one it cares about and compares them --
// which is the only way to answer "did it help" with this deployment's
// own numbers, and is now possible at all.

import (
	"sort"
	"time"
)

// SettingsChange is one commit that changed something a setting lives in.
// It mirrors staterepo.SettingsChange field for field; the caller converts,
// so that this package keeps measuring what it is handed and stays out of
// the business of reading a git history (see Input.SettingsChanges).
type SettingsChange struct {
	At      time.Time
	Commit  string
	Subject string
	Paths   []string
	Fields  []SettingsField
	// Truncated is whether the change moved more fields than were read --
	// a commit that rewrote a whole row rather than turned a dial.
	Truncated bool
}

// SettingsField is one setting a change moved. From and To are empty for a
// value that is not a number or a boolean, which is a redaction rather
// than a gap.
type SettingsField struct {
	Path string
	Name string
	From string
	To   string
}

// MaxChangeEvents is how many of the window's changes a report carries in
// full. The series counts all of them; this bounds the detail, because the
// report is read inside a bounded answer and the changes worth reading
// about are the recent ones. A window with more says so (Changes.Dropped).
const MaxChangeEvents = 25

// Changes is what moved in this deployment's settings during the window.
type Changes struct {
	// Available is whether there was a history to read at all. False is "no
	// state repository to ask", which is a different answer from a
	// deployment nobody has changed anything on, and the two must not read
	// the same.
	Available bool
	// N is how many settings changes fell in the window, and Fields how
	// many individual parameters they moved between them.
	N      int
	Fields int
	// Events is the newest MaxChangeEvents of them, newest first, and
	// Dropped how many older ones the window held and this does not carry.
	Events  []SettingsChange
	Dropped int
	// Reaches is the oldest change the reader got to, over however much
	// history it was given rather than over the window: a window asking
	// for ninety days of a repository seeded last week reaches last week,
	// and the difference between "nothing changed" and "nothing is
	// recorded" is this field. nil when no change was read at all.
	Reaches *time.Time
	// Buckets is how many changes landed in each slice of the window,
	// oldest first -- the series the other sections' series are read
	// against.
	Buckets []ChangeBucket
}

// ChangeBucket is one slice of the window: how many parameters moved
// during it.
type ChangeBucket struct {
	Since time.Time
	Until time.Time
	// Changes is commits and Fields is the parameters inside them, which
	// are different questions: one operator turning three dials in one
	// commit is one change and three fields.
	Changes int
	Fields  int
}

// changesOf builds the section from the events the caller read.
//
// available says whether there was a history to read, and is a separate
// argument for the reason ui passes Cycles' own the same way: this cannot
// tell "nothing to ask" from "nothing happened", and those are different
// answers to give an operator.
func changesOf(w Window, buckets []Bucket, available bool, events []SettingsChange) Changes {
	out := Changes{Available: available}
	if len(buckets) > 0 {
		out.Buckets = make([]ChangeBucket, len(buckets))
		for i, b := range buckets {
			out.Buckets[i].Since, out.Buckets[i].Until = b.Since, b.Until
		}
	}
	kept := make([]SettingsChange, 0, len(events))
	for _, e := range events {
		at := e.At
		if out.Reaches == nil || at.Before(*out.Reaches) {
			// Over everything the reader got to, window or no window: this
			// is how far back the *history* goes, which is what says
			// whether an empty window means anything.
			oldest := at
			out.Reaches = &oldest
		}
		if !w.holds(&at) {
			continue
		}
		out.N++
		out.Fields += len(e.Fields)
		kept = append(kept, e)
		if i := bucketOf(buckets, w, &at); i >= 0 {
			out.Buckets[i].Changes++
			out.Buckets[i].Fields += len(e.Fields)
		}
	}
	// Newest first, whatever order the reader handed them over in: a
	// reader of a bounded list wants the changes nearest the numbers it is
	// looking at, and ties broken by commit so the same report computed
	// twice is the same report.
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].At.Equal(kept[j].At) {
			return kept[i].Commit < kept[j].Commit
		}
		return kept[i].At.After(kept[j].At)
	})
	if len(kept) > MaxChangeEvents {
		out.Dropped = len(kept) - MaxChangeEvents
		kept = kept[:MaxChangeEvents]
	}
	out.Events = kept
	return out
}
