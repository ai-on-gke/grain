package ui

// The typed credential editor behind Team settings, Git identity: a team's
// credential, and each per-repository override, is one of three auth
// types -- a PAT, an SSH key or a GitHub App -- or none at all (public
// repositories, read anonymously). Each is saved write-only through the
// same CredentialSet the git proxy reads (gitproxy.SetPAT, SetSSHKey,
// SetApp); what comes back is the type and what may be said about it
// (host, key fingerprint, whether an API credential is paired), never
// the material.
//
// The credential written is a name grain mints for the connection
// (model.GitConnectionCredentialPrefix), and "none" is the literal
// anonymous credential, which reaches nothing a public read does not.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/githubtoken"
	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
)

// GitCredentialInput is the typed editor's save: PUT
// /api/teams/{id}/github-credential and
// /api/teams/{id}/repos/{owner}/{name}/github-credential. A body with no
// type and a value is a PAT, as these endpoints always took.
type GitCredentialInput struct {
	// Type is "pat", "ssh", "app", "ambient" or "none".
	Type string `json:"type,omitempty"`
	// Source is, for an ambient credential, which of this host's tools
	// is asked for a token: "gh", "git" or "auto" (gitproxy.AmbientFile).
	Source string `json:"source,omitempty"`
	// Value is the PAT.
	Value string `json:"value,omitempty"`
	// Host and Kind are the credential's git host and its kind ("github"
	// or "git"). Empty Host is github.com, empty Kind is github.
	Host string `json:"host,omitempty"`
	Kind string `json:"kind,omitempty"`
	// BaseURL is the host's scheme://host[:port] when that is not https
	// on the default port (gitproxy.CredentialHost.BaseURL).
	BaseURL string `json:"baseUrl,omitempty"`
	// Proxy is the http(s)://host:port proxy the host is reached through
	// (gitproxy.CredentialHost.Proxy).
	Proxy string              `json:"proxy,omitempty"`
	SSH   *SSHCredentialInput `json:"ssh,omitempty"`
	App   *AppCredentialInput `json:"app,omitempty"`
	// APIToken is, for an SSH key on a GitHub-kind host, the PAT its pull
	// requests, checks and merges are made with. Without one the
	// repositories it reaches are git-only.
	APIToken string `json:"apiToken,omitempty"`
	User     string `json:"user,omitempty"`
}

// SSHCredentialInput is an SSH key as the editor sends it.
type SSHCredentialInput struct {
	PrivateKey string `json:"privateKey"`
	Passphrase string `json:"passphrase,omitempty"`
	KnownHosts string `json:"knownHosts,omitempty"`
	Port       int    `json:"port,omitempty"`
	User       string `json:"user,omitempty"`
	PathPrefix string `json:"pathPrefix,omitempty"`
}

// AppCredentialInput is a GitHub App as the editor sends it. InstallationID
// is optional: omitting it resolves each repository's installation via the
// App's JWT at mint time.
type AppCredentialInput struct {
	AppID          string `json:"appId"`
	InstallationID string `json:"installationId,omitempty"`
	PrivateKey     string `json:"privateKey"`
}

// GitCredentialStatus is what GET .../github-credential says about a saved
// credential: its type and the parts of it that are not secret.
type GitCredentialStatus struct {
	Configured bool   `json:"configured"`
	Type       string `json:"type,omitempty"`
	Host       string `json:"host,omitempty"`
	Kind       string `json:"kind,omitempty"`
	// Proxy is the host record's proxy: optional everywhere, a command-backed
	// forge's host included (its traffic goes through the forge's tool).
	Proxy string `json:"proxy,omitempty"`
	// Fingerprint and SSHUser describe an SSH key.
	Fingerprint string `json:"fingerprint,omitempty"`
	SSHUser     string `json:"sshUser,omitempty"`
	// APIPaired is whether an SSH key has an API credential paired with
	// it; GitOnly says what an unpaired one on a GitHub host cannot do.
	APIPaired bool   `json:"apiPaired,omitempty"`
	GitOnly   string `json:"gitOnly,omitempty"`
	// AmbientSource and AmbientLogin describe an ambient credential:
	// which of this host's tools it asks, and the GitHub account that
	// login currently is -- the account every push and API call made
	// with it acts as. AmbientWarning says so in a sentence.
	AmbientSource  string `json:"ambientSource,omitempty"`
	AmbientLogin   string `json:"ambientLogin,omitempty"`
	AmbientWarning string `json:"ambientWarning,omitempty"`
	// AmbientPending is set while the host's tools have not yet said who
	// the login is (ambientStatusWait): the editor shows "checking
	// login…" and asks again.
	AmbientPending bool `json:"ambientPending,omitempty"`
	// CommitName and CommitEmail are the commit identity a run pushing
	// with this credential commits under, derived from the account the
	// credential acts as (gitproxy.CredentialSet.CredentialGitIdentity):
	// a PAT's GitHub login and its noreply address, a GitHub App's bot
	// account, an SSH key's paired API credential's, or an ambient
	// connection's host login. The pane shows them as who the team's
	// commits are authored by -- there is no field to type one in.
	// Empty while the lookup is pending (CommitPending) or when the
	// credential names nobody GitHub can say (CommitError says why).
	CommitName    string `json:"commitName,omitempty"`
	CommitEmail   string `json:"commitEmail,omitempty"`
	CommitPending bool   `json:"commitPending,omitempty"`
	CommitError   string `json:"commitError,omitempty"`
	Error         string `json:"error,omitempty"`
	// DefaultKnownHosts is GitHub's published host keys, the editor's
	// default known_hosts on github.com.
	DefaultKnownHosts string `json:"defaultKnownHosts"`
}

