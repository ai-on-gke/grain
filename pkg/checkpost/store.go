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

package checkpost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Retention is how long a verdict is kept.
//
// A verdict is worth having for as long as something might still gate on
// the commit it is about, and that is bounded by how long a pull request
// sits between its last push and its merge -- hours on this deployment,
// and days on a change waiting for a person. A week is comfortably past
// both and small enough that the whole file stays trivial.
//
// The cost of pruning too early is worse than the cost of keeping too
// long, which is why it is generous: a reader that finds nothing sees
// silence, and silence on a commit whose checks really did pass is a
// pull request that waits for a person rather than merging. Nothing is
// mis-merged either way -- absence is never a pass -- but one of the two
// wastes somebody's afternoon.
const Retention = 7 * 24 * time.Hour

// Store holds what has been reported, in memory, with an append-only
// file behind it so a restart does not lose a verdict.
//
// # Why a file and not SQLite
//
// grain's own store is SQLite and this deliberately is not. What is held
// here is a few hundred rows of small, flat, write-once records with one
// lookup key, kept for a week -- there is no query here worth a query
// planner, no schema worth migrating, and no second reader. Against that,
// the dependency is the whole of modernc.org/sqlite in a binary whose
// reason for existing is to be small enough to expose to the internet.
//
// The file is JSON lines: one Verdict per line, appended, never rewritten
// except by the compaction that drops what pruning has already dropped
// from memory. A line that will not parse is skipped with a warning
// rather than failing the load, because the alternative is a service
// that will not start over one torn write -- and a torn write is exactly
// what a power cut during an append leaves.
type Store struct {
	mu       sync.RWMutex
	verdicts map[key]Verdict
	path     string
	file     *os.File
	now      func() time.Time
	// sinceCompaction counts appends, so compaction is amortised rather
	// than run on a timer this package would have to own.
	sinceCompaction int
}

// compactEvery is how many appends go by before the file is rewritten
// without the lines pruning has dropped. Amortised rather than timed:
// this service has no other reason to hold a goroutine, and a rewrite
// costs a few hundred lines of JSON.
const compactEvery = 512

// OpenStore loads whatever path already holds and returns a Store that
// appends to it.
//
// An empty path is a Store that keeps nothing across restarts -- for
// tests, and for anybody running this to try it out. It is not a
// supported way to deploy: losing verdicts on restart means a pull
// request whose checks passed reads as silence, which is a merge that
// waits for a person who does not know they are being waited for.
func OpenStore(path string) (*Store, error) { return openStore(path, time.Now) }

// openStore is OpenStore with the clock it loads by.
//
// load is the one thing here that decides against the clock before its
// caller has a Store to put a clock on, so a test that freezes time has
// no way to reach it through OpenStore -- which left the cutoff load
// applies as the only rule in this file with no test that fixes what it
// is measured from. Tests reopen through this instead.
func openStore(path string, now func() time.Time) (*Store, error) {
	s := &Store{verdicts: map[key]Verdict{}, path: path, now: now}
	if path == "" {
		return s, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("checkpost: making the directory for %s: %w", path, err)
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("checkpost: opening %s to append: %w", path, err)
	}
	s.file = f
	return s, nil
}

// load reads the file into memory, dropping what Retention has already
// expired so a long-stopped service does not come back holding a month
// of history.
func (s *Store) load() error {
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checkpost: reading %s: %w", s.path, err)
	}
	defer f.Close()

	cutoff := s.now().Add(-Retention)
	// A verdict is small, and a line longer than maxLine is damage rather
	// than data. ReadSlice rather than a bufio.Scanner, because a Scanner
	// handed a line past its ceiling stops altogether with ErrTooLong --
	// which made one over-long line the thing that kept this service from
	// starting, the very outcome skipping torn lines is there to prevent.
	r := bufio.NewReaderSize(f, maxLine)
	for lineNo := 1; ; lineNo++ {
		line, err := r.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			// Over the ceiling: discard the rest of it, up to its newline
			// or the end of the file, and carry on with the next.
			log.Printf("checkpost: %s line %d is longer than %d bytes; skipping it", s.path, lineNo, maxLine)
			for err == bufio.ErrBufferFull {
				_, err = r.ReadSlice('\n')
			}
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return fmt.Errorf("checkpost: reading %s: %w", s.path, err)
			}
			continue
		}
		if err != nil && err != io.EOF {
			return fmt.Errorf("checkpost: reading %s: %w", s.path, err)
		}
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var v Verdict
			if jerr := json.Unmarshal(trimmed, &v); jerr != nil {
				// Skipped rather than fatal: one torn write -- which is what
				// a power cut during an append leaves -- must not stop this
				// service starting.
				log.Printf("checkpost: %s line %d does not parse; skipping it: %v", s.path, lineNo, jerr)
			} else if !v.ReportedAt.Before(cutoff) {
				s.verdicts[v.key()] = v
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}

