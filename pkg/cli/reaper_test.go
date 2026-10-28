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

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestReapBoundaryTrailsOldestLiveWorkAndNowWhenIdle(t *testing.T) {
	t.Parallel()
	store, ctx := pacingStore(t)
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

	got, err := daemon.ReapBoundary(ctx, store, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(-daemon.ReapGracePeriod); !got.Equal(want) {
		t.Fatalf("idle boundary = %v, want %v", got, want)
	}

	started := now.Add(-2 * time.Hour)
	run := model.CIRun{
		ID: "live-ci", Key: "live-ci-key", Repo: "owner/repo",
		Event: "push", StartedAt: started,
	}
	if err := store.PutCIRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	got, err = daemon.ReapBoundary(ctx, store, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := started.Add(-daemon.ReapGracePeriod); !got.Equal(want) {
		t.Fatalf("live boundary = %v, want %v", got, want)
	}

	run.Conclusion = "success"
	run.FinishedAt = now
	if err := store.PutCIRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	got, err = daemon.ReapBoundary(ctx, store, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(-daemon.ReapGracePeriod); !got.Equal(want) {
		t.Fatalf("boundary after CI finished = %v, want %v", got, want)
	}
}

type testReaperCapability struct {
	model.BaseCapability
	name     string
	reapedAt time.Time
	deleted  []string
}

func (m *testReaperCapability) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{Name: m.name}
}

func (m *testReaperCapability) Reap(ctx context.Context, creds model.CredentialResolver, before time.Time) ([]string, error) {
	m.reapedAt = before
	return m.deleted, nil
}

func TestReapCapabilitiesBeforeSweepsWithExactBoundary(t *testing.T) {
	t.Parallel()
	startupTime := time.Date(2026, 9, 19, 5, 30, 0, 0, time.UTC)
	reaper := &testReaperCapability{
		name:    "gcp-key",
		deleted: []string{"key-1", "key-2"},
	}
	reg := model.NewCapabilityRegistry(reaper)

	daemon.ReapCapabilitiesBefore(context.Background(), reg, nil, startupTime)

	if !reaper.reapedAt.Equal(startupTime) {
		t.Fatalf("reapedAt = %v, want exact startup time %v", reaper.reapedAt, startupTime)
	}
}
