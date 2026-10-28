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

// What these pin is the half of the feature that is grain's own: a
// directory that outlives a run, one per task, that two runs cannot share
// and that cannot grow without a bound. What a CLI does inside it is the
// framework packages' business (agent/codex's and agent/antigravity's own
// suspend_test.go).

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The point of the package: the same task gets the same directory on its
// next dispatch, with whatever the CLI left in it still there.
func TestATaskGetsTheSameHomeOnItsNextDispatch(t *testing.T) {
	s := New(t.TempDir())

	first, release, ok := s.Open("codex", "42")
	if !ok {
		t.Fatal("Open said no on a fresh store")
	}
	if err := os.WriteFile(filepath.Join(first, "rollout.jsonl"), []byte("a thread"), 0o600); err != nil {
		t.Fatal(err)
	}
	release()

	second, release, ok := s.Open("codex", "42")
	if !ok {
		t.Fatal("Open said no on the task's second dispatch")
	}
	defer release()
	if second != first {
		t.Errorf("second dispatch got %q, want the first dispatch's own %q", second, first)
	}
	if b, err := os.ReadFile(filepath.Join(second, "rollout.jsonl")); err != nil || string(b) != "a thread" {
		t.Errorf("the conversation the first run left did not survive: %q, %v", b, err)
	}
}

// One task's conversation is never another's, and never another
// framework's -- the whole of what keeps this store from being the shared
// registration both framework packages build a private home to avoid.
func TestEachTaskAndFrameworkGetsItsOwnHome(t *testing.T) {
	s := New(t.TempDir())

	a, releaseA, _ := s.Open("codex", "1")
	defer releaseA()
	b, releaseB, _ := s.Open("codex", "2")
	defer releaseB()
	c, releaseC, _ := s.Open("antigravity", "1")
	defer releaseC()

	if a == b {
		t.Errorf("two tasks share the home %q", a)
	}
	if a == c {
		t.Errorf("two frameworks share the home %q", a)
	}
}

// A second run of the same task, while the first still holds it, gets no
// store at all rather than the same directory: a caller told no builds
// the private per-run home it always built.
func TestASecondRunOfTheSameTaskIsRefused(t *testing.T) {
	s := New(t.TempDir())

	_, release, ok := s.Open("codex", "7")
	if !ok {
		t.Fatal("Open said no on a fresh store")
	}
	if _, _, ok := s.Open("codex", "7"); ok {
		t.Error("Open handed the same task's home to a second run while the first was still in it")
	}
	release()
	if _, release, ok := s.Open("codex", "7"); !ok {
		t.Error("Open still refuses the task after the first run released it")
	} else {
		release()
	}
}

// And a lock nobody can still be holding -- a daemon killed mid-run --
// must not cost that task its store forever.
func TestAnAbandonedLockIsTakenOver(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	_, _, ok := s.Open("codex", "7") // released by nobody: the daemon died here
	if !ok {
		t.Fatal("Open said no on a fresh store")
	}

	later := New(root)
	later.now = func() time.Time { return time.Now().Add(lockTTL + time.Hour) }
	if _, release, ok := later.Open("codex", "7"); !ok {
		t.Error("a lock older than lockTTL still refused the task its store")
	} else {
		release()
	}
}

