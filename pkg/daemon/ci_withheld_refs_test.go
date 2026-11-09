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

package daemon

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A job declaring one of the release server's methods on an event its
// rule does not admit is told so -- the grain ci: note and JobRun.Warning
// (#594) -- as a job declaring the retired provider was. The release
// methods are capability references, which the note skipped, so after
// migration 101 a push job declaring release#package-publish ran with no
// files and no word why (docs/test-runs/2026-11-04-capabilities.md, F6).
func TestEventGatedReleaseReferencesAreNotedWhenWithheld(t *testing.T) {
	publish := model.PackagePublishRef + "={}"
	signing := model.ImageSigningRef + "={}"
	for _, tc := range []struct {
		name     string
		event    ci.Event
		trusted  bool
		withhold bool
		ids      []string
		want     []string // substrings, one note each; nil for none
	}{
		{name: "push", event: ci.EventPush, ids: []string{publish},
			want: []string{"capability " + publish + " is withheld on push runs (released only to release, workflow_dispatch, and grain's scheduled runs)"}},
		{name: "the bare alias on a pull request", event: ci.EventPullRequest, ids: []string{"package-publish"},
			want: []string{"capability package-publish is withheld on pull_request runs"}},
		{name: "image-signing on workflow_dispatch", event: ci.EventManual, ids: []string{signing},
			want: []string{"capability " + signing + " is withheld on workflow_dispatch runs (released only to release runs)"}},
		{name: "workflow_dispatch admits package-publish", event: ci.EventManual, ids: []string{publish}},
		{name: "grain's own schedule admits package-publish", event: ci.EventSchedule, trusted: true, ids: []string{publish}},
		{name: "a dispatch no person started", event: ci.EventManual, withhold: true, ids: []string{publish},
			want: []string{"withheld because this workflow_dispatch run was not started by a person"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notes := ciWithheldCapabilityNotes(nil, tc.event, tc.trusted, tc.withhold, false, tc.ids)
			if len(notes) != len(tc.want) {
				t.Fatalf("notes = %q, want %d", notes, len(tc.want))
			}
			for i, want := range tc.want {
				if !strings.Contains(notes[i], want) {
					t.Errorf("note %d = %q, want it to contain %q", i, notes[i], want)
				}
			}
		})
	}
	// Grants the repository's CI capabilities list supplied, which the job
	// did not declare, stay unannounced, as the providers' did.
	if notes := ciWithheldCapabilityNotes(nil, ci.EventPush, false, false, true, []string{publish}); len(notes) != 0 {
		t.Errorf("implicit capabilities noted: %q", notes)
	}
}
