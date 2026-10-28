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

//go:build linux

package procgroup

import (
	"fmt"
	"os"
	"strconv"
)

// ChildOOMScoreAdj is the oom_score_adj written for sandbox and agent child
// processes spawned by a daemon or hostd running at -900. Linux's OOM killer
// scores processes from -1000 (never kill) to +1000 (always kill first), and
// fork/exec inherits the parent's oom_score_adj; resetting children to +500
// makes a runaway build or test inside a sandbox the OOM victim instead of
// the daemon or hostd.
const ChildOOMScoreAdj = 500

// SacrificeOnOOM sets pid's oom_score_adj to ChildOOMScoreAdj (+500) so it does
// not inherit a parent daemon's or hostd's -900 score. Best-effort: returns the
// underlying write error for callers that log or test it.
func SacrificeOnOOM(pid int) error {
	if pid <= 0 {
		return nil
	}
	path := fmt.Sprintf("/proc/%d/oom_score_adj", pid)
	return os.WriteFile(path, []byte(strconv.Itoa(ChildOOMScoreAdj)), 0o644)
}
