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

package claude

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	// The reset clock below names its zone ("(UTC)", "(Europe/Berlin)"),
	// and a zone the host has no tzdata for would cost the pause its
	// time; pkg/model links the same database into the binary already.
	_ "time/tzdata"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// A run that ends because the Claude Code account has no budget left in
// its current window is not a run that failed on its own terms, and
// treating it as one costs the whole queue: dispatch retries the task,
// the retry hits the same wall, and every other task in the deployment
// follows it there. agent.UsageLimitError is what says so to a caller,
// and this file is what recognises the condition in what the CLI
// actually prints.
//
// Two forms are recognised, deliberately at different strictnesses.
//
// The strict one is the CLI's own machine-readable report of an
// OAuth-account limit: the exact text "Claude AI usage limit
// reached|<unix seconds>", which it emits as the run's terminal result
// -- sometimes as a failure, sometimes as an ordinary final answer with
// exit 0. Because that second shape reaches us through a *successful*
// parse, matching on it has to be tight enough that an agent quoting the
// phrase back at us (a run reading this very file, say) cannot trip it:
// the "|<epoch>" suffix is what makes it a report from the CLI rather
// than a sentence from the model.
//
// The loose one is every other way a provider says "not now" -- an API
// 429's rate_limit_error, a quota refusal -- and is only ever consulted
// for a run that has *already* failed, where the alternative reading is
// not "the agent said something" but "the run broke for reasons we
// cannot name".
const (
	// usageLimitFramework names this framework in the errors below, so a
	// deployment running both frameworks can tell whose credential is out
	// of budget from the run's own detail.
	usageLimitFramework = "claude"
)

// resetEpochPattern is the strict form: the CLI's own
// "...limit reached|<unix seconds>". The digit bound admits both a
// seconds epoch (10 digits until 2286) and a milliseconds one, which
// resetFromEpoch tells apart; it excludes a short number that happened
// to follow a pipe.
var resetEpochPattern = regexp.MustCompile(`(?i)usage limit reached\|(\d{9,13})`)

// looseUsageLimitMarkers are the phrases that mean "out of budget" in
// text that already belongs to a failed run. Lowercase, matched against
// a lowercased haystack.
//
// "429" is deliberately not one of them on its own: it is three digits
// that appear in file paths, diffs and test output, and the run whose
// text this reads is one whose sandbox has been echoing all three back
// for an hour. Every marker here is a phrase a provider writes and a
// repository does not.
var looseUsageLimitMarkers = []string{
	"usage limit reached",
	"usage limit exceeded",
	"rate_limit_error",
	"rate limit exceeded",
	"exceeded your rate limit",
}

// usageLimitFromResult reads the terminal result event's own text under
// the strict rule above -- the one detection that is allowed to turn a
// *successful* claude run into an error, because the CLI reports an
// account limit as the run's final answer as readily as it reports it as
// a failure. nil means this result says nothing about a limit, which is
// every ordinary run.
func usageLimitFromResult(resultText string) *agent.UsageLimitError {
	if m := resetEpochPattern.FindStringSubmatch(resultText); m != nil {
		return classifyClaudeLimit(&agent.UsageLimitError{
			Framework: usageLimitFramework,
			Message:   agent.TrimLimitMessage(resultText),
			ResetAt:   resetFromEpoch(m[1]),
		}, resultText)
	}
	if isHitLimitAnswer(resultText) {
		return classifyClaudeLimit(&agent.UsageLimitError{
			Framework: usageLimitFramework,
			Message:   agent.TrimLimitMessage(resultText),
			ResetAt:   resetFromClock(resultText, usageLimitNow()),
		}, resultText)
	}
	return nil
}

// usageLimitFromFailure reads a failed run's own account of itself --
// the terminal result event's text and whatever the subprocess itself
// reported (a non-zero exit with the API's error body on stderr) --
// under the loose rule. nil means the run failed for some other reason,
// which is what a caller falls back to its ordinary diagnosis for.
func usageLimitFromFailure(resultText string, runErr error) *agent.UsageLimitError {
	text := resultText
	if runErr != nil {
		text = strings.TrimSpace(text + "\n" + runErr.Error())
	}
	if !mentionsUsageLimit(text) {
		return nil
	}
	limit := &agent.UsageLimitError{
		Framework: usageLimitFramework,
		Message:   agent.TrimLimitMessage(text),
		Err:       runErr,
	}
	if m := resetEpochPattern.FindStringSubmatch(text); m != nil {
		limit.ResetAt = resetFromEpoch(m[1])
	} else {
		limit.ResetAt = resetFromClock(text, usageLimitNow())
	}
	return classifyClaudeLimit(limit, text)
}

