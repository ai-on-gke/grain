// Package memagent implements the host-side half of automatic memory
// hotplug and unplug: a small listener that lets a guest-side agent (see
// deploy/guest-image's granule-mem-agent, installed in both guest image
// variants) ask this VM's own already-running "granule run" process to
// grow its memory when the guest sees pressure, or reclaim memory when
// the guest is idle, via the same cloud-hypervisor API
// hypervisor.APIClient.Resize (and so "granule resize") already uses --
// just triggered automatically, from inside the guest, rather than by an
// operator's kubectl exec.
//
// The protocol is deliberately minimal: the guest opens a TCP
// connection to Server's listen address (reachable directly, since the
// guest and this container are joined by netshim's control link -- see
// the top-level README's "Container networking") and writes one line:
//   - "PRESSURE <token> <value>\n" to request growth under memory strain,
//     where <token> is Config.Token and <value> is whatever pressure metric
//     the guest used to decide to signal (e.g. /proc/pressure/memory's
//     "some avg10", which Server parses to adaptively scale the step size).
//   - "PRESSURE_LOW <token> <available_mb>\n" to request scale-down /
//     reclamation when the guest is idle (e.g. PSI "some avg300 == 0.0"
//     and /proc/meminfo MemAvailable > 60% over a stabilization window).
//
// A line longer than MaxLineBytes is refused unread past that point.
//
// # The token
//
// The token is mandatory: Serve refuses to listen without one. The
// listener's address is not reachable only by the guest -- every
// container in this pod shares the network namespace it is bound in,
// and Linux accepts a packet for any local address on any interface, so
// whatever can put one on the pod's own segment can reach it too -- and
// inside the guest any process at all can open a TCP connection. The
// token is what limits resizing to the guest-side agent.
//
// That only holds if the token reaches the guest somewhere only that
// agent can read it. It used to travel on the guest kernel's command
// line, which every unprivileged guest process can read from
// /proc/cmdline (and which sits on cloud-hypervisor's argv in this
// container besides). It now goes over the exec agent's vsock channel
// instead, into TokenFile, a root-only file the agent re-reads per
// signal: see InstallTokenCommand, and cmd/granule-shim, which runs it
// on every boot and every resume.
//
// Any successfully authenticated growth signal grows the guest by an adaptive step
// (1x, 2x, or 4x Config.StepMB depending on pressure severity, capped at
// Config.MaxMB), and any low-pressure signal shrinks the guest by
// Config.StepDownMB (or Config.StepMB, down to Config.MinMB or the VM's
// boot size), rate-limited by Config.Cooldown and Config.ScaleDownCooldown.
//
// The step is measured from the size cloud-hypervisor currently holds
// for this VM, read back per signal (hypervisor.APIClient.Info), rather
// than from a size Server counts up itself: a "granule resize" run from
// outside moves the guest without Server hearing about it, and a Server
// stepping from its own tally would then either ask for a size the
// guest already has -- which the virtio-mem device rejects outright --
// or believe it had reached Config.MaxMB while the guest sat well below
// it.
package memagent

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/granule/pkg/hypervisor"
	"github.com/bwsalmon/granule/pkg/telemetry"
)

// Resizer is the subset of hypervisor.APIClient's methods Server depends
// on -- just enough to keep this package's tests (see memagent_test.go)
// from needing a real cloud-hypervisor API socket.
//
// Info is what Server measures its step from, and ResizeIfNeeded rather
// than plain Resize is what sends it: between reading the size and
// asking for the next one an operator's own "granule resize" may have
// landed on exactly the size Server was about to ask for, and that
// request is a 500 from the virtio-mem device rather than a no-op.
type Resizer interface {
	Info(ctx context.Context) (hypervisor.VMInfo, error)
	ResizeIfNeeded(ctx context.Context, desiredRAMBytes uint64) (resized bool, err error)
}