func (in GitCredentialInput) authType() string {
	t := strings.ToLower(strings.TrimSpace(in.Type))
	if t == "" {
		switch {
		case in.SSH != nil:
			return gitproxy.AuthTypeSSH
		case in.App != nil:
			return gitproxy.AuthTypeApp
		}
		return gitproxy.AuthTypePAT
	}
	return t
}

// apiCredentialName is the PAT paired with SSH credential name. A prefix
// rather than a suffix, because a name minted while connections belonged
// to teams ends in base64, whose alphabet includes "-": no suffix is
// safe from colliding with another name (model.GitCredentialStem).
func apiCredentialName(name string) string {
	return "grain-api-" + model.GitCredentialStem(name)
}

// saveTypedCredential writes in as credential name and returns the
// selector the team or repo then names: name, or "anonymous" for none.
func (c *Client) saveTypedCredential(name string, in GitCredentialInput) (string, error) {
	creds := c.Config.Credentials
	host := strings.TrimSpace(in.Host)
	if host == "" {
		host = model.DefaultGitHost
	}
	kind, err := model.ParseGitHostKind(in.Kind)
	if err != nil {
		return "", validationErrorf("%s", err)
	}
	setHost := func(n string) error {
		if strings.TrimSpace(in.Host) == "" && kind == model.GitHostGitHub && strings.TrimSpace(in.Proxy) == "" {
			// No host given: the deployment's default host, which
			// is what a credential with no record already means.
			return nil
		}
		return creds.SetHost(n, gitproxy.CredentialHost{Host: host, Kind: kind, Proxy: in.Proxy})
	}
	removePair := func() {
		if creds.AuthType(apiCredentialName(name)) != "" {
			_ = creds.Remove(apiCredentialName(name))
		}
	}
	switch in.authType() {
	case gitproxy.AuthTypeAnonymous:
		if creds.AuthType(name) != "" {
			_ = creds.Remove(name)
		}
		removePair()
		return "anonymous", nil
	case gitproxy.AuthTypePAT:
		if err := creds.SetPAT(name, in.Value); err != nil {
			return "", validationErrorf("%s", err)
		}
		removePair()
		if err := setHost(name); err != nil {
			return "", validationErrorf("%s", err)
		}
		return name, nil
	case gitproxy.AuthTypeApp:
		if in.App == nil {
			return "", validationErrorf("a GitHub App needs its app ID and private key")
		}
		if kind == model.GitHostGit {
			return "", validationErrorf("a GitHub App needs a GitHub host: a plain git host has no App API")
		}
		if err := creds.SetApp(name, in.App.AppID, in.App.InstallationID, in.App.PrivateKey); err != nil {
			return "", validationErrorf("%s", err)
		}
		removePair()
		if err := setHost(name); err != nil {
			return "", validationErrorf("%s", err)
		}
		return name, nil
	case gitproxy.AuthTypeAmbient:
		// On a plain git host the login is git's own credential helper's
		// (SetAmbientOn refuses gh there).
		// A command-backed forge's host needs no proxy: its tool carries
		// its traffic itself (gitproxy.CommandForgeTransport). One the
		// operator typed is kept.
		proxy := strings.TrimSpace(in.Proxy)
		if err := creds.SetAmbientOn(name, gitproxy.CredentialHost{Host: host, Kind: kind, BaseURL: in.BaseURL, Proxy: proxy}, in.Source); err != nil {
			return "", validationErrorf("%s", err)
		}
		removePair()
		// Start asking who the login is now, without waiting for it:
		// the editor's status read after the Save then finds it done,
		// or reports it pending.
		creds.AmbientIdentityWithin(name, 0)
		return name, nil
	case gitproxy.AuthTypeSSH:
		if in.SSH == nil {
			return "", validationErrorf("an SSH key credential needs a private key")
		}
		f := gitproxy.SSHKeyFile{
			Host: host, Port: in.SSH.Port, User: in.SSH.User, PrivateKey: in.SSH.PrivateKey,
			Passphrase: in.SSH.Passphrase, KnownHosts: in.SSH.KnownHosts, PathPrefix: in.SSH.PathPrefix,
		}
		// Checked before the paired token is written, so a key that will
		// not parse leaves nothing behind.
		if _, err := gitproxy.ParseSSHKey(name, f); err != nil {
			return "", validationErrorf("%s", err)
		}
		api := apiCredentialName(name)
		if token := strings.TrimSpace(in.APIToken); token != "" {
			if kind == model.GitHostGit {
				return "", validationErrorf("a plain git host has no forge API, so there is nothing to pair an API token with")
			}
			if err := creds.SetPAT(api, token); err != nil {
				return "", validationErrorf("%s", err)
			}
			if err := creds.SetHost(api, gitproxy.CredentialHost{Host: host, Kind: model.GitHostGitHub}); err != nil {
				return "", validationErrorf("%s", err)
			}
			f.APICredential = api
		} else if creds.AuthType(api) != "" {
			// Replacing the key keeps the API token already paired,
			// while it is still for the same host; otherwise it goes.
			if h := creds.HostOf(api); kind != model.GitHostGit && model.SameGitHost(h.Host, host, model.DefaultGitHost) {
				f.APICredential = api
			} else {
				removePair()
			}
		}
		if err := creds.SetSSHKey(name, f, kind); err != nil {
			return "", validationErrorf("%s", err)
		}
		return name, nil
	}
	return "", validationErrorf("unknown credential type %q: pat, ssh, app, ambient or none", in.Type)
}

