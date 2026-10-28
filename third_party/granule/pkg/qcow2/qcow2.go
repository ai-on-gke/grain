package qcow2

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	// Magic is the 4-byte QCOW2 magic number: "QFI\xfb" (0x514649fb).
	Magic uint32 = 0x514649fb

	// Version is the QCOW2 specification version (3).
	Version uint32 = 3

	// DefaultClusterBits is 21: 2MiB cluster size (1 << 21 = 2097152 bytes).
	// Using 2MB clusters significantly reduces L2 metadata table lookups and overhead.
	DefaultClusterBits uint32 = 21

	// DefaultClusterSize is 2MiB.
	DefaultClusterSize int64 = 1 << DefaultClusterBits

	// MinClusterBits is 9 (512 bytes).
	MinClusterBits uint32 = 9

	// MaxClusterBits is 21 (2 MiB).
	MaxClusterBits uint32 = 21

	// HeaderSize is the size of the basic v3 header without compression padding.
	HeaderSize = 104

	// qcow2RefcountOrder is 4 (16-bit refcount entries, 1 << 4 = 16 bits).
	qcow2RefcountOrder uint32 = 4

	// qcow2RefcountEntryBits is 16 bits (2 bytes).
	qcow2RefcountEntryBits = 16

	// qcow2CompressionTypeAndPaddingSize is 8 bytes.
	qcow2CompressionTypeAndPaddingSize = 8

	// qcow2HeaderLength is the full v3 header length (112 bytes).
	qcow2HeaderLength = HeaderSize + qcow2CompressionTypeAndPaddingSize

	// qcow2ExtBackingFormatMagic is the magic for the Backing Format header extension.
	qcow2ExtBackingFormatMagic uint32 = 0xE2792ACA

	// qcow2ExtEndSize is 8 bytes (magic=0, len=0).
	qcow2ExtEndSize = 8

	// MaxVirtualSize bounds the virtual size a header may declare: 64PiB,
	// well past any disk granule makes, and far enough below 2^63 that
	// sizes and offsets computed from it cannot overflow an int64.
	MaxVirtualSize int64 = 1 << 56

	// maxL1TableBytes is qemu's QCOW_MAX_L1_SIZE: an L1 table larger than
	// 32MiB is refused rather than allocated.
	maxL1TableBytes = 32 << 20

	// maxBackingFileName is qemu's bound on a backing file name.
	maxBackingFileName = 1023

	// maxBackingChain bounds how many backing files Flatten follows. A
	// chain this long is not a disk granule made; a chain that never ends
	// is a file naming itself.
	maxBackingChain = 64

	// MaxBootableBackingChain is how many images deep a chain may be and
	// still boot: cloud-hypervisor opens at most this many and refuses the
	// next with MaxNestingDepthExceeded. Measured, not inferred -- see
	// docs/qcow2-storage-design.md section 2.1 for the method and the
	// error it produces.
	//
	// Distinct from maxBackingChain above, which is a loop guard on a walk
	// and says nothing about what boots. 64 is "this is not a disk we
	// made"; 10 is "the VMM will not open this".
	MaxBootableBackingChain = 10

	// maxStoredBackingChain is how deep a *stored* chain may be, which is
	// one less: PrepareOverlay adds the per-VM overlay on top at boot, and
	// that counts toward the hypervisor's limit like any other layer. A
	// stored chain at MaxBootableBackingChain boots only until something
	// puts a VM on it.
	maxStoredBackingChain = MaxBootableBackingChain - 1

	// DefaultSmallLayerThreshold is 64MiB: the threshold under which an existing
	// qcow2 layer is considered small enough to extend/overwrite in-place instead
	// of adding a new chained layer.
	DefaultSmallLayerThreshold int64 = 64 * 1024 * 1024
)

