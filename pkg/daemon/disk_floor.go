package daemon

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/sysstat"
)

// diskWatch is where a deployment's free disk is read for an admission:
// one statfs per volume grain writes to, folded into the single tightest
// reading, so that a CI job is held and an agent dispatch deferred while
// the disk is filling (model.DiskFloor, grain/task-196).
//
// Read per admission rather than polled onto a field, because the two
// callers ask at exactly the moments the answer matters -- a gate poll
// and a reconcile tick -- and statfs(2) is a syscall on an already
// mounted filesystem rather than a walk of it. What is kept between
// calls is not the reading but what was last *said* about it: the log
// below is one line when a volume drops under the floor and one when it
// comes back, rather than the same line every second for as long as an
// operator takes to clear space.
//
// Which volumes: whichever ones the host status pane draws a row for,
// which is the list run() resolved once for this process
// (config.volumes, hostDisks) -- the store and the secrets under
// -data-dir, whose loss takes the deployment with it; -sandbox-dir when
// a deployment keeps per-run checkouts on this host; and docker's data
// root, which is where a granule VM's qcow2 overlay actually lands.
//
// The same list as the pane's, deliberately. It used to be the three
// flags, with docker's root read only when an operator had named one,
// because discovering it costs a subprocess and a timeout
// (dockerRootDir) and paying that twice for one answer is not worth a
// disk figure. But hostDisks already pays it once, before either half is
// built, so the choice is not between one subprocess and two -- and a
// floor judged against volumes the pane does not draw, or a bar the
// floor does not judge, is a deployment that has gone quiet for a reason
// that is not on the screen. On terraform/gcp's own layout the two lists
// come to the same filesystems anyway: docker's root is on the sandbox
// volume, reached through a bind mount, and readVolumes folds them by
// st_dev.
//
// A config built without that resolution -- a test's -- still reads the
// flags alone, and still execs nothing (diskFloorPaths).
type DiskWatch struct {
	// paths are the configured paths to take readings through, in the
	// order an operator would name them. Duplicates and paths that turn
	// out to share a filesystem are folded by space().
	paths []string
	// floorOf is the rule the readings are judged against, asked for per
	// reading rather than resolved once -- the floor is a stored setting
	// and config.withModelConfig re-reads it off grain_config each
	// reconcile tick, so a watcher holding the floor this process
	// started with would go on holding work back against a number an
	// operator had already lowered in the pane (grain/task-372).
	//
	// Read through floor() below, never directly, so a watcher built
	// without one (a test's) behaves as a deployment that holds nothing
	// back rather than panicking. What it is used for here is the log
	// and the reclaim's target; the floor that actually admits or
	// refuses travels in the model.Budget, which config.budget() builds
	// from the same re-read fields.
	floorOf func() model.DiskFloor
	// usage and identity are sysstat's two readings, injectable so the
	// tests can say "this volume is nearly full" without filling one.
	Usage    func(path string) (totalMB, usedMB int, err error)
	Identity func(path string) (uint64, error)
	Log      func(format string, args ...any)
	Now      func() time.Time
	// reclaim is what this deployment does about a volume under its
	// floor, and nil where it does nothing -- which is what a floor with
	// nothing behind it was (grain/task-196, and diskReclaim's own doc
	// comment for what changed). Wired after construction by
	// reclaimWith, because the steps need things the daemon builds long
	// after it reads its flags: its sandbox backend, and a CI service
	// that is not up until the first reconcile tick.
	Reclaim *DiskReclaim
	// reclaimCtx is what a sweep runs under: the daemon's own context,
	// so a sweep in flight ends when the daemon does rather than holding
	// a shutdown open for as long as a `granule vm delete` takes. nil
	// until reclaimWith, and never nil after it.
	reclaimCtx context.Context

	mu sync.Mutex
	// held is whether the last reading was under the floor, and said is
	// when that was last logged -- together, the whole of "say it once,
	// and say it again if it is still true an hour later".
	held bool
	said time.Time
	// unreadable is the last path that would not answer, so a volume
	// that has gone away is reported once rather than once per poll.
	unreadable string
}

