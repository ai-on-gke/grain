package model

// What this deployment's sandboxes have cost it, over its whole life
// rather than at an instant (grain/task-315).
//
// The reading that already existed is a snapshot: ui.SandboxHealth
// reports the sandboxes that are up *now*, with a count beside them,
// which answers "is the machine full" and nothing about yesterday. Asked
// plainly -- "how many sandboxes, agent and CI, have we used so far?" --
// a deployment had no answer at all, and the only route to one was
// counting job-start lines in the journal: a floor rather than a figure,
// unextendable backwards, and lost entirely for any window where a crash
// loop had pushed the lines past the log reader's ceiling.
//
// So the counting is here, in the store, and for one reason above the
// others: it has to survive a restart. The deployment this was written
// for had three upgrade shutdowns and a 52-restart crash loop in a
// single day, and a counter living in the daemon's memory would have
// been reset by every one of them -- it would read "0 sandboxes" at
// exactly the moments somebody most wants the number.
//
// # What is counted
//
// Four counters per kind, and the kinds are the two holders a sandbox
// can have: an agent run's (SandboxKindAgent) and a CI job's
// (SandboxKindCI).
//
//   - Attempted: every sandbox this deployment asked its backend for.
//   - Created: the ones that came up. A sandbox that never started still
//     spent something -- a `konturctl vm create` that ran, an image that
//     was pulled, a job that then waited -- so it is counted as attempted
//     and not as created, rather than dropped. A retry is a second
//     attempt, because it is a second sandbox; nothing is counted twice
//     under the same identity.
//   - Finished: the ones grain saw released, which is the denominator of
//     the time below.
//   - Duration: how long those lived, summed. This is the figure that
//     makes the count worth having: a 36-minute image build and a
//     40-second lint are not the same sandbox in any sense an operator
//     cares about, and only this tells them apart.
//
// # What it cannot say
//
// A sandbox whose daemon died while it was live is counted as created
// and never as finished, and contributes no time at all. That is not
// silent: Created minus Finished is exactly the sandboxes whose ends
// grain never saw (the live ones right now, plus every one a crash or an
// upgrade shutdown took with it), so the size of the undercount is
// readable from the same four numbers. Crediting a guessed duration to
// them would be worse -- nobody knows how long a VM ran after the
// process watching it stopped.
//
// A sandbox rebuilt in place -- the recreate_sandbox tool, which
// destroys a run's VM and brings an empty one back under the same name
// (orchestrator.SandboxRebuilder) -- is not a second sandbox here. It
// keeps the run's name and the run's clock, so its machine time is
// already inside the one measurement that began when the run acquired
// it; counting it again as created, with no separate release to close
// it, would only put the counters out by one.
//
// It is also per kind and nothing finer. "What does kontur cost against
// grain" wants a per-repository and per-job breakdown, which is the
// obvious next step and deliberately not in these rows: see the schema's
// own note on why this is an aggregate rather than a row per sandbox.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SandboxKindAgent and SandboxKindCI are the two holders a sandbox can
// have, and the two keys sandbox_usage is counted under.
//
// They are the same two words orchestrator.SandboxHealth.Kind already
// reports for a live sandbox, spelled once here so that the lifetime
// counters and the live pane cannot drift apart: an operator reading
// "agent" in one place and "agents" in the other has two numbers they
// cannot add up.
const (
	SandboxKindAgent = "agent"
	SandboxKindCI    = "ci"
)

// SandboxUsage is one kind's lifetime counters.
//
// Every field is cumulative and none of them ever goes down: this is a
// meter, not a gauge. A report over a window cannot be taken from it,
// which is the price of the aggregate and is stated wherever it is
// displayed -- the number an operator wants from this is "what has this
// deployment spent since it started counting", and the answer to "what
// did that change cost" is this read twice.
type SandboxUsage struct {
	// Kind is SandboxKindAgent or SandboxKindCI.
	Kind string
	// Attempted is every sandbox asked for; Created is those that came
	// up. Attempted minus Created is how many never started at all.
	Attempted int
	Created   int
	// Finished is how many were released while grain was watching, and
	// so how many contributed to Duration. Created minus Finished is the
	// sandboxes whose ends nobody saw -- the live ones, and the ones a
	// restart took (see the package comment).
	Finished int
	// Duration is how long the finished ones lived, summed: sandbox-time,
	// which is the number this exists for.
	Duration time.Duration
	// Since is when this kind was first counted -- the date every figure
	// above is "since", and the one thing that makes them quotable. A
	// deployment that upgraded into this counting yesterday has not run
	// eleven sandboxes in its life; it has run eleven since yesterday.
	Since time.Time
	// UpdatedAt is when the last of them was counted, which is what says
	// whether a kind is still being used at all.
	UpdatedAt time.Time
}

// Failed is how many sandboxes of this kind were asked for and never
// came up.
func (u SandboxUsage) Failed() int { return u.Attempted - u.Created }

// Unaccounted is how many came up and were never seen to stop: the ones
// live right now, plus every one whose daemon stopped before it did. It
// is how much of Duration is missing rather than an error.
func (u SandboxUsage) Unaccounted() int { return u.Created - u.Finished }

