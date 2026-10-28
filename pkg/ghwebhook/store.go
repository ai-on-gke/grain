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

package ghwebhook

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Retention is how long verified webhook envelopes stay in the receiver's
// JSONL log before periodic pruning drops them.
const Retention = 7 * 24 * time.Hour

const (
	compactEvery = 512
	maxStoreLine = 256 << 10
)

// Store persists verified webhook events in monotonic sequence order,
// deduplicating GitHub redeliveries by DeliveryID and waking long-polling
// readers immediately when a new event arrives.
type Store struct {
	mu              sync.Mutex
	events          []Event
	byDelivery      map[string]uint64
	nextSeq         uint64
	path            string
	file            *os.File
	now             func() time.Time
	notify          chan struct{}
	sinceCompaction int
}

// OpenStore opens or creates a JSONL webhook event store at path. An empty
// path creates an in-memory store for tests or single-process ephemeral use.
func OpenStore(path string) (*Store, error) {
	return openStore(path, time.Now)
}

func openStore(path string, now func() time.Time) (*Store, error) {
	s := &Store{
		byDelivery: map[string]uint64{},
		path:       path,
		now:        now,
		notify:     make(chan struct{}),
	}
	if path == "" {
		return s, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("ghwebhook: making directory for %s: %w", path, err)
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("ghwebhook: opening %s to append: %w", path, err)
	}
	s.file = f
	return s, nil
}

// Close closes the underlying append file, if any.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

func (s *Store) load() error {
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ghwebhook: reading %s: %w", s.path, err)
	}
	defer f.Close()

	cutoff := s.now().Add(-Retention)
	r := bufio.NewReaderSize(f, maxStoreLine)
	for lineNo := 1; ; lineNo++ {
		line, err := r.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			log.Printf("ghwebhook: %s line %d exceeds %d bytes; skipping", s.path, lineNo, maxStoreLine)
			for err == bufio.ErrBufferFull {
				_, err = r.ReadSlice('\n')
			}
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return fmt.Errorf("ghwebhook: reading %s: %w", s.path, err)
			}
			continue
		}
		if err != nil && err != io.EOF {
			return fmt.Errorf("ghwebhook: reading %s: %w", s.path, err)
		}
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var ev Event
			if jerr := json.Unmarshal(trimmed, &ev); jerr != nil {
				log.Printf("ghwebhook: %s line %d did not parse; skipping: %v", s.path, lineNo, jerr)
			} else {
				if ev.Seq > s.nextSeq {
					s.nextSeq = ev.Seq
				}
				if !ev.ReceivedAt.Before(cutoff) && ev.DeliveryID != "" {
					if _, dup := s.byDelivery[ev.DeliveryID]; !dup {
						s.byDelivery[ev.DeliveryID] = ev.Seq
						s.events = append(s.events, ev)
					}
				}
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}

// Append records ev if its DeliveryID has not been seen yet, assigning the
// next monotonic sequence number and waking any waiting consumers.
func (s *Store) Append(ev Event) (stored Event, duplicate bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if seq, ok := s.byDelivery[ev.DeliveryID]; ok {
		for _, existing := range s.events {
			if existing.Seq == seq {
				return existing, true, nil
			}
		}
		ev.Seq = seq
		return ev, true, nil
	}

	s.nextSeq++
	ev.Seq = s.nextSeq
	ev.ReceivedAt = s.now().UTC()
	if err := s.appendLocked(ev); err != nil {
		s.nextSeq--
		return Event{}, false, err
	}
	s.byDelivery[ev.DeliveryID] = ev.Seq
	s.events = append(s.events, ev)

	close(s.notify)
	s.notify = make(chan struct{})
	return ev, false, nil
}

func (s *Store) appendLocked(ev Event) error {
	if s.file == nil {
		return nil
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("ghwebhook: encoding event %s: %w", ev.DeliveryID, err)
	}
	if _, err := s.file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("ghwebhook: appending to %s: %w", s.path, err)
	}
	s.sinceCompaction++
	if s.sinceCompaction >= compactEvery {
		if err := s.compactLocked(); err != nil {
			log.Printf("ghwebhook: %v", err)
		}
	}
	return nil
}

// After returns up to limit events with Seq > after, plus the store's
// current highest sequence number.
func (s *Store) After(after uint64, limit int) ([]Event, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.afterLocked(after, limit), s.nextSeq
}

// WaitAfter returns immediately if any event has Seq > after, or blocks
// until one arrives or ctx expires.
func (s *Store) WaitAfter(ctx context.Context, after uint64, limit int) ([]Event, uint64) {
	for {
		s.mu.Lock()
		out := s.afterLocked(after, limit)
		latest := s.nextSeq
		ch := s.notify
		s.mu.Unlock()
		if len(out) > 0 || ctx.Err() != nil {
			return out, latest
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return s.After(after, limit)
		}
	}
}

func (s *Store) afterLocked(after uint64, limit int) []Event {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cutoff := s.now().Add(-Retention)
	var out []Event
	for _, ev := range s.events {
		if ev.Seq <= after {
			continue
		}
		if ev.ReceivedAt.Before(cutoff) {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// Prune drops expired events from memory and compacts the JSONL file.
func (s *Store) Prune() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.now().Add(-Retention)
	kept := s.events[:0]
	dropped := false
	for _, ev := range s.events {
		if ev.ReceivedAt.Before(cutoff) {
			delete(s.byDelivery, ev.DeliveryID)
			dropped = true
			continue
		}
		kept = append(kept, ev)
	}
	s.events = kept
	if !dropped {
		return nil
	}
	return s.compactLocked()
}

func (s *Store) compactLocked() error {
	if s.file == nil {
		return nil
	}
	tmp := s.path + ".compacting"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("ghwebhook: compacting %s: %w", s.path, err)
	}
	w := bufio.NewWriter(f)
	for _, ev := range s.events {
		line, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("ghwebhook: compacting %s: %w", s.path, err)
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("ghwebhook: compacting %s: %w", s.path, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("ghwebhook: compacting %s: %w", s.path, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("ghwebhook: replacing %s: %w", s.path, err)
	}
	reopened, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("ghwebhook: reopening %s: %w", s.path, err)
	}
	_ = s.file.Close()
	s.file = reopened
	s.sinceCompaction = 0
	return nil
}
