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

// Whose credential a forwarded request spends: the requesting run's own
// team connection, never another team's and never a deployment-wide
// key -- and a usage limit met on it pauses that connection alone.

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// teamProxyFor is proxyFor with one credential per connection, keyed by
// connection id, so a test can tell whose key a request was sent with.
func teamProxyFor(t *testing.T, vendor Vendor, up *upstream, keys map[string]string) (*Endpoint, func(token string) *http.Response, *[]Entry) {
	t.Helper()
	server, endpoint, audit := proxyFor(t, vendor, up, "")
	endpoint.SetCredential(func(_ context.Context, connection Connection) (string, error) {
		return keys[connection.ID], nil
	})
	send := func(token string) *http.Response {
		return call(t, server, http.MethodPost, path(vendor), authenticated(vendor, token), "{}")
	}
	return endpoint, send, audit
}

// Two teams, each with its own connection of this vendor: each run's
// proxied request carries its own team's credential, and the audit line
// names the connection it spent.
func TestEachTeamsRunSpendsItsOwnTeamsConnection(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newUpstream(t)
		endpoint, send, audit := teamProxyFor(t, vendor, up, map[string]string{
			"alpha-conn": "alpha-key",
			"beta-conn":  "beta-key",
		})
		for _, tc := range []struct{ team, connection, key string }{
			{"alpha", "alpha-conn", "alpha-key"},
			{"beta", "beta-conn", "beta-key"},
		} {
			_, token, err := endpoint.Mint("run-"+tc.team, Connection{ID: tc.connection})
			if err != nil {
				t.Fatal(err)
			}
			up.got = nil
			if resp := send(token); resp.StatusCode != http.StatusOK {
				t.Fatalf("team %s: status %d, want 200", tc.team, resp.StatusCode)
			}
			if up.got == nil {
				t.Fatalf("team %s: nothing reached the vendor", tc.team)
			}
			if got := credentialShown(vendor, up.got); got != tc.key {
				t.Errorf("team %s's run reached the vendor with %q, want its own connection's %q", tc.team, got, tc.key)
			}
		}
		var forwarded []Entry
		for _, entry := range *audit {
			if entry.Outcome == OutcomeForwarded {
				forwarded = append(forwarded, entry)
			}
		}
		if len(forwarded) != 2 || forwarded[0].Connection != "alpha-conn" ||
			forwarded[1].Connection != "beta-conn" {
			t.Errorf("audit = %+v, want each forwarded entry to name its connection", *audit)
		}
	})
}

// A deployment with no connection for the vendor is refused in that
// vendor's own shape -- and the credential resolver is never asked, so
// no other key can stand in.
func TestADeploymentWithNoConnectionForTheVendorIsRefused(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newUpstream(t)
		server, endpoint, audit := proxyFor(t, vendor, up, "")
		endpoint.SetCredential(func(context.Context, Connection) (string, error) {
			t.Error("a credential was resolved for a token with no connection")
			return "somebody-elses-key", nil
		})
		_, token, err := endpoint.Mint("run-1", Connection{})
		if err != nil {
			t.Fatal(err)
		}
		resp := call(t, server, http.MethodPost, path(vendor), authenticated(vendor, token), "{}")
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", resp.StatusCode)
		}
		message := assertVendorError(t, vendor, resp, DenialForbidden)
		if want := "this deployment has no " + vendor.Display + " connection"; !strings.Contains(message, want) {
			t.Errorf("message = %q, want it to say %q", message, want)
		}
		if up.got != nil {
			t.Fatal("a request with no connection reached the vendor")
		}
		if len(*audit) != 1 || (*audit)[0].Outcome != OutcomeNoConnection || (*audit)[0].Owner != "run-1" {
			t.Errorf("audit = %+v, want one no-connection entry for run-1", *audit)
		}
	})
}

