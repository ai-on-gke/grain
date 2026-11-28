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

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const defaultDockerHubBaseURL = "https://hub.docker.com"

// DockerHubAPI implements deleter against the Docker Hub v2 API
// (`https://hub.docker.com/v2/...`), grouping tags in each repository by
// manifest digest so BuildPlan's version-level safety rule (never delete a
// manifest while any tag pointing at it is still live) holds identically on
// Docker Hub and GHCR.
type DockerHubAPI struct {
	Client    *http.Client
	BaseURL   string
	Namespace string
	Username  string
	Password  string

	// BranchLister lists live Git branch tags (`/` -> `..`) from the source
	// GitHub repository.
	BranchLister func() (map[string]bool, error)

	jwt        string
	orgToken   bool
	nextID     int64
	byID       map[int64]Version
	httpClient *http.Client
}

type dockerHubLoginResponse struct {
	Token  string `json:"token"`
	Detail string `json:"detail"`
}

type dockerHubTagsPage struct {
	Next    string            `json:"next"`
	Results []dockerHubTagRow `json:"results"`
}

type dockerHubTagRow struct {
	Name        string               `json:"name"`
	Digest      string               `json:"digest"`
	LastUpdated string               `json:"last_updated"`
	TagLastPush string               `json:"tag_last_pushed"`
	Images      []dockerHubTagDigest `json:"images"`
}

type dockerHubTagDigest struct {
	Digest string `json:"digest"`
}

func (d *DockerHubAPI) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	if d.httpClient == nil {
		d.httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return d.httpClient
}

func (d *DockerHubAPI) baseURL() string {
	if strings.TrimSpace(d.BaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(d.BaseURL), "/")
	}
	return defaultDockerHubBaseURL
}

// orgAccountLoginRefusal is the detail /v2/users/login/ answers when it is
// handed an organization name: that route only logs users in.
const orgAccountLoginRefusal = "Cannot log into an organization account"

// orgAccessTokenPrefix starts every Docker Hub organization access token
// (OAT); personal access tokens start dckr_pat_.
const orgAccessTokenPrefix = "dckr_oat_"

// ErrDockerHubOrgLogin reports that Docker Hub refused to log in as an
// organization account with a token that is not an organization access
// token. Its message names the repair, since nothing in this run can make
// the pair work.
type ErrDockerHubOrgLogin struct {
	User   string
	Status int
	Body   string
}

func (e *ErrDockerHubOrgLogin) Error() string {
	return fmt.Sprintf("Docker Hub login for %q failed (HTTP %d): %s: %q is an organization, and Docker Hub's API logs in only users with a personal access token or organizations with an organization access token (dckr_oat_...). "+
		"Fix: set vars.DOCKERHUB_USERNAME to the Docker Hub user who owns secrets.DOCKER_PAT, a personal access token with Read, Write, Delete permission "+
		"(or store an organization access token for %q with Tag Read and Tag Admin scopes in secrets.DOCKER_PAT)",
		e.User, e.Status, e.Body, e.User, e.User)
}

func (d *DockerHubAPI) loginUser() string {
	if user := strings.TrimSpace(d.Username); user != "" {
		return user
	}
	return strings.TrimSpace(d.Namespace)
}

// ensureJWT returns the bearer token for Docker Hub API calls, logging in
// first if this run has none. A personal access token logs in through
// /v2/users/login/; an organization access token cannot, so it is exchanged
// through /v2/auth/token with the organization name as identifier. A token
// without the dckr_oat_ prefix that /v2/users/login/ refuses as an
// organization account gets the same exchange before the run gives up.
func (d *DockerHubAPI) ensureJWT() (string, error) {
	if d.jwt != "" {
		return d.jwt, nil
	}
	secret := strings.TrimSpace(d.Password)
	if secret == "" {
		return "", nil
	}
	user := d.loginUser()
	if strings.HasPrefix(secret, orgAccessTokenPrefix) {
		return d.loginOrgAccessToken(user, secret)
	}
	status, body, err := d.postLogin("/v2/users/login/", map[string]string{
		"username": user,
		"password": secret,
	})
	if err != nil {
		return "", fmt.Errorf("logging in to Docker Hub as %q: %w", user, err)
	}
	if status < 200 || status >= 300 {
		detail := strings.TrimSpace(string(body))
		if status == http.StatusUnauthorized && strings.Contains(detail, orgAccountLoginRefusal) {
			if jwt, oatErr := d.loginOrgAccessToken(user, secret); oatErr == nil {
				return jwt, nil
			}
			return "", &ErrDockerHubOrgLogin{User: user, Status: status, Body: detail}
		}
		return "", fmt.Errorf("Docker Hub login for %q failed (HTTP %d): %s", user, status, detail)
	}
	var out dockerHubLoginResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decoding Docker Hub login response: %w", err)
	}
	if strings.TrimSpace(out.Token) == "" {
		return "", fmt.Errorf("Docker Hub login for %q returned an empty token", user)
	}
	d.jwt = strings.TrimSpace(out.Token)
	return d.jwt, nil
}

