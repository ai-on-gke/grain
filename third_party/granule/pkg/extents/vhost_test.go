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

func sendVhostMsg(t *testing.T, sockFD int, req, flags uint32, payload []byte, fds ...int) {
	t.Helper()
	hdr := make([]byte, vhostUserHeaderSize+len(payload))
	binary.LittleEndian.PutUint32(hdr[0:4], req)
	binary.LittleEndian.PutUint32(hdr[4:8], vhostUserVersion|flags)
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(len(payload)))
	copy(hdr[vhostUserHeaderSize:], payload)

	var oob []byte
	if len(fds) > 0 {
		oob = unix.UnixRights(fds...)
	}
	if err := unix.Sendmsg(sockFD, hdr, oob, nil, 0); err != nil {
		t.Fatalf("Sendmsg(req=%d): %v", req, err)
	}
}

func recvVhostReply(t *testing.T, sockFD int) (req uint32, payload []byte) {
	t.Helper()
	r, _, p, fds, err := readVhostMsg(sockFD)
	closeFDs(fds)
	if err != nil {
		t.Fatalf("recvVhostReply: %v", err)
	}
	return r, p
}

func TestVhostUserBlkServer_EndToEnd(t *testing.T) {
	tmp := t.TempDir()
	extDir := filepath.Join(tmp, "disk.extents")
	sockPath := filepath.Join(tmp, "vhost-blk.sock")

	const extSize = 64 * 1024
	const virtSize = 4 * extSize

	st, err := Open(Options{
		Dir:              extDir,
		VirtualSizeBytes: virtSize,
		ExtentSizeBytes:  extSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv, err := NewServer(st, sockPath, 2)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.Start()
	defer srv.Close()

	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sockPath, Net: "unix"})
	if err != nil {
		t.Fatalf("DialUnix: %v", err)
	}
	defer conn.Close()

	rawConn, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var sockFD int
	if err := rawConn.Control(func(fd uintptr) { sockFD = int(fd) }); err != nil {
		t.Fatal(err)
	}

	// 1. Negotiate features and protocol features.
	sendVhostMsg(t, sockFD, VhostUserGetFeatures, 0, nil)
	_, featPayload := recvVhostReply(t, sockFD)
	feats := binary.LittleEndian.Uint64(featPayload)
	if (feats&VirtioFVersion1) == 0 || (feats&VirtioBlkFDiscard) == 0 || (feats&VirtioBlkFWriteZeroes) == 0 {
		t.Fatalf("unexpected features 0x%x", feats)
	}

	sendVhostMsg(t, sockFD, VhostUserSetFeatures, 0, featPayload)
	sendVhostMsg(t, sockFD, VhostUserGetProtocolFeatures, 0, nil)
	_, protoPayload := recvVhostReply(t, sockFD)
	sendVhostMsg(t, sockFD, VhostUserSetProtocolFeatures, 0, protoPayload)

	// 2. Query queue count and virtio-blk config.
	sendVhostMsg(t, sockFD, VhostUserGetQueueNum, 0, nil)
	_, qnumPayload := recvVhostReply(t, sockFD)
	if qnum := binary.LittleEndian.Uint64(qnumPayload); qnum != 2 {
		t.Fatalf("queue num = %d, want 2", qnum)
	}

	cfgReq := make([]byte, 12+60)
	binary.LittleEndian.PutUint32(cfgReq[0:4], 0)
	binary.LittleEndian.PutUint32(cfgReq[4:8], 60)
	sendVhostMsg(t, sockFD, VhostUserGetConfig, 0, cfgReq)
	_, cfgReply := recvVhostReply(t, sockFD)
	capacitySectors := binary.LittleEndian.Uint64(cfgReply[12:20])
	if want := uint64(virtSize / 512); capacitySectors != want {
		t.Fatalf("config capacity sectors = %d, want %d", capacitySectors, want)
	}

	// 3. Set up shared guest memory (256 KiB) and map it in the test client.
	memFile, err := os.CreateTemp(tmp, "guest-ram-*")
	if err != nil {
		t.Fatal(err)
	}
	defer memFile.Close()
	const guestMemSize = 256 * 1024
	if err := memFile.Truncate(guestMemSize); err != nil {
		t.Fatal(err)
	}
	guestMem, err := unix.Mmap(int(memFile.Fd()), 0, guestMemSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Munmap(guestMem)

	const baseGPA = 0x10000000
	const baseUserAddr = 0x7f0000000000
	memTablePayload := make([]byte, 8+32)
	binary.LittleEndian.PutUint32(memTablePayload[0:4], 1) // 1 region
	binary.LittleEndian.PutUint64(memTablePayload[8:16], baseGPA)
	binary.LittleEndian.PutUint64(memTablePayload[16:24], guestMemSize)
	binary.LittleEndian.PutUint64(memTablePayload[24:32], baseUserAddr)
	binary.LittleEndian.PutUint64(memTablePayload[32:40], 0) // mmap_offset = 0
	sendVhostMsg(t, sockFD, VhostUserSetMemTable, vhostUserNeedReplyMask, memTablePayload, int(memFile.Fd()))
	_, ack := recvVhostReply(t, sockFD)
	if binary.LittleEndian.Uint64(ack) != 0 {
		t.Fatal("VHOST_USER_SET_MEM_TABLE failed")
	}

	// 4. Configure vring 0 (queue size = 16).
	const qSize = 16
	const descOff = 0x0000
	const availOff = 0x1000
	const usedOff = 0x2000
	const reqAreaOff = 0x4000

	var vringNumPayload [8]byte
	binary.LittleEndian.PutUint32(vringNumPayload[0:4], 0)
	binary.LittleEndian.PutUint32(vringNumPayload[4:8], qSize)
	sendVhostMsg(t, sockFD, VhostUserSetVringNum, 0, vringNumPayload[:])

	var vringBasePayload [8]byte
	binary.LittleEndian.PutUint32(vringBasePayload[0:4], 0)
	binary.LittleEndian.PutUint32(vringBasePayload[4:8], 0)
	sendVhostMsg(t, sockFD, VhostUserSetVringBase, 0, vringBasePayload[:])

	var vringAddrPayload [40]byte
	binary.LittleEndian.PutUint32(vringAddrPayload[0:4], 0)
	binary.LittleEndian.PutUint64(vringAddrPayload[8:16], baseUserAddr+descOff)
	binary.LittleEndian.PutUint64(vringAddrPayload[16:24], baseUserAddr+usedOff)
	binary.LittleEndian.PutUint64(vringAddrPayload[24:32], baseUserAddr+availOff)
	sendVhostMsg(t, sockFD, VhostUserSetVringAddr, vhostUserNeedReplyMask, vringAddrPayload[:])
	_, ack = recvVhostReply(t, sockFD)
	if binary.LittleEndian.Uint64(ack) != 0 {
		t.Fatal("VHOST_USER_SET_VRING_ADDR failed")
	}

	kickFD, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(kickFD)
	callFD, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(callFD)

	var fdPayload [8]byte
	binary.LittleEndian.PutUint64(fdPayload[:], 0) // queue 0
	sendVhostMsg(t, sockFD, VhostUserSetVringCall, vhostUserNeedReplyMask, fdPayload[:], callFD)
	_, _ = recvVhostReply(t, sockFD)
	sendVhostMsg(t, sockFD, VhostUserSetVringKick, vhostUserNeedReplyMask, fdPayload[:], kickFD)
	_, _ = recvVhostReply(t, sockFD)

	var enablePayload [8]byte
	binary.LittleEndian.PutUint32(enablePayload[0:4], 0)
	binary.LittleEndian.PutUint32(enablePayload[4:8], 1)
	sendVhostMsg(t, sockFD, VhostUserSetVringEnable, vhostUserNeedReplyMask, enablePayload[:])
	_, _ = recvVhostReply(t, sockFD)

	var curAvailIdx uint16
	var curUsedIdx uint16

	writeDesc := func(idx uint16, gpa uint64, length uint32, flags, next uint16) {
		off := descOff + int(idx)*16
		binary.LittleEndian.PutUint64(guestMem[off:off+8], gpa)
		binary.LittleEndian.PutUint32(guestMem[off+8:off+12], length)
		binary.LittleEndian.PutUint16(guestMem[off+12:off+14], flags)
		binary.LittleEndian.PutUint16(guestMem[off+14:off+16], next)
	}

	submitAndWait := func(headDesc uint16) {
		t.Helper()
		slot := int(curAvailIdx % qSize)
		binary.LittleEndian.PutUint16(guestMem[availOff+4+2*slot:availOff+6+2*slot], headDesc)
		curAvailIdx++
		atomicStoreUint16(guestMem[availOff+2:availOff+4], curAvailIdx)

		var one [8]byte
		binary.LittleEndian.PutUint64(one[:], 1)
		if _, err := unix.Write(kickFD, one[:]); err != nil {
			t.Fatalf("kick write: %v", err)
		}

		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if atomicLoadUint16(guestMem[usedOff+2:usedOff+4]) != curUsedIdx {
				curUsedIdx++
				var drain [8]byte
				_, _ = unix.Read(callFD, drain[:])
				return
			}
			time.Sleep(500 * time.Microsecond)
		}
		t.Fatal("timed out waiting for vring completion")
	}

	// 5. Test VIRTIO_BLK_T_OUT (Write 4096 bytes across boundary of extent 0 and extent 1).
	// Sector 126 * 512 = 64512 (1024 bytes before the 64 KiB boundary).
	const testSector = 126
	const dataLen = 4096
	hdrOff := reqAreaOff
	dataOff := reqAreaOff + 64
	statusOff := reqAreaOff + 64 + dataLen

	binary.LittleEndian.PutUint32(guestMem[hdrOff:hdrOff+4], VirtioBlkTOut)
	binary.LittleEndian.PutUint32(guestMem[hdrOff+4:hdrOff+8], 0)
	binary.LittleEndian.PutUint64(guestMem[hdrOff+8:hdrOff+16], testSector)
	writePattern := bytes.Repeat([]byte("GRANULE!"), dataLen/8)
	copy(guestMem[dataOff:dataOff+dataLen], writePattern)
	guestMem[statusOff] = 0xff

	writeDesc(0, baseGPA+uint64(hdrOff), 16, VringDescFNext, 1)
	writeDesc(1, baseGPA+uint64(dataOff), dataLen, VringDescFNext, 2)
	writeDesc(2, baseGPA+uint64(statusOff), 1, VringDescFWrite, 0)
	submitAndWait(0)

	if guestMem[statusOff] != VirtioBlkSOK {
		t.Fatalf("VIRTIO_BLK_T_OUT status = %d, want 0", guestMem[statusOff])
	}

	// 6. Test VIRTIO_BLK_T_FLUSH.
	binary.LittleEndian.PutUint32(guestMem[hdrOff:hdrOff+4], VirtioBlkTFlush)
	binary.LittleEndian.PutUint64(guestMem[hdrOff+8:hdrOff+16], 0)
	guestMem[statusOff] = 0xff
	writeDesc(0, baseGPA+uint64(hdrOff), 16, VringDescFNext, 1)
	writeDesc(1, baseGPA+uint64(statusOff), 1, VringDescFWrite, 0)
	submitAndWait(0)
	if guestMem[statusOff] != VirtioBlkSOK {
		t.Fatalf("VIRTIO_BLK_T_FLUSH status = %d, want 0", guestMem[statusOff])
	}

	// 7. Test VIRTIO_BLK_T_IN (Read back the 4096 bytes into a fresh buffer).
	readDataOff := reqAreaOff + 8192
	clear(guestMem[readDataOff : readDataOff+dataLen])
	binary.LittleEndian.PutUint32(guestMem[hdrOff:hdrOff+4], VirtioBlkTIn)
	binary.LittleEndian.PutUint64(guestMem[hdrOff+8:hdrOff+16], testSector)
	guestMem[statusOff] = 0xff

	writeDesc(0, baseGPA+uint64(hdrOff), 16, VringDescFNext, 1)
	writeDesc(1, baseGPA+uint64(readDataOff), dataLen, VringDescFWrite|VringDescFNext, 2)
	writeDesc(2, baseGPA+uint64(statusOff), 1, VringDescFWrite, 0)
	submitAndWait(0)

	if guestMem[statusOff] != VirtioBlkSOK {
		t.Fatalf("VIRTIO_BLK_T_IN status = %d, want 0", guestMem[statusOff])
	}
	if !bytes.Equal(guestMem[readDataOff:readDataOff+dataLen], writePattern) {
		t.Fatal("VIRTIO_BLK_T_IN read data mismatch")
	}

	// 8. Test VIRTIO_BLK_T_DISCARD across all of extent 0 (128 sectors = 64 KiB).
	dwzOff := reqAreaOff + 16384
	binary.LittleEndian.PutUint32(guestMem[hdrOff:hdrOff+4], VirtioBlkTDiscard)
	binary.LittleEndian.PutUint64(guestMem[hdrOff+8:hdrOff+16], 0)
	binary.LittleEndian.PutUint64(guestMem[dwzOff:dwzOff+8], 0)      // sector 0
	binary.LittleEndian.PutUint32(guestMem[dwzOff+8:dwzOff+12], 128) // 128 sectors = 64 KiB
	binary.LittleEndian.PutUint32(guestMem[dwzOff+12:dwzOff+16], 0)
	guestMem[statusOff] = 0xff

	writeDesc(0, baseGPA+uint64(hdrOff), 16, VringDescFNext, 1)
	writeDesc(1, baseGPA+uint64(dwzOff), 16, VringDescFNext, 2)
	writeDesc(2, baseGPA+uint64(statusOff), 1, VringDescFWrite, 0)
	submitAndWait(0)

	if guestMem[statusOff] != VirtioBlkSOK {
		t.Fatalf("VIRTIO_BLK_T_DISCARD status = %d, want 0", guestMem[statusOff])
	}
	if st.HasExtent(0) {
		t.Fatal("extent 0 should be unlinked after full-extent VIRTIO_BLK_T_DISCARD")
	}

	// 9. Test VIRTIO_BLK_T_GET_ID.
	idOff := reqAreaOff + 20480
	binary.LittleEndian.PutUint32(guestMem[hdrOff:hdrOff+4], VirtioBlkTGetID)
	guestMem[statusOff] = 0xff
	writeDesc(0, baseGPA+uint64(hdrOff), 16, VringDescFNext, 1)
	writeDesc(1, baseGPA+uint64(idOff), virtioBlkIDBytes, VringDescFWrite|VringDescFNext, 2)
	writeDesc(2, baseGPA+uint64(statusOff), 1, VringDescFWrite, 0)
	submitAndWait(0)

	if guestMem[statusOff] != VirtioBlkSOK {
		t.Fatalf("VIRTIO_BLK_T_GET_ID status = %d, want 0", guestMem[statusOff])
	}
	if !bytes.HasPrefix(guestMem[idOff:idOff+virtioBlkIDBytes], []byte("granule-extents")) {
		t.Fatalf("VIRTIO_BLK_T_GET_ID = %q", guestMem[idOff:idOff+virtioBlkIDBytes])
	}
}
