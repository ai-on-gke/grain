package pkgcache

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// DefaultInterceptedDomains is the standard set of package repository,
// container registry, and toolchain domains intercepted by the DNS proxy.
var DefaultInterceptedDomains = []string{
	// APT / Linux distribution archives
	"deb.debian.org",
	"security.debian.org",
	"ftp.debian.org",
	"archive.debian.org",
	"archive.ubuntu.com",
	"security.ubuntu.com",
	"ports.ubuntu.com",
	"dl-cdn.alpinelinux.org",
	"download.docker.com",
	"packages.cloud.google.com",
	"apt.releases.hashicorp.com",
	"pkgs.tailscale.com",
	"pkgs.k8s.io",
	"apt.kubernetes.io",
	"deb.nodesource.com",
	"packages.microsoft.com",
	"ppa.launchpadcontent.net",
	"ppa.launchpad.net",
	".archive.ubuntu.com",
	".debian.org",
	".ubuntu.com",
	".alpinelinux.org",

	// PyPI / Python
	"pypi.org",
	"files.pythonhosted.org",
	".pythonhosted.org",
	"pypi.python.org",

	// NPM / Node
	"registry.npmjs.org",
	"registry.yarnpkg.com",

	// Go modules
	"proxy.golang.org",
	"sum.golang.org",

	// Docker / OCI container registries
	"registry-1.docker.io",
	"auth.docker.io",
	"docker.io",
	"ghcr.io",
	"gcr.io",
	"quay.io",
	"registry.k8s.io",
	"k8s.gcr.io",
	"pkg.dev",
	".pkg.dev",

	// Playwright browser CDN
	"cdn.playwright.dev",
	"playwright.azureedge.net",
	"playwright.download.prss.microsoft.com",
}

// DNSConfig configures the embedded DNS server.
type DNSConfig struct {
	// ListenAddr is the address to bind UDP and TCP listeners on (e.g. "172.17.0.1:53" or ":53").
	ListenAddr string
	// InterceptIP is the IPv4 address returned for intercepted domains (e.g. 172.17.0.1).
	InterceptIP net.IP
	// AllowedDomains overrides DefaultInterceptedDomains.
	AllowedDomains []string
	// ZeroEgress, when true, returns NXDOMAIN for any non-intercepted domain,
	// whoever asks. A resolver shared by sandboxes with different network
	// policies leaves it false and names the ones with no network through
	// RestrictClient instead.
	ZeroEgress bool
	// Restricted is the set of clients answered as though ZeroEgress
	// were set. A resolver beside a Server shares that Server's set
	// (Config.Restricted), so one call names a sandbox with no network to
	// both. Nil means a set of this server's own, reached through
	// RestrictClient.
	Restricted *ClientSet
	// UpstreamDNS is the list of upstream DNS resolvers (e.g. ["8.8.8.8:53"]).
	UpstreamDNS []string
	// Log receives diagnostic messages.
	Log func(format string, args ...any)
}

// DNSServer provides an internal DNS service for sandboxes.
type DNSServer struct {
	listenAddr     string
	interceptIP    net.IP
	allowedDomains []string
	zeroEgress     bool
	upstreamDNS    []string
	logf           func(format string, args ...any)

	// restricted are the client addresses answered as though ZeroEgress
	// were set, whatever it is: the sandboxes with model.NetworkNone.
	// Forwarding their out-of-allowlist queries upstream would hand every
	// one of them a general-purpose channel to any authoritative server on
	// the internet -- a hostname is as good a place to put data as any --
	// through the one address their missing default route still reaches.
	restricted *ClientSet

	udpLn *net.UDPConn
	tcpLn net.Listener

	mu      sync.Mutex
	closed  bool
	closeCh chan struct{}
	wg      sync.WaitGroup
}

// NewDNSServer creates a new DNSServer with cfg.
func NewDNSServer(cfg DNSConfig) (*DNSServer, error) {
	listenAddr := cfg.ListenAddr
	if listenAddr == "" {
		listenAddr = "127.0.0.1:53"
	}

	interceptIP := cfg.InterceptIP
	if interceptIP == nil {
		interceptIP = net.ParseIP("172.17.0.1")
	}

	allowed := cfg.AllowedDomains
	if len(allowed) == 0 {
		allowed = DefaultInterceptedDomains
	}

	upstream := cfg.UpstreamDNS
	if len(upstream) == 0 {
		upstream = hostResolvers()
		if len(upstream) == 0 {
			upstream = []string{"8.8.8.8:53", "1.1.1.1:53"}
		}
	}

	logf := cfg.Log
	if logf == nil {
		logf = log.Printf
	}

	restricted := cfg.Restricted
	if restricted == nil {
		restricted = &ClientSet{}
	}

	return &DNSServer{
		listenAddr:     listenAddr,
		interceptIP:    interceptIP,
		allowedDomains: allowed,
		zeroEgress:     cfg.ZeroEgress,
		upstreamDNS:    upstream,
		logf:           logf,
		restricted:     restricted,
		closeCh:        make(chan struct{}),
	}, nil
}

