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

package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// grain/task-288, widened by grain/task-65: `grain list` narrows and
// orders on the same attributes, with the same comparisons, the UI's own
// filter editor offers. These exercise taskQuery and the conditions it
// is built out of, which is all of that question -- cmdList around it
// only turns flags into one of these and prints what comes back.

func at(day int) *time.Time {
	t := time.Date(2026, 9, day, 12, 0, 0, 0, time.UTC)
	return &t
}

// listing is one of everything worth asking about: two repos and a task
// with none, two authors, a scheduled task, a merge fix, a live chat, a
// blocked task, one task that merges itself, and -- for the attributes
// that had no flag at all before -- a read-only repo, an extra write
// target, a pull request, a review, an agent override and a depends-on
// link.
func listing() []ui.Task {
	return []ui.Task{
		{
			ID: "1", Title: "Widget work", State: model.StateQueued, Repo: "acme/widgets", Base: "main",
			Author: "ada", Capabilities: []string{"gh"}, AutoMerge: true, CreatedAt: at(3),
			Writes:      []ui.WriteTarget{{Repo: "acme/widgets-state", Base: "deploy"}},
			PullRequest: "https://github.com/acme/widgets/pull/7", ReviewTemplateName: "Second pair of eyes",
		},
		{
			ID: "2", Title: "Gadget work", State: model.StateRunning, Repo: "acme/gadgets",
			Author: "grace", Capabilities: []string{"gcp", "gh"}, Scheduled: true, CreatedAt: at(1),
			Reads: []string{"acme/widgets"}, AgentFramework: "codex",
		},
		{
			ID: "3", Title: "Loose chat", State: model.StateProposed,
			Author: "ada", Interactive: true, CreatedAt: at(2),
		},
		{
			ID: "4", Title: "Repair the widget branch", State: model.StateQueued, Repo: "acme/widgets",
			Author: "grain", Stacked: true, GeneratedFrom: "1", Blocked: true, BlockedBy: []string{"1"},
			DependsOn: []string{"1"}, CreatedAt: at(4),
		},
	}
}

func ids(tasks []ui.Task) []string {
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.ID)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// asked builds the narrowing a list of written conditions describes,
// against a fixed "now" -- the fixture's own latest task plus an hour,
// so "within the last N days" means something a test can predict.
func asked(t *testing.T, filters ...string) taskQuery {
	t.Helper()
	q := taskQuery{now: time.Date(2026, 9, 4, 13, 0, 0, 0, time.UTC), zone: time.UTC}
	for _, raw := range filters {
		cond, err := builtinAttributes.parseCondition(raw)
		if err != nil {
			t.Fatalf("parseCondition(%q): %v", raw, err)
		}
		q.filters = append(q.filters, cond)
	}
	return q
}

