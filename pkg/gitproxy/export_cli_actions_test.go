package gitproxy

import "time"

// SetCLIWaitIntervals shortens how often a blocked /cli/ci/wait polls and
// heartbeats, for the test's lifetime.
func SetCLIWaitIntervals(poll, heartbeat time.Duration) (restore func()) {
	oldPoll, oldBeat := cliWaitPoll, cliWaitHeartbeat
	cliWaitPoll, cliWaitHeartbeat = poll, heartbeat
	return func() { cliWaitPoll, cliWaitHeartbeat = oldPoll, oldBeat }
}