// requireAmbientAvailable refuses creating or testing an ambient
// credential on a host that has no ambient git login to act as
// (gitproxy.AmbientAvailable).
func (c *Client) requireAmbientAvailable() error {
	if err := gitproxy.AmbientAvailable(); err != nil {
		return validationErrorf("%s", err)
	}
	return nil
}

// ambientWarning is the sentence the editor shows beside an ambient
// credential.
func ambientWarning(login string) string {
	who := "the account this host's gh (or git credential helper) is logged in as"
	if login != "" {
		who = login
	}
	return "pushes and API calls act as " + who + ", with all of its access"
}

// ambientStatusWait is how long a status read waits for an ambient
// credential's login before answering AmbientPending: asking means
// running gh or git credential, which a slow credential helper can make
// take up to gitproxy's own bound on it, and neither a Save nor a
// settings pane should sit on that with nothing to show.
const ambientStatusWait = 2 * time.Second

// gitCredentialStatus is GitCredentialStatus for selector name.
func (c *Client) gitCredentialStatus(name string) GitCredentialStatus {
	out := GitCredentialStatus{DefaultKnownHosts: gitproxy.GitHubKnownHosts()}
	creds := c.Config.Credentials
	if name == "" || creds == nil {
		return out
	}
	out.Type = creds.AuthType(name)
	out.Configured = out.Type != ""
	if !out.Configured {
		out.Error = fmt.Sprintf("credential %q is not configured on this deployment", name)
		return out
	}
	h := creds.HostOf(name)
	out.Host, out.Kind, out.Proxy = h.Host, string(h.Kind), h.Proxy
	if f, ok := creds.AmbientOf(name); ok {
		out.Host, out.AmbientSource = f.Host, f.Source
		id, pending, err := creds.AmbientIdentityWithin(name, ambientStatusWait)
		switch {
		case pending:
			out.AmbientPending = true
		case err != nil:
			out.Error = err.Error()
		case id.NoRepoScope:
			out.Error = fmt.Sprintf("the login %s has no `repo` scope, so it cannot push or reach private "+
				"repositories: run `%s` on the grain host", id.Login, gitproxy.AmbientScopeFix(f.Host, f.Source))
		}
		out.AmbientLogin = id.Login
		out.AmbientWarning = ambientWarning(id.Login)
	}
	if info, ok := creds.SSHKeyInfoOf(name); ok {
		out.Error = info.Error
		out.Fingerprint, out.SSHUser = info.Fingerprint, info.User
		if info.Host != "" {
			out.Host = info.Host
		}
		if cred, ok := creds.Get(name); ok && cred.SSH != nil {
			if _, paired := creds.APICredential(cred); paired {
				out.APIPaired = true
			} else if h.Kind != model.GitHostGit {
				out.GitOnly = "no API credential is paired with this SSH key, so grain can clone, fetch and push " +
					"with it but cannot open, check or merge pull requests"
			}
		}
	}
	// Who this credential's commits are authored by: derived from the
	// account it acts as, never typed in. An ambient login still being
	// looked up is pending for this too, rather than waited on twice.
	switch {
	case out.Type == gitproxy.AuthTypeAnonymous:
	case out.AmbientPending:
		out.CommitPending = true
	default:
		commit, pending, err := creds.CredentialGitIdentityWithin(name, ambientStatusWait)
		switch {
		case pending:
			out.CommitPending = true
		case err != nil:
			out.CommitError = err.Error()
		default:
			out.CommitName, out.CommitEmail = commit.Name, commit.Email
		}
		if !out.CommitPending {
			if cfg, err := c.deploymentSettings(context.Background()); err == nil {
				if v := strings.TrimSpace(cfg.AgentGitName); v != "" {
					out.CommitName = v
				}
				if v := strings.TrimSpace(cfg.AgentGitEmail); v != "" {
					out.CommitEmail = v
				}
				if out.CommitName != "" && out.CommitEmail != "" {
					out.CommitError = ""
				}
			}
		}
	}
	return out
}