func TestConditionsNarrowTheListing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		filters []string
		want    []string
	}{
		{name: "no question at all keeps everything", want: []string{"1", "2", "3", "4"}},
		{name: "state", filters: []string{"state:any:queued"}, want: []string{"1", "4"}},
		// The question a flag per attribute could not ask: either of
		// two values for one attribute.
		{name: "either of two states", filters: []string{"state:any:queued|running"}, want: []string{"1", "2", "4"}},
		{name: "either of two repos, comma-separated", filters: []string{"repo:any:acme/widgets,acme/gadgets"}, want: []string{"1", "2", "4"}},
		{name: "every state but one", filters: []string{"state:none:queued"}, want: []string{"2", "3"}},
		{name: "no repo at all", filters: []string{"repo:any:none"}, want: []string{"3"}},
		// The frontend's own spelling of the same value, so a condition
		// out of a link is pasteable unaltered.
		{name: "no repo at all, spelt as a link spells it", filters: []string{"repo:any:__none__"}, want: []string{"3"}},
		{name: "a repo or none of them", filters: []string{"repo:any:acme/gadgets|none"}, want: []string{"2", "3"}},
		{name: "empty is the same question", filters: []string{"repo:empty:"}, want: []string{"3"}},
		{name: "filled is its opposite", filters: []string{"repo:filled"}, want: []string{"1", "2", "4"}},
		{name: "base branch", filters: []string{"base:any:main"}, want: []string{"1"}},
		{name: "the repo's default base", filters: []string{"base:any:none"}, want: []string{"2", "3", "4"}},
		{name: "capability", filters: []string{"capability:any:gcp"}, want: []string{"2"}},
		{name: "no capabilities", filters: []string{"capability:any:none"}, want: []string{"3", "4"}},
		// "has all of" over a multi-valued attribute, which "is any of"
		// cannot say.
		{name: "every one of these capabilities", filters: []string{"capability:all:gh,gcp"}, want: []string{"2"}},
		{name: "author", filters: []string{"author:any:ada"}, want: []string{"1", "3"}},
		{name: "user is the author too", filters: []string{"user:any:ada"}, want: []string{"1", "3"}},
		{name: "part of a name", filters: []string{"author:has:AD"}, want: []string{"1", "3"}},
		{name: "part of nobody's name", filters: []string{"user:hasnot:gra"}, want: []string{"1", "3"}},
		{name: "filed by hand", filters: []string{"origin:any:hand"}, want: []string{"1", "3"}},
		{name: "filed by a schedule", filters: []string{"origin:any:schedule"}, want: []string{"2"}},
		// A merge fix is generated from another task, so it answers the
		// more specific of the two origins it could claim.
		{name: "filed by the merge queue", filters: []string{"origin:any:fix"}, want: []string{"4"}},
		{name: "nothing was proposed here", filters: []string{"origin:any:proposed"}, want: []string{}},
		{name: "blocked", filters: []string{"blocked:any:yes"}, want: []string{"4"}},
		{name: "not blocked", filters: []string{"blocked:any:no"}, want: []string{"1", "2", "3"}},
		{name: "merges itself", filters: []string{"autoMerge:any:on"}, want: []string{"1"}},
		{name: "needs a human", filters: []string{"autoMerge:any:off"}, want: []string{"2", "3", "4"}},
		{name: "a live chat", filters: []string{"kind:any:interactive"}, want: []string{"3"}},
		{name: "title mentions a word", filters: []string{"title:has:widget"}, want: []string{"1", "4"}},
		{name: "title mentions it, case-insensitively", filters: []string{"title:has:WIDGET"}, want: []string{"1", "4"}},
		{name: "title mentions nothing of the sort", filters: []string{"title:hasnot:work"}, want: []string{"3", "4"}},
		// The attributes that had no flag at all before.
		{name: "the repos a task only reads", filters: []string{"reads:any:acme/widgets"}, want: []string{"2"}},
		{name: "no read-only repos", filters: []string{"reads:empty"}, want: []string{"1", "3", "4"}},
		// A write target is a repo and a branch, and the attribute is
		// its repo -- which is also why it is not "repo": that stays the
		// one repo a task's own runs push to, so the two conditions
		// together are what "may land work in acme/widgets-state" is.
		{name: "the other repos a task's work may land in", filters: []string{"writes:any:acme/widgets-state"}, want: []string{"1"}},
		{name: "a write target is not the repo a task is filed against", filters: []string{"repo:any:acme/widgets-state"}, want: []string{}},
		{name: "no other writeable repos", filters: []string{"writes:empty"}, want: []string{"2", "3", "4"}},
		{name: "has a pull request", filters: []string{"pullRequest:filled"}, want: []string{"1"}},
		{name: "pull request number", filters: []string{"pullRequest:has:/pull/7"}, want: []string{"1"}},
		{name: "a review is attached", filters: []string{"review:any:Second pair of eyes"}, want: []string{"1"}},
		{name: "no review", filters: []string{"review:empty"}, want: []string{"2", "3", "4"}},
		{name: "an agent of its own", filters: []string{"framework:any:codex"}, want: []string{"2"}},
		{name: "waits on another task", filters: []string{"dependsOn:any:1"}, want: []string{"4"}},
		{name: "one task by id", filters: []string{"id:has:3"}, want: []string{"3"}},
		// Dates: the fixture runs 1 September to 4 September, and
		// "now" is the 4th at 13:00.
		{name: "created within the last two days", filters: []string{"created:within:2"}, want: []string{"1", "4"}},
		{name: "created after a day", filters: []string{"created:after:2026-09-03"}, want: []string{"1", "4"}},
		{name: "created before a day", filters: []string{"created:before:2026-09-03"}, want: []string{"2", "3"}},
		// Two conditions on one attribute, which one menu per attribute
		// could not hold: a range with no gap and no overlap.
		{
			name:    "created between two days",
			filters: []string{"created:after:2026-09-02", "created:before:2026-09-04"},
			want:    []string{"1", "3"},
		},
		{name: "conditions combine", filters: []string{"repo:any:acme/widgets", "author:any:ada"}, want: []string{"1"}},
		{name: "conditions that cannot both hold keep nothing", filters: []string{"repo:any:acme/widgets", "author:any:grace"}, want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := asked(t, tc.filters...).apply(listing())
			if !equal(ids(got), tc.want) {
				t.Fatalf("apply(%v) = %v, want %v", tc.filters, ids(got), tc.want)
			}
		})
	}
}

