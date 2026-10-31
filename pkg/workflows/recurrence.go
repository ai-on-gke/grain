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
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	// The IANA zone database, so that a schedule's zone resolves the same
	// on a host or image without /usr/share/zoneinfo (pkg/model/timezone.go
	// carries the same import for the same reason).
	_ "time/tzdata"
)

// The schedule types a workflow's `on: schedule:` translates into
// (cron.go). They live here rather than in pkg/model so the parser stays
// neutral (docs/design/grit-split.md, "Neutral packages"); pkg/model
// re-exports them under their old names for grain's scheduler.

// RecurrenceKind is which of the cadences a Recurrence expresses --
// bwsalmon/agents#464's widening of the schedule feature past "every N
// hours" into the options a human setting up a cron job already expects.
type RecurrenceKind string

const (
	// RecurrenceEveryNHours fires every EveryNHours hours, counted from
	// whenever it last fired (or was created) -- no wall-clock alignment,
	// the original (and still simplest) cadence.
	RecurrenceEveryNHours RecurrenceKind = "everyNHours"
	// RecurrenceEveryNMinutes fires every EveryNMinutes minutes, counted
	// the same way RecurrenceEveryNHours is: from whenever it last fired,
	// with no wall-clock alignment. It exists for the jobs an hour is too
	// coarse for -- a release manager that wakes every half hour -- and
	// has a floor (MinEveryNMinutes) so it cannot become a sandbox a
	// minute.
	RecurrenceEveryNMinutes RecurrenceKind = "everyNMinutes"
	// RecurrenceDaily fires once a day, at TimeOfDay.
	RecurrenceDaily RecurrenceKind = "daily"
	// RecurrenceWeekly fires once a week, on Weekday at TimeOfDay.
	RecurrenceWeekly RecurrenceKind = "weekly"
	// RecurrenceMonthly fires once a month, on DayOfMonth at TimeOfDay --
	// clamped to a shorter month's last day, so DayOfMonth 31 reads as
	// much as "the last day of every month" as it does "the 31st".
	RecurrenceMonthly RecurrenceKind = "monthly"
	// RecurrenceGitHubWorkflowRun fires when a GitHub Actions workflow run
	// concludes matching GitHubWorkflowRunTrigger.
	RecurrenceGitHubWorkflowRun RecurrenceKind = "github_workflow_run"
)

// GitHubWorkflowRunTrigger configures a schedule that fires when a GitHub
// Actions workflow run concludes.
type GitHubWorkflowRunTrigger struct {
	Workflow    string   `json:"workflow" yaml:"workflow"`
	Conclusions []string `json:"conclusions,omitempty" yaml:"conclusions,omitempty"`
	Branches    []string `json:"branches,omitempty" yaml:"branches,omitempty"`
}

// WorkflowStem returns the stem of t.Workflow (e.g. "nightly" from
// ".github/workflows/nightly.yml" or "nightly.yml").
func (t GitHubWorkflowRunTrigger) WorkflowStem() string {
	clean := path.Base(strings.TrimSpace(t.Workflow))
	ext := path.Ext(clean)
	return strings.TrimSuffix(clean, ext)
}

// MatchesWorkflow reports whether workflowName or workflowPath matches t.Workflow.
func (t GitHubWorkflowRunTrigger) MatchesWorkflow(workflowName, workflowPath string) bool {
	want := strings.TrimSpace(t.Workflow)
	if want == "" {
		return false
	}
	wantClean := strings.TrimPrefix(path.Clean(want), ".github/workflows/")
	if strings.EqualFold(workflowName, want) || strings.EqualFold(workflowPath, want) {
		return true
	}
	if workflowPath != "" {
		pathClean := strings.TrimPrefix(path.Clean(workflowPath), ".github/workflows/")
		if strings.EqualFold(pathClean, wantClean) || strings.EqualFold(path.Base(pathClean), wantClean) {
			return true
		}
		stem := strings.TrimSuffix(path.Base(pathClean), path.Ext(pathClean))
		if strings.EqualFold(stem, wantClean) {
			return true
		}
	}
	wantStem := strings.TrimSuffix(path.Base(wantClean), path.Ext(wantClean))
	return strings.EqualFold(workflowName, wantStem)
}