// loginOrgAccessToken exchanges an organization access token for a
// short-lived bearer token. Docker Hub accepts such bearers only on the
// namespace-scoped routes (/v2/namespaces/{namespace}/repositories/...), so
// it also switches this client onto them.
func (d *DockerHubAPI) loginOrgAccessToken(org, secret string) (string, error) {
	status, body, err := d.postLogin("/v2/auth/token", map[string]string{
		"identifier": org,
		"secret":     secret,
	})
	if err != nil {
		return "", fmt.Errorf("exchanging the Docker Hub organization access token for %q: %w", org, err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("Docker Hub organization access token exchange for %q failed (HTTP %d): %s", org, status, strings.TrimSpace(string(body)))
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decoding Docker Hub access token response: %w", err)
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return "", fmt.Errorf("Docker Hub organization access token exchange for %q returned an empty token", org)
	}
	d.jwt = strings.TrimSpace(out.AccessToken)
	d.orgToken = true
	return d.jwt, nil
}

func (d *DockerHubAPI) postLogin(path string, fields map[string]string) (int, []byte, error) {
	payload, err := json.Marshal(fields)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, d.baseURL()+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "grain-ghcr-prune")
	resp, err := d.client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return resp.StatusCode, body, nil
}

// tagsURL is the tag collection of one repository, or one tag in it when tag
// is non-empty. A user's bearer uses the /v2/repositories/ routes; an
// organization access token's bearer is refused there and uses the
// namespace-scoped ones.
func (d *DockerHubAPI) tagsURL(pkg, tag string) string {
	ns := url.PathEscape(strings.ToLower(strings.TrimSpace(d.Namespace)))
	repo := url.PathEscape(strings.ToLower(strings.TrimSpace(pkg)))
	if d.orgToken {
		u := fmt.Sprintf("%s/v2/namespaces/%s/repositories/%s/tags", d.baseURL(), ns, repo)
		if tag != "" {
			u += "/" + url.PathEscape(tag)
		}
		return u
	}
	u := fmt.Sprintf("%s/v2/repositories/%s/%s/tags/", d.baseURL(), ns, repo)
	if tag != "" {
		u += url.PathEscape(tag) + "/"
	}
	return u
}

func (d *DockerHubAPI) doJSON(method, targetURL string) (int, []byte, error) {
	status, body, err := d.doJSONOnce(method, targetURL)
	if err == nil && status == http.StatusUnauthorized && d.jwt != "" {
		// Docker Hub bearers are short-lived (an organization access
		// token's lasts ten minutes), so log in again once before calling
		// the request refused.
		d.jwt = ""
		return d.doJSONOnce(method, targetURL)
	}
	return status, body, err
}

func (d *DockerHubAPI) doJSONOnce(method, targetURL string) (int, []byte, error) {
	jwt, err := d.ensureJWT()
	if err != nil {
		return 0, nil, err
	}
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		targetURL = d.baseURL() + targetURL
	}
	req, err := http.NewRequest(method, targetURL, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "grain-ghcr-prune")
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func (d *DockerHubAPI) ListBranchTags() (map[string]bool, error) {
	if d.BranchLister == nil {
		return nil, fmt.Errorf("no GitHub branch lister configured")
	}
	return d.BranchLister()
}

