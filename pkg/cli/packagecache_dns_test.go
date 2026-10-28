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

package cli

// The interception half of the package cache, and the one invariant it
// has to hold: every address the daemon binds is an address the sandbox
// is told to go to, and every address it tells the sandbox about is one
// it bound. When the resolver's two halves were written as independent
// literals -- 127.0.0.1:53 for the bind, 172.17.0.1 for the resolv.conf
// -- every sandbox on the deployment lost name resolution altogether,
// and nothing said so. The same invariant covers the listeners a DNS
// answer redirects a fetch to (443 and 80 of that same address), which
// is the other way a sandbox can be sent somewhere nobody answers.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/pkgcache"
	"golang.org/x/net/dns/dnsmessage"
)

// resolvNameserver is the nameserver a sandbox would end up using,
// read back out of the file grain writes into it.
func resolvNameserver(t *testing.T, dnsIP string) string {
	t.Helper()
	file := pkgcache.SandboxResolvConf(dnsIP)
	if file.Path != pkgcache.ResolvConfPath {
		t.Fatalf("resolv.conf written to %q, want %q", file.Path, pkgcache.ResolvConfPath)
	}
	for _, line := range strings.Split(file.Content, "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "nameserver" {
			return fields[1]
		}
	}
	t.Fatalf("no nameserver line in the sandbox's resolv.conf:\n%s", file.Content)
	return ""
}

func TestPackageCacheDNSBindsWhereTheSandboxIsPointed(t *testing.T) {
	for _, advertise := range []string{"", "172.17.0.1", "10.1.2.3"} {
		listenAddr, dnsIP := daemon.PackageCacheDNSAddrs(advertise)
		host, port, err := net.SplitHostPort(listenAddr)
		if err != nil {
			t.Fatalf("advertising %q: listen address %q: %v", advertise, listenAddr, err)
		}
		if port != "53" {
			t.Errorf("advertising %q: DNS bound on port %s, want 53 -- a resolver on any other "+
				"port is one no sandbox can find", advertise, port)
		}
		// The assertion this whole file exists for.
		if nameserver := resolvNameserver(t, dnsIP); nameserver != host {
			t.Errorf("advertising %q: sandboxes resolve through %s but the daemon binds %s "+
				"-- a sandbox pointed at a dead nameserver has no DNS at all",
				advertise, nameserver, host)
		}
		if advertise != "" && dnsIP != advertise {
			t.Errorf("advertising %q: sandboxes told to use %s", advertise, dnsIP)
		}
	}
}

func TestStartSandboxDNSServesAndReportsItsFailures(t *testing.T) {
	server, err := daemon.StartSandboxDNS("127.0.0.1:0", "172.17.0.1", nil, nil)
	if err != nil {
		t.Fatalf("startSandboxDNS: %v", err)
	}
	defer server.Close()

	msg := dnsmessage.Message{
		Header: dnsmessage.Header{ID: 7, RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name:  dnsmessage.MustNewName("deb.debian.org."),
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		}},
	}
	packed, err := msg.Pack()
	if err != nil {
		t.Fatalf("packing the query: %v", err)
	}
	conn, err := net.Dial("udp", server.Addr().String())
	if err != nil {
		t.Fatalf("dialling the resolver: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(packed); err != nil {
		t.Fatalf("writing the query: %v", err)
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}
	var reply dnsmessage.Message
	if err := reply.Unpack(buf[:n]); err != nil {
		t.Fatalf("unpacking the answer: %v", err)
	}
	if len(reply.Answers) != 1 {
		t.Fatalf("got %d answers for deb.debian.org, want 1", len(reply.Answers))
	}
	a, ok := reply.Answers[0].Body.(*dnsmessage.AResource)
	if !ok {
		t.Fatalf("answer is a %T, want an A record", reply.Answers[0].Body)
	}
	if got := net.IPv4(a.A[0], a.A[1], a.A[2], a.A[3]).String(); got != "172.17.0.1" {
		t.Errorf("deb.debian.org resolved to %s, want the cache at 172.17.0.1", got)
	}

	// A bind that cannot succeed comes back as an error rather than as a
	// server that is quietly answering nobody: 192.0.2.1 is TEST-NET-1
	// and is on no machine's interfaces.
	if _, err := daemon.StartSandboxDNS("192.0.2.1:53", "172.17.0.1", nil, nil); err == nil {
		t.Error("startSandboxDNS reported success binding an address that is not on this host")
	}
}

