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

//go:build !darwin

package sysstat

// The /proc half of Read: /proc/loadavg, /proc/meminfo and /proc/stat,
// three files, no subprocess and no syscall table to get right. This is
// the implementation grain deploys on, and it is unchanged from when it
// was the only one -- the darwin work beside it (sysstat_darwin.go) was
// deliberately kept out of these functions rather than folded into them
// behind runtime.GOOS checks, because a reading that looks the same in
// source and comes from two entirely different interfaces is exactly the
// kind of code where one platform's bug hides in the other's tests.
//
// The tag is !darwin rather than linux so that every platform with
// neither /proc nor a native implementation -- windows, the BSDs -- keeps
// the behaviour it has always had: these functions compile anywhere (they
// are file reads and string parsing, nothing platform-specific), fail at
// the open, and name the missing /proc file in the error. There is
// nothing to gain from a third stub that says the same thing less
// precisely.

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func read() (Snapshot, error) {
	s, err := readLoadAverage()
	if err != nil {
		return Snapshot{}, err
	}
	s.MemTotalMB, s.MemUsedMB, s.SwapTotalMB, s.SwapUsedMB, err = readMemory()
	if err != nil {
		return Snapshot{}, err
	}
	s.CPUs, err = readCPUs()
	if err != nil {
		return Snapshot{}, err
	}
	before, err := readCPUTimes()
	if err != nil {
		return Snapshot{}, err
	}
	time.Sleep(100 * time.Millisecond)
	after, err := readCPUTimes()
	if err != nil {
		return Snapshot{}, err
	}
	if total := after.total - before.total; total > 0 {
		idle := after.idle - before.idle
		s.CPUUtilization = float64(total-idle) / float64(total)
	}
	return s, nil
}

type cpuTimes struct {
	total uint64
	idle  uint64
}

func readCPUTimes() (cpuTimes, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTimes{}, fmt.Errorf("sysstat: reading /proc/stat: %w", err)
	}
	line, _, _ := strings.Cut(string(data), "\n")
	return parseCPUTimes(line)
}

// cpuTimeColumns is how many of the aggregate line's columns are summed:
// user, nice, system, idle, iowait, irq, softirq and steal. The guest and
// guest_nice columns that follow are not added, because the kernel
// already counts that time inside user and nice (see proc(5)); summing
// them again inflates both total and busy time on any host running VMs.
const cpuTimeColumns = 8

// parseCPUTimes reads /proc/stat's aggregate "cpu" line.
func parseCPUTimes(line string) (cpuTimes, error) {
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuTimes{}, fmt.Errorf("sysstat: malformed aggregate CPU line: %q", line)
	}
	var times cpuTimes
	for i, field := range fields[1:] {
		if i >= cpuTimeColumns {
			break
		}
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuTimes{}, fmt.Errorf("sysstat: parsing aggregate CPU line %q: %w", line, err)
		}
		times.total += value
		if i == 3 || i == 4 {
			times.idle += value
		}
	}
	return times, nil
}

// readCPUs counts /proc/stat's per-CPU lines: "cpu0", "cpu1", ... beside
// the aggregate "cpu" line the kernel writes first. One line per logical
// processor the kernel knows about, in the same file on every
// architecture -- see Snapshot.CPUs for why this rather than
// /proc/cpuinfo or runtime.NumCPU.
func readCPUs() (int, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, fmt.Errorf("sysstat: reading /proc/stat: %w", err)
	}
	defer f.Close()

	cpus := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		name, _, _ := strings.Cut(scanner.Text(), " ")
		// "cpu" itself is the sum of the others, not a processor.
		if len(name) > len("cpu") && strings.HasPrefix(name, "cpu") {
			cpus++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("sysstat: reading /proc/stat: %w", err)
	}
	if cpus == 0 {
		return 0, fmt.Errorf("sysstat: /proc/stat has no per-CPU lines")
	}
	return cpus, nil
}

func readLoadAverage() (Snapshot, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return Snapshot{}, fmt.Errorf("sysstat: reading /proc/loadavg: %w", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return Snapshot{}, fmt.Errorf("sysstat: /proc/loadavg has too few fields: %q", data)
	}
	load1, err1 := strconv.ParseFloat(fields[0], 64)
	load5, err2 := strconv.ParseFloat(fields[1], 64)
	load15, err3 := strconv.ParseFloat(fields[2], 64)
	if err := firstOf(err1, err2, err3); err != nil {
		return Snapshot{}, fmt.Errorf("sysstat: parsing /proc/loadavg %q: %w", data, err)
	}
	return Snapshot{LoadAverage1: load1, LoadAverage5: load5, LoadAverage15: load15}, nil
}

func firstOf(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func readMemory() (totalMB, usedMB, swapTotalMB, swapUsedMB int, err error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("sysstat: reading /proc/meminfo: %w", err)
	}
	defer f.Close()

	var totalKB, availKB, swapTotalKB, swapFreeKB int
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		val, convErr := strconv.Atoi(fields[1])
		if convErr != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			totalKB = val
		case "MemAvailable:":
			availKB = val
		case "SwapTotal:":
			swapTotalKB = val
		case "SwapFree:":
			swapFreeKB = val
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("sysstat: reading /proc/meminfo: %w", err)
	}
	if totalKB == 0 {
		return 0, 0, 0, 0, fmt.Errorf("sysstat: /proc/meminfo has no MemTotal line")
	}
	return totalKB / 1024, (totalKB - availKB) / 1024, swapTotalKB / 1024, (swapTotalKB - swapFreeKB) / 1024, nil
}