// MatchesConclusion reports whether conclusion matches t.Conclusions.
// When Conclusions is empty, any non-empty conclusion matches.
func (t GitHubWorkflowRunTrigger) MatchesConclusion(conclusion string) bool {
	c := strings.ToLower(strings.TrimSpace(conclusion))
	if c == "" {
		return false
	}
	if len(t.Conclusions) == 0 {
		return true
	}
	for _, want := range t.Conclusions {
		if strings.ToLower(strings.TrimSpace(want)) == c {
			return true
		}
	}
	return false
}

// MatchesBranch reports whether branch matches t.Branches (or fallbackBase when
// Branches is empty). When both Branches and fallbackBase are empty, any branch matches.
func (t GitHubWorkflowRunTrigger) MatchesBranch(branch, fallbackBase string) bool {
	b := strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
	if len(t.Branches) == 0 {
		fb := strings.TrimPrefix(strings.TrimSpace(fallbackBase), "refs/heads/")
		if fb == "" {
			return true
		}
		return b == fb
	}
	for _, want := range t.Branches {
		w := strings.TrimPrefix(strings.TrimSpace(want), "refs/heads/")
		if w == b {
			return true
		}
	}
	return false
}

// Recurrence is a schedule's cadence. TimeOfDay, Weekday and DayOfMonth
// are read against the deployment's own wall clock -- the zone
// Config.TimeZone names, handed to Next as a *time.Location by whoever
// fires the schedule (orchestrator.reconcileSchedule). They were always
// read against UTC until grain/task-368, which is docs/schedules.md's
// "no per-schedule timezone" limit taken one step too far: there is
// still no zone *per schedule*, and deliberately so, but a deployment
// whose operator is in California should not have to write 17:00 to mean
// nine in the morning.
type Recurrence struct {
	Kind RecurrenceKind

	// EveryNHours is how many hours between firings -- only set when Kind
	// is RecurrenceEveryNHours. Hours, not an arbitrary duration: v1's
	// own Interval-Hours header (docs/schedules.md) already put the
	// cadence a human actually asks for ("every so often") at this
	// granularity, and it is what bwsalmon/agents#464 itself asks for.
	EveryNHours int

	// EveryNMinutes is how many minutes between firings -- only set when
	// Kind is RecurrenceEveryNMinutes, and never below MinEveryNMinutes.
	EveryNMinutes int

	// TimeOfDay is minutes since midnight (0-1439) on the deployment's
	// own wall clock -- only set when Kind is RecurrenceDaily,
	// RecurrenceWeekly or RecurrenceMonthly.
	TimeOfDay int

	// Weekday is only set when Kind is RecurrenceWeekly.
	Weekday time.Weekday

	// DayOfMonth is 1-31 -- only set when Kind is RecurrenceMonthly.
	DayOfMonth int

	// TimeZone is an optional IANA time zone name (e.g. "America/New_York").
	// When empty, cadences are read against UTC.
	TimeZone string

	// GitHubWorkflowRun is set when Kind is RecurrenceGitHubWorkflowRun.
	GitHubWorkflowRun *GitHubWorkflowRunTrigger
}

// StoredKind encodes r.Kind (and GitHubWorkflowRun trigger details when
// Kind is RecurrenceGitHubWorkflowRun) into the schedule.recurrence_kind
// column so no schema migration is required.
func (r Recurrence) StoredKind() string {
	if r.Kind == RecurrenceGitHubWorkflowRun && r.GitHubWorkflowRun != nil {
		b, err := json.Marshal(r.GitHubWorkflowRun)
		if err == nil {
			return string(RecurrenceGitHubWorkflowRun) + ":" + string(b)
		}
	}
	return string(r.Kind)
}