// diskHoldRepeat is how long a deployment held by its disk floor goes
// before the journal says so again. Long enough not to fill a log, short
// enough that an operator who looks at a quiet deployment ten minutes
// after the first line still finds the reason near the bottom.
const DiskHoldRepeat = 10 * time.Minute

// newDiskWatch is the watcher for this configuration, or nil where there
// is no path to take a reading through at all.
//
// floor is where the rule comes from, and it is a func rather than a
// value because the floor is a setting an operator moves while the
// daemon runs (model.Config.SandboxDiskFloorShare). The daemon passes
// liveConfig.diskFloor; a caller with nothing live to ask -- a test --
// passes nil and gets the flags as they stand.
//
// A floor that holds nothing back is not a reason to return nothing any
// more, which it was while the floor was two flags this process could
// not be told to change: a deployment that starts with the floor off and
// turns it on in the pane has to be watched from the tick that happens,
// not from the next restart. The cost of watching a deployment that
// leaves it off is one statfs per admission and no journal line at all,
// since an unknown floor admits every reading (model.DiskFloor.Admits).
func NewDiskWatch(cfg Config, floor func() model.DiskFloor) *DiskWatch {
	paths := DiskFloorPaths(cfg)
	if len(paths) == 0 {
		return nil
	}
	if floor == nil {
		floor = cfg.DiskFloor
	}
	return &DiskWatch{
		paths: paths, floorOf: floor,
		Usage: sysstat.DiskUsage, Identity: sysstat.FilesystemID,
		Log: log.Printf, Now: time.Now,
	}
}

// floor is the rule as it stands right now -- see floorOf. The zero
// floor, which holds nothing back, where there is nobody to ask.
func (w *DiskWatch) Floor() model.DiskFloor {
	if w == nil || w.floorOf == nil {
		return model.DiskFloor{}
	}
	return w.floorOf()
}

