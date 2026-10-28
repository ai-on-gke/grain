package github

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// retryTransport is a transport aimed at a test server whose backoff is
// observed rather than waited out -- every retry test wants both, and
// waiting 250ms per case would be the slowest thing in this package.
func retryTransport(t *testing.T, server *httptest.Server) (*RealTransport, *[]time.Duration) {
	t.Helper()
	var (
		mu    sync.Mutex
		slept []time.Duration
	)
	transport := &RealTransport{
		Host:   strings.TrimPrefix(server.URL, "http://"),
		UseTLS: false,
		sleep: func(d time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			slept = append(slept, d)
		},
	}
	return transport, &slept
}

// The case this file exists for: GitHub answers a read with a 502, and
// the caller -- which may be EnsurePullRequest, the finish path or an
// MCP tool, none of which has a poll cycle behind it -- gets the answer
// rather than the flake.
func TestRealTransportRetriesAGetThroughAServerError(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(502)
			w.Write([]byte(`{"message":"Server Error"}`))
			return
		}
		w.Write([]byte(`{"number":7}`))
	}))
	defer server.Close()

	transport, slept := retryTransport(t, server)
	resp, err := transport.Request("GET", "/repos/acme/widgets/pulls/7", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 {
		t.Fatalf("status %d, want the retry's 200 -- the 502 reached the caller", resp.Status)
	}
	if string(resp.Body) != `{"number":7}` {
		t.Errorf("body = %q, want the second attempt's", resp.Body)
	}
	if calls != 2 {
		t.Errorf("the server saw %d calls, want 2", calls)
	}
	if len(*slept) != 1 || (*slept)[0] != retryBackoff {
		t.Errorf("backoff = %v, want one pause of %v", *slept, retryBackoff)
	}
	if got := transport.Stats().Retried; got != 1 {
		t.Errorf("Stats().Retried = %d, want 1 -- the retry is invisible to an operator", got)
	}
	// Both attempts were real requests and both are counted, because both
	// were spent: the retry is not free and the stats must not say it was.
	if got := transport.Stats().Sent; got != 2 {
		t.Errorf("Stats().Sent = %d, want 2", got)
	}
}

// The other half, and the one that matters more: a write is never sent
// twice. A 502 on a merge or an issue creation may well mean the write
// happened, so asking again could merge a pull request twice or file two
// copies of the same issue.
func TestRealTransportNeverRetriesAWrite(t *testing.T) {
	for _, method := range []string{"POST", "PATCH", "PUT", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(502)
				w.Write([]byte(`{"message":"Server Error"}`))
			}))
			defer server.Close()

			transport, slept := retryTransport(t, server)
			resp, err := transport.Request(method, "/repos/acme/widgets/merges", nil, []byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if resp.Status != 502 {
				t.Fatalf("status %d, want the 502 handed straight back", resp.Status)
			}
			if calls != 1 {
				t.Errorf("the server saw %d calls, want 1 -- a %s was retried", calls, method)
			}
			if len(*slept) != 0 {
				t.Errorf("waited %v before a retry it must not make", *slept)
			}
			if got := transport.Stats().Retried; got != 0 {
				t.Errorf("Stats().Retried = %d, want 0", got)
			}
		})
	}
}

// A 5xx that is still there on the second attempt is the caller's, and
// arrives as the same error it always did -- one retry, not a ladder, so
// an outage costs twice the calls and not ten times.
func TestRealTransportGivesUpAfterOneRetry(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(503)
		w.Write([]byte("upstream connect error"))
	}))
	defer server.Close()

	transport, _ := retryTransport(t, server)
	resp, err := transport.Request("GET", "/repos/acme/widgets/pulls/7", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 503 {
		t.Fatalf("status %d, want the 503", resp.Status)
	}
	if calls != maxRequestAttempts {
		t.Errorf("the server saw %d calls, want %d", calls, maxRequestAttempts)
	}
	// And it is still what every caller's predicate says it is.
	if !IsServerError(&Error{Status: resp.Status, Body: resp.Body}) {
		t.Error("the persistent 5xx stopped reading as a server error")
	}
}

