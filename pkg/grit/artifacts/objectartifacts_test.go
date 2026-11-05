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

package artifacts

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/objectstore"
	"github.com/ai-on-gke/grain/pkg/workflows"
)

func TestObjectArtifactsSaveReadThroughClearAndReplaceVM(t *testing.T) {
	ctx := context.Background()
	mem := objectstore.NewMemory()
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	mem.WithClock(func() time.Time { return now })

	scoped := objectstore.WithPrefix(mem, "ci-artifacts")
	store1, err := OpenObjectArtifacts(scoped, filepath.Join(t.TempDir(), "cache-vm1"), DirArtifactsOptions{
		MaxAge:           24 * time.Hour,
		MaxArtifactBytes: 1024,
		MaxRunBytes:      1500,
		Now:              func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	ref := workflows.ArtifactRef{Run: "run001", Job: "e2e", Name: "trace"}
	payload := []byte("playwright-trace-bundle-bytes")
	art, err := store1.Save(ctx, ref, payload)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !art.Stored || art.Bytes != int64(len(payload)) {
		t.Fatalf("Save result = %+v", art)
	}

	// Verify object landed in MemoryStore under ci-artifacts/run001/e2e.trace.tar.gz.
	if _, ok := mem.Bytes("ci-artifacts/run001/e2e.trace.tar.gz"); !ok {
		t.Fatalf("expected object in MemoryStore, keys = %v", mem.Keys())
	}

	// Opening on VM1 hits local cache with zero remote Get calls.
	mem.ResetCounts()
	rc, gotArt, err := store1.Open(ctx, ref)
	if err != nil {
		t.Fatalf("Open cached: %v", err)
	}
	gotBytes, _ := io.ReadAll(rc)
	rc.Close()
	if string(gotBytes) != string(payload) || gotArt.Bytes != int64(len(payload)) {
		t.Fatalf("Open cached = %q (%+v)", gotBytes, gotArt)
	}
	if mem.GetCount() != 0 {
		t.Fatalf("GetCount on local cache hit = %d, want 0", mem.GetCount())
	}

	// Clear() reclaims local VM disk space without deleting the remote object.
	removed, freed, err := store1.Clear(ctx)
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if removed != 1 || freed != int64(len(payload)) {
		t.Fatalf("Clear = (%d, %d), want (1, %d)", removed, freed, len(payload))
	}
	if _, ok := mem.Bytes("ci-artifacts/run001/e2e.trace.tar.gz"); !ok {
		t.Fatal("Clear deleted durable object from MemoryStore")
	}

	// Simulate VM replacement: open a brand new cache directory against the same MemoryStore.
	store2, err := OpenObjectArtifacts(scoped, filepath.Join(t.TempDir(), "cache-vm2"), DirArtifactsOptions{
		MaxAge:           24 * time.Hour,
		MaxArtifactBytes: 1024,
		MaxRunBytes:      1500,
		Now:              func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	listed, err := store2.List(ctx, "run001")
	if err != nil {
		t.Fatalf("List on VM2: %v", err)
	}
	if len(listed) != 1 || listed[0].Name != "trace" || listed[0].Job != "e2e" {
		t.Fatalf("List on VM2 = %+v", listed)
	}

	stats, err := store2.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats on VM2: %v", err)
	}
	if stats.Runs != 1 || stats.Artifacts != 1 || stats.Bytes != int64(len(payload)) {
		t.Fatalf("Stats on VM2 = %+v", stats)
	}

	// First Open on VM2 misses local cache -> 1 remote Get; second Open hits local cache -> still 1 remote Get.
	mem.ResetCounts()
	for i := 0; i < 2; i++ {
		rc, _, err := store2.Open(ctx, ref)
		if err != nil {
			t.Fatalf("Open #%d on VM2: %v", i, err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		if string(b) != string(payload) {
			t.Fatalf("Open #%d bytes = %q", i, b)
		}
	}
	if mem.GetCount() != 1 {
		t.Fatalf("GetCount after 2 Opens on VM2 = %d, want 1", mem.GetCount())
	}

	// Verify MaxArtifactBytes and MaxRunBytes bounds across VM2.
	tooBig, err := store2.Save(ctx, workflows.ArtifactRef{Run: "run001", Job: "e2e", Name: "huge"}, make([]byte, 1025))
	if err != nil {
		t.Fatalf("Save oversized artifact: %v", err)
	}
	if tooBig.Stored || !strings.Contains(tooBig.Note, "single artifact may be") {
		t.Fatalf("expected per-artifact cap note, got %+v", tooBig)
	}

	chunk := make([]byte, 1000)
	if a2, err := store2.Save(ctx, workflows.ArtifactRef{Run: "run001", Job: "unit", Name: "cov"}, chunk); err != nil || !a2.Stored {
		t.Fatalf("Save second artifact: %+v, %v", a2, err)
	}
	overRun, err := store2.Save(ctx, workflows.ArtifactRef{Run: "run001", Job: "lint", Name: "out"}, make([]byte, 600))
	if err != nil {
		t.Fatalf("Save over run cap: %v", err)
	}
	if overRun.Stored || !strings.Contains(overRun.Note, "one run may have") {
		t.Fatalf("expected per-run cap note, got %+v", overRun)
	}

	// Advance clock past MaxAge: List and Open treat expired artifacts as gone.
	now = now.Add(25 * time.Hour)
	expiredList, err := store2.List(ctx, "run001")
	if err != nil {
		t.Fatal(err)
	}
	if len(expiredList) != 0 {
		t.Fatalf("expected 0 listed past MaxAge, got %+v", expiredList)
	}
	if _, _, err := store2.Open(ctx, ref); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open past MaxAge = %v, want fs.ErrNotExist", err)
	}
}

func TestObjectArtifactsFlushLocal(t *testing.T) {
	ctx := context.Background()
	cacheDir := filepath.Join(t.TempDir(), "local-artifacts")
	localOnly, err := OpenDirArtifacts(cacheDir, DirArtifactsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ref := workflows.ArtifactRef{Run: "run777", Job: "test", Name: "junit"}
	if _, err := localOnly.Save(ctx, ref, []byte("xml-report")); err != nil {
		t.Fatal(err)
	}
	// Write a non-artifact file that FlushLocal should ignore.
	_ = os.WriteFile(filepath.Join(cacheDir, "run777", "ignore.txt"), []byte("nope"), 0o600)

	mem := objectstore.NewMemory()
	objStore, err := OpenObjectArtifacts(objectstore.WithPrefix(mem, "ci-artifacts"), cacheDir, DirArtifactsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := objStore.FlushLocal(ctx); err != nil {
		t.Fatalf("FlushLocal: %v", err)
	}
	got, ok := mem.Bytes("ci-artifacts/run777/test.junit.tar.gz")
	if !ok || string(got) != "xml-report" {
		t.Fatalf("flushed bytes = %q (ok=%v), keys=%v", got, ok, mem.Keys())
	}
}
