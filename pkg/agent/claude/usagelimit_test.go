package claude

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
)

// The shape that matters most: claude reports an OAuth account's own
// limit as the run's terminal result, exits 0, and nothing else about
// the capture says anything is wrong. Run must still hand back a
// usage limit, or the deployment sends the next task at the same wall.
func TestRunReportsAUsageLimitDeliveredAsAFinalAnswer(t *testing.T) {
	fake := &fakeRunner{stdout: strings.Join([]string{
		streamJSONLine(t, map[string]any{
			"type": "result", "result": "Claude AI usage limit reached|1772370000",
		}),
	}, "\n")}
	f := newFramework(fake, "mcpserver-path")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	limit, ok := agent.UsageLimit(err)
	if !ok {
		t.Fatalf("Run err = %v, want an agent.UsageLimitError", err)
	}
	if limit.Framework != "claude" {
		t.Errorf("Framework = %q, want claude", limit.Framework)
	}
	if want := time.Unix(1772370000, 0).UTC(); !limit.ResetAt.Equal(want) {
		t.Errorf("ResetAt = %s, want %s (the epoch the CLI appended)", limit.ResetAt, want)
	}
	// agent.Framework's contract: whatever the run did still comes back
	// alongside the error.
	if result == nil {
		t.Error("Run returned no Result, want the parsed one alongside the limit")
	}
}

// The failing shape: a non-zero exit with the API's own refusal in the
// stream. runFailure must prefer the limit to its generic rendering of
// the result event.
func TestRunReportsAUsageLimitFromAFailedRun(t *testing.T) {
	fake := &fakeRunner{
		stdout: streamJSONLine(t, map[string]any{
			"type": "result", "subtype": "error_during_execution", "is_error": true,
			"result": `API Error: 429 {"type":"error","error":{"type":"rate_limit_error","message":"rate limit exceeded"}}`,
		}),
		err: errors.New("exit status 1"),
	}
	f := newFramework(fake, "mcpserver-path")

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	limit, ok := agent.UsageLimit(err)
	if !ok {
		t.Fatalf("Run err = %v, want an agent.UsageLimitError", err)
	}
	if !strings.Contains(limit.Message, "rate_limit_error") {
		t.Errorf("Message = %q, want the provider's own words", limit.Message)
	}
	if !limit.ResetAt.IsZero() {
		t.Errorf("ResetAt = %s, want none: a bare 429 names no reset", limit.ResetAt)
	}
}

// A run that ran out of turns is not a run that ran out of budget, and
// the two must not be confused: one is a setting to raise, the other is
// a deployment-wide wait.
func TestRunKeepsMaxTurnsDistinctFromAUsageLimit(t *testing.T) {
	fake := &fakeRunner{
		stdout: streamJSONLine(t, map[string]any{
			"type": "result", "subtype": "error_max_turns", "is_error": true, "result": "",
		}),
		err: errors.New("exit status 1"),
	}
	f := newFramework(fake, "mcpserver-path", WithMaxTurns(3))

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if _, ok := agent.UsageLimit(err); ok {
		t.Fatalf("Run err = %v, want an ordinary turn-cap failure", err)
	}
	if !strings.Contains(err.Error(), "max turns") {
		t.Errorf("err = %q, want it to still name the turn cap", err)
	}
}

// The false positive that would matter: an agent whose own work quotes
// the phrase -- reading this very file, grepping a log -- must not put
// the whole deployment to sleep. A successful run is only ever read
// under the strict "|<epoch>" form, and only from the terminal result
// event, never from tool output.
func TestRunIgnoresTheLimitPhraseInASuccessfulRunsWork(t *testing.T) {
	fake := &fakeRunner{stdout: strings.Join([]string{
		streamJSONLine(t, map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{
					"type": "tool_use", "id": "call-1", "name": "run_command",
					"input": map[string]any{"command": "grep -r 'usage limit reached' ."},
				}},
			},
		}),
		streamJSONLine(t, map[string]any{
			"type": "user",
			"message": map[string]any{
				"content": []map[string]any{{
					"type": "tool_result", "tool_use_id": "call-1",
					"content": "usagelimit.go: usage limit reached|1772370000",
				}},
			},
		}),
		streamJSONLine(t, map[string]any{"type": "result", "result": "found the phrase in one file"}),
	}, "\n")}
	f := newFramework(fake, "mcpserver-path")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "grep", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run err = %v, want the run to pass: nothing here is a refusal from the CLI", err)
	}
	if result.FinalText != "found the phrase in one file" {
		t.Errorf("FinalText = %q", result.FinalText)
	}
}

