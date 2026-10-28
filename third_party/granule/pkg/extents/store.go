// Package extents implements an OCI-layer-friendly N-MiB extent block storage
// backend and an in-process vhost-user-blk server for Cloud-Hypervisor.
//
// A logical disk of VirtualSizeBytes is represented on disk as a directory
// containing a manifest.json and fixed-size raw extent files under
// extents/%08d.raw. All-zero extents are omitted on disk, and existing extent
// files are opened strictly O_RDONLY on read paths and promoted to O_RDWR only
// when a write targets that specific extent, avoiding Docker overlayfs
// copy-up of unmodified extents across image build layers.
package extents

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

const (
	// ManifestVersion is the current on-disk format version for manifest.json.
	ManifestVersion = 1

	// ManifestFileName is the metadata file stored at the root of an extent directory.
	ManifestFileName = "manifest.json"

	// ExtentsSubdir is the subdirectory holding %08d.raw extent files.
	ExtentsSubdir = "extents"

	// DefaultExtentSizeMB is the default extent size in MiB when none is specified.
	DefaultExtentSizeMB = 64

	// DefaultExtentSizeBytes is the default extent size in bytes (64 MiB).
	DefaultExtentSizeBytes = uint64(DefaultExtentSizeMB) << 20

	// minExtentSizeBytes is the minimum valid extent size (4 KiB block alignment).
	minExtentSizeBytes = 4096
)

// Manifest describes the layout of an extent-backed virtual disk.
type Manifest struct {
	Version          int    `json:"version"`
	VirtualSizeBytes uint64 `json:"virtualSizeBytes"`
	ExtentSizeBytes  uint64 `json:"extentSizeBytes"`
}

// Validate checks that the manifest invariants hold.
func (m Manifest) Validate() error {
	if m.Version != ManifestVersion {
		return fmt.Errorf("extents: unsupported manifest version %d (want %d)", m.Version, ManifestVersion)
	}
	if m.ExtentSizeBytes < minExtentSizeBytes || m.ExtentSizeBytes%minExtentSizeBytes != 0 {
		return fmt.Errorf("extents: invalid extentSizeBytes %d (must be a positive multiple of %d)", m.ExtentSizeBytes, minExtentSizeBytes)
	}
	return nil
}

// NumExtents returns the number of extents spanning VirtualSizeBytes.
func (m Manifest) NumExtents() uint64 {
	if m.VirtualSizeBytes == 0 || m.ExtentSizeBytes == 0 {
		return 0
	}
	return (m.VirtualSizeBytes + m.ExtentSizeBytes - 1) / m.ExtentSizeBytes
}

// ExtentLength returns the logical byte length of extent idx.
// All extents except possibly the final one have length ExtentSizeBytes.
func (m Manifest) ExtentLength(idx uint64) uint64 {
	num := m.NumExtents()
	if idx >= num {
		return 0
	}
	start := idx * m.ExtentSizeBytes
	rem := m.VirtualSizeBytes - start
	if rem < m.ExtentSizeBytes {
		return rem
	}
	return m.ExtentSizeBytes
}

// ExtentFilename returns the canonical filename for an extent index ("%08d.raw").
func ExtentFilename(index uint64) string {
	return fmt.Sprintf("%08d.raw", index)
}

// ExtentPath returns the path to extent index inside an extent store directory.
// Because index is an unsigned integer formatted as %08d.raw inside a fixed
// subdirectory, no untrusted path from a header or descriptor is ever resolved.
func ExtentPath(dir string, index uint64) string {
	return filepath.Join(dir, ExtentsSubdir, ExtentFilename(index))
}

// IsExtentDir reports whether path is an initialized extent store directory
// containing a regular manifest.json file and an extents subdirectory.
func IsExtentDir(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(path, ManifestFileName))
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	sub, err := os.Stat(filepath.Join(path, ExtentsSubdir))
	return err == nil && sub.IsDir()
}

// ReadManifest reads and validates manifest.json from dir.
func ReadManifest(dir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFileName))
	if err != nil {
		return Manifest{}, fmt.Errorf("extents: reading manifest in %s: %w", dir, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("extents: parsing manifest in %s: %w", dir, err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// WriteManifest atomically writes manifest.json into dir.
func WriteManifest(dir string, m Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, ExtentsSubdir), 0o755); err != nil {
		return fmt.Errorf("extents: creating directory %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("extents: marshaling manifest: %w", err)
	}
	data = append(data, '\n')
	tmpPath := filepath.Join(dir, ManifestFileName+".tmp")
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("extents: writing manifest temp file: %w", err)
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, ManifestFileName)); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("extents: renaming manifest: %w", err)
	}
	return nil
}

// VirtualSize returns the virtual disk size in bytes from dir's manifest.json.
func VirtualSize(dir string) (int64, error) {
	m, err := ReadManifest(dir)
	if err != nil {
		return 0, err
	}
	return int64(m.VirtualSizeBytes), nil
}

// Init initializes a new extent directory at dir with the given virtual size
// and extent size. If extentSizeBytes is 0, DefaultExtentSizeBytes (64 MiB) is used.
func Init(dir string, virtualSizeBytes, extentSizeBytes uint64) (*Manifest, error) {
	if extentSizeBytes == 0 {
		extentSizeBytes = DefaultExtentSizeBytes
	}
	m := Manifest{
		Version:          ManifestVersion,
		VirtualSizeBytes: virtualSizeBytes,
		ExtentSizeBytes:  extentSizeBytes,
	}
	if err := WriteManifest(dir, m); err != nil {
		return nil, err
	}
	return &m, nil
}

