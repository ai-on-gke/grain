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

package cifault_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/cifault"
)

// captureLog points the standard logger at a buffer for the test, and
// back again after it. Not parallel: log's writer is the process's.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return &buf
}

func logLines(buf *bytes.Buffer) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// manyOpen is a store with n open changes in the fault's repository, the
// shape of a deployment with two hundred pull requests open.
func manyOpen(n int) *fakeStore {
	store := &fakeStore{}
	for i := 1; i <= n; i++ {
		store.open(fmt.Sprint(i), fmt.Sprintf("acme/widgets#%d", i), "change")
	}
	return store
}

// stuckFiles is a lister GitHub never answers in time.
type stuckFiles struct {
	calls   atomic.Int32
	release chan struct{}
}

func (f *stuckFiles) ListPullRequestFiles(string, string, int) ([]string, error) {
	f.calls.Add(1)
	<-f.release
	return nil, nil
}

// The daemon's log after v3.11.0: a read that outlived the pass's
// deadline was followed by a line for every other open change. A pass
// whose reads time out says so once and stops reading.
func TestFixesStopsAtTheDeadlineAndSaysSoOnce(t *testing.T) {
	buf := captureLog(t)
	files := &stuckFiles{release: make(chan struct{})}
	defer close(files.release)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if fixes := cifault.Fixes(ctx, manyOpen(200), files, mirrorFault()); fixes != nil {
		t.Fatalf("fixes = %+v", fixes)
	}
	if got := files.calls.Load(); got != 1 {
		t.Errorf("asked GitHub %d times after the deadline, want the one read it was waiting on", got)
	}
	lines := logLines(buf)
	if len(lines) != 1 || !strings.Contains(lines[0], "context deadline exceeded") {
		t.Fatalf("logged %q, want one line naming the deadline", lines)
	}
}

// A lister that times its own calls out answers with the deadline
// rather than blocking; that is the pass's deadline too.
func TestFixesStopsOnADeadlineTheListerReports(t *testing.T) {
	buf := captureLog(t)
	files := &fakeFiles{err: fmt.Errorf("listing files: %w", context.DeadlineExceeded)}

	cifault.Fixes(context.Background(), manyOpen(200), files, mirrorFault())
	if files.calls != 1 {
		t.Errorf("asked %d times after a deadline, want 1", files.calls)
	}
	if lines := logLines(buf); len(lines) != 1 {
		t.Fatalf("logged %q, want one line", lines)
	}
}

// Any other failure is that pull request's, so the rest are still asked
// -- but the pass still says it in one line, with the count and the
// first error.
func TestFixesLogsOtherFailuresOncePerPass(t *testing.T) {
	buf := captureLog(t)
	files := &fakeFiles{err: errors.New("502 Bad Gateway")}

	cifault.Fixes(context.Background(), manyOpen(5), files, mirrorFault())
	if files.calls != 5 {
		t.Errorf("asked %d of 5 changes", files.calls)
	}
	lines := logLines(buf)
	if len(lines) != 1 || !strings.Contains(lines[0], "5 open pull requests") ||
		!strings.Contains(lines[0], "acme/widgets#1") || !strings.Contains(lines[0], "502 Bad Gateway") {
		t.Fatalf("logged %q, want one line with the count and the first error", lines)
	}
}

// Every reader asks when the cached fault list expires; a cold pull
// request is one GitHub call however many of them are waiting on it.
func TestFileCacheSharesAReadInFlight(t *testing.T) {
	t.Parallel()
	files := &stuckFiles{release: make(chan struct{})}
	cache := cifault.NewFileCache(files)
	cache.Now = func() time.Time { return cacheStart }

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cache.ListPullRequestFiles("acme", "widgets", 26)
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for files.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(files.release)
	wg.Wait()
	if got := files.calls.Load(); got != 1 {
		t.Fatalf("20 concurrent askers cost %d GitHub calls", got)
	}
}

// A deadline is not GitHub's answer about the pull request, so it is not
// remembered as one.
func TestFileCacheDoesNotRememberADeadline(t *testing.T) {
	t.Parallel()
	files := &fakeFiles{err: context.DeadlineExceeded}
	cache := cifault.NewFileCache(files)
	cache.Now = func() time.Time { return cacheStart }

	cache.ListPullRequestFiles("acme", "widgets", 26)
	files.err = nil
	files.files = map[int][]string{26: {"go.mod"}}
	got, err := cache.ListPullRequestFiles("acme", "widgets", 26)
	if err != nil || len(got) != 1 || files.calls != 2 {
		t.Fatalf("files = %v, err = %v, calls = %d: want the deadline asked again", got, err, files.calls)
	}
}
