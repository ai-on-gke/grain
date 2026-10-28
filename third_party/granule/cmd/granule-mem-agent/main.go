// Command granule-mem-agent runs inside a guest image and asks the VM
// container (pkg/memagent) to grow the guest's memory when memory
// pressure rises.
//
// It reads /proc/pressure/memory (Pressure Stall Information, PSI),
// looking at the "some avg10" figure -- the share of the last ten
// seconds in which at least one task was stalled waiting on memory. When
// that figure exceeds GRANULE_MEM_AGENT_THRESHOLD (default 10%), it
// opens a short-lived TCP connection to GRANULE_MEM_AGENT_HOST:PORT
// (default 169.254.100.1:30090) and writes "PRESSURE <token> <value>\n".
//
// The token is the host's (pkg/memagent's Config.Token). granule writes
// it into tokenFile over the exec agent's vsock channel once the guest
// is up, and again after every resume; the file is root-only, which is
// what keeps it from every other process in this guest. It is re-read
// for every signal, so a token that arrives after this agent starts, or
// changes underneath it on a resume, is picked up without a restart.
// Until there is one, nothing is sent: the host refuses any signal
// without it.
//
// In addition to periodic polling, it sets up an event-driven PSI trigger
// using epoll on /proc/pressure/memory (default threshold: "some 150000 1000000",
// i.e., 150ms stall within any 1s window) to react to sudden memory spikes
// sub-second without waiting for the 10s polling interval.
//
// Tunables:
//
//	GRANULE_MEM_AGENT_INTERVAL   polling interval (Go duration, default 10s)
//	GRANULE_MEM_AGENT_THRESHOLD  "some avg10" threshold to signal at (float, default 10.0)
//	GRANULE_MEM_AGENT_TRIGGER    PSI trigger specification (default "some 150000 1000000")
//	GRANULE_MEM_AGENT_STABILIZATION_WINDOW idle time before requesting scale-down (default 5m)
//	GRANULE_MEM_AGENT_AVAILABLE_PERCENT    percentage of MemAvailable to consider idle (default 60.0)
//	GRANULE_MEM_AGENT_HOST       host to dial (default: default route's gateway)
//	GRANULE_MEM_AGENT_PORT       port to dial (default: 30090)
//	GRANULE_MEM_AGENT_TOKEN      token to sign signals with, overriding tokenFile
//	GRANULE_MEM_AGENT_TOKEN_FILE where to read the token from (default tokenFile)
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	pressurePath = "/proc/pressure/memory"
	meminfoPath  = "/proc/meminfo"
	routePath    = "/proc/net/route"

	// tokenFile is where granule's host side delivers the token: see
	// pkg/memagent's TokenFile, which this must match (main_test.go
	// checks). Not imported from there, because that package would bring
	// the whole host-side hypervisor client into this static guest binary.
	tokenFile = "/run/granule-mem-agent/token"

	defaultInterval            = 10 * time.Second
	defaultThreshold           = 10.0
	defaultPort                = 30090
	defaultTrigger             = "some 150000 1000000"
	defaultStabilizationWindow = 5 * time.Minute
	defaultAvailablePercent    = 60.0
)

type psiTrigger struct {
	file *os.File
	epfd int
}

func registerTrigger(path, triggerSpec string) (*psiTrigger, error) {
	f, err := os.OpenFile(path, os.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	spec := strings.TrimSpace(triggerSpec) + "\n"
	if _, err := f.Write([]byte(spec)); err != nil {
		f.Close()
		return nil, fmt.Errorf("write trigger %q to %s: %w", triggerSpec, path, err)
	}

	epfd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("epoll_create1: %w", err)
	}

	event := unix.EpollEvent{
		Events: unix.EPOLLPRI,
		Fd:     int32(f.Fd()),
	}
	if err := unix.EpollCtl(epfd, unix.EPOLL_CTL_ADD, int(f.Fd()), &event); err != nil {
		unix.Close(epfd)
		f.Close()
		return nil, fmt.Errorf("epoll_ctl add: %w", err)
	}

	return &psiTrigger{file: f, epfd: epfd}, nil
}

