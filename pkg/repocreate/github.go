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

package repocreate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/ghrepos"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
)

// UserTokenSecret is the secret a GitHub user token for login is kept
// under: a user OAuth token or fine-grained personal access token for
// that account, which is the only kind of credential GitHub lets create
// a repository under a personal account (POST /user/repos) -- an App
// installation token is refused there. Set it with
// `grain secret set github-user-token/<login>`, or under Settings →
// Secrets.
func UserTokenSecret(login string) string { return "github-user-token/" + strings.ToLower(login) }

// branchPolls and branchPollEvery bound how long a create waits for a
// new repository's default branch: GitHub writes an auto-init or
// template commit asynchronously, usually within a second or two.
const (
	branchPolls     = 15
	branchPollEvery = 2 * time.Second
)

// GitHub is the Creator for github.com, registered by this package.
//
// An org repository is created with the deployment's own GitHub App,
// through an installation token, which needs the installation to hold
// Administration: write. A personal account's repository cannot be made
// that way at all, so it is created with the account's own user token
// from grain's secret store (UserTokenSecret), and failing that the
// outcome says how to create it by hand.
type GitHub struct {
	// WebBase is the forge's web origin, for the links a fix names;
	// empty is https://github.com.
	WebBase string
}

func init() {
	Register("github.com", GitHub{})
}

func (g GitHub) web() string {
	if g.WebBase != "" {
		return strings.TrimRight(g.WebBase, "/")
	}
	return "https://github.com"
}

