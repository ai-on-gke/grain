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

package model_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The three answers the merge queue's gather window has to give, and the
// one that is easy to get wrong: a stored zero is "nobody chose" and a
// stored negative is "turn the wait off", which are opposite meanings
// either side of the value a fresh column holds (grain/task-360).
func TestMergeGatherWindowOr(t *testing.T) {
	t.Parallel()
	var none *model.Config
	if got := none.MergeGatherWindowOr(); got != model.DefaultMergeGatherWindow {
		t.Fatalf("a deployment with no config row waits %v, want grain's own %v",
			got, model.DefaultMergeGatherWindow)
	}
	tests := []struct {
		name   string
		stored time.Duration
		want   time.Duration
	}{
		{"never chosen", 0, model.DefaultMergeGatherWindow},
		{"chosen", 90 * time.Second, 90 * time.Second},
		{"turned off", -time.Second, 0},
		{"beyond the ceiling", 10 * model.MaxMergeGatherWindow, model.MaxMergeGatherWindow},
	}
	for _, tc := range tests {
		cfg := model.Config{MergeGatherWindow: tc.stored}
		if got := cfg.MergeGatherWindowOr(); got != tc.want {
			t.Errorf("%s: a stored %v reads as %v, want %v", tc.name, tc.stored, got, tc.want)
		}
	}
	if got := model.DefaultConfig().MergeGatherWindow; got != model.DefaultMergeGatherWindow {
		t.Fatalf("DefaultConfig().MergeGatherWindow = %v, want %v", got, model.DefaultMergeGatherWindow)
	}
}

func TestDefaultConfigPollInterval(t *testing.T) {
	t.Parallel()
	if model.DefaultPollInterval != 10*time.Second {
		t.Fatalf("DefaultPollInterval = %v, want 10s", model.DefaultPollInterval)
	}
	cfg := model.DefaultConfig()
	if cfg.PollInterval != model.DefaultPollInterval {
		t.Fatalf("DefaultConfig().PollInterval = %v, want %v", cfg.PollInterval, model.DefaultPollInterval)
	}
}

func TestDefaultConfigMaxWorkers(t *testing.T) {
	t.Parallel()
	if model.DefaultMaxWorkers != 10 {
		t.Fatalf("DefaultMaxWorkers = %d, want 10", model.DefaultMaxWorkers)
	}
	cfg := model.DefaultConfig()
	if cfg.MaxWorkers != model.DefaultMaxWorkers {
		t.Fatalf("DefaultConfig().MaxWorkers = %d, want %d", cfg.MaxWorkers, model.DefaultMaxWorkers)
	}
}

func TestAgentBackendEffectiveConcurrency(t *testing.T) {
	t.Parallel()
	if model.DefaultAgentBackendConcurrency != 4 {
		t.Fatalf("DefaultAgentBackendConcurrency = %d, want 4", model.DefaultAgentBackendConcurrency)
	}
	for _, tc := range []struct {
		name        string
		concurrency int
		want        int
	}{
		{"unset zero", 0, 4},
		{"negative", -1, 4},
		{"explicit 1", 1, 1},
		{"explicit 8", 8, 8},
	} {
		b := model.AgentBackend{Concurrency: tc.concurrency}
		if got := b.EffectiveConcurrency(); got != tc.want {
			t.Errorf("%s: EffectiveConcurrency() = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestAutoUpgradeMode(t *testing.T) {
	for _, tc := range []struct {
		on, channel, want string
	}{
		{"", "", model.AutoUpgradeOff},
		{"", "latest", model.AutoUpgradeDaily},
		{"off", "latest", model.AutoUpgradeOff},
		{"daily", "candidate", model.AutoUpgradeDaily},
		{"release", "", model.AutoUpgradeOnRelease},
		{" Release ", "latest", model.AutoUpgradeOnRelease},
		{"hourly", "latest", model.AutoUpgradeOff},
	} {
		c := model.Config{AutoUpgradeOn: tc.on, AutoUpgradeChannel: tc.channel}
		if got := c.AutoUpgradeMode(); got != tc.want {
			t.Errorf("AutoUpgradeOn %q, channel %q: mode %q, want %q", tc.on, tc.channel, got, tc.want)
		}
	}
	if on, err := model.ParseAutoUpgradeOn(""); err != nil || on != model.AutoUpgradeOff {
		t.Errorf(`ParseAutoUpgradeOn("") = %q, %v; want off`, on, err)
	}
	if _, err := model.ParseAutoUpgradeOn("hourly"); err == nil {
		t.Error(`ParseAutoUpgradeOn("hourly") was accepted`)
	}
	def := model.DefaultConfig()
	if def.AutoUpgradeMinIntervalHours != model.DefaultAutoUpgradeMinIntervalHours {
		t.Errorf("DefaultConfig().AutoUpgradeMinIntervalHours = %d, want %d",
			def.AutoUpgradeMinIntervalHours, model.DefaultAutoUpgradeMinIntervalHours)
	}
	if got := def.AutoUpgradeMinInterval(); got != 6*time.Hour {
		t.Errorf("DefaultConfig().AutoUpgradeMinInterval() = %s, want 6h", got)
	}
	for _, hours := range []int{0, -3} {
		if got := (model.Config{AutoUpgradeMinIntervalHours: hours}).AutoUpgradeMinInterval(); got != 0 {
			t.Errorf("AutoUpgradeMinInterval(%d) = %s, want 0", hours, got)
		}
	}
	// An hour count past what a time.Duration holds stays the longest
	// interval instead of wrapping round to a short or negative one.
	if got := (model.Config{AutoUpgradeMinIntervalHours: 1 << 40}).AutoUpgradeMinInterval(); got != time.Duration(1<<63-1) {
		t.Errorf("AutoUpgradeMinInterval(1<<40) = %s, want the longest time.Duration", got)
	}
}
