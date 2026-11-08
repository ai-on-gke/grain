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

package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
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
	return nil
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
			}
			fmt.Printf("  %s (%s): %s\n", s.ID, state, s.Reason)
		}
		fmt.Println()
	}
	hasRegistryIssue := h.Registry != nil && (h.Registry.LastError != "" || len(h.Registry.ScopeConflicts) > 0)
	if hasRegistryIssue {
		fmt.Printf("registry (%s @ %s): %s\n", h.Registry.Entry, h.Registry.URL, h.Registry.State)
		if h.Registry.LastError != "" {
			if h.Registry.ConsecutiveFailures > 0 {
				fmt.Printf("  last error (%d failure(s)): %s\n", h.Registry.ConsecutiveFailures, h.Registry.LastError)
			} else {
				fmt.Printf("  last error: %s\n", h.Registry.LastError)
			}
		}
		for _, c := range h.Registry.ScopeConflicts {
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
		if !hasRegistryIssue && len(h.CapabilityServers) == 0 {
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
