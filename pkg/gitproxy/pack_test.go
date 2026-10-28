package gitproxy

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func encodeVarint(v int) []byte {
	var buf []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v > 0 {
			buf = append(buf, b|0x80)
		} else {
			buf = append(buf, b)
			break
		}
	}
	return buf
}

func encodeDeltaHeader(srcSize, dstSize int) []byte {
	var buf []byte
	buf = append(buf, encodeVarint(srcSize)...)
	buf = append(buf, encodeVarint(dstSize)...)
	return buf
}

func TestApplyDeltaValidation(t *testing.T) {
	base := []byte("hello world")

	t.Run("valid delta", func(t *testing.T) {
		// Delta replaces "world" with "grain"
		// Header: srcSize = 11, dstSize = 11
		// Copy 6 bytes from offset 0 ("hello ")
		// Insert 5 bytes: "grain"
		delta := encodeDeltaHeader(len(base), 11)
		// Op copy: offset=0, size=6 -> op = 0x80 | 0x10 (size in byte 1)
		delta = append(delta, 0x90, 6)
		// Op insert: 5 bytes
		delta = append(delta, 5)
		delta = append(delta, []byte("grain")...)

		out, err := applyDelta(base, delta)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(out) != "hello grain" {
			t.Fatalf("got %q, want %q", string(out), "hello grain")
		}
	})

	t.Run("dstSize exceeding maxPackObjectBytes rejected", func(t *testing.T) {
		delta := encodeDeltaHeader(len(base), maxPackObjectBytes+1)
		_, err := applyDelta(base, delta)
		if err == nil {
			t.Fatalf("expected error for dstSize exceeding limit, got nil")
		}
		if !strings.Contains(err.Error(), "exceeds limit") {
			t.Errorf("expected 'exceeds limit' error, got %v", err)
		}
	})

	t.Run("dstSize negative or overflowed rejected", func(t *testing.T) {
		// Encoded with high bits set so signed int becomes negative or exceeds bound
		delta := encodeDeltaHeader(len(base), -1)
		_, err := applyDelta(base, delta)
		if err == nil {
			t.Fatalf("expected error for negative dstSize, got nil")
		}
	})
}

