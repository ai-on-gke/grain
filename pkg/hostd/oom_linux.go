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

package hostd

import (
	"os"
	"strconv"
)

// ChildOOMScoreAdj is the oom_score_adj every process hostd starts is
// given. grain-hostd.service runs with OOMScoreAdjust=-900 so the kernel
// never picks hostd under memory pressure, and a child inherits that;
// the CLIs and capability servers hostd starts are the work, not the
// controller, so they are put back to the kill-me-first side of zero
// (docs/host-scripts.md, auto overcommit). Raising one's own score needs
// no privilege, so this works as the unprivileged account hostd runs as.
const ChildOOMScoreAdj = 500

// sacrificeOnOOM sets pid's oom_score_adj to ChildOOMScoreAdj. Best
// effort: a process that has already exited, or a kernel without the
// file, leaves nothing to protect.
func sacrificeOnOOM(pid int) {
	_ = os.WriteFile("/proc/"+strconv.Itoa(pid)+"/oom_score_adj", []byte(strconv.Itoa(ChildOOMScoreAdj)), 0)
}
