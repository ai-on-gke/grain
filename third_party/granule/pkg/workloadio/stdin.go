package workloadio

import (
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/bwsalmon/granule/pkg/execwire"
)

// # Stdin
//
// A container started with its stdin open (containerspec.Spec.OpenStdin,
// `docker run -i`) reads what the host writes to it, on the same
// connection its output leaves by: the host sends execwire's TypeStdin
// frames, and a TypeStdinClose once the host's own end has closed, which
// the workload reads as end-of-file. One started on a terminal
// (containerspec.Spec.Tty) also takes TypeWinsize frames, and its
// output is the terminal's, so it all arrives as TypeStdout.
//
// The guest says which of these it accepts in its opening line --
// FeatureStdin for the first, FeatureWinsize for the second -- and only
// for a container that has them, so a host attached to one that does not
// (an older image, or a container whose spec never opened its stdin) can
// say so rather than write into nothing.
//
// Input is not kept anywhere while nothing is attached: the host's own
// end stops being read until a connection is up, which is the
// backpressure a pipe gives. And end-of-file is for good: a host whose
// stdin has closed sends TypeStdinClose to every later connection too,
// so a container restarted in the same VM reads end-of-file at once
// rather than waiting on an input that is never coming.

// FeatureStdin is the feature a guest names when the workload's stdin is
// open to the host: TypeStdin frames reach it, and TypeStdinClose ends
// it.
const FeatureStdin = "stdin"

// FeatureWinsize is the feature a guest names when the workload runs on
// a terminal whose size TypeWinsize frames set.
const FeatureWinsize = "winsize"

// Input is the workload's end of what the host sends it.
type Input interface {
	// Write is the host's stdin, in order. It may block for as long as
	// the workload does not read, which holds the host up in turn.
	Write(p []byte) (int, error)

	// CloseWrite is the host's stdin ending: the workload reads
	// end-of-file.
	CloseWrite() error

	// Resize sets the terminal's size. Nothing happens without a
	// terminal.
	Resize(cols, rows uint16)
}

// OfferInput announces, in the opening line of every connection from
// here on, that the workload takes stdin (stdin) and terminal sizes
// (winsize) from the host. It has to be called before the first Serve,
// since a host decides what to send from the opening line; the Input
// itself can follow (SetInput), and frames that arrive before it are
// held until it does.
func (b *Broker) OfferInput(stdin, winsize bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	features := append([]string(nil), Features...)
	if stdin {
		features = append(features, FeatureStdin)
	}
	if winsize {
		features = append(features, FeatureWinsize)
	}
	b.features = features
	b.offerStdin, b.offerWinsize = stdin, winsize
}

// SetInput is where the host's input goes from now on: the workload's
// stdin, once the workload has one.
func (b *Broker) SetInput(in Input) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.input = in
	b.changed.Broadcast()
}

// waitInput is the Input to deliver to, waiting for SetInput if one was
// offered and is not there yet. It is nil once there is nothing to wait
// for: none was offered, or the broker has closed.
func (b *Broker) waitInput() Input {
	b.mu.Lock()
	defer b.mu.Unlock()
	for (b.offerStdin || b.offerWinsize) && b.input == nil && !b.closed {
		b.changed.Wait()
	}
	return b.input
}

// deliver hands one frame from the host to the workload's Input. Frames
// of other types, and frames for an Input that was never offered, are
// stepped over.
func (b *Broker) deliver(typ byte, payload []byte) {
	b.mu.Lock()
	offered := false
	switch typ {
	case execwire.TypeStdin, execwire.TypeStdinClose:
		offered = b.offerStdin
	case execwire.TypeWinsize:
		offered = b.offerWinsize
	}
	b.mu.Unlock()
	if !offered {
		return
	}
	in := b.waitInput()
	if in == nil {
		return
	}
	b.inputMu.Lock()
	defer b.inputMu.Unlock()
	switch typ {
	case execwire.TypeStdin:
		if b.stdinDone {
			return
		}
		if _, err := in.Write(payload); err != nil {
			// The workload has stopped reading for good -- it exited, or
			// closed its stdin. What the host sends after that has
			// nowhere to go.
			b.stdinDone = true
		}
	case execwire.TypeStdinClose:
		if !b.stdinDone {
			b.stdinDone = true
			_ = in.CloseWrite()
		}
	case execwire.TypeWinsize:
		if cols, rows, err := execwire.DecodeWinsize(payload); err == nil {
			in.Resize(cols, rows)
		}
	}
}

