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
	"strings"
	"testing"

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
