package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/extent"
	"github.com/bwsalmon/granule/pkg/qcow2"
)

// splitDisk writes a 3 MiB disk with a marker in its second MiB and
// splits it into 1 MiB extents, returning the disk bytes and the
// extent directory.
func splitDisk(t *testing.T) ([]byte, string) {
	t.Helper()
	dir := t.TempDir()
	disk := make([]byte, 3<<20)
	copy(disk[1<<20:], "extent one marker")
	src := filepath.Join(dir, "disk.img")
	if err := os.WriteFile(src, disk, 0o644); err != nil {
		t.Fatal(err)
	}
	extents := filepath.Join(dir, "disk.extents")
	if _, err := extent.Split(src, extents, 1); err != nil {
		t.Fatal(err)
	}
	return disk, extents
}

func TestPrepareOverlayAssemblesAnExtentDisk(t *testing.T) {
	disk, extents := splitDisk(t)
	overlay := filepath.Join(t.TempDir(), "overlay", "disk.qcow2")
	cfg := Config{
		DiskMode:    DiskModeOverlay,
		OverlayPath: overlay,
		DiskSizeMB:  8,
		Disks:       []Disk{{Path: extents, NumQueues: 2}},
	}
	if err := cfg.PrepareOverlay(); err != nil {
		t.Fatalf("PrepareOverlay() error = %v", err)
	}
	if cfg.Disks[0].Path != overlay || cfg.Disks[0].NumQueues != 2 {
		t.Errorf("Disks[0] = %+v, want the assembled disk at %s keeping its parameters", cfg.Disks[0], overlay)
	}
	// What boots is a flat raw disk with no backing file, whatever the
	// path's suffix says.
	if qcow2.IsQcow2(overlay) {
		t.Error("the assembled disk is a qcow2 image, want raw")
	}
	got, err := os.ReadFile(overlay)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8<<20 || !bytes.Equal(got[:len(disk)], disk) {
		t.Errorf("assembled disk is %d bytes / content mismatch, want the source grown to 8 MiB", len(got))
	}

	// A restart keeps the guest's writes rather than reassembling.
	if err := os.WriteFile(overlay, []byte("written by the guest"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2 := Config{DiskMode: DiskModeOverlay, OverlayPath: overlay, Disks: []Disk{{Path: extents}}}
	if err := cfg2.PrepareOverlay(); err != nil {
		t.Fatalf("PrepareOverlay() second call error = %v", err)
	}
	if data, _ := os.ReadFile(overlay); string(data) != "written by the guest" {
		t.Error("PrepareOverlay() reassembled over an existing disk, discarding the guest's writes")
	}
}

func TestPrepareOverlayRejectsAnExtentDiskSmallerThanItsSource(t *testing.T) {
	_, extents := splitDisk(t)
	cfg := Config{
		DiskMode:    DiskModeOverlay,
		OverlayPath: filepath.Join(t.TempDir(), "disk.qcow2"),
		DiskSizeMB:  2,
		Disks:       []Disk{{Path: extents}},
	}
	if err := cfg.PrepareOverlay(); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Errorf("PrepareOverlay() = %v, want a truncation refusal", err)
	}
}

func TestPrepareOverlayRefusesExtentDisksOutsideOverlayMode(t *testing.T) {
	_, extents := splitDisk(t)
	for _, mode := range []string{DiskModePersistent, DiskModeReadOnly} {
		cfg := Config{DiskMode: mode, Disks: []Disk{{Path: extents}}}
		if err := cfg.PrepareOverlay(); err == nil || !strings.Contains(err.Error(), "extent directory") {
			t.Errorf("%s: PrepareOverlay() = %v, want an extent-directory refusal", mode, err)
		}
	}
}
