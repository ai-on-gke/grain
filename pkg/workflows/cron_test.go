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

// Translating a workflow cron into a task schedule's cadence, and refusing
// the ones that have no cadence at all (grain/task-344, cron.go).

import (
	"strings"
	"testing"
	"time"
)

func TestParseCronTranslatesTheShapesATaskScheduleCanExpress(t *testing.T) {
	for _, tc := range []struct {
		cron string
		want Recurrence
	}{
		// The real case: bwsalmon/grain's own nightly.
		{"11 2 * * *", Recurrence{Kind: RecurrenceDaily, TimeOfDay: 2*60 + 11}},
		{"0 0 * * *", Recurrence{Kind: RecurrenceDaily}},
		{"59 23 * * *", Recurrence{Kind: RecurrenceDaily, TimeOfDay: 23*60 + 59}},
		{"17 9 * * 1", Recurrence{Kind: RecurrenceWeekly, TimeOfDay: 9*60 + 17, Weekday: time.Monday}},
		{"17 9 * * MON", Recurrence{Kind: RecurrenceWeekly, TimeOfDay: 9*60 + 17, Weekday: time.Monday}},
		// cron says Sunday twice, and both mean Sunday.
		{"0 6 * * 0", Recurrence{Kind: RecurrenceWeekly, TimeOfDay: 6 * 60, Weekday: time.Sunday}},
		{"0 6 * * 7", Recurrence{Kind: RecurrenceWeekly, TimeOfDay: 6 * 60, Weekday: time.Sunday}},
		{"0 6 * * sat", Recurrence{Kind: RecurrenceWeekly, TimeOfDay: 6 * 60, Weekday: time.Saturday}},
		{"0 3 1 * *", Recurrence{Kind: RecurrenceMonthly, TimeOfDay: 3 * 60, DayOfMonth: 1}},
		{"0 3 31 * *", Recurrence{Kind: RecurrenceMonthly, TimeOfDay: 3 * 60, DayOfMonth: 31}},
		// Whitespace a human leaves behind.
		{"  11 2 * * *  ", Recurrence{Kind: RecurrenceDaily, TimeOfDay: 2*60 + 11}},
	} {
		got, err := ParseCron(tc.cron)
		if err != nil {
			t.Fatalf("%q: %v", tc.cron, err)
		}
		if got != tc.want {
			t.Fatalf("%q: %+v, want %+v", tc.cron, got, tc.want)
		}
		if err := got.Validate(); err != nil {
			t.Fatalf("%q: produced a cadence the store would refuse: %v", tc.cron, err)
		}
	}
}

// Every one of these is a cron that runs at times no cadence here can say.
// Refusing is the answer rather than rounding: a suite somebody believes
// runs every fifteen minutes and actually runs hourly is worse than one
// that does not run, because nothing about it looks wrong.
func TestParseCronRefusesWhatItCannotSayExactly(t *testing.T) {
	for _, cron := range []string{
		"*/15 * * * *",    // a step
		"0 */6 * * *",     // a step in the hour field
		"0 9 * * 1-5",     // a range
		"0 9 * * 1,4",     // a list
		"0,30 9 * * *",    // a list of minutes
		"11 * * * *",      // hourly: finer than daily
		"* 2 * * *",       // every minute of an hour
		"* * * * *",       // every minute
		"0 9 1 * 1",       // day-of-month and day-of-week: cron ORs them
		"0 9 1 6 *",       // only in June
		"@daily",          // a nickname, which GitHub does not take either
		"0 9 * *",         // four fields
		"0 9 * * * *",     // six fields
		"0 24 * * *",      // out of range
		"60 9 * * *",      // out of range
		"0 9 32 * *",      // out of range
		"0 9 * * 8",       // out of range
		"0 9 * * someday", // not a day
		"",
	} {
		if got, err := ParseCron(cron); err == nil {
			t.Fatalf("%q was accepted as %+v", cron, got)
		}
	}
}