// Header holds the parsed metadata of a QCOW2 image.
type Header struct {
	Magic                 uint32
	Version               uint32
	BackingFileOffset     uint64
	BackingFileSize       uint32
	ClusterBits           uint32
	ClusterSize           int64
	Size                  int64
	CryptMethod           uint32
	L1Size                uint32
	L1TableOffset         uint64
	RefcountTableOffset   uint64
	RefcountTableClusters uint32
	NbSnapshots           uint32
	SnapshotsOffset       uint64
	IncompatibleFeatures  uint64
	CompatibleFeatures    uint64
	AutoclearFeatures     uint64
	RefcountOrder         uint32
	HeaderLength          uint32
	BackingFile           string
	BackingFormat         string
}

// OverlayOptions configures WriteOverlay behavior.
type OverlayOptions struct {
	ClusterBits   uint32
	BackingFormat string
}

// OverlayOption is a functional option for WriteOverlay.
type OverlayOption func(*OverlayOptions)

// WithClusterBits sets the cluster bits (must be between 9 and 21).
func WithClusterBits(bits uint32) OverlayOption {
	return func(o *OverlayOptions) {
		o.ClusterBits = bits
	}
}

// WithBackingFormat explicitly overrides the backing format string ("raw" or "qcow2").
func WithBackingFormat(format string) OverlayOption {
	return func(o *OverlayOptions) {
		o.BackingFormat = format
	}
}

// IsQcow2 reports whether the file at path exists and begins with the QCOW2 magic bytes.
func IsQcow2(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	return binary.BigEndian.Uint32(magic[:]) == Magic
}

// ReadHeader reads and parses the QCOW2 header from path.
func ReadHeader(path string) (*Header, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	hdr := &Header{}
	buf := make([]byte, qcow2HeaderLength)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, fmt.Errorf("reading header from %s: %w", path, err)
	}

	hdr.Magic = binary.BigEndian.Uint32(buf[0:4])
	if hdr.Magic != Magic {
		return nil, fmt.Errorf("%s is not a qcow2 file (magic 0x%x, want 0x%x)", path, hdr.Magic, Magic)
	}
	hdr.Version = binary.BigEndian.Uint32(buf[4:8])
	if hdr.Version != 2 && hdr.Version != 3 {
		return nil, fmt.Errorf("%s has unsupported qcow2 version %d (want 2 or 3)", path, hdr.Version)
	}

	hdr.BackingFileOffset = binary.BigEndian.Uint64(buf[8:16])
	hdr.BackingFileSize = binary.BigEndian.Uint32(buf[16:20])
	hdr.ClusterBits = binary.BigEndian.Uint32(buf[20:24])
	if hdr.ClusterBits < MinClusterBits || hdr.ClusterBits > MaxClusterBits {
		return nil, fmt.Errorf("%s has invalid cluster bits %d (want %d..%d)", path, hdr.ClusterBits, MinClusterBits, MaxClusterBits)
	}
	hdr.ClusterSize = int64(1) << hdr.ClusterBits

	size := binary.BigEndian.Uint64(buf[24:32])
	if size > uint64(MaxVirtualSize) {
		return nil, fmt.Errorf("%s has virtual size %d, larger than the %d this reads", path, size, uint64(MaxVirtualSize))
	}
	hdr.Size = int64(size)
	hdr.CryptMethod = binary.BigEndian.Uint32(buf[32:36])
	hdr.L1Size = binary.BigEndian.Uint32(buf[36:40])
	hdr.L1TableOffset = binary.BigEndian.Uint64(buf[40:48])
	if err := checkL1(hdr); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	hdr.RefcountTableOffset = binary.BigEndian.Uint64(buf[48:56])
	hdr.RefcountTableClusters = binary.BigEndian.Uint32(buf[56:60])
	hdr.NbSnapshots = binary.BigEndian.Uint32(buf[60:64])
	hdr.SnapshotsOffset = binary.BigEndian.Uint64(buf[64:72])

	if hdr.Version >= 3 {
		hdr.IncompatibleFeatures = binary.BigEndian.Uint64(buf[72:80])
		hdr.CompatibleFeatures = binary.BigEndian.Uint64(buf[80:88])
		hdr.AutoclearFeatures = binary.BigEndian.Uint64(buf[88:96])
		hdr.RefcountOrder = binary.BigEndian.Uint32(buf[96:100])
		hdr.HeaderLength = binary.BigEndian.Uint32(buf[100:104])

		// Read header extensions starting at qcow2HeaderLength. They
		// live in the first cluster, as the specification has it, and
		// nothing past it is read as one: every length below comes out
		// of the file, and a file is not trusted to bound its own reads.
		extOffset := int64(qcow2HeaderLength)
		for extOffset+8 <= hdr.ClusterSize {
			var extHdr [8]byte
			if _, err := f.ReadAt(extHdr[:], extOffset); err != nil {
				break
			}
			extMagic := binary.BigEndian.Uint32(extHdr[0:4])
			extLen := binary.BigEndian.Uint32(extHdr[4:8])
			if extMagic == 0 && extLen == 0 {
				break
			}
			extDataOffset := extOffset + 8
			if int64(extLen) > hdr.ClusterSize-extDataOffset {
				return nil, fmt.Errorf("%s has a header extension (0x%x) of %d bytes running past its first cluster", path, extMagic, extLen)
			}
			if extMagic == qcow2ExtBackingFormatMagic && extLen > 0 {
				formatBuf := make([]byte, extLen)
				if _, err := f.ReadAt(formatBuf, extDataOffset); err == nil {
					hdr.BackingFormat = string(formatBuf)
				}
			}
			paddedLen := (int64(extLen) + 7) &^ 7
			extOffset = extDataOffset + paddedLen
		}
	}

	if hdr.BackingFileOffset > 0 && hdr.BackingFileSize > 0 {
		// The same bounds qemu holds a backing file name to: at most
		// 1023 bytes, and inside the first cluster.
		if hdr.BackingFileSize > maxBackingFileName ||
			hdr.BackingFileOffset > uint64(hdr.ClusterSize) ||
			uint64(hdr.BackingFileSize) > uint64(hdr.ClusterSize)-hdr.BackingFileOffset {
			return nil, fmt.Errorf("%s has a %d-byte backing file name at offset %d, outside its first cluster or longer than %d bytes", path, hdr.BackingFileSize, hdr.BackingFileOffset, maxBackingFileName)
		}
		bfBuf := make([]byte, hdr.BackingFileSize)
		if _, err := f.ReadAt(bfBuf, int64(hdr.BackingFileOffset)); err == nil {
			hdr.BackingFile = string(bfBuf)
		}
	}

	return hdr, nil
}