func (t *psiTrigger) Close() error {
	var errs []error
	if t.epfd >= 0 {
		if err := unix.Close(t.epfd); err != nil {
			errs = append(errs, err)
		}
		t.epfd = -1
	}
	if t.file != nil {
		if err := t.file.Close(); err != nil {
			errs = append(errs, err)
		}
		t.file = nil
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

func (t *psiTrigger) Wait(timeout time.Duration) (bool, error) {
	var timeoutMs int
	if timeout < 0 {
		timeoutMs = -1
	} else {
		timeoutMs = int(timeout.Milliseconds())
	}

	events := make([]unix.EpollEvent, 1)
	for {
		n, err := unix.EpollWait(t.epfd, events, timeoutMs)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return false, err
		}
		if n == 0 {
			return false, nil
		}
		if events[0].Events&(unix.EPOLLPRI|unix.EPOLLERR|unix.EPOLLHUP) != 0 {
			return true, nil
		}
		return false, nil
	}
}

// PSIStats holds memory pressure averages from /proc/pressure/memory.
type PSIStats struct {
	Avg10  float64
	Avg60  float64
	Avg300 float64
}

// MemInfo holds memory capacity metrics from /proc/meminfo.
type MemInfo struct {
	TotalKB     uint64
	AvailableKB uint64
}

// AvailableMB returns MemAvailable in MiB.
func (m MemInfo) AvailableMB() int {
	return int(m.AvailableKB / 1024)
}

// AvailablePercent returns the percentage of total RAM that is available.
func (m MemInfo) AvailablePercent() float64 {
	if m.TotalKB == 0 {
		return 0
	}
	return float64(m.AvailableKB) / float64(m.TotalKB) * 100.0
}

// MonitorConfig contains tunables for the memory monitoring loop.
type MonitorConfig struct {
	PressurePath        string
	MeminfoPath         string
	Interval            time.Duration
	Threshold           float64
	LowPercent          float64
	StabilizationWindow time.Duration
	Host                string
	Port                int
	Token               *tokenSource
}

// MonitorState tracks time-window state across polling intervals.
type MonitorState struct {
	idleSince time.Time
}

// ResetIdle clears any recorded idle stabilization window.
func (s *MonitorState) ResetIdle() {
	s.idleSince = time.Time{}
}

// Poll evaluates a single sample of PSI and MemInfo metrics.
func (s *MonitorState) Poll(now time.Time, psi PSIStats, mem MemInfo, cfg MonitorConfig, onHigh func(float64), onLow func(int)) {
	if psi.Avg10 >= cfg.Threshold {
		s.idleSince = time.Time{}
		if onHigh != nil {
			onHigh(psi.Avg10)
		}
		return
	}

	if psi.Avg300 == 0.0 && mem.AvailablePercent() > cfg.LowPercent {
		if s.idleSince.IsZero() {
			s.idleSince = now
		}
		if now.Sub(s.idleSince) >= cfg.StabilizationWindow {
			if onLow != nil {
				onLow(mem.AvailableMB())
			}
			s.idleSince = now
		}
		return
	}

	// Memory is neither strained nor idle (e.g. avg300 > 0.0 or MemAvailable <= LowPercent)
	s.idleSince = time.Time{}
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("granule-mem-agent: ")

	interval := envDuration("GRANULE_MEM_AGENT_INTERVAL", defaultInterval)
	threshold := envFloat("GRANULE_MEM_AGENT_THRESHOLD", defaultThreshold)
	port := envInt("GRANULE_MEM_AGENT_PORT", defaultPort)
	host := os.Getenv("GRANULE_MEM_AGENT_HOST")
	token := &tokenSource{
		override: firstEnv("GRANULE_MEM_AGENT_TOKEN", "CHV_MEM_AGENT_TOKEN"),
		path:     envString("GRANULE_MEM_AGENT_TOKEN_FILE", tokenFile),
	}
	trigger := envString("GRANULE_MEM_AGENT_TRIGGER", defaultTrigger)

	stabWindow := envDuration("GRANULE_MEM_AGENT_STABILIZATION_WINDOW", defaultStabilizationWindow)
	if v := os.Getenv("GRANULE_MEM_AGENT_IDLE_WINDOW"); v != "" {
		stabWindow = envDuration("GRANULE_MEM_AGENT_IDLE_WINDOW", stabWindow)
	}
	lowPercent := envFloat("GRANULE_MEM_AGENT_AVAILABLE_PERCENT", defaultAvailablePercent)
	if v := os.Getenv("GRANULE_MEM_AGENT_LOW_THRESHOLD"); v != "" {
		lowPercent = envFloat("GRANULE_MEM_AGENT_LOW_THRESHOLD", lowPercent)
	}

	// A kernel built without CONFIG_PSI has no /proc/pressure at all,
	// and never grows one later -- the kernel cloud-hypervisor publishes
	// for its direct-kernel boots is built this way. Exit 0 and let
	// systemd consider this service finished: logging the error every 10
	// seconds would just fill the guest's journal with noise for
	// something nobody can do anything about without rebuilding the
	// guest's kernel.
	if _, err := os.Stat(pressurePath); errors.Is(err, fs.ErrNotExist) {
		log.Printf("%s does not exist; kernel was built without CONFIG_PSI, disabling mem-agent", pressurePath)
		return
	}

	cfg := MonitorConfig{
		PressurePath:        pressurePath,
		MeminfoPath:         meminfoPath,
		Interval:            interval,
		Threshold:           threshold,
		LowPercent:          lowPercent,
		StabilizationWindow: stabWindow,
		Host:                host,
		Port:                port,
		Token:               token,
	}

	tr, err := registerTrigger(pressurePath, trigger)
	if err != nil {
		log.Printf("event-driven PSI trigger setup failed (%v); falling back to polling %s and %s every %s (threshold avg10 >= %.2f or idle some avg300 == 0.0, MemAvailable > %.0f%% for %s)",
			err, pressurePath, meminfoPath, interval, threshold, lowPercent, stabWindow)
	} else {
		defer tr.Close()
		log.Printf("monitoring %s with event-driven PSI trigger %q (fallback polling %s every %s, low threshold MemAvailable > %.0f%% for %s), signalling port %d",
			pressurePath, trigger, meminfoPath, interval, lowPercent, stabWindow, port)
	}

	var state MonitorState
	for {
		var triggered bool
		if tr != nil {
			hit, waitErr := tr.Wait(cfg.Interval)
			if waitErr != nil {
				log.Printf("waiting for PSI trigger on %s: %v", cfg.PressurePath, waitErr)
				time.Sleep(time.Second)
				continue
			}
			triggered = hit
		} else {
			time.Sleep(cfg.Interval)
		}

		psi, psiErr := readPSI(cfg.PressurePath)
		if psiErr != nil {
			log.Printf("reading %s: %v", cfg.PressurePath, psiErr)
		}
		mem, memErr := readMemInfo(cfg.MeminfoPath)
		if memErr != nil {
			log.Printf("reading %s: %v", cfg.MeminfoPath, memErr)
		}

		if triggered {
			state.ResetIdle()
			val := cfg.Threshold
			if psiErr == nil && psi.Avg10 > 0 {
				val = psi.Avg10
			}
			signalPressure(cfg.Host, cfg.Port, val, cfg.Token.Get())
			continue
		}

		if psiErr == nil {
			state.Poll(time.Now(), psi, mem, cfg,
				func(avg10 float64) {
					signalPressure(cfg.Host, cfg.Port, avg10, cfg.Token.Get())
				},
				func(availMB int) {
					if memErr == nil {
						signalLowPressure(cfg.Host, cfg.Port, availMB, cfg.Token.Get())
					}
				},
			)
		}
	}
}

// tokenSource is where signals get their token from: an operator's
// override from the environment if there is one, otherwise path, read
// afresh for every signal (see this command's doc comment).
type tokenSource struct {
	override string
	path     string

	// lastErr is the last reason Get had no token, so the same reason
	// is logged once rather than at every poll until it changes.
	lastErr string
}

// Get returns the token to sign the next signal with, or "" -- having
// logged why, the first time -- if there is none yet.
func (s *tokenSource) Get() string {
	if s.override != "" {
		return s.override
	}
	tok, err := readToken(s.path)
	if err != nil {
		if msg := err.Error(); msg != s.lastErr {
			log.Printf("not signalling until granule delivers a token: %v", err)
			s.lastErr = msg
		}
		return ""
	}
	if s.lastErr != "" {
		log.Printf("token now present at %s; signalling", s.path)
		s.lastErr = ""
	}
	return tok
}

// readToken returns the token stored in path: its first line, trimmed.
func readToken(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// A token is a few dozen bytes; there is no reason to read further
	// into a file that has turned out to be something else.
	line, err := bufio.NewReader(io.LimitReader(f, 4096)).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	tok := strings.TrimSpace(line)
	if tok == "" || strings.ContainsAny(tok, " \t") {
		return "", fmt.Errorf("%s holds no usable token", path)
	}
	return tok, nil
}

// firstEnv returns the value of the first of names that is set and
// non-empty, or "".
func firstEnv(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

// readPSI returns the memory pressure averages from /proc/pressure/memory.
func readPSI(path string) (PSIStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return PSIStats{}, err
	}
	defer f.Close()

	var stats PSIStats
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "some ") {
			continue
		}
		for _, field := range strings.Fields(line[5:]) {
			k, v, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			val, err := strconv.ParseFloat(v, 64)
			if err != nil {
				continue
			}
			switch k {
			case "avg10":
				stats.Avg10 = val
			case "avg60":
				stats.Avg60 = val
			case "avg300":
				stats.Avg300 = val
			}
		}
		return stats, nil
	}
	if err := sc.Err(); err != nil {
		return PSIStats{}, err
	}
	return PSIStats{}, fmt.Errorf("no \"some\" line found in %s", path)
}