// ParseStoredRecurrenceKind decodes a schedule.recurrence_kind column value
// written by Recurrence.StoredKind.
func ParseStoredRecurrenceKind(raw string) (RecurrenceKind, *GitHubWorkflowRunTrigger) {
	prefix := string(RecurrenceGitHubWorkflowRun) + ":"
	if strings.HasPrefix(raw, prefix) {
		var trig GitHubWorkflowRunTrigger
		if err := json.Unmarshal([]byte(strings.TrimPrefix(raw, prefix)), &trig); err == nil {
			return RecurrenceGitHubWorkflowRun, &trig
		}
	}
	return RecurrenceKind(raw), nil
}

// MaxEveryNHours is the longest interval an everyNHours recurrence may
// ask for: a (leap) year. Anything rarer than that is not a cadence
// anyone schedules, and the cap keeps the interval far inside what
// time.Duration can hold (about 2,562,047 hours) -- past that, EveryNHours
// hours wraps around to an unrelated, often negative, duration.
const MaxEveryNHours = 366 * 24

// MinEveryNMinutes is the shortest interval an everyNMinutes recurrence
// may ask for. Every firing is an agent run in a sandbox of its own, and
// the orchestrator only looks at schedules once a cycle, so a cadence
// much finer than this buys nothing but cost: ten minutes is the floor.
const MinEveryNMinutes = 10

// MaxEveryNMinutes is the longest interval an everyNMinutes recurrence
// may ask for: the same year MaxEveryNHours allows, in minutes. A cadence
// that long is better written in hours, but there is no reason to refuse
// it, and the cap keeps the duration far inside what time.Duration holds.
const MaxEveryNMinutes = MaxEveryNHours * 60

// Location resolves r.TimeZone to a *time.Location, defaulting to UTC.
func (r Recurrence) Location() *time.Location {
	return loadLocation(r.TimeZone)
}

// Validate reports whether r is a recognized Kind with its fields in
// range for it -- checked once at creation/update (ui.parseRecurrence) so
// Next never has to handle a Kind it does not know or an out-of-range
// field.
func (r Recurrence) Validate() error {
	if r.TimeZone != "" && !ValidTimeZone(r.TimeZone) {
		return fmt.Errorf("invalid timezone %q", r.TimeZone)
	}
	switch r.Kind {
	case RecurrenceEveryNHours:
		if r.EveryNHours <= 0 {
			return fmt.Errorf("everyNHours must be positive")
		}
		if r.EveryNHours > MaxEveryNHours {
			return fmt.Errorf("everyNHours must be at most %d (a year)", MaxEveryNHours)
		}
	case RecurrenceEveryNMinutes:
		if r.EveryNMinutes < MinEveryNMinutes {
			return fmt.Errorf("everyNMinutes must be at least %d", MinEveryNMinutes)
		}
		if r.EveryNMinutes > MaxEveryNMinutes {
			return fmt.Errorf("everyNMinutes must be at most %d (a year)", MaxEveryNMinutes)
		}
	case RecurrenceDaily:
		return validTimeOfDay(r.TimeOfDay)
	case RecurrenceWeekly:
		if err := validTimeOfDay(r.TimeOfDay); err != nil {
			return err
		}
		if r.Weekday < time.Sunday || r.Weekday > time.Saturday {
			return fmt.Errorf("weekday out of range")
		}
	case RecurrenceMonthly:
		if err := validTimeOfDay(r.TimeOfDay); err != nil {
			return err
		}
		if r.DayOfMonth < 1 || r.DayOfMonth > 31 {
			return fmt.Errorf("dayOfMonth must be between 1 and 31")
		}
	case RecurrenceGitHubWorkflowRun:
		if r.GitHubWorkflowRun == nil || strings.TrimSpace(r.GitHubWorkflowRun.Workflow) == "" {
			return fmt.Errorf("github_workflow_run trigger needs a workflow")
		}
		for _, c := range r.GitHubWorkflowRun.Conclusions {
			if strings.TrimSpace(c) == "" {
				return fmt.Errorf("github_workflow_run conclusions cannot contain empty strings")
			}
		}
		for _, b := range r.GitHubWorkflowRun.Branches {
			if strings.TrimSpace(b) == "" {
				return fmt.Errorf("github_workflow_run branches cannot contain empty strings")
			}
		}
	default:
		return fmt.Errorf("unknown recurrence kind %q", r.Kind)
	}
	return nil
}

