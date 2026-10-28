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

package github

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// primaryLimitBody and secondaryLimitBody are GitHub's own wording for
// the two limits, quoted rather than paraphrased: every predicate here
// reads the message, so a test that invented its own sentence would be
// testing nothing.
const (
	primaryLimitBody   = `{"message":"API rate limit exceeded for user ID 1.","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api"}`
	secondaryLimitBody = `{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again.","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api"}`
	abuseBody          = `{"message":"You have triggered an abuse detection mechanism. Please wait a few minutes before you try again."}`
	deniedBody         = `{"message":"Resource not accessible by personal access token"}`
)

func TestIsRateLimitedRecognisesBothOfGitHubsLimits(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"the primary limit", &Error{Status: 403, Body: []byte(primaryLimitBody)}, true},
		{"a secondary limit", &Error{Status: 403, Body: []byte(secondaryLimitBody)}, true},
		{"a secondary limit answered 429", &Error{Status: 429, Body: []byte(secondaryLimitBody)}, true},
		{"the older abuse wording", &Error{Status: 403, Body: []byte(abuseBody)}, true},
		{"this process declining to call", &RateLimitError{Until: time.Now().Add(time.Minute)}, true},

		// Everything below is a refusal that is NOT throttling, and must
		// not be read as one: the two differ in what a caller should do
		// next, and IsPermissionDenied latches auto-merge off on one of
		// them.
		{"a missing permission", &Error{Status: 403, Body: []byte(deniedBody)}, false},
		{"a 404", &Error{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, false},
		{"a 500 with no message", &Error{Status: 500, Body: []byte("boom")}, false},
		{"an error that is not GitHub's", errors.New("dial tcp: connection refused"), false},
		{"no error at all", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsRateLimited(c.err); got != c.want {
				t.Errorf("IsRateLimited(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// The two predicates partition 403 rather than overlapping on it: a
// rate limit must never latch ChecksUnavailable, and a missing
// permission must never be waited out.
func TestRateLimitAndPermissionDenialAreDisjoint(t *testing.T) {
	limited := &Error{Status: 403, Body: []byte(secondaryLimitBody)}
	if IsPermissionDenied(limited) {
		t.Error("a secondary rate limit was read as a permission the credential cannot hold")
	}
	denied := &Error{Status: 403, Body: []byte(deniedBody)}
	if IsRateLimited(denied) {
		t.Error("a missing permission was read as throttling, so grain would wait it out forever")
	}
}

func TestIsRateLimitedFindsAWrappedRefusal(t *testing.T) {
	wrapped := fmt.Errorf("reading check runs for acme/widgets: %w",
		&Error{Status: 403, Body: []byte(primaryLimitBody)})
	if !IsRateLimited(wrapped) {
		t.Error("a wrapped rate limit was not recognised")
	}
}

func TestThrottledUntilReadsRetryAfterFirst(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	resp := ApiResponse{
		Status: 403,
		Headers: map[string]string{
			"Retry-After": "60",
			// Present and contradictory on purpose: Retry-After is the
			// one GitHub attaches an obligation to, so it wins.
			"X-Ratelimit-Remaining": "0",
			"X-Ratelimit-Reset":     "1700003600",
		},
		Body: []byte(secondaryLimitBody),
	}
	until, reason, ok := throttledUntil(now, resp)
	if !ok {
		t.Fatal("a secondary rate limit with Retry-After was not read as throttling")
	}
	if want := now.Add(time.Minute); !until.Equal(want) {
		t.Errorf("until = %v, want %v", until, want)
	}
	if !strings.Contains(reason, "secondary rate limit") {
		t.Errorf("reason = %q, want GitHub's own sentence", reason)
	}
}

func TestThrottledUntilReadsThePrimaryLimitsResetInstant(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	reset := now.Add(23 * time.Minute)
	resp := ApiResponse{
		Status: 403,
		Headers: map[string]string{
			"X-Ratelimit-Remaining": "0",
			"X-Ratelimit-Reset":     fmt.Sprint(reset.Unix()),
		},
		Body: []byte(primaryLimitBody),
	}
	until, _, ok := throttledUntil(now, resp)
	if !ok {
		t.Fatal("an exhausted primary limit was not read as throttling")
	}
	if !until.Equal(reset) {
		t.Errorf("until = %v, want the reset instant %v", until, reset)
	}
}

// A 403 with budget still on the clock is one of 403's other meanings --
// SAML, an IP allow list, a missing permission -- and reading it as
// throttling would stop every GitHub call this deployment makes for a
// minute over something waiting will never fix.
func TestThrottledUntilIgnoresARefusalWithBudgetLeft(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	resp := ApiResponse{
		Status:  403,
		Headers: map[string]string{"X-Ratelimit-Remaining": "4821"},
		Body:    []byte(deniedBody),
	}
	if _, _, ok := throttledUntil(now, resp); ok {
		t.Error("a permission denial was read as throttling")
	}
}

// GitHub's wording alone is enough when neither header came with it,
// which is a shape the secondary limits really do send.
func TestThrottledUntilFallsBackToTheWording(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	until, _, ok := throttledUntil(now, ApiResponse{Status: 403, Body: []byte(abuseBody)})
	if !ok {
		t.Fatal("an abuse-detection refusal with no headers was not read as throttling")
	}
	if want := now.Add(defaultThrottleWait); !until.Equal(want) {
		t.Errorf("until = %v, want %v", until, want)
	}
}

func TestThrottledUntilIgnoresAnythingThatIsNotARefusal(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ok200 := ApiResponse{Status: 200, Headers: map[string]string{"X-Ratelimit-Remaining": "0"}}
	if _, _, ok := throttledUntil(now, ok200); ok {
		t.Error("a 200 closed the gate")
	}
}

func TestThrottleHoldsTheLaterInstantAndOpensOnItsOwn(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var gate throttle

	if _, _, blocked := gate.blocked(now); blocked {
		t.Fatal("a gate nothing has shut is closed")
	}

	gate.shut(now, now.Add(time.Minute), "secondary")
	// A shorter instant arriving second must not shorten the wait that
	// is already in force.
	gate.shut(now, now.Add(10*time.Second), "secondary")
	until, reason, blocked := gate.blocked(now)
	if !blocked {
		t.Fatal("the gate did not close")
	}
	if want := now.Add(time.Minute); !until.Equal(want) {
		t.Errorf("until = %v, want the later instant %v", until, want)
	}
	if reason != "secondary" {
		t.Errorf("reason = %q", reason)
	}

	if _, _, blocked := gate.blocked(now.Add(time.Minute)); blocked {
		t.Error("the gate was still closed at the instant it was due to open")
	}
}

func TestThrottleOpenedDoesNotPrematurelyReopenWhenUntilIsInFuture(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var gate throttle

	gate.shut(now, now.Add(time.Minute), "secondary rate limit")
	if _, _, blocked := gate.blocked(now); !blocked {
		t.Fatal("the gate did not close")
	}

	// A successful response finishing concurrently while the throttle is active
	// must NOT prematurely clear the throttle or reopen the gate.
	gate.opened(now.Add(10 * time.Second))
	until, reason, blocked := gate.blocked(now.Add(10 * time.Second))
	if !blocked {
		t.Fatal("opened() prematurely cleared the gate while until was still in the future")
	}
	if want := now.Add(time.Minute); !until.Equal(want) {
		t.Errorf("until = %v, want %v", until, want)
	}
	if reason != "secondary rate limit" {
		t.Errorf("reason = %q", reason)
	}

	// Once the throttle window has passed, opened() clears announced state.
	gate.opened(now.Add(70 * time.Second))
	if _, _, blocked := gate.blocked(now.Add(70 * time.Second)); blocked {
		t.Error("the gate should be open after throttle window elapsed")
	}
}

// A header out of all proportion -- a proxy in front of GitHub, a clock
// a long way out -- must not be able to switch a deployment's GitHub
// access off for a day.
func TestThrottleCapsWhatAHeaderCanAskFor(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var gate throttle
	gate.shut(now, now.Add(72*time.Hour), "a very confused proxy")
	until, _, blocked := gate.blocked(now)
	if !blocked {
		t.Fatal("the gate did not close")
	}
	if want := now.Add(maxThrottleWait); !until.Equal(want) {
		t.Errorf("until = %v, want it capped at %v", until, want)
	}
}

func TestRetryAfterReadsSecondsAndDates(t *testing.T) {
	if d, ok := retryAfter("120"); !ok || d != 2*time.Minute {
		t.Errorf("retryAfter(\"120\") = %v, %v", d, ok)
	}
	if d, ok := retryAfter(time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)); !ok ||
		d < 80*time.Second || d > 90*time.Second {
		t.Errorf("retryAfter(<http date>) = %v, %v", d, ok)
	}
	// Nothing readable, and nothing that would make the gate a no-op at
	// the one moment it is needed.
	for _, bad := range []string{"", "   ", "soon", "0", "-30"} {
		if _, ok := retryAfter(bad); ok {
			t.Errorf("retryAfter(%q) was read as an answer", bad)
		}
	}
}

func TestHeaderValueReadsEverySpellingItsSourcesUse(t *testing.T) {
	// The three shapes an ApiResponse's headers arrive in: Go's
	// canonicalisation (RealTransport), GitHub's own spelling (a
	// githubsim or a hand-written test), and lower case (a proxy).
	for _, headers := range []map[string]string{
		{"X-Ratelimit-Reset": "7"},
		{"X-RateLimit-Reset": "7"},
		{"x-ratelimit-reset": "7"},
	} {
		if got := headerValue(headers, "X-RateLimit-Reset"); got != "7" {
			t.Errorf("headerValue(%v) = %q", headers, got)
		}
	}
	if got := headerValue(nil, "Retry-After"); got != "" {
		t.Errorf("headerValue(nil) = %q", got)
	}
}

// The whole point, end to end: one refusal from GitHub and the next call
// never leaves the host.
func TestRealTransportStopsCallingOnceGitHubHasRefused(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(403)
		w.Write([]byte(secondaryLimitBody))
	}))
	defer server.Close()

	transport := &RealTransport{Host: strings.TrimPrefix(server.URL, "http://"), UseTLS: false}

	// The first call is made and answered: the refusal is a real
	// response, and reaches the caller as one.
	resp, err := transport.Request("GET", "/repos/acme/widgets/pulls/1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 403 {
		t.Fatalf("first call: status %d, want the 403 back", resp.Status)
	}

	// The second is refused here.
	_, err = transport.Request("GET", "/repos/acme/widgets/pulls/2", nil, nil)
	if err == nil {
		t.Fatal("the second call was made, so grain kept spending calls GitHub had refused")
	}
	if !IsRateLimited(err) {
		t.Errorf("the refusal was not recognisable as throttling: %v", err)
	}
	var refused *RateLimitError
	if !errors.As(err, &refused) {
		t.Fatalf("want a *RateLimitError, got %T", err)
	}
	if !strings.Contains(refused.Reason, "secondary rate limit") {
		t.Errorf("reason = %q, want GitHub's own sentence carried through", refused.Reason)
	}
	if calls != 1 {
		t.Errorf("the server saw %d calls, want 1", calls)
	}
}

