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
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

// Ownership is a deployment's answer to "do I run this work" -- and it
// is three-valued on purpose, because the two things a bool cannot tell
// apart here are exactly the two that matter.
//
// A deployment that has never been asked about an item and a deployment
// that has decided the item is somebody else's are not the same state:
// the first follows whatever the repository's default says and moves
// with it, and the second is a decision that has to survive the default
// being changed. `RepoConfig.ReleaseWorkflowsDisabled` -- what this
// replaces -- could only say "off", spelled negatively so that its zero
// value meant "on", and so could never record that a person had
// deliberately turned something on.
type Ownership string

const (
	// OwnershipUnset is the zero value and means nobody has said.
	// At the repository level it reads as "this deployment runs this
	// repository's work": a deployment configured to work on a
	// repository at all is the one running it unless an operator says
	// another deployment has it. At the item level it means "follow the
	// repository's default".
	OwnershipUnset Ownership = ""
	// OwnershipOn is an explicit yes.
	OwnershipOn Ownership = "on"
	// OwnershipOff is an explicit no: this deployment does not run it,
	// whatever the level above it says.
	OwnershipOff Ownership = "off"
)

// ParseOwnership reads one of the three values back, rejecting anything
// else rather than guessing -- a stored "0" or "true" is a row somebody
// or something wrote by a route that does not exist, and quietly reading
// it as "off" is how publishing gets turned off by a typo.
func ParseOwnership(s string) (Ownership, error) {
	switch v := Ownership(strings.ToLower(strings.TrimSpace(s))); v {
	case OwnershipUnset, OwnershipOn, OwnershipOff:
		return v, nil
	default:
		return OwnershipUnset, fmt.Errorf("%q is not an ownership setting (%q, %q or empty)", s, OwnershipOn, OwnershipOff)
	}
}

// WorkItem is one piece of a repository's standing work that a
// deployment either runs or does not.
//
// A schedule's id is a work item. So is ReleaseItem, which is the whole
// of what makes release "one more item" rather than a switch of its own:
// the operator's direction is that a release *is* a kind of schedule,
// and this task did not unify the two, so what it owes instead is that
// ownership resolution never learns the difference. Unifying them later
// is then a rename of one constant and nothing else.
type WorkItem string

// ReleaseItem is the work item id under which a repository's release --
// the trusted publish grain starts after a merge -- is owned.
//
// It is a reserved name rather than a schedule id because there is no
// release schedule row to take an id from yet. When there is one, this
// constant becomes that row's id and every override written under this
// name is migrated to it; nothing else here has to change, because
// nothing else here knows what a release is.
const ReleaseItem WorkItem = "release"

// The rest of the reserved items: the standing work grain does for a
// repository that no schedule row describes (grain/task-3332). Each is
// one item rather than all of them being the repository default, so an
// operator who turns a repository's standing work off can turn one piece
// of it back on -- and so the default is what every one of them follows
// until somebody does.
const (
	// BaseBranchCIItem is the base-branch check: the untrusted push run
	// grain starts on every new tip of a branch it merges into, its
	// retries and confirmations, and the verdict the merge queue reads
	// off it. Off, grain starts none of it and every reader of the
	// branch's row treats it as no verdict expected. The deployment's
	// own configuration repository (grainci.RequiredFor) is judged
	// whatever this says.
	BaseBranchCIItem WorkItem = "base-ci"
	// PostMergeItem is the jobs grain dispatches after a merge that are
	// not a verdict -- RepoConfig.PostMergeDispatchJobs and the guest
	// cache job grain dispatches unasked (EffectivePostMergeDispatchJobs).
	PostMergeItem WorkItem = "post-merge"
	// PortsItem is the automatic port of a merged change to the branch
	// its base is paired with ("Port … to <branch>").
	PortsItem WorkItem = "ports"
	// TriageItem is the tasks grain files on its own about a repository's
	// standing work going wrong: release triage ("Fix base branch release
	// …") and base-branch repair ("Fix grain CI on …").
	TriageItem WorkItem = "triage"
)

