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

// Package modelproxy is the model APIs' equivalent of pkg/gitproxy: the
// one path from a sandbox to a model vendor -- Anthropic, OpenAI, Google
// -- holding the credential of the deployment's connection for that
// vendor so that nothing inside a sandbox ever has to.
//
// A run that wants to call a model for itself -- a script it writes, a
// second `claude` or `codex` it starts inside its own checkout, an
// evaluation harness -- has, without this, exactly two options: be
// handed the deployment's credential (which is the credential every other
// run authenticates as, so a run that leaks it into a log or a
// commit leaks all of it), or do without. This is the third: a proxy on
// the controller that holds the real credentials, and a token per run
// that is worth nothing except against that proxy, for as long as the
// run that minted it lasts.
//
// # What it is, and what it deliberately is not
//
// It is a reverse proxy in front of one upstream, with one decision in
// front of it: does this caller hold a token this deployment minted and
// has not revoked (Tokens). If it does, the request is forwarded with
// the caller's own credentials replaced by its connection's (see
// "Whose credential" below); if it does not, it is refused with a 401 in
// the shape that vendor's own clients already know how to read. There is
// no per-model policy, no request rewriting (bar a Claude connection on
// Vertex AI, below), no accounting and no cache -- only a cap on how many requests one run may have open at once
// (Proxy.MaxConcurrent), so one run cannot monopolize the
// credential: everything past the token
// check is the vendor's own API, streaming included, and a client pointed at
// this proxy through ANTHROPIC_BASE_URL, OPENAI_BASE_URL or its Gemini
// equivalent should not be able to tell the difference beyond the
// credential it authenticates with.
//
// It is also not a route. docs/design.md's sandbox networking gives a
// guest its own namespace behind netshim's NAT, with the host reachable
// on the docker bridge gateway address and nothing clever in between --
// which is exactly how the git proxy is reached from inside a sandbox
// today (cmd/grain's -kontur-git-proxy-host). This binds a port the same
// way, one per vendor, advertised at the same host, so "visible to
// something running in the guest" costs a listener and no routing at
// all.
//
// # Whose credential
//
// Model connections are the deployment's (docs/single-tenant-reset.md,
// stage 1), and a token belongs to no team (stage 4c). When a run's
// token is minted it records a Connection -- the connection owning the
// credential the run's own agent spends when that agent is this
// vendor's framework (model.AgentBackend.CredentialOwnerID, so a
// connection borrowing another's key by CredentialBackendID records the
// source), or otherwise the deployment's connection of this vendor's
// framework. Every request on that token is forwarded with that
// connection's credential, resolved per request (Endpoint.SetCredential),
// authenticating the way the agent framework itself does on that
// connection -- an API key, or a Claude Code OAuth token. A Claude
// connection on Vertex AI is the one exception to "swap the credential":
// its Messages calls are translated into Vertex AI's and authenticated
// with an access token from its service account (Endpoint.SetVertex,
// vertex.go). A connection authenticating some other way (ambient host
// login, a codex subscription login) has no key this proxy can present,
// and is refused with a sentence saying so rather than forwarded with
// another one.
//
// A deployment with no connection for the vendor being called is
// refused in that vendor's own error shape ("this deployment has no
// Claude connection").
//
// A usage-limit refusal the vendor answers through this proxy is that
// connection's, too (Vendor.UsageLimit): it is reported to the same
// per-connection pause agent runs are held by (orchestrator.Pause, via
// Endpoint.SetPause), and a connection paused there refuses this proxy's
// requests until it lifts -- so proxy traffic and agent runs on one key
// stop and resume together, and nothing on another key does.
//
// # Why a token per run rather than a shared one
//
// Because the one property worth having here is that a credential a
// sandbox holds stops working when that sandbox is gone. A run's token
// is minted as its capability is materialized, placed in the sandbox as
// a file, and dropped from the registry when the run's lease is revoked
// -- so a token copied out of a sandbox, or left behind in a transcript,
// authorizes nothing a few minutes later. The registry is in memory:
// tokens do not survive a restart of the daemon, which is the same
// bound every run in flight already has.
//
// # What is one package, and what is per vendor
//
// This began as pkg/claudeproxy, one vendor's proxy, and was generalized
// against the second and third callers rather than ahead of them
// (grain/task-307). The split it settled on is the one that was already
// visible in that package: the registry (Tokens), the endpoint a
// capability provider mints from (Endpoint) and the handler (Proxy) know
// nothing about any vendor, and everything that does is a Vendor value
// -- the upstream, how a caller's credential is recognised, how this
// connection's credential is presented upstream, what a usage limit looks
// like, and the JSON a refusal is written in.
//
// That last one is why a Vendor is not merely a base URL. A client
// library refused by this proxy should raise the error it would raise if
// the vendor had refused it, so Anthropic's `{"type":"error",...}`,
// OpenAI's `{"error":{...,"code":"invalid_api_key"}}` and Google's
// `{"error":{"code":401,"status":"UNAUTHENTICATED"}}` are each written
// as that vendor writes them. And the credential swap is not always a
// header: the Gemini API takes its key as a `key=` query parameter, so
// swapping one means rewriting the outbound URL -- which is also why
// nothing here ever logs a query string.
package modelproxy

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// Connection is the model connection a token forwards with: the id of
// the connection owning the credential it spends
// (model.AgentBackend.CredentialOwnerID). An empty ID is a deployment
// with no connection for this proxy's vendor, which is refused rather
// than forwarded.
type Connection struct {
	ID string
}