func TestResetFromEpochReadsBothUnits(t *testing.T) {
	seconds := resetFromEpoch("1772370000")
	if want := time.Unix(1772370000, 0).UTC(); !seconds.Equal(want) {
		t.Errorf("seconds epoch = %s, want %s", seconds, want)
	}
	millis := resetFromEpoch("1772370000000")
	if want := time.UnixMilli(1772370000000).UTC(); !millis.Equal(want) {
		t.Errorf("millisecond epoch = %s, want %s", millis, want)
	}
	// Both spellings must name the same moment, which is the whole point
	// of telling them apart by magnitude.
	if !seconds.Equal(millis) {
		t.Errorf("the same instant read back as %s and %s", seconds, millis)
	}
}

func TestUsageLimitFromResultRequiresTheStrictForm(t *testing.T) {
	if limit := usageLimitFromResult("I hit a rate limit exceeded error while testing"); limit != nil {
		t.Errorf("usageLimitFromResult reported %v for a sentence with no epoch", limit)
	}
	if limit := usageLimitFromResult("Claude AI usage limit reached|1772370000"); limit == nil {
		t.Error("usageLimitFromResult reported nothing for the CLI's own report")
	}
}

// sessionLimitText is what the CLI printed for every run of the
// 2026-10-02 17:15-17:30 UTC outage, none of which paused dispatch.
const sessionLimitText = "You've hit your session limit · resets 5:30pm (UTC)"

// atClock stands usageLimitNow at now for the length of the test.
func atClock(t *testing.T, now time.Time) {
	t.Helper()
	prev := usageLimitNow
	usageLimitNow = func() time.Time { return now }
	t.Cleanup(func() { usageLimitNow = prev })
}

// The outage's exact shape: a result event with is_error set and the
// misleading subtype "success", then exit 1 with nothing on stderr.
func TestRunReportsTheWordedSessionLimitFromAFailedRun(t *testing.T) {
	atClock(t, time.Date(2026, 10, 2, 17, 15, 0, 0, time.UTC))
	fake := &fakeRunner{
		stdout: streamJSONLine(t, map[string]any{
			"type": "result", "subtype": "success", "is_error": true, "result": sessionLimitText,
		}),
		err: errors.New("exit status 1 (stderr: )"),
	}
	f := newFramework(fake, "mcpserver-path")

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	limit, ok := agent.UsageLimit(err)
	if !ok {
		t.Fatalf("Run err = %v, want an agent.UsageLimitError", err)
	}
	if want := time.Date(2026, 10, 2, 17, 30, 0, 0, time.UTC); !limit.ResetAt.Equal(want) {
		t.Errorf("ResetAt = %s, want %s", limit.ResetAt, want)
	}
	if !strings.Contains(limit.Message, "session limit") {
		t.Errorf("Message = %q, want the CLI's own words", limit.Message)
	}
}

// The same words delivered as an ordinary final answer, exit 0.
func TestRunReportsTheWordedSessionLimitDeliveredAsAFinalAnswer(t *testing.T) {
	atClock(t, time.Date(2026, 10, 2, 17, 15, 0, 0, time.UTC))
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{
		"type": "result", "result": sessionLimitText,
	})}
	f := newFramework(fake, "mcpserver-path")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	limit, ok := agent.UsageLimit(err)
	if !ok {
		t.Fatalf("Run err = %v, want an agent.UsageLimitError", err)
	}
	if want := time.Date(2026, 10, 2, 17, 30, 0, 0, time.UTC); !limit.ResetAt.Equal(want) {
		t.Errorf("ResetAt = %s, want %s", limit.ResetAt, want)
	}
	if result == nil {
		t.Error("Run returned no Result, want the parsed one alongside the limit")
	}
}

// An agent whose answer talks about a session limit -- even one that
// quotes the CLI's sentence -- has not been refused by anybody.
func TestRunIgnoresTheSessionLimitPhraseInASuccessfulAnswer(t *testing.T) {
	for _, answer := range []string{
		"I added handling for when the agent has hit your session limit wording; tests pass.",
		"Fixed the detector. The CLI now prints \"You've hit your session limit · resets 5:30pm (UTC)\", which grain did not recognise, so dispatch kept retrying; usagelimit.go now matches it in both paths and parses the reset clock.",
		"The session limit is configurable in settings.",
	} {
		fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": answer})}
		f := newFramework(fake, "mcpserver-path")
		if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
			t.Errorf("Run(%q) err = %v, want a passing run", answer, err)
		}
	}
}

