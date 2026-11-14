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
	"fmt"
	"os"
	"strings"
	"time"
)

// DebugClockOffsetFileEnv names the file a grain built with the
// grain_debugclock tag reads, on every release-upgrade tick, for how far
// ahead of the real clock the release-triggered upgrade is to measure
// its settle window and minimum interval
// (orchestrator.Config.ReleaseUpgradeClockOffset): one Go duration, such
// as "7h". It is how a walkthrough on a real daemon steps past an hour's
// settle and a six-hour interval in minutes, which libfaketime cannot do
// for a Go binary (Go reads its clock through the vDSO, not libc).
//
// Every other build ignores it: DebugClockOffset is nil unless
// debug_clock_tagged.go is compiled in.
const DebugClockOffsetFileEnv = "GRAIN_DEBUG_CLOCK_OFFSET_FILE"

// DebugClockBuildTag is the build tag that compiles the debug clock in.
// No release build passes it (TestNoReleaseBuildPassesTheDebugClockTag).
const DebugClockBuildTag = "grain_debugclock"

// DebugClockOffset, when set, is orchestrator.Config.ReleaseUpgradeClockOffset.
// It is nil in every build a deployment runs; only a build tagged
// grain_debugclock (debug_clock_tagged.go) sets it, so no flag, setting
// or environment variable can turn it on in a normal `grain daemon`.
var DebugClockOffset func() time.Duration

// readClockOffset reads the offset a DebugClockOffsetFileEnv file holds.
// A missing or empty file is no offset. The offset only moves the clock
// forward, so a negative one is refused rather than letting the times
// the reconciler records run backwards.
func readClockOffset(path string) (time.Duration, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s: %s is negative; the debug clock only moves forward", path, s)
	}
	return d, nil
}

// debugClockFrom is DebugClockOffset reading path: the offset in it now,
// or the last one read while the file cannot be parsed. logf is told
// each time the offset changes and each time the file is unreadable.
func debugClockFrom(path string, logf func(string, ...any)) func() time.Duration {
	var last time.Duration
	return func() time.Duration {
		d, err := readClockOffset(path)
		if err != nil {
			logf("daemon: debug clock: %v; keeping %s", err, last)
			return last
		}
		if d != last {
			logf("daemon: debug clock: the release-triggered upgrade now reads the time %s ahead", d)
			last = d
		}
		return d
	}
}