// Only a 5xx. A refusal GitHub made deliberately -- a 404, a 403, a 422
// -- is an answer, and asking the same question again gets the same
// answer at twice the price.
func TestRealTransportDoesNotRetryAnAnswer(t *testing.T) {
	for _, status := range []int{200, 304, 401, 403, 404, 409, 422} {
		var calls int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(status)
		}))

		transport, _ := retryTransport(t, server)
		if _, err := transport.Request("GET", "/repos/acme/widgets/pulls/7", nil, nil); err != nil {
			server.Close()
			t.Fatalf("%d: %v", status, err)
		}
		if calls != 1 {
			t.Errorf("a %d was asked %d times, want 1", status, calls)
		}
		server.Close()
	}
}

// The retry goes through the whole of Request's preamble, gate included,
// so a throttle that arrived between the two attempts stops the second
// one rather than spending a call GitHub has already said it will
// refuse. Simulated by shutting the gate during the backoff, which is
// where a concurrent call meeting a secondary limit would do it.
func TestRealTransportDoesNotRetryThroughAShutGate(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(502)
	}))
	defer server.Close()

	transport := &RealTransport{Host: strings.TrimPrefix(server.URL, "http://"), UseTLS: false}
	transport.init()
	transport.sleep = func(time.Duration) {
		now := time.Now()
		transport.gates.forCredential("").shut(now, now.Add(time.Minute), "a secondary rate limit, from another call")
	}
	_, err := transport.Request("GET", "/repos/acme/widgets/pulls/7", nil, nil)
	if err == nil {
		t.Fatal("the retry was sent into a closed gate")
	}
	var refused *RateLimitError
	if !errors.As(err, &refused) {
		t.Fatalf("want a *RateLimitError, got %T: %v", err, err)
	}
	if calls != 1 {
		t.Errorf("the server saw %d calls, want 1", calls)
	}
}

// A retried GET is still a conditional one: the refactor that put the
// round trip behind a retry loop must not have cost the ETag cache the
// answer it is there to store.
func TestRetriedGetStillCaches(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			w.WriteHeader(502)
		case 2:
			w.Header().Set("ETag", `"v1"`)
			w.Write([]byte(`{"number":7}`))
		default:
			if r.Header.Get("If-None-Match") != `"v1"` {
				t.Errorf("call %d sent If-None-Match %q, want the stored validator",
					calls, r.Header.Get("If-None-Match"))
			}
			w.WriteHeader(http.StatusNotModified)
		}
	}))
	defer server.Close()

	transport, _ := retryTransport(t, server)
	transport.Conditional = true
	if _, err := transport.Request("GET", "/repos/acme/widgets/pulls/7", nil, nil); err != nil {
		t.Fatal(err)
	}
	resp, err := transport.Request("GET", "/repos/acme/widgets/pulls/7", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Body) != `{"number":7}` {
		t.Errorf("body = %q, want the cached entity the 304 stands for", resp.Body)
	}
	if got := transport.Stats().Revalidated; got != 1 {
		t.Errorf("Stats().Revalidated = %d, want 1", got)
	}
}

// idempotentMethod is the whole of the "by construction" claim, so it is
// worth a table of its own: a method not named here is a write, whatever
// it looks like.
func TestIdempotentMethod(t *testing.T) {
	cases := []struct {
		method string
		want   bool
	}{
		{"GET", true},
		{"get", true},
		{"HEAD", true},
		{"POST", false},
		{"PATCH", false},
		// Idempotent in HTTP's sense and not in the sense that matters:
		// GitHub answers the second PUT /merge 409 and the second DELETE
		// of a branch 422, both of which would report an operation that
		// succeeded as one that failed.
		{"PUT", false},
		{"DELETE", false},
		{"", false},
	}
	for _, c := range cases {
		if got := idempotentMethod(c.method); got != c.want {
			t.Errorf("idempotentMethod(%q) = %v, want %v", c.method, got, c.want)
		}
	}
}

func TestServerErrorStatus(t *testing.T) {
	for _, status := range []int{500, 502, 503, 504, 599} {
		if !serverErrorStatus(status) {
			t.Errorf("serverErrorStatus(%d) = false", status)
		}
	}
	for _, status := range []int{0, 200, 304, 404, 429, 499, 600} {
		if serverErrorStatus(status) {
			t.Errorf("serverErrorStatus(%d) = true", status)
		}
	}
}
