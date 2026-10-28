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

package pkgcache

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// A DNS server is named by one port, and a client that retries over TCP
// after a truncated answer expects to find us on the same one -- so a
// dynamic port is only useful if both protocols end up holding it.
//
// The two port spaces are independent, so the number the kernel hands the
// UDP socket can already be taken in TCP by anything else on the host.
// That is not hypothetical: this file's own test failed grain CI with
// "listening tcp on 127.0.0.1:54642: bind: address already in use" while
// the rest of the suite had the box full of sockets. Asking repeatedly
// here is what makes a collision, if one happens, show up as a failure
// rather than as one flaky run in a hundred.
func TestDynamicPortIsBoundInBothProtocols(t *testing.T) {
	for i := 0; i < 50; i++ {
		udpLn, tcpLn, err := listenDNSPair("127.0.0.1:0")
		if err != nil {
			t.Fatalf("listenDNSPair: %v", err)
		}
		udpPort := udpLn.LocalAddr().(*net.UDPAddr).Port
		tcpPort := tcpLn.Addr().(*net.TCPAddr).Port
		if udpPort != tcpPort {
			t.Fatalf("udp port %d, tcp port %d: a client cannot reach both", udpPort, tcpPort)
		}
		udpLn.Close()
		tcpLn.Close()
	}
}

// An explicitly named port is the caller's choice, so a conflict on it is
// reported rather than retried -- and the UDP socket that did bind is
// handed back to the kernel on the way out, so a caller that goes on to
// try another port is not competing with itself.
func TestExplicitPortConflictIsReportedAndLeavesNothingBound(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer held.Close()
	addr := held.Addr().String()

	if _, _, err := listenDNSPair(addr); err == nil {
		t.Fatalf("listenDNSPair(%s) succeeded on a port already held for tcp", addr)
	}

	// The UDP half is free again: binding it now must not collide with a
	// socket the failed attempt left behind.
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatalf("ResolveUDPAddr: %v", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		t.Fatalf("the failed attempt left its udp socket bound: %v", err)
	}
	conn.Close()
}