// maxLine is the longest line load will read as a verdict. A verdict is
// a few hundred bytes; this is the same ceiling the server puts on a
// whole request body, so nothing Put could have written is over it.
const maxLine = maxBody

// Put records one verdict, replacing whatever was held for the same
// repo, commit and name -- if it may (supersedes).
//
// A report that may not is refused with *Superseded and recorded
// nowhere: a re-run's answer replaces the failure it was re-run to clear
// only when it says which attempt it was, and a passing report from the
// same attempt as a failing one cannot overwrite it. The last report no
// longer wins merely by being last (#2401).
//
// ReportedAt is stamped here, from this service's clock, whatever the
// caller sent. A reporter that could set its own timestamp could keep a
// verdict past its retention or make a stale one look fresh.
func (s *Store) Put(v Verdict) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if held, ok := s.verdicts[v.key()]; ok && !held.ReportedAt.Before(s.now().Add(-Retention)) && !supersedes(v, held) {
		return &Superseded{Held: held, Sent: v}
	}
	v.ReportedAt = s.now().UTC()
	s.verdicts[v.key()] = v
	return s.append(v)
}

// append writes one line, and compacts every so often.
//
// The write is not fsynced. Losing the last verdict to a power cut costs
// one pull request one re-report or one person one look, and fsyncing
// every append would make this service's latency the reporter's problem
// -- a workflow job holding a runner open while a disk settles.
func (s *Store) append(v Verdict) error {
	if s.file == nil {
		return nil
	}
	line, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("checkpost: encoding a verdict for %s: %w", v.Repo, err)
	}
	if _, err := s.file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("checkpost: appending to %s: %w", s.path, err)
	}
	s.sinceCompaction++
	if s.sinceCompaction >= compactEvery {
		// Logged rather than returned: the verdict is already on disk, so
		// an error here would tell the reporter its report failed when it
		// was recorded. A compaction that fails is retried on the next
		// append, since the count is only reset by one that succeeds;
		// until then the file is only longer than it needs to be.
		if err := s.compactLocked(); err != nil {
			log.Printf("checkpost: %v", err)
		}
	}
	return nil
}

// For returns everything reported about one commit, oldest report first.
//
// Expired entries are dropped as they are found rather than by a
// sweeper: this is the only path that reads them, so a verdict nobody
// asks about costs nothing by staying, and one somebody does ask about
// is checked at exactly the moment its age matters.
func (s *Store) For(repo, commit string) []Verdict {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.now().Add(-Retention)
	var out []Verdict
	for k, v := range s.verdicts {
		if k.repo != repo || k.commit != commit {
			continue
		}
		if v.ReportedAt.Before(cutoff) {
			delete(s.verdicts, k)
			continue
		}
		out = append(out, v)
	}
	sortByReportedAt(out)
	return out
}

// Prune drops everything past Retention and rewrites the file without
// it. Called on a schedule by the server so that a store nobody is
// reading does not grow without bound.
func (s *Store) Prune() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.now().Add(-Retention)
	dropped := false
	for k, v := range s.verdicts {
		if v.ReportedAt.Before(cutoff) {
			delete(s.verdicts, k)
			dropped = true
		}
	}
	if !dropped {
		return nil
	}
	return s.compactLocked()
}

// compactLocked rewrites the file from what is in memory. Through a
// temporary file and a rename, so a crash midway leaves the old file
// whole rather than a half-written one: this is the only operation here
// that could lose more than one verdict.
func (s *Store) compactLocked() error {
	if s.file == nil {
		return nil
	}
	tmp := s.path + ".compacting"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("checkpost: compacting %s: %w", s.path, err)
	}
	w := bufio.NewWriter(f)
	for _, v := range s.verdicts {
		line, err := json.Marshal(v)
		if err != nil {
			continue
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("checkpost: compacting %s: %w", s.path, err)
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("checkpost: compacting %s: %w", s.path, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("checkpost: compacting %s: %w", s.path, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("checkpost: replacing %s: %w", s.path, err)
	}
	// The handle this Store appends through still points at the file that
	// was just replaced, so it has to be reopened or every later append
	// would go to an unlinked inode.
	old := s.file
	reopened, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("checkpost: reopening %s after compaction: %w", s.path, err)
	}
	s.file = reopened
	s.sinceCompaction = 0
	return old.Close()
}

// Close flushes nothing -- every append is already written -- and closes
// the file.
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

// sortByReportedAt orders a commit's verdicts oldest first, and by name
// where two arrived in the same instant, so a reader's rendering is
// stable rather than map-order.
func sortByReportedAt(vs []Verdict) {
	for i := 1; i < len(vs); i++ {
		for j := i; j > 0; j-- {
			a, b := vs[j-1], vs[j]
			if a.ReportedAt.Before(b.ReportedAt) || (a.ReportedAt.Equal(b.ReportedAt) && a.Name <= b.Name) {
				break
			}
			vs[j-1], vs[j] = b, a
		}
	}
}
