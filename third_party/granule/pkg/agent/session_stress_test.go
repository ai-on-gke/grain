package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/execwire"
)

// TestStress_ConcurrentSessions runs dozens of concurrent agent sessions
// over simulated vsock channels, asserting that output multiplexing,
// stdin delivery, and exit reporting remain strictly isolated and race-free.
func TestStress_ConcurrentSessions(t *testing.T) {
	const numSessions = 40
	var wg sync.WaitGroup
	errCh := make(chan error, numSessions)

	account := currentAccount(t)

	for i := 0; i < numSessions; i++ {
		wg.Add(1)
		go func(sessID int) {
			defer wg.Done()
			client, server := net.Pipe()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			serverDone := make(chan error, 1)
			go func() {
				defer server.Close()
				serverDone <- Serve(ctx, server)
			}()
			defer client.Close()

			req := execwire.Request{
				Line: fmt.Sprintf("echo 'START_%d'; echo 'MID_%d' >&2; echo 'DONE_%d'", sessID, sessID, sessID),
				User: account,
			}
			if err := execwire.WriteRequest(client, req); err != nil {
				errCh <- fmt.Errorf("session %d: WriteRequest: %w", sessID, err)
				return
			}

			br := bufio.NewReader(client)
			resp, err := execwire.ReadResponse(br)
			if err != nil {
				errCh <- fmt.Errorf("session %d: ReadResponse: %w", sessID, err)
				return
			}
			if !resp.OK {
				errCh <- fmt.Errorf("session %d: agent returned error: %s", sessID, resp.Error)
				return
			}

			_ = execwire.WriteFrame(client, execwire.TypeStdinClose, nil)

			var stdoutBuf, stderrBuf bytes.Buffer
			var exitCode int
			gotExit := false

			for {
				typ, payload, err := execwire.ReadFrame(br)
				if err != nil {
					if err == io.EOF {
						break
					}
					errCh <- fmt.Errorf("session %d: ReadFrame: %w", sessID, err)
					return
				}
				switch typ {
				case execwire.TypeStdout:
					stdoutBuf.Write(payload)
				case execwire.TypeStderr:
					stderrBuf.Write(payload)
				case execwire.TypeExit:
					exitCode, err = execwire.DecodeExit(payload)
					if err != nil {
						errCh <- fmt.Errorf("session %d: DecodeExit: %w", sessID, err)
						return
					}
					gotExit = true
				}
			}

			if !gotExit {
				errCh <- fmt.Errorf("session %d: did not receive TypeExit frame", sessID)
				return
			}
			if exitCode != 0 {
				errCh <- fmt.Errorf("session %d: exit code = %d, want 0", sessID, exitCode)
				return
			}

			wantStdout := fmt.Sprintf("START_%d\nDONE_%d\n", sessID, sessID)
			if stdoutBuf.String() != wantStdout {
				errCh <- fmt.Errorf("session %d stdout mismatch: got %q, want %q", sessID, stdoutBuf.String(), wantStdout)
				return
			}

			wantStderr := fmt.Sprintf("MID_%d\n", sessID)
			if stderrBuf.String() != wantStderr {
				errCh <- fmt.Errorf("session %d stderr mismatch: got %q, want %q", sessID, stderrBuf.String(), wantStderr)
				return
			}

			if serr := <-serverDone; serr != nil {
				errCh <- fmt.Errorf("session %d: Serve returned: %w", sessID, serr)
				return
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent session stress error: %v", err)
	}
}

// TestStress_MassiveThroughputStreaming streams multi-megabyte payloads
// through concurrent agent sessions to stress frame encoding, buffers, and pipe pumps.
func TestStress_MassiveThroughputStreaming(t *testing.T) {
	const workers = 8
	const payloadBytes = 2 * 1024 * 1024 // 2MB per worker = 16MB total
	account := currentAccount(t)

	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			client, server := net.Pipe()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			go func() {
				defer server.Close()
				_ = Serve(ctx, server)
			}()
			defer client.Close()

			// Command generates deterministic data
			req := execwire.Request{
				Line: fmt.Sprintf("head -c %d /dev/zero | tr '\\0' 'A'", payloadBytes),
				User: account,
			}
			if err := execwire.WriteRequest(client, req); err != nil {
				errCh <- err
				return
			}

			br := bufio.NewReader(client)
			resp, err := execwire.ReadResponse(br)
			if err != nil || !resp.OK {
				errCh <- fmt.Errorf("worker %d: response error: %v / %s", workerID, err, resp.Error)
				return
			}

			_ = execwire.WriteFrame(client, execwire.TypeStdinClose, nil)

			hasher := sha256.New()
			var totalReceived int64
			for {
				typ, payload, err := execwire.ReadFrame(br)
				if err != nil {
					break
				}
				if typ == execwire.TypeStdout {
					hasher.Write(payload)
					totalReceived += int64(len(payload))
				}
			}

			if totalReceived != payloadBytes {
				errCh <- fmt.Errorf("worker %d: received %d bytes, want %d", workerID, totalReceived, payloadBytes)
				return
			}

			// Verify hash of payloadBytes of 'A'
			expectedData := bytes.Repeat([]byte("A"), payloadBytes)
			expectedHash := sha256.Sum256(expectedData)
			actualHash := hex.EncodeToString(hasher.Sum(nil))
			if actualHash != hex.EncodeToString(expectedHash[:]) {
				errCh <- fmt.Errorf("worker %d: checksum mismatch: got %s, want %s", workerID, actualHash, hex.EncodeToString(expectedHash[:]))
				return
			}
		}(w)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("throughput stress error: %v", err)
	}
}

// TestStress_RapidSignalsAndCancellation creates processes and cancels/signals them
// repeatedly under high concurrency to stress process group cleanup.
func TestStress_RapidSignalsAndCancellation(t *testing.T) {
	const iterations = 25
	account := currentAccount(t)

	var wg sync.WaitGroup
	errCh := make(chan error, iterations)

	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			client, server := net.Pipe()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			serverDone := make(chan error, 1)
			go func() {
				defer server.Close()
				serverDone <- Serve(ctx, server)
			}()
			defer client.Close()

			req := execwire.Request{
				Line: "sleep 30",
				User: account,
			}
			if err := execwire.WriteRequest(client, req); err != nil {
				errCh <- err
				return
			}

			br := bufio.NewReader(client)
			resp, err := execwire.ReadResponse(br)
			if err != nil || !resp.OK {
				errCh <- fmt.Errorf("resp: %v / %s", err, resp.Error)
				return
			}

			// Let command start, then cancel or signal
			time.Sleep(10 * time.Millisecond)

			if idx%2 == 0 {
				// Cancel context
				cancel()
			} else {
				// Send SIGINT
				_ = execwire.WriteFrame(client, execwire.TypeSignal, execwire.EncodeSignal(2))
			}

			// Drain connection
			for {
				_, _, err := execwire.ReadFrame(br)
				if err != nil {
					break
				}
			}

			select {
			case <-serverDone:
			case <-time.After(3 * time.Second):
				errCh <- fmt.Errorf("iteration %d: Serve did not terminate promptly", idx)
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("signals stress error: %v", err)
	}
}
