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

package secrets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// KeySource is where a Store gets its private key from. Today that is
// only ever a file (FileKeySource); the seam exists so that a key sealed
// somewhere else -- wrapped by a cloud KMS, held in a TPM
// (design/design/secrets-key-protection.design.md, Phases 2 and 5) --
// can stand in without every reader of the key learning about it.
//
// A Store calls Load once and keeps the key in memory for the life of
// the process, so a source whose Load is expensive or remote (one KMS
// unwrap) costs that once, not once per Resolve.
type KeySource interface {
	// Load returns the key, or an error wrapping ErrNoKey when the
	// source holds none yet.
	Load(ctx context.Context) (Key, error)
	// Describe names the source for an operator: a path, a KMS key
	// name. It never includes key material.
	Describe() string
}

// FileKeySource reads the key from a file in the form WriteKeyFile
// writes, which is what every grain install has done so far.
type FileKeySource struct {
	Path string
}

// Load reads the file. It is ReadKeyFile, unchanged.
func (f FileKeySource) Load(context.Context) (Key, error) { return ReadKeyFile(f.Path) }

// Describe is the path.
func (f FileKeySource) Describe() string { return f.Path }

// RetiredKeyMarker is what Store.ImportKey puts between a key file's
// name and the time it was replaced: secrets.key.replaced-20261110-052600.
const RetiredKeyMarker = ".replaced-"

// RetiredKey is a key file ImportKey moved aside when it installed
// another over it. Each one is a whole private key: it still opens any
// secrets file, secrets repository commit or backup sealed to it, which
// is why ImportKey never deletes one itself and why an operator should
// be shown that they are there.
type RetiredKey struct {
	Path string `json:"path"`
	// Name is the file's base name.
	Name string `json:"name"`
	// Public is the key's public half, or "" when the file could not be
	// read as a key.
	Public  string    `json:"public,omitempty"`
	ModTime time.Time `json:"modTime"`
}

// RetiredKeys lists the files beside keyFile that ImportKey archived,
// oldest first. A directory that is not there lists nothing.
func RetiredKeys(keyFile string) ([]RetiredKey, error) {
	if keyFile == "" {
		return nil, nil
	}
	dir, base := filepath.Dir(keyFile), filepath.Base(keyFile)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []RetiredKey
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), base+RetiredKeyMarker) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		rk := RetiredKey{Path: path, Name: e.Name()}
		if info, err := e.Info(); err == nil {
			rk.ModTime = info.ModTime().UTC()
		}
		if k, err := ReadKeyFile(path); err == nil {
			rk.Public = k.Public()
		}
		out = append(out, rk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// DeleteRetiredKey removes one archive RetiredKeys listed, named by its
// base name. It refuses anything else -- the live key above all, and any
// name that would reach out of the key's directory -- so that a typo
// cannot cost an operator the one key that still opens their secrets.
func DeleteRetiredKey(keyFile, name string) error {
	base := filepath.Base(keyFile)
	if name != filepath.Base(name) || !strings.HasPrefix(name, base+RetiredKeyMarker) || name == base+RetiredKeyMarker {
		return fmt.Errorf("secrets: %q is not a retired key beside %s (they are named %s%s<time>)", name, keyFile, base, RetiredKeyMarker)
	}
	path := filepath.Join(filepath.Dir(keyFile), name)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("secrets: no retired key %s", path)
		}
		return fmt.Errorf("secrets: deleting %s: %w", path, err)
	}
	return nil
}
