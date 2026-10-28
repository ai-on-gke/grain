package main

import (
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/workloadio"
)

// The reporter's two reports reach a host collector in order, even when
// the workload exits before the start report has gone out.
func TestExitReporterReachesTheHost(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "vsock.sock")
	c, err := workloadio.ListenExit(sock)
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	first := true
	r := newExitReporter(func() (io.ReadWriteCloser, error) {
		if first {
			// Hold the start report back until the exit report is
			// waiting behind it.
			first = false
			<-gate
		}
		return net.Dial("unix", workloadio.ExitSocket(sock))
	})
	r.start()
	done := make(chan struct{})
	go func() {
		r.exit(130)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("the exit report went ahead of the start report")
	case <-time.After(50 * time.Millisecond):
	}
	close(gate)
	<-done

	if got := c.Close(); got != (workloadio.Observed{Started: true, Exited: true, Code: 130}) {
		t.Fatalf("host observed %+v", got)
	}
}

// A host that is not listening costs the report and nothing else.
func TestExitReporterWithNoHost(t *testing.T) {
	r := newExitReporter(func() (io.ReadWriteCloser, error) {
		return nil, errors.New("connection refused")
	})
	r.start()
	start := time.Now()
	r.exit(0)
	if d := time.Since(start); d > time.Second {
		t.Errorf("exit waited %s with nothing listening", d)
	}
}