// StandingWorkItems is every reserved item, in the order an operator
// surface lists them. Schedules are items too, but each has an id of its
// own (ScheduleItem) and is listed from the schedules that exist.
var StandingWorkItems = []WorkItem{ReleaseItem, BaseBranchCIItem, PostMergeItem, PortsItem, TriageItem}

// StandingWorkOffReason is the sentence every refusal under a "no" from
// RunsWorkItem is written in -- a skipped schedule's status, a log line,
// a reconciler's refusal -- so the one fact reads the same wherever an
// operator meets it.
func StandingWorkOffReason(repo RepoRef) string {
	return fmt.Sprintf("this deployment doesn't run %s's standing work", repo)
}

// ScheduleItem is a schedule's ownership item id. It is the identity
// function today and exists so that every caller spells the conversion
// the same way, and so that the one place it would have to change if
// schedule ids ever needed qualifying is this line.
func ScheduleItem(scheduleID string) WorkItem { return WorkItem(scheduleID) }

// ValidateWorkItem rejects an item id that could not survive the storage
// encoding (EncodeWorkOverrides), and an empty one.
func ValidateWorkItem(item WorkItem) error {
	s := string(item)
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("a work item id cannot be empty")
	}
	if s != strings.TrimSpace(s) {
		return fmt.Errorf("work item %q has leading or trailing space", s)
	}
	if strings.ContainsAny(s, ",=") {
		return fmt.Errorf("work item %q holds ',' or '=', which the override list separates on", s)
	}
	return nil
}

// WorkOwnership is the whole of what a deployment says about a
// repository's standing work: one default, plus the exceptions to it.
//
// This is the deployment's half of the split this type exists for
// (grain/task-486). Everything about what a repository *is* and what it
// *needs* -- its settings, its templates, its suites, its schedules, its
// workflows and the capabilities those workflows require -- belongs in
// the repository, declared in its own `.grain` directory. What stays
// here is the one question a repository cannot answer about itself,
// because the answer is about this deployment and not about the
// repository: does this deployment run that work, or is another
// deployment already running it?
//
// # Two levels, and why both exist from the start
//
// Default is the "I own this repository" bit, and Overrides is the
// per-item exception. The overrides are expected to be empty on very
// nearly every repository -- and the level exists anyway, because the
// alternative was shipping one bool that callers read directly, and
// every caller that read it would then have to be found and changed on
// the day the first exception was wanted. Resolution is RunsWorkItem,
// which is one function, and nothing outside this file reads either
// field to decide whether work runs.
type WorkOwnership struct {
	// Default is this deployment's answer for the repository as a whole.
	Default Ownership
	// Overrides is the per-item exception, keyed by work item id. An
	// item that is absent follows Default; an item mapped to
	// OwnershipUnset is the same thing said out loud, which is what
	// makes "nobody decided" and "somebody decided no" two states a
	// reader can tell apart rather than one.
	Overrides map[WorkItem]Ownership
}

// ItemOwnership is a record holding one decision about one item and
// nothing else -- the shape very nearly every repository with an
// exception at all is in, and the one a caller writing a single
// override reaches for.
func ItemOwnership(item WorkItem, state Ownership) WorkOwnership {
	return WorkOwnership{}.With(item, state)
}

// Empty reports whether this says nothing at all -- the state every
// repository is in until somebody decides otherwise, and the state
// RepoConfig.Empty folds into its own answer.
func (o WorkOwnership) Empty() bool {
	if o.Default != OwnershipUnset {
		return false
	}
	for _, v := range o.Overrides {
		if v != OwnershipUnset {
			return false
		}
	}
	return true
}

// Item reports what has been said about one item, and whether anything
// has been said about it at all. It is the accessor for a reader that
// needs the difference -- an operator surface drawing "follows the
// default" differently from "off" -- and never the one that decides
// whether work runs, which is RunsWorkItem's job alone.
func (o WorkOwnership) Item(item WorkItem) (Ownership, bool) {
	v, ok := o.Overrides[item]
	if !ok || v == OwnershipUnset {
		return OwnershipUnset, false
	}
	return v, true
}

