package model_test

import (
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// The zone database is compiled in (timezone.go's own import), so this
// answers the same way on a slimmed container image as on a developer's
// laptop.
func TestLoadLocationResolvesIANAWithoutTheHostsZoneinfo(t *testing.T) {
	loc := model.LoadLocation("America/Los_Angeles")
	if loc.String() != "America/Los_Angeles" {
		t.Fatalf("LoadLocation(%q) = %q, want the zone itself", "America/Los_Angeles", loc)
	}
	// Winter, so this is the standing offset rather than the summer one:
	// Pacific is eight hours behind UTC.
	_, offset := time.Date(2026, time.January, 15, 12, 0, 0, 0, loc).Zone()
	if want := -8 * 60 * 60; offset != want {
		t.Errorf("January offset = %ds, want %ds", offset, want)
	}
}

func TestLoadLocationFallsBackToUTCSDefaultForANameNothingAnswersTo(t *testing.T) {
	if got := model.LoadLocation("Mars/Olympus_Mons"); got.String() != "UTC" {
		t.Errorf("LoadLocation of a nonsense zone = %q, want %q", got, "UTC")
	}
	if got := model.LoadLocation(""); got.String() != "UTC" {
		t.Errorf("LoadLocation(\"\") = %q, want %q", got, "UTC")
	}
}

func TestTimeZoneOrDefaultOnlyFillsInAnEmptyName(t *testing.T) {
	if got := model.TimeZoneOrDefault(""); got != model.DefaultTimeZone {
		t.Errorf("TimeZoneOrDefault(\"\") = %q, want %q", got, model.DefaultTimeZone)
	}
	if got := model.TimeZoneOrDefault("America/New_York"); got != "America/New_York" {
		t.Errorf("TimeZoneOrDefault(\"America/New_York\") = %q, want it left alone", got)
	}
}

func TestValidTimeZone(t *testing.T) {
	for _, name := range []string{"", "UTC", "America/Los_Angeles", "Europe/Berlin"} {
		if !model.ValidTimeZone(name) {
			t.Errorf("ValidTimeZone(%q) = false, want true", name)
		}
	}
	// "Local" resolves for time.LoadLocation and is refused here on
	// purpose: it names whatever the daemon's host happens to be set to.
	for _, name := range []string{"Mars/Olympus_Mons", "Local", "PST", "-08:00"} {
		if model.ValidTimeZone(name) {
			t.Errorf("ValidTimeZone(%q) = true, want false", name)
		}
	}
}

func TestScheduleAndRecurrenceLocation(t *testing.T) {
	sched := model.Schedule{TimeZone: "America/Chicago"}
	if loc := sched.Location(); loc.String() != "America/Chicago" {
		t.Errorf("Schedule.Location() = %q, want America/Chicago", loc.String())
	}

	rec := model.Recurrence{TimeZone: "Europe/London"}
	if loc := rec.Location(); loc.String() != "Europe/London" {
		t.Errorf("Recurrence.Location() = %q, want Europe/London", loc.String())
	}

	schedWithRec := model.Schedule{Recurrence: rec}
	if loc := schedWithRec.Location(); loc.String() != "Europe/London" {
		t.Errorf("Schedule with Recurrence.TimeZone Location() = %q, want Europe/London", loc.String())
	}

	zeroSched := model.Schedule{}
	if loc := zeroSched.Location(); loc.String() != "UTC" {
		t.Errorf("zero Schedule Location() = %q, want UTC", loc.String())
	}
}
