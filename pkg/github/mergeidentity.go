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

package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// A merge identity of grain's own (task 4386).
//
// A deployment whose git connection is a person's token merged as that
// person, so the forge's merged_by could not tell grain's merges from the
// person's own, and a reviewed-merge gate that trusts "merged by grain"
// (pkg/reviewedmerge, vars.GRAIN_MERGE_LOGINS) had to trust every merge
// the person made by hand. A git connection may now name a merge
// credential (model.TeamGitConnection.MergeCredential) -- a GitHub App's
// -- and the token source behind a RESTClient that implements
// MergeTokenSource hands its token out for the merge alone: the PUT
// /pulls/{n}/merge and the enqueuePullRequest mutation. Cloning, pushing,
// opening the pull request and everything else stay on the push
// credential.

// MergeTokenSource is a TokenSource that may hold a separate credential
// for merging owner/repo's pull requests. configured reports whether
// owner/repo has one; token is nil when it does but no token could be
// had for it (an App whose installation token could not be minted).
type MergeTokenSource interface {
	MergeTokenFor(owner, repo string) (token *string, configured bool)
	// MergeIdentityFor names owner/repo's merge credential for a person
	// reading what merged a pull request -- "grain-merge ([bot] App
	// grain-merge-bot)" -- or "" when owner/repo has none.
	MergeIdentityFor(owner, repo string) string
}

// MergeRefusalSource is a MergeTokenSource that can say why it gave no
// merge token when the reason is not the credential's own (an App that
// could not mint one): "" leaves ErrNoMergeToken's own hint standing.
type MergeRefusalSource interface {
	MergeRefusalFor(owner, repo string) string
}

// ErrNoMergeToken is a merge refused before it was asked because the
// repository's merge credential gave no token. It is never retried under
// the push credential: that is exactly the merge-as-a-person the merge
// credential is there to stop.
type ErrNoMergeToken struct {
	Owner, Repo, Identity string
	// Reason, when set, is why (MergeRefusalSource), in place of the
	// hint about the App's installation.
	Reason string
}

func (e *ErrNoMergeToken) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("%s/%s merges through %s, which gave no token: %s; grain does not merge under the push credential instead",
			e.Owner, e.Repo, e.Identity, e.Reason)
	}
	return fmt.Sprintf("%s/%s merges through %s, which gave no token: grain does not merge under the push credential instead "+
		"(check the App's installation on %s/%s and its private key)", e.Owner, e.Repo, e.Identity, e.Owner, e.Repo)
}

// mergeTokens is a TokenSource answering every repository with one
// token: the merge credential's, for a client that makes one merge.
type mergeTokens struct{ token *string }

func (m mergeTokens) TokenFor(owner, repo string) *string { return m.token }

// forMerge is c as the merge of a pull request in owner/repo should go
// out: c itself when its token source holds no merge credential for the
// repository, otherwise a copy authenticated with that credential's
// token, or ErrNoMergeToken when it gave none.
func (c *RESTClient) forMerge(owner, repo string) (*RESTClient, error) {
	src, ok := c.Tokens.(MergeTokenSource)
	if !ok {
		return c, nil
	}
	token, configured := src.MergeTokenFor(owner, repo)
	if !configured {
		return c, nil
	}
	if token == nil || *token == "" {
		refused := &ErrNoMergeToken{Owner: owner, Repo: repo, Identity: src.MergeIdentityFor(owner, repo)}
		if r, ok := src.(MergeRefusalSource); ok {
			refused.Reason = r.MergeRefusalFor(owner, repo)
		}
		return nil, refused
	}
	merger := *c
	merger.Tokens = mergeTokens{token: token}
	return &merger, nil
}

// MergeIdentity names the credential c merges owner/repo's pull requests
// with when that is not its push credential (MergeTokenSource), or "".
func (c *RESTClient) MergeIdentity(owner, repo string) string {
	if src, ok := c.Tokens.(MergeTokenSource); ok {
		return src.MergeIdentityFor(owner, repo)
	}
	return ""
}

// RepoVariable is the value of owner/repo's GitHub Actions variable
// name (GET /repos/{owner}/{repo}/actions/variables/{name}), false when
// the repository has none by that name. An organization's variables are
// not read. Reading needs the Actions variables read permission (a
// classic token's repo scope, or an App's variables: read).
func (c *RESTClient) RepoVariable(owner, repo, name string) (string, bool, error) {
	resp, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s/actions/variables/%s", owner, repo, url.PathEscape(name)))
	if err != nil {
		return "", false, err
	}
	if resp.Status == http.StatusNotFound {
		return "", false, nil
	}
	if resp.Status != http.StatusOK {
		return "", false, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return "", false, err
	}
	return data.Value, true, nil
}

// AccountType is GitHub's type for login (GET /users/{login}): "User"
// for an account somebody signs in to -- a person's, or a machine user's
// -- "Bot" for a GitHub App's `<slug>[bot]`, "Organization" for an
// organization. owner/repo picks the credential the call goes out with.
func (c *RESTClient) AccountType(owner, repo, login string) (string, error) {
	resp, err := c.get(owner, repo, "/users/"+url.PathEscape(login))
	if err != nil {
		return "", err
	}
	if resp.Status != http.StatusOK {
		return "", &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return "", err
	}
	return data.Type, nil
}
