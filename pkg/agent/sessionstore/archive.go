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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/objectstore"
)

// ephemeralCacheDirNames are CLI tool cache, temporary, and crash/log
// directories inside a per-task home that stay local to the VM and are left out
// of durable agent-session archives.
var ephemeralCacheDirNames = map[string]bool{
	".cache":       true,
	"cache":        true,
	"tmp":          true,
	".tmp":         true,
	"temp":         true,
	"logs":         true,
	"node_modules": true,
	"Crashpad":     true,
	"GPUCache":     true,
	"Code Cache":   true,
	"CachedData":   true,
	"blob_storage": true,
}

// WithObjectStore attaches a backing objectstore.Store (typically scoped under
// "agent-sessions/") to s.
//
// On Open, if the task's home directory is not yet cached locally on disk, any
// durable bundle (<framework>/<taskID>.tar.gz) in store is extracted into place
// before the turn runs; local hits skip the object store altogether. On release
// (at the turn boundary, after the framework removes per-run configs and
// credential links), the conversation directory is archived (excluding
// ephemeral CLI tool caches) and uploaded back to store.
func (s *Store) WithObjectStore(store objectstore.Store) *Store {
	if s == nil {
		return nil
	}
	s.objects = store
	return s
}

func taskArchiveKey(framework, taskID string) string {
	return framework + "/" + taskID + ".tar.gz"
}

