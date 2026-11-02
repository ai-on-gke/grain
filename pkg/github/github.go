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

// Package github is the GitHub REST calls a grain deployment needs to run
// GitHub-driven work: list labelled issues, move labels, confirm a branch
// exists, open a PR, read a PR's own data and its review comments, relay a
// human's reply. Ported from v1's own Python github module, carrying its
// design history (v1 roadmap items 2, 8, 9, 12, 13, and
// bwsalmon/agents#154's create_review) forward unchanged; nothing here
// revisits those decisions.
//
// Still no sandbox-side access -- docs/design.md's split surface
// ("Orchestrator: API operations... sandboxes: git transport only") holds
// exactly as it did in v1. Only the thing that calls this package changes
// once v2 has an executor to call it from; the package itself does not
// know or care who its caller is.
//
// create_review posts a **draft** review: the request body carries no
// event key, which is what keeps GitHub from submitting it (GitHub's own
// "Create a review for a pull request" reference -- omitting event leaves
// the review PENDING, visible only to the credential that created it,
// until a human opens it on github.com and submits it themselves). An
// agent posting a *submitted* review of its own code would be marking its
// own homework; a draft is a human's opinion of what an agent found,
// waiting on that same human's sign-off before anyone else sees it.
//
// Field shapes below (head.ref/head.sha, base.ref, and the review-comment
// object's id/user.login/body/path/line) are pinned against GitHub's own
// REST reference (GET .../pulls/{number}, GET .../pulls/{number}/comments),
// not guessed.
package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// ApiResponse is one HTTP response, decoupled from net/http so a Transport
// can be satisfied by something that never opens a socket (FakeTransport,
// githubsim.Sim).
type ApiResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
	// URL is the request as it went out -- method and full URL, host and
	// API path prefix included -- set by RealTransport and empty from a
	// fake. Error carries it, so a 404 from a client aimed at the wrong
	// host (api.github.com for a repository on a GitHub Enterprise host,
	// say) names that host in the one line it is reported in.
	URL string
}

// Transport sends one already-built request and returns the raw response.
// The seam GitHubClient's own logic (path building, pagination, status
// handling, JSON field extraction) is testable through, with no real call
// to api.github.com -- the same shape gitproxy.Forwarder wraps http.Client
// in one layer down.
type Transport interface {
	Request(method, path string, headers map[string]string, body []byte) (ApiResponse, error)
}

// RealTransport talks to the GitHub API over HTTPS by default. UseTLS
// false exists for a Client pointed at a local mock server for a live
// end-to-end test -- a mock has no clean answer for a self-signed cert,
// and this package already treats "point it at a mock" as a first-class
// test seam (Transport itself, FakeTransport, githubsim.Sim) rather than
// something to fake with disabled certificate verification.
type RealTransport struct {
	Host   string
	UseTLS bool
	Client *http.Client
	// PathPrefix is put in front of every API path: "/api/v3" for a
	// GitHub Enterprise Server, whose REST API lives under that path on
	// the git host itself rather than on a host of its own, and empty for
	// github.com (APIHost) and a test server serving the API at its root.
	PathPrefix string

	// GraphQLPath is where this transport's host serves GraphQL, as a
	// path on Host taken whole rather than put under PathPrefix: the
	// request a RESTClient makes to "/graphql" goes there. Empty works it
	// out from PathPrefix (graphqlPath), which is right for github.com
	// and for a GitHub Enterprise Server; a layer whose forge serves it
	// somewhere else sets it.
	GraphQLPath string

	// Conditional has GETs revalidate against the ETag of the answer
	// this transport last got for the same request, so an unchanged
	// resource comes back 304 and costs nothing against the rate limit.
	// On for every transport this package builds and off in the zero
	// value -- conditional.go's own doc comment says why the test seam
	// is the one shape that keeps sending unconditional requests.
	Conditional bool

	// gates is this transport's "GitHub told us to stop" (ratelimit.go),
	// one per credential, and cache the ETag store the field above turns
	// on. Both are built on first use so a struct-literal RealTransport
	// needs no constructor. The gates are keyed by credential rather than
	// held one per transport because the daemon sends every token of its
	// ladder through a single transport, and one credential's exhaustion
	// must not stop another credential's calls.
	once   sync.Once
	gates  *throttles
	cache  *responseCache
	counts *stats

	// ledger is where per-caller spend and the budget are recorded:
	// the process-wide one (budget.go) unless a test set its own.
	ledger *callerLedger

	// sleep is the backoff between a 5xx and the one retry of it
	// (retry.go), replaced by a test that wants to observe the pause
	// rather than wait it out. nil is the live path.
	sleep func(time.Duration)
}

// NewRealTransport returns a transport aimed at host's REST API over
// HTTPS. host is a *git* host -- what a clone URL names -- and APIHost
// turns it into the host that actually answers REST paths.
func NewRealTransport(host string) *RealTransport {
	return &RealTransport{Host: APIHost(host), UseTLS: true, Conditional: true}
}

// init builds the two pieces of state Request keeps between calls.
func (t *RealTransport) init() {
	t.once.Do(func() {
		t.gates = &throttles{}
		t.cache = newResponseCache()
		t.counts = &stats{}
	})
}

func (t *RealTransport) ledgerOf() *callerLedger {
	if t.ledger != nil {
		return t.ledger
	}
	return ledger
}

// APIHost maps a GitHub git host to the host serving its REST API.
//
// For github.com those are two different names: git lives on github.com,
// the REST API on api.github.com. Everything in this package builds paths
// like "/repos/{owner}/{repo}/branches/{branch}", which are API paths, so
// a transport pointed at github.com sends them to
// https://github.com/repos/... -- a URL that has no API behind it and
// answers 404 to every one of them.
//
// That 404 is indistinguishable from a real one, and BranchExists
// (deliberately) reads 404 as "no such branch". A daemon configured with
// the correct git host therefore reported every branch as unpushed:
// pushes succeeded, since the git proxy forwards to the same host and for
// git that host is right, and then the run that had just pushed was
// recorded as having done nothing. No test caught it because the live
// tests point this at a githubsim on 127.0.0.1:port, which serves API
// paths at its root -- the one shape where git host and API host coincide.
//
// Anything that is not github.com is left alone: that is either such a
// mock or a GitHub Enterprise host. Enterprise is not actually supported
// by this mapping -- its API lives under a /api/v3 path prefix, not on a
// separate hostname, and Transport has no place to put a base path yet.
// Naming that here rather than silently pretending otherwise.
func APIHost(host string) string {
	if host == "github.com" {
		return "api.github.com"
	}
	return host
}

// Request sends one call, unless this deployment has already been told
// not to -- and asks again once if GitHub failed to answer it.
//
// Three things wrap the round trip, and all three exist because grain
// talks to a service it does not control: the rate-limit gate refuses the
// call outright while GitHub is throttling this credential
// (ratelimit.go), a GET revalidates against the ETag of the last answer
// for the same request, so an unchanged resource comes back 304 and is
// answered out of the cache (conditional.go), and a 5xx on an idempotent
// method is asked a second time after a short pause (retry.go). None of
// them changes what a caller sees on any call GitHub actually answered.
func (t *RealTransport) Request(method, path string, headers map[string]string, body []byte) (ApiResponse, error) {
	t.init()
	for attempt := 1; ; attempt++ {
		resp, err := t.sendOnce(method, path, headers, body)
		// A transport-level failure is not retried here (retry.go says
		// why), and neither is a refusal this transport made itself.
		if err != nil || !worthRetrying(method, resp, attempt) {
			return resp, err
		}
		t.counts.recordRetried()
		t.pause(retryBackoff)
	}
}

// graphqlRequestPath is the path a RESTClient hands its Transport for a
// GraphQL request: the endpoint by name rather than by where any one host
// serves it, which is the transport's business (graphqlPath). A Transport
// that is not a RealTransport maps it to its own host's endpoint itself.
const graphqlRequestPath = "/graphql"

// graphqlPath is the path on Host GraphQL is posted to: GraphQLPath when
// set, and otherwise beside the REST API rather than under it --
// /graphql for api.github.com (no prefix), /api/graphql for a GitHub
// Enterprise Server whose REST API is under /api/v3. A REST prefix that
// is not a /v3 one has GraphQL put under it, as every other path is.
func (t *RealTransport) graphqlPath() string {
	if t.GraphQLPath != "" {
		return t.GraphQLPath
	}
	if base, ok := strings.CutSuffix(t.PathPrefix, "/v3"); ok {
		return base + graphqlRequestPath
	}
	return t.PathPrefix + graphqlRequestPath
}

// sendOnce is one attempt at a call: the gate, the conditional request,
// the round trip and the bookkeeping that follows it. Everything in here
// is per-attempt on purpose -- a retry re-asks the gate, so a throttle
// that arrived from a concurrent call in between stops the second
// request, and re-counts, so the stats say what was actually spent.
func (t *RealTransport) sendOnce(method, path string, headers map[string]string, body []byte) (ApiResponse, error) {
	now := time.Now()
	caller := headerValue(headers, CallerHeader)
	gate := t.gates.forCredential(headerValue(headers, "Authorization"))
	if until, reason, blocked := gate.blocked(now); blocked {
		// Counted, and counted apart from a refusal GitHub made: this is
		// the one grain declined to send, and a non-zero count of them is
		// how an operator sees a throttle that is in force right now
		// rather than one that happened at some point (stats.go).
		t.counts.recordDeclined()
		t.ledgerOf().recordDeclined(caller)
		return ApiResponse{}, &RateLimitError{Until: until, Reason: reason}
	}

	scheme := "https"
	if !t.UseTLS {
		scheme = "http"
	}
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	fullPath := t.PathPrefix + path
	switch {
	case path == graphqlRequestPath:
		fullPath = t.graphqlPath()
	case t.PathPrefix != "" && (path == t.PathPrefix || strings.HasPrefix(path, t.PathPrefix+"/")):
		fullPath = path
	}
	req, err := http.NewRequest(method, scheme+"://"+t.Host+fullPath, bodyReader)
	if err != nil {
		return ApiResponse{}, err
	}
	for k, v := range headers {
		// The caller and repo labels are grain's own bookkeeping
		// (budget.go, pkg/daemon's forgeRouter), and GitHub has no
		// business seeing them.
		switch http.CanonicalHeaderKey(k) {
		case CallerHeader, RepoHeader:
			continue
		}
		req.Header.Set(k, v)
	}

	// The validator for this exact request, if there is one to send.
	// Only a plain GET with nothing of its own to revalidate against:
	// a caller that set If-None-Match itself has said what it wants and
	// is not to be second-guessed.
	var (
		key      string
		cached   *cachedResponse
		revalidn bool
	)
	if t.Conditional && method == http.MethodGet && body == nil &&
		headerValue(headers, "If-None-Match") == "" {
		key = cacheKey(method, path, headers)
		if entry, ok := t.cache.lookup(key); ok {
			req.Header.Set("If-None-Match", entry.etag)
			cached, revalidn = entry, true
		}
	}

	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return ApiResponse{}, err
	}
	defer resp.Body.Close()

	// GitHub says the entity is byte-identical to the one held, so the
	// held one is the answer -- headers included, since a 304 carries no
	// `Link` and every paginated read here walks that header.
	if revalidn && resp.StatusCode == http.StatusNotModified {
		gate.opened(now)
		t.cache.touch(key)
		// Sent, and free: a 304 does not count against the primary rate
		// limit, and the ratio of these to everything else is the whole
		// evidence that revalidation is working.
		t.counts.record(endpointOf(path), true, false)
		t.counts.noteLimit(headerMap(resp.Header), now)
		t.ledgerOf().record(caller, method, path, true, false)
		t.ledgerOf().noteLimit(headerValue(headers, "Authorization"), headerMap(resp.Header), now)
		out := copyResponse(cached.resp)
		out.URL = method + " " + req.URL.String()
		return out, nil
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return ApiResponse{}, err
	}
	respHeaders := headerMap(resp.Header)
	out := ApiResponse{Status: resp.StatusCode, Headers: respHeaders, Body: data,
		URL: method + " " + req.URL.String()}
	// A caller that sent its own validator (CompletedWorkflowRuns keeps
	// one per repository) and got a 304 back was revalidated all the
	// same: GitHub does not charge it, so neither does Health's budget.
	callerRevalidated := out.Status == http.StatusNotModified && headerValue(headers, "If-None-Match") != ""
	t.counts.record(endpointOf(path), callerRevalidated, out.Status >= 400)
	t.counts.noteLimit(respHeaders, now)
	t.ledgerOf().record(caller, method, path, callerRevalidated, out.Status >= 400)
	t.ledgerOf().noteLimit(headerValue(headers, "Authorization"), respHeaders, now)

	if until, reason, throttled := throttledUntil(now, out); throttled {
		gate.shut(now, until, reason)
	} else {
		gate.opened(now)
	}
	if key != "" && out.Status == http.StatusOK {
		t.cache.store(key, resp.Header.Get("ETag"), out)
	}
	return out, nil
}

// FakeCall is one recorded call to FakeTransport.Request.
type FakeCall struct {
	Method  string
	Path    string
	Headers map[string]string
	Body    []byte
}

// FakeTransport replays scripted responses in order. For unit tests,
// including pagination -- a call site that needs more than one page
// queues one response per page. Default answers every call once
// Responses is empty, rather than erroring, so a test that doesn't care
// about a particular call's answer need not script one.
type FakeTransport struct {
	Responses []ApiResponse
	Default   ApiResponse
	Calls     []FakeCall
}

// NewFakeTransport returns a FakeTransport whose Default is an empty JSON
// array -- the common case for a list endpoint a test doesn't care about.
func NewFakeTransport(responses ...ApiResponse) *FakeTransport {
	return &FakeTransport{Responses: responses, Default: ApiResponse{Status: 200, Body: []byte("[]")}}
}

func (t *FakeTransport) Request(method, path string, headers map[string]string, body []byte) (ApiResponse, error) {
	t.Calls = append(t.Calls, FakeCall{Method: method, Path: path, Headers: headers, Body: body})
	if len(t.Responses) > 0 {
		resp := t.Responses[0]
		t.Responses = t.Responses[1:]
		return resp, nil
	}
	return t.Default, nil
}

// TokenSource resolves the API token to use for one repo. A structural
// interface, not a named implementation grain/proxy's credential ladder
// has to declare conformance to -- anything with this method satisfies it.
type TokenSource interface {
	TokenFor(owner, repo string) *string
}

// StaticToken is one token regardless of repo -- what NewClient wraps a
// bare *string in when a caller has no per-repo ladder to consult.
type StaticToken struct {
	Token *string
}

func (s StaticToken) TokenFor(owner, repo string) *string { return s.Token }

// GitOnlySource is a TokenSource that can say a repository has no forge
// API credential at all -- one reached with an SSH key and nothing paired
// to it (gitproxy.GitOnlyReason). GitOnly is that sentence, or "".
type GitOnlySource interface {
	GitOnly(owner, repo string) string
}

// GitBranchSource is a TokenSource that can read a repository's branch
// over git itself, for a repository on a host with no forge API to ask.
// ok is false when the repository's host has an API, and the caller asks
// that instead.
type GitBranchSource interface {
	GitBranchExists(owner, repo, branch string) (exists, ok bool, err error)
}

// gitOnly is the error opening or merging a pull request on owner/repo
// fails with, before anything is sent, when its credential covers git
// only: said plainly rather than as the 401 or 404 an unauthenticated
// call would come back with.
func (c *RESTClient) gitOnly(owner, repo string) error {
	if src, ok := c.Tokens.(GitOnlySource); ok {
		if reason := src.GitOnly(owner, repo); reason != "" {
			return errors.New(reason)
		}
	}
	return nil
}

// Error is the error a non-2xx GitHub response is reported as.
type Error struct {
	Status int
	Body   []byte
	// URL is ApiResponse.URL: the method and URL that were refused, or
	// empty when the transport does not say.
	URL string
	// Note is grain's own sentence about the refusal, when it knows more
	// than GitHub said (graphql: which endpoint refused, and whether REST
	// on the same connection still answers). Both renderings end with it.
	Note string
}

func (e *Error) Error() string {
	body := e.Body
	if len(body) > 200 {
		body = body[:200]
	}
	var out string
	if e.URL != "" {
		out = fmt.Sprintf("GitHub API error %d from %s: %q", e.Status, e.URL, body)
	} else {
		out = fmt.Sprintf("GitHub API error %d: %q", e.Status, body)
	}
	if e.Note != "" {
		out += " (" + e.Note + ")"
	}
	return out
}

// Message is the same refusal written for somebody to read: the status
// and GitHub's own sentence about it, without the JSON around it.
//
// Error, above, quotes the body whole and escaped, which is the right
// thing for a log somebody is debugging against -- it carries the
// `documentation_url` that says which endpoint refused, and the fields a
// predicate here keys on. It is the wrong thing on a pane: what an
// operator needs out of
//
//	GitHub API error 404: "{\r\n  \"message\": \"Not Found\",\r\n  \"documentation_url\": ...
//
// is the two words GitHub put three lines deep in it. So the surfaces a
// person reads render their GitHub failures through here instead, and
// get `GitHub answered 404: Not Found`: the State pane's reset notice
// and its journal line (pkg/cli/stateresets.go), the journal line
// about a state check nobody can read (cmd/grain/statechecks.go),
// pkg/mcp's CI tools, whose answers an agent acts on and a human reads
// back in a transcript (pkg/mcp/readable_github.go), and cmd/ghcr-prune,
// which prints its failures at whoever is reading the workflow log. The
// last three hand back an error rather than a string and reach this
// through Readable, below.
//
// The dividing line is who the rendering is for, not how it travels: a
// failure written down for whoever is debugging grain keeps Error's
// whole body, which is why the orchestrator's own logs, the sync path
// and every predicate here are untouched. A rendering meant to tell
// somebody what to do about it comes here.
//
// It is deliberately one helper rather than a change to Error: every
// caller in this repository logs that rendering, several tests pin it,
// and a body no message could be read out of is exactly when the whole
// of it is worth having. Anything this cannot make a sentence of falls
// back to it -- an unparseable body, a message that is not plain text,
// an error that did not come from GitHub at all -- so a caller can use
// this without checking what it was handed.
func Message(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if !errors.As(err, &e) {
		return err.Error()
	}
	if said := errorMessage(e.Body); said != "" {
		if e.Note != "" {
			return fmt.Sprintf("GitHub answered %d: %s (%s)", e.Status, said, e.Note)
		}
		return fmt.Sprintf("GitHub answered %d: %s", e.Status, said)
	}
	return e.Error()
}

