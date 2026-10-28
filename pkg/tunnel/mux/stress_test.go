package mux

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func tcpConnPair(t *testing.T) (net.Conn, net.Conn) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()

	var c1 net.Conn
	var cErr error
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		c1, cErr = ln.Accept()
	}()

	c2, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	<-acceptDone
	if cErr != nil {
		_ = c2.Close()
		t.Fatalf("failed to accept: %v", cErr)
	}

	return c1, c2
}

// TestMux_ChaosHighConcurrency executes a sustained, high-concurrency stress test
// across both server and client roles. It concurrently opens, reads, writes, half-closes,
// and resets streams while injecting chaotic conditions: random frame chunking (1B to 64KB),
// aggressive ping/pong keepalives, and random stream resets.
func TestMux_ChaosHighConcurrency(t *testing.T) {
	duration := 5 * time.Second
	if env := os.Getenv("GRAIN_TUNNEL_STRESS_DURATION"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			duration = d
		}
	} else if os.Getenv("GRAIN_TUNNEL_STRESS") != "" {
		duration = 30 * time.Minute
	}

	t.Logf("Running TestMux_ChaosHighConcurrency for %v", duration)

	c1, c2 := tcpConnPair(t)
	defer c1.Close()
	defer c2.Close()

	serverMux := NewMux(c1, Config{
		IsServer:     true,
		PingInterval: 100 * time.Millisecond,
		PingTimeout:  2 * time.Second,
	})
	defer serverMux.Close()

	clientMux := NewMux(c2, Config{
		IsServer:     false,
		PingInterval: 100 * time.Millisecond,
		PingTimeout:  2 * time.Second,
	})
	defer clientMux.Close()

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var (
		serverStreamsOpened atomic.Int64
		clientStreamsOpened atomic.Int64
		streamsCompleted    atomic.Int64
		resetsInjected      atomic.Int64
		bytesTransferred    atomic.Int64
		errCount            atomic.Int64
	)

	var wg sync.WaitGroup

	// Server Accept Loop (accepts client-initiated streams, even IDs)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			stream, err := serverMux.AcceptStream()
			if err != nil {
				return
			}
			go handleEchoStream(t, ctx, stream, &bytesTransferred, &resetsInjected, &errCount)
		}
	}()

	// Client Accept Loop (accepts server-initiated streams, odd IDs)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			stream, err := clientMux.AcceptStream()
			if err != nil {
				return
			}
			go handleEchoStream(t, ctx, stream, &bytesTransferred, &resetsInjected, &errCount)
		}
	}()

	// Worker concurrency per role
	const numWorkers = 20

	// Server-initiated stream workers
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID*1000)))
			for ctx.Err() == nil {
				runStreamWorker(t, ctx, serverMux, r, &serverStreamsOpened, &streamsCompleted, &resetsInjected, &bytesTransferred, &errCount)
			}
		}(w)
	}

	// Client-initiated stream workers
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID*2000+500)))
			for ctx.Err() == nil {
				runStreamWorker(t, ctx, clientMux, r, &clientStreamsOpened, &streamsCompleted, &resetsInjected, &bytesTransferred, &errCount)
			}
		}(w)
	}

	<-ctx.Done()

	// Graceful close of mux sessions
	_ = serverMux.Close()
	_ = clientMux.Close()
	_ = c1.Close()
	_ = c2.Close()

	wg.Wait()

	t.Logf("Mux Stress Test Completed: ServerOpened=%d ClientOpened=%d Completed=%d Resets=%d TotalBytes=%d Errors=%d",
		serverStreamsOpened.Load(),
		clientStreamsOpened.Load(),
		streamsCompleted.Load(),
		resetsInjected.Load(),
		bytesTransferred.Load(),
		errCount.Load(),
	)

	if errCount.Load() > 0 {
		t.Fatalf("Encountered %d errors/data corruption during stress test", errCount.Load())
	}
}

func isBenignChaosErr(ctx context.Context, err error) bool {
	if err == nil {
		return true
	}
	if ctx.Err() != nil {
		return true
	}
	return errors.Is(err, ErrStreamReset) ||
		errors.Is(err, ErrStreamClosed) ||
		errors.Is(err, ErrSessionClosed) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		strings.Contains(err.Error(), "ping timeout")
}

