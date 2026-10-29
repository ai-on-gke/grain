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

package sandboxaccess

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// FileCAStore keeps the CA bundle in one owner-only file: for a
// deployment with no Secret Manager, where the key is then only as safe
// as the daemon's data directory.
type FileCAStore struct {
	Path string
}

// Load implements CAStore.
func (s FileCAStore) Load(context.Context) ([]byte, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoCA
	}
	return data, err
}

// Save implements CAStore, atomically.
func (s FileCAStore) Save(_ context.Context, bundle []byte) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".ca-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bundle); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.Path)
}