// The refusal has to be usable: it names the key it is about and says what
// can be written instead, because somebody is reading it in a failed
// declaration sync with only the message to go on.
func TestTheCronRefusalNamesTheKeyAndTheCadences(t *testing.T) {
	_, err := ParseCron("*/15 * * * *")
	if err == nil {
		t.Fatal("accepted */15 * * * *")
	}
	for _, want := range []string{
		"schedule:", "cron:", `"*/15 * * * *"`,
		"every N hours", "daily", "weekly", "monthly",
		`"M H * * *"`, `"M H * * DOW"`, `"M H DOM * *"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal does not say %q: %v", want, err)
		}
	}
}

// The parse-time half of the same rule, through the file rather than the
// function: a native workflow whose cron cannot be expressed does not load,
// so no schedule is created from it and the repository is told why.
func TestAWorkflowWithAnInexpressibleCronDoesNotParse(t *testing.T) {
	_, err := Parse(WorkflowDir+"/often.yml",
		[]byte("on:\n  schedule:\n    - cron: '*/15 * * * *'\njobs:\n  test:\n    steps:\n      - run: true\n"))
	if err == nil {
		t.Fatal("a native workflow with an inexpressible cron parsed")
	}
	if !strings.Contains(err.Error(), "often.yml") || !strings.Contains(err.Error(), "*/15 * * * *") {
		t.Fatalf("error names neither the file nor the cron: %v", err)
	}
	// The same file borrowed from GitHub keeps parsing: that cron is
	// GitHub's scheduler's business and grain neither runs nor judges it.
	if _, err := Parse(GitHubWorkflowDir+"/often.yml", []byte(
		"on:\n  schedule:\n    - cron: '*/15 * * * *'\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: true\n",
	)); err != nil {
		t.Fatalf("a GitHub Actions cron was refused: %v", err)
	}
}

// The defect this feature fixes, stated as a test: a workflow whose only
// trigger is a cron used to plan no jobs for any event grain had, so its
// jobs could not run at all.
func TestAScheduledWorkflowPlansItsJobsForTheScheduleEvent(t *testing.T) {
	w, err := Parse(WorkflowDir+"/nightly.yml", []byte(
		"on:\n  schedule:\n    - cron: '11 2 * * *'\njobs:\n  long:\n    steps:\n      - run: make test-long\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(w.On.Schedule) != 1 || w.On.Schedule[0].Cron != "11 2 * * *" {
		t.Fatalf("triggers %+v", w.On.Schedule)
	}
	if !w.On.Matches(EventSchedule, "main") {
		t.Fatal("a scheduled workflow does not answer to the schedule event")
	}
	if w.On.Matches(EventPush, "main") || w.On.Matches(EventManual, "main") {
		t.Fatal("a cron is not a push and not a dispatch")
	}
	plan, err := PlanRun(Request{Repo: "bwsalmon/grain", Ref: "main", Event: EventSchedule}, []Workflow{w})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 1 || plan.Jobs[0].Job.ID != "long" {
		t.Fatalf("planned %d jobs, want the workflow's one", len(plan.Jobs))
	}
	// And a push does not drag a nightly in behind it.
	pushed, err := PlanRun(Request{Repo: "bwsalmon/grain", Ref: "main", Event: EventPush}, []Workflow{w})
	if err != nil {
		t.Fatal(err)
	}
	if len(pushed.Jobs) != 0 {
		t.Fatalf("a push planned %d jobs of a scheduled workflow", len(pushed.Jobs))
	}
}

func TestAScheduleRunIsAValidRequest(t *testing.T) {
	req := Request{Repo: "bwsalmon/grain", Ref: "main", Event: EventSchedule}
	if err := req.Validate(); err != nil {
		t.Fatalf("a schedule run is not a valid request: %v", err)
	}
}

func TestWorkflowScheduleWithExplicitTimeZone(t *testing.T) {
	w, err := Parse(WorkflowDir+"/nightly.yml", []byte(
		"on:\n  schedule:\n    - cron: '0 9 * * 1'\n      timezone: 'America/Los_Angeles'\njobs:\n  test:\n    steps:\n      - run: true\n"))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(w.On.Schedule) != 1 {
		t.Fatalf("len(Schedule) = %d, want 1", len(w.On.Schedule))
	}
	entry := w.On.Schedule[0]
	if entry.Cron != "0 9 * * 1" {
		t.Errorf("Cron = %q, want '0 9 * * 1'", entry.Cron)
	}
	if entry.TimeZone != "America/Los_Angeles" {
		t.Errorf("TimeZone = %q, want America/Los_Angeles", entry.TimeZone)
	}
	if entry.Recurrence.TimeZone != "America/Los_Angeles" {
		t.Errorf("Recurrence.TimeZone = %q, want America/Los_Angeles", entry.Recurrence.TimeZone)
	}
}

func TestWorkflowScheduleRejectsInvalidTimeZone(t *testing.T) {
	_, err := Parse(WorkflowDir+"/nightly.yml", []byte(
		"on:\n  schedule:\n    - cron: '0 9 * * 1'\n      timezone: 'Mars/Olympus_Mons'\njobs:\n  test:\n    steps:\n      - run: true\n"))
	if err == nil {
		t.Fatal("expected error for invalid timezone, got nil")
	}
}

// `review:` beside `timezone:` (grain/task-3160): when the schedule the
// cron becomes files a review of the run it starts.
func TestWorkflowScheduleCarriesItsReviewPolicy(t *testing.T) {
	w, err := Parse(WorkflowDir+"/nightly.yml", []byte(
		"on:\n  schedule:\n    - cron: '0 9 * * 1'\n      review: always\n    - cron: '0 9 * * 2'\njobs:\n  test:\n    steps:\n      - run: true\n"))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(w.On.Schedule) != 2 {
		t.Fatalf("len(Schedule) = %d, want 2", len(w.On.Schedule))
	}
	if got := w.On.Schedule[0].Review; got != ReviewAlways {
		t.Errorf("Review = %q, want always", got)
	}
	if got := w.On.Schedule[1].Review; got != "" {
		t.Errorf("Review = %q, want the default", got)
	}

	_, err = Parse(WorkflowDir+"/nightly.yml", []byte(
		"on:\n  schedule:\n    - cron: '0 9 * * 1'\n      review: never\njobs:\n  test:\n    steps:\n      - run: true\n"))
	if err == nil || !strings.Contains(err.Error(), "never") {
		t.Fatalf("err = %v, want a refusal naming the value", err)
	}
}
