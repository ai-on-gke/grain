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

package githubsim

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/github"
)

// Accounts is the account-and-repository half of GitHub, as a
// github.Transport of its own: users and orgs, one GitHub App's
// installations on them, user tokens, and the repositories they hold.
// Sim answers for one repository's issues and pull requests; Accounts
// answers for making repositories (pkg/repocreate), where what matters
// is who may create what where -- an installation token in an org it
// has Administration: write on, a user token under its own account, and
// never an installation token under a personal account.
//
// Any Authorization other than a token Accounts minted or was given is
// taken as the App's own JWT: Accounts checks no signatures.
type Accounts struct {
	// Kinds is every account, by login: "User" or "Organization".
	Kinds map[string]string
	// Installations is the App's installation on each account, by login.
	Installations map[string]*AppInstall
	// UserTokens maps a user token to the login it belongs to.
	UserTokens map[string]string
	// Repos is every repository, by "owner/name".
	Repos map[string]*AccountRepo
	// AppHTMLURL is what GET /app answers as html_url.
	AppHTMLURL string
	// BranchAfter is how many reads of a new repository's branch answer
	// 404 before it exists, so a test sees a create wait for it.
	BranchAfter int

	// Calls is every request, in order.
	Calls []Call

	mu          sync.Mutex
	nextID      int64
	instTokens  map[string]int64
	branchReads map[string]int
}

// AppInstall is the App's installation on one account.
type AppInstall struct {
	ID int64
	// Selection is "all" or "selected".
	Selection   string
	Permissions map[string]string
	// RepoIDs is the selected repositories, when Selection is "selected".
	RepoIDs []int64
}

// AccountRepo is one repository Accounts holds.
type AccountRepo struct {
	ID            int64
	Owner, Name   string
	Private       bool
	Description   string
	DefaultBranch string
	// HasBranch is whether DefaultBranch has a commit.
	HasBranch bool
	// Template is the repository it was generated from, if any.
	Template string
}

// NewAccounts is an Accounts with nothing in it.
func NewAccounts() *Accounts {
	return &Accounts{
		Kinds: map[string]string{}, Installations: map[string]*AppInstall{},
		UserTokens: map[string]string{}, Repos: map[string]*AccountRepo{},
		AppHTMLURL: "https://github.com/apps/grain-test",
	}
}

// AddRepo seeds an existing repository with a default branch.
func (a *Accounts) AddRepo(owner, name string, private bool) *AccountRepo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.addRepoLocked(owner, name, private, true)
}

func (a *Accounts) addRepoLocked(owner, name string, private, hasBranch bool) *AccountRepo {
	a.nextID++
	r := &AccountRepo{ID: 7000 + a.nextID, Owner: owner, Name: name, Private: private, DefaultBranch: "main", HasBranch: hasBranch}
	a.Repos[strings.ToLower(owner+"/"+name)] = r
	return r
}

// Repo is the repository owner/name, or nil.
func (a *Accounts) Repo(owner, name string) *AccountRepo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Repos[strings.ToLower(owner+"/"+name)]
}

