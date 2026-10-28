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

// Package sysstat reads this machine's own CPU-load and memory pressure
// straight out of the kernel, the same two numbers `uptime` and `free`
// report, plus a named filesystem's own usage
// (DiskUsage, one statfs call, the numbers `df` reports, and
// FilesystemID, which says when two such paths are really one disk)
// -- pkg/cli/daemon.go's own
// ui.Config.HostStats (bwsalmon/agents#536's "sandbox health" pane: a
// sandbox that looks stuck is often really the host it runs on being
// starved, and that is a question about the daemon's own machine, not
// about any one sandbox -- see orchestrator.SandboxHealth's own doc comment
// on why a sandbox's usage is reported separately from this).
//
// # Two kernels, one Snapshot
//
// Linux is what grain deploys onto, and /proc is the whole interface
// there: /proc/loadavg, /proc/meminfo and /proc/stat, three file reads
// and no subprocess (sysstat_procfs.go). darwin is what grain is
// *developed* on, and it has no /proc at all, so every reading here used
// to fail on a maintainer's own laptop. That cost two things, and the
// second is the one that made it worth fixing: the host-stats pane went
// blank, which is survivable and already has a shape for it, and
// cmd/grain's sandbox budget stopped bounding anything -- withHostCapacity
// reads CPUs and MemTotalMB from here and, on a failure, logs it and
// carries on with zeros, which leaves the budget with no host to hold a
// commitment against. It says so (budgetSentence prints "nothing here
// knows how big this host is"), so this was never silent; it did mean a
// maintainer running grain locally was exercising the no-budget path and
// never the admission control a deployment runs under.
//
// So darwin is a second implementation of the same Snapshot, from sysctl
// where sysctl has the figure and from `vm_stat`/`iostat` where only a
// Mach call would -- sysstat_darwin.go argues each of those two
// subprocesses on its own, because not needing one was this package's
// whole point on Linux. The two implementations are kept in separate
// files rather than behind runtime.GOOS checks inside shared functions:
// the figures are defined differently enough (MemAvailable is published
// on Linux and has to be *chosen* on darwin) that interleaving them would
// hide which definition a given number came from.
//
// Every other platform -- windows, the BSDs -- still builds against the
// /proc implementation and still fails on it by name, which is the honest
// answer for a kernel nobody here has taken a reading on. Only DiskUsage
// and FilesystemID are different in kind: statfs(2) and stat(2) are one
// implementation for Linux and darwin both (disk_unix.go), because
// nothing about them had to be chosen twice.
package sysstat

// Snapshot is one point-in-time reading Read returns.
//
// Disk usage is deliberately not among these: /proc has no equivalent to
// statfs, and there is no one filesystem "this machine" means -- the ones
// worth watching are whichever disks the caller's own state sits on (for
// grain, the daemon's -data-dir, its -sandbox-dir and docker's data root,
// which are one filesystem on a laptop and three on a deployment sized
// the way terraform/gcp is). DiskUsage takes a path and is asked
// separately, once per filesystem, so a machine with no readable one
// still gets the figures here (grain/task-41, grain/task-148).
type Snapshot struct {
	// LoadAverage1/5/15 are /proc/loadavg's own first three fields: the
	// number of runnable-or-uninterruptible processes, averaged over the
	// last 1/5/15 minutes. On darwin they are the same three figures from
	// `sysctl vm.loadavg`, which is where that kernel keeps them -- the
	// same numbers `uptime` prints on either platform.
	LoadAverage1, LoadAverage5, LoadAverage15 float64
	// CPUs is how many logical processors this machine has -- the
	// denominator the load averages above are only meaningful over (1.0
	// of load is an idle machine on 4 cores and a saturated one on a
	// single core), and what a sandbox's own vCPU count is committed
	// against when a deployment sizes itself (ui.HostCapacity,
	// grain/task-223).
	//
	// Counted from /proc/stat's own per-CPU lines rather than from
	// /proc/cpuinfo, whose format differs between architectures, or from
	// runtime.NumCPU, which reports this *process's* CPU affinity mask.
	// The machine's own count is the one wanted here: on a deployment
	// whose daemon runs in a container, the sandbox VMs are containers
	// beside it on the same host, and it is the host's cores they share
	// rather than whatever slice this process was given. darwin's
	// hw.logicalcpu is the same claim about the machine, for the same
	// reason it is not runtime.NumCPU there either.
	CPUs int
	// CPUUtilization is the fraction of aggregate CPU time spent doing
	// work during a short sample, from 0 (idle) to 1 (fully utilized).
	//
	// A rate, not a level, so there is no way to read one instant of it:
	// Read spends a sampling interval on this and nothing else (100ms on
	// Linux, one second on darwin, which is `iostat`'s own floor).
	CPUUtilization float64
	// MemTotalMB and MemUsedMB are /proc/meminfo's MemTotal and MemTotal
	// minus MemAvailable, converted from the kB /proc/meminfo itself
	// reports in. MemAvailable, not MemFree, is the kernel's own estimate
	// of memory a new process could actually get without swapping (man 5
	// proc) -- the same number `free`'s "available" column shows.
	//
	// darwin means the same subtraction over the same definition, but it
	// publishes no MemAvailable, so the estimate is assembled from the
	// page queues `vm_stat` reports: see readAvailableMemory in
	// sysstat_darwin.go, which names every queue it counts and every one
	// it deliberately leaves out. Read that before comparing a figure
	// from a mac against one from a deployment.
	MemTotalMB, MemUsedMB int
	// SwapTotalMB and SwapUsedMB are /proc/meminfo's SwapTotal and
	// SwapTotal minus SwapFree, in MiB. Both are zero when no swap is configured.
	//
	// darwin has no configured swap to speak of -- its swap files grow on
	// demand -- so SwapTotalMB there is how much backing store the kernel
	// has opened so far (`sysctl vm.swapusage`), which grows as a machine
	// comes under pressure rather than being a constant to read a
	// fraction against. SwapUsedMB is still the same subtraction.
	SwapTotalMB, SwapUsedMB int
}

// Read returns a fresh Snapshot of this machine's own load, memory and
// CPU utilization, from whichever interface the platform it was built for
// keeps them in -- /proc on Linux, sysctl plus two short-lived
// subprocesses on darwin, nothing anywhere else (see the package doc
// comment). A platform with no implementation returns the error from the
// /proc read it attempted, which is the same nil-means-unavailable
// contract every other optional pane already gives when its own
// precondition is missing.
//
// The failure is reported rather than absorbed into a zeroed Snapshot on
// purpose, and the whole call fails rather than the one field that could
// not be read. A caller handed CPUs=0 and MemTotalMB=0 cannot tell them
// from a machine nobody managed to measure, and the caller that most
// matters here is cmd/grain's withHostCapacity: a sandbox budget built on
// those zeros does not bound a smaller deployment, it stops bounding
// anything at all. Reporting nothing lets it say so (logSandboxBudget);
// reporting zeros would have it quietly admit every run.
//
// Read costs a sampling interval, all of it CPUUtilization's -- see that
// field. Callers sample on a cadence and share the result rather than
// calling it per request (cmd/grain's hostLoadInterval).
func Read() (Snapshot, error) {
	return read()
}
