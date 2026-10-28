package extents

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// vhost-user protocol message types.
const (
	VhostUserGetFeatures         uint32 = 1
	VhostUserSetFeatures         uint32 = 2
	VhostUserSetOwner            uint32 = 3
	VhostUserResetOwner          uint32 = 4
	VhostUserSetMemTable         uint32 = 5
	VhostUserSetLogBase          uint32 = 6
	VhostUserSetLogFD            uint32 = 7
	VhostUserSetVringNum         uint32 = 8
	VhostUserSetVringAddr        uint32 = 9
	VhostUserSetVringBase        uint32 = 10
	VhostUserGetVringBase        uint32 = 11
	VhostUserSetVringKick        uint32 = 12
	VhostUserSetVringCall        uint32 = 13
	VhostUserSetVringErr         uint32 = 14
	VhostUserGetProtocolFeatures uint32 = 15
	VhostUserSetProtocolFeatures uint32 = 16
	VhostUserGetQueueNum         uint32 = 17
	VhostUserSetVringEnable      uint32 = 18
	VhostUserGetConfig           uint32 = 24
	VhostUserSetConfig           uint32 = 25
	VhostUserGetMaxMemSlots      uint32 = 36
	VhostUserAddMemReg           uint32 = 37
	VhostUserRemMemReg           uint32 = 38
)

// vhost-user header flags.
const (
	vhostUserHeaderSize     = 12
	vhostUserVersion        = 0x1
	vhostUserReplyFlag      = 0x4
	vhostUserNeedReplyMask  = 0x8
	vhostUserVringIndexMask = 0xff
	vhostUserVringNoFDMask  = 1 << 8
)

// Virtio and vhost-user feature bits.
const (
	VirtioBlkFSizeMax     uint64 = 1 << 1
	VirtioBlkFSegMax      uint64 = 1 << 2
	VirtioBlkFRO          uint64 = 1 << 5
	VirtioBlkFBlkSize     uint64 = 1 << 6
	VirtioBlkFFlush       uint64 = 1 << 9
	VirtioBlkFMQ          uint64 = 1 << 12
	VirtioBlkFDiscard     uint64 = 1 << 13
	VirtioBlkFWriteZeroes uint64 = 1 << 14

	VirtioRingFIndirectDesc    uint64 = 1 << 28
	VirtioRingFEventIdx        uint64 = 1 << 29
	VhostUserFProtocolFeatures uint64 = 1 << 30
	VirtioFVersion1            uint64 = 1 << 32
)

// vhost-user protocol feature bits.
const (
	VhostUserProtocolFMQ                uint64 = 1 << 0
	VhostUserProtocolFReplyAck          uint64 = 1 << 3
	VhostUserProtocolFConfig            uint64 = 1 << 9
	VhostUserProtocolFConfigureMemSlots uint64 = 1 << 15
)

// virtio-blk request types and status codes.
const (
	VirtioBlkTIn          uint32 = 0
	VirtioBlkTOut         uint32 = 1
	VirtioBlkTFlush       uint32 = 4
	VirtioBlkTGetID       uint32 = 8
	VirtioBlkTDiscard     uint32 = 11
	VirtioBlkTWriteZeroes uint32 = 13

	VirtioBlkSOK     byte = 0
	VirtioBlkSIOErr  byte = 1
	VirtioBlkSUnsupp byte = 2

	virtioBlkIDBytes = 20
)

// Virtqueue descriptor flags.
const (
	VringDescFNext     uint16 = 1
	VringDescFWrite    uint16 = 2
	VringDescFIndirect uint16 = 4
)

const (
	defaultMaxQueues   = 16
	defaultMaxMemSlots = 512
)

// DefaultWorkersPerQueue is how many requests one virtqueue serves at once:
// each is a goroutine blocked in pread(2)/preadv(2), the way io_uring's
// io-wq serves buffered reads. It is capped at the queue size.
const DefaultWorkersPerQueue = 32

// MemRegion represents a shared guest memory region mapped into the server process.
type MemRegion struct {
	GuestPhysAddr uint64
	MemorySize    uint64
	UserspaceAddr uint64
	MmapOffset    uint64
	rawMmap       []byte
	data          []byte
}

type vringDesc struct {
	addr   uint64
	length uint32
	flags  uint16
	next   uint16
}

type vringQueue struct {
	mu           sync.Mutex
	index        int
	num          uint32
	descAddr     uint64
	usedAddr     uint64
	availAddr    uint64
	addrSet      bool
	descSlice    []byte
	availSlice   []byte
	usedSlice    []byte
	lastAvailIdx uint16
	kickFD       int
	callFD       int
	enabled      bool
	workerStopFD int
	workerDone   chan struct{}
	// workerStopping is the done channel of the worker being stopped, if any.
	workerStopping chan struct{}
	// callFDShadow mirrors callFD for the I/O workers, which complete
	// requests without taking mu.
	callFDShadow atomic.Int32
}

func newVringQueue(index int) *vringQueue {
	q := &vringQueue{
		index:        index,
		kickFD:       -1,
		callFD:       -1,
		enabled:      true,
		workerStopFD: -1,
	}
	q.callFDShadow.Store(-1)
	return q
}

// Server serves an extent Store over the vhost-user-blk Unix domain socket protocol.
type Server struct {
	store      *Store
	socketPath string
	numQueues  int
	listener   *net.UnixListener

	mu              sync.RWMutex
	ackedFeatures   uint64
	ackedProtoFeats uint64
	memRegions      []*MemRegion
	// memSnap is memRegions as the I/O path reads it: a copy published after
	// every change under mu, so translating a descriptor takes no lock.
	memSnap         atomic.Pointer[[]*MemRegion]
	queues          []*vringQueue
	closed          atomic.Bool
	wg              sync.WaitGroup
	activeConnMu    sync.Mutex
	activeConn      *net.UnixConn
	workersPerQueue int
}

// SetWorkersPerQueue sets how many requests each virtqueue serves
// concurrently (DefaultWorkersPerQueue if n <= 0). 1 restores strictly serial
// service. It applies to queues started after the call.
func (s *Server) SetWorkersPerQueue(n int) {
	s.mu.Lock()
	s.workersPerQueue = n
	s.mu.Unlock()
}

