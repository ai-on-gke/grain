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

package cli

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// CredentialSource mints the bearer credential for a context whose auth
// is the name it was registered under (external-cli.spec.md#XC-10): a
// wrapper distribution's sign-in to the proxy its deployments sit
// behind, which core knows nothing about.
type CredentialSource interface {
	// Token is the bearer credential for req. A source whose local
	// sign-in session is missing or expired returns SignInRequired, so
	// the CLI stops at once and says how to renew it.
	Token(ctx context.Context, req CredentialRequest) (string, error)
}

// CredentialRequest is what a CredentialSource is asked for.
type CredentialRequest struct {
	// Context is the context's name, Server its URL, Audience its
	// audience ("" for none).
	Context, Server, Audience string
}

// CredentialSourceFunc is a CredentialSource from a function.
type CredentialSourceFunc func(ctx context.Context, req CredentialRequest) (string, error)

// Token calls f.
func (f CredentialSourceFunc) Token(ctx context.Context, req CredentialRequest) (string, error) {
	return f(ctx, req)
}

var (
	credentialSourcesMu sync.RWMutex
	credentialSources   = map[string]CredentialSource{}
)

// RegisterCredentialSource makes src the credential of every context
// whose auth is name, replacing any source registered under it; a nil
// src removes it. Register from an init function, before Main.
func RegisterCredentialSource(name string, src CredentialSource) {
	credentialSourcesMu.Lock()
	defer credentialSourcesMu.Unlock()
	if src == nil {
		delete(credentialSources, name)
		return
	}
	credentialSources[name] = src
}

func credentialSource(name string) (CredentialSource, bool) {
	credentialSourcesMu.RLock()
	defer credentialSourcesMu.RUnlock()
	src, ok := credentialSources[name]
	return src, ok
}

// SignInRequired is the error a CredentialSource returns for a missing
// or expired sign-in: exit 3, saying "not signed in to <provider>: run
// <fix>", with fix as the -json error's fix.
func SignInRequired(provider, fix string) error {
	return &cliError{exit: exitAuth, code: "login_required", fix: fix,
		err: fmt.Errorf("not signed in to %s: run %s", provider, fix)}
}

// builtinAuth are the auth values core itself knows (#XC-1).
var builtinAuth = map[string]bool{
	"external-token": true, "access-code": true, "google-id-token": true, "local": true, "none": true, "": true,
}

// contextToken is HTTPClient.ContextToken for sel against server: its
// stored secret, or its registered source's token, or "" -- for a
// context that signs in some other way, or for a server that is not the
// context's own (#XC-4: -server and $GRAIN_SERVER pointing elsewhere
// never receive a context's credential).
func contextToken(sel selectedContext, server string, store credentialStore) func(context.Context) (string, error) {
	if sel.ctx == nil || !sameOrigin(sel.ctx.Server, server) {
		return nil
	}
	c := *sel.ctx
	return func(ctx context.Context) (string, error) {
		switch c.Auth {
		case "external-token", "access-code":
			return store.load(c.Credential)
		}
		if builtinAuth[c.Auth] {
			return "", nil
		}
		src, ok := credentialSource(c.Auth)
		if !ok {
			return "", &cliError{exit: exitAuth, code: "unknown_auth",
				err: fmt.Errorf("context %s signs in with %q, which this grain build has no credential source for", sel.name, c.Auth)}
		}
		return src.Token(ctx, CredentialRequest{Context: sel.name, Server: c.Server, Audience: c.Audience})
	}
}

// sameOrigin reports whether a and b are the same scheme, host and port.
func sameOrigin(a, b string) bool {
	ua, errA := url.Parse(strings.TrimSpace(a))
	ub, errB := url.Parse(strings.TrimSpace(b))
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host)
}
