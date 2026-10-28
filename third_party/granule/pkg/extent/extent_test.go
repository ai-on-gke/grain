package extent

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeDisk writes a disk of size bytes with data at the given MiB
// offsets and zeroes everywhere else.
func writeDisk(t *testing.T, path string, size int64, dataAtMiB ...int64) []byte {
	t.Helper()
	disk := make([]byte, size)
	r := rand.New(rand.NewSource(1))
	for _, m := range dataAtMiB {
		r.Read(disk[m*mib : m*mib+4096])
	}
	if err := os.WriteFile(path, disk, 0o644); err != nil {
		t.Fatal(err)
	}
	return disk
}

func TestSplitAssembleRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "disk.img")
	// 5.5 MiB in 1 MiB extents: extents 0, 2 and the short last one hold
	// data, 1, 3 and 4 are holes.
	want := writeDisk(t, src, 5*mib+mib/2, 0, 2, 5)
	dir := filepath.Join(tmp, "disk.extents")

	st, err := Split(src, dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if st.Written != 3 || st.Holes != 3 || st.Unchanged != 0 || st.Removed != 0 {
		t.Errorf("Split stats = %+v, want 3 written, 3 holes", st)
	}
	if !IsExtentDir(dir) {
		t.Fatalf("IsExtentDir(%s) = false after Split", dir)
	}
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Count() != 6 || m.Len(5) != mib/2 {
		t.Errorf("manifest count %d, last len %d; want 6 and %d", m.Count(), m.Len(5), mib/2)
	}
	if err := Verify(dir); err != nil {
		t.Errorf("Verify: %v", err)
	}

	out := filepath.Join(tmp, "out", "disk.raw")
	ast, err := Assemble(dir, out, 8*mib)
	if err != nil {
		t.Fatal(err)
	}
	if ast.Reflinked+ast.Copied != 3 || ast.Holes != 3 {
		t.Errorf("Assemble stats = %+v, want 3 placed and 3 holes", ast)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(got)) != 8*mib {
		t.Fatalf("assembled disk is %d bytes, want %d (grown to the requested size)", len(got), 8*mib)
	}
	if !bytes.Equal(got[:len(want)], want) {
		t.Error("assembled disk does not match the source")
	}
	if !isZero(got[len(want):]) {
		t.Error("growth past the virtual size is not zero")
	}
	if _, err := os.Stat(out + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temporary file left behind: %v", err)
	}
	if _, err := Assemble(dir, out, 0); err == nil {
		t.Error("Assemble over an existing disk succeeded, want a refusal")
	}
}

// The property the backend exists for: re-splitting an edited disk into
// the same directory touches only the extents that changed.
func TestSplitIsIncremental(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "disk.img")
	writeDisk(t, src, 4*mib, 0, 1, 2, 3)
	dir := filepath.Join(tmp, "disk.extents")
	if _, err := Split(src, dir, 1); err != nil {
		t.Fatal(err)
	}
	before := map[string]os.FileInfo{}
	for i := int64(0); i < 4; i++ {
		info, err := os.Stat(filepath.Join(dir, FileName(i)))
		if err != nil {
			t.Fatal(err)
		}
		before[FileName(i)] = info
	}

	// Change extent 1, zero extent 3.
	f, err := os.OpenFile(src, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("changed"), mib+10); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(make([]byte, mib), 3*mib); err != nil {
		t.Fatal(err)
	}
	f.Close()

	st, err := Split(src, dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if st.Written != 1 || st.Unchanged != 2 || st.Holes != 1 || st.Removed != 1 {
		t.Errorf("re-split stats = %+v, want 1 written, 2 unchanged, 1 hole, 1 removed", st)
	}
	for _, i := range []int64{0, 2} {
		info, err := os.Stat(filepath.Join(dir, FileName(i)))
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(info, before[FileName(i)]) || !info.ModTime().Equal(before[FileName(i)].ModTime()) {
			t.Errorf("unchanged extent %d was rewritten", i)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, FileName(3))); !os.IsNotExist(err) {
		t.Errorf("extent 3 is now all zeroes but its file remains: %v", err)
	}
	if err := Verify(dir); err != nil {
		t.Errorf("Verify after re-split: %v", err)
	}

	if _, err := Split(src, dir, 2); err == nil || !strings.Contains(err.Error(), "MiB extents") {
		t.Errorf("Split with a different extent size = %v, want a refusal", err)
	}
}

