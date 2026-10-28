package qcow2

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteOverlay_DefaultClusterSize2MB(t *testing.T) {
	dir := t.TempDir()
	overlayPath := filepath.Join(dir, "disk.qcow2")

	sizeBytes := int64(10 * 1024 * 1024 * 1024) // 10 GiB
	if err := WriteOverlay(overlayPath, "", sizeBytes); err != nil {
		t.Fatalf("WriteOverlay failed: %v", err)
	}

	if !IsQcow2(overlayPath) {
		t.Fatalf("IsQcow2 returned false for created image")
	}

	hdr, err := ReadHeader(overlayPath)
	if err != nil {
		t.Fatalf("ReadHeader failed: %v", err)
	}

	if hdr.ClusterBits != 21 {
		t.Errorf("ClusterBits = %d, want 21 (2MB)", hdr.ClusterBits)
	}
	if hdr.ClusterSize != 2*1024*1024 {
		t.Errorf("ClusterSize = %d, want %d", hdr.ClusterSize, 2*1024*1024)
	}
	if hdr.Size != sizeBytes {
		t.Errorf("Size = %d, want %d", hdr.Size, sizeBytes)
	}

	vSize, err := VirtualSize(overlayPath)
	if err != nil {
		t.Fatalf("VirtualSize failed: %v", err)
	}
	if vSize != sizeBytes {
		t.Errorf("VirtualSize = %d, want %d", vSize, sizeBytes)
	}
}

// The overlay holds everything the guest writes, so only its owner may
// read it, and a symlink left where it is to go is refused rather than
// followed.
func TestWriteOverlay_PrivateAndNoSymlinkFollow(t *testing.T) {
	dir := t.TempDir()
	overlayPath := filepath.Join(dir, "disk.qcow2")
	if err := WriteOverlay(overlayPath, "", 64*1024*1024); err != nil {
		t.Fatalf("WriteOverlay failed: %v", err)
	}
	info, err := os.Stat(overlayPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("overlay mode = %o, want no group or other access", perm)
	}

	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked.qcow2")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteOverlay(link, "", 64*1024*1024); err == nil {
		t.Errorf("WriteOverlay through a symlink succeeded, want it refused")
	}
	if got, _ := os.ReadFile(target); string(got) != "keep" {
		t.Errorf("symlink target was overwritten: %q", got)
	}
}

func TestWriteOverlay_WithRawBackingFile(t *testing.T) {
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "base.img")
	overlayPath := filepath.Join(dir, "overlay.qcow2")

	rawSize := int64(500 * 1024 * 1024) // 500 MiB
	if err := os.WriteFile(rawPath, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(rawPath, rawSize); err != nil {
		t.Fatal(err)
	}

	if err := WriteOverlay(overlayPath, rawPath, rawSize); err != nil {
		t.Fatalf("WriteOverlay with raw backing failed: %v", err)
	}

	hdr, err := ReadHeader(overlayPath)
	if err != nil {
		t.Fatalf("ReadHeader failed: %v", err)
	}

	if hdr.BackingFile != rawPath {
		t.Errorf("BackingFile = %q, want %q", hdr.BackingFile, rawPath)
	}
	if hdr.BackingFormat != "raw" {
		t.Errorf("BackingFormat = %q, want %q", hdr.BackingFormat, "raw")
	}
}

func TestWriteOverlay_WithQcowBackingFile(t *testing.T) {
	dir := t.TempDir()
	baseQcow := filepath.Join(dir, "base.qcow2")
	overlayPath := filepath.Join(dir, "overlay.qcow2")

	size := int64(1 * 1024 * 1024 * 1024)
	if err := Create(baseQcow, size); err != nil {
		t.Fatal(err)
	}

	if err := WriteOverlay(overlayPath, baseQcow, size); err != nil {
		t.Fatalf("WriteOverlay with qcow backing failed: %v", err)
	}

	hdr, err := ReadHeader(overlayPath)
	if err != nil {
		t.Fatalf("ReadHeader failed: %v", err)
	}

	if hdr.BackingFile != baseQcow {
		t.Errorf("BackingFile = %q, want %q", hdr.BackingFile, baseQcow)
	}
	if hdr.BackingFormat != "qcow2" {
		t.Errorf("BackingFormat = %q, want %q", hdr.BackingFormat, "qcow2")
	}
}

func TestResize(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "disk.qcow2")

	initialSize := int64(10 * 1024 * 1024 * 1024)
	if err := Create(p, initialSize); err != nil {
		t.Fatal(err)
	}

	newSize := int64(20 * 1024 * 1024 * 1024)
	if err := Resize(p, newSize); err != nil {
		t.Fatalf("Resize failed: %v", err)
	}

	hdr, err := ReadHeader(p)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Size != newSize {
		t.Errorf("Size after resize = %d, want %d", hdr.Size, newSize)
	}

	// Shrink should fail
	if err := Resize(p, initialSize); err == nil {
		t.Errorf("Resize shrinking did not fail")
	}
}