// The other half of the same invariant: a DNS answer is only worth
// giving if something answers at the address it names. Interception
// sends every fetch of a package domain to the intercept IP's 443 (and
// 80 for a plain-HTTP source), so those are the addresses the resolver's
// answer commits this daemon to.
func TestTransparentListenersFollowTheAddressTheResolverHandsOut(t *testing.T) {
	for _, advertise := range []string{"", "172.17.0.1", "10.1.2.3"} {
		_, dnsIP := daemon.PackageCacheDNSAddrs(advertise)
		httpsAddr, httpAddr := daemon.PackageCacheTransparentAddrs(dnsIP)
		for _, want := range []struct {
			addr string
			port string
		}{{httpsAddr, "443"}, {httpAddr, "80"}} {
			host, port, err := net.SplitHostPort(want.addr)
			if err != nil {
				t.Fatalf("advertising %q: transparent address %q: %v", advertise, want.addr, err)
			}
			if port != want.port {
				t.Errorf("advertising %q: transparent listener on port %s, want %s -- a client that "+
					"resolved a hostname asks for the scheme's own port and nothing else",
					advertise, port, want.port)
			}
			if nameserver := resolvNameserver(t, dnsIP); host != nameserver {
				t.Errorf("advertising %q: sandboxes resolve package domains to %s but this daemon "+
					"serves them on %s -- nothing answers where they are sent",
					advertise, nameserver, host)
			}
		}
	}
}

func TestTransparentServersServeTheCacheUnderItsOwnCA(t *testing.T) {
	ca, err := pkgcache.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	handler := pkgcache.NewServer(pkgcache.Config{Log: func(string, ...any) {}})

	// Ephemeral ports: the real pair is 443 and 80, which a test process
	// has no business binding. What is under test here is that both
	// halves serve the cache and that the TLS half issues for whatever
	// name the client asked for -- the ports are
	// TestTransparentListenersFollowTheAddressTheResolverHandsOut's
	// business.
	started, err := daemon.StartTransparentServers("127.0.0.1:0", "127.0.0.1:0", handler, ca)
	if err != nil {
		t.Fatalf("startTransparentServers: %v", err)
	}
	defer started.Shutdown(context.Background())

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca.RootCertPEM()) {
		t.Fatal("the CA's own root certificate did not parse")
	}
	// Dialled under an intercepted domain's name, because that is the
	// handshake a sandbox makes: it believes it is talking to
	// deb.debian.org, and the certificate it is offered has to be one
	// this deployment's CA -- installed into that sandbox's trust store
	// -- issued for that name.
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "deb.debian.org"},
	}}
	for _, url := range []string{"https://" + started.HttpsAddr + "/", "http://" + started.HttpAddr + "/"} {
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("reading %s: %v", url, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: %d, want 200: %s", url, resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "grain proxy cache") {
			t.Errorf("GET %s served something that is not the proxy cache: %s", url, body)
		}
	}
}

// Both listeners or neither, and a failure that says so rather than a
// half-bound pair: a sandbox cannot be told to intercept https and not
// http, so half of this is a sandbox whose plain-HTTP archive fetches
// fail outright.
func TestTransparentServersAreBothOrNeither(t *testing.T) {
	ca, err := pkgcache.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	handler := pkgcache.NewServer(pkgcache.Config{Log: func(string, ...any) {}})

	// A free port to watch: 192.0.2.1 is TEST-NET-1 and on no machine's
	// interfaces, so the HTTP half cannot bind, and the HTTPS half that
	// did bind has to be handed back.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	addr := probe.Addr().String()
	probe.Close()

	if started, err := daemon.StartTransparentServers(addr, "192.0.2.1:80", handler, ca); err == nil {
		started.Shutdown(context.Background())
		t.Fatal("startTransparentServers reported success with an address that is not on this host")
	}
	deadline := time.Now().Add(daemonWaitLimit)
	var again net.Listener
	for time.Now().Before(deadline) {
		again, err = net.Listen("tcp", addr)
		if err == nil {
			again.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Errorf("%s is still held after the pair failed to start: %v -- a failed start has to leave "+
			"nothing bound, or the retry an operator makes by restarting the daemon cannot succeed", addr, err)
	}
}

func TestSandboxDNSLeavesTheSandboxAloneWhenTheResolverIsDown(t *testing.T) {
	live := daemon.NewLiveConfig(nil, nil, daemon.Config{PackageCacheEnabled: true}, nil)

	serving := &daemon.PackageCache{DnsIP: "172.17.0.1", DnsServing: true}
	if got := serving.SandboxDNS(live)(context.Background()); got != "172.17.0.1" {
		t.Errorf("sandboxDNS = %q with the resolver up, want 172.17.0.1", got)
	}

	down := &daemon.PackageCache{DnsIP: "172.17.0.1"}
	if got := down.SandboxDNS(live)(context.Background()); got != "" {
		t.Errorf("sandboxDNS = %q with the resolver down, want \"\" -- pointing a sandbox at a "+
			"nameserver nobody answers on takes away the DNS it already had", got)
	}

	off := daemon.NewLiveConfig(nil, nil, daemon.Config{}, nil)
	if got := serving.SandboxDNS(off)(context.Background()); got != "" {
		t.Errorf("sandboxDNS = %q with the package cache disabled, want \"\"", got)
	}
}