// checkL1 holds a header's L1 table to what its own virtual size needs:
// no smaller (an L1 that does not cover the disk leaves guest offsets no
// table describes) and no larger than qemu's own 32MiB bound, since every
// reader of the table allocates it whole. The table also has to start on
// a cluster boundary and sit where a signed file offset can reach it.
func checkL1(hdr *Header) error {
	coverage := hdr.ClusterSize * (hdr.ClusterSize / 8)
	need := (hdr.Size + coverage - 1) / coverage
	if int64(hdr.L1Size) < need {
		return fmt.Errorf("L1 table of %d entries does not cover its %d-byte virtual size (needs %d)", hdr.L1Size, hdr.Size, need)
	}
	if int64(hdr.L1Size)*8 > maxL1TableBytes {
		return fmt.Errorf("L1 table of %d entries is larger than the %d bytes this reads", hdr.L1Size, maxL1TableBytes)
	}
	if hdr.L1Size > 0 && (hdr.L1TableOffset == 0 || hdr.L1TableOffset%uint64(hdr.ClusterSize) != 0 ||
		hdr.L1TableOffset > uint64(MaxVirtualSize)) {
		return fmt.Errorf("L1 table offset %d is not a cluster-aligned offset in the file", hdr.L1TableOffset)
	}
	return nil
}

// VirtualSize returns the virtual disk size in bytes for path.
// If path is a QCOW2 image, it reads the virtual size from its header.
// Otherwise, it returns the size of the flat file on disk.
func VirtualSize(path string) (int64, error) {
	if IsQcow2(path) {
		hdr, err := ReadHeader(path)
		if err != nil {
			return 0, err
		}
		return hdr.Size, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("statting %s: %w", path, err)
	}
	return info.Size(), nil
}

