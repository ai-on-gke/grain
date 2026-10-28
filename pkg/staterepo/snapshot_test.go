package staterepo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

func TestSnapshotSettingsAndPruning(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	_, db := sqlitetest.Open(t)

	// 1. Snapshot with empty dataDir should fail
	if _, err := takeSettingsSnapshot(ctx, "", db, "base1", "head1", nil); err == nil {
		t.Fatalf("expected error for empty dataDir, got nil")
	}

	// 2. Snapshot creates snapshot directory and manifest
	replaced := []replacedRow{
		{
			path: "tables/grain_config.json",
			values: []replacedValue{
				{column: "max_workers", from: "0", to: "5"},
			},
		},
	}
	snapshotDir, err := takeSettingsSnapshot(ctx, dataDir, db, "base12345678", "head87654321", replaced)
	if err != nil {
		t.Fatalf("takeSettingsSnapshot failed: %v", err)
	}

	manifestBytes, err := os.ReadFile(filepath.Join(snapshotDir, SnapshotManifestFile))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	var manifest SnapshotManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("unmarshaling manifest: %v", err)
	}

	if manifest.Base != "base12345678" || manifest.Head != "head87654321" {
		t.Fatalf("unexpected manifest base/head: %+v", manifest)
	}
	if len(manifest.Replaced) != 1 {
		t.Fatalf("expected 1 replaced row in manifest, got %d", len(manifest.Replaced))
	}
	if manifest.Notice == "" {
		t.Fatalf("expected non-empty notice in manifest")
	}

	// Verify tables were exported in snapshot
	if _, err := os.Stat(filepath.Join(snapshotDir, TablesDir, "grain_config.json")); err != nil {
		t.Fatalf("expected tables/grain_config.json in snapshot: %v", err)
	}

	// 3. Test LatestSnapshot
	latestPath, latestAt, err := LatestSnapshot(dataDir)
	if err != nil {
		t.Fatalf("LatestSnapshot failed: %v", err)
	}
	if latestPath != snapshotDir {
		t.Fatalf("expected latestPath %q, got %q", snapshotDir, latestPath)
	}
	if latestAt.IsZero() {
		t.Fatalf("expected non-zero latestAt")
	}

	// 4. Test Pruning
	snapshotsBase := filepath.Join(dataDir, SettingsSnapshotsDir)
	// Create 15 fake snapshots with timestamps
	now := time.Now().UTC()
	for i := 1; i <= 15; i++ {
		fakeTime := now.Add(-time.Duration(i*24) * time.Hour)
		fakeDir := filepath.Join(snapshotsBase, fmt.Sprintf("%s-fake%02d", fakeTime.Format("20060102T150405Z"), i))
		if err := os.MkdirAll(fakeDir, 0o700); err != nil {
			t.Fatal(err)
		}
		fakeManifest := SnapshotManifest{
			CreatedAt: fakeTime,
			Base:      "base",
			Head:      fmt.Sprintf("fake%02d", i),
		}
		b, _ := json.Marshal(fakeManifest)
		_ = os.WriteFile(filepath.Join(fakeDir, SnapshotManifestFile), b, 0o600)
	}

	// Retention: keep for 7 days OR the last 10 snapshots.
	// We created 1 real (today) + 15 older (1..15 days ago). Total 16 snapshots.
	// Snapshots within 7 days: day 0 (real), day 1, 2, 3, 4, 5, 6, 7 (total ~8 snapshots within 7 days).
	// But keepCount is 10, so at least 10 newest must be kept!
	if err := PruneSnapshots(snapshotsBase, 7*24*time.Hour, 10); err != nil {
		t.Fatalf("PruneSnapshots failed: %v", err)
	}

	entries, err := os.ReadDir(snapshotsBase)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 10 {
		t.Fatalf("expected exactly 10 snapshots retained, got %d", len(entries))
	}
}
