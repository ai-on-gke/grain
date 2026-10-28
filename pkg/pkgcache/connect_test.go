package pkgcache

// The CONNECT half, tested the way a client meets it: a real listener, a
// real CONNECT on the wire, and for the tunnel a real TLS fetch through
// it. A hijack is the one thing an httptest.ResponseRecorder cannot do,
// which is also the reason none of these use one.

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
)

// TestCONNECTTunnelsToAnArchiveItServes is the whole point of connect.go:
// a client that knows nothing about this service except that it is its
// proxy fetches an https:// URL from an archive the cache serves, and
// gets the archive's own answer over the archive's own certificate.
//
// This is what every `docker build` doing `apt-get install` from an
// https:// source line does, and what got 405 Method Not Allowed until
// 2026-09-18.
func TestCONNECTTunnelsToAnArchiveItServes(t *testing.T) {
	// The archive, addressed as example.com because that is who httptest's
	// own certificate is issued to, which saves this test a CA.
	archive := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			t.Errorf("the tunnelled request did not arrive over TLS")
		}
		fmt.Fprint(w, "the archive answered for itself")
	}))
	defer archive.Close()

	// The cache, with example.com as the one archive it serves and its
	// dialer pointed at where that archive really listens -- the same
	// override every other test in this package uses for a fetch, which
	// a tunnel deliberately shares.
	s, _ := newTestServer(t, Config{
		AptHosts: []string{"example.com"},
		Client: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if addr != "example.com:443" {
					return nil, fmt.Errorf("the tunnel dialled %s, not the archive on 443", addr)
				}
				return (&net.Dialer{}).DialContext(ctx, "tcp", archive.Listener.Addr().String())
			},
		}},
	})
	cache := httptest.NewServer(s)
	defer cache.Close()

	pool := x509.NewCertPool()
	pool.AddCert(archive.Certificate())
	client := &http.Client{Transport: &http.Transport{
		Proxy:           proxyTo(t, cache.URL),
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}

	resp, err := client.Get("https://example.com/debian/pool/main/h/hello/hello_2.10-3_amd64.deb")
	if err != nil {
		t.Fatalf("an https fetch through the cache as a proxy failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "the archive answered for itself" {
		t.Fatalf("the tunnelled fetch answered %d %q", resp.StatusCode, body)
	}
	// Nothing about a tunnel is cached, and the cache does not claim
	// otherwise: what came back is the archive's response, headers and all.
	if got := resp.Header.Get(cacheStatusHeader); got != "" {
		t.Errorf("a tunnelled response carries %s: %q, but the cache never saw its bytes", cacheStatusHeader, got)
	}
}

// TestCONNECTCarriesWhatWasSentWithIt is the two details of a tunnel that
// are easy to get wrong and invisible when they are:
//
//   - a client may send its first bytes along with the CONNECT rather than
//     waiting for the 200, so they arrive in the buffer the hijack hands
//     over and not on the socket. A tunnel that read the socket would lose
//     a TLS ClientHello and hang.
//   - a client that has finished sending half-closes and goes on reading.
//     A tunnel that closed both directions when the first one ended would
//     truncate the download it was asked for.
//
// A plain echo rather than TLS, because these are about the bytes.
func TestCONNECTCarriesWhatWasSentWithIt(t *testing.T) {
	archive, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening as the archive: %v", err)
	}
	defer archive.Close()
	go func() {
		conn, err := archive.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Everything the client sent, back again -- and then a word the
		// client can only read if the tunnel outlived its own last write.
		io.Copy(conn, conn)
		conn.Write([]byte("and the last word"))
	}()

	s, _ := newTestServer(t, Config{
		AptHosts: []string{"example.com"},
		Client: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", archive.Addr().String())
			},
		}},
	})
	cache := httptest.NewServer(s)
	defer cache.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(cache.URL, "http://"))
	if err != nil {
		t.Fatalf("dialling the cache: %v", err)
	}
	defer conn.Close()
	// The request and the first bytes of the conversation in one write, the
	// way a client that does not wait for the 200 sends them.
	if _, err := conn.Write([]byte("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\nfirst")); err != nil {
		t.Fatalf("sending the CONNECT: %v", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("reading the answer to the CONNECT: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT to an archive the cache serves answered %s, want 200", resp.Status)
	}
	if _, err := conn.Write([]byte("second")); err != nil {
		t.Fatalf("writing through the tunnel: %v", err)
	}
	halfClose(conn)

	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading the tunnel out: %v", err)
	}
	if got := string(rest); got != "firstsecondand the last word" {
		t.Errorf("the tunnel carried %q, want the bytes sent with the CONNECT, the bytes sent after it, and what the "+
			"archive said once the client had stopped writing", got)
	}
}

