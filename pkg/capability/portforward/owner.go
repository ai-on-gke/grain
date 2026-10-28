package portforward

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// procRoot is where the process and socket tables are read from; tests
// point it elsewhere.
var procRoot = "/proc"

// Service is the set of processes a capability server's data-plane
// listeners must belong to (docs/capabilities-spec.md §5.3.2): the
// process serving its control plane -- a stdio server's spawned child, or
// whatever owns an HTTP server's /mcp listener -- and its descendants,
// and, for an HTTP server running in a cgroup of its own (a systemd unit,
// a container), anything else in that cgroup.
type Service struct {
	PID int
	// Cgroup is the contents of /proc/<pid>/cgroup that also qualifies a
	// process; empty admits by ancestry alone. It is never grain's own
	// cgroup (ServiceFor), or every listener of the daemon -- the git
	// proxy, the UI -- would qualify.
	Cgroup string
}

// Contains reports whether pid belongs to the service.
func (s Service) Contains(pid int) bool {
	if s.PID <= 0 || pid <= 0 {
		return false
	}
	if s.Cgroup != "" {
		if cg, err := cgroupOf(pid); err == nil && cg == s.Cgroup {
			return true
		}
	}
	// Walk up from pid; a bounded walk, since a table read mid-fork can
	// in principle show a loop.
	for i := 0; i < 1024 && pid > 1; i++ {
		if pid == s.PID {
			return true
		}
		parent, err := parentOf(pid)
		if err != nil {
			return false
		}
		pid = parent
	}
	return pid == s.PID
}

