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

// The HTTP half: authenticate the caller against the token registry,
// swap its credentials for its connection's, and stream whatever
// the vendor says back untouched. What "its credentials" means, and what
// a refusal looks like, is the Vendor this proxy was built with; nothing
// else here knows which API is at the far end.
//
// net/http/httputil's ReverseProxy does the streaming, rather than the
// read-body/decide/write-body shape pkg/gitproxy uses. The git proxy can
// buffer because a git request is a bounded pack the proxy has already
// decided about; a model request is a server-sent-event stream a client
// is rendering token by token, and buffering one would turn a streaming
// API into a batch one.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Entry is one decision this proxy made, for an audit callback to
// record. It carries no token, no credential and no request body: the
// point of an entry is that a run called a model through here, not what
// it said.
type Entry struct {
	Time time.Time
	// Vendor is Vendor.Name -- which of this deployment's proxies made
	// the decision, since a deployment serves one per vendor and they
	// log to the same place.
	Vendor string
	// Owner is the run the token belonged to, empty where no token
	// authenticated.
	Owner string
	// Connection is the token's Connection: the connection whose
	// credential the request was (or would have been) forwarded with.
	// It is empty where no token authenticated, or where the deployment
	// has no connection for this vendor.
	Connection string
	// Method and Path are the request line, query strings excluded --
	// deliberately, and not merely as tidiness: the Gemini API takes its
	// API key as a `key=` query parameter, so a query string here is
	// where both the caller's token and this deployment's own credential
	// would end up, and this is a log.
	Method string
	Path   string
	// Outcome is one of the outcome* constants below.
	Outcome string
}

// The outcomes an Entry reports. Everything that is not "forwarded" is a
// request this proxy answered itself, and is worth a log line in a way
// the ordinary path is not: a deployment logging every forwarded request
// would write one line per streamed reply per run.
const (
	OutcomeForwarded    = "forwarded"
	OutcomeNoToken      = "no-token"
	OutcomeUnknownToken = "unknown-token"
	OutcomeNoCredential = "no-credential"
	OutcomeTooLarge     = "too-large"
	OutcomeForbidden    = "forbidden-endpoint"
	OutcomeTooMany      = "too-many-requests"
	// OutcomeNoConnection is a run whose deployment has no connection for this
	// vendor, and OutcomePaused one whose connection is held by a
	// usage-limit pause.
	OutcomeNoConnection = "no-connection"
	OutcomePaused       = "connection-paused"
	// OutcomeUsageLimit is a forwarded request the vendor answered with a
	// usage limit, which paused the connection (Vendor.UsageLimit).
	OutcomeUsageLimit = "usage-limit"
)

// DefaultMaxBodyBytes is the request body limit a Proxy with no
// MaxBodyBytes of its own enforces. Comfortably above the largest
// generation request any of these vendors accepts (Anthropic's Messages
// API caps a request at 32 MB, Gemini's inline payloads at 20 MB,
// OpenAI's image-bearing requests at 50 MB), so nothing a vendor would
// answer is refused here -- and finite, so a sandbox cannot hold this
// daemon's connection, and its upstream one, open for as many bytes as
// it cares to send. It applies to an Unrestricted proxy too, whose
// file-upload endpoints are where a body this size would be headed.
const DefaultMaxBodyBytes int64 = 64 << 20

// DefaultMaxConcurrent is the number of requests one run may have open
// through a Proxy with no MaxConcurrent of its own. Enough for a run
// fanning work out over a handful of parallel model calls -- a second
// agent, a few subagents, an evaluation harness running several cases
// at once -- and few enough that one run's token cannot hold the
// deployment's shared credential at whatever concurrency the vendor's
// own rate limit allows, starving every other run (and the daemon's own
// agents) of it until that run's deadline.
const DefaultMaxConcurrent = 16