// Readable is Message where an error rather than a string is what has to
// be handed back: the same sentence, with the GitHub error still
// underneath it.
//
// Message renders; this carries. The surfaces a person reads are not all
// reached from the call that made the request -- staterepo logs what a
// callback in cmd/grain returns (statechecks.go), pkg/mcp's CI tools
// hand an agent whatever their reads came back with, cmd/ghcr-prune
// prints one at an operator -- and each of those seams is typed `error`.
// Rendering there with Message means returning errors.New of a string,
// which throws away the *Error that IsPermissionDenied, IsNotFound and
// the rest key on: an identity nobody may be reading at that seam today,
// and the sort of thing whose absence is discovered by a predicate
// quietly answering false somewhere downstream. So this changes only the
// rendering. Error() is Message(err), Unwrap is err, errors.As finds the
// *Error exactly as before, and no seam has to change its contract to
// stop printing JSON at somebody.
//
// Wrap at the leaf, next to the call that failed, and add the context
// around the outside:
//
//	fmt.Errorf("listing versions of %s: %w", pkg, github.Readable(err))
//
// The other way round loses that context, since Message reads the
// *Error out of whatever it is handed and answers with GitHub's sentence
// alone.
//
// An error that is not GitHub's comes back untouched, so a caller can
// wrap without first asking what it is holding.
func Readable(err error) error {
	var e *Error
	if err == nil || !errors.As(err, &e) {
		return err
	}
	return readableError{err: err}
}

// readableError is what Readable wraps with: GitHub's sentence where the
// text is, and the original error everywhere else.
type readableError struct{ err error }

func (r readableError) Error() string { return Message(r.err) }

func (r readableError) Unwrap() error { return r.err }

