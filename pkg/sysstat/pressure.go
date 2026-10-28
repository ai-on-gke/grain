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

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Pressure is what the auto overcommit controller (model.OvercommitController)
// reads off the host: the kernel's pressure stall information for cpu,
// memory and io, and the two /proc/vmstat counters that say memory has
// already run out -- pages swapped back in, and processes OOM-killed.
//
// PSI is the reading the load average and MemAvailable are not: the share
// of recent wall time in which tasks were actually stalled waiting for a
// resource (Documentation/accounting/psi.rst). A host can be at load 180
// on 188 cores with nothing stalled, or have half its memory "available"
// while every guest is in reclaim; PSI tells those apart.
//
// Linux only. Everywhere else ReadPressure fails at the first open, the
// same nil-means-unavailable contract Read has.
type Pressure struct {
	// CPUSome, MemorySome, MemoryFull, IOSome and IOFull are each file's
	// avg10: the percentage of the last ten seconds in which some (or,
	// for full, all non-idle) tasks were stalled on that resource. A
	// kernel without CONFIG_PSI has no /proc/pressure, and Known is false.
	CPUSome, MemorySome, MemoryFull, IOSome, IOFull float64
	// Known is whether /proc/pressure could be read at all.
	Known bool
	// SwapIns is /proc/vmstat's pswpin: pages swapped in since boot.
	SwapIns uint64
	// OOMKills is /proc/vmstat's oom_kill: processes the kernel's OOM
	// killer (global or cgroup) has killed since boot.
	OOMKills uint64
}

// procRoot is where ReadPressure looks for /proc -- a var so a test can
// point it at a fake tree.
var procRoot = "/proc"

// ReadPressure reads PSI and the vmstat counters. A missing
// /proc/pressure (a kernel built without PSI) is not an error: the
// counters are still worth having, and Known says the rest is absent.
func ReadPressure() (Pressure, error) {
	var p Pressure
	data, err := os.ReadFile(filepath.Join(procRoot, "vmstat"))
	if err != nil {
		return Pressure{}, fmt.Errorf("sysstat: reading /proc/vmstat: %w", err)
	}
	p.SwapIns, p.OOMKills = parseVMStat(string(data))
	read := func(name string) (some, full float64, ok bool) {
		data, err := os.ReadFile(filepath.Join(procRoot, "pressure", name))
		if err != nil {
			return 0, 0, false
		}
		some, full = parsePSI(string(data))
		return some, full, true
	}
	var okCPU, okMem, okIO bool
	p.CPUSome, _, okCPU = read("cpu")
	p.MemorySome, p.MemoryFull, okMem = read("memory")
	p.IOSome, p.IOFull, okIO = read("io")
	p.Known = okCPU || okMem || okIO
	return p, nil
}

// parsePSI reads one /proc/pressure file's avg10 for its "some" and
// "full" lines:
//
//	some avg10=0.12 avg60=0.05 avg300=0.01 total=12345
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=0
func parsePSI(text string) (some, full float64) {
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		var avg10 float64
		for _, f := range fields[1:] {
			if v, ok := strings.CutPrefix(f, "avg10="); ok {
				avg10, _ = strconv.ParseFloat(v, 64)
			}
		}
		switch fields[0] {
		case "some":
			some = avg10
		case "full":
			full = avg10
		}
	}
	return some, full
}

// parseVMStat reads pswpin and oom_kill out of /proc/vmstat.
func parseVMStat(text string) (swapIns, oomKills uint64) {
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		name, value, ok := strings.Cut(sc.Text(), " ")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil {
			continue
		}
		switch name {
		case "pswpin":
			swapIns = n
		case "oom_kill":
			oomKills = n
		}
	}
	return swapIns, oomKills
}
