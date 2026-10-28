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
