package extents

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/bwsalmon/granule/pkg/qcow2"
)

func BenchmarkPrepareOverlay_Extents(b *testing.B) {
	dir := b.TempDir()
	baseDir := filepath.Join(dir, "base.extents")
	if _, err := Init(baseDir, 10<<30, DefaultExtentSizeBytes); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ovlDir := filepath.Join(dir, "ovl", strconv.Itoa(i))
		if err := PrepareOverlay(ovlDir, baseDir, 10<<30, DefaultExtentSizeBytes); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPrepareOverlay_QCOW2_2MB(b *testing.B) {
	dir := b.TempDir()
	baseImg := filepath.Join(dir, "base.img")
	f, err := os.Create(baseImg)
	if err != nil {
		b.Fatal(err)
	}
	_ = f.Truncate(10 << 30)
	_ = f.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ovlPath := filepath.Join(dir, "ovl", strconv.Itoa(i)+".qcow2")
		if err := qcow2.WriteOverlay(ovlPath, baseImg, 10<<30); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRandom4KRead_Extents(b *testing.B) {
	dir := b.TempDir()
	extDir := filepath.Join(dir, "disk.extents")
	const extSize = 16 << 20 // 16 MiB
	const virtSize = 64 << 20
	st, err := Open(Options{
		Dir:              extDir,
		VirtualSizeBytes: virtSize,
		ExtentSizeBytes:  extSize,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()

	block := bytes.Repeat([]byte{0x42}, 4096)
	for off := int64(0); off < virtSize; off += 1 << 20 {
		if _, err := st.WriteAt(block, off); err != nil {
			b.Fatal(err)
		}
	}

	readBuf := make([]byte, 4096)
	b.SetBytes(4096)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := int64((i*7919)%(virtSize/4096)) * 4096
		if _, err := st.ReadAt(readBuf, off); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRandom4KRead_FlatFile(b *testing.B) {
	dir := b.TempDir()
	flatPath := filepath.Join(dir, "disk.img")
	const virtSize = 64 << 20
	f, err := os.OpenFile(flatPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(virtSize); err != nil {
		b.Fatal(err)
	}

	block := bytes.Repeat([]byte{0x42}, 4096)
	for off := int64(0); off < virtSize; off += 1 << 20 {
		if _, err := f.WriteAt(block, off); err != nil {
			b.Fatal(err)
		}
	}

	readBuf := make([]byte, 4096)
	b.SetBytes(4096)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := int64((i*7919)%(virtSize/4096)) * 4096
		if _, err := f.ReadAt(readBuf, off); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRandom4KWrite_Extents(b *testing.B) {
	dir := b.TempDir()
	extDir := filepath.Join(dir, "disk.extents")
	const extSize = 16 << 20
	const virtSize = 64 << 20
	st, err := Open(Options{
		Dir:              extDir,
		VirtualSizeBytes: virtSize,
		ExtentSizeBytes:  extSize,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()

	block := bytes.Repeat([]byte{0x42}, 4096)
	for off := int64(0); off < virtSize; off += extSize {
		if _, err := st.WriteAt(block, off); err != nil {
			b.Fatal(err)
		}
	}

	b.SetBytes(4096)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := int64((i*7919)%(virtSize/4096)) * 4096
		if _, err := st.WriteAt(block, off); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRandom4KWrite_FlatFile(b *testing.B) {
	dir := b.TempDir()
	flatPath := filepath.Join(dir, "disk.img")
	const virtSize = 64 << 20
	f, err := os.OpenFile(flatPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(virtSize); err != nil {
		b.Fatal(err)
	}

	block := bytes.Repeat([]byte{0x42}, 4096)
	b.SetBytes(4096)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := int64((i*7919)%(virtSize/4096)) * 4096
		if _, err := f.WriteAt(block, off); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSequential1MWrite_Extents(b *testing.B) {
	dir := b.TempDir()
	extDir := filepath.Join(dir, "disk.extents")
	const extSize = 64 << 20
	const virtSize = 256 << 20
	st, err := Open(Options{
		Dir:              extDir,
		VirtualSizeBytes: virtSize,
		ExtentSizeBytes:  extSize,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()

	buf := bytes.Repeat([]byte{0x5A}, 1<<20)
	b.SetBytes(1 << 20)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := int64(i%(virtSize/(1<<20))) * (1 << 20)
		if _, err := st.WriteAt(buf, off); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSequential1MRead_Extents(b *testing.B) {
	dir := b.TempDir()
	extDir := filepath.Join(dir, "disk.extents")
	const extSize = 64 << 20
	const virtSize = 256 << 20
	st, err := Open(Options{
		Dir:              extDir,
		VirtualSizeBytes: virtSize,
		ExtentSizeBytes:  extSize,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()

	buf := bytes.Repeat([]byte{0x5A}, 1<<20)
	for off := int64(0); off < virtSize; off += 1 << 20 {
		if _, err := st.WriteAt(buf, off); err != nil {
			b.Fatal(err)
		}
	}

	readBuf := make([]byte, 1<<20)
	b.SetBytes(1 << 20)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := int64(i%(virtSize/(1<<20))) * (1 << 20)
		if _, err := st.ReadAt(readBuf, off); err != nil {
			b.Fatal(err)
		}
	}
}
