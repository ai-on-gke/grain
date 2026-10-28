package cli

import (
	"context"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/daemon"

	"github.com/bwsalmon/grain/pkg/model"
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
