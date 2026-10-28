package guestimage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPaths pins the guest half's paths as literals rather than
// re-deriving them from the constants, because their value is exactly
// that they don't change: an overlay records its backing files as paths, and
// an image assembled by one version of granule is opened by another. A
// change here is a compatibility break with every guest image already
// published, so this test is the place to notice one being made.
func TestPaths(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{Dir, "/var/lib/granule/guest"},
		{DiskImage, "/var/lib/granule/guest/disk.img"},
		{SnapshotDir, "/var/lib/granule/guest/snapshot"},
		{GuestKernel, "/var/lib/granule/guest/vmlinuz"},
		{GuestInitramfs, "/var/lib/granule/guest/initrd.img"},
		{RuntimeKernel, "/var/lib/granule/guest/vmlinux"},
		{Overlay, "/var/lib/granule/overlay/disk.qcow2"},
	} {
		if tc.got != tc.want {
			t.Errorf("path = %q, want %q", tc.got, tc.want)
		}
	}

	if !strings.HasSuffix(DiskImage, ".img") {
		t.Errorf("DiskImage = %q, want a .img suffix", DiskImage)
	}
	if !strings.HasSuffix(Overlay, ".qcow2") {
		t.Errorf("Overlay = %q, want a .qcow2 suffix", Overlay)
	}
}

// TestFilesExcludesRuntimeKernel: the list is what a copy of the guest
// half moves, and the runtime kernel is the VMM half's. A consumer's
// image carrying one pins a kernel the deployment has since replaced,
// which is the whole failure the split exists to prevent.
func TestFilesExcludesRuntimeKernel(t *testing.T) {
	files := Files()
	for _, f := range files {
		if f.Path == RuntimeKernel {
			t.Errorf("Files() lists %s, which belongs to the VMM half", RuntimeKernel)
		}
		if f.Description == "" {
			t.Errorf("Files() entry %s has no description", f.Path)
		}
		if f.Required && f.Path != DiskImage {
			t.Errorf("Files() marks %s required; only the root disk is", f.Path)
		}
	}
	if len(files) != 4 {
		t.Errorf("Files() returned %d entries, want the disk, its contract declaration and the kernel pair", len(files))
	}
}

func TestUnder(t *testing.T) {
	for _, tc := range []struct{ root, path, want string }{
		{"", DiskImage, DiskImage},
		{"/", DiskImage, DiskImage},
		{"/tmp/staging", DiskImage, "/tmp/staging/var/lib/granule/guest/disk.img"},
		{"rootfs", GuestKernel, "rootfs/var/lib/granule/guest/vmlinuz"},
	} {
		if got := Under(tc.root, tc.path); got != tc.want {
			t.Errorf("Under(%q, %q) = %q, want %q", tc.root, tc.path, got, tc.want)
		}
	}
}

func TestPresentAndCheck(t *testing.T) {
	for _, tc := range []struct {
		name          string
		files         []string
		dirs          []string
		wantDisk      bool
		wantKernel    bool
		wantErrSubstr string
	}{
		{
			name:          "empty",
			wantErrSubstr: "disk.img is missing",
		},
		{
			name:     "disk only, the ordinary build",
			files:    []string{DiskImage},
			wantDisk: true,
		},
		{
			name:     "extent disk manifest only",
			files:    []string{DiskExtentsManifest},
			wantDisk: true,
		},
		{
			name:       "disk and its own kernel pair",
			files:      []string{DiskImage, GuestKernel, GuestInitramfs},
			wantDisk:   true,
			wantKernel: true,
		},
		{
			name:          "kernel without its initramfs",
			files:         []string{DiskImage, GuestKernel},
			wantDisk:      true,
			wantErrSubstr: "initrd.img is missing",
		},
		{
			name:          "initramfs without its kernel",
			files:         []string{DiskImage, GuestInitramfs},
			wantDisk:      true,
			wantErrSubstr: "vmlinuz is missing",
		},
		{
			// A directory where a file belongs is not a guest granule can
			// boot, so it reads as absent rather than as present-but-odd.
			name:          "a directory in the disk's place",
			dirs:          []string{DiskImage},
			wantErrSubstr: "disk.img is missing",
		},
		{
			// The runtime kernel alone is the VMM half with no guest
			// under it: nothing to boot.
			name:          "runtime kernel only",
			files:         []string{RuntimeKernel},
			wantErrSubstr: "disk.img is missing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, d := range tc.dirs {
				if err := os.MkdirAll(Under(root, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range tc.files {
				path := Under(root, f)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			gotDisk, gotKernel := Present(root)
			if gotDisk != tc.wantDisk || gotKernel != tc.wantKernel {
				t.Errorf("Present() = (%v, %v), want (%v, %v)", gotDisk, gotKernel, tc.wantDisk, tc.wantKernel)
			}

			err := Check(root)
			if tc.wantErrSubstr == "" {
				if err != nil {
					t.Fatalf("Check() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Check() = nil, want an error mentioning %q", tc.wantErrSubstr)
			}
			if !strings.Contains(err.Error(), tc.wantErrSubstr) {
				t.Errorf("Check() error = %v, want it to mention %q", err, tc.wantErrSubstr)
			}
		})
	}
}