var (
	accUserRe        = regexp.MustCompile(`^/users/([^/]+)$`)
	accInstallRe     = regexp.MustCompile(`^/(users|orgs)/([^/]+)/installation$`)
	accTokenRe       = regexp.MustCompile(`^/app/installations/(\d+)/access_tokens$`)
	accOrgReposRe    = regexp.MustCompile(`^/orgs/([^/]+)/repos$`)
	accGenerateRe    = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/generate$`)
	accRepoRe        = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)$`)
	accBranchRe      = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/branches/([^/]+)$`)
	accAddToInstallR = regexp.MustCompile(`^/user/installations/(\d+)/repositories/(\d+)$`)
)

type accCaller struct {
	user   string // a user token's login
	instID int64  // an installation token's installation
	app    bool   // the App's JWT
}

func (a *Accounts) caller(headers map[string]string) accCaller {
	auth := headers["Authorization"]
	token := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(auth, "Bearer "), "token "))
	if token == "" {
		return accCaller{}
	}
	if login, ok := a.UserTokens[token]; ok {
		return accCaller{user: login}
	}
	if id, ok := a.instTokens[token]; ok {
		return accCaller{instID: id}
	}
	return accCaller{app: true}
}

func (a *Accounts) installByID(id int64) (string, *AppInstall) {
	for login, inst := range a.Installations {
		if inst.ID == id {
			return login, inst
		}
	}
	return "", nil
}

func (a *Accounts) kindOf(login string) (string, string, bool) {
	for k, v := range a.Kinds {
		if strings.EqualFold(k, login) {
			return k, v, true
		}
	}
	return "", "", false
}

// reaches reports whether c may see r.
func (a *Accounts) reaches(c accCaller, r *AccountRepo) bool {
	if !r.Private {
		return true
	}
	switch {
	case c.user != "":
		return strings.EqualFold(c.user, r.Owner)
	case c.instID != 0:
		return a.covers(c.instID, r)
	}
	return false
}

func repoJSON(r *AccountRepo) map[string]any {
	return map[string]any{
		"id": r.ID, "name": r.Name, "full_name": r.Owner + "/" + r.Name, "private": r.Private,
		"default_branch": r.DefaultBranch, "html_url": "https://github.com/" + r.Owner + "/" + r.Name,
		"description": r.Description,
	}
}

func notFound() github.ApiResponse {
	return jsonResponse(404, map[string]string{"message": "Not Found"})
}

func integrationRefused() github.ApiResponse {
	return jsonResponse(403, map[string]string{"message": "Resource not accessible by integration"})
}

// Request implements github.Transport.
func (a *Accounts) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Calls = append(a.Calls, Call{Method: method, Path: path})
	p, _ := splitPathQuery(path)
	c := a.caller(headers)

	switch {
	case method == "GET" && p == "/app":
		return jsonResponse(200, map[string]any{"html_url": a.AppHTMLURL}), nil
	case method == "GET" && p == "/user":
		if c.user == "" {
			return jsonResponse(401, map[string]string{"message": "Requires authentication"}), nil
		}
		return jsonResponse(200, map[string]any{"login": c.user}), nil
	case method == "GET" && p == "/installation/repositories":
		if c.instID == 0 {
			return jsonResponse(401, map[string]string{"message": "Bad credentials"}), nil
		}
		list := []map[string]any{}
		for _, r := range a.Repos {
			if a.covers(c.instID, r) {
				list = append(list, repoJSON(r))
			}
		}
		return jsonResponse(200, map[string]any{"total_count": len(list), "repositories": list}), nil
	}
	if m := accInstallRe.FindStringSubmatch(p); m != nil && method == "GET" {
		if !c.app {
			return jsonResponse(401, map[string]string{"message": "A JSON web token could not be decoded"}), nil
		}
		login, kind, ok := a.kindOf(m[2])
		if !ok || (m[1] == "orgs") != (kind == "Organization") {
			return notFound(), nil
		}
		inst, ok := a.Installations[login]
		if !ok {
			return notFound(), nil
		}
		return jsonResponse(200, map[string]any{"id": inst.ID, "account": map[string]string{"login": login},
			"repository_selection": inst.Selection, "permissions": inst.Permissions}), nil
	}
	if m := accUserRe.FindStringSubmatch(p); m != nil && method == "GET" {
		login, kind, ok := a.kindOf(m[1])
		if !ok {
			return notFound(), nil
		}
		return jsonResponse(200, map[string]any{"login": login, "type": kind}), nil
	}
	if m := accTokenRe.FindStringSubmatch(p); m != nil && method == "POST" {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		if !c.app {
			return jsonResponse(401, map[string]string{"message": "A JSON web token could not be decoded"}), nil
		}
		if _, inst := a.installByID(id); inst == nil {
			return notFound(), nil
		}
		if a.instTokens == nil {
			a.instTokens = map[string]int64{}
		}
		token := fmt.Sprintf("ghs_sim_%d_%d", id, len(a.instTokens)+1)
		a.instTokens[token] = id
		return jsonResponse(201, map[string]any{"token": token, "expires_at": "2099-01-01T00:00:00Z"}), nil
	}
	if m := accOrgReposRe.FindStringSubmatch(p); m != nil && method == "POST" {
		login, kind, ok := a.kindOf(m[1])
		if !ok || kind != "Organization" {
			return notFound(), nil
		}
		if c.instID == 0 {
			return jsonResponse(403, map[string]string{"message": "Must have admin rights to Repository."}), nil
		}
		instLogin, inst := a.installByID(c.instID)
		if !strings.EqualFold(instLogin, login) || inst.Permissions["administration"] != "write" {
			return integrationRefused(), nil
		}
		return a.createLocked(login, body, "", inst), nil
	}
	if method == "POST" && p == "/user/repos" {
		if c.instID != 0 || c.app {
			return integrationRefused(), nil
		}
		if c.user == "" {
			return jsonResponse(401, map[string]string{"message": "Requires authentication"}), nil
		}
		return a.createLocked(c.user, body, "", nil), nil
	}
	if m := accGenerateRe.FindStringSubmatch(p); m != nil && method == "POST" {
		tmpl := a.Repos[strings.ToLower(m[1]+"/"+m[2])]
		if tmpl == nil || !a.reaches(c, tmpl) {
			return notFound(), nil
		}
		var req struct {
			Owner string `json:"owner"`
		}
		_ = json.Unmarshal(body, &req)
		login, kind, ok := a.kindOf(req.Owner)
		if !ok {
			return jsonResponse(422, map[string]string{"message": "Validation Failed"}), nil
		}
		var inst *AppInstall
		switch {
		case c.user != "" && strings.EqualFold(c.user, login):
		case c.instID != 0 && kind == "Organization":
			instLogin, i := a.installByID(c.instID)
			if !strings.EqualFold(instLogin, login) || i.Permissions["administration"] != "write" {
				return integrationRefused(), nil
			}
			inst = i
		default:
			return integrationRefused(), nil
		}
		return a.createLocked(login, body, tmpl.Owner+"/"+tmpl.Name, inst), nil
	}
	if m := accAddToInstallR.FindStringSubmatch(p); m != nil && method == "PUT" {
		if c.user == "" {
			return jsonResponse(403, map[string]string{"message": "You must authenticate with a user access token"}), nil
		}
		instID, _ := strconv.ParseInt(m[1], 10, 64)
		repoID, _ := strconv.ParseInt(m[2], 10, 64)
		login, inst := a.installByID(instID)
		if inst == nil || !strings.EqualFold(login, c.user) {
			return notFound(), nil
		}
		inst.RepoIDs = append(inst.RepoIDs, repoID)
		return github.ApiResponse{Status: 204}, nil
	}
	if m := accBranchRe.FindStringSubmatch(p); m != nil && method == "GET" {
		r := a.Repos[strings.ToLower(m[1]+"/"+m[2])]
		if r == nil || !a.reaches(c, r) {
			return notFound(), nil
		}
		key := strings.ToLower(m[1] + "/" + m[2])
		if a.branchReads == nil {
			a.branchReads = map[string]int{}
		}
		a.branchReads[key]++
		if !r.HasBranch || m[3] != r.DefaultBranch || a.branchReads[key] <= a.BranchAfter {
			return jsonResponse(404, map[string]string{"message": "Branch not found"}), nil
		}
		return jsonResponse(200, map[string]any{"name": r.DefaultBranch}), nil
	}
	if m := accRepoRe.FindStringSubmatch(p); m != nil && method == "GET" {
		r := a.Repos[strings.ToLower(m[1]+"/"+m[2])]
		if r == nil || !a.reaches(c, r) {
			return notFound(), nil
		}
		return jsonResponse(200, repoJSON(r)), nil
	}
	panic(fmt.Sprintf("githubsim.Accounts: unexpected request %s %s", method, path))
}

// covers reports whether installation instID's selection holds r.
func (a *Accounts) covers(instID int64, r *AccountRepo) bool {
	login, inst := a.installByID(instID)
	if inst == nil || !strings.EqualFold(login, r.Owner) {
		return false
	}
	if inst.Selection == "all" {
		return true
	}
	for _, id := range inst.RepoIDs {
		if id == r.ID {
			return true
		}
	}
	return false
}

// createLocked makes owner's repository from a create body, the way
// GitHub does: 422 on a taken name, a README commit when auto_init (or a
// template) asks for one, and the new repository added to the creating
// installation's selection.
func (a *Accounts) createLocked(owner string, body []byte, template string, inst *AppInstall) github.ApiResponse {
	var req struct {
		Name        string `json:"name"`
		Private     bool   `json:"private"`
		Description string `json:"description"`
		AutoInit    bool   `json:"auto_init"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Name == "" {
		return jsonResponse(422, map[string]string{"message": "Validation Failed"})
	}
	if a.Repos[strings.ToLower(owner+"/"+req.Name)] != nil {
		return jsonResponse(422, map[string]any{"message": "Repository creation failed.",
			"errors": []map[string]string{{"resource": "Repository", "field": "name", "message": "name already exists on this account"}}})
	}
	r := a.addRepoLocked(owner, req.Name, req.Private, req.AutoInit || template != "")
	r.Description = req.Description
	r.Template = template
	if inst != nil && inst.Selection != "all" {
		inst.RepoIDs = append(inst.RepoIDs, r.ID)
	}
	return jsonResponse(201, repoJSON(r))
}
