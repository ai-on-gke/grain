// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mux

import (
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Stream is a logical bidirectional stream multiplexed over Mux.
// It implements net.Conn and supports half-close (CloseWrite).
type Stream struct {
	mux *Mux
	id  uint32

	mu          sync.Mutex
	buf         [][]byte // FIFO queue of received payload chunks
	bufLen      int      // total buffered bytes
	notifyCh    chan struct{}
	readClosed  bool
	writeClosed bool
	closed      bool
	closeErr    error

	// sendWindow is how many more bytes the peer has agreed to buffer for us;
	// recvUnacked is how many bytes we have read but not yet credited back.
	sendWindow  int64
	recvUnacked int
	sendNotify  chan struct{}

	readDeadline  atomic.Pointer[time.Time]
	writeDeadline atomic.Pointer[time.Time]

	closeOnce sync.Once
	done      chan struct{}
}

func newStream(m *Mux, id uint32) *Stream {
	return &Stream{
		mux:        m,
		id:         id,
		notifyCh:   make(chan struct{}, 1),
		sendNotify: make(chan struct{}, 1),
		sendWindow: initialWindow,
		done:       make(chan struct{}),
	}
}

// ID returns the stream's unique identifier.
func (s *Stream) ID() uint32 {
	return s.id
}

// Done returns a channel that is closed when the stream is closed or reset.
func (s *Stream) Done() <-chan struct{} {
	return s.done
}

// Read reads received payload bytes for this stream.
func (s *Stream) Read(b []byte) (n int, err error) {
	for {
		s.mu.Lock()
		if len(s.buf) > 0 {
			chunk := s.buf[0]
			copied := copy(b, chunk)
			if copied == len(chunk) {
				s.buf = s.buf[1:]
			} else {
				s.buf[0] = chunk[copied:]
			}
			s.bufLen -= copied
			s.mux.buffered.Add(-int64(copied))
			var credit int
			if s.mux.peerFlowControl.Load() && !s.readClosed && !s.closed {
				s.recvUnacked += copied
				if s.recvUnacked >= initialWindow/2 {
					credit = s.recvUnacked
					s.recvUnacked = 0
				}
			}
			s.mu.Unlock()
			if credit > 0 {
				s.mux.sendWindowUpdate(s.id, uint32(credit))
			}
			return copied, nil
		}

		if s.closeErr != nil {
			err := s.closeErr
			s.mu.Unlock()
			return 0, err
		}

		if s.readClosed || s.closed {
			s.mu.Unlock()
			return 0, io.EOF
		}

		dl := s.readDeadline.Load()
		if dl != nil && !dl.IsZero() && time.Now().After(*dl) {
			s.mu.Unlock()
			return 0, ErrTimeout
		}

		s.mu.Unlock()

		var timer *time.Timer
		var timerCh <-chan time.Time
		if dl != nil && !dl.IsZero() {
			dur := time.Until(*dl)
			if dur <= 0 {
				return 0, ErrTimeout
			}
			timer = time.NewTimer(dur)
			timerCh = timer.C
		}

		select {
		case <-s.notifyCh:
			if timer != nil {
				timer.Stop()
			}
		case <-timerCh:
			return 0, ErrTimeout
		case <-s.done:
			if timer != nil {
				timer.Stop()
			}
		case <-s.mux.Done():
			if timer != nil {
				timer.Stop()
			}
			return 0, s.mux.getCloseErr()
		}
	}
}

// Write writes payload data to the peer across Mux.
func (s *Stream) Write(b []byte) (int, error) {
	s.mu.Lock()
	if s.closed || s.writeClosed {
		err := s.closeErr
		if err == nil {
			err = ErrStreamClosed
		}
		s.mu.Unlock()
		return 0, err
	}
	s.mu.Unlock()

	dl := s.writeDeadline.Load()
	if dl != nil && !dl.IsZero() && time.Now().After(*dl) {
		return 0, ErrTimeout
	}

	total := len(b)
	for len(b) > 0 {
		chunkSize := len(b)
		if chunkSize > defaultChunk {
			chunkSize = defaultChunk
		}

		chunkSize, err := s.reserveSend(chunkSize, dl)
		if err != nil {
			return total - len(b), err
		}

		if err := s.mux.writeFrame(FrameData, s.id, b[:chunkSize]); err != nil {
			return total - len(b), err
		}
		b = b[chunkSize:]
	}

	return total, nil
}

// reserveSend waits until the peer has window for at least one byte and
// reserves up to want bytes of it. Against a peer without flow control it
// reserves want immediately once negotiation has settled.
func (s *Stream) reserveSend(want int, dl *time.Time) (int, error) {
	for {
		if dl != nil && !dl.IsZero() && time.Now().After(*dl) {
			return 0, ErrTimeout
		}

		negotiated := s.mux.negotiatedChan()
		s.mu.Lock()
		if s.closed || s.writeClosed {
			err := s.closeErr
			if err == nil {
				err = ErrStreamClosed
			}
			s.mu.Unlock()
			return 0, err
		}
		if negotiated == nil && !s.mux.peerFlowControl.Load() {
			s.mu.Unlock()
			return want, nil
		}
		if s.sendWindow > 0 {
			n := int64(want)
			if n > s.sendWindow {
				n = s.sendWindow
			}
			s.sendWindow -= n
			left := s.sendWindow
			s.mu.Unlock()
			if left > 0 {
				// Pass the wakeup on to any other writer waiting on this stream.
				s.notifySend()
			}
			return int(n), nil
		}
		s.mu.Unlock()

		var timer *time.Timer
		var timerCh <-chan time.Time
		if dl != nil && !dl.IsZero() {
			timer = time.NewTimer(time.Until(*dl))
			timerCh = timer.C
		}
		select {
		case <-s.sendNotify:
		case <-negotiated:
		case <-timerCh:
		case <-s.done:
		case <-s.mux.Done():
			if timer != nil {
				timer.Stop()
			}
			return 0, s.mux.getCloseErr()
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// CloseWrite half-closes the stream from the local side, indicating no more data will be written.
func (s *Stream) CloseWrite() error {
	s.mu.Lock()
	if s.writeClosed || s.closed {
		s.mu.Unlock()
		return nil
	}
	s.writeClosed = true
	s.mu.Unlock()

	_ = s.mux.writeFrame(FrameCloseWrite, s.id, nil)
	return nil
}

// Close closes both reading and writing halves of the stream.
func (s *Stream) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		wasWriteClosed := s.writeClosed
		s.closed = true
		s.readClosed = true
		s.writeClosed = true
		s.closeErr = ErrStreamClosed
		s.dropBufLocked()
		s.mu.Unlock()

		if !wasWriteClosed {
			_ = s.mux.writeFrame(FrameCloseWrite, s.id, nil)
		}
		s.mux.removeStream(s.id)
		close(s.done)
		s.notify()
	})
	return nil
}

// Reset aborts the stream with FrameReset.
func (s *Stream) Reset() error {
	s.resetWithError(ErrStreamReset)
	return nil
}

func (s *Stream) resetWithError(err error) {
	s.abort(err, func() { _ = s.mux.writeFrame(FrameReset, s.id, nil) })
}

// abort closes the stream with err, calling sendReset to tell the peer.
func (s *Stream) abort(err error, sendReset func()) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.writeClosed = true
		s.readClosed = true
		s.closeErr = err
		s.dropBufLocked()
		s.mu.Unlock()

		sendReset()
		s.mux.removeStream(s.id)
		close(s.done)
		s.notify()
	})
}