// The search box's own question, which is not an attribute: a task's
// title or its id.
func TestSearchLooksAtTitleAndID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		search string
		want   []string
	}{
		{search: "WIDGET", want: []string{"1", "4"}},
		{search: "2", want: []string{"2"}},
	} {
		got := taskQuery{search: tc.search}.apply(listing())
		if !equal(ids(got), tc.want) {
			t.Errorf("apply(-search %q) = %v, want %v", tc.search, ids(got), tc.want)
		}
	}
}

// The shorthand flags are the conditions they always stood for, so a
// script written against the flag-per-attribute vocabulary prints what
// it printed.
func TestShorthandFlagsAreConditions(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	for _, tc := range []struct {
		name  string
		flags listFlags
		want  []string
	}{
		{name: "state", flags: listFlags{state: "queued"}, want: []string{"1", "4"}},
		{name: "repo", flags: listFlags{repo: "acme/widgets"}, want: []string{"1", "4"}},
		{name: "no repo at all", flags: listFlags{repo: "none"}, want: []string{"3"}},
		{name: "base", flags: listFlags{base: "main"}, want: []string{"1"}},
		{name: "capability", flags: listFlags{capability: "gcp"}, want: []string{"2"}},
		{name: "no capabilities", flags: listFlags{capability: "none"}, want: []string{"3", "4"}},
		{name: "author", flags: listFlags{author: "ada"}, want: []string{"1", "3"}},
		{name: "origin", flags: listFlags{origin: "schedule"}, want: []string{"2"}},
		{name: "blocked", flags: listFlags{blocked: &yes}, want: []string{"4"}},
		{name: "not blocked", flags: listFlags{blocked: &no}, want: []string{"1", "2", "3"}},
		{name: "merges itself", flags: listFlags{autoMerge: &yes}, want: []string{"1"}},
		{name: "needs a human", flags: listFlags{autoMerge: &no}, want: []string{"2", "3", "4"}},
		{name: "a live chat", flags: listFlags{interactive: &yes}, want: []string{"3"}},
		{name: "a background task", flags: listFlags{interactive: &no}, want: []string{"1", "2", "4"}},
		{name: "search", flags: listFlags{search: "gadget"}, want: []string{"2"}},
		{name: "a shorthand and a condition together", flags: listFlags{repo: "acme/widgets", state: "queued"}, want: []string{"1", "4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := tc.flags.query(builtinAttributes)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if got := q.apply(listing()); !equal(ids(got), tc.want) {
				t.Fatalf("apply(%+v) = %v, want %v", tc.flags, ids(got), tc.want)
			}
		})
	}
}

func TestInteractiveTasksAreHiddenUntilKindIsAskedFor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		filters []string
		want    []string
	}{
		{name: "ordinary listing", want: []string{"1", "2", "4"}},
		{name: "interactive tasks requested", filters: []string{"kind:any:interactive"}, want: []string{"3"}},
		{name: "background tasks requested", filters: []string{"kind:any:background"}, want: []string{"1", "2", "4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := withDefaultTaskKind(asked(t, tc.filters...), builtinAttributes)
			if err != nil {
				t.Fatalf("default task kind: %v", err)
			}
			if got := ids(q.apply(listing())); !equal(got, tc.want) {
				t.Fatalf("apply(%v) = %v, want %v", tc.filters, got, tc.want)
			}
		})
	}
}

