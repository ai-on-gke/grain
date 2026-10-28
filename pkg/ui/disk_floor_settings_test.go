package ui_test

// grain/task-372: the free-space disk floor was two daemon flags and is
// a setting now, beside the sandbox budget's other three. These cover
// what that move has to be true for: it is stored and read back, a share
// that is not a share is refused rather than silently unbounding the one
// dimension that takes the store down with it, and the pane is told what
// the setting comes to on this deployment's own volumes -- because a
// share of a volume is not a quantity of disk until it meets one.

import (
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// floorVolumes is the deployment this floor was filed about: a 20 GB
// store disk and a 443 GB sandbox volume, whose figures the client
// resolves through Config.SandboxVolumes the way cmd/grain's diskUsage
// does for the host pane.
func floorVolumes(floor model.DiskFloor) []ui.DiskUsage {
	volumes := []ui.DiskUsage{
		{Holds: []string{"store"}, Path: "/var/lib/grain/data", UsedMB: 16 * 1024, TotalMB: 20 * 1024},
		{Holds: []string{"sandboxes", "docker"}, Path: "/var/lib/grain/scratch", UsedMB: 181 * 1024, TotalMB: 443 * 1024},
	}
	for i := range volumes {
		volumes[i].FloorMB = floor.FloorMB(volumes[i].TotalMB)
		volumes[i].ReclaimMB = floor.ReclaimTargetMB(volumes[i].TotalMB)
	}
	return volumes
}

func TestSettingsStoresTheDiskFloor(t *testing.T) {
	t.Parallel()
	c, ctx := settingsClientOnHost(t, budgetHost, nil)
	c.Config.SandboxVolumes = floorVolumes

	// The change an operator makes while a filling volume is holding
	// every dispatch: a twentieth of the volume rather than a tenth.
	share, minMB := 0.05, 2048
	got, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		SandboxDiskFloorShare: &share, SandboxDiskFloorMinMB: &minMB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SandboxDiskFloorShare != 0.05 || got.SandboxDiskFloorMinMB != 2048 {
		t.Errorf("saved floor = %v of each volume, never less than %d MiB, want 0.05/2048",
			got.SandboxDiskFloorShare, got.SandboxDiskFloorMinMB)
	}
	// And read back out of the row rather than only echoed by the save:
	// a setting the next process does not see is a flag with extra steps.
	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.SandboxDiskFloorShare != 0.05 || read.SandboxDiskFloorMinMB != 2048 {
		t.Errorf("stored floor reads back as %v/%d MiB, want what was saved",
			read.SandboxDiskFloorShare, read.SandboxDiskFloorMinMB)
	}
	if read.SandboxDiskFloorShareDefault != model.DefaultDiskFloorShare ||
		read.SandboxDiskFloorMinMBDefault != model.MinDiskFloorMB ||
		read.SandboxDiskFloorMaxShare != model.MaxDiskFloorShare {
		t.Errorf("reported floor constants = %v/%d capped at %v, want grain's own %v/%d capped at %v",
			read.SandboxDiskFloorShareDefault, read.SandboxDiskFloorMinMBDefault, read.SandboxDiskFloorMaxShare,
			model.DefaultDiskFloorShare, model.MinDiskFloorMB, model.MaxDiskFloorShare)
	}

	// And what that setting means on the volumes it is applied to, which
	// is the half of it a number in a box cannot say: a twentieth of the
	// 443 GB volume is 22.1 GiB, while the 20 GB store disk is held by
	// the 2 GiB minimum instead.
	if len(read.SandboxDiskFloors) != 2 {
		t.Fatalf("resolved floors = %+v, want one per volume", read.SandboxDiskFloors)
	}
	if got, want := read.SandboxDiskFloors[0].FloorMB, 2048; got != want {
		t.Errorf("the store disk's floor = %d MiB, want the minimum, %d", got, want)
	}
	sandboxVolumeMB := 443 * 1024
	if got, want := read.SandboxDiskFloors[1].FloorMB, int(0.05*float64(sandboxVolumeMB)); got != want {
		t.Errorf("the sandbox volume's floor = %d MiB, want a twentieth of it, %d", got, want)
	}
	// Resolved against the row being reported, so the figures under the
	// field are the ones just saved rather than the ones the daemon is
	// still a reconcile tick away from applying.
	if got.SandboxDiskFloors[1].FloorMB != read.SandboxDiskFloors[1].FloorMB {
		t.Errorf("the save reported %+v and the read %+v", got.SandboxDiskFloors[1], read.SandboxDiskFloors[1])
	}
}

// Zero is a floor an operator may deliberately want -- it is what every
// deployment did before the floor existed -- and a negative share is not
// a share at all. model.DiskFloor reads both as "hold nothing back", so
// the refusal has to happen here or nobody is ever told.
func TestSettingsRefusesADiskFloorShareThatIsNotAShare(t *testing.T) {
	t.Parallel()
	c, ctx := settingsClientOnHost(t, budgetHost, nil)
	off, none := 0.0, 0
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		SandboxDiskFloorShare: &off, SandboxDiskFloorMinMB: &none,
	}); err != nil {
		t.Fatalf("turning the floor off: %v -- holding nothing back is a choice, not an error", err)
	}
	for name, value := range map[string]float64{"negative": -0.1, "a percentage": 10} {
		if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxDiskFloorShare: &value}); err == nil {
			t.Errorf("a %s disk floor share (%v) was accepted, want it refused", name, value)
		}
	}
	negative := -1
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxDiskFloorMinMB: &negative}); err == nil {
		t.Error("a negative disk floor minimum was accepted, want it refused")
	}
}