// Config holds Server's tunables. See pkg/config's CHV_MEM_AGENT*
// variables, which populate this in production.
type Config struct {
	// Addr is the address Server listens on, e.g. "169.254.100.1:30090"
	// -- must be reachable from the guest, and must match whatever
	// address/port the guest-side agent baked into the disk image was
	// told (or defaults) to signal.
	Addr string

	// Token is the secret every pressure signal must carry, as
	// "PRESSURE <token> <value>" or "PRESSURE_LOW <token> <available_mb>".
	// Required: Serve refuses to start without one (see the package doc
	// comment's "The token").
	Token string

	// MinMB is the floor below which no shrink Server performs may drop
	// (config.Config's MemoryMB / the starting boot size). If unset or
	// zero, it defaults to the VM's boot size (VMInfo.Config.Memory.Size).
	MinMB int

	// MaxMB is the ceiling no resize Server performs may exceed
	// (config.Config's MemoryMaxMB, which is also the ceiling the
	// virtio-mem device was given at boot -- so this only ever agrees
	// with what cloud-hypervisor would enforce itself).
	MaxMB int

	// StepMB is how much a single pressure signal grows the guest by,
	// before clamping to MaxMB.
	StepMB int

	// StepDownMB is how much a single low pressure signal shrinks the
	// guest by, before clamping to MinMB. If unset or zero, it defaults
	// to StepMB.
	StepDownMB int

	// Cooldown is the minimum time between two resizes Server performs.
	// A resize is asynchronous from the guest's point of view (see
	// hypervisor.APIClient.Resize's own doc comment), so without this a
	// guest still under pressure right after one grow -- because the
	// grow hasn't actually landed yet, not because it wasn't enough --
	// would just trigger another one immediately.
	Cooldown time.Duration

	// ScaleDownCooldown is the minimum time between a resize and a subsequent
	// scale-down resize Server performs, to prevent thrashing. If unset or
	// zero, it defaults to Cooldown.
	ScaleDownCooldown time.Duration

	// Metrics, if non-nil, receives structured memory elasticity telemetry
	// and Prometheus metrics. If nil, New initializes a new collector.
	Metrics *telemetry.MemoryHotplugMetrics
}

// MaxLineBytes is the longest signal line Server reads, newline
// included. A real one is a keyword, a token and a number -- well under
// a hundred bytes with the token cmd/granule-shim generates -- so this
// leaves room for an operator's own longer token while keeping what a
// peer can make the listener buffer per connection small.
const MaxLineBytes = 512

// TokenFile is where the guest-side agent (cmd/granule-mem-agent) reads
// Config.Token from, inside the guest. Its directory and the file are
// root-only; see InstallTokenCommand.
const TokenFile = "/run/granule-mem-agent/token"

// InstallTokenCommand is the guest shell command line that stores the
// token it is handed on standard input, as one line, in TokenFile.
//
// The token arrives on stdin rather than in the command line because a
// guest command line is as public as the kernel's: any guest process can
// read it from /proc/<pid>/cmdline while the command runs. read and
// printf are shell builtins in every guest shell granule supports (dash,
// bash, busybox ash), so the token never lands on another process's argv
// on the way to the file either. Reading one line, rather than to end
// of input, is what keeps this working on a guest whose sessions run
// under a pty, where end of input is not something stdin can say.
//
// The file is written beside its final name and renamed into place, so
// the agent never reads half of one; umask 077 makes both the directory
// and the file root-only from the moment they exist, and the chmod
// covers a directory left over from before a resume.
func InstallTokenCommand() string {
	dir := TokenFile[:strings.LastIndex(TokenFile, "/")]
	return "umask 077 && mkdir -p " + dir + " && chmod 700 " + dir +
		" && IFS= read -r t && [ -n \"$t\" ] && printf '%s\\n' \"$t\" > " + TokenFile + ".new && mv -f " + TokenFile + ".new " + TokenFile
}

// GuestRunner runs one shell command line inside the guest, as root,
// with stdin as its standard input and its standard error copied to
// stderr, and returns its exit status: guestexec.RunLine's shape, bound
// to a guestexec.Config, which is how cmd/granule-shim supplies it.
type GuestRunner func(ctx context.Context, line string, stdin io.Reader, stderr io.Writer) (int, error)