// DefaultSlotWait is how long a request over a run's MaxConcurrent waits
// for one of that run's slots to come free, through a Proxy with no
// SlotWait of its own, before it is refused. A slot frees only when a
// model call ends, which is seconds for a short call and minutes for a
// stream, and the vendors' clients give up long before that at their
// default settings: the Anthropic and OpenAI SDKs retry twice a
// Retry-After apart, two seconds in all, and google-genai does not retry
// at all (docs/test-runs/2026-10-12-modelproxy-concurrency-cap.md, F1).
// Waiting here instead turns the cap into backpressure a stock client
// rides out, rather than a failure every call over it sees.
const DefaultSlotWait = 60 * time.Second

// Proxy is the handler. Vendor is which API it fronts and Endpoint is
// what it authenticates against and forwards with; everything else has a
// working default.
type Proxy struct {
	// Vendor is the API at the far end and the rules for talking to it
	// (see Vendor). A zero Vendor has no upstream, so every request is
	// refused -- fail-closed, like a nil Endpoint.
	Vendor Vendor
	// Endpoint is the running proxy's registry, credential and address
	// (see Endpoint). A nil one authenticates nobody, so every request
	// is refused -- fail-closed, deliberately.
	Endpoint *Endpoint
	// Upstream overrides Vendor.Upstream. A test points it at an
	// httptest.Server; a deployment leaves it empty, since a proxy that
	// forwarded this deployment's credential somewhere other than the
	// vendor that issued it would be a different thing entirely.
	Upstream string
	// VertexUpstream overrides the Vertex AI origin a request on a
	// connection authenticating through Vertex AI is forwarded to
	// (https://{region}-aiplatform.googleapis.com), the way Upstream does
	// Vendor.Upstream -- for a test, and for nothing else.
	VertexUpstream string
	// Transport is the round tripper used for forwarded requests. nil
	// means http.DefaultTransport.
	Transport http.RoundTripper
	// Unrestricted forwards every method, path and query parameter the
	// caller sends, rather than only the vendor's model-inference
	// endpoints (Vendor.Routes, Vendor.Query). It is the escape hatch for a
	// deployment that deliberately lets its runs reach the rest of a
	// vendor's API -- files, batches, fine-tuning -- with its credential;
	// left false, those are refused with a 403.
	Unrestricted bool
	// Audit, when set, is called once per request with what was decided
	// -- and once more for a forwarded body cut off at MaxBodyBytes
	// mid-stream, since that is only known after forwarding began.
	// It must not block: it runs on the request's own goroutine, before
	// the body is streamed.
	Audit func(Entry)
	// Now is the clock an Entry is stamped with, for tests. nil means
	// time.Now.
	Now func() time.Time
	// MaxBodyBytes is the largest request body forwarded, in bytes. 0
	// means DefaultMaxBodyBytes; there is deliberately no "unlimited". A
	// request declaring a larger Content-Length is refused with a 413
	// before anything is forwarded; a chunked one that runs past the
	// limit is cut off where it does, and answered with the same 413.
	MaxBodyBytes int64
	// MaxConcurrent is how many requests one token owner -- one run --
	// may have open through this proxy at once; the next waits up to
	// SlotWait for one of them to finish, and is refused with a 429 in
	// the vendor's own shape, which its clients back off and retry on,
	// only if none does. A request holds its slot until its response has been
	// streamed to the end, so a long streamed reply counts for as long as
	// it lasts. 0 means DefaultMaxConcurrent; like MaxBodyBytes there is
	// deliberately no "unlimited", and a deployment that wants more sets
	// a bigger number. The count is per Proxy, so per vendor: a run's
	// Claude and Gemini calls are capped separately.
	MaxConcurrent int
	// SlotWait is how long a request over MaxConcurrent waits for a slot
	// before it is refused. A waiting request holds the caller's
	// connection to this proxy and an unread body, but nothing upstream.
	// It gives up as soon as its context is done -- though net/http only
	// notices a caller hanging up once a request's body has been read,
	// so for a request with a body SlotWait is what ends a wait nobody
	// is left for. 0 means DefaultSlotWait; a negative SlotWait
	// refuses at once, as this proxy did before it waited at all.
	SlotWait time.Duration

	once    sync.Once
	reverse *httputil.ReverseProxy
	initErr error

	inflightMu sync.Mutex
	inflight   map[string]*slots
}

