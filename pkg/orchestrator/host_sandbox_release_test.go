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

//go:build !windows

package orchestrator

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// populateReadOnlyGoModTree creates a nested 0555/0444 directory tree under
// root mimicking GOPATH/pkg/mod after `go test` / `go mod download`.
func populateReadOnlyGoModTree(t *testing.T, root string) {
	t.Helper()
	modRoot := filepath.Join(root, "go", "pkg", "mod")
	pkgDir := filepath.Join(modRoot, "example.com", "dep@v1.2.3", "sub")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	goFile := filepath.Join(pkgDir, "dep.go")
	if err := os.WriteFile(goFile, []byte("package sub\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	modFile := filepath.Join(modRoot, "example.com", "dep@v1.2.3", "go.mod")
	if err := os.WriteFile(modFile, []byte("module example.com/dep\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	// Lock directories bottom-up so child writes succeed before parent becomes 0555.
	for _, dir := range []string{
		pkgDir,
		filepath.Join(modRoot, "example.com", "dep@v1.2.3"),
		filepath.Join(modRoot, "example.com"),
		modRoot,
	} {
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHostSandboxReleaseReadOnlyTree(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	t.Cleanup(func() { _ = removeSandboxDir(baseDir) })

	hs := NewHostSandboxes(baseDir)
	sb, err := hs.Acquire(ctx, "run-release", Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	root, err := sb.(*hostSandbox).Root()
	if err != nil {
		t.Fatalf("Root: %v", err)
	}

	populateReadOnlyGoModTree(t, root)

	if err := sb.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("sandbox root %q still exists after Release: %v", root, err)
	}
}

func TestHostSandboxAcquireRebuildReapOrphansReadOnlyTree(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	t.Cleanup(func() { _ = removeSandboxDir(baseDir) })

	hs := NewHostSandboxes(baseDir)

	// 1. Acquire clears a pre-existing leftover directory containing 0555/0444 entries.
	staleRoot := filepath.Join(baseDir, "run-reacquire")
	populateReadOnlyGoModTree(t, staleRoot)

	sb, err := hs.Acquire(ctx, "run-reacquire", Shape{})
	if err != nil {
		t.Fatalf("Acquire over stale read-only dir: %v", err)
	}
	hsb := sb.(*hostSandbox)
	if _, err := os.Lstat(filepath.Join(hsb.root, "go")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale read-only tree still present after Acquire: %v", err)
	}

	// 2. Rebuild removes a read-only tree and recreates an empty 0700 sandbox root.
	populateReadOnlyGoModTree(t, hsb.root)
	if err := hsb.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(hsb.root, "go")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("read-only tree still present after Rebuild: %v", err)
	}
	info, err := os.Stat(hsb.root)
	if err != nil {
		t.Fatalf("Stat rebuilt root: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("rebuilt root perm = %#o, want 0700", info.Mode().Perm())
	}
	if err := sb.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// 3. ReapOrphans removes orphaned directories containing 0555/0444 entries.
	orphanRoot := filepath.Join(baseDir, "orphan-run")
	populateReadOnlyGoModTree(t, orphanRoot)
	reaped, err := hs.ReapOrphans(ctx)
	if err != nil {
		t.Fatalf("ReapOrphans: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("ReapOrphans reaped = %d, want 1", reaped)
	}
	if _, err := os.Lstat(orphanRoot); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("orphan dir %q still exists after ReapOrphans: %v", orphanRoot, err)
	}
}

func TestRemoveSandboxDirSymlinkLeavesExternalTargetIntact(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	baseDir := filepath.Join(parent, "sandboxes")
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// External directory tree and file with read-only permissions that must not
	// be removed or have their permissions altered.
	extDir := filepath.Join(parent, "external-dir")
	extSub := filepath.Join(extDir, "nested")
	if err := os.MkdirAll(extSub, 0o755); err != nil {
		t.Fatal(err)
	}
	extDirFile := filepath.Join(extSub, "data.txt")
	if err := os.WriteFile(extDirFile, []byte("keep-dir"), 0o444); err != nil {
		t.Fatal(err)
	}
	extFile := filepath.Join(parent, "external-file.txt")
	if err := os.WriteFile(extFile, []byte("keep-file"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(extSub, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(extDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(extDir, 0o755)
		_ = os.Chmod(extSub, 0o755)
		_ = os.Chmod(extDirFile, 0o644)
		_ = os.Chmod(extFile, 0o644)
	})

	hs := NewHostSandboxes(baseDir)
	sb, err := hs.Acquire(ctx, "run-symlink", Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	root, err := sb.(*hostSandbox).Root()
	if err != nil {
		t.Fatalf("Root: %v", err)
	}

	if err := os.Symlink(extDir, filepath.Join(root, "link-to-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(extFile, filepath.Join(root, "link-to-file")); err != nil {
		t.Fatal(err)
	}
	populateReadOnlyGoModTree(t, root)

	if err := sb.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("sandbox root %q still exists after Release: %v", root, err)
	}

	assertPermAndContent := func(path string, wantPerm fs.FileMode, wantContent string) {
		t.Helper()
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("Lstat(%q): %v", path, err)
		}
		if got := info.Mode().Perm(); got != wantPerm {
			t.Fatalf("%q perm = %#o, want %#o", path, got, wantPerm)
		}
		if wantContent != "" {
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(%q): %v", path, err)
			}
			if string(got) != wantContent {
				t.Fatalf("%q content = %q, want %q", path, got, wantContent)
			}
		}
	}

	assertPermAndContent(extDir, 0o555, "")
	assertPermAndContent(extSub, 0o555, "")
	assertPermAndContent(extDirFile, 0o444, "keep-dir")
	assertPermAndContent(extFile, 0o444, "keep-file")
}

func TestRemoveSandboxDirMissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := removeSandboxDir(missing); err != nil {
		t.Fatalf("removeSandboxDir(missing): %v", err)
	}
}
