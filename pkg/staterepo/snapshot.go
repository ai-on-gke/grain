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

package staterepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// SettingsSnapshotsDir is the directory under data-dir where snapshots of
	// the settings tables are kept before an import that changes rows.
	SettingsSnapshotsDir = "settings-snapshots"

	// SnapshotManifestFile is the metadata file stored in every snapshot directory.
	SnapshotManifestFile = "manifest.json"

	// DefaultSnapshotRetentionDays is how long snapshots are kept before pruning.
	DefaultSnapshotRetentionDays = 7

	// DefaultSnapshotRetentionCount is the minimum number of recent snapshots to keep.
	DefaultSnapshotRetentionCount = 10
)

// SnapshotManifest records the context under which a settings snapshot was taken.
type SnapshotManifest struct {
	CreatedAt time.Time `json:"createdAt"`
	Base      string    `json:"base"`
	Head      string    `json:"head"`
	Notice    string    `json:"notice,omitempty"`
	Replaced  []string  `json:"replaced,omitempty"`
}

// snapshotDirName formats the snapshot directory name: <UTC time>-<head>.
func snapshotDirName(t time.Time, head string) string {
	ts := t.UTC().Format("20060102T150405Z")
	h := head
	if len(h) > 12 {
		h = h[:12]
	}
	if h == "" {
		h = "unknown"
	}
	return fmt.Sprintf("%s-%s", ts, h)
}

// takeSettingsSnapshot writes an ExportConfig snapshot of the current settings
// and memory tables to data-dir/settings-snapshots/<time>-<head>, along with a
// manifest containing the base commit, arriving commit, and replaced-row notice.
// It prunes older snapshots according to retention policy.
func takeSettingsSnapshot(ctx context.Context, dataDir string, db *sql.DB, base, head string, replaced []replacedRow) (string, error) {
	if dataDir == "" {
		return "", fmt.Errorf("staterepo: cannot take settings snapshot with empty dataDir")
	}

	snapshotsDir := filepath.Join(dataDir, SettingsSnapshotsDir)
	if err := os.MkdirAll(snapshotsDir, 0o700); err != nil {
		return "", fmt.Errorf("staterepo: creating snapshots directory: %w", err)
	}

	now := time.Now().UTC()
	dirName := snapshotDirName(now, head)
	targetDir := filepath.Join(snapshotsDir, dirName)

	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return "", fmt.Errorf("staterepo: creating snapshot target %s: %w", targetDir, err)
	}

	// ExportConfig dumps SettingsTables + memoryTables + release_note
	if err := ExportConfig(ctx, db, targetDir); err != nil {
		_ = os.RemoveAll(targetDir)
		return "", fmt.Errorf("staterepo: exporting config for snapshot: %w", err)
	}

	// Build notice and replaced row list
	var replacedStrs []string
	for _, r := range replaced {
		replacedStrs = append(replacedStrs, trimValues(r).String())
	}
	notice := formatReplacedNotice(replaced)

	manifest := SnapshotManifest{
		CreatedAt: now,
		Base:      base,
		Head:      head,
		Notice:    notice,
		Replaced:  replacedStrs,
	}

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = os.RemoveAll(targetDir)
		return "", fmt.Errorf("staterepo: marshaling snapshot manifest: %w", err)
	}

	manifestPath := filepath.Join(targetDir, SnapshotManifestFile)
	if err := os.WriteFile(manifestPath, manifestBytes, 0o600); err != nil {
		_ = os.RemoveAll(targetDir)
		return "", fmt.Errorf("staterepo: writing snapshot manifest: %w", err)
	}

	// Prune older snapshots: keep for 7 days or the last 10, whichever keeps more.
	if err := PruneSnapshots(snapshotsDir, DefaultSnapshotRetentionDays*24*time.Hour, DefaultSnapshotRetentionCount); err != nil {
		log.Printf("staterepo: warning: pruning settings snapshots: %v", err)
	}

	return targetDir, nil
}

