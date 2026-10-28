package security

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/pkgcache"
)

// A sandbox with network access "none" reaches this deployment's own
// services -- the package cache and the git proxy -- and nothing else.
// On a VM backend that is three layers (orchestrator.GranuleSandboxes'
// enforceGuestNetwork): the guest's default route taken away, a host
// nftables chain dropping everything not for its gateway or the git
// proxy, and the sandbox resolver refusing to forward the guest's
// queries. The first two act on a live host and have no exported entry
// point to drive without one; the resolver does, and it is the layer
// both of the others leave reachable on purpose, so it is the one stated
// here: a restricted client resolves the package registries the cache
// serves, and every other name is NXDOMAIN without the query ever
// leaving the deployment. A backend that cannot enforce "none" at all
// refuses the sandbox rather than handing over one with the host's
// network.

// stubUpstream is the internet's resolver: it answers every A query
// with one address, and counts the queries that reached it.
func stubUpstream(t *testing.T, answer string) (string, *atomic.Int32) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	var asked atomic.Int32
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			asked.Add(1)
			var msg dnsmessage.Message
			if msg.Unpack(buf[:n]) != nil || len(msg.Questions) == 0 {
				continue
			}
			msg.Header.Response = true
			ip := net.ParseIP(answer).To4()
			msg.Answers = []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{Name: msg.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
				Body:   &dnsmessage.AResource{A: [4]byte{ip[0], ip[1], ip[2], ip[3]}},
			}}
			if out, err := msg.Pack(); err == nil {
				_, _ = conn.WriteTo(out, addr)
			}
		}
	}()
	return conn.LocalAddr().String(), &asked
}

func resolveA(t *testing.T, server, name string) (dnsmessage.RCode, []string) {
	t.Helper()
	q := dnsmessage.Message{
		Header: dnsmessage.Header{ID: 7, RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name: dnsmessage.MustNewName(name + "."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET,
		}},
	}
	packed, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("udp", server)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(packed); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("resolving %s: %v", name, err)
	}
	var resp dnsmessage.Message
	if err := resp.Unpack(buf[:n]); err != nil {
		t.Fatal(err)
	}
	var ips []string
	for _, a := range resp.Answers {
		if r, ok := a.Body.(*dnsmessage.AResource); ok {
			ips = append(ips, net.IP(r.A[:]).String())
		}
	}
	return resp.Header.RCode, ips
}

func TestANoNetworkSandboxResolvesOnlyThePackageCache(t *testing.T) {
	const cacheIP, internetIP = "172.17.0.1", "203.0.113.9"
	upstream, asked := stubUpstream(t, internetIP)
	server, err := pkgcache.NewDNSServer(pkgcache.DNSConfig{
		ListenAddr:  "127.0.0.1:0",
		InterceptIP: net.ParseIP(cacheIP),
		UpstreamDNS: []string{upstream},
		Log:         t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Shutdown(context.Background()) })
	addr := server.Addr().String()

	arbitrary := []string{"exfil.attacker.example", "github.com", "api.openai.com", "pypi.org.attacker.example"}

	// Not vacuous: an open sandbox on the same resolver reaches the
	// internet's answer.
	if rcode, ips := resolveA(t, addr, arbitrary[0]); rcode != dnsmessage.RCodeSuccess || len(ips) != 1 || ips[0] != internetIP {
		t.Fatalf("an unrestricted client got %v %v, want the upstream's answer", rcode, ips)
	}

	server.RestrictClient(net.ParseIP("127.0.0.1"))
	before := asked.Load()
	for _, name := range arbitrary {
		if rcode, ips := resolveA(t, addr, name); rcode != dnsmessage.RCodeNameError || len(ips) != 0 {
			t.Errorf("a no-network sandbox resolving %s got %v %v, want NXDOMAIN", name, rcode, ips)
		}
	}
	if after := asked.Load(); after != before {
		t.Errorf("%d of a no-network sandbox's queries were forwarded upstream: "+
			"a resolver that forwards is a channel to any nameserver on the internet", after-before)
	}
	for _, name := range []string{"pypi.org", "registry.npmjs.org", "proxy.golang.org", "deb.debian.org"} {
		if rcode, ips := resolveA(t, addr, name); rcode != dnsmessage.RCodeSuccess || len(ips) != 1 || ips[0] != cacheIP {
			t.Errorf("a no-network sandbox resolving %s got %v %v, want the package cache at %s", name, rcode, ips, cacheIP)
		}
	}
}

// A sandbox backend with nothing to withhold -- the host-directory one
// runs on the daemon's own network -- refuses a no-network task rather
// than handing it the host's network.
func TestABackendThatCannotIsolateRefusesANoNetworkSandbox(t *testing.T) {
	sandboxes := orchestrator.NewHostSandboxes(t.TempDir())
	sb, err := sandboxes.Acquire(context.Background(), "security-egress", orchestrator.Shape{NetworkAccess: model.NetworkNone})
	if err == nil {
		_ = sb.Release(context.Background())
		t.Fatal("a host-directory sandbox was handed to a task asking for no network")
	}
}

// Every network access an agent may ask for is one grain can build: an
// unknown word must never become, by default, an open sandbox. The
// schema is only half of it: propose_task's handler refuses any other
// value, and orchestrator's relay drops one and notes it on the proposal.
func TestProposeTaskOffersOnlyNetworkAccessesGrainCanBuild(t *testing.T) {
	var offered []any
	for _, tool := range mcp.NewMockTools(&mcp.MockSink{}, nil) {
		if tool.Name != "propose_task" {
			continue
		}
		props, _ := tool.InputSchema["properties"].(map[string]any)
		network, _ := props["network_access"].(map[string]any)
		offered, _ = network["enum"].([]any)
	}
	if len(offered) == 0 {
		t.Fatal("propose_task offers no network_access values; this test has lost what it checks")
	}
	var unbuildable []string
	for _, v := range offered {
		s, _ := v.(string)
		if !model.ValidNetworkAccess(model.NetworkAccess(s)) {
			unbuildable = append(unbuildable, s)
		}
	}
	if len(unbuildable) > 0 {
		t.Fatalf("propose_task offers network_access %v, which model.ValidNetworkAccess refuses", unbuildable)
	}
}
