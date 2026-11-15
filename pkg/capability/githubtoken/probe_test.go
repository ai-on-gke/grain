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

package githubtoken

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/authfix"
	"github.com/ai-on-gke/grain/pkg/github"
)

// The refusals Probe has to tell apart, each from GitHub's own signal
// rather than its bare status, and none of them repeating the token.
func TestProbeExplainsRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		resp github.ApiResponse
		want string
	}{
		{"bad token", github.ApiResponse{Status: 401, Body: []byte(`{"message":"Bad credentials"}`)}, "bad credentials"},
		{"sso", github.ApiResponse{Status: 403, Headers: map[string]string{"X-GitHub-SSO": "required; url=https://github.com/orgs/acme/sso"}}, "single sign-on"},
		{"rate limited", github.ApiResponse{Status: 403, Headers: map[string]string{"X-RateLimit-Remaining": "0"}}, "rate limiting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := Probe(github.NewFakeTransport(tc.resp), "ghp_secret_value", false, []string{"acme/widgets"})
			if report.OK || !strings.Contains(report.Error, tc.want) {
				t.Fatalf("report = %+v, want a refusal mentioning %q", report, tc.want)
			}
			if strings.Contains(report.Error, "ghp_secret_value") {
				t.Fatalf("error echoed the token: %s", report.Error)
			}
		})
	}
}

// An App installation token has no user: it is identified by how many
// repos its installation sees, and /user is never asked.
func TestProbeIdentifiesAppInstallation(t *testing.T) {
	transport := github.NewFakeTransport(
		github.ApiResponse{Status: 200, Body: []byte(`{"resources":{"core":{"limit":5000,"remaining":10}}}`)},
		github.ApiResponse{Status: 200, Headers: map[string]string{"GitHub-Authentication-Token-Expiration": "2026-10-04 07:00:00 UTC"},
			Body: []byte(`{"total_count":3,"repositories":[]}`)},
		github.ApiResponse{Status: 200, Body: []byte(`{"permissions":{"pull":true,"push":true}}`)},
	)
	report := Probe(transport, "ghs_installation", true, []string{"acme/widgets", "acme/*"})
	if !report.OK || !report.App || report.InstallationRepos != 3 || report.Login != "" {
		t.Fatalf("report = %+v", report)
	}
	if report.Expires != "2026-10-04 07:00:00 UTC" {
		t.Errorf("expires = %q", report.Expires)
	}
	if len(report.Repos) != 1 || !report.Repos[0].Push {
		t.Errorf("repos = %+v, want acme/widgets pushable and the pattern skipped", report.Repos)
	}
	for _, call := range transport.Calls {
		if call.Path == "/user" {
			t.Errorf("an App token was asked for its user")
		}
	}
}

// A front end in front of the API -- one that sends every request to a
// sign-in page, which answers 200 with HTML -- is never a pass: neither
// the redirect off the API's host nor a web page where JSON belongs is
// read as the forge accepting the token.
func TestProbeRefusesALoginPageForTheAPI(t *testing.T) {
	page := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><title>Example Single Sign On</title></html>"))
	}
	login := httptest.NewServer(http.HandlerFunc(page))
	defer login.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, login.URL+"/?continue="+url.QueryEscape(r.URL.String()), http.StatusFound)
	}))
	defer front.Close()
	inPlace := httptest.NewServer(http.HandlerFunc(page))
	defer inPlace.Close()

	probe := func(t *testing.T, base string) Report {
		t.Helper()
		u, _ := url.Parse(base)
		transport := github.NewRealTransport(u.Host)
		transport.Host, transport.UseTLS = u.Host, false
		return Probe(transport, "ghp_secret_value", false, []string{"acme/widgets"})
	}
	t.Run("redirected off the API host", func(t *testing.T) {
		report := probe(t, front.URL)
		if report.OK || len(report.Repos) != 0 || !strings.Contains(report.Error, "redirected to 127.0.0.1") {
			t.Fatalf("report = %+v, want the redirect refused", report)
		}
	})
	t.Run("a registered login page", func(t *testing.T) {
		defer authfix.RegisterRedirect("probe-test", func(loc *url.URL) (authfix.Fix, bool) {
			return authfix.Fix{Summary: "forge login needed", Action: "run `forge-login`"}, loc.Host == strings.TrimPrefix(login.URL, "http://")
		})()
		report := probe(t, front.URL)
		if report.OK || !strings.HasPrefix(report.Error, "forge login needed: run `forge-login`") {
			t.Fatalf("report = %+v, want the login page's own fix", report)
		}
	})
	t.Run("a web page on the API host", func(t *testing.T) {
		report := probe(t, inPlace.URL)
		if report.OK || !strings.Contains(report.Error, "text/html") {
			t.Fatalf("report = %+v, want the HTML answer refused", report)
		}
	})

	// The same pages later on: a repo answered with HTML is not readable,
	// and an untyped body that is a web page is refused too.
	html := github.ApiResponse{Status: 200, Headers: map[string]string{"Content-Type": "text/html"}, Body: []byte("<html></html>")}
	report := Probe(github.NewFakeTransport(
		github.ApiResponse{Status: 200, Headers: map[string]string{"Content-Type": "application/json; charset=utf-8"}, Body: []byte(`{}`)},
		github.ApiResponse{Status: 200, Body: []byte(`{"login":"octo"}`)},
		html,
	), "ghp_secret_value", false, []string{"acme/widgets"})
	if !report.OK || report.Login != "octo" || len(report.Repos) != 1 || report.Repos[0].Read || report.Repos[0].Error == "" {
		t.Fatalf("report = %+v, want acme/widgets unread", report)
	}
	report = Probe(github.NewFakeTransport(github.ApiResponse{Status: 200, Body: []byte("  <!doctype html>")}), "ghp_secret_value", false, nil)
	if report.OK || !strings.Contains(report.Error, "web page") {
		t.Fatalf("report = %+v, want the untyped page refused", report)
	}
}