// normalized is c with its id trimmed.
func (c Connection) normalized() Connection {
	c.ID = strings.TrimSpace(c.ID)
	return c
}

// CredentialRefusal is what a credential resolver (Endpoint.SetCredential)
// returns for a connection it will not forward with, with the sentence a
// run is refused with -- a connection authenticating by Vertex or by an
// ambient login, which has no key to present. It names no credential.
type CredentialRefusal struct {
	Reason string
}

func (e *CredentialRefusal) Error() string { return e.Reason }

// Endpoint is one running proxy, as everything that is not the HTTP
// handler sees it: the address a sandbox reaches it at, the registry of
// tokens it will answer for, and how a token's team connection and that
// connection's credential are resolved. One per vendor a deployment
// serves, and vendor-blind itself -- which vendor an endpoint belongs to
// is the Vendor its Proxy and its capability provider are built with.
//
// It exists as a value of its own -- rather than as fields on Proxy --
// because two things need it at two different moments, and one of them
// comes first. cmd/grain builds the capability providers that mint
// tokens out of these while assembling its capability registry, which
// happens before (and, for a process that never starts a proxy at all,
// instead of) anything binding a listener. So a provider is handed an
// Endpoint whose URL is still empty, and SetURL is what the server calls
// once it knows where it is listening; a URL that is still empty when a
// task asks for the capability is how the provider knows there is no
// proxy to hand anybody, and refuses cleanly rather than placing a file
// naming an address nothing answers on.
//
// Every method is safe on a nil *Endpoint, which reads as "there is no
// proxy": no URL, no token can be minted, and nothing authenticates.
// Fail-closed in the same shape gitproxy.ReadScopes is nil-safe.
//
// Everything about an Endpoint is safe to use from several goroutines at
// once, because everything about one is: a handler resolves the
// credential per request while a capability provider reads the URL and
// whatever started the listener wrote both a moment earlier. That is why
// none of the URL, the resolvers or the pause hooks is a field anybody
// outside this package assigns -- see SetURL, SetConnections,
// SetCredential and SetPause.
type Endpoint struct {
	// Tokens is the registry this endpoint mints into and the handler
	// authenticates against. Never nil on an Endpoint from NewEndpoint.
	Tokens *Tokens

	mu          sync.RWMutex
	url         string
	connections func(ctx context.Context, own string) (Connection, error)
	credential  func(ctx context.Context, connection Connection) (string, error)
	paused      func(connection Connection, now time.Time) bool
	limited     func(connection Connection, limit *agent.UsageLimitError)
	vertex      func(ctx context.Context, connection Connection) (*VertexTarget, error)
}

// NewEndpoint returns an endpoint with an empty registry and no address
// yet -- see the type's own doc comment for why those are two moments.
func NewEndpoint() *Endpoint {
	return &Endpoint{Tokens: NewTokens()}
}

// SetURL publishes the base URL a sandbox reaches this proxy at, e.g.
// "http://172.17.0.1:41234". Called once by whatever binds the listener,
// before anything is dispatched onto it.
func (e *Endpoint) SetURL(url string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.url = url
}

