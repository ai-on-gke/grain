package upgrade

// How long a restart made grain unreachable, per listener, measured by
// the daemon itself on both sides of the restart.
//
// This is Stage 1's measurement in docs/non-disruptive-upgrade.md: the
// API, the git proxy and the model proxies all refuse connections while
// one process replaces another, and whether that gap is worth a
// long-lived front door to hide depends on how long it actually is in
// each deployment mode. Nothing outside the daemon can answer that as
// well as the daemon can, so the outgoing process writes down when it
// closed each listener (GapRecorder.Down), the incoming one when it
// bound each again (GapRecorder.Up), and the difference is the gap.
//
// The file lives under the data directory, beside the upgrade's own
// status file, and holds nothing but service names and times.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The services a gap is measured for. Model proxies are named
// ServiceModelProxy+" ("+vendor+")".
const (
	ServiceAPI        = "API"
	ServiceGitProxy   = "git proxy"
	ServiceModelProxy = "model proxy"
	// serviceShutdown is not a listener: it is when the outgoing process
	// was told to stop, which stands in for a listener whose own close
	// was never recorded -- a process killed (docker stop's timeout, a
	// pod's grace period) before it got that far.
	serviceShutdown = "shutdown"
)

// DeploymentModeName is how a gap names the mode it was measured in --
// the three the design doc asks for -- from the upgrader's own Mode. A
// deployment with no upgrader is a host binary.
func DeploymentModeName(m Mode) string {
	switch m {
	case ModeImage:
		return "container"
	case ModeKube:
		return "kubernetes"
	}
	return "host binary"
}

// RestartGap is the last restart's measurement.
type RestartGap struct {
	// Mode is the deployment mode it was measured in (DeploymentModeName).
	Mode string `json:"mode"`
	// StartedAt is when the incoming process started.
	StartedAt time.Time `json:"startedAt"`
	// Services is one entry per listener the incoming process bound, in
	// the order it bound them.
	Services []ServiceGap `json:"services"`
}

// ServiceGap is one listener's gap.
type ServiceGap struct {
	Service string    `json:"service"`
	UpAt    time.Time `json:"upAt"`
	// DownAt is when the outgoing process stopped serving it. Nil when it
	// left no record at all -- a first start, or a process that died
	// without being told to stop -- and then there is no gap to report.
	DownAt *time.Time `json:"downAt,omitempty"`
	// FromShutdown says DownAt is when the outgoing process was told to
	// stop rather than when it closed this listener, because it was
	// killed before it did: the listener may have served for part of
	// that, so the gap is an upper bound.
	FromShutdown bool `json:"fromShutdown,omitempty"`
}

// Gap is how long the service was unavailable, and false where that is
// not known.
func (s ServiceGap) Gap() (time.Duration, bool) {
	if s.DownAt == nil {
		return 0, false
	}
	return s.UpAt.Sub(*s.DownAt), true
}

// Describe is one line for a person: "API 4.2s", "git proxy at most
// 31s (...)", "model proxy (claude) unknown (...)".
func (s ServiceGap) Describe() string {
	gap, ok := s.Gap()
	switch {
	case !ok:
		return s.Service + " unknown (the previous process left no record of stopping)"
	case s.FromShutdown:
		return fmt.Sprintf("%s at most %s (the previous process was killed before it closed this listener; measured from when it was told to stop)",
			s.Service, gap.Round(100*time.Millisecond))
	}
	return fmt.Sprintf("%s %s", s.Service, gap.Round(100*time.Millisecond))
}

// Summary is the whole measurement on one line.
func (g RestartGap) Summary() string {
	parts := make([]string, 0, len(g.Services))
	for _, s := range g.Services {
		parts = append(parts, s.Describe())
	}
	return fmt.Sprintf("unavailable across the restart (%s): %s", g.Mode, strings.Join(parts, "; "))
}

// gapFile is what is on disk: the outgoing process's down times, and the
// incoming process's measurement.
type gapFile struct {
	Down map[string]time.Time `json:"down,omitempty"`
	Last *RestartGap          `json:"last,omitempty"`
}

// GapFile is where a deployment keeps its restart-gap file, under its
// data directory.
func GapFile(dataDir string) string { return filepath.Join(dataDir, "state", "restart-gap.json") }