// RestrictClient makes s answer every query from ip that is not for an
// intercepted domain with NXDOMAIN rather than forwarding it upstream,
// until a matching UnrestrictClient. Calls nest (ClientSet). It changes
// the set s was configured with (DNSConfig.Restricted), so the HTTP half
// sharing that set restricts ip too.
func (s *DNSServer) RestrictClient(ip net.IP) {
	if s == nil {
		return
	}
	s.restricted.RestrictClient(ip)
}

// UnrestrictClient releases one RestrictClient for ip.
func (s *DNSServer) UnrestrictClient(ip net.IP) {
	if s == nil {
		return
	}
	s.restricted.UnrestrictClient(ip)
}

// zeroEgressFor reports whether a query from client is to be refused
// rather than forwarded when it is not for an intercepted domain.
func (s *DNSServer) zeroEgressFor(client net.IP) bool {
	return s.zeroEgress || s.restricted.Restricted(client)
}

// Start binds UDP and TCP listeners and begins serving DNS requests.
func (s *DNSServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	udpLn, tcpLn, err := listenDNSPair(s.listenAddr)
	if err != nil {
		return err
	}
	s.udpLn = udpLn
	s.tcpLn = tcpLn

	s.wg.Add(2)
	go s.serveUDP()
	go s.serveTCP()

	return nil
}

// dynamicPortAttempts is how many times listenDNSPair will ask the kernel
// for a fresh ephemeral port before giving up on finding one free in both
// protocols. Each collision needs the one TCP port matching the UDP port
// we were just handed to be busy, so a handful of tries is already far
// past what a loaded host produces.
const dynamicPortAttempts = 20

// listenDNSPair binds the UDP and TCP listeners a DNS server needs on the
// same port of addr.
//
// A port of 0 means "any", and the two protocols have to agree on which
// one they got: resolv.conf names a server by port alone, so a client
// that falls back to TCP after a truncated answer has to find us there.
// UDP and TCP keep separate port spaces, though, so the ephemeral port
// the kernel hands the UDP socket says nothing about whether the same
// number is free for TCP -- on a busy host it is routinely held by some
// unrelated outbound connection. Binding UDP and then assuming TCP will
// follow is a race, and it lost: pkgcache's own test failed grain CI with
// "listening tcp on 127.0.0.1:54642: bind: address already in use" while
// the rest of the suite was saturating the box with sockets.
//
// So a dynamic port is retried as a pair, and both are given up if either
// fails. An explicit port is not retried: the caller asked for that one,
// and a second attempt at it would only report the same conflict later.
func listenDNSPair(addr string) (*net.UDPConn, net.Listener, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("pkgcache dns: resolving udp address %s: %w", addr, err)
	}

	attempts := 1
	if udpAddr.Port == 0 {
		attempts = dynamicPortAttempts
	}

	var lastErr error
	for i := 0; i < attempts; i++ {
		udpLn, err := net.ListenUDP("udp", udpAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("pkgcache dns: listening udp on %s: %w", addr, err)
		}

		tcpAddr := addr
		if udpLn.LocalAddr() != nil {
			tcpAddr = udpLn.LocalAddr().String()
		}

		tcpLn, err := net.Listen("tcp", tcpAddr)
		if err == nil {
			return udpLn, tcpLn, nil
		}
		udpLn.Close()
		lastErr = fmt.Errorf("pkgcache dns: listening tcp on %s: %w", tcpAddr, err)
	}
	return nil, nil, lastErr
}

// Addr returns the local UDP listening address.
func (s *DNSServer) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.udpLn != nil {
		return s.udpLn.LocalAddr()
	}
	return nil
}

// Close stops the DNS listeners.
func (s *DNSServer) Close() error {
	return s.Shutdown(context.Background())
}

// Shutdown gracefully stops the DNS listeners.
func (s *DNSServer) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.closeCh)
	if s.udpLn != nil {
		s.udpLn.Close()
	}
	if s.tcpLn != nil {
		s.tcpLn.Close()
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *DNSServer) serveUDP() {
	defer s.wg.Done()
	buf := make([]byte, 4096)

	for {
		n, remoteAddr, err := s.udpLn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.closeCh:
				return
			default:
				s.logf("pkgcache dns: udp read error: %v", err)
				return
			}
		}

		reqBytes := make([]byte, n)
		copy(reqBytes, buf[:n])

		go func(data []byte, addr *net.UDPAddr) {
			respBytes := s.handleQuery(data, addr.IP)
			if len(respBytes) > 0 {
				_, _ = s.udpLn.WriteToUDP(respBytes, addr)
			}
		}(reqBytes, remoteAddr)
	}
}

