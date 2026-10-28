package agent_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bwsalmon/grain/pkg/agent"
)

func TestUsageLimitFindsAWrappedLimit(t *testing.T) {
	limit := &agent.UsageLimitError{Framework: "claude", Message: "usage limit reached"}
	err := fmt.Errorf("orchestrator: running 7-1: %w", limit)

	got, ok := agent.UsageLimit(err)
	if !ok {
		t.Fatalf("UsageLimit(%v) = _, false, want the limit back", err)
	}
	if got != limit {
		t.Errorf("UsageLimit returned %+v, want the very error that was wrapped", got)
	}
	if !errors.Is(err, agent.ErrUsageLimit) {
		t.Error("errors.Is(err, ErrUsageLimit) = false, want any usage limit to match the sentinel")
	}
}

// An ordinary framework failure must not read as a usage limit: it is
// the difference between one run failing and the whole deployment
// standing still.
func TestUsageLimitIgnoresAnOrdinaryError(t *testing.T) {
	err := fmt.Errorf("claude: exceeded max turns (20) without a final answer")
	if _, ok := agent.UsageLimit(err); ok {
		t.Errorf("UsageLimit(%v) reported a usage limit", err)
	}
	if errors.Is(err, agent.ErrUsageLimit) {
		t.Errorf("errors.Is(%v, ErrUsageLimit) = true", err)
	}
}

func TestUsageLimitErrorUnwrapsWhatItWraps(t *testing.T) {
	underlying := errors.New("exit status 1")
	limit := &agent.UsageLimitError{Framework: "claude", Err: underlying}
	if !errors.Is(limit, underlying) {
		t.Error("errors.Is(limit, underlying) = false, want the CLI's own error still reachable")
	}
}

func TestResumeAtPrefersAnAbsoluteReset(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	limit := &agent.UsageLimitError{
		ResetAt:    now.Add(2 * time.Hour),
		RetryAfter: time.Minute,
	}
	got, ok := limit.ResumeAt(now)
	if !ok {
		t.Fatal("ResumeAt reported nothing, want the reset instant")
	}
	if want := now.Add(2 * time.Hour); !got.Equal(want) {
		t.Errorf("ResumeAt = %s, want %s -- an instant survives the delay reaching us late", got, want)
	}
}

func TestResumeAtFallsBackToTheDelay(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	limit := &agent.UsageLimitError{RetryAfter: 90 * time.Second}
	got, ok := limit.ResumeAt(now)
	if !ok {
		t.Fatal("ResumeAt reported nothing, want now plus the delay")
	}
	if want := now.Add(90 * time.Second); !got.Equal(want) {
		t.Errorf("ResumeAt = %s, want %s", got, want)
	}
}

// A reset already in the past is not "resume immediately" -- see
// ResumeAt's own doc comment: the caller's floor is the safer reading of
// a clock the two ends disagree about.
func TestResumeAtDeclinesAStaleReset(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	limit := &agent.UsageLimitError{ResetAt: now.Add(-time.Hour)}
	if got, ok := limit.ResumeAt(now); ok {
		t.Errorf("ResumeAt = %s, true, want no answer at all for a reset in the past", got)
	}
}

func TestResumeAtReportsNothingWhenTheProviderNamedNothing(t *testing.T) {
	if _, ok := (&agent.UsageLimitError{}).ResumeAt(time.Now()); ok {
		t.Error("ResumeAt reported an instant for a limit that named neither")
	}
}

func TestErrorNamesTheFrameworkAndTheReset(t *testing.T) {
	limit := &agent.UsageLimitError{
		Framework: "claude",
		Message:   "Claude AI usage limit reached",
		ResetAt:   time.Date(2026, 3, 1, 17, 0, 0, 0, time.UTC),
	}
	got := limit.Error()
	for _, want := range []string{"claude", "usage limit reached", "2026-03-01T17:00:00Z"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, want it to contain %q", got, want)
		}
	}
}

// The message lands in task_run.detail, which `grain get` prints inline:
// a provider's whole JSON error body must not take a run listing over.
func TestTrimLimitMessageFoldsAndBounds(t *testing.T) {
	got := agent.TrimLimitMessage("  API Error: 429\n {\"type\":\"error\"}  ")
	if want := `API Error: 429 {"type":"error"}`; got != want {
		t.Errorf("TrimLimitMessage = %q, want %q", got, want)
	}
	long := agent.TrimLimitMessage(strings.Repeat("x", 1000))
	if len(long) > 260 {
		t.Errorf("TrimLimitMessage kept %d bytes of a 1000-byte message", len(long))
	}
	if !strings.HasSuffix(long, "...") {
		t.Errorf("TrimLimitMessage = %q, want a truncated message to say so", long)
	}
}

// The bound is in bytes, but the cut must not split a multi-byte
// character: the message is stored in task_run.detail and rendered by
// the UI, and half a rune there is an invalid UTF-8 string.
func TestTrimLimitMessageCutsOnARuneBoundary(t *testing.T) {
	for pad := 0; pad < 4; pad++ {
		got := agent.TrimLimitMessage(strings.Repeat("x", pad) + strings.Repeat("€", 200))
		if !utf8.ValidString(got) {
			t.Errorf("pad %d: TrimLimitMessage = %q, not valid UTF-8", pad, got)
		}
		if !strings.HasSuffix(got, "...") {
			t.Errorf("pad %d: TrimLimitMessage = %q, want a truncated message to say so", pad, got)
		}
	}
}
