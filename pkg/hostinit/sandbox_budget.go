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

package hostinit

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// sandboxDiskFloor is one entry of GET /api/settings' sandboxDiskFloors
// (ui.DiskUsage): a volume the daemon writes to and the free-space floor
// kept on it, which is also the volume reading model.Budget.DiskBudgetGB
// sizes the sandbox disk budget from on a local host.
type sandboxDiskFloor struct {
	Holds   []string `json:"holds"`
	Path    string   `json:"path"`
	UsedMB  int      `json:"usedMB"`
	TotalMB int      `json:"totalMB"`
	FloorMB int      `json:"floorMB"`
}

// sandboxBudgetSettings is the slice of GET /api/settings that
// CheckSandboxBudget reads: ui.SandboxCommitment, its Budget, and the
// host's disk volumes, trimmed to the fields that say whether one
// sandbox of the deployment's own shape fits inside the budget at all
// and whether the effective concurrency is out of whack with max_workers.
type sandboxBudgetSettings struct {
	MaxWorkers            int                `json:"maxWorkers"`
	SandboxDiskGB         int                `json:"sandboxDiskGb"`
	SandboxDiskGBDefault  int                `json:"sandboxDiskGbDefault"`
	SandboxDiskOvercommit float64            `json:"sandboxDiskOvercommit"`
	SandboxDiskFloors     []sandboxDiskFloor `json:"sandboxDiskFloors"`
	SandboxCommitment     *struct {
		Uncapped           bool `json:"uncapped"`
		Workers            int  `json:"workers"`
		Mergers            int  `json:"mergers"`
		CIJobs             int  `json:"ciJobs"`
		Runs               int  `json:"runs"`
		CPUsPerSandbox     int  `json:"cpusPerSandbox"`
		MemoryMBPerSandbox int  `json:"memoryMbPerSandbox"`
		DiskGBPerSandbox   int  `json:"diskGbPerSandbox"`
		HostCPUs           int  `json:"hostCpus"`
		HostMemoryMB       int  `json:"hostMemoryMb"`
		Budget             *struct {
			CPUOvercommit    float64 `json:"cpuOvercommit"`
			MemoryOvercommit float64 `json:"memoryOvercommit"`
			DiskOvercommit   float64 `json:"diskOvercommit"`
			ReserveMemoryMB  int     `json:"reserveMemoryMb"`
			CPUs             float64 `json:"cpus"`
			CPUsBounded      bool    `json:"cpusBounded"`
			MemoryMB         int     `json:"memoryMb"`
			MemoryBounded    bool    `json:"memoryBounded"`
			DiskGB           int     `json:"diskGb"`
			DiskBounded      bool    `json:"diskBounded"`
		} `json:"budget"`
	} `json:"sandboxCommitment"`
}

// minSuggestedReserveMB is the smallest memory reserve
// CheckSandboxBudget will suggest: what the 10-04 walkthrough's 7.8 GiB
// host was brought to dispatching with (docs/test-runs/2026-10-04-grain-init-platforms.md).
const minSuggestedReserveMB = 512

// defaultSandboxDiskGB is the default per-sandbox disk shape (granule.DefaultDiskGB)
// when GET /api/settings did not include one explicitly.
const defaultSandboxDiskGB = 30

// defaultMaxWorkers is model.DefaultMaxWorkers, used when disk volumes are
// reported without an explicit worker count in a partial settings payload.
const defaultMaxWorkers = 10

// SandboxBudgetVerdict is CheckSandboxBudget's answer.
type SandboxBudgetVerdict struct {
	// Checked is false when there is no budget to check at all: no
	// commitment, no budget, or host sandboxing. Detail says which.
	Checked bool
	// Fits is whether one sandbox of the deployment's shape fits inside
	// its budget. Meaningful only when Checked.
	Fits bool
	// UnderResourced is true when one sandbox fits, but the budget's
	// effective concurrency (once 1 sandbox is held back for the repair
	// reserve) is significantly out of whack with the configured worker
	// concurrency -- for example when a beefy host has its grain/docker
	// directories on a small boot partition that restricts it to running
	// 1 task at a time against max_workers=10.
	UnderResourced bool
	// ConfiguredWorkers is the deployment's configured max_workers when
	// UnderResourced is evaluated.
	ConfiguredWorkers int
	// MaxConcurrent is the maximum number of concurrent ordinary tasks
	// the sandbox budget can admit when UnderResourced is evaluated.
	MaxConcurrent int
	// Detail is one sentence on what was found, for a person.
	Detail string
	// Fix is the settings that would fit, keyed by their JSON names in
	// ui.UpdateSettingsRequest. Empty when it fits without warning, and
	// when TooSmall.
	Fix map[string]any
	// TooSmall is a host with too little memory left for even a 1 GiB
	// sandbox, which no setting here can fix.
	TooSmall bool
}

