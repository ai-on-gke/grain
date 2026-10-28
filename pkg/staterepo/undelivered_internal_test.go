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

package staterepo

// The bounds on the marker a notice nobody could deliver is kept in
// (resets.go's recordUndeliveredReset), from inside the package: what
// happens when a deployment nobody is reading goes on losing values.
//
// Both caps exist for the same deployment -- the one this whole surface
// is for, where nothing ever comes along to clear the last notice -- and
// both are checked here rather than through a merge, because a merge
// that resets three hundred values at once is a fixture the size of the
// thing being tested.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// aRepoForNotices is a local-only repository with a git directory to
// keep a marker in, which is all the marker needs.
func aRepoForNotices(t *testing.T) *Repo {
	t.Helper()
	repo, err := Open(context.Background(), Config{Dir: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	return repo
}

// resetsOf makes n distinct values, standing in for the merge that costs
// a deployment a whole upgrade's worth of columns at once.
func resetsOf(n int) []Reset {
	out := make([]Reset, n)
	for i := range out {
		out[i] = Reset{
			Path: filepath.FromSlash(DumpPath("template")), Column: "prompt",
			Row: "id = " + strconv.Itoa(i), From: "'something'", To: "NULL",
		}
	}
	return out
}

// One merge can lose several hundred values -- a settings file branched
// before an upgrade is missing every column that upgrade added, in every
// row of every settings table. The notice keeps as many as a person will
// read and counts the rest, which are in the journal in full: a panel of
// three hundred database values is one nobody reads at all, and a marker
// that grew without limit would be the deployment's own disk.
func TestAnUndeliveredNoticeKeepsAReadableNumberOfValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := aRepoForNotices(t)
	if err := repo.recordUndeliveredReset(ctx, "abc123", resetsOf(undeliveredValueLimit*3), errors.New("no")); err != nil {
		t.Fatalf("recording: %v", err)
	}
	kept, ok := repo.UndeliveredResets(ctx)
	if !ok || len(kept.Notices) != 1 {
		t.Fatalf("recorded nothing readable: %+v %v", kept, ok)
	}
	notice := kept.Notices[0]
	if len(notice.Values) != undeliveredValueLimit {
		t.Errorf("kept %d values, want the cap of %d", len(notice.Values), undeliveredValueLimit)
	}
	// The count is of everything that went, not of what fits: a reader
	// has to be able to tell "this is all of it" from "this is the start
	// of it", or the pane is quietly lying about what the merge cost.
	if notice.MoreValues != undeliveredValueLimit*2 {
		t.Errorf("said %d more, want the %d that did not fit", notice.MoreValues, undeliveredValueLimit*2)
	}
}

// And a deployment that goes on merging resets nobody reads keeps the
// most recent ones and says how many it let go. Dropping silently would
// be the same failure as overwriting, one cap further out.
func TestOlderUndeliveredNoticesAreDroppedAndCounted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := aRepoForNotices(t)
	const extra = 3
	for i := 0; i < undeliveredNoticeLimit+extra; i++ {
		err := fmt.Errorf("nobody was told about merge %d", i)
		if err := repo.recordUndeliveredReset(ctx, "commit-"+strconv.Itoa(i), resetsOf(1), err); err != nil {
			t.Fatalf("recording notice %d: %v", i, err)
		}
	}
	kept, ok := repo.UndeliveredResets(ctx)
	if !ok {
		t.Fatal("nothing was kept")
	}
	if len(kept.Notices) != undeliveredNoticeLimit {
		t.Fatalf("kept %d notices, want the cap of %d", len(kept.Notices), undeliveredNoticeLimit)
	}
	if kept.Dropped != extra {
		t.Errorf("said %d were dropped, want %d", kept.Dropped, extra)
	}
	if got := kept.Notices[len(kept.Notices)-1].Commit; got != "commit-"+strconv.Itoa(undeliveredNoticeLimit+extra-1) {
		t.Errorf("the newest notice kept is %s, want the last merge", got)
	}
	if got := kept.Notices[0].Commit; got != "commit-"+strconv.Itoa(extra) {
		t.Errorf("the oldest notice kept is %s, want the oldest one that still fits", got)
	}
}

// A marker written by a grain that is not this one -- a build with a
// different shape for it, or a file half-written by a host that lost
// power -- reports nothing rather than failing. What reporting loudly
// would cost is a State pane saying something is wrong with a deployment
// because a file about a notice cannot be read; what passing over costs
// is one notice, whose values are in the journal either way.
func TestAnUnreadableMarkerIsNotANotice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := aRepoForNotices(t)
	path, err := repo.gitDirFile(ctx, undeliveredResetsFile)
	if err != nil {
		t.Fatalf("finding the git directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("{ this is not JSON"), 0o600); err != nil {
		t.Fatalf("writing a broken marker: %v", err)
	}
	if kept, ok := repo.UndeliveredResets(ctx); ok {
		t.Errorf("a marker grain cannot read reported a notice: %+v", kept)
	}
	// And the next notice replaces it rather than being lost behind it:
	// a deployment must not stop being able to say what a merge took
	// away because of one bad file.
	if err := repo.recordUndeliveredReset(ctx, "abc123", resetsOf(1), errors.New("no")); err != nil {
		t.Fatalf("recording over a broken marker: %v", err)
	}
	if kept, ok := repo.UndeliveredResets(ctx); !ok || len(kept.Notices) != 1 {
		t.Errorf("the notice after a broken marker was lost: %+v %v", kept, ok)
	}
}
