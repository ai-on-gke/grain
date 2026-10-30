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

// sandboxBudgetSettings is the slice of GET /api/settings that
// CheckSandboxBudget reads: ui.SandboxCommitment and its Budget, trimmed
// to the fields that say whether one sandbox of the deployment's own
// shape fits inside the budget at all.
type sandboxBudgetSettings struct {
	SandboxCommitment *struct {
		Uncapped           bool `json:"uncapped"`
		CPUsPerSandbox     int  `json:"cpusPerSandbox"`
		MemoryMBPerSandbox int  `json:"memoryMbPerSandbox"`
		HostCPUs           int  `json:"hostCpus"`
		HostMemoryMB       int  `json:"hostMemoryMb"`
		Budget             *struct {
			CPUOvercommit    float64 `json:"cpuOvercommit"`
			MemoryOvercommit float64 `json:"memoryOvercommit"`
			ReserveMemoryMB  int     `json:"reserveMemoryMb"`
			CPUs             float64 `json:"cpus"`
			CPUsBounded      bool    `json:"cpusBounded"`
			MemoryMB         int     `json:"memoryMb"`
			MemoryBounded    bool    `json:"memoryBounded"`
		} `json:"budget"`
	} `json:"sandboxCommitment"`
}

// minSuggestedReserveMB is the smallest memory reserve
// CheckSandboxBudget will suggest: what the 10-04 walkthrough's 7.8 GiB
// host was brought to dispatching with (docs/test-runs/2026-10-04-grain-init-platforms.md).
const minSuggestedReserveMB = 512

// SandboxBudgetVerdict is CheckSandboxBudget's answer.
type SandboxBudgetVerdict struct {
	// Checked is false when there is no budget to check at all: no
	// commitment, no budget, or host sandboxing. Detail says which.
	Checked bool
	// Fits is whether one sandbox of the deployment's shape fits inside
	// its budget. Meaningful only when Checked.
	Fits bool
	// Detail is one sentence on what was found, for a person.
	Detail string
	// Fix is the settings that would fit, keyed by their JSON names in
	// ui.UpdateSettingsRequest. Empty when it fits, and when TooSmall.
	Fix map[string]any
	// TooSmall is a host with too little memory left for even a 1 GiB
	// sandbox, which no setting here can fix.
	TooSmall bool
}

// settingsFlags is `grain settings`' flag for each of Fix's keys
// (pkg/cli/settingscmd.go).
var settingsFlags = map[string]string{
	"sandboxMemoryReserveMb": "-sandbox-memory-reserve-mb",
	"sandboxMemoryMb":        "-sandbox-memory-mb",
	"sandboxCpus":            "-sandbox-cpus",
	"sandboxCpuOvercommit":   "-sandbox-cpu-overcommit",
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
