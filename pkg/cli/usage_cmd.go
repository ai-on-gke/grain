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

package cli

// grain usage: token usage and budgets per connection and model, and the
// concurrency the remaining budget sustains (GET /api/usage, PUT
// /api/usage/settings; docs/connections-and-models.md).

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func cmdUsage(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain usage", flag.ContinueOnError)
	mode := fs.String("mode", "", "set the concurrency mode: advisory (estimate only, the default), auto (also cap workers at the estimate) or off")
	minWorkers := fs.Int("min-workers", -1, "set the floor the automatic cap never goes below; unset reads as 1")
	retention := fs.Int("retention-days", -1, "set how many days per-run usage is kept; unset reads as 30")
	var budgets stringList
	fs.Var(&budgets, "budget", "set a budget, connection[/model]=tokens/window (e.g. claude-1/opus=2000000/5h); repeatable, replaces every budget")
	clearBudgets := fs.Bool("clear-budgets", false, "remove every configured budget")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	changing := *mode != "" || *minWorkers >= 0 || *retention >= 0 || len(budgets) > 0 || *clearBudgets
	resp, err := c.Usage(ctx)
	if err != nil {
		return err
	}
	if changing {
		settings := resp.Settings
		if *mode != "" {
			settings.Mode = *mode
		}
		if *minWorkers >= 0 {
			settings.MinWorkers = *minWorkers
		}
		if *retention >= 0 {
			settings.RetentionDays = *retention
		}
		if *clearBudgets {
			settings.Budgets = nil
		}
		if len(budgets) > 0 {
			settings.Budgets = nil
			for _, raw := range budgets {
				b, err := parseBudget(raw)
				if err != nil {
					return err
				}
				settings.Budgets = append(settings.Budgets, b)
			}
		}
		if err := settings.Validate(); err != nil {
			return err
		}
		if resp, err = c.SetUsageSettings(ctx, settings); err != nil {
			return err
		}
	}
	out.usage(resp)
	return nil
}

// parseBudget reads connection[/model]=tokens/window.
func parseBudget(raw string) (model.TokenBudget, error) {
	scope, amount, ok := strings.Cut(strings.TrimSpace(raw), "=")
	if !ok {
		return model.TokenBudget{}, fmt.Errorf("-budget %q: want connection[/model]=tokens/window", raw)
	}
	conn, m, _ := strings.Cut(scope, "/")
	tokens, window, ok := strings.Cut(amount, "/")
	if !ok {
		return model.TokenBudget{}, fmt.Errorf("-budget %q: want connection[/model]=tokens/window", raw)
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(strings.TrimSpace(tokens), "_", ""), 10, 64)
	if err != nil {
		return model.TokenBudget{}, fmt.Errorf("-budget %q: tokens %q is not a whole number", raw, tokens)
	}
	return model.TokenBudget{Connection: strings.TrimSpace(conn), Model: strings.TrimSpace(m), Tokens: n, Window: strings.TrimSpace(window)}, nil
}

// tokens renders a token count compactly: 950, 12.3k, 4.5M.
func tokens(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fG", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return strconv.FormatInt(n, 10)
}

func (p *printer) usage(rep ui.UsageResponse) {
	if p.json {
		p.encode(rep)
		return
	}
	fmt.Printf("mode: %s   max_workers: %d   effective worker cap: %d", rep.Mode, rep.MaxWorkers, rep.EffectiveWorkers)
	if rep.SustainableWorkers != nil {
		fmt.Printf("   sustainable: ≈%d", *rep.SustainableWorkers)
	} else {
		fmt.Printf("   sustainable: unbounded (a connection in use has no known budget)")
	}
	fmt.Println()
	r := rep.Rollup
	since := "no runs recorded"
	if !r.Since.IsZero() {
		since = "since " + r.Since.Format(time.RFC3339)
	}
	fmt.Printf("deployment: %d runs, %s billable (%s in, %s out, %s cache read, %s cache write), %s/h now, %s\n",
		r.Runs, tokens(r.Billable), tokens(r.InputTokens), tokens(r.OutputTokens), tokens(r.CacheReadTokens), tokens(r.CacheWriteTokens),
		tokens(int64(r.BurnPerHour)), since)
	if len(rep.Scopes) > 0 {
		fmt.Println()
		fmt.Printf("%-24s %-22s %9s %9s %9s %-16s %9s %s\n", "CONNECTION", "MODEL", "USED", "LIMIT", "LEFT", "RESET", "BURN/H", "SUSTAINS")
		for _, s := range rep.Scopes {
			conn := s.Connection
			if s.ConnectionName != "" {
				conn = s.ConnectionName
			}
			m := s.Model
			if m == "" {
				m = "(all models)"
			}
			limit, left := "?", "?"
			if s.Limit > 0 {
				limit = tokens(s.Limit)
				if s.LimitSource == "learned" {
					limit += "~"
				}
				left = tokens(s.Remaining)
			}
			if s.PausedUntil != nil {
				left = "paused"
			}
			reset := "-"
			if s.ResetAt != nil {
				reset = s.ResetAt.UTC().Format("01-02 15:04 UTC")
			}
			sustains := "-"
			if s.SustainableWorkers != nil {
				sustains = strconv.FormatFloat(*s.SustainableWorkers, 'f', -1, 64)
			}
			fmt.Printf("%-24s %-22s %9s %9s %9s %-16s %9s %s\n", conn, m, tokens(s.Used), limit, left, reset, tokens(int64(s.BurnPerHour)), sustains)
		}
		fmt.Println("\n(~ marks a limit learned from the usage a window had consumed when its provider refused it)")
	}
	for _, a := range rep.Advisories {
		fmt.Println("advisory: " + a)
	}
	for _, w := range rep.Warnings {
		fmt.Println("warning: " + w)
	}
}