// WriteOverlay creates a new QCOW2 v3 overlay at dest.
// If backingFile is non-empty, the overlay points to backingFile as its backing store.
// If opts does not specify cluster bits, it defaults to DefaultClusterBits (21, 2MiB clusters).
func WriteOverlay(dest, backingFile string, sizeBytes int64, opts ...OverlayOption) error {
	opt := OverlayOptions{
		ClusterBits: DefaultClusterBits,
	}
	for _, fn := range opts {
		fn(&opt)
	}

	if opt.ClusterBits < MinClusterBits || opt.ClusterBits > MaxClusterBits {
		return fmt.Errorf("invalid cluster bits %d (must be between %d and %d)", opt.ClusterBits, MinClusterBits, MaxClusterBits)
	}
	clusterSize := int64(1) << opt.ClusterBits

	if sizeBytes <= 0 {
		if backingFile == "" {
			return fmt.Errorf("sizeBytes must be positive when no backing file is specified")
		}
		vSize, err := VirtualSize(backingFile)
		if err != nil {
			return fmt.Errorf("detecting virtual size of backing file %s: %w", backingFile, err)
		}
		sizeBytes = vSize
	}

	if sizeBytes > MaxVirtualSize {
		return fmt.Errorf("virtual size %d is larger than the %d a qcow2 disk here may be", sizeBytes, MaxVirtualSize)
	}
	if len(backingFile) > maxBackingFileName {
		return fmt.Errorf("backing file name is %d bytes, longer than the %d a qcow2 header may carry", len(backingFile), maxBackingFileName)
	}

	backingFormat := opt.BackingFormat
	if backingFile != "" && backingFormat == "" {
		if IsQcow2(backingFile) {
			backingFormat = "qcow2"
		} else {
			backingFormat = "raw"
		}
	}

	l2EntriesPerTable := clusterSize / 8
	l1CoverageBytes := clusterSize * l2EntriesPerTable
	l1Size := (sizeBytes + l1CoverageBytes - 1) / l1CoverageBytes
	if l1Size < 1 {
		l1Size = 1
	}

	l1TableClusters := (l1Size*8 + clusterSize - 1) / clusterSize
	if l1TableClusters < 1 {
		l1TableClusters = 1
	}

	// Layout of initial clusters:
	// Cluster 0: Header + extensions + backing file name
	// Cluster 1: Refcount Table (1 cluster)
	// Cluster 2: Refcount Block (1 cluster)
	// Cluster 3..(3+l1TableClusters-1): L1 Table
	totalClusters := 3 + l1TableClusters

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", dest, err)
	}

	// 0600: the overlay is where everything the guest writes to its disk
	// lands, which is the guest's to read and nobody else's -- the same
	// mode CloneFileReflink gives the raw overlay it makes instead.
	// O_EXCL also refuses to follow a symlink planted at dest.
	f, err := os.OpenFile(dest, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("creating overlay %s: %w", dest, err)
	}
	defer func() {
		if f != nil {
			f.Close()
		}
	}()

	// Compute extensions and backing file offsets in Cluster 0
	var extBytes []byte
	if backingFormat != "" {
		extLen := len(backingFormat)
		paddedLen := (extLen + 7) &^ 7
		ext := make([]byte, 8+paddedLen)
		binary.BigEndian.PutUint32(ext[0:4], qcow2ExtBackingFormatMagic)
		binary.BigEndian.PutUint32(ext[4:8], uint32(extLen))
		copy(ext[8:], backingFormat)
		extBytes = append(extBytes, ext...)
	}
	endExt := make([]byte, qcow2ExtEndSize)
	extBytes = append(extBytes, endExt...)

	backingFileOffset := uint64(0)
	backingFileSize := uint32(0)
	if backingFile != "" {
		backingFileOffset = uint64(qcow2HeaderLength + len(extBytes))
		backingFileSize = uint32(len(backingFile))
	}

	hdr := make([]byte, qcow2HeaderLength)
	binary.BigEndian.PutUint32(hdr[0:4], Magic)
	binary.BigEndian.PutUint32(hdr[4:8], Version)
	binary.BigEndian.PutUint64(hdr[8:16], backingFileOffset)
	binary.BigEndian.PutUint32(hdr[16:20], backingFileSize)
	binary.BigEndian.PutUint32(hdr[20:24], opt.ClusterBits)
	binary.BigEndian.PutUint64(hdr[24:32], uint64(sizeBytes))
	binary.BigEndian.PutUint32(hdr[32:36], 0) // crypt_method: none
	binary.BigEndian.PutUint32(hdr[36:40], uint32(l1Size))
	binary.BigEndian.PutUint64(hdr[40:48], uint64(3*clusterSize)) // L1 table offset
	binary.BigEndian.PutUint64(hdr[48:56], uint64(1*clusterSize)) // Refcount table offset
	binary.BigEndian.PutUint32(hdr[56:60], 1)                     // Refcount table clusters: 1
	binary.BigEndian.PutUint32(hdr[60:64], 0)                     // nb_snapshots: 0
	binary.BigEndian.PutUint64(hdr[64:72], 0)                     // snapshots_offset: 0
	binary.BigEndian.PutUint64(hdr[72:80], 0)                     // incompatible_features
	binary.BigEndian.PutUint64(hdr[80:88], 0)                     // compatible_features
	binary.BigEndian.PutUint64(hdr[88:96], 0)                     // autoclear_features
	binary.BigEndian.PutUint32(hdr[96:100], qcow2RefcountOrder)   // refcount_order: 4 (16-bit)
	binary.BigEndian.PutUint32(hdr[100:104], qcow2HeaderLength)   // header_length: 112
	// bytes 104..111 are compression type (0) and 7 bytes padding

	// Assemble cluster 0
	cluster0 := make([]byte, clusterSize)
	copy(cluster0[0:qcow2HeaderLength], hdr)
	if len(extBytes) > 0 {
		copy(cluster0[qcow2HeaderLength:], extBytes)
	}
	if backingFile != "" {
		copy(cluster0[backingFileOffset:], backingFile)
	}

	if _, err := f.WriteAt(cluster0, 0); err != nil {
		return fmt.Errorf("writing cluster 0 to %s: %w", dest, err)
	}

	// Refcount table (Cluster 1): 1 entry pointing to Cluster 2
	refTable := make([]byte, clusterSize)
	binary.BigEndian.PutUint64(refTable[0:8], uint64(2*clusterSize))
	if _, err := f.WriteAt(refTable, 1*clusterSize); err != nil {
		return fmt.Errorf("writing refcount table to %s: %w", dest, err)
	}

	// Refcount block (Cluster 2): mark clusters 0..(totalClusters-1) with refcount 1
	refBlock := make([]byte, clusterSize)
	for i := int64(0); i < totalClusters; i++ {
		binary.BigEndian.PutUint16(refBlock[i*2:i*2+2], 1)
	}
	if _, err := f.WriteAt(refBlock, 2*clusterSize); err != nil {
		return fmt.Errorf("writing refcount block to %s: %w", dest, err)
	}

	// L1 Table (Cluster 3..): zeroed
	l1TableTotalBytes := l1TableClusters * clusterSize
	if err := f.Truncate(3*clusterSize + l1TableTotalBytes); err != nil {
		return fmt.Errorf("truncating qcow2 %s to %d: %w", dest, 3*clusterSize+l1TableTotalBytes, err)
	}

	if err := f.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", dest, err)
	}

	return f.Close()
}

