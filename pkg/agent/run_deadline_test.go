package agent_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
)

// The flag a forked mcpserver needs before it can tell a run how long it
// has: the deadline of the very ctx the run is being driven under, in a
// form that survives a fork into another process's environment.
func TestRunDeadlineArgsCarriesTheContextsOwnDeadline(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	ctx, cancel := context.WithDeadline(context.Background(), at)
	defer cancel()

	args := agent.RunDeadlineArgs(ctx, agent.RunConfig{})
	if want := []string{"-run-deadline", "2026-03-04T05:06:07Z"}; !slices.Equal(args, want) {
		t.Fatalf("RunDeadlineArgs = %v, want %v", args, want)
	}

	// The receiving process parses it back as the same instant it was
	// written as, which is the whole reason it is not a local time.
	parsed, err := time.Parse(time.RFC3339, args[1])
	if err != nil {
		t.Fatalf("parsing %q back: %v", args[1], err)
	}
	if !parsed.Equal(at) {
		t.Errorf("parsed = %s, want %s", parsed, at)
	}
}

// A deadline stated in some other zone is still one moment, and that is
// the moment passed on -- an mcpserver forked with a different TZ must
// not read it as a different one.
func TestRunDeadlineArgsNormalizesToUTC(t *testing.T) {
	zone := time.FixedZone("UTC+7", 7*60*60)
	at := time.Date(2026, 3, 4, 12, 0, 0, 0, zone)
	ctx, cancel := context.WithDeadline(context.Background(), at)
	defer cancel()

	args := agent.RunDeadlineArgs(ctx, agent.RunConfig{})
	if want := []string{"-run-deadline", "2026-03-04T05:00:00Z"}; !slices.Equal(args, want) {
		t.Fatalf("RunDeadlineArgs = %v, want %v", args, want)
	}
}

// Nothing at all for a ctx with no deadline -- every in-process caller,
// and any deployment whose run is not bounded here -- so those runs' tool
// results read exactly as they always have rather than carrying a
// countdown to a moment nobody set.
func TestRunDeadlineArgsSaysNothingWithoutADeadline(t *testing.T) {
	if args := agent.RunDeadlineArgs(context.Background(), agent.RunConfig{}); args != nil {
		t.Fatalf("RunDeadlineArgs = %v, want none for a ctx with no deadline", args)
	}
}

// RunConfig.Deadline is what a real dispatch passes now, because the ctx
// it hands framework.Run no longer carries a deadline of its own: the
// run's wall clock is a timer grain resets as the run earns wait credit
// (orchestrator's runClock), and what the fork is told is the fixed
// number that credit is added to.
func TestRunDeadlineArgsPrefersTheConfigsOwnDeadline(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

	args := agent.RunDeadlineArgs(context.Background(), agent.RunConfig{Deadline: at})
	if want := []string{"-run-deadline", "2026-03-04T05:06:07Z"}; !slices.Equal(args, want) {
		t.Fatalf("RunDeadlineArgs = %v, want %v", args, want)
	}

	// And it wins over a ctx that does carry one, which is the case that
	// decides whether a run is told the number it is actually measured
	// against: a ctx deadline here would be some other bound entirely.
	ctx, cancel := context.WithDeadline(context.Background(), at.Add(time.Hour))
	defer cancel()
	if got := agent.RunDeadlineArgs(ctx, agent.RunConfig{Deadline: at}); !slices.Equal(got, args) {
		t.Fatalf("RunDeadlineArgs = %v, want the config's own deadline %v", got, args)
	}
}