func TestDNSServerResolution(t *testing.T) {
	server, err := NewDNSServer(DNSConfig{
		ListenAddr:  "127.0.0.1:0",
		InterceptIP: net.ParseIP("172.17.0.1"),
		ZeroEgress:  true,
	})
	if err != nil {
		t.Fatalf("NewDNSServer: %v", err)
	}

	if err := server.Start(); err != nil {
		t.Fatalf("server.Start: %v", err)
	}
	defer server.Shutdown(context.Background())

	addr := server.Addr().String()

	// Test 1: deb.debian.org (A record) -> 172.17.0.1
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{ID: 1, RecursionDesired: true},
		Questions: []dnsmessage.Question{
			{
				Name:  dnsmessage.MustNewName("deb.debian.org."),
				Type:  dnsmessage.TypeA,
				Class: dnsmessage.ClassINET,
			},
		},
	}
	packed, _ := msg.Pack()

	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("Dial UDP %s: %v", addr, err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(packed); err != nil {
		t.Fatalf("Write query: %v", err)
	}

	respBuf := make([]byte, 4096)
	n, err := conn.Read(respBuf)
	if err != nil {
		t.Fatalf("Read response: %v", err)
	}

	var respMsg dnsmessage.Message
	if err := respMsg.Unpack(respBuf[:n]); err != nil {
		t.Fatalf("Unpack response: %v", err)
	}

	if respMsg.Header.RCode != dnsmessage.RCodeSuccess {
		t.Errorf("RCode = %v, want Success", respMsg.Header.RCode)
	}
	if len(respMsg.Answers) != 1 {
		t.Fatalf("len(Answers) = %d, want 1", len(respMsg.Answers))
	}
	aRes, ok := respMsg.Answers[0].Body.(*dnsmessage.AResource)
	if !ok {
		t.Fatalf("Answer body is not *AResource: %T", respMsg.Answers[0].Body)
	}
	if aRes.A != [4]byte{172, 17, 0, 1} {
		t.Errorf("A record = %v, want [172, 17, 0, 1]", aRes.A)
	}

	// Test 2: registry.npmjs.org (AAAA record) -> NODATA (empty answers, RCodeSuccess)
	msgAAAA := dnsmessage.Message{
		Header: dnsmessage.Header{ID: 2, RecursionDesired: true},
		Questions: []dnsmessage.Question{
			{
				Name:  dnsmessage.MustNewName("registry.npmjs.org."),
				Type:  dnsmessage.TypeAAAA,
				Class: dnsmessage.ClassINET,
			},
		},
	}
	packedAAAA, _ := msgAAAA.Pack()
	if _, err := conn.Write(packedAAAA); err != nil {
		t.Fatalf("Write AAAA query: %v", err)
	}
	n, err = conn.Read(respBuf)
	if err != nil {
		t.Fatalf("Read AAAA response: %v", err)
	}
	var respAAAA dnsmessage.Message
	if err := respAAAA.Unpack(respBuf[:n]); err != nil {
		t.Fatalf("Unpack AAAA response: %v", err)
	}
	if respAAAA.Header.RCode != dnsmessage.RCodeSuccess {
		t.Errorf("AAAA RCode = %v, want Success", respAAAA.Header.RCode)
	}
	if len(respAAAA.Answers) != 0 {
		t.Errorf("AAAA len(Answers) = %d, want 0", len(respAAAA.Answers))
	}

	// Test 3: Unapproved domain in zero-egress mode -> NXDOMAIN
	msgNX := dnsmessage.Message{
		Header: dnsmessage.Header{ID: 3, RecursionDesired: true},
		Questions: []dnsmessage.Question{
			{
				Name:  dnsmessage.MustNewName("evil.example.com."),
				Type:  dnsmessage.TypeA,
				Class: dnsmessage.ClassINET,
			},
		},
	}
	packedNX, _ := msgNX.Pack()
	if _, err := conn.Write(packedNX); err != nil {
		t.Fatalf("Write NX query: %v", err)
	}
	n, err = conn.Read(respBuf)
	if err != nil {
		t.Fatalf("Read NX response: %v", err)
	}
	var respNX dnsmessage.Message
	if err := respNX.Unpack(respBuf[:n]); err != nil {
		t.Fatalf("Unpack NX response: %v", err)
	}
	if respNX.Header.RCode != dnsmessage.RCodeNameError {
		t.Errorf("NX RCode = %v, want NameError (NXDOMAIN)", respNX.Header.RCode)
	}
}

