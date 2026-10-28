package qcow2

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// patched writes a fresh 1MiB qcow2 to a temporary directory and then
// overwrites the header bytes at off with b: a header as a hostile file
// would carry it, around an image that is otherwise well-formed.
func patched(t *testing.T, opts []OverlayOption, off int64, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := Create(p, 1<<20, opts...); err != nil {
		t.Fatalf("Create: %v", err)
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt(b, off); err != nil {
		t.Fatal(err)
	}
	return p
}

func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func be64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

// Every length and offset ReadHeader trusts comes out of the file, so each
// is bounded before it sizes an allocation or a read.
func TestReadHeaderRefusesHostileHeaders(t *testing.T) {
	small := []OverlayOption{WithClusterBits(16)}
	for _, tc := range []struct {
		name string
		off  int64
		b    []byte
		want string
	}{
		{"backing file name of 4GiB", 8, append(be64(512), be32(0xffffffff)...), "backing file name"},
		{"backing file name past the first cluster", 8, append(be64(1<<16), be32(16)...), "backing file name"},
		{"backing file offset that wraps", 8, append(be64(^uint64(0)), be32(16)...), "backing file name"},
		{"virtual size past 2^63", 24, be64(^uint64(0)), "virtual size"},
		{"L1 table of 4G entries", 36, be32(0xffffffff), "L1 table"},
		{"L1 table too small for the disk", 24, be64(1 << 40), "does not cover"},
		{"L1 table at an unaligned offset", 40, be64(12345), "L1 table offset"},
		{"L1 table at a negative offset", 40, be64(1 << 63), "L1 table offset"},
		{"extension of 4GiB", qcow2HeaderLength, append(be32(qcow2ExtBackingFormatMagic), be32(0xffffffff)...), "header extension"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := patched(t, small, tc.off, tc.b)
			_, err := ReadHeader(p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ReadHeader = %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
}

// A header whose extension area never ends in a terminator is bounded by
// the first cluster, rather than read until the file runs out.
func TestReadHeaderStopsAtFirstCluster(t *testing.T) {
	p := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := Create(p, 1<<20, WithClusterBits(9)); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Unknown, zero-length extensions all the way to the end of cluster 0.
	for off := int64(qcow2HeaderLength); off+8 <= 512; off += 8 {
		if _, err := f.WriteAt(append(be32(0x12345678), be32(0)...), off); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	if _, err := ReadHeader(p); err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
}

// A backing file that names itself would otherwise be followed forever.
func TestFlattenRefusesBackingCycle(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.qcow2")
	b := filepath.Join(dir, "b.qcow2")
	base := filepath.Join(dir, "base.raw")
	if err := os.WriteFile(base, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteOverlay(a, base, 0); err != nil {
		t.Fatal(err)
	}
	if err := WriteOverlay(b, a, 0); err != nil {
		t.Fatal(err)
	}
	// Point a back at b: same length as "base.raw" isn't needed, the name
	// is read by its own size field, so rewrite both.
	hdr, err := ReadHeader(a)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(a, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte(b), int64(hdr.BackingFileOffset)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(be32(uint32(len(b))), 16); err != nil {
		t.Fatal(err)
	}
	f.Close()

	err = Flatten(b, filepath.Join(dir, "out.raw"))
	if err == nil || !strings.Contains(err.Error(), "its own backing chain") {
		t.Fatalf("Flatten = %v, want a backing-chain cycle refused", err)
	}
}

// A backing file name is the image's to choose, and a device node is not
// a disk image Flatten should read whole into its output.
func TestFlattenRefusesNonRegularBacking(t *testing.T) {
	if _, err := os.Stat("/dev/zero"); err != nil {
		t.Skip("no /dev/zero")
	}
	dir := t.TempDir()
	top := filepath.Join(dir, "top.qcow2")
	if err := WriteOverlay(top, "/dev/zero", 1<<20, WithBackingFormat("raw")); err != nil {
		t.Fatal(err)
	}
	err := Flatten(top, filepath.Join(dir, "out.raw"))
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Flatten = %v, want a device backing file refused", err)
	}
}

// Growing l1_size past the clusters the table was given would have the
// L1 read out of whatever follows it.
func TestResizeRefusesOutgrowingL1Table(t *testing.T) {
	p := filepath.Join(t.TempDir(), "disk.qcow2")
	// 512-byte clusters: an L1 entry covers 32KiB and one L1 cluster
	// holds 64 of them, so the allocated table covers 2MiB.
	if err := Create(p, 1<<20, WithClusterBits(9)); err != nil {
		t.Fatal(err)
	}
	if err := Resize(p, 2<<20); err != nil {
		t.Fatalf("Resize within the allocated table: %v", err)
	}
	if err := Resize(p, 4<<20); err == nil || !strings.Contains(err.Error(), "growing the table itself") {
		t.Fatalf("Resize past the allocated table = %v, want it refused", err)
	}
	hdr, err := ReadHeader(p)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Size != 2<<20 {
		t.Fatalf("size after refused resize = %d, want %d", hdr.Size, 2<<20)
	}
}