// errorMessage reads GitHub's own wording out of an error body.
//
// The shape is the one every REST error here has: a `message`, and for
// the 422s a per-field `errors` array whose entries carry the part that
// actually says which field was wrong ("Validation Failed" alone sends
// nobody anywhere). Both are included, because dropping the array would
// make this rendering less useful than the raw body it replaces on
// exactly the failures where the body is worth reading.
func errorMessage(body []byte) string {
	var payload struct {
		Message string `json:"message"`
		Errors  []struct {
			Field   string `json:"field"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	said := payload.Message
	var details []string
	for _, item := range payload.Errors {
		switch {
		case item.Message != "":
			details = append(details, item.Message)
		case item.Field != "" && item.Code != "":
			details = append(details, item.Field+" "+item.Code)
		case item.Code != "":
			details = append(details, item.Code)
		}
	}
	if len(details) > 0 {
		said += " (" + strings.Join(details, "; ") + ")"
	}
	return plainSentence(said)
}

// plainSentence reduces GitHub's wording to something safe to repeat
// anywhere this failure is shown: collapsed onto one line and bounded,
// or dropped entirely if it is not plain text -- an HTML error page from
// something in front of GitHub parses as no JSON at all, but a message
// carrying control characters would still be a line of a journal nobody
// can read.
func plainSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" || !utf8.ValidString(s) {
		return ""
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return ""
		}
	}
	// By rune, so a cut cannot leave invalid UTF-8 behind.
	if runes := []rune(s); len(runes) > maxErrorMessage {
		s = string(runes[:maxErrorMessage]) + "..."
	}
	return s
}

// maxErrorMessage bounds what Message quotes, the same way Error bounds
// the body it quotes: past a sentence or two this is noise wrapped
// around the thing an operator is trying to read.
const maxErrorMessage = 200

// IsPermissionDenied reports whether err is GitHub refusing the call for
// want of a permission the credential can never hold -- a 403 whose body
// says "Resource not accessible by personal access token" (or "... by
// integration", the same refusal phrased for an App).
//
// It exists because one endpoint here can be unreachable by a whole
// class of credential: reading the Checks API needs the `repo` scope on
// a classic personal access token, or the "Checks" repository permission
// on a fine-grained one. A deployment whose credential has neither gets
// a 403 from ListCheckRuns on every call -- unlike an ordinary transient
// error, retrying never helps.
//
// The status alone is deliberately not enough. GitHub answers 403 for
// several conditions that are nothing to do with a missing permission
// and that do clear on their own or with a configuration change an
// operator can make: primary and secondary rate limits, SAML enforcement
// on an unauthorized token, and an organization IP allow list. Reading
// any of those as a permanent property of the credential would have a
// single transient 403 silently switch auto-merge off for the rest of
// the process's life (orchestrator.ChecksUnavailable latches), so
// anything this cannot positively identify as a missing permission stays
// an ordinary error, to fail loudly and be retried on the next cycle.
//
// A caller that can carry on without whatever the call would have told
// it should treat this as "unknown", never as "nothing found": the two
// differ exactly where it matters, since a 403 read as an empty result
// is indistinguishable from a genuinely clean answer.
//
// IsRateLimited (ratelimit.go) is the other half of that split, and the
// two partition 403 rather than overlapping on it: this one is "never
// going to work", that one is "will work again shortly". A test pins
// that neither ever answers for the other's refusal.
func IsPermissionDenied(err error) bool {
	var e *Error
	if !errors.As(err, &e) || e.Status != 403 {
		return false
	}
	var body struct {
		Message string `json:"message"`
	}
	// An unparseable or message-less body is not a permission denial we
	// can vouch for, so it stays an error -- the safe direction, since
	// the cost of a false negative here is one retried cycle and the
	// cost of a false positive is auto-merge off until a restart.
	if err := json.Unmarshal(e.Body, &body); err != nil {
		return false
	}
	return strings.HasPrefix(body.Message, "Resource not accessible by")
}

// IsNotFound reports whether err is GitHub's 404 -- which is two
// different answers wearing one status, and that is the whole reason
// this exists as a named thing rather than as `e.Status == 404` at a
// call site.
//
// GitHub does not tell a credential that a repository exists but is none
// of its business; it answers 404, the same as for a repository that
// genuinely is not there. So a deployment whose credential has lost its
// access to a repository -- revoked, an App uninstalled, a fine-grained
// token whose repository list was narrowed -- meets a 404 on the first
// call it makes there, not the 403 IsPermissionDenied was written for.
// That predicate correctly does not answer for this (it names a
// permission a credential can never hold, and latches auto-merge off on
// the strength of it), and this one deliberately does not either: 404
// cannot positively identify a credential fault, so nothing may switch
// itself off on it.
//
// What it is good for is a sentence: a caller that would otherwise tell
// an operator "there is no such thing" can say what GitHub actually
// refused and name both halves, so the operator whose credential lost
// its access is not left reading about a resource they can see with
// their own eyes.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == 404
}

// IsServerError reports whether err is GitHub having failed to answer
// rather than having answered "no" -- any 5xx.
//
// It is the third member of the family above, and the one that says
// "ask again": IsPermissionDenied is a credential that will never hold
// the permission, IsNotFound is a resource this credential cannot see,
// and this is GitHub itself having a bad minute. A caller that shows an
// operator a fault, or that latches anything on a failed call, wants to
// tell them apart -- the first two are a person's to fix and this one
// resolves without anybody doing anything.
//
// The status alone is enough here, unlike 403 and 422: 5xx is GitHub
// saying the request never got as far as an answer, and it means that
// whatever the body says. What arrives in practice is a mixture nothing
// could sniff anyway -- a JSON `{"message":"Server Error"}` on a 502, an
// empty body on a 500, GitHub's HTML error page on another 502, an Envoy
// "upstream connect error ... connection refused" on a 503 -- all of
// which this deployment's own daemon log carries examples of.
//
// Deliberately *not* including a transport-level failure that never
// reached GitHub (a refused connection, a DNS failure, a timeout). Those
// are not a *Error at all, so a caller that wants "anything but a real
// answer from GitHub" asks for this and for errors.As separately, and a
// caller that means the status asks only for this.
//
// It is also the predicate the transport's own one retry keys on: the
// status test is serverErrorStatus, shared with retry.go, so an error a
// caller would call transient and a response this package asks again is
// the same set of statuses by construction.
func IsServerError(err error) bool {
	var e *Error
	return errors.As(err, &e) && serverErrorStatus(e.Status)
}

// IsMergeConflict reports whether err is GitHub refusing a MergeBranch
// because the two branches genuinely conflict -- a 409 from the merges
// endpoint, which is unambiguous there in a way 403 is not for
// IsPermissionDenied above: that endpoint answers 409 for a merge
// conflict and for nothing else, so this needs none of that predicate's
// message-sniffing.
//
// A caller uses it to tell the one case an automatic merge cannot fix
// from the ordinary failures it should retry on the next cycle. Reading
// a conflict as a transient error would have the merge queue re-ask
// forever instead of asking a person; reading a transient error as a
// conflict would file a fix task describing a conflict that is not
// there.
func IsMergeConflict(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == 409
}

// ConflictFiles extracts any list of conflicting file paths reported in a 409
// merge conflict error response body.
func ConflictFiles(err error) []string {
	var e *Error
	if !errors.As(err, &e) || e.Status != 409 {
		return nil
	}
	var body struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(e.Body, &body); err != nil {
		return nil
	}
	return body.Files
}

// IsNoCommitsBetween reports whether err is GitHub refusing to open a
// pull request because its head carries nothing its base does not
// already have -- the 422 whose body says "No commits between <base> and
// <head>".
//
// It is the sibling of the vanished-base 422 that
// orchestrator.pullRequestBase already handles, and it is just as
// permanent: a branch that adds nothing to its base is refused on every
// attempt, so a caller that reads this as a transient failure retries a
// call that can never succeed. Telling the two apart is what lets the
// finish path say what actually happened instead of repeating the
// refusal.
//
// Message-sniffing, like IsPermissionDenied and unlike IsMergeConflict:
// 422 is the pulls endpoint's answer to every invalid field it has --
// a base that is not a branch, a head that is not there, a pull request
// that already exists -- so the status alone says nothing about which.
// GitHub reports this one as a per-field entry in `errors`, so that is
// what is read; an unparseable body, or one with no such entry, stays an
// ordinary error, which is the safe direction (a missed case costs one
// wasted retry, a false positive would end a task that had nothing wrong
// with it).
func IsNoCommitsBetween(err error) bool {
	var e *Error
	if !errors.As(err, &e) || e.Status != 422 {
		return false
	}
	var body struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(e.Body, &body); err != nil {
		return false
	}
	for _, item := range body.Errors {
		if strings.HasPrefix(item.Message, "No commits between") {
			return true
		}
	}
	return false
}

// MergeResult is what MergeBranch did. Merged is false, with a nil error,
// for GitHub's own 204: the base branch already contained the head, so
// nothing was written and there is no commit to name.
type MergeResult struct {
	Merged bool
	SHA    string
}

// Issue is one issue or pull request from the issues endpoint (GitHub
// unifies the two; ListIssues filters pull requests out itself).
type Issue struct {
	Number  int
	Title   string
	Body    string
	HTMLURL string
	Labels  map[string]struct{}
	// State is GitHub's own field, "open" or "closed" -- read by a
	// cancel-on-close poll to tell a task issue a human closed early from
	// one still open.
	State string
	// Author is the issue's own opening account's login -- who a poll
	// attributes a freshly filed model.Task to, since nothing else names
	// the actor a labelled issue's own filing should be credited to.
	Author string
}

// HasLabel reports whether name is one of Issue's labels.
func (i Issue) HasLabel(name string) bool {
	_, ok := i.Labels[name]
	return ok
}

func labelSet(names ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return set
}

// PullRequest is a freshly created (or found) pull request -- just enough
// to record and log. PullRequestDetail (below) is the wider shape a
// dispatch against an existing PR needs.
type PullRequest struct {
	Number  int
	HTMLURL string
}

// BranchHead is the tip of a branch: its sha and that commit's own
// message -- so a freshly opened PR's body can be seeded with the agent's
// own account of its change, the commit message it already wrote to
// explain the diff, rather than a generic, content-free line.
type BranchHead struct {
	SHA     string
	Message string
}

// Commit is one commit a branch carries that its base does not, as
// CompareCommits reports it -- its sha and its own message, which is
// where the agent that wrote it explained the change, and the one place
// a pull request description can be built from something other than
// metadata.
//
// Merge says the commit has more than one parent. It is carried rather
// than filtered here because only the caller knows whether it wants
// them: a description wants the commits that say what changed, and
// "Merge branch 'main' into grain/task-12" says nothing about the change
// at all.
//
// Parents is those parents' own shas, in GitHub's own order -- so the
// first is the branch the merge was made on and the rest are what was
// merged into it. It is what lets a reader find the merge that brought a
// branch into its base (CompareRefs' own doc comment) once "the commits
// head carries that base does not" has become the empty set.
type Commit struct {
	SHA     string
	Message string
	Merge   bool
	Parents []string
}

// Comparison is the whole of what GitHub's compare endpoint says about
// two refs, rather than only the commits between them: where they last
// met, how head stands relative to base, and those commits.
//
// Status is GitHub's own word -- "identical", "behind", "ahead" or
// "diverged" -- and is read as "base...head": "behind" and "identical"
// both mean base already contains head, which is what a merged branch
// looks like from here.
//
// MergeBase is the commit the two last had in common, which is the
// commit GitHub's own diff of base...head is taken from. Note that for a
// head base already contains, that is head's own tip -- which is why a
// merged pull request compares to nothing at all, and why reconstructing
// the fork point needs the merge commit rather than this.
type Comparison struct {
	Status    string
	MergeBase string
	Commits   []Commit
	// Files is GitHub's own list of the paths head changes against
	// MergeBase -- the three-dot diff a pull request shows -- each with
	// the blob it leaves there. It is what lets a caller ask whether
	// what a branch changes is already on its base by content rather
	// than by commit, which is the only question that survives a squash
	// merge (orchestrator's landed.go). GitHub caps the list at 300
	// files; FilesTruncated says when that cap may have been reached.
	Files          []ComparedFile
	FilesTruncated bool
}

// ComparedFile is one entry of a compare's file list: the path, GitHub's
// word for what happened to it ("added", "modified", "removed",
// "renamed", ...), the path it was renamed from, and the blob SHA the
// change leaves at that path (meaningless for "removed").
type ComparedFile struct {
	Filename         string
	PreviousFilename string
	Status           string
	SHA              string
}

// compareFileCap is the most files GitHub lists on one compare response.
const compareFileCap = 300

// PullRequestDetail is enough of a PR object to dispatch against it --
// title/body for the prompt, HeadRef for the branch to check out and push
// back to. Deliberately a separate type from PullRequest: widening that
// one would ripple into every existing call site for no benefit, since
// nothing there needs the extra fields.
type PullRequestDetail struct {
	Number  int
	Title   string
	Body    string
	HTMLURL string
	HeadRef string
	// HeadSHA is the PR head branch's current tip. Free on this same
	// response, and what scopes a workflow-run read to the commit the PR
	// actually points at -- filtering Actions runs by branch instead
	// would let a run from an older commit stand in for one the newest
	// push has not triggered yet, which is exactly the stale "passing"
	// that must never reach the merge gate.
	HeadSHA string
	BaseRef string
	// State is GitHub's own field, "open" or "closed" -- "closed" covers
	// both merged and closed-without-merging: a caller closing out a
	// finished PR treats them the same way (either one means nobody is
	// going to push more commits to it), but Merged below still tells
	// them apart for a caller that cares which.
	State string
	// Merged is GitHub's own field, meaningful only once State is
	// "closed" -- false the whole time a PR is open, same as GitHub's own
	// API returns. It is what lets a caller distinguish a merged PR from
	// one closed without merging, a distinction State alone collapses.
	Merged bool
	// MergedAt is GitHub's own field: when the PR merged, nil until
	// Merged is true. GitHub also returns it nil for a PR closed without
	// merging, which is the same nil this stays for that case.
	MergedAt *time.Time
	// CreatedAt is GitHub's own field: when the PR was opened. Present
	// for every PR regardless of state, unlike MergedAt.
	CreatedAt time.Time
	// Mergeable is GitHub's own field: true/false once it has finished
	// computing whether this PR can merge cleanly against its base, nil
	// while that computation is still in flight -- GitHub does this
	// asynchronously, so a request right after a push can legitimately see
	// nil for a cycle or two. A caller should read nil the same as "don't
	// know yet, check again next cycle," never as either a conflict or a
	// clean merge, since guessing either way could file a needless fix or
	// auto-merge something broken.
	Mergeable *bool
	// AuthorLogin is the login of the user who opened the pull request
	// (GitHub's user.login), "" when GitHub did not say.
	AuthorLogin string
	// HeadRepo is the "owner/name" the head branch lives in (GitHub's
	// head.repo.full_name): a fork's, for a pull request opened from one.
	// "" when GitHub did not say -- a deleted fork, or an older fake --
	// which a caller reads as the base repository itself.
	HeadRepo string
	// MaintainerCanModify is GitHub's maintainer_can_modify: whether a
	// fork's author lets the base repository's maintainers push to its
	// head branch. Meaningless for a same-repository pull request.
	MaintainerCanModify bool
	// MergeableState is GitHub's own summary of why a PR can or cannot
	// merge -- "clean", "unstable", "blocked", "dirty", "behind",
	// "draft", "unknown". Parsed and carried because it needs no
	// permission beyond the Pull requests read this response already
	// costs, unlike either CI read.
	//
	// Deliberately NOT wired into the merge gate -- now for a measured
	// reason rather than an open question. Confirmed against live pull
	// requests: bwsalmon/grain#544, a branch carrying one check that goes
	// red in seconds beside one that runs for five minutes, polled every
	// five seconds from the moment it opened until every check had
	// finished, and bwsalmon/agents#655 for the empty case below.
	//
	// It does account for check runs, not merely for the older commit
	// statuses. Every reading was taken on commits carrying *zero*
	// commit statuses -- Actions creates check runs and nothing else, and
	// /commits/{sha}/status stayed empty the whole run -- and a check
	// still running or finished red read "unstable" throughout, never
	// "clean". So trusting it would not have auto-merged a PR with red
	// CI, which was the question that kept it out of the gate. The other
	// half of that reading came later on the same pull request, once the
	// red check was actually repaired: with all six checks completed and
	// green it read "clean". So where checks exist, "clean" and
	// "unstable" do separate a passing run from a broken one.
	//
	// It still cannot replace defaultCheckRegistrationWindow, which was
	// the reason to want it. On a pull request no workflow watches --
	// agents#655, whose diff touches no path that repo's only
	// pull_request workflow is filtered to -- mergeable_state is "clean"
	// with zero check runs and zero statuses, and stayed "clean" for
	// thirty consecutive reads over two and a half minutes. That is
	// byte-identical to a repo whose CI has merely not registered yet,
	// because it is computed from the same empty list: GitHub is no
	// better placed to tell those apart than the Checks API is. The
	// clock in orchestrator.healthFrom stays.
	//
	// That window was caught open rather than argued from. Polling
	// grain#544 once a second across a push, on this repository, whose
	// every push runs CI: two seconds after the new head appeared the PR
	// read "unknown" with no checks; two seconds after that it read
	// "clean" with *zero* check runs; two seconds after that the first
	// check registered and it went back to "unstable". A merge gate
	// reading "clean" as passing would have had a four-second window,
	// per push, to merge a pull request whose tests had not been created
	// yet -- which is the exact failure defaultCheckRegistrationWindow
	// exists to prevent, not a reason to retire it.
	//
	// Nor does "unstable" separate pending from failing -- a check still
	// running and a check finished red both read "unstable" -- so it
	// could not stand in for the PrPending/PrFailing distinction either.
	//
	// Two further readings from the same run, for whoever reaches for
	// this next. It is "unknown", with Mergeable nil, for a second or two
	// after every push while GitHub recomputes, and "unknown" again once
	// the PR is closed or merged -- so it can never serve the
	// merged/closed distinction Merged draws. A conflicted PR reads
	// "dirty" (observed on grain#545), agreeing with Mergeable false.
	//
	// One caveat the run could not cover: grain's own repository requires
	// no status check, which is why a red check reads "unstable" there.
	// Where checks are required the same failure reads "blocked", and so
	// does a PR waiting on a required review -- so "blocked" carries more
	// than CI, and a caller reading these values across arbitrary target
	// repos cannot treat one repo's vocabulary as every repo's.
	MergeableState string
	// MergeCommitSHA is GitHub's own merge_commit_sha: once Merged, the
	// commit the merge left on the base -- the squash commit, for a
	// squash merge. Empty when GitHub did not say.
	MergeCommitSHA string
	// Additions, Deletions and ChangedFiles are how big this pull
	// request's diff is, as GitHub counts it against the merge base:
	// lines added, lines removed, and files touched. They are GitHub's
	// own fields, and they arrive on this response and no other -- a
	// list of pull requests does not carry them, which is why the
	// single-PR GET is the only place grain can learn the size of a
	// change without paying for a second call (docs/github-api-rate-
	// limits.md).
	//
	// Free, therefore, for the one caller that wants them: the merge
	// queue already fetches this response for every tracked pull
	// request on every sync cycle, and orchestrator's
	// recordPullRequestEvents copies these three onto the task's
	// observation the cycle it sees the merge, which is what lets a
	// week's release notes report lines changed at all
	// (model.ClusterStats).
	//
	// All three are zero on a response that did not carry them -- a
	// client standing in for GitHub that does not populate them, a
	// truncated body -- which is indistinguishable from a pull request
	// that really changed nothing. Since a merged pull request that
	// changed nothing at all is not a thing GitHub will produce, a
	// caller recording these treats all-zero as "not reported" rather
	// than as a measured zero.
	Additions    int
	Deletions    int
	ChangedFiles int
}

// Comment is a plain top-level comment on an issue or PR -- GitHub's own
// /issues/{number}/comments endpoint serves both, since a PR is a special
// kind of issue in its data model. Distinct from ReviewComment: those are
// inline, diff-attached; this is the ordinary conversation thread -- where
// a human's reply to an agent's ask_question call actually lands.
type Comment struct {
	ID int
	// User is the commenting account's login.
	User string
	Body string
	// AuthorAssociation is GitHub's own field ("OWNER", "MEMBER",
	// "COLLABORATOR", "CONTRIBUTOR", "NONE", ...) -- what lets a caller
	// tell a trusted reply from an arbitrary public comment before acting
	// on it, the same trust tier as "can apply a label." A random
	// commenter on a public repo must not be able to redispatch the agent
	// with content of their choosing -- that would reopen the exact
	// prompt-injection gate the trigger label exists to close.
	AuthorAssociation string
}

// ReviewComment is one inline (diff-attached) review comment -- GitHub's
// own term for what GET .../pulls/{number}/comments returns, distinct
// from a plain top-level issue/PR comment. Line is nil for a comment
// GitHub considers outdated (the API returns original_line instead in
// that case) -- left nil here rather than falling back to original_line,
// since telling the agent "line N" about a line the diff has since moved
// past would be actively misleading.
type ReviewComment struct {
	ID   int
	User string
	Body string
	Path string
	Line *int
	// InReplyTo is the ID of the comment this one answers, 0 for the
	// comment that opened its thread. GitHub threads replies under the
	// first comment of a thread only, so every reply in one thread
	// names the same root.
	InReplyTo int
	// ReviewID is the review this comment was posted as part of
	// (pull_request_review_id), 0 when GitHub did not say.
	ReviewID int
}

// ReviewThread is one inline review conversation on a pull request, as
// GitHub's GraphQL API reports it -- the unit a ruleset's "conversations
// must be resolved" rule counts, and the one thing the REST API has no
// view of. ID is the GraphQL node ID resolveReviewThread takes;
// CommentIDs are the REST (database) IDs of the comments in it, which is
// how a thread is found from a ReviewComment.
type ReviewThread struct {
	ID         string
	Resolved   bool
	Outdated   bool
	Path       string
	CommentIDs []int
}

// NewReviewComment is one inline comment to attach to a draft review
// (Client.CreateReview) -- the input shape for what ReviewComment above
// is the output shape of. A separate type rather than reusing
// ReviewComment itself: ID and User are GitHub's to assign once the
// review is created, never a caller's to supply.
//
// Line is the line number in the file's *new* version (the right-hand
// side of the diff) that GitHub's own reviews API expects a comments[]
// entry to name -- the same "new" side ReviewComment.Line already reads
// back once a comment exists.
type NewReviewComment struct {
	Path string
	Line int
	Body string
}

// CheckRun is one check run against a commit -- GitHub's own shape for CI
// results (a GitHub Actions job, or any third-party check that posts
// through the Checks API), read to decide whether an open PR's tests are
// failing.
//
// Status is GitHub's own lifecycle field: "queued", "in_progress", or
// "completed" -- only "completed" has a meaningful Conclusion at all,
// which is why a caller checks Status before ever looking at it.
// Conclusion is nil until then, and one of "success", "failure",
// "neutral", "cancelled", "skipped", "timed_out", or "action_required"
// once it is -- GitHub's own enum, not narrowed here, since which of
// those count as "broken" is a policy decision for the caller, not this
// package.
type CheckRun struct {
	Name       string
	Status     string
	Conclusion *string
	// HTMLURL is the check's own page on github.com -- the Actions job's
	// page for a check Actions posted, and whatever page the provider
	// chose for one it did not.
	//
	// It is here for the answer that has nothing else to offer: a failing
	// check with no readable Actions log (failing_job_logs.go) leaves a
	// reader holding a name, and a name is not somewhere they can go and
	// look. Empty is an ordinary value -- GitHub omits it on some checks,
	// and a caller renders the name alone when it does.
	HTMLURL string
	// FailureReason is GitHub's own explanation for a check that did not run
	// or failed at startup (from check run output title/summary/text).
	FailureReason string
	// StepsExecuted is the number of steps that actually executed in the job
	// (0 when GitHub failed the check before starting any step).
	StepsExecuted int

	// The rest is what recording this check as GitHub CI needs
	// (orchestrator.recordPolledChecks, model.GitHubCIJob), and comes in
	// the same response: ID is the check run's id (an Actions job's id),
	// App the slug of the App that posted it, Output its whole output --
	// title, summary and text -- bounded.
	ID          int64
	HeadSHA     string
	App         string
	StartedAt   *time.Time
	CompletedAt *time.Time
	Output      string
}

// JobLog is one failed GitHub Actions job and the tail of what it
// printed -- FailedJobLogs' unit.
//
// A check run says only that a job called "go" did not pass. That is
// enough to know a pull request is red and not enough to do anything
// about it, which is the gap this closes: the agent the merge queue
// files a fix task for reads the failure itself, in the fix task's own
// body, rather than being told a job name and left to go and find out
// what it said.
type JobLog struct {
	Name string
	// URL is the job's own page on github.com -- where the whole log
	// lives, for a reader who needs more than the tail below.
	URL string
	// Log is the last JobLogTailBytes of the job's log at most, cut at a
	// line boundary. The tail rather than the head because a job's log is
	// the whole job, every step of it, and the thing that broke is at the
	// end of it.
	Log string
	// Truncated reports whether Log is a tail rather than the whole log,
	// so a caller rendering it can say so.
	Truncated bool
	// RunConclusion marks this entry as standing for a whole workflow run
	// rather than for one job: GitHub failed the run and served no job
	// log under it, so Name is the workflow's name, URL is the run's own
	// page, and Log is empty. It holds GitHub's own conclusion for the
	// run -- "startup_failure" for a run that failed before a job of it
	// ever started.
	//
	// This exists because "no log" was the one answer a reader could do
	// nothing with. A run that fails at startup -- an unparseable
	// workflow file at this commit, Actions disabled or restricted for
	// the repository, a runner it may not use, a private repository out
	// of Actions minutes -- has no job, so it has no job log, and every
	// caller here rendered that identically to CI that is not Actions at
	// all. Which of those it is changes what the reader should do next
	// (fix the tree, or go and look at the repository's settings), so the
	// run's own verdict travels rather than being flattened into silence.
	RunConclusion string
	// RunJobsFailed is how many jobs of that run GitHub reports as
	// failed: zero when nothing ran, and non-zero when jobs did run and
	// fail but none of their logs could be read -- a log past Actions'
	// retention window, or a credential that may not fetch one. Only
	// meaningful alongside RunConclusion.
	RunJobsFailed int
	// RunJobsNeverStarted reports that not one of those failing jobs ran
	// a step, so there is no log because there was never anything to log.
	// Only meaningful when RunJobsFailed is non-zero.
	//
	// This is the third shape, and for a long time it was read as the
	// second: GitHub does not conclude a run `startup_failure` when it
	// declines to *start* a job it has already created -- no Actions
	// minutes, a spending limit reached, payments failed, no runner for
	// the labels the job asked for. It creates the job, concludes it
	// `failure` with an empty `steps` array, and concludes the run
	// `failure` too. From the outside that is indistinguishable from a
	// job that ran and failed with an unreadable log, which is what every
	// reader here told its audience it was -- and "reproduce it in a
	// checkout" is the one instruction that cannot work for it. The steps
	// array is the whole signal, so it travels.
	RunJobsNeverStarted bool
	// RunFailureReason is GitHub's own account of why this run failed,
	// read off the check run behind one of its jobs (its output, or its
	// annotations) when there was no log to read. Empty when GitHub said
	// nothing there or the credential may not read checks, in which case
	// every reader falls back to describing the class of failure rather
	// than its cause.
	//
	// It is one sentence and it is usually the whole answer: "The job was
	// not started because recent account payments have failed or your
	// spending limit needs to be increased" is a thing no amount of
	// reasoning from a conclusion and a job name arrives at.
	RunFailureReason string
}

// IsRunFailure reports whether this entry stands for a whole workflow
// run with no job log behind it rather than for one job's output. The
// two render differently everywhere both are shown, so the test for it
// lives here rather than being spelled out at each of those.
func (l JobLog) IsRunFailure() bool { return l.RunConclusion != "" }

// NoJobStarted reports whether GitHub failed this run without a single
// job of it running a step -- which is the difference between a failure
// a commit can own and a failure that belongs to the repository, and so
// the difference between a build worth sending an agent at and one worth
// waking a person for.
//
// Two shapes answer yes, and they are the same fact reported two ways by
// GitHub. A run with no jobs at all is `startup_failure`: it never got as
// far as creating one. A run whose jobs exist but ran no step is plain
// `failure`: GitHub created the jobs and then declined to start them (see
// RunJobsNeverStarted).
//
// A run with failing jobs that did run steps answers no however unreadable
// their logs are, and a `startup_failure` reported with failing jobs under
// it -- which GitHub does not produce, but this type can hold -- answers
// no on the same grounds: jobs that ran are jobs that could have been
// broken by the tree.
func (l JobLog) NoJobStarted() bool {
	switch {
	case !l.IsRunFailure():
		return false
	case l.RunJobsFailed == 0:
		return l.RunConclusion == StartupFailure
	default:
		return l.RunJobsNeverStarted
	}
}

// StartupFailure is the conclusion GitHub gives a workflow run that
// failed before any job of it began. Named because it is the one
// conclusion whose cause is certainly not in the tree the run was
// started against.
const StartupFailure = "startup_failure"

// JobLogTailBytes is how much of one job's log FailedJobLogs keeps. Big
// enough for a Go test failure's own output plus the surrounding
// package lines; small enough that four of them stay something a person
// (and a model's context window) can read.
const JobLogTailBytes = 16 << 10

// maxFailedJobLogs bounds how many failed jobs one FailedJobLogs call
// reads logs for. A commit that failed more jobs than this has something
// wrong that reading a fifth log will not add to.
const maxFailedJobLogs = 4

// runFailureReasonBytes bounds JobLog.RunFailureReason. GitHub's own
// reason for a run that never started is one sentence; a check run's
// summary is free-form markdown and some actions post a whole report
// into it, which is not what this field is for and not what any reader
// of it has room for.
const runFailureReasonBytes = 1 << 10

// maxReasonAnnotations bounds how many of a check run's annotations are
// carried when its output says nothing. The reason for a job that never
// started is the first one; a build with dozens of them has jobs that
// ran, which is the other case entirely.
const maxReasonAnnotations = 3

// JobLogTailLines is how many lines of a JobLog anything rendering one
// keeps. Enough for a Go test failure's own output and the package lines
// around it; short enough that four of them stay a thing a reader
// scrolls rather than a file they search.
//
// A second bound on top of JobLogTailBytes because the two answer
// different questions: that one is how much log crosses the wire, this
// one is how much of it a reader is shown. A log of very long lines
// fits the byte cap and still buries the failure.
const JobLogTailLines = 80

// actionsLogTimestamp is the RFC3339 stamp GitHub prefixes every line of
// a job log with. It is the same on every line, says nothing about the
// failure, and costs about a quarter of each line, so it comes off.
var actionsLogTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T[0-9:.]+Z `)

// JobLogExcerpt is one job's log as a reader is shown it: its last
// JobLogTailLines lines, without Actions' own per-line timestamps.
//
// It lives here, next to JobLog itself, because two callers render the
// same logs to two different audiences -- the comment the merge queue
// asks a repair with (orchestrator's requeueForRepair) and mcp's own CI
// answers (failingJobLogs) -- and a run reading its build break through
// one of them should not see a differently-bounded excerpt than the
// repair asked for that same break would carry.
func JobLogExcerpt(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > JobLogTailLines {
		lines = lines[len(lines)-JobLogTailLines:]
	}
	for i, line := range lines {
		lines[i] = strings.TrimRight(actionsLogTimestamp.ReplaceAllString(line, ""), "\r")
	}
	return strings.Join(lines, "\n")
}

// AnyJobLogRead reports whether any of these entries is a job's own
// output rather than a run standing in for jobs it has no log from --
// which is what a renderer needs to know before it introduces them as
// "what the failing jobs printed".
func AnyJobLogRead(logs []JobLog) bool {
	for _, l := range logs {
		if !l.IsRunFailure() {
			return true
		}
	}
	return false
}

// OnlyStartupFailures reports whether every failing run behind these
// entries is one GitHub concluded before a job of it ran a step, and
// there is at least one. It is the question "can pushing a different tree
// at this build make it green" -- answered "almost certainly not" -- and
// it lives here, beside StartupFailure itself, because it is a fact about
// what FailedJobLogs returns rather than about any one caller's policy.
//
// Which entries those are is JobLog.NoJobStarted's question, and it is
// not only the ones GitHub labelled `startup_failure`: a job GitHub
// created and then refused to start is concluded `failure` and is just
// as far outside the tree's reach. A mixed build -- one run dead before
// it started, another with a genuinely red job -- is not only a startup
// failure, and the red job is the part worth sending an agent at.
//
// An empty slice answers false: no failing Actions run was read at all,
// which is what a repo whose CI is not Actions, or a credential that may
// not read Actions, looks like from here. Neither says anything about
// whether the failure is fixable, so neither should be read as saying it
// is not.
func OnlyStartupFailures(logs []JobLog) bool {
	if len(logs) == 0 {
		return false
	}
	for _, l := range logs {
		if !l.NoJobStarted() {
			return false
		}
	}
	return true
}

// RunFailureNote is what stands where a log would be for a run GitHub
// failed without serving one.
//
// It is written for a reader who is about to go and reproduce the
// failure in a sandbox, because that is the wrong move for half of what
// lands here and there is no other way for them to tell: a startup
// failure is a fact about the repository -- its workflow file at this
// commit, whether it may run Actions at all, whether the account has
// minutes left -- and pushing a different tree at it changes none of
// them. A deployment has watched an agent spend a whole run doing
// exactly that, against a check that had never once passed on any tree.
//
// Here rather than in either caller, and for the reason JobLogExcerpt is
// here: both renderers of a JobLog (mcp's failingJobLogs, the
// orchestrator's) show the same runs to different audiences, and a run
// reading its own build break should not be told something different
// from the repair the merge queue asks for that same break.
func RunFailureNote(l JobLog) string {
	if l.RunConclusion == StartupFailure || l.RunJobsFailed == 0 || l.RunJobsNeverStarted {
		note := "GitHub concluded this whole run `" + l.RunConclusion + "` without any job of " +
			"it running a step, so there is no log to read and nothing in this commit's " +
			"tree is necessarily what broke. On the run's own page its jobs are drawn " +
			"with no steps under them, because no step of any of them was reached: \"it " +
			"has no steps\" there is what a run that never started looks like, and not a " +
			"workflow file that forgot to declare any. A run fails that way for reasons " +
			"that belong to the repository rather than to the change: a workflow file " +
			"GitHub cannot parse at this commit, Actions disabled or restricted for the " +
			"repository, no runner it may use, or an account with no Actions minutes left " +
			"-- a spending limit reached, or a payment that failed. Only the first of " +
			"those can be fixed from a branch, and a repository whose runs fail this way " +
			"on every commit has none of its checks working rather than a broken change. "
		if l.RunFailureReason != "" {
			return note + "GitHub has said which of them it is, in its own words:\n\n" +
				QuoteReason(l.RunFailureReason) + "\n\nAct on that sentence rather than on " +
				"the list above. If it names something only an operator can change, say so " +
				"and stop: no commit pushed to this branch reaches it."
		}
		return note + "The run's own page above carries GitHub's reason for it; a reader who " +
			"cannot reach that page should say so, and say what they have ruled out, " +
			"rather than push at it again."
	}
	note := fmt.Sprintf("GitHub concluded this run `%s` with %d failing job(s), each of which "+
		"ran at least one step, and served no log for any of them -- a log past Actions' "+
		"90-day retention window, or a credential that may not read one.",
		l.RunConclusion, l.RunJobsFailed)
	if l.RunFailureReason != "" {
		return note + " What the check run behind it says is all that survives of what it " +
			"printed:\n\n" + QuoteReason(l.RunFailureReason) + "\n\nIf that is not enough to " +
			"act on, reproduce the failure in a checkout."
	}
	return note + " The check names and the run's own page above are all there is; " +
		"reproduce the failure in a checkout instead."
}

// QuoteReason renders GitHub's own wording as a markdown blockquote, so a
// reader (and an agent) can tell the sentence GitHub wrote from the
// sentences written around it. Every line is quoted: a summary runs to
// several, and a half-quoted block renders as prose.
//
// Exported, and here rather than in a caller, for JobLogExcerpt's reason:
// the same sentence is shown to a run reading its own build break and to
// the person the merge queue wakes about it (orchestrator's
// escalateStartupFailure), and neither should see it dressed differently.
func QuoteReason(reason string) string {
	lines := strings.Split(strings.TrimSpace(reason), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight("> "+line, " ")
	}
	return strings.Join(lines, "\n")
}

// failedConclusion reports whether a completed run or job's conclusion is
// one of the three GitHub uses for "this did not pass" --
// orchestrator.failingChecks reads the same three, and the two lists
// have to keep agreeing: a job whose conclusion made a pull request
// PrFailing there but not "failed" here is one the fix task names and
// carries no log for.
func failedConclusion(conclusion string) bool {
	switch conclusion {
	case "failure", "timed_out", "startup_failure":
		return true
	}
	return false
}

// nextPagePath is the path+query of the rel="next" link, or "" on the
// last page.
//
// GitHub paginates via a full Link header URL rather than an opaque
// cursor; Transport.Request takes a path against a fixed host, so only
// the path and query survive the hop. On a GitHub Enterprise Server the
// Link URL's path begins with /api/v3/, which comes off here so a
// RealTransport with PathPrefix "/api/v3" does not put a second one in
// front of it.
func nextPagePath(linkHeader string) string {
	if linkHeader == "" {
		return ""
	}
	for _, part := range strings.Split(linkHeader, ",") {
		segment := strings.TrimSpace(part)
		if !strings.Contains(segment, `rel="next"`) {
			continue
		}
		start := strings.Index(segment, "<")
		end := strings.Index(segment, ">")
		if start == -1 || end == -1 || end < start {
			continue
		}
		u, err := url.Parse(segment[start+1 : end])
		if err != nil {
			continue
		}
		// EscapedPath, not Path: a segment the first request escaped (a
		// ref with a slash in it, sent as %2F) must reach the next page
		// still escaped, or page two asks for a different resource.
		path := u.EscapedPath()
		if strings.HasPrefix(path, "/api/v3/") {
			path = strings.TrimPrefix(path, "/api/v3")
		}
		if u.RawQuery != "" {
			return path + "?" + u.RawQuery
		}
		return path
	}
	return ""
}

// Client is the GitHub operations a grain deployment needs. RESTClient is
// the real implementation, talking through a Transport.
type Client interface {
	ListIssues(owner, repo, label string) ([]Issue, error)
	GetIssue(owner, repo string, number int) (Issue, error)
	AddLabel(owner, repo string, number int, label string) error
	RemoveLabel(owner, repo string, number int, label string) error
	CloseIssue(owner, repo string, number int) error
	ReopenIssue(owner, repo string, number int) error
	UpdateIssue(owner, repo string, number int, title, body *string) error
	BranchExists(owner, repo, branch string) (bool, error)
	GetBranchHead(owner, repo, branch string) (*BranchHead, error)
	CompareCommits(owner, repo, base, head string) ([]Commit, error)
	CompareRefs(owner, repo, base, head string) (Comparison, error)
	CreateBranch(owner, repo, branch, sha string) error
	UpdateBranch(owner, repo, branch, sha string, force bool) error
	DeleteBranch(owner, repo, branch string) error
	CreatePullRequest(owner, repo, head, base, title, body string) (PullRequest, error)
	UpdatePullRequestBody(owner, repo string, number int, body string) error
	UpdatePullRequestTitle(owner, repo string, number int, title string) error
	ClosePullRequest(owner, repo string, number int) error
	FindOpenPullRequestForBranch(owner, repo, branch string) (*PullRequest, error)
	MergedPullRequestForCommit(owner, repo, sha string) (*PullRequest, error)
	CreateIssue(owner, repo, title, body string, labels []string) (Issue, error)
	MergePullRequest(owner, repo string, number int, headSHA string) error
	MergeBranch(owner, repo, base, head, commitMessage string) (MergeResult, error)
	GetPullRequest(owner, repo string, number int) (PullRequestDetail, error)
	DefaultBranch(owner, repo string) (string, error)
	SetDefaultBranch(owner, repo, branch string) error
	ListReviewComments(owner, repo string, number int) ([]ReviewComment, error)
	ListPullRequestFiles(owner, repo string, number int) ([]string, error)
	ListPullRequestCommits(owner, repo string, number int) ([]Commit, error)
	ListCheckRuns(owner, repo, ref string) ([]CheckRun, error)
	ListWorkflowRuns(owner, repo, headSHA string) ([]CheckRun, error)
	FailedJobLogs(owner, repo, headSHA string) ([]JobLog, error)
	ActionsState(owner, repo, headSHA string) (ActionsState, error)
	RecentRunStarts(owner, repo string, limit int) (RunStarts, error)
	ListComments(owner, repo string, number int) ([]Comment, error)
	CreateComment(owner, repo string, number int, body string) (int, error)
	CreateReview(owner, repo string, number int, body string, comments []NewReviewComment) (int, error)
}

// RESTClient is Client talking to a real (or faked-at-the-Transport-level)
// GitHub REST API.
type RESTClient struct {
	Transport Transport
	Tokens    TokenSource
	// Caller is the label every request this client makes is charged to
	// in the process-wide ledger (budget.go). Empty is Unattributed; As
	// and WithCaller make a labelled copy.
	Caller string
}

// NewClient returns a RESTClient. tokens may be nil, meaning every
// request goes out unauthenticated -- a fine, deliberate credential shape
// for a public repo, not an error case.
func NewClient(transport Transport, tokens TokenSource) *RESTClient {
	return &RESTClient{Transport: transport, Tokens: tokens}
}

func (c *RESTClient) headers(owner, repo string, jsonBody bool) map[string]string {
	headers := map[string]string{
		"Accept":               "application/vnd.github+json",
		"User-Agent":           "grain-automation",
		"X-GitHub-Api-Version": "2022-11-28",
	}
	var token *string
	if c.Tokens != nil {
		token = c.Tokens.TokenFor(owner, repo)
	}
	if token != nil {
		headers["Authorization"] = "token " + *token
	}
	if jsonBody {
		headers["Content-Type"] = "application/json"
	}
	if c.Caller != "" {
		headers[CallerHeader] = c.Caller
	}
	if owner != "" && repo != "" {
		headers[RepoHeader] = owner + "/" + repo
	}
	return headers
}

// TokenRefusedSource is a TokenSource that wants to hear when a token it
// handed out was refused (401): a token fetched from the host's own login
// (gitproxy's ambient credential) is dropped from its cache, so the next
// request asks that login again.
type TokenRefusedSource interface {
	TokenRefused(token string)
}

// request is Transport.Request, telling a TokenRefusedSource about a 401.
func (c *RESTClient) request(method, path string, headers map[string]string, body []byte) (ApiResponse, error) {
	resp, err := c.Transport.Request(method, path, headers, body)
	if err == nil && resp.Status < 300 {
		// grain has just changed this pull request or branch itself, so
		// whatever polls it should look again now rather than at the end
		// of a backoff it built up while nothing was moving (poll.go).
		noteWrite(method, path)
	}
	if err == nil && resp.Status == http.StatusUnauthorized {
		if refused, ok := c.Tokens.(TokenRefusedSource); ok {
			if token, ok := strings.CutPrefix(headers["Authorization"], "token "); ok {
				refused.TokenRefused(token)
			}
		}
	}
	return resp, err
}

func (c *RESTClient) get(owner, repo, path string) (ApiResponse, error) {
	return c.request("GET", path, c.headers(owner, repo, false), nil)
}

// ListBranches returns every branch ref in a repository. Follow GitHub's
// pagination so large repositories do not get a silently partial picker.
func (c *RESTClient) ListBranches(owner, repo string) ([]string, error) {
	var branches []string
	path := fmt.Sprintf("/repos/%s/%s/branches?per_page=100", owner, repo)
	for path != "" {
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status != http.StatusOK {
			return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var page []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			return nil, err
		}
		for _, branch := range page {
			branches = append(branches, branch.Name)
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	return branches, nil
}

// ListIssues returns the open issues carrying label. Filters out pull
// requests -- the issues endpoint returns both, distinguished only by the
// presence of a pull_request key on the item.
func (c *RESTClient) ListIssues(owner, repo, label string) ([]Issue, error) {
	var issues []Issue
	path := fmt.Sprintf("/repos/%s/%s/issues?labels=%s&state=open&per_page=100",
		owner, repo, url.QueryEscape(label))
	for path != "" {
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status != 200 {
			return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var items []map[string]json.RawMessage
		if err := json.Unmarshal(resp.Body, &items); err != nil {
			return nil, err
		}
		for _, raw := range items {
			if _, isPR := raw["pull_request"]; isPR {
				continue
			}
			issue, err := decodeIssue(raw)
			if err != nil {
				return nil, err
			}
			issues = append(issues, issue)
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	return issues, nil
}

type issueJSON struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Labels  []struct {
		Name string `json:"name"`
	} `json:"labels"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

func decodeIssue(raw map[string]json.RawMessage) (Issue, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return Issue{}, err
	}
	var parsed issueJSON
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Issue{}, err
	}
	names := make([]string, len(parsed.Labels))
	for i, l := range parsed.Labels {
		names[i] = l.Name
	}
	state := parsed.State
	if state == "" {
		state = "open"
	}
	return Issue{
		Number: parsed.Number, Title: parsed.Title, Body: parsed.Body,
		HTMLURL: parsed.HTMLURL, Labels: labelSet(names...), State: state,
		Author: parsed.User.Login,
	}, nil
}

// GetIssue fetches a single issue fresh -- used to read an issue's
// current title when it isn't otherwise on hand (e.g. PR creation, which
// only carries the issue number, not its title).
func (c *RESTClient) GetIssue(owner, repo string, number int) (Issue, error) {
	resp, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number))
	if err != nil {
		return Issue{}, err
	}
	if resp.Status != 200 {
		return Issue{}, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return Issue{}, err
	}
	return decodeIssue(raw)
}

// AddLabel puts label on an issue or pull request, leaving its other
// labels where they are.
func (c *RESTClient) AddLabel(owner, repo string, number int, label string) error {
	body, _ := json.Marshal(map[string][]string{"labels": {label}})
	resp, err := c.request(
		"POST", fmt.Sprintf("/repos/%s/%s/issues/%d/labels", owner, repo, number),
		c.headers(owner, repo, true), body,
	)
	if err != nil {
		return err
	}
	if resp.Status != 200 && resp.Status != 201 {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}

// quoteKeepSlash percent-encodes s the way Python's urllib.parse.quote
// does with its default safe="/": every reserved character except "/"
// itself. url.PathEscape encodes "/" too (it's meant for a single
// segment), so this escapes segment-by-segment instead.
func quoteKeepSlash(s string) string {
	parts := strings.Split(s, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// quoteAll percent-encodes s the way Python's urllib.parse.quote(s,
// safe="") does: every byte except the unreserved set (ASCII letters,
// digits, and "_.-~") is escaped -- including "/" and ":", which
// url.PathEscape leaves alone since it allows both within a single path
// segment per RFC 3986. Needed wherever a call builds a qualified
// owner:branch value, since a literal ":" there would otherwise reach
// GitHub unescaped.
func quoteAll(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '_' || c == '.' || c == '-' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// RemoveLabel takes label off an issue or pull request.
//
// The label is one path segment, so it is escaped with quoteAll: a "/" in
// a label's name left bare would address a different path, which GitHub
// answers 404 -- read below as "already off", so the label would stay on
// and nobody would hear about it.
func (c *RESTClient) RemoveLabel(owner, repo string, number int, label string) error {
	resp, err := c.request(
		"DELETE", fmt.Sprintf("/repos/%s/%s/issues/%d/labels/%s", owner, repo, number, quoteAll(label)),
		c.headers(owner, repo, false), nil,
	)
	if err != nil {
		return err
	}
	// 404 means the label is already off the issue -- a fine outcome, not
	// an error, since the caller's intent ("this label should not be on
	// there") is already satisfied.
	if resp.Status != 200 && resp.Status != 404 {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}

func (c *RESTClient) setIssueState(owner, repo string, number int, state string) error {
	body, _ := json.Marshal(map[string]string{"state": state})
	resp, err := c.request(
		"PATCH", fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number),
		c.headers(owner, repo, true), body,
	)
	if err != nil {
		return err
	}
	if resp.Status != 200 {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}

// CloseIssue closes a task issue directly. A fully qualified "Closes
// owner/repo#N" in a PR body only auto-closes within the *same* repo it's
// opened in -- across repos (the task/target split's normal case) it just
// links, never closes -- so a caller closes the task issue itself rather
// than relying on the PR body text to do it.
func (c *RESTClient) CloseIssue(owner, repo string, number int) error {
	return c.setIssueState(owner, repo, number, "closed")
}

// UpdateIssue edits an issue's title and/or body in place, leaving
// anything else about it (labels, state) untouched -- a caller wanting
// to close or relabel too makes those calls separately. Both nil is the
// caller's own mistake to avoid; this method sends whichever of the two
// it is given, since GitHub's PATCH endpoint leaves an omitted field as
// it was.
func (c *RESTClient) UpdateIssue(owner, repo string, number int, title, body *string) error {
	fields := map[string]string{}
	if title != nil {
		fields["title"] = *title
	}
	if body != nil {
		fields["body"] = *body
	}
	data, _ := json.Marshal(fields)
	resp, err := c.request(
		"PATCH", fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number),
		c.headers(owner, repo, true), data,
	)
	if err != nil {
		return err
	}
	if resp.Status != 200 {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}

// ReopenIssue reopens a task issue GitHub had closed -- a human reviewing
// "done" work may come back with a follow-up comment instead of
// relabelling it, and this is the other half of CloseIssue, just the
// other state value.
func (c *RESTClient) ReopenIssue(owner, repo string, number int) error {
	return c.setIssueState(owner, repo, number, "open")
}

func (c *RESTClient) getBranch(owner, repo, branch string) (ApiResponse, error) {
	// GitHub's branch-get endpoint requires "/" within the branch name
	// itself percent-encoded (%2F), not left as a path separator --
	// quoteAll is what does that.
	return c.get(owner, repo, fmt.Sprintf("/repos/%s/%s/branches/%s", owner, repo, quoteAll(branch)))
}

// BranchExists reports whether branch is really on the remote.
//
// The design decision behind why this exists at all: a dispatch tells the
// agent exactly what branch to push to, but the prompt it received came
// from untrusted issue content, so the caller confirms the branch is real
// via the API before opening a PR against it rather than trusting the
// agent's own report of what it did.
func (c *RESTClient) BranchExists(owner, repo, branch string) (bool, error) {
	resp, err := c.getBranch(owner, repo, branch)
	if err != nil {
		return false, err
	}
	if resp.Status == 200 {
		return true, nil
	}
	if resp.Status == 404 {
		return false, nil
	}
	return false, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
}

// GetBranchHead returns branch's tip commit, or nil if the branch doesn't
// exist -- the same GET BranchExists makes, kept as its own method rather
// than folded into it since most callers only ever need the boolean and
// this one's response also carries the head commit's own message, which
// only a fresh-PR path needs, to seed a real description instead of a
// generic one.
func (c *RESTClient) GetBranchHead(owner, repo, branch string) (*BranchHead, error) {
	resp, err := c.getBranch(owner, repo, branch)
	if err != nil {
		return nil, err
	}
	if resp.Status == 404 {
		return nil, nil
	}
	if resp.Status != 200 {
		return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		Commit struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
			} `json:"commit"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return nil, err
	}
	return &BranchHead{SHA: data.Commit.SHA, Message: data.Commit.Commit.Message}, nil
}

// CompareCommits returns the commits head carries that base does not,
// oldest first -- git's own `base..head`, which is exactly the set of
// commits a pull request from head into base would contain, and so
// exactly the set whose messages describe it.
//
// Two refs, not one branch head: a description built from the tip commit
// alone is at the mercy of whatever the last push happened to be, and on
// grain's own push/check/repair loop that is routinely "Fix the vet
// warning" rather than the commit that explains the change.
//
// GitHub caps this response at 250 commits and pages beyond that; no
// pagination here, because a caller reading commit messages for a
// description has long since had more than it can use by then, and a
// grain branch that carries 250 commits has a bigger problem than its
// description.
func (c *RESTClient) CompareCommits(owner, repo, base, head string) ([]Commit, error) {
	comparison, err := c.CompareRefs(owner, repo, base, head)
	if err != nil {
		return nil, err
	}
	return comparison.Commits, nil
}

// CompareRefs is that same request read whole -- the status, the merge
// base and the commits (Comparison above) rather than the commits alone.
//
// It exists for the one reader that has to work out where a branch
// forked from its base *after* the two have been merged: the UI's own
// pull-request view, which is asked to draw a change long after the
// commits between the refs have become the empty set. That reader walks
// the commits base has and head does not, finds the one whose Parents
// name head's tip -- the merge that brought the branch in -- and takes
// its first parent as the base as it stood, which is a comparison GitHub
// can still answer.
//
// Capped at GitHub's own 250 commits, like CompareCommits: a merge that
// landed more than 250 commits ago is one this cannot find, and the
// caller is expected to fall back rather than fail.
func (c *RESTClient) CompareRefs(owner, repo, base, head string) (Comparison, error) {
	// quoteKeepSlash, not quoteAll: the compare endpoint takes both refs
	// in one path segment separated by "...", and a branch name's own "/"
	// (grain/task-12, the shape model.BranchName produces) is a literal
	// slash there, the same way github.com's own compare URLs spell it.
	path := fmt.Sprintf("/repos/%s/%s/compare/%s...%s",
		owner, repo, quoteKeepSlash(base), quoteKeepSlash(head))
	resp, err := c.get(owner, repo, path)
	if err != nil {
		return Comparison{}, err
	}
	if resp.Status != 200 {
		return Comparison{}, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		Status    string `json:"status"`
		MergeBase struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
		Commits []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
			} `json:"commit"`
			Parents []struct {
				SHA string `json:"sha"`
			} `json:"parents"`
		} `json:"commits"`
		Files []struct {
			Filename         string `json:"filename"`
			PreviousFilename string `json:"previous_filename"`
			Status           string `json:"status"`
			SHA              string `json:"sha"`
		} `json:"files"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return Comparison{}, err
	}
	files := make([]ComparedFile, 0, len(data.Files))
	for _, f := range data.Files {
		files = append(files, ComparedFile{Filename: f.Filename, PreviousFilename: f.PreviousFilename,
			Status: f.Status, SHA: f.SHA})
	}
	commits := make([]Commit, 0, len(data.Commits))
	for _, item := range data.Commits {
		parents := make([]string, 0, len(item.Parents))
		for _, parent := range item.Parents {
			parents = append(parents, parent.SHA)
		}
		commits = append(commits, Commit{
			SHA:     item.SHA,
			Message: item.Commit.Message,
			Merge:   len(parents) > 1,
			Parents: parents,
		})
	}
	return Comparison{Status: data.Status, MergeBase: data.MergeBase.SHA, Commits: commits,
		Files: files, FilesTruncated: len(files) >= compareFileCap}, nil
}

// DiffReader is the optional half of Client that serves raw diffs:
// CompareDiff and PullRequestDiff below. Optional for MergeRulesReader's
// reason -- the security hold's carried clearance
// (orchestrator.carryClearance) and the UI's branch comparison are its
// only callers, and both find it by type assertion. Which means a client
// wrapping another has to pass it through (as the daemon's
// StateRepoPullRequestClient does), or every clearance is quietly
// reviewed again: the carry fails closed into an ordinary hold.
type DiffReader interface {
	CompareDiff(owner, repo, base, head string) (string, error)
	PullRequestDiff(owner, repo string, number int) (string, error)
}

var _ DiffReader = (*RESTClient)(nil)

// CompareDiff returns the patch head would bring to base. It uses GitHub's
// raw diff representation of the compare endpoint rather than rebuilding a
// patch from the JSON response's per-file snippets: those snippets may be
// omitted for binary and very large changes, while this is the same complete
// comparison GitHub renders for a pull request before one has been opened.
//
// This deliberately is not part of Client. Reconciliation only needs commit
// metadata from CompareCommits; the wider response exists for the optional UI
// comparison surface, whose small interface is declared at its own boundary.
func (c *RESTClient) CompareDiff(owner, repo, base, head string) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/compare/%s...%s",
		owner, repo, quoteKeepSlash(base), quoteKeepSlash(head))
	headers := c.headers(owner, repo, false)
	headers["Accept"] = "application/vnd.github.diff"
	resp, err := c.request("GET", path, headers, nil)
	if err != nil {
		return "", err
	}
	if resp.Status != 200 {
		return "", &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return string(resp.Body), nil
}

// PullRequestDiff returns the pull request's own diff, the raw one GitHub
// renders on its Files tab: between its head and where that head forked
// from its base. Unlike a comparison of the two refs, GitHub goes on
// answering it once the pull request has merged and its base contains its
// head, which is when the security hold reads a port's source
// (orchestrator.inheritedClearance).
//
// Like CompareDiff, this is not part of Client: its one caller asserts
// for it and does without when a client cannot say.
func (c *RESTClient) PullRequestDiff(owner, repo string, number int) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number)
	headers := c.headers(owner, repo, false)
	headers["Accept"] = "application/vnd.github.diff"
	resp, err := c.request("GET", path, headers, nil)
	if err != nil {
		return "", err
	}
	if resp.Status != 200 {
		return "", &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return string(resp.Body), nil
}

// FileReader is an optional Client capability for reading a single file's raw
// contents from a repository at a given git ref (branch, tag, or commit SHA).
// It returns (nil, false, nil) when the file does not exist at ref.
//
// Like DiffReader, callers assert for it on their github.Client, and wrappers
// such as StateRepoPullRequestClient implement it directly against the local
// config repository checkout.
type FileReader interface {
	ReadFile(owner, repo, ref, filePath string) ([]byte, bool, error)
}

var _ FileReader = (*RESTClient)(nil)

// ReadFile returns the raw bytes of filePath at ref in owner/repo, or
// (nil, false, nil) when the file is absent (HTTP 404).
func (c *RESTClient) ReadFile(owner, repo, ref, filePath string) ([]byte, bool, error) {
	clean := strings.TrimPrefix(strings.TrimSpace(filePath), "/")
	if clean == "" {
		return nil, false, nil
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/contents/%s", owner, repo, quoteKeepSlash(clean))
	if strings.TrimSpace(ref) != "" {
		endpoint += "?ref=" + url.QueryEscape(strings.TrimSpace(ref))
	}
	headers := c.headers(owner, repo, false)
	headers["Accept"] = "application/vnd.github.raw+json"
	resp, err := c.request("GET", endpoint, headers, nil)
	if err != nil {
		return nil, false, err
	}
	switch resp.Status {
	case 200:
		return resp.Body, true, nil
	case 404:
		return nil, false, nil
	default:
		return nil, false, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
}

// CreateBranch creates branch pointing at sha -- release management's own
// "cut" (bwsalmon/agents#398): a fresh, never-before-existing ref, via
// GitHub's git-database API rather than the higher-level "create from" a
// human uses on github.com, since the caller already has the exact
// commit in hand (typically another branch's own GetBranchHead) and has
// no reason to ask GitHub to resolve one itself. GitHub answers 422 if
// branch already exists; that reaches the caller as an *Error the same
// as any other non-201 status, since a caller wanting "create or move"
// has UpdateBranch for the second half.
func (c *RESTClient) CreateBranch(owner, repo, branch, sha string) error {
	payload, _ := json.Marshal(map[string]string{"ref": "refs/heads/" + branch, "sha": sha})
	resp, err := c.request(
		"POST", fmt.Sprintf("/repos/%s/%s/git/refs", owner, repo),
		c.headers(owner, repo, true), payload,
	)
	if err != nil {
		return err
	}
	if resp.Status != 201 {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}

// UpdateBranch moves branch's tip to sha -- release management's other
// half of a cut (repointing the repo's own moving rc branch at a freshly
// cut candidate) and of a promotion (fast-forwarding ProdBranch to the
// candidate being promoted). force matches GitHub's own field: false
// refuses a move that is not a fast-forward, true moves it regardless --
// a caller doing the former should treat a refusal (422) as a caller
// error, not a transient failure to retry unmodified.
func (c *RESTClient) UpdateBranch(owner, repo, branch, sha string, force bool) error {
	payload, _ := json.Marshal(map[string]any{"sha": sha, "force": force})
	resp, err := c.request(
		"PATCH", fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", owner, repo, branch),
		c.headers(owner, repo, true), payload,
	)
	if err != nil {
		return err
	}
	if resp.Status != 200 {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}

// DeleteBranch removes branch entirely. It is for a ref grain owns and
// nobody has a pull request against -- the merge queue's own throwaway
// batch ref (model.MergeBatchRef), destroyed when the batch it stood for
// is dropped part-way through merging (orchestrator.dropBatchRef).
//
// A branch that is already gone is not an error: GitHub answers 422 for a
// ref that does not exist, and "make sure this is not there" is what every
// caller means. Anything else reaches the caller as an *Error like any
// other status.
func (c *RESTClient) DeleteBranch(owner, repo, branch string) error {
	resp, err := c.request(
		"DELETE", fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", owner, repo, branch),
		c.headers(owner, repo, false), nil,
	)
	if err != nil {
		return err
	}
	switch resp.Status {
	case 204, 404, 422:
		return nil
	}
	return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
}

// CreatePullRequest opens a pull request from head into base.
func (c *RESTClient) CreatePullRequest(owner, repo, head, base, title, body string) (PullRequest, error) {
	if err := c.gitOnly(owner, repo); err != nil {
		return PullRequest{}, err
	}
	payload, _ := json.Marshal(map[string]string{
		"title": title, "head": head, "base": base, "body": body,
	})
	resp, err := c.request(
		"POST", fmt.Sprintf("/repos/%s/%s/pulls", owner, repo),
		c.headers(owner, repo, true), payload,
	)
	if err != nil {
		return PullRequest{}, err
	}
	if resp.Status != 201 {
		return PullRequest{}, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return PullRequest{}, err
	}
	return PullRequest{Number: data.Number, HTMLURL: data.HTMLURL}, nil
}

// UpdatePullRequestBody rewrites a pull request's description in place,
// leaving its title, base and everything else untouched -- the pulls
// endpoint rather than the issues one, since a body is the only thing
// here that is ever rewritten and PATCH .../pulls/{n} is where GitHub
// documents it.
//
// It exists for a pull request grain opened before the run that owns it
// had finished pushing (OpenPullRequestForTask): the description written
// then describes the commits that existed then, and the run's later
// commits are the ones a reviewer needs to read about.
func (c *RESTClient) UpdatePullRequestBody(owner, repo string, number int, body string) error {
	payload, _ := json.Marshal(map[string]string{"body": body})
	resp, err := c.request(
		"PATCH", fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number),
		c.headers(owner, repo, true), payload,
	)
	if err != nil {
		return err
	}
	if resp.Status != 200 {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}

// UpdatePullRequestTitle renames a pull request in place, leaving its
// body, base and everything else untouched -- UpdatePullRequestBody's
// sibling, on the same PATCH .../pulls/{n} endpoint and there for the
// same reason.
//
// The reason is that grain names a pull request after the change on its
// branch rather than after the task that asked for it
// (orchestrator.titleFromCommits): a task saying "check the parser for
// bugs" earns a pull request called "Fix the off-by-one in the header
// parser", and which commits the branch carries is a thing that keeps
// changing while the run that owns it is still pushing.
func (c *RESTClient) UpdatePullRequestTitle(owner, repo string, number int, title string) error {
	payload, _ := json.Marshal(map[string]string{"title": title})
	resp, err := c.request(
		"PATCH", fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number),
		c.headers(owner, repo, true), payload,
	)
	if err != nil {
		return err
	}
	if resp.Status != 200 {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}

// ClosePullRequest closes a pull request without merging it -- PATCH
// .../pulls/{n} with GitHub's own `state`, the same field CloseIssue
// sets on an issue, on the endpoint that owns a pull request's state.
//
// The one call in grain that throws work away rather than making it, so
// it is worth being exact about what it does and does not do: closing a
// pull request on GitHub leaves the branch and every commit on it
// untouched, and reopening the pull request restores it whole. Nothing
// here deletes a branch.
//
// It is called on exactly one path -- a human closing a task who asked,
// in the same breath, for its pull request to be closed with it
// (ui.CloseOptions.ClosePullRequest). Nothing grain decides for itself
// ever reaches this: a task whose work landed has a pull request that
// merged, and a task closed without that explicit ask leaves its pull
// request open for a human (model.OrphanedPullRequestNote).
//
// A pull request that is already closed is not an error: GitHub accepts
// the PATCH and answers 200 with the state it already had, which is the
// caller's intent already satisfied -- the same tolerance CloseIssue
// relies on when a close is retried.
func (c *RESTClient) ClosePullRequest(owner, repo string, number int) error {
	payload, _ := json.Marshal(map[string]string{"state": "closed"})
	resp, err := c.request(
		"PATCH", fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number),
		c.headers(owner, repo, true), payload,
	)
	if err != nil {
		return err
	}
	if resp.Status != 200 {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}

// FindOpenPullRequestForBranch returns the open PR whose head is branch,
// if there is one.
//
// GitHub allows at most one open PR per head branch, so this is either
// exactly one or none -- used to recognise "the PR for this task already
// exists" after a CreatePullRequest 422, rather than failing a finish
// that has in fact already succeeded.
//
// head is qualified owner:branch the way GitHub's own filter requires;
// grain only ever pushes to branches in the target repo itself (never a
// fork), so the head owner is always owner.
func (c *RESTClient) FindOpenPullRequestForBranch(owner, repo, branch string) (*PullRequest, error) {
	return c.FindOpenPullRequestForHead(owner, repo, owner+":"+branch)
}

// FindOpenPullRequestForHead returns the open PR on owner/repo whose head
// label is head ("<head-owner>:<branch>", GitHub's head.label), if there
// is one. Unlike FindOpenPullRequestForBranch the head owner is the
// caller's to say, so it finds a pull request opened from a fork.
func (c *RESTClient) FindOpenPullRequestForHead(owner, repo, head string) (*PullRequest, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&head=%s",
		owner, repo, quoteAll(head))
	resp, err := c.get(owner, repo, path)
	if err != nil {
		return nil, err
	}
	if resp.Status != 200 {
		return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var items []struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(resp.Body, &items); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return &PullRequest{Number: items[0].Number, HTMLURL: items[0].HTMLURL}, nil
}

// MergedPullRequestForCommit answers "which merged pull request did this
// commit arrive in", or nil for a commit that arrived some other way --
// a push straight at the branch, which is a perfectly ordinary way for a
// commit to exist and not something to report as a failure.
//
// The endpoint behind it lists every pull request a commit is associated
// with, open ones included: a branch whose tip is this commit has an
// open pull request that "contains" it without having delivered it. Only
// a merged one delivered it, so only a merged one is answered with --
// the caller is looking for the conversation the change was made in, and
// commenting on an unrelated open pull request that happens to sit on
// the same commit would be talking to the wrong people.
//
// The first merged one when there is more than one, which happens when a
// branch is merged twice over (a revert and a re-merge, a backport):
// GitHub lists them newest first, and the newest is the merge that put
// the commit where the caller found it.
func (c *RESTClient) MergedPullRequestForCommit(owner, repo, sha string) (*PullRequest, error) {
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/pulls?per_page=100", owner, repo, quoteAll(sha))
	resp, err := c.get(owner, repo, path)
	if err != nil {
		return nil, err
	}
	if resp.Status != 200 {
		return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var items []struct {
		Number   int     `json:"number"`
		HTMLURL  string  `json:"html_url"`
		MergedAt *string `json:"merged_at"`
	}
	if err := json.Unmarshal(resp.Body, &items); err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.MergedAt == nil || *item.MergedAt == "" {
			continue
		}
		return &PullRequest{Number: item.Number, HTMLURL: item.HTMLURL}, nil
	}
	return nil, nil
}

// CreateIssue files a new issue on the task repo. labels lets it land
// already carrying a needs-approval label in the same request, rather
// than a second AddLabel call that could fail partway and leave the issue
// unlabelled.
func (c *RESTClient) CreateIssue(owner, repo, title, body string, labels []string) (Issue, error) {
	payload := map[string]any{"title": title, "body": body}
	if len(labels) > 0 {
		payload["labels"] = labels
	}
	data, _ := json.Marshal(payload)
	resp, err := c.request(
		"POST", fmt.Sprintf("/repos/%s/%s/issues", owner, repo),
		c.headers(owner, repo, true), data,
	)
	if err != nil {
		return Issue{}, err
	}
	if resp.Status != 201 {
		return Issue{}, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return Issue{}, err
	}
	return decodeIssue(raw)
}

// MergePullRequest merges a PR directly, rather than leaving it for a
// human to click. A 405 (not mergeable -- GitHub's own answer if
// Mergeable went stale between the read and this call) or 409 (base
// branch moved underneath it, or headSHA no longer matches) is left for
// the caller to decide whether to retry next cycle via the returned
// *Error's Status; only a genuinely unexpected status is a surprise here.
//
// headSHA, when non-empty, goes in the body as GitHub's own `sha` field
// -- "SHA that pull request head must match to allow merge" -- and makes
// this a merge of one named commit rather than of whatever the head
// branch points at by the time the request lands. A caller that decided
// to merge by reading a commit's CI wants exactly that: without it, a
// push landing in the gap between the read and this call is merged
// untested, and no answer the caller got back could have told it so.
// GitHub answers 409 when the branch has moved, which is the caller's cue
// to look again at the commit that is there now.
//
// Empty means unpinned, the old behaviour: for a caller merging on a
// human's say-so rather than on a verdict it computed itself, there is no
// commit the merge has to match, and pinning one would only refuse a
// merge the human asked for.
func (c *RESTClient) MergePullRequest(owner, repo string, number int, headSHA string) error {
	return c.MergePullRequestWithMessage(owner, repo, number, headSHA, "", "")
}

// MergeBranch merges head into base server-side, both named as branches
// -- GitHub's own POST /repos/{owner}/{repo}/merges, the write behind the
// "Update branch" button when the merge queue brings a pull request's own
// branch up to date with the branch it targets.
//
// Named by branch rather than by sha on purpose: GitHub takes whatever
// head's tip is at the moment it processes the call, which is fresher
// than any revision this caller could have read a moment earlier, and a
// merge of the base into a branch has no equivalent of MergePullRequest's
// "only if the head still matches" to protect -- there is nothing being
// judged on a commit's CI here, only a branch being caught up.
//
// Its three answers are three status codes, which is why this is the
// endpoint rather than PUT .../pulls/{n}/update-branch (a 202 Accepted
// that says only that GitHub will try, and signals a conflict as a 422
// with a message to parse):
//
//   - 201: the merge landed; MergeResult.Merged is true and SHA names the
//     merge commit.
//   - 204: base already contained head; nothing was written, and
//     MergeResult is zero with a nil error. That is not a failure -- it
//     is the authoritative answer to "was this branch behind at all".
//   - 409: the two genuinely conflict. Returned as an *Error, which
//     IsMergeConflict identifies.
//
// Anything else -- notably 404, which is what a head branch in another
// repository (a fork's) or a base branch that is gone reads as -- comes
// back as an ordinary *Error for the caller to decide about.
func (c *RESTClient) MergeBranch(owner, repo, base, head, commitMessage string) (MergeResult, error) {
	payload := map[string]string{"base": base, "head": head}
	if commitMessage != "" {
		payload["commit_message"] = commitMessage
	}
	data, _ := json.Marshal(payload)
	resp, err := c.request(
		"POST", fmt.Sprintf("/repos/%s/%s/merges", owner, repo),
		c.headers(owner, repo, true), data,
	)
	if err != nil {
		return MergeResult{}, err
	}
	switch resp.Status {
	case 201:
		var data struct {
			SHA string `json:"sha"`
		}
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return MergeResult{}, err
		}
		return MergeResult{Merged: true, SHA: data.SHA}, nil
	case 204:
		return MergeResult{}, nil
	default:
		return MergeResult{}, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
}

// GetPullRequest reads one pull request: its title, body, head and base,
// state and mergeability (PullRequestDetail).
func (c *RESTClient) GetPullRequest(owner, repo string, number int) (PullRequestDetail, error) {
	resp, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number))
	if err != nil {
		return PullRequestDetail{}, err
	}
	if resp.Status != 200 {
		return PullRequestDetail{}, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
		Head    struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo *struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		MaintainerCanModify bool       `json:"maintainer_can_modify"`
		Mergeable           *bool      `json:"mergeable"`
		MergeableState      string     `json:"mergeable_state"`
		MergeCommitSHA      string     `json:"merge_commit_sha"`
		Merged              bool       `json:"merged"`
		MergedAt            *time.Time `json:"merged_at"`
		CreatedAt           time.Time  `json:"created_at"`
		// The diff's size, which only this endpoint returns -- see
		// PullRequestDetail.Additions.
		Additions    int `json:"additions"`
		Deletions    int `json:"deletions"`
		ChangedFiles int `json:"changed_files"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return PullRequestDetail{}, err
	}
	state := data.State
	if state == "" {
		state = "open"
	}
	var headRepo string
	if data.Head.Repo != nil {
		headRepo = data.Head.Repo.FullName
	}
	return PullRequestDetail{
		Number: data.Number, Title: data.Title, Body: data.Body, HTMLURL: data.HTMLURL,
		HeadRef: data.Head.Ref, HeadSHA: data.Head.SHA, BaseRef: data.Base.Ref,
		AuthorLogin: data.User.Login, HeadRepo: headRepo, MaintainerCanModify: data.MaintainerCanModify,
		State: state, Mergeable: data.Mergeable, MergeableState: data.MergeableState,
		Merged: data.Merged, MergedAt: data.MergedAt, CreatedAt: data.CreatedAt,
		MergeCommitSHA: data.MergeCommitSHA,
		Additions:      data.Additions, Deletions: data.Deletions, ChangedFiles: data.ChangedFiles,
	}, nil
}

// DefaultBranch is the target repo's own default branch -- the base a PR
// opened there should target when a task's own directive doesn't say
// otherwise.
//
// Read from GitHub rather than configured: with one repo per deployment a
// single configured base branch was a fair guess, but a task repo
// dispatching into many target repos would need an operator to keep a
// per-repo table of "main" vs "master" vs "trunk" correct, and GitHub
// already knows.
func (c *RESTClient) DefaultBranch(owner, repo string) (string, error) {
	resp, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s", owner, repo))
	if err != nil {
		return "", err
	}
	if resp.Status != 200 {
		return "", &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return "", err
	}
	return data.DefaultBranch, nil
}

// SetDefaultBranch changes the repository's default branch to branch -- what
// finishing the first push to an empty repository calls after creating the
// initial trunk ref at the pushed branch's tip, since GitHub sets an empty
// repository's default_branch to whichever branch was pushed first. branch
// must already exist on the remote; GitHub answers 422 when it does not.
func (c *RESTClient) SetDefaultBranch(owner, repo, branch string) error {
	payload, _ := json.Marshal(map[string]string{"default_branch": branch})
	resp, err := c.request(
		"PATCH", fmt.Sprintf("/repos/%s/%s", owner, repo),
		c.headers(owner, repo, true), payload,
	)
	if err != nil {
		return err
	}
	if resp.Status != 200 {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}

// RepoFullName is the repository's own "owner/name", as GitHub spells
// it: the answer to "what is this repo actually called", which nothing
// else here can give.
//
// Two things make it differ from what was asked. Case, which GitHub
// resolves away on every path grain uses -- a clone, a push and a pull
// request all work under any capitalisation -- so nothing ever forces a
// deployment to spell a repo the way the repo is spelled, and a name
// typed once wrongly stays wrong. And a rename: this endpoint follows
// GitHub's own redirect, so a repository that has moved answers under
// its new name.
//
// Both matter to exactly one caller, ui.(*Client).AddTargetRepo, which
// is where a deployment writes a repo's name down for everything else to
// copy (ui.(*Client).canonicalRepo). Resolving it there once is what
// keeps `Bwsalmon/grain-home` from becoming a deployment's canonical
// spelling of `bwsalmon/grain-home` and, through canonicalRepo, the
// spelling every task filed afterwards inherits.
//
// Deliberately not on the Client interface. Nothing in the reconcile
// loop asks this question -- the loop works in the repo a task already
// names, whatever its spelling -- so putting it there would make every
// implementation of Client carry a method only the settings path calls.
// pkg/ui declares the one-method interface it needs (ui.RepoNames) and
// this satisfies it.
func (c *RESTClient) RepoFullName(owner, repo string) (string, error) {
	resp, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s", owner, repo))
	if err != nil {
		return "", err
	}
	if resp.Status != 200 {
		return "", &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		FullName string `json:"full_name"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return "", err
	}
	return data.FullName, nil
}

// ListReviewComments returns the inline review comments on a PR -- the
// context a dispatch needs to tell the agent what feedback it's
// addressing. Paginates the same way ListIssues does; GitHub's REST API
// paginates every list endpoint via the same Link header convention, not
// something specific to the issues endpoint.
func (c *RESTClient) ListReviewComments(owner, repo string, number int) ([]ReviewComment, error) {
	var comments []ReviewComment
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?per_page=100", owner, repo, number)
	for path != "" {
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status != 200 {
			return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var items []struct {
			ID        int    `json:"id"`
			Body      string `json:"body"`
			Path      string `json:"path"`
			Line      *int   `json:"line"`
			InReplyTo int    `json:"in_reply_to_id"`
			ReviewID  int    `json:"pull_request_review_id"`
			User      struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if err := json.Unmarshal(resp.Body, &items); err != nil {
			return nil, err
		}
		for _, item := range items {
			comments = append(comments, ReviewComment{
				ID: item.ID, User: item.User.Login, Body: item.Body,
				Path: item.Path, Line: item.Line, InReplyTo: item.InReplyTo,
				ReviewID: item.ReviewID,
			})
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	return comments, nil
}

// PullRequestFilesCap is how many files GitHub will list for one pull
// request (ListPullRequestFiles). A listing that reaches it may be missing
// files.
const PullRequestFilesCap = 3000

// ListPullRequestFiles returns the paths this pull request touches, as
// GitHub lists them: every file added, modified, removed or renamed
// between its base and its head. A renamed file is listed under both its
// new path and the one it was renamed from, since moving a file out of a
// directory touches that directory as surely as editing it does
// (orchestrator's security hold, pkg/trustboundary).
//
// Paths only. GitHub also serves each file's status, its patch and the
// line counts either side of it, and the one caller here
// (orchestrator.touchedWorkflowFiles) asks a question -- "does this
// change touch .github/workflows/" -- that only the path can answer.
// Deciding on the path alone is also the safe half of the question: a
// caller reading a workflow file's own patch to judge whether the edit
// was the breaking one would be parsing YAML GitHub itself has already
// refused to parse.
//
// GitHub caps this listing at 3000 files however many pages are asked
// for, so a pull request larger than that is under-reported here. That
// is GitHub's own limit rather than a choice made here, and it errs the
// harmless way for the one caller: a workflow edit lost past the cap
// reads as "no workflow edit", which is the answer that asks a person
// rather than an agent.
//
// A caller for whom under-reporting is not harmless -- the security hold,
// which must not read a 3000-file change as touching nothing -- compares
// against PullRequestFilesCap.
func (c *RESTClient) ListPullRequestFiles(owner, repo string, number int) ([]string, error) {
	var files []string
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/files?per_page=100", owner, repo, number)
	for path != "" {
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status != 200 {
			return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var items []struct {
			Filename         string `json:"filename"`
			PreviousFilename string `json:"previous_filename"`
		}
		if err := json.Unmarshal(resp.Body, &items); err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.Filename != "" {
				files = append(files, item.Filename)
			}
			if item.PreviousFilename != "" && item.PreviousFilename != item.Filename {
				files = append(files, item.PreviousFilename)
			}
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	return files, nil
}

// ListPullRequestCommits returns the pull request's own commits, oldest
// first, in CompareCommits' shape -- what GitHub recorded as the change,
// which it keeps answering after the merge and after the head branch is
// deleted, when comparing the two refs has become the empty set.
//
// It is what the merge queue reads a change's port footers from
// (orchestrator.decidePort) rather than the description, which is a
// summary of these same commits that grain rewrites at every refresh and
// truncates (capDescription).
//
// GitHub stops this listing at 250 commits, like CompareCommits.
func (c *RESTClient) ListPullRequestCommits(owner, repo string, number int) ([]Commit, error) {
	var commits []Commit
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/commits?per_page=100", owner, repo, number)
	for path != "" {
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status != 200 {
			return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var items []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
			} `json:"commit"`
			Parents []struct {
				SHA string `json:"sha"`
			} `json:"parents"`
		}
		if err := json.Unmarshal(resp.Body, &items); err != nil {
			return nil, err
		}
		for _, item := range items {
			commit := Commit{SHA: item.SHA, Message: item.Commit.Message, Merge: len(item.Parents) > 1}
			for _, p := range item.Parents {
				commit.Parents = append(commit.Parents, p.SHA)
			}
			commits = append(commits, commit)
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	return commits, nil
}

// ListCheckRuns returns the check runs against ref -- a branch name works
// fine here, GitHub resolves it to that branch's current tip itself, so a
// caller never needs a commit sha in hand. Paginates the same Link-header
// way every other list endpoint here does, but the response body itself
// is shaped differently -- {"total_count": N, "check_runs": [...]}, not a
// bare array, which is GitHub's own shape for this one endpoint.
func (c *RESTClient) ListCheckRuns(owner, repo, ref string) ([]CheckRun, error) {
	var runs []CheckRun
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=100", owner, repo, url.PathEscape(ref))
	for path != "" {
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status != 200 {
			return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var data struct {
			CheckRuns []struct {
				ID          int64      `json:"id"`
				HeadSHA     string     `json:"head_sha"`
				Name        string     `json:"name"`
				Status      string     `json:"status"`
				Conclusion  *string    `json:"conclusion"`
				HTMLURL     string     `json:"html_url"`
				StartedAt   *time.Time `json:"started_at"`
				CompletedAt *time.Time `json:"completed_at"`
				App         struct {
					Slug string `json:"slug"`
				} `json:"app"`
				Output struct {
					Title   *string `json:"title"`
					Summary *string `json:"summary"`
					Text    *string `json:"text"`
				} `json:"output"`
				Steps []struct {
					Name string `json:"name"`
				} `json:"steps"`
			} `json:"check_runs"`
		}
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return nil, err
		}
		for _, item := range data.CheckRuns {
			var reason string
			if item.Output.Title != nil && *item.Output.Title != "" {
				reason = *item.Output.Title
			} else if item.Output.Summary != nil && *item.Output.Summary != "" {
				reason = *item.Output.Summary
			} else if item.Output.Text != nil && *item.Output.Text != "" {
				reason = *item.Output.Text
			}
			runs = append(runs, CheckRun{
				Name:          item.Name,
				Status:        item.Status,
				Conclusion:    item.Conclusion,
				HTMLURL:       item.HTMLURL,
				FailureReason: reason,
				StepsExecuted: len(item.Steps),
				ID:            item.ID,
				HeadSHA:       item.HeadSHA,
				App:           item.App.Slug,
				StartedAt:     item.StartedAt,
				CompletedAt:   item.CompletedAt,
				Output:        checkOutput(item.Output.Title, item.Output.Summary, item.Output.Text),
			})
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	return runs, nil
}

// ListWorkflowRuns returns the GitHub Actions workflow runs against one
// commit, shaped as CheckRuns so a caller can treat them exactly like the
// check runs ListCheckRuns returns.
//
// This is the fallback for a credential that cannot reach the Checks API.
// Reading check runs needs the `repo` scope on a classic PAT, and on a
// fine-grained PAT it cannot be granted at all -- GitHub offered a
// "Checks" permission for fine-grained tokens, withdrew it over "some
// edge cases", and has said only GitHub Apps may use that API in the
// meantime. This endpoint sits behind the "Actions" repository
// permission instead, which a fine-grained token *can* hold, so between
// the two every credential type a deployment might use has some way to
// read CI.
//
// What it costs is generality, not accuracy: Actions runs are all this
// sees, so CI reported through the Checks API by a third party
// (Buildkite, CircleCI, a review bot) is invisible here where
// ListCheckRuns would have shown it. A deployment whose checks are
// GitHub Actions loses nothing; one that adds another CI provider needs
// a credential that can read checks properly.
//
// headSHA is a commit sha, not a branch: filtering by branch would
// return runs from older commits too, and the newest run for a workflow
// on a branch is not necessarily a run of the commit the PR now points
// at. Reading a stale pass as this commit's pass is the one error that
// would auto-merge untested code.
//
// The response is {"total_count": N, "workflow_runs": [...]}, the same
// wrapped shape the Checks API uses rather than a bare array.
func (c *RESTClient) ListWorkflowRuns(owner, repo, headSHA string) ([]CheckRun, error) {
	var runs []CheckRun
	path := fmt.Sprintf("/repos/%s/%s/actions/runs?head_sha=%s&per_page=100",
		owner, repo, url.QueryEscape(headSHA))
	for path != "" {
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status != 200 {
			return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var data struct {
			WorkflowRuns []struct {
				Name       string  `json:"name"`
				Status     string  `json:"status"`
				Conclusion *string `json:"conclusion"`
				HTMLURL    string  `json:"html_url"`
			} `json:"workflow_runs"`
		}
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return nil, err
		}
		for _, item := range data.WorkflowRuns {
			runs = append(runs, CheckRun{
				Name: item.Name, Status: item.Status, Conclusion: item.Conclusion,
				HTMLURL: item.HTMLURL,
			})
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	return runs, nil
}

// WorkflowFileRuns returns the most recent runs of one workflow file,
// newest first and at most limit of them, shaped as CheckRuns like
// ListWorkflowRuns' are.
//
// It answers a question none of the reads above can, because all of them
// are scoped to a commit: has this workflow ever actually run. A
// deployment asks that about the check it installs on its own state
// repository (pkg/staterepo, cmd/grain/statechecks.go), where "every run
// of it that has ever finished died before a job started" is the
// difference between a change that will not load and a repository that
// cannot run Actions at all -- and that is a fact about the workflow's
// whole history rather than about any one pull request.
//
// file is the workflow's file name as GitHub knows it -- the base name,
// `grain-state-check.yml`, not the path under .github/workflows. The
// endpoint takes either that or the workflow's numeric id, and a caller
// holding a repository-relative path has to cut it down itself.
//
// A 404 is no runs rather than an error: GitHub answers it for a
// workflow file it has never registered, which is what a file committed
// on a branch Actions does not watch, or a repository with Actions
// switched off, looks like from here. Neither is evidence of anything,
// and the caller's question is only ever about runs that exist.
func (c *RESTClient) WorkflowFileRuns(owner, repo, file string, limit int) ([]CheckRun, error) {
	if limit <= 0 {
		return nil, nil
	}
	// One page, deliberately: this is a "what has been happening lately"
	// read, and a workflow with thousands of runs behind it must not turn
	// into a walk of all of them on a timer. GitHub returns them newest
	// first, so the page is the recent history the caller asked for.
	path := fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/runs?per_page=%d",
		owner, repo, url.PathEscape(file), limit)
	resp, err := c.get(owner, repo, path)
	if err != nil {
		return nil, err
	}
	if resp.Status == 404 {
		return nil, nil
	}
	if resp.Status != 200 {
		return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		WorkflowRuns []struct {
			Name       string  `json:"name"`
			Status     string  `json:"status"`
			Conclusion *string `json:"conclusion"`
			HTMLURL    string  `json:"html_url"`
		} `json:"workflow_runs"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return nil, err
	}
	runs := make([]CheckRun, 0, len(data.WorkflowRuns))
	for _, item := range data.WorkflowRuns {
		runs = append(runs, CheckRun{
			Name: item.Name, Status: item.Status, Conclusion: item.Conclusion,
			HTMLURL: item.HTMLURL,
		})
	}
	return runs, nil
}

// RunStarts is what a repository's own recent Actions history says about
// whether its runs get as far as running a job at all.
//
// It is OnlyStartupFailures' question asked of a repository rather than
// of a commit. That one is answered from the runs behind one pull
// request and means "no tree pushed at this build clears it"; this one
// is answered from whatever the repository has been running lately and
// means "nothing here has run for anybody", which is a different and
// much larger thing to tell somebody -- see
// docs/repository-checks-never-started.md.
type RunStarts struct {
	// Finished is how many of the runs read GitHub has concluded. Runs
	// still queued or in progress are not counted at all: they have said
	// nothing yet, and a repository that cannot start a job has none of
	// them anyway.
	Finished int
	// Started is how many of those are known to have got a job as far as
	// completing a step -- a run that succeeded, or a failing one with a
	// job that ran something. This is the half that disproves the
	// verdict, so it is counted from positive evidence only.
	Started int
	// NeverStarted is how many are known not to have: concluded
	// `startup_failure`, or concluded failed with no job of them having
	// completed a step (JobLog.NoJobStarted's distinction, which is the
	// one an account out of Actions minutes lands on -- GitHub concludes
	// those runs plain `failure`).
	NeverStarted int
	// RunURL is the newest never-started run's own page, where GitHub
	// writes its own reason and where no API read reaches. Empty when
	// there is no such run.
	RunURL string
}

// ChecksNeverStarted reports the verdict: this repository has finished
// runs, at least one of them demonstrably never got a job started, and
// not one of them is known to have.
//
// All three conditions are load-bearing. Without finished runs there is
// no history to read. Without positive evidence of a never-started run
// the claim would be made about a repository whose runs were merely
// cancelled, or whose logs this credential may not read. And a single
// run that started a job is decisive the other way, whatever it then
// did: Actions works here, and whatever is failing is failing inside a
// job, which is a fact about a tree and not about the repository.
func (s RunStarts) ChecksNeverStarted() bool {
	return s.Finished > 0 && s.Started == 0 && s.NeverStarted > 0
}

// RunStartsRead is how many of a repository's recent runs RecentRunStarts
// asks for: enough that one started job among them settles it, few enough
// that this is one page and one request.
//
// staterepo.WorkflowRunsRead is the same number for the same reasons,
// asked of one workflow file rather than of a repository, and the two are
// deliberately separate constants: they are read by different code with
// different credentials, and a later change to either is not a reason to
// change the other.
const RunStartsRead = 20

// maxRunStartsJobReads bounds the second read, the way maxActionsStateRuns
// bounds ActionsState's.
//
// Only an ambiguous run costs one -- a `failure` whose jobs might have
// run or might never have been started -- and the answer is the same for
// every run of a repository in this condition, so a handful of them is
// the whole of the evidence. What the bound buys is that a repository
// with twenty ordinary red builds is not twenty-one requests every time
// the merge queue escalates one pull request.
const maxRunStartsJobReads = 6

// RecentRunStarts reads the repository's most recent Actions runs and
// reports whether any of them got a job started.
//
// One read for the runs, plus one per ambiguous run for its jobs (bounded
// above). A caller reaches for it having already established that
// *something* here failed before a job of it started, to find out whether
// that is true of the repository as a whole; it is not a read for the
// ordinary path.
//
// A 404 is no runs rather than an error, exactly as it is in
// WorkflowFileRuns: a repository with Actions switched off answers it,
// and so does one this credential may not read Actions on. Both are
// "grain cannot say", which is what an empty RunStarts means, and neither
// is a fault to report to anybody.
func (c *RESTClient) RecentRunStarts(owner, repo string, limit int) (RunStarts, error) {
	if limit <= 0 {
		return RunStarts{}, nil
	}
	// One page, newest first, like WorkflowFileRuns: the question is what
	// has been happening lately, not what the repository has ever done.
	path := fmt.Sprintf("/repos/%s/%s/actions/runs?per_page=%d", owner, repo, limit)
	resp, err := c.get(owner, repo, path)
	if err != nil {
		return RunStarts{}, err
	}
	if resp.Status == 404 {
		return RunStarts{}, nil
	}
	if resp.Status != 200 {
		return RunStarts{}, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		WorkflowRuns []struct {
			ID         int64  `json:"id"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		} `json:"workflow_runs"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return RunStarts{}, err
	}
	var starts RunStarts
	opened := 0
	for _, run := range data.WorkflowRuns {
		if run.Status != "completed" {
			continue
		}
		starts.Finished++
		switch {
		case run.Conclusion == "success":
			// A run cannot succeed without its jobs having run, so this
			// costs no second read and ends the question on its own.
			starts.Started++
			continue
		case run.Conclusion == StartupFailure:
			starts.NeverStarted++
			if starts.RunURL == "" {
				starts.RunURL = run.HTMLURL
			}
			continue
		case !failedConclusion(run.Conclusion):
			// Cancelled, skipped, neutral: counted as finished, and
			// evidence of nothing. A run somebody cancelled says
			// neither that Actions works here nor that it does not.
			continue
		}
		// What is left is the ambiguous case and the reason this call
		// reads jobs at all: GitHub concludes a run plain `failure`
		// both when a job of it ran and broke and when it created the
		// jobs and then refused to start them, which is what an account
		// out of Actions minutes looks like. Only the jobs tell those
		// apart (noJobRanAStep), and only one of them is a fact about
		// the repository.
		if opened >= maxRunStartsJobReads {
			continue
		}
		opened++
		jobs, err := c.runJobs(owner, repo, run.ID)
		if err != nil {
			return RunStarts{}, err
		}
		if !noJobRanAStep(jobs) {
			starts.Started++
			continue
		}
		starts.NeverStarted++
		if starts.RunURL == "" {
			starts.RunURL = run.HTMLURL
		}
	}
	return starts, nil
}

// FailedJobLogs returns the GitHub Actions jobs that failed against one
// commit, each with the tail of its own log.
//
// Three reads, because GitHub has no endpoint from a commit to a log:
// the runs for the commit, the jobs of each run that failed, and then
// each failed job's log. It goes through Actions rather than the Checks
// API for the same reason ListWorkflowRuns does -- Checks cannot be
// granted to a fine-grained PAT at all, while "Actions" read can, and a
// classic PAT's `repo` scope covers both. What that costs is the same
// thing it costs there: CI reported by a third party (Buildkite,
// CircleCI) has no Actions job behind it and so no log here. The caller
// gets an empty result, not an error, and a fix task's body says what it
// always said.
//
// The logs endpoint answers 302 to a short-lived storage URL, which the
// http.Client under RealTransport follows on its own (dropping the
// Authorization header on the cross-host hop, as it should). A job whose
// log has expired -- GitHub keeps them 90 days by default -- answers 410
// instead, and a job whose logs this credential cannot read answers 403;
// both are skipped rather than failing the call, since the log is the
// bonus here and the job names are the part the caller already has.
//
// A failed run that comes out of that walk with no log -- because every
// job's log was skipped, or because the run failed at startup and has no
// jobs at all -- is reported as itself instead of dropped: one entry with
// RunConclusion set, naming the workflow and its own page. See that
// field for why the difference matters to whoever reads this.
//
// headSHA may be empty on a pull request read before GitHub filled it in,
// the same way it may be for checkRunsFor's fallback; there is nothing to
// scope to, so this answers nothing.
func (c *RESTClient) FailedJobLogs(owner, repo, headSHA string) ([]JobLog, error) {
	if headSHA == "" {
		return nil, nil
	}
	runs, err := c.failedRuns(owner, repo, headSHA)
	if err != nil {
		return nil, err
	}
	var logs []JobLog
	for _, run := range runs {
		if len(logs) >= maxFailedJobLogs {
			return logs, nil
		}
		jobs, err := c.failedJobs(owner, repo, run.ID)
		if err != nil {
			return nil, err
		}
		before := len(logs)
		for _, job := range jobs {
			if len(logs) >= maxFailedJobLogs {
				return logs, nil
			}
			text, err := c.jobLog(owner, repo, job.ID)
			if err != nil {
				return nil, err
			}
			if text == "" {
				continue
			}
			tail, truncated := tailOf(text, JobLogTailBytes)
			logs = append(logs, JobLog{
				Name: job.Name, URL: job.URL, Log: tail, Truncated: truncated,
			})
		}
		// A failed run that yielded no log at all still has something to
		// say, and until this it said nothing: the caller was handed an
		// empty slice and told, wrongly in this case, that the failing
		// check was probably not Actions. So the run stands in for the
		// jobs it has none of -- see JobLog.RunConclusion.
		if len(logs) == before {
			logs = append(logs, JobLog{
				Name:          run.Name,
				URL:           run.URL,
				RunConclusion: run.Conclusion,
				RunJobsFailed: len(jobs),
				// Both of these are about the jobs rather than the run,
				// and a run with no jobs at all has neither: what a
				// `startup_failure` means is already in its conclusion.
				RunJobsNeverStarted: len(jobs) > 0 && noJobRanAStep(jobs),
				RunFailureReason:    c.jobsFailureReason(owner, repo, jobs),
			})
		}
	}
	return logs, nil
}

// ActionsState is what GitHub Actions has against one commit when
// FailedJobLogs has come back with nothing readable: how much CI there
// is, how much of it is still going, and which jobs have already failed.
//
// It exists because "no log" was being reported as a guess. A caller
// holding an empty FailedJobLogs knew only that: it then told its reader
// the failing check was probably not Actions, or that the log had
// expired, when by far the commonest cause is neither -- the check run
// concluded `failure` while its workflow run is still in progress, so
// there is no *completed failed run* for FailedJobLogs to walk and the
// log is not downloadable yet either. The three cases look identical
// from an empty slice and completely different from here, so this is the
// read that lets the answer name the one that happened.
//
// The failed jobs come with it because they are the consolation prize.
// A job's steps arrive with the job itself, no log fetch involved and no
// waiting for one -- "the `Test e2e` step failed", with the job's own
// page beside it, is available the moment the job concludes and is worth
// a great deal more than a check's name on its own.
type ActionsState struct {
	// Runs is how many workflow runs GitHub has against the commit at
	// all, whatever they are doing. Zero is the one answer that really
	// does mean "this failing check is not Actions".
	Runs int
	// Unfinished is how many of those have not concluded. Non-zero with
	// no readable log is the race: wait, and the log turns up.
	Unfinished int
	// Jobs are the jobs of those runs that have already failed --
	// including jobs of a run still in progress, which is exactly where
	// FailedJobLogs cannot see them.
	Jobs []ActionsJob
}

// ActionsJob is one failed job of a workflow run, as much as can be said
// about it without reading a log.
type ActionsJob struct {
	Name string
	// RunName is the workflow the job belongs to, and RunFinished
	// whether GitHub has concluded that run -- the difference between a
	// log that is coming and one that is not.
	RunName     string
	RunFinished bool
	// URL is the job's own page on github.com, where the whole log is
	// for a reader who can open it.
	URL string
	// FailedSteps names the steps GitHub concluded did not pass, in the
	// order they ran.
	FailedSteps []string
}

// FailedJobsNote lists these jobs and the steps of them that broke, the
// way a log block is listed, so a reader scanning a report for "###
// something" finds both kinds under the same heading shape.
//
// Here rather than in either caller, for RunFailureNote's reason: this
// is what stands in for a log in both renderings of a build that has
// none -- the tool result a run reads about its own CI (mcp's
// failingJobLogs) and the repair the merge queue writes onto a task
// about the same build (orchestrator's failingJobLogsSection) -- and the
// two must not describe one failure differently. Only the sentences
// around it differ, because their readers can do different things about
// it: one can call the tool again, the other is a task body being read
// minutes later in a cold sandbox.
func FailedJobsNote(jobs []ActionsJob) string {
	var b strings.Builder
	for _, j := range jobs {
		fmt.Fprintf(&b, "\n### %s", j.Name)
		if j.RunName != "" {
			fmt.Fprintf(&b, " (in the %s workflow", j.RunName)
			if !j.RunFinished {
				b.WriteString(", still running")
			}
			b.WriteString(")")
		}
		b.WriteString("\n")
		if j.URL != "" {
			fmt.Fprintf(&b, "\n%s\n", j.URL)
		}
		switch len(j.FailedSteps) {
		case 0:
			b.WriteString("\nGitHub names no failed step for it.\n")
		case 1:
			fmt.Fprintf(&b, "\nThe step that failed: `%s`.\n", j.FailedSteps[0])
		default:
			fmt.Fprintf(&b, "\nThe steps that failed: `%s`.\n",
				strings.Join(j.FailedSteps, "`, `"))
		}
	}
	return b.String()
}

// maxActionsStateRuns bounds how many of a commit's runs ActionsState
// opens the jobs of. Runs itself still counts every run: what is bounded
// is the second read, not the answer.
const maxActionsStateRuns = 6

// ActionsState reads the state of Actions against one commit -- see the
// type. Meant for the moment a caller already knows something failed and
// has no log for it, not for the ordinary path: it is one read for the
// runs plus one per unfinished-or-failed run for its jobs.
//
// headSHA may be empty, on the same terms FailedJobLogs allows it: there
// is nothing to scope to, so this answers nothing.
func (c *RESTClient) ActionsState(owner, repo, headSHA string) (ActionsState, error) {
	if headSHA == "" {
		return ActionsState{}, nil
	}
	runs, err := c.commitRuns(owner, repo, headSHA)
	if err != nil {
		return ActionsState{}, err
	}
	state := ActionsState{Runs: len(runs)}
	opened := 0
	for _, run := range runs {
		if run.finished() && !run.failed() {
			continue
		}
		if !run.finished() {
			state.Unfinished++
		}
		if opened >= maxActionsStateRuns {
			continue
		}
		opened++
		jobs, err := c.runJobs(owner, repo, run.ID)
		if err != nil {
			return ActionsState{}, err
		}
		for _, job := range jobs {
			if !job.failed() {
				continue
			}
			state.Jobs = append(state.Jobs, ActionsJob{
				Name:        job.Name,
				RunName:     run.Name,
				RunFinished: run.finished(),
				URL:         job.URL,
				FailedSteps: job.FailedSteps,
			})
		}
	}
	return state, nil
}

// runRef is one workflow run of a commit: enough of it to walk to its
// jobs, and enough to report the run itself when none of them has a log
// to read.
type runRef struct {
	ID         int64
	Name       string
	Status     string
	Conclusion string
	URL        string
}

// finished reports whether GitHub has concluded this run, which is the
// difference between "no failed job here" and "no failed job here yet".
func (r runRef) finished() bool { return r.Status == "completed" }

// failed reports whether this run has concluded and did not pass.
func (r runRef) failed() bool { return r.finished() && failedConclusion(r.Conclusion) }

// failedRuns is FailedJobLogs' first read: the workflow runs against
// headSHA that finished and did not pass.
func (c *RESTClient) failedRuns(owner, repo, headSHA string) ([]runRef, error) {
	all, err := c.commitRuns(owner, repo, headSHA)
	if err != nil {
		return nil, err
	}
	var runs []runRef
	for _, run := range all {
		if run.failed() {
			runs = append(runs, run)
		}
	}
	return runs, nil
}

// commitRuns is every workflow run against headSHA, whatever it is
// doing. Scoped to the commit, not the branch, for the reason
// ListWorkflowRuns' own doc comment gives.
//
// The run's name, conclusion and page come back with its id because a
// run whose jobs have no readable log is reported as itself, and none of
// those three can be recovered from a second read once the walk below
// has come up empty. Its status comes back too because the unfinished
// runs are the whole of ActionsState's answer: they are invisible to
// failedRuns, and a commit whose only failing run is still going is
// exactly the commit FailedJobLogs finds nothing for.
func (c *RESTClient) commitRuns(owner, repo, headSHA string) ([]runRef, error) {
	var runs []runRef
	path := fmt.Sprintf("/repos/%s/%s/actions/runs?head_sha=%s&per_page=100",
		owner, repo, url.QueryEscape(headSHA))
	for path != "" {
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status != 200 {
			return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var data struct {
			WorkflowRuns []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				HTMLURL    string `json:"html_url"`
			} `json:"workflow_runs"`
		}
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return nil, err
		}
		for _, run := range data.WorkflowRuns {
			runs = append(runs, runRef{
				ID: run.ID, Name: run.Name, Status: run.Status,
				Conclusion: run.Conclusion, URL: run.HTMLURL,
			})
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	return runs, nil
}

// jobRef is one job of a workflow run, as much of it as fetching a log,
// naming it afterwards, and saying which of its steps broke needs.
type jobRef struct {
	ID         int64
	Name       string
	Status     string
	Conclusion string
	URL        string
	// FailedSteps names the steps of this job GitHub concluded did not
	// pass, in the order it ran them. The jobs endpoint hands these over
	// with the job itself, so they cost no extra read and -- unlike the
	// log -- they are there the instant the job concludes.
	FailedSteps []string
	// CompletedSteps is how many steps of this job GitHub reports as
	// finished, passed or failed -- zero for a job it created and then
	// never started, which is a different failure from a job that ran and
	// broke (see JobLog.RunJobsNeverStarted). Counted rather than named
	// because the question it answers is "did anything run at all".
	CompletedSteps int
	// CheckRunID is the check run behind this job, which is where GitHub
	// writes the reason a job it never started did not start. Zero when
	// GitHub did not say (it always does, but nothing here depends on
	// that).
	CheckRunID int64
}

// noJobRanAStep reports whether not one of these failing jobs completed a
// step. It is the signal that separates "no log because the log is gone"
// from "no log because nothing ran", which are the two ways a failing run
// arrives here empty-handed and which want opposite advice.
func noJobRanAStep(jobs []jobRef) bool {
	for _, job := range jobs {
		if job.CompletedSteps > 0 {
			return false
		}
	}
	return true
}

// failed reports whether GitHub has concluded this job and it did not
// pass, which is the only kind of job either caller here wants.
func (j jobRef) failed() bool { return j.Status == "completed" && failedConclusion(j.Conclusion) }

// runJobs is FailedJobLogs' second read: the jobs of one run. A run
// fails as a whole, but its log is per job -- and it is the job's name
// ("go", "ui e2e") that matches what a check run is called, so this is
// also where the two views of the same CI line up.
//
// Every job comes back rather than only the failed ones, since a run
// still in progress has jobs of both kinds and the caller decides which
// it is asking about.
func (c *RESTClient) runJobs(owner, repo string, runID int64) ([]jobRef, error) {
	var jobs []jobRef
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?per_page=100", owner, repo, runID)
	for path != "" {
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status != 200 {
			return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var data struct {
			Jobs []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				HTMLURL    string `json:"html_url"`
				// CheckRunURL is the API URL of this job's check run, not
				// its id: GitHub gives the id nowhere else in this
				// response, so it comes off the end of the path (see
				// checkRunIDFrom).
				CheckRunURL string `json:"check_run_url"`
				Steps       []struct {
					Name       string `json:"name"`
					Status     string `json:"status"`
					Conclusion string `json:"conclusion"`
				} `json:"steps"`
			} `json:"jobs"`
		}
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return nil, err
		}
		for _, job := range data.Jobs {
			ref := jobRef{
				ID: job.ID, Name: job.Name, Status: job.Status,
				Conclusion: job.Conclusion, URL: job.HTMLURL,
				CheckRunID: checkRunIDFrom(job.CheckRunURL),
			}
			for _, step := range job.Steps {
				if step.Status != "completed" {
					continue
				}
				ref.CompletedSteps++
				if failedConclusion(step.Conclusion) {
					ref.FailedSteps = append(ref.FailedSteps, step.Name)
				}
			}
			jobs = append(jobs, ref)
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	return jobs, nil
}

// failedJobs is the failed half of runJobs, which is all FailedJobLogs
// has ever wanted.
func (c *RESTClient) failedJobs(owner, repo string, runID int64) ([]jobRef, error) {
	all, err := c.runJobs(owner, repo, runID)
	if err != nil {
		return nil, err
	}
	var jobs []jobRef
	for _, job := range all {
		if job.failed() {
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}

// jobLog is FailedJobLogs' third read: one job's whole log as plain
// text, or "" when GitHub will not serve it (see FailedJobLogs on the
// 410 and 403 cases). Only a transport-level failure is an error --
// nothing above this can carry on regardless of what one job's log says.
func (c *RESTClient) jobLog(owner, repo string, jobID int64) (string, error) {
	resp, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s/actions/jobs/%d/logs", owner, repo, jobID))
	if err != nil {
		return "", err
	}
	if resp.Status != 200 {
		return "", nil
	}
	return string(resp.Body), nil
}

// checkOutputBytes bounds CheckRun.Output.
const checkOutputBytes = 16 << 10

// checkOutput is a check run's output fields as one bounded text, title
// first, keeping the end where it has to cut.
func checkOutput(parts ...*string) string {
	var kept []string
	for _, p := range parts {
		if p != nil && strings.TrimSpace(*p) != "" {
			kept = append(kept, strings.TrimSpace(*p))
		}
	}
	out := strings.Join(kept, "\n\n")
	if len(out) > checkOutputBytes {
		out = strings.ToValidUTF8(out[len(out)-checkOutputBytes:], "")
	}
	return out
}

// JobLogTail is one GitHub Actions job's log, the last JobLogTailBytes
// of it at most, cut at a line boundary -- or "" with no error when
// GitHub will not serve it (a log past its retention window answers 410,
// a credential without actions:read 403). It is what GitHub CI's failure
// tails are read with (orchestrator's reconcileGitHubCITails), one call
// per failed job and only for failed jobs.
func (c *RESTClient) JobLogTail(owner, repo string, jobID int64) (string, error) {
	text, err := c.jobLog(owner, repo, jobID)
	if err != nil || text == "" {
		return "", err
	}
	if len(text) > JobLogTailBytes {
		text = text[len(text)-JobLogTailBytes:]
		if i := strings.IndexByte(text, '\n'); i >= 0 && i < len(text)-1 {
			text = text[i+1:]
		}
	}
	return strings.ToValidUTF8(text, ""), nil
}

// JobLogTailer is the optional Client method JobLogTail is reached by: a
// client that cannot read Actions logs -- a test's fake, a non-GitHub
// forge -- simply does not implement it, and GitHub CI keeps its records
// without tails.
type JobLogTailer interface {
	JobLogTail(owner, repo string, jobID int64) (string, error)
}

// WorkflowRun is one GitHub Actions workflow run returned by
// GET /repos/{owner}/{repo}/actions/runs.
type WorkflowRun struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	HeadBranch string     `json:"head_branch"`
	HeadSHA    string     `json:"head_sha"`
	HeadRepo   string     `json:"head_repo,omitempty"`
	Event      string     `json:"event"`
	Status     string     `json:"status"`
	Conclusion string     `json:"conclusion"`
	HTMLURL    string     `json:"html_url"`
	RunAttempt int        `json:"run_attempt"`
	StartedAt  *time.Time `json:"run_started_at"`
	UpdatedAt  *time.Time `json:"updated_at"`
}

// WorkflowRunJob is one job of a GitHub Actions workflow run returned by
// GET /repos/{owner}/{repo}/actions/runs/{run_id}/jobs.
type WorkflowRunJob struct {
	ID          int64      `json:"id"`
	RunID       int64      `json:"run_id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	HTMLURL     string     `json:"html_url"`
	FailingStep string     `json:"failing_step,omitempty"`
}

// WorkflowRunsReader is the optional Client capability for polling completed
// GitHub Actions workflow runs and listing a run's jobs.
type WorkflowRunsReader interface {
	CompletedWorkflowRuns(owner, repo string, limit int) ([]WorkflowRun, error)
	ListWorkflowRunJobs(owner, repo string, runID int64) ([]WorkflowRunJob, error)
}

var _ WorkflowRunsReader = (*RESTClient)(nil)

// completedRunsETags caches the last ETag seen per (Transport, owner, repo) for
// CompletedWorkflowRuns so unchanged polls cost 304 Not Modified. Keyed in a
// package-level sync.Map because RESTClient.As makes a shallow copy of RESTClient.
var completedRunsETags sync.Map

// CompletedWorkflowRuns returns the most recent completed workflow runs in
// owner/repo (up to limit), sending If-None-Match when an ETag was previously
// observed and returning (nil, nil) on 304 Not Modified or 404.
func (c *RESTClient) CompletedWorkflowRuns(owner, repo string, limit int) ([]WorkflowRun, error) {
	if limit <= 0 {
		return nil, nil
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/actions/runs?status=completed&per_page=%d", owner, repo, limit)
	headers := c.headers(owner, repo, false)
	cacheKey := fmt.Sprintf("%p/%s/%s/%d", c.Transport, strings.ToLower(owner), strings.ToLower(repo), limit)
	if etag, ok := completedRunsETags.Load(cacheKey); ok {
		if s, ok := etag.(string); ok && s != "" {
			headers["If-None-Match"] = s
		}
	}
	resp, err := c.request("GET", endpoint, headers, nil)
	if err != nil {
		return nil, err
	}
	switch resp.Status {
	case http.StatusNotModified, http.StatusNotFound:
		return nil, nil
	case http.StatusOK:
	default:
		return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	if etag := strings.TrimSpace(resp.Headers["ETag"]); etag != "" {
		completedRunsETags.Store(cacheKey, etag)
	} else if etag := strings.TrimSpace(resp.Headers["Etag"]); etag != "" {
		completedRunsETags.Store(cacheKey, etag)
	}
	var data struct {
		WorkflowRuns []struct {
			WorkflowRun
			HeadRepository *struct {
				FullName string `json:"full_name"`
			} `json:"head_repository"`
		} `json:"workflow_runs"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return nil, err
	}
	runs := make([]WorkflowRun, len(data.WorkflowRuns))
	for i, item := range data.WorkflowRuns {
		r := item.WorkflowRun
		if r.RunAttempt <= 0 {
			r.RunAttempt = 1
		}
		if item.HeadRepository != nil && r.HeadRepo == "" {
			r.HeadRepo = strings.TrimSpace(item.HeadRepository.FullName)
		}
		runs[i] = r
	}
	return runs, nil
}

// ListWorkflowRunJobs returns the jobs of one GitHub Actions workflow run.
func (c *RESTClient) ListWorkflowRunJobs(owner, repo string, runID int64) ([]WorkflowRunJob, error) {
	if runID <= 0 {
		return nil, nil
	}
	var out []WorkflowRunJob
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?per_page=100", owner, repo, runID)
	for path != "" {
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status == http.StatusNotFound {
			return nil, nil
		}
		if resp.Status != http.StatusOK {
			return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var data struct {
			Jobs []struct {
				ID          int64      `json:"id"`
				RunID       int64      `json:"run_id"`
				Name        string     `json:"name"`
				Status      string     `json:"status"`
				Conclusion  string     `json:"conclusion"`
				StartedAt   *time.Time `json:"started_at"`
				CompletedAt *time.Time `json:"completed_at"`
				HTMLURL     string     `json:"html_url"`
				Steps       []struct {
					Name       string `json:"name"`
					Status     string `json:"status"`
					Conclusion string `json:"conclusion"`
				} `json:"steps"`
			} `json:"jobs"`
		}
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return nil, err
		}
		for _, job := range data.Jobs {
			j := WorkflowRunJob{
				ID:          job.ID,
				RunID:       job.RunID,
				Name:        job.Name,
				Status:      job.Status,
				Conclusion:  job.Conclusion,
				StartedAt:   job.StartedAt,
				CompletedAt: job.CompletedAt,
				HTMLURL:     job.HTMLURL,
			}
			if j.RunID == 0 {
				j.RunID = runID
			}
			for _, step := range job.Steps {
				if step.Status == "completed" && failedConclusion(step.Conclusion) {
					j.FailingStep = step.Name
					break
				}
			}
			out = append(out, j)
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	return out, nil
}

// checkRunIDFrom is the numeric id at the end of a job's check_run_url
// ("https://api.github.com/repos/o/r/check-runs/123"), or 0 if it is not
// shaped like that. The whole URL cannot simply be fetched: Transport
// speaks paths against a fixed host, and a URL out of a response body is
// not something to hand it unexamined anyway.
func checkRunIDFrom(u string) int64 {
	at := strings.LastIndexByte(u, '/')
	if at < 0 {
		return 0
	}
	id, err := strconv.ParseInt(u[at+1:], 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// jobsFailureReason is GitHub's own account of why these failing jobs
// produced no log, for JobLog.RunFailureReason -- the first of them that
// has one, since a run whose jobs all failed to start failed them all for
// the same reason and one sentence is the point.
//
// Only reached when every log has already come back empty, so it costs a
// request on a path that is already reporting a broken build and nothing
// at all on a green one. Failures here are swallowed rather than
// returned: this is the bonus on top of an answer that stands up without
// it, exactly as the logs themselves are, and a deployment whose
// credential cannot read the Checks API (which no fine-grained PAT can --
// see ListWorkflowRuns on why) gets the wording that was there before
// rather than an error where a CI report should be.
func (c *RESTClient) jobsFailureReason(owner, repo string, jobs []jobRef) string {
	for i, job := range jobs {
		// Bounded like the log reads themselves: a build with twenty
		// failing jobs and no log for any of them is a repository-wide
		// fact, and the twentieth check run says what the first did.
		if i >= maxFailedJobLogs {
			return ""
		}
		if job.CheckRunID == 0 {
			continue
		}
		if reason := c.checkRunReason(owner, repo, job.CheckRunID); reason != "" {
			return reason
		}
	}
	return ""
}

// checkRunReason reads one check run for whatever GitHub wrote on it: the
// output's title and summary, or failing that its annotations.
//
// This is where the sentence lives that a run conclusion cannot carry.
// "The job was not started because recent account payments have failed or
// your spending limit needs to be increased" is written here and on the
// run's own page, and nowhere in the Actions API at all -- a deployment
// reading only runs and jobs sees a `failure` with no log and has to
// guess, which is what this exists to stop.
//
// The annotations read is skipped when the output already said something,
// since that is the common case and this is a second request on a path
// that has already made several.
func (c *RESTClient) checkRunReason(owner, repo string, checkRunID int64) string {
	resp, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s/check-runs/%d", owner, repo, checkRunID))
	if err != nil || resp.Status != 200 {
		return ""
	}
	var data struct {
		Output struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
		} `json:"output"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return ""
	}
	if reason := joinReason(data.Output.Title, data.Output.Summary); reason != "" {
		return reason
	}
	return c.checkRunAnnotations(owner, repo, checkRunID)
}

// checkRunAnnotations is the second place GitHub writes a check run's
// reason: an annotation, which is what the red banner at the top of a run
// page is drawn from. Same best-effort contract as checkRunReason.
func (c *RESTClient) checkRunAnnotations(owner, repo string, checkRunID int64) string {
	resp, err := c.get(owner, repo,
		fmt.Sprintf("/repos/%s/%s/check-runs/%d/annotations?per_page=%d",
			owner, repo, checkRunID, maxReasonAnnotations))
	if err != nil || resp.Status != 200 {
		return ""
	}
	var items []struct {
		Title   string `json:"title"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp.Body, &items); err != nil {
		return ""
	}
	var parts []string
	for i, item := range items {
		if i >= maxReasonAnnotations {
			break
		}
		if part := joinReason(item.Title, item.Message); part != "" {
			parts = append(parts, part)
		}
	}
	return joinReason(parts...)
}

// joinReason assembles GitHub's wording out of the fields it spreads it
// across, dropping the empty ones (both are nullable everywhere they
// appear) and the duplicates -- a check run whose title and summary are
// the same string is common, and reading it twice helps nobody. Bounded
// by runFailureReasonBytes, cut at a line boundary the way a log tail is.
func joinReason(parts ...string) string {
	var kept []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || slices.Contains(kept, part) {
			continue
		}
		kept = append(kept, part)
	}
	reason := strings.Join(kept, "\n")
	if len(reason) > runFailureReasonBytes {
		reason = reason[:runFailureReasonBytes]
		if at := strings.LastIndexByte(reason, '\n'); at > 0 {
			reason = reason[:at]
		}
		reason = strings.TrimSpace(reason) + " [...]"
	}
	return reason
}

// tailOf returns the last limit bytes of text, advanced to the next line
// boundary so it never opens mid-line, and whether anything was cut.
func tailOf(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	tail := text[len(text)-limit:]
	if at := strings.IndexByte(tail, '\n'); at >= 0 {
		tail = tail[at+1:]
	}
	return tail, true
}

// ListComments returns the plain top-level conversation on an issue or PR
// -- where a human's reply to an agent's ask_question call lands. Same
// shared issues-comments endpoint and pagination shape ListReviewComments
// uses for its own (inline) endpoint.
func (c *RESTClient) ListComments(owner, repo string, number int) ([]Comment, error) {
	var comments []Comment
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100", owner, repo, number)
	for path != "" {
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status != 200 {
			return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var items []struct {
			ID                int    `json:"id"`
			Body              string `json:"body"`
			AuthorAssociation string `json:"author_association"`
			User              struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if err := json.Unmarshal(resp.Body, &items); err != nil {
			return nil, err
		}
		for _, item := range items {
			assoc := item.AuthorAssociation
			if assoc == "" {
				assoc = "NONE"
			}
			comments = append(comments, Comment{
				ID: item.ID, User: item.User.Login, Body: item.Body, AuthorAssociation: assoc,
			})
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	return comments, nil
}

// CreateComment posts a top-level comment. Not something the agent can
// reach directly: the only caller relays an ask_question call to a human.
//
// Returns the new comment's id -- recorded as the baseline for "has a
// trusted reply arrived after this," since a comment's own id is the one
// thing that can't be spoofed by editing an earlier comment's body.
func (c *RESTClient) CreateComment(owner, repo string, number int, body string) (int, error) {
	payload, _ := json.Marshal(map[string]string{"body": body})
	resp, err := c.request(
		"POST", fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number),
		c.headers(owner, repo, true), payload,
	)
	if err != nil {
		return 0, err
	}
	if resp.Status != 201 {
		return 0, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return 0, err
	}
	return data.ID, nil
}

// ReplyToReviewComment answers an inline review comment on pull request
// pullNumber, adding the reply to that comment's thread. commentID must
// be the comment that opened the thread or any reply in it -- GitHub
// files the reply under the thread's root either way. Returns the new
// comment's id, as CreateComment does.
func (c *RESTClient) ReplyToReviewComment(owner, repo string, pullNumber, commentID int, body string) (int, error) {
	payload, _ := json.Marshal(map[string]string{"body": body})
	resp, err := c.request(
		"POST", fmt.Sprintf("/repos/%s/%s/pulls/%d/comments/%d/replies", owner, repo, pullNumber, commentID),
		c.headers(owner, repo, true), payload,
	)
	if err != nil {
		return 0, err
	}
	if resp.Status != 201 {
		return 0, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return 0, err
	}
	return data.ID, nil
}

// graphqlRefusal is the Note on a 401 or 404 from the GraphQL endpoint:
// which endpoint refused, and whether the REST API on the same transport
// and credential answers for the same repository. "Bad credentials" alone
// sends whoever reads it after the token, when a token that REST takes
// and GraphQL refuses says the GraphQL request went somewhere it should
// not have, or that this host's GraphQL wants something REST does not.
func (c *RESTClient) graphqlRefusal(owner, repo, refused string) string {
	endpoint := "the GraphQL endpoint"
	if refused != "" {
		endpoint += " (" + refused + ")"
	}
	note := endpoint + " refused the request"
	if owner == "" || repo == "" {
		return note
	}
	probe, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s", owner, repo))
	if err == nil && probe.Status == http.StatusOK {
		note += fmt.Sprintf(", though the REST API on the same connection answers for %s/%s", owner, repo)
	}
	return note
}

// graphql posts one GraphQL document and decodes its data into out.
// GitHub answers a GraphQL request it could not carry out with a 200 and
// an "errors" array rather than a status, so those are turned into an
// error here instead of leaving every caller to look.
//
// Every GraphQL call here comes through this one function, and it hands
// the transport "/graphql" (graphqlRequestPath): where that is on the
// host -- /graphql on api.github.com, /api/graphql beside a GitHub
// Enterprise Server's /api/v3 -- is the transport's to say
// (RealTransport.graphqlPath).
func (c *RESTClient) graphql(owner, repo, query string, variables map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	resp, err := c.request("POST", graphqlRequestPath, c.headers(owner, repo, true), payload)
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK {
		e := &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusNotFound {
			e.Note = c.graphqlRefusal(owner, repo, resp.URL)
		}
		return e
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(resp.Body, &envelope); err != nil {
		return err
	}
	if len(envelope.Errors) > 0 {
		messages := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			messages = append(messages, e.Message)
		}
		// NOT_FOUND and FORBIDDEN are GitHub's GraphQL spellings of a 404
		// and a 403; reported as those statuses so IsNotFound and
		// IsPermissionDenied answer for them as they do for REST.
		status := http.StatusUnprocessableEntity
		switch envelope.Errors[0].Type {
		case "NOT_FOUND":
			status = http.StatusNotFound
		case "FORBIDDEN":
			status = http.StatusForbidden
		}
		body, _ := json.Marshal(map[string]string{"message": strings.Join(messages, "; ")})
		return &Error{Status: status, Body: body}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}

const reviewThreadsQuery = `query($owner: String!, $repo: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          isResolved
          isOutdated
          path
          comments(first: 100) { nodes { databaseId } }
        }
      }
    }
  }
}`

// ListReviewThreads returns every inline review conversation on pull
// request number, resolved or not -- what the REST review comments
// endpoint cannot say, since a thread and its resolution exist only in
// GitHub's GraphQL API.
func (c *RESTClient) ListReviewThreads(owner, repo string, number int) ([]ReviewThread, error) {
	var threads []ReviewThread
	var after any
	for {
		var data struct {
			Repository *struct {
				PullRequest *struct {
					ReviewThreads struct {
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
						Nodes []struct {
							ID         string `json:"id"`
							IsResolved bool   `json:"isResolved"`
							IsOutdated bool   `json:"isOutdated"`
							Path       string `json:"path"`
							Comments   struct {
								Nodes []struct {
									DatabaseID int `json:"databaseId"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		err := c.graphql(owner, repo, reviewThreadsQuery, map[string]any{
			"owner": owner, "repo": repo, "number": number, "after": after,
		}, &data)
		if err != nil {
			return nil, err
		}
		if data.Repository == nil || data.Repository.PullRequest == nil {
			return nil, &Error{Status: http.StatusNotFound, Body: []byte(`{"message":"Not Found"}`)}
		}
		page := data.Repository.PullRequest.ReviewThreads
		for _, node := range page.Nodes {
			thread := ReviewThread{ID: node.ID, Resolved: node.IsResolved, Outdated: node.IsOutdated, Path: node.Path}
			for _, comment := range node.Comments.Nodes {
				thread.CommentIDs = append(thread.CommentIDs, comment.DatabaseID)
			}
			threads = append(threads, thread)
		}
		if !page.PageInfo.HasNextPage || page.PageInfo.EndCursor == "" {
			return threads, nil
		}
		after = page.PageInfo.EndCursor
	}
}

const resolveReviewThreadMutation = `mutation($threadId: ID!) {
  resolveReviewThread(input: {threadId: $threadId}) { thread { id isResolved } }
}`

// ResolveReviewThread marks one review thread resolved. owner and repo
// name the repository the thread is on, which is what picks the
// credential: the thread's own node ID carries no repository a token
// ladder could read.
func (c *RESTClient) ResolveReviewThread(owner, repo, threadID string) error {
	return c.graphql(owner, repo, resolveReviewThreadMutation, map[string]any{"threadId": threadID}, nil)
}

// UpdateComment rewrites one issue or pull request conversation comment
// in place -- what lets grain keep a single progress comment on a work
// tree's origin (orchestrator.reconcileWorkTrees) rather than posting a
// new one each time the tree moves.
func (c *RESTClient) UpdateComment(owner, repo string, commentID int64, body string) error {
	payload, _ := json.Marshal(map[string]string{"body": body})
	resp, err := c.request(
		"PATCH", fmt.Sprintf("/repos/%s/%s/issues/comments/%d", owner, repo, commentID),
		c.headers(owner, repo, true), payload,
	)
	if err != nil {
		return err
	}
	if resp.Status != 200 {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}

// MergePullRequestWithMessage is MergePullRequest with the merge commit's
// own message given: title becomes its summary line and message its body,
// which is where grain writes a task's `Origin:` and `Filed-by:` trailers.
//
// Omitting merge_method defaults on GitHub to "merge" (a merge commit). When
// the repository disallows merge commits (HTTP 405 "Merge commits are not
// allowed on this repository"), this falls back to "squash", and if squash
// is also disallowed ("Squash commits are not allowed on this repository"),
// once more to "rebase".
func (c *RESTClient) MergePullRequestWithMessage(owner, repo string, number int, headSHA, title, message string) error {
	if err := c.gitOnly(owner, repo); err != nil {
		return err
	}
	payload := map[string]any{}
	if headSHA != "" {
		payload["sha"] = headSHA
	}
	if title != "" {
		payload["commit_title"] = title
	}
	if message != "" {
		payload["commit_message"] = message
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", owner, repo, number)
	headers := c.headers(owner, repo, true)
	putMerge := func() (ApiResponse, error) {
		data, _ := json.Marshal(payload)
		return c.request("PUT", path, headers, data)
	}
	resp, err := putMerge()
	if err != nil {
		return err
	}
	if resp.Status == http.StatusMethodNotAllowed &&
		strings.Contains(strings.ToLower(string(resp.Body)), "merge commits are not allowed on this repository") {
		payload["merge_method"] = "squash"
		resp, err = putMerge()
		if err != nil {
			return err
		}
		if resp.Status == http.StatusMethodNotAllowed &&
			strings.Contains(strings.ToLower(string(resp.Body)), "squash commits are not allowed on this repository") {
			payload["merge_method"] = "rebase"
			resp, err = putMerge()
			if err != nil {
				return err
			}
		}
	}
	if resp.Status != 200 {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}

// CreateReview creates a draft review on a pull request -- see the
// package doc comment for why it is always a draft. Returns the new
// review's id, kept for the same reason CreateComment returns one: cheap,
// and the pattern every other creation call here already follows.
func (c *RESTClient) CreateReview(owner, repo string, number int, body string, comments []NewReviewComment) (int, error) {
	payloadComments := make([]map[string]any, len(comments))
	for i, cm := range comments {
		payloadComments[i] = map[string]any{"path": cm.Path, "line": cm.Line, "body": cm.Body}
	}
	payload, _ := json.Marshal(map[string]any{"body": body, "comments": payloadComments})
	resp, err := c.request(
		"POST", fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, number),
		c.headers(owner, repo, true), payload,
	)
	if err != nil {
		return 0, err
	}
	if resp.Status != 200 {
		return 0, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return 0, err
	}
	return data.ID, nil
}

var _ Client = (*RESTClient)(nil)

// headerMap flattens an http.Header into the single-valued map
// ApiResponse carries. Here rather than inline because both the 304 path
// and the ordinary one need it, and a second copy of the loop is a
// second place for the two to diverge about casing.
func headerMap(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k := range h {
		out[k] = h.Get(k)
	}
	return out
}