// DeliverToken stores token in the guest's TokenFile by running
// InstallTokenCommand through run, trying again every retryEvery until
// that succeeds or ctx is done (whose error it then returns). A guest
// still booting is the ordinary reason for a failed attempt -- run is
// expected to wait out a missing exec agent itself, for a while, before
// giving up -- so each failure is logged and retried rather than fatal:
// the guest's agent signals nothing until the token arrives, and a guest
// that cannot ask for memory still runs at the size it has.
func DeliverToken(ctx context.Context, run GuestRunner, token string, retryEvery time.Duration) error {
	if token == "" {
		return errors.New("memagent: no token to deliver")
	}
	for {
		var stderr strings.Builder
		code, err := run(ctx, InstallTokenCommand(), strings.NewReader(token+"\n"), &stderr)
		if err == nil && code == 0 {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			err = fmt.Errorf("exited with status %d: %q", code, strings.TrimSpace(stderr.String()))
		}
		log.Printf("memagent: delivering the token to the guest (retrying in %s): %v", retryEvery, err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryEvery):
		}
	}
}

// errNoToken is what Serve refuses an empty Config.Token with.
var errNoToken = errors.New("memagent: refusing to listen without a token: anything that can reach the listener could resize the guest")

// Server accepts pressure signals from a guest-side agent and turns them
// into resize calls against Resizer. See this package's doc comment for
// the wire protocol.
type Server struct {
	cfg Config
	api Resizer

	// conns bounds how many connections handle works on at once; see
	// maxConns.
	conns chan struct{}

	mu         sync.Mutex
	lastResize time.Time
	inFlight   bool
	metrics    *telemetry.MemoryHotplugMetrics
}

// New returns a Server ready for Serve. It performs no I/O itself.
func New(cfg Config, api Resizer) *Server {
	m := cfg.Metrics
	if m == nil {
		m = telemetry.NewMemoryHotplugMetrics()
	}
	if cfg.MaxMB > 0 {
		m.SetCeilingBytes(uint64(cfg.MaxMB) * 1024 * 1024)
	}
	return &Server{cfg: cfg, api: api, metrics: m, conns: make(chan struct{}, maxConns)}
}

// Metrics returns the memory hotplug telemetry and Prometheus metrics collector.
func (s *Server) Metrics() *telemetry.MemoryHotplugMetrics {
	return s.metrics
}

// Serve listens on cfg.Addr and handles pressure signals until ctx is
// done, at which point it closes the listener and returns nil. Blocks
// until then; run it in its own goroutine.
func (s *Server) Serve(ctx context.Context) error {
	if s.cfg.Token == "" {
		return errNoToken
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("memagent: listening on %s: %w", s.cfg.Addr, err)
	}
	// Said once, at startup, because otherwise nothing at all is logged
	// until the first signal arrives -- and on a guest whose kernel
	// cannot report pressure (see cmd/granule-mem-agent), that is never,
	// leaving no way to tell a listener that is up and unused from one
	// that was never started.
	log.Printf("memagent: listening on %s for pressure signals, growing this guest by %d MiB per signal up to %d MiB, at most once per %s",
		s.cfg.Addr, s.cfg.StepMB, s.cfg.MaxMB, s.cfg.Cooldown)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("memagent: accept on %s: %w", s.cfg.Addr, err)
			}
		}
		// Bounded, because anything that reaches the address can open
		// connections and keep them: each one is a goroutine and a file
		// descriptor in the process supervising the VMM until its read
		// deadline. The guest-side agent sends one signal at a time, so
		// a connection past maxConns is closed as soon as it is accepted
		// rather than queued.
		select {
		case s.conns <- struct{}{}:
			go func() {
				defer func() { <-s.conns }()
				s.handle(ctx, conn)
			}()
		default:
			conn.Close()
		}
	}
}

// maxConns is how many connections Serve reads signals from at once.
const maxConns = 8

