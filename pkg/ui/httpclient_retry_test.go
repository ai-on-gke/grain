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

package ui

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

var fastRetry = RetryPolicy{Budget: 2 * time.Second, Initial: 5 * time.Millisecond, Max: 20 * time.Millisecond}

func TestRetryableErrorClassifiesWhatAReachedDaemonMayHaveDone(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	reset := &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	timeout := &net.OpError{Op: "read", Net: "tcp", Err: timeoutErr{}}
	for _, tc := range []struct {
		name   string
		method string
		err    error
		want   bool
	}{
		{"refused GET", http.MethodGet, refused, true},
		{"refused POST: never reached grain", http.MethodPost, refused, true},
		{"reset GET", http.MethodGet, reset, true},
		{"reset POST: may have been handled", http.MethodPost, reset, false},
		{"EOF GET", http.MethodGet, errors.New("Get \"x\": EOF"), true},
		{"EOF POST", http.MethodPost, errors.New("Post \"x\": EOF"), false},
		{"timeout GET: a hang is not a restart", http.MethodGet, timeout, false},
		{"cancelled", http.MethodGet, context.Canceled, false},
		{"something else", http.MethodGet, errors.New("tls: bad certificate"), false},
	} {
		if got := retryableError(tc.method, tc.err); got != tc.want {
			t.Errorf("%s: retryableError = %v, want %v", tc.name, got, tc.want)
		}
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestRetryableResponseIsAGatewayNotGrain(t *testing.T) {
	resp := func(status int, contentType string) *http.Response {
		h := http.Header{}
		if contentType != "" {
			h.Set("Content-Type", contentType)
		}
		return &http.Response{StatusCode: status, Header: h}
	}
	for _, tc := range []struct {
		name   string
		method string
		resp   *http.Response
		want   bool
	}{
		{"gateway 502 GET", http.MethodGet, resp(502, "text/html"), true},
		{"gateway 503 GET", http.MethodGet, resp(503, ""), true},
		{"gateway 504 GET", http.MethodGet, resp(504, "text/plain"), true},
		{"gateway 502 POST: a dropped tunnel may have been applied", http.MethodPost, resp(502, "text/html"), false},
		{"gateway 503 POST: a proxy's upstream reset may have been applied", http.MethodPost, resp(503, ""), false},
		{"gateway 504 POST: grain may have acted", http.MethodPost, resp(504, "text/plain"), false},
		{"grain's own 503", http.MethodGet, resp(503, "application/json"), false},
		{"500", http.MethodGet, resp(500, "text/plain"), false},
		{"200", http.MethodGet, resp(200, "application/json"), false},
	} {
		if got := retryableResponse(tc.method, tc.resp); got != tc.want {
			t.Errorf("%s: retryableResponse = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A POST to a daemon that comes up part way through the call succeeds:
// the refused attempts never reached it, so sending again is safe.
func TestHTTPClientWaitsOutADaemonThatIsNotListeningYet(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	})}
	go func() {
		time.Sleep(150 * time.Millisecond)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Errorf("relisten: %v", err)
			return
		}
		srv.Serve(ln)
	}()
	t.Cleanup(func() { srv.Close() })

	c := NewHTTPClient("http://" + addr)
	c.RetryPolicy = &fastRetry
	if err := c.do(context.Background(), http.MethodPost, "/api/anything", map[string]string{"a": "b"}, nil); err != nil {
		t.Fatalf("call across the gap failed: %v", err)
	}
}

// Once the budget is spent the error says the daemon is restarting, and
// still unwraps to what went wrong underneath.
func TestHTTPClientGivesUpWithASentenceNamingTheRestart(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	c := NewHTTPClient("http://" + addr)
	c.RetryPolicy = &RetryPolicy{Budget: 100 * time.Millisecond, Initial: 10 * time.Millisecond, Max: 20 * time.Millisecond}
	started := time.Now()
	err = c.do(context.Background(), http.MethodGet, "/api/sandboxes", nil, nil)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("gave up after %s, want about the 100ms budget", elapsed)
	}
	var unavailable *DaemonUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v (%T), want a DaemonUnavailableError", err, err)
	}
	if unavailable.Attempts < 2 {
		t.Errorf("Attempts = %d, want more than one", unavailable.Attempts)
	}
	if !strings.Contains(err.Error(), "restarting") {
		t.Errorf("error %q does not name the restart", err)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Errorf("error %q does not unwrap to ECONNREFUSED", err)
	}
}

// Grain's own error, even a 503, is returned the first time.
func TestHTTPClientDoesNotRetryGrainsOwnAnswer(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		writeError(w, http.StatusServiceUnavailable, errors.New("database is not available"))
	}))
	t.Cleanup(srv.Close)

	c := NewHTTPClient(srv.URL)
	c.RetryPolicy = &fastRetry
	err := c.do(context.Background(), http.MethodGet, "/api/x", nil, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("attempts = %d, want 1", n)
	}
}

// A non-idempotent call cut off mid-flight is not sent twice.
func TestHTTPClientDoesNotResendAPOSTThatMayHaveLanded(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	t.Cleanup(srv.Close)

	c := NewHTTPClient(srv.URL)
	c.RetryPolicy = &fastRetry
	if err := c.do(context.Background(), http.MethodPost, "/api/tasks", map[string]string{}, nil); err == nil {
		t.Fatal("want an error")
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("attempts = %d, want 1", n)
	}
}

// The caller's context still bounds the wait.
func TestHTTPClientRetryStopsWithTheContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	c := NewHTTPClient("http://" + addr)
	c.RetryPolicy = &RetryPolicy{Budget: time.Minute, Initial: 10 * time.Millisecond, Max: 20 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = c.do(ctx, http.MethodGet, "/api/sandboxes", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context's deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("took %s, want about the context's 100ms", elapsed)
	}
}