// readPressure returns the "some avg10" field from /proc/pressure/memory.
func readPressure(path string) (float64, error) {
	stats, err := readPSI(path)
	if err != nil {
		return 0, err
	}
	return stats.Avg10, nil
}

// readMemInfo returns MemTotal and MemAvailable from /proc/meminfo.
func readMemInfo(path string) (MemInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return MemInfo{}, err
	}
	defer f.Close()

	var info MemInfo
	var freeKB, buffersKB, cachedKB uint64
	hasAvailable := false

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		valFields := strings.Fields(parts[1])
		if len(valFields) == 0 {
			continue
		}
		val, err := strconv.ParseUint(valFields[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			info.TotalKB = val
		case "MemAvailable":
			info.AvailableKB = val
			hasAvailable = true
		case "MemFree":
			freeKB = val
		case "Buffers":
			buffersKB = val
		case "Cached":
			cachedKB = val
		}
	}
	if err := sc.Err(); err != nil {
		return MemInfo{}, err
	}
	if info.TotalKB == 0 {
		return MemInfo{}, fmt.Errorf("no MemTotal found in %s", path)
	}
	if !hasAvailable {
		info.AvailableKB = freeKB + buffersKB + cachedKB
	}
	return info, nil
}

// signalPressure asks the host to grow this guest's memory, by dialing
// its pkg/memagent listener on port and writing a single
// "PRESSURE <token> <value>\n" line. An empty token sends nothing (the
// host would refuse it; tokenSource.Get has said why). Errors
// (including no listener there at all, e.g. because the host has
// CHV_MEM_AGENT unset) are logged and otherwise ignored: this is just
// the next poll's problem too.
//
// An empty host falls back to this guest's default route's gateway (see
// defaultGateway), which is wrong here: the guest is spliced onto the
// container network, so its default route leads out there rather than to
// the host side of this VM. The guest's control link is configured with
// an explicit host instead -- see deploy/guest-image's
// granule-control-net.
func signalPressure(host string, port int, avg10 float64, token string) {
	if token == "" {
		return
	}
	if host == "" {
		gw, err := defaultGateway(routePath)
		if err != nil {
			log.Printf("finding default gateway: %v", err)
			return
		}
		host = gw.String()
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		log.Printf("signalling %s: %v", addr, err)
		return
	}
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	line := fmt.Sprintf("PRESSURE %s %.2f\n", token, avg10)
	if _, err := io.WriteString(conn, line); err != nil {
		log.Printf("signalling %s: %v", addr, err)
		return
	}
	log.Printf("signalled memory pressure (some avg10=%.2f) to %s", avg10, addr)
}