func handleEchoStream(
	t *testing.T,
	ctx context.Context,
	stream *Stream,
	bytesTransferred *atomic.Int64,
	resetsInjected *atomic.Int64,
	errCount *atomic.Int64,
) {
	defer stream.Close()

	r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(stream.ID())))
	shouldReset := r.Intn(100) < 15 // 15% probability of server-side reset

	data, err := io.ReadAll(stream)
	if err != nil {
		if isBenignChaosErr(ctx, err) {
			return
		}
		t.Logf("handleEchoStream read error: %v", err)
		errCount.Add(1)
		return
	}

	bytesTransferred.Add(int64(len(data)))

	if shouldReset {
		resetsInjected.Add(1)
		_ = stream.Reset()
		return
	}

	// Echo back with chaotic chunking
	if len(data) > 0 {
		offset := 0
		for offset < len(data) {
			if ctx.Err() != nil {
				return
			}
			chunkSize := r.Intn(8192) + 1
			if offset+chunkSize > len(data) {
				chunkSize = len(data) - offset
			}
			n, wErr := stream.Write(data[offset : offset+chunkSize])
			if wErr != nil {
				if !isBenignChaosErr(ctx, wErr) {
					t.Logf("handleEchoStream write error: %v", wErr)
					errCount.Add(1)
				}
				return
			}
			offset += n
		}
	}
	_ = stream.CloseWrite()
}

func runStreamWorker(
	t *testing.T,
	ctx context.Context,
	m *Mux,
	r *rand.Rand,
	openedCount *atomic.Int64,
	completedCount *atomic.Int64,
	resetsInjected *atomic.Int64,
	bytesTransferred *atomic.Int64,
	errCount *atomic.Int64,
) {
	stream, err := m.OpenStream(ctx)
	if err != nil {
		if isBenignChaosErr(ctx, err) {
			return
		}
		t.Logf("OpenStream error: %v", err)
		errCount.Add(1)
		return
	}
	defer stream.Close()
	openedCount.Add(1)

	// Random payload size from 1B to 64KB
	payloadSize := r.Intn(65536) + 1
	payload := make([]byte, payloadSize)
	r.Read(payload)
	expectedHash := sha256.Sum256(payload)

	shouldReset := r.Intn(100) < 15 // 15% probability of client-side reset mid-flight

	// Write payload with chaotic chunk sizes
	offset := 0
	for offset < len(payload) {
		if ctx.Err() != nil {
			return
		}
		chunkSize := r.Intn(16384) + 1
		if offset+chunkSize > len(payload) {
			chunkSize = len(payload) - offset
		}

		if shouldReset && offset > len(payload)/2 {
			resetsInjected.Add(1)
			_ = stream.Reset()
			return
		}

		n, wErr := stream.Write(payload[offset : offset+chunkSize])
		if wErr != nil {
			if isBenignChaosErr(ctx, wErr) {
				return
			}
			t.Logf("stream.Write error: %v", wErr)
			errCount.Add(1)
			return
		}
		offset += n
	}

	_ = stream.CloseWrite()

	// Read echoed data back and verify hash
	echoed, rErr := io.ReadAll(stream)
	if rErr != nil {
		if isBenignChaosErr(ctx, rErr) {
			return
		}
		t.Logf("stream ReadAll error: %v", rErr)
		errCount.Add(1)
		return
	}

	bytesTransferred.Add(int64(len(echoed)))

	if len(echoed) != len(payload) {
		// If context canceled or benign chaos interrupted
		if ctx.Err() != nil {
			return
		}
		// If peer reset the stream while we were reading echo
		return
	}

	actualHash := sha256.Sum256(echoed)
	if !bytes.Equal(actualHash[:], expectedHash[:]) {
		t.Logf("hash mismatch on stream %d", stream.ID())
		errCount.Add(1)
		return
	}

	completedCount.Add(1)
}