// GapRecorder records one process's side of two restarts: when it came
// up after the previous one (Up), and when it goes down for the next one
// (Down). Every method is safe on a nil *GapRecorder and does nothing.
//
// Nothing is written to its file until this process has served: the
// first Up. A start that is refused before any listener comes up -- a
// store the startup guard turns away, a bad flag -- leaves the file
// exactly as the previous process left it, so whichever build runs on
// the data dir next still measures from the process that last served.
type GapRecorder struct {
	path string
	now  func() time.Time

	mu       sync.Mutex
	previous map[string]time.Time
	file     gapFile
	// served is set by the first Up, and is what lets this process's
	// measurement replace the previous one on disk.
	served bool
}

// OpenGapRecorder starts this process's measurement in path: it reads
// the outgoing process's down times, and starts a fresh measurement for
// mode, kept in memory until the first Up writes it. A file that cannot
// be read is logged and treated as empty -- a measurement is never a
// reason for the daemon not to start.
func OpenGapRecorder(path, mode string, now func() time.Time) *GapRecorder {
	if now == nil {
		now = time.Now
	}
	r := &GapRecorder{path: path, now: now}
	var prev gapFile
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &prev); err != nil {
			log.Printf("grain daemon: reading %s: %v; the restart gap will not be measured this time", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		log.Printf("grain daemon: reading %s: %v; the restart gap will not be measured this time", path, err)
	}
	r.previous = prev.Down
	r.file = gapFile{Last: &RestartGap{Mode: mode, StartedAt: now().UTC()}}
	return r
}

// Up records that service is serving again, works out how long it was
// not, and says so in the journal.
func (r *GapRecorder) Up(service string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := ServiceGap{Service: service, UpAt: r.now().UTC()}
	if down, ok := r.previous[service]; ok {
		entry.DownAt = &down
	} else if down, ok := r.previous[serviceShutdown]; ok {
		entry.DownAt = &down
		entry.FromShutdown = true
	}
	r.file.Last.Services = append(r.file.Last.Services, entry)
	r.served = true
	r.saveLocked()
	if entry.DownAt != nil {
		log.Printf("grain daemon: %s (%s)", entry.Describe(), "unavailable across the restart, "+r.file.Last.Mode)
	}
}

// Down records that service has stopped serving, for the next process
// to measure from. Down(serviceShutdown) is ShuttingDown. Before any Up
// it is recorded in memory only: a process that never served has nothing
// the next one should measure from but what the previous one left.
func (r *GapRecorder) Down(service string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file.Down == nil {
		r.file.Down = map[string]time.Time{}
	}
	r.file.Down[service] = r.now().UTC()
	r.saveLocked()
}

// ShuttingDown records that this process has been told to stop, which
// is what a listener it never got to close is measured from.
func (r *GapRecorder) ShuttingDown() { r.Down(serviceShutdown) }

// Last is this process's measurement so far.
func (r *GapRecorder) Last() RestartGap {
	if r == nil {
		return RestartGap{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := *r.file.Last
	out.Services = append([]ServiceGap(nil), r.file.Last.Services...)
	return out
}

// saveLocked writes the file, aside and renamed into place, once this
// process has served (r.served). A failure is logged and otherwise
// ignored. r.mu must be held.
func (r *GapRecorder) saveLocked() {
	if !r.served {
		return
	}
	data, err := json.MarshalIndent(r.file, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		log.Printf("grain daemon: writing %s: %v", r.path, err)
		return
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		log.Printf("grain daemon: writing %s: %v", r.path, err)
		return
	}
	if err := os.Rename(tmp, r.path); err != nil {
		log.Printf("grain daemon: writing %s: %v", r.path, err)
	}
}

// ReadRestartGap reads the last measurement from path, and false where
// there is none.
func ReadRestartGap(path string) (RestartGap, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RestartGap{}, false
	}
	var f gapFile
	if err := json.Unmarshal(data, &f); err != nil || f.Last == nil {
		return RestartGap{}, false
	}
	sort.SliceStable(f.Last.Services, func(i, j int) bool { return f.Last.Services[i].UpAt.Before(f.Last.Services[j].UpAt) })
	return *f.Last, true
}