// NewServer creates and binds a new vhost-user-blk Server on socketPath.
func NewServer(store *Store, socketPath string, numQueues int) (*Server, error) {
	if store == nil {
		return nil, errors.New("extents: NewServer requires a non-nil Store")
	}
	if socketPath == "" {
		return nil, errors.New("extents: NewServer requires a socketPath")
	}
	if numQueues <= 0 {
		numQueues = 4
	}
	if numQueues > defaultMaxQueues {
		numQueues = defaultMaxQueues
	}

	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("extents: creating socket directory %s: %w", dir, err)
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("extents: removing stale socket %s: %w", socketPath, err)
	}

	addr := &net.UnixAddr{Name: socketPath, Net: "unix"}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("extents: listening on %s: %w", socketPath, err)
	}
	_ = os.Chmod(socketPath, 0o600)

	queues := make([]*vringQueue, numQueues)
	for i := range queues {
		queues[i] = newVringQueue(i)
	}

	return &Server{
		store:      store,
		socketPath: socketPath,
		numQueues:  numQueues,
		listener:   ln,
		queues:     queues,
	}, nil
}

// SocketPath returns the Unix socket path the server is listening on.
func (s *Server) SocketPath() string {
	return s.socketPath
}

// Start begins accepting vhost-user client connections in the background.
func (s *Server) Start() {
	s.wg.Add(1)
	go s.acceptLoop()
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			if s.closed.Load() {
				return
			}
			continue
		}
		s.activeConnMu.Lock()
		if s.activeConn != nil {
			_ = s.activeConn.Close()
		}
		s.activeConn = conn
		s.activeConnMu.Unlock()

		s.handleConn(conn)
	}
}

// Close stops all queue workers, unmaps guest memory regions, closes the listener,
// and removes the Unix socket.
func (s *Server) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	_ = s.listener.Close()

	s.activeConnMu.Lock()
	if s.activeConn != nil {
		_ = s.activeConn.Close()
		s.activeConn = nil
	}
	s.activeConnMu.Unlock()

	s.stopAllQueues()
	s.unmapAllMemory()
	s.wg.Wait()
	_ = os.Remove(s.socketPath)
	return nil
}

func (s *Server) stopAllQueues() {
	for _, q := range s.queues {
		s.stopQueueWorker(q)
		q.mu.Lock()
		if q.kickFD >= 0 {
			_ = unix.Close(q.kickFD)
			q.kickFD = -1
		}
		if q.callFD >= 0 {
			q.callFDShadow.Store(-1)
			_ = unix.Close(q.callFD)
			q.callFD = -1
		}
		q.addrSet = false
		q.descSlice = nil
		q.availSlice = nil
		q.usedSlice = nil
		q.mu.Unlock()
	}
}

func (s *Server) unmapAllMemory() {
	s.mu.Lock()
	regions := s.memRegions
	s.memRegions = nil
	s.publishMemLocked()
	s.mu.Unlock()

	for _, r := range regions {
		if r.rawMmap != nil {
			_ = unix.Munmap(r.rawMmap)
			r.rawMmap = nil
			r.data = nil
		}
	}
}

func (s *Server) handleConn(conn *net.UnixConn) {
	defer func() {
		s.stopAllQueues()
		s.unmapAllMemory()
		_ = conn.Close()
	}()

	rawConn, err := conn.SyscallConn()
	if err != nil {
		return
	}
	var sockFD int
	if err := rawConn.Control(func(fd uintptr) {
		sockFD = int(fd)
	}); err != nil {
		return
	}

	for !s.closed.Load() {
		req, flags, payload, fds, err := readVhostMsg(sockFD)
		if err != nil {
			closeFDs(fds)
			return
		}
		if err := s.dispatchMsg(sockFD, req, flags, payload, fds); err != nil {
			return
		}
	}
}

func recvmsgBlocking(fd int, p, oob []byte) (n, oobn int, err error) {
	for {
		n, oobn, _, _, err = unix.Recvmsg(fd, p, oob, 0)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
			pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			_, _ = unix.Poll(pfd, 50)
			continue
		}
		return n, oobn, err
	}
}

func readBlocking(fd int, p []byte) (int, error) {
	for {
		n, err := unix.Read(fd, p)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
			pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			_, _ = unix.Poll(pfd, 50)
			continue
		}
		return n, err
	}
}

func readVhostMsg(fd int) (req uint32, flags uint32, payload []byte, fds []int, err error) {
	var hdr [vhostUserHeaderSize]byte
	oob := make([]byte, unix.CmsgSpace(8*4))

	n, oobn, err := recvmsgBlocking(fd, hdr[:], oob)
	if err != nil {
		return 0, 0, nil, nil, err
	}
	if n == 0 {
		return 0, 0, nil, nil, io.EOF
	}
	if oobn > 0 {
		parsed, pErr := parseSCMFileDescriptors(oob[:oobn])
		if pErr != nil {
			return 0, 0, nil, nil, pErr
		}
		fds = append(fds, parsed...)
	}

	readBytes := n
	for readBytes < vhostUserHeaderSize {
		m, rErr := readBlocking(fd, hdr[readBytes:])
		if rErr != nil {
			closeFDs(fds)
			return 0, 0, nil, nil, rErr
		}
		if m == 0 {
			closeFDs(fds)
			return 0, 0, nil, nil, io.ErrUnexpectedEOF
		}
		readBytes += m
	}

	req = binary.LittleEndian.Uint32(hdr[0:4])
	flags = binary.LittleEndian.Uint32(hdr[4:8])
	size := binary.LittleEndian.Uint32(hdr[8:12])

	if size > 0 {
		if size > 64*1024 {
			closeFDs(fds)
			return 0, 0, nil, nil, fmt.Errorf("extents: vhost-user payload size %d exceeds maximum", size)
		}
		payload = make([]byte, size)
		pRead := 0
		for pRead < int(size) {
			var m int
			var oobn2 int
			var rErr error
			if len(fds) == 0 && pRead == 0 {
				m, oobn2, rErr = recvmsgBlocking(fd, payload[pRead:], oob)
				if oobn2 > 0 {
					parsed, pErr := parseSCMFileDescriptors(oob[:oobn2])
					if pErr == nil {
						fds = append(fds, parsed...)
					}
				}
			} else {
				m, rErr = readBlocking(fd, payload[pRead:])
			}
			if rErr != nil {
				closeFDs(fds)
				return 0, 0, nil, nil, rErr
			}
			if m == 0 {
				closeFDs(fds)
				return 0, 0, nil, nil, io.ErrUnexpectedEOF
			}
			pRead += m
		}
	}

	return req, flags, payload, fds, nil
}

