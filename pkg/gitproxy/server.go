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

package gitproxy

// Wires GitProxy's decision logic to a real HTTP server. net/http is
// enough for a proxy whose whole job is decide-then-stream, no routing
// framework needed.

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
)

// NewHandler wraps proxy as an http.Handler: split the path and query,
// hand the body over unread (GitProxy.HandleRequest reads it, bounded,
// only once the request is allowed), run the decision logic, and stream
// the response back with WriteResponse.
func NewHandler(proxy *GitProxy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isNamespacePath(r.URL.Path) {
			proxy.ServeNamespaceHTTP(w, r)
			return
		}
		if isCLIPath(r.URL.Path) {
			proxy.ServeCLIHTTP(w, r)
			return
		}
		if isGitSignPath(r.URL.Path) {
			proxy.ServeGitSignHTTP(w, r)
			return
		}
		var body io.Reader
		if r.ContentLength != 0 {
			body = r.Body
		}
		headers := map[string]string{}
		for k := range r.Header {
			headers[k] = r.Header.Get(k)
		}

		WriteResponse(w, proxy.HandleRequest(r.Context(), r.Method, r.URL.Path, r.URL.RawQuery, headers, body))
	})
}

// WriteResponse writes resp to w: its headers, less the hop-by-hop ones
// and Content-Length (which a decision about an upstream response could
// get wrong for this transport's own), then its body. A Stream is copied
// through as it is read and flushed after every read, so a large
// packfile is never held whole and git's progress messages reach the
// sandbox when they are sent; it is closed once copied. A Stream that
// fails part way aborts the response rather than ending it cleanly, so
// the client sees a truncated transfer as one instead of as a complete
// response that happens to be short.
func WriteResponse(w http.ResponseWriter, resp ProxyResponse) {
	if resp.Stream != nil {
		defer resp.Stream.Close()
	}
	for k, v := range resp.Headers {
		switch strings.ToLower(k) {
		case "content-length", "connection", "transfer-encoding":
			continue
		}
		w.Header().Set(k, v)
	}
	w.WriteHeader(resp.Status)
	if resp.Stream == nil {
		w.Write(resp.Body)
		return
	}
	if _, err := io.Copy(flushWriter{w, http.NewResponseController(w)}, resp.Stream); err != nil {
		panic(http.ErrAbortHandler)
	}
}

// flushWriter flushes every write straight through to the client.
// io.Copy hands it whatever each read of the upstream returned, so this
// adds no buffering of its own and keeps none back.
type flushWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err != nil {
		return n, err
	}
	if err := f.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return n, err
	}
	return n, nil
}

// BuildConfig is what BuildProxy needs to wire the real files under a
// /data-shaped directory (docs/design.md, "secrets on /data") to the live
// model store that now answers what a hand-edited allowlist file used to.
type BuildConfig struct {
	// DataDir holds secrets/github/{credentials.json,*.token} and
	// secrets/sandbox-tokens.json, and receives state/git-proxy/audit.log.
	DataDir string
	// Store answers GitScope for the sandbox a request authenticates as.
	Store *model.Store
	// ForwardHost overrides the real github.com forward target -- the
	// git-transport half of a mock-GitHub live-test seam. Empty means
	// "github.com over TLS". Any host is accepted, but only as a bare
	// host[:port], and plain HTTP (ForwardTLS false) only to loopback --
	// see ValidateForwardTarget.
	ForwardHost string
	ForwardTLS  bool
	// Forbidden is ModelAuthorizer.Forbidden: the live set of repos no
	// sandbox may reach through this proxy at all. Nil for a deployment
	// with none and no way of acquiring any; a caller that can change
	// which repository grain's state lives in passes a set it keeps a
	// handle on instead, and replaces its contents when that changes
	// (cmd/grain's stateManager).
	Forbidden *ForbiddenSet
	// ReadScopes is the registry of sandboxes that may fetch one
	// repository without a task behind them -- grain CI's job sandboxes,
	// and nothing else today (see readscope.go). Nil for a deployment
	// running no CI, which grants nothing this way and leaves every
	// request answered by the model exactly as it was before.
	ReadScopes *ReadScopes
	// WriteScopes is the registry of sandboxes that may push to (and fetch
	// from) repositories in CI. Nil for a deployment running no CI.
	WriteScopes *WriteScopes
	// SandboxTeams is GitProxy.SandboxTeams: the CI sandboxes whose
	// requests go out with their team's credential. Nil for a deployment
	// running no CI.
	SandboxTeams *SandboxTeams
	// Local is the controller-owned repository exposed through its stable
	// alias instead of forwarded to the external git host.
	Local *LocalRepository
	// MaxPushBodyBytes and MaxFetchBodyBytes are GitProxy's request-body
	// limits; zero leaves each at its default.
	MaxPushBodyBytes  int64
	MaxFetchBodyBytes int64
	// SingleTenant is GitProxy.SingleTenant: always true from the daemon.
	SingleTenant bool
	// Namespace serves /fs/<scheme>/<path> when set.
	Namespace       *namespace.Server
	NamespaceScopes NamespaceScopeLookup
	// CLI is GitProxy.CLI.
	CLI CLIBackend
	// CLIActions and WaitCredit are GitProxy's own: what the acting /cli/
	// routes act through, and what credits their waits.
	CLIActions CLIActions
	WaitCredit CLIWaitCreditor
}

