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

// Package ghrepos is the GitHub REST surface for making and unmaking
// repositories: creating one in an org, under the authenticated user or
// from a template, reading one back, deleting and listing them, and
// adding one to a GitHub App installation's selected repositories.
//
// Each call takes the token it authenticates with, so the same calls
// serve an App installation token (pkg/capability/githubsandbox, and
// pkg/repocreate for an org) and a user's own token (pkg/repocreate for
// a personal account, which no installation token may create under).
// A non-2xx answer comes back as a *github.Error carrying GitHub's own
// status and body, so a caller can tell "already exists" (422) from "not
// permitted" (403) without parsing a sentence.
package ghrepos

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

// APIVersion pins every request against GitHub's own dated REST
// contract.
const APIVersion = "2022-11-28"

// Repo is one repository as GitHub answers for it: enough to connect it
// (FullName, DefaultBranch) and to add it to an installation (ID).
type Repo struct {
	ID            int64
	Name          string
	FullName      string
	DefaultBranch string
	Private       bool
	HTMLURL       string
	CreatedAt     time.Time
}

// CreateOptions is what a new repository is created with.
type CreateOptions struct {
	Name        string
	Description string
	Private     bool
	// AutoInit creates a first commit holding a README on the default
	// branch, so the repository has a branch to clone and to open pull
	// requests against from the start.
	AutoInit bool
}

// Request sends one authenticated REST request. authorization is the
// whole header value ("Bearer <token>").
func Request(t github.Transport, method, path, authorization string, body []byte) (github.ApiResponse, error) {
	headers := map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": APIVersion,
	}
	if authorization != "" {
		headers["Authorization"] = authorization
	}
	if body != nil {
		headers["Content-Type"] = "application/json"
	}
	return t.Request(method, path, headers, body)
}

// ExpectStatus is nil when resp's status is one of want, and resp as a
// *github.Error otherwise.
func ExpectStatus(resp github.ApiResponse, want ...int) error {
	for _, w := range want {
		if resp.Status == w {
			return nil
		}
	}
	return &github.Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
}

func bearer(token string) string {
	if token == "" {
		return ""
	}
	return "Bearer " + token
}

// IsAlreadyExists reports whether err is GitHub refusing a create
// because the name is taken (422 "name already exists on this account").
func IsAlreadyExists(err error) bool {
	var apiErr *github.Error
	if !asError(err, &apiErr) || apiErr.Status != 422 {
		return false
	}
	return strings.Contains(strings.ToLower(string(apiErr.Body)), "already exists")
}

