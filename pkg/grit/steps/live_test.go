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

package steps

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/workflows"
)

// testClock is a clock a test moves by hand, read from whichever goroutine
// the engine happens to be on.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestLiveOutputPublishesOnlyOnTheInterval(t *testing.T) {
	clock := &testClock{now: time.Unix(0, 0)}
	var published []string
	live := NewLiveOutput(clock.Now, LiveOutputInterval, func(text string) {
		published = append(published, text)
	})

	live.Write("a")
	live.Write("b")
	if len(published) != 0 {
		t.Fatalf("nothing is published before the first interval: got %v", published)
	}
	clock.advance(LiveOutputInterval)
	live.Write("c")
	if len(published) != 1 || published[0] != "abc" {
		t.Fatalf("a publication carries everything printed so far: got %v", published)
	}
	live.Close()
	clock.advance(LiveOutputInterval)
	live.Write("d")
	if len(published) != 1 {
		t.Errorf("nothing is published after the step is over: got %v", published)
	}
}

func TestLiveOutputKeepsTheHeadAndTheNewestLines(t *testing.T) {
	clock := &testClock{now: time.Unix(0, 0)}
	var last string
	live := NewLiveOutput(clock.Now, LiveOutputInterval, func(text string) { last = text })

	head := strings.Repeat("H", liveHeadBytes)
	live.Write(head)
	// Enough past the tail bound to force a compaction, then a tail nobody
	// could mistake for anything else.
	live.Write(strings.Repeat("x", liveTailSlack+1024))
	live.Write("THE NEWEST LINE\n")
	clock.advance(LiveOutputInterval)
	live.Write("")
	live.Write("!")

	if last == "" {
		t.Fatalf("a publication was expected")
	}
	if !strings.HasPrefix(last, head) {
		t.Errorf("the head of a step's output is kept")
	}
	if !strings.HasSuffix(last, "THE NEWEST LINE\n!") {
		t.Errorf("the newest lines are kept: got the last 40 bytes as %q", last[max(0, len(last)-40):])
	}
	if !strings.Contains(last, "elided from the middle by grain") {
		t.Errorf("what was dropped is said, in the same words a finished step says it in")
	}
	if len(last) > workflows.MaxStepOutput+1024 {
		t.Errorf("live output stays bounded: got %d bytes", len(last))
	}
}
