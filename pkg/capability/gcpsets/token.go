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

package gcpsets

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// TokenTimeout bounds one fetch of an admin's access token. A Cloud
// outlives the call that built it -- the Manager keeps it for every later
// mint -- so its token source must not be bound to that call's context:
// golang.org/x/oauth2 refreshes a user's (authorized_user) and a
// service-account key's tokens with the context they were built with, and
// a Cloud built during configure, whose context is cancelled as configure
// returns, refreshed its first expired token an hour later with "Post
// https://oauth2.googleapis.com/token: context canceled" (task #1315).
// Each fetch is bounded by this instead.
const TokenTimeout = 20 * time.Second

// CredentialKind says what is wrong with an admin's credentials.
type CredentialKind string

const (
	// CredentialMissing is credentials that are not there or do not load:
	// no ambient credentials where the server runs, or a configured
	// credential that does not parse.
	CredentialMissing CredentialKind = "missing"
	// CredentialExpired is credentials Google refused to exchange for a
	// token: a refresh token expired, revoked or needing reauthentication,
	// or a key that was deleted or disabled.
	CredentialExpired CredentialKind = "expired"
	// CredentialTimeout is a token fetch that was cancelled or timed out,
	// after its one retry: transient, nothing is wrong with the
	// credentials as far as anyone can tell.
	CredentialTimeout CredentialKind = "timeout"
	// CredentialFailed is any other failure to fetch a token.
	CredentialFailed CredentialKind = "failed"
)

// CredentialError is an admin's credentials failing to give a token, with
// what the operator should do about it. It never holds a credential.
type CredentialError struct {
	Admin  string
	Source string
	Kind   CredentialKind
	Err    error
}

func (e *CredentialError) Error() string {
	ambient := e.Source == SourceAmbient
	switch e.Kind {
	case CredentialMissing:
		if ambient {
			return fmt.Sprintf("admin %s: no ambient credentials where the gcp server runs (%v); on that host, as the user grain was installed as, run `gcloud auth application-default login`, then press Test in Settings → Capabilities", e.Admin, e.Err)
		}
		return fmt.Sprintf("admin %s: its configured credential did not load (%v); replace it in Settings → Capabilities", e.Admin, e.Err)
	case CredentialExpired:
		if ambient {
			return fmt.Sprintf("admin %s: its ambient credentials are expired or revoked (Google said: %v); on the host the gcp server runs on, as the user grain was installed as, run `gcloud auth application-default login`, then press Test in Settings → Capabilities", e.Admin, e.Err)
		}
		return fmt.Sprintf("admin %s: Google rejected its configured credential (%v); the key may have been deleted or disabled -- replace it in Settings → Capabilities", e.Admin, e.Err)
	case CredentialTimeout:
		return fmt.Sprintf("admin %s: fetching an access token from Google was cancelled or timed out twice (%v); this is usually transient", e.Admin, e.Err)
	}
	return fmt.Sprintf("admin %s: fetching an access token from Google failed: %v", e.Admin, e.Err)
}

// Unwrap is the cause -- except Google's own refusal, which it hides:
// the API clients (oauth2adapt) replace any error whose chain holds an
// *oauth2.RetrieveError with one of their own, which would lose e.
func (e *CredentialError) Unwrap() error {
	var re *oauth2.RetrieveError
	if errors.As(e.Err, &re) {
		return nil
	}
	return e.Err
}

// NeedsOperator reports whether only a person can clear e: credentials
// to log in again or replace, rather than a fetch to try again.
func (e *CredentialError) NeedsOperator() bool {
	return e.Kind == CredentialMissing || e.Kind == CredentialExpired
}

// tokenContext is the context an admin's credentials are built with: no
// caller's cancellation, and an HTTP client that bounds each token fetch
// by TokenTimeout. base is the transport to use, nil for the default.
func tokenContext(ctx context.Context, base http.RoundTripper) context.Context {
	return context.WithValue(context.WithoutCancel(ctx), oauth2.HTTPClient, &http.Client{Timeout: TokenTimeout, Transport: base})
}

// adminTokenSource is an admin's token source, classifying its failures
// and retrying a cancelled or timed-out fetch once.
type adminTokenSource struct {
	admin Admin
	src   oauth2.TokenSource
}

func (s *adminTokenSource) Token() (*oauth2.Token, error) {
	var err error
	for try := 0; try < 2; try++ {
		var tok *oauth2.Token
		tok, err = s.src.Token()
		if err == nil {
			return tok, nil
		}
		if classifyToken(err) != CredentialTimeout {
			break
		}
	}
	return nil, &CredentialError{Admin: s.admin.Name, Source: s.admin.CredentialSource, Kind: classifyToken(err), Err: err}
}

// expiredCodes are the OAuth error codes Google answers a token request
// with when the credential itself is no good any more.
var expiredCodes = map[string]bool{
	"invalid_grant":       true, // refresh token expired or revoked; "reauth related error (invalid_rapt)"
	"invalid_rapt":        true,
	"unauthorized_client": true,
	"invalid_client":      true, // deleted OAuth client or service-account key
}

// classifyToken says which kind of failure a token fetch's err is.
func classifyToken(err error) CredentialKind {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		if expiredCodes[re.ErrorCode] {
			return CredentialExpired
		}
		if re.Response != nil && (re.Response.StatusCode == http.StatusBadRequest || re.Response.StatusCode == http.StatusUnauthorized) &&
			(strings.Contains(string(re.Body), "invalid_grant") || strings.Contains(string(re.Body), "invalid_rapt")) {
			return CredentialExpired
		}
		return CredentialFailed
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return CredentialTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return CredentialTimeout
	}
	return CredentialFailed
}

// AsCredentialError is the CredentialError in err's chain.
func AsCredentialError(err error) (*CredentialError, bool) {
	var ce *CredentialError
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}
