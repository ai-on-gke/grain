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

// Probe is the structured form of the named-token check, for a caller
// that holds the token itself rather than a name on the ladder: Team
// settings' Test button on a team's own GitHub credential and on a
// per-repo token (grain/task-1849). CheckCredential answers one sentence
// for a Settings row; a team admin testing the token they just pasted
// wants the same evidence laid out -- who GitHub says the token is, and
// a read/push verdict per repo -- so the UI can draw a ✓/✗ beside each.
//
// It makes the same read-only calls CheckCredential does, through the
// same transport and host grain's other GitHub calls use, and adds the
// two answers only a token-holding caller can put to GitHub: whom the
// token authenticates as (GET /user, or the installation's repository
// list for an App token, which has no user), and the scopes and expiry
// GitHub sends back in that answer's headers. Nothing in a Report
// carries the token.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// MaxProbedRepos bounds how many repos one Probe looks up, for the same
// reason maxCheckedRepos bounds a Settings check: every repo is another
// round trip somebody pressed a button for. A team's own list is usually
// a handful; Report.Skipped says how many were left out.
const MaxProbedRepos = 20

// Report is what Probe found. OK is whether GitHub accepted the token at
// all; a live token that cannot reach a repo is OK with that repo's own
// RepoProbe saying so.
type Report struct {
	OK bool `json:"ok"`
	// Error is why OK is false, in words a team admin can act on. It
	// never contains the token.
	Error string `json:"error,omitempty"`
	// Login is the user the token authenticates as. Empty for an App.
	Login string `json:"login,omitempty"`
	// App is true for a GitHub App installation token, which has no user.
	App bool `json:"app,omitempty"`
	// InstallationRepos is how many repos the App installation can see,
	// when App is set and GitHub said.
	InstallationRepos int `json:"installationRepos,omitempty"`
	// Scopes is a classic token's OAuth scopes (X-OAuth-Scopes). A
	// fine-grained token or App sends none.
	Scopes []string `json:"scopes,omitempty"`
	// Expires is GitHub's own GitHub-Authentication-Token-Expiration
	// header, verbatim, when the token has an expiry GitHub reports.
	Expires string `json:"expires,omitempty"`
	// RateLimitRemaining and RateLimit are the core bucket, for a token
	// that was accepted.
	RateLimitRemaining int `json:"rateLimitRemaining,omitempty"`
	RateLimit          int `json:"rateLimit,omitempty"`
	// Repos is one entry per repo looked up, in the order asked.
	Repos []RepoProbe `json:"repos,omitempty"`
	// Skipped is how many repos past MaxProbedRepos were not looked up.
	Skipped int `json:"skipped,omitempty"`
}

// RepoProbe is one repo's verdict.
type RepoProbe struct {
	Repo  string `json:"repo"`
	Read  bool   `json:"read"`
	Push  bool   `json:"push"`
	Error string `json:"error,omitempty"`
}

// ProbeTransport is the transport Probe should use for host: the same
// github.RealTransport every other grain REST call is made through.
func ProbeTransport(host string, insecureHTTP bool) github.Transport {
	if strings.TrimSpace(host) == "" {
		host = DefaultHost
	}
	return realTransport(host, insecureHTTP)
}