func (d *DockerHubAPI) ListVersions(pkg string) ([]Version, error) {
	// Log in before building the URL: which routes this run uses depends
	// on the kind of token Docker Hub accepted.
	if _, err := d.ensureJWT(); err != nil {
		return nil, fmt.Errorf("listing Docker Hub tags for %s/%s: %w", d.Namespace, pkg, err)
	}
	nextURL := d.tagsURL(pkg, "") + "?page_size=100"

	type grouped struct {
		digest  string
		tags    []string
		created time.Time
	}
	var order []string
	groups := map[string]*grouped{}

	for nextURL != "" {
		status, body, err := d.doJSON(http.MethodGet, nextURL)
		if err != nil {
			return nil, fmt.Errorf("listing Docker Hub tags for %s/%s: %w", d.Namespace, pkg, err)
		}
		if status == http.StatusNotFound {
			return nil, nil
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("listing Docker Hub tags for %s/%s: HTTP %d: %s", d.Namespace, pkg, status, strings.TrimSpace(string(body)))
		}
		var page dockerHubTagsPage
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decoding Docker Hub tags for %s/%s: %w", d.Namespace, pkg, err)
		}
		for _, row := range page.Results {
			tag := strings.TrimSpace(row.Name)
			if tag == "" {
				continue
			}
			digest := strings.TrimSpace(row.Digest)
			if digest == "" && len(row.Images) > 0 {
				digest = strings.TrimSpace(row.Images[0].Digest)
			}
			key := digest
			if key == "" {
				key = "tag:" + tag
			}
			ts := parseDockerHubTime(row.TagLastPush)
			if ts.IsZero() {
				ts = parseDockerHubTime(row.LastUpdated)
			}
			g, ok := groups[key]
			if !ok {
				g = &grouped{digest: digest, created: ts}
				groups[key] = g
				order = append(order, key)
			}
			g.tags = append(g.tags, tag)
			if ts.After(g.created) {
				g.created = ts
			}
		}
		nextURL = strings.TrimSpace(page.Next)
	}

	if d.byID == nil {
		d.byID = map[int64]Version{}
	}
	versions := make([]Version, 0, len(order))
	for _, key := range order {
		g := groups[key]
		sort.Strings(g.tags)
		d.nextID++
		id := d.nextID
		digest := g.digest
		if digest == "" {
			digest = key
		}
		v := Version{
			Package: pkg,
			ID:      id,
			Digest:  digest,
			Tags:    g.tags,
			Created: g.created,
		}
		d.byID[id] = v
		versions = append(versions, v)
	}
	return versions, nil
}

func (d *DockerHubAPI) DeleteVersion(pkg string, id int64) error {
	v, ok := d.byID[id]
	if !ok {
		return &ErrAlreadyGone{Body: fmt.Sprintf("%s id=%d", pkg, id)}
	}
	if _, err := d.ensureJWT(); err != nil {
		return err
	}
	allGone := true
	for _, tag := range v.Tags {
		endpoint := d.tagsURL(pkg, tag)
		status, body, err := d.doJSON(http.MethodDelete, endpoint)
		if err != nil {
			return err
		}
		switch {
		case status == http.StatusNoContent || status == http.StatusOK || status == http.StatusAccepted:
			allGone = false
		case status == http.StatusNotFound:
			continue
		case status == http.StatusForbidden || status == http.StatusUnauthorized:
			return &ErrRefused{
				Status: status,
				Body:   fmt.Sprintf("Docker Hub refused tag deletion for %s/%s:%s (ensure DOCKER_PAT %s or configure Docker Hub tag retention): %s", d.Namespace, pkg, tag, d.deleteGrant(), strings.TrimSpace(string(body))),
			}
		default:
			return fmt.Errorf("Docker Hub DELETE %s/%s:%s returned HTTP %d: %s", d.Namespace, pkg, tag, status, strings.TrimSpace(string(body)))
		}
	}
	if allGone {
		return &ErrAlreadyGone{Body: fmt.Sprintf("%s %s", pkg, v.Digest)}
	}
	return nil
}

// deleteGrant names what DOCKER_PAT needs to delete tags, in the terms of
// the kind of token it is.
func (d *DockerHubAPI) deleteGrant() string {
	if d.orgToken {
		return "has the Tag Admin scope on this repository"
	}
	return "has Delete permission"
}

func parseDockerHubTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