// slots is one owner's share of MaxConcurrent: how many it has open, and
// a channel closed the next time one of them is given back, for the
// requests waiting on one. freed is made only once somebody waits.
type slots struct {
	open  int
	freed chan struct{}
}

// connectionKey carries the token's Connection from ServeHTTP to the
// response hook, which reports a usage limit against it.
type connectionKey struct{}

// credentialKey carries the resolved upstream credential from ServeHTTP,
// which is where a failure to resolve one can still be answered, to the
// rewrite below, which runs too late to answer anything -- and to the
// error handler, which uses it to make sure it never writes it back out.
type credentialKey struct{}

// ownerKey carries the authenticated run from ServeHTTP to the error
// handler, so that a body cut off at MaxBodyBytes mid-stream is audited
// against the run that sent it, the way a declared oversize body is.
type ownerKey struct{}

// ServeHTTP implements http.Handler.
//
// Several things happen before anything leaves this host, and a request
// that fails any of them never reaches the vendor: the caller must
// present a token this deployment minted (Tokens.Authenticate), that
// token must not have been revoked or expired, the request must be for
// one of the vendor's model-inference endpoints, with a method that
// endpoint is forwarded for (Vendor.Routes and Vendor.Query, unless
// Unrestricted), the deployment must have a connection for this
// vendor, that connection must not be paused after a usage limit, and it
// must hold a credential this proxy can forward with. Everything after
// that is the vendor's -- the body, the status, the stream -- except
// how many of them one run may have open at once (MaxConcurrent), and a
// usage limit in the answer, which pauses the connection.
//
// The endpoint check is what keeps a sandbox token from being a token
// for the whole of the vendor's API: the credential this proxy forwards
// with is often scoped well beyond inference, and without it a run could
// upload files, start fine-tuning or batch jobs, or reach organization
// endpoints on the deployment's account (#2403).
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := p.token(r)
	if !ok {
		p.deny(w, r, "", Connection{}, OutcomeNoToken, DenialAuth,
			"This is a grain "+p.Vendor.Display+" proxy. Authenticate with the token in $"+
				p.Vendor.TokenEnv+", minted for the run this sandbox belongs to.")
		return
	}
	owner, connection, ok := p.Endpoint.authenticate(token)
	if !ok {
		p.deny(w, r, "", Connection{}, OutcomeUnknownToken, DenialAuth,
			"This grain "+p.Vendor.Display+" proxy does not recognise that token: it was never minted "+
				"here, or the run that minted it has finished and its token has been revoked.")
		return
	}
	if !p.Unrestricted {
		if reason, ok := p.Vendor.permits(r); !ok {
			p.deny(w, r, owner, connection, OutcomeForbidden, DenialForbidden,
				"This grain "+p.Vendor.Display+" proxy forwards model-inference endpoints only ("+
					p.Vendor.Endpoints+"), and refused this request: "+reason+".")
			return
		}
	}
	if connection.ID == "" {
		// A 403: the token is fine and nothing on the operator's side is
		// broken -- this deployment simply has no key of this vendor's
		// to spend.
		p.deny(w, r, owner, connection, OutcomeNoConnection, DenialForbidden,
			"this deployment has no "+p.Vendor.Display+" connection for this grain proxy to forward with. "+
				"An admin adds one in Settings -> Models.")
		return
	}
	if p.Endpoint.connectionPaused(connection, p.now()) {
		w.Header().Set("Retry-After", "60")
		p.deny(w, r, owner, connection, OutcomePaused, DenialTooManyRequests,
			"connection "+connection.ID+" is paused after reaching its "+
				p.Vendor.Company+" usage limit; this grain proxy forwards on it again once the pause lifts.")
		return
	}
	// A connection on Vertex AI has no key of the vendor's to swap in,
	// and is translated instead (serveVertex).
	vertex, err := p.Endpoint.ResolveVertex(r.Context(), connection)
	if err != nil {
		message := "connection " + connection.ID + " authenticates through Vertex AI, " +
			"and this grain proxy could not mint a Google Cloud access token for it."
		var refusal *CredentialRefusal
		if errors.As(err, &refusal) {
			message = refusal.Reason
		}
		p.deny(w, r, owner, connection, OutcomeNoCredential, DenialUpstream, message)
		return
	}
	if vertex != nil {
		p.serveVertex(w, r, owner, connection, vertex)
		return
	}
	credential, err := p.Endpoint.ResolveCredential(r.Context(), connection)
	if err != nil || strings.TrimSpace(credential) == "" {
		// A 502 rather than a 401: the caller's own token was fine, and
		// what is missing is on this side of the proxy. The sentence
		// says so, because the run reading it can do nothing about it
		// and the admin who can needs to know which connection to fix.
		message := "connection " + connection.ID + " holds no " +
			p.Vendor.Company + " credential for this grain proxy to forward with: an admin sets " +
			"one on it in Settings -> Models."
		var refusal *CredentialRefusal
		if errors.As(err, &refusal) {
			message = refusal.Reason
		}
		p.deny(w, r, owner, connection, OutcomeNoCredential, DenialUpstream, message)
		return
	}

	limit := p.maxBodyBytes()
	if r.ContentLength > limit {
		p.deny(w, r, owner, connection, OutcomeTooLarge, DenialTooLarge, p.tooLarge(limit))
		return
	}
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
	}

	reverse, err := p.proxy()
	if err != nil {
		p.deny(w, r, owner, connection, OutcomeNoCredential, DenialUpstream,
			"This grain model proxy is misconfigured and has no upstream to forward to: "+err.Error())
		return
	}
	// Last of the checks, so that a request refused for anything else
	// never takes (or waits for) a slot, and released only once
	// ReverseProxy has returned -- which is after the whole response, a
	// stream included, has been copied to the caller.
	release, ok := p.awaitSlot(r.Context(), owner)
	if !ok {
		p.tooMany(w, r, owner, connection, p.Vendor.Display)
		return
	}
	defer release()
	p.record(p.entry(r, owner, connection, OutcomeForwarded))
	ctx := context.WithValue(r.Context(), credentialKey{}, strings.TrimSpace(credential))
	ctx = context.WithValue(ctx, ownerKey{}, owner)
	ctx = context.WithValue(ctx, connectionKey{}, connection)
	reverse.ServeHTTP(w, r.WithContext(ctx))
}

