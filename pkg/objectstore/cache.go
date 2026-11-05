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

package objectstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ReadThroughCache caches objects from a backing Store in a local directory.
// Hits are served directly from local disk without contacting the backing
// Store; misses fetch from Store, populate local disk atomically, and serve the
// cached file.
type ReadThroughCache struct {
	store Store
	dir   string
}

// NewReadThroughCache opens or creates dir as a read-through cache fronting store.
func NewReadThroughCache(store Store, dir string) (*ReadThroughCache, error) {
	if store == nil {
		return nil, errors.New("objectstore: backing store is nil")
	}
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("objectstore: cache directory is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("objectstore: resolving cache directory %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("objectstore: creating cache directory %q: %w", abs, err)
	}
	if err := os.Chmod(abs, 0o700); err != nil {
		return nil, fmt.Errorf("objectstore: restricting cache directory %q: %w", abs, err)
	}
	return &ReadThroughCache{store: store, dir: abs}, nil
}

// Store returns the backing Store.
func (c *ReadThroughCache) Store() Store { return c.store }

// Dir returns the local cache directory.
func (c *ReadThroughCache) Dir() string { return c.dir }

func (c *ReadThroughCache) localPath(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	target := filepath.Join(c.dir, filepath.FromSlash(key))
	rel, err := filepath.Rel(c.dir, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("objectstore: key %q escapes cache directory", key)
	}
	return target, nil
}

// PutBytes writes data to the backing Store and populates the local cache.
func (c *ReadThroughCache) PutBytes(ctx context.Context, key string, data []byte) error {
	if _, err := c.localPath(key); err != nil {
		return err
	}
	if err := c.store.Put(ctx, key, bytes.NewReader(data)); err != nil {
		return err
	}
	return c.CacheLocalBytes(key, data)
}

// CacheLocalBytes populates only the local cache entry for key without writing
// to the backing Store.
func (c *ReadThroughCache) CacheLocalBytes(key string, data []byte) error {
	target, err := c.localPath(key)
	if err != nil {
		return err
	}
	return writeFileAtomic(target, bytes.NewReader(data))
}

// Open returns an open local file and metadata for key, fetching from the
// backing Store and populating the local cache on a cache miss.
func (c *ReadThroughCache) Open(ctx context.Context, key string) (*os.File, ObjectInfo, error) {
	target, err := c.localPath(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	if f, err := os.Open(target); err == nil {
		if info, statErr := f.Stat(); statErr == nil && !info.IsDir() {
			return f, ObjectInfo{
				Key:     key,
				Size:    info.Size(),
				Updated: info.ModTime().UTC(),
			}, nil
		}
		f.Close()
	}

	rc, remoteInfo, err := c.store.Get(ctx, key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	defer rc.Close()

	if err := writeFileAtomic(target, rc); err != nil {
		return nil, ObjectInfo{}, err
	}
	if !remoteInfo.Updated.IsZero() {
		_ = os.Chtimes(target, remoteInfo.Updated, remoteInfo.Updated)
	}
	f, err := os.Open(target)
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: opening cached %q: %w", key, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: stat cached %q: %w", key, err)
	}
	updated := remoteInfo.Updated
	if updated.IsZero() {
		updated = info.ModTime().UTC()
	}
	return f, ObjectInfo{
		Key:     key,
		Size:    info.Size(),
		Updated: updated,
	}, nil
}

// GetBytes returns key's bytes via the read-through cache.
func (c *ReadThroughCache) GetBytes(ctx context.Context, key string) ([]byte, error) {
	f, _, err := c.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// InvalidateLocal removes key from the local cache without deleting it from
// the backing Store.
func (c *ReadThroughCache) InvalidateLocal(key string) error {
	target, err := c.localPath(key)
	if err != nil {
		return err
	}
	err = os.Remove(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// ClearLocal removes all cached files from the local cache directory while
// preserving every object in the backing Store. Returns the count and total
// bytes removed.
func (c *ReadThroughCache) ClearLocal() (files int, bytesFreed int64, err error) {
	entries, readErr := os.ReadDir(c.dir)
	if errors.Is(readErr, fs.ErrNotExist) {
		return 0, 0, nil
	}
	if readErr != nil {
		return 0, 0, readErr
	}
	for _, entry := range entries {
		sub := filepath.Join(c.dir, entry.Name())
		n, sz := countDirFiles(sub)
		if rmErr := os.RemoveAll(sub); rmErr != nil {
			if err == nil {
				err = rmErr
			}
			continue
		}
		files += n
		bytesFreed += sz
	}
	return files, bytesFreed, err
}

func countDirFiles(root string) (int, int64) {
	info, err := os.Lstat(root)
	if err != nil {
		return 0, 0
	}
	if !info.IsDir() {
		return 1, info.Size()
	}
	var count int
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			count++
			total += fi.Size()
		}
		return nil
	})
	return count, total
}
