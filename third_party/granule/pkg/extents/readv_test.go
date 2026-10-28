package extents

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func patternAt(off int64, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((off + int64(i)) * 7 >> 3)
	}
	return b
}

// TestStoreReadvAt checks the vectored read against ReadAt across the cases
// it takes different paths for: inside one open extent (one preadv), across
// an extent boundary, an absent overlay extent served by the base, and an
// extent absent everywhere.
func TestStoreReadvAt(t *testing.T) {
	tmp := t.TempDir()
	const extSize = 64 * 1024
	const virtSize = 8 * extSize
	base, err := Open(Options{Dir: filepath.Join(tmp, "base"), VirtualSizeBytes: virtSize, ExtentSizeBytes: extSize})
	if err != nil {
		t.Fatal(err)
	}
	// Extents 0-3 are data in the base; 4-7 stay absent.
	if _, err := base.WriteAt(patternAt(0, 4*extSize), 0); err != nil {
		t.Fatal(err)
	}
	if err := base.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(Options{Dir: filepath.Join(tmp, "ovl"), BasePath: filepath.Join(tmp, "base")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// Overlay extent 1 has its own data.
	if _, err := st.WriteAt(bytes.Repeat([]byte{0xab}, extSize), extSize); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		off  int64
		segs []int
	}{
		{"overlay extent", extSize + 512, []int{4096, 512, 8192}},
		{"base extent", 2*extSize + 4096, []int{4096, 4096, 4096, 4096}},
		{"across boundary", 2*extSize - 4096, []int{4096, 4096, 4096}},
		{"split segment at boundary", extSize - 1024, []int{2048, 4096}},
		{"absent extent", 5*extSize + 4096, []int{4096, 4096}},
		{"into absent", 4*extSize - 2048, []int{4096, 4096}},
		{"single", 3 * extSize, []int{4096}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			total := 0
			bufs := make([][]byte, len(tc.segs))
			for i, n := range tc.segs {
				bufs[i] = bytes.Repeat([]byte{0xee}, n)
				total += n
			}
			n, err := st.ReadvAt(bufs, tc.off)
			if err != nil || n != total {
				t.Fatalf("ReadvAt = %d, %v; want %d", n, err, total)
			}
			want := make([]byte, total)
			if _, err := st.ReadAt(want, tc.off); err != nil {
				t.Fatal(err)
			}
			if got := bytes.Join(bufs, nil); !bytes.Equal(got, want) {
				t.Fatalf("ReadvAt data differs from ReadAt")
			}
		})
	}
}

