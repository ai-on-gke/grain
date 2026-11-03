// Package cgroupmem reads the memory accounting of the cgroup this
// process runs in: how many times the kernel's OOM killer has fired in it,
// and what limit it enforces.
//
// It exists so that a VMM killed by its container's memory limit is
// reported as that. When cloud-hypervisor is OOM-killed the guest sees
// nothing at all -- no OOM, no panic, nothing in its dmesg -- and
// everything talking to it over vsock sees only a connection that closed.
// The one place the cause is recorded is the cgroup's own memory.events
// (cgroup v2) or memory.oom_control (v1) oom_kill counter, which both
// "granule run" (the VMM's parent) and "granule exec" (started in the same
// container by docker or kubectl exec, so in the same cgroup) can read.
package cgroupmem

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Root is where this process's cgroup is mounted. Inside a container with
// its own cgroup namespace -- docker's default under cgroup v2, and every
// Kubernetes container -- that is the container's own cgroup.
var Root = "/sys/fs/cgroup"

// Stats is a snapshot of the counters this package reads.
type Stats struct {
	// OOMKills is the number of processes the OOM killer has killed in
	// this cgroup (and, under v2, its descendants) since it was created.
	OOMKills int64

	// LimitBytes is the cgroup's hard memory limit, or zero when it has
	// none.
	LimitBytes int64

	// PeakBytes is the highest usage the cgroup has recorded (v2's
	// memory.peak, v1's memory.max_usage_in_bytes), or zero when the
	// kernel does not keep one.
	PeakBytes int64
}

// Read returns the current Stats for the cgroup at Root. It returns an
// error when no memory controller is visible there at all, so a caller can
// tell "no OOM kills" apart from "cannot know".
func Read() (Stats, error) {
	return ReadAt(Root)
}

// ReadAt is Read for the cgroup mounted at root.
func ReadAt(root string) (Stats, error) {
	if data, err := os.ReadFile(filepath.Join(root, "memory.events")); err == nil {
		var s Stats
		s.OOMKills = field(data, "oom_kill")
		s.LimitBytes = readLimit(filepath.Join(root, "memory.max"))
		s.PeakBytes = readLimit(filepath.Join(root, "memory.peak"))
		return s, nil
	}
	v1 := filepath.Join(root, "memory")
	if data, err := os.ReadFile(filepath.Join(v1, "memory.oom_control")); err == nil {
		var s Stats
		s.OOMKills = field(data, "oom_kill")
		s.LimitBytes = readLimit(filepath.Join(v1, "memory.limit_in_bytes"))
		s.PeakBytes = readLimit(filepath.Join(v1, "memory.max_usage_in_bytes"))
		// v1 reports "no limit" as a page-rounded LONG_MAX rather than
		// "max"; anything past an exabyte is that.
		if s.LimitBytes > 1<<60 {
			s.LimitBytes = 0
		}
		return s, nil
	}
	return Stats{}, errors.New("no cgroup memory controller found under " + root)
}

// field returns the value of key in a flat-keyed cgroup file ("key value"
// per line), or zero when it is absent.
func field(data []byte, key string) int64 {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), " ")
		if !ok || k != key {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// readLimit reads a single-value cgroup file, mapping "max", an absent
// file and anything unparseable to zero.
func readLimit(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// OOMKilledError is what a VMM's death becomes when the cgroup's OOM
// killer is what ended it.
type OOMKilledError struct {
	// Before and After are Stats from before and after the death; After's
	// OOMKills is greater than Before's.
	Before, After Stats
}

func (e *OOMKilledError) Error() string {
	var b strings.Builder
	b.WriteString("VM killed: host memory limit, cgroup OOM")
	var details []string
	if e.After.LimitBytes > 0 {
		details = append(details, fmt.Sprintf("the VM's container is limited to %d MiB", e.After.LimitBytes>>20))
	}
	details = append(details, fmt.Sprintf("memory.events oom_kill went from %d to %d", e.Before.OOMKills, e.After.OOMKills))
	b.WriteString(" (" + strings.Join(details, "; ") + ")")
	b.WriteString(": cloud-hypervisor was killed by the host kernel for exceeding the container's memory limit, not by anything inside the guest")
	return b.String()
}

// Is lets errors.Is(err, ErrOOMKilled) match any *OOMKilledError.
func (e *OOMKilledError) Is(target error) bool { return target == ErrOOMKilled }

// ErrOOMKilled matches any *OOMKilledError under errors.Is.
var ErrOOMKilled = errors.New("VM killed: host memory limit, cgroup OOM")

// Watch takes a baseline now and returns a function that, called later,
// returns an *OOMKilledError if the OOM killer has fired in the cgroup
// since -- and nil if it has not, or if the cgroup cannot be read.
func Watch() func() error {
	root := Root
	before, err := ReadAt(root)
	if err != nil {
		return func() error { return nil }
	}
	return func() error {
		after, err := ReadAt(root)
		if err != nil || after.OOMKills <= before.OOMKills {
			return nil
		}
		return &OOMKilledError{Before: before, After: after}
	}
}
