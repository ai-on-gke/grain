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

// Package usage turns recorded token usage (model.RunUsage), the
// operator's budgets, learned capacities and the usage-limit pauses in
// force into a budget report per connection and model, and from it the
// concurrency the remaining budget can sustain until the next reset
// (grain/task-3267).
//
// Everything here is arithmetic over its Input: no store, no clock of its
// own. Settings → Models, `grain usage`, GET /api/usage and the
// automatic worker cap all read the same Report, so they cannot disagree.
//
// # The model
//
// A scope is a connection (the credential owner a pause is filed under)
// and a model on it; a scope with no model is the connection as a whole,
// which a connection-wide budget or a connection-wide pause describes.
//
//   - Limit is the operator's configured budget for the scope
//     (model.TokenBudget), else the usage the last window consumed before
//     its provider refused it (model.UsageCapacity, learned when a pause
//     opened), else unknown.
//   - Window is the budget's window, else the learned one, else
//     DefaultWindow. Used is the billable tokens of the scope's runs
//     that finished inside it -- since the last known reset, where a
//     reset has passed inside the window.
//   - Burn rate is the billable tokens finished in the last BurnWindow,
//     per hour; RunsOutAt is when Remaining runs out at that rate.
//   - Sustainable workers is Remaining / (tokens per worker-hour x hours
//     until the reset), with tokens per worker-hour taken from the
//     scope's own runs (billable tokens over run-hours).
//   - The weekly horizon (weekly.go) is the same over WeeklyWindow: the
//     tokens the scope's runs spent in the last week, the weekly
//     capacity learned when a weekly limit refused it, and the weekly
//     pause in force. A known weekly limit bounds sustainable workers
//     as the interval's does.
//
// A usage-limit pause is the source of truth for "refused now": a paused
// scope has nothing remaining and sustains no workers until its pause
// ends, whatever the estimate said.
package usage

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// DefaultWindow is the window a scope with no configured or learned one
// is reported over: Claude's session window, the shortest of the
// provider windows grain meets.
const DefaultWindow = 5 * time.Hour

// BurnWindow is how far back the burn rate looks.
const BurnWindow = time.Hour

// Lookback is how far back a caller should read runs for an Input:
// AverageSpan, and at least WeeklyWindow, so the weekly horizon has its
// week of runs to count.
func Lookback(settings model.UsageSettings, capacities []model.UsageCapacity) time.Duration {
	longest := max(AverageSpan(settings, capacities), WeeklyWindow)
	if r := settings.Retention(); longest > r {
		longest = r
	}
	return longest
}

// AverageSpan is how far back the rollup and a scope's per-run averages
// (AvgTokensPerRun, tokens per worker-hour) look: the longest interval
// window any scope is reported over, and at least a day, so the average
// has something to average. The weekly horizon does not lengthen it: a
// week of runs would shift every interval estimate, and the auto worker
// cap with it, on a deployment that knows no weekly limit at all.
func AverageSpan(settings model.UsageSettings, capacities []model.UsageCapacity) time.Duration {
	longest := 24 * time.Hour
	for _, b := range settings.Budgets {
		if d := b.WindowDuration(); d > longest {
			longest = d
		}
	}
	for _, c := range capacities {
		if _, weekly := splitCapacityModel(norm(c.Model)); !weekly && c.Window > longest {
			longest = c.Window
		}
	}
	if r := settings.Retention(); longest > r {
		longest = r
	}
	return longest
}

// Input is everything a Report is computed from.
type Input struct {
	Now        time.Time
	Runs       []model.RunUsage
	Settings   model.UsageSettings
	Capacities []model.UsageCapacity
	// Pauses are the usage-limit pauses in force.
	Pauses []model.UsageLimitPause
	// DefaultModels is the deployment's default model list, first to
	// last (model.ParseDefaultModels).
	DefaultModels []string
	// MaxWorkers is the operator's ceiling (model.Config.MaxWorkers).
	MaxWorkers int
	// ConnectionNames maps a connection id to its display name, for
	// messages. Optional.
	ConnectionNames map[string]string
}

