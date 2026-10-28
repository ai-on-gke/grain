package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// diskReclaim is what stands behind the disk floor (grain/task-213):
// what this deployment does about its own volume when diskWatch reports
// it under the floor and starts holding work.
//
// The floor is worth what stands behind it. Without this, a deployment
// that crosses it stops admitting work until a person notices -- which
// is the trade grain/task-196 made deliberately, and the better half of
// it is that a full disk is now a quiet deployment rather than a dead
// store. But most of what crosses that floor is reclaimable without
// anyone being woken, and the measurement behind these three steps says
// which of it:
//
//   - Sandbox overlays are the only thing on the volume big enough to
//     have filled it -- 60-80 GB apiece for a CI sandbox. They are
//     reclaimed correctly on release: a VM's writable root is a qcow2
//     overlay inside its own container (bwsalmon/granule#37), the
//     container declares no volume for it, and `granule vm delete`
//     force-removes both of a VM's containers, so the overlay goes with
//     the container's writable layer. What is left behind is exactly
//     what a delete that never ran left: a daemon killed mid-run, a
//     `granule vm delete` that failed. Those used to wait for the next
//     restart (ReapOrphans); SweepOrphans is the same pass while the
//     daemon is up, and it is the only step here that can return tens of
//     gigabytes.
//   - The CI job cache and the CI artifact store are both already
//     bounded (model.Config.CICacheMB at 256 MiB, CIArtifactMB at
//     2 GiB), and 2.25 GiB together is not what filled a 443 GB volume.
//     They are here because sweeping them is nearly free and the floor is
//     a better trigger than the bound: the bound asks "is this cache too
//     big", and the question being asked here is "is this volume too
//     full", which a cache well inside its own bound can still be part
//     of the answer to.
//
// The off switch is the floor itself. newDiskWatch returns nothing for a
// deployment that configured no floor, so nothing is wired here either:
// a deployment that holds no work back over its volume has nothing this
// could be reclaiming *for*, and sweeping one would be deleting against
// a threshold nobody set.
//
// Ordered by what each step costs if it turns out not to have been
// needed, cheapest first: an orphaned VM costs nothing at all, since
// nothing can reach it; the job cache costs re-runs, which is what
// `grain ci cache clear` has always cost; a failing job's artifacts are
// the only copy of the evidence and nothing will make them again. The
// sweep stops at the first step that gets the volume over its target, so
// on an ordinary crossing the last step never runs.
type DiskReclaim struct {
	Steps []ReclaimStep
	// read is a reading of the volume, taken again between steps --
	// diskWatch.read rather than diskWatch.space, so a sweep does not
	// write the journal's "held"/"admitting again" lines as a side
	// effect of measuring itself.
	Read func() model.DiskSpace
	// floorOf is the floor a sweep measures itself against, asked for
	// per sweep rather than held: it is a stored setting an operator
	// moves while the daemon runs, and the deployment's own diskWatch is
	// what answers it (diskWatch.floor, grain/task-372). nil holds
	// nothing back, which is what floor() below returns for it.
	FloorOf func() model.DiskFloor
	Log     func(format string, args ...any)
	Now     func() time.Time

	Mu sync.Mutex
	// running is the single flight: one sweep at a time, whatever asks.
	// The two callers are a gate poll and a reconcile tick, both of which
	// go on arriving while a sweep is deleting VMs.
	Running bool
	// last is when a sweep last started, so a deployment that stays
	// under its floor sweeps occasionally rather than continuously. See
	// begin for why the start and not the finish.
	Last time.Time
}

// reclaimStep is one thing a deployment can give back, named as the
// journal should say it.
type ReclaimStep struct {
	// what is the subject of the sentence "reclaimed ...", in the words
	// an operator would use for it.
	What string
	// run frees what it can and says what it did, in the same words. An
	// empty detail means there was nothing of this kind to reclaim,
	// which is the ordinary answer for two of the three steps and is not
	// worth a line in the journal.
	Run func(ctx context.Context) (detail string, err error)
}

// orphanSweeper is a sandbox backend that can tell a VM nothing is
// using from one a run is in, and delete the first kind
// (orchestrator.GranuleSandboxes.SweepOrphans).
//
// An interface rather than a concrete type for the reason every other
// optional capability of a backend is one: a deployment sandboxing on
// this host with plain directories (orchestrator.HostSandboxes) has no
// VMs to sweep and is not a deployment whose disk this fills. A
// Kubernetes backend answers it too (K8sSandboxes.SweepOrphans), for
// reconcileSandboxes' sake; its pods are not this host's disk, but
// sweeping them here costs nothing and frees cluster capacity all the same.
type orphanSweeper interface {
	SweepOrphans(ctx context.Context) (int, error)
}