func TestSortKeysOrderTheListing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		sort string
		want []string
	}{
		// The backlog's own order is what the daemon served, so nothing
		// here reorders it.
		{name: "no keys leaves the served order alone", sort: "", want: []string{"1", "2", "3", "4"}},
		{name: "created, newest first", sort: "created:desc", want: []string{"4", "1", "3", "2"}},
		{name: "created, oldest first", sort: "created:asc", want: []string{"2", "3", "1", "4"}},
		{name: "a direction left off is ascending", sort: "created", want: []string{"2", "3", "1", "4"}},
		{name: "title", sort: "title:asc", want: []string{"2", "3", "4", "1"}},
		{name: "title, backwards", sort: "title:desc", want: []string{"1", "4", "3", "2"}},
		{name: "state, in the order a task lives through them", sort: "state:asc", want: []string{"3", "1", "4", "2"}},
		// The task with no repo sorts last rather than first, and stays
		// last when the order is reversed.
		{name: "repo, with the repo-less last", sort: "repo:asc", want: []string{"2", "1", "4", "3"}},
		{name: "repo backwards, with the repo-less still last", sort: "repo:desc", want: []string{"1", "4", "2", "3"}},
		{name: "author", sort: "author:asc", want: []string{"1", "3", "2", "4"}},
		{name: "task id, as a number", sort: "id:desc", want: []string{"4", "3", "2", "1"}},
		// A second key breaking the first one's ties, which one named
		// order could not say: the two queued tasks are ordered by age
		// inside their state.
		{name: "state, then newest first", sort: "state:asc,created:desc", want: []string{"3", "4", "1", "2"}},
		// The named orders this command took before there were keys.
		{name: "backlog", sort: "backlog", want: []string{"1", "2", "3", "4"}},
		// "manual" is the same order under the name a link gives it
		// (paths.js's LEGACY_SORTS), so a kept URL's sort clause is
		// pasteable here too.
		{name: "manual", sort: "manual", want: []string{"1", "2", "3", "4"}},
		{name: "newest", sort: "newest", want: []string{"4", "1", "3", "2"}},
		{name: "oldest", sort: "oldest", want: []string{"2", "3", "1", "4"}},
		{name: "title, named", sort: "title", want: []string{"2", "3", "4", "1"}},
		{name: "state, named", sort: "state", want: []string{"3", "1", "4", "2"}},
		{name: "repo, named", sort: "repo", want: []string{"2", "1", "4", "3"}},
		{name: "author, named", sort: "author", want: []string{"1", "3", "2", "4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := listFlags{sortBy: tc.sort}.query(builtinAttributes)
			if err != nil {
				t.Fatalf("query(-sort %q): %v", tc.sort, err)
			}
			if got := q.apply(listing()); !equal(ids(got), tc.want) {
				t.Fatalf("apply(-sort %q) = %v, want %v", tc.sort, ids(got), tc.want)
			}
		})
	}
}

// Ties keep the order the daemon served, so `-sort state:asc` is the
// backlog grouped by state rather than reshuffled inside each group.
func TestSortIsStable(t *testing.T) {
	t.Parallel()
	q, err := listFlags{sortBy: "state:asc"}.query(builtinAttributes)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// 1 and 4 are both queued, and 1 came first off the wire.
	if want := []string{"3", "1", "4", "2"}; !equal(ids(q.apply(listing())), want) {
		t.Fatalf("apply(-sort state:asc) = %v, want %v", ids(q.apply(listing())), want)
	}
}

// grain/task-284: a review is stacked on another task's branch the way
// the merge queue's own fix is, so both would answer to "fix" if
// taskOrigin read Stacked alone. The origin attribute has to tell them
// apart -- one is a second agent reading a change, the other is a repair
// of a red build.
func TestTaskOriginTellsAReviewFromAMergeFix(t *testing.T) {
	t.Parallel()
	tasks := []ui.Task{
		{ID: "1", Title: "Widget work", State: model.StateQueued},
		{ID: "2", Title: "Repair it", State: model.StateQueued, Stacked: true, GeneratedFrom: "1"},
		{ID: "3", Title: "Review it", State: model.StateQueued, Stacked: true, Review: true, GeneratedFrom: "1"},
	}
	for _, tc := range []struct {
		origin string
		want   []string
	}{
		{origin: "fix", want: []string{"2"}},
		{origin: "review", want: []string{"3"}},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			got := asked(t, "origin:any:"+tc.origin).apply(tasks)
			if !equal(ids(got), tc.want) {
				t.Fatalf("apply(origin:any:%s) = %v, want %v", tc.origin, ids(got), tc.want)
			}
		})
	}
}