// diskFloorPaths is which volumes to take those readings through: the
// paths of the list the host status pane draws, where this process has
// resolved one, so the bars and the floor under them are about the same
// filesystems.
//
// The configured paths otherwise, which is what a config a test built
// has -- and the reason this is not simply config.hostVolumes: resolving
// a list execs `docker info` on a config with no -docker-root-dir, and
// the watcher has no business starting a subprocess. In a real daemon
// run() has already resolved it, so this branch is the test's.
func DiskFloorPaths(cfg Config) []string {
	var paths []string
	if len(cfg.Volumes) > 0 {
		for _, v := range cfg.Volumes {
			paths = append(paths, v.Path)
		}
		return paths
	}
	for _, p := range []string{cfg.DataDir, cfg.SandboxDir, cfg.DockerRootDir} {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// space is the reading an admission is held against: the volume with the
// least room left over the floor, of the ones this deployment writes to.
//
// The tightest rather than the sum, because these are separate
// filesystems and a job cannot spend one volume's free space on another.
// The whole deployment is held by whichever of them is closest to full,
// which is the same reading an operator takes of `df` when they are
// deciding whether the machine is in trouble.
//
// A volume that will not answer is reported and then passed over: this
// bounds how much work runs, and refusing every admission because a
// statfs failed would turn a monitoring failure into an outage. The
// zero DiskSpace it returns when nothing answers leaves disk unbounded,
// which is what a deployment that cannot see its own disk always had.
func (w *DiskWatch) Space() model.DiskSpace {
	if w == nil {
		return model.DiskSpace{}
	}
	tightest, unreadable := w.readVolumes()
	w.report(tightest, unreadable)
	return tightest
}

// read is the same reading with nothing said about it, for the reclaim,
// which measures itself between steps and should not be writing the
// journal's "held"/"admitting again" lines every time it does.
func (w *DiskWatch) read() model.DiskSpace {
	if w == nil {
		return model.DiskSpace{}
	}
	space, _ := w.readVolumes()
	return space
}

// readVolumes is the statfs pass itself: the tightest volume, and the first
// path that would not answer.
func (w *DiskWatch) readVolumes() (model.DiskSpace, string) {
	var tightest model.DiskSpace
	var headroom int
	seen := map[uint64]bool{}
	var unreadable string
	// One resolution of the floor for the whole pass, so the volumes are
	// compared against the same rule as each other even if a reconcile
	// tick changes it half way down the list.
	floor := w.Floor()
	for _, path := range w.paths {
		totalMB, usedMB, err := w.Usage(path)
		if err != nil {
			if unreadable == "" {
				unreadable = path + ": " + err.Error()
			}
			continue
		}
		if id, idErr := w.Identity(path); idErr == nil {
			if seen[id] {
				continue
			}
			seen[id] = true
		}
		space := model.DiskSpace{Path: path, TotalMB: totalMB, FreeMB: max(totalMB-usedMB, 0)}
		left := space.FreeMB - floor.FloorMB(space.TotalMB)
		if !tightest.Known() || left < headroom {
			tightest, headroom = space, left
		}
	}
	return tightest, unreadable
}

// report is the journal's side of the same reading: one line when the
// deployment starts holding work back, one when it stops, and a repeat
// no more often than diskHoldRepeat while it goes on holding.
//
// It is here rather than in pkg/model because it is a fact about this
// host at this moment, not about the rule: the rule's own refusal is
// already carried to whoever is waiting for the job (ci.JobRun.Activity,
// through model.BudgetVerdict.Reason). This is for the operator who is
// looking at a deployment that has gone quiet and has no job in front of
// them to read.
func (w *DiskWatch) report(space model.DiskSpace, unreadable string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if unreadable != "" && unreadable != w.unreadable {
		w.unreadable = unreadable
		w.Log("grain daemon: a volume this deployment writes to would not answer (%s) -- "+
			"the disk floor is applied to the ones that did", unreadable)
	} else if unreadable == "" {
		w.unreadable = ""
	}
	floor := w.Floor()
	verdict := floor.Admits(space)
	now := w.Now()
	switch {
	case !verdict.OK && (!w.held || now.Sub(w.said) >= DiskHoldRepeat):
		w.held, w.said = true, now
		w.Log("grain daemon: %s -- new CI jobs are held and agent dispatch deferred until space is freed. "+
			"A sandbox's disk is an overlay that grows as it is written, so this is what the runs have "+
			"written rather than what they were promised: this deployment is reclaiming what it can "+
			"(orphaned sandbox VMs, the CI job cache, kept artifacts) and says below what that got back",
			verdict.Reason)
		w.startReclaim()
	case verdict.OK && w.held:
		w.held = false
		w.Log("grain daemon: %s -- CI jobs and agent dispatch are being admitted again",
			floor.Describe(space))
	}
}

// startReclaim sets a sweep going against a volume that has just been
// found under its floor -- the crossing this file already logs being the
// one moment worth acting on, rather than a timer of its own asking a
// question the admissions are asking anyway (diskReclaim).
//
// In a goroutine, and that is the whole of why this is three lines
// rather than a call: report runs inside an admission check, on the
// path of a CI gate poll and a reconcile tick, and a sweep execs
// `granule vm delete` once per orphaned VM. A dispatch should not
// wait minutes for a reclaim that exists to make its next attempt
// succeed. diskReclaim.sweep is single-flighted and rate-limited, so
// the goroutines this starts while the deployment stays under its floor
// return immediately rather than piling sweeps on each other.
func (w *DiskWatch) startReclaim() {
	if w.Reclaim == nil {
		return
	}
	ctx := w.reclaimCtx
	if ctx == nil {
		ctx = context.Background()
	}
	go w.Reclaim.Sweep(ctx)
}

// reclaimWith gives this watcher what to do about a volume under its
// floor, and the context to do it under. Called once, from the daemon,
// after the things a sweep needs exist; a watcher nobody calls it on
// goes on holding work back and reclaiming nothing, which is where
// grain/task-196 left this.
func ReclaimWith(w *DiskWatch, ctx context.Context, reclaim *DiskReclaim) {
	if w == nil || reclaim == nil || len(reclaim.Steps) == 0 {
		return
	}
	reclaim.Read, reclaim.FloorOf = w.read, w.Floor
	if reclaim.Log == nil {
		reclaim.Log = w.Log
	}
	w.mu.Lock()
	w.Reclaim, w.reclaimCtx = reclaim, ctx
	w.mu.Unlock()
}

// diskSpaceOrNil is the same reading as orchestrator.Deps.DiskSpace
// wants it: the watcher's own method, or nil where there is no watcher,
// since a typed nil in that field would be called and would report a
// reading of nothing rather than no reading at all.
func DiskSpaceOrNil(w *DiskWatch) func() model.DiskSpace {
	if w == nil {
		return nil
	}
	return w.Space
}

// logDiskFloor says at startup what this deployment keeps free and what
// the volume it keeps it on has, for the same reason logSandboxBudget
// says what the memory budget is: a floor an operator has never seen a
// figure for is one they discover by having their jobs held.
func LogDiskFloor(w *DiskWatch) {
	if w == nil {
		log.Print("grain daemon: this deployment has no volume it can take a free-space reading " +
			"through, so nothing holds work back as its disk fills (model.DiskFloor)")
		return
	}
	// A floor of nothing is a setting rather than an absence now, and
	// one that can be turned back on without restarting this process --
	// so this says what it is and where it lives, rather than naming a
	// flag that no longer decides it (grain/task-372).
	if floor := w.Floor(); !floor.Known() {
		w.Log("grain daemon: no disk floor is configured, so nothing holds work back as this " +
			"deployment's volumes fill -- the disk it writes to is bounded by nothing at all. " +
			"Settings -> Sandbox ('Disk floor'), or 'grain settings -sandbox-disk-floor-share', " +
			"is where that is turned back on, and it binds the next admission rather than the " +
			"next restart")
		return
	}
	space := w.Space()
	// Naming every path it reads, not only the tightest one Describe
	// reports: which volumes this deployment is judged on is the half of
	// the rule an operator cannot infer from the figures, and since the
	// list is the host status pane's own (config.volumes) this line is
	// also where a docker data root nobody configured first appears as
	// something work is held against.
	// Through the watcher's own logger, which is log.Printf in a daemon and
	// a capture in a test -- this line is the only place that says which
	// volumes the floor is read through, so it is worth a test being able
	// to see it.
	w.Log("grain daemon: disk floor -- %s. Read through %s. A new CI job is held and an agent "+
		"dispatch deferred while free space is under it, because a sandbox's disk grows as it is "+
		"written rather than being taken out of the volume when the sandbox is made. Moved in "+
		"Settings -> Sandbox, and binding from the reconcile tick that reads it",
		w.Floor().Describe(space), strings.Join(w.paths, ", "))
}

// limitsWithDisk is how the live reading reaches the CI gate: the
// deployment's limits as they stand, with the volume as it stands now
// put into the budget those limits are held against.
//
// Wrapped here rather than inside liveConfig.limits because the reading
// is a property of this host rather than of the configuration, and
// because a caller with no watcher -- every test of that type, and a
// deployment with the floor turned off -- should get exactly the limits
// it always did.
func LimitsWithDisk(limits func() model.Limits, w *DiskWatch) func() model.Limits {
	if w == nil {
		return limits
	}
	return func() model.Limits {
		l := limits()
		l.Budget.Disk = w.Space()
		return l
	}
}