// PrepareOverlay creates or resizes an extent overlay directory at overlayDir
// backed by basePath (which may be either an extent directory or a flat raw disk image).
func PrepareOverlay(overlayDir, basePath string, targetSizeBytes, extentSizeBytes uint64) error {
	var baseSize uint64
	var baseExtentSize uint64

	if basePath != "" {
		if IsExtentDir(basePath) {
			bm, err := ReadManifest(basePath)
			if err != nil {
				return fmt.Errorf("extents: reading base manifest %s: %w", basePath, err)
			}
			baseSize = bm.VirtualSizeBytes
			baseExtentSize = bm.ExtentSizeBytes
		} else {
			info, err := os.Stat(basePath)
			if err != nil {
				return fmt.Errorf("extents: stat base image %s: %w", basePath, err)
			}
			baseSize = uint64(info.Size())
		}
	}

	if IsExtentDir(overlayDir) {
		existing, err := ReadManifest(overlayDir)
		if err != nil {
			return err
		}
		if targetSizeBytes > 0 {
			if targetSizeBytes < existing.VirtualSizeBytes {
				return fmt.Errorf("extents: requested disk size (%d bytes) is smaller than existing overlay (%d bytes): guest filesystem would be truncated",
					targetSizeBytes, existing.VirtualSizeBytes)
			}
			if targetSizeBytes > existing.VirtualSizeBytes {
				st, err := Open(Options{Dir: overlayDir, BasePath: basePath})
				if err != nil {
					return err
				}
				defer st.Close()
				return st.Resize(targetSizeBytes)
			}
		}
		return nil
	}

	virtualSize := baseSize
	if targetSizeBytes > 0 {
		if targetSizeBytes < baseSize {
			return fmt.Errorf("extents: requested disk size (%d bytes) is smaller than base image %s (%d bytes): guest filesystem would be truncated",
				targetSizeBytes, basePath, baseSize)
		}
		virtualSize = targetSizeBytes
	}

	extSize := extentSizeBytes
	if extSize == 0 {
		if baseExtentSize > 0 {
			extSize = baseExtentSize
		} else {
			extSize = DefaultExtentSizeBytes
		}
	}

	_, err := Init(overlayDir, virtualSize, extSize)
	return err
}

// Options configures opening or creating an extent Store.
type Options struct {
	// Dir is the path to the disk.extents directory.
	Dir string
	// BasePath is an optional read-only backing source: either a disk.extents
	// directory or a flat raw disk.img file.
	BasePath string
	// VirtualSizeBytes creates or resizes the store to this virtual size if > 0.
	VirtualSizeBytes uint64
	// ExtentSizeBytes sets the extent size when initializing a new store.
	// Defaults to the base's extent size or DefaultExtentSizeBytes (64 MiB).
	ExtentSizeBytes uint64
	// ReadOnly opens the store in read-only mode, rejecting all write operations.
	ReadOnly bool
}

type extentHandle struct {
	mu       sync.RWMutex
	file     *os.File
	writable bool
	absent   bool
	dirty    atomic.Bool
}

// Store manages I/O to an extent directory and its optional read-only base.
type Store struct {
	mu           sync.RWMutex
	dir          string
	basePath     string
	manifest     Manifest
	readOnly     bool
	baseStore    *Store
	baseFlat     *os.File
	baseFlatSize int64
	extents      []*extentHandle
}

// Open opens an existing extent store at opts.Dir (or initializes one if
// opts.VirtualSizeBytes > 0 or opts.BasePath != "" and manifest.json does not exist yet).
func Open(opts Options) (*Store, error) {
	if opts.Dir == "" {
		return nil, errors.New("extents: Options.Dir is required")
	}

	var baseStore *Store
	var baseFlat *os.File
	var baseFlatSize int64
	var baseSize uint64
	var baseExtentSize uint64

	if opts.BasePath != "" {
		if IsExtentDir(opts.BasePath) {
			bs, err := Open(Options{
				Dir:      opts.BasePath,
				ReadOnly: true,
			})
			if err != nil {
				return nil, fmt.Errorf("extents: opening base extent store %s: %w", opts.BasePath, err)
			}
			baseStore = bs
			baseSize = bs.VirtualSize()
			baseExtentSize = bs.ExtentSize()
		} else {
			f, err := os.OpenFile(opts.BasePath, os.O_RDONLY, 0)
			if err != nil {
				return nil, fmt.Errorf("extents: opening base flat image %s: %w", opts.BasePath, err)
			}
			info, err := f.Stat()
			if err != nil {
				f.Close()
				return nil, fmt.Errorf("extents: stat base flat image %s: %w", opts.BasePath, err)
			}
			baseFlat = f
			baseFlatSize = info.Size()
			baseSize = uint64(baseFlatSize)
		}
	}

	closeBase := func() {
		if baseStore != nil {
			_ = baseStore.Close()
		}
		if baseFlat != nil {
			_ = baseFlat.Close()
		}
	}

	var m Manifest
	if IsExtentDir(opts.Dir) {
		var err error
		m, err = ReadManifest(opts.Dir)
		if err != nil {
			closeBase()
			return nil, err
		}
	} else {
		if opts.ReadOnly && opts.BasePath == "" {
			closeBase()
			return nil, fmt.Errorf("extents: manifest not found in %s", opts.Dir)
		}
		vsize := opts.VirtualSizeBytes
		if vsize == 0 {
			vsize = baseSize
		}
		if vsize < baseSize {
			closeBase()
			return nil, fmt.Errorf("extents: virtual size %d is smaller than base size %d: guest filesystem would be truncated", vsize, baseSize)
		}
		esize := opts.ExtentSizeBytes
		if esize == 0 {
			if baseExtentSize > 0 {
				esize = baseExtentSize
			} else {
				esize = DefaultExtentSizeBytes
			}
		}
		m = Manifest{
			Version:          ManifestVersion,
			VirtualSizeBytes: vsize,
			ExtentSizeBytes:  esize,
		}
		if !opts.ReadOnly {
			if err := WriteManifest(opts.Dir, m); err != nil {
				closeBase()
				return nil, err
			}
		}
	}

	numExt := m.NumExtents()
	handles := make([]*extentHandle, numExt)
	for i := range handles {
		handles[i] = &extentHandle{}
	}

	st := &Store{
		dir:          opts.Dir,
		basePath:     opts.BasePath,
		manifest:     m,
		readOnly:     opts.ReadOnly,
		baseStore:    baseStore,
		baseFlat:     baseFlat,
		baseFlatSize: baseFlatSize,
		extents:      handles,
	}

	if opts.VirtualSizeBytes > m.VirtualSizeBytes && !opts.ReadOnly {
		if err := st.Resize(opts.VirtualSizeBytes); err != nil {
			_ = st.Close()
			return nil, err
		}
	}

	return st, nil
}

// Dir returns the extent store directory path.
func (s *Store) Dir() string {
	return s.dir
}

// BasePath returns the backing store path, if any.
func (s *Store) BasePath() string {
	return s.basePath
}