func parseSCMFileDescriptors(oob []byte) ([]int, error) {
	cmsgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, err
	}
	var fds []int
	for _, cmsg := range cmsgs {
		rights, err := unix.ParseUnixRights(&cmsg)
		if err != nil {
			closeFDs(fds)
			return nil, err
		}
		fds = append(fds, rights...)
	}
	return fds, nil
}

func closeFDs(fds []int) {
	for _, fd := range fds {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}
}

func writeVhostReply(fd int, req uint32, payload []byte) error {
	buf := make([]byte, vhostUserHeaderSize+len(payload))
	binary.LittleEndian.PutUint32(buf[0:4], req)
	binary.LittleEndian.PutUint32(buf[4:8], vhostUserVersion|vhostUserReplyFlag)
	binary.LittleEndian.PutUint32(buf[8:12], uint32(len(payload)))
	copy(buf[vhostUserHeaderSize:], payload)

	written := 0
	for written < len(buf) {
		n, err := unix.Write(fd, buf[written:])
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
				pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
				_, _ = unix.Poll(pfd, 50)
				continue
			}
			return err
		}
		written += n
	}
	return nil
}

func writeVhostU64Reply(fd int, req uint32, val uint64) error {
	var payload [8]byte
	binary.LittleEndian.PutUint64(payload[:], val)
	return writeVhostReply(fd, req, payload[:])
}

func (s *Server) maybeAck(fd int, req, flags uint32, errCode uint64) error {
	s.mu.RLock()
	replyAck := (s.ackedProtoFeats & VhostUserProtocolFReplyAck) != 0
	s.mu.RUnlock()

	if (flags&vhostUserNeedReplyMask) != 0 && replyAck {
		return writeVhostU64Reply(fd, req, errCode)
	}
	return nil
}