// token reads the caller's token the way this vendor's clients present
// one. A Proxy with no vendor at all finds none, which is a 401: nothing
// is forwarded by a handler that does not know where.
func (p *Proxy) token(r *http.Request) (string, bool) {
	if p.Vendor.Token == nil {
		return "", false
	}
	return p.Vendor.Token(r)
}

// proxy builds the ReverseProxy once, on the first request, so that
// Upstream and Transport can be set on a zero value and still be read.
func (p *Proxy) proxy() (*httputil.ReverseProxy, error) {
	p.once.Do(func() {
		upstream := p.Upstream
		if upstream == "" {
			upstream = p.Vendor.Upstream
		}
		if upstream == "" || p.Vendor.Authorize == nil {
			// A handler built with no vendor. It cannot forward
			// anything, and the one thing it must not do is forward
			// something unauthenticated.
			p.initErr = errors.New("this proxy was built with no vendor to forward to")
			return
		}
		target, err := url.Parse(upstream)
		if err != nil {
			p.initErr = err
			return
		}
		authorize := p.Vendor.Authorize
		p.reverse = &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				credential, _ := pr.Out.Context().Value(credentialKey{}).(string)
				if outbound, ok := pr.Out.Context().Value(vertexKey{}).(*url.URL); ok {
					authorizeVertex(pr.Out, outbound, credential)
					return
				}
				pr.SetURL(target)
				// SetURL leaves the outbound Host as the inbound one,
				// which for this proxy is a bridge address the vendor's
				// TLS front end has never heard of.
				pr.Out.Host = target.Host
				authorize(pr.Out, credential)
			},
			Transport: p.Transport,
			// Every byte, as soon as it arrives. A model response is a
			// stream a human is watching, and ReverseProxy's default
			// only flushes immediately for a response that already
			// declares itself text/event-stream -- which the non-
			// streaming half of these APIs does not, and which a future
			// content type this build has not heard of might not
			// either.
			FlushInterval: -1,
			// A usage limit is the connection's, not this request's: every
			// other run spending the same key -- through this proxy or as
			// its own agent -- is about to meet it too, so it is reported
			// to the pause those runs share. The response itself goes back
			// to the caller untouched.
			ModifyResponse: func(resp *http.Response) error {
				if resp.Request == nil {
					return nil
				}
				usageLimit := p.Vendor.UsageLimit
				if _, ok := resp.Request.Context().Value(vertexKey{}).(*url.URL); ok {
					usageLimit = p.vertexUsageLimit
				}
				if usageLimit == nil {
					return nil
				}
				limit, ok := usageLimit(resp)
				if !ok {
					return nil
				}
				connection, _ := resp.Request.Context().Value(connectionKey{}).(Connection)
				owner, _ := resp.Request.Context().Value(ownerKey{}).(string)
				limit.Framework = p.Vendor.Framework
				p.record(p.entry(resp.Request, owner, connection, OutcomeUsageLimit))
				p.Endpoint.reportUsageLimit(connection, limit)
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				// The transport's own error, with this deployment's
				// credential taken back out of it. Nothing in net/http
				// puts one there today, but this vendor's credential
				// travels in the URL (Gemini's `key=`), and a transport
				// error naming the URL it failed on would otherwise hand
				// the sandbox the very credential this proxy exists to
				// keep from it.
				// A body that ran past MaxBodyBytes mid-stream surfaces
				// here, as the transport's failure to read it, and is the
				// caller's doing rather than the vendor's.
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					owner, _ := r.Context().Value(ownerKey{}).(string)
					connection, _ := r.Context().Value(connectionKey{}).(Connection)
					p.record(p.entry(r, owner, connection, OutcomeTooLarge))
					p.Vendor.writeError(w, DenialTooLarge, p.tooLarge(tooLarge.Limit))
					return
				}
				credential, _ := r.Context().Value(credentialKey{}).(string)
				p.Vendor.writeError(w, DenialUpstream,
					"This grain "+p.Vendor.Display+" proxy could not reach "+p.Vendor.Company+": "+
						redact(err.Error(), credential))
			},
		}
	})
	if p.initErr != nil {
		return nil, p.initErr
	}
	return p.reverse, nil
}

