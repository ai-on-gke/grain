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

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/safepath"
)

// userHZ is the unit of /proc/<pid>/stat's utime, stime, cutime and
// cstime columns: clock ticks of USER_HZ, which the kernel fixes at 100
// for everything it exports to user space on every architecture Linux
// supports (sysconf(_SC_CLK_TCK) answers 100; CONFIG_HZ is internal and
// converted before it reaches /proc). Reading it through sysconf would
// need cgo, which the release build does not have.
const userHZ = 100

// maxTreeDepth bounds the walk up a process's parents, so a corrupt or
// racing read of ppid can never loop forever.
const maxTreeDepth = 64

func processTreeCPU(roots map[string]string) (map[string]time.Duration, error) {
	return readProcessTreeCPU("/proc", roots)
}

type procStat struct {
	ppid  int
	ticks int64
}

// readProcessTreeCPU is ProcessTreeCPU over a /proc rooted at procDir,
// so tests can hand it a directory of their own.
func readProcessTreeCPU(procDir string, roots map[string]string) (map[string]time.Duration, error) {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return nil, fmt.Errorf("sysstat: reading %s: %w", procDir, err)
	}
	clean := make(map[string]string, len(roots))
	for key, dir := range roots {
		if dir == "" {
			continue
		}
		clean[key] = filepath.Clean(dir)
	}

	procs := map[int]procStat{}
	owner := map[int]string{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		dir := filepath.Join(procDir, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			// Gone since the directory was listed, or not ours to read.
			continue
		}
		st, ok := parseProcStat(string(data))
		if !ok {
			continue
		}
		procs[pid] = st
		cwd, err := os.Readlink(filepath.Join(dir, "cwd"))
		if err != nil {
			// Another user's process: not something a run of ours
			// started, and nothing to attribute.
			continue
		}
		if key, ok := rootFor(clean, cwd); ok {
			owner[pid] = key
		}
	}

	out := map[string]time.Duration{}
	for pid, st := range procs {
		key, ok := ownerOf(pid, procs, owner)
		if !ok {
			continue
		}
		out[key] += time.Duration(st.ticks) * time.Second / userHZ
	}
	return out, nil
}

// rootFor names the root whose directory cwd is, or is under. Roots are
// compared with safepath.Within so symlinks are resolved and runs/1-1
// never claims runs/1-10 or a symlink pointing outside the root.
func rootFor(roots map[string]string, cwd string) (string, bool) {
	if !filepath.IsAbs(cwd) {
		return "", false
	}
	for key, dir := range roots {
		if resolved, err := safepath.Within(dir, cwd); err == nil && resolved != "" {
			return key, true
		}
	}
	return "", false
}

// ownerOf finds the root a process belongs to: its own, or the nearest
// ancestor's. The result is memoised in owner, including the negative
// answer as "" so a deep unattributed tree is walked once.
func ownerOf(pid int, procs map[int]procStat, owner map[int]string) (string, bool) {
	var path []int
	cur := pid
	key := ""
	for depth := 0; depth < maxTreeDepth; depth++ {
		if k, ok := owner[cur]; ok {
			key = k
			break
		}
		st, ok := procs[cur]
		if !ok || st.ppid <= 0 || st.ppid == cur {
			break
		}
		path = append(path, cur)
		cur = st.ppid
	}
	for _, p := range path {
		owner[p] = key
	}
	if _, seen := owner[pid]; !seen {
		owner[pid] = key
	}
	return key, key != ""
}

// parseProcStat reads the ppid and the CPU columns out of one
// /proc/<pid>/stat line. comm, the second field, is in parentheses and
// may itself contain spaces and parentheses, so the fields are counted
// from the last ')' rather than split from the start.
func parseProcStat(line string) (procStat, bool) {
	end := strings.LastIndexByte(line, ')')
	if end < 0 {
		return procStat{}, false
	}
	// After comm: state(3) ppid(4) ... utime(14) stime(15) cutime(16) cstime(17).
	f := strings.Fields(line[end+1:])
	if len(f) < 15 {
		return procStat{}, false
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return procStat{}, false
	}
	var ticks int64
	for _, s := range f[11:15] {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return procStat{}, false
		}
		if v > 0 {
			ticks += v
		}
	}
	return procStat{ppid: ppid, ticks: ticks}, true
}
