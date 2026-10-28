package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// The driver below is a minimal virtio-blk *driver* (what the guest kernel
// does) speaking the vhost-user protocol (what cloud-hypervisor does) to a
// vhost-user-blk backend over its socket. It shares one memfd as "guest RAM",
// lays out a split virtqueue per queue in it, and keeps a fixed number of read
// requests in flight per queue. Nothing in it is specific to the Go server, so
// the same binary measures the Rust prototype in experimental/.

const (
	reqGetFeatures         = 1
	reqSetFeatures         = 2
	reqSetOwner            = 3
	reqSetMemTable         = 5
	reqSetVringNum         = 8
	reqSetVringAddr        = 9
	reqSetVringBase        = 10
	reqSetVringKick        = 12
	reqSetVringCall        = 13
	reqGetProtocolFeatures = 15
	reqSetProtocolFeatures = 16
	reqGetQueueNum         = 17
	reqSetVringEnable      = 18

	featVersion1       = uint64(1) << 32
	featProtocol       = uint64(1) << 30
	featEventIdx       = uint64(1) << 29
	featBlkSegMax      = uint64(1) << 2
	featBlkBlkSize     = uint64(1) << 6
	featBlkFlush       = uint64(1) << 9
	featBlkMQ          = uint64(1) << 12
	protoMQ            = uint64(1) << 0
	protoReplyAck      = uint64(1) << 3
	protoConfig        = uint64(1) << 9
	descFNext          = 1
	descFWrite         = 2
	virtioBlkTIn       = 0
	guestPhysBase      = 0x100000000
	guestUserspaceBase = 0x7e0000000000
)

type driverConfig struct {
	socket   string
	queues   int
	qd       int // requests in flight per queue
	bs       int
	seg      int // bytes per data descriptor, like a guest's scattered pages
	dur      time.Duration
	span     int64 // bytes of the disk to read from, from offset 0
	eventIdx bool
	verify   *os.File // if set, check every read against this flat image
}

type result struct {
	IOPS       float64 `json:"iops"`
	MBps       float64 `json:"mbps"`
	P50us      float64 `json:"p50_us"`
	P99us      float64 `json:"p99_us"`
	Requests   int64   `json:"requests"`
	Errors     int64   `json:"errors"`
	Mismatches int64   `json:"mismatches,omitempty"`
	Kicks      int64   `json:"kicks"`
	Calls      int64   `json:"calls"`
	Seconds    float64 `json:"seconds"`
	ServerCPU  float64 `json:"server_cpu_pct,omitempty"`
}

type conn struct{ fd int }

func (c *conn) send(req uint32, payload []byte, fds ...int) error {
	msg := make([]byte, 12+len(payload))
	binary.LittleEndian.PutUint32(msg[0:], req)
	binary.LittleEndian.PutUint32(msg[4:], 1)
	binary.LittleEndian.PutUint32(msg[8:], uint32(len(payload)))
	copy(msg[12:], payload)
	var oob []byte
	if len(fds) > 0 {
		oob = unix.UnixRights(fds...)
	}
	return unix.Sendmsg(c.fd, msg, oob, nil, 0)
}

func (c *conn) recvU64(want uint32) (uint64, error) {
	buf := make([]byte, 12+8)
	if _, err := readFull(c.fd, buf); err != nil {
		return 0, err
	}
	if r := binary.LittleEndian.Uint32(buf[0:]); r != want {
		return 0, fmt.Errorf("reply to %d, want %d", r, want)
	}
	return binary.LittleEndian.Uint64(buf[12:]), nil
}

func (c *conn) query(req uint32) (uint64, error) {
	if err := c.send(req, nil); err != nil {
		return 0, err
	}
	return c.recvU64(req)
}

func readFull(fd int, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := unix.Read(fd, b[n:])
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return n, err
		}
		if m == 0 {
			return n, errors.New("socket closed")
		}
		n += m
	}
	return n, nil
}

func u32pair(a, b uint32) []byte {
	p := make([]byte, 8)
	binary.LittleEndian.PutUint32(p[0:], a)
	binary.LittleEndian.PutUint32(p[4:], b)
	return p
}

