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

package gitproxy

// What a GitHub App credential's installation on one account looks
// like, asked as the App itself: what pkg/repocreate needs to decide
// whether the deployment's own App may create a repository in an org
// (Administration: write), and whether a repository it made or found is
// one the installation reaches (all repositories, or a selected few).

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/ghrepos"
)

// AppInstallation is a GitHub App's installation on one account.
type AppInstallation struct {
	ID int64
	// Account is the login it is installed on.
	Account string
	// RepositorySelection is "all" or "selected".
	RepositorySelection string
	// Permissions is what the installation was granted, by permission
	// name ("administration": "write").
	Permissions map[string]string
}

// ErrAppNotInstalled is AppInstallationOn's answer for an account the App
// has no installation on.
var ErrAppNotInstalled = errors.New("the GitHub App is not installed on that account")

// appRequest sends one request authenticated as the App credential name
// itself (a fresh JWT), against the API its host serves.
func (c *CredentialSet) appRequest(name, method, path string) (github.ApiResponse, error) {
	appCred, ok := c.loadAppCredential(name)
	if !ok {
		return github.ApiResponse{}, fmt.Errorf("credential %q is not a GitHub App", name)
	}
	transport, err := c.appTransportFor(name)
	if err != nil {
		return github.ApiResponse{}, err
	}
	jwt, err := appJWT(appCred.AppID, appCred.PrivateKey, c.now())
	if err != nil {
		return github.ApiResponse{}, err
	}
	return ghrepos.Request(transport, method, path, "Bearer "+jwt, nil)
}

// AppInstallationOn is the App credential name's installation on owner,
// an "Organization" or a "User" account (GitHub asks each its own way),
// and ErrAppNotInstalled when there is none.
func (c *CredentialSet) AppInstallationOn(name, owner, accountType string) (AppInstallation, error) {
	path := "/users/" + url.PathEscape(owner) + "/installation"
	if accountType == "Organization" {
		path = "/orgs/" + url.PathEscape(owner) + "/installation"
	}
	resp, err := c.appRequest(name, "GET", path)
	if err != nil {
		return AppInstallation{}, err
	}
	if resp.Status == 404 {
		return AppInstallation{}, ErrAppNotInstalled
	}
	if err := ghrepos.ExpectStatus(resp, 200); err != nil {
		return AppInstallation{}, err
	}
	var raw struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
		RepositorySelection string            `json:"repository_selection"`
		Permissions         map[string]string `json:"permissions"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return AppInstallation{}, fmt.Errorf("gitproxy: decoding installation: %w", err)
	}
	return AppInstallation{ID: raw.ID, Account: raw.Account.Login,
		RepositorySelection: raw.RepositorySelection, Permissions: raw.Permissions}, nil
}

// AppInstallationToken mints an installation-wide access token for
// installation id of the App credential name.
func (c *CredentialSet) AppInstallationToken(name string, installationID int64) (string, error) {
	appCred, ok := c.loadAppCredential(name)
	if !ok {
		return "", fmt.Errorf("credential %q is not a GitHub App", name)
	}
	transport, err := c.appTransportFor(name)
	if err != nil {
		return "", err
	}
	appCred.InstallationID = strconv.FormatInt(installationID, 10)
	token, _, err := mintInstallationToken(transport, appCred, c.now(), "")
	return token, err
}

// AppPage is the App credential name's own page on its host
// (https://github.com/apps/<slug>), from GET /app: where its install
// button is.
func (c *CredentialSet) AppPage(name string) (string, error) {
	resp, err := c.appRequest(name, "GET", "/app")
	if err != nil {
		return "", err
	}
	if err := ghrepos.ExpectStatus(resp, 200); err != nil {
		return "", err
	}
	var raw struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return "", fmt.Errorf("gitproxy: decoding app: %w", err)
	}
	return raw.HTMLURL, nil
}
