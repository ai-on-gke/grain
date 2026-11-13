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

// health.go is "grain health": the counters a layer built on grain keeps
// in the daemon (ui.RegisterHealthCounters), read off GET /api/sandboxes
// -- the same response System → Health and an agent's `grain health`
// read. -json prints that whole response.
//
// It exits 1 whenever it prints a FAILING section (or, under -json,
// whenever the response carries one), so a monitoring script or a
// `set -e` check can tell a failing deployment from a healthy one by
// status alone (docs/test-runs/2026-11-11-schedule-groups.md, F4).
// WARNING and ALERT sections leave it at 0.

package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func cmdHealth(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain health", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	health, err := c.SandboxHealth(ctx)
	if err != nil {
		return err
	}
	out.health(health)
	if failing := healthFailing(health); len(failing) > 0 {
		return targetFailed(fmt.Errorf("health: failing: %s", strings.Join(failing, ", ")))
	}
	return nil
}

// healthFailing names each FAILING section the printer shows for h, in
// the order it shows them: what makes `grain health` exit 1.
func healthFailing(h ui.SandboxHealthResponse) []string {
	var failing []string
	if len(h.DispatchBlocked) > 0 {
		failing = append(failing, "dispatch is blocked")
	}
	if len(h.CapabilityServers) > 0 {
		failing = append(failing, fmt.Sprintf("%d capability server(s) out of use", len(h.CapabilityServers)))
	}
	if len(h.StaleSchedules) > 0 {
		failing = append(failing, fmt.Sprintf("%d stale schedule(s)", len(h.StaleSchedules)))
	}
	if len(h.RefusedDeclarations) > 0 {
		failing = append(failing, fmt.Sprintf("%d repository declaration set(s) not read in full", len(h.RefusedDeclarations)))
	}
	return failing
}