func (s *Store) restoreTaskArchive(ctx context.Context, framework, taskID, dir string) error {
	if s == nil || s.objects == nil {
		return nil
	}
	rc, info, err := s.objects.Get(ctx, taskArchiveKey(framework, taskID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer rc.Close()

	if s.ttl > 0 && !info.Updated.IsZero() && s.now().Sub(info.Updated) > s.ttl {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := extractSafeTarGz(rc, dir, s.maxTaskBytes); err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	return nil
}

func (s *Store) syncTaskArchive(ctx context.Context, framework, taskID, dir string) error {
	if s == nil || s.objects == nil {
		return nil
	}
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	if s.oversized(dir) {
		return nil
	}
	archiveBytes, count, err := packDirTarGz(dir, s.maxTaskBytes)
	if err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	return s.objects.Put(ctx, taskArchiveKey(framework, taskID), bytes.NewReader(archiveBytes))
}

// FlushAll uploads archives for every unlocked task directory currently present
// under s.root. Used during daemon drain and startup migration.
func (s *Store) FlushAll(ctx context.Context) error {
	if s == nil || s.root == "" || s.objects == nil {
		return nil
	}
	frameworks, err := os.ReadDir(s.root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var firstErr error
	for _, fwEntry := range frameworks {
		if !fwEntry.IsDir() || !validName(fwEntry.Name()) {
			continue
		}
		fw := fwEntry.Name()
		base := filepath.Join(s.root, fw)
		tasks, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, taskEntry := range tasks {
			if !taskEntry.IsDir() || !validName(taskEntry.Name()) {
				continue
			}
			taskID := taskEntry.Name()
			if s.locked(base, taskID) {
				continue
			}
			dir := filepath.Join(base, taskID)
			if err := s.syncTaskArchive(ctx, fw, taskID, dir); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// ClaudeArchive syncs Claude CLI's per-session files under
// <claudeDir>/projects/<project-slug>/<sessionID>.jsonl (and any companion
// <sessionID>/ directory) to and from an objectstore.Store under
// "claude/<sessionID>.tar.gz" with local read-through caching.
type ClaudeArchive struct {
	objects      objectstore.Store
	claudeDir    string
	ttl          time.Duration
	maxTaskBytes int64
	now          func() time.Time
}

// ResolveClaudeConfigDir returns $CLAUDE_CONFIG_DIR when set, or $HOME/.claude.
func ResolveClaudeConfigDir(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if dir := strings.TrimSpace(getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return filepath.Clean(dir)
	}
	if home := strings.TrimSpace(getenv("HOME")); home != "" {
		return filepath.Join(filepath.Clean(home), ".claude")
	}
	return ""
}

// NewClaudeArchive constructs a ClaudeArchive syncing sessions in claudeDir
// against store (scoped to "agent-sessions/"). Returns nil if store is nil or
// claudeDir is empty.
func NewClaudeArchive(store objectstore.Store, claudeDir string) *ClaudeArchive {
	if store == nil || strings.TrimSpace(claudeDir) == "" {
		return nil
	}
	if abs, err := filepath.Abs(claudeDir); err == nil {
		claudeDir = abs
	}
	return &ClaudeArchive{
		objects:      store,
		claudeDir:    claudeDir,
		ttl:          DefaultTTL,
		maxTaskBytes: DefaultMaxTaskBytes,
		now:          time.Now,
	}
}

// ValidSessionID reports whether id is a safe single-segment Claude session ID.
func ValidSessionID(id string) bool {
	if id == "" || len(id) > 128 || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func claudeSessionKey(sessionID string) string {
	return "claude/" + sessionID + ".tar.gz"
}

func (c *ClaudeArchive) hasLocalSession(projectsDir, sessionID string) bool {
	slugs, err := os.ReadDir(projectsDir)
	if err != nil {
		return false
	}
	wantFile := sessionID + ".jsonl"
	for _, slug := range slugs {
		if !slug.IsDir() {
			continue
		}
		if info, err := os.Stat(filepath.Join(projectsDir, slug.Name(), wantFile)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// RestoreSession ensures sessionID's JSONL transcript exists locally under
// <claudeDir>/projects/, serving local disk hits directly and downloading from
// the backing object store on cache misses.
func (c *ClaudeArchive) RestoreSession(ctx context.Context, sessionID string) error {
	if c == nil || c.objects == nil || !ValidSessionID(sessionID) {
		return nil
	}
	projectsDir := filepath.Join(c.claudeDir, "projects")
	if c.hasLocalSession(projectsDir, sessionID) {
		return nil
	}
	rc, info, err := c.objects.Get(ctx, claudeSessionKey(sessionID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer rc.Close()

	if c.ttl > 0 && !info.Updated.IsZero() && c.now().Sub(info.Updated) > c.ttl {
		return nil
	}
	if err := os.MkdirAll(projectsDir, 0o700); err != nil {
		return err
	}
	return extractSafeTarGz(rc, projectsDir, c.maxTaskBytes)
}

// SaveSession packs sessionID's files from <claudeDir>/projects/*/ and uploads
// them to claude/<sessionID>.tar.gz in the backing object store.
func (c *ClaudeArchive) SaveSession(ctx context.Context, sessionID string) error {
	if c == nil || c.objects == nil || !ValidSessionID(sessionID) {
		return nil
	}
	projectsDir := filepath.Join(c.claudeDir, "projects")
	slugs, err := os.ReadDir(projectsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	var (
		totalBytes int64
		filesAdded int
	)

	wantFile := sessionID + ".jsonl"
	for _, slug := range slugs {
		if !slug.IsDir() {
			continue
		}
		slugDir := filepath.Join(projectsDir, slug.Name())
		jsonlPath := filepath.Join(slugDir, wantFile)
		if info, err := os.Lstat(jsonlPath); err == nil && info.Mode().IsRegular() {
			if totalBytes+info.Size() > c.maxTaskBytes {
				_ = tw.Close()
				_ = gz.Close()
				return nil
			}
			rel := filepath.ToSlash(filepath.Join(slug.Name(), wantFile))
			if err := writeTarFile(tw, jsonlPath, rel, info); err != nil {
				_ = tw.Close()
				_ = gz.Close()
				return err
			}
			totalBytes += info.Size()
			filesAdded++
		}
		subDir := filepath.Join(slugDir, sessionID)
		if info, err := os.Lstat(subDir); err == nil && info.IsDir() {
			walkErr := filepath.WalkDir(subDir, func(path string, d fs.DirEntry, wErr error) error {
				if wErr != nil {
					return nil
				}
				if d.IsDir() {
					if ephemeralCacheDirNames[d.Name()] {
						return filepath.SkipDir
					}
					return nil
				}
				fi, err := d.Info()
				if err != nil || !fi.Mode().IsRegular() {
					return nil
				}
				if totalBytes+fi.Size() > c.maxTaskBytes {
					return errArchiveTooLarge
				}
				relPath, err := filepath.Rel(projectsDir, path)
				if err != nil {
					return nil
				}
				if err := writeTarFile(tw, path, filepath.ToSlash(relPath), fi); err != nil {
					return err
				}
				totalBytes += fi.Size()
				filesAdded++
				return nil
			})
			if errors.Is(walkErr, errArchiveTooLarge) {
				_ = tw.Close()
				_ = gz.Close()
				return nil
			}
			if walkErr != nil {
				_ = tw.Close()
				_ = gz.Close()
				return walkErr
			}
		}
	}

	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if filesAdded == 0 {
		return nil
	}
	return c.objects.Put(ctx, claudeSessionKey(sessionID), bytes.NewReader(buf.Bytes()))
}

// FlushAll uploads every local Claude session under <claudeDir>/projects/ to
// the backing object store.
func (c *ClaudeArchive) FlushAll(ctx context.Context) error {
	if c == nil || c.objects == nil {
		return nil
	}
	projectsDir := filepath.Join(c.claudeDir, "projects")
	slugs, err := os.ReadDir(projectsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	var firstErr error
	for _, slug := range slugs {
		if !slug.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(projectsDir, slug.Name()))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			id, ok := strings.CutSuffix(e.Name(), ".jsonl")
			if !ok || !ValidSessionID(id) || seen[id] {
				continue
			}
			seen[id] = true
			if err := c.SaveSession(ctx, id); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

var errArchiveTooLarge = errors.New("sessionstore: archive exceeds maxTaskBytes")

func packDirTarGz(root string, maxBytes int64) ([]byte, int, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	var (
		totalBytes int64
		count      int
	)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == root {
			return nil
		}
		if d.IsDir() {
			if ephemeralCacheDirNames[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), lockSuffix) || strings.HasPrefix(d.Name(), ".tmp-") {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		if maxBytes > 0 && totalBytes+info.Size() > maxBytes {
			return errArchiveTooLarge
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if err := writeTarFile(tw, path, filepath.ToSlash(rel), info); err != nil {
			return err
		}
		totalBytes += info.Size()
		count++
		return nil
	})
	if err != nil {
		_ = tw.Close()
		_ = gz.Close()
		if errors.Is(err, errArchiveTooLarge) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return nil, 0, err
	}
	if err := gz.Close(); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), count, nil
}

func writeTarFile(tw *tar.Writer, path, relSlash string, info fs.FileInfo) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	hdr := &tar.Header{
		Name:     relSlash,
		Mode:     0o600,
		Size:     info.Size(),
		ModTime:  info.ModTime().UTC(),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, io.LimitReader(f, info.Size()))
	return err
}

func extractSafeTarGz(r io.Reader, destRoot string, maxBytes int64) error {
	absRoot, err := filepath.Abs(destRoot)
	if err != nil {
		return err
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("sessionstore: reading gzip stream: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var extracted int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("sessionstore: reading tar entry: %w", err)
		}
		target, err := safeExtractPath(absRoot, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Size < 0 {
				return fmt.Errorf("sessionstore: negative tar entry size for %q", hdr.Name)
			}
			if maxBytes > 0 && extracted+hdr.Size > maxBytes {
				return fmt.Errorf("sessionstore: archive exceeds ceiling of %d bytes", maxBytes)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			limit := hdr.Size + 1
			if maxBytes > 0 && maxBytes-extracted+1 < limit {
				limit = maxBytes - extracted + 1
			}
			tmp, err := os.CreateTemp(filepath.Dir(target), ".tmp-*")
			if err != nil {
				return err
			}
			tmpName := tmp.Name()
			n, copyErr := io.Copy(tmp, io.LimitReader(tr, limit))
			closeErr := tmp.Close()
			if copyErr != nil || closeErr != nil || n > hdr.Size || (maxBytes > 0 && extracted+n > maxBytes) {
				_ = os.Remove(tmpName)
				if copyErr != nil {
					return copyErr
				}
				if closeErr != nil {
					return closeErr
				}
				return fmt.Errorf("sessionstore: archive entry %q exceeded declared or maximum size", hdr.Name)
			}
			_ = os.Chmod(tmpName, 0o600)
			if !hdr.ModTime.IsZero() {
				_ = os.Chtimes(tmpName, hdr.ModTime, hdr.ModTime)
			}
			if err := os.Rename(tmpName, target); err != nil {
				_ = os.Remove(tmpName)
				return err
			}
			extracted += n
		default:
			return fmt.Errorf("sessionstore: unsupported tar entry type %d for %q", hdr.Typeflag, hdr.Name)
		}
	}
	return nil
}

func safeExtractPath(absRoot, rawName string) (string, error) {
	if rawName == "" || strings.HasPrefix(rawName, "/") || strings.ContainsRune(rawName, '\\') || strings.ContainsRune(rawName, 0) {
		return "", fmt.Errorf("sessionstore: invalid tar path %q", rawName)
	}
	trimmed := strings.TrimSuffix(rawName, "/")
	if trimmed == "" || trimmed == "." {
		return absRoot, nil
	}
	for _, seg := range strings.Split(trimmed, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("sessionstore: unsafe tar path segment %q in %q", seg, rawName)
		}
	}
	target := filepath.Join(absRoot, filepath.FromSlash(trimmed))
	rel, err := filepath.Rel(absRoot, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("sessionstore: tar path %q escapes %q", rawName, absRoot)
	}
	return target, nil
}