func TestReadPackCommitsDecompressionLimit(t *testing.T) {
	// Build a valid packfile with 1 commit object whose decompressed payload exceeds maxPackObjectBytes.
	// Packfile header: "PACK" + version 2 (uint32) + count 1 (uint32)
	var pack bytes.Buffer
	pack.WriteString("PACK")
	_ = binary.Write(&pack, binary.BigEndian, uint32(2))
	_ = binary.Write(&pack, binary.BigEndian, uint32(1))

	// Object header: type commit (1)
	// Object size varint: we can claim any size or 0
	pack.WriteByte(byte(objCommit << 4))

	// Zlib stream that decompresses to > maxPackObjectBytes of zeroes.
	// A small compressed stream can expand to 33 MiB easily.
	zw := zlib.NewWriter(&pack)
	chunk := make([]byte, 1<<20) // 1 MiB chunk
	for i := 0; i < 33; i++ {
		if _, err := zw.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	// 20-byte pack checksum trailer
	pack.Write(make([]byte, 20))

	_, err := readPackCommits(pack.Bytes(), 20)
	if err == nil {
		t.Fatalf("expected error reading decompression bomb packfile, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds maximum size") {
		t.Errorf("expected 'exceeds maximum size' error, got %v", err)
	}
}

// The commits a pack keeps in hand are bounded in total, not only one at
// a time: each is under maxPackObjectBytes, but together they are not.
func TestReadPackCommitsBoundsTheCommitsItKeeps(t *testing.T) {
	commit := func(i int) []byte {
		return []byte(fmt.Sprintf("tree %040d\nauthor a <a@b> 0 +0000\ncommitter a <a@b> 0 +0000\n\n%s\n", i, strings.Repeat("x", 300)))
	}
	var pack bytes.Buffer
	pack.WriteString("PACK")
	_ = binary.Write(&pack, binary.BigEndian, uint32(2))
	_ = binary.Write(&pack, binary.BigEndian, uint32(3))
	for i := 0; i < 3; i++ {
		data := commit(i)
		// Type and size, the size in 4 bits then 7-bit continuation bytes.
		size := len(data)
		pack.WriteByte(byte(objCommit<<4) | byte(size&0x0f) | 0x80)
		pack.Write(encodeVarint(size >> 4))
		zw := zlib.NewWriter(&pack)
		if _, err := zw.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	pack.Write(make([]byte, 20))

	commits, err := readPackCommits(pack.Bytes(), 20)
	if err != nil || len(commits) != 3 {
		t.Fatalf("under the bound: got %d commits, err %v; want 3", len(commits), err)
	}

	prev := maxPackCommitBytes
	maxPackCommitBytes = int64(2*len(commit(0)) + 1)
	t.Cleanup(func() { maxPackCommitBytes = prev })
	if _, err := readPackCommits(pack.Bytes(), 20); err == nil || !strings.Contains(err.Error(), "maximum total size") {
		t.Fatalf("over the bound: got err %v, want the total-size refusal", err)
	}
}

// A blob over maxPackObjectBytes is an ordinary thing for a fast-forward
// to carry -- a 40 MiB file -- and the walk, which keeps only commits,
// reads past it without holding it, and still finds the commit after it.
// The bound stays on what the walk keeps (the tests above).
func TestReadPackCommitsReadsPastABlobOverTheObjectBound(t *testing.T) {
	commit := []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nauthor a <a@b> 0 +0000\ncommitter a <a@b> 0 +0000\n\nlarge\n")
	pack := packOf(packEntry(3 /* blob */, nil, nil, maxPackObjectBytes+(1<<20)), packEntry(objCommit, nil, commit, 0))

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	commits, err := readPackCommits(pack, 20)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("a pack carrying a %d MiB blob: %v", (maxPackObjectBytes+(1<<20))>>20, err)
	}
	if _, ok := commits[objectName(sha1.New, "commit", commit)]; !ok {
		t.Errorf("the commit after the blob was not read: %v", commits)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Errorf("reading past the blob allocated %d MiB; it should be inflated into io.Discard", allocated>>20)
	}
}

// bombPack is a pack of n blobs, each a zlib stream of size zeros: a few
// KiB of pack apiece that inflate about 1000:1.
func bombPack(n, size int) []byte {
	entries := make([][]byte, n)
	one := packEntry(3 /* blob */, nil, nil, size)
	for i := range entries {
		entries[i] = one
	}
	return packOf(entries...)
}

// What a pack inflates is bounded in total, not only one object at a
// time: 64 blobs just under maxPackObjectBytes each are 2 MiB of pack
// and 2 GiB inflated, which before this cost about 1.7 s of CPU in the
// push request and now fails closed after a few hundred MiB.
func TestReadPackCommitsBoundsWhatThePackInflates(t *testing.T) {
	pack := bombPack(64, maxPackObjectBytes-1)
	// The budget at the end of the pack: the most it could ever allow.
	budget := packInflateFloor + packInflateRatio*int64(len(pack))
	if total := int64(64) * (maxPackObjectBytes - 1); total <= budget {
		t.Fatalf("the bomb inflates %d bytes, within the %d budget it is meant to exceed", total, budget)
	}
	if _, err := readPackCommits(pack, 20); err == nil || !strings.Contains(err.Error(), "inflates beyond") {
		t.Fatalf("got err %v, want the total-inflation refusal", err)
	}

	// The same objects within the budget are read: the bound is on the
	// total, and a pack that fits it is no bomb.
	small := bombPack(4, 1<<20)
	if _, err := readPackCommits(small, 20); err != nil {
		t.Fatalf("4 MiB inflated: %v", err)
	}
	prev := packInflateFloor
	packInflateFloor = 3 << 20
	t.Cleanup(func() { packInflateFloor = prev })
	if _, err := readPackCommits(small, 20); err == nil || !strings.Contains(err.Error(), "inflates beyond") {
		t.Fatalf("4 MiB inflated against a 3 MiB floor: got err %v, want the total-inflation refusal", err)
	}

	// A blob is not held to maxPackObjectBytes, so the budget is all
	// that bounds one on its own.
	if _, err := readPackCommits(bombPack(1, 8<<20), 20); err == nil || !strings.Contains(err.Error(), "inflates beyond") {
		t.Fatalf("one 8 MiB blob against a 3 MiB floor: got err %v, want the total-inflation refusal", err)
	}
}
