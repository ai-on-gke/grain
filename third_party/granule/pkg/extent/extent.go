// Package extent is granule's optional N-MiB extent storage backend: a
// guest root disk kept as a directory of fixed-size raw extent files
// instead of one flat disk.img, and assembled back into a single flat raw
// disk when a VM boots. docs/extent-storage-design.md is the spec; this
// package is the format and the two operations on it.
//
// The point of splitting is the OCI layer, not the VM. A flat disk.img is
// one file, so an image built FROM another and changing one byte of the
// guest's disk ships the whole disk again: overlayfs copies up files, not
// blocks, and so does every layer diff. A directory of extents changes by
// the extents that changed. Split leaves an extent file it would write
// identically untouched -- not rewritten, so not copied up -- which is
// what keeps a derived image's layer proportional to its edit.
//
// The point of assembling is the VM. cloud-hypervisor attaches a disk as
// one file (it refuses multi-extent VMDK descriptors at runtime), and a
// qcow2 chain would bring back the cluster lookups and backing_files=on
// that a raw disk avoids. Assemble produces exactly what the persistent
// and reflink paths already boot: a raw file, image_type=raw, no backing
// file -- by FICLONERANGE per extent where the filesystem shares blocks,
// which is O(extents) metadata and no data, and by a hole-preserving copy
// where it cannot.
//
// The layout of a directory, all of it fixed by this package:
//
//	manifest.json   the Manifest: format version, extent size, virtual size,
//	                and the index and sha256 of every extent present
//	00000000.raw    extent 0, bytes [0, ExtentSize)
//	00000001.raw    extent 1, and so on
//
// An extent that is entirely zero is not stored at all; it is a hole in
// the assembled disk. Every extent is ExtentSize bytes except the last,
// which ends at VirtualSize.
//
// A directory arrives inside an image somebody else may have built, so
// ReadManifest treats it as untrusted: file names are derived from
// indices rather than read from the manifest, extents must be regular
// files (not symlinks) of exactly their expected size, and the sizes are
// bounded.
package extent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"golang.org/x/sys/unix"
)

const (
	// ManifestName is the file in an extent directory that makes it one.
	ManifestName = "manifest.json"

	// FormatVersion is the manifest version this package writes and reads.
	FormatVersion = 1

	// DefaultExtentSizeMB is the extent size Split uses when given zero:
	// small enough that a package install touches a handful of extents,
	// large enough that a multi-GiB disk is tens of files, not thousands.
	DefaultExtentSizeMB = 64

	// MaxExtentSizeMB bounds ExtentSize. 4 GiB is far past any useful
	// extent and keeps index*size arithmetic nowhere near overflow.
	MaxExtentSizeMB = 4096

	// MaxVirtualSize bounds a manifest's VirtualSize at 64 TiB, so an
	// untrusted manifest cannot ask Assemble for an absurd sparse file.
	MaxVirtualSize = 64 << 40

	mib = 1 << 20
)

// Manifest describes an extent directory.
type Manifest struct {
	// Version is FormatVersion.
	Version int `json:"version"`
	// ExtentSize is the size of every extent but the last, in bytes. A
	// whole number of MiB, so every extent starts block-aligned and
	// FICLONERANGE can share it.
	ExtentSize int64 `json:"extentSize"`
	// VirtualSize is the size of the assembled disk, in bytes.
	VirtualSize int64 `json:"virtualSize"`
	// Extents lists the extents stored, by ascending index. An index
	// absent from it is all zeroes.
	Extents []Entry `json:"extents"`
}

// Entry is one stored extent.
type Entry struct {
	Index  int64  `json:"index"`
	SHA256 string `json:"sha256"`
}

// Count is how many extents, stored or not, cover VirtualSize.
func (m Manifest) Count() int64 {
	return (m.VirtualSize + m.ExtentSize - 1) / m.ExtentSize
}

// Len is how many bytes extent i covers: ExtentSize, or less for the last.
func (m Manifest) Len(i int64) int64 {
	if rest := m.VirtualSize - i*m.ExtentSize; rest < m.ExtentSize {
		return rest
	}
	return m.ExtentSize
}