// modelLimitPattern is Claude Code's own wording for a plan limit on one
// model family ("You've hit your Opus limit"), as opposed to the plan's
// session or weekly limit, which every model on the login shares.
var modelLimitPattern = regexp.MustCompile(`(?i)\b(opus|sonnet|haiku) (?:usage )?limit\b`)

// claudePlanLimitPattern is the plan's own limit, shared by every model
// on the login: "Claude AI usage limit reached", and the session and
// weekly limits Claude Code names in its "hit your ... limit" answer.
var claudePlanLimitPattern = regexp.MustCompile(`(?i)claude ai usage limit reached|\bhit your (?:session|weekly|5-hour|daily|usage) limit\b`)

// classifyClaudeLimit fills limit's Scope and Model from text: the
// providers' shared hints (agent.ClassifyLimit) first, then Claude
// Code's own -- a plan limit is account-wide, and a model family's limit
// names that family, which is all Claude Code says of the model.
func classifyClaudeLimit(limit *agent.UsageLimitError, text string) *agent.UsageLimitError {
	limit.Classify(text)
	switch {
	case modelLimitPattern.MatchString(text):
		if limit.Model == "" {
			limit.Model = strings.ToLower(modelLimitPattern.FindStringSubmatch(text)[1])
		}
	case claudePlanLimitPattern.MatchString(text):
		limit.Scope = agent.LimitScopeAccount
	}
	return limit
}

// mentionsUsageLimit reports whether text carries any of the loose
// markers, or the CLI's worded "hit your ... limit".
func mentionsUsageLimit(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range looseUsageLimitMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return hitLimitPattern.MatchString(text)
}

// usageLimitNow is the clock resetFromClock measures "the next 5:30pm"
// from; a variable so a test can stand at a chosen moment.
var usageLimitNow = time.Now

// hitLimitPattern is the CLI's worded refusal: "hit your session
// limit", "hit your usage limit", "hit your weekly limit" -- and, so the
// next window it names does not need a release of grain to be
// recognised, any one or two words in that slot ("hit your Opus weekly
// limit"). Loose: consulted on its own only for a run that has already
// failed.
var hitLimitPattern = regexp.MustCompile(`(?i)\bhit your (?:[\w-]+ ){1,2}limit\b`)

// hitLimitAnswerPattern is the same refusal as the CLI words a whole
// answer: it opens the text. Apostrophe straight or curly.
var hitLimitAnswerPattern = regexp.MustCompile(`(?i)^(?:you['’]ve|you have) hit your (?:[\w-]+ ){1,2}limit\b`)

// maxHitLimitAnswer bounds how long a successful run's final answer can
// be and still be read as the CLI's refusal. The CLI's own is about
// fifty characters; an agent's report that happens to open with the
// same words is a paragraph.
const maxHitLimitAnswer = 200

// isHitLimitAnswer is the strict reading of the worded form, for a
// successful run's final answer: the whole text is short and is the
// refusal, from its first word.
func isHitLimitAnswer(resultText string) bool {
	text := strings.TrimSpace(resultText)
	return len(text) <= maxHitLimitAnswer && hitLimitAnswerPattern.MatchString(text)
}

// resetClockPattern is the "resets ..." clause the worded form carries:
// an optional date ("Oct 5,", "October 5 at"), a clock time in either
// convention ("5:30pm", "5pm", "17:30"), and an optional zone in
// parentheses ("(UTC)", "(America/New_York)", "(PDT)").
var resetClockPattern = regexp.MustCompile(`(?i)\bresets\s+(?:([a-z]{3,9})\.?\s+(\d{1,2})(?:st|nd|rd|th)?,?\s+(?:at\s+)?)?(\d{1,2})(?::(\d{2}))?\s*([ap]\.?m\.?)?(?:\s*\(([^)]+)\))?`)