// A share between the cap and 1 is stored as asked and capped by the
// arithmetic: the deployment keeps a quarter of each volume free rather
// than being wedged by the safety margin it just set. The pane can see
// that happen, which is what the resolved figures are for.
func TestADiskFloorLargerThanTheCapIsCappedRatherThanRefused(t *testing.T) {
	t.Parallel()
	c, ctx := settingsClientOnHost(t, budgetHost, nil)
	c.Config.SandboxVolumes = floorVolumes
	huge := 0.9
	got, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxDiskFloorShare: &huge})
	if err != nil {
		t.Fatalf("a floor of 90%% was refused: %v -- the cap is what makes it safe to accept", err)
	}
	if got.SandboxDiskFloorShare != 0.9 {
		t.Errorf("stored share = %v, want what was asked for, 0.9", got.SandboxDiskFloorShare)
	}
	sandboxVolumeMB := 443 * 1024
	capped := int(model.MaxDiskFloorShare * float64(sandboxVolumeMB))
	if figure := got.SandboxDiskFloors[1].FloorMB; figure != capped {
		t.Errorf("the sandbox volume's floor = %d MiB, want it capped at %d", figure, capped)
	}
}

// A UI with no host of this deployment's own to speak for still shows
// the setting; it just has nothing to resolve it against.
func TestTheDiskFloorIsReportedWithoutVolumesToResolveItAgainst(t *testing.T) {
	t.Parallel()
	c, ctx := settingsClientOnHost(t, budgetHost, nil)
	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.SandboxDiskFloors) != 0 {
		t.Errorf("resolved floors = %+v, want none where nothing can be asked", read.SandboxDiskFloors)
	}
	if read.SandboxDiskFloorShare == 0 && read.SandboxDiskFloorMinMB == 0 {
		t.Error("the stored floor was hidden along with the figures it could not be resolved into")
	}
}

// The refusal names the cap, since the number an operator wants next is
// "well, what is the most this can be".
func TestTheDiskFloorRefusalNamesTheCap(t *testing.T) {
	t.Parallel()
	c, ctx := settingsClientOnHost(t, budgetHost, nil)
	percent := 25.0
	_, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxDiskFloorShare: &percent})
	if err == nil {
		t.Fatal("a share of 25 (a percentage, not a share) was accepted")
	}
	if !strings.Contains(err.Error(), "25%") {
		t.Errorf("refusal = %q, want it naming the cap a share is held to", err)
	}
}
