package model

import (
	"fmt"
	"strings"
	"time"
)

// ReleaseNote is one week of what this deployment actually shipped,
// written in prose by the standing job that fires every Monday morning
// (ReleaseNotesTemplateID, system_template.go) and kept as a row here.
//
// It is the one thing grain writes about itself that is meant for a
// person rather than for the next run. Memory (memory.go) next door is
// written for agents and is deliberately not a report -- its own prompt
// spends a paragraph refusing "a narrative of what happened last night",
// because every word of a memory is repeated in every later dispatch. A
// week's release notes are the opposite trade: nothing carries them into
// a prompt, nobody pays for them twice, and their whole job is to say
// what a fortnight of unattended work amounts to -- how many pull
// requests merged, in which repositories, what changed in them, what is
// still failing.
//
// # A row, and therefore also a file
//
// The same argument MemoryDocument's own doc comment makes applies
// unchanged here: this is written by an agent mid-run, through the
// daemon's REST API, from a sandbox that holds no credential for the
// state repository and cannot be given one. So the row is where the
// truth is, and staterepo's export is what puts it in the state
// repository as `release-notes/<period>.md` on the next sync -- a real
// markdown file, in a well-known place, readable by anybody with a
// clone and diffable in the repository's own history, with no agent
// pushing to this deployment's settings to produce it.
//
// It is not a settings row (it is off staterepo.SettingsTables, with
// memory and memory_document), so a merged pull request against the
// state repository never replaces one: a branch cut before Monday's run
// cannot silently delete the week it wrote.
type ReleaseNote struct {
	// Period is the week these notes are for, as an ISO week -- the
	// string ReleaseNotePeriod returns, "2026-W38". It is the primary
	// key and the file name, so writing the same period twice replaces
	// that week's notes rather than filing a second copy: a run that
	// was retried, or that found more to say, corrects the week it was
	// about rather than leaving two accounts of it.
	Period string
	// Title is the headline the pane and the file lead with -- "A
	// quiet week on kontur", whatever the run found to call it. Empty
	// is allowed and falls back to the period itself; a week with
	// nothing in it is still a week.
	Title string
	// Text is the notes themselves, markdown, replaced wholesale on
	// every write. Empty deletes the row, exactly as an empty memory
	// forgets a scope -- which is how a run that fired against a week
	// in which nothing happened, or a note somebody wants gone, is
	// taken back out of the repository.
	Text string
	// Length is how many bytes Text holds, as reported by a listing
	// that did not read it (ListReleaseNotes). Zero from any path that
	// carries the text itself.
	Length int
	// CreatedAt is when this week's notes were first written, kept
	// across a rewrite so that "when did this deployment first have
	// something to say about that week" survives a correction.
	CreatedAt time.Time
	// UpdatedAt is when they were last written.
	UpdatedAt time.Time
	// UpdatedBy is who wrote them -- an agent through
	// write_release_notes, or a person through the API. The same
	// (kind, id) pair Memory.UpdatedBy is, recorded for the same
	// reason: these are published to whoever opens the state
	// repository, and a reader is entitled to know whether a week's
	// account is an agent's or somebody's own correction of one.
	UpdatedBy Principal
}

const (
	// MaxReleaseNoteLength bounds one week's notes. Generous next to a
	// memory's 8 KiB, because nothing carries this into a prompt and
	// the cost of a long one falls only on the person who opens it --
	// and still bounded, because past a point a week's notes have
	// stopped being notes and become the task list they were written
	// from, which grain already has.
	MaxReleaseNoteLength = 64 * 1024
	// MaxReleaseNoteTitleLength bounds the headline. A line, not a
	// paragraph: it is what the pane lists and what the file leads
	// with.
	MaxReleaseNoteTitleLength = 200
)

