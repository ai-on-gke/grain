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
	"path/filepath"
	"sync"
)

// sharedStores is OpenShared's per-key-file cache.
var sharedStores sync.Map

// OpenShared is Open for a reader that would otherwise open the same
// store again on every call: one Store per key file per process. A Store
// loads its key once and keeps it, and a KMS-wrapped key (KMSKeySource)
// is a network unwrap, so a fresh Store per call is one Cloud KMS
// decrypt per call -- a repository token read on every config sync, say.
// A Store notices a key installed under it by another (ErrWrongKey, then
// a reload), so sharing it loses nothing. An unreadable store (a key not
// imported yet, KMS refusing) is not cached, so the next call opens it
// afresh. Like Open, it mints a key on a fresh install; callers that
// must never mint check for the key file first. A Config naming its own
// KeySource is not shared: the cache is keyed by file paths alone, and a
// caller that chose where its key comes from must not be handed a Store
// that loads it from somewhere else.
func OpenShared(cfg Config) *Store {
	if cfg.KeySource != nil {
		return Open(cfg)
	}
	id := filepath.Clean(cfg.KeyFile) + "\x00" + filepath.Clean(cfg.File)
	if v, ok := sharedStores.Load(id); ok {
		return v.(*Store)
	}
	store := Open(cfg)
	if err := store.Check(); err != nil {
		return store
	}
	v, _ := sharedStores.LoadOrStore(id, store)
	return v.(*Store)
}
