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
	"errors"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// A sync that keeps failing is reported, outside the journal, once it has
// been failing for syncFailureGrace -- with the time it started and the
// latest reason -- and stops being reported on the first clean cycle.
// grain-eu-host's settings went 36 hours without an export and the only
// trace was one journal line.
func TestStateSyncFailureIsReportedUntilASyncSucceeds(t *testing.T) {
	m := &StateManager{}
	m.noteOutcome(nil)
	if got := m.syncFailure(); got != nil {
		t.Fatalf("a working sync reported a failure: %+v", got)
	}
	m.noteOutcome(errors.New("first"))
	if got := m.syncFailure(); got != nil {
		t.Fatalf("a failure younger than the grace period was reported: %+v", got)
	}
	// Age the run of failures past the grace period.
	f := m.failure.Load()
	started := time.Now().Add(-2 * syncFailureGrace)
	f.Since = started
	m.noteOutcome(errors.New("ERROR: syntax error at or near \"$2\""))
	got := m.syncFailure()
	if got == nil {
		t.Fatal("a sync failing for longer than the grace period was not reported")
	}
	if !got.Since.Equal(started) {
		t.Errorf("since = %v, want the first failure's %v", got.Since, started)
	}
	if got.Error != "ERROR: syntax error at or near \"$2\"" {
		t.Errorf("error = %q, want the latest", got.Error)
	}
	if got.Repo == "" {
		t.Error("the report does not name the repository")
	}
	m.noteOutcome(nil)
	if got := m.syncFailure(); got != nil {
		t.Fatalf("a sync that succeeded is still reported failing: %+v", got)
	}
}

// A failing sync of an external config repository says so: grain only
// reads from one, so the alert has to speak of configuration not
// arriving rather than of settings not being exported -- which is what
// it said on 2026-10-30, with the UI's settings read-only all along.
func TestStateSyncFailureSaysWhenTheRepositoryIsExternal(t *testing.T) {
	internal := &StateManager{dataDir: t.TempDir()}
	internal.noteOutcome(errors.New("export failed"))
	if f := internal.failure.Load(); f == nil || f.External {
		t.Fatalf("an internal grain/config's failure = %+v, want one not marked external", f)
	}

	dataDir := t.TempDir()
	if err := staterepo.SaveSettings(dataDir, staterepo.Settings{Remote: "https://github.com/acme/grain-config.git", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	external := &StateManager{dataDir: dataDir}
	external.noteOutcome(errors.New("the state repository's remote could not be reached"))
	if f := external.failure.Load(); f == nil || !f.External {
		t.Fatalf("an external config repository's failure = %+v, want it marked external", f)
	}
}
