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

package orchestrator

// Internal, for the two things about setupNotes that are not visible from
// outside this package: the phrases themselves, and the bound they have
// to fit into. setupnotes_test.go is the rest -- what a dispatch actually
// puts on a task's row.

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The bound is ui.MaxActivityLength's, restated here rather than imported
// by the package itself (see maxSetupNote) -- so it is held to it here,
// where a test may import the daemon's own HTTP surface. A setup phrase
// never passes through ui.Client, which is what enforces that limit on
// an agent's own status, so nothing else would catch a phrase too long
// to render on the row it is written for.
func TestSetupNotesFitWhatTheTaskRowWillShow(t *testing.T) {
	t.Parallel()
	if maxSetupNote != ui.MaxActivityLength {
		t.Errorf("maxSetupNote = %d, want ui.MaxActivityLength (%d): the row is the same row",
			maxSetupNote, ui.MaxActivityLength)
	}
	for _, note := range []string{
		buildingSandboxNote, sandboxCredentialsNote, setupCommandNote, capabilityCredentialsNote,
		cloningNote(model.RepoRef{Owner: "acme", Name: "widgets"}),
	} {
		if note == "" {
			t.Error("a setup phrase is empty, which records nothing at all")
		}
		if n := utf8.RuneCountInString(note); n > ui.MaxActivityLength {
			t.Errorf("%q is %d characters, over the %d the row can show", note, n, ui.MaxActivityLength)
		}
		if strings.ContainsAny(note, "\n\r") {
			t.Errorf("%q spans more than one line; the row is one line", note)
		}
	}
}

// A repository nobody would name by hand still names itself in the
// phrase, so the bound is enforced on the way out rather than assumed of
// the inputs -- an over-long note is cut to fit instead of being written
// at a length the row cannot show.
func TestSetupNotesCutsAPhraseTooLongForTheRow(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	if err := store.PutTask(ctx, model.Task{ID: "t1", Title: "Do the thing"}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "t1", Sandbox: "r1", Attempt: 1, StartedAt: time.Now().UTC(),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	notes := setupNotes{store: store, taskID: "t1"}
	notes.say(ctx, cloningNote(model.RepoRef{
		Owner: strings.Repeat("o", 100), Name: strings.Repeat("n", 100),
	}))

	got, err := store.TaskActivityOf(ctx, "t1")
	if err != nil || got == nil {
		t.Fatalf("TaskActivityOf = (%+v, %v), want the phrase that was said", got, err)
	}
	if n := utf8.RuneCountInString(got.Note); n != maxSetupNote {
		t.Errorf("stored note is %d characters, want it cut to %d", n, maxSetupNote)
	}
	if !strings.HasPrefix(got.Note, "cloning "+strings.Repeat("o", 10)) {
		t.Errorf("note = %q, want the front of the phrase kept", got.Note)
	}
}

// The zero value narrates nothing and, above all, does not panic on a
// nil store -- that is what restoreCheckout passes, so that a sandbox
// rebuilt mid-run does not talk over the agent that asked for it.
func TestZeroSetupNotesSaysNothing(t *testing.T) {
	t.Parallel()
	_, ctx := recreateStore(t)
	var notes setupNotes
	notes.say(ctx, buildingSandboxNote)
	notes.handOver(ctx)
}