func (p *Proxy) deny(w http.ResponseWriter, r *http.Request, owner string, connection Connection, outcome string, kind Denial, message string) {
	p.record(p.entry(r, owner, connection, outcome))
	p.Vendor.writeError(w, kind, message)
}

// entry is the audit Entry for one decision about r.
func (p *Proxy) entry(r *http.Request, owner string, connection Connection, outcome string) Entry {
	entry := Entry{Time: p.now(), Vendor: p.Vendor.Name, Owner: owner, Method: r.Method, Path: r.URL.Path, Outcome: outcome}
	if owner != "" {
		entry.Connection = connection.normalized().ID
	}
	return entry
}

// writeError writes a refusal in this vendor's own error shape -- see
// Vendor.WriteError, of which this is the nil-safe half: a Proxy built
// with no vendor still has to answer the caller it is refusing, and it
// answers in the shape most of these APIs share.
func (v Vendor) writeError(w http.ResponseWriter, kind Denial, message string) {
	if v.WriteError == nil {
		writeJSON(w, kind.Status(), map[string]any{"error": map[string]any{"message": message}})
		return
	}
	v.WriteError(w, kind, message)
}

// redact takes a credential back out of text it should never have
// reached. Empty credentials redact nothing, since strings.ReplaceAll
// with an empty needle would otherwise rewrite the whole string.
func redact(text, credential string) string {
	if credential == "" {
		return text
	}
	return strings.ReplaceAll(text, credential, "[redacted]")
}