func (s *Server) dispatchMsg(fd int, req, flags uint32, payload []byte, fds []int) error {
	switch req {
	case VhostUserGetFeatures:
		closeFDs(fds)
		feats := VirtioFVersion1 |
			VhostUserFProtocolFeatures |
			VirtioRingFIndirectDesc |
			VirtioRingFEventIdx |
			VirtioBlkFSizeMax |
			VirtioBlkFSegMax |
			VirtioBlkFBlkSize |
			VirtioBlkFFlush |
			VirtioBlkFMQ |
			VirtioBlkFDiscard |
			VirtioBlkFWriteZeroes
		if s.store.ReadOnly() {
			feats |= VirtioBlkFRO
		}
		return writeVhostU64Reply(fd, req, feats)

	case VhostUserSetFeatures:
		closeFDs(fds)
		if len(payload) < 8 {
			return s.maybeAck(fd, req, flags, 1)
		}
		s.mu.Lock()
		s.ackedFeatures = binary.LittleEndian.Uint64(payload[:8])
		s.mu.Unlock()
		return s.maybeAck(fd, req, flags, 0)

	case VhostUserGetProtocolFeatures:
		closeFDs(fds)
		protoFeats := VhostUserProtocolFMQ |
			VhostUserProtocolFReplyAck |
			VhostUserProtocolFConfig |
			VhostUserProtocolFConfigureMemSlots
		return writeVhostU64Reply(fd, req, protoFeats)

	case VhostUserSetProtocolFeatures:
		closeFDs(fds)
		if len(payload) < 8 {
			return s.maybeAck(fd, req, flags, 1)
		}
		s.mu.Lock()
		s.ackedProtoFeats = binary.LittleEndian.Uint64(payload[:8])
		s.mu.Unlock()
		return s.maybeAck(fd, req, flags, 0)

	case VhostUserGetQueueNum:
		closeFDs(fds)
		return writeVhostU64Reply(fd, req, uint64(s.numQueues))

	case VhostUserGetMaxMemSlots:
		closeFDs(fds)
		return writeVhostU64Reply(fd, req, defaultMaxMemSlots)

	case VhostUserSetOwner, VhostUserResetOwner, VhostUserSetLogBase, VhostUserSetLogFD, VhostUserSetConfig:
		closeFDs(fds)
		return s.maybeAck(fd, req, flags, 0)

	case VhostUserGetConfig:
		closeFDs(fds)
		if len(payload) < 12 {
			return errors.New("extents: VHOST_USER_GET_CONFIG payload too short")
		}
		cfgOffset := binary.LittleEndian.Uint32(payload[0:4])
		cfgSize := binary.LittleEndian.Uint32(payload[4:8])
		cfgFlags := binary.LittleEndian.Uint32(payload[8:12])

		blkCfg := s.buildVirtioBlkConfig()
		reply := make([]byte, 12+int(cfgSize))
		binary.LittleEndian.PutUint32(reply[0:4], cfgOffset)
		binary.LittleEndian.PutUint32(reply[4:8], cfgSize)
		binary.LittleEndian.PutUint32(reply[8:12], cfgFlags)
		if int(cfgOffset) < len(blkCfg) {
			end := min(int(cfgOffset+cfgSize), len(blkCfg))
			copy(reply[12:], blkCfg[cfgOffset:end])
		}
		return writeVhostReply(fd, req, reply)

	case VhostUserSetMemTable:
		if err := s.handleSetMemTable(payload, fds); err != nil {
			_ = s.maybeAck(fd, req, flags, 1)
			return err
		}
		return s.maybeAck(fd, req, flags, 0)

	case VhostUserAddMemReg:
		if err := s.handleAddMemReg(payload, fds); err != nil {
			_ = s.maybeAck(fd, req, flags, 1)
			return err
		}
		return s.maybeAck(fd, req, flags, 0)

	case VhostUserRemMemReg:
		closeFDs(fds)
		if err := s.handleRemMemReg(payload); err != nil {
			_ = s.maybeAck(fd, req, flags, 1)
			return err
		}
		return s.maybeAck(fd, req, flags, 0)

	case VhostUserSetVringNum:
		closeFDs(fds)
		if len(payload) < 8 {
			return s.maybeAck(fd, req, flags, 1)
		}
		idx := int(binary.LittleEndian.Uint32(payload[0:4]))
		num := binary.LittleEndian.Uint32(payload[4:8])
		if idx < 0 || idx >= len(s.queues) {
			return s.maybeAck(fd, req, flags, 1)
		}
		q := s.queues[idx]
		s.stopQueueWorker(q)
		q.mu.Lock()
		q.num = num
		q.mu.Unlock()
		return s.maybeAck(fd, req, flags, 0)

	case VhostUserSetVringAddr:
		closeFDs(fds)
		if len(payload) < 40 {
			return s.maybeAck(fd, req, flags, 1)
		}
		idx := int(binary.LittleEndian.Uint32(payload[0:4]))
		descAddr := binary.LittleEndian.Uint64(payload[8:16])
		usedAddr := binary.LittleEndian.Uint64(payload[16:24])
		availAddr := binary.LittleEndian.Uint64(payload[24:32])
		if idx < 0 || idx >= len(s.queues) {
			return s.maybeAck(fd, req, flags, 1)
		}
		q := s.queues[idx]
		s.stopQueueWorker(q)
		if err := s.configureQueueAddresses(q, descAddr, usedAddr, availAddr); err != nil {
			return s.maybeAck(fd, req, flags, 1)
		}
		s.maybeStartQueueWorker(q)
		return s.maybeAck(fd, req, flags, 0)

	case VhostUserSetVringBase:
		closeFDs(fds)
		if len(payload) < 8 {
			return s.maybeAck(fd, req, flags, 1)
		}
		idx := int(binary.LittleEndian.Uint32(payload[0:4]))
		base := uint16(binary.LittleEndian.Uint32(payload[4:8]))
		if idx < 0 || idx >= len(s.queues) {
			return s.maybeAck(fd, req, flags, 1)
		}
		q := s.queues[idx]
		s.stopQueueWorker(q)
		q.mu.Lock()
		q.lastAvailIdx = base
		q.mu.Unlock()
		s.maybeStartQueueWorker(q)
		return s.maybeAck(fd, req, flags, 0)

	case VhostUserGetVringBase:
		closeFDs(fds)
		if len(payload) < 8 {
			return errors.New("extents: VHOST_USER_GET_VRING_BASE payload too short")
		}
		idx := int(binary.LittleEndian.Uint32(payload[0:4]))
		if idx < 0 || idx >= len(s.queues) {
			return errors.New("extents: invalid queue index in VHOST_USER_GET_VRING_BASE")
		}
		q := s.queues[idx]
		s.stopQueueWorker(q)
		q.mu.Lock()
		base := q.lastAvailIdx
		q.mu.Unlock()
		var reply [8]byte
		binary.LittleEndian.PutUint32(reply[0:4], uint32(idx))
		binary.LittleEndian.PutUint32(reply[4:8], uint32(base))
		return writeVhostReply(fd, req, reply[:])

	case VhostUserSetVringKick:
		if len(payload) < 8 {
			closeFDs(fds)
			return s.maybeAck(fd, req, flags, 1)
		}
		u64Val := binary.LittleEndian.Uint64(payload[:8])
		idx := int(u64Val & vhostUserVringIndexMask)
		noFD := (u64Val & vhostUserVringNoFDMask) != 0
		if idx < 0 || idx >= len(s.queues) {
			closeFDs(fds)
			return s.maybeAck(fd, req, flags, 1)
		}
		q := s.queues[idx]
		s.stopQueueWorker(q)
		q.mu.Lock()
		if q.kickFD >= 0 {
			_ = unix.Close(q.kickFD)
			q.kickFD = -1
		}
		if !noFD && len(fds) > 0 {
			q.kickFD = fds[0]
			closeFDs(fds[1:])
		} else {
			closeFDs(fds)
		}
		q.mu.Unlock()
		s.maybeStartQueueWorker(q)
		return s.maybeAck(fd, req, flags, 0)

	case VhostUserSetVringCall:
		if len(payload) < 8 {
			closeFDs(fds)
			return s.maybeAck(fd, req, flags, 1)
		}
		u64Val := binary.LittleEndian.Uint64(payload[:8])
		idx := int(u64Val & vhostUserVringIndexMask)
		noFD := (u64Val & vhostUserVringNoFDMask) != 0
		if idx < 0 || idx >= len(s.queues) {
			closeFDs(fds)
			return s.maybeAck(fd, req, flags, 1)
		}
		q := s.queues[idx]
		q.mu.Lock()
		if q.callFD >= 0 {
			q.callFDShadow.Store(-1)
			_ = unix.Close(q.callFD)
			q.callFD = -1
		}
		if !noFD && len(fds) > 0 {
			q.callFD = fds[0]
			q.callFDShadow.Store(int32(fds[0]))
			closeFDs(fds[1:])
		} else {
			closeFDs(fds)
		}
		q.mu.Unlock()
		return s.maybeAck(fd, req, flags, 0)

	case VhostUserSetVringErr:
		closeFDs(fds)
		return s.maybeAck(fd, req, flags, 0)

	case VhostUserSetVringEnable:
		closeFDs(fds)
		if len(payload) < 8 {
			return s.maybeAck(fd, req, flags, 1)
		}
		idx := int(binary.LittleEndian.Uint32(payload[0:4]))
		enable := binary.LittleEndian.Uint32(payload[4:8]) != 0
		if idx < 0 || idx >= len(s.queues) {
			return s.maybeAck(fd, req, flags, 1)
		}
		q := s.queues[idx]
		q.mu.Lock()
		q.enabled = enable
		q.mu.Unlock()
		if enable {
			s.maybeStartQueueWorker(q)
		} else {
			s.stopQueueWorker(q)
		}
		return s.maybeAck(fd, req, flags, 0)

	default:
		closeFDs(fds)
		return s.maybeAck(fd, req, flags, 0)
	}
}