// Scope is one connection-and-model's usage and budget.
type Scope struct {
	Connection string `json:"connection"`
	// ConnectionName is the connection's display name, where known.
	ConnectionName string `json:"connectionName,omitempty"`
	// Model is "" for the connection as a whole.
	Model string `json:"model,omitempty"`
	// Runs, the token counts and Used are over the current window.
	Runs             int       `json:"runs"`
	InputTokens      int64     `json:"inputTokens"`
	OutputTokens     int64     `json:"outputTokens"`
	CacheReadTokens  int64     `json:"cacheReadTokens"`
	CacheWriteTokens int64     `json:"cacheWriteTokens"`
	CostUSD          float64   `json:"costUsd,omitempty"`
	Used             int64     `json:"used"`
	WindowStart      time.Time `json:"windowStart"`
	Window           string    `json:"window"`
	// Limit is 0 when unknown; LimitSource says where a known one came
	// from: "configured" or "learned".
	Limit       int64  `json:"limit,omitempty"`
	LimitSource string `json:"limitSource,omitempty"`
	// Remaining is Limit - Used, floored at 0; meaningful only with a
	// Limit (or a pause, which makes it 0).
	Remaining int64 `json:"remaining,omitempty"`
	// ResetAt is the next known reset: a pause's end, a learned reset
	// time still ahead, or -- for a rolling window -- when the oldest
	// usage in it ages out.
	ResetAt     *time.Time `json:"resetAt,omitempty"`
	PausedUntil *time.Time `json:"pausedUntil,omitempty"`
	// BurnPerHour is billable tokens per hour over the last BurnWindow.
	BurnPerHour float64 `json:"burnPerHour"`
	// RunsOutAt is when Remaining runs out at BurnPerHour; nil without
	// a Limit or a burn.
	RunsOutAt *time.Time `json:"runsOutAt,omitempty"`
	// AvgTokensPerRun and AvgRunMinutes are over the runs of the last
	// AverageSpan (not just the window).
	AvgTokensPerRun float64 `json:"avgTokensPerRun,omitempty"`
	AvgRunMinutes   float64 `json:"avgRunMinutes,omitempty"`
	// SustainableWorkers is how many concurrent runs Remaining can
	// sustain until ResetAt; nil when unbounded or unknown.
	SustainableWorkers *float64 `json:"sustainableWorkers,omitempty"`
	// PauseHorizon is which limit the pause in force met: HorizonWeekly
	// or HorizonInterval; "" while the scope is not paused.
	PauseHorizon string `json:"pauseHorizon,omitempty"`

	// The weekly horizon: WeeklyRuns and WeeklyUsed are the scope's runs
	// and billable tokens since WeeklyWindowStart (WeeklyWindow ago, or
	// the last known weekly reset inside it).
	WeeklyRuns        int       `json:"weeklyRuns"`
	WeeklyUsed        int64     `json:"weeklyUsed"`
	WeeklyWindowStart time.Time `json:"weeklyWindowStart"`
	// WeeklyLimit is the weekly capacity learned when a weekly limit last
	// refused the scope, 0 when unknown; WeeklyLimitSource is "learned"
	// with one.
	WeeklyLimit       int64  `json:"weeklyLimit,omitempty"`
	WeeklyLimitSource string `json:"weeklyLimitSource,omitempty"`
	// WeeklyRemaining is WeeklyLimit - WeeklyUsed, floored at 0; 0 too
	// while a weekly pause is in force.
	WeeklyRemaining int64 `json:"weeklyRemaining,omitempty"`
	// WeeklyResetAt is the weekly pause's end, a learned weekly reset
	// still ahead, or when the oldest usage in the week ages out.
	WeeklyResetAt *time.Time `json:"weeklyResetAt,omitempty"`
	// WeeklyPausedUntil is set while a weekly limit pauses the scope (or
	// its whole connection): the weekly quota is spent until then,
	// whatever the interval shows.
	WeeklyPausedUntil *time.Time `json:"weeklyPausedUntil,omitempty"`
	// WeeklyRunsOutAt is when WeeklyRemaining runs out at BurnPerHour;
	// nil without a WeeklyLimit or a burn.
	WeeklyRunsOutAt *time.Time `json:"weeklyRunsOutAt,omitempty"`

	window              time.Duration
	tokensPerWorkerHour float64
}