// BuildProxy wires the real files under DataDir plus the live model store
// into a GitProxy ready to serve.
func BuildProxy(cfg BuildConfig) (*GitProxy, error) {
	credentials, err := LoadCredentialSet(filepath.Join(cfg.DataDir, "secrets", "github"))
	if err != nil {
		return nil, err
	}
	tokens, err := LoadSandboxTokens(filepath.Join(cfg.DataDir, "secrets", "sandbox-tokens.json"))
	if err != nil {
		return nil, err
	}
	audit, err := NewFileAuditLog(filepath.Join(cfg.DataDir, "state", "git-proxy", "audit.log"))
	if err != nil {
		return nil, err
	}

	host, useTLS := cfg.ForwardHost, cfg.ForwardTLS
	if host == "" {
		host, useTLS = "github.com", true
	}
	if err := ValidateForwardTarget(host, useTLS); err != nil {
		return nil, err
	}
	// The forward target is the default host of every credential with no
	// host record, and the one host this proxy's credentials may go to:
	// one recorded for another host is refused rather than sent
	// (hosts.go). Writing the default down keeps an existing credential on
	// this host if ForwardHost later changes.
	credentials.ForwardHost = host
	credentials.ForwardInsecure = !useTLS
	credentials.MigrateCredentialHosts()

	// The model is the authority on every sandbox that has a task behind
	// it; a CI job's sandbox has none, and is answered by the scope
	// registry in front of it (ReadOnlyScopeAuthorizer). A deployment
	// with no registry is wired to the model alone, as it always was.
	var authorizer Authorizer = &ModelAuthorizer{Store: cfg.Store, Forbidden: cfg.Forbidden}
	if cfg.ReadScopes != nil || cfg.WriteScopes != nil {
		authorizer = &ReadOnlyScopeAuthorizer{
			Scopes:      cfg.ReadScopes,
			WriteScopes: cfg.WriteScopes,
			Next:        authorizer,
			Forbidden:   cfg.Forbidden,
		}
	}

	// A nil *model.Store in the interface would read as a lookup that
	// is there; leaving it unset makes a registered sandbox's request
	// refused with a sentence instead.
	var teamCredentials TeamCredentialLookup
	var credentialOverrides CredentialOverrideLookup
	var namespaceScopes NamespaceScopeLookup = cfg.NamespaceScopes
	var auditLog AuditLog = audit
	var activity ProxyActivityRecorder
	if cfg.Store != nil {
		activity = cfg.Store
		teamCredentials = cfg.Store
		credentialOverrides = cfg.Store
		if namespaceScopes == nil {
			namespaceScopes = cfg.Store
		}
		auditLog = &TeamAuditLog{Next: audit, Sink: cfg.Store, SandboxTeams: cfg.SandboxTeams}
	}

	return &GitProxy{
		Authorizer:          authorizer,
		Credentials:         credentials,
		Tokens:              tokens,
		Forwarder:           &RealForwarder{Host: host, UseTLS: useTLS},
		Audit:               auditLog,
		CredentialOverrides: credentialOverrides,
		SandboxTeams:        cfg.SandboxTeams,
		TeamCredentials:     teamCredentials,
		Local:               cfg.Local,
		MaxPushBodyBytes:    cfg.MaxPushBodyBytes,
		MaxFetchBodyBytes:   cfg.MaxFetchBodyBytes,
		SingleTenant:        cfg.SingleTenant,
		Namespace:           cfg.Namespace,
		NamespaceScopes:     namespaceScopes,
		CLI:                 cfg.CLI,
		Activity:            activity,
		CLIActions:          cfg.CLIActions,
		WaitCredit:          cfg.WaitCredit,
	}, nil
}