// formatReplacedNotice produces the human-readable notice describing the replaced rows.
func formatReplacedNotice(replaced []replacedRow) string {
	if len(replaced) == 0 {
		return ""
	}
	var tables []string
	seen := map[string]bool{}
	for _, r := range replaced {
		if !seen[r.path] {
			seen[r.path] = true
			tables = append(tables, r.path)
		}
	}
	shown := replaced
	var tail string
	if len(shown) > replacedRowLimit {
		tail = fmt.Sprintf(" (and %d more, which this line does not have room for)", len(shown)-replacedRowLimit)
		shown = shown[:replacedRowLimit]
	}
	lines := make([]string, len(shown))
	for i, r := range shown {
		lines[i] = trimValues(r).String()
	}
	return fmt.Sprintf("staterepo: the settings that just arrived did not match this database on %d %s of its "+
		"settings tables, across %d %s (%s), and a settings file holds whole rows: the repository is "+
		"what says what a setting is, so the import replaced every one of them with the file's version "+
		"-- including rows the change was not about, which the last export wrote and this deployment "+
		"has moved on from since. What differed: %s%s. If one of those was not meant, what this "+
		"database held is above: put it back in the repository and merge that, because the database "+
		"now holds what arrived and the next export writes that back out.",
		len(replaced),
		plural(len(replaced), "row", "rows"),
		len(tables),
		plural(len(tables), "file", "files"),
		strings.Join(tables, ", "),
		strings.Join(lines, "; "),
		tail)
}

type snapshotInfo struct {
	path    string
	modTime time.Time
}

// PruneSnapshots keeps snapshots within maxAge OR the most recent keepCount,
// whichever keeps more, and removes the rest.
func PruneSnapshots(snapshotsDir string, maxAge time.Duration, keepCount int) error {
	entries, err := os.ReadDir(snapshotsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var snapshots []snapshotInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(snapshotsDir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		t := info.ModTime()
		// Also try to read timestamp from manifest or folder name if available
		if manifestData, err := os.ReadFile(filepath.Join(p, SnapshotManifestFile)); err == nil {
			var m SnapshotManifest
			if err := json.Unmarshal(manifestData, &m); err == nil && !m.CreatedAt.IsZero() {
				t = m.CreatedAt
			}
		}
		snapshots = append(snapshots, snapshotInfo{
			path:    p,
			modTime: t,
		})
	}

	if len(snapshots) <= keepCount {
		return nil
	}

	// Sort newest first
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].modTime.After(snapshots[j].modTime)
	})

	now := time.Now().UTC()
	cutoff := now.Add(-maxAge)

	for i, s := range snapshots {
		// Keep the first keepCount regardless of age
		if i < keepCount {
			continue
		}
		// Also keep if newer than cutoff
		if s.modTime.After(cutoff) {
			continue
		}
		// Otherwise, remove
		if err := os.RemoveAll(s.path); err != nil {
			log.Printf("staterepo: error removing old snapshot %s: %v", s.path, err)
		}
	}

	return nil
}

// LatestSnapshot finds the most recent settings snapshot in dataDir.
// Returns the directory path and creation time, or "", zero time, nil if none found.
func LatestSnapshot(dataDir string) (string, time.Time, error) {
	snapshotsDir := filepath.Join(dataDir, SettingsSnapshotsDir)
	entries, err := os.ReadDir(snapshotsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", time.Time{}, nil
		}
		return "", time.Time{}, err
	}

	var latest snapshotInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(snapshotsDir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		t := info.ModTime()
		if manifestData, err := os.ReadFile(filepath.Join(p, SnapshotManifestFile)); err == nil {
			var m SnapshotManifest
			if err := json.Unmarshal(manifestData, &m); err == nil && !m.CreatedAt.IsZero() {
				t = m.CreatedAt
			}
		}
		if latest.path == "" || t.After(latest.modTime) {
			latest = snapshotInfo{
				path:    p,
				modTime: t,
			}
		}
	}

	return latest.path, latest.modTime, nil
}
