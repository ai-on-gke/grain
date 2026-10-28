package workloadio

// # Exit status
//
// A wrapped container's exit status reaches the host on a vsock port of
// its own (ExitPort), and the other way round from the streams: the
// guest connects to the host rather than waiting for the host to connect
// to it. That is what lets it reach a host which asked for nothing --
// a plain `docker run` of a wrapped image, which never set
// GRANULE_WORKLOAD_STDOUT -- without making every such VM wait at
// power-off for a host that is not coming: a guest-initiated connection
// that nothing is listening for is refused at once, by cloud-hypervisor,
// which is how an older shim looks from inside the guest.
//
// granule-container sends two reports, each on a connection of its own
// and each acknowledged before it counts as delivered: EventStarted when
// it starts, and EventExited, with the status a container runtime would
// give (the process's exit code, or 128+N for signal N), once the
// workload has exited and before the machine powers off. The host keeps
// the last one it was sent (ExitCollector). A start after an exit -- a
// restart policy bringing the workload back on a VM that is not powering
// off -- clears the status the earlier run left, so that a VM which then
// dies is not reported as having exited the way a previous run did.
//
// When the VM stops, granule-shim exits with what it was told (see
// Observed.Status):
//
//   - the reported status, if the workload's exit was the last thing
//     reported;
//   - ExitUnreported, if granule-container started and no exit followed
//     -- the guest panicked, was killed, or the host stopped it before
//     the workload finished -- or if the caller declared the VM a wrapped
//     one (GRANULE_WORKLOAD_EXIT_FILE) and nothing was reported at all;
//   - otherwise cloud-hypervisor's own exit code, as before this
//     existed: a VM that runs no wrapped container has no workload
//     status to give.
//
// GRANULE_WORKLOAD_EXIT_FILE also has the status written to a file
// (WriteExitFile), so that a caller that is not the shim's parent can
// read it, and tell ExitUnreported from a workload that exited 125 on
// its own.
//
// # Trust
//
// Anything running as root in the guest can connect to ExitPort and
// report a status; the workload itself can, in other words, lie about
// how it exited. That costs nothing: the status is the workload's to
// report in the first place, and the guest as a whole is the workload's.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/bwsalmon/granule/pkg/execwire"
)

// ExitPort is the vsock port on the host that granule-container reports
// the workload's start and exit to. The guest connects to it; on the
// host, cloud-hypervisor hands such a connection to the unix socket
// ExitSocket names.
const ExitPort = execwire.Port + 2

// ExitUnreported is the exit code granule-shim gives a wrapped VM that
// stopped without its workload's exit having been reported. It is
// docker run's code for a failure of the runtime rather than of the
// container, which is what this is: the machine around the workload went
// away, or was taken away, before the workload finished. A workload can
// exit 125 too; the exit file's Reported field is what tells them apart.
const ExitUnreported = 125

// ReportTimeout bounds each report, connection and acknowledgement both,
// so that a host that accepted and then hung cannot hold a VM's power-off
// for longer than this.
const ReportTimeout = 2 * time.Second

// The events a Report carries.
const (
	EventStarted = "started"
	EventExited  = "exited"
)

// Report is one line from granule-container to the host.
type Report struct {
	Event string `json:"event"`
	// ExitCode is the workload's status, for EventExited.
	ExitCode int `json:"exitCode"`
}

// reportAck is the host's answer: the report has been recorded.
type reportAck struct {
	OK bool `json:"ok"`
}

// maxReport bounds a report line; a real one is a few dozen bytes.
const maxReport = 4096

// ExitSocket is the unix socket a host listens on for connections the
// guest makes to ExitPort: cloud-hypervisor's hybrid vsock forwards a
// guest's connection to host port P to "<vsock socket>_P".
func ExitSocket(vsockSocket string) string {
	return vsockSocket + "_" + strconv.Itoa(ExitPort)
}

// SendReport sends r on conn and waits for the host's acknowledgement.
// The caller sets any deadline, and closes conn.
func SendReport(conn io.ReadWriter, r Report) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("sending the workload's %s report: %w", r.Event, err)
	}
	line, err := bufio.NewReaderSize(io.LimitReader(conn, maxReport), 256).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("waiting for the host to acknowledge the workload's %s report: %w", r.Event, err)
	}
	var ack reportAck
	if err := json.Unmarshal(line, &ack); err != nil || !ack.OK {
		return fmt.Errorf("the host did not acknowledge the workload's %s report: %q", r.Event, line)
	}
	return nil
}

