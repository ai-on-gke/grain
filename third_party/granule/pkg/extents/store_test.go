package extents

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func TestStore_InitReadWriteAcrossExtentBoundaries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "disk.extents")
	const extSize = 64 * 1024 // 64 KiB for fast unit testing
	const virtSize = 4*extSize + 16*1024

	st, err := Open(Options{
		Dir:              dir,
		VirtualSizeBytes: virtSize,
		ExtentSizeBytes:  extSize,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer st.Close()

	if st.VirtualSize() != virtSize {
		t.Fatalf("VirtualSize() = %d, want %d", st.VirtualSize(), virtSize)
	}
	if st.NumExtents() != 5 {
		t.Fatalf("NumExtents() = %d, want 5", st.NumExtents())
	}

	// Reading unallocated extents should return zeros and not create files.
	readBuf := make([]byte, virtSize)
	n, err := st.ReadAt(readBuf, 0)
	if err != nil || n != virtSize {
		t.Fatalf("ReadAt unallocated = (%d, %v), want (%d, nil)", n, err, virtSize)
	}
	if !isAllZero(readBuf) {
		t.Fatal("ReadAt unallocated returned non-zero data")
	}
	for i := uint64(0); i < 5; i++ {
		if st.HasExtent(i) {
			t.Fatalf("extent %d should not exist on disk after read", i)
		}
	}

	// Write a payload spanning from middle of extent 0 into middle of extent 2.
	payload := make([]byte, 2*extSize)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	writeOff := int64(extSize / 2)
	if n, err := st.WriteAt(payload, writeOff); err != nil || n != len(payload) {
		t.Fatalf("WriteAt() = (%d, %v), want (%d, nil)", n, err, len(payload))
	}

	// Extents 0, 1, 2 must exist; extents 3, 4 must still be omitted.
	for _, idx := range []uint64{0, 1, 2} {
		if !st.HasExtent(idx) {
			t.Errorf("extent %d should exist after write", idx)
		}
	}
	for _, idx := range []uint64{3, 4} {
		if st.HasExtent(idx) {
			t.Errorf("extent %d should remain omitted on disk", idx)
		}
	}

	got := make([]byte, len(payload))
	if n, err := st.ReadAt(got, writeOff); err != nil || n != len(got) {
		t.Fatalf("ReadAt() = (%d, %v)", n, err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("ReadAt() data mismatch across extent boundaries")
	}
}

// TestStore_OverlayfsReadOnlyInvariant verifies the critical OCI layering invariant:
// existing extent files are opened strictly O_RDONLY on read paths and only promoted
// to O_RDWR when a write targets that specific extent.
func TestStore_OverlayfsReadOnlyInvariant(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "disk.extents")
	const extSize = 64 * 1024
	const virtSize = 4 * extSize

	// Populate extents 0 and 1.
	st, err := Open(Options{
		Dir:              dir,
		VirtualSizeBytes: virtSize,
		ExtentSizeBytes:  extSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteAt([]byte("extent-zero-data"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteAt([]byte("extent-one-data"), extSize+128); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen in persistent writable mode (as in a subsequent Granulefile RUN layer).
	st2, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()

	// Read from both extent 0 and extent 1.
	buf := make([]byte, 16)
	if _, err := st2.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "extent-zero-data" {
		t.Fatalf("got %q, want extent-zero-data", buf)
	}
	buf1 := make([]byte, 15)
	if _, err := st2.ReadAt(buf1, extSize+128); err != nil {
		t.Fatal(err)
	}
	if string(buf1) != "extent-one-data" {
		t.Fatalf("got %q, want extent-one-data", buf1)
	}

	// Verify both extent 0 and extent 1 are opened strictly O_RDONLY at the kernel fd level.
	for _, idx := range []uint64{0, 1} {
		writable, open := st2.ExtentFileMode(idx)
		if !open {
			t.Fatalf("extent %d should be open after ReadAt", idx)
		}
		if writable {
			t.Fatalf("extent %d marked writable after ReadAt; must be O_RDONLY to avoid overlayfs copy_up", idx)
		}
		fd := st2.ExtentFD(idx)
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if err != nil {
			t.Fatalf("FcntlInt F_GETFL on extent %d fd %d: %v", idx, fd, err)
		}
		if acc := flags & unix.O_ACCMODE; acc != unix.O_RDONLY {
			t.Fatalf("extent %d kernel fd access mode = %d, want O_RDONLY (%d)", idx, acc, unix.O_RDONLY)
		}
	}

	// Now write ONLY to extent 1. Extent 0 must remain O_RDONLY while extent 1 promotes to O_RDWR.
	if _, err := st2.WriteAt([]byte("updated-one"), extSize+256); err != nil {
		t.Fatal(err)
	}

	w0, open0 := st2.ExtentFileMode(0)
	if !open0 || w0 {
		t.Fatalf("extent 0 mode = (writable=%v, open=%v), want (false, true)", w0, open0)
	}
	flags0, err := unix.FcntlInt(uintptr(st2.ExtentFD(0)), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags0&unix.O_ACCMODE != unix.O_RDONLY {
		t.Fatalf("extent 0 kernel fd was promoted despite no write to extent 0")
	}

	w1, open1 := st2.ExtentFileMode(1)
	if !open1 || !w1 {
		t.Fatalf("extent 1 mode = (writable=%v, open=%v), want (true, true)", w1, open1)
	}
	flags1, err := unix.FcntlInt(uintptr(st2.ExtentFD(1)), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags1&unix.O_ACCMODE != unix.O_RDWR {
		t.Fatalf("extent 1 kernel fd access mode = %d, want O_RDWR (%d)", flags1&unix.O_ACCMODE, unix.O_RDWR)
	}
}

func TestStore_OverlayBackedByExtentStore(t *testing.T) {
	tmp := t.TempDir()
	baseDir := filepath.Join(tmp, "base.extents")
	overlayDir := filepath.Join(tmp, "overlay.extents")
	const extSize = 64 * 1024
	const virtSize = 4 * extSize

	base, err := Open(Options{
		Dir:              baseDir,
		VirtualSizeBytes: virtSize,
		ExtentSizeBytes:  extSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.WriteAt([]byte("BASE-EXTENT-0"), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := base.WriteAt([]byte("BASE-EXTENT-1"), extSize+200); err != nil {
		t.Fatal(err)
	}
	if err := base.Close(); err != nil {
		t.Fatal(err)
	}

	if err := PrepareOverlay(overlayDir, baseDir, 0, 0); err != nil {
		t.Fatalf("PrepareOverlay: %v", err)
	}

	ovl, err := Open(Options{
		Dir:      overlayDir,
		BasePath: baseDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ovl.Close()

	// Unmodified reads come from base without materializing overlay extent files.
	got0 := make([]byte, 13)
	if _, err := ovl.ReadAt(got0, 100); err != nil {
		t.Fatal(err)
	}
	if string(got0) != "BASE-EXTENT-0" {
		t.Fatalf("ReadAt base extent 0 = %q, want BASE-EXTENT-0", got0)
	}
	if ovl.HasExtent(0) {
		t.Fatal("overlay extent 0 should not be materialized on read")
	}

	// Partial write to extent 1 lazily materializes extent 1 in overlay while preserving
	// unmodified bytes in extent 1 and leaving baseDir untouched.
	if _, err := ovl.WriteAt([]byte("OVERLAY-PATCH"), extSize+500); err != nil {
		t.Fatal(err)
	}
	if !ovl.HasExtent(1) {
		t.Fatal("overlay extent 1 should be materialized after write")
	}
	if ovl.HasExtent(0) {
		t.Fatal("overlay extent 0 should still not be materialized")
	}

	got1Base := make([]byte, 13)
	if _, err := ovl.ReadAt(got1Base, extSize+200); err != nil {
		t.Fatal(err)
	}
	if string(got1Base) != "BASE-EXTENT-1" {
		t.Fatalf("unmodified range in materialized extent 1 = %q, want BASE-EXTENT-1", got1Base)
	}
	got1Patch := make([]byte, 13)
	if _, err := ovl.ReadAt(got1Patch, extSize+500); err != nil {
		t.Fatal(err)
	}
	if string(got1Patch) != "OVERLAY-PATCH" {
		t.Fatalf("patched range in extent 1 = %q, want OVERLAY-PATCH", got1Patch)
	}

	// Verify base store was not mutated.
	baseCheck, err := Open(Options{Dir: baseDir, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer baseCheck.Close()
	baseBuf := make([]byte, 13)
	if _, err := baseCheck.ReadAt(baseBuf, extSize+500); err != nil {
		t.Fatal(err)
	}
	if !isAllZero(baseBuf) {
		t.Fatalf("base extent 1 was mutated: got %q", baseBuf)
	}
}

func TestStore_OverlayBackedByFlatImage(t *testing.T) {
	tmp := t.TempDir()
	flatPath := filepath.Join(tmp, "disk.img")
	overlayDir := filepath.Join(tmp, "overlay.extents")
	const extSize = 64 * 1024
	const virtSize = 3 * extSize

	flatData := make([]byte, virtSize)
	copy(flatData[50:], "FLAT-EXTENT-0")
	copy(flatData[extSize+70:], "FLAT-EXTENT-1")
	if err := os.WriteFile(flatPath, flatData, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := PrepareOverlay(overlayDir, flatPath, 4*extSize, extSize); err != nil {
		t.Fatalf("PrepareOverlay from flat base: %v", err)
	}

	ovl, err := Open(Options{
		Dir:      overlayDir,
		BasePath: flatPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ovl.Close()

	if ovl.VirtualSize() != 4*extSize {
		t.Fatalf("VirtualSize = %d, want %d", ovl.VirtualSize(), 4*extSize)
	}

	buf := make([]byte, 13)
	if _, err := ovl.ReadAt(buf, 50); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "FLAT-EXTENT-0" {
		t.Fatalf("got %q, want FLAT-EXTENT-0", buf)
	}

	// Modify extent 1 in overlay.
	if _, err := ovl.WriteAt([]byte("NEW-DATA"), extSize+1000); err != nil {
		t.Fatal(err)
	}
	if _, err := ovl.ReadAt(buf, extSize+70); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "FLAT-EXTENT-1" {
		t.Fatalf("got %q, want FLAT-EXTENT-1", buf)
	}
}

func TestStore_WriteZeroesAndDiscard(t *testing.T) {
	tmp := t.TempDir()
	baseDir := filepath.Join(tmp, "base.extents")
	const extSize = 64 * 1024
	const virtSize = 3 * extSize

	st, err := Open(Options{
		Dir:              baseDir,
		VirtualSizeBytes: virtSize,
		ExtentSizeBytes:  extSize,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Write non-zero data to extent 0 and extent 1.
	if _, err := st.WriteAt(bytes.Repeat([]byte{0xAB}, extSize), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteAt(bytes.Repeat([]byte{0xCD}, extSize), extSize); err != nil {
		t.Fatal(err)
	}
	if !st.HasExtent(0) || !st.HasExtent(1) {
		t.Fatal("extents 0 and 1 should exist on disk")
	}

	// Full-extent Discard on extent 0 (no base store) must unlink 00000000.raw!
	if err := st.Discard(0, extSize); err != nil {
		t.Fatalf("Discard full extent 0: %v", err)
	}
	if st.HasExtent(0) {
		t.Fatal("extent 0 file should have been unlinked after full-extent Discard")
	}
	check0 := make([]byte, extSize)
	if _, err := st.ReadAt(check0, 0); err != nil {
		t.Fatal(err)
	}
	if !isAllZero(check0) {
		t.Fatal("extent 0 should read back as all zeros after Discard")
	}

	// Partial WriteZeroes on extent 1 zeroes only [extSize+4096, extSize+8192).
	if err := st.WriteZeroes(extSize+4096, 4096); err != nil {
		t.Fatalf("WriteZeroes partial extent 1: %v", err)
	}
	check1 := make([]byte, extSize)
	if _, err := st.ReadAt(check1, extSize); err != nil {
		t.Fatal(err)
	}
	if !isAllZero(check1[4096:8192]) {
		t.Fatal("punched sub-range in extent 1 should be zero")
	}
	if check1[0] != 0xCD || check1[8192] != 0xCD {
		t.Fatal("bytes outside punched sub-range in extent 1 were corrupted")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Now open an overlay on top of baseDir and Discard all of extent 1 (which is non-zero in baseDir).
	// The overlay must mask baseDir's extent 1 so reads return zeros!
	overlayDir := filepath.Join(tmp, "overlay.extents")
	ovl, err := Open(Options{
		Dir:      overlayDir,
		BasePath: baseDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ovl.Close()

	if err := ovl.Discard(extSize, extSize); err != nil {
		t.Fatalf("overlay Discard full extent 1: %v", err)
	}
	if _, err := ovl.ReadAt(check1, extSize); err != nil {
		t.Fatal(err)
	}
	if !isAllZero(check1) {
		t.Fatal("overlay full-extent Discard on base-backed extent 1 did not mask base data with zeros")
	}
}

func TestStore_SplitFromFlatAndFlattenToFlat(t *testing.T) {
	tmp := t.TempDir()
	flatSrc := filepath.Join(tmp, "src.img")
	extDir := filepath.Join(tmp, "disk.extents")
	flatDst := filepath.Join(tmp, "dst.img")

	const extSize = 64 * 1024
	const virtSize = 5*extSize + 12*1024

	srcBytes := make([]byte, virtSize)
	// Populate only extents 0, 2, and 4; leave extents 1 and 3 all-zero.
	copy(srcBytes[0:], "EXTENT-0-HEADER")
	copy(srcBytes[2*extSize+1024:], bytes.Repeat([]byte{0x5A}, 8192))
	copy(srcBytes[4*extSize+4096:], "FINAL-PARTIAL-EXTENT")

	if err := os.WriteFile(flatSrc, srcBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := SplitFromFlat(flatSrc, extDir, extSize)
	if err != nil {
		t.Fatalf("SplitFromFlat: %v", err)
	}
	if m.VirtualSizeBytes != virtSize || m.ExtentSizeBytes != extSize {
		t.Fatalf("Manifest = %+v", m)
	}

	// Verify all-zero extents 1 and 3 were omitted on disk.
	for _, presentIdx := range []uint64{0, 2, 4} {
		if _, err := os.Stat(ExtentPath(extDir, presentIdx)); err != nil {
			t.Errorf("expected extent %d to exist on disk: %v", presentIdx, err)
		}
	}
	for _, absentIdx := range []uint64{1, 3} {
		if _, err := os.Stat(ExtentPath(extDir, absentIdx)); !os.IsNotExist(err) {
			t.Errorf("expected all-zero extent %d to be omitted on disk, stat err = %v", absentIdx, err)
		}
	}

	if err := FlattenToFlat(extDir, flatDst); err != nil {
		t.Fatalf("FlattenToFlat: %v", err)
	}

	dstBytes, err := os.ReadFile(flatDst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(srcBytes, dstBytes) {
		t.Fatal("round-trip SplitFromFlat -> FlattenToFlat did not produce identical image")
	}
}

func TestStore_ResizeAndFlush(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "disk.extents")
	const extSize = 64 * 1024

	st, err := Open(Options{
		Dir:              dir,
		VirtualSizeBytes: extSize + 4096, // 2 extents, second is partial (4 KiB)
		ExtentSizeBytes:  extSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if _, err := st.WriteAt([]byte("partial-tail"), extSize); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Shrinking must be rejected.
	if err := st.Resize(extSize); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("Resize(shrink) err = %v, want error mentioning truncated", err)
	}

	// Grow to 4 full extents.
	if err := st.Resize(4 * extSize); err != nil {
		t.Fatalf("Resize(grow): %v", err)
	}
	if st.VirtualSize() != 4*extSize {
		t.Fatalf("VirtualSize() = %d, want %d", st.VirtualSize(), 4*extSize)
	}

	// Verify the previously partial extent 1 was grown to full extSize on disk.
	info, err := os.Stat(ExtentPath(dir, 1))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != extSize {
		t.Fatalf("extent 1 size after grow = %d, want %d", info.Size(), extSize)
	}

	// Write near the end of the newly grown disk.
	if _, err := st.WriteAt([]byte("grown-end"), 4*extSize-9); err != nil {
		t.Fatalf("WriteAt grown end: %v", err)
	}
}

func TestStore_ConcurrentReadWriteRace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "disk.extents")
	const extSize = 64 * 1024
	const numExtents = 8
	const virtSize = numExtents * extSize

	st, err := Open(Options{
		Dir:              dir,
		VirtualSizeBytes: virtSize,
		ExtentSizeBytes:  extSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			buf := bytes.Repeat([]byte{byte(id + 1)}, 1024)
			readBuf := make([]byte, 1024)
			for i := 0; i < 50; i++ {
				off := int64(((id*31 + i*17) % (virtSize / 1024)) * 1024)
				if _, err := st.WriteAt(buf, off); err != nil {
					t.Errorf("WriteAt: %v", err)
					return
				}
				if _, err := st.ReadAt(readBuf, off); err != nil {
					t.Errorf("ReadAt: %v", err)
					return
				}
				if i%10 == 0 {
					_ = st.Flush()
				}
			}
		}(worker)
	}
	wg.Wait()
}