// NewDiskReclaim returns the reclaim steps in the order the doc comment above
// gives them: what each costs if it turns out not to have been needed,
// cheapest first.
//
// The CI steps reach the running service through liveCI rather than
// through a service handed in here, because there is no service yet
// when the daemon wires this -- one is built on the first reconcile tick
// and rebuilt whenever the settings behind it change. ciGate is how the
// rest of the process asks the same question, and it answers "there is
// no cache/store" on a daemon whose service is not up, which is exactly
// what a sweep in that first second should be told.
func NewDiskReclaim(sandboxes orchestrator.Sandboxes, docker ...*DockerPruner) *DiskReclaim {
	var steps []ReclaimStep
	if sweeper, ok := sandboxes.(orphanSweeper); ok {
		steps = append(steps, ReclaimStep{
			What: "sweeping orphaned sandbox VMs",
			Run: func(ctx context.Context) (string, error) {
				swept, err := sweeper.SweepOrphans(ctx)
				if swept == 0 {
					return "", err
				}
				return fmt.Sprintf("%d orphaned sandbox VM(s) and the overlay each was holding", swept), err
			},
		})
	}
	var pruner *DockerPruner
	if len(docker) > 0 && docker[0] != nil {
		pruner = docker[0]
	} else {
		pruner = &DockerPruner{}
	}
	steps = append(steps,
		ReclaimStep{
			What: "pruning unused Docker images and build cache",
			Run:  pruner.Prune,
		},
		ReclaimStep{
			What: "clearing the CI job cache",
			Run: func(ctx context.Context) (string, error) {
				removed, ok, err := CiGate{}.ClearCache(ctx)
				if !ok || removed == 0 {
					return "", err
				}
				return fmt.Sprintf("%d cached job result(s), which cost re-runs and nothing else", removed), err
			},
		},
		ReclaimStep{
			What: "clearing the CI artifacts failing jobs left behind",
			Run: func(ctx context.Context) (string, error) {
				removed, freed, ok, err := CiGate{}.ClearArtifacts(ctx)
				if !ok || removed == 0 {
					return "", err
				}
				return fmt.Sprintf("%d artifact(s) of failing jobs, %s, which nothing will make again",
					removed, reclaimedBytes(freed)), err
			},
		},
	)
	return &DiskReclaim{Steps: steps}
}

// diskReclaimInterval is how long a deployment that stays under its
// floor waits before sweeping again.
//
// The same ten minutes diskHoldRepeat gives the journal, and for a
// related reason: a sweep that found nothing will find nothing again a
// second later, and one that found something has already had its effect.
// What makes a second sweep worth running at all is that the deployment
// has gone on working in between -- runs have ended, VMs have been
// released -- so this is paced by how fast that happens rather than by
// how fast the disk is read.
const DiskReclaimInterval = 10 * time.Minute

// diskReclaimSlack is how far short of diskReclaimInterval a sweep may
// still start. The caller that paces the retries is diskWatch.report,
// which starts one each time its held line repeats -- diskHoldRepeat,
// measured on its own clock reading, taken before the sweep's goroutine
// is scheduled and stamps this one. Without the slack a repeat that lands
// exactly on the interval could find the previous sweep's stamp a few
// microseconds too recent and be refused, which puts the next sweep a
// whole repeat later. A minute is far below the interval, so the pacing
// still holds for every caller that arrives early by more than scheduling
// noise.
const diskReclaimSlack = time.Minute

// diskReclaimTimeout bounds one whole sweep. Generous because the step
// that matters execs `granule vm delete` once per orphan and each of
// those stops a VM (bwsalmon/granule's own terminationGracePeriod), and
// bounded at all because this runs on a timer with nobody watching it:
// a wedged delete should cost this deployment one sweep, not a goroutine
// that never comes back.
const diskReclaimTimeout = 5 * time.Minute

