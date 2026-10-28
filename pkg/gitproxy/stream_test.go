package gitproxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// streamingProxy serves a GitProxy whose RealForwarder points at upstream,
// over NewHandler, the way the controller runs it.
func streamingProxy(t *testing.T, upstream http.Handler) *httptest.Server {
	t.Helper()
	origin := httptest.NewServer(upstream)
	t.Cleanup(origin.Close)
	proxy := &GitProxy{
		Authorizer:      alwaysAuthorize{},
		Credentials:     mustEmptyCredentialSet(t),
		TeamCredentials: ladderConnections{creds: mustEmptyCredentialSet(t)},
		Tokens:          mustAlwaysAuthTokens(t),
		Forwarder:       &RealForwarder{Host: origin.Listener.Addr().String(), UseTLS: false},
	}
	server := httptest.NewServer(NewHandler(proxy))
	t.Cleanup(server.Close)
	return server
}

// The upstream sends the first part of a large response and then waits
// for the client to have read it before sending the rest. A proxy that
// held the response until the upstream finished would never deliver
// those first bytes, and the two would wait on each other until the
// deadline below.
func TestALargeUpstreamResponseReachesTheClientBeforeTheUpstreamFinishes(t *testing.T) {
	const firstPart = 256 << 10
	const total = 16 << 20
	payload := bytes.Repeat([]byte("0123456789abcdef"), total/16)
	clientHasFirstPart := make(chan struct{})
	upstreamFinished := make(chan struct{})
	server := streamingProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamFinished)
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		w.WriteHeader(200)
		w.Write(payload[:firstPart])
		w.(http.Flusher).Flush()
		select {
		case <-clientHasFirstPart:
		case <-r.Context().Done():
			return
		case <-time.After(20 * time.Second):
			// Long enough to be sure: the client below has given up by
			// now, on a proxy that is holding the response back.
			return
		}
		w.Write(payload[firstPart:])
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := newGitRequest(t, "GET", server.URL+"/acme/widgets.git/info/refs?service=git-upload-pack", nil)
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("no response while the upstream was still writing: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-git-upload-pack-advertisement" {
		t.Fatalf("status = %d, Content-Type = %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	first := make([]byte, firstPart)
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatalf("reading the first %d bytes while the upstream was still writing: %v", firstPart, err)
	}
	select {
	case <-upstreamFinished:
		t.Fatal("the upstream finished before the client was let through, so nothing here was streamed")
	default:
	}
	close(clientHasFirstPart)

	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := append(first, rest...); !bytes.Equal(got, payload) {
		t.Fatalf("client got %d bytes, want the upstream's %d unchanged", len(got), len(payload))
	}
}

// countingStream is an upstream body that never ends, and remembers how
// much of it was read and whether it was closed.
type countingStream struct {
	read   atomic.Int64
	closed atomic.Bool
}

func (s *countingStream) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	s.read.Add(int64(len(p)))
	return len(p), nil
}

func (s *countingStream) Close() error {
	s.closed.Store(true)
	return nil
}

// A refusal is the one upstream answer the proxy reads, to quote it --
// and an upstream that answers 404 with a body that never ends must not
// be able to make it read without bound.
func TestARefusalsBodyIsReadOnlyUpToTheLimit(t *testing.T) {
	stream := &countingStream{}
	proxy := &GitProxy{
		Authorizer:      alwaysAuthorize{},
		Credentials:     mustEmptyCredentialSet(t),
		TeamCredentials: ladderConnections{creds: mustEmptyCredentialSet(t)},
		Tokens:          mustAlwaysAuthTokens(t),
		Forwarder:       &FakeForwarder{Response: UpstreamResponse{Status: 404, Stream: stream}},
	}
	resp := proxy.HandleRequest(context.Background(), "GET", "/acme/widgets.git/info/refs", "service=git-upload-pack",
		map[string]string{"User-Agent": "git/2.39.2", "Accept": "*/*", "Authorization": basicAuthHeader("tok0")}, nil)
	if resp.Status != 404 || resp.Stream != nil {
		t.Fatalf("status = %d, stream = %v; want the proxy's own 404", resp.Status, resp.Stream)
	}
	if n := stream.read.Load(); n > maxRefusalBodyBytes {
		t.Errorf("read %d bytes of the refusal, want at most %d", n, maxRefusalBodyBytes)
	}
	if !stream.closed.Load() {
		t.Error("the refused upstream body was never closed")
	}
}

// A client that goes away part way through a response is the end of it:
// the upstream body is closed rather than left open until forwardTimeout.
func TestAnUpstreamBodyIsClosedWhenTheClientGoesAway(t *testing.T) {
	stream := &countingStream{}
	proxy := &GitProxy{
		Authorizer:      alwaysAuthorize{},
		Credentials:     mustEmptyCredentialSet(t),
		TeamCredentials: ladderConnections{creds: mustEmptyCredentialSet(t)},
		Tokens:          mustAlwaysAuthTokens(t),
		Forwarder:       &FakeForwarder{Response: UpstreamResponse{Status: 200, Stream: stream}},
	}
	server := httptest.NewServer(NewHandler(proxy))
	defer server.Close()

	resp, err := http.DefaultClient.Do(newGitRequest(t, "GET", server.URL+"/acme/widgets.git/info/refs?service=git-upload-pack", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 64<<10)); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	deadline := time.Now().Add(10 * time.Second)
	for !stream.closed.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("upstream body still open after the client left (%d bytes read)", stream.read.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