func validTimeOfDay(minutes int) error {
	if minutes < 0 || minutes >= 24*60 {
		return fmt.Errorf("timeOfDay must be between 0 and 1439 minutes")
	}
	return nil
}

// Next returns the first time this recurrence comes due strictly after
// after. fireTaskSchedule calls this in a loop from a schedule's own
// NextRunAt (never from "now" directly) so a schedule paused, or a daemon
// down, through several missed occurrences still fires exactly once on
// resume and resyncs to its normal cadence -- Next itself only has to
// answer "what's the one after this", the same contract the old
// "NextRunAt.Add(Interval)" loop already had, generalized to cadences a
// fixed duration cannot express (a calendar month is not a fixed number
// of hours).
//
// loc is the wall clock the three calendar cadences are read against --
// the deployment's own Config.Location(). Nil is UTC, which is what all
// three meant before a deployment had a zone at all, so a caller with no
// configuration to hand still gets a defined answer.
// RecurrenceEveryNHours and RecurrenceEveryNMinutes ignore it entirely:
// adding N hours (or minutes) to an instant lands on the same instant in every zone, daylight saving
// included, which is exactly the difference between "every 24 hours" and
// "daily at 09:00".
func (r Recurrence) Next(after time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = r.Location()
	}
	after = after.In(loc)
	switch r.Kind {
	case RecurrenceDaily:
		return nextDaily(after, r.TimeOfDay, loc)
	case RecurrenceWeekly:
		return nextWeekly(after, r.Weekday, r.TimeOfDay, loc)
	case RecurrenceMonthly:
		return nextMonthly(after, r.DayOfMonth, r.TimeOfDay, loc)
	case RecurrenceEveryNHours:
		// Validate keeps EveryNHours in range; a stored value that is not
		// (written before the cap existed) is clamped rather than
		// multiplied out, since that overflows time.Duration and can
		// wrap round to something that fires far more often than asked.
		hours := r.EveryNHours
		if hours <= 0 {
			hours = 1
		}
		if hours > MaxEveryNHours {
			hours = MaxEveryNHours
		}
		return after.Add(time.Duration(hours) * time.Hour)
	case RecurrenceEveryNMinutes:
		// Clamped for the same reason as EveryNHours, and to the floor as
		// well: a row that came in below it (a hand-edited state
		// repository, say) fires every ten minutes, not every minute.
		minutes := r.EveryNMinutes
		if minutes < MinEveryNMinutes {
			minutes = MinEveryNMinutes
		}
		if minutes > MaxEveryNMinutes {
			minutes = MaxEveryNMinutes
		}
		return after.Add(time.Duration(minutes) * time.Minute)
	case RecurrenceGitHubWorkflowRun:
		// Event-triggered schedules do not fire on a wall-clock cadence.
		return after.Add(time.Duration(MaxEveryNHours) * time.Hour)
	default:
		// Validate is expected to have already rejected this -- an hour
		// out is a safe, visible fallback rather than looping forever.
		return after.Add(time.Hour)
	}
}

// atTimeOfDay is day's own date at timeOfDay, on loc's wall clock. On
// the two days a year a zone with daylight saving has no such wall-clock
// time, or two of them, time.Date resolves it the way it resolves every
// other impossible or ambiguous date -- 02:30 on a spring-forward
// morning comes out as 03:30 -- so a schedule set for an hour that does
// not exist that day fires once, an hour late, rather than not at all.
func atTimeOfDay(day time.Time, timeOfDay int, loc *time.Location) time.Time {
	y, m, d := day.Date()
	return time.Date(y, m, d, timeOfDay/60, timeOfDay%60, 0, 0, loc)
}