// TestCONNECTToAnywhereElseIsRefused: the allowlist is what keeps this
// service from being the general-purpose egress a model.NetworkNone
// sandbox had taken away from it, and it is enforced over CONNECT exactly
// as it is over an absolute-form GET.
func TestCONNECTToAnywhereElseIsRefused(t *testing.T) {
	dialled := make(chan string, 1)
	s, _ := newTestServer(t, Config{
		Client: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dialled <- addr
				return nil, errors.New("this test's cache has no upstream")
			},
		}},
	})
	cache := httptest.NewServer(s)
	defer cache.Close()

	for _, tc := range []struct {
		name, target string
		want         []string
	}{
		{
			name:   "a host it does not serve",
			target: "www.example.com:443",
			want:   []string{"does not fetch from www.example.com", "http_proxy", "deb.debian.org"},
		},
		{
			// A served host on another port is still refused: a tunnel is
			// opaque, so the port is all that says what is travelling over it.
			name:   "an archive it serves, on another port",
			target: "deb.debian.org:22",
			want:   []string{"port 443", "deb.debian.org:22"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := connectThrough(t, cache.URL, tc.target)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("CONNECT %s answered %s, want 403 -- the status line is all a refused CONNECT tells its client, "+
					"so it has to be the one that means \"not this host\"", tc.target, resp.Status)
			}
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("the refusal does not mention %q:\n%s", want, body)
				}
			}
		})
	}

	select {
	case addr := <-dialled:
		t.Fatalf("a refused CONNECT still dialled %s", addr)
	default:
	}
}

// TestCONNECTToAnArchiveItCannotReach: the tunnel's dial happens before
// the connection is taken over, so an unreachable archive is a 502 with a
// sentence in it rather than a connection dropped without a word.
func TestCONNECTToAnArchiveItCannotReach(t *testing.T) {
	s, _ := newTestServer(t, Config{
		Client: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return nil, errors.New("no route to the archive")
			},
		}},
	})
	cache := httptest.NewServer(s)
	defer cache.Close()

	resp, body := connectThrough(t, cache.URL, "deb.debian.org:443")
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("CONNECT to an unreachable archive answered %s, want 502", resp.Status)
	}
	if !strings.Contains(body, "deb.debian.org") || !strings.Contains(body, "no route to the archive") {
		t.Errorf("the 502 does not say what could not be reached or why:\n%s", body)
	}
}

// TestConnectHostsIsEveryHostTheCacheFetchesFrom: the tunnel's allowlist
// is not a list of its own -- it is the union of what the ecosystem
// halves already fetch from, so an archive added to one of those lists
// cannot end up servable over http and refused over https.
func TestConnectHostsIsEveryHostTheCacheFetchesFrom(t *testing.T) {
	s, _ := newTestServer(t, Config{})
	hosts := s.connectHosts()

	want := append([]string{}, DefaultAptHosts...)
	want = append(want, DefaultPyPIFileHosts...)
	want = append(want, DefaultGoSumDBHosts...)
	want = append(want, DefaultPlaywrightHosts...)
	want = append(want, "registry.npmjs.org", "proxy.golang.org", "registry-1.docker.io", "ghcr.io", "gcr.io",
		"docker.io", "auth.docker.io", "pypi.org")
	for _, host := range want {
		if !hostAllowed(host, hosts) {
			t.Errorf("connectHosts does not cover %s, which the cache fetches from: %v", host, hosts)
		}
	}
	if !sort.StringsAreSorted(hosts) {
		t.Errorf("connectHosts is not sorted, so two identical refusals would print different lists: %v", hosts)
	}
	seen := map[string]bool{}
	for _, host := range hosts {
		if seen[host] {
			t.Errorf("connectHosts lists %s twice: %v", host, hosts)
		}
		seen[host] = true
	}
	// An override is honoured here too: a deployment with an internal
	// mirror names it in Config, and that is the host its tunnels go to.
	mirrored, _ := newTestServer(t, Config{AptHosts: []string{"mirror.internal"}})
	if hostAllowed("deb.debian.org", mirrored.connectHosts()) {
		t.Errorf("a cache configured with one archive still tunnels to Debian's: %v", mirrored.connectHosts())
	}
}

// proxyTo is a transport's Proxy function pointing every request at the
// cache, which is what a container holding http_proxy=... has.
func proxyTo(t *testing.T, cacheURL string) func(*http.Request) (*url.URL, error) {
	t.Helper()
	u, err := url.Parse(cacheURL)
	if err != nil {
		t.Fatalf("parsing the cache's address: %v", err)
	}
	return func(*http.Request) (*url.URL, error) { return u, nil }
}

// connectThrough makes one CONNECT against the cache by hand and returns
// what the client would see. By hand because the point is the answer to a
// CONNECT: a client's own proxy support discards everything but the
// status line, and these refusals are read in a package manager's output.
func connectThrough(t *testing.T, cacheURL, target string) (*http.Response, string) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(cacheURL, "http://"))
	if err != nil {
		t.Fatalf("dialling the cache: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		t.Fatalf("sending the CONNECT: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("reading the answer to the CONNECT: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the refusal: %v", err)
	}
	return resp, string(body)
}
