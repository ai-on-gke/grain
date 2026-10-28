// daemon_restarts.go answers, from inside the daemon, the question
// nothing on this deployment could answer on 2026-09-18: how many times
// has this service started lately?
//
// It had started 52 times in eight minutes (grain/task-233). Every one
// of those starts failed on a flag the unit passed and the image did not
// define, so the deployment dispatched nothing for as long as it lasted,
// and nothing raised it anywhere: the UI was down with the daemon, the
// journal was the only record, and the journal was being filled with the
// crash's own output at ~200 lines a restart. It was found two hours
// later by a person reading it by hand.
//
// The reading is taken once, at startup, and served from memory
// afterwards (ui.DaemonHealth.Starts). Twice deliberate:
//
//   - It is a question about the recent *past*, and the only process
//     able to answer it is one that is currently running -- which,
//     during a loop, is a new one every few seconds. Each of those
//     processes takes the reading afresh, so whenever there is a UI up
//     to look at, the figure on it was taken by the process serving it.
//   - The pane polls every few seconds and journalctl is a subprocess
//     over a window that, in a crash loop, holds thousands of lines.
//     Per-request would make the pane cost most where the deployment can
//     least afford it.
//
// Two deployment shapes ask the question differently, and both are here
// (grain/task-367). A host running grain-daemon.service counts starts in
// the unit's journal; a pod running one replica of a Deployment has no
// journal at all and counts the kubelet's own restarts of its container
// instead. The numbers are not interchangeable -- see daemonShape and
// newPodDaemonHealth -- so each reading travels with the words for what
// it is a count of and for what an operator does about it on this shape.
package cli