// HostInput is the host's end of the workload's input: what it reads
// from the caller's stdin (CopyStdin) and the terminal sizes it is given
// (Resize), sent to whichever connection Forward has open.
type HostInput struct {
	data    chan []byte   // the stdin, a chunk at a time; closed at its end
	resized chan struct{} // a Resize since the last one was sent

	mu   sync.Mutex
	size []byte // the latest Resize, as a TypeWinsize payload
}

// NewHostInput returns a HostInput with nothing in it yet.
func NewHostInput() *HostInput {
	return &HostInput{data: make(chan []byte), resized: make(chan struct{}, 1)}
}

// CopyStdin reads r, the caller's stdin, to its end and sends it to the
// guest, then ends the workload's stdin. Each read waits for a
// connection to take the last, so r is read no faster than the workload
// reads. A read error ends the workload's stdin as end-of-file does, and
// is returned. Closing done gives up instead, without ending it. Call it
// once.
func (h *HostInput) CopyStdin(done <-chan struct{}, r io.Reader) error {
	defer close(h.data)
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			select {
			case h.data <- append([]byte(nil), buf[:n]...):
			case <-done:
				return nil
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Resize sets the workload's terminal to cols by rows: at once if a
// connection is up, and on each later connection too, so a container
// restarted on a new terminal gets the size its host last asked for.
func (h *HostInput) Resize(cols, rows uint16) {
	h.mu.Lock()
	h.size = execwire.EncodeWinsize(cols, rows)
	h.mu.Unlock()
	select {
	case h.resized <- struct{}{}:
	default:
	}
}

func (h *HostInput) currentSize() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.size
}

// feed sends h to w, one connection whose guest opened with resp, until
// done is closed or a write fails. It is the only writer on w.
func (h *HostInput) feed(w io.Writer, resp execwire.Response, done <-chan struct{}) {
	stdin, winsize := resp.Supports(FeatureStdin), resp.Supports(FeatureWinsize)
	if winsize {
		if size := h.currentSize(); size != nil {
			if execwire.WriteFrame(w, execwire.TypeWinsize, size) != nil {
				return
			}
		}
	}
	data := h.data
	warned := false
	for {
		select {
		case <-done:
			return
		case chunk, ok := <-data:
			switch {
			case !stdin:
				// Read and dropped rather than left unread, so that a
				// caller writing to a container that cannot take it is
				// not blocked forever.
				if ok && !warned {
					log.Printf("the wrapped container's stdin is not open (its spec does not ask for it, or its image predates it); discarding what is written to it")
					warned = true
				}
				if !ok {
					data = nil
				}
			case !ok:
				data = nil
				if execwire.WriteFrame(w, execwire.TypeStdinClose, nil) != nil {
					return
				}
			default:
				if execwire.WriteFrame(w, execwire.TypeStdin, chunk) != nil {
					// The connection is gone and the chunk with it: a
					// guest that loses its host mid-write is usually a
					// workload restarting, which has a new stdin.
					return
				}
			}
		case <-h.resized:
			if !winsize {
				continue
			}
			if size := h.currentSize(); size != nil {
				if execwire.WriteFrame(w, execwire.TypeWinsize, size) != nil {
					return
				}
			}
		}
	}
}

// ParseWinsize reads a terminal size as a caller writes it to the
// resize FIFO: columns then rows, "80 24" or "80x24".
func ParseWinsize(line string) (cols, rows uint16, err error) {
	line = strings.TrimSpace(line)
	fields := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' || r == 'x' })
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("terminal size %q is not \"COLS ROWS\"", line)
	}
	c, err := strconv.ParseUint(fields[0], 10, 16)
	if err != nil {
		return 0, 0, fmt.Errorf("terminal size %q: columns: %w", line, err)
	}
	r, err := strconv.ParseUint(fields[1], 10, 16)
	if err != nil {
		return 0, 0, fmt.Errorf("terminal size %q: rows: %w", line, err)
	}
	return uint16(c), uint16(r), nil
}
