package qcow2

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestStress_ConcurrentDescriptorSynthesisAndResize tests concurrent creation and resizing
// of QCOW2 disks across parallel workers.
func TestStress_ConcurrentDescriptorSynthesisAndResize(t *testing.T) {
	tempDir := t.TempDir()
	const workers = 20
	const iterationsPerWorker = 5

	var wg sync.WaitGroup
	errCh := make(chan error, workers*iterationsPerWorker)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < iterationsPerWorker; i++ {
				diskDir := filepath.Join(tempDir, fmt.Sprintf("worker_%d_iter_%d", workerID, i))
				if err := os.MkdirAll(diskDir, 0o755); err != nil {
					errCh <- fmt.Errorf("mkdir %s: %w", diskDir, err)
					return
				}
				qcowPath := filepath.Join(diskDir, "rootfs.qcow2")

				// Initial disk: 256MB
				initialSize := int64(256 * 1024 * 1024)
				if err := WriteOverlay(qcowPath, "", initialSize); err != nil {
					errCh <- fmt.Errorf("WriteOverlay: %w", err)
					return
				}

				// Check initial virtual size
				vSize, err := VirtualSize(qcowPath)
				if err != nil || vSize != initialSize {
					errCh <- fmt.Errorf("VirtualSize = %d, err = %v; want %d", vSize, err, initialSize)
					return
				}

				// Expand to 2GB
				expandedSize1 := int64(2 * 1024 * 1024 * 1024)
				if err := Resize(qcowPath, expandedSize1); err != nil {
					errCh <- fmt.Errorf("Resize to 2GB: %w", err)
					return
				}

				// Expand to 8GB
				expandedSize2 := int64(8 * 1024 * 1024 * 1024)
				if err := Resize(qcowPath, expandedSize2); err != nil {
					errCh <- fmt.Errorf("Resize to 8GB: %w", err)
					return
				}

				// Verify header
				hdr, err := ReadHeader(qcowPath)
				if err != nil {
					errCh <- fmt.Errorf("ReadHeader: %w", err)
					return
				}
				if int64(hdr.Size) != expandedSize2 {
					errCh <- fmt.Errorf("hdr.Size = %d, want %d", hdr.Size, expandedSize2)
					return
				}
			}
		}(w)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("stress worker failed: %v", err)
	}
}

// TestStress_ConcurrentReflinkAndExtentCloning tests data integrity when multiple workers
// create overlays and flatten them back against a base image concurrently.
func TestStress_ConcurrentReflinkAndExtentCloning(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Create a 16MB base backing file with known pattern
	basePath := filepath.Join(tempDir, "base_backing.raw")
	const baseSize = 16 * 1024 * 1024
	baseData := make([]byte, baseSize)
	for i := range baseData {
		baseData[i] = byte((i * 31) & 0xff)
	}
	baseHash := sha256.Sum256(baseData)
	baseHashStr := hex.EncodeToString(baseHash[:])

	if err := os.WriteFile(basePath, baseData, 0o644); err != nil {
		t.Fatalf("writing backing file: %v", err)
	}

	const workers = 16
	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			workerDir := filepath.Join(tempDir, fmt.Sprintf("clone_worker_%d", workerID))
			if err := os.MkdirAll(workerDir, 0o755); err != nil {
				errCh <- err
				return
			}
			qcowPath := filepath.Join(workerDir, "overlay.qcow2")

			// Write overlay from backing file
			if err := WriteOverlay(qcowPath, basePath, baseSize); err != nil {
				errCh <- fmt.Errorf("WriteOverlay: %w", err)
				return
			}

			// Flatten back to flat raw file
			flattenedPath := filepath.Join(workerDir, "flattened.raw")
			if err := Flatten(qcowPath, flattenedPath); err != nil {
				errCh <- fmt.Errorf("Flatten: %w", err)
				return
			}

			// Read and verify hash
			data, err := os.ReadFile(flattenedPath)
			if err != nil {
				errCh <- fmt.Errorf("reading flattened file: %w", err)
				return
			}
			flattenedHash := sha256.Sum256(data)
			flattenedHashStr := hex.EncodeToString(flattenedHash[:])

			if flattenedHashStr != baseHashStr {
				errCh <- fmt.Errorf("checksum mismatch: got %s, want %s", flattenedHashStr, baseHashStr)
				return
			}
		}(w)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("cloning stress worker failed: %v", err)
	}
}

// TestStress_ConcurrentLayerChainsAndFlatten tests deep layer chains and concurrent flattening
func TestStress_ConcurrentLayerChainsAndFlatten(t *testing.T) {
	tempDir := t.TempDir()
	const clusterSize int64 = 2 * 1024 * 1024
	const diskSize int64 = 8 * 1024 * 1024 // 4 clusters

	baseRaw := filepath.Join(tempDir, "base.raw")
	baseData := bytes.Repeat([]byte{0x11}, int(diskSize))
	if err := os.WriteFile(baseRaw, baseData, 0o644); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			workerDir := filepath.Join(tempDir, fmt.Sprintf("chain_worker_%d", workerID))
			if err := os.MkdirAll(workerDir, 0o755); err != nil {
				errCh <- err
				return
			}

			layer1 := filepath.Join(workerDir, "layer1.qcow2")
			if err := WriteOverlay(layer1, baseRaw, diskSize); err != nil {
				errCh <- fmt.Errorf("layer1: %w", err)
				return
			}

			// Add a second layer
			layer2 := filepath.Join(workerDir, "layer2.qcow2")
			_, outPath, err := AddLayer(layer2, layer1, diskSize, 0)
			if err != nil {
				errCh <- fmt.Errorf("layer2: %w", err)
				return
			}

			// Write a cluster to layer1
			f1, err := os.OpenFile(layer1, os.O_RDWR, 0o644)
			if err != nil {
				errCh <- err
				return
			}
			hdr1, _ := ReadHeader(layer1)
			l2Offset := 4 * clusterSize
			dataOffset := 5 * clusterSize

			var l1Entry [8]byte
			binary.BigEndian.PutUint64(l1Entry[:], uint64(l2Offset))
			f1.WriteAt(l1Entry[:], int64(hdr1.L1TableOffset))

			var l2Entry [8]byte
			binary.BigEndian.PutUint64(l2Entry[:], uint64(dataOffset)|(1<<63))
			f1.WriteAt(l2Entry[:], l2Offset+8) // cluster 1

			c1Data := bytes.Repeat([]byte{0x22}, int(clusterSize))
			f1.WriteAt(c1Data, dataOffset)
			f1.Close()

			flatOut := filepath.Join(workerDir, "flattened.raw")
			if err := Flatten(outPath, flatOut); err != nil {
				errCh <- fmt.Errorf("flatten chain: %w", err)
				return
			}

			flatData, err := os.ReadFile(flatOut)
			if err != nil {
				errCh <- err
				return
			}
			if int64(len(flatData)) != diskSize {
				errCh <- fmt.Errorf("size mismatch: got %d, want %d", len(flatData), diskSize)
				return
			}
			if !bytes.Equal(flatData[clusterSize:2*clusterSize], c1Data) {
				errCh <- fmt.Errorf("cluster 1 data did not match written cluster")
				return
			}
		}(w)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("chain stress worker failed: %v", err)
	}
}