// VirtualSize returns the current logical disk size in bytes.
func (s *Store) VirtualSize() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.manifest.VirtualSizeBytes
}

// ExtentSize returns the extent size in bytes.
func (s *Store) ExtentSize() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.manifest.ExtentSizeBytes
}

// NumExtents returns the number of extents in the store.
func (s *Store) NumExtents() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.manifest.NumExtents()
}

// ReadOnly reports whether the store is read-only.
func (s *Store) ReadOnly() bool {
	return s.readOnly
}

// HasExtent reports whether extent idx is materialized as a file inside s.dir.
func (s *Store) HasExtent(idx uint64) bool {
	s.mu.RLock()
	if idx >= uint64(len(s.extents)) {
		s.mu.RUnlock()
		return false
	}
	h := s.extents[idx]
	s.mu.RUnlock()

	h.mu.RLock()
	if h.file != nil {
		h.mu.RUnlock()
		return true
	}
	if h.absent {
		h.mu.RUnlock()
		return false
	}
	h.mu.RUnlock()

	_, err := os.Stat(ExtentPath(s.dir, idx))
	return err == nil
}

// HasDataAtExtent reports whether either this store or its backing store has
// materialized data for extent idx.
func (s *Store) HasDataAtExtent(idx uint64) bool {
	if s.HasExtent(idx) {
		return true
	}
	return s.baseHasDataAtExtent(idx)
}

func (s *Store) baseHasDataAtExtent(idx uint64) bool {
	if s.baseStore != nil {
		extSize := s.ExtentSize()
		baseExtSize := s.baseStore.ExtentSize()
		if extSize == baseExtSize {
			return s.baseStore.HasDataAtExtent(idx)
		}
		startByte := idx * extSize
		endByte := startByte + extSize
		firstBaseIdx := startByte / baseExtSize
		lastBaseIdx := (endByte - 1) / baseExtSize
		for bIdx := firstBaseIdx; bIdx <= lastBaseIdx; bIdx++ {
			if s.baseStore.HasDataAtExtent(bIdx) {
				return true
			}
		}
		return false
	}
	if s.baseFlat != nil {
		extSize := s.ExtentSize()
		startOff := int64(idx * extSize)
		if startOff >= s.baseFlatSize {
			return false
		}
		endOff := startOff + int64(extSize)
		if endOff > s.baseFlatSize {
			endOff = s.baseFlatSize
		}
		dataOff, err := unix.Seek(int(s.baseFlat.Fd()), startOff, unix.SEEK_DATA)
		if err != nil {
			if errors.Is(err, unix.ENXIO) {
				return false
			}
			return true
		}
		return dataOff < endOff
	}
	return false
}

// ExtentFileMode returns whether extent idx currently has an open file handle
// and whether that handle was opened writable (O_RDWR). Used by tests to verify
// the overlayfs O_RDONLY invariant.
func (s *Store) ExtentFileMode(idx uint64) (writable bool, open bool) {
	s.mu.RLock()
	if idx >= uint64(len(s.extents)) {
		s.mu.RUnlock()
		return false, false
	}
	h := s.extents[idx]
	s.mu.RUnlock()

	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.file == nil {
		return false, false
	}
	return h.writable, true
}

// ExtentFD returns the raw file descriptor for extent idx if open, or -1.
func (s *Store) ExtentFD(idx uint64) int {
	s.mu.RLock()
	if idx >= uint64(len(s.extents)) {
		s.mu.RUnlock()
		return -1
	}
	h := s.extents[idx]
	s.mu.RUnlock()

	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.file == nil {
		return -1
	}
	return int(h.file.Fd())
}

// ReadAt reads len(p) bytes starting at byte offset off.
// Existing extent files are opened strictly O_RDONLY so reading never triggers
// an overlayfs copy-up. Unallocated extents without a base return zeros in O(1).
func (s *Store) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, fmt.Errorf("extents: negative read offset %d", off)
	}

	s.mu.RLock()
	vsize := int64(s.manifest.VirtualSizeBytes)
	extSize := int64(s.manifest.ExtentSizeBytes)
	extents := s.extents
	s.mu.RUnlock()

	if off >= vsize {
		return 0, io.EOF
	}

	maxRead := len(p)
	var finalErr error
	if off+int64(maxRead) > vsize {
		maxRead = int(vsize - off)
		finalErr = io.EOF
	}

	total := 0
	for total < maxRead {
		curOff := off + int64(total)
		idx := uint64(curOff / extSize)
		extOff := curOff % extSize
		chunkLen := int(extSize - extOff)
		if rem := maxRead - total; chunkLen > rem {
			chunkLen = rem
		}

		dst := p[total : total+chunkLen]
		if err := s.readExtentChunk(extents[idx], idx, extOff, curOff, dst); err != nil {
			return total, err
		}
		total += chunkLen
	}

	return total, finalErr
}

// ReadvAt is the vectored ReadAt: it fills bufs in order from off and
// returns the bytes read. A range inside one already-open extent file (or one
// its base serves) is a single preadv(2) straight into the buffers, which for
// a virtio request are guest memory; anything else falls back to ReadAt per
// buffer.
func (s *Store) ReadvAt(bufs [][]byte, off int64) (int, error) {
	if len(bufs) == 1 {
		return s.ReadAt(bufs[0], off)
	}
	total := 0
	for _, b := range bufs {
		total += len(b)
	}
	if total == 0 {
		return 0, nil
	}
	if off >= 0 {
		if n, ok, err := s.preadvExtent(bufs, off, total); ok {
			return n, err
		}
	}
	done := 0
	for _, b := range bufs {
		n, err := s.ReadAt(b, off+int64(done))
		done += n
		if err != nil {
			return done, err
		}
	}
	return done, nil
}

