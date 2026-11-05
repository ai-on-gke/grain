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
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryStore is a concurrency-safe in-memory Store fake for unit and
// integration tests. It records operation counts so tests can assert that
// read-through caches serve local hits without issuing remote Get calls.
type MemoryStore struct {
	mu      sync.RWMutex
	objects map[string]memoryObject
	now     func() time.Time

	putCalls    int
	getCalls    int
	statCalls   int
	deleteCalls int
	listCalls   int
	keyGets     map[string]int
}

type memoryObject struct {
	data    []byte
	updated time.Time
}

// NewMemory returns an empty in-memory Store fake.
func NewMemory() *MemoryStore {
	return &MemoryStore{
		objects: make(map[string]memoryObject),
		now:     time.Now,
		keyGets: make(map[string]int),
	}
}

// WithClock sets the clock used for ObjectInfo.Updated on Put.
func (m *MemoryStore) WithClock(now func() time.Time) *MemoryStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
	return m
}

func (m *MemoryStore) clock() time.Time {
	if m.now != nil {
		return m.now().UTC()
	}
	return time.Now().UTC()
}

// Put implements Store.
func (m *MemoryStore) Put(ctx context.Context, key string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateKey(key); err != nil {
		return err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("objectstore: memory put %q: %w", key, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.putCalls++
	m.objects[key] = memoryObject{
		data:    bytes.Clone(data),
		updated: m.clock(),
	}
	return nil
}

// Get implements Store.
func (m *MemoryStore) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, ObjectInfo{}, err
	}
	if err := ValidateKey(key); err != nil {
		return nil, ObjectInfo{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	m.keyGets[key]++
	obj, ok := m.objects[key]
	if !ok {
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: %q: %w", key, ErrNotExist)
	}
	cp := bytes.Clone(obj.data)
	return io.NopCloser(bytes.NewReader(cp)), ObjectInfo{
		Key:     key,
		Size:    int64(len(cp)),
		Updated: obj.updated,
	}, nil
}

// Stat implements Store.
func (m *MemoryStore) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	if err := ValidateKey(key); err != nil {
		return ObjectInfo{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statCalls++
	obj, ok := m.objects[key]
	if !ok {
		return ObjectInfo{}, fmt.Errorf("objectstore: %q: %w", key, ErrNotExist)
	}
	return ObjectInfo{
		Key:     key,
		Size:    int64(len(obj.data)),
		Updated: obj.updated,
	}, nil
}

// Delete implements Store.
func (m *MemoryStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateKey(key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteCalls++
	delete(m.objects, key)
	return nil
}

// List implements Store.
func (m *MemoryStore) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidatePrefix(prefix); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listCalls++
	var out []ObjectInfo
	for k, obj := range m.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, ObjectInfo{
				Key:     k,
				Size:    int64(len(obj.data)),
				Updated: obj.updated,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// PutCount returns the total number of Put calls received.
func (m *MemoryStore) PutCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.putCalls
}

// GetCount returns the total number of Get calls received.
func (m *MemoryStore) GetCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.getCalls
}

// StatCount returns the total number of Stat calls received.
func (m *MemoryStore) StatCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.statCalls
}

// DeleteCount returns the total number of Delete calls received.
func (m *MemoryStore) DeleteCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.deleteCalls
}

// ListCount returns the total number of List calls received.
func (m *MemoryStore) ListCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.listCalls
}

// KeyGetCount returns how many Get calls targeted key specifically.
func (m *MemoryStore) KeyGetCount(key string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.keyGets[key]
}

// Keys returns all stored object keys in sorted order.
func (m *MemoryStore) Keys() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0, len(m.objects))
	for k := range m.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Bytes returns a copy of key's stored bytes and true, or nil, false if absent.
func (m *MemoryStore) Bytes(key string) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	obj, ok := m.objects[key]
	if !ok {
		return nil, false
	}
	return bytes.Clone(obj.data), true
}

// ResetCounts zeroes all recorded operation counters without deleting objects.
func (m *MemoryStore) ResetCounts() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.putCalls = 0
	m.getCalls = 0
	m.statCalls = 0
	m.deleteCalls = 0
	m.listCalls = 0
	m.keyGets = make(map[string]int)
}