// queue is one split virtqueue in shared memory plus the per-slot request
// buffers the driver reuses.
type queue struct {
	idx            int
	size           int
	desc, avail    []byte
	used           []byte
	reqArea        []byte // per slot: 16 B header, bs data, 1 B status
	reqAreaGPA     uint64
	slotStride     int
	kick, call     int
	availIdx       uint16
	lastUsed       uint16
	submitted      []time.Time
	sectors        []uint64
	nbad           int64
	lat            []time.Duration
	nreq, nerr     int64
	nkick, ncall   int64
	descPerRequest int
}

func fence() {
	// LOCK-prefixed instruction: a full barrier on amd64, which the split
	// ring needs between publishing an index and reading the other side's.
	var x int32
	atomic.AddInt32(&x, 1)
}

func runDriver(cfg driverConfig) (result, error) {
	var res result
	sock, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: cfg.socket, Net: "unix"})
	if err != nil {
		return res, err
	}
	defer sock.Close()
	f, err := sock.File()
	if err != nil {
		return res, err
	}
	defer f.Close()
	c := &conn{fd: int(f.Fd())}

	if err := c.send(reqSetOwner, nil); err != nil {
		return res, err
	}
	feats, err := c.query(reqGetFeatures)
	if err != nil {
		return res, err
	}
	want := featVersion1 | featProtocol | featBlkSegMax | featBlkBlkSize | featBlkFlush | featBlkMQ
	if cfg.eventIdx {
		want |= featEventIdx
	}
	acked := feats & want
	pl := make([]byte, 8)
	binary.LittleEndian.PutUint64(pl, acked)
	if err := c.send(reqSetFeatures, pl); err != nil {
		return res, err
	}
	proto, err := c.query(reqGetProtocolFeatures)
	if err != nil {
		return res, err
	}
	binary.LittleEndian.PutUint64(pl, proto&(protoMQ|protoConfig))
	if err := c.send(reqSetProtocolFeatures, pl); err != nil {
		return res, err
	}
	if nq, err := c.query(reqGetQueueNum); err != nil {
		return res, err
	} else if int(nq) < cfg.queues {
		return res, fmt.Errorf("backend offers %d queues, want %d", nq, cfg.queues)
	}

	// Guest RAM layout: per queue, 64 KiB of ring followed by its slots.
	qsize := 128
	for qsize < cfg.qd {
		qsize *= 2
	}
	segs := (cfg.bs + cfg.seg - 1) / cfg.seg
	descPerReq := segs + 2
	if descPerReq*cfg.qd > qsize {
		for qsize < descPerReq*cfg.qd {
			qsize *= 2
		}
	}
	if qsize > 32768 {
		return res, fmt.Errorf("queue size %d too large", qsize)
	}
	ringBytes := (qsize*16 + 6 + 2*qsize + 4096 + 6 + 8*qsize + 4095) &^ 4095
	slotStride := (16 + cfg.bs + 1 + 4095) &^ 4095
	slotStride += 4096 // header and status get their own page, data stays page aligned
	perQueue := ringBytes + slotStride*cfg.qd
	memSize := ((perQueue*cfg.queues + (1 << 21) - 1) >> 21) << 21

	memfd, err := unix.MemfdCreate("vhostbench-guest-ram", 0)
	if err != nil {
		return res, err
	}
	defer unix.Close(memfd)
	if err := unix.Ftruncate(memfd, int64(memSize)); err != nil {
		return res, err
	}
	mem, err := unix.Mmap(memfd, 0, memSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED|unix.MAP_POPULATE)
	if err != nil {
		return res, err
	}
	defer unix.Munmap(mem)

	mt := make([]byte, 8+32)
	binary.LittleEndian.PutUint32(mt[0:], 1)
	binary.LittleEndian.PutUint64(mt[8:], guestPhysBase)
	binary.LittleEndian.PutUint64(mt[16:], uint64(memSize))
	binary.LittleEndian.PutUint64(mt[24:], guestUserspaceBase)
	binary.LittleEndian.PutUint64(mt[32:], 0)
	if err := c.send(reqSetMemTable, mt, memfd); err != nil {
		return res, err
	}

	qs := make([]*queue, cfg.queues)
	for i := range qs {
		base := i * perQueue
		descOff := base
		availOff := base + qsize*16
		usedOff := (availOff + 6 + 2*qsize + 4095) &^ 4095
		q := &queue{
			idx: i, size: qsize,
			desc:           mem[descOff : descOff+qsize*16],
			avail:          mem[availOff : availOff+6+2*qsize],
			used:           mem[usedOff : usedOff+6+8*qsize],
			reqArea:        mem[base+ringBytes : base+perQueue],
			reqAreaGPA:     guestPhysBase + uint64(base+ringBytes),
			slotStride:     slotStride,
			submitted:      make([]time.Time, cfg.qd),
			sectors:        make([]uint64, cfg.qd),
			descPerRequest: descPerReq,
		}
		qs[i] = q
		if q.kick, err = unix.Eventfd(0, unix.EFD_CLOEXEC); err != nil {
			return res, err
		}
		if q.call, err = unix.Eventfd(0, unix.EFD_CLOEXEC); err != nil {
			return res, err
		}
		defer unix.Close(q.kick)
		defer unix.Close(q.call)

		if err := c.send(reqSetVringNum, u32pair(uint32(i), uint32(qsize))); err != nil {
			return res, err
		}
		if err := c.send(reqSetVringBase, u32pair(uint32(i), 0)); err != nil {
			return res, err
		}
		addr := make([]byte, 40)
		binary.LittleEndian.PutUint32(addr[0:], uint32(i))
		binary.LittleEndian.PutUint64(addr[8:], guestUserspaceBase+uint64(descOff))
		binary.LittleEndian.PutUint64(addr[16:], guestUserspaceBase+uint64(usedOff))
		binary.LittleEndian.PutUint64(addr[24:], guestUserspaceBase+uint64(availOff))
		if err := c.send(reqSetVringAddr, addr); err != nil {
			return res, err
		}
		idx := make([]byte, 8)
		binary.LittleEndian.PutUint64(idx, uint64(i))
		if err := c.send(reqSetVringCall, idx, q.call); err != nil {
			return res, err
		}
		if err := c.send(reqSetVringKick, idx, q.kick); err != nil {
			return res, err
		}
		if err := c.send(reqSetVringEnable, u32pair(uint32(i), 1)); err != nil {
			return res, err
		}
	}
	// A round trip so every message above has been applied before we start.
	if _, err := c.query(reqGetFeatures); err != nil {
		return res, err
	}

	eventIdx := acked&featEventIdx != 0
	blocks := cfg.span / int64(cfg.bs)
	if blocks <= 0 {
		return res, errors.New("span smaller than block size")
	}
	start := time.Now()
	deadline := start.Add(cfg.dur)
	var wg sync.WaitGroup
	for _, q := range qs {
		wg.Add(1)
		go func(q *queue) {
			defer wg.Done()
			runtime.LockOSThread()
			q.run(cfg, eventIdx, blocks, deadline, rand.New(rand.NewSource(int64(q.idx)+1)))
		}(q)
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()

	var all []time.Duration
	for _, q := range qs {
		res.Requests += q.nreq
		res.Errors += q.nerr
		res.Mismatches += q.nbad
		res.Kicks += q.nkick
		res.Calls += q.ncall
		all = append(all, q.lat...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	res.Seconds = elapsed
	res.IOPS = float64(res.Requests) / elapsed
	res.MBps = res.IOPS * float64(cfg.bs) / 1e6
	if len(all) > 0 {
		res.P50us = float64(all[len(all)/2].Nanoseconds()) / 1e3
		res.P99us = float64(all[len(all)*99/100].Nanoseconds()) / 1e3
	}
	return res, nil
}

func (q *queue) setDesc(i int, gpa uint64, length uint32, flags, next uint16) {
	d := q.desc[i*16 : i*16+16]
	binary.LittleEndian.PutUint64(d[0:], gpa)
	binary.LittleEndian.PutUint32(d[8:], length)
	binary.LittleEndian.PutUint16(d[12:], flags)
	binary.LittleEndian.PutUint16(d[14:], next)
}

// submit writes slot's request (header, data segments, status) as a
// descriptor chain starting at descriptor slot*descPerRequest and puts it on
// the avail ring. The caller publishes avail.idx and kicks.
func (q *queue) submit(slot int, cfg driverConfig, sector uint64) {
	base := slot * q.slotStride
	area := q.reqArea[base : base+q.slotStride]
	gpa := q.reqAreaGPA + uint64(base)
	// Header and status share the first page; data starts on the second.
	binary.LittleEndian.PutUint32(area[0:], virtioBlkTIn)
	binary.LittleEndian.PutUint32(area[4:], 0)
	binary.LittleEndian.PutUint64(area[8:], sector)
	area[64] = 0xff
	head := slot * q.descPerRequest
	d := head
	q.setDesc(d, gpa, 16, descFNext, uint16(d+1))
	d++
	for off := 0; off < cfg.bs; off += cfg.seg {
		n := cfg.seg
		if off+n > cfg.bs {
			n = cfg.bs - off
		}
		q.setDesc(d, gpa+4096+uint64(off), uint32(n), descFWrite|descFNext, uint16(d+1))
		d++
	}
	q.setDesc(d, gpa+64, 1, descFWrite, 0)
	ring := 4 + 2*int(q.availIdx%uint16(q.size))
	binary.LittleEndian.PutUint16(q.avail[ring:], uint16(head))
	q.availIdx++
	q.sectors[slot] = sector
	q.submitted[slot] = time.Now()
}

func (q *queue) publish(eventIdx bool, oldIdx uint16) {
	fence()
	binary.LittleEndian.PutUint16(q.avail[2:], q.availIdx)
	fence()
	kick := true
	if eventIdx {
		// vring_need_event(avail_event, new, old)
		availEvent := binary.LittleEndian.Uint16(q.used[4+8*q.size:])
		kick = uint16(q.availIdx-availEvent-1) < uint16(q.availIdx-oldIdx)
	}
	if kick {
		var one [8]byte
		binary.LittleEndian.PutUint64(one[:], 1)
		_, _ = unix.Write(q.kick, one[:])
		q.nkick++
	}
}

func (q *queue) run(cfg driverConfig, eventIdx bool, blocks int64, deadline time.Time, rng *rand.Rand) {
	sectorsPerBlock := uint64(cfg.bs / 512)
	next := func() uint64 { return uint64(rng.Int63n(blocks)) * sectorsPerBlock }
	old := q.availIdx
	for s := 0; s < cfg.qd; s++ {
		q.submit(s, cfg, next())
	}
	q.publish(eventIdx, old)
	inflight := cfg.qd
	q.lat = make([]time.Duration, 0, 1<<16)
	var buf [8]byte
	stopping := false
	for inflight > 0 {
		usedIdx := binary.LittleEndian.Uint16(q.used[2:])
		if usedIdx == q.lastUsed {
			if eventIdx {
				// used_event lives after the avail ring.
				binary.LittleEndian.PutUint16(q.avail[4+2*q.size:], q.lastUsed)
				fence()
				if binary.LittleEndian.Uint16(q.used[2:]) != q.lastUsed {
					continue
				}
			}
			if _, err := unix.Read(q.call, buf[:]); err != nil && err != unix.EINTR {
				return
			}
			q.ncall++
			continue
		}
		fence()
		now := time.Now()
		if now.After(deadline) {
			stopping = true
		}
		old := q.availIdx
		resubmitted := false
		for q.lastUsed != usedIdx {
			e := 4 + 8*int(q.lastUsed%uint16(q.size))
			head := int(binary.LittleEndian.Uint32(q.used[e:]))
			q.lastUsed++
			slot := head / q.descPerRequest
			q.lat = append(q.lat, now.Sub(q.submitted[slot]))
			q.nreq++
			if q.reqArea[slot*q.slotStride+64] != 0 {
				q.nerr++
			} else if cfg.verify != nil {
				want := make([]byte, cfg.bs)
				_, _ = cfg.verify.ReadAt(want, int64(q.sectors[slot]*512))
				data := q.reqArea[slot*q.slotStride+4096 : slot*q.slotStride+4096+cfg.bs]
				if string(data) != string(want) {
					q.nbad++
				}
			}
			if stopping {
				inflight--
				continue
			}
			q.submit(slot, cfg, next())
			resubmitted = true
		}
		if resubmitted {
			q.publish(eventIdx, old)
		}
	}
}

// procCPU returns user+system CPU seconds consumed so far by pid.
func procCPU(pid int) (float64, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// Fields after the ")" of comm: state is field 3; utime 14, stime 15.
	s := string(b)
	i := len(s) - 1
	for i >= 0 && s[i] != ')' {
		i--
	}
	var fields []string
	cur := ""
	for _, r := range s[i+2:] {
		if r == ' ' {
			fields = append(fields, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	fields = append(fields, cur)
	var ut, st float64
	fmt.Sscan(fields[11], &ut)
	fmt.Sscan(fields[12], &st)
	return (ut + st) / 100.0, nil
}