func TestUsageLimitRecognisesEachWordedLimit(t *testing.T) {
	for _, text := range []string{
		sessionLimitText,
		"You've hit your usage limit · resets 5pm (UTC)",
		"You’ve hit your weekly limit · resets Oct 5, 5pm (UTC)",
		"You've hit your Opus weekly limit · resets 9am",
		"You have hit your session limit",
	} {
		if usageLimitFromResult(text) == nil {
			t.Errorf("usageLimitFromResult(%q) = nil, want a limit", text)
		}
		if usageLimitFromFailure("", errors.New(text)) == nil {
			t.Errorf("usageLimitFromFailure(%q) = nil, want a limit", text)
		}
	}
}

func TestResetFromClock(t *testing.T) {
	now := time.Date(2026, 10, 2, 17, 15, 0, 0, time.UTC)
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		text string
		now  time.Time
		want time.Time
	}{
		{"resets 5:30pm (UTC)", now, time.Date(2026, 10, 2, 17, 30, 0, 0, time.UTC)},
		{"resets 5pm", now, time.Date(2026, 10, 3, 17, 0, 0, 0, time.UTC)}, // passed today
		{"resets 17:30", now, time.Date(2026, 10, 2, 17, 30, 0, 0, time.UTC)},
		{"resets 17:15", now, time.Date(2026, 10, 3, 17, 15, 0, 0, time.UTC)}, // now is not "next"
		{"resets 12am (UTC)", now, time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)},
		{"resets 12pm", now, time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)},
		{"resets 1:30 p.m.", now, time.Date(2026, 10, 3, 13, 30, 0, 0, time.UTC)},
		// The midnight rollover: 23:50 asking for 12:10am is ten minutes away.
		{"resets 12:10am (UTC)", time.Date(2026, 10, 2, 23, 50, 0, 0, time.UTC), time.Date(2026, 10, 3, 0, 10, 0, 0, time.UTC)},
		// ...and across a month and year end.
		{"resets 00:05", time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 5, 0, 0, time.UTC)},
		// A zone: 1:30pm in New York is 17:30 UTC in October (EDT).
		{"resets 1:30pm (America/New_York)", now, time.Date(2026, 10, 2, 13, 30, 0, 0, newYork).UTC()},
		{"resets 10am (PDT)", now, time.Date(2026, 10, 3, 17, 0, 0, 0, time.UTC)},
		// The local day differs from UTC's: 17:15 UTC is 02:15 Oct 3 in Tokyo.
		{"resets 3am (Asia/Tokyo)", now, time.Date(2026, 10, 2, 18, 0, 0, 0, time.UTC)},
		// Weekly forms with a date.
		{"resets Oct 5, 5pm (UTC)", now, time.Date(2026, 10, 5, 17, 0, 0, 0, time.UTC)},
		{"resets October 5 at 9:00am", now, time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)},
		{"resets Jan 2, 5pm", time.Date(2026, 12, 30, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 2, 17, 0, 0, 0, time.UTC)},
		// Unreadable: no reset, but nothing worse.
		{"resets 5", now, time.Time{}},
		{"resets 25:00", now, time.Time{}},
		{"resets 13pm", now, time.Time{}},
		{"resets 5pm (Nowhere/Atlantis)", now, time.Time{}},
		{"resets Feb 30, 5pm", now, time.Time{}},
		{"resets soon", now, time.Time{}},
		{"no clause at all", now, time.Time{}},
	} {
		got := resetFromClock("You've hit your session limit · "+tc.text, tc.now)
		if !got.Equal(tc.want) {
			t.Errorf("resetFromClock(%q at %s) = %s, want %s", tc.text, tc.now, got, tc.want)
		}
	}
}

// A reset the parser cannot read still reports the limit: detection
// does not hang on the clock.
func TestUnparseableResetStillReportsTheLimit(t *testing.T) {
	limit := usageLimitFromResult("You've hit your session limit · resets whenever")
	if limit == nil {
		t.Fatal("usageLimitFromResult = nil, want a limit with no reset")
	}
	if !limit.ResetAt.IsZero() {
		t.Errorf("ResetAt = %s, want zero", limit.ResetAt)
	}
}