func (s *DNSServer) serveTCP() {
	defer s.wg.Done()

	for {
		conn, err := s.tcpLn.Accept()
		if err != nil {
			select {
			case <-s.closeCh:
				return
			default:
				s.logf("pkgcache dns: tcp accept error: %v", err)
				return
			}
		}

		go s.handleTCPConn(conn)
	}
}

func (s *DNSServer) handleTCPConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	var length uint16
	if err := binary.Read(conn, binary.BigEndian, &length); err != nil {
		return
	}

	buf := make([]byte, length)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return
	}

	var client net.IP
	if addr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		client = addr.IP
	}
	resp := s.handleQuery(buf, client)
	if len(resp) == 0 {
		return
	}

	respLen := uint16(len(resp))
	if err := binary.Write(conn, binary.BigEndian, respLen); err != nil {
		return
	}
	_, _ = conn.Write(resp)
}

// handleQuery answers one query from client: an intercepted domain with
// InterceptIP, anything else with NXDOMAIN if client is held to zero
// egress (zeroEgressFor), and otherwise with whatever upstream says.
func (s *DNSServer) handleQuery(reqBytes []byte, client net.IP) []byte {
	var msg dnsmessage.Message
	if err := msg.Unpack(reqBytes); err != nil {
		return nil
	}

	if len(msg.Questions) == 0 {
		return nil
	}

	q := msg.Questions[0]
	name := strings.TrimSuffix(q.Name.String(), ".")

	if s.isDomainIntercepted(name) {
		return s.buildInterceptedResponse(msg, q)
	}

	if s.zeroEgressFor(client) {
		// Zero egress: return NXDOMAIN for all non-whitelisted domains
		msg.Header.Response = true
		msg.Header.Authoritative = true
		msg.Header.RCode = dnsmessage.RCodeNameError
		packed, err := msg.Pack()
		if err != nil {
			return nil
		}
		return packed
	}

	// Forward to upstream DNS
	return s.forwardUpstream(reqBytes)
}

func (s *DNSServer) isDomainIntercepted(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	for _, entry := range s.allowedDomains {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if strings.HasPrefix(entry, ".") {
			if strings.HasSuffix(domain, entry) || domain == strings.TrimPrefix(entry, ".") {
				return true
			}
			continue
		}
		if domain == entry || strings.HasSuffix(domain, "."+entry) {
			return true
		}
	}
	return false
}

func (s *DNSServer) buildInterceptedResponse(reqMsg dnsmessage.Message, q dnsmessage.Question) []byte {
	resp := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:                 reqMsg.Header.ID,
			Response:           true,
			OpCode:             reqMsg.Header.OpCode,
			Authoritative:      true,
			RecursionDesired:   reqMsg.Header.RecursionDesired,
			RecursionAvailable: true,
			RCode:              dnsmessage.RCodeSuccess,
		},
		Questions: []dnsmessage.Question{q},
	}

	if q.Type == dnsmessage.TypeA {
		ip4 := s.interceptIP.To4()
		if ip4 != nil {
			resp.Answers = []dnsmessage.Resource{
				{
					Header: dnsmessage.ResourceHeader{
						Name:  q.Name,
						Type:  dnsmessage.TypeA,
						Class: dnsmessage.ClassINET,
						TTL:   60,
					},
					Body: &dnsmessage.AResource{
						A: [4]byte{ip4[0], ip4[1], ip4[2], ip4[3]},
					},
				},
			}
		}
	}

	packed, err := resp.Pack()
	if err != nil {
		return nil
	}
	return packed
}

func (s *DNSServer) forwardUpstream(reqBytes []byte) []byte {
	for _, upstream := range s.upstreamDNS {
		if !strings.Contains(upstream, ":") {
			upstream += ":53"
		}
		conn, err := net.DialTimeout("udp", upstream, 2*time.Second)
		if err != nil {
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

		if _, err := conn.Write(reqBytes); err != nil {
			conn.Close()
			continue
		}

		respBuf := make([]byte, 4096)
		n, err := conn.Read(respBuf)
		conn.Close()
		if err != nil {
			continue
		}

		return respBuf[:n]
	}

	// If all upstreams fail, construct SERVFAIL
	var msg dnsmessage.Message
	if err := msg.Unpack(reqBytes); err == nil {
		msg.Header.Response = true
		msg.Header.RCode = dnsmessage.RCodeServerFailure
		if packed, err := msg.Pack(); err == nil {
			return packed
		}
	}
	return nil
}

// hostResolvers parses nameservers out of /etc/resolv.conf if readable.
func hostResolvers() []string {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nameserver ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				ip := fields[1]
				if !strings.HasPrefix(ip, "127.") && ip != "::1" {
					out = append(out, ip+":53")
				}
			}
		}
	}
	return out
}
