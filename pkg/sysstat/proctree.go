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

package sysstat

import "time"

// ProcessTreeCPU reports how much CPU time the processes working in each
// of roots' directories have used so far, keyed the same way roots is.
//
// It is what gives a host-directory sandbox (orchestrator.HostSandboxes)
// a CPU reading of its own. That backend has no VM or cgroup to read, so
// the run's share of the host is told apart by where its processes
// work: a process belongs to a root when its working directory is that
// directory or anything under it, and so does every descendant of such a
// process wherever it has since cd'd to -- the agent's own harness
// starts in the run's directory, and the shells, compilers and test
// binaries it runs are its children.
//
// The figure is cumulative (user + system time, including children the
// tree has already reaped), so a rate needs two readings: the caller
// keeps the previous one. A root with no process found is absent from
// the result rather than reported as zero, since "nothing is running
// there" and "something used no CPU" are different readings.
//
// Only /proc implements it; elsewhere it returns errors.ErrUnsupported
// (or the /proc open error on a platform with neither), which callers
// treat as "no reading".
func ProcessTreeCPU(roots map[string]string) (map[string]time.Duration, error) {
	return processTreeCPU(roots)
}