// The invariant this file's rest is about, stated once from the other
// direction: this service intercepts what it can serve and nothing else.
//
// It is worth a test of its own because breaking it is not a hypothetical.
// Between 2026-09-18 and fa0d90eb1 the cache was handed to every container
// a sandbox started as a blanket httpProxy, and the installer e2e --
// whose fake GitHub is a plain-HTTP listener on a loopback port -- spent a
// day failing on "grain's package cache does not fetch from 127.0.0.1:...:
// it proxies distribution archives ... and nothing else". That proxy is
// gone (sandbox.go says why at length, and TestChildContainerEnv holds the
// door shut), and DNS is what points a sandbox here now: a name on the
// allowlist resolves to this service, and every other name has to come
// back from the sandbox's real resolver untouched. The failure mode is the
// same one wearing a different hat -- a host the cache cannot serve
// answering 403 instead of answering -- so the forwarding half is asserted
// rather than assumed.
//
// (A fetch of a literal address, which is what the installer's own
// loopback GitHub is, never asks a resolver at all. That is a property of
// the shape, not an accident: nothing that is not a name can be caught by
// a name server.)
func TestDNSForwardsEverythingItDoesNotServe(t *testing.T) {
	// A stand-in for the sandbox's real resolver, answering every A
	// query with one address that is not this service's. Anything the
	// intercepted set has not claimed should come back holding it.
	const upstreamAnswer = "203.0.113.9"
	upstream := stubResolver(t, upstreamAnswer)

	server, err := NewDNSServer(DNSConfig{
		ListenAddr:  "127.0.0.1:0",
		InterceptIP: net.ParseIP("172.17.0.1"),
		// What a deployment actually runs (pkg/cli/packagecache.go):
		// forwarding, not zero-egress. A sandbox here keeps the rest of
		// the internet.
		ZeroEgress:  false,
		UpstreamDNS: []string{upstream},
		Log:         t.Logf,
	})
	if err != nil {
		t.Fatalf("NewDNSServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("server.Start: %v", err)
	}
	defer server.Shutdown(context.Background())
	addr := server.Addr().String()

	// Hosts an ordinary run reaches that are nobody's distribution
	// archive: a git remote, a deployment's own stand-in, a name
	// that merely ends in an allowlisted one's letters.
	for _, name := range []string{
		"gitlab.com",
		"bitbucket.org",
		"example.com",
		"notdebian.org",
		"debian.org.attacker.example",
		"pypi.org.attacker.example",
		"github.com",
		"codeload.github.com",
		"raw.githubusercontent.com",
	} {
		rcode, ips := askA(t, addr, name)
		if rcode != dnsmessage.RCodeSuccess {
			t.Errorf("%s answered %v, want the upstream's answer forwarded", name, rcode)
			continue
		}
		if len(ips) != 1 || ips[0] != upstreamAnswer {
			t.Errorf("%s resolved to %v, want %s from the sandbox's own resolver -- "+
				"a host this cache cannot serve must not be pointed at it", name, ips, upstreamAnswer)
		}
	}

	// And the half that has to keep working: a name on the list, and a
	// subdomain of one, still arrive here.
	for _, name := range []string{"deb.debian.org", "pypi.org", "snapshot.debian.org", "us.archive.ubuntu.com"} {
		rcode, ips := askA(t, addr, name)
		if rcode != dnsmessage.RCodeSuccess || len(ips) != 1 || ips[0] != "172.17.0.1" {
			t.Errorf("%s answered %v %v, want this service's own address: an archive that stops "+
				"resolving here is a cache nothing goes through", name, rcode, ips)
		}
	}
}

// TestInterceptedDomainsMatchOnALabelBoundary is the same claim one layer
// down, where the entries with a leading dot and the entries without it
// are two different rules and both have to stop at a label boundary. A
// suffix match that did not would hand this service names it has never
// heard of, which it can only refuse.
func TestInterceptedDomainsMatchOnALabelBoundary(t *testing.T) {
	server, err := NewDNSServer(DNSConfig{ListenAddr: "127.0.0.1:0", Log: t.Logf})
	if err != nil {
		t.Fatalf("NewDNSServer: %v", err)
	}
	for _, c := range []struct {
		domain string
		want   bool
	}{
		{"deb.debian.org", true},
		{"DEB.DEBIAN.ORG", true},
		{"security.debian.org", true},
		{"snapshot.debian.org", true}, // by the ".debian.org" entry
		{"debian.org", true},          // the bare zone of that entry
		{"archive.ubuntu.com", true},
		{"ghcr.io", true},
		{"pypi.org", true},
		{"notdebian.org", false},
		{"mydebian.org.example", false},
		{"debian.org.attacker.example", false},
		{"deb.debian.org.attacker.example", false},
		{"ghcr.io.attacker.example", false},
		{"github.com", false},
		{"gitlab.com", false},
		{"", false},
	} {
		if got := server.isDomainIntercepted(c.domain); got != c.want {
			t.Errorf("isDomainIntercepted(%q) = %v, want %v", c.domain, got, c.want)
		}
	}
}

// stubResolver answers every A query with answer, and returns its
// host:port. Anything else it is asked is answered with no records, which
// is enough for a test about where a query goes rather than what comes
// back.
func stubResolver(t *testing.T, answer string) string {
	t.Helper()
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the stand-in resolver: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	ip := net.ParseIP(answer).To4()
	if ip == nil {
		t.Fatalf("%q is not an IPv4 address", answer)
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := ln.ReadFrom(buf)
			if err != nil {
				return
			}
			var msg dnsmessage.Message
			if err := msg.Unpack(buf[:n]); err != nil || len(msg.Questions) == 0 {
				continue
			}
			q := msg.Questions[0]
			msg.Header.Response = true
			msg.Header.RecursionAvailable = true
			msg.Header.RCode = dnsmessage.RCodeSuccess
			msg.Answers = nil
			if q.Type == dnsmessage.TypeA {
				msg.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.AResource{A: [4]byte{ip[0], ip[1], ip[2], ip[3]}},
				}}
			}
			packed, err := msg.Pack()
			if err != nil {
				continue
			}
			_, _ = ln.WriteTo(packed, from)
		}
	}()
	return ln.LocalAddr().String()
}