// zoneAbbreviations are the zone names the CLI might print that the
// tz database does not know by that name. Fixed offsets: an
// abbreviation already says which half of the year it means.
var zoneAbbreviations = map[string]int{
	"UTC": 0, "GMT": 0, "Z": 0,
	"BST": 1, "CET": 1, "CEST": 2, "EET": 2, "EEST": 3,
	"IST": 5*60 + 30, "JST": 9 * 60, "AEST": 10 * 60, "AEDT": 11 * 60,
	"EST": -5 * 60, "EDT": -4 * 60, "CST": -6 * 60, "CDT": -5 * 60,
	"MST": -7 * 60, "MDT": -6 * 60, "PST": -8 * 60, "PDT": -7 * 60,
}

// resetFromClock reads the worded form's "resets <time>" into the
// instant the window reopens: the next occurrence of that wall-clock
// time after now -- tomorrow's, when today's has already passed -- or,
// when a date is named, that date's, in the coming year if this year's
// is well past. A time with no zone is read as UTC, which is what the
// sandboxes the CLI runs in keep. Anything that does not read cleanly
// yields the zero time, which agent.UsageLimitError reads as "no reset
// named": the limit is still a limit, and the pause falls back to its
// own default window.
func resetFromClock(text string, now time.Time) time.Time {
	m := resetClockPattern.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}
	}
	monthName, dayDigits, hourDigits, minuteDigits, meridiem, zone := m[1], m[2], m[3], m[4], m[5], m[6]
	// A bare "resets 5" says neither which half of the day nor that it
	// is a time at all.
	if minuteDigits == "" && meridiem == "" {
		return time.Time{}
	}
	hour, _ := strconv.Atoi(hourDigits)
	minute := 0
	if minuteDigits != "" {
		minute, _ = strconv.Atoi(minuteDigits)
	}
	if minute > 59 {
		return time.Time{}
	}
	if meridiem != "" {
		if hour < 1 || hour > 12 {
			return time.Time{}
		}
		hour %= 12
		if strings.HasPrefix(strings.ToLower(meridiem), "p") {
			hour += 12
		}
	} else if hour > 23 {
		return time.Time{}
	}
	loc, ok := resetLocation(zone)
	if !ok {
		return time.Time{}
	}
	local := now.In(loc)
	if monthName != "" {
		month, ok := parseMonth(monthName)
		if !ok {
			return time.Time{}
		}
		day, _ := strconv.Atoi(dayDigits)
		at := time.Date(local.Year(), month, day, hour, minute, 0, 0, loc)
		if at.Month() != month || at.Day() != day {
			return time.Time{} // "Feb 30" normalised into March
		}
		// A date more than a day behind now is next year's: a weekly
		// limit named on Dec 30 that resets "Jan 2".
		if at.Before(now.Add(-24 * time.Hour)) {
			at = at.AddDate(1, 0, 0)
		}
		return at.UTC()
	}
	at := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if !at.After(now) {
		at = time.Date(local.Year(), local.Month(), local.Day()+1, hour, minute, 0, 0, loc)
	}
	return at.UTC()
}

// resetLocation reads the zone the CLI put in parentheses: an IANA name
// or one of zoneAbbreviations. No zone at all is UTC; one that is
// neither is not guessed at.
func resetLocation(zone string) (*time.Location, bool) {
	zone = strings.TrimSpace(zone)
	if zone == "" {
		return time.UTC, true
	}
	if offset, ok := zoneAbbreviations[strings.ToUpper(zone)]; ok {
		if offset == 0 {
			return time.UTC, true
		}
		return time.FixedZone(strings.ToUpper(zone), offset*60), true
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, false
	}
	return loc, true
}

// parseMonth reads an English month name, full or abbreviated to its
// first three letters ("Sept" too).
func parseMonth(name string) (time.Month, bool) {
	lower := strings.ToLower(name)
	if len(lower) < 3 {
		return 0, false
	}
	for m := time.January; m <= time.December; m++ {
		full := strings.ToLower(m.String())
		if lower == full || lower == full[:3] || (m == time.September && lower == "sept") {
			return m, true
		}
	}
	return 0, false
}

// resetFromEpoch turns the digits captured after the pipe into an
// instant, reading a 12-or-more-digit number as milliseconds and
// anything shorter as seconds -- the two spellings a Unix epoch arrives
// in, told apart by magnitude rather than by trusting one. A number that
// does not parse at all (longer than an int64) yields the zero time,
// which agent.UsageLimitError already reads as "the provider named no
// reset".
func resetFromEpoch(digits string) time.Time {
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return time.Time{}
	}
	if len(digits) >= 12 {
		return time.UnixMilli(n).UTC()
	}
	return time.Unix(n, 0).UTC()
}