// Create creates a standalone QCOW2 image at dest with no backing file.
func Create(dest string, sizeBytes int64, opts ...OverlayOption) error {
	return WriteOverlay(dest, "", sizeBytes, opts...)
}

// Resize grows the virtual size of a QCOW2 image in place.
func Resize(path string, sizeBytes int64) error {
	hdr, err := ReadHeader(path)
	if err != nil {
		return fmt.Errorf("reading header of %s: %w", path, err)
	}

	if sizeBytes == hdr.Size {
		return nil
	}
	if sizeBytes < hdr.Size {
		return fmt.Errorf("resizing %s to %d bytes: image is already %d bytes: shrinking a qcow2 disk is not supported", path, sizeBytes, hdr.Size)
	}
	if sizeBytes > MaxVirtualSize {
		return fmt.Errorf("resizing %s to %d bytes: larger than the %d a qcow2 disk here may be", path, sizeBytes, MaxVirtualSize)
	}

	l2EntriesPerTable := hdr.ClusterSize / 8
	l1CoverageBytes := hdr.ClusterSize * l2EntriesPerTable
	newL1Size := (sizeBytes + l1CoverageBytes - 1) / l1CoverageBytes
	if newL1Size < 1 {
		newL1Size = 1
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("opening %s for resize: %w", path, err)
	}
	defer f.Close()

	// Growing l1_size only rewrites the number: the table itself stays
	// where it is, in the clusters allocated for the old size. Entries
	// past those clusters would be read out of whatever follows them --
	// refcount blocks, L2 tables, guest data -- as L1 entries, so a
	// resize that needs more table than was allocated is refused rather
	// than written.
	l1Allocated := (int64(hdr.L1Size)*8 + hdr.ClusterSize - 1) / hdr.ClusterSize * hdr.ClusterSize
	if newL1Size*8 > l1Allocated {
		return fmt.Errorf("resizing %s to %d bytes needs an L1 table of %d entries, and the image has room for %d: growing the table itself is not supported", path, sizeBytes, newL1Size, l1Allocated/8)
	}

	if uint32(newL1Size) > hdr.L1Size {
		var sizeBuf [4]byte
		binary.BigEndian.PutUint32(sizeBuf[:], uint32(newL1Size))
		if _, err := f.WriteAt(sizeBuf[:], 36); err != nil {
			return fmt.Errorf("writing new l1_size to %s: %w", path, err)
		}
	}

	var sizeBytesBuf [8]byte
	binary.BigEndian.PutUint64(sizeBytesBuf[:], uint64(sizeBytes))
	if _, err := f.WriteAt(sizeBytesBuf[:], 24); err != nil {
		return fmt.Errorf("writing new size to %s: %w", path, err)
	}

	return f.Sync()
}