// Probe asks GitHub, as token, whether it is accepted, as whom, and what
// it can do to each of repos. app says the token was minted from a
// GitHub App installation, which answers GET /user with a 403 and is
// identified by its installation's repository list instead.
func Probe(transport github.Transport, token string, app bool, repos []string) Report {
	a := api{transport: transport}
	var report Report
	if strings.TrimSpace(token) == "" {
		report.Error = "no token to test"
		return report
	}

	resp, err := a.get(token, "/rate_limit")
	if err != nil {
		report.Error = transportError(err)
		return report
	}
	if resp.Status != 200 {
		report.Error = refusal(resp)
		return report
	}
	var limit struct {
		Resources struct {
			Core coreLimit `json:"core"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(resp.Body, &limit); err == nil {
		report.RateLimit = limit.Resources.Core.Limit
		report.RateLimitRemaining = limit.Resources.Core.Remaining
	}
	report.OK = true
	report.App = app
	if app {
		a.identifyInstallation(token, &report)
	} else if !a.identifyUser(token, &report) {
		return report
	}

	for _, entry := range repos {
		repo, err := model.ParseRepo(entry)
		if err != nil || strings.ContainsAny(repo.Owner+repo.Name, "*?[") {
			// A pattern names no one repo to look up.
			continue
		}
		if len(report.Repos) == MaxProbedRepos {
			report.Skipped++
			continue
		}
		report.Repos = append(report.Repos, a.probeRepo(token, repo))
	}
	return report
}

// identifyUser fills in the login, scopes and expiry from GET /user. A
// refusal here, after /rate_limit accepted the token, is reported rather
// than ignored; it returns false when the report is final.
func (a api) identifyUser(token string, report *Report) bool {
	resp, err := a.get(token, "/user")
	if err != nil {
		report.OK = false
		report.Error = transportError(err)
		return false
	}
	report.Expires = strings.TrimSpace(headerValue(resp.Headers, "GitHub-Authentication-Token-Expiration"))
	if scopes := headerValue(resp.Headers, "X-OAuth-Scopes"); scopes != "" {
		for _, s := range strings.Split(scopes, ",") {
			if s = strings.TrimSpace(s); s != "" {
				report.Scopes = append(report.Scopes, s)
			}
		}
	}
	switch resp.Status {
	case 200:
		var user struct {
			Login string `json:"login"`
		}
		_ = json.Unmarshal(resp.Body, &user)
		report.Login = user.Login
		return true
	case 403:
		if strings.HasPrefix(token, "ghs_") {
			// An installation token pasted as a plain token.
			report.App = true
			a.identifyInstallation(token, report)
			return true
		}
	}
	report.OK = false
	report.Error = refusal(resp)
	return false
}

// identifyInstallation counts the repos an App installation token can
// see. Best effort: the token is already known to be accepted.
func (a api) identifyInstallation(token string, report *Report) {
	resp, err := a.get(token, "/installation/repositories?per_page=1")
	if err != nil || resp.Status != 200 {
		return
	}
	report.Expires = strings.TrimSpace(headerValue(resp.Headers, "GitHub-Authentication-Token-Expiration"))
	var body struct {
		TotalCount int `json:"total_count"`
	}
	if json.Unmarshal(resp.Body, &body) == nil {
		report.InstallationRepos = body.TotalCount
	}
}

// ProbeRepo is Probe's verdict on one repo alone, for a caller that has
// its identity answer already and takes the rest of its repo verdicts
// from somewhere else -- the Test button on a saved credential, which
// asks gitproxy.Reach first and GitHub directly only for a repo past the
// end of a listing too long to have been read in full.
func ProbeRepo(transport github.Transport, token string, repo model.RepoRef) RepoProbe {
	return api{transport: transport}.probeRepo(token, repo)
}

func (a api) probeRepo(token string, repo model.RepoRef) RepoProbe {
	out := RepoProbe{Repo: repo.String()}
	resp, err := a.get(token, "/repos/"+repo.Owner+"/"+repo.Name)
	if err != nil {
		out.Error = transportError(err)
		return out
	}
	if resp.Status != 200 {
		if resp.Status == 404 {
			out.Error = "not visible to this token (no access, or no such repo)"
		} else {
			out.Error = refusal(resp)
		}
		return out
	}
	var body struct {
		Permissions *struct {
			Pull  bool `json:"pull"`
			Push  bool `json:"push"`
			Admin bool `json:"admin"`
		} `json:"permissions"`
	}
	_ = json.Unmarshal(resp.Body, &body)
	out.Read = true
	if body.Permissions != nil {
		out.Push = body.Permissions.Push || body.Permissions.Admin
	}
	return out
}

// refusal explains a non-200 answer by its status and GitHub's own
// signals, without repeating anything the request carried.
func refusal(resp github.ApiResponse) string {
	if sso := headerValue(resp.Headers, "X-GitHub-SSO"); sso != "" {
		return "the token has not been authorized for this organization's SAML single sign-on; " +
			"authorize it under the token's \"Configure SSO\" on GitHub"
	}
	if resp.Status == 429 || (resp.Status == 403 && headerValue(resp.Headers, "X-RateLimit-Remaining") == "0") {
		return "GitHub is rate limiting this token; try again later"
	}
	msg := githubMessage(resp.Body)
	switch resp.Status {
	case 401:
		return "GitHub rejected the token (bad credentials): it is wrong, revoked or expired"
	case 403:
		if msg != "" {
			return "GitHub refused the call: " + msg
		}
		return "GitHub refused the call (403)"
	}
	if msg != "" {
		return fmt.Sprintf("GitHub answered %d: %s", resp.Status, msg)
	}
	return fmt.Sprintf("GitHub answered %d", resp.Status)
}

// githubMessage is the "message" field of a GitHub error body.
func githubMessage(body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	return strings.TrimSpace(e.Message)
}

func transportError(err error) string {
	var limited *github.RateLimitError
	if errors.As(err, &limited) {
		return "GitHub is rate limiting this token; try again later"
	}
	return "could not reach GitHub: " + err.Error()
}
