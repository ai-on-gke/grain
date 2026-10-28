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

package runhost

import (
	"fmt"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

// grain hostd's detached processes take part in the adoption pass as run
// hosts do (docs/design/cli-run-location.md, "Upgrades and host runs"):
// a host CLI started through hostd with Detach outlives the daemon until
// its own deadline, spools its output the way a run host does, and is
// listed by hostd with the run and task that claim it. DecideHostd maps
// that listing onto Info and hands it to Decide, so a live row matched to
// a hostd process is adopted, an unmatched process released, and an
// expired one finished, by the same table run hosts are.

// HostdPrefix starts the Info.Name of a hostd process, so the adoption
// pass can tell it from a container and release it through hostd.
const HostdPrefix = "hostd:"

// HostdID is the hostd process id an Info from HostdInfos names, and
// whether it is one.
func HostdID(i Info) (string, bool) {
	return strings.CutPrefix(i.Name, HostdPrefix)
}

// HostdInfos is procs as the adoption pass lists run hosts. Only detached
// processes are: a protocol-1 start dies with its connection, so there is
// nothing of it to adopt. Every one is labelled with this build's
// run-host protocol, since the hostd protocol is checked as a whole
// (DecideHostd), not per process.
func HostdInfos(procs []hostd.Process, peer hostd.Hello) []Info {
	var out []Info
	for _, p := range procs {
		if !p.Detached {
			continue
		}
		out = append(out, Info{
			Name: HostdPrefix + p.ID,
			Labels: Labels{
				Task:       p.Task,
				Run:        p.Run,
				Generation: peer.Build,
				Protocol:   Protocol,
				Deadline:   p.Deadline,
				Spool:      p.Spool,
			},
			Running: !p.Exited,
		})
	}
	return out
}

// DecideHostd is Decide for the processes a hostd that said peer lists.
// rows are the live rows driven through hostd, chosen as RecoverRunHosts
// chooses rows driven in run hosts.
//
// A hostd this build cannot talk to (hostd.Compatible) cannot be
// re-attached to: each row it holds a process for is finished with the
// mismatch as its reason, and each process is released, which a hostd one
// protocol version apart still understands. Within one version of skew
// the processes are decided as any run host is.
func DecideHostd(rows []Claim, procs []hostd.Process, local, peer hostd.Hello, now time.Time) Plan {
	infos := HostdInfos(procs, peer)
	if err := hostd.Compatible(local, peer); err != nil {
		var plan Plan
		byRun := map[string]Info{}
		for _, i := range infos {
			byRun[i.Labels.Run] = i
			plan.Release = append(plan.Release, Release{Host: i, Reason: err.Error()})
		}
		for _, row := range rows {
			if i, ok := byRun[row.Run]; ok {
				plan.Finish = append(plan.Finish, Ending{Claim: row, Reason: fmt.Sprintf(
					"its host process %s could not be re-attached: %v", i.Name, err)})
			}
		}
		return plan
	}
	exited := map[string]bool{}
	for _, p := range procs {
		exited[HostdPrefix+p.ID] = p.Exited
	}
	return Decide(rows, infos, func(i Info) SpoolState {
		return SpoolState{Exited: exited[i.Name]}
	}, now)
}
