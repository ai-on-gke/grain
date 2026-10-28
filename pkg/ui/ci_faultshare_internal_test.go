package ui

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// A runs page is only reused for the shared fault list when it is the
// whole newest-first listing; one scoped to a reader's own visibility
// must not seed the cache every reader shares.
func TestCanShareFaultRunsRefusesAScopedOrNarrowedPage(t *testing.T) {
	whole := ci.RunFilter{Descending: true}
	if !canShareFaultRuns(whole, 0) {
		t.Fatal("the whole newest-first listing should be shareable")
	}
	scoped := whole
	scoped.Visible = func(string) bool { return true }
	if canShareFaultRuns(scoped, 0) {
		t.Fatal("a page bounded by a reader's visibility must not be shared")
	}
	narrowed := whole
	narrowed.Ref = "main"
	if canShareFaultRuns(narrowed, 0) {
		t.Fatal("a narrowed page must not be shared")
	}
	if canShareFaultRuns(ci.RunFilter{}, 0) {
		t.Fatal("an oldest-first page must not be shared")
	}
	if canShareFaultRuns(whole, ciFaultRunLimit-1) {
		t.Fatal("a page shorter than the fault run limit must not be shared")
	}
}
