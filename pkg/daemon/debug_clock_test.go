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

//go:build !grain_debugclock

package daemon

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A normal `grain daemon` -- this package built without the
// grain_debugclock tag, as every deployment's is -- has no debug clock,
// however the environment asks for one: DebugClockOffset stays nil, so
// the daemon's orchestrator.Config.ReleaseUpgradeClockOffset is nil and
// the release-triggered upgrade reads the real time.
func TestTheDebugClockCannotBeTurnedOnInANormalDaemon(t *testing.T) {
	file := filepath.Join(t.TempDir(), "offset")
	if err := os.WriteFile(file, []byte("7h\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(DebugClockOffsetFileEnv, file)
	t.Setenv("GRAIN_DEBUG_CLOCK_OFFSET", "7h")
	if DebugClockOffset != nil {
		t.Fatalf("DebugClockOffset is set in a build without -tags %s (offset %s); a deployment's daemon must read the real clock",
			DebugClockBuildTag, DebugClockOffset())
	}
}

// No build a deployment runs -- an image, a release script, a CI
// workflow, the Makefile -- compiles the debug clock in.
func TestNoReleaseBuildPassesTheDebugClockTag(t *testing.T) {
	root := filepath.Join("..", "..")
	var roots []string
	for _, glob := range []string{"Dockerfile*", "Granulefile*", "Makefile"} {
		m, err := filepath.Glob(filepath.Join(root, glob))
		if err != nil {
			t.Fatal(err)
		}
		roots = append(roots, m...)
	}
	for _, dir := range []string{"scripts", "deploy", filepath.Join(".github", "workflows"), filepath.Join(".grain", "templates")} {
		roots = append(roots, filepath.Join(root, dir))
	}
	scanned := 0
	for _, r := range roots {
		err := filepath.WalkDir(r, func(path string, d fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			scanned++
			if strings.Contains(string(b), DebugClockBuildTag) {
				return fmt.Errorf("%s names the %s build tag; that build is test-only and must never ship", path, DebugClockBuildTag)
			}
			return nil
		})
		if err != nil {
			t.Error(err)
		}
	}
	if scanned < 10 {
		t.Fatalf("scanned only %d build files from %s; is the repository root where this test thinks?", scanned, root)
	}
}

func TestReadClockOffset(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, content string
		want          time.Duration
		wantErr       bool
	}{
		{"hours", "7h\n", 7 * time.Hour, false},
		{"compound", " 1h5m ", time.Hour + 5*time.Minute, false},
		{"empty", "", 0, false},
		{"zero", "0", 0, false},
		{"negative", "-1h", 0, true},
		{"not a duration", "seven hours", 0, true},
	} {
		path := filepath.Join(dir, tc.name)
		if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := readClockOffset(path)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("%s: readClockOffset(%q) = %s, %v; want %s, error %v", tc.name, tc.content, got, err, tc.want, tc.wantErr)
		}
	}
	if got, err := readClockOffset(filepath.Join(dir, "missing")); got != 0 || err != nil {
		t.Errorf("a missing file = %s, %v; want no offset", got, err)
	}
}

// The tagged build's offset follows the file as a walkthrough rewrites
// it, and keeps the last good offset through a bad write.
func TestDebugClockFollowsItsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offset")
	var logged []string
	clock := debugClockFrom(path, func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) })
	if got := clock(); got != 0 {
		t.Fatalf("with no file the offset is %s, want 0", got)
	}
	for _, step := range []struct {
		content string
		want    time.Duration
	}{
		{"1h", time.Hour},
		{"7h", 7 * time.Hour},
		{"oops", 7 * time.Hour},
		{"-2h", 7 * time.Hour},
		{"14h", 14 * time.Hour},
	} {
		if err := os.WriteFile(path, []byte(step.content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := clock(); got != step.want {
			t.Fatalf("after writing %q the offset is %s, want %s", step.content, got, step.want)
		}
	}
	if len(logged) != 5 {
		t.Fatalf("logged %d lines, want one per change and per bad write (5): %q", len(logged), logged)
	}
}