func (s *Server) buildVirtioBlkConfig() []byte {
	var cfg [60]byte
	vsize := s.store.VirtualSize()
	extSize := s.store.ExtentSize()
	sectors := vsize / 512
	maxDiscardSectors := uint32(min(extSize/512, 0x7fffff))
	if maxDiscardSectors == 0 {
		maxDiscardSectors = 2048
	}

	// struct virtio_blk_config layout (little-endian):
	// 0..8:   capacity (in 512-byte sectors)
	// 8..12:  size_max
	// 12..16: seg_max
	// 16..20: geometry (cylinders u16, heads u8, sectors u8)
	// 20..24: blk_size
	// 24..32: topology (physical_block_exp u8, alignment_offset u8, min_io_size u16, opt_io_size u32)
	// 32..33: writeback
	// 33..34: unused0
	// 34..36: num_queues
	// 36..40: max_discard_sectors
	// 40..44: max_discard_seg
	// 44..48: discard_sector_alignment
	// 48..52: max_write_zeroes_sectors
	// 52..56: max_write_zeroes_seg
	// 56..57: write_zeroes_may_unmap
	binary.LittleEndian.PutUint64(cfg[0:8], sectors)
	binary.LittleEndian.PutUint32(cfg[8:12], 1<<20) // 1 MiB max segment size
	binary.LittleEndian.PutUint32(cfg[12:16], 128)  // 128 max segments
	binary.LittleEndian.PutUint32(cfg[20:24], 512)  // 512-byte logical block size
	cfg[24] = 3                                     // 4096-byte physical block size (512 << 3)
	cfg[32] = 1                                     // writeback cache enabled (paired with VIRTIO_BLK_F_FLUSH)
	binary.LittleEndian.PutUint16(cfg[34:36], uint16(s.numQueues))
	binary.LittleEndian.PutUint32(cfg[36:40], maxDiscardSectors)
	binary.LittleEndian.PutUint32(cfg[40:44], 1)
	binary.LittleEndian.PutUint32(cfg[44:48], 8) // 4 KiB sector alignment
	binary.LittleEndian.PutUint32(cfg[48:52], maxDiscardSectors)
	binary.LittleEndian.PutUint32(cfg[52:56], 1)
	cfg[56] = 1
	return cfg[:]
}

func (s *Server) handleSetMemTable(payload []byte, fds []int) error {
	if len(payload) < 8 {
		closeFDs(fds)
		return errors.New("extents: VHOST_USER_SET_MEM_TABLE payload too short")
	}
	nregions := int(binary.LittleEndian.Uint32(payload[0:4]))
	if len(payload) < 8+nregions*32 || len(fds) < nregions {
		closeFDs(fds)
		return fmt.Errorf("extents: VHOST_USER_SET_MEM_TABLE region count mismatch (nregions=%d, payload=%d, fds=%d)", nregions, len(payload), len(fds))
	}

	newRegions := make([]*MemRegion, 0, nregions)
	for i := 0; i < nregions; i++ {
		off := 8 + i*32
		reg, err := mmapRegion(payload[off:off+32], fds[i])
		if err != nil {
			for _, r := range newRegions {
				_ = unix.Munmap(r.rawMmap)
			}
			closeFDs(fds[i+1:])
			return err
		}
		newRegions = append(newRegions, reg)
	}
	closeFDs(fds[nregions:])

	s.unmapAllMemory()
	s.mu.Lock()
	s.memRegions = newRegions
	s.publishMemLocked()
	s.mu.Unlock()
	return nil
}

func (s *Server) handleAddMemReg(payload []byte, fds []int) error {
	if len(fds) < 1 {
		return errors.New("extents: VHOST_USER_ADD_MEM_REG missing fd")
	}
	var regBytes []byte
	if len(payload) >= 40 {
		// VhostUserSingleMemReg has 8 bytes of padding before the 32-byte region struct.
		regBytes = payload[8:40]
	} else if len(payload) >= 32 {
		regBytes = payload[0:32]
	} else {
		closeFDs(fds)
		return errors.New("extents: VHOST_USER_ADD_MEM_REG payload too short")
	}

	reg, err := mmapRegion(regBytes, fds[0])
	closeFDs(fds[1:])
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.memRegions = append(s.memRegions, reg)
	s.publishMemLocked()
	s.mu.Unlock()
	return nil
}

func (s *Server) handleRemMemReg(payload []byte) error {
	var regBytes []byte
	if len(payload) >= 40 {
		regBytes = payload[8:40]
	} else if len(payload) >= 32 {
		regBytes = payload[0:32]
	} else {
		return errors.New("extents: VHOST_USER_REM_MEM_REG payload too short")
	}
	gpa := binary.LittleEndian.Uint64(regBytes[0:8])
	memSize := binary.LittleEndian.Uint64(regBytes[8:16])

	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.memRegions {
		if r.GuestPhysAddr == gpa && r.MemorySize == memSize {
			_ = unix.Munmap(r.rawMmap)
			s.memRegions = append(s.memRegions[:i], s.memRegions[i+1:]...)
			s.publishMemLocked()
			break
		}
	}
	return nil
}

func mmapRegion(regBytes []byte, fd int) (*MemRegion, error) {
	defer unix.Close(fd)
	gpa := binary.LittleEndian.Uint64(regBytes[0:8])
	memSize := binary.LittleEndian.Uint64(regBytes[8:16])
	userAddr := binary.LittleEndian.Uint64(regBytes[16:24])
	mmapOff := binary.LittleEndian.Uint64(regBytes[24:32])

	totalLen := int(mmapOff + memSize)
	if totalLen <= 0 {
		return nil, fmt.Errorf("extents: invalid memory region size %d", totalLen)
	}
	raw, err := unix.Mmap(fd, 0, totalLen, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("extents: mmap guest memory region: %w", err)
	}
	return &MemRegion{
		GuestPhysAddr: gpa,
		MemorySize:    memSize,
		UserspaceAddr: userAddr,
		MmapOffset:    mmapOff,
		rawMmap:       raw,
		data:          raw[mmapOff : mmapOff+memSize],
	}, nil
}