// A connection the resolver will not forward with -- Vertex, an ambient
// login -- is refused with the resolver's own sentence.
func TestAConnectionThatCannotBeForwardedSaysWhy(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newUpstream(t)
		server, endpoint, _ := proxyFor(t, vendor, up, "")
		endpoint.SetCredential(func(context.Context, Connection) (string, error) {
			return "", &CredentialRefusal{Reason: "connection conn-a authenticates through Vertex"}
		})
		_, token, err := endpoint.Mint("run-1", testConnection)
		if err != nil {
			t.Fatal(err)
		}
		resp := call(t, server, http.MethodPost, path(vendor), authenticated(vendor, token), "{}")
		if message := assertVendorError(t, vendor, resp, DenialUpstream); !strings.Contains(message, "Vertex") {
			t.Errorf("message = %q, want the resolver's reason", message)
		}
		if up.got != nil {
			t.Fatal("a request on an unforwardable connection reached the vendor")
		}
	})
}

// usageLimitAnswer is how each vendor answers a request whose key has
// spent its budget.
func usageLimitAnswer(up *upstream, vendor Vendor) {
	up.status = http.StatusTooManyRequests
	switch vendor.Name {
	case Claude.Name:
		up.header = map[string]string{
			"anthropic-ratelimit-unified-status": "rejected",
			"anthropic-ratelimit-unified-reset":  "4102444800",
		}
		up.body = `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit."}}`
	case OpenAI.Name:
		up.header = map[string]string{"Retry-After": "3600"}
		up.body = `{"error":{"message":"You exceeded your current quota.","type":"insufficient_quota","param":null,"code":"insufficient_quota"}}`
	default:
		up.body = `{"error":{"code":429,"message":"Quota exceeded.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"56s"}]}}`
	}
}

// pauseRecorder is a stand-in for orchestrator.Pause: the connections a
// limit was reported for, and the ones held.
type pauseRecorder struct {
	mu      sync.Mutex
	limited []Connection
	limits  []*agent.UsageLimitError
	held    map[string]bool
}

func (p *pauseRecorder) paused(connection Connection, _ time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.held[connection.ID]
}

func (p *pauseRecorder) limit(connection Connection, limit *agent.UsageLimitError) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.limited = append(p.limited, connection)
	p.limits = append(p.limits, limit)
	if p.held == nil {
		p.held = map[string]bool{}
	}
	p.held[connection.ID] = true
}

// A usage limit the vendor answers through the proxy is reported against
// the connection that met it, and that connection only: its next request
// is refused while the other team's is still forwarded. The caller still
// receives the vendor's own answer to the request that met it.
func TestAUsageLimitThroughTheProxyPausesThatConnectionOnly(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newUpstream(t)
		endpoint, send, audit := teamProxyFor(t, vendor, up, map[string]string{
			"alpha-conn": "alpha-key",
			"beta-conn":  "beta-key",
		})
		pause := &pauseRecorder{}
		endpoint.SetPause(pause.paused, pause.limit)
		_, alpha, _ := endpoint.Mint("run-alpha", Connection{ID: "alpha-conn"})
		_, beta, _ := endpoint.Mint("run-beta", Connection{ID: "beta-conn"})

		usageLimitAnswer(up, vendor)
		resp := send(alpha)
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want the vendor's own 429", resp.StatusCode)
		}
		var body bytes.Buffer
		_, _ = body.ReadFrom(resp.Body)
		if body.String() != up.body {
			t.Errorf("the caller received %q, want the vendor's answer untouched %q", body.String(), up.body)
		}
		if len(pause.limited) != 1 || pause.limited[0] != (Connection{ID: "alpha-conn"}) {
			t.Fatalf("limits reported for %+v, want alpha-conn alone", pause.limited)
		}
		limit := pause.limits[0]
		if limit.Framework != vendor.Framework {
			t.Errorf("limit framework = %q, want %q", limit.Framework, vendor.Framework)
		}
		if _, ok := limit.ResumeAt(time.Now()); !ok {
			t.Errorf("limit %+v carries no reset, want the one the vendor named", limit)
		}

		up.status, up.body, up.header, up.got = http.StatusOK, `{"ok":true}`, nil, nil
		if resp := send(alpha); resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("a paused connection's next request got %d, want 429", resp.StatusCode)
		} else if resp.Header.Get("Retry-After") == "" {
			t.Error("a paused connection's refusal carries no Retry-After")
		}
		if up.got != nil {
			t.Fatal("a request on a paused connection reached the vendor")
		}
		if resp := send(beta); resp.StatusCode != http.StatusOK {
			t.Fatalf("the other team's connection got %d, want it still forwarded", resp.StatusCode)
		}
		if got := credentialShown(vendor, up.got); got != "beta-key" {
			t.Errorf("the other team reached the vendor with %q, want beta-key", got)
		}
		var outcomes []string
		for _, entry := range *audit {
			outcomes = append(outcomes, entry.Outcome)
		}
		want := []string{OutcomeForwarded, OutcomeUsageLimit, OutcomePaused, OutcomeForwarded}
		if strings.Join(outcomes, ",") != strings.Join(want, ",") {
			t.Errorf("audit outcomes = %v, want %v", outcomes, want)
		}
	})
}

