package daemon

// Which filesystems the host status pane reports, and how the readings
// for them are folded together (grain/task-148).
//
// The pane used to show one "disk" figure, taken from -data-dir's own
// filesystem, on the assumption that everything a run writes lands
// there. It stopped being true once terraform/gcp gave sandboxes a
// volume of their own (task 134, #635): docker's data root -- the
// sandbox image and every granule VM's qcow2 overlay -- and
// HostSandboxes' per-run checkouts moved onto a 100 GB disk the UI never
// looked at, while the store's 20 GB disk went on reading as healthy
// however full the other one got.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

func TestHostDisksReportsTheStoreTheSandboxRootAndDockersOwn(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("hostDisks only ever has a docker data root to check on Linux")
	}

	dataDir, sandboxDir, dockerRoot := t.TempDir(), t.TempDir(), t.TempDir()
	disks := hostDisks(Config{DataDir: dataDir, SandboxDir: sandboxDir, DockerRootDir: dockerRoot})

	want := []HostDisk{
		{Holds: "store", Path: dataDir},
		{Holds: "sandboxes", Path: sandboxDir},
		{Holds: "docker", Path: dockerRoot},
	}
	if len(disks) != len(want) {
		t.Fatalf("hostDisks = %v, want %v", disks, want)
	}
	for i, w := range want {
		if disks[i] != w {
			t.Errorf("hostDisks[%d] = %v, want %v", i, disks[i], w)
		}
	}
}

// A granule deployment keeps no checkout on this host, so -sandbox-dir is
// allowed to be empty (daemon()'s own argument checks) -- and an empty
// path is not a filesystem to statfs.
func TestHostDisksOmitsAnUnsetSandboxDir(t *testing.T) {
	t.Parallel()
	disks := hostDisks(Config{DataDir: t.TempDir(), DockerRootDir: filepath.Join(t.TempDir(), "no-such-dir")})

	if len(disks) != 1 || disks[0].Holds != "store" {
		t.Fatalf("hostDisks = %v, want the store's disk alone", disks)
	}
}

// Dropped rather than carried as a broken row: a containerised daemon is
// shown docker's socket and not the tree behind it, so dockerd names a
// path this process cannot stat -- and on terraform/gcp that path is the
// sandbox volume the entry above already reports through its bind mount,
// so nothing is lost by leaving it out.
func TestHostDisksOmitsADockerRootItCannotRead(t *testing.T) {
	t.Parallel()
	dataDir, sandboxDir := t.TempDir(), t.TempDir()
	disks := hostDisks(Config{DataDir: dataDir, SandboxDir: sandboxDir, DockerRootDir: "/mnt/grain-sandbox/docker-that-is-not-mounted-here"})

	for _, d := range disks {
		if d.Holds == "docker" {
			t.Fatalf("hostDisks = %v, want no entry for a docker root this process cannot stat", disks)
		}
	}
	if len(disks) != 2 {
		t.Fatalf("hostDisks = %v, want the store and the sandbox root", disks)
	}
}

// Resolved once per process and read from there by both halves that want
// it -- the pane's rows and the disk floor (grain/task-214). Asking again
// would cost a second `docker info`, and a list resolved twice is two
// answers to "which volumes does this deployment write to".
func TestHostVolumesPrefersTheListThisProcessAlreadyResolved(t *testing.T) {
	t.Parallel()
	resolved := []HostDisk{{Holds: "store", Path: "/var/lib/grain/data"}, {Holds: "docker", Path: "/mnt/sandbox/docker"}}
	cfg := Config{DataDir: t.TempDir(), Volumes: resolved}

	got := cfg.hostVolumes()
	if len(got) != len(resolved) || got[0] != resolved[0] || got[1] != resolved[1] {
		t.Errorf("hostVolumes = %v, want the list run() resolved %v", got, resolved)
	}
	// And a config that was never given one still resolves what it can,
	// rather than reporting a host with no disks at all.
	if bare := (Config{DataDir: cfg.DataDir, DockerRootDir: filepath.Join(t.TempDir(), "no-such-dir")}).hostVolumes(); len(bare) != 1 {
		t.Errorf("hostVolumes on an unresolved config = %v, want the store's disk", bare)
	}
}

// The single-disk case: a developer's laptop, and any deployment without
// terraform/gcp's own separate sandbox volume. Three paths, one
// filesystem, one row -- naming all three, rather than three identical
// rows that would each claim to be a disk of its own.
func TestDiskUsageFoldsPathsThatShareAFilesystem(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("sysstat.DiskUsage only reads a filesystem on Linux and macOS")
	}

	root := t.TempDir()
	sandboxes := filepath.Join(root, "sandboxes")
	docker := filepath.Join(root, "docker")
	for _, dir := range []string{sandboxes, docker} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	got := diskUsage([]HostDisk{
		{Holds: "store", Path: root},
		{Holds: "sandboxes", Path: sandboxes},
		{Holds: "docker", Path: docker},
	}, model.DefaultDiskFloor())

	if len(got) != 1 {
		t.Fatalf("diskUsage = %v, want one entry for one filesystem", got)
	}
	if strings.Join(got[0].Holds, ",") != "store,sandboxes,docker" {
		t.Errorf("Holds = %v, want every path that shares the filesystem, in the order asked for", got[0].Holds)
	}
	// The first path onto a filesystem is the one shown, so the row reads
	// as the same thing an operator would run `df` against.
	if got[0].Path != root {
		t.Errorf("Path = %q, want %q", got[0].Path, root)
	}
	if got[0].TotalMB <= 0 || got[0].UsedMB > got[0].TotalMB {
		t.Errorf("usage = %d/%d MB, want a real df reading", got[0].UsedMB, got[0].TotalMB)
	}
	if got[0].Error != "" {
		t.Errorf("Error = %q, want none", got[0].Error)
	}
}