// preadvExtent serves ReadvAt with one preadv(2) when it can; ok is false
// when it cannot and the caller should fall back.
func (s *Store) preadvExtent(bufs [][]byte, off int64, total int) (n int, ok bool, err error) {
	s.mu.RLock()
	vsize := int64(s.manifest.VirtualSizeBytes)
	extSize := int64(s.manifest.ExtentSizeBytes)
	extents := s.extents
	s.mu.RUnlock()
	if extSize <= 0 || off+int64(total) > vsize || off/extSize != (off+int64(total)-1)/extSize {
		return 0, false, nil
	}
	idx := uint64(off / extSize)
	h := extents[idx]
	h.mu.RLock()
	if h.file == nil {
		absent := h.absent
		h.mu.RUnlock()
		if absent && s.baseStore != nil {
			n, err := s.baseStore.ReadvAt(bufs, off)
			return n, true, err
		}
		return 0, false, nil
	}
	extOff := off % extSize
	got := 0
	for got < total {
		m, rerr := unix.Preadv(int(h.file.Fd()), bufs, extOff+int64(got))
		if rerr == unix.EINTR {
			continue
		}
		if rerr != nil {
			h.mu.RUnlock()
			return got, true, rerr
		}
		if m == 0 {
			break
		}
		got += m
		bufs = advanceIovecs(bufs, m)
	}
	h.mu.RUnlock()
	// Past the end of a short extent file reads as zeros, as in ReadAt.
	for _, b := range bufs {
		clear(b)
	}
	return total, true, nil
}

// ReadvAtNowait is ReadvAt for a caller that must not block on the disk:
// it reads only if every byte is already in the page cache
// (preadv2(RWF_NOWAIT)) or reads as zeros, and otherwise reports ok=false
// without having changed anything the caller relies on, so the caller can
// hand the request to a goroutine that may block.
func (s *Store) ReadvAtNowait(bufs [][]byte, off int64) (n int, ok bool) {
	total := 0
	for _, b := range bufs {
		total += len(b)
	}
	if total == 0 || off < 0 {
		return 0, false
	}
	s.mu.RLock()
	vsize := int64(s.manifest.VirtualSizeBytes)
	extSize := int64(s.manifest.ExtentSizeBytes)
	extents := s.extents
	s.mu.RUnlock()
	if extSize <= 0 || off+int64(total) > vsize || off/extSize != (off+int64(total)-1)/extSize {
		return 0, false
	}
	h := extents[off/extSize]
	h.mu.RLock()
	if h.file == nil {
		absent := h.absent
		h.mu.RUnlock()
		switch {
		case !absent:
			return 0, false // not opened yet: the blocking path opens it
		case s.baseStore != nil:
			return s.baseStore.ReadvAtNowait(bufs, off)
		case s.baseFlat != nil:
			return 0, false
		}
		for _, b := range bufs {
			clear(b)
		}
		return total, true
	}
	m, err := unix.Preadv2(int(h.file.Fd()), bufs, off%extSize, unix.RWF_NOWAIT)
	h.mu.RUnlock()
	// A short read is either a partly cached range or the end of a short
	// extent file; the blocking path tells them apart.
	if err != nil || m != total {
		return 0, false
	}
	return total, true
}

// advanceIovecs drops the first n bytes from bufs.
func advanceIovecs(bufs [][]byte, n int) [][]byte {
	for len(bufs) > 0 && n >= len(bufs[0]) {
		n -= len(bufs[0])
		bufs = bufs[1:]
	}
	if len(bufs) > 0 && n > 0 {
		bufs[0] = bufs[0][n:]
	}
	return bufs
}

func (s *Store) readExtentChunk(h *extentHandle, idx uint64, extOff, globalOff int64, dst []byte) error {
	// Fast path: extent handle already opened or known absent under RLock.
	h.mu.RLock()
	if h.file != nil {
		n, err := h.file.ReadAt(dst, extOff)
		h.mu.RUnlock()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if n < len(dst) {
			clear(dst[n:])
		}
		return nil
	}
	if h.absent {
		h.mu.RUnlock()
		return s.readFromBaseOrZero(globalOff, dst)
	}
	h.mu.RUnlock()

	// Slow path: try opening extent file with O_RDONLY (never O_RDWR on read!).
	h.mu.Lock()
	if h.file == nil && !h.absent {
		path := ExtentPath(s.dir, idx)
		f, err := os.OpenFile(path, os.O_RDONLY, 0)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				h.absent = true
			} else {
				h.mu.Unlock()
				return fmt.Errorf("extents: open extent %d for read: %w", idx, err)
			}
		} else {
			h.file = f
			h.writable = false
		}
	}
	if h.absent {
		h.mu.Unlock()
		return s.readFromBaseOrZero(globalOff, dst)
	}
	// Downgrade or read while holding lock, or grab file reference.
	n, err := h.file.ReadAt(dst, extOff)
	h.mu.Unlock()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if n < len(dst) {
		clear(dst[n:])
	}
	return nil
}

func (s *Store) readFromBaseOrZero(globalOff int64, dst []byte) error {
	if s.baseStore != nil {
		n, err := s.baseStore.ReadAt(dst, globalOff)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if n < len(dst) {
			clear(dst[n:])
		}
		return nil
	}
	if s.baseFlat != nil {
		if globalOff >= s.baseFlatSize {
			clear(dst)
			return nil
		}
		readLen := len(dst)
		if globalOff+int64(readLen) > s.baseFlatSize {
			readLen = int(s.baseFlatSize - globalOff)
		}
		n, err := s.baseFlat.ReadAt(dst[:readLen], globalOff)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if n < len(dst) {
			clear(dst[n:])
		}
		return nil
	}
	clear(dst)
	return nil
}

// WriteAt writes len(p) bytes starting at byte offset off.
// An extent file is only opened/promoted to O_RDWR|O_CREATE when a write
// targets that extent.
func (s *Store) WriteAt(p []byte, off int64) (int, error) {
	if s.readOnly {
		return 0, errors.New("extents: write to read-only store")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, fmt.Errorf("extents: negative write offset %d", off)
	}

	s.mu.RLock()
	vsize := int64(s.manifest.VirtualSizeBytes)
	extSize := int64(s.manifest.ExtentSizeBytes)
	manifest := s.manifest
	extents := s.extents
	s.mu.RUnlock()

	if off+int64(len(p)) > vsize {
		return 0, fmt.Errorf("extents: write [%d, %d) exceeds virtual disk size %d", off, off+int64(len(p)), vsize)
	}

	total := 0
	for total < len(p) {
		curOff := off + int64(total)
		idx := uint64(curOff / extSize)
		extOff := curOff % extSize
		extLen := int64(manifest.ExtentLength(idx))
		chunkLen := int(extLen - extOff)
		if rem := len(p) - total; chunkLen > rem {
			chunkLen = rem
		}

		chunk := p[total : total+chunkLen]
		fullExtent := extOff == 0 && int64(chunkLen) == extLen

		// Optimization: if writing all zeros across an entire extent, avoid
		// materializing a physical non-sparse extent file.
		if fullExtent && isAllZero(chunk) {
			if err := s.zeroFullExtent(extents[idx], idx, extLen); err != nil {
				return total, err
			}
			total += chunkLen
			continue
		}

		// Optimization: if writing all zeros to a sub-range of an extent that is
		// currently absent and has no backing data, keep it absent!
		if isAllZero(chunk) && !s.HasDataAtExtent(idx) {
			total += chunkLen
			continue
		}

		if err := s.writeExtentChunk(extents[idx], idx, extOff, extLen, chunk, fullExtent); err != nil {
			return total, err
		}
		total += chunkLen
	}

	return total, nil
}