func TestAddLayer_SmallLayerOptimization(t *testing.T) {
	dir := t.TempDir()
	layer1 := filepath.Join(dir, "layer1.qcow2")
	layer2 := filepath.Join(dir, "layer2.qcow2")

	size := int64(5 * 1024 * 1024 * 1024)
	if err := Create(layer1, size); err != nil {
		t.Fatal(err)
	}

	// Initial layer1 is small (empty metadata only, on-disk size < 64MB)
	extended, outPath, err := AddLayer(layer2, layer1, size, 64*1024*1024)
	if err != nil {
		t.Fatalf("AddLayer failed: %v", err)
	}
	if !extended {
		t.Errorf("AddLayer expected to extend small layer, but did not")
	}
	if outPath != layer1 {
		t.Errorf("outPath = %q, want %q", outPath, layer1)
	}

	// Simulate large layer by writing dummy data to layer1 so file size > threshold
	f, err := os.OpenFile(layer1, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	largeData := make([]byte, 70*1024*1024)
	if _, err := f.Write(largeData); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Now AddLayer should create a new child layer
	layer3 := filepath.Join(dir, "layer3.qcow2")
	extended, outPath, err = AddLayer(layer3, layer1, size, 64*1024*1024)
	if err != nil {
		t.Fatalf("AddLayer on large parent failed: %v", err)
	}
	if extended {
		t.Errorf("AddLayer extended large parent instead of creating new layer")
	}
	if outPath != layer3 {
		t.Errorf("outPath = %q, want %q", outPath, layer3)
	}

	hdr, err := ReadHeader(layer3)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.BackingFile != layer1 {
		t.Errorf("layer3 backing file = %q, want %q", hdr.BackingFile, layer1)
	}
}

func TestFlatten(t *testing.T) {
	dir := t.TempDir()
	baseRaw := filepath.Join(dir, "base.img")
	layer1 := filepath.Join(dir, "layer1.qcow2")
	layer2 := filepath.Join(dir, "layer2.qcow2")
	flatOut := filepath.Join(dir, "flattened.img")

	clusterSize := int64(2 * 1024 * 1024) // 2MB
	diskSize := 4 * clusterSize           // 8MB

	// 1. Create base raw file with known pattern
	baseData := make([]byte, diskSize)
	for i := range baseData {
		baseData[i] = 0xAA
	}
	if err := os.WriteFile(baseRaw, baseData, 0o644); err != nil {
		t.Fatal(err)
	}

	// 2. Create layer1 overlay backed by baseRaw
	if err := WriteOverlay(layer1, baseRaw, diskSize); err != nil {
		t.Fatal(err)
	}

	// Write cluster 1 in layer1 with 0xBB pattern
	// Let's populate L2 table and write host cluster in layer1
	f1, err := os.OpenFile(layer1, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	hdr1, _ := ReadHeader(layer1)
	// Cluster layout: 0=Hdr, 1=RefTbl, 2=RefBlk, 3=L1, 4=L2, 5=Data
	l2Offset := int64(4 * clusterSize)
	data1Offset := int64(5 * clusterSize)

	// Update L1 entry 0 to point to L2 cluster 4
	var l1Entry [8]byte
	binary.BigEndian.PutUint64(l1Entry[:], uint64(l2Offset))
	f1.WriteAt(l1Entry[:], int64(hdr1.L1TableOffset))

	// In L2 table, entry 1 (guest cluster 1, offset 2MB) points to host cluster 5
	var l2Entry [8]byte
	binary.BigEndian.PutUint64(l2Entry[:], uint64(data1Offset)|(1<<63)) // COPIED flag
	f1.WriteAt(l2Entry[:], l2Offset+8)

	// Write data for cluster 1
	c1Data := bytes.Repeat([]byte{0xBB}, int(clusterSize))
	f1.WriteAt(c1Data, data1Offset)
	f1.Close()

	// 3. Create layer2 overlay backed by layer1
	if err := WriteOverlay(layer2, layer1, diskSize); err != nil {
		t.Fatal(err)
	}

	// In layer2, write cluster 2 with 0xCC pattern
	f2, err := os.OpenFile(layer2, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	hdr2, _ := ReadHeader(layer2)
	l2Offset2 := int64(4 * clusterSize)
	data2Offset := int64(5 * clusterSize)

	binary.BigEndian.PutUint64(l1Entry[:], uint64(l2Offset2))
	f2.WriteAt(l1Entry[:], int64(hdr2.L1TableOffset))

	// In L2 table of layer2, entry 2 (guest cluster 2, offset 4MB) points to host cluster 5
	binary.BigEndian.PutUint64(l2Entry[:], uint64(data2Offset)|(1<<63))
	f2.WriteAt(l2Entry[:], l2Offset2+16)

	c2Data := bytes.Repeat([]byte{0xCC}, int(clusterSize))
	f2.WriteAt(c2Data, data2Offset)
	f2.Close()

	// 4. Flatten layer2 to flatOut
	if err := Flatten(layer2, flatOut); err != nil {
		t.Fatalf("Flatten failed: %v", err)
	}

	flatData, err := os.ReadFile(flatOut)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(flatData)) != diskSize {
		t.Fatalf("Flattened file size = %d, want %d", len(flatData), diskSize)
	}

	// Cluster 0 should be from baseRaw (0xAA)
	if !bytes.Equal(flatData[0:clusterSize], bytes.Repeat([]byte{0xAA}, int(clusterSize))) {
		t.Errorf("Cluster 0 did not match base image data")
	}

	// Cluster 1 should be from layer1 (0xBB)
	if !bytes.Equal(flatData[clusterSize:2*clusterSize], bytes.Repeat([]byte{0xBB}, int(clusterSize))) {
		t.Errorf("Cluster 1 did not match layer 1 data")
	}

	// Cluster 2 should be from layer2 (0xCC)
	if !bytes.Equal(flatData[2*clusterSize:3*clusterSize], bytes.Repeat([]byte{0xCC}, int(clusterSize))) {
		t.Errorf("Cluster 2 did not match layer 2 data")
	}

	// Cluster 3 should be from baseRaw (0xAA)
	if !bytes.Equal(flatData[3*clusterSize:4*clusterSize], bytes.Repeat([]byte{0xAA}, int(clusterSize))) {
		t.Errorf("Cluster 3 did not match base image data")
	}
}

func TestReflinkHelpers(t *testing.T) {
	dir := t.TempDir()
	_ = HasReflinkSupport(dir)

	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")

	testData := []byte("hello reflink and sparse test")
	if err := os.WriteFile(src, testData, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := CloneFileReflink(src, dst); err != nil {
		t.Fatalf("CloneFileReflink failed: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, testData) {
		t.Errorf("CloneFileReflink content = %q, want %q", got, testData)
	}
}

// chainTo builds a chain dir/base.img plus n qcow2 overlays over it and
// returns the top layer's path. The base is raw, as a packed guest's is.
func chainTo(t *testing.T, dir string, n int) string {
	t.Helper()
	base := filepath.Join(dir, "base.img")
	if err := os.WriteFile(base, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatalf("writing base: %v", err)
	}
	top := base
	for i := 1; i <= n; i++ {
		next := filepath.Join(dir, fmt.Sprintf("layer.%02d.qcow2", i))
		if err := WriteOverlay(next, top, 1<<20); err != nil {
			t.Fatalf("overlay %d: %v", i, err)
		}
		top = next
	}
	return top
}

func TestChainDepthCountsTheImageItself(t *testing.T) {
	dir := t.TempDir()
	// A raw base alone is depth 1, not 0: the thing the VMM opens counts.
	if got, err := ChainDepth(chainTo(t, dir, 0)); err != nil || got != 1 {
		t.Fatalf("ChainDepth(base) = %d, %v; want 1, nil", got, err)
	}
	for _, n := range []int{1, 3, 8} {
		sub := t.TempDir()
		got, err := ChainDepth(chainTo(t, sub, n))
		if err != nil || got != n+1 {
			t.Errorf("ChainDepth(chain of %d over a base) = %d, %v; want %d, nil", n, got, err, n+1)
		}
	}
}

func TestAddLayerRefusesALayerThatWouldNotBoot(t *testing.T) {
	dir := t.TempDir()
	// maxStoredBackingChain images stored, so one more would not boot once
	// PrepareOverlay adds the per-VM layer.
	top := chainTo(t, dir, maxStoredBackingChain-1)
	if got, err := ChainDepth(top); err != nil || got != maxStoredBackingChain {
		t.Fatalf("setup: ChainDepth = %d, %v; want %d", got, err, maxStoredBackingChain)
	}
	dest := filepath.Join(dir, "one-too-many.qcow2")
	// A big threshold would extend a small top layer instead of adding one,
	// which is not what this test is about; 1 byte forces the add path.
	_, _, err := AddLayer(dest, top, 1<<20, 1)
	if err == nil {
		t.Fatalf("AddLayer over a chain at the limit: want an error, got none")
	}
	for _, want := range []string{"would not boot", "flatten"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q -- a caller needs to know what to do", err, want)
		}
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Errorf("AddLayer refused but wrote %s anyway; a refusal must leave nothing behind", dest)
	}
}

func TestAddLayerStillAllowsAChainWithinBudget(t *testing.T) {
	// The control for the test above: a guard that refused everything would
	// pass that one and break the feature.
	dir := t.TempDir()
	top := chainTo(t, dir, maxStoredBackingChain-2)
	dest := filepath.Join(dir, "fits.qcow2")
	extended, out, err := AddLayer(dest, top, 1<<20, 1)
	if err != nil {
		t.Fatalf("AddLayer within budget: %v", err)
	}
	if extended || out != dest {
		t.Fatalf("AddLayer = (%v, %q); want (false, %q)", extended, out, dest)
	}
	if got, err := ChainDepth(dest); err != nil || got != maxStoredBackingChain {
		t.Fatalf("ChainDepth after adding = %d, %v; want %d", got, err, maxStoredBackingChain)
	}
}
