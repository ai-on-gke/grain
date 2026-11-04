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
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/ai-on-gke/grain/pkg/authfix"
)

// A proxy in front of a daemon that wants its caller to sign in answers
// an API call with a redirect to its login page, or with that page
// itself. Followed, the redirect ends in HTML, and the CLI used to say
// "invalid character '<'" -- a JSON parse error about a page nobody saw
// -- or, worse, sat on a login form that never completes. So HTTPClient
// follows no redirect to a sign-in page and reads no HTML where it asked
// for JSON: either ends the call with a LoginRequiredError, which says
// what to run (design/spec/external-cli/external-cli.spec.md#XC-10).

// LoginRequiredError is a call a proxy answered with its sign-in page
// rather than the daemon's answer.
type LoginRequiredError struct {
	// Provider is who wants the sign-in: the login page's host.
	Provider string
	// Fix is what renews the session, from a classifier a wrapper
	// registered with authfix.RegisterRedirect; "" when none knew it.
	Fix string
	// URL is the page the call was sent to.
	URL string
}

func (e *LoginRequiredError) Error() string {
	if e.Fix != "" {
		return fmt.Sprintf("not signed in to %s: run %s", e.Provider, strings.TrimPrefix(e.Fix, "run "))
	}
	return "unexpected HTML sign-in response from " + e.URL
}

// contextTokenError is ContextToken's own failure, which says what
// happened in the caller's words -- "not signed in to <provider>: run
// <fix>" -- and is returned as it is.
type contextTokenError struct{ err error }

func (e contextTokenError) Error() string { return e.err.Error() }
func (e contextTokenError) Unwrap() error { return e.err }

// redirectClient is httpClient with its redirects checked by
// refuseLoginRedirect. The client is copied, never changed: c.HTTP may
// be shared.
func (c *HTTPClient) redirectClient() *http.Client {
	base := c.httpClient()
	hc := *base
	next := base.CheckRedirect
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := refuseLoginRedirect(req, via); err != nil {
			return err
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &hc
}

// refuseLoginRedirect is the LoginRequiredError for a redirect to a login
// page: one a registered classifier knows, or one to another origin that
// reads as a sign-in (signInLocation). A redirect anywhere else -- an
// artifact download handed to storage -- is followed.
func refuseLoginRedirect(req *http.Request, via []*http.Request) error {
	if fix, ok := authfix.ClassifyRedirect(req.URL); ok {
		return &LoginRequiredError{Provider: req.URL.Hostname(), Fix: fix.Action, URL: req.URL.String()}
	}
	if len(via) > 0 && via[0].URL.Host != req.URL.Host && signInLocation(req.URL) {
		return &LoginRequiredError{Provider: req.URL.Hostname(), URL: req.URL.String()}
	}
	return nil
}

func signInLocation(u *url.URL) bool {
	where := strings.ToLower(u.Hostname() + u.Path)
	for _, w := range []string{"login", "signin", "sign-in", "sign_in", "oauth", "sso", "accounts."} {
		if strings.Contains(where, w) {
			return true
		}
	}
	return false
}

// signInResponse is the LoginRequiredError for resp being an HTML page
// where the daemon answers JSON: a 2xx, 401 or 403 a proxy sent instead.
// Anything else is the daemon's own answer, left for the caller to read.
func signInResponse(baseURL string, resp *http.Response) error {
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode <= 299:
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
	default:
		return nil
	}
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if media != "text/html" {
		return nil
	}
	where := baseURL
	if resp.Request != nil && resp.Request.URL != nil {
		where = resp.Request.URL.String()
		if fix, ok := authfix.ClassifyRedirect(resp.Request.URL); ok {
			return &LoginRequiredError{Provider: resp.Request.URL.Hostname(), Fix: fix.Action, URL: where}
		}
	}
	host := where
	if u, err := url.Parse(where); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	return &LoginRequiredError{Provider: host, URL: where}
}
