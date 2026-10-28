package mcp

// ConfigureGitCredentials points a sandbox's git at its proxy token via a
// credential helper, so neither the clone URL nor any command grain
// writes ever needs to carry it. That is all the helper buys: the token
// sits in plaintext in root/.git-credentials (mode 0600, but owned by the
// same user run_command runs as, with HOME=root), so an agent's own shell
// can still read it -- `cat ~/.git-credentials` works. What bounds a
// token that leaks that way is on the proxy's side, not here: it
// authorizes only what that sandbox's live run may touch, it is revoked
// when the run's sandbox is released, and it expires on its own after
// gitproxy.DefaultSandboxTokenTTL even if that revocation is missed (see
// pkg/gitproxy/tokens.go and docs/design.md, "sandbox identity"). Same idea as
// grain/automation/dispatch.py's function of the same name, adapted to
// v2's stand-in for a sandbox: a plain directory (NewSandboxTools' root)
// rather than a whole VM, so "the sandbox's global git config" becomes
// root/.gitconfig with $HOME pinned to root (runCommandTool does that),
// instead of one real user's actual home directory.
//
// Also sets a git identity (user.name/user.email): a fresh sandbox has
// none configured anywhere, which makes `git commit` fail outright. Which
// identity is a deployment's own choice -- GitIdentity, from
// model.Config.AgentGitName/AgentGitEmail by way of
// orchestrator.Config.GitIdentity -- so the commits and the pull requests
// an agent pushes are attributed to whatever name that deployment's
// operator wants to see on them, rather than to a name baked into this
// file. DefaultGitIdentity is what a deployment that has never chosen one
// still gets, which is the name grain used before it was configurable --
// though it is only the last resort: the orchestrator first fills the
// identity from the account the run's git credential acts as (a PAT's
// own GitHub login, a GitHub App's bot account, an ambient connection's
// host login: orchestrator.runGitIdentity,
// gitproxy.CredentialSet.CredentialGitIdentity), since the default's
// address is one no GitHub account owns.

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DefaultGitIdentityName and DefaultGitIdentityEmail are the git identity
// a deployment that has configured none commits under. Named here, in the
// one package that actually writes a sandbox's .gitconfig, rather than
// repeated at each of the places that have to agree on what "unset" means
// -- model.Config.AgentGitName's empty value, the grain_config columns'
// own DEFAULT ”, and the effective defaults ui.Settings reports so a
// settings pane can show the real current identity instead of a blank box
// (ui.Settings.AgentGitNameDefault, the same shape SandboxCPUsDefault
// already has for kontur's VM shape).
const (
	DefaultGitIdentityName  = "grain agent"
	DefaultGitIdentityEmail = "grain-agent@localhost"
)

// GitIdentity is the name and email an agent's commits are authored and
// committed with -- what `git log` shows against every commit a run
// pushes, and what GitHub matches a commit to an account by.
//
// Both halves are optional at this boundary: an empty one is a deployment
// that has not chosen, and OrDefault resolves it. That matters here more
// than at the callers, because a sandbox with no identity at all is one
// where `git commit` fails outright -- so the fallback belongs where the
// file is written, not at each caller that happens to remember it.
type GitIdentity struct {
	Name  string
	Email string
}

// OrDefault fills each half this identity leaves empty from grain's own
// default, per half rather than all-or-nothing: a deployment that renames
// the committer without giving it an address of its own still gets a
// working one.
//
// It also folds away line breaks, because these two values are written
// into a .gitconfig as bare `name = <value>` lines and a newline in one
// would end the line early -- turning the rest of the name into a
// key/value pair of its own, or into a section header. ui.UpdateSettings
// refuses one on the way in, so this only catches a value that reached
// grain_config some other way (a hand-edited state-repo dump is the real
// one), and it catches it as a mangled name rather than as a sandbox
// whose git config no longer parses.
func (i GitIdentity) OrDefault() GitIdentity {
	i.Name, i.Email = oneLine(i.Name), oneLine(i.Email)
	if i.Name == "" {
		i.Name = DefaultGitIdentityName
	}
	if i.Email == "" {
		i.Email = DefaultGitIdentityEmail
	}
	return i
}

// oneLine collapses every line break in s to a space and trims the
// result, so a value that arrived with one is still recognisably itself.
func oneLine(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s))
}