// userspaceToSlice translates a VMM userspace address range [uaddr, uaddr+size)
// (used in VHOST_USER_SET_VRING_ADDR) to a mapped host slice.
func (s *Server) userspaceToSlice(uaddr, size uint64) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.memRegions {
		if uaddr >= r.UserspaceAddr && uaddr+size <= r.UserspaceAddr+r.MemorySize {
			off := uaddr - r.UserspaceAddr
			return r.data[off : off+size], nil
		}
	}
	// Fallback to GPA lookup if the caller passed a GPA directly.
	for _, r := range s.memRegions {
		if uaddr >= r.GuestPhysAddr && uaddr+size <= r.GuestPhysAddr+r.MemorySize {
			off := uaddr - r.GuestPhysAddr
			return r.data[off : off+size], nil
		}
	}
	return nil, fmt.Errorf("extents: userspace address 0x%x (size %d) not found in memory table", uaddr, size)
}

// publishMemLocked publishes a copy of memRegions for gpaToSlice. Caller
// holds mu.
func (s *Server) publishMemLocked() {
	snap := append([]*MemRegion(nil), s.memRegions...)
	s.memSnap.Store(&snap)
}

// gpaToSlice translates a guest physical address range [gpa, gpa+size)
// (used in virtqueue descriptors) to a mapped host slice.
func (s *Server) gpaToSlice(gpa, size uint64) ([]byte, error) {
	snap := s.memSnap.Load()
	if snap == nil {
		return nil, fmt.Errorf("extents: guest physical address 0x%x (size %d): no memory table", gpa, size)
	}
	for _, r := range *snap {
		if gpa >= r.GuestPhysAddr && gpa+size <= r.GuestPhysAddr+r.MemorySize {
			off := gpa - r.GuestPhysAddr
			return r.data[off : off+size], nil
		}
	}
	return nil, fmt.Errorf("extents: guest physical address 0x%x (size %d) not found in memory table", gpa, size)
}

func (s *Server) configureQueueAddresses(q *vringQueue, descAddr, usedAddr, availAddr uint64) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.num == 0 {
		return errors.New("extents: VHOST_USER_SET_VRING_ADDR before VHOST_USER_SET_VRING_NUM")
	}
	num := uint64(q.num)
	descBytes, err := s.userspaceToSlice(descAddr, 16*num)
	if err != nil {
		return err
	}
	availBytes, err := s.userspaceToSlice(availAddr, 6+2*num)
	if err != nil {
		return err
	}
	usedBytes, err := s.userspaceToSlice(usedAddr, 6+8*num)
	if err != nil {
		return err
	}

	q.descAddr = descAddr
	q.availAddr = availAddr
	q.usedAddr = usedAddr
	q.descSlice = descBytes
	q.availSlice = availBytes
	q.usedSlice = usedBytes
	q.addrSet = true
	return nil
}

func (s *Server) maybeStartQueueWorker(q *vringQueue) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.workerDone != nil {
		return
	}
	if !q.addrSet || q.num == 0 || q.kickFD < 0 || !q.enabled {
		return
	}

	stopFD, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		return
	}
	done := make(chan struct{})
	q.workerStopFD = stopFD
	q.workerDone = done

	s.wg.Add(1)
	go s.runQueueWorker(q, q.kickFD, stopFD, done)
}

func (s *Server) stopQueueWorker(q *vringQueue) {
	q.mu.Lock()
	stopFD := q.workerStopFD
	done := q.workerDone
	q.workerStopFD = -1
	q.workerDone = nil
	// A stop already under way (the connection's teardown racing Close)
	// still has to be waited for: the worker may be mid-request, reading
	// guest memory the caller is about to unmap.
	stopping := q.workerStopping
	if done != nil {
		q.workerStopping = done
	}
	q.mu.Unlock()

	if done == nil {
		if stopping != nil {
			<-stopping
		}
		return
	}
	var one [8]byte
	binary.LittleEndian.PutUint64(one[:], 1)
	_, _ = unix.Write(stopFD, one[:])
	<-done
	_ = unix.Close(stopFD)
}

// runQueueWorker is a queue's poller. It waits for kicks, pops descriptor
// chains off the avail ring and hands them to the queue's I/O workers, so the
// requests a guest has in flight on one queue reach the host's disk at once
// instead of one after another. Completions go back on the used ring in the
// order they finish, as virtio allows.
func (s *Server) runQueueWorker(q *vringQueue, kickFD, stopFD int, done chan struct{}) {
	defer s.wg.Done()
	defer close(done)

	qio := s.newQueueIO(q)
	defer qio.stop()

	var eventBuf [8]byte
	pollFDs := []unix.PollFd{
		{Fd: int32(kickFD), Events: unix.POLLIN},
		{Fd: int32(stopFD), Events: unix.POLLIN},
	}

	for {
		s.drainQueue(qio)

		_, err := unix.Poll(pollFDs, -1)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return
		}
		if (pollFDs[1].Revents & (unix.POLLIN | unix.POLLHUP | unix.POLLERR)) != 0 {
			return
		}
		if (pollFDs[0].Revents & unix.POLLIN) != 0 {
			_, _ = unix.Read(kickFD, eventBuf[:])
		}
		if (pollFDs[0].Revents & (unix.POLLHUP | unix.POLLERR | unix.POLLNVAL)) != 0 {
			s.drainQueue(qio)
			return
		}
	}
}

// queueIO is the state a running queue's poller shares with its I/O workers.
type queueIO struct {
	q          *vringQueue
	num        uint16
	descSlice  []byte
	availSlice []byte
	usedSlice  []byte
	eventIdx   bool

	reqs     chan uint16
	inflight sync.WaitGroup
	workers  sync.WaitGroup
	scratch  *reqScratch // the poller's own, for the requests it serves inline

	usedMu        sync.Mutex
	usedIdx       uint16
	signalledUsed uint16
}

// reqScratch is the per-goroutine memory one request needs, reused so the
// request path does not allocate.
type reqScratch struct {
	segments []descSegment
	bufs     [][]byte
}

func newReqScratch() *reqScratch {
	return &reqScratch{
		segments: make([]descSegment, 0, 64),
		bufs:     make([][]byte, 0, 64),
	}
}