// nextDaily walks whole days on loc's calendar rather than adding 24
// hours: AddDate keeps the wall-clock time it started from, so "daily at
// 09:00" stays at 09:00 across the two days a year that are 23 or 25
// hours long.
func nextDaily(after time.Time, timeOfDay int, loc *time.Location) time.Time {
	candidate := atTimeOfDay(after, timeOfDay, loc)
	if !candidate.After(after) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
}

func nextWeekly(after time.Time, weekday time.Weekday, timeOfDay int, loc *time.Location) time.Time {
	candidate := atTimeOfDay(after, timeOfDay, loc)
	for candidate.Weekday() != weekday || !candidate.After(after) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
}

func nextMonthly(after time.Time, dayOfMonth, timeOfDay int, loc *time.Location) time.Time {
	y, m, _ := after.Date()
	candidate := monthlyOccurrence(y, m, dayOfMonth, timeOfDay, loc)
	if !candidate.After(after) {
		y, m = addMonth(y, m)
		candidate = monthlyOccurrence(y, m, dayOfMonth, timeOfDay, loc)
	}
	return candidate
}

func addMonth(y int, m time.Month) (int, time.Month) {
	m++
	if m > time.December {
		m = time.January
		y++
	}
	return y, m
}

// monthlyOccurrence is dayOfMonth (clamped to year/month's own last day)
// at timeOfDay -- time.Date's own month-overflow semantics would instead
// roll a too-large day into the following month, which is exactly the
// drift clamping avoids.
func monthlyOccurrence(y int, m time.Month, dayOfMonth, timeOfDay int, loc *time.Location) time.Time {
	lastDay := time.Date(y, m+1, 0, 0, 0, 0, 0, loc).Day()
	day := dayOfMonth
	if day > lastDay {
		day = lastDay
	}
	return time.Date(y, m, day, timeOfDay/60, timeOfDay%60, 0, 0, loc)
}

// ReviewPolicy is when a schedule that runs a workflow
// (WorkflowReviewTemplateID) files a task to review the run it started
// (grain/task-3160).
//
// The run is the check. A night whose every job passed is visible on the
// schedule and on the CI page without anybody being sent to read it, and
// spending an agent run to report "everything passed" was the most
// common thing a nightly did. So by default only a run that is not
// clearly green -- failed, errored, cancelled, lost, never started, or
// waited out -- gets a review.
type ReviewPolicy string

const (
	// ReviewOnFailure files a review for every run that did not
	// pass, and none for one that did. The zero value means this.
	ReviewOnFailure ReviewPolicy = "on-failure"
	// ReviewAlways files a review for every firing, green or
	// not -- what every workflow schedule did before the policy existed.
	ReviewAlways ReviewPolicy = "always"
)

// Always reports whether p files a review for a run that passed. The
// zero value does not.
func (p ReviewPolicy) Always() bool { return p == ReviewAlways }

// Validate rejects a policy this package does not know. The zero value
// is accepted and means ReviewOnFailure.
func (p ReviewPolicy) Validate() error {
	switch p {
	case "", ReviewOnFailure, ReviewAlways:
		return nil
	default:
		return fmt.Errorf("review is %q or %q, got %q",
			ReviewOnFailure, ReviewAlways, string(p))
	}
}

// loadLocation resolves a zone name to the *time.Location wall-clock
// computation in grain is done against -- Recurrence.Next chief among them,
// which is what decides when a daily, weekly or monthly schedule actually fires.
//
// An empty name or "UTC" resolves to time.UTC.
// Unlike time.LoadLocation it cannot fail; an unknown name falls back to time.UTC.
func loadLocation(name string) *time.Location {
	if name == "" || name == "UTC" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.UTC
}

// ValidTimeZone reports whether name is an IANA zone this build can
// actually resolve.
//
// "" and "UTC" are valid. "Local", which time.LoadLocation accepts, is not --
// it names whatever zone the daemon's host or TZ environment variable happens
// to be set to, which is non-deterministic.
func ValidTimeZone(name string) bool {
	if name == "" || name == "UTC" {
		return true
	}
	if name == "Local" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}