// handle reads a single signal line ("PRESSURE <token> <value>" or
// "PRESSURE_LOW <token> <available_mb>") from conn and, if one arrives
// carrying the right token, requests growth or shrinking. One connection
// is used per signal rather than a persistent one, so a guest with
// nothing to say need not hold anything open.
//
// Everything a peer sends is logged quoted (%q): nothing on this side of
// the token check is known to come from the guest's agent, and a raw
// line could otherwise forge entries of its own in the shim's log.
func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	// One byte past the cap, so a line of exactly MaxLineBytes (newline
	// included) still reads whole and anything longer is told apart from
	// it rather than silently cut to fit.
	line, err := bufio.NewReader(io.LimitReader(conn, MaxLineBytes+1)).ReadString('\n')
	if len(line) > MaxLineBytes {
		log.Printf("memagent: refusing signal from %s: longer than %d bytes", conn.RemoteAddr(), MaxLineBytes)
		return
	}
	line = strings.TrimSpace(line)
	if line == "" {
		if err != nil {
			log.Printf("memagent: reading signal from %s: %v", conn.RemoteAddr(), err)
		}
		return
	}

	var command, payload string
	if rest, ok := strings.CutPrefix(line, "PRESSURE_LOW "); ok {
		command, payload = "PRESSURE_LOW", rest
	} else if rest, ok := strings.CutPrefix(line, "PRESSURE "); ok {
		command, payload = "PRESSURE", rest
	} else {
		log.Printf("memagent: unrecognized signal %q from %s", line, conn.RemoteAddr())
		return
	}

	fields := strings.Fields(payload)
	if len(fields) == 0 {
		log.Printf("memagent: unauthorized pressure signal from %s: missing token", conn.RemoteAddr())
		return
	}
	// Constant-time, so how long a refusal takes says nothing about how
	// much of a guessed token was right. (It does still say whether the
	// length was, which a token of fixed, generated length gives away
	// anyway.)
	if subtle.ConstantTimeCompare([]byte(fields[0]), []byte(s.cfg.Token)) != 1 {
		log.Printf("memagent: unauthorized pressure signal from %s: invalid token", conn.RemoteAddr())
		return
	}
	reading := strings.Join(fields[1:], " ")

	if command == "PRESSURE_LOW" {
		s.requestShrink(ctx, reading)
	} else {
		s.requestGrowth(ctx, reading)
	}
}

// requestGrowth grows the guest adaptively based on the pressure reading
// in reading (capped at cfg.MaxMB), unless it's already at the ceiling,
// another resize is in flight, or still within cfg.Cooldown of the last grow.
//
// Growth step scaling based on PSI "some avg10":
//   - Moderate pressure (10% <= avg10 < 30%): 1x cfg.StepMB
//   - High pressure (30% <= avg10 < 60%): 2x cfg.StepMB
//   - Critical pressure (avg10 >= 60%): 4x cfg.StepMB (clamped to cfg.MaxMB)
//
// If reading cannot be parsed as a float, it defaults to 1x cfg.StepMB.
//
// The size the step is added to is the VM's own, read back from
// cloud-hypervisor per signal rather than remembered: see the package
// doc comment. It is specifically VMInfo.DesiredRAMBytes -- what
// cloud-hypervisor was last asked for -- and not MemoryActualSize --
// what the guest has plugged in so far -- because a guest still working
// through an earlier grow would otherwise have that grow counted twice.
// (cfg.Cooldown exists for the same lag, and is checked first, so the
// common case of a guest signalling again mid-grow costs no vm.info
// call at all.)
func (s *Server) requestGrowth(ctx context.Context, reading string) {
	s.mu.Lock()
	if s.inFlight {
		s.mu.Unlock()
		log.Printf("memagent: pressure signal (%q) ignored: resize already in flight", reading)
		s.metrics.RecordSkipped("in_flight", reading, 0, uint64(s.cfg.MaxMB)*1024*1024)
		return
	}
	now := time.Now()
	if since := now.Sub(s.lastResize); !s.lastResize.IsZero() && since < s.cfg.Cooldown {
		s.mu.Unlock()
		log.Printf("memagent: pressure signal (%q) ignored: %s left in cooldown", reading, s.cfg.Cooldown-since)
		s.metrics.RecordSkipped("cooldown", reading, 0, uint64(s.cfg.MaxMB)*1024*1024)
		return
	}
	s.inFlight = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.inFlight = false
		s.mu.Unlock()
	}()

	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	info, err := s.api.Info(rctx)
	if err != nil {
		log.Printf("memagent: reading the guest's current size after pressure signal (%q): %v", reading, err)
		s.metrics.RecordFailed("info_error", reading, 0, 0)
		return
	}

	multiplier := 1
	if avg10, err := strconv.ParseFloat(reading, 64); err == nil {
		switch {
		case avg10 >= 60.0:
			multiplier = 4
		case avg10 >= 30.0:
			multiplier = 2
		default:
			multiplier = 1
		}
	}

	const mib = 1024 * 1024
	current := info.DesiredRAMBytes()
	ceiling := uint64(s.cfg.MaxMB) * mib
	s.metrics.SetAllocatedBytes(current)
	s.metrics.SetCeilingBytes(ceiling)

	if current >= ceiling {
		log.Printf("memagent: pressure signal (%q) ignored: already at ceiling of %d MiB", reading, s.cfg.MaxMB)
		s.metrics.RecordSkipped("already_at_ceiling", reading, current, ceiling)
		return
	}
	target := current + uint64(multiplier*s.cfg.StepMB)*mib
	if target > ceiling {
		target = ceiling
	}

	// ResizeIfNeeded rather than Resize: a resize from outside (another
	// "granule resize") between the read above and this call can leave
	// the VM already sized to target, which the virtio-mem device
	// answers with a 500 rather than a shrug.
	resizeStart := time.Now()
	resized, err := s.api.ResizeIfNeeded(rctx, target)
	resizeDuration := time.Since(resizeStart)
	if err != nil {
		log.Printf("memagent: resizing to %d MiB after pressure signal (%q): %v", target/mib, reading, err)
		s.metrics.RecordFailed("resize_error", reading, current, resizeDuration)
		return
	}
	if !resized {
		log.Printf("memagent: pressure signal (%q) ignored: guest is already at %d MiB", reading, target/mib)
		s.metrics.RecordSkipped("already_at_target", reading, current, ceiling)
		return
	}

	s.mu.Lock()
	s.lastResize = time.Now()
	s.mu.Unlock()

	s.metrics.RecordSuccess(current, target, ceiling, resizeDuration, reading)
	log.Printf("memagent: grew guest memory to %d MiB after pressure signal (%q)", target/mib, reading)
}