func (s *Server) newQueueIO(q *vringQueue) *queueIO {
	q.mu.Lock()
	qio := &queueIO{
		q:          q,
		num:        uint16(q.num),
		descSlice:  q.descSlice,
		availSlice: q.availSlice,
		usedSlice:  q.usedSlice,
		scratch:    newReqScratch(),
	}
	q.mu.Unlock()
	s.mu.RLock()
	qio.eventIdx = (s.ackedFeatures & VirtioRingFEventIdx) != 0
	s.mu.RUnlock()
	if len(qio.usedSlice) >= 4 {
		qio.usedIdx = atomicLoadUint16(qio.usedSlice[2:4])
		qio.signalledUsed = qio.usedIdx
	}

	s.mu.RLock()
	workers := s.workersPerQueue
	s.mu.RUnlock()
	if workers <= 0 {
		workers = DefaultWorkersPerQueue
	}
	if n := int(qio.num); n > 0 && workers > n {
		workers = n
	}
	qio.reqs = make(chan uint16, max(int(qio.num), 1))
	for i := 0; i < workers; i++ {
		qio.workers.Add(1)
		go func() {
			defer qio.workers.Done()
			scratch := newReqScratch()
			for head := range qio.reqs {
				s.serveChain(qio, head, scratch)
				qio.inflight.Done()
			}
		}()
	}
	return qio
}

// stop waits for every request already handed out to complete, so that
// GET_VRING_BASE and a ring reconfiguration see a quiesced queue, then ends
// the workers.
func (qio *queueIO) stop() {
	qio.inflight.Wait()
	close(qio.reqs)
	qio.workers.Wait()
}

func (s *Server) serveChain(qio *queueIO, head uint16, scratch *reqScratch) {
	written, _ := s.processChain(qio.descSlice, qio.num, head, scratch, false)
	qio.complete(head, written)
}

// processDescriptorChain serves one chain, blocking as long as it takes, and
// returns the bytes written to the guest.
func (s *Server) processDescriptorChain(descTable []byte, queueSize uint16, headIdx uint16, scratch *reqScratch) uint32 {
	written, _ := s.processChain(descTable, queueSize, headIdx, scratch, false)
	return written
}

// complete puts one finished chain on the used ring and interrupts the guest
// if it asked to be (VIRTIO_RING_F_EVENT_IDX), so completions that land
// before the guest gets round to the ring share one interrupt.
func (qio *queueIO) complete(head uint16, written uint32) {
	qio.usedMu.Lock()
	used := qio.usedSlice
	slot := int(qio.usedIdx % qio.num)
	elem := 4 + 8*slot
	binary.LittleEndian.PutUint32(used[elem:elem+4], uint32(head))
	binary.LittleEndian.PutUint32(used[elem+4:elem+8], written)
	qio.usedIdx++
	newIdx := qio.usedIdx
	atomicStoreUint16(used[2:4], newIdx)

	notify := true
	if qio.eventIdx {
		usedEventOff := 4 + 2*int(qio.num)
		if usedEventOff+2 <= len(qio.availSlice) {
			// vring_need_event(used_event, new, signalled)
			usedEvent := atomicLoadUint16(qio.availSlice[usedEventOff : usedEventOff+2])
			notify = newIdx-usedEvent-1 < newIdx-qio.signalledUsed
		}
	}
	if notify {
		qio.signalledUsed = newIdx
	}
	qio.usedMu.Unlock()

	if notify {
		if callFD := int(qio.q.callFDShadow.Load()); callFD >= 0 {
			var one [8]byte
			binary.LittleEndian.PutUint64(one[:], 1)
			_, _ = unix.Write(callFD, one[:])
		}
	}
}

func atomicLoadUint16(b []byte) uint16 {
	var barrier uint32
	v := binary.LittleEndian.Uint16(b[:2])
	_ = atomic.LoadUint32(&barrier)
	return v
}

func atomicStoreUint16(b []byte, val uint16) {
	// Write 16-bit value then issue an atomic release barrier via an atomic
	// operation so prior descriptor/used-ring stores are visible before used.idx updates.
	var barrier uint32
	atomic.StoreUint32(&barrier, 1)
	binary.LittleEndian.PutUint16(b[:2], val)
	atomic.StoreUint32(&barrier, 2)
}

// drainQueue pops every available chain, serves the ones that cannot block
// and hands the rest to the queue's I/O workers.
func (s *Server) drainQueue(qio *queueIO) {
	q := qio.q
	num := qio.num
	if num == 0 || len(qio.availSlice) < 4 {
		return
	}
	availSlice := qio.availSlice
	usedSlice := qio.usedSlice

	for {
		availIdx := atomicLoadUint16(availSlice[2:4])
		q.mu.Lock()
		lastAvail := q.lastAvailIdx
		q.mu.Unlock()

		if lastAvail == availIdx {
			if qio.eventIdx {
				// Publish avail_event at used.ring[num] so guest kicks on next descriptor.
				availEventOff := 4 + 8*int(num)
				if availEventOff+2 <= len(usedSlice) {
					atomicStoreUint16(usedSlice[availEventOff:availEventOff+2], lastAvail)
				}
				// Re-check avail.idx after publishing avail_event to avoid a race.
				if atomicLoadUint16(availSlice[2:4]) == lastAvail {
					return
				}
				continue
			}
			return
		}

		for lastAvail != availIdx {
			ringSlot := int(lastAvail % num)
			headIdx := binary.LittleEndian.Uint16(availSlice[4+2*ringSlot : 6+2*ringSlot])
			lastAvail++
			// A read the page cache can answer completes right here: a
			// goroutine handoff would cost more than the read. Anything
			// that might wait for the disk goes to a worker.
			if written, ok := s.processChain(qio.descSlice, num, headIdx, qio.scratch, true); ok {
				qio.complete(headIdx, written)
				continue
			}
			qio.inflight.Add(1)
			qio.reqs <- headIdx
		}

		q.mu.Lock()
		q.lastAvailIdx = lastAvail
		q.mu.Unlock()
	}
}

func readDesc(table []byte, idx uint16) vringDesc {
	off := int(idx) * 16
	return vringDesc{
		addr:   binary.LittleEndian.Uint64(table[off : off+8]),
		length: binary.LittleEndian.Uint32(table[off+8 : off+12]),
		flags:  binary.LittleEndian.Uint16(table[off+12 : off+14]),
		next:   binary.LittleEndian.Uint16(table[off+14 : off+16]),
	}
}