// Hours is Duration in sandbox-hours, which is the unit the figure is
// actually quoted in.
func (u SandboxUsage) Hours() float64 { return u.Duration.Hours() }

// SandboxUsageDelta represents an incremental delta to record for a sandbox kind.
type SandboxUsageDelta struct {
	Kind      string
	Attempted int
	Created   int
	Finished  int
	Duration  time.Duration
	At        time.Time
}

// RecordSandboxUsageDeltas records multiple sandbox usage deltas in a single write transaction.
func (s *Store) RecordSandboxUsageDeltas(ctx context.Context, deltas []SandboxUsageDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	for _, d := range deltas {
		if err := checkSandboxKind(d.Kind); err != nil {
			return err
		}
	}
	desc := "count sandbox usages batch"
	if len(deltas) == 1 {
		desc = "count a " + deltas[0].Kind + " sandbox"
	}
	return s.write(ctx, desc, func(tx *sql.Tx) error {
		for _, d := range deltas {
			lived := d.Duration
			if lived < 0 {
				lived = 0
			}
			_, err := tx.ExecContext(ctx,
				"INSERT INTO `sandbox_usage` (`kind`,`attempted`,`created`,`finished`,`duration_ms`,`since`,`updated_at`) "+
					"VALUES (?,?,?,?,?,?,?) "+
					// The counters add rather than replace, which is the
					// whole of "durable across restarts": this process
					// contributes what it saw to whatever every process
					// before it saw. `since` is deliberately not updated --
					// the first write of a kind is the date it has been
					// counting from.
					"ON CONFLICT (`kind`) DO UPDATE SET "+
					"`attempted` = `attempted` + excluded.`attempted`, "+
					"`created` = `created` + excluded.`created`, "+
					"`finished` = `finished` + excluded.`finished`, "+
					"`duration_ms` = `duration_ms` + excluded.`duration_ms`, "+
					"`updated_at` = excluded.`updated_at`",
				d.Kind, d.Attempted, d.Created, d.Finished, lived.Milliseconds(), d.At.UTC(), d.At.UTC())
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// RecordSandboxStart counts one sandbox this deployment asked for, and
// whether it came up.
//
// One row per kind, created on first use with `since` stamped, so the
// counters carry their own start date rather than needing a separate
// note of when counting began.
//
// at is the moment to attribute it to, which the caller passes rather
// than this reading a clock: every other write in this package takes its
// time from the caller for the same reason (a test that cannot control
// the clock cannot test a duration).
func (s *Store) RecordSandboxStart(ctx context.Context, kind string, created bool, at time.Time) error {
	createdCount := 0
	if created {
		createdCount = 1
	}
	return s.RecordSandboxUsageDeltas(ctx, []SandboxUsageDelta{{
		Kind:      kind,
		Attempted: 1,
		Created:   createdCount,
		At:        at,
	}})
}

// RecordSandboxFinish counts one sandbox's life: that it ended, and how
// long it lasted.
//
// A negative duration is counted as zero rather than subtracted -- it
// can only come from a clock that went backwards, and a meter that can go
// down is worse than one that is missing a sandbox's time. The finish
// itself still counts.
func (s *Store) RecordSandboxFinish(ctx context.Context, kind string, lived time.Duration, at time.Time) error {
	return s.RecordSandboxUsageDeltas(ctx, []SandboxUsageDelta{{
		Kind:     kind,
		Finished: 1,
		Duration: lived,
		At:       at,
	}})
}

// SandboxUsages is every kind's lifetime counters, in kind order so two
// readings of the same deployment read the same way.
//
// A kind with no row has never been used and is left out rather than
// reported as zeroes: "this deployment has run no CI" and "this
// deployment has run CI that cost nothing" are different answers.
func (s *Store) SandboxUsages(ctx context.Context) ([]SandboxUsage, error) {
	var out []SandboxUsage
	err := each(ctx, s.db,
		"SELECT `kind`,`attempted`,`created`,`finished`,`duration_ms`,`since`,`updated_at` "+
			"FROM `sandbox_usage` ORDER BY `kind`", nil,
		func(rows *sql.Rows) error {
			var u SandboxUsage
			var ms int64
			var since, updated sql.NullTime
			if err := rows.Scan(&u.Kind, &u.Attempted, &u.Created, &u.Finished, &ms, &since, &updated); err != nil {
				return err
			}
			u.Duration = time.Duration(ms) * time.Millisecond
			if since.Valid {
				u.Since = since.Time.UTC()
			}
			if updated.Valid {
				u.UpdatedAt = updated.Time.UTC()
			}
			out = append(out, u)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading sandbox usage: %w", err)
	}
	return out, nil
}

// checkSandboxKind refuses a kind that is not one of the two, so that a
// typo at a call site becomes an error on that write rather than a third
// row nobody adds up.
func checkSandboxKind(kind string) error {
	switch kind {
	case SandboxKindAgent, SandboxKindCI:
		return nil
	default:
		return errors.New("model: a sandbox is counted as " + SandboxKindAgent + " or " + SandboxKindCI + ", not " + kind)
	}
}