// settingsFlags is `grain settings`' flag for each of Fix's keys
// (pkg/cli/settingscmd.go).
var settingsFlags = map[string]string{
	"maxWorkers":             "-max-workers",
	"sandboxMemoryReserveMb": "-sandbox-memory-reserve-mb",
	"sandboxMemoryMb":        "-sandbox-memory-mb",
	"sandboxCpus":            "-sandbox-cpus",
	"sandboxCpuOvercommit":   "-sandbox-cpu-overcommit",
	"sandboxDiskGb":          "-sandbox-disk-gb",
	"sandboxDiskOvercommit":  "-sandbox-disk-overcommit",
}

// SettingsCommand is Fix as the `grain settings` command line that
// applies it, or "" when there is nothing to apply. Unlike a curl of the
// API, it presents the access code by itself.
func (v SandboxBudgetVerdict) SettingsCommand() string {
	if len(v.Fix) == 0 {
		return ""
	}
	keys := make([]string, 0, len(v.Fix))
	for k := range v.Fix {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := []string{"grain settings"}
	for _, k := range keys {
		flag, ok := settingsFlags[k]
		if !ok {
			return ""
		}
		var val string
		switch n := v.Fix[k].(type) {
		case int:
			val = strconv.Itoa(n)
		case float64:
			val = strconv.FormatFloat(n, 'f', -1, 64)
		default:
			val = fmt.Sprint(n)
		}
		parts = append(parts, flag, val)
	}
	return strings.Join(parts, " ")
}

// CheckSandboxBudget decides, from the body of GET /api/settings,
// whether one sandbox of the deployment's own shape fits inside its
// sandbox budget -- and, when it does not, which settings would fit.
// A budget that cannot hold one is a deployment where no run can ever
// be admitted: every approved task stays queued, because
// model.Budget.Admits refuses the first sandbox.
//
// It is grain init's check (checkSandboxBudget) and `grain sandbox-budget`'s,
// which scripts/setup.sh's readiness report runs.
func CheckSandboxBudget(settingsJSON []byte) (SandboxBudgetVerdict, error) {
	var s sandboxBudgetSettings
	if err := json.Unmarshal(settingsJSON, &s); err != nil {
		return SandboxBudgetVerdict{}, err
	}
	c := s.SandboxCommitment
	switch {
	case c == nil:
		// GET /api/settings answers the same for every caller; it omits
		// the commitment while the deployment's own max_workers is 0.
		return SandboxBudgetVerdict{Detail: "the daemon reports no sandbox commitment -- none while the " +
			"deployment has no worker slots, and none at all where it cannot count its host's cores and memory (a Kubernetes " +
			"cluster, or a daemon not on Linux), so there is no budget to check"}, nil
	case c.Budget == nil:
		return SandboxBudgetVerdict{Detail: "the daemon reports no sandbox budget on this host -- nothing bounds a run"}, nil
	case c.Uncapped:
		return SandboxBudgetVerdict{Detail: "host sandboxing -- runs are not shaped, so there is no budget to fit"}, nil
	}
	b := c.Budget

	var problems []string
	v := SandboxBudgetVerdict{Checked: true, Fix: map[string]any{}}
	if b.MemoryBounded && c.MemoryMBPerSandbox > 0 && b.MemoryMB < c.MemoryMBPerSandbox {
		problems = append(problems, fmt.Sprintf("%d MiB of sandbox memory (%d MiB host less %d MiB reserved, at %.2fx) cannot hold one %d MiB sandbox",
			b.MemoryMB, c.HostMemoryMB, b.ReserveMemoryMB, b.MemoryOvercommit, c.MemoryMBPerSandbox))
		reserve, shape := fittingMemory(c.HostMemoryMB, b.ReserveMemoryMB, b.MemoryOvercommit, c.MemoryMBPerSandbox)
		if reserve != b.ReserveMemoryMB {
			v.Fix["sandboxMemoryReserveMb"] = reserve
		}
		switch {
		case shape == 0:
			v.TooSmall = true
		case shape != c.MemoryMBPerSandbox:
			v.Fix["sandboxMemoryMb"] = shape
		}
	}
	if b.CPUsBounded && c.CPUsPerSandbox > 0 && b.CPUs < float64(c.CPUsPerSandbox) {
		problems = append(problems, fmt.Sprintf("%.1f vCPU of sandbox cpu (%d host vCPU less the controller's reserve, at %.2fx) cannot hold one %d vCPU sandbox",
			b.CPUs, c.HostCPUs, b.CPUOvercommit, c.CPUsPerSandbox))
		if cpus := int(b.CPUs); cpus >= 1 {
			v.Fix["sandboxCpus"] = cpus
		} else {
			// The cpu reserve is not a setting, so a host it eats whole
			// can only be unbounded in cpu (model.Budget.CPUBudget).
			v.Fix["sandboxCpuOvercommit"] = 0
		}
	}
	if len(problems) == 0 {
		v.Fits = true
		v.Fix = nil
		v.Detail = fmt.Sprintf("one %d MiB sandbox fits", c.MemoryMBPerSandbox)
		if b.MemoryBounded {
			v.Detail += fmt.Sprintf(" in %d MiB of sandbox memory", b.MemoryMB)
		}
		checkConcurrencyFit(&v, s)
		return v, nil
	}

	v.Detail = "no run can ever be admitted, so every approved task would stay queued: " + strings.Join(problems, "; ") +
		". Lower the reserve or the sandbox shape in Settings → Sandbox"
	if v.TooSmall {
		v.Detail += " -- though this host has too little memory left for even a 1 GiB sandbox"
		v.Fix = nil
	}
	return v, nil
}

// ordinaryConcurrency is how many concurrent ordinary runs a dimension with
// total budget and per-sandbox cost perSandbox can admit at once:
// model.Budget.Admits holds back 1 agent sandbox for repair tasks
// (model.DefaultRepairReserveSandboxes) and admits the first ordinary run via
// the sole-run rule (Sole: true) even when the budget left after the repair
// reserve is smaller than one sandbox.
func ordinaryConcurrency(budget, perSandbox int) int {
	if perSandbox <= 0 {
		return 0
	}
	kept := min(perSandbox, max(budget, 0))
	return max(1, max(budget-kept, 0)/perSandbox)
}

// constrainingDiskFloor picks the volume from floors that bounds the local
// sandbox disk budget: matching daemon.DiskWatch.readVolumes, the readable
// volume (TotalMB > 0) with the least usable room over its floor.
func constrainingDiskFloor(floors []sandboxDiskFloor) (sandboxDiskFloor, bool) {
	var chosen sandboxDiskFloor
	var chosenUsable, chosenFreeLeft int
	found := false
	for _, f := range floors {
		if f.TotalMB <= 0 {
			continue
		}
		usable := max(f.TotalMB-f.FloorMB, 0)
		freeLeft := max(f.TotalMB-f.UsedMB, 0) - f.FloorMB
		if !found || usable < chosenUsable || (usable == chosenUsable && freeLeft < chosenFreeLeft) {
			chosen = f
			chosenUsable = usable
			chosenFreeLeft = freeLeft
			found = true
		}
	}
	return chosen, found
}

// formatTaskLimit renders a concurrency ceiling in prose ("1 task at a time"
// or "N concurrent tasks").
func formatTaskLimit(n int) string {
	if n <= 1 {
		return "1 task at a time"
	}
	return fmt.Sprintf("%d concurrent tasks", n)
}

// checkConcurrencyFit checks whether a budget that fits at least one sandbox
// still restricts concurrent runs far below the configured worker concurrency
// (for example, when docker and grain files sit on a small boot partition that
// caps a 10-worker machine at 1 task at a time).
func checkConcurrencyFit(v *SandboxBudgetVerdict, s sandboxBudgetSettings) {
	c := s.SandboxCommitment
	b := c.Budget

	workers := c.Workers
	if workers <= 0 {
		workers = s.MaxWorkers
	}
	if workers <= 0 && (len(s.SandboxDiskFloors) > 0 || b.DiskBounded) {
		workers = defaultMaxWorkers
	}
	if workers <= 1 {
		return
	}

	diskPerSandbox := c.DiskGBPerSandbox
	if diskPerSandbox <= 0 {
		diskPerSandbox = s.SandboxDiskGB
	}
	if diskPerSandbox <= 0 {
		diskPerSandbox = s.SandboxDiskGBDefault
	}
	if diskPerSandbox <= 0 {
		diskPerSandbox = defaultSandboxDiskGB
	}
	diskOvercommit := b.DiskOvercommit
	if diskOvercommit <= 0 {
		diskOvercommit = s.SandboxDiskOvercommit
	}

	maxConcurrent := workers
	var bottlenecks []string
	diskBottleneck := false

	if vol, ok := constrainingDiskFloor(s.SandboxDiskFloors); ok && diskOvercommit > 0 {
		usableMB := max(vol.TotalMB-vol.FloorMB, 0)
		diskBudgetGB := int(float64(usableMB) / 1024 * diskOvercommit)
		diskSlots := ordinaryConcurrency(diskBudgetGB, diskPerSandbox)
		if diskSlots < maxConcurrent {
			maxConcurrent = diskSlots
		}
		if diskSlots <= 1 || diskSlots*2 < workers {
			diskBottleneck = true
			holds := "grain state"
			if len(vol.Holds) > 0 {
				holds = strings.Join(vol.Holds, ", ")
			}
			loc := vol.Path
			if loc == "" {
				loc = "the sandbox volume"
			}
			bottlenecks = append(bottlenecks, fmt.Sprintf(
				"%s (%.1f GiB holding %s, %.1f GiB floor, %d GiB sandbox disk budget at %.2fx) can only admit %s of %d GiB once 1 is held back for repair",
				loc, float64(vol.TotalMB)/1024, holds, float64(vol.FloorMB)/1024, diskBudgetGB, diskOvercommit,
				formatTaskLimit(diskSlots), diskPerSandbox))
		}
	} else if b.DiskBounded && diskPerSandbox > 0 {
		diskSlots := ordinaryConcurrency(b.DiskGB, diskPerSandbox)
		if diskSlots < maxConcurrent {
			maxConcurrent = diskSlots
		}
		if diskSlots <= 1 || diskSlots*2 < workers {
			diskBottleneck = true
			bottlenecks = append(bottlenecks, fmt.Sprintf(
				"%d GiB of sandbox disk (at %.2fx) can only admit %s of %d GiB once 1 is held back for repair",
				b.DiskGB, diskOvercommit, formatTaskLimit(diskSlots), diskPerSandbox))
		}
	}

	if b.MemoryBounded && c.MemoryMBPerSandbox > 0 {
		memSlots := ordinaryConcurrency(b.MemoryMB, c.MemoryMBPerSandbox)
		if memSlots < maxConcurrent {
			maxConcurrent = memSlots
		}
		if memSlots <= 1 || memSlots*2 < workers {
			bottlenecks = append(bottlenecks, fmt.Sprintf(
				"%d MiB of sandbox memory (%d MiB host less %d MiB reserved, at %.2fx) can only admit %s of %d MiB once 1 is held back for repair",
				b.MemoryMB, c.HostMemoryMB, b.ReserveMemoryMB, b.MemoryOvercommit,
				formatTaskLimit(memSlots), c.MemoryMBPerSandbox))
		}
	}

	if b.CPUsBounded && c.CPUsPerSandbox > 0 {
		cpuSlots := ordinaryConcurrency(int(b.CPUs), c.CPUsPerSandbox)
		if cpuSlots < maxConcurrent {
			maxConcurrent = cpuSlots
		}
		if cpuSlots <= 1 || cpuSlots*2 < workers {
			bottlenecks = append(bottlenecks, fmt.Sprintf(
				"%.1f vCPU of sandbox cpu (%d host vCPU less the controller's reserve, at %.2fx) can only admit %s of %d vCPU once 1 is held back for repair",
				b.CPUs, c.HostCPUs, b.CPUOvercommit,
				formatTaskLimit(cpuSlots), c.CPUsPerSandbox))
		}
	}

	v.ConfiguredWorkers = workers
	v.MaxConcurrent = maxConcurrent
	if len(bottlenecks) == 0 {
		return
	}

	v.UnderResourced = true
	v.Fix = map[string]any{"maxWorkers": maxConcurrent}
	remedy := "Lower max_workers or the sandbox shape in Settings → Sandbox"
	if diskBottleneck {
		remedy = "Move the grain data directory (GRAIN_DATA_DIR / -data-dir) and Docker's data-root onto a larger volume, or lower max_workers or the sandbox disk shape in Settings → Sandbox"
	}
	v.Detail += fmt.Sprintf(
		", but this host is configured for %d workers (max_workers) while its sandbox budget restricts it to %s: %s. %s",
		workers, formatTaskLimit(maxConcurrent), strings.Join(bottlenecks, "; "), remedy)
}