type descSegment struct {
	buf      []byte
	writable bool
}

// processChain serves one chain. With nowait it only serves a read the page
// cache can answer (or a malformed chain), and otherwise returns ok=false
// having written nothing the guest can see as a completion, so the chain can
// be served again by a goroutine that may block.
func (s *Server) processChain(descTable []byte, queueSize uint16, headIdx uint16, scratch *reqScratch, nowait bool) (written uint32, ok bool) {
	if headIdx >= queueSize {
		return 0, true
	}

	segments := scratch.segments[:0]
	defer func() {
		// Keep the grown backing array, but not the guest-memory slices in it.
		clear(segments[:cap(segments)])
		scratch.segments = segments[:0]
	}()
	curTable := descTable
	curMax := queueSize
	curIdx := headIdx

	for steps := 0; steps < int(curMax)+16; steps++ {
		if curIdx >= curMax {
			break
		}
		d := readDesc(curTable, curIdx)
		if (d.flags & VringDescFIndirect) != 0 {
			indTable, err := s.gpaToSlice(d.addr, uint64(d.length))
			if err != nil || d.length < 16 {
				break
			}
			curTable = indTable
			curMax = uint16(d.length / 16)
			curIdx = 0
			continue
		}

		if d.length > 0 {
			slice, err := s.gpaToSlice(d.addr, uint64(d.length))
			if err != nil {
				return 0, true
			}
			segments = append(segments, descSegment{
				buf:      slice,
				writable: (d.flags & VringDescFWrite) != 0,
			})
		}

		if (d.flags & VringDescFNext) == 0 {
			break
		}
		curIdx = d.next
	}

	if len(segments) < 2 {
		return 0, true
	}

	// The final writable byte in the chain is the 1-byte virtio_blk status field.
	lastSeg := &segments[len(segments)-1]
	if !lastSeg.writable || len(lastSeg.buf) == 0 {
		return 0, true
	}
	statusPtr := &lastSeg.buf[len(lastSeg.buf)-1]
	if len(lastSeg.buf) == 1 {
		segments = segments[:len(segments)-1]
	} else {
		lastSeg.buf = lastSeg.buf[:len(lastSeg.buf)-1]
	}

	// Read the 16-byte virtio_blk_outhdr from the start of readable segments.
	var hdr [16]byte
	hdrRead := 0
	for len(segments) > 0 && hdrRead < 16 {
		seg := &segments[0]
		if seg.writable {
			break
		}
		n := copy(hdr[hdrRead:], seg.buf)
		hdrRead += n
		if n == len(seg.buf) {
			segments = segments[1:]
		} else {
			seg.buf = seg.buf[n:]
		}
	}
	if hdrRead < 16 {
		*statusPtr = VirtioBlkSIOErr
		return 1, true
	}

	reqType := binary.LittleEndian.Uint32(hdr[0:4])
	sector := binary.LittleEndian.Uint64(hdr[8:16])
	if nowait && reqType != VirtioBlkTIn && reqType != VirtioBlkTGetID {
		return 0, false
	}

	switch reqType {
	case VirtioBlkTIn:
		off := int64(sector * 512)
		bufs := scratch.bufs[:0]
		for _, seg := range segments {
			if seg.writable {
				bufs = append(bufs, seg.buf)
			}
		}
		var n int
		var err error
		if nowait {
			var hit bool
			if n, hit = s.store.ReadvAtNowait(bufs, off); !hit {
				clear(bufs)
				scratch.bufs = bufs[:0]
				return 0, false
			}
		} else {
			n, err = s.store.ReadvAt(bufs, off)
		}
		clear(bufs)
		scratch.bufs = bufs[:0]
		if err != nil && !errors.Is(err, io.EOF) {
			*statusPtr = VirtioBlkSIOErr
			return 1, true
		}
		*statusPtr = VirtioBlkSOK
		return uint32(n) + 1, true

	case VirtioBlkTOut:
		if s.store.ReadOnly() {
			*statusPtr = VirtioBlkSIOErr
			return 1, true
		}
		off := int64(sector * 512)
		for _, seg := range segments {
			if seg.writable {
				continue
			}
			n, err := s.store.WriteAt(seg.buf, off)
			if err != nil {
				*statusPtr = VirtioBlkSIOErr
				return 1, true
			}
			off += int64(n)
		}
		*statusPtr = VirtioBlkSOK
		return 1, true

	case VirtioBlkTFlush:
		if err := s.store.Flush(); err != nil {
			*statusPtr = VirtioBlkSIOErr
			return 1, true
		}
		*statusPtr = VirtioBlkSOK
		return 1, true

	case VirtioBlkTGetID:
		var written uint32
		id := []byte("granule-extents")
		for _, seg := range segments {
			if !seg.writable {
				continue
			}
			clear(seg.buf)
			n := copy(seg.buf, id)
			id = id[n:]
			written += uint32(len(seg.buf))
		}
		*statusPtr = VirtioBlkSOK
		return written + 1, true

	case VirtioBlkTDiscard, VirtioBlkTWriteZeroes:
		if s.store.ReadOnly() {
			*statusPtr = VirtioBlkSIOErr
			return 1, true
		}
		var dwzBuf []byte
		for _, seg := range segments {
			if !seg.writable {
				dwzBuf = append(dwzBuf, seg.buf...)
			}
		}
		if len(dwzBuf) < 16 {
			*statusPtr = VirtioBlkSIOErr
			return 1, true
		}
		for off := 0; off+16 <= len(dwzBuf); off += 16 {
			startSector := binary.LittleEndian.Uint64(dwzBuf[off : off+8])
			numSectors := binary.LittleEndian.Uint32(dwzBuf[off+8 : off+12])
			byteOff := int64(startSector * 512)
			byteLen := int64(numSectors) * 512
			var err error
			if reqType == VirtioBlkTDiscard {
				err = s.store.Discard(byteOff, byteLen)
			} else {
				err = s.store.WriteZeroes(byteOff, byteLen)
			}
			if err != nil {
				*statusPtr = VirtioBlkSIOErr
				return 1, true
			}
		}
		*statusPtr = VirtioBlkSOK
		return 1, true

	default:
		*statusPtr = VirtioBlkSUnsupp
		return 1, true
	}
}