// The first bound: a task nothing has dispatched for TTL loses its store,
// which is the case a resume would have been worthless in anyway.
func TestAStoreNothingHasUsedForTTLIsDropped(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	old, release, _ := s.Open("codex", "1")
	release()
	if err := os.WriteFile(filepath.Join(old, "rollout.jsonl"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	later := New(root)
	later.now = func() time.Time { return time.Now().Add(DefaultTTL + time.Hour) }
	_, release, ok := later.Open("codex", "2")
	if !ok {
		t.Fatal("Open said no")
	}
	defer release()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("a task store older than the TTL was still there: %v", err)
	}
}

// The second: however busy a deployment is, only so many tasks' stores
// are kept, and they are the ones most recently dispatched.
func TestOnlyTheMostRecentlyUsedTasksAreKept(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	s.maxTasks = 2

	base := time.Now()
	for i, id := range []string{"1", "2", "3"} {
		s.now = func() time.Time { return base.Add(time.Duration(i) * time.Minute) }
		_, release, ok := s.Open("codex", id)
		if !ok {
			t.Fatalf("Open(%q) said no", id)
		}
		release()
	}
	// Opening a fourth is what prunes: three stores were kept while the
	// bound was being reached, and the oldest goes when it is exceeded.
	s.now = func() time.Time { return base.Add(time.Hour) }
	_, release, _ := s.Open("codex", "4")
	defer release()

	if _, err := os.Stat(filepath.Join(root, "codex", "1")); !os.IsNotExist(err) {
		t.Errorf("the least recently used task's store survived the count bound: %v", err)
	}
	for _, id := range []string{"3", "4"} {
		if _, err := os.Stat(filepath.Join(root, "codex", id)); err != nil {
			t.Errorf("task %s's store was pruned though it is among the most recent: %v", id, err)
		}
	}
}

// A live run's store is never pruned out from under it, whatever the
// bounds say about its age.
func TestPruningLeavesALiveRunsStoreAlone(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	live, release, _ := s.Open("codex", "1")
	defer release()

	// A TTL short enough that every store in here has aged out, rather
	// than a clock moved forward: a clock far enough ahead to age a
	// store out would age this run's own lock out with it, which is the
	// crash case rather than this one.
	later := New(root)
	later.ttl = -time.Hour
	_, release2, _ := later.Open("codex", "2")
	defer release2()

	if _, err := os.Stat(live); err != nil {
		t.Errorf("a run holding its store had it pruned away: %v", err)
	}
}

// The third: one task's store cannot grow without limit either -- a CLI
// that unpacks a cache into the home it is given would otherwise keep it
// forever.
func TestATaskStorePastItsCeilingIsDropped(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	s.maxTaskBytes = 128

	dir, release, _ := s.Open("codex", "1")
	if err := os.WriteFile(filepath.Join(dir, "cache"), make([]byte, 1024), 0o600); err != nil {
		t.Fatal(err)
	}
	release()

	dir, release, ok := s.Open("codex", "1")
	if !ok {
		t.Fatal("Open said no")
	}
	defer release()
	if _, err := os.Stat(filepath.Join(dir, "cache")); !os.IsNotExist(err) {
		t.Errorf("a task store past its ceiling kept its contents: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the store itself is gone, so this run has nowhere to work: %v", err)
	}
}

// Every way of having no store answers the same: no, and the caller
// builds the home it always built.
func TestNoStoreIsNoStore(t *testing.T) {
	var nilStore *Store
	for _, tc := range []struct {
		name      string
		store     *Store
		framework string
		task      string
	}{
		{"nil store", nilStore, "codex", "1"},
		{"no root configured", New(""), "codex", "1"},
		{"no task id", New(t.TempDir()), "codex", ""},
		{"no framework", New(t.TempDir()), "", "1"},
		{"a task id that is a path", New(t.TempDir()), "codex", "../../etc"},
		{"a task id that is a dot", New(t.TempDir()), "codex", ".."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if dir, _, ok := tc.store.Open(tc.framework, tc.task); ok {
				t.Errorf("Open returned %q, want no store", dir)
			}
		})
	}
}

// What Open returns is a run's HOME, and the CLI runs in the sandbox, not
// in the daemon's directory: a relative store root gave agy a HOME that
// resolved against the sandbox, so it found none of the config grain had
// written, its pre-tool hook included (a daemon started with `-data-dir
// d/data`, docs/test-runs/2026-10-15-corp-research-capability.md).
func TestOpenReturnsAnAbsoluteHomeForARelativeRoot(t *testing.T) {
	t.Chdir(t.TempDir())
	dir, release, ok := New(filepath.Join("data", "agent-sessions")).Open("antigravity", "7")
	if !ok {
		t.Fatal("Open refused")
	}
	defer release()
	if !filepath.IsAbs(dir) {
		t.Fatalf("Open = %q, want an absolute path", dir)
	}
	if _, _, ok := New("").Open("antigravity", "7"); ok {
		t.Error("an unconfigured store opened a home")
	}
}
