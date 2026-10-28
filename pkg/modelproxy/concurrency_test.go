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

package modelproxy

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// heldUpstream is a vendor whose every answer starts streaming at once
// and finishes only when the test says so: a request through it is one
// the proxy is still forwarding for as long as the test likes.
type heldUpstream struct {
	server  *httptest.Server
	release chan struct{}
	once    sync.Once
}

func newHeldUpstream(t *testing.T) *heldUpstream {
	t.Helper()
	u := &heldUpstream{release: make(chan struct{})}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\n\n")
		w.(http.Flusher).Flush()
		<-u.release
		_, _ = io.WriteString(w, "event: message_stop\n\n")
	}))
	t.Cleanup(func() {
		u.finish()
		u.server.Close()
	})
	return u
}

// finish lets every held answer, and every later one, run to its end.
func (u *heldUpstream) finish() { u.once.Do(func() { close(u.release) }) }

// cappedProxyFor serves a proxy for vendor in front of target with a
// MaxConcurrent of the test's own, and returns the endpoint to mint from
// and the audit it writes. A request over the cap waits only a moment
// for a slot, so a test of the refusal itself is not kept waiting for
// one; cappedProxyWaiting says how long.
func cappedProxyFor(t *testing.T, vendor Vendor, target string, limit int) (*httptest.Server, *Endpoint, func() []Entry) {
	t.Helper()
	return cappedProxyWaiting(t, vendor, target, limit, 50*time.Millisecond)
}

func cappedProxyWaiting(t *testing.T, vendor Vendor, target string, limit int, wait time.Duration) (*httptest.Server, *Endpoint, func() []Entry) {
	t.Helper()
	endpoint := NewEndpoint()
	endpoint.SetCredential(func(context.Context, Connection) (string, error) { return "credential", nil })
	var mu sync.Mutex
	var audit []Entry
	proxy := &Proxy{
		Vendor:   vendor,
		Endpoint: endpoint,
		Upstream: target,
		Audit: func(e Entry) {
			mu.Lock()
			defer mu.Unlock()
			audit = append(audit, e)
		},
		MaxConcurrent: limit,
		SlotWait:      wait,
	}
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	return server, endpoint, func() []Entry {
		mu.Lock()
		defer mu.Unlock()
		return append([]Entry(nil), audit...)
	}
}

func mint(t *testing.T, endpoint *Endpoint, owner string) string {
	t.Helper()
	_, token, err := endpoint.Mint(owner, testConnection)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// startStream opens a request whose answer the upstream is holding, and
// returns once its first event has arrived -- once the proxy is
// demonstrably forwarding it -- with the rest of the body still to come.
func startStream(t *testing.T, server *httptest.Server, vendor Vendor, token string) *http.Response {
	t.Helper()
	resp := call(t, server, http.MethodPost, path(vendor), authenticated(vendor, token), "{}")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d, want the stream to start: %s", resp.StatusCode, body)
	}
	first := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(io.LimitReader(resp.Body, int64(len("event: message_start\n")))).ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if !strings.Contains(line, "message_start") {
			t.Fatalf("first line = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream's first event never arrived")
	}
	return resp
}

func TestARunOverItsConcurrentRequestCapIsRefusedWith429(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newHeldUpstream(t)
		server, endpoint, audit := cappedProxyFor(t, vendor, up.server.URL, 2)
		token := mint(t, endpoint, "run-busy")

		startStream(t, server, vendor, token)
		startStream(t, server, vendor, token)

		resp := call(t, server, http.MethodPost, path(vendor), authenticated(vendor, token), "{}")
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("third concurrent request: status %d, want 429", resp.StatusCode)
		}
		if got := resp.Header.Get("Retry-After"); got == "" {
			t.Error("the 429 carries no Retry-After for the vendor's client to back off by")
		}
		message := assertVendorError(t, vendor, resp, DenialTooManyRequests)
		if !strings.Contains(message, "2") {
			t.Errorf("message = %q, want it to say what the cap is", message)
		}
		entries := audit()
		if got := entries[len(entries)-1]; got.Outcome != OutcomeTooMany || got.Owner != "run-busy" || got.Path != path(vendor) {
			t.Errorf("audit = %+v, want a too-many-requests refusal for run-busy", got)
		}

		// The cap is the run's, not the proxy's: another run is not
		// held up by this one's open requests.
		startStream(t, server, vendor, mint(t, endpoint, "run-quiet"))
	})
}

