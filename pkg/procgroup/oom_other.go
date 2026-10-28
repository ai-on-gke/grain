//go:build !linux

package procgroup

// ChildOOMScoreAdj is the positive oom_score_adj used on Linux for child
// sandbox and agent processes.
const ChildOOMScoreAdj = 500

// SacrificeOnOOM is a no-op outside Linux.
func SacrificeOnOOM(pid int) error {
	return nil
}