func TestReadManifestRejectsUntrustedLayouts(t *testing.T) {
	good := func(t *testing.T) string {
		dir := t.TempDir()
		src := filepath.Join(t.TempDir(), "disk.img")
		writeDisk(t, src, 2*mib, 0, 1)
		if _, err := Split(src, dir, 1); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	editManifest := func(t *testing.T, dir string, edit func(*Manifest)) {
		raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
		if err != nil {
			t.Fatal(err)
		}
		var m Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		edit(&m)
		raw, _ = json.Marshal(m)
		if err := os.WriteFile(filepath.Join(dir, ManifestName), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string]func(t *testing.T, dir string){
		"wrong version": func(t *testing.T, dir string) {
			editManifest(t, dir, func(m *Manifest) { m.Version = 2 })
		},
		"unaligned extent size": func(t *testing.T, dir string) {
			editManifest(t, dir, func(m *Manifest) { m.ExtentSize = mib + 512 })
		},
		"huge virtual size": func(t *testing.T, dir string) {
			editManifest(t, dir, func(m *Manifest) { m.VirtualSize = MaxVirtualSize + 1 })
		},
		"index past the end": func(t *testing.T, dir string) {
			editManifest(t, dir, func(m *Manifest) { m.Extents[1].Index = 7 })
		},
		"duplicate index": func(t *testing.T, dir string) {
			editManifest(t, dir, func(m *Manifest) { m.Extents[1].Index = 0 })
		},
		"short extent": func(t *testing.T, dir string) {
			if err := os.Truncate(filepath.Join(dir, FileName(1)), 100); err != nil {
				t.Fatal(err)
			}
		},
		"symlinked extent": func(t *testing.T, dir string) {
			p := filepath.Join(dir, FileName(1))
			if err := os.Rename(p, p+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(p+".real", p); err != nil {
				t.Fatal(err)
			}
		},
		"unknown field": func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(`{"version":1,"extentSize":1048576,"virtualSize":1048576,"extents":[],"path":"/etc/shadow"}`), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			dir := good(t)
			breakIt(t, dir)
			if _, err := ReadManifest(dir); err == nil {
				t.Error("ReadManifest accepted it")
			}
			if _, err := Assemble(dir, filepath.Join(t.TempDir(), "out"), 0); err == nil {
				t.Error("Assemble accepted it")
			}
		})
	}
}

func TestVerifyCatchesCorruption(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "disk.img")
	writeDisk(t, src, 2*mib, 0, 1)
	dir := filepath.Join(tmp, "x")
	if _, err := Split(src, dir, 1); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, FileName(1)), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteAt([]byte{0xff}, 0)
	f.Close()
	if err := Verify(dir); err == nil || !strings.Contains(err.Error(), "extent 1") {
		t.Errorf("Verify = %v, want an extent 1 mismatch", err)
	}
}

func TestIsExtentDir(t *testing.T) {
	tmp := t.TempDir()
	if IsExtentDir(tmp) {
		t.Error("an empty directory is not an extent directory")
	}
	file := filepath.Join(tmp, "disk.img")
	os.WriteFile(file, []byte("x"), 0o644)
	if IsExtentDir(file) {
		t.Error("a file is not an extent directory")
	}
	if IsExtentDir(filepath.Join(tmp, "missing")) {
		t.Error("a missing path is not an extent directory")
	}
}