// ConfigureGitCredentials writes root/.gitconfig and root/.git-credentials
// so that any git command run_command later executes with HOME=root
// (see runCommandTool) authenticates through remoteURL's host as sandbox,
// using token as the password half of HTTP Basic auth, and commits as
// identity (or as DefaultGitIdentity, for an identity left empty).
func ConfigureGitCredentials(root, remoteURL, token string, identity GitIdentity) error {
	line, gitconfig, err := gitCredentialFiles(remoteURL, token, identity)
	if err != nil {
		return err
	}
	netrc, curlrc, err := CurlCredentialFiles(remoteURL, token, root)
	if err != nil {
		return err
	}

	// git-credential-store matches on protocol+host, not path, so one
	// line covers every repo this sandbox might be pointed at through the
	// same proxy.
	credentialsPath := filepath.Join(root, ".git-credentials")
	if err := os.WriteFile(credentialsPath, []byte(line), 0o600); err != nil {
		return fmt.Errorf("gitcredentials: writing %s: %w", credentialsPath, err)
	}

	gitconfigPath := filepath.Join(root, ".gitconfig")
	if err := os.WriteFile(gitconfigPath, []byte(gitconfig), 0o600); err != nil {
		return fmt.Errorf("gitcredentials: writing %s: %w", gitconfigPath, err)
	}

	netrcPath := filepath.Join(root, ".git-credentials.netrc")
	if err := os.WriteFile(netrcPath, []byte(netrc), 0o600); err != nil {
		return fmt.Errorf("gitcredentials: writing %s: %w", netrcPath, err)
	}

	curlrcPath := filepath.Join(root, ".curlrc")
	if err := os.WriteFile(curlrcPath, []byte(curlrc), 0o600); err != nil {
		return fmt.Errorf("gitcredentials: writing %s: %w", curlrcPath, err)
	}
	return nil
}

// CurlCredentialFiles renders the .git-credentials.netrc and .curlrc files
// that let curl authenticate to the sandbox HTTP proxy (127.0.0.1:9728 or
// remoteURL's host) automatically without passing credentials on the command line.
func CurlCredentialFiles(remoteURL, token, homeDir string) (netrc, curlrc string, err error) {
	parsed, err := url.Parse(remoteURL)
	if err != nil {
		return "", "", fmt.Errorf("gitcredentials: parsing %q: %w", remoteURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", "", fmt.Errorf("gitcredentials: %q is not an absolute URL", remoteURL)
	}
	host := parsed.Hostname()
	if host == "" {
		host = parsed.Host
	}
	var b strings.Builder
	fmt.Fprintf(&b, "machine %s login sandbox password %s\n", host, token)
	if host != "127.0.0.1" {
		fmt.Fprintf(&b, "machine 127.0.0.1 login sandbox password %s\n", token)
	}
	netrcPath := ".git-credentials.netrc"
	if homeDir != "" {
		netrcPath = filepath.Join(homeDir, ".git-credentials.netrc")
	}
	curlrc = fmt.Sprintf("netrc-optional\nnetrc-file = %q\n", netrcPath)
	return b.String(), curlrc, nil
}

// GitCredentialFiles renders the two files ConfigureGitCredentials (and
// ConfigureGitCredentialsOverSSH, ssh_git_credentials.go) write, without
// committing to how or where they land -- a local directory's
// os.WriteFile for the former, a remote host's $HOME over SSH for the
// latter.
//
// The .gitconfig opens credential.helper with an empty value before
// naming `store`, because git *appends* credential helpers rather than
// replacing them: every helper configured at any level runs, in order,
// for every credential. Setting the key to the empty string is git's
// documented way to discard the list built up so far (git-config(1),
// credential.helper), so grain's `store` ends up the only helper a
// sandbox's git consults. Without that line a helper the host already
// configured runs too, and it runs on the way *out* as well as in -- git
// calls `<helper> store` with the credential it just used, so a sandbox's
// proxy token is handed to the host operator's own credential store. The
// concrete case this was written for: Apple's Command Line Tools ship
// /Library/Developer/CommandLineTools/usr/share/git-core/gitconfig with
// `helper = osxkeychain` at *system* level, so every authenticated push
// from a sandbox wrote that sandbox's gitproxy token into the operator's
// login keychain (and raised a GUI dialog doing it). Linux hosts are
// quieter about it, not exempt from it. Compare the other half of the
// same fence, orchestrator's hostShellEnv, which sets
// GIT_CONFIG_NOSYSTEM=1 so that system gitconfig is not read at all --
// this line is what protects the transports that have no environment of
// grain's to rely on (a run_command inside a VM, say).
func GitCredentialFiles(remoteURL, token string, identity GitIdentity) (credentials, gitconfig string, err error) {
	parsed, err := url.Parse(remoteURL)
	if err != nil {
		return "", "", fmt.Errorf("gitcredentials: parsing %q: %w", remoteURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", "", fmt.Errorf("gitcredentials: %q is not an absolute URL", remoteURL)
	}

	credentials = fmt.Sprintf("%s://sandbox:%s@%s\n", parsed.Scheme, token, parsed.Host)
	id := identity.OrDefault()
	// The empty `helper =` first, then grain's own: the reset has to come
	// before the helper it is meant to leave standing, since git applies
	// these in file order and a reset after `store` would discard that too.
	gitconfig = fmt.Sprintf(
		"[credential]\n\thelper =\n\thelper = store\n[user]\n\tname = %s\n\temail = %s\n",
		id.Name, id.Email,
	)
	return credentials, gitconfig, nil
}

func gitCredentialFiles(remoteURL, token string, identity GitIdentity) (credentials, gitconfig string, err error) {
	return GitCredentialFiles(remoteURL, token, identity)
}