func (p *printer) health(h ui.SandboxHealthResponse) {
	if p.json {
		p.encode(h)
		return
	}
	// The failing checks first: a deployment whose dispatch is down has
	// nothing more important to say, and a reader who stops at the
	// first line has to see it.
	if len(h.DispatchBlocked) > 0 {
		fmt.Println("FAILING: dispatch is blocked -- no task is dispatched until this clears:")
		for _, c := range h.DispatchBlocked {
			fmt.Printf("  [%s] %s (for %s, since %s)\n", c.Name, c.Error, c.Held, c.Since.Format(time.RFC3339))
			if c.Remedy != "" {
				fmt.Printf("    remedy: %s\n", c.Remedy)
			}
		}
		fmt.Println()
	}
	if h.Daemon != nil && h.Daemon.StoreSwitch != nil {
		fmt.Printf("ALERT (since %s): %s\n\n", h.Daemon.StoreSwitch.Since.UTC().Format("2006-01-02 15:04:05Z"), h.Daemon.StoreSwitch.Message)
	}
	// Capability servers out of use: what the capability.server-down
	// alert names (#647). The response has carried them since, but this
	// printer never did, so `grain health` said "no counters" with a
	// server down (docs/test-runs/2026-11-04-capabilities.md, F7).
	if len(h.CapabilityServers) > 0 {
		fmt.Println("FAILING: capability servers out of use -- no grant is minted from one until it attaches again:")
		for _, s := range h.CapabilityServers {
			state := string(s.State)
			if s.ConfigRefused {
				state = "refused its saved config"
			} else if s.StartError != "" {
				state = s.StartError
			} else if s.Waiting != "" {
				state = "waiting for " + s.Waiting + ", retried every minute"
			}
			fmt.Printf("  %s (%s): %s\n", s.ID, state, s.Reason)
		}
		fmt.Println()
	}
	if len(h.TargetRepoHostWarnings) > 0 {
		fmt.Println("WARNING: target repositories missing their git host:")
		for _, w := range h.TargetRepoHostWarnings {
			fmt.Printf("  %s: %s\n", w.Repo, w.Message)
		}
		fmt.Println()
	}
	// Reviewed-merge logins a person signs in to: the gate passes every
	// merge they make by hand as grain's (task 4386).
	if len(h.MergeLoginWarnings) > 0 {
		fmt.Println("WARNING: reviewed-merge logins that belong to a user account:")
		for _, w := range h.MergeLoginWarnings {
			fmt.Printf("  %s %s: %s\n", w.Repo, w.Login, w.Message)
		}
		fmt.Println()
	}
	// Schedules that stopped running: a cron that comes due and is
	// dropped every time looks exactly like one that is fine from
	// everywhere else (the live model suites, two weeks dark).
	if len(h.StaleSchedules) > 0 {
		fmt.Println("FAILING: schedules with no run in more than twice their cadence:")
		for _, s := range h.StaleSchedules {
			last := "never"
			if s.LastRunAt != nil {
				last = s.LastRunAt.UTC().Format(time.RFC3339)
			}
			name := s.ID
			if s.Workflow != "" {
				name += " (workflow " + s.Workflow + ")"
			}
			fmt.Printf("  %s on %s: every %s, last ran %s\n", name, s.Repo, s.Cadence, last)
			if s.Reason != "" {
				fmt.Printf("    why: %s\n", s.Reason)
			}
		}
		fmt.Println()
	}
	// Declarations a repository merged that this daemon would not read:
	// until grain/task-4392 they were in the journal and nowhere else.
	if len(h.RefusedDeclarations) > 0 {
		fmt.Println("FAILING: repository declarations not read in full:")
		for _, d := range h.RefusedDeclarations {
			inForce := "nothing has loaded yet"
			if d.InForce != "" {
				inForce = "in force: " + shortSHA(d.InForce)
			}
			what := "refused"
			if d.HeldBack {
				what = "files held back for a newer grain"
			}
			fmt.Printf("  %s (%s; %s):\n", d.Repo, what, inForce)
			for _, line := range strings.Split(d.Error, "\n") {
				fmt.Printf("    %s\n", line)
			}
		}
		fmt.Println()
	}
	// Tasks resume_later deferred on a CI verdict grain has not seen yet:
	// the orchestrator's backstop wakes them in the end, but a wait this
	// old usually means a verdict grain is failing to read.
	if len(h.CIWaits) > 0 {
		fmt.Printf("WARNING: tasks waiting on CI for more than %s (resume_later):\n", ui.CIWaitHealthAge)
		for _, w := range h.CIWaits {
			where := w.Branch
			if w.Repo != "" {
				where = w.Repo + ":" + w.Branch
			}
			note := ""
			if w.Recovered {
				note = " (recovered from its resume_later comment)"
			}
			fmt.Printf("  task %s on %s: CI %s (%s), deferred since %s%s\n", w.TaskID, where, w.CI, w.CIMode,
				w.Since.UTC().Format(time.RFC3339), note)
		}
		fmt.Println()
	}
	hasHubIssue := h.Hub != nil && (h.Hub.LastError != "" || len(h.Hub.ScopeConflicts) > 0)
	if hasHubIssue {
		fmt.Printf("hub (%s @ %s): %s\n", h.Hub.Entry, h.Hub.URL, h.Hub.State)
		if h.Hub.LastError != "" {
			if h.Hub.ConsecutiveFailures > 0 {
				fmt.Printf("  last error (%d failure(s)): %s\n", h.Hub.ConsecutiveFailures, h.Hub.LastError)
			} else {
				fmt.Printf("  last error: %s\n", h.Hub.LastError)
			}
		}
		for _, c := range h.Hub.ScopeConflicts {
			scope := c.Repo
			if c.PathPrefix != "" {
				scope += "/" + c.PathPrefix
			}
			if c.ConflictingOwner != "" {
				fmt.Printf("  scope conflict on %s: claimed by %s (%s)\n", scope, c.ConflictingEntry, c.ConflictingOwner)
			} else {
				fmt.Printf("  scope conflict on %s: claimed by %s\n", scope, c.ConflictingEntry)
			}
		}
		if len(h.Counters) > 0 {
			fmt.Println()
		}
	}
	if len(h.Counters) == 0 {
		if !hasHubIssue && len(h.CapabilityServers) == 0 && len(h.TargetRepoHostWarnings) == 0 && len(h.MergeLoginWarnings) == 0 && len(h.StaleSchedules) == 0 && len(h.RefusedDeclarations) == 0 {
			fmt.Println("no counters: nothing on this daemon registers any")
		}
		return
	}
	fmt.Println("counters since the daemon started:")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, section := range h.Counters {
		fmt.Fprintf(w, "\n%s\n", section.Name)
		if len(section.Counters) == 0 {
			fmt.Fprintln(w, "  (none yet)")
			continue
		}
		keys := make([]string, 0, len(section.Counters))
		for k := range section.Counters {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "  %s\t%d\n", k, section.Counters[k])
		}
	}
	_ = w.Flush()
}