// sweep frees what it can, and stops as soon as the volume is over the
// target rather than at the floor itself.
//
// It returns without doing anything when there is nothing to do: no
// steps wired, no reading to judge, a volume already over its target, or
// a sweep that ran within diskReclaimInterval. It is safe to call from
// anywhere, as often as anything likes.
//
// The figures in the journal are the volume's own readings before and
// after, not a sum of what the steps removed, and that is deliberate:
// the runs that filled the volume are still writing to it while this
// sweeps, so what an operator needs to know is where the volume ended up
// rather than how much this deleted. The two differ, sometimes by more
// than this reclaimed -- which is itself the answer to "why is it still
// holding work back".
func (r *DiskReclaim) Sweep(ctx context.Context) {
	if r == nil || len(r.Steps) == 0 {
		return
	}
	// One resolution of the floor for the whole sweep: the steps below
	// take minutes, and a sweep that measured itself against a floor
	// that moved half way through would log a target it never aimed at.
	// The next crossing reads the new one.
	floor := r.Floor()
	before := r.Read()
	if !floor.NeedsReclaim(before) {
		return
	}
	if !r.begin() {
		return
	}
	defer r.end()

	ctx, cancel := context.WithTimeout(ctx, diskReclaimTimeout)
	defer cancel()

	target := floor.ReclaimTargetMB(before.TotalMB)
	r.logf("grain daemon: reclaiming disk -- %s GiB is free on %s and this deployment is holding work "+
		"below %s GiB, so it is sweeping back to %s GiB",
		GibMB(before.FreeMB), before.Path, GibMB(floor.FloorMB(before.TotalMB)), GibMB(target))

	var (
		done  []string
		after = before
	)
	for _, step := range r.Steps {
		if ctx.Err() != nil {
			break
		}
		detail, err := step.Run(ctx)
		if err != nil {
			// Reported and stepped over, for the reason every sweep in
			// this repository steps over its own failures: a reclaim that
			// gave up on the first error would leave the space the later
			// steps could have freed, and the deployment is already in
			// the state where that matters.
			r.logf("grain daemon: reclaiming disk: %s: %v", step.What, err)
		}
		if detail == "" {
			continue
		}
		done = append(done, detail)
		after = r.Read()
		if !floor.NeedsReclaim(after) {
			break
		}
	}

	switch {
	case len(done) == 0:
		r.logf("grain daemon: reclaiming disk found nothing to reclaim on %s -- no orphaned sandbox VM, "+
			"no cached job result and no kept artifact. %s GiB of the %s GiB on it is being written by "+
			"work that is still going, so this deployment goes on holding new work until that finishes "+
			"or somebody lowers the disk floor in Settings -> Sandbox, which binds the next admission "+
			"rather than the next restart (grain/task-372)",
			before.Path, GibMB(before.UsedMB()), GibMB(before.TotalMB))
	case !floor.NeedsReclaim(after):
		r.logf("grain daemon: reclaimed %s -- %s GiB free on %s, up from %s GiB, which is over the %s GiB "+
			"this deployment sweeps back to. CI jobs and agent dispatch are admitted again",
			strings.Join(done, ", "), GibMB(after.FreeMB), after.Path, GibMB(before.FreeMB), GibMB(target))
	default:
		r.logf("grain daemon: reclaimed %s -- %s GiB free on %s, up from %s GiB, and still short of the "+
			"%s GiB this deployment sweeps back to. What is left is being written by work that is still "+
			"going; another sweep follows in %s if it is still short then",
			strings.Join(done, ", "), GibMB(after.FreeMB), after.Path, GibMB(before.FreeMB),
			GibMB(target), DiskReclaimInterval)
	}
}

// floor is the rule this sweep measures itself against right now, and
// the zero floor -- which has nothing to sweep for -- where nobody wired
// one in. See floorOf.
func (r *DiskReclaim) Floor() model.DiskFloor {
	if r == nil || r.FloorOf == nil {
		return model.DiskFloor{}
	}
	return r.FloorOf()
}

// begin claims the single flight and the interval together: true means
// this caller is the one sweeping, and end releases it.
//
// The interval is measured from the start of the previous sweep, not its
// end (grain/task-1472). The retries are paced from outside, by the held
// line diskWatch repeats every diskHoldRepeat -- the same ten minutes --
// so a stamp taken when a sweep finished made every retry that followed
// a sweep that took any time at all arrive too early and be refused, and
// a deployment that stayed under its floor was swept every twenty minutes
// while the journal promised ten.
func (r *DiskReclaim) begin() bool {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	now := r.clock()
	if r.Running || (!r.Last.IsZero() && now.Sub(r.Last) < DiskReclaimInterval-diskReclaimSlack) {
		return false
	}
	r.Running, r.Last = true, now
	return true
}

func (r *DiskReclaim) end() {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	r.Running = false
}

func (r *DiskReclaim) clock() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *DiskReclaim) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

// gibMB is pkg/model's own rendering of a figure in MiB, repeated here
// because the journal lines above and the refusals a held job carries
// should read in the same units.
func GibMB(mb int) string { return fmt.Sprintf("%.1f", float64(mb)/1024) }

