package cli

// The host's side of grain/task-196: which volumes a deployment reads,
// which reading it is held by when they disagree, and what it says in
// the journal about holding work back. The filesystem figures are
// stubbed -- filling a 443 GB volume is not a unit test -- and
// pkg/sysstat's own tests cover the statfs underneath them.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
)

// fakeVolumes is a set of paths with sizes, standing in for statfs.
type fakeVolumes struct {
	// usage is total/used MiB per path, as sysstat.DiskUsage reports it.
	usage map[string][2]int
	// device is the filesystem each path falls on, as sysstat.
	// FilesystemID reports it -- two paths with the same number are one
	// volume answering twice.
	device map[string]uint64
	// missing are the paths that will not answer at all.
	missing map[string]bool
}

func (f *fakeVolumes) DiskUsage(path string) (int, int, error) {
	if f.missing[path] {
		return 0, 0, errors.New("statfs " + path + ": no such file or directory")
	}
	u, ok := f.usage[path]
	if !ok {
		return 0, 0, fmt.Errorf("no stub for %s", path)
	}
	return u[0], u[1], nil
}

func (f *fakeVolumes) FilesystemID(path string) (uint64, error) {
	if id, ok := f.device[path]; ok {
		return id, nil
	}
	return 0, errors.New("no device for " + path)
}

func watchOn(cfg daemon.Config, vols *fakeVolumes, log func(string, ...any)) *daemon.DiskWatch {
	return watchFloor(cfg, vols, log, nil)
}

// watchFloor is the same watcher with a floor that can move under it --
// what a daemon has, where the floor is a stored setting re-read each
// reconcile tick (liveConfig.diskFloor). nil reads cfg's own, which is
// what the flags seeded.
func watchFloor(cfg daemon.Config, vols *fakeVolumes, log func(string, ...any), floor func() model.DiskFloor) *daemon.DiskWatch {
	w := daemon.NewDiskWatch(cfg, floor)
	if w == nil {
		return nil
	}
	w.Usage, w.Identity = vols.DiskUsage, vols.FilesystemID
	w.Now = func() time.Time { return time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC) }
	if log != nil {
		w.Log = log
	} else {
		w.Log = func(string, ...any) {}
	}
	return w
}

func floorCfg() daemon.Config {
	return daemon.Config{
		DataDir:        "/var/lib/grain/data",
		SandboxDir:     "/var/lib/grain/scratch",
		DiskFloorShare: model.DefaultDiskFloorShare,
		DiskFloorMinMB: model.MinDiskFloorMB,
	}
}

// The deployment is held by whichever volume is closest to full: these
// are separate filesystems, and a job cannot spend one's free space on
// another.
func TestDiskWatchReportsTheVolumeWithLeastRoomOverItsFloor(t *testing.T) {
	vols := &fakeVolumes{
		usage: map[string][2]int{
			// A roomy 443 GB sandbox volume and a 20 GB store disk with
			// 4 GiB left -- under its own 5 GiB floor.
			"/var/lib/grain/data":    {20 * 1024, 16 * 1024},
			"/var/lib/grain/scratch": {443 * 1024, 100 * 1024},
		},
		device: map[string]uint64{"/var/lib/grain/data": 1, "/var/lib/grain/scratch": 2},
	}
	space := watchOn(floorCfg(), vols, nil).Space()
	if space.Path != "/var/lib/grain/data" {
		t.Fatalf("the reading is of %s, want the store disk that is nearly full: %+v", space.Path, space)
	}
	if space.FreeMB != 4*1024 {
		t.Errorf("free = %d MiB, want 4 GiB", space.FreeMB)
	}
	if verdict := model.DefaultDiskFloor().Admits(space); verdict.OK {
		t.Error("a volume 4 GiB from full admitted work")
	}
}