// IsLayerSmall reports whether path is a QCOW2 image whose on-disk size is less
// than or equal to thresholdBytes. If thresholdBytes <= 0, DefaultSmallLayerThreshold is used.
func IsLayerSmall(path string, thresholdBytes int64) (bool, error) {
	if thresholdBytes <= 0 {
		thresholdBytes = DefaultSmallLayerThreshold
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.Size() <= thresholdBytes, nil
}

// AddLayer creates a new QCOW2 layer or extends an existing one if it is small.
// If parentBacking is an existing QCOW2 image whose size <= thresholdBytes,
// it extends parentBacking (if sizeBytes > parent size) and returns extended=true.
// Otherwise, it creates a new QCOW2 overlay at dest with parentBacking as backing file.
func AddLayer(dest, parentBacking string, sizeBytes int64, thresholdBytes int64) (extended bool, outPath string, err error) {
	if thresholdBytes <= 0 {
		thresholdBytes = DefaultSmallLayerThreshold
	}

	if parentBacking != "" && IsQcow2(parentBacking) {
		small, err := IsLayerSmall(parentBacking, thresholdBytes)
		if err == nil && small {
			if sizeBytes > 0 {
				vSize, err := VirtualSize(parentBacking)
				if err == nil && sizeBytes > vSize {
					if err := Resize(parentBacking, sizeBytes); err != nil {
						return false, "", fmt.Errorf("extending small layer %s: %w", parentBacking, err)
					}
				}
			}
			return true, parentBacking, nil
		}
	}

	// Refuse a layer that would make the chain unbootable, rather than
	// writing one. An over-deep chain is not a slow disk: cloud-hypervisor
	// refuses to open it and the VM never starts, with nothing before boot
	// reporting why. The depth is knowable here for the price of a walk,
	// and a caller that gets an error now can flatten instead.
	if parentBacking != "" {
		depth, err := ChainDepth(parentBacking)
		if err != nil {
			return false, "", fmt.Errorf("measuring %s's backing chain before adding a layer: %w", parentBacking, err)
		}
		if depth+1 > maxStoredBackingChain {
			return false, "", fmt.Errorf(
				"adding a layer over %s would make the stored chain %d images deep, past the %d a stored chain may be: "+
					"cloud-hypervisor opens at most %d and the per-VM overlay takes one of them, so this chain would not boot -- "+
					"flatten it first (granule guest flatten)",
				parentBacking, depth+1, maxStoredBackingChain, MaxBootableBackingChain)
		}
	}

	if err := WriteOverlay(dest, parentBacking, sizeBytes); err != nil {
		return false, "", err
	}
	return false, dest, nil
}

// Flatten replays a QCOW2 disk and its entire backing chain into a single flat raw image at destFlatPath.
func Flatten(qcowPath, destFlatPath string) error {
	chain, err := backingChain(qcowPath)
	if err != nil {
		return err
	}
	return FlattenChain(chain, destFlatPath)
}

// ChainDepth reports how many images deep path's backing chain is,
// counting path itself: 1 for a raw disk or a qcow2 with no backing file,
// 2 for one overlay over a base, and so on. Compare it against
// MaxBootableBackingChain to know whether a chain will boot.
//
// It walks with the same distrust backingChain does, because the depth of
// a chain that loops or leaves regular files is not a number worth
// returning.
func ChainDepth(path string) (int, error) {
	chain, err := backingChain(path)
	if err != nil {
		return 0, err
	}
	return len(chain), nil
}

// backingChain walks path's backing chain and returns it ordered from the
// root base to path itself.
//
// Nothing the images say about each other is trusted. The backing file
// name comes out of the image, so it is not trusted to end the walk or to
// name something that is a disk: a file naming itself would loop here
// forever, and a name like /dev/sda would be read whole by a caller that
// flattens.
func backingChain(qcowPath string) ([]string, error) {
	var chain []string
	seen := map[string]bool{}
	curr := qcowPath
	for curr != "" {
		// The backing file name comes out of the image, so it is not
		// trusted to end the walk or to name something that is a disk:
		// a file naming itself would loop here forever, and a name like
		// /dev/sda would be read whole into the output.
		info, err := os.Stat(curr)
		if err != nil {
			return nil, fmt.Errorf("reading backing chain: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("reading backing chain: %s is not a regular file", curr)
		}
		key := filepath.Clean(curr)
		if resolved, err := filepath.EvalSymlinks(curr); err == nil {
			key = resolved
		}
		if seen[key] {
			return nil, fmt.Errorf("reading backing chain: %s appears in its own backing chain", curr)
		}
		seen[key] = true
		if len(chain) >= maxBackingChain {
			return nil, fmt.Errorf("reading backing chain from %s: more than %d images deep", qcowPath, maxBackingChain)
		}
		chain = append([]string{curr}, chain...)
		if !IsQcow2(curr) {
			break
		}
		hdr, err := ReadHeader(curr)
		if err != nil {
			return nil, fmt.Errorf("reading backing chain from %s: %w", curr, err)
		}
		if hdr.BackingFile == "" {
			break
		}
		backing := hdr.BackingFile
		if !filepath.IsAbs(backing) {
			backing = filepath.Join(filepath.Dir(curr), backing)
		}
		curr = backing
	}

	return chain, nil
}

// FlattenChain replays an ordered chain of disk images (from root to top child)
// into a single flat raw image at destFlatPath.
func FlattenChain(chain []string, destFlatPath string) error {
	if len(chain) == 0 {
		return errors.New("empty disk chain")
	}

	topPath := chain[len(chain)-1]
	topSize, err := VirtualSize(topPath)
	if err != nil {
		return fmt.Errorf("reading virtual size of top disk %s: %w", topPath, err)
	}

	rootPath := chain[0]
	if err := os.MkdirAll(filepath.Dir(destFlatPath), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", destFlatPath, err)
	}

	if !IsQcow2(rootPath) {
		// Root is a flat raw file: copy or reflink it to destFlatPath
		if _, err := CloneFileReflink(rootPath, destFlatPath); err != nil {
			return fmt.Errorf("cloning base flat file %s to %s: %w", rootPath, destFlatPath, err)
		}
		if err := os.Truncate(destFlatPath, topSize); err != nil {
			return fmt.Errorf("sizing %s to %d: %w", destFlatPath, topSize, err)
		}
	} else {
		// Root is qcow2: create fresh flat file
		f, err := os.OpenFile(destFlatPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return fmt.Errorf("creating flat file %s: %w", destFlatPath, err)
		}
		if err := f.Truncate(topSize); err != nil {
			f.Close()
			return fmt.Errorf("sizing flat file %s to %d: %w", destFlatPath, topSize, err)
		}
		f.Close()
	}

	destFile, err := os.OpenFile(destFlatPath, os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("opening flat file %s for replay: %w", destFlatPath, err)
	}
	defer destFile.Close()

	// Replay all qcow2 disks in the chain from oldest to newest
	for _, diskPath := range chain {
		if !IsQcow2(diskPath) {
			continue
		}
		if err := replayQcowClusters(diskPath, destFile); err != nil {
			return fmt.Errorf("replaying qcow %s to flat file: %w", diskPath, err)
		}
	}

	return destFile.Sync()
}

func replayQcowClusters(qcowPath string, destFile *os.File) error {
	f, err := os.Open(qcowPath)
	if err != nil {
		return err
	}
	defer f.Close()

	hdr, err := ReadHeader(qcowPath)
	if err != nil {
		return err
	}

	clusterSize := hdr.ClusterSize
	l2EntriesPerTable := clusterSize / 8
	l1Size := int64(hdr.L1Size)

	l1Buf := make([]byte, l1Size*8)
	if _, err := f.ReadAt(l1Buf, int64(hdr.L1TableOffset)); err != nil {
		return fmt.Errorf("reading L1 table from %s: %w", qcowPath, err)
	}

	clusterBuf := make([]byte, clusterSize)
	l2Buf := make([]byte, clusterSize)

	for i := int64(0); i < l1Size; i++ {
		l1Entry := binary.BigEndian.Uint64(l1Buf[i*8 : i*8+8])
		if l1Entry == 0 {
			continue
		}
		l2Offset := int64(l1Entry & 0x00fffffffffffe00)
		if l2Offset == 0 {
			continue
		}

		if _, err := f.ReadAt(l2Buf, l2Offset); err != nil {
			return fmt.Errorf("reading L2 table at %d from %s: %w", l2Offset, qcowPath, err)
		}

		for j := int64(0); j < l2EntriesPerTable; j++ {
			guestOffset := (i*l2EntriesPerTable + j) * clusterSize
			if guestOffset >= hdr.Size {
				break
			}

			l2Entry := binary.BigEndian.Uint64(l2Buf[j*8 : j*8+8])
			if l2Entry == 0 {
				continue
			}

			// Bit 62 is compressed cluster
			if (l2Entry & (1 << 62)) != 0 {
				// Handle compressed if needed
				continue
			}

			// Bit 0 is all-zero cluster marker
			if (l2Entry & 1) != 0 {
				continue
			}

			hostOffset := int64(l2Entry & 0x00fffffffffffe00)
			if hostOffset == 0 {
				continue
			}

			bytesToCopy := clusterSize
			if guestOffset+bytesToCopy > hdr.Size {
				bytesToCopy = hdr.Size - guestOffset
			}

			if _, err := f.ReadAt(clusterBuf[:bytesToCopy], hostOffset); err != nil {
				return fmt.Errorf("reading cluster at %d from %s: %w", hostOffset, qcowPath, err)
			}

			if _, err := destFile.WriteAt(clusterBuf[:bytesToCopy], guestOffset); err != nil {
				return fmt.Errorf("writing cluster to guest offset %d: %w", guestOffset, err)
			}
		}
	}

	return nil
}