// TestVhostUserBlkServer_ConcurrentReads puts a full queue of multi-segment
// reads on the ring with one kick and checks every one completes, once, with
// the right data, now that a queue serves its requests concurrently and
// completes them out of order.
func TestVhostUserBlkServer_ConcurrentReads(t *testing.T) {
	tmp := t.TempDir()
	const extSize = 64 * 1024
	const virtSize = 16 * extSize
	st, err := Open(Options{Dir: filepath.Join(tmp, "disk.extents"), VirtualSizeBytes: virtSize, ExtentSizeBytes: extSize})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.WriteAt(patternAt(0, 12*extSize), 0); err != nil {
		t.Fatal(err)
	}

	sockPath := filepath.Join(tmp, "s.sock")
	srv, err := NewServer(st, sockPath, 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetWorkersPerQueue(8)
	srv.Start()
	defer srv.Close()

	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sockPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	f, err := conn.File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sockFD := int(f.Fd())

	var u64 [8]byte
	binary.LittleEndian.PutUint64(u64[:], VirtioFVersion1|VirtioRingFEventIdx|VhostUserFProtocolFeatures)
	sendVhostMsg(t, sockFD, VhostUserSetFeatures, 0, u64[:])
	sendVhostMsg(t, sockFD, VhostUserGetProtocolFeatures, 0, nil)
	_, proto := recvVhostReply(t, sockFD)
	sendVhostMsg(t, sockFD, VhostUserSetProtocolFeatures, 0, proto)

	const memSize = 4 << 20
	memFile, err := os.CreateTemp(tmp, "ram-*")
	if err != nil {
		t.Fatal(err)
	}
	defer memFile.Close()
	if err := memFile.Truncate(memSize); err != nil {
		t.Fatal(err)
	}
	mem, err := unix.Mmap(int(memFile.Fd()), 0, memSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Munmap(mem)
	const gpa, uaddr = 0x40000000, 0x7f0000000000
	mt := make([]byte, 40)
	binary.LittleEndian.PutUint32(mt[0:], 1)
	binary.LittleEndian.PutUint64(mt[8:], gpa)
	binary.LittleEndian.PutUint64(mt[16:], memSize)
	binary.LittleEndian.PutUint64(mt[24:], uaddr)
	sendVhostMsg(t, sockFD, VhostUserSetMemTable, vhostUserNeedReplyMask, mt, int(memFile.Fd()))
	recvVhostReply(t, sockFD)

	const qSize = 256
	const reqs = qSize / 4 // four descriptors each: header, two data, status
	const descOff, availOff, usedOff, dataOff = 0, 0x1000, 0x2000, 0x10000
	const bs = 8192
	pair := func(a, b uint32) []byte {
		p := make([]byte, 8)
		binary.LittleEndian.PutUint32(p[0:], a)
		binary.LittleEndian.PutUint32(p[4:], b)
		return p
	}
	sendVhostMsg(t, sockFD, VhostUserSetVringNum, 0, pair(0, qSize))
	sendVhostMsg(t, sockFD, VhostUserSetVringBase, 0, pair(0, 0))
	addr := make([]byte, 40)
	binary.LittleEndian.PutUint64(addr[8:], uaddr+descOff)
	binary.LittleEndian.PutUint64(addr[16:], uaddr+usedOff)
	binary.LittleEndian.PutUint64(addr[24:], uaddr+availOff)
	sendVhostMsg(t, sockFD, VhostUserSetVringAddr, vhostUserNeedReplyMask, addr)
	recvVhostReply(t, sockFD)
	kickFD, _ := unix.Eventfd(0, unix.EFD_CLOEXEC)
	callFD, _ := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	defer unix.Close(kickFD)
	defer unix.Close(callFD)
	binary.LittleEndian.PutUint64(u64[:], 0)
	sendVhostMsg(t, sockFD, VhostUserSetVringCall, vhostUserNeedReplyMask, u64[:], callFD)
	recvVhostReply(t, sockFD)
	sendVhostMsg(t, sockFD, VhostUserSetVringKick, vhostUserNeedReplyMask, u64[:], kickFD)
	recvVhostReply(t, sockFD)

	setDesc := func(i int, a uint64, n uint32, flags, next uint16) {
		d := mem[descOff+16*i:]
		binary.LittleEndian.PutUint64(d[0:], a)
		binary.LittleEndian.PutUint32(d[8:], n)
		binary.LittleEndian.PutUint16(d[12:], flags)
		binary.LittleEndian.PutUint16(d[14:], next)
	}
	sectors := make([]uint64, reqs)
	for r := 0; r < reqs; r++ {
		area := dataOff + r*(bs+4096)
		// Spread reads over the disk, some across an extent boundary.
		sectors[r] = uint64((r*37*4096 + (r%3)*512) % (virtSize - bs) / 512)
		binary.LittleEndian.PutUint32(mem[area:], VirtioBlkTIn)
		binary.LittleEndian.PutUint64(mem[area+8:], sectors[r])
		mem[area+64] = 0xff
		h := 4 * r
		setDesc(h, gpa+uint64(area), 16, VringDescFNext, uint16(h+1))
		setDesc(h+1, gpa+uint64(area+4096), bs/2, VringDescFNext|VringDescFWrite, uint16(h+2))
		setDesc(h+2, gpa+uint64(area+4096+bs/2), bs/2, VringDescFNext|VringDescFWrite, uint16(h+3))
		setDesc(h+3, gpa+uint64(area+64), 1, VringDescFWrite, 0)
		binary.LittleEndian.PutUint16(mem[availOff+4+2*r:], uint16(h))
	}
	atomicStoreUint16(mem[availOff+2:], reqs)
	binary.LittleEndian.PutUint64(u64[:], 1)
	if _, err := unix.Write(kickFD, u64[:]); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for atomicLoadUint16(mem[usedOff+2:]) != reqs {
		if time.Now().After(deadline) {
			t.Fatalf("used.idx = %d, want %d", atomicLoadUint16(mem[usedOff+2:]), reqs)
		}
		time.Sleep(time.Millisecond)
	}
	seen := make(map[uint32]bool)
	for i := 0; i < reqs; i++ {
		e := usedOff + 4 + 8*i
		head := binary.LittleEndian.Uint32(mem[e:])
		if n := binary.LittleEndian.Uint32(mem[e+4:]); n != bs+1 {
			t.Errorf("used[%d] len = %d, want %d", i, n, bs+1)
		}
		if seen[head] || head%4 != 0 {
			t.Fatalf("used[%d] head %d duplicated or not a chain head", i, head)
		}
		seen[head] = true
	}
	for r := 0; r < reqs; r++ {
		area := dataOff + r*(bs+4096)
		if mem[area+64] != VirtioBlkSOK {
			t.Fatalf("request %d status %d", r, mem[area+64])
		}
		want := make([]byte, bs)
		if _, err := st.ReadAt(want, int64(sectors[r]*512)); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(mem[area+4096:area+4096+bs], want) {
			t.Fatalf("request %d (sector %d) data mismatch", r, sectors[r])
		}
	}
}