// FileName is extent i's file name inside its directory.
func FileName(i int64) string {
	return fmt.Sprintf("%08d.raw", i)
}

var extentFileRE = regexp.MustCompile(`^[0-9]{8}\.raw$`)

// IsExtentDir reports whether path is a directory holding a manifest:
// the one test callers use to decide a disk source is extents rather
// than an image file.
func IsExtentDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	m, err := os.Lstat(filepath.Join(path, ManifestName))
	return err == nil && m.Mode().IsRegular()
}

// ReadManifest reads and validates dir's manifest, and checks that every
// extent it lists is present at its expected size. It does not hash the
// extents; Verify does.
func ReadManifest(dir string) (Manifest, error) {
	path := filepath.Join(dir, ManifestName)
	f, err := os.Open(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("reading the extent manifest: %w", err)
	}
	defer f.Close()
	// A manifest is an index and a hash per extent: 1 MiB is tens of
	// thousands of extents, past anything MaxVirtualSize/ExtentSize asks.
	raw, err := io.ReadAll(io.LimitReader(f, mib+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("reading the extent manifest %s: %w", path, err)
	}
	if len(raw) > mib {
		return Manifest{}, fmt.Errorf("the extent manifest %s is over the %d-byte limit", path, mib)
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("parsing the extent manifest %s: %w", path, err)
	}
	if err := m.validate(); err != nil {
		return Manifest{}, fmt.Errorf("the extent manifest %s: %w", path, err)
	}
	for _, e := range m.Extents {
		p := filepath.Join(dir, FileName(e.Index))
		info, err := os.Lstat(p)
		if err != nil {
			return Manifest{}, fmt.Errorf("extent %d: %w", e.Index, err)
		}
		if !info.Mode().IsRegular() {
			return Manifest{}, fmt.Errorf("extent %d (%s) is not a regular file (mode %s)", e.Index, p, info.Mode())
		}
		if info.Size() != m.Len(e.Index) {
			return Manifest{}, fmt.Errorf("extent %d (%s) is %d bytes, want %d", e.Index, p, info.Size(), m.Len(e.Index))
		}
	}
	return m, nil
}

func (m Manifest) validate() error {
	if m.Version != FormatVersion {
		return fmt.Errorf("version %d, this granule reads version %d", m.Version, FormatVersion)
	}
	if m.ExtentSize <= 0 || m.ExtentSize%mib != 0 || m.ExtentSize > MaxExtentSizeMB*mib {
		return fmt.Errorf("extentSize %d is not a whole number of MiB between 1 and %d MiB", m.ExtentSize, MaxExtentSizeMB)
	}
	if m.VirtualSize <= 0 || m.VirtualSize > MaxVirtualSize {
		return fmt.Errorf("virtualSize %d is outside (0, %d]", m.VirtualSize, int64(MaxVirtualSize))
	}
	n := m.Count()
	prev := int64(-1)
	for _, e := range m.Extents {
		if e.Index <= prev || e.Index >= n {
			return fmt.Errorf("extent index %d is out of order or past the last extent %d", e.Index, n-1)
		}
		if b, err := hex.DecodeString(e.SHA256); err != nil || len(b) != sha256.Size {
			return fmt.Errorf("extent %d has a malformed sha256 %q", e.Index, e.SHA256)
		}
		prev = e.Index
	}
	return nil
}

// SplitStats says what Split did to the directory.
type SplitStats struct {
	// Written extents were new or changed.
	Written int
	// Unchanged extents already held the same bytes and were not touched.
	Unchanged int
	// Holes are all-zero extents, not stored.
	Holes int
	// Removed extent files no longer belonged in the directory.
	Removed int
}

// Split writes the raw disk image src into dir as extents of
// extentSizeMB MiB (DefaultExtentSizeMB if zero), creating dir if needed.
//
// Split is incremental by design. Into a directory that already holds a
// split of an earlier version of the disk, an extent whose bytes did not
// change is left exactly as it is -- not reopened, not rewritten -- and
// only the manifest and changed extents are written; an extent that is
// now all zeroes, or past the new end, is removed. Run inside a
// Dockerfile over a parent image's extent directory, that is what makes
// the resulting layer carry only the changed extents.
//
// Split does not follow a different extent size into an existing
// directory: it refuses one whose manifest disagrees, rather than
// rewriting every extent.
func Split(src, dir string, extentSizeMB int) (SplitStats, error) {
	var st SplitStats
	if extentSizeMB == 0 {
		extentSizeMB = DefaultExtentSizeMB
	}
	if extentSizeMB < 0 || extentSizeMB > MaxExtentSizeMB {
		return st, fmt.Errorf("extent size %d MiB is outside 1-%d MiB", extentSizeMB, MaxExtentSizeMB)
	}
	extentSize := int64(extentSizeMB) * mib

	in, err := os.Open(src)
	if err != nil {
		return st, err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return st, err
	}
	if !info.Mode().IsRegular() {
		return st, fmt.Errorf("%s is not a regular file", src)
	}
	if info.Size() == 0 {
		return st, fmt.Errorf("%s is empty", src)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return st, err
	}
	if IsExtentDir(dir) {
		old, err := ReadManifest(dir)
		if err != nil {
			return st, fmt.Errorf("%s already holds extents that do not read back: %w", dir, err)
		}
		if old.ExtentSize != extentSize {
			return st, fmt.Errorf("%s already holds %d MiB extents; splitting into %d MiB ones would rewrite every extent (remove the directory first to change the size)",
				dir, old.ExtentSize/mib, extentSizeMB)
		}
	}

	m := Manifest{Version: FormatVersion, ExtentSize: extentSize, VirtualSize: info.Size()}
	keep := map[string]bool{}
	buf := make([]byte, extentSize)
	for i := int64(0); i < m.Count(); i++ {
		n := m.Len(i)
		if _, err := io.ReadFull(io.NewSectionReader(in, i*extentSize, n), buf[:n]); err != nil {
			return st, fmt.Errorf("reading extent %d of %s: %w", i, src, err)
		}
		data := buf[:n]
		if isZero(data) {
			st.Holes++
			continue
		}
		sum := sha256.Sum256(data)
		m.Extents = append(m.Extents, Entry{Index: i, SHA256: hex.EncodeToString(sum[:])})
		name := FileName(i)
		keep[name] = true
		path := filepath.Join(dir, name)
		if same, err := sameContent(path, data); err != nil {
			return st, err
		} else if same {
			st.Unchanged++
			continue
		}
		if err := writeExtent(path, data); err != nil {
			return st, fmt.Errorf("writing extent %d: %w", i, err)
		}
		st.Written++
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return st, err
	}
	for _, e := range entries {
		if extentFileRE.MatchString(e.Name()) && !keep[e.Name()] {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return st, err
			}
			st.Removed++
		}
	}
	return st, writeManifest(dir, m)
}