func (s *Store) writeExtentChunk(h *extentHandle, idx uint64, extOff, extLen int64, chunk []byte, fullExtent bool) error {
	// Fast path: extent is already opened O_RDWR. Hold RLock during WriteAt so
	// concurrent writes to the same extent run in parallel via positional pwrite.
	h.mu.RLock()
	if h.file != nil && h.writable {
		_, err := h.file.WriteAt(chunk, extOff)
		if err == nil {
			h.dirty.Store(true)
		}
		h.mu.RUnlock()
		return err
	}
	h.mu.RUnlock()

	// Slow path: promote or materialize the extent under write lock.
	h.mu.Lock()
	if err := s.promoteExtentLocked(h, idx, extLen, fullExtent); err != nil {
		h.mu.Unlock()
		return err
	}
	_, err := h.file.WriteAt(chunk, extOff)
	if err == nil {
		h.dirty.Store(true)
	}
	h.mu.Unlock()
	return err
}

// promoteExtentLocked ensures h.file is opened O_RDWR and backed by a file of
// size extLen in s.dir. Caller must hold h.mu.Lock().
func (s *Store) promoteExtentLocked(h *extentHandle, idx uint64, extLen int64, overwriteFullExtent bool) error {
	if h.file != nil && h.writable {
		return nil
	}

	path := ExtentPath(s.dir, idx)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	// Case 1: The extent file already exists in s.dir (either opened O_RDONLY
	// earlier, or untouched on disk). Open it with O_RDWR, which triggers
	// overlayfs copy-up only now on first write, then close any old O_RDONLY fd.
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		f, err := os.OpenFile(path, os.O_RDWR, 0o600)
		if err != nil {
			return fmt.Errorf("extents: promoting extent %d to O_RDWR: %w", idx, err)
		}
		if info.Size() < extLen {
			if err := f.Truncate(extLen); err != nil {
				f.Close()
				return fmt.Errorf("extents: truncating extent %d: %w", idx, err)
			}
		}
		if h.file != nil {
			_ = h.file.Close()
		}
		h.file = f
		h.writable = true
		h.absent = false
		return nil
	}

	if h.file != nil {
		_ = h.file.Close()
		h.file = nil
		h.writable = false
	}

	// Case 2: The extent file does not yet exist in s.dir.
	// If we are in overlay mode (s.baseStore != nil or s.baseFlat != nil) and
	// this write does not overwrite the entire extent, materialize the base data first.
	if !overwriteFullExtent {
		if err := s.materializeFromBaseLocked(idx, path, extLen); err != nil {
			return err
		}
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("extents: creating extent %d: %w", idx, err)
	}
	if err := f.Truncate(extLen); err != nil {
		f.Close()
		return fmt.Errorf("extents: sizing extent %d: %w", idx, err)
	}
	h.file = f
	h.writable = true
	h.absent = false
	return nil
}

func (s *Store) materializeFromBaseLocked(idx uint64, dstPath string, extLen int64) error {
	extSize := int64(s.ExtentSize())
	globalOff := int64(idx) * extSize

	if s.baseStore != nil {
		// If the base store has identical extent size, no deeper base of its own,
		// and a physical file for idx, reflink/sparse-copy that file directly.
		if s.baseStore.ExtentSize() == uint64(extSize) && s.baseStore.baseStore == nil && s.baseStore.baseFlat == nil {
			baseExtPath := ExtentPath(s.baseStore.dir, idx)
			if info, err := os.Stat(baseExtPath); err == nil && info.Mode().IsRegular() {
				copyLen := extLen
				if info.Size() < copyLen {
					copyLen = info.Size()
				}
				if _, err := cloneOrCopyRangeNoSync(baseExtPath, dstPath, 0, 0, copyLen, extLen); err != nil {
					return fmt.Errorf("extents: cloning base extent %d: %w", idx, err)
				}
				return nil
			}
			// Base extent is absent (all zeros), so no copy needed.
			return nil
		}

		// General baseStore fallback (e.g., different extent sizes or multi-level base):
		if !s.baseHasDataAtExtent(idx) {
			return nil
		}
		buf := make([]byte, extLen)
		if _, err := s.baseStore.ReadAt(buf, globalOff); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("extents: reading base extent %d: %w", idx, err)
		}
		if isAllZero(buf) {
			return nil
		}
		return writeSparseBuffer(dstPath, buf, extLen)
	}

	if s.baseFlat != nil {
		if globalOff >= s.baseFlatSize {
			return nil
		}
		copyLen := extLen
		if globalOff+copyLen > s.baseFlatSize {
			copyLen = s.baseFlatSize - globalOff
		}
		if _, err := cloneOrCopyRangeNoSync(s.basePath, dstPath, globalOff, 0, copyLen, extLen); err != nil {
			return fmt.Errorf("extents: cloning base flat range for extent %d: %w", idx, err)
		}
	}
	return nil
}

