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

package usage

import (
	"context"
	"log"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Load reads everything Compute needs out of store and computes the
// Report as of now. maxWorkers < 0 reads the store's own max_workers.
func Load(ctx context.Context, store *model.Store, now time.Time, maxWorkers int) (Report, error) {
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		return Report{}, err
	}
	if cfg == nil {
		cfg = &model.Config{}
	}
	settings, err := store.UsageSettings(ctx)
	if err != nil {
		return Report{}, err
	}
	capacities, err := store.ListUsageCapacities(ctx)
	if err != nil {
		return Report{}, err
	}
	stored, err := store.ListUsageLimitPauses(ctx)
	if err != nil {
		return Report{}, err
	}
	var pauses []model.UsageLimitPause
	for _, p := range stored {
		if p.Pause.Until.After(now) {
			pauses = append(pauses, p.Pause)
		}
	}
	runs, err := store.RunUsagesSince(ctx, now.Add(-Lookback(settings, capacities)))
	if err != nil {
		return Report{}, err
	}
	names := map[string]string{}
	for _, b := range cfg.AgentBackends {
		if strings.TrimSpace(b.Name) != "" {
			names[b.ID] = b.Name
		}
	}
	if maxWorkers < 0 {
		maxWorkers = cfg.MaxWorkers
	}
	return Compute(Input{
		Now: now, Runs: runs, Settings: settings, Capacities: capacities, Pauses: pauses,
		DefaultModels: model.ParseDefaultModels(cfg.DefaultModel), MaxWorkers: maxWorkers, ConnectionNames: names,
	}), nil
}

// LearnFromPause records, when a usage-limit pause opens on a
// connection, the usage the window consumed before it as an estimate of
// that window's capacity (model.UsageCapacity). The window is the
// operator's configured one for the scope, else the one learned last
// time, else DefaultWindow. A pause on no connection, or one whose window
// held no recorded usage, teaches nothing.
func LearnFromPause(ctx context.Context, store *model.Store, pause model.UsageLimitPause, now time.Time) error {
	if pause.Connection == "" {
		return nil
	}
	settings, err := store.UsageSettings(ctx)
	if err != nil {
		return err
	}
	capacities, err := store.ListUsageCapacities(ctx)
	if err != nil {
		return err
	}
	m := norm(pause.Model)
	window := DefaultWindow
	found := false
	for _, b := range settings.Budgets {
		if strings.TrimSpace(b.Connection) == pause.Connection && norm(b.Model) == m && b.WindowDuration() > 0 {
			window, found = b.WindowDuration(), true
		}
	}
	start := now.Add(-window)
	for _, c := range capacities {
		if c.Connection != pause.Connection || norm(c.Model) != m {
			continue
		}
		if !found && c.Window > 0 {
			window, start = c.Window, now.Add(-c.Window)
		}
		// The window began no earlier than the last reset grain knows of.
		if !c.ResetAt.IsZero() && c.ResetAt.After(start) && c.ResetAt.Before(now) {
			start = c.ResetAt
		}
	}
	runs, err := store.RunUsagesSince(ctx, start)
	if err != nil {
		return err
	}
	var used int64
	for _, r := range runs {
		if r.Connection == pause.Connection && (m == "" || norm(r.Model) == m) {
			used += r.Billable()
		}
	}
	if used <= 0 {
		return nil
	}
	return store.RecordUsageCapacity(ctx, model.UsageCapacity{
		Connection: pause.Connection, Model: m, Tokens: used, Window: window, ObservedAt: now, ResetAt: pause.Until,
	})
}

// Cap is the automatic worker cap a dispatch cycle applies: the
// Report's EffectiveWorkers, recomputed at most every refresh -- or at
// once when the operator's usage settings changed since -- and
// maxWorkers unchanged whenever the mode is not auto or anything fails
// -- an estimate never blocks dispatch it cannot compute. It also prunes
// usage rows past their retention, at most hourly.
type Cap struct {
	Store   *model.Store
	Refresh time.Duration

	mu         sync.Mutex
	computedAt time.Time
	forMax     int
	forSetting model.UsageSettings
	effective  int
	prunedAt   time.Time
}

// Workers is the worker cap to dispatch under, given the operator's
// maxWorkers. It never exceeds maxWorkers.
func (c *Cap) Workers(ctx context.Context, now time.Time, maxWorkers int) int {
	if c == nil || c.Store == nil || maxWorkers <= 0 {
		return maxWorkers
	}
	refresh := c.Refresh
	if refresh <= 0 {
		refresh = time.Minute
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Read every cycle (one row): a mode, floor or budget the operator
	// just set binds the next dispatch, not the one after the cached
	// estimate expires -- a cap computed under "advisory" or under no
	// budget kept dispatching at max_workers for up to refresh after
	// `grain usage -mode auto -budget ...`.
	settings, err := c.Store.UsageSettings(ctx)
	if err != nil {
		log.Printf("usage: reading usage settings: %v -- dispatching under max_workers", err)
		return maxWorkers
	}
	if now.Sub(c.prunedAt) >= time.Hour {
		c.prunedAt = now
		if n, err := c.Store.PruneRunUsage(ctx, now.Add(-settings.Retention())); err != nil {
			log.Printf("usage: pruning run usage: %v", err)
		} else if n > 0 {
			log.Printf("usage: pruned %d run usage row(s) past retention", n)
		}
	}
	if c.forMax == maxWorkers && reflect.DeepEqual(c.forSetting, settings) && !c.computedAt.IsZero() && now.Sub(c.computedAt) < refresh && now.After(c.computedAt) {
		return min(c.effective, maxWorkers)
	}
	rep, err := Load(ctx, c.Store, now, maxWorkers)
	if err != nil {
		log.Printf("usage: computing the automatic worker cap: %v -- dispatching under max_workers", err)
		return maxWorkers
	}
	if rep.EffectiveWorkers != c.effective {
		if rep.Mode == model.UsageModeAuto {
			log.Printf("usage: automatic worker cap %d of max_workers %d (sustainable %v)", rep.EffectiveWorkers, maxWorkers, sustainableText(rep))
		} else if !c.computedAt.IsZero() && c.effective < c.forMax {
			log.Printf("usage: mode %s: the automatic worker cap is lifted, dispatching under max_workers %d", rep.Mode, maxWorkers)
		}
	}
	c.computedAt, c.forMax, c.forSetting, c.effective = now, maxWorkers, settings, rep.EffectiveWorkers
	return min(rep.EffectiveWorkers, maxWorkers)
}

func sustainableText(rep Report) any {
	if rep.SustainableWorkers == nil {
		return "unbounded"
	}
	return *rep.SustainableWorkers
}