// signalLowPressure asks the host to shrink this guest's memory, by dialing
// its pkg/memagent listener on port and writing a single
// "PRESSURE_LOW <token> <available_mb>\n" line, or nothing if token is
// empty, as signalPressure does.
func signalLowPressure(host string, port int, availableMB int, token string) {
	if token == "" {
		return
	}
	if host == "" {
		gw, err := defaultGateway(routePath)
		if err != nil {
			log.Printf("finding default gateway: %v", err)
			return
		}
		host = gw.String()
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		log.Printf("signalling %s: %v", addr, err)
		return
	}
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	line := fmt.Sprintf("PRESSURE_LOW %s %d\n", token, availableMB)
	if _, err := io.WriteString(conn, line); err != nil {
		log.Printf("signalling %s: %v", addr, err)
		return
	}
	log.Printf("signalled low memory pressure (available=%d MiB) to %s", availableMB, addr)
}

// defaultGateway returns this guest's default route's gateway address.
func defaultGateway(path string) (net.IP, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Scan() // header line: "Iface Destination Gateway Flags ..."
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		if fields[1] != "00000000" {
			continue
		}
		return hexLEToIPv4(fields[2])
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("no default route in route table")
}

// hexLEToIPv4 parses an 8-hex-digit little-endian string (as formatted
// in /proc/net/route) into a net.IP.
func hexLEToIPv4(s string) (net.IP, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decoding gateway %q: %w", s, err)
	}
	if len(b) != 4 {
		return nil, fmt.Errorf("gateway %q decoded to %d bytes, want 4", s, len(b))
	}
	ipInt := binary.LittleEndian.Uint32(b)
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, ipInt)
	return ip, nil
}

func envDuration(name string, fallback time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("parsing %s=%q: %v; using default %s", name, v, err, fallback)
		return fallback
	}
	return d
}

func envFloat(name string, fallback float64) float64 {
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		log.Printf("parsing %s=%q: %v; using default %.2f", name, v, err, fallback)
		return fallback
	}
	return f
}

func envInt(name string, fallback int) int {
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("parsing %s=%q: %v; using default %d", name, v, err, fallback)
		return fallback
	}
	return i
}

func envString(name string, fallback string) string {
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}
	return v
}
