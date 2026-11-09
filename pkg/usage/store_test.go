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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func openStore(t *testing.T) (*model.Store, context.Context) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

// A pause opening records what its window had consumed as that window's
// capacity, and the cap then holds dispatch until the reset and lets it
// back up after.
func TestLearnFromPauseThenCapRecovers(t *testing.T) {
	store, ctx := openStore(t)
	for i, ago := range []time.Duration{20 * time.Minute, 80 * time.Minute, 7 * time.Hour} {
		r := run(string(rune('a'+i)), "conn", "opus", ago, 400000)
		if err := store.RecordRunUsage(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	resetAt := now.Add(3 * time.Hour)
	pause := model.UsageLimitPause{Connection: "conn", Model: "Opus", Until: resetAt}
	if err := LearnFromPause(ctx, store, pause, now); err != nil {
		t.Fatal(err)
	}
	caps, err := store.ListUsageCapacities(ctx)
	if err != nil || len(caps) != 1 {
		t.Fatalf("capacities = %+v, %v", caps, err)
	}
	// The 7h-old run is outside the default 5h window.
	if c := caps[0]; c.Tokens != 800000 || c.Model != "opus" || c.Window != DefaultWindow || !c.ResetAt.Equal(resetAt) {
		t.Fatalf("learned = %+v", c)
	}
	if err := store.PutUsageLimitPause(ctx, "k", pause); err != nil {
		t.Fatal(err)
	}
	if err := store.PutUsageSettings(ctx, model.UsageSettings{Mode: model.UsageModeAuto, MinWorkers: 2}, now); err != nil {
		t.Fatal(err)
	}
	c := &Cap{Store: store}
	if got := c.Workers(ctx, now, 6); got != 2 {
		t.Fatalf("cap while paused = %d, want the floor 2", got)
	}
	// Advisory mode never caps.
	if err := store.PutUsageSettings(ctx, model.UsageSettings{Mode: model.UsageModeAdvisory, MinWorkers: 2}, now); err != nil {
		t.Fatal(err)
	}
	if got := (&Cap{Store: store}).Workers(ctx, now, 6); got != 6 {
		t.Fatalf("advisory cap = %d, want max_workers 6", got)
	}
	if err := store.PutUsageSettings(ctx, model.UsageSettings{Mode: model.UsageModeAuto, MinWorkers: 2}, now); err != nil {
		t.Fatal(err)
	}
	// After the reset the stored pause has expired and the window is
	// fresh: 800k left at 400k per worker-hour over 5h is 0.4 workers,
	// held at the floor -- and with the capacity larger, back to
	// max_workers, never above it.
	after := resetAt.Add(time.Minute)
	if got := (&Cap{Store: store}).Workers(ctx, after, 6); got != 2 {
		t.Fatalf("cap after reset = %d, want 2", got)
	}
	if err := store.RecordUsageCapacity(ctx, model.UsageCapacity{Connection: "conn", Model: "opus", Tokens: 1 << 40, Window: DefaultWindow, ObservedAt: now, ResetAt: resetAt}); err != nil {
		t.Fatal(err)
	}
	if got := (&Cap{Store: store}).Workers(ctx, after, 6); got != 6 {
		t.Fatalf("cap with a large window = %d, want 6", got)
	}
}
