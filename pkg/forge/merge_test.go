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

package forge

import (
	"testing"
	"time"
)

func refusalRef() Ref { return Ref{Owner: "acme", Repo: "refused", Number: 3} }

var refusalStart = time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)

// A run of refusals is one wait, counted and dated from its first: that
// pair is what lets a row say "refused 12 times since this morning"
// rather than saying "Merging" once a cycle forever.
func TestMergeRefusalsAreCountedFromTheFirst(t *testing.T) {
	ref := refusalRef()
	t.Cleanup(func() { ForgetMergeRefusals(ref) })

	first := RecordMergeRefusal(ref, "abc123", "protected branch", refusalStart)
	if first.Count != 1 || !first.First.Equal(refusalStart) {
		t.Fatalf("first refusal = %+v, want one refusal dated %v", first, refusalStart)
	}
	second := RecordMergeRefusal(ref, "abc123", "protected branch", refusalStart.Add(time.Minute))
	if second.Count != 2 {
		t.Fatalf("count = %d, want the refusals to add up", second.Count)
	}
	if !second.First.Equal(refusalStart) {
		t.Fatalf("first = %v, want the wait to be dated from %v", second.First, refusalStart)
	}

	on, ok := LastMergeRefusal(ref, "abc123")
	if !ok || on.Count != 2 {
		t.Fatalf("LastMergeRefusal = %+v, %v; want the run of two back", on, ok)
	}
	// A cycle asking about some other commit is asking about a different
	// pull request as far as merging goes, and gets nothing.
	if _, ok := LastMergeRefusal(ref, "def456"); ok {
		t.Fatal("a refusal of one commit was answered for another")
	}
}

// A push is a fresh story: the refusal on record was about a tree that is
// no longer there, so the count and the clock start again rather than
// carrying a previous commit's rejections into the new one's deadline.
func TestAPushStartsTheRefusalsAgain(t *testing.T) {
	ref := refusalRef()
	t.Cleanup(func() { ForgetMergeRefusals(ref) })

	RecordMergeRefusal(ref, "abc123", "protected branch", refusalStart)
	RecordMergeRefusal(ref, "abc123", "protected branch", refusalStart.Add(time.Minute))
	after := RecordMergeRefusal(ref, "newsha", "protected branch", refusalStart.Add(2*time.Minute))
	if after.Count != 1 || !after.First.Equal(refusalStart.Add(2*time.Minute)) {
		t.Fatalf("after a push = %+v, want a fresh run of one", after)
	}
}

// The deadline is what turns a wait into somebody's problem, and it is
// measured over one unbroken run: a merge that goes through (or a pull
// request that stops reading clean) forgets the record, so the refusals
// after it are timed from themselves.
func TestARunOfRefusalsStallsAtItsDeadline(t *testing.T) {
	ref := refusalRef()
	t.Cleanup(func() { ForgetMergeRefusals(ref) })
	restore := SetMergeRefusalDeadline(30 * time.Minute)
	t.Cleanup(restore)

	r := RecordMergeRefusal(ref, "abc123", "protected branch", refusalStart)
	if r.Stalled(refusalStart.Add(29 * time.Minute)) {
		t.Fatal("gave up inside the deadline: a refusal that resolves itself does so in a cycle or two")
	}
	if !r.Stalled(refusalStart.Add(31 * time.Minute)) {
		t.Fatal("waited past the deadline on a merge GitHub has refused for half an hour")
	}
	// A clock that jumped backwards reads as "not stalled yet", the
	// direction everything else timed in this package errs in.
	if r.Stalled(refusalStart.Add(-time.Hour)) {
		t.Fatal("a backwards clock made the queue give up")
	}

	ForgetMergeRefusals(ref)
	again := RecordMergeRefusal(ref, "abc123", "protected branch", refusalStart.Add(time.Hour))
	if again.Stalled(refusalStart.Add(time.Hour)) {
		t.Fatal("the new run inherited the old one's clock")
	}
}

// Zero is the setting that leaves the queue doing less on its own:
// refusals are still recorded and still said on the row, and nobody is
// ever asked to look at them.
func TestAZeroDeadlineNeverGivesUp(t *testing.T) {
	ref := refusalRef()
	t.Cleanup(func() { ForgetMergeRefusals(ref) })
	restore := SetMergeRefusalDeadline(0)
	t.Cleanup(restore)

	r := RecordMergeRefusal(ref, "abc123", "protected branch", refusalStart)
	if r.Count != 1 {
		t.Fatalf("count = %d, want the refusal recorded even with the deadline off", r.Count)
	}
	if r.Stalled(refusalStart.Add(100 * time.Hour)) {
		t.Fatal("gave up on a deployment that had switched giving up off")
	}
}