// A word nobody can act on is answered with the words that work, rather
// than with an empty listing that reads like a deployment with no such
// tasks. Unlike the frontend -- which drops a clause it cannot answer so
// that a stale link still opens the list it names -- a typed flag is
// refused.
func TestConditionsRejectWhatNobodyCanActOn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
		says string
	}{
		{name: "an attribute nothing has", raw: "colour:any:blue", says: "unknown attribute"},
		{name: "a comparison nothing makes", raw: "repo:startswith:acme", says: "takes no comparison"},
		{name: "a comparison this type does not take", raw: "title:any:flake", says: "takes no comparison"},
		{name: "a value outside a fixed vocabulary", raw: "state:any:in_progress", says: "unknown state"},
		// The alias the UI's "Asked" stands for is one of the words that
		// work, so the refusal names it too (2026-10-28 walkthrough).
		{name: "a state the alias list names", raw: "state:any:waiting", says: "asked (awaiting_reply or awaiting_submit)"},
		{name: "an origin nobody files from", raw: "origin:any:robot", says: "unknown origin"},
		{name: "none of an attribute that always has one", raw: "state:any:none", says: "not one of its values"},
		{name: "a condition with no value", raw: "repo:any:", says: "needs a value"},
		{name: "a comparison given a value it does not take", raw: "repo:empty:acme/widgets", says: "takes no value"},
		{name: "a date that is not one", raw: "created:after:last tuesday", says: "takes a date"},
		{name: "days that are not a number", raw: "created:within:a week", says: "number of days"},
		{name: "no comparison at all", raw: "repo", says: "malformed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := builtinAttributes.parseCondition(tc.raw)
			if err == nil {
				t.Fatalf("parseCondition(%q) succeeded, want an error naming the words that work", tc.raw)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("parseCondition(%q) = %v, want it to say %q", tc.raw, err, tc.says)
			}
		})
	}
}

func TestShorthandFlagsRejectWhatNobodyCanActOn(t *testing.T) {
	t.Parallel()
	for _, f := range []listFlags{
		{state: "in_progress"},
		{origin: "robot"},
		{sortBy: "oldest-first"},
		{sortBy: "created:descending"},
		{sortBy: "colour:asc"},
	} {
		if _, err := f.query(builtinAttributes); err == nil {
			t.Errorf("query(%+v) succeeded, want an error naming the values that work", f)
		}
	}
}

// A state this build has never heard of sorts after the ones it knows,
// so a newer daemon's own vocabulary cannot take over the top of a
// listing an older CLI prints.
func TestUnknownStateSortsLast(t *testing.T) {
	t.Parallel()
	tasks := []ui.Task{
		{ID: "1", Title: "From the future", State: model.State("hibernating")},
		{ID: "2", Title: "Ordinary", State: model.StateCompleted},
	}
	q, err := listFlags{sortBy: "state:asc"}.query(builtinAttributes)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if want := []string{"2", "1"}; !equal(ids(q.apply(tasks)), want) {
		t.Fatalf("apply(-sort state:asc) = %v, want %v", ids(q.apply(tasks)), want)
	}
}

// A bare day is read in the deployment's own wall clock, the way the
// frontend reads one -- every timestamp either surface prints is in that
// zone, so a day named in a filter has to start when those rows say it
// does. The same condition against the same task is a different answer
// eight hours west.
func TestADayIsReadInTheDeploymentsZone(t *testing.T) {
	t.Parallel()
	// 03:00 UTC on the 4th is still the evening of the 3rd in Los
	// Angeles.
	created := time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC)
	tasks := []ui.Task{{ID: "1", Title: "Late one", State: model.StateQueued, CreatedAt: &created}}
	cond, err := builtinAttributes.parseCondition("created:after:2026-09-04")
	if err != nil {
		t.Fatalf("parseCondition: %v", err)
	}
	utc := taskQuery{filters: []condition{cond}, zone: time.UTC}
	if got := ids(utc.apply(tasks)); !equal(got, []string{"1"}) {
		t.Errorf("in UTC the task was created on the 4th, so apply() = %v, want [1]", got)
	}
	pacific := taskQuery{filters: []condition{cond}, zone: model.LoadLocation("America/Los_Angeles")}
	if got := ids(pacific.apply(tasks)); !equal(got, []string{}) {
		t.Errorf("in Los Angeles it was still the 3rd, so apply() = %v, want nothing", got)
	}
	if !utc.needsZone() {
		t.Error("a condition naming a bare day should ask the deployment which zone it keeps")
	}
	instant, err := builtinAttributes.parseCondition("created:after:2026-09-04T00:00:00Z")
	if err != nil {
		t.Fatalf("parseCondition: %v", err)
	}
	if (taskQuery{filters: []condition{instant}}).needsZone() {
		t.Error("a condition naming an instant answers itself, and should not cost a settings request")
	}
}