// Totals is the deployment rollup.
type Totals struct {
	Runs             int       `json:"runs"`
	InputTokens      int64     `json:"inputTokens"`
	OutputTokens     int64     `json:"outputTokens"`
	CacheReadTokens  int64     `json:"cacheReadTokens"`
	CacheWriteTokens int64     `json:"cacheWriteTokens"`
	CostUSD          float64   `json:"costUsd,omitempty"`
	Billable         int64     `json:"billable"`
	BurnPerHour      float64   `json:"burnPerHour"`
	Since            time.Time `json:"since"`
}

// Report is the whole answer.
type Report struct {
	GeneratedAt time.Time `json:"generatedAt"`
	// Mode is the effective concurrency mode (model.UsageSettings).
	Mode       string  `json:"mode"`
	MaxWorkers int     `json:"maxWorkers"`
	MinWorkers int     `json:"minWorkers"`
	Scopes     []Scope `json:"scopes"`
	// Rollup is every run of the last AverageSpan, across every scope.
	Rollup Totals `json:"rollup"`
	// SustainableWorkers is the deployment-wide estimate; nil when some
	// connection in use has no known budget, so nothing bounds it.
	SustainableWorkers *int `json:"sustainableWorkers,omitempty"`
	// EffectiveWorkers is the worker cap in effect: MaxWorkers, unless
	// Mode is auto and the estimate is lower.
	EffectiveWorkers int `json:"effectiveWorkers"`
	// Advisories are the estimate in words; Warnings are default-list
	// models about to run out before their reset.
	Advisories []string `json:"advisories,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

type scopeKey struct{ connection, model string }

func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Compute builds the Report for in.
func Compute(in Input) Report {
	now := in.Now
	settings := in.Settings
	minWorkers := settings.MinWorkers
	if minWorkers < 1 {
		minWorkers = 1
	}
	rep := Report{GeneratedAt: now, Mode: settings.EffectiveMode(), MaxWorkers: in.MaxWorkers, MinWorkers: minWorkers,
		EffectiveWorkers: in.MaxWorkers, Scopes: []Scope{}}

	// The rollup: every run over the averaging span.
	avgSince := now.Add(-AverageSpan(settings, in.Capacities))
	for _, r := range in.Runs {
		if r.FinishedAt.Before(avgSince) {
			continue
		}
		rep.Rollup.Runs++
		rep.Rollup.InputTokens += r.InputTokens
		rep.Rollup.OutputTokens += r.OutputTokens
		rep.Rollup.CacheReadTokens += r.CacheReadTokens
		rep.Rollup.CacheWriteTokens += r.CacheWriteTokens
		rep.Rollup.CostUSD += r.CostUSD
		rep.Rollup.Billable += r.Billable()
		if rep.Rollup.Since.IsZero() || r.FinishedAt.Before(rep.Rollup.Since) {
			rep.Rollup.Since = r.FinishedAt
		}
		if !r.FinishedAt.Before(now.Add(-BurnWindow)) {
			rep.Rollup.BurnPerHour += float64(r.Billable()) / BurnWindow.Hours()
		}
	}
	if rep.Mode == model.UsageModeOff {
		return rep
	}

	budgets := map[scopeKey]model.TokenBudget{}
	for _, b := range settings.Budgets {
		budgets[scopeKey{strings.TrimSpace(b.Connection), norm(b.Model)}] = b
	}
	learned := map[scopeKey]model.UsageCapacity{}
	learnedWeekly := map[scopeKey]model.UsageCapacity{}
	for _, c := range in.Capacities {
		m, weekly := splitCapacityModel(norm(c.Model))
		if weekly {
			learnedWeekly[scopeKey{c.Connection, m}] = c
		} else {
			learned[scopeKey{c.Connection, m}] = c
		}
	}
	paused := map[scopeKey]time.Time{}
	pausedWeekly := map[scopeKey]time.Time{}
	for _, p := range in.Pauses {
		if p.Connection == "" || !p.Until.After(now) {
			continue
		}
		k := scopeKey{p.Connection, norm(p.Model)}
		if p.Until.After(paused[k]) {
			paused[k] = p.Until
		}
		if PauseHorizon(p) == HorizonWeekly && p.Until.After(pausedWeekly[k]) {
			pausedWeekly[k] = p.Until
		}
	}
	lim := limits{avgSince: avgSince, budgets: budgets, learned: learned, learnedWeekly: learnedWeekly, paused: paused, pausedWeekly: pausedWeekly}

	keys := map[scopeKey]bool{}
	for _, r := range in.Runs {
		if r.Connection != "" {
			keys[scopeKey{r.Connection, norm(r.Model)}] = true
		}
	}
	for k := range budgets {
		keys[k] = true
	}
	for k := range learned {
		keys[k] = true
	}
	for k := range learnedWeekly {
		keys[k] = true
	}
	for k := range paused {
		keys[k] = true
	}
	ordered := make([]scopeKey, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].connection != ordered[j].connection {
			return ordered[i].connection < ordered[j].connection
		}
		return ordered[i].model < ordered[j].model
	})

	for _, k := range ordered {
		rep.Scopes = append(rep.Scopes, computeScope(in, k, lim))
	}

	// The deployment estimate: per connection, its connection-wide
	// scope where that has a bound, else the sum of its models'.
	unbounded := false
	total := 0.0
	byConn := map[string][]Scope{}
	var conns []string
	for _, s := range rep.Scopes {
		if _, ok := byConn[s.Connection]; !ok {
			conns = append(conns, s.Connection)
		}
		byConn[s.Connection] = append(byConn[s.Connection], s)
	}
	for _, c := range conns {
		v, bounded := connectionSustainable(byConn[c])
		if !bounded {
			unbounded = true
			continue
		}
		total += v
	}
	if !unbounded && len(conns) > 0 {
		n := int(math.Floor(total))
		rep.SustainableWorkers = &n
		if rep.Mode == model.UsageModeAuto && in.MaxWorkers > 0 {
			rep.EffectiveWorkers = clampWorkers(n, minWorkers, in.MaxWorkers)
		}
	}
	rep.Advisories, rep.Warnings = messages(in, rep)
	return rep
}

// clampWorkers is n within [floor, ceiling], ceiling winning: max_workers
// is never exceeded, even by a floor set above it.
func clampWorkers(n, floor, ceiling int) int {
	if n < floor {
		n = floor
	}
	if n > ceiling {
		n = ceiling
	}
	return n
}

// connectionSustainable is a connection's sustainable workers from its
// scopes, and whether anything bounds it.
func connectionSustainable(scopes []Scope) (float64, bool) {
	for _, s := range scopes {
		if s.Model == "" && s.SustainableWorkers != nil {
			return *s.SustainableWorkers, true
		}
	}
	sum := 0.0
	any := false
	for _, s := range scopes {
		if s.Model == "" {
			continue
		}
		if s.SustainableWorkers == nil {
			return 0, false
		}
		sum += *s.SustainableWorkers
		any = true
	}
	return sum, any
}

func (s Scope) matches(r model.RunUsage) bool {
	return r.Connection == s.Connection && (s.Model == "" || norm(r.Model) == s.Model)
}

// limits is what Compute read out of its Input for computeScope: the
// budgets, the learned interval and weekly capacities, and the pauses in
// force -- all of them, and the weekly ones alone.
type limits struct {
	// avgSince is where the per-run averages start (AverageSpan).
	avgSince      time.Time
	budgets       map[scopeKey]model.TokenBudget
	learned       map[scopeKey]model.UsageCapacity
	learnedWeekly map[scopeKey]model.UsageCapacity
	paused        map[scopeKey]time.Time
	pausedWeekly  map[scopeKey]time.Time
}

// pausedOn is the latest end of a pause in pauses on k or on k's whole
// connection.
func pausedOn(pauses map[scopeKey]time.Time, k scopeKey) (time.Time, bool) {
	until, ok := pauses[k]
	if whole, wok := pauses[scopeKey{k.connection, ""}]; wok && whole.After(until) {
		until, ok = whole, true
	}
	return until, ok
}

func computeScope(in Input, k scopeKey, lim limits) Scope {
	budgets, learned := lim.budgets, lim.learned
	now := in.Now
	s := Scope{Connection: k.connection, Model: k.model, ConnectionName: in.ConnectionNames[k.connection]}
	s.window = DefaultWindow
	if b, ok := budgets[k]; ok {
		s.Limit, s.LimitSource, s.window = b.Tokens, "configured", b.WindowDuration()
	} else if c, ok := learned[k]; ok && c.Tokens > 0 {
		s.Limit, s.LimitSource = c.Tokens, "learned"
		if c.Window > 0 {
			s.window = c.Window
		}
	}
	s.Window = s.window.String()
	start := now.Add(-s.window)
	var reset *time.Time
	knownReset := false
	if c, ok := learned[k]; ok && !c.ResetAt.IsZero() {
		if c.ResetAt.After(start) && !c.ResetAt.After(now) {
			// The provider reset this window inside it: nothing before
			// the reset counts against what is left.
			start = c.ResetAt
		} else if c.ResetAt.After(now) {
			t := c.ResetAt
			reset, knownReset = &t, true
		}
	}
	// A pause on the scope, or on its whole connection, is in force.
	until, isPaused := pausedOn(lim.paused, k)
	weeklyUntil, weeklyPaused := pausedOn(lim.pausedWeekly, k)
	if isPaused {
		t := until
		s.PausedUntil = &t
		reset = &t
		s.PauseHorizon = HorizonInterval
		if weeklyPaused {
			s.PauseHorizon = HorizonWeekly
		}
	}
	s.WindowStart = start

	// The weekly horizon's start, limit and known reset.
	weeklyStart := now.Add(-WeeklyWindow)
	var weeklyReset *time.Time
	knownWeeklyReset := false
	if c, ok := lim.learnedWeekly[k]; ok {
		if c.Tokens > 0 {
			s.WeeklyLimit, s.WeeklyLimitSource = c.Tokens, "learned"
		}
		if !c.ResetAt.IsZero() {
			// The weekly quota resets on a fixed weekly cycle: a reset
			// already past puts the next one a week after it (or after
			// the latest one since), and the week began at the last.
			next := c.ResetAt
			for !next.After(now) {
				next = next.Add(WeeklyWindow)
			}
			if last := next.Add(-WeeklyWindow); last.After(weeklyStart) {
				weeklyStart = last
			}
			weeklyReset, knownWeeklyReset = &next, true
		}
	}
	if weeklyPaused {
		t := weeklyUntil
		s.WeeklyPausedUntil = &t
		weeklyReset, knownWeeklyReset = &t, true
	}
	s.WeeklyWindowStart = weeklyStart
	var weeklyOldest time.Time

	var lookTokens int64
	var lookHours float64
	var lookRuns int
	var oldest time.Time
	for _, r := range in.Runs {
		if !s.matches(r) {
			continue
		}
		if !r.FinishedAt.Before(lim.avgSince) {
			lookRuns++
			lookTokens += r.Billable()
			lookHours += r.Duration().Hours()
		}
		if !r.FinishedAt.Before(weeklyStart) {
			s.WeeklyRuns++
			s.WeeklyUsed += r.Billable()
			if weeklyOldest.IsZero() || r.FinishedAt.Before(weeklyOldest) {
				weeklyOldest = r.FinishedAt
			}
		}
		if r.FinishedAt.Before(start) {
			continue
		}
		s.Runs++
		s.InputTokens += r.InputTokens
		s.OutputTokens += r.OutputTokens
		s.CacheReadTokens += r.CacheReadTokens
		s.CacheWriteTokens += r.CacheWriteTokens
		s.CostUSD += r.CostUSD
		s.Used += r.Billable()
		if oldest.IsZero() || r.FinishedAt.Before(oldest) {
			oldest = r.FinishedAt
		}
		if !r.FinishedAt.Before(now.Add(-BurnWindow)) {
			s.BurnPerHour += float64(r.Billable()) / BurnWindow.Hours()
		}
	}
	if lookRuns > 0 {
		s.AvgTokensPerRun = float64(lookTokens) / float64(lookRuns)
		s.AvgRunMinutes = lookHours * 60 / float64(lookRuns)
		if lookHours > 0 {
			s.tokensPerWorkerHour = float64(lookTokens) / lookHours
		}
	}
	if reset == nil && !oldest.IsZero() {
		t := oldest.Add(s.window)
		reset = &t
	}
	s.ResetAt = reset
	if weeklyReset == nil && !weeklyOldest.IsZero() {
		t := weeklyOldest.Add(WeeklyWindow)
		weeklyReset = &t
	}
	s.WeeklyResetAt = weeklyReset

	switch {
	case weeklyPaused:
		s.WeeklyRemaining = 0
	case s.WeeklyLimit > 0:
		s.WeeklyRemaining = max(s.WeeklyLimit-s.WeeklyUsed, 0)
	}

	switch {
	case isPaused:
		s.Remaining = 0
		zero := 0.0
		s.SustainableWorkers = &zero
		return s
	case s.Limit > 0:
		s.Remaining = s.Limit - s.Used
		if s.Remaining < 0 {
			s.Remaining = 0
		}
		if s.BurnPerHour > 0 {
			t := now.Add(time.Duration(float64(s.Remaining) / s.BurnPerHour * float64(time.Hour)))
			s.RunsOutAt = &t
		}
		if s.tokensPerWorkerHour > 0 {
			horizon := s.window
			if knownReset {
				horizon = s.ResetAt.Sub(now)
			}
			if horizon < 15*time.Minute {
				horizon = 15 * time.Minute
			}
			v := float64(s.Remaining) / (s.tokensPerWorkerHour * horizon.Hours())
			v = math.Round(v*10) / 10
			s.SustainableWorkers = &v
		}
	}
	// A known weekly limit bounds the scope too: what is left of the week
	// spread over the hours until the week resets.
	if s.WeeklyLimit > 0 {
		if s.BurnPerHour > 0 {
			t := now.Add(time.Duration(float64(s.WeeklyRemaining) / s.BurnPerHour * float64(time.Hour)))
			s.WeeklyRunsOutAt = &t
		}
		if s.tokensPerWorkerHour > 0 {
			horizon := WeeklyWindow
			if knownWeeklyReset {
				horizon = s.WeeklyResetAt.Sub(now)
			}
			if horizon < 15*time.Minute {
				horizon = 15 * time.Minute
			}
			v := float64(s.WeeklyRemaining) / (s.tokensPerWorkerHour * horizon.Hours())
			v = math.Round(v*10) / 10
			if s.SustainableWorkers == nil || v < *s.SustainableWorkers {
				s.SustainableWorkers = &v
			}
		}
	}
	return s
}

func (s Scope) connectionLabel() string {
	if s.ConnectionName != "" {
		return s.ConnectionName
	}
	return s.Connection
}

func (s Scope) label() string {
	name := s.connectionLabel()
	if s.Model == "" {
		return name
	}
	return name + "'s " + s.Model
}

func roughly(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("~%dm", int(d.Minutes()+0.5))
	case d < 48*time.Hour:
		return fmt.Sprintf("~%.0fh", d.Hours())
	}
	return fmt.Sprintf("~%.0fd", d.Hours()/24)
}

func messages(in Input, rep Report) (advisories, warnings []string) {
	now := in.Now
	for _, s := range rep.Scopes {
		if s.PausedUntil != nil {
			continue
		}
		if s.RunsOutAt != nil && s.SustainableWorkers != nil {
			advisories = append(advisories, fmt.Sprintf("at this rate %s budget runs out in %s; sustainable ≈ %s workers",
				s.label(), roughly(s.RunsOutAt.Sub(now)), trimFloat(*s.SustainableWorkers)))
		}
		if s.WeeklyRunsOutAt != nil && s.SustainableWorkers != nil {
			advisories = append(advisories, fmt.Sprintf("at this rate %s weekly limit runs out in %s; sustainable ≈ %s workers",
				s.label(), roughly(s.WeeklyRunsOutAt.Sub(now)), trimFloat(*s.SustainableWorkers)))
		}
	}
	if rep.SustainableWorkers != nil {
		line := fmt.Sprintf("the remaining budget sustains ≈ %d workers until the next reset (max_workers is %d)", *rep.SustainableWorkers, in.MaxWorkers)
		if rep.Mode == model.UsageModeAuto {
			line += fmt.Sprintf("; the automatic cap holds dispatch at %d", rep.EffectiveWorkers)
		}
		advisories = append(advisories, line)
	}
	for i, m := range in.DefaultModels {
		m = norm(m)
		for _, s := range rep.Scopes {
			if s.Model != m {
				continue
			}
			next := "nothing -- it is the last model on the default list"
			if i+1 < len(in.DefaultModels) {
				next = in.DefaultModels[i+1]
			}
			switch {
			case s.WeeklyPausedUntil != nil:
				warnings = append(warnings, fmt.Sprintf("default model %s on %s hit its weekly limit and is paused until %s; work falls back to %s",
					s.Model, s.connectionLabel(), untilText(now, *s.WeeklyPausedUntil), next))
			case s.PausedUntil != nil:
				warnings = append(warnings, fmt.Sprintf("default model %s on %s hit its %s interval limit and is paused until %s; work falls back to %s",
					s.Model, s.connectionLabel(), shortWindow(s.window), untilText(now, *s.PausedUntil), next))
			// Each horizon warns when it runs out before its own reset;
			// where both would, the one that runs out first is said.
			case weeklyWarns(s) && (!intervalWarns(s) || s.WeeklyRunsOutAt.Before(*s.RunsOutAt)):
				warnings = append(warnings, fmt.Sprintf("default model %s on %s runs out of its weekly limit in %s, before its weekly reset; next on the default list: %s",
					s.Model, s.connectionLabel(), roughly(s.WeeklyRunsOutAt.Sub(now)), next))
			case intervalWarns(s):
				warnings = append(warnings, fmt.Sprintf("default model %s on %s runs out of its %s interval limit in %s, before its reset; next on the default list: %s",
					s.Model, s.connectionLabel(), shortWindow(s.window), roughly(s.RunsOutAt.Sub(now)), next))
			}
		}
	}
	return advisories, warnings
}

// intervalWarns is whether s's interval runs out before its reset.
func intervalWarns(s Scope) bool {
	return s.RunsOutAt != nil && (s.ResetAt == nil || s.RunsOutAt.Before(*s.ResetAt))
}

// weeklyWarns is whether s's weekly limit runs out before its weekly
// reset.
func weeklyWarns(s Scope) bool {
	return s.WeeklyRunsOutAt != nil && (s.WeeklyResetAt == nil || s.WeeklyRunsOutAt.Before(*s.WeeklyResetAt))
}

// untilText is t as a warning names it: "15:04 UTC" within a day of
// now, else "Jan 2 15:04 UTC" -- a weekly pause ends days away.
func untilText(now, t time.Time) string {
	if t.Sub(now) < 24*time.Hour {
		return t.UTC().Format("15:04 MST")
	}
	return t.UTC().Format("Jan 2 15:04 MST")
}

// shortWindow is d without its zero units: "5h", "90m", "24h".
func shortWindow(d time.Duration) string {
	out := d.String()
	if strings.HasSuffix(out, "m0s") {
		out = strings.TrimSuffix(out, "0s")
	}
	if strings.HasSuffix(out, "h0m") {
		out = strings.TrimSuffix(out, "0m")
	}
	return out
}

func trimFloat(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}