// askA asks the server at addr for name's A records over UDP, and returns
// the response code with whatever addresses came back in it.
func askA(t *testing.T, addr, name string) (dnsmessage.RCode, []string) {
	t.Helper()
	query := dnsmessage.Message{
		Header: dnsmessage.Header{ID: 1, RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name:  dnsmessage.MustNewName(name + "."),
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		}},
	}
	packed, err := query.Pack()
	if err != nil {
		t.Fatalf("packing a query for %q: %v", name, err)
	}
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dialing %s: %v", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(packed); err != nil {
		t.Fatalf("asking about %q: %v", name, err)
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("reading the answer for %q: %v", name, err)
	}
	var resp dnsmessage.Message
	if err := resp.Unpack(buf[:n]); err != nil {
		t.Fatalf("unpacking the answer for %q: %v", name, err)
	}
	var ips []string
	for _, a := range resp.Answers {
		if rec, ok := a.Body.(*dnsmessage.AResource); ok {
			ips = append(ips, net.IP(rec.A[:]).String())
		}
	}
	return resp.Header.RCode, ips
}

// TestDNSRestrictedClientGetsNoForwarding is NetworkNone at the
// resolver: the one resolver every sandbox shares keeps forwarding for
// an ordinary sandbox, and answers NXDOMAIN to one named with
// RestrictClient for anything outside its allowlist -- a forwarding
// resolver is a channel to any nameserver on the internet, which is the
// whole of what that sandbox was promised it would not have.
func TestDNSRestrictedClientGetsNoForwarding(t *testing.T) {
	const upstreamAnswer = "203.0.113.9"
	upstream := stubResolver(t, upstreamAnswer)
	server, err := NewDNSServer(DNSConfig{
		ListenAddr:  "127.0.0.1:0",
		InterceptIP: net.ParseIP("172.17.0.1"),
		UpstreamDNS: []string{upstream},
		Log:         t.Logf,
	})
	if err != nil {
		t.Fatalf("NewDNSServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("server.Start: %v", err)
	}
	defer server.Shutdown(context.Background())
	addr := server.Addr().String()

	if rcode, ips := askA(t, addr, "exfil.attacker.example"); rcode != dnsmessage.RCodeSuccess || len(ips) != 1 || ips[0] != upstreamAnswer {
		t.Fatalf("unrestricted client got %v %v, want the upstream answer", rcode, ips)
	}

	loopback := net.ParseIP("127.0.0.1")
	server.RestrictClient(loopback)
	server.RestrictClient(loopback)
	if rcode, ips := askA(t, addr, "exfil.attacker.example"); rcode != dnsmessage.RCodeNameError || len(ips) != 0 {
		t.Errorf("restricted client got %v %v, want NXDOMAIN", rcode, ips)
	}
	if rcode, ips := askA(t, addr, "pypi.org"); rcode != dnsmessage.RCodeSuccess || len(ips) != 1 || ips[0] != "172.17.0.1" {
		t.Errorf("restricted client asking for pypi.org got %v %v, want this service's address", rcode, ips)
	}

	// Restrictions nest: one release of two leaves it restricted.
	server.UnrestrictClient(loopback)
	if rcode, _ := askA(t, addr, "exfil.attacker.example"); rcode != dnsmessage.RCodeNameError {
		t.Errorf("after one of two releases got %v, want NXDOMAIN still", rcode)
	}
	server.UnrestrictClient(loopback)
	if rcode, ips := askA(t, addr, "exfil.attacker.example"); rcode != dnsmessage.RCodeSuccess || len(ips) != 1 {
		t.Errorf("after both releases got %v %v, want the upstream answer again", rcode, ips)
	}
}