// WriteZeroes zeroes the byte range [off, off+length).
// If an entire extent is zeroed and has no non-zero backing data, its file is
// unlinked from disk; if backed by non-zero base data in overlay mode, a sparse
// zeroed extent file is kept to mask the base.
func (s *Store) WriteZeroes(off, length int64) error {
	if s.readOnly {
		return errors.New("extents: write zeroes on read-only store")
	}
	if length == 0 {
		return nil
	}
	if off < 0 || length < 0 {
		return fmt.Errorf("extents: invalid WriteZeroes range [%d, %d)", off, off+length)
	}

	s.mu.RLock()
	vsize := int64(s.manifest.VirtualSizeBytes)
	extSize := int64(s.manifest.ExtentSizeBytes)
	manifest := s.manifest
	extents := s.extents
	s.mu.RUnlock()

	if off+length > vsize {
		return fmt.Errorf("extents: WriteZeroes [%d, %d) exceeds virtual disk size %d", off, off+length, vsize)
	}

	var done int64
	for done < length {
		curOff := off + done
		idx := uint64(curOff / extSize)
		extOff := curOff % extSize
		extLen := int64(manifest.ExtentLength(idx))
		chunkLen := extLen - extOff
		if rem := length - done; chunkLen > rem {
			chunkLen = rem
		}

		fullExtent := extOff == 0 && chunkLen == extLen
		if fullExtent {
			if err := s.zeroFullExtent(extents[idx], idx, extLen); err != nil {
				return err
			}
		} else {
			if err := s.zeroPartialExtent(extents[idx], idx, extOff, chunkLen, extLen); err != nil {
				return err
			}
		}
		done += chunkLen
	}
	return nil
}

// Discard (TRIM/UNMAP) punches a hole / zeroes the byte range [off, off+length).
func (s *Store) Discard(off, length int64) error {
	return s.WriteZeroes(off, length)
}

// PunchHole is an alias for Discard.
func (s *Store) PunchHole(off, length int64) error {
	return s.WriteZeroes(off, length)
}

func (s *Store) zeroFullExtent(h *extentHandle, idx uint64, extLen int64) error {
	baseHasData := s.baseHasDataAtExtent(idx)

	h.mu.Lock()
	defer h.mu.Unlock()

	path := ExtentPath(s.dir, idx)
	if !baseHasData {
		// No backing data: unlink the extent file so all-zero extents are omitted on disk.
		if h.file != nil {
			_ = h.file.Close()
			h.file = nil
		}
		h.writable = false
		h.absent = true
		h.dirty.Store(false)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("extents: unlinking zeroed extent %d: %w", idx, err)
		}
		return nil
	}

	// Base has data in overlay mode: create/keep a 100% sparse zeroed file of
	// size extLen so reads to this extent return zeros instead of falling
	// through to the non-zero base.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if h.file != nil {
		_ = h.file.Close()
		h.file = nil
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("extents: creating sparse zero overlay extent %d: %w", idx, err)
	}
	if err := f.Truncate(extLen); err != nil {
		f.Close()
		return err
	}
	h.file = f
	h.writable = true
	h.absent = false
	h.dirty.Store(true)
	return nil
}