// checkSSHCredential is Test for an SSH key: ls-remote over SSH for each
// repo -- upload-pack for read, receive-pack's advertisement for push,
// which a host refuses a read-only key -- and, when an API credential is
// paired, the same API probe a token gets.
func (c *Client) checkSSHCredential(ctx context.Context, cred gitproxy.Credential, repos []string, saved bool) TeamGitHubCredentialCheck {
	out := TeamGitHubCredentialCheck{Saved: saved, Host: cred.SSH.Host, CheckedAt: c.now()}
	out.OK = true
	for _, entry := range repos {
		repo, err := model.ParseRepo(entry)
		if err != nil || strings.ContainsAny(repo.Owner+repo.Name, "*?[") {
			continue
		}
		if len(out.Repos) == githubtoken.MaxProbedRepos {
			out.Skipped++
			continue
		}
		verdict := githubtoken.RepoProbe{Repo: repo.String()}
		lsRemote := func(service string) (bool, string) {
			resp, refusal, err := gitproxy.SSHForward(ctx, cred, repo.Owner, repo.Name, "info/refs",
				"service="+service, map[string]string{}, nil, gitproxy.DefaultMaxFetchBodyBytes)
			switch {
			case err != nil:
				return false, err.Error()
			case refusal != "":
				return false, refusal
			}
			if resp.Stream != nil {
				resp.Stream.Close()
			}
			return resp.Status == http.StatusOK, ""
		}
		verdict.Read, verdict.Error = lsRemote("git-upload-pack")
		if verdict.Read {
			verdict.Push, _ = lsRemote("git-receive-pack")
		} else {
			out.OK = false
		}
		out.Repos = append(out.Repos, verdict)
	}
	if !out.OK {
		out.Error = "the SSH key could not read every repository"
		for _, r := range out.Repos {
			if r.Error != "" {
				out.Error = r.Error
				break
			}
		}
	}
	if out.OK && cred.SSH.APICredential != "" {
		if api, ok := c.Config.Credentials.APICredential(cred); ok && api.Token != nil {
			host, insecure := c.gitHubHost(ctx)
			report := githubtoken.Probe(githubtoken.ProbeTransport(host, insecure), *api.Token,
				c.Config.Credentials.IsApp(api.Name), nil)
			out.Login, out.App, out.Scopes = report.Login, report.App, report.Scopes
			if !report.OK {
				out.OK = false
				out.Error = "the SSH key works, but its paired API token was refused: " + report.Error
			}
		}
	}
	return out
}