// sameContent reports whether path already holds exactly data.
func sameContent(path string, data []byte) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(data)) {
		return false, nil
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return bytes.Equal(old, data), nil
}

// writeExtent replaces path with data via a temporary file and a rename,
// so a reader never sees a half-written extent. Runs of zeroes are left
// as holes.
func writeExtent(path string, data []byte) error {
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if err := writeSparse(f, data, 0); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Truncate(int64(len(data))); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func writeManifest(dir string, m Manifest) error {
	sort.Slice(m.Extents, func(a, b int) bool { return m.Extents[a].Index < m.Extents[b].Index })
	if m.Extents == nil {
		m.Extents = []Entry{}
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	path := filepath.Join(dir, ManifestName)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, raw) {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// AssembleStats says how Assemble built the disk.
type AssembleStats struct {
	// Reflinked extents share blocks with their extent file.
	Reflinked int
	// Copied extents were copied, holes preserved.
	Copied int
	// Holes are the extents left unwritten.
	Holes int
}

// Assemble builds the single flat raw disk dir describes at dst, at
// least size bytes long (the manifest's VirtualSize if size is smaller).
//
// It writes dst.tmp and renames it into place, so a dst that exists is
// always a complete disk: callers that reuse an existing disk across
// restarts (config.PrepareOverlay) can trust one they find. dst must not
// already exist.
func Assemble(dir, dst string, size int64) (AssembleStats, error) {
	var st AssembleStats
	m, err := ReadManifest(dir)
	if err != nil {
		return st, err
	}
	if size < m.VirtualSize {
		size = m.VirtualSize
	}
	if _, err := os.Lstat(dst); err == nil {
		return st, fmt.Errorf("%s already exists", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return st, err
	}
	tmp := dst + ".tmp"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return st, err
	}
	fail := func(err error) (AssembleStats, error) {
		out.Close()
		os.Remove(tmp)
		return st, err
	}
	if err := out.Truncate(m.VirtualSize); err != nil {
		return fail(err)
	}

	reflink := true
	for _, e := range m.Extents {
		used, err := placeExtent(out, filepath.Join(dir, FileName(e.Index)), e.Index*m.ExtentSize, m.Len(e.Index), reflink)
		if err != nil {
			return fail(fmt.Errorf("assembling extent %d: %w", e.Index, err))
		}
		if used {
			st.Reflinked++
		} else {
			// One refusal means the filesystem pair cannot share blocks;
			// asking again per extent only costs an ioctl each.
			reflink = false
			st.Copied++
		}
	}
	st.Holes = int(m.Count()) - len(m.Extents)

	if size > m.VirtualSize {
		if err := out.Truncate(size); err != nil {
			return fail(err)
		}
	}
	if err := out.Sync(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return st, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return st, err
	}
	return st, nil
}

// placeExtent puts the extent file at path into out at off: shared by
// FICLONERANGE when tryReflink and the filesystem allow it, copied
// otherwise. It reports whether it shared.
func placeExtent(out *os.File, path string, off, n int64, tryReflink bool) (bool, error) {
	// Check the file actually opened, without following a link:
	// ReadManifest's Lstat is not a promise about what is opened here.
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != n {
		return false, fmt.Errorf("%s changed underneath the assembly", path)
	}
	if tryReflink {
		req := unix.FileCloneRange{
			Src_fd:      int64(f.Fd()),
			Src_offset:  0,
			Src_length:  uint64(n),
			Dest_offset: uint64(off),
		}
		if err := unix.IoctlFileCloneRange(int(out.Fd()), &req); err == nil {
			return true, nil
		}
	}
	buf := make([]byte, mib)
	for done := int64(0); done < n; {
		chunk := buf[:min(int64(len(buf)), n-done)]
		if _, err := io.ReadFull(f, chunk); err != nil {
			return false, err
		}
		if err := writeSparse(out, chunk, off+done); err != nil {
			return false, err
		}
		done += int64(len(chunk))
	}
	return false, nil
}

// sparseBlock is the granularity writeSparse skips zeroes at.
const sparseBlock = 64 << 10

// writeSparse writes data to f at off, skipping sparseBlock-sized runs of
// zeroes. f must already read as zero over the range (a fresh or
// truncated file), which both callers guarantee.
func writeSparse(f *os.File, data []byte, off int64) error {
	for i := 0; i < len(data); i += sparseBlock {
		end := min(i+sparseBlock, len(data))
		if isZero(data[i:end]) {
			continue
		}
		if _, err := f.WriteAt(data[i:end], off+int64(i)); err != nil {
			return err
		}
	}
	return nil
}

// Verify hashes every stored extent against the manifest. Assemble does
// not, because reading every byte at boot is the cost the backend exists
// to avoid; this is for `granule guest extents verify` and image checks.
func Verify(dir string) error {
	m, err := ReadManifest(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range m.Extents {
		f, err := os.OpenFile(filepath.Join(dir, FileName(e.Index)), os.O_RDONLY|unix.O_NOFOLLOW, 0)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != e.SHA256 {
			errs = append(errs, fmt.Errorf("extent %d: sha256 %s, manifest says %s", e.Index, got, e.SHA256))
		}
	}
	return errors.Join(errs...)
}

func isZero(b []byte) bool {
	for len(b) >= 8 {
		if b[0]|b[1]|b[2]|b[3]|b[4]|b[5]|b[6]|b[7] != 0 {
			return false
		}
		b = b[8:]
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