// requestShrink shrinks the guest by cfg.StepDownMB (or cfg.StepMB, floored
// at cfg.MinMB or the VM's boot size), unless it's already at the floor or
// still within ScaleDownCooldown of the last resize. reading is used for
// logging only.
func (s *Server) requestShrink(ctx context.Context, reading string) {
	s.mu.Lock()
	if s.inFlight {
		s.mu.Unlock()
		log.Printf("memagent: low pressure signal (%q) ignored: resize already in flight", reading)
		return
	}
	now := time.Now()
	scaleDownCooldown := s.cfg.ScaleDownCooldown
	if scaleDownCooldown == 0 {
		scaleDownCooldown = s.cfg.Cooldown
	}
	if since := now.Sub(s.lastResize); !s.lastResize.IsZero() && since < scaleDownCooldown {
		s.mu.Unlock()
		log.Printf("memagent: low pressure signal (%q) ignored: %s left in cooldown", reading, scaleDownCooldown-since)
		return
	}
	s.inFlight = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.inFlight = false
		s.mu.Unlock()
	}()

	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	info, err := s.api.Info(rctx)
	if err != nil {
		log.Printf("memagent: reading the guest's current size after low pressure signal (%q): %v", reading, err)
		return
	}

	const mib = 1024 * 1024
	current := info.DesiredRAMBytes()

	floor := info.Config.Memory.Size
	if s.cfg.MinMB > 0 {
		minBytes := uint64(s.cfg.MinMB) * mib
		if minBytes > floor {
			floor = minBytes
		}
	}

	if current <= floor {
		log.Printf("memagent: low pressure signal (%q) ignored: already at floor of %d MiB", reading, floor/mib)
		return
	}

	stepMB := s.cfg.StepMB
	if s.cfg.StepDownMB > 0 {
		stepMB = s.cfg.StepDownMB
	}
	stepBytes := uint64(stepMB) * mib

	var target uint64
	if current > stepBytes && current-stepBytes > floor {
		target = current - stepBytes
	} else {
		target = floor
	}

	resized, err := s.api.ResizeIfNeeded(rctx, target)
	if err != nil {
		log.Printf("memagent: resizing to %d MiB after low pressure signal (%q): %v", target/mib, reading, err)
		return
	}
	if !resized {
		log.Printf("memagent: low pressure signal (%q) ignored: guest is already at %d MiB", reading, target/mib)
		return
	}

	s.mu.Lock()
	s.lastResize = time.Now()
	s.mu.Unlock()

	log.Printf("memagent: shrunk guest memory to %d MiB after low pressure signal (%q)", target/mib, reading)
}
