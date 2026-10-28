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
	"strconv"
	"strings"
	"time"
)

// A workflow's `on: schedule` is translated into one of grain's own task
// schedules rather than run by a cron of this package's own (grain/task-344).
//
// The two halves of that sentence are both deliberate. grain has a
// scheduler already -- model.Schedule, fired by
// orchestrator.reconcileSchedule -- and it is the one an operator can see,
// pause and read the firing history of; a second timer living inside the CI
// engine would be a recurring claim on this deployment's sandboxes that
// appears nowhere an operator looks. And a run nobody reads is the failure
// this replaces: what the translated schedule fires is a task that watches
// the run it started and reports on it (model.WorkflowReviewTemplateID), so
// a nightly suite that starts failing is read by somebody the next morning
// rather than sitting green-or-red in a table.
//
// Until this existed the `schedule:` key parsed -- carefully, refusing a
// non-list, an empty list and an entry with no `cron` -- and was then
// dropped: `o.events[Event("schedule")] = nil` was the only occurrence of
// that event in the tree and nothing read it. The diligence is what made it
// invisible. A misspelling was caught and a correct file was accepted and
// ignored, which is how bwsalmon/grain's own `.grain/workflows/nightly.yml`
// declared five jobs and a 02:11 cron that had never run once.
//
// # Why some crons are refused
//
// A task schedule has exactly four cadences (RecurrenceKind), and
// cron is strictly more expressive. `11 2 * * *` is "daily at 02:11" and
// translates exactly; `*/15 * * * *`, `0 9 * * 1-5` and every other list,
// range or step expression has no representation at all.
//
// The refusal is the point. A suite somebody believes runs every fifteen
// minutes and actually runs hourly is worse than one that does not run,
// because nothing about it looks wrong, so nothing here approximates: a
// cron that cannot be said exactly is a parse error naming the key and
// listing what can be said (ErrCronNotExpressible's message), the same way
// this package refuses every other GitHub key it does not implement.
//
// # The one divergence from GitHub, said out loud
//
// GitHub reads a workflow cron in UTC. A grain schedule is read on the
// deployment's own wall clock (Recurrence, Config.TimeZone), and a
// translated cron is a grain schedule, so `11 2 * * *` fires at 02:11 where
// the deployment lives. That is the same clock every other schedule on this
// deployment is read against, and having one workflow keep its own zone
// would be a second answer to "when is 02:11" for no gain. A deployment
// running in UTC -- the default -- sees no difference at all.

// ScheduleTrigger is one entry of a workflow's `on: schedule` list: the
// cron as written, and the task-schedule cadence it means.
//
// Cron is kept for the error messages and for the UI: what an operator
// wrote is what they will go looking for, and "daily at 02:11" is not a
// string they can find in the file.
type ScheduleTrigger struct {
	Cron       string
	TimeZone   string
	Recurrence Recurrence
	// Review is the entry's own `review:`, grain's extension beside
	// `timezone:`: when the schedule this cron becomes files a review of
	// the run it starts (ReviewPolicy). Empty means the
	// default, on-failure.
	Review ReviewPolicy
}

// cronCadences is what the refusal offers instead, in the file's own
// vocabulary. Written once here so the message and the doc comment cannot
// drift apart.
const cronCadences = `"M H * * *" (daily at H:M), "M H * * DOW" (weekly), ` +
	`"M H DOM * *" (monthly on day DOM)`