// Observed is what a host has been told about the workload.
type Observed struct {
	// Started is set once granule-container has reported starting:
	// this VM runs a wrapped container.
	Started bool
	// Exited is set when the last report was the workload's exit, with
	// Code its status.
	Exited bool
	Code   int
}

// ExitStatus is what the exit file holds, as JSON.
type ExitStatus struct {
	// ExitCode is what granule-shim exits with.
	ExitCode int `json:"exitCode"`
	// Reported is whether ExitCode is the workload's own status, as
	// granule-container reported it, rather than ExitUnreported.
	Reported bool `json:"reported"`
	// Reason says why there was no report, when there was not.
	Reason string `json:"reason,omitempty"`
}

// Status is the wrapped workload's status once its VM has stopped. wrapped
// is the caller's own declaration that the VM runs a wrapped container
// (GRANULE_WORKLOAD_EXIT_FILE). ok is false when there is no workload
// status to give -- nothing was reported and nothing was declared, which
// is a VM running no wrapped container -- and the VM's own exit code is
// the one that counts.
func (o Observed) Status(wrapped bool) (status ExitStatus, ok bool) {
	switch {
	case o.Exited:
		return ExitStatus{ExitCode: o.Code, Reported: true}, true
	case o.Started:
		return ExitStatus{ExitCode: ExitUnreported, Reason: "the VM stopped while the workload was running"}, true
	case wrapped:
		return ExitStatus{ExitCode: ExitUnreported, Reason: "the VM stopped before the workload was started"}, true
	}
	return ExitStatus{}, false
}

// ExitCollector is the host's end: it listens for granule-container's
// reports and keeps what they said.
type ExitCollector struct {
	ln   net.Listener
	path string
	wg   sync.WaitGroup

	mu       sync.Mutex
	observed Observed
}

// ListenExit starts listening for the guest's reports on
// ExitSocket(vsockSocket), replacing anything already there -- a socket
// a previous boot of this container left behind. The caller has already
// established that no VM is using the vsock socket itself.
func ListenExit(vsockSocket string) (*ExitCollector, error) {
	path := ExitSocket(vsockSocket)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("removing a stale socket for the workload's exit status: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listening for the workload's exit status: %w", err)
	}
	c := &ExitCollector{ln: ln, path: path}
	c.wg.Add(1)
	go c.accept()
	return c, nil
}

func (c *ExitCollector) accept() {
	defer c.wg.Done()
	for {
		conn, err := c.ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				log.Printf("accepting the workload's exit status: %v", err)
			}
			return
		}
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			defer conn.Close()
			if err := c.handle(conn); err != nil {
				log.Printf("the workload's exit status: %v", err)
			}
		}()
	}
}

// handle reads one report and acknowledges it once it is recorded.
func (c *ExitCollector) handle(conn net.Conn) error {
	if err := conn.SetDeadline(time.Now().Add(ReportTimeout)); err != nil {
		return err
	}
	line, err := bufio.NewReader(io.LimitReader(conn, maxReport)).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("reading a report from the guest: %w", err)
	}
	var r Report
	if err := json.Unmarshal(line, &r); err != nil {
		return fmt.Errorf("a report from the guest that is not one: %q", line)
	}
	c.mu.Lock()
	switch r.Event {
	case EventStarted:
		c.observed = Observed{Started: true}
	case EventExited:
		c.observed = Observed{Started: true, Exited: true, Code: r.ExitCode}
	default:
		// From a newer guest: acknowledged, since it was delivered, and
		// otherwise stepped over.
	}
	c.mu.Unlock()
	switch r.Event {
	case EventStarted:
		log.Printf("the wrapped container has started")
	case EventExited:
		log.Printf("the wrapped container exited with status %d", r.ExitCode)
	}
	data, _ := json.Marshal(reportAck{OK: true})
	_, err = conn.Write(append(data, '\n'))
	return err
}

// Observed returns what has been reported so far.
func (c *ExitCollector) Observed() Observed {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.observed
}

// Close stops listening, waits for any report in flight, removes the
// socket, and returns what was reported.
func (c *ExitCollector) Close() Observed {
	c.ln.Close()
	c.wg.Wait()
	os.Remove(c.path)
	return c.Observed()
}

// WriteExitFile writes status to path as JSON, atomically: a reader
// that finds the file finds all of it.
func WriteExitFile(path string, status ExitStatus) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("writing the workload's exit status: %w", err)
	}
	_, err = tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("writing the workload's exit status to %s: %w", path, err)
	}
	return nil
}