// ReleaseNotePeriod is the week t falls in, on loc's calendar, as the
// ISO week the notes for it are filed under: "2026-W38".
//
// ISO weeks rather than "the seven days up to Monday", because the
// period is a key a person types, a file name in a repository and a
// heading in a document, and all three want the same stable, sortable,
// year-crossing spelling. ISO's own year is used rather than the
// calendar year (t.ISOWeek returns both), which is what keeps the last
// few days of December in the week they actually belong to rather than
// filing them as week 1 of the year that has not started.
//
// loc nil is UTC, the same silence every other calendar answer in this
// package keeps for a caller with no deployment configuration to hand.
func ReleaseNotePeriod(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	year, week := t.In(loc).ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week)
}

// LastCompleteReleaseNotePeriod is the week before the one t falls in:
// the most recent week that has actually finished, and so the week a
// report written now is about.
//
// It is the default period everywhere a caller does not name one -- the
// tool an agent writes through, the tool that counts the numbers, and
// the API behind them -- because the alternative is every caller
// separately deciding what "this week" means and one of them getting it
// wrong. The job fires on a Monday morning and is looking back over the
// week that ended a few hours earlier; a person opening the pane on a
// Thursday means the same week, since the one they are in is not over.
func LastCompleteReleaseNotePeriod(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return ReleaseNotePeriod(t.In(loc).AddDate(0, 0, -7), loc)
}

// ReleaseNotePeriodRange is the half-open instant range a period covers
// -- Monday 00:00 up to the next Monday 00:00, on loc's calendar.
//
// What it is for is the window the week's numbers are counted over
// (Store.ClusterStats): the notes and the statistics beside them have
// to be about the same seven days, and deriving both from the period
// string is what makes that so rather than hoping two callers picked
// the same "last week".
func ReleaseNotePeriodRange(period string, loc *time.Location) (time.Time, time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	year, week, err := parseReleaseNotePeriod(period)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	// 4 January is in ISO week 1 of its own year by definition, so the
	// Monday of week 1 is the Monday on or before it; every other week
	// is that Monday plus seven days per week.
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, loc)
	offset := (int(jan4.Weekday()) + 6) % 7 // Monday is 0
	start := jan4.AddDate(0, 0, -offset+7*(week-1))
	return start, start.AddDate(0, 0, 7), nil
}

// ValidateReleaseNotePeriod accepts exactly what ReleaseNotePeriod
// writes, and nothing else.
//
// Narrow on purpose, for ValidateMemoryDocumentName's reason: the period
// is a key in the store, a path component in the state repository and a
// word an agent types back into a tool call, and the three have to be
// the same string. A period that differed by a missing zero would be a
// week that exists twice and reads as neither.
func ValidateReleaseNotePeriod(period string) error {
	if period == "" {
		return fmt.Errorf("release notes are filed under a week, like %q", ReleaseNotePeriod(time.Now().UTC(), time.UTC))
	}
	if _, _, err := parseReleaseNotePeriod(period); err != nil {
		return err
	}
	return nil
}

// parseReleaseNotePeriod splits "2026-W38" into its ISO year and week,
// refusing anything else with the sentence a model reading the refusal
// can act on in the same turn.
func parseReleaseNotePeriod(period string) (int, int, error) {
	bad := func() (int, int, error) {
		return 0, 0, fmt.Errorf("%q is not a week grain files release notes under: "+
			"they are ISO weeks, written %q", period, "2026-W38")
	}
	year, week, ok := strings.Cut(period, "-W")
	if !ok || len(year) != 4 || len(week) != 2 {
		return bad()
	}
	y, err := atoiExact(year)
	if err != nil {
		return bad()
	}
	w, err := atoiExact(week)
	if err != nil || w < 1 || w > 53 {
		return bad()
	}
	return y, w, nil
}

// atoiExact reads a run of digits and refuses everything else -- a sign,
// a space, a stray letter -- so that "20x6" and " 12" are not quietly
// taken for numbers the way a looser parse would take them.
func atoiExact(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%q is not a number", s)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}
