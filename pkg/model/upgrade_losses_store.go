package model

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// RunEnd is one run as the post-upgrade check reports it: which task it
// was, which agent framework drove it, and how it ended.
type RunEnd struct {
	Run, Task string
	// Framework is task_run.agent_framework: claude, codex, antigravity,
	// or empty when the run ended before its framework was chosen (a run
	// still in setup: its sandbox building, no CLI started).
	Framework string
	Outcome   string
	Detail    string
}

// InterruptedRunsSince is every run that ended interrupted at or after
// since, oldest first. The post-upgrade check reads it from the upgrade's
// start: a run whose CLI was a child of the daemon (agent.LocalHost) is
// finished as interrupted by the daemon that is stopping
// (orchestrator.InterruptedRuns, DrainInFlight) or by the next one at
// startup (RecoverOrphanedRuns), and neither shows up in the adoption
// pass, which only sees run hosts and grain hostd's processes. Without
// this the check said "1 adopted, 0 lost" over an upgrade that had lost
// eight runs (task 1244).
func (s *Store) InterruptedRunsSince(ctx context.Context, since time.Time) ([]RunEnd, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `id`,`task_id`,`agent_framework`,`detail` FROM `task_run` "+
			"WHERE `outcome` = ? AND `finished_at` >= ? ORDER BY `finished_at`, `id`",
		InterruptedOutcome, since.UTC())
	if err != nil {
		return nil, fmt.Errorf("listing the runs interrupted since %s: %w", since.UTC().Format(time.RFC3339), err)
	}
	defer rows.Close()
	var out []RunEnd
	for rows.Next() {
		var r RunEnd
		var framework, detail sql.NullString
		if err := rows.Scan(&r.Run, &r.Task, &framework, &detail); err != nil {
			return nil, err
		}
		r.Framework, r.Outcome, r.Detail = framework.String, InterruptedOutcome, detail.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunFrameworks is task_run.agent_framework for each of runs that has
// one, keyed by run id; a run with none, or no row, is absent.
func (s *Store) RunFrameworks(ctx context.Context, runs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(runs) == 0 {
		return out, nil
	}
	args := make([]any, len(runs))
	for i, r := range runs {
		args[i] = r
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT `id`,`agent_framework` FROM `task_run` WHERE `id` IN (?"+strings.Repeat(",?", len(runs)-1)+")", args...)
	if err != nil {
		return nil, fmt.Errorf("reading the frameworks of %d run(s): %w", len(runs), err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var framework sql.NullString
		if err := rows.Scan(&id, &framework); err != nil {
			return nil, err
		}
		if framework.String != "" {
			out[id] = framework.String
		}
	}
	return out, rows.Err()
}

// LiveRunSubstrate is one live run, and whether a daemon restart would
// leave it running for the next daemon to adopt.
type LiveRunSubstrate struct {
	Run, Task string
	// Framework is task_run.agent_framework, empty for a run still in
	// setup (RunEnd.Framework).
	Framework string
	// Detached is whether the run's CLI was started where it outlives
	// the daemon: in a run host (runhost.Host) or detached in grain hostd
	// (runhost.Hostd). Both persist a spool cursor at 0 the moment the
	// CLI starts, which is what marks the run as one driven that way. A
	// run without one is a child of the daemon (agent.LocalHost) or has
	// not started its CLI yet, and a restart loses it.
	Detached bool
}

// LiveRunSubstrates is every live run, oldest first, with whether a
// restart would lose it: what the upgrade pane says before an upgrade,
// by framework, beside the count of runs in flight.
func (s *Store) LiveRunSubstrates(ctx context.Context) ([]LiveRunSubstrate, error) {
	var out []LiveRunSubstrate
	err := each(ctx, s.db,
		"SELECT `r`.`id`,`r`.`task_id`,`r`.`agent_framework`,"+
			"EXISTS (SELECT 1 FROM `task_run_spool_cursor` AS `c` WHERE `c`.`run_id` IN (`r`.`id`, '"+hostdCursorPrefix+"' || `r`.`id`)) "+
			"FROM `task_run` AS `r` WHERE `r`.`finished_at` IS NULL ORDER BY `r`.`started_at`, `r`.`id`",
		nil,
		func(rows *sql.Rows) error {
			var r LiveRunSubstrate
			var framework sql.NullString
			if err := rows.Scan(&r.Run, &r.Task, &framework, &r.Detached); err != nil {
				return err
			}
			r.Framework = framework.String
			out = append(out, r)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing the live runs' substrates: %w", err)
	}
	return out, nil
}