// A streamed answer holds its slot until the last byte of it has been
// forwarded, not merely until its headers have: otherwise a run could
// keep any number of long generations open at once, which is exactly
// what the cap is for.
func TestAStreamingRequestHoldsItsSlotUntilTheBodyFinishes(t *testing.T) {
	up := newHeldUpstream(t)
	server, endpoint, _ := cappedProxyFor(t, Claude, up.server.URL, 1)
	token := mint(t, endpoint, "run-7")

	stream := startStream(t, server, Claude, token)
	if resp := call(t, server, http.MethodPost, path(Claude), authenticated(Claude, token), "{}"); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("while the first stream is still open: status %d, want 429", resp.StatusCode)
	}

	up.finish()
	rest, err := io.ReadAll(stream.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rest), "message_stop") {
		t.Fatalf("rest of the stream = %q", rest)
	}

	// The slot comes back as the handler returns, which can be a moment
	// after the caller has read the last byte.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := call(t, server, http.MethodPost, path(Claude), authenticated(Claude, token), "{}")
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode == http.StatusOK {
			break
		}
		if resp.StatusCode != http.StatusTooManyRequests || time.Now().After(deadline) {
			t.Fatalf("after the stream finished: status %d, want its slot given back", resp.StatusCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A caller that goes away mid-stream -- a sandbox whose client was
// killed, or that timed out and hung up -- gives its slot back as soon as
// the proxy notices, not once the vendor gets round to finishing a reply
// nobody is reading. The 2026-10-12 walkthrough
// (docs/test-runs/2026-10-12-modelproxy-concurrency-cap.md) saw this
// against a real daemon; this keeps it so while the upstream is still
// holding the stream open.
func TestACallerThatHangsUpMidStreamGivesItsSlotBack(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newHeldUpstream(t)
		server, endpoint, _ := cappedProxyFor(t, vendor, up.server.URL, 1)
		// Before the proxy's own Close, which waits for its handlers: a
		// proxy that kept forwarding for a caller long gone would otherwise
		// hang this test's cleanup rather than fail it.
		t.Cleanup(up.finish)
		token := mint(t, endpoint, "run-7")

		stream := startStream(t, server, vendor, token)
		// Closing an unread body closes the connection under it, which is
		// all a killed client leaves behind.
		stream.Body.Close()

		deadline := time.Now().Add(5 * time.Second)
		for {
			resp := call(t, server, http.MethodPost, path(vendor), authenticated(vendor, token), "{}")
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
			if resp.StatusCode != http.StatusTooManyRequests || time.Now().After(deadline) {
				t.Fatalf("after the caller hung up: status %d, want its slot given back while the upstream still holds the stream", resp.StatusCode)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

// A request refused for anything else never takes a slot, so a run
// cannot lock itself out by asking for something it may not have.
func TestARefusedRequestTakesNoSlot(t *testing.T) {
	up := newHeldUpstream(t)
	server, endpoint, _ := cappedProxyFor(t, Claude, up.server.URL, 1)
	token := mint(t, endpoint, "run-7")
	for range 3 {
		if resp := call(t, server, http.MethodPost, "/v1/files", authenticated(Claude, token), "{}"); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status %d, want 403", resp.StatusCode)
		}
	}
	startStream(t, server, Claude, token)
}

func TestAZeroMaxConcurrentMeansTheDefault(t *testing.T) {
	// -model-proxy-max-concurrent documents "0 or less means the
	// default", and the daemon passes a negative value through as given.
	for _, limit := range []int{0, -1} {
		if got := (&Proxy{MaxConcurrent: limit}).maxConcurrent(); got != DefaultMaxConcurrent {
			t.Fatalf("MaxConcurrent %d: maxConcurrent() = %d, want %d", limit, got, DefaultMaxConcurrent)
		}
	}
	p := &Proxy{}
	for i := range DefaultMaxConcurrent {
		if _, ok := p.acquire("run-7"); !ok {
			t.Fatalf("slot %d refused, under the default cap of %d", i+1, DefaultMaxConcurrent)
		}
	}
	if _, ok := p.acquire("run-7"); ok {
		t.Fatalf("slot %d granted, over the default cap of %d", DefaultMaxConcurrent+1, DefaultMaxConcurrent)
	}
}

// Releasing gives exactly one slot back, however often it is called, and
// an owner holding none is forgotten.
func TestReleasingASlotIsIdempotentAndForgetsAnIdleOwner(t *testing.T) {
	p := &Proxy{MaxConcurrent: 1}
	release, ok := p.acquire("run-7")
	if !ok {
		t.Fatal("first slot refused")
	}
	release()
	release()
	if len(p.inflight) != 0 {
		t.Fatalf("inflight = %v, want the idle owner forgotten", p.inflight)
	}
	if _, ok := p.acquire("run-7"); !ok {
		t.Fatal("slot refused after release")
	}
	if _, ok := p.acquire("run-7"); ok {
		t.Fatal("a double release gave back two slots")
	}
}

// retryingClient is a vendor SDK at its default settings: it sends a
// request, and on a 429 sleeps for the Retry-After it was given and sends
// it again, at most retries times -- two for the Anthropic and OpenAI
// SDKs, none for google-genai. It answers the last status it saw and how
// many attempts that took.
func retryingClient(server *httptest.Server, vendor Vendor, token string, retries int) (status, attempts int, err error) {
	for {
		attempts++
		req, err := http.NewRequest(http.MethodPost, server.URL+path(vendor), strings.NewReader("{}"))
		if err != nil {
			return 0, attempts, err
		}
		req.Header = authenticated(vendor, token)
		resp, err := server.Client().Do(req)
		if err != nil {
			return 0, attempts, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests || attempts > retries {
			return resp.StatusCode, attempts, nil
		}
		seconds, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		time.Sleep(time.Duration(seconds) * time.Second)
	}
}

// A request over the cap waits for a slot rather than being refused at
// once, so a stock client completes whenever one of its run's calls
// finishes within SlotWait -- even one whose whole retry budget runs out
// long before that. The 2026-10-12 walkthrough
// (docs/test-runs/2026-10-12-modelproxy-concurrency-cap.md, F1) saw every
// call over the cap fail at default settings when the proxy refused at
// once: about two seconds for the Anthropic and OpenAI SDKs, under a
// tenth of one for google-genai.
func TestAStockClientCompletesWhenASlotFreesWithinTheWait(t *testing.T) {
	cases := []struct {
		name    string
		retries int
		hold    time.Duration
	}{
		// Longer than two retries a Retry-After apart.
		{"retries twice", 2, 2500 * time.Millisecond},
		{"never retries", 0, 200 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eachVendor(t, func(t *testing.T, vendor Vendor) {
				if tc.retries > 0 && vendor.Name != Claude.Name {
					// One vendor is enough for the slow case: the wait is
					// the same code for every one of them.
					t.Skip()
				}
				up := newHeldUpstream(t)
				server, endpoint, audit := cappedProxyWaiting(t, vendor, up.server.URL, 1, 10*time.Second)
				token := mint(t, endpoint, "run-7")
				stream := startStream(t, server, vendor, token)

				type result struct{ status, attempts int }
				done := make(chan result, 1)
				go func() {
					status, attempts, err := retryingClient(server, vendor, token, tc.retries)
					if err != nil {
						t.Error(err)
					}
					done <- result{status, attempts}
				}()

				select {
				case got := <-done:
					t.Fatalf("the request over the cap finished with %d while every slot was still taken", got.status)
				case <-time.After(tc.hold):
				}
				// The first call finishes, and with it the wait.
				up.finish()
				_, _ = io.Copy(io.Discard, stream.Body)

				select {
				case got := <-done:
					if got.status != http.StatusOK || got.attempts != 1 {
						t.Fatalf("status %d after %d attempts, want 200 at the first", got.status, got.attempts)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("the waiting request never got the slot the first call gave back")
				}
				for _, e := range audit() {
					if e.Outcome == OutcomeTooMany {
						t.Errorf("audit = %+v, want no refusal for a request that got its slot within the wait", e)
					}
				}
			})
		})
	}
}

// A request still waiting for a slot when its caller hangs up stops
// waiting then, rather than holding on for a caller long gone, and takes
// no slot on its way out. Its body is empty because net/http notices a
// caller hanging up only once the body has been read, which a request
// waiting for a slot has not yet done; one with a body waits out
// SlotWait instead.
func TestAWaitingRequestGivesUpWhenItsCallerDoes(t *testing.T) {
	up := newHeldUpstream(t)
	server, endpoint, audit := cappedProxyWaiting(t, Claude, up.server.URL, 1, time.Minute)
	t.Cleanup(up.finish)
	token := mint(t, endpoint, "run-7")
	stream := startStream(t, server, Claude, token)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path(Claude), http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = authenticated(Claude, token)
	if resp, err := server.Client().Do(req); err == nil {
		resp.Body.Close()
		t.Fatalf("status %d, want the caller to have given up while every slot was taken", resp.StatusCode)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		refused := false
		for _, e := range audit() {
			refused = refused || e.Outcome == OutcomeTooMany
		}
		if refused {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the proxy was still waiting for a slot for a caller that had hung up")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The one slot is still the first stream's, and comes back with it.
	up.finish()
	_, _ = io.Copy(io.Discard, stream.Body)
	if status, _, err := retryingClient(server, Claude, token, 0); err != nil || status != http.StatusOK {
		t.Fatalf("after the first stream finished: status %d (%v), want 200", status, err)
	}
}

// Every request waiting on a run's slots gets one in turn as they are
// given back, not only the first to wake.
func TestEveryWaiterGetsASlotInTurn(t *testing.T) {
	p := &Proxy{MaxConcurrent: 2, SlotWait: 10 * time.Second}
	var held []func()
	for range 2 {
		release, ok := p.acquire("run-7")
		if !ok {
			t.Fatal("slot refused under the cap")
		}
		held = append(held, release)
	}
	const waiters = 6
	var wg sync.WaitGroup
	failed := make(chan int, waiters)
	for i := range waiters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, ok := p.awaitSlot(context.Background(), "run-7")
			if !ok {
				failed <- i
				return
			}
			time.Sleep(10 * time.Millisecond)
			release()
		}()
	}
	time.Sleep(50 * time.Millisecond)
	for _, release := range held {
		release()
	}
	wg.Wait()
	close(failed)
	for i := range failed {
		t.Errorf("waiter %d got no slot", i)
	}
	p.inflightMu.Lock()
	defer p.inflightMu.Unlock()
	if len(p.inflight) != 0 {
		t.Fatalf("inflight = %v, want every slot given back and the owner forgotten", p.inflight)
	}
}

// A negative SlotWait refuses at once, the way this proxy did before it
// waited at all, and 0 means the default.
func TestSlotWaitDefaultsAndANegativeOneRefusesAtOnce(t *testing.T) {
	if got := (&Proxy{}).slotWait(); got != DefaultSlotWait {
		t.Fatalf("slotWait() = %v, want %v", got, DefaultSlotWait)
	}
	p := &Proxy{MaxConcurrent: 1, SlotWait: -1}
	if _, ok := p.acquire("run-7"); !ok {
		t.Fatal("first slot refused")
	}
	start := time.Now()
	if _, ok := p.awaitSlot(context.Background(), "run-7"); ok {
		t.Fatal("slot granted over the cap")
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("waited %v, want an immediate refusal", waited)
	}
}
