// failures.go is "grain failures": what runs said when they failed for a
// reason grain could not name -- neither the account's usage limit nor
// the provider having no capacity (pkg/agent/refusal.go, served as GET
// /api/unmatched-failures).
//
// It is not a list of failed tasks, and it is not for finding out why one
// task failed; a task's own attempts say that, and `grain get` prints
// them. This answers a question about grain rather than about the work:
// can it still recognise a provider refusing us? Both refusal detectors
// match phrases somebody else's product writes, so the day one is
// reworded, refusals arrive here instead -- read as ordinary failures,
// counted against the tasks that suffered them, and contradicted by no
// test. Reading a week of this is how that gets noticed. A run of the
// same unfamiliar sentence, or a "looks like a refusal" count that has
// climbed, is the signal; the fix is a marker in pkg/agent.
//
// A task verb like every other one in main.go: it asks a running daemon
// over REST and takes -server and -json from the same global flags.
package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/daemon"

	"github.com/bwsalmon/grain/pkg/ui"
)

func cmdFailures(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain failures", flag.ContinueOnError)
	window := fs.String("window", "", "how far back to read: a duration like 36h, or a count of days or weeks like 7d (default 7d)")
	limit := fs.Int("limit", 0, "at most this many failures (default: the server's own bound)")
	suspect := fs.Bool("suspect", false, "only the ones that read like a provider refusing the run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	// Parsed here as well as by the daemon, so a typo is a message from
	// the command the operator actually ran rather than a 400 relayed
	// back from a server -- the same courtesy `grain metrics` does.
	if _, err := ui.ParseMetricsWindow(*window); err != nil {
		return err
	}
	report, err := c.UnmatchedFailures(ctx, *window, *limit)
	if err != nil {
		return err
	}
	out.failures(report, *suspect)
	return nil
}

func (p *printer) failures(rep ui.UnmatchedFailureReport, suspectOnly bool) {
	if suspectOnly {
		kept := make([]ui.UnmatchedFailure, 0, rep.Suspected)
		for _, f := range rep.Failures {
			if f.Suspect {
				kept = append(kept, f)
			}
		}
		rep.Failures = kept
	}
	if p.json {
		p.encode(rep)
		return
	}
	fmt.Printf("window: %s -> %s (%s)\n",
		rep.Since.Format(time.RFC3339), rep.Until.Format(time.RFC3339),
		daemon.Seconds(rep.WindowSeconds))
	// The two counts before the list, because they are the reading. One
	// failure grain could not classify is a Tuesday; a week of them that
	// all read like a provider refusing us is a marker that has stopped
	// matching.
	fmt.Printf("failures grain could not classify: %d, of which %d read like a provider refusal\n",
		rep.Total, rep.Suspected)
	if rep.Truncated {
		fmt.Println("(more than fit -- ask for a shorter window or a larger -limit)")
	}
	// The cluster's failures, which grain did classify and so are not in
	// the list: beside it because a failure count that climbs while the
	// cluster is failing is the cluster, not a marker that stopped
	// matching.
	infra := rep.Infrastructure
	fmt.Printf("infrastructure: %d runs never reached their agent because the cluster's exec tunnel failed; "+
		"%d more had %d tool calls fail on it mid-run\n",
		infra.ExecTunnelSetups, infra.ExecTunnelToolRuns, infra.ExecTunnelToolCalls)
	if len(rep.Failures) == 0 {
		fmt.Println("\nnothing to show. Either nothing failed, or everything that did was " +
			"recognised as a usage limit or a capacity refusal.")
		return
	}
	fmt.Println()
	for _, f := range rep.Failures {
		mark := " "
		if f.Suspect {
			// Marked rather than sorted to the top: the sequence is what
			// says whether this is one bad afternoon or a change of
			// wording, and reordering it would hide that.
			mark = "*"
		}
		fmt.Printf("%s %s  task %-10s %-12s run %s\n",
			mark, f.At.Format(time.RFC3339), f.TaskID, f.Framework, f.RunID)
		fmt.Printf("    %s\n", strings.TrimSpace(f.Text))
	}
	if rep.Suspected > 0 {
		fmt.Println("\n* reads like the provider refusing the run. If several say the same " +
			"unfamiliar thing, that is a refusal grain has no marker for: see pkg/agent/capacity.go.")
	}
}