// Status is err's HTTP status when it is a *github.Error, and 0 otherwise.
func Status(err error) int {
	var apiErr *github.Error
	if asError(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

// Message is GitHub's own "message" field from err's body, or err's text.
func Message(err error) string {
	var apiErr *github.Error
	if asError(err, &apiErr) {
		var body struct {
			Message string `json:"message"`
			Errors  []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if json.Unmarshal(apiErr.Body, &body) == nil && body.Message != "" {
			msg := body.Message
			for _, e := range body.Errors {
				if e.Message != "" {
					msg += ": " + e.Message
				}
			}
			return msg
		}
		return fmt.Sprintf("HTTP %d", apiErr.Status)
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func asError(err error, target **github.Error) bool { return errors.As(err, target) }

func decodeRepo(resp github.ApiResponse) (Repo, error) {
	var raw struct {
		ID            int64     `json:"id"`
		Name          string    `json:"name"`
		FullName      string    `json:"full_name"`
		DefaultBranch string    `json:"default_branch"`
		Private       bool      `json:"private"`
		HTMLURL       string    `json:"html_url"`
		CreatedAt     time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return Repo{}, fmt.Errorf("parsing repository: %w", err)
	}
	return Repo{ID: raw.ID, Name: raw.Name, FullName: raw.FullName, DefaultBranch: raw.DefaultBranch,
		Private: raw.Private, HTMLURL: raw.HTMLURL, CreatedAt: raw.CreatedAt}, nil
}

func create(t github.Transport, token, path string, body map[string]any) (Repo, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return Repo{}, err
	}
	resp, err := Request(t, "POST", path, bearer(token), data)
	if err != nil {
		return Repo{}, err
	}
	if err := ExpectStatus(resp, 201); err != nil {
		return Repo{}, err
	}
	return decodeRepo(resp)
}

func createBody(opts CreateOptions) map[string]any {
	body := map[string]any{"name": opts.Name, "private": opts.Private}
	if opts.Description != "" {
		body["description"] = opts.Description
	}
	if opts.AutoInit {
		body["auto_init"] = true
	}
	return body
}

// CreateInOrg is POST /orgs/{org}/repos.
func CreateInOrg(t github.Transport, token, org string, opts CreateOptions) (Repo, error) {
	return create(t, token, "/orgs/"+url.PathEscape(org)+"/repos", createBody(opts))
}

// CreateForUser is POST /user/repos: a repository under the account
// token belongs to. Only a user's own token may call it; an App
// installation token is refused.
func CreateForUser(t github.Transport, token string, opts CreateOptions) (Repo, error) {
	return create(t, token, "/user/repos", createBody(opts))
}

// CreateFromTemplate is POST /repos/{template}/generate: owner/opts.Name
// made from the template repository's default branch.
func CreateFromTemplate(t github.Transport, token, templateOwner, templateRepo, owner string, opts CreateOptions) (Repo, error) {
	body := map[string]any{"owner": owner, "name": opts.Name, "private": opts.Private}
	if opts.Description != "" {
		body["description"] = opts.Description
	}
	return create(t, token, "/repos/"+url.PathEscape(templateOwner)+"/"+url.PathEscape(templateRepo)+"/generate", body)
}

// Get is GET /repos/{owner}/{name}; found is false on a 404.
func Get(t github.Transport, token, owner, name string) (repo Repo, found bool, err error) {
	resp, err := Request(t, "GET", "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), bearer(token), nil)
	if err != nil {
		return Repo{}, false, err
	}
	if resp.Status == 404 {
		return Repo{}, false, nil
	}
	if err := ExpectStatus(resp, 200); err != nil {
		return Repo{}, false, err
	}
	repo, err = decodeRepo(resp)
	return repo, err == nil, err
}

// BranchExists is GET /repos/{owner}/{name}/branches/{branch}.
func BranchExists(t github.Transport, token, owner, name, branch string) (bool, error) {
	resp, err := Request(t, "GET", "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+
		"/branches/"+url.PathEscape(branch), bearer(token), nil)
	if err != nil {
		return false, err
	}
	if resp.Status == 404 {
		return false, nil
	}
	if err := ExpectStatus(resp, 200); err != nil {
		return false, err
	}
	return true, nil
}

// Delete is DELETE /repos/{owner}/{name}. An already-gone repository is
// success.
func Delete(t github.Transport, token, owner, name string) error {
	resp, err := Request(t, "DELETE", "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), bearer(token), nil)
	if err != nil {
		return err
	}
	if resp.Status == 404 {
		return nil
	}
	return ExpectStatus(resp, 204)
}

// ListOrgPrivate lists up to 100 private repositories in org.
func ListOrgPrivate(t github.Transport, token, org string) ([]Repo, error) {
	resp, err := Request(t, "GET", "/orgs/"+url.PathEscape(org)+"/repos?type=private&per_page=100", bearer(token), nil)
	if err != nil {
		return nil, err
	}
	if err := ExpectStatus(resp, 200); err != nil {
		return nil, err
	}
	var raw []struct {
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, fmt.Errorf("parsing repo list: %w", err)
	}
	out := make([]Repo, len(raw))
	for i, r := range raw {
		out[i] = Repo{Name: r.Name, CreatedAt: r.CreatedAt}
	}
	return out, nil
}

// AccountKind is GET /users/{login}'s type: "User" or "Organization",
// and found false on a 404.
func AccountKind(t github.Transport, token, login string) (kind string, found bool, err error) {
	resp, err := Request(t, "GET", "/users/"+url.PathEscape(login), bearer(token), nil)
	if err != nil {
		return "", false, err
	}
	if resp.Status == 404 {
		return "", false, nil
	}
	if err := ExpectStatus(resp, 200); err != nil {
		return "", false, err
	}
	var raw struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return "", false, fmt.Errorf("parsing account: %w", err)
	}
	return raw.Type, true, nil
}

// AuthenticatedLogin is GET /user's login: whose token token is.
func AuthenticatedLogin(t github.Transport, token string) (string, error) {
	resp, err := Request(t, "GET", "/user", bearer(token), nil)
	if err != nil {
		return "", err
	}
	if err := ExpectStatus(resp, 200); err != nil {
		return "", err
	}
	var raw struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return "", fmt.Errorf("parsing user: %w", err)
	}
	return raw.Login, nil
}

// InstallationReaches reports whether the installation token token can
// see owner/name, from GET /installation/repositories.
func InstallationReaches(t github.Transport, token, owner, name string) (bool, error) {
	want := strings.ToLower(owner + "/" + name)
	for page := 1; page <= 30; page++ {
		resp, err := Request(t, "GET", fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), bearer(token), nil)
		if err != nil {
			return false, err
		}
		if err := ExpectStatus(resp, 200); err != nil {
			return false, err
		}
		var raw struct {
			Repositories []struct {
				FullName string `json:"full_name"`
			} `json:"repositories"`
		}
		if err := json.Unmarshal(resp.Body, &raw); err != nil {
			return false, fmt.Errorf("parsing installation repositories: %w", err)
		}
		for _, r := range raw.Repositories {
			if strings.ToLower(r.FullName) == want {
				return true, nil
			}
		}
		if len(raw.Repositories) < 100 {
			return false, nil
		}
	}
	return false, nil
}

// AddToInstallation is PUT /user/installations/{id}/repositories/{repo}:
// adds the repository to an installation limited to selected
// repositories. Only a user token with access to both may call it.
func AddToInstallation(t github.Transport, userToken string, installationID, repoID int64) error {
	resp, err := Request(t, "PUT", fmt.Sprintf("/user/installations/%d/repositories/%d", installationID, repoID), bearer(userToken), nil)
	if err != nil {
		return err
	}
	return ExpectStatus(resp, 204, 304)
}