// reclaimedBytes renders one step's own figure, which is in bytes and is
// usually much smaller than the volume's: an artifact store is bounded
// at 2 GiB, so rendering it in the GiB the volume is measured in would
// report most sweeps as having reclaimed "0.0".
func reclaimedBytes(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%d MiB", n>>20)
}

// DockerPruner reclaims unused Docker images, superseded sandbox guest and
// daemon image tags, and unused BuildKit build cache on the host Docker daemon.
type DockerPruner struct {
	// ActiveSandboxImage is the sandbox guest image reference currently
	// configured for new sandboxes (defaults to DefaultSandboxImage).
	ActiveSandboxImage string
	// ActiveGrainImage is the grain daemon image reference to preserve
	// (defaults to DefaultGrainImage).
	ActiveGrainImage string
	// ExtraKeep lists any additional image references that must not be removed.
	ExtraKeep []string
	// KeepImages, when set, lists more references to keep, asked at each
	// prune: the images agent CLIs are set to run in (clirun.go), which a
	// Settings save can change while the daemon runs.
	KeepImages func() []string
	// Docker is the docker CLI binary name or path (defaults to "docker").
	Docker string
	// Run executes a docker CLI command and returns its combined output.
	// When nil, Prune invokes the Docker binary with an explicit client environment.
	Run func(ctx context.Context, args ...string) (string, error)
}

// Prune removes old tags and untagged digests in the sandbox guest and grain
// image repositories (preserving the active sandbox image, active grain image,
// :previous tags, and any image used by a running container), then prunes
// dangling images and unused BuildKit build cache.
func (p *DockerPruner) Prune(ctx context.Context) (string, error) {
	if p == nil {
		p = &DockerPruner{}
	}
	activeSandbox := strings.TrimSpace(p.ActiveSandboxImage)
	if activeSandbox == "" {
		activeSandbox = DefaultSandboxImage
	}
	activeGrain := strings.TrimSpace(p.ActiveGrainImage)
	if activeGrain == "" {
		activeGrain = DefaultGrainImage
	}

	psOut, psErr := p.run(ctx, "ps", "--format", "{{.Image}}")
	if psErr != nil && isDockerUnavailable(psOut, psErr) {
		return "", nil
	}

	var removed []string
	if psErr == nil {
		keep := make(map[string]bool)
		for _, ref := range []string{activeSandbox, activeGrain, DefaultSandboxImage, DefaultGrainImage} {
			for _, name := range reclaimImageNames(ref) {
				keep[name] = true
			}
		}
		extra := p.ExtraKeep
		if p.KeepImages != nil {
			extra = append(slices.Clone(extra), p.KeepImages()...)
		}
		for _, ref := range extra {
			for _, name := range reclaimImageNames(ref) {
				keep[name] = true
			}
		}
		for _, line := range strings.Split(psOut, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			keep[line] = true
			for _, name := range reclaimImageNames(line) {
				keep[name] = true
			}
		}

		var repos []string
		seenRepo := make(map[string]bool)
		for _, ref := range []string{activeSandbox, activeGrain, DefaultSandboxImage, DefaultGrainImage} {
			repo := strings.TrimSpace(imageRepository(ref))
			if repo != "" && !seenRepo[repo] {
				seenRepo[repo] = true
				repos = append(repos, repo)
			}
		}

		for _, repo := range repos {
			keep[repo+":previous"] = true
			lsOut, err := p.run(ctx, "image", "ls", "--format", "{{.Repository}}:{{.Tag}} {{.Repository}}@{{.Digest}} {{.ID}}", repo)
			if err != nil {
				continue
			}
			for _, line := range strings.Split(lsOut, "\n") {
				names := strings.Fields(line)
				if len(names) == 0 {
					continue
				}
				img := reclaimRemovableImage(repo, names)
				if img == "" {
					continue
				}
				kept := false
				for _, name := range names {
					if keep[name] {
						kept = true
						break
					}
				}
				if kept {
					continue
				}
				if _, err := p.run(ctx, "image", "rm", img); err == nil {
					removed = append(removed, img)
				}
			}
		}
	}

	var (
		freed     int64
		prunedAny bool
	)
	imgOut, imgErr := p.run(ctx, "image", "prune", "-f")
	if imgErr == nil {
		reclaimed := parseDockerReclaimedBytes(imgOut)
		freed += reclaimed
		if reclaimed > 0 || strings.Contains(imgOut, "Deleted") {
			prunedAny = true
		}
	}
	buildOut, buildErr := p.run(ctx, "builder", "prune", "-f")
	if buildErr == nil {
		reclaimed := parseDockerReclaimedBytes(buildOut)
		freed += reclaimed
		if reclaimed > 0 || strings.Contains(buildOut, "Deleted") {
			prunedAny = true
		}
	}

	if psErr != nil && imgErr != nil && buildErr != nil {
		if isDockerUnavailable(imgOut, imgErr) || isDockerUnavailable(buildOut, buildErr) {
			return "", nil
		}
		return "", psErr
	}

	var parts []string
	if len(removed) > 0 {
		parts = append(parts, fmt.Sprintf("%d unused Docker image(s) (%s)", len(removed), strings.Join(removed, ", ")))
	}
	if freed > 0 {
		parts = append(parts, fmt.Sprintf("%s of dangling Docker images and build cache", reclaimedBytes(freed)))
	} else if prunedAny && len(removed) == 0 {
		parts = append(parts, "dangling Docker images and build cache")
	}
	return strings.Join(parts, " and "), nil
}

