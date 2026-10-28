package gitproxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// postHeaders is what git sends with a POST for action, authenticated as
// token ("" for no Authorization at all).
func postHeaders(action, token string) map[string]string {
	h := map[string]string{"User-Agent": "git/2.39.2", "Accept": "application/x-" + action + "-result"}
	if token != "" {
		h["Authorization"] = basicAuthHeader(token)
	}
	return h
}

// countingReader is an endless body that counts how much of it was read.
type countingReader struct{ n atomic.Int64 }

func (c *countingReader) Read(p []byte) (int, error) {
	c.n.Add(int64(len(p)))
	return len(p), nil
}

// A request that is refused from its path and headers alone never has
// its body read: not running git, no valid token, or not in scope.
func TestHandleRequestRefusesWithoutReadingTheBody(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		headers map[string]string
		status  int
	}{
		{"non-git user agent", "/owner/repo.git/git-upload-pack",
			map[string]string{"User-Agent": "curl/8", "Authorization": basicAuthHeader("tok0")}, 404},
		{"no token", "/owner/repo.git/git-upload-pack", postHeaders("git-upload-pack", ""), 401},
		{"bad token", "/owner/repo.git/git-upload-pack", postHeaders("git-upload-pack", "bogus"), 401},
		{"not in scope", "/other/repo.git/git-receive-pack", postHeaders("git-receive-pack", "tok0"), 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, forwarder, _ := newTestProxy(t)
			body := &countingReader{}
			resp := p.HandleRequest(context.Background(), "POST", tc.path, "", tc.headers, body)
			if resp.Status != tc.status {
				t.Errorf("status = %d, want %d", resp.Status, tc.status)
			}
			if n := body.n.Load(); n != 0 {
				t.Errorf("read %d bytes of the body before refusing", n)
			}
			if len(forwarder.Calls) != 0 {
				t.Error("expected no forward call")
			}
		})
	}
}

func TestHandleRequestRefusesAnOversizedBodyWith413(t *testing.T) {
	p, forwarder, audit := newTestProxy(t)
	p.MaxFetchBodyBytes = 1024
	headers := postHeaders("git-upload-pack", "tok0")

	body := &countingReader{}
	resp := p.HandleRequest(context.Background(), "POST", "/owner/repo.git/git-upload-pack", "", headers, body)
	if resp.Status != 413 {
		t.Fatalf("status = %d, want 413", resp.Status)
	}
	// LimitReader hands ReadAll at most limit+1 bytes, whatever buffer it
	// asks with.
	if n := body.n.Load(); n > 1025 {
		t.Errorf("read %d bytes of a body limited to 1024", n)
	}
	if len(forwarder.Calls) != 0 {
		t.Error("expected no forward call")
	}
	if len(audit.Entries) != 1 || !strings.Contains(audit.Entries[0].Outcome, "request body over 1024 bytes") {
		t.Errorf("audit = %+v", audit.Entries)
	}

	// Exactly at the limit is fine.
	resp = p.Handle(context.Background(), "POST", "/owner/repo.git/git-upload-pack", "", headers, bytes.Repeat([]byte("x"), 1024))
	if resp.Status != 200 || len(forwarder.Calls) != 1 || len(forwarder.Calls[0].Body) != 1024 {
		t.Errorf("status = %d, calls = %d", resp.Status, len(forwarder.Calls))
	}
}

func TestPushGetsItsOwnBodyLimit(t *testing.T) {
	p, forwarder, _ := newTestProxy(t)
	p.MaxFetchBodyBytes = 16
	p.MaxPushBodyBytes = 64
	body := bytes.Repeat([]byte("x"), 32)

	if resp := p.Handle(context.Background(), "POST", "/owner/repo.git/git-upload-pack", "", postHeaders("git-upload-pack", "tok0"), body); resp.Status != 413 {
		t.Errorf("upload-pack status = %d, want 413", resp.Status)
	}
	if resp := p.Handle(context.Background(), "POST", "/owner/repo.git/git-receive-pack", "", postHeaders("git-receive-pack", "tok0"), body); resp.Status != 200 {
		t.Errorf("receive-pack status = %d, want 200", resp.Status)
	}
	if len(forwarder.Calls) != 1 {
		t.Errorf("forward calls = %d, want 1", len(forwarder.Calls))
	}
}

func TestMaxBodyBytesDefaults(t *testing.T) {
	p := &GitProxy{}
	if got := p.maxBodyBytes("git-receive-pack"); got != DefaultMaxPushBodyBytes {
		t.Errorf("push limit = %d", got)
	}
	for _, action := range []string{"git-upload-pack", "info/refs"} {
		if got := p.maxBodyBytes(action); got != DefaultMaxFetchBodyBytes {
			t.Errorf("%s limit = %d", action, got)
		}
	}
}

// Over real HTTP: an unauthenticated POST declaring a gigabyte body is
// answered without the handler reading any of it. What the client makes
// of the connection being closed under it is net/http's business, so
// only the server side is asserted on.
func TestHandlerRefusesAnUnauthenticatedOversizedPostWithoutReadingIt(t *testing.T) {
	proxy := &GitProxy{
		Authorizer:      alwaysAuthorize{},
		Credentials:     mustEmptyCredentialSet(t),
		TeamCredentials: ladderConnections{creds: mustEmptyCredentialSet(t)},
		Tokens:          mustAlwaysAuthTokens(t),
		Forwarder:       &FakeForwarder{Response: UpstreamResponse{Status: 200}},
	}
	var read atomic.Int64
	handler := NewHandler(proxy)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = countingBody{r.Body, &read}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	const size = 1 << 30
	req := newGitRequest(t, "POST", server.URL+"/acme/widgets.git/git-receive-pack", io.LimitReader(zeros{}, size))
	req.ContentLength = size
	req.Header.Set("Accept", "application/x-git-receive-pack-result")
	req.Header.Set("Authorization", basicAuthHeader("not-a-token"))
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	}
	if n := read.Load(); n != 0 {
		t.Errorf("handler read %d bytes of an unauthenticated body", n)
	}
}

func TestHandlerAnswersAnOversizedUploadPackWith413(t *testing.T) {
	forwarder := &FakeForwarder{Response: UpstreamResponse{Status: 200, Body: []byte("ok")}}
	proxy := &GitProxy{
		Authorizer:        alwaysAuthorize{},
		Credentials:       mustEmptyCredentialSet(t),
		TeamCredentials:   ladderConnections{creds: mustEmptyCredentialSet(t)},
		Tokens:            mustAlwaysAuthTokens(t),
		Forwarder:         forwarder,
		MaxFetchBodyBytes: 1024,
	}
	server := httptest.NewServer(NewHandler(proxy))
	defer server.Close()

	req := newGitRequest(t, "POST", server.URL+"/acme/widgets.git/git-upload-pack", bytes.NewReader(make([]byte, 4096)))
	req.Header.Set("Accept", "application/x-git-upload-pack-result")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
	if len(forwarder.Calls) != 0 {
		t.Error("expected no forward call")
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type countingBody struct {
	io.ReadCloser
	n *atomic.Int64
}

func (c countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n.Add(int64(n))
	return n, err
}