// ParseCron translates one workflow cron into the cadence a grain task
// schedule can express, or says why it cannot.
//
// The accepted shapes are exactly the three that map with no loss, each
// with a single value in every field it uses:
//
//   - "11 2 * * *"  -> daily at 02:11
//   - "0 9 * * mon" -> weekly, Monday at 09:00
//   - "0 9 1 * *"   -> monthly, the 1st at 09:00
//
// Everything else is refused, and the list of what is refused is short
// because it is everything: `*` in the minute or hour field, a list
// ("0,30"), a range ("1-5"), a step ("*/15"), a month other than `*`, and
// both day-of-month and day-of-week together -- cron ORs those two, which
// no single cadence here means. Nicknames (`@daily`) are refused too, as
// GitHub does not accept them either.
func ParseCron(cron string) (Recurrence, error) {
	fields := strings.Fields(strings.TrimSpace(cron))
	if len(fields) != 5 {
		return Recurrence{}, fmt.Errorf("%q is not a cron expression -- five fields, "+
			"minute hour day-of-month month day-of-week", cron)
	}
	minute, hour, dom, month, dow := fields[0], fields[1], fields[2], fields[3], fields[4]

	m, err := cronNumber(cron, "minute", minute, 0, 59)
	if err != nil {
		return Recurrence{}, err
	}
	h, err := cronNumber(cron, "hour", hour, 0, 23)
	if err != nil {
		return Recurrence{}, err
	}
	if month != "*" {
		return Recurrence{}, cronNotExpressible(cron,
			"a schedule that only runs in some months has no cadence here")
	}
	atDay, atWeekday := dom != "*", dow != "*"
	switch {
	case atDay && atWeekday:
		return Recurrence{}, cronNotExpressible(cron,
			"cron runs a day-of-month and a day-of-week entry on either day, which no single cadence here means")
	case atWeekday:
		weekday, err := cronWeekday(cron, dow)
		if err != nil {
			return Recurrence{}, err
		}
		return validRecurrence(cron, Recurrence{
			Kind: RecurrenceWeekly, TimeOfDay: h*60 + m, Weekday: weekday,
		})
	case atDay:
		day, err := cronNumber(cron, "day-of-month", dom, 1, 31)
		if err != nil {
			return Recurrence{}, err
		}
		return validRecurrence(cron, Recurrence{
			Kind: RecurrenceMonthly, TimeOfDay: h*60 + m, DayOfMonth: day,
		})
	default:
		return validRecurrence(cron, Recurrence{
			Kind: RecurrenceDaily, TimeOfDay: h*60 + m,
		})
	}
}

// validRecurrence is the belt to ParseCron's braces: the field bounds above
// already guarantee a valid cadence, and asking model itself means a cadence
// this package builds can never be one the store would refuse to fire.
func validRecurrence(cron string, r Recurrence) (Recurrence, error) {
	if err := r.Validate(); err != nil {
		return Recurrence{}, fmt.Errorf("%q: %w", cron, err)
	}
	return r, nil
}

// cronNumber reads one field that must be a single number in range --
// which is to say it refuses `*`, and refuses every operator, naming the
// one it found.
func cronNumber(cron, field, value string, min, max int) (int, error) {
	if value == "*" {
		return 0, cronNotExpressible(cron, fmt.Sprintf("the %s field is `*`, so this runs at a set of "+
			"times a day rather than at one time of day", field))
	}
	if i := strings.IndexAny(value, ",-/"); i >= 0 {
		return 0, cronNotExpressible(cron, fmt.Sprintf("the %s field uses %q, and a task schedule "+
			"fires on one cadence rather than a set of times", field, value[i:i+1]))
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%q: %s: %q is not a number between %d and %d", cron, field, value, min, max)
	}
	return n, nil
}

// cronWeekday reads cron's day-of-week field: 0-7 with both 0 and 7 as
// Sunday, or GitHub's three-letter names.
func cronWeekday(cron, value string) (time.Weekday, error) {
	switch strings.ToLower(value) {
	case "sun":
		return time.Sunday, nil
	case "mon":
		return time.Monday, nil
	case "tue":
		return time.Tuesday, nil
	case "wed":
		return time.Wednesday, nil
	case "thu":
		return time.Thursday, nil
	case "fri":
		return time.Friday, nil
	case "sat":
		return time.Saturday, nil
	}
	n, err := cronNumber(cron, "day-of-week", value, 0, 7)
	if err != nil {
		return 0, err
	}
	if n == 7 {
		n = 0
	}
	return time.Weekday(n), nil
}

// cronNotExpressible is the one refusal, said the same way every time: the
// key, the cron, what about it cannot be said, and what can be.
func cronNotExpressible(cron, because string) error {
	return fmt.Errorf("schedule: cron: %q cannot be run as a task schedule -- %s. "+
		"A task schedule has four cadences: every N hours, daily, weekly and monthly, "+
		"so a workflow cron translates when it is %s",
		cron, because, cronCadences)
}
