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

package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clock is a settable time for a recorder.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestGapRecorderMeasuresEachListenerAcrossARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "restart-gap.json")
	c := &clock{t: time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC)}

	// The first process ever has nothing to measure from.
	first := OpenGapRecorder(path, "container", c.now)
	first.Up(ServiceAPI)
	if _, ok := first.Last().Services[0].Gap(); ok {
		t.Error("a first start reported a gap")
	}

	// It is told to stop, then closes the API; it is killed before it
	// closes the git proxy.
	c.t = c.t.Add(time.Hour)
	first.ShuttingDown()
	c.t = c.t.Add(20 * time.Second)
	first.Down(ServiceAPI)

	c.t = c.t.Add(3 * time.Second)
	second := OpenGapRecorder(path, "container", c.now)
	c.t = c.t.Add(1 * time.Second)
	second.Up(ServiceAPI)
	c.t = c.t.Add(1 * time.Second)
	second.Up(ServiceGitProxy)

	gap, ok := ReadRestartGap(path)
	if !ok {
		t.Fatal("no measurement on disk")
	}
	if gap.Mode != "container" || len(gap.Services) != 2 {
		t.Fatalf("measurement = %+v", gap)
	}
	if d, ok := gap.Services[0].Gap(); !ok || d != 4*time.Second || gap.Services[0].FromShutdown {
		t.Errorf("API gap = %s, %v (from shutdown %v), want 4s measured from its own close", d, ok, gap.Services[0].FromShutdown)
	}
	if d, ok := gap.Services[1].Gap(); !ok || d != 25*time.Second || !gap.Services[1].FromShutdown {
		t.Errorf("git proxy gap = %s, %v (from shutdown %v), want 25s measured from the shutdown", d, ok, gap.Services[1].FromShutdown)
	}
	summary := gap.Summary()
	for _, want := range []string{"container", "API 4s", "git proxy at most 25s"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q does not say %q", summary, want)
		}
	}

	// The second process's down times are its own: a third start after
	// it died with no record measures nothing rather than reusing the
	// first's.
	third := OpenGapRecorder(path, "container", c.now)
	third.Up(ServiceAPI)
	if _, ok := third.Last().Services[0].Gap(); ok {
		t.Error("a start after a process that left no record reported a gap")
	}
}

func TestGapRecorderSurvivesAnUnreadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart-gap.json")
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := OpenGapRecorder(path, "host binary", nil)
	r.Up(ServiceAPI)
	if gap, ok := ReadRestartGap(path); !ok || len(gap.Services) != 1 {
		t.Errorf("after an unreadable file: %+v, %v", gap, ok)
	}
}

func TestNilGapRecorderRecordsNothing(t *testing.T) {
	var r *GapRecorder
	r.Up(ServiceAPI)
	r.Down(ServiceAPI)
	r.ShuttingDown()
	if got := r.Last(); len(got.Services) != 0 {
		t.Errorf("nil recorder Last = %+v", got)
	}
}

func TestDeploymentModeName(t *testing.T) {
	for m, want := range map[Mode]string{ModeSource: "host binary", ModeImage: "container", ModeKube: "kubernetes", "": "host binary"} {
		if got := DeploymentModeName(m); got != want {
			t.Errorf("DeploymentModeName(%q) = %q, want %q", m, got, want)
		}
	}
}

// A process refused before any listener came up -- and told to stop, as
// a refused daemon is on its way out -- leaves the previous process's
// file byte for byte, and the next start still measures from it.
func TestGapRecorderThatNeverServedLeavesTheFileAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "restart-gap.json")
	c := &clock{t: time.Date(2026, 10, 19, 3, 0, 0, 0, time.UTC)}
	served := OpenGapRecorder(path, "host binary", c.now)
	served.Up(ServiceAPI)
	c.t = c.t.Add(time.Hour)
	served.Down(ServiceAPI)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	c.t = c.t.Add(time.Minute)
	refused := OpenGapRecorder(path, "host binary", c.now)
	refused.ShuttingDown()
	refused.Down(ServiceGitProxy)
	if after, err := os.ReadFile(path); err != nil || string(after) != string(before) {
		t.Fatalf("a start that never served rewrote %s:\nbefore %s\nafter  %s (%v)", path, before, after, err)
	}

	c.t = c.t.Add(time.Minute)
	next := OpenGapRecorder(path, "host binary", c.now)
	next.Up(ServiceAPI)
	if d, ok := next.Last().Services[0].Gap(); !ok || d != 2*time.Minute {
		t.Errorf("gap after a refused start = %s, %v, want 2m measured from the process that last served", d, ok)
	}
}

// A first start that is refused writes no file at all.
func TestGapRecorderThatNeverServedWritesNoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "restart-gap.json")
	r := OpenGapRecorder(path, "host binary", nil)
	r.ShuttingDown()
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("a start that never served created %s: %v", filepath.Dir(path), err)
	}
}