// An ordinary 429 -- per-minute throttling a client retries through -- is
// not a usage limit and pauses nothing.
func TestAPlainRateLimitPausesNothing(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		up := newUpstream(t)
		endpoint, send, _ := teamProxyFor(t, vendor, up, map[string]string{"conn-a": "key"})
		pause := &pauseRecorder{}
		endpoint.SetPause(pause.paused, pause.limit)
		_, token, _ := endpoint.Mint("run-1", testConnection)
		up.status = http.StatusTooManyRequests
		switch vendor.Name {
		case Claude.Name:
			up.body = `{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`
		case OpenAI.Name:
			up.body = `{"error":{"message":"Rate limit reached","type":"requests","param":null,"code":"rate_limit_exceeded"}}`
		default:
			up.body = `{"error":{"code":429,"message":"busy","status":"UNAVAILABLE"}}`
		}
		send(token)
		if len(pause.limited) != 0 {
			t.Errorf("a plain rate limit paused %+v", pause.limited)
		}
	})
}

// A vendor's error body is gzipped whenever the caller's client asked
// for it, and is read through that.
func TestAGzippedUsageLimitIsRecognised(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(`{"error":{"message":"You exceeded your current quota.","type":"insufficient_quota","code":"insufficient_quota"}}`))
	_ = zw.Close()
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Encoding": {"gzip"}},
		Body:       ioNopCloser(bytes.NewReader(gz.Bytes())),
	}
	if _, ok := OpenAI.UsageLimit(resp); !ok {
		t.Fatal("a gzipped insufficient_quota was not recognised")
	}
	var rest bytes.Buffer
	_, _ = rest.ReadFrom(resp.Body)
	if !bytes.Equal(rest.Bytes(), gz.Bytes()) {
		t.Error("reading the limit changed the body the caller receives")
	}
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

func ioNopCloser(r *bytes.Reader) nopCloser { return nopCloser{r} }

// ConnectionFor is what a token is minted with. An endpoint with no
// resolver answers no connection at all, and a failing resolver answers
// its error and no connection.
func TestConnectionForAnswersTheResolversConnection(t *testing.T) {
	endpoint := NewEndpoint()
	got, err := endpoint.ConnectionFor(t.Context(), "alpha-conn")
	if err != nil || got != (Connection{}) {
		t.Fatalf("an unwired endpoint answered %+v, %v, want no connection", got, err)
	}

	endpoint.SetConnections(func(_ context.Context, own string) (Connection, error) {
		if own != "" {
			return Connection{ID: own}, nil
		}
		return Connection{ID: " deploy-conn "}, nil
	})
	if got, err := endpoint.ConnectionFor(t.Context(), "alpha-conn"); err != nil || got.ID != "alpha-conn" {
		t.Errorf("ConnectionFor = %+v, %v, want alpha-conn", got, err)
	}
	if got, err := endpoint.ConnectionFor(t.Context(), ""); err != nil || got.ID != "deploy-conn" {
		t.Errorf("ConnectionFor = %+v, %v, want deploy-conn", got, err)
	}

	endpoint.SetConnections(func(context.Context, string) (Connection, error) {
		return Connection{}, errors.New("store is down")
	})
	if got, err := endpoint.ConnectionFor(t.Context(), ""); err == nil || got.ID != "" {
		t.Errorf("a failing resolver gave %+v, %v, want its error and no connection", got, err)
	}
}
