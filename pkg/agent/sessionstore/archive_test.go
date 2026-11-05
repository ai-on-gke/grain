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

package sessionstore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/objectstore"
)

func TestStoreWithObjectStoreSyncsAtTurnBoundaryAndCachesLocally(t *testing.T) {
	mem := objectstore.NewMemory()
	scoped := objectstore.WithPrefix(mem, "agent-sessions")

	// VM 1: run a turn, write both durable conversation state and ephemeral CLI caches.
	store1 := New(filepath.Join(t.TempDir(), "vm1-sessions")).WithObjectStore(scoped)
	dir1, release1, ok := store1.Open("antigravity", "task-42")
	if !ok {
		t.Fatal("Open failed on VM1")
	}

	convFile := filepath.Join(dir1, ".gemini", "jetski", "brain", "conv-1", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(convFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(convFile, []byte(`{"step":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Ephemeral CLI tool caches and symlinks must NOT be archived.
	cacheFile := filepath.Join(dir1, ".cache", "cli-tools", "bundle.bin")
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cacheFile, []byte("ephemeral-tool-cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	secretTarget := filepath.Join(t.TempDir(), "secret.json")
	_ = os.WriteFile(secretTarget, []byte("do-not-archive"), 0o600)
	_ = os.Symlink(secretTarget, filepath.Join(dir1, "auth-link.json"))

	// Turn ends -> release1() packs and uploads bundle to agent-sessions/antigravity/task-42.tar.gz.
	release1()

	if _, ok := mem.Bytes("agent-sessions/antigravity/task-42.tar.gz"); !ok {
		t.Fatalf("expected session archive in MemoryStore, keys = %v", mem.Keys())
	}

	// Second turn on the SAME VM hits local disk without calling MemoryStore.Get.
	mem.ResetCounts()
	dir1b, release1b, ok := store1.Open("antigravity", "task-42")
	if !ok {
		t.Fatal("second Open failed on VM1")
	}
	if mem.GetCount() != 0 {
		t.Fatalf("GetCount on local cache hit = %d, want 0", mem.GetCount())
	}
	_ = os.WriteFile(convFile, []byte(`{"step":1}`+"\n"+`{"step":2}`+"\n"), 0o600)
	release1b()
	if dir1b != dir1 {
		t.Fatalf("dir changed across turns: %q vs %q", dir1b, dir1)
	}

	// VM 2 (replacement VM with an empty local disk): Open restores the bundle on cache miss.
	mem.ResetCounts()
	store2 := New(filepath.Join(t.TempDir(), "vm2-sessions")).WithObjectStore(scoped)
	dir2, release2, ok := store2.Open("antigravity", "task-42")
	if !ok {
		t.Fatal("Open failed on VM2")
	}
	defer release2()

	if mem.GetCount() != 1 {
		t.Fatalf("GetCount on VM2 cache miss = %d, want 1", mem.GetCount())
	}
	restored, err := os.ReadFile(filepath.Join(dir2, ".gemini", "jetski", "brain", "conv-1", "transcript.jsonl"))
	if err != nil {
		t.Fatalf("reading restored conversation on VM2: %v", err)
	}
	if string(restored) != `{"step":1}`+"\n"+`{"step":2}`+"\n" {
		t.Fatalf("restored conversation = %q", restored)
	}
	if _, err := os.Stat(filepath.Join(dir2, ".cache")); !os.IsNotExist(err) {
		t.Errorf("ephemeral .cache directory should not have been archived, stat err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir2, "auth-link.json")); !os.IsNotExist(err) {
		t.Errorf("symlink should not have been archived, lstat err = %v", err)
	}
}

func TestClaudeArchiveSaveRestoreAndReadThrough(t *testing.T) {
	ctx := context.Background()
	mem := objectstore.NewMemory()
	scoped := objectstore.WithPrefix(mem, "agent-sessions")

	claudeDir1 := filepath.Join(t.TempDir(), "vm1-claude")
	projSlug := filepath.Join(claudeDir1, "projects", "-home-debian-work")
	if err := os.MkdirAll(filepath.Join(projSlug, "sess-abc-123", "cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projSlug, "sess-abc-123.jsonl"), []byte(`{"type":"assistant","session_id":"sess-abc-123"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projSlug, "sess-abc-123", "subagent.jsonl"), []byte(`{"sub":true}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projSlug, "sess-abc-123", "cache", "skip.bin"), []byte("ephemeral"), 0o600); err != nil {
		t.Fatal(err)
	}

	arch1 := NewClaudeArchive(scoped, claudeDir1)
	if err := arch1.SaveSession(ctx, "sess-abc-123"); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if _, ok := mem.Bytes("agent-sessions/claude/sess-abc-123.tar.gz"); !ok {
		t.Fatalf("expected claude session archive in MemoryStore, keys = %v", mem.Keys())
	}

	// Local read-through hit on VM1 does not issue a Get.
	mem.ResetCounts()
	if err := arch1.RestoreSession(ctx, "sess-abc-123"); err != nil {
		t.Fatalf("RestoreSession local hit: %v", err)
	}
	if mem.GetCount() != 0 {
		t.Fatalf("GetCount on local hit = %d, want 0", mem.GetCount())
	}

	// VM2: empty ~/.claude restores session JSONL and subagent dir (without ephemeral cache).
	claudeDir2 := filepath.Join(t.TempDir(), "vm2-claude")
	arch2 := NewClaudeArchive(scoped, claudeDir2)
	if err := arch2.RestoreSession(ctx, "sess-abc-123"); err != nil {
		t.Fatalf("RestoreSession on VM2: %v", err)
	}
	if mem.GetCount() != 1 {
		t.Fatalf("GetCount on VM2 miss = %d, want 1", mem.GetCount())
	}
	got, err := os.ReadFile(filepath.Join(claudeDir2, "projects", "-home-debian-work", "sess-abc-123.jsonl"))
	if err != nil {
		t.Fatalf("reading restored claude session: %v", err)
	}
	if string(got) != `{"type":"assistant","session_id":"sess-abc-123"}`+"\n" {
		t.Fatalf("restored claude session = %q", got)
	}
	if _, err := os.Stat(filepath.Join(claudeDir2, "projects", "-home-debian-work", "sess-abc-123", "subagent.jsonl")); err != nil {
		t.Fatalf("expected companion subagent.jsonl to be restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(claudeDir2, "projects", "-home-debian-work", "sess-abc-123", "cache")); !os.IsNotExist(err) {
		t.Errorf("ephemeral cache inside claude session should be excluded, err = %v", err)
	}
}

func TestExtractSafeTarGzRejectsTraversalAndSymlinks(t *testing.T) {
	makeTarGz := func(hdr *tar.Header, body []byte) *bytes.Reader {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		_ = tw.WriteHeader(hdr)
		if len(body) > 0 {
			_, _ = tw.Write(body)
		}
		_ = tw.Close()
		_ = gz.Close()
		return bytes.NewReader(buf.Bytes())
	}

	dest := t.TempDir()
	if err := extractSafeTarGz(makeTarGz(&tar.Header{
		Name:     "../escape.txt",
		Mode:     0o600,
		Size:     4,
		Typeflag: tar.TypeReg,
	}, []byte("evil")), dest, 1024); err == nil {
		t.Fatal("expected path traversal tar entry to be rejected")
	}

	if err := extractSafeTarGz(makeTarGz(&tar.Header{
		Name:     "link",
		Linkname: "/etc/passwd",
		Typeflag: tar.TypeSymlink,
	}, nil), dest, 1024); err == nil {
		t.Fatal("expected symlink tar entry to be rejected")
	}

	if err := extractSafeTarGz(makeTarGz(&tar.Header{
		Name:     "big.bin",
		Mode:     0o600,
		Size:     2048,
		Typeflag: tar.TypeReg,
	}, make([]byte, 2048)), dest, 1024); err == nil {
		t.Fatal("expected oversized tar entry to be rejected")
	}
}