// A value carrying one of the characters that separate values survives
// the trip, escaped the way a link escapes it -- so a condition can be
// pasted between the two surfaces whatever it names.
func TestSeparatorsInAValueAreEscapedAsInALink(t *testing.T) {
	t.Parallel()
	tasks := []ui.Task{
		{ID: "1", Title: "Widgets, gadgets and other things", State: model.StateQueued},
		{ID: "2", Title: "Widgets and gadgets", State: model.StateQueued},
	}
	got := asked(t, "title:has:Widgets%2C gadgets").apply(tasks)
	if !equal(ids(got), []string{"1"}) {
		t.Fatalf("apply(title:has:Widgets%%2C gadgets) = %v, want [1]", ids(got))
	}
	// A comparison that takes one value takes it whole, separators and
	// all, so the escaping above is a courtesy rather than a
	// requirement: only a list of choices is ever cut.
	cond, err := builtinAttributes.parseCondition("title:has:Widgets, gadgets")
	if err != nil {
		t.Fatalf("parseCondition: %v", err)
	}
	if want := []string{"Widgets, gadgets"}; !equal(cond.values, want) {
		t.Fatalf("parseCondition(title:has:Widgets, gadgets) took %q, want %q", cond.values, want)
	}
	// And a condition given more values than its comparison can use is
	// refused rather than quietly answering on the first of them.
	if _, err := builtinAttributes.newCondition("title", "has", "a", "b"); err == nil {
		t.Error(`newCondition("title", "has", "a", "b") succeeded, want an error saying "contains" takes one value`)
	}
}

// The state has a column of its own in `grain list`'s rows and in the
// history `grain get` prints, and a column one character short of the
// longest state does not truncate anything -- it shifts every column
// after it one place right, on exactly the rows that hold that state.
// "awaiting_submit" is the long one, which is how a listing containing
// an unsubmitted pull request came to be ragged from that row down.
//
// The vocabulary is written out here rather than read off some list
// model exports, because it exports none; a state added there and not
// added here is the one thing this cannot catch, which is the same
// bargain pkg/model's own TestStateOf makes.
func TestTaskLineColumnsLineUpForEveryState(t *testing.T) {
	t.Parallel()
	states := []model.State{
		model.StateProposed, model.StateQueued, model.StateRunning,
		model.StateAwaitingReply, model.StateFailed, model.StateAwaitingSubmit,
		model.StateCompleted, model.StateDeferred,
		model.StateClosed,
	}
	want := -1
	for _, s := range states {
		line := taskLine(ui.Task{ID: "1", State: s, Repo: "acme/widgets", Title: "Widget work"})
		at := strings.Index(line, "acme/widgets")
		if at < 0 {
			t.Fatalf("taskLine(%s) = %q, which does not name the repo at all", s, line)
		}
		if want == -1 {
			want = at
			continue
		}
		if at != want {
			t.Errorf("taskLine(%s) starts the repo column at %d, not %d, so a listing holding this state is ragged:\n%s", s, at, want, line)
		}
	}
}

// "asked" is the one value the UI's State menu offers for both
// awaiting_reply and awaiting_submit, so a link carrying it filters the
// same way here: both states, and nothing else.
func TestStateFilterAskedSelectsBothAskedStates(t *testing.T) {
	t.Parallel()
	attrs := attributesFor(nil)
	cond, err := attrs.newCondition("state", "any", "asked")
	if err != nil {
		t.Fatalf("state:any:asked: %v", err)
	}
	now := time.Now()
	for state, want := range map[model.State]bool{
		model.StateAwaitingReply:  true,
		model.StateAwaitingSubmit: true,
		model.StateQueued:         false,
	} {
		if got := cond.match(ui.Task{State: state}, now, time.UTC); got != want {
			t.Errorf("state:any:asked on a %s task = %v, want %v", state, got, want)
		}
	}
	old, err := attrs.newCondition("state", "any", "awaiting_reply")
	if err != nil {
		t.Fatal(err)
	}
	if old.match(ui.Task{State: model.StateAwaitingSubmit}, now, time.UTC) {
		t.Error("state:any:awaiting_reply matched an awaiting_submit task")
	}
}
