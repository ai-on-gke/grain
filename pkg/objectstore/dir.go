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
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DirStore implements Store over a local filesystem directory (file:// URIs).
type DirStore struct {
	root string
}

// NewDir opens or creates root as a local filesystem-backed Store.
func NewDir(root string) (*DirStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("objectstore: directory path is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("objectstore: resolving directory %q: %w", root, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("objectstore: creating directory %q: %w", abs, err)
	}
	return &DirStore{root: abs}, nil
}

// Root returns the absolute directory backing d.
func (d *DirStore) Root() string { return d.root }

func (d *DirStore) pathFor(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	target := filepath.Join(d.root, filepath.FromSlash(key))
	rel, err := filepath.Rel(d.root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("objectstore: key %q escapes root", key)
	}
	return target, nil
}

// Put implements Store.
func (d *DirStore) Put(ctx context.Context, key string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := d.pathFor(key)
	if err != nil {
		return err
	}
	return writeFileAtomic(target, r)
}

// Get implements Store.
func (d *DirStore) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, ObjectInfo{}, err
	}
	target, err := d.pathFor(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	linfo, err := os.Lstat(target)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ObjectInfo{}, fmt.Errorf("objectstore: %q: %w", key, ErrNotExist)
		}
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: stat %q: %w", key, err)
	}
	if !linfo.Mode().IsRegular() {
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: %q: %w", key, ErrNotExist)
	}
	f, err := os.Open(target)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ObjectInfo{}, fmt.Errorf("objectstore: %q: %w", key, ErrNotExist)
		}
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: opening %q: %w", key, err)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if info != nil && !info.Mode().IsRegular() {
			return nil, ObjectInfo{}, fmt.Errorf("objectstore: %q: %w", key, ErrNotExist)
		}
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: stat %q: %w", key, err)
	}
	return f, ObjectInfo{
		Key:     key,
		Size:    info.Size(),
		Updated: info.ModTime().UTC(),
	}, nil
}

// Stat implements Store.
func (d *DirStore) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	target, err := d.pathFor(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	info, err := os.Lstat(target)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ObjectInfo{}, fmt.Errorf("objectstore: %q: %w", key, ErrNotExist)
		}
		return ObjectInfo{}, fmt.Errorf("objectstore: stat %q: %w", key, err)
	}
	if !info.Mode().IsRegular() {
		return ObjectInfo{}, fmt.Errorf("objectstore: %q: %w", key, ErrNotExist)
	}
	return ObjectInfo{
		Key:     key,
		Size:    info.Size(),
		Updated: info.ModTime().UTC(),
	}, nil
}

// Delete implements Store.
func (d *DirStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := d.pathFor(key)
	if err != nil {
		return err
	}
	err = os.Remove(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// List implements Store.
func (d *DirStore) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidatePrefix(prefix); err != nil {
		return nil, err
	}
	var out []ObjectInfo
	err := filepath.WalkDir(d.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			return nil
		}
		rel, err := filepath.Rel(d.root, path)
		if err != nil {
			return nil
		}
		key := filepath.ToSlash(rel)
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		out = append(out, ObjectInfo{
			Key:     key,
			Size:    info.Size(),
			Updated: info.ModTime().UTC(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("objectstore: listing %q: %w", prefix, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func writeFileAtomic(target string, r io.Reader) error {
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("objectstore: creating directory %q: %w", parent, err)
	}
	tmp, err := os.CreateTemp(parent, ".tmp-*")
	if err != nil {
		return fmt.Errorf("objectstore: creating temp file in %q: %w", parent, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return fmt.Errorf("objectstore: writing %q: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("objectstore: closing %q: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("objectstore: chmod %q: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("objectstore: renaming %q to %q: %w", tmpName, target, err)
	}
	return nil
}
