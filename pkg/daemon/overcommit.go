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

package daemon

import (
	"context"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/sysstat"
)

// overcommitSampler turns two readings of the host into one
// model.OvercommitSignals: PSI and the levels are read as they are, and
// the two /proc/vmstat counters (pages swapped in, OOM kills) become a
// rate and a count since the previous reading.
type overcommitSampler struct {
	have     bool
	at       time.Time
	swapIns  uint64
	oomKills uint64
}

// pageMB is one swapped-in page, in MB: /proc/vmstat counts pages, and
// every architecture grain deploys on swaps 4 KiB pages.
const pageMB = 4096.0 / (1 << 20)

func (s *overcommitSampler) signals(at time.Time, snap sysstat.Snapshot, p sysstat.Pressure, committed model.Commitment) model.OvercommitSignals {
	out := model.OvercommitSignals{
		At: at, CPUSome: p.CPUSome, MemorySome: p.MemorySome, MemoryFull: p.MemoryFull,
		IOSome: p.IOSome, IOFull: p.IOFull,
		SwapUsedMB: snap.SwapUsedMB, AvailableMemoryMB: snap.MemTotalMB - snap.MemUsedMB,
		Load1: snap.LoadAverage1, CPUs: snap.CPUs, Committed: committed,
	}
	if s.have {
		if secs := at.Sub(s.at).Seconds(); secs > 0 && p.SwapIns >= s.swapIns {
			out.SwapInMBps = float64(p.SwapIns-s.swapIns) * pageMB / secs
		}
		if p.OOMKills >= s.oomKills {
			out.OOMKills = int(p.OOMKills - s.oomKills)
		}
	}
	s.have, s.at, s.swapIns, s.oomKills = true, at, p.SwapIns, p.OOMKills
	return out
}

// runOvercommit is the auto overcommit controller's loop: every
// model.OvercommitInterval it applies the live settings to the controller
// and, in auto mode, reads the host and steps it, logging every change
// with the readings that caused it. In fixed mode it still keeps its
// counters current, so switching to auto does not read every OOM kill
// since boot as one that just happened.
//
// Only on a deployment whose sandboxes are built on this machine
// (sandboxHostCapacity): anywhere else this host's pressure says nothing
// about the sandboxes, and the budget has no host figure to scale.
func runOvercommit(ctx context.Context, live *LiveConfig, ctrl *model.OvercommitController) {
	ticker := time.NewTicker(model.OvercommitInterval)
	defer ticker.Stop()
	var sampler overcommitSampler
	logged := false
	for {
		cfg := live.Current()
		ctrl.Configure(cfg.SandboxOvercommit, cfg.CpuOvercommit, cfg.MemoryOvercommit)
		snap, err := sysstat.Read()
		var p sysstat.Pressure
		if err == nil {
			p, err = sysstat.ReadPressure()
		}
		switch {
		case err != nil:
			if !logged && cfg.SandboxOvercommit.Auto() {
				logged = true
				log.Printf("grain daemon: auto overcommit cannot read this host's pressure (%v), so its ratios stay where they are", err)
			}
		default:
			s := sampler.signals(time.Now(), snap, p, cfg.Ledger.Live())
			if cfg.SandboxOvercommit.Auto() && cfg.SandboxOvercommit.Validate() == nil {
				for _, a := range ctrl.Step(cfg.Budget(), s) {
					log.Printf("grain daemon: %s", a)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// overcommitState is ui.Config.Overcommit: the controller's state, on a
// deployment that runs one (runOvercommit's own condition), and nil
// everywhere else.
func overcommitState(live *LiveConfig, sandboxes orchestrator.Sandboxes) func() model.OvercommitState {
	if live == nil || !localHostSandboxes(sandboxes) {
		return nil
	}
	ctrl := live.Current().Overcommit
	if ctrl == nil {
		return nil
	}
	return ctrl.State
}

// overcommitNoteOOM is ctrl.NoteOOM, or nil where there is no controller:
// what orchestrator.Config.NoteOOM and ci.Engine.NoteOOM are handed, so
// every sandbox grain finds OOM-killed backs auto overcommit off.
func overcommitNoteOOM(ctrl *model.OvercommitController) func(time.Time) {
	if ctrl == nil {
		return nil
	}
	return ctrl.NoteOOM
}
