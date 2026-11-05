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

func (d *DockerHubAPI) ensureJWT() (string, error) {
	if d.jwt != "" {
		return d.jwt, nil
	}
	if strings.TrimSpace(d.Password) == "" {
		return "", nil
	}
	user := strings.TrimSpace(d.Username)
	if user == "" {
		user = strings.TrimSpace(d.Namespace)
	}
	payload, err := json.Marshal(map[string]string{
		"username": user,
		"password": strings.TrimSpace(d.Password),
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, d.baseURL()+"/v2/users/login/", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "grain-ghcr-prune")
	resp, err := d.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("logging in to Docker Hub as %q: %w", user, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Docker Hub login for %q failed (HTTP %d): %s", user, resp.StatusCode, strings.TrimSpace(string(body)))
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

func (d *DockerHubAPI) doJSON(method, targetURL string) (int, []byte, error) {
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
	ns := url.PathEscape(strings.ToLower(strings.TrimSpace(d.Namespace)))
	repo := url.PathEscape(strings.ToLower(strings.TrimSpace(pkg)))
	nextURL := fmt.Sprintf("%s/v2/repositories/%s/%s/tags/?page_size=100", d.baseURL(), ns, repo)

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
	ns := url.PathEscape(strings.ToLower(strings.TrimSpace(d.Namespace)))
	repo := url.PathEscape(strings.ToLower(strings.TrimSpace(pkg)))
	allGone := true
	for _, tag := range v.Tags {
		endpoint := fmt.Sprintf("%s/v2/repositories/%s/%s/tags/%s/", d.baseURL(), ns, repo, url.PathEscape(tag))
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
				Body:   fmt.Sprintf("Docker Hub refused tag deletion for %s/%s:%s (ensure DOCKER_PAT has Delete permission or configure Docker Hub tag retention): %s", d.Namespace, pkg, tag, strings.TrimSpace(string(body))),
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