func ghCreateCommand(req Request) string {
	cmd := "gh repo create " + req.Repo()
	if req.Private {
		cmd += " --private"
	} else {
		cmd += " --public"
	}
	if req.Template != "" {
		cmd += " --template " + req.Template
	} else if req.Init {
		cmd += " --add-readme"
	}
	if req.Description != "" {
		cmd += " --description " + shellQuote(req.Description)
	}
	return cmd
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// connectExisting is the sentence every fix that ends with the operator
// creating the repository by hand closes on.
const connectExisting = ` Then use "Add repo" (connect existing) under Settings → Repos, or send this request again: an existing repository is connected rather than refused.`

// ghState is one GitHub create in progress.
type ghState struct {
	g       GitHub
	env     Env
	req     Request
	api     github.Transport
	out     *Outcome
	kind    string // "User" or "Organization"
	install *gitproxy.AppInstallation
	// installErr is why install is nil, when the deployment's credential
	// is an App.
	installErr error
	instToken  string
	userToken  string
	isApp      bool
}

func (s *ghState) step(st Step) { s.out.Steps = append(s.out.Steps, st) }

// Create implements Creator.
func (g GitHub) Create(ctx context.Context, env Env, req Request) (Outcome, error) {
	if err := Validate(req); err != nil {
		return Outcome{}, err
	}
	api := env.API
	if api == nil {
		api = github.NewRealTransport("github.com")
	}
	out := Outcome{Repo: req.Repo()}
	s := &ghState{g: g, env: env, req: req, api: api, out: &out}
	s.isApp = env.Credentials != nil && env.Credential != "" && env.Credentials.IsApp(env.Credential)
	s.userToken, _ = env.secret(ctx, UserTokenSecret(req.Owner))

	kind, found, err := ghrepos.AccountKind(api, s.userToken, req.Owner)
	if err != nil {
		s.step(Step{Name: "create", Status: StepFailed,
			Detail: fmt.Sprintf("Could not look up the account %s on GitHub: %s.", req.Owner, ghrepos.Message(err))})
		return out, nil
	}
	if !found {
		s.step(Step{Name: "create", Status: StepFailed,
			Detail: fmt.Sprintf("GitHub has no user or organization named %s.", req.Owner),
			Fix:    "Check the owner's spelling."})
		return out, nil
	}
	s.kind = kind
	if s.isApp {
		inst, err := env.Credentials.AppInstallationOn(env.Credential, req.Owner, kind)
		if err == nil {
			s.install = &inst
			s.instToken, err = env.Credentials.AppInstallationToken(env.Credential, inst.ID)
		}
		if err != nil {
			s.install, s.instToken, s.installErr = nil, "", err
		}
	}

	if !s.findExisting() && !s.create() {
		return out, nil
	}
	s.waitForDefaultBranch(ctx)
	s.ensureAccess()
	return out, nil
}

// tokens is every token that may see the repository, the one it is
// created with first: a user's new private repository is invisible to
// an installation limited to selected repositories until ensureAccess
// adds it.
func (s *ghState) tokens() []string {
	var out []string
	first, second := s.instToken, s.userToken
	if s.kind != "Organization" {
		first, second = second, first
	}
	for _, t := range []string{first, second} {
		if t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		out = append(out, "")
	}
	return out
}

// get reads the repository with the first token that sees it.
func (s *ghState) get(owner, name string) (ghrepos.Repo, string, bool) {
	for _, t := range s.tokens() {
		if repo, found, err := ghrepos.Get(s.api, t, owner, name); err == nil && found {
			return repo, t, true
		}
	}
	return ghrepos.Repo{}, "", false
}

// findExisting reports, and records, whether the repository is already
// there: an existing repository is connected, never an error.
func (s *ghState) findExisting() bool {
	repo, _, found := s.get(s.req.Owner, s.req.Name)
	if !found {
		return false
	}
	s.out.Existed = true
	s.out.Repo = repo.FullName
	s.out.DefaultBranch = repo.DefaultBranch
	s.step(Step{Name: "create", Status: StepSkipped,
		Detail: fmt.Sprintf("%s already exists, so nothing was created: it is connected as it is.", repo.FullName)})
	return true
}

// create makes the repository, and reports whether it now exists.
func (s *ghState) create() bool {
	req := s.req
	opts := ghrepos.CreateOptions{Name: req.Name, Description: req.Description, Private: req.Private, AutoInit: req.Init}
	var token, how string
	switch {
	case s.kind == "Organization":
		if !s.orgReady() {
			return false
		}
		token, how = s.instToken, "with the deployment's GitHub App"
	default:
		if s.userToken == "" {
			s.step(Step{Name: "create", Status: StepFailed,
				Detail: fmt.Sprintf("%s is a personal account, and GitHub lets only that account's own token create a repository there: "+
					"a GitHub App installation token cannot (POST /user/repos refuses it). This deployment holds no user token for %s.",
					req.Owner, req.Owner),
				Fix: fmt.Sprintf("Either store a user token for %s (a fine-grained personal access token with Administration: write on "+
					"its repositories, or a user OAuth token) in grain's secret store as %s and send this request again, "+
					"or create the repository yourself with the command below.", req.Owner, UserTokenSecret(req.Owner)) + connectExisting,
				Command: ghCreateCommand(req),
			})
			return false
		}
		token, how = s.userToken, "with the user token stored as "+UserTokenSecret(req.Owner)
	}

	var repo ghrepos.Repo
	var err error
	if req.Template != "" {
		tOwner, tName, _ := strings.Cut(req.Template, "/")
		repo, err = ghrepos.CreateFromTemplate(s.api, token, tOwner, tName, req.Owner, opts)
	} else if s.kind == "Organization" {
		repo, err = ghrepos.CreateInOrg(s.api, token, req.Owner, opts)
	} else {
		repo, err = ghrepos.CreateForUser(s.api, token, opts)
	}
	if ghrepos.IsAlreadyExists(err) {
		s.out.Existed = true
		s.step(Step{Name: "create", Status: StepSkipped,
			Detail: fmt.Sprintf("%s already exists, so nothing was created: it is connected as it is.", req.Repo())})
		return true
	}
	if err != nil {
		st := Step{Name: "create", Status: StepFailed,
			Detail: fmt.Sprintf("GitHub refused to create %s %s: %s.", req.Repo(), how, ghrepos.Message(err)),
			Fix:    "Create the repository yourself with the command below." + connectExisting, Command: ghCreateCommand(req)}
		if s.kind == "Organization" && ghrepos.Status(err) == 403 {
			st.Fix = fmt.Sprintf("Check that the GitHub App's installation on %s holds Administration: write, and that the org "+
				"lets members create repositories of this visibility.", req.Owner) + " Or create it yourself with the command below." + connectExisting
			st.Link = s.installationLink()
		}
		s.step(st)
		return false
	}
	s.out.Created = true
	s.out.Repo = repo.FullName
	s.out.DefaultBranch = repo.DefaultBranch
	vis := "public"
	if req.Private {
		vis = "private"
	}
	detail := fmt.Sprintf("Created %s %s repository %s %s.", article(vis), vis, repo.FullName, how)
	if req.Template != "" {
		detail = fmt.Sprintf("Created %s repository %s from the template %s %s.", vis, repo.FullName, req.Template, how)
	}
	s.step(Step{Name: "create", Status: StepDone, Detail: detail})
	return true
}

func article(word string) string {
	if strings.ContainsRune("aeiou", rune(word[0])) {
		return "an"
	}
	return "a"
}

// orgReady reports whether the App may create in the org, recording the
// failed step that says why not when it may not.
func (s *ghState) orgReady() bool {
	req := s.req
	switch {
	case !s.isApp:
		s.step(Step{Name: "create", Status: StepFailed,
			Detail: fmt.Sprintf("Creating a repository in the org %s needs the deployment's GitHub App, and this deployment's git "+
				"connection for it (%s) is not one.", req.Owner, credentialName(s.env.Credential)),
			Fix:     "Connect a GitHub App under Settings → Git, or create the repository yourself with the command below." + connectExisting,
			Command: ghCreateCommand(req)})
		return false
	case s.install == nil:
		detail := fmt.Sprintf("The deployment's GitHub App is not installed on the org %s.", req.Owner)
		if s.installErr != nil && !errors.Is(s.installErr, gitproxy.ErrAppNotInstalled) {
			detail = fmt.Sprintf("Could not read the GitHub App's installation on %s: %s.", req.Owner, ghrepos.Message(s.installErr))
		}
		s.step(Step{Name: "create", Status: StepFailed, Detail: detail,
			Fix: fmt.Sprintf("Install the App on %s with Administration: write (and Contents: write), then send this request again; "+
				"or create the repository yourself with the command below.", req.Owner) + connectExisting,
			Command: ghCreateCommand(req),
			Link:    s.installNewLink()})
		return false
	case s.install.Permissions["administration"] != "write":
		have := s.install.Permissions["administration"]
		if have == "" {
			have = "none"
		}
		s.step(Step{Name: "create", Status: StepFailed,
			Detail: fmt.Sprintf("The deployment's GitHub App is installed on %s but its installation lacks Administration: write "+
				"(it has %s), which creating a repository needs.", req.Owner, have),
			Fix: "Grant the App Administration: write in its permissions, then have an owner of " + req.Owner +
				" accept the new permission on the installation; or create the repository yourself with the command below." + connectExisting,
			Command: ghCreateCommand(req),
			Link:    s.installationLink()})
		return false
	}
	return true
}

func credentialName(name string) string {
	if name == "" {
		return "none"
	}
	return fmt.Sprintf("%q", name)
}

// installationLink is the installation's settings page on its account.
func (s *ghState) installationLink() string {
	if s.install == nil {
		return s.installNewLink()
	}
	if s.kind == "Organization" {
		return fmt.Sprintf("%s/organizations/%s/settings/installations/%d", s.g.web(), s.req.Owner, s.install.ID)
	}
	return fmt.Sprintf("%s/settings/installations/%d", s.g.web(), s.install.ID)
}

// installNewLink is where the App is installed on another account.
func (s *ghState) installNewLink() string {
	if s.isApp {
		if page, err := s.env.Credentials.AppPage(s.env.Credential); err == nil && page != "" {
			return strings.TrimRight(page, "/") + "/installations/new"
		}
	}
	return s.g.web() + "/settings/installations"
}

// waitForDefaultBranch waits until the default branch exists: what
// grain clones and opens pull requests against.
func (s *ghState) waitForDefaultBranch(ctx context.Context) {
	req := s.req
	if s.out.Created && !req.Init && req.Template == "" {
		s.step(Step{Name: "default-branch", Status: StepSkipped,
			Detail: fmt.Sprintf("%s was created empty, so it has no default branch yet.", s.out.Repo),
			Fix:    "Push a first commit to it before filing work against it."})
		return
	}
	owner, name, _ := strings.Cut(s.out.Repo, "/")
	for i := 0; i < branchPolls; i++ {
		if i > 0 {
			if err := s.env.sleep(ctx, branchPollEvery); err != nil {
				break
			}
		}
		repo, token, found := s.get(owner, name)
		if !found || repo.DefaultBranch == "" {
			continue
		}
		s.out.DefaultBranch = repo.DefaultBranch
		ok, err := ghrepos.BranchExists(s.api, token, owner, name, repo.DefaultBranch)
		if err == nil && ok {
			s.step(Step{Name: "default-branch", Status: StepDone,
				Detail: fmt.Sprintf("The default branch %s exists.", repo.DefaultBranch)})
			return
		}
	}
	if s.out.Existed && !s.out.Created {
		s.step(Step{Name: "default-branch", Status: StepSkipped,
			Detail: fmt.Sprintf("%s has no commits on a default branch yet.", s.out.Repo),
			Fix:    "Push a first commit to it before filing work against it."})
		return
	}
	s.step(Step{Name: "default-branch", Status: StepFailed,
		Detail: fmt.Sprintf("%s was created, but its default branch did not appear within %s.", s.out.Repo, branchPolls*branchPollEvery),
		Fix:    "Check the repository on GitHub; once it has a default branch, grain can work in it."})
}

// ensureAccess makes sure the deployment's credential reaches the
// repository: an App installation limited to selected repositories has
// it added when a user token for the account allows it.
func (s *ghState) ensureAccess() {
	req := s.req
	owner, name, _ := strings.Cut(s.out.Repo, "/")
	if !s.isApp {
		s.step(Step{Name: "access", Status: StepSkipped,
			Detail: fmt.Sprintf("This deployment's git connection for %s (%s) is a token, not a GitHub App, so grain cannot check "+
				"or widen what it reaches.", req.Owner, credentialName(s.env.Credential)),
			Fix: "If it is a fine-grained personal access token limited to selected repositories, add " + s.out.Repo + " to it."})
		return
	}
	if s.install == nil {
		s.step(Step{Name: "access", Status: StepFailed,
			Detail: fmt.Sprintf("The deployment's GitHub App is not installed on %s, so grain cannot reach %s.", req.Owner, s.out.Repo),
			Fix:    fmt.Sprintf("Install the App on %s, for all repositories or with %s selected.", req.Owner, s.out.Repo),
			Link:   s.installNewLink()})
		return
	}
	if s.install.RepositorySelection == "all" {
		s.step(Step{Name: "access", Status: StepDone,
			Detail: fmt.Sprintf("The GitHub App's installation on %s covers all its repositories, so it reaches %s.", req.Owner, s.out.Repo)})
		return
	}
	if ok, err := ghrepos.InstallationReaches(s.api, s.instToken, owner, name); err == nil && ok {
		s.step(Step{Name: "access", Status: StepDone,
			Detail: fmt.Sprintf("The GitHub App's installation on %s reaches %s.", req.Owner, s.out.Repo)})
		return
	}
	if s.userToken != "" {
		repo, found, err := ghrepos.Get(s.api, s.userToken, owner, name)
		if err == nil && found {
			err = ghrepos.AddToInstallation(s.api, s.userToken, s.install.ID, repo.ID)
		} else if err == nil {
			err = fmt.Errorf("%s is not visible to the user token", s.out.Repo)
		}
		if err == nil {
			s.step(Step{Name: "access", Status: StepDone,
				Detail: fmt.Sprintf("Added %s to the GitHub App's installation on %s, which is limited to selected repositories.",
					s.out.Repo, req.Owner)})
			return
		}
		s.step(Step{Name: "access", Status: StepFailed,
			Detail: fmt.Sprintf("The GitHub App's installation on %s is limited to selected repositories, and adding %s to it "+
				"with the token stored as %s failed: %s.", req.Owner, s.out.Repo, UserTokenSecret(req.Owner), ghrepos.Message(err)),
			Fix:  fmt.Sprintf("Add %s to the installation's selected repositories on its settings page.", s.out.Repo),
			Link: s.installationLink()})
		return
	}
	s.step(Step{Name: "access", Status: StepFailed,
		Detail: fmt.Sprintf("The GitHub App's installation on %s is limited to selected repositories and does not include %s; "+
			"an installation token cannot add a repository to its own selection, and this deployment holds no user token "+
			"(%s) that could.", req.Owner, s.out.Repo, UserTokenSecret(req.Owner)),
		Fix:  fmt.Sprintf("Add %s to the installation's selected repositories on its settings page.", s.out.Repo),
		Link: s.installationLink()})
}