// One unreadable path costs its own row a figure and nothing else. The
// case this is really about is docker's data root on a containerised
// daemon: dockerd reports a host path (/mnt/grain-sandbox/docker) that
// scripts/setup.sh does not mount into the container, so statfs fails
// there while the store's and the sandbox root's readings are both fine.
func TestDiskUsageCarriesOneUnreadablePathWithoutLosingTheRest(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("sysstat.DiskUsage only reads a filesystem on Linux and macOS")
	}

	root := t.TempDir()
	missing := filepath.Join(root, "no-such-dir")

	got := diskUsage([]HostDisk{
		{Holds: "store", Path: root},
		{Holds: "docker", Path: missing},
	}, model.DefaultDiskFloor())

	if len(got) != 2 {
		t.Fatalf("diskUsage = %v, want an entry each", got)
	}
	if got[0].TotalMB <= 0 {
		t.Errorf("store TotalMB = %d, want a real df reading", got[0].TotalMB)
	}
	if got[1].Error == "" {
		t.Errorf("docker entry = %v, want the reason it has no figure", got[1])
	}
	if got[1].TotalMB != 0 || got[1].UsedMB != 0 {
		t.Errorf("docker usage = %d/%d MB, want 0/0 -- the pane's own \"no figure\"", got[1].UsedMB, got[1].TotalMB)
	}
	// Not folded into the store's row: an unreadable path has no
	// filesystem identity to compare, and claiming it shares one would
	// put a figure next to a path this process cannot even see.
	if len(got[1].Holds) != 1 || got[1].Holds[0] != "docker" {
		t.Errorf("docker Holds = %v, want just its own", got[1].Holds)
	}
}

// The floor travels with the reading, resolved against the volume it
// applies to (grain/task-214). The pane draws it beside the usage
// because a bar says what is eating the disk and says nothing about how
// much of the rest a job can have: a new CI job is held and an agent
// dispatch deferred below this figure, which is how a deployment with
// tens of gigabytes free goes quiet and looks healthy doing it.
//
// Resolved here rather than sent as a share for the pane to apply: the
// arithmetic has a minimum and a cap in it (model.DiskFloor.FloorMB), and
// a second implementation of it in JavaScript would be a second answer to
// "why is this deployment holding work".
func TestDiskUsageCarriesTheFloorEachVolumeIsHeldTo(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("sysstat.DiskUsage only reads a filesystem on Linux and macOS")
	}

	root := t.TempDir()
	floor := model.DefaultDiskFloor()
	got := diskUsage([]HostDisk{{Holds: "store", Path: root}}, floor)

	if len(got) != 1 {
		t.Fatalf("diskUsage = %v, want one entry", got)
	}
	want := floor.FloorMB(got[0].TotalMB)
	if want <= 0 {
		t.Fatalf("a %d MB volume resolved to no floor at all", got[0].TotalMB)
	}
	if got[0].FloorMB != want {
		t.Errorf("FloorMB = %d, want %d -- the floor this deployment holds work at on this volume", got[0].FloorMB, want)
	}
	// And the margin a sweep of this volume aims for, which is what the
	// pane's quieter level is drawn at: a volume between the two is
	// admitting every job and is close enough to say so before it stops.
	if wantTarget := floor.ReclaimTargetMB(got[0].TotalMB); got[0].ReclaimMB != wantTarget {
		t.Errorf("ReclaimMB = %d, want %d -- what a reclaim of this volume sweeps back to", got[0].ReclaimMB, wantTarget)
	}
	if got[0].ReclaimMB <= got[0].FloorMB {
		t.Errorf("ReclaimMB %d is not above FloorMB %d, so the pane has no caution level to draw",
			got[0].ReclaimMB, got[0].FloorMB)
	}
}

// A deployment that holds nothing back as its volume fills says so with
// a zero rather than with a floor it is not applying: the pane reports
// "nothing holds work back as they fill", which is the state
// grain/task-196 was filed about and is worth saying out loud.
func TestDiskUsageReportsNoFloorWhereNoneIsConfigured(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("sysstat.DiskUsage only reads a filesystem on Linux and macOS")
	}

	got := diskUsage([]HostDisk{{Holds: "store", Path: t.TempDir()}}, model.DiskFloor{})
	if len(got) != 1 || got[0].FloorMB != 0 || got[0].ReclaimMB != 0 {
		t.Errorf("diskUsage = %v, want a reading with no floor on it and nothing to sweep for", got)
	}
}