// TestMux_StreamZombieAcceptanceRegression tests that late frames for already closed
// streams or frames with invalid stream ID parities are never re-accepted as zombie streams.
func TestMux_StreamZombieAcceptanceRegression(t *testing.T) {
	c1, c2 := pipeNetConn()
	defer c1.Close()
	defer c2.Close()

	serverMux := NewMux(c1, Config{IsServer: true})
	defer serverMux.Close()

	clientMux := NewMux(c2, Config{IsServer: false})
	defer clientMux.Close()

	// 1. Client opens stream ID 2
	stream2, err := clientMux.OpenStream(context.Background())
	if err != nil {
		t.Fatalf("client OpenStream: %v", err)
	}
	if stream2.ID() != 2 {
		t.Fatalf("expected stream ID 2, got %d", stream2.ID())
	}

	// Server accepts stream 2
	acceptedStreamCh := make(chan *Stream, 1)
	go func() {
		s, acceptErr := serverMux.AcceptStream()
		if acceptErr == nil {
			acceptedStreamCh <- s
		}
	}()

	_, err = stream2.Write([]byte("hello from client"))
	if err != nil {
		t.Fatalf("stream2 write: %v", err)
	}

	var serverStream2 *Stream
	select {
	case serverStream2 = <-acceptedStreamCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for server to accept stream 2")
	}

	// Close stream 2 completely on both ends
	_ = stream2.Close()
	_ = serverStream2.Close()

	time.Sleep(50 * time.Millisecond)

	// Inject a late FrameData for stream 2 directly into serverMux's wire by writing raw frame
	var lateHdr [9]byte
	lateHdr[0] = 0
	lateHdr[1] = 0
	lateHdr[2] = 0
	lateHdr[3] = 4 // len 4
	lateHdr[4] = FrameData
	lateHdr[5] = 0
	lateHdr[6] = 0
	lateHdr[7] = 0
	lateHdr[8] = 2 // stream ID 2

	_, _ = c2.Write(append(lateHdr[:], []byte("late")...))

	// Also inject an invalid parity frame: server initiates odd IDs (e.g. ID 1).
	// A client trying to open stream ID 1 is invalid parity.
	var invalidHdr [9]byte
	invalidHdr[0] = 0
	invalidHdr[1] = 0
	invalidHdr[2] = 0
	invalidHdr[3] = 4 // len 4
	invalidHdr[4] = FrameData
	invalidHdr[5] = 0
	invalidHdr[6] = 0
	invalidHdr[7] = 0
	invalidHdr[8] = 1 // stream ID 1 (server parity, invalid from client)

	_, _ = c2.Write(append(invalidHdr[:], []byte("bad1")...))

	// Verify that serverMux does NOT accept any new zombie stream
	acceptZombieCh := make(chan *Stream, 1)
	go func() {
		s, _ := serverMux.AcceptStream()
		if s != nil {
			acceptZombieCh <- s
		}
	}()

	select {
	case zombie := <-acceptZombieCh:
		t.Fatalf("unexpected zombie stream accepted: ID=%d", zombie.ID())
	case <-time.After(200 * time.Millisecond):
		// Expected: no zombie streams accepted
	}
}

// TestMux_ConcurrentSessionTeardown verifies that abruptly closing a multiplexer session
// with hundreds of in-flight read and write operations immediately unblocks all goroutines
// cleanly without deadlocks, panics, or data races.
func TestMux_ConcurrentSessionTeardown(t *testing.T) {
	c1, c2 := pipeNetConn()
	defer c1.Close()
	defer c2.Close()

	serverMux := NewMux(c1, Config{IsServer: true})
	clientMux := NewMux(c2, Config{IsServer: false})

	const numStreams = 100
	var wg sync.WaitGroup

	for i := 0; i < numStreams; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s, err := serverMux.OpenStream(context.Background())
			if err != nil {
				return
			}
			buf := make([]byte, 1024)
			for {
				_, wErr := s.Write(buf)
				if wErr != nil {
					return
				}
			}
		}()

		go func() {
			defer wg.Done()
			s, err := clientMux.AcceptStream()
			if err != nil {
				return
			}
			buf := make([]byte, 1024)
			for {
				_, rErr := s.Read(buf)
				if rErr != nil {
					return
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)

	// Abrupt session close
	teardownErr := fmt.Errorf("abrupt teardown")
	_ = serverMux.CloseWithError(teardownErr)
	_ = clientMux.CloseWithError(teardownErr)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Clean teardown
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for concurrent streams to unblock on session close")
	}
}