// Two of these paths are usually one filesystem (terraform/gcp
// bind-mounts the sandbox volume onto -sandbox-dir), and one volume
// should not be read as two.
func TestDiskWatchFoldsPathsThatShareAFilesystem(t *testing.T) {
	vols := &fakeVolumes{
		usage: map[string][2]int{
			"/var/lib/grain/data":    {443 * 1024, 400 * 1024},
			"/var/lib/grain/scratch": {443 * 1024, 400 * 1024},
		},
		device: map[string]uint64{"/var/lib/grain/data": 7, "/var/lib/grain/scratch": 7},
	}
	var logged []string
	w := watchOn(floorCfg(), vols, func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	space := w.Space()
	if space.Path != "/var/lib/grain/data" {
		t.Errorf("the first path onto a filesystem keeps the reading: got %s", space.Path)
	}
	if len(logged) != 1 {
		t.Fatalf("a volume under its floor was said %d times, want once: %v", len(logged), logged)
	}
	if !strings.Contains(logged[0], "held") || !strings.Contains(logged[0], "/var/lib/grain/data") {
		t.Errorf("the journal line does not say what is held or why: %q", logged[0])
	}
}

// Said once when it starts holding work back, and once when it stops --
// not once per poll, which on a gate polling every second is a log an
// operator cannot read.
func TestDiskWatchSaysItOnceGoingDownAndOnceComingBack(t *testing.T) {
	vols := &fakeVolumes{
		usage:  map[string][2]int{"/var/lib/grain/data": {443 * 1024, 430 * 1024}},
		device: map[string]uint64{"/var/lib/grain/data": 1},
	}
	var logged []string
	cfg := floorCfg()
	cfg.SandboxDir = ""
	w := watchOn(cfg, vols, func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	for i := 0; i < 5; i++ {
		w.Space()
	}
	if len(logged) != 1 {
		t.Fatalf("five readings of a full volume logged %d lines, want 1: %v", len(logged), logged)
	}

	vols.usage["/var/lib/grain/data"] = [2]int{443 * 1024, 100 * 1024}
	for i := 0; i < 5; i++ {
		w.Space()
	}
	if len(logged) != 2 {
		t.Fatalf("clearing the volume logged %d lines in total, want 2: %v", len(logged), logged)
	}
	if !strings.Contains(logged[1], "admitted again") {
		t.Errorf("the recovery line does not say work is flowing again: %q", logged[1])
	}
}

// A volume that has gone away bounds nothing rather than holding
// everything: this limits how much work runs, it is not a precondition
// for running any.
func TestDiskWatchPassesOverAVolumeThatWillNotAnswer(t *testing.T) {
	vols := &fakeVolumes{
		usage:   map[string][2]int{"/var/lib/grain/scratch": {443 * 1024, 100 * 1024}},
		device:  map[string]uint64{"/var/lib/grain/scratch": 2},
		missing: map[string]bool{"/var/lib/grain/data": true},
	}
	var logged []string
	w := watchOn(floorCfg(), vols, func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	space := w.Space()
	if space.Path != "/var/lib/grain/scratch" {
		t.Fatalf("the reading is %+v, want the volume that did answer", space)
	}
	if !model.DefaultDiskFloor().Admits(space).OK {
		t.Error("a deployment was held by a volume it could not read")
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "would not answer") {
		t.Fatalf("an unreadable volume was reported as %v, want one line saying so", logged)
	}
	// And said once, not once per poll.
	w.Space()
	if len(logged) != 1 {
		t.Errorf("an unreadable volume was reported %d times: %v", len(logged), logged)
	}

	// With nothing readable at all there is no reading, and no reading
	// bounds nothing.
	vols.missing["/var/lib/grain/scratch"] = true
	if space := w.Space(); space.Known() {
		t.Errorf("a deployment that could read no volume reported %+v", space)
	}
}

// A deployment that has turned the floor off holds nothing back, and the
// limits it hands its gate are the ones it always had. It is still
// watched, though, because the floor is a stored setting now: one turned
// back on in the pane binds the next admission rather than the next
// restart (grain/task-372).
func TestDiskWatchHoldsNothingBackWithNoFloorConfiguredAndLimitsAreUntouched(t *testing.T) {
	off := floorCfg()
	off.DiskFloorShare, off.DiskFloorMinMB = 0, 0
	vols := &fakeVolumes{
		usage:  map[string][2]int{"/var/lib/grain/data": {443 * 1024, 442 * 1024}},
		device: map[string]uint64{"/var/lib/grain/data": 1},
	}
	off.SandboxDir = ""
	var logged []string
	w := watchOn(off, vols, func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	if w == nil {
		t.Fatal("a deployment with a volume to read got no watcher, so a floor turned on in the pane would need a restart")
	}
	if space := w.Space(); !w.Floor().Admits(space).OK {
		t.Errorf("a deployment holding nothing back refused work on %+v", space)
	}
	if len(logged) != 0 {
		t.Errorf("a deployment with no floor wrote to the journal about one: %v", logged)
	}
	// And says what it is doing at startup, naming the setting rather
	// than the flag that used to decide it.
	daemon.LogDiskFloor(w)
	if len(logged) != 1 || !strings.Contains(logged[0], "no disk floor") || !strings.Contains(logged[0], "Settings") {
		t.Errorf("the startup line for a deployment with no floor is %v", logged)
	}

	if w := daemon.NewDiskWatch(daemon.Config{DiskFloorShare: model.DefaultDiskFloorShare}, nil); w != nil {
		t.Errorf("a deployment with no path to read got a watcher: %+v", w)
	}
	if daemon.DiskSpaceOrNil(nil) != nil {
		t.Error("a cycle with no watcher was given a reader that answers nothing")
	}

	base := model.Limits{Workers: 3, Budget: model.Budget{DiskFloor: model.DefaultDiskFloor()}}
	limits := daemon.LimitsWithDisk(func() model.Limits { return base }, nil)
	if got := limits(); got.Budget.Disk.Known() {
		t.Errorf("limits with no watcher carried a reading: %+v", got.Budget.Disk)
	}
}

// The whole of grain/task-372, from the watcher's side: the floor is a
// stored setting, so the moment it is lowered in the Settings pane is
// the moment the next reading is judged against the new figure -- not
// the next daemon restart, which is a thing nobody wants to do to a
// deployment whose disk has already stopped every dispatch.
func TestDiskWatchReadsTheFloorLiveRatherThanTheOneItWasBuiltWith(t *testing.T) {
	vols := &fakeVolumes{
		// 443 GB with 30 GiB free: under the default 44.3 GiB floor, and
		// over a floor of a twentieth (22.1 GiB).
		usage:  map[string][2]int{"/var/lib/grain/data": {443 * 1024, 413 * 1024}},
		device: map[string]uint64{"/var/lib/grain/data": 1},
	}
	cfg := floorCfg()
	cfg.SandboxDir = ""
	floor := model.DefaultDiskFloor()
	var logged []string
	w := watchFloor(cfg, vols, func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}, func() model.DiskFloor { return floor })

	if space := w.Space(); w.Floor().Admits(space).OK {
		t.Fatalf("a volume 30 GiB from full under a 44 GiB floor admitted work: %+v", space)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "held") {
		t.Fatalf("the journal did not say work was held: %v", logged)
	}

	// An operator lowers it in the pane; the next reconcile tick puts it
	// on the row this watcher reads.
	floor = model.DiskFloor{Share: 0.05, MinMB: model.MinDiskFloorMB}
	if space := w.Space(); !w.Floor().Admits(space).OK {
		t.Errorf("the lowered floor did not reach the watcher: %+v held against %+v", space, w.Floor())
	}
	if len(logged) != 2 || !strings.Contains(logged[1], "admitted again") {
		t.Errorf("the journal did not say work was flowing again: %v", logged)
	}
	// And the reclaim wired to this watcher sweeps to the new floor's
	// target rather than the old one's, since it asks the same func.
	r := &daemon.DiskReclaim{Steps: []daemon.ReclaimStep{{What: "nothing", Run: func(context.Context) (string, error) { return "", nil }}}}
	daemon.ReclaimWith(w, context.Background(), r)
	if got, want := r.Floor(), floor; got != want {
		t.Errorf("the reclaim's floor = %+v, want the one in effect now, %+v", got, want)
	}
}

// The floor is held against the volumes the host status pane draws a row
// for, not against a list of its own (grain/task-214): run() resolves
// them once (config.volumes) and both halves read that, so a docker data
// root nobody configured -- discovered by the one `docker info` the pane
// already pays for -- is a volume this deployment is judged against as
// well as one it reports.
func TestDiskWatchReadsTheSameVolumesTheHostPaneDraws(t *testing.T) {
	cfg := floorCfg()
	cfg.Volumes = []daemon.HostDisk{
		{Holds: "store", Path: "/var/lib/grain/data"},
		{Holds: "sandboxes", Path: "/var/lib/grain/scratch"},
		// Discovered rather than configured: cfg.dockerRootDir is empty,
		// so the old three-flag list would never have read this one.
		{Holds: "docker", Path: "/mnt/sandbox/docker"},
	}
	vols := &fakeVolumes{
		usage: map[string][2]int{
			"/var/lib/grain/data":    {20 * 1024, 4 * 1024},
			"/var/lib/grain/scratch": {443 * 1024, 100 * 1024},
			// The volume actually in trouble, and the one the pane is
			// drawing a bar for: 13 GiB left of 443, against a 44 GiB floor.
			"/mnt/sandbox/docker": {443 * 1024, 430 * 1024},
		},
		device: map[string]uint64{
			"/var/lib/grain/data": 1, "/var/lib/grain/scratch": 2, "/mnt/sandbox/docker": 3,
		},
	}
	space := watchOn(cfg, vols, nil).Space()
	if space.Path != "/mnt/sandbox/docker" {
		t.Errorf("the reading is of %s, want docker's own data root: %+v", space.Path, space)
	}
	if model.DefaultDiskFloor().Admits(space).OK {
		t.Error("work was admitted onto the volume the pane shows 13 GiB free on")
	}

	// And a config nobody resolved a list for still reads the flags, and
	// still execs nothing: a `docker info` from in here would be a
	// subprocess per test binary and per `grain daemon` that never got as
	// far as a UI.
	bare := floorCfg()
	bare.DockerRootDir = "/mnt/sandbox/docker"
	if got, want := daemon.DiskFloorPaths(bare), []string{"/var/lib/grain/data", "/var/lib/grain/scratch", "/mnt/sandbox/docker"}; !slices.Equal(got, want) {
		t.Errorf("diskFloorPaths = %v, want the configured paths %v", got, want)
	}

	// And the startup line names all three, not only the one it happened
	// to be tightest on: which volumes a deployment is judged on is the
	// half of the floor that cannot be inferred from the figures, and a
	// docker root nobody configured appears here first.
	var logged []string
	w := watchOn(cfg, vols, func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	daemon.LogDiskFloor(w)
	startup := logged[len(logged)-1]
	for _, path := range []string{"/var/lib/grain/data", "/var/lib/grain/scratch", "/mnt/sandbox/docker"} {
		if !strings.Contains(startup, path) {
			t.Errorf("the startup line does not say it reads %s: %q", path, startup)
		}
	}
}

// And with one, the reading reaches the budget the CI gate is held
// against -- taken per call, so a job waiting on a full volume is
// admitted by the next attempt rather than by a restart.
func TestLimitsWithDiskCarryTheCurrentReadingToTheGate(t *testing.T) {
	vols := &fakeVolumes{
		usage:  map[string][2]int{"/var/lib/grain/data": {443 * 1024, 430 * 1024}},
		device: map[string]uint64{"/var/lib/grain/data": 1},
	}
	cfg := floorCfg()
	cfg.SandboxDir = ""
	w := watchOn(cfg, vols, nil)
	base := model.Limits{
		Workers: 3, CIJobs: 4,
		AgentShape: model.Commitment{CPUs: 2, MemoryMB: 8 * 1024},
		Budget: model.Budget{
			Host:             model.Commitment{CPUs: 28, MemoryMB: 128 * 1024},
			CPUOvercommit:    2,
			MemoryOvercommit: 1,
			DiskFloor:        model.DefaultDiskFloor(),
		},
	}
	limits := daemon.LimitsWithDisk(func() model.Limits { return base }, w)

	held := limits()
	if verdict := held.Budget.Admits(model.Commitment{}, model.Commitment{CPUs: 2, MemoryMB: 4 * 1024}); verdict.OK {
		t.Error("a CI job was admitted onto a volume 13 GiB from full")
	}
	if held.SandboxAdmits() {
		t.Error("an agent run was admitted onto the same volume")
	}

	vols.usage["/var/lib/grain/data"] = [2]int{443 * 1024, 100 * 1024}
	free := limits()
	if verdict := free.Budget.Admits(model.Commitment{}, model.Commitment{CPUs: 2, MemoryMB: 4 * 1024}); !verdict.OK {
		t.Errorf("a CI job was held on a cleared volume: %s", verdict.Reason)
	}
	if !free.SandboxAdmits() {
		t.Error("an agent run was held on a cleared volume")
	}
}
