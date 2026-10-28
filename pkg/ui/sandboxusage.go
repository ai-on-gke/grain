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

package ui

// How many sandboxes this deployment has built over its lifetime, and
// how long they lived (grain/task-315).
//
// The rows are model.SandboxUsage's -- two of them, counters only, and
// its package comment is where what they mean is written down. This is
// the shape they are read in, and the one reading feeds both surfaces
// that want it: the System pane, beside the live sandboxes it is the
// history of, and the metrics report, beside everything else somebody
// costs a change against. One function, so an operator reading the pane
// and an agent reading `grain metrics -json` cannot be told two
// different numbers.

import (
	"context"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// SandboxLifetime is the whole of that answer: a total, and the kinds it
// is made of.
//
// It is explicitly not windowed, which is the difference between this
// and every other number in a metrics report, and the reason it carries
// Since: the counters are an aggregate rather than rows, so there is no
// way to ask what last Tuesday cost, and the honest thing to report is
// the date the counting started (MetricsGitHub says the same about its
// own counts for the same reason). "What did that change cost" is this
// read twice, before and after.
type SandboxLifetime struct {
	// Total is every kind added together, which is the number the
	// question is usually asked in -- "how many sandboxes have we used".
	// Its Kind is empty and its Since is the earliest of the kinds', so
	// a deployment that ran agents for a month before it ran any CI
	// still reports the date its counting began.
	Total SandboxUsageTotals `json:"total"`
	// Kinds is the same, per holder, in kind order ("agent", then "ci").
	// A kind this deployment has never built is absent rather than zero:
	// a deployment that has run no CI has not run cheap CI.
	Kinds []SandboxUsageTotals `json:"kinds,omitempty"`
}

// SandboxUsageTotals is one kind's counters -- or every kind's, for
// SandboxLifetime.Total.
type SandboxUsageTotals struct {
	// Kind is "agent" or "ci", and empty on the total.
	Kind string `json:"kind,omitempty"`
	// Attempted is every sandbox asked for, Created the ones that came
	// up, and Failed the difference: a sandbox that was requested and
	// never started spent something, so it is counted as attempted
	// rather than dropped (model.SandboxUsage).
	Attempted int `json:"attempted"`
	Created   int `json:"created"`
	Failed    int `json:"failed"`
	// Finished is how many were seen to stop, and so how much of the
	// time below is complete. Unaccounted is the rest -- live now, or
	// interrupted by a restart -- and is how the undercount is read
	// rather than an error.
	Finished    int `json:"finished"`
	Unaccounted int `json:"unaccounted"`
	// Seconds and Hours are the same figure: how long the finished ones
	// lived, summed. Both are carried so that the pane, the CLI and
	// anything reading the JSON quote one number rather than each
	// rounding the other's.
	Seconds float64 `json:"seconds"`
	Hours   float64 `json:"hours"`
	// MeanSeconds is Seconds over Finished -- what one sandbox of this
	// kind costs on average, which is what makes two kinds comparable
	// when one of them is built ten times as often.
	MeanSeconds float64 `json:"meanSeconds"`
	// Since is when this kind was first counted, and UpdatedAt when it
	// was last. Zero on a deployment that has built none.
	Since     time.Time `json:"since,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

// Empty reports whether nothing has been counted at all -- a deployment
// that has built no sandbox since this began, which both surfaces show
// as nothing rather than as a row of noughts.
func (l SandboxLifetime) Empty() bool { return l.Total.Attempted == 0 }

// sandboxLifetime reads the counters, or nil where there is no store to
// read them from.
//
// Uncached, unlike the two other store-backed readings on the System
// pane (ciHistoryStorage, and the sandbox health sweep itself): this is
// two rows of integers off a primary key, not a scan of every stored
// step's output, and a stale count is a worse answer than a fresh one is
// expensive.
//
// A failed read is nil, which shows one line fewer rather than taking
// the pane or the report down with it: not being able to say what
// sandboxes have cost is not a reason to stop reporting the machine.
func (c *Client) sandboxLifetime(ctx context.Context) *SandboxLifetime {
	if c.Store == nil {
		return nil
	}
	usages, err := c.Store.SandboxUsages(ctx)
	if err != nil {
		return nil
	}
	lifetime := sandboxLifetimeOf(usages)
	return &lifetime
}

// sandboxLifetimeOf is the rows as one answer: each kind's own totals,
// and the sum of them.
func sandboxLifetimeOf(usages []model.SandboxUsage) SandboxLifetime {
	out := SandboxLifetime{}
	var total model.SandboxUsage
	for _, u := range usages {
		out.Kinds = append(out.Kinds, sandboxUsageTotalsOf(u))
		total.Attempted += u.Attempted
		total.Created += u.Created
		total.Finished += u.Finished
		total.Duration += u.Duration
		// The earliest start and the latest finish, so the total is
		// "since the first sandbox of any kind" and "as at the last one",
		// which is what a reader adding two kinds together means by them.
		if !u.Since.IsZero() && (total.Since.IsZero() || u.Since.Before(total.Since)) {
			total.Since = u.Since
		}
		if u.UpdatedAt.After(total.UpdatedAt) {
			total.UpdatedAt = u.UpdatedAt
		}
	}
	out.Total = sandboxUsageTotalsOf(total)
	out.Total.Kind = ""
	return out
}

func sandboxUsageTotalsOf(u model.SandboxUsage) SandboxUsageTotals {
	out := SandboxUsageTotals{
		Kind:        u.Kind,
		Attempted:   u.Attempted,
		Created:     u.Created,
		Failed:      u.Failed(),
		Finished:    u.Finished,
		Unaccounted: u.Unaccounted(),
		Seconds:     u.Duration.Seconds(),
		Hours:       u.Hours(),
		Since:       u.Since,
		UpdatedAt:   u.UpdatedAt,
	}
	if u.Finished > 0 {
		out.MeanSeconds = u.Duration.Seconds() / float64(u.Finished)
	}
	return out
}