func (p *DockerPruner) run(ctx context.Context, args ...string) (string, error) {
	if p.Run != nil {
		return p.Run(ctx, args...)
	}
	bin := p.Docker
	if bin == "" {
		bin = "docker"
	}
	cmd := dockerReclaimCommand(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func dockerReclaimCommand(ctx context.Context, bin string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = dockerReclaimClientEnv()
	return cmd
}

func dockerReclaimClientEnv() []string {
	var env []string
	for _, name := range []string{
		"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG",
		"DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY",
	} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func isDockerUnavailable(out string, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return true
	}
	lower := strings.ToLower(out + " " + err.Error())
	for _, marker := range []string{
		"cannot connect to the docker daemon",
		"is the docker daemon running",
		"error during connect",
		"executable file not found",
		"no such file or directory",
		"permission denied while trying to connect to the docker daemon socket",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// reclaimImageNames returns all normalized spellings of ref so a keep check
// matches by tag, by digest, or by combined tag@digest.
func reclaimImageNames(ref string) []string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	names := []string{ref}
	repo := imageRepository(ref)
	at := strings.LastIndex(ref, "@")
	if at >= 0 && repo != "" {
		names = append(names, repo+ref[at:])
		withoutDigest := ref[:at]
		if withoutDigest != "" && withoutDigest != repo {
			names = append(names, withoutDigest)
		}
	}
	if at < 0 && repo == ref {
		names = append(names, repo+":latest")
	}
	return names
}

// reclaimRemovableImage returns the image reference (tag or digest) to pass to
// `docker image rm` for an entry from `docker image ls`, or "" if the entry
// does not belong to repo or has neither a valid tag nor a valid digest.
func reclaimRemovableImage(repo string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	first := names[0]
	if strings.HasPrefix(first, repo+":") && !strings.HasSuffix(first, ":<none>") {
		return first
	}
	if first == repo+":<none>" || strings.HasPrefix(first, repo+"@") {
		for _, name := range names {
			if strings.HasPrefix(name, repo+"@") && name != repo+"@" && !strings.HasSuffix(name, "@<none>") {
				return name
			}
		}
	}
	return ""
}

// parseDockerReclaimedBytes extracts the byte count reported by a Docker prune
// command's "Total reclaimed space: <value><unit>" footer.
func parseDockerReclaimedBytes(out string) int64 {
	const prefix = "total reclaimed space:"
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(trimmed), prefix) {
			continue
		}
		rest := strings.TrimSpace(trimmed[len(prefix):])
		rest = strings.ReplaceAll(rest, " ", "")
		if rest == "" {
			return 0
		}
		numEnd := 0
		for numEnd < len(rest) && ((rest[numEnd] >= '0' && rest[numEnd] <= '9') || rest[numEnd] == '.') {
			numEnd++
		}
		if numEnd == 0 {
			return 0
		}
		val, err := strconv.ParseFloat(rest[:numEnd], 64)
		if err != nil || val <= 0 {
			return 0
		}
		unit := strings.ToUpper(strings.TrimSpace(rest[numEnd:]))
		var mult float64
		switch unit {
		case "B", "":
			mult = 1
		case "KB":
			mult = 1e3
		case "KIB":
			mult = 1 << 10
		case "MB":
			mult = 1e6
		case "MIB":
			mult = 1 << 20
		case "GB":
			mult = 1e9
		case "GIB":
			mult = 1 << 30
		case "TB":
			mult = 1e12
		case "TIB":
			mult = 1 << 40
		default:
			return 0
		}
		return int64(val * mult)
	}
	return 0
}
