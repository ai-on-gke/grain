package cli

import "time"

// daemonWaitLimit is the ceiling the tests in this package wait under for
// something the daemon does: a UI/API server binding and answering, a
// reconcile tick sweeping an orphaned sandbox or dispatching a task, a
// settings refresh returning, run() returning once its context is
// cancelled, a forked subcommand answering on its pipes.
//
// It is a hang-guard and not a measurement. Every one of those waits is
// waiting for a signal the code raises as it goes -- a row written, a
// file appearing, a channel closed -- so it either arrives as fast as
// this machine can carry it or it never arrives at all, and nothing here
// is asserting how quickly a machine opens an embedded SQLite store or
// binds a port. The numbers these waits used to carry read as budgets
// instead: one second, ten, fifteen, twenty, thirty, sixty, chosen one
// test at a time and none of them bounding anything on the host this
// suite actually runs on.
//
// That host is the point. The `go` job runs this module's whole suite
// race-instrumented in one sandbox, which went from four vCPU to eight on
// 2026-09-20 -- so `go test` runs eight packages at once and gives this
// one eight parallel lanes of its own, every one of them opening stores
// on the same disk, while pkg/ui, pkg/orchestrator, pkg/model/sqlite and
// pkg/staterepo do the same beside it. A daemon start on a box in that
// state is not quick, and ten seconds of it bounds nothing. pkg/ci,
// pkg/mcp, pkg/orchestrator and pkg/console each reached that conclusion
// about their own stopwatches (grain/task-426, grain/task-431); this is
// the same treatment for the package that was still reddening the job
// (grain/task-510).
//
// Sixty seconds rather than those packages' thirty because what is waited
// for here is bigger than a goroutine handoff: run() opening a store,
// laying out a git proxy, binding a UI server and getting through a
// reconcile tick. A ceiling this generous costs a genuinely wedged daemon
// nothing but a slower red -- the test still fails, and says what never
// happened -- and `go test -timeout 45m` is what stops a suite that has
// truly hung.
const daemonWaitLimit = 60 * time.Second