// A 403 that is not throttling leaves the gate open, so a deployment
// whose credential cannot read one endpoint keeps working everywhere
// else.
func TestRealTransportKeepsCallingAfterAnOrdinaryRefusal(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(403)
		w.Write([]byte(deniedBody))
	}))
	defer server.Close()

	transport := &RealTransport{Host: strings.TrimPrefix(server.URL, "http://"), UseTLS: false}
	for i := 0; i < 3; i++ {
		if _, err := transport.Request("GET", "/repos/acme/widgets/check-runs", nil, nil); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if calls != 3 {
		t.Errorf("the server saw %d calls, want 3 -- a permission denial closed the gate", calls)
	}
}

// One transport carries the daemon's whole credential ladder, so a
// throttle on one token must leave every other token's calls alone.
func TestRealTransportGatesEachCredentialSeparately(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		calls = append(calls, auth)
		if auth == "Bearer exhausted" {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(403)
			w.Write([]byte(secondaryLimitBody))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	transport := &RealTransport{Host: strings.TrimPrefix(server.URL, "http://"), UseTLS: false}
	exhausted := map[string]string{"Authorization": "Bearer exhausted"}
	healthy := map[string]string{"Authorization": "Bearer healthy"}

	if resp, err := transport.Request("GET", "/repos/acme/widgets/pulls/1", exhausted, nil); err != nil || resp.Status != 403 {
		t.Fatalf("first call: status %d, err %v; want GitHub's 403 back", resp.Status, err)
	}
	if _, err := transport.Request("GET", "/repos/acme/widgets/pulls/2", exhausted, nil); !IsRateLimited(err) {
		t.Fatalf("the refused credential was not gated: %v", err)
	}
	resp, err := transport.Request("GET", "/repos/other/repo/pulls/1", healthy, nil)
	if err != nil {
		t.Fatalf("another credential was gated by the first one's throttle: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("status %d, want 200", resp.Status)
	}
	if len(calls) != 2 {
		t.Errorf("the server saw %d calls (%v), want 2", len(calls), calls)
	}
}
