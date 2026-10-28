package cli

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The 2026-10-07 proposer walkthrough: `grain create -approve` run by a
// proposer printed "state: proposed" and nothing else, so the flag
// looked silently dropped. It now says why the task waits.
func TestUnapprovedNote(t *testing.T) {
	t.Parallel()
	proposed := ui.Task{ID: "14", State: model.StateProposed}
	note := unapprovedNote(true, proposed)
	if !strings.Contains(note, "task 14 was filed as a proposal") || !strings.Contains(note, "waits for a human to approve it") {
		t.Errorf("unapprovedNote(-approve, proposed) = %q, want it to say task 14 waits for a human", note)
	}
	for name, tc := range map[string]struct {
		asked bool
		task  ui.Task
	}{
		"no -approve asked":       {false, proposed},
		"approved and queued":     {true, ui.Task{ID: "16", State: model.StateQueued, Approved: true}},
		"approved, still blocked": {true, ui.Task{ID: "17", State: model.StateProposed, Approved: true}},
	} {
		if note := unapprovedNote(tc.asked, tc.task); note != "" {
			t.Errorf("%s: unapprovedNote = %q, want nothing", name, note)
		}
	}
}
