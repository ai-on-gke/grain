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

//go:build grain_debugclock

package daemon

import (
	"log"
	"os"
	"sync"
	"time"
)

// A walkthrough builds grain with -tags grain_debugclock to step the
// release-triggered upgrade's clock forward rather than wait it out. See
// DebugClockOffsetFileEnv. The warning is logged on the first tick that
// reads the clock, so it is the daemon that says it, not every grain
// subprocess the daemon starts.
func init() {
	path := os.Getenv(DebugClockOffsetFileEnv)
	read := func() time.Duration { return 0 }
	if path != "" {
		read = debugClockFrom(path, log.Printf)
	}
	var warn sync.Once
	DebugClockOffset = func() time.Duration {
		warn.Do(func() {
			log.Printf("daemon: WARNING: built with -tags %s, a test-only build: the release-triggered upgrade "+
				"reads its clock shifted forward by the duration in %s=%q. Never run this build as a deployment.",
				DebugClockBuildTag, DebugClockOffsetFileEnv, path)
		})
		return read()
	}
}