// dropBufLocked discards the unread data. Callers hold s.mu.
func (s *Stream) dropBufLocked() {
	s.mux.buffered.Add(-int64(s.bufLen))
	s.buf = nil
	s.bufLen = 0
}

// receiveData queues payload for Read. It never blocks: a stream whose unread
// data would exceed the window, or take the session past its cap on unread
// data, is reset instead, so one stalled reader can neither stall the session
// nor grow without bound. The window applies whether or not the peer speaks
// flow control: one that does not cannot be throttled, so it gets no more.
func (s *Stream) receiveData(payload []byte) {
	s.mu.Lock()
	if s.readClosed || s.closed {
		s.mu.Unlock()
		return
	}
	n := len(payload)
	if s.bufLen+n > initialWindow || s.mux.buffered.Load()+int64(n) > s.mux.maxBuffered {
		s.mu.Unlock()
		// This runs on the read loop, which must not block writing to the
		// peer, so the reset goes through the control writer.
		s.abort(ErrBufferFull, func() { s.mux.queueReset(s.id) })
		return
	}
	if n > 0 {
		chunk := make([]byte, n)
		copy(chunk, payload)
		s.buf = append(s.buf, chunk)
		s.bufLen += n
		s.mux.buffered.Add(int64(n))
	}
	s.mu.Unlock()
	s.notify()
}

func (s *Stream) receiveWindowUpdate(increment uint32) {
	s.mu.Lock()
	s.sendWindow += int64(increment)
	s.mu.Unlock()
	s.notifySend()
}

func (s *Stream) receiveCloseWrite() {
	s.mu.Lock()
	s.readClosed = true
	s.mu.Unlock()
	s.notify()
}

func (s *Stream) receiveReset() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.writeClosed = true
		s.readClosed = true
		s.closeErr = ErrStreamReset
		s.dropBufLocked()
		s.mu.Unlock()

		s.mux.removeStream(s.id)
		close(s.done)
		s.notify()
	})
}

func (s *Stream) closeWithError(err error) {
	if err == nil {
		err = ErrStreamClosed
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.writeClosed = true
		s.readClosed = true
		s.closeErr = err
		s.dropBufLocked()
		s.mu.Unlock()

		close(s.done)
		s.notify()
	})
}

func (s *Stream) notify() {
	select {
	case s.notifyCh <- struct{}{}:
	default:
	}
}

func (s *Stream) notifySend() {
	select {
	case s.sendNotify <- struct{}{}:
	default:
	}
}

// LocalAddr returns a virtual network address for the local stream endpoint.
func (s *Stream) LocalAddr() net.Addr {
	return streamAddr{id: s.id, role: "local"}
}

// RemoteAddr returns a virtual network address for the remote stream endpoint.
func (s *Stream) RemoteAddr() net.Addr {
	return streamAddr{id: s.id, role: "remote"}
}

// SetDeadline sets both read and write deadlines.
func (s *Stream) SetDeadline(t time.Time) error {
	s.SetReadDeadline(t)
	s.SetWriteDeadline(t)
	return nil
}

// SetReadDeadline sets the deadline for future Read calls.
func (s *Stream) SetReadDeadline(t time.Time) error {
	s.readDeadline.Store(&t)
	s.notify()
	return nil
}

// SetWriteDeadline sets the deadline for future Write calls.
func (s *Stream) SetWriteDeadline(t time.Time) error {
	s.writeDeadline.Store(&t)
	return nil
}

type streamAddr struct {
	id   uint32
	role string
}

func (a streamAddr) Network() string { return "mux" }
func (a streamAddr) String() string  { return fmt.Sprintf("stream-%d-%s", a.id, a.role) }