// URL is that base URL, empty where no proxy is serving.
func (e *Endpoint) URL() string {
	if e == nil {
		return ""
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.url
}

// Mint records a token for owner forwarding with connection and returns
// it, with the id to revoke it by later -- see Tokens.Mint, which this is
// a nil-safe forward to.
func (e *Endpoint) Mint(owner string, connection Connection) (id, token string, err error) {
	if e == nil {
		return "", "", ErrNoProxy
	}
	return e.Tokens.Mint(owner, connection)
}

// MintUntil is Mint for an owner whose end is known -- see
// Tokens.MintUntil: the token is never honoured past until.
func (e *Endpoint) MintUntil(owner string, connection Connection, until time.Time) (id, token string, err error) {
	if e == nil {
		return "", "", ErrNoProxy
	}
	return e.Tokens.MintUntil(owner, connection, until)
}

// Revoke drops the token id names, so nothing holding it can call this
// proxy again. Safe for an id that holds no token.
func (e *Endpoint) Revoke(id string) {
	if e == nil {
		return
	}
	e.Tokens.Revoke(id)
}

// authenticate answers who token belongs to and the connection it
// forwards with -- Tokens.Lookup, through a nil-safe hop, so a handler
// wired up with no endpoint at all recognises nobody instead of
// panicking on the first request it is shown.
func (e *Endpoint) authenticate(token string) (owner string, connection Connection, ok bool) {
	if e == nil {
		return "", Connection{}, false
	}
	return e.Tokens.Lookup(token)
}

// SetConnections publishes how a run's Connection is chosen when its
// token is minted (ConnectionFor): given the
// connection whose credential its own agent spends (own, empty for a run
// on none), the connection of this endpoint's vendor that run forwards
// with. Called by whatever starts the proxy, beside SetURL.
func (e *Endpoint) SetConnections(resolve func(ctx context.Context, own string) (Connection, error)) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.connections = resolve
}

// ConnectionFor is the Connection a run whose own agent spends own's
// credential is minted with -- through the resolver SetConnections
// published. An endpoint with none wired answers a connection with no
// ID, which every request on it is refused for.
func (e *Endpoint) ConnectionFor(ctx context.Context, own string) (Connection, error) {
	var none Connection
	if e == nil {
		return none, nil
	}
	e.mu.RLock()
	resolve := e.connections
	e.mu.RUnlock()
	if resolve == nil {
		return none, nil
	}
	connection, err := resolve(ctx, strings.TrimSpace(own))
	if err != nil {
		return none, err
	}
	return connection.normalized(), nil
}

// SetCredential publishes how a connection's credential is resolved --
// the key that connection's own agent runs authenticate with, which is
// what every request forwarded on a token minted for it is
// authenticated with. Called by whatever starts the proxy, beside
// SetURL.
//
// A function, called per request, rather than a string read once: an
// admin who replaces a connection's key while the daemon runs expects
// the next request to use the new value, exactly as the agent
// frameworks' own credential functions do. It may return an empty
// string, which is a connection with no key set -- the handler answers
// that with a refusal naming the connection -- or a *CredentialRefusal
// for one whose authentication this proxy cannot present.
func (e *Endpoint) SetCredential(resolve func(ctx context.Context, connection Connection) (string, error)) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.credential = resolve
}

// ResolveCredential resolves connection's credential through the
// resolver SetCredential published, answering "" for an endpoint that
// has none wired or a connection with no ID -- neither of which is ever
// forwarded.
//
// The resolver is read out from under the lock and called without it:
// resolving a credential reads a secrets database, and holding a
// listener's lock across that would serialize every request this proxy
// forwards behind the slowest of them.
func (e *Endpoint) ResolveCredential(ctx context.Context, connection Connection) (string, error) {
	if e == nil || connection.ID == "" {
		return "", nil
	}
	e.mu.RLock()
	resolve := e.credential
	e.mu.RUnlock()
	if resolve == nil {
		return "", nil
	}
	return resolve(ctx, connection.normalized())
}

// SetPause publishes the per-connection usage-limit pause this proxy
// shares with agent runs (orchestrator.Pause): paused answers whether a
// connection is held at now, and limited reports a usage limit the
// vendor answered one of its requests with. Either may be nil.
func (e *Endpoint) SetPause(paused func(connection Connection, now time.Time) bool, limited func(connection Connection, limit *agent.UsageLimitError)) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.paused, e.limited = paused, limited
}

// connectionPaused reports whether connection is held by a usage-limit
// pause at now.
func (e *Endpoint) connectionPaused(connection Connection, now time.Time) bool {
	if e == nil || connection.ID == "" {
		return false
	}
	e.mu.RLock()
	paused := e.paused
	e.mu.RUnlock()
	return paused != nil && paused(connection, now)
}

// reportUsageLimit hands a usage limit met on connection to the pause.
func (e *Endpoint) reportUsageLimit(connection Connection, limit *agent.UsageLimitError) {
	if e == nil || connection.ID == "" || limit == nil {
		return
	}
	e.mu.RLock()
	limited := e.limited
	e.mu.RUnlock()
	if limited != nil {
		limited(connection, limit)
	}
}
