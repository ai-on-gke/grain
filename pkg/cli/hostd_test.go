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

package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

func TestHostdStatusNamesEachDetachedRun(t *testing.T) {
	now := time.Date(2026, 10, 23, 7, 0, 0, 0, time.UTC)
	var out strings.Builder
	writeHostdProcesses(&out, []hostd.Process{
		{ID: "p1", Run: "9-1", Task: "9", Kind: hostd.KindCLI, Name: "agy", PID: 17616, Started: now.Add(-90 * time.Second), Detached: true},
		{ID: "p2", Kind: hostd.KindCapability, Name: "fakecap", PID: 16551, Started: now.Add(-time.Hour), Attached: true},
		{ID: "p3", Run: "8-1", Task: "8", Kind: hostd.KindCLI, Name: "agy", PID: 1, Detached: true, Exited: true, Exit: &hostd.Exit{Code: 3}},
	}, now)
	want := `process p1: cli "agy", pid 17616, run 9-1 of task 9, running for 1m30s, detached, no daemon attached
process p2: capability "fakecap", pid 16551, running for 1h0m0s
process p3: cli "agy", pid 1, run 8-1 of task 8, ended (code 3), detached, no daemon attached
`
	if got := out.String(); got != want {
		t.Fatalf("status:\n%s\nwant:\n%s", got, want)
	}

}