func (p *Proxy) maxBodyBytes() int64 {
	if p.MaxBodyBytes > 0 {
		return p.MaxBodyBytes
	}
	return DefaultMaxBodyBytes
}

func (p *Proxy) maxConcurrent() int {
	if p.MaxConcurrent > 0 {
		return p.MaxConcurrent
	}
	return DefaultMaxConcurrent
}

func (p *Proxy) slotWait() time.Duration {
	if p.SlotWait != 0 {
		return p.SlotWait
	}
	return DefaultSlotWait
}

// tooMany refuses a request that found no slot within SlotWait.
// Retry-After is what the vendors' own clients read before backing off
// on a 429; this proxy has already waited on the caller's behalf, so a
// second is enough before it does so again.
func (p *Proxy) tooMany(w http.ResponseWriter, r *http.Request, owner string, connection Connection, display string) {
	w.Header().Set("Retry-After", "1")
	p.deny(w, r, owner, connection, OutcomeTooMany, DenialTooManyRequests, fmt.Sprintf(
		"This grain %s proxy forwards at most %d requests at once for one run, and this run had that many "+
			"open for as long as the proxy waits for one to finish. Wait for one to finish and retry.",
		display, p.maxConcurrent()))
}

// awaitSlot takes one of owner's MaxConcurrent slots, waiting up to
// SlotWait for one to be given back if every one is taken, and answers
// false if none is by then -- or as soon as ctx, the caller's request,
// is done, since nobody is left to forward for.
func (p *Proxy) awaitSlot(ctx context.Context, owner string) (release func(), ok bool) {
	release, freed, ok := p.take(owner)
	if ok || p.slotWait() < 0 {
		return release, ok
	}
	timer := time.NewTimer(p.slotWait())
	defer timer.Stop()
	for {
		select {
		case <-freed:
		case <-timer.C:
			return nil, false
		case <-ctx.Done():
			return nil, false
		}
		// Every waiter wakes when a slot is given back and only one of
		// them gets it; the rest wait on the next.
		if release, freed, ok = p.take(owner); ok {
			return release, true
		}
	}
}

// acquire takes one of owner's MaxConcurrent slots, answering false at
// once if every one is already taken.
func (p *Proxy) acquire(owner string) (release func(), ok bool) {
	release, _, ok = p.take(owner)
	return release, ok
}

// take is acquire, also answering, when it refuses, a channel closed the
// next time one of owner's slots is given back. The release it returns
// gives the slot back, and forgets an owner left holding none, so the
// map is only ever as large as the number of runs with a request in
// flight.
func (p *Proxy) take(owner string) (release func(), freed <-chan struct{}, ok bool) {
	p.inflightMu.Lock()
	defer p.inflightMu.Unlock()
	s := p.inflight[owner]
	if s == nil {
		s = &slots{}
		if p.inflight == nil {
			p.inflight = make(map[string]*slots)
		}
		p.inflight[owner] = s
	}
	if s.open >= p.maxConcurrent() {
		if s.freed == nil {
			s.freed = make(chan struct{})
		}
		return nil, s.freed, false
	}
	s.open++
	var once sync.Once
	return func() {
		once.Do(func() {
			p.inflightMu.Lock()
			defer p.inflightMu.Unlock()
			s.open--
			if s.freed != nil {
				close(s.freed)
				s.freed = nil
			}
			if s.open <= 0 {
				delete(p.inflight, owner)
			}
		})
	}, nil, true
}

func (p *Proxy) tooLarge(limit int64) string {
	return fmt.Sprintf("This grain %s proxy forwards request bodies of at most %d bytes, and this one is larger.",
		p.Vendor.Display, limit)
}

func (p *Proxy) record(entry Entry) {
	if p.Audit == nil {
		return
	}
	p.Audit(entry)
}

func (p *Proxy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}