func (s *Store) zeroPartialExtent(h *extentHandle, idx uint64, extOff, chunkLen, extLen int64) error {
	if !s.HasDataAtExtent(idx) {
		// Already all zeros in both overlay and base.
		return nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if err := s.promoteExtentLocked(h, idx, extLen, false); err != nil {
		return err
	}

	// Try FALLOC_FL_PUNCH_HOLE | FALLOC_FL_KEEP_SIZE first to deallocate blocks.
	err := unix.Fallocate(int(h.file.Fd()), unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, extOff, chunkLen)
	if err != nil {
		// Fallback to explicit zero writes if filesystem does not support punch hole.
		zeroBuf := make([]byte, min(chunkLen, 1<<20))
		var written int64
		for written < chunkLen {
			n := min(int64(len(zeroBuf)), chunkLen-written)
			if _, wErr := h.file.WriteAt(zeroBuf[:n], extOff+written); wErr != nil {
				return wErr
			}
			written += n
		}
	}
	h.dirty.Store(true)
	return nil
}

// Flush synchronizes all extents modified since the last Flush using fdatasync.
func (s *Store) Flush() error {
	if s.readOnly {
		return nil
	}
	s.mu.RLock()
	extents := s.extents
	s.mu.RUnlock()

	for idx, h := range extents {
		if !h.dirty.Swap(false) {
			continue
		}
		h.mu.RLock()
		f := h.file
		if f != nil && h.writable {
			err := unix.Fdatasync(int(f.Fd()))
			h.mu.RUnlock()
			if err != nil {
				h.dirty.Store(true)
				return fmt.Errorf("extents: fdatasync extent %d: %w", idx, err)
			}
		} else {
			h.mu.RUnlock()
		}
	}
	return nil
}

// Resize grows the logical disk size to newSizeBytes and updates manifest.json.
// Shrinking a disk is rejected to prevent truncating an existing guest filesystem.
func (s *Store) Resize(newSizeBytes uint64) error {
	if s.readOnly {
		return errors.New("extents: cannot resize read-only store")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	oldSize := s.manifest.VirtualSizeBytes
	if newSizeBytes < oldSize {
		return fmt.Errorf("extents: cannot shrink store from %d to %d bytes: guest filesystem would be truncated", oldSize, newSizeBytes)
	}
	if newSizeBytes == oldSize {
		return nil
	}

	oldNum := s.manifest.NumExtents()
	newManifest := s.manifest
	newManifest.VirtualSizeBytes = newSizeBytes
	newNum := newManifest.NumExtents()

	// If the previous final extent was smaller than ExtentSizeBytes and already
	// exists on disk, extend its file size to its new logical length.
	if oldNum > 0 {
		lastIdx := oldNum - 1
		newLastLen := int64(newManifest.ExtentLength(lastIdx))
		h := s.extents[lastIdx]
		h.mu.Lock()
		path := ExtentPath(s.dir, lastIdx)
		if h.file != nil && h.writable {
			if err := h.file.Truncate(newLastLen); err != nil {
				h.mu.Unlock()
				return err
			}
		} else if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Size() < newLastLen {
			if err := os.Truncate(path, newLastLen); err != nil {
				h.mu.Unlock()
				return err
			}
		}
		h.mu.Unlock()
	}

	if newNum > uint64(len(s.extents)) {
		grown := make([]*extentHandle, newNum)
		copy(grown, s.extents)
		for i := uint64(len(s.extents)); i < newNum; i++ {
			grown[i] = &extentHandle{}
		}
		s.extents = grown
	}

	if err := WriteManifest(s.dir, newManifest); err != nil {
		return err
	}
	s.manifest = newManifest
	return nil
}

// Close flushes dirty extents and closes all open file descriptors.
func (s *Store) Close() error {
	var firstErr error
	if !s.readOnly {
		if err := s.Flush(); err != nil {
			firstErr = err
		}
	}

	s.mu.Lock()
	extents := s.extents
	s.extents = nil
	baseStore := s.baseStore
	s.baseStore = nil
	baseFlat := s.baseFlat
	s.baseFlat = nil
	s.mu.Unlock()

	for _, h := range extents {
		h.mu.Lock()
		if h.file != nil {
			if err := h.file.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
			h.file = nil
			h.writable = false
		}
		h.mu.Unlock()
	}

	if baseStore != nil {
		if err := baseStore.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if baseFlat != nil {
		if err := baseFlat.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// SplitFromFlat splits a flat raw disk image at flatPath into an extent store
// directory at dstDir with extentSizeBytes per extent (defaulting to 64 MiB if 0).
// Extents that are entirely zero are omitted on disk.
func SplitFromFlat(flatPath, dstDir string, extentSizeBytes uint64) (*Manifest, error) {
	if extentSizeBytes == 0 {
		extentSizeBytes = DefaultExtentSizeBytes
	}
	srcFile, err := os.Open(flatPath)
	if err != nil {
		return nil, fmt.Errorf("extents: open flat image %s: %w", flatPath, err)
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return nil, fmt.Errorf("extents: stat flat image %s: %w", flatPath, err)
	}
	virtualSize := uint64(info.Size())

	if err := os.RemoveAll(dstDir); err != nil {
		return nil, fmt.Errorf("extents: cleaning dstDir %s: %w", dstDir, err)
	}
	m, err := Init(dstDir, virtualSize, extentSizeBytes)
	if err != nil {
		return nil, err
	}

	numExt := m.NumExtents()
	buf := make([]byte, 1<<20) // 1 MiB scan/copy buffer

	for idx := uint64(0); idx < numExt; idx++ {
		extOff := int64(idx * extentSizeBytes)
		extLen := int64(m.ExtentLength(idx))

		hasNonZero, err := rangeHasNonZero(srcFile, extOff, extLen, buf)
		if err != nil {
			return nil, fmt.Errorf("extents: scanning extent %d in %s: %w", idx, flatPath, err)
		}
		if !hasNonZero {
			// Sparse/all-zero omission: do not create extent file on disk.
			continue
		}

		dstPath := ExtentPath(dstDir, idx)
		if _, err := cloneOrCopyRangeNoSync(flatPath, dstPath, extOff, 0, extLen, extLen); err != nil {
			return nil, fmt.Errorf("extents: writing extent %d to %s: %w", idx, dstPath, err)
		}
	}

	return m, nil
}

// FlattenToFlat materializes an extent store directory (including any backing
// store) into a single sparse flat raw disk image at flatPath.
func FlattenToFlat(srcDir, flatPath string) error {
	st, err := Open(Options{
		Dir:      srcDir,
		ReadOnly: true,
	})
	if err != nil {
		return err
	}
	defer st.Close()

	return FlattenStoreToFlat(st, flatPath)
}

// CopyExtentDir copies an extent directory (manifest.json and all materialized
// extents/*.raw files) from srcDir to dstDir, using reflinks or sparse copying.
func CopyExtentDir(srcDir, dstDir string) error {
	m, err := ReadManifest(srcDir)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dstDir); err != nil {
		return err
	}
	if err := WriteManifest(dstDir, m); err != nil {
		return err
	}
	_ = os.Chmod(dstDir, 0o700)
	_ = os.Chmod(filepath.Join(dstDir, ManifestFileName), 0o600)
	_ = os.Chmod(filepath.Join(dstDir, ExtentsSubdir), 0o700)

	entries, err := os.ReadDir(filepath.Join(srcDir, ExtentsSubdir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		srcPath := filepath.Join(srcDir, ExtentsSubdir, entry.Name())
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		dstPath := filepath.Join(dstDir, ExtentsSubdir, entry.Name())
		if _, err := cloneOrCopyRangeNoSync(srcPath, dstPath, 0, 0, info.Size(), info.Size()); err != nil {
			return fmt.Errorf("extents: copying extent %s: %w", entry.Name(), err)
		}
		_ = os.Chmod(dstPath, 0o600)
	}
	return nil
}

// FlattenStoreToFlat materializes an open Store into a sparse flat raw disk image at flatPath.
func FlattenStoreToFlat(st *Store, flatPath string) error {
	if err := os.MkdirAll(filepath.Dir(flatPath), 0o755); err != nil {
		return err
	}
	dstFile, err := os.OpenFile(flatPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("extents: creating flat image %s: %w", flatPath, err)
	}
	defer dstFile.Close()

	vsize := int64(st.VirtualSize())
	if err := dstFile.Truncate(vsize); err != nil {
		return fmt.Errorf("extents: truncating flat image %s to %d: %w", flatPath, vsize, err)
	}

	extSize := int64(st.ExtentSize())
	numExt := st.NumExtents()
	buf := make([]byte, 1<<20) // 1 MiB buffer

	for idx := uint64(0); idx < numExt; idx++ {
		if !st.HasDataAtExtent(idx) {
			continue
		}
		globalOff := int64(idx) * extSize
		extLen := extSize
		if globalOff+extLen > vsize {
			extLen = vsize - globalOff
		}

		// If the extent exists directly in st.dir and no base overrides are needed,
		// try in-kernel FICLONERANGE first, then sparse copy into dstFile.
		extPath := ExtentPath(st.dir, idx)
		if st.HasExtent(idx) {
			if srcExt, err := os.Open(extPath); err == nil {
				req := unix.FileCloneRange{
					Src_fd:      int64(srcExt.Fd()),
					Src_offset:  0,
					Src_length:  uint64(extLen),
					Dest_offset: uint64(globalOff),
				}
				cloneErr := unix.IoctlFileCloneRange(int(dstFile.Fd()), &req)
				if cloneErr == nil {
					srcExt.Close()
					continue
				}
				copyErr := copySparseFileRange(srcExt, dstFile, 0, globalOff, extLen, buf)
				srcExt.Close()
				if copyErr != nil {
					return fmt.Errorf("extents: flattening extent %d: %w", idx, copyErr)
				}
				continue
			}
		}

		// Otherwise read through st.ReadAt and write non-zero chunks.
		var offInExt int64
		for offInExt < extLen {
			toRead := min(int64(len(buf)), extLen-offInExt)
			n, rErr := st.ReadAt(buf[:toRead], globalOff+offInExt)
			if rErr != nil && !errors.Is(rErr, io.EOF) {
				return rErr
			}
			chunk := buf[:n]
			if !isAllZero(chunk) {
				if _, wErr := dstFile.WriteAt(chunk, globalOff+offInExt); wErr != nil {
					return wErr
				}
			}
			offInExt += int64(n)
			if n == 0 {
				break
			}
		}
	}

	return dstFile.Sync()
}

// cloneOrCopyRangeNoSync clones or copies [srcOffset, srcOffset+copyLen) from srcPath
// into dstPath at dstOffset, truncating dstPath to totalDstSize without forcing fsync
// (since Store.Flush performs fdatasync on dirty extents when requested by the guest).
func cloneOrCopyRangeNoSync(srcPath, dstPath string, srcOffset, dstOffset, copyLen, totalDstSize int64) (bool, error) {
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return false, err
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return false, err
	}

	dstFile, err := os.OpenFile(dstPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return false, err
	}
	defer dstFile.Close()

	if srcOffset == 0 && dstOffset == 0 && srcInfo.Size() == copyLen && copyLen == totalDstSize {
		if err := unix.IoctlFileClone(int(dstFile.Fd()), int(srcFile.Fd())); err == nil {
			return true, nil
		}
	}

	if err := dstFile.Truncate(totalDstSize); err != nil {
		return false, err
	}

	if copyLen > 0 {
		req := unix.FileCloneRange{
			Src_fd:      int64(srcFile.Fd()),
			Src_offset:  uint64(srcOffset),
			Src_length:  uint64(copyLen),
			Dest_offset: uint64(dstOffset),
		}
		if err := unix.IoctlFileCloneRange(int(dstFile.Fd()), &req); err == nil {
			return true, nil
		}
	}

	buf := make([]byte, 1<<20)
	if err := copySparseFileRange(srcFile, dstFile, srcOffset, dstOffset, copyLen, buf); err != nil {
		return false, err
	}
	return false, nil
}

func copySparseFileRange(srcFile, dstFile *os.File, srcOffset, dstOffset, copyLen int64, buf []byte) error {
	srcEnd := srcOffset + copyLen
	currSrc := srcOffset

	for currSrc < srcEnd {
		dataOff, err := unix.Seek(int(srcFile.Fd()), currSrc, unix.SEEK_DATA)
		if err != nil {
			if errors.Is(err, unix.ENXIO) {
				break
			}
			return copyLinearNoSync(srcFile, dstFile, currSrc, dstOffset+(currSrc-srcOffset), srcEnd-currSrc, buf)
		}
		if dataOff >= srcEnd {
			break
		}
		holeOff, err := unix.Seek(int(srcFile.Fd()), dataOff, unix.SEEK_HOLE)
		if err != nil {
			return copyLinearNoSync(srcFile, dstFile, dataOff, dstOffset+(dataOff-srcOffset), srcEnd-dataOff, buf)
		}
		dataLen := holeOff - dataOff
		if dataOff+dataLen > srcEnd {
			dataLen = srcEnd - dataOff
		}
		if err := copyLinearNoSync(srcFile, dstFile, dataOff, dstOffset+(dataOff-srcOffset), dataLen, buf); err != nil {
			return err
		}
		currSrc = holeOff
	}
	return nil
}

func copyLinearNoSync(srcFile, dstFile *os.File, srcOff, dstOff, length int64, buf []byte) error {
	var copied int64
	for copied < length {
		toRead := min(int64(len(buf)), length-copied)
		n, rErr := srcFile.ReadAt(buf[:toRead], srcOff+copied)
		if n > 0 {
			chunk := buf[:n]
			if !isAllZero(chunk) {
				if _, wErr := dstFile.WriteAt(chunk, dstOff+copied); wErr != nil {
					return wErr
				}
			}
			copied += int64(n)
		}
		if rErr != nil {
			if errors.Is(rErr, io.EOF) {
				break
			}
			return rErr
		}
	}
	return nil
}

func writeSparseBuffer(dstPath string, data []byte, totalSize int64) error {
	f, err := os.OpenFile(dstPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(totalSize); err != nil {
		return err
	}
	const blockSize = 64 * 1024
	for off := 0; off < len(data); off += blockSize {
		end := min(off+blockSize, len(data))
		chunk := data[off:end]
		if !isAllZero(chunk) {
			if _, err := f.WriteAt(chunk, int64(off)); err != nil {
				return err
			}
		}
	}
	return nil
}

func rangeHasNonZero(f *os.File, startOff, length int64, buf []byte) (bool, error) {
	endOff := startOff + length
	curr := startOff
	for curr < endOff {
		dataOff, err := unix.Seek(int(f.Fd()), curr, unix.SEEK_DATA)
		if err != nil {
			if errors.Is(err, unix.ENXIO) {
				return false, nil
			}
			// Fallback to reading if SEEK_DATA is unsupported.
			return rangeHasNonZeroLinear(f, curr, endOff-curr, buf)
		}
		if dataOff >= endOff {
			return false, nil
		}
		holeOff, err := unix.Seek(int(f.Fd()), dataOff, unix.SEEK_HOLE)
		if err != nil {
			return rangeHasNonZeroLinear(f, dataOff, endOff-dataOff, buf)
		}
		dataLen := min(holeOff, endOff) - dataOff
		nonZero, err := rangeHasNonZeroLinear(f, dataOff, dataLen, buf)
		if err != nil || nonZero {
			return nonZero, err
		}
		curr = holeOff
	}
	return false, nil
}

func rangeHasNonZeroLinear(f *os.File, startOff, length int64, buf []byte) (bool, error) {
	var read int64
	for read < length {
		toRead := min(int64(len(buf)), length-read)
		n, err := f.ReadAt(buf[:toRead], startOff+read)
		if n > 0 {
			if !isAllZero(buf[:n]) {
				return true, nil
			}
			read += int64(n)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return false, err
		}
	}
	return false, nil
}

func isAllZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