// ServiceFor identifies the service of a capability server: pid is a
// stdio server's child (0 for an HTTP server) and serverURL an HTTP
// server's serverUrl. An HTTP server is identified by the process owning
// the listener its serverUrl names, which has to be on this host's
// loopback: ports are only available from same-host servers (§5.3.2).
func ServiceFor(pid int, serverURL string) (Service, error) {
	if pid > 0 {
		return Service{PID: pid}, nil
	}
	u, err := url.Parse(serverURL)
	if err != nil || u.Host == "" {
		return Service{}, fmt.Errorf("capability server has no stdio process and no usable serverUrl %q", serverURL)
	}
	host := u.Hostname()
	if host == "localhost" {
		host = "127.0.0.1"
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !addr.IsLoopback() {
		return Service{}, fmt.Errorf("ports are only available from a capability server on this host's loopback, and %s is not", u.Host)
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return Service{}, fmt.Errorf("serverUrl %q names no port", serverURL)
	}
	owner, err := ListenerOwner(n)
	if err != nil {
		return Service{}, fmt.Errorf("finding the process serving %s: %w", serverURL, err)
	}
	cg, err := cgroupOf(owner)
	if err != nil {
		return Service{PID: owner}, nil
	}
	if self, err := cgroupOf(os.Getpid()); err == nil && self == cg {
		// Sharing grain's own cgroup: ancestry alone, or grain's own
		// listeners would qualify.
		return Service{PID: owner}, nil
	}
	return Service{PID: owner, Cgroup: cg}, nil
}

// ConnOwner is the process owning the far end of conn, an established
// TCP connection this process dialled to a loopback listener: the socket
// whose local address is conn's remote one and whose peer is conn's local
// one. A connection whose far end cannot be found (another user's
// process this one may not inspect, a socket closed since) is an error,
// which callers treat as failing the check.
func ConnOwner(conn net.Conn) (int, error) {
	inode, err := connInode(conn)
	if err != nil {
		return 0, err
	}
	return inodeOwner(inode)
}

func connInode(conn net.Conn) (uint64, error) {
	local, err := netip.ParseAddrPort(conn.LocalAddr().String())
	if err != nil {
		return 0, err
	}
	remote, err := netip.ParseAddrPort(conn.RemoteAddr().String())
	if err != nil {
		return 0, err
	}
	// Until the server accept()s it, the far end sits in the listener's
	// queue with no file -- inode 0 -- and so no owner to find yet; it
	// is waited for, briefly, rather than failed.
	deadline := time.Now().Add(acceptWait)
	for {
		inode, err := findSocket(func(e tcpEntry) bool {
			return e.local == remote && e.remote == local
		})
		if err == nil {
			return inode, nil
		}
		if time.Now().After(deadline) {
			return 0, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// acceptWait is how long ConnOwner waits for a server to accept.
var acceptWait = 2 * time.Second

// ListenerInode is the socket inode listening on port on loopback or the
// wildcard address.
func ListenerInode(port int) (uint64, error) {
	return findSocket(func(e tcpEntry) bool {
		if e.state != tcpListen || int(e.local.Port()) != port {
			return false
		}
		a := e.local.Addr().Unmap()
		return a.IsLoopback() || a.IsUnspecified()
	})
}

// ListenerOwner is the process listening on port on loopback or the
// wildcard address.
func ListenerOwner(port int) (int, error) {
	inode, err := ListenerInode(port)
	if err != nil {
		return 0, err
	}
	return inodeOwner(inode)
}

const tcpListen = 0x0A

type tcpEntry struct {
	local, remote netip.AddrPort
	state         int
	inode         uint64
}

var errNoSocket = errors.New("no such socket")

func findSocket(match func(tcpEntry) bool) (uint64, error) {
	for _, table := range []string{"net/tcp", "net/tcp6"} {
		f, err := os.Open(filepath.Join(procRoot, table))
		if err != nil {
			continue
		}
		entries := parseTCPTable(f)
		f.Close()
		for _, e := range entries {
			if e.inode != 0 && match(e) {
				return e.inode, nil
			}
		}
	}
	return 0, errNoSocket
}

// parseTCPTable reads /proc/net/tcp{,6}: "sl local rem st tx:rx tr:when
// retrnsmt uid timeout inode ...", addresses in hex.
func parseTCPTable(f *os.File) []tcpEntry {
	var out []tcpEntry
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		local, err1 := parseHexAddr(fields[1])
		remote, err2 := parseHexAddr(fields[2])
		state, err3 := strconv.ParseInt(fields[3], 16, 32)
		inode, err4 := strconv.ParseUint(fields[9], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}
		out = append(out, tcpEntry{local: local, remote: remote, state: int(state), inode: inode})
	}
	return out
}

// parseHexAddr parses "0100007F:1F90": the address as the kernel's
// 32-bit words in host (little-endian) order, then the port big-endian.
func parseHexAddr(s string) (netip.AddrPort, error) {
	addrHex, portHex, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("bad address %q", s)
	}
	raw, err := hex.DecodeString(addrHex)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.AddrPort{}, fmt.Errorf("bad address %q", s)
	}
	for i := 0; i < len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("bad port in %q", s)
	}
	addr, _ := netip.AddrFromSlice(raw)
	return netip.AddrPortFrom(addr.Unmap(), uint16(port)), nil
}

// inodeOwner finds the process holding socket inode open, scanning every
// /proc/<pid>/fd this process may read.
func inodeOwner(inode uint64) (int, error) {
	want := "socket:[" + strconv.FormatUint(inode, 10) + "]"
	procs, err := os.ReadDir(procRoot)
	if err != nil {
		return 0, err
	}
	for _, p := range procs {
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join(procRoot, p.Name(), "fd")
		fds, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			if link, err := os.Readlink(filepath.Join(dir, fd.Name())); err == nil && link == want {
				return pid, nil
			}
		}
	}
	return 0, fmt.Errorf("no process this one can inspect holds socket %d", inode)
}

func parentOf(pid int) (int, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "PPid:"); ok {
			return strconv.Atoi(strings.TrimSpace(v))
		}
	}
	return 0, fmt.Errorf("process %d has no PPid", pid)
}

func cgroupOf(pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", err
	}
	cg := strings.TrimSpace(string(data))
	if cg == "" || cg == "0::/" {
		// The root cgroup is everybody's, so it identifies nothing.
		return "", errors.New("process is in the root cgroup")
	}
	return cg, nil
}