// With returns a copy of o with item set to state, which is how a caller
// records a decision without mutating a map it does not own. Setting
// OwnershipUnset removes the override, so "follow the default" is
// reachable from the same call that set an exception.
func (o WorkOwnership) With(item WorkItem, state Ownership) WorkOwnership {
	out := WorkOwnership{Default: o.Default, Overrides: maps.Clone(o.Overrides)}
	if state == OwnershipUnset {
		delete(out.Overrides, item)
		if len(out.Overrides) == 0 {
			out.Overrides = nil
		}
		return out
	}
	if out.Overrides == nil {
		out.Overrides = map[WorkItem]Ownership{}
	}
	out.Overrides[item] = state
	return out
}

// runs resolves one item against this record: the override where there
// is one, the repository default otherwise, and "yes" where neither has
// been set.
//
// Unset resolving to "yes" is the compatibility the change had to keep,
// and it is also the honest default: a deployment that has been
// configured to work on a repository is the deployment running that
// repository's work, and one that is not is not reading the repository
// at all. Flipping it to opt-in would have stopped every release and
// every schedule on every existing deployment at the moment of upgrade,
// which is precisely the outage this whole design is a response to.
func (o WorkOwnership) runs(item WorkItem) bool {
	if v, ok := o.Item(item); ok {
		return v == OwnershipOn
	}
	return o.RunsByDefault()
}

// RunsByDefault is what an item nobody has said anything about resolves
// to: the repository default alone, with no override applied.
//
// It is here, beside runs, because it is a resolution and resolution
// lives in this file. An operator surface drawing the default's three
// states still has to say which of them means "this deployment runs this
// repository's work" -- and a pane that worked that out from Default
// would be a second copy of the rule, in JavaScript, free to drift from
// the one line above that decides it.
func (o WorkOwnership) RunsByDefault() bool { return o.Default != OwnershipOff }

// RunsWorkItem is the one function that answers "does this deployment
// run item of this repository".
//
// Every caller goes through it -- the release reconciler, the guest
// cache reading, the repository pane -- and nothing reads
// WorkOwnership's fields to decide. That is the whole reason it takes a
// *RepoConfig rather than hanging off one: a repository with no row of
// its own is the common case, and a nil-safe package function means no
// caller has to spell the nil check, and so no caller can spell it
// differently.
func RunsWorkItem(cfg *RepoConfig, item WorkItem) bool {
	if cfg == nil {
		return WorkOwnership{}.runs(item)
	}
	return cfg.Work.runs(item)
}

// EncodeWorkOverrides renders the override map as the row stores it:
// `item=on` pairs, comma-separated, sorted by item so the same record
// writes the same string every time -- which is what keeps a settings
// export a diff of decisions rather than of map iteration order.
func EncodeWorkOverrides(overrides map[WorkItem]Ownership) string {
	if len(overrides) == 0 {
		return ""
	}
	items := make([]string, 0, len(overrides))
	for item, state := range overrides {
		if state == OwnershipUnset || ValidateWorkItem(item) != nil {
			continue
		}
		items = append(items, string(item)+"="+string(state))
	}
	sort.Strings(items)
	return strings.Join(items, ",")
}

// ParseWorkOverrides reads EncodeWorkOverrides back.
//
// A malformed entry is dropped rather than failing the read of the whole
// row: this is a stored value, the row it is on carries a dozen other
// settings, and refusing to load a repository's configuration because
// one override is unreadable would take the repository out of service
// over the least important field on it. A dropped entry falls back to
// the repository default, which is the same thing an override that was
// never written does.
func ParseWorkOverrides(s string) map[WorkItem]Ownership {
	var out map[WorkItem]Ownership
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		item := WorkItem(strings.TrimSpace(name))
		if ValidateWorkItem(item) != nil {
			continue
		}
		state, err := ParseOwnership(value)
		if err != nil || state == OwnershipUnset {
			continue
		}
		if out == nil {
			out = map[WorkItem]Ownership{}
		}
		out[item] = state
	}
	return out
}

// OverriddenItems is every item this record says something explicit
// about, sorted -- what an operator surface lists and what a test
// asserts on.
func (o WorkOwnership) OverriddenItems() []WorkItem {
	items := make([]WorkItem, 0, len(o.Overrides))
	for item, state := range o.Overrides {
		if state != OwnershipUnset {
			items = append(items, item)
		}
	}
	slices.Sort(items)
	return items
}
