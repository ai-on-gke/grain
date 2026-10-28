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

// Package publication: the login a release job pushes container images
// with, answered by the same credential ladder every fetch and push
// through this proxy is answered by.
//
// Everything else in this package hands a credential to an *upstream
// request* -- the sandbox never sees it, because the sandbox never
// speaks to GitHub itself. A container push cannot be brokered that way:
// `docker push` talks the registry protocol to ghcr.io directly, over
// blob uploads far too large to stream through a proxy that buffers
// whole request bodies (forward.go), so what a publishing job needs is
// the credential itself and not a route to use it through.
//
// What that changed, and why this lives here rather than in the
// capability that places it: *which* credential. Grain's release job
// used to be handed a registry token of its own -- one more long-lived
// secret an operator pasted, rotated by hand, and which nothing on any
// pane could hold against the repository it publishes for
// (pkg/capability/registrypush). This deployment already knows what
// credential speaks for bwsalmon/grain: it is the one in
// secrets/github/, selected by the owner/repo ladder, re-minted from an
// App's private key when that is what backs it (credentials.go,
// apptoken.go). Asking the ladder for it means package publication
// authenticates as the deployment does, expires when an installation
// token expires, and follows a credential rotation the same day the
// pushes do.
//
// Two things the ladder cannot answer, and so are settled here:
//
//   - **The registry.** A credential is a GitHub credential; the
//     registry that accepts it is GitHub's own package registry, which
//     is ghcr.io for github.com and a per-host name on GHES. Hence a
//     hostname, defaulted rather than derived, and validated to be a
//     bare host so that what a caller writes into a `docker login`
//     cannot turn into a scheme or a path.
//   - **The username.** GHCR identifies the account from the token, not
//     from the username beside it, and an App installation token has no
//     user to name at all -- so this sends GitHub's own convention for
//     "a token is the whole credential", x-access-token, rather than
//     inventing a login for a credential that may not have one.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/authfix"
	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	// DefaultPackageRegistry is GitHub's package registry for
	// github.com. A deployment against GHES names its own instead.
	DefaultPackageRegistry = "ghcr.io"

	// PackageUsername is what the login sends beside the token. See the
	// note above: the token is the credential, and this is the name
	// GitHub itself uses when there is no user behind one.
	PackageUsername = "x-access-token"
)

// PackageCredentialSource is a host tool that mints a container-registry
// login for repositories on a forge whose release images do not publish to
// GitHub Packages (such as a self-hosted forge publishing to a cloud registry).
// "{host}" anywhere in Args, Env or LoginFix is the forge hostname.
type PackageCredentialSource struct {
	// Registry is the default container registry hostname when the caller
	// did not specify one (for example "us-docker.pkg.dev").
	Registry string
	// RegistryEnv, when set, names an environment variable whose non-empty
	// value overrides Registry when the caller did not specify one.
	RegistryEnv string
	// Username is the registry login username beside the token (for example
	// "oauth2accesstoken"). Empty defaults to PackageUsername.
	Username string
	// Command is the executable on PATH; Args follow it.
	Command string
	Args    []string
	// Env is added to the daemon's own environment for the run.
	Env []string
	// LoginFix is the command or instructions that repair a missing login.
	LoginFix string
}

type registeredPackageSource struct {
	hosts []string
	src   PackageCredentialSource
}

var (
	packageSourcesMu sync.RWMutex
	packageSources   []registeredPackageSource
)

// RegisterPackageCredentialSource registers src as the container-registry
// credential source for repositories on hosts (exact hostnames or "*.domain"
// patterns). An AmbientCommandSource may also set its own PackageCredential
// field directly. It returns a function that removes the registration.
func RegisterPackageCredentialSource(hosts []string, src PackageCredentialSource) (unregister func()) {
	if strings.TrimSpace(src.Command) == "" || len(hosts) == 0 {
		panic("gitproxy: RegisterPackageCredentialSource needs Hosts and Command")
	}
	cleanHosts := make([]string, len(hosts))
	for i, h := range hosts {
		cleanHosts[i] = strings.ToLower(strings.TrimSpace(h))
	}
	entry := registeredPackageSource{hosts: cleanHosts, src: src}
	packageSourcesMu.Lock()
	packageSources = append(packageSources, entry)
	packageSourcesMu.Unlock()
	return func() {
		packageSourcesMu.Lock()
		defer packageSourcesMu.Unlock()
		for i, have := range packageSources {
			if have.src.Command == src.Command && len(have.hosts) == len(cleanHosts) {
				packageSources = append(packageSources[:i:i], packageSources[i+1:]...)
				return
			}
		}
	}
}

func packageCredentialSourceFor(host, source string) (PackageCredentialSource, bool) {
	host = hostName(host)
	if host == "" {
		return PackageCredentialSource{}, false
	}
	if src, ok := resolvedSource(host, source); ok && src.PackageCredential != nil && (!src.OnlyHosts || src.claims(host)) {
		return *src.PackageCredential, true
	}
	packageSourcesMu.RLock()
	defer packageSourcesMu.RUnlock()
	for _, entry := range packageSources {
		if (AmbientCommandSource{Hosts: entry.hosts}).claims(host) {
			return entry.src, true
		}
	}
	return PackageCredentialSource{}, false
}

func commandPackageCredential(pkg PackageCredentialSource, host, registry, name, source string) (PackageCredential, error) {
	if registry == "" && pkg.RegistryEnv != "" {
		registry = strings.TrimSpace(os.Getenv(pkg.RegistryEnv))
	}
	if registry == "" {
		registry = strings.TrimSpace(pkg.Registry)
	}
	if registry == "" {
		registry = DefaultPackageRegistry
	}
	if strings.ContainsAny(registry, "/:@ ") {
		return PackageCredential{}, fmt.Errorf("%q is not a registry host: give a hostname, with no scheme, port or path", registry)
	}
	username := strings.TrimSpace(pkg.Username)
	if username == "" {
		username = PackageUsername
	}
	token, err := packageToken(pkg, host, registry, source)
	if err != nil {
		return PackageCredential{}, err
	}
	return PackageCredential{
		Registry: registry,
		Username: username,
		Token:    token,
		Name:     name,
	}, nil
}

// packageToken is pkg's token for registry, from the host account's
// login: run here on the host, or through the host's host-git server
// (UseHostGit) by source's name for a daemon in the release container.
func packageToken(pkg PackageCredentialSource, host, registry, source string) (string, error) {
	if c := activeHostGit(); c != nil {
		return c.packageToken(source, host, registry)
	}
	return localPackageToken(pkg, host, registry)
}

// localPackageToken runs pkg's command on this host.
func localPackageToken(pkg PackageCredentialSource, host, registry string) (string, error) {
	fix := expandHost(pkg.LoginFix, host)
	if fix == "" {
		fix = pkg.Command
	}
	path, err := exec.LookPath(pkg.Command)
	if err != nil {
		return "", fmt.Errorf("%s is not installed for %s: install it and run %s",
			pkg.Command, ambientAccount(), formatLoginFix(fix))
	}
	env := ambientEnv()
	for _, kv := range pkg.Env {
		env = append(env, expandHost(kv, host))
	}
	args := make([]string, len(pkg.Args))
	for i, a := range pkg.Args {
		args[i] = expandHost(a, host)
	}
	stdout, stderr, err := runHostTool(env, "", path, args...)
	if errors.Is(err, errHostToolTimeout) {
		return "", err
	}
	if _, expired := authfix.As(err); expired {
		return "", fmt.Errorf("%s package token for %s: %w (or run %s as %s)",
			pkg.Command, registry, err, formatLoginFix(fix), ambientAccount())
	}
	if err != nil {
		return "", fmt.Errorf("%s could not mint a package token for %s (%s): run %s as %s",
			pkg.Command, registry, shortMessage(strings.TrimSpace(stderr)+" "+err.Error()), formatLoginFix(fix), ambientAccount())
	}
	token := strings.TrimSpace(stdout)
	if token == "" || strings.ContainsAny(token, " \n\t") {
		return "", fmt.Errorf("%s printed no package token for %s: run %s as %s",
			pkg.Command, registry, formatLoginFix(fix), ambientAccount())
	}
	return token, nil
}

func formatLoginFix(fix string) string {
	if strings.Contains(fix, "`") {
		return fix
	}
	return "`" + fix + "`"
}

// PackageCredential is one registry login, ready for `docker login`.
type PackageCredential struct {
	// Registry is the registry host, with no scheme and no path.
	Registry string
	// Username is what to log in as -- PackageUsername today, kept a
	// field because it is the registry's convention and not the
	// caller's business to know.
	Username string
	// Token is the material. A GitHub token, with whatever package
	// permission the credential behind it carries: this resolves which
	// credential publishes, and only the registry can say whether that
	// credential is allowed to.
	Token string
	// Name is which ladder credential answered, for a log line or an
	// error that has to say where a refused push got its token from. It
	// is a name, never material.
	Name string
}

// PackageCredential is the login that publishes owner/repo's packages:
// the ladder's own credential for that repository, addressed to
// registry (DefaultPackageRegistry when empty, or the registered
// PackageCredentialSource's default registry).
//
// It fails rather than falling back to an anonymous push. Every other
// caller of the ladder has a reason to serve a credential-less request
// -- a public repository stays readable through a failed App mint
// (MintFailure's own doc comment) -- and a publish has none: an
// unauthenticated `docker push` is refused by the registry, several
// minutes of upload later, with the registry's error rather than this
// deployment's reason. So the reason is given here, before a job starts.
func (c *CredentialSet) PackageCredential(registry, owner, repo string) (PackageCredential, error) {
	if c == nil {
		return PackageCredential{}, errors.New("this deployment has no GitHub credentials, so nothing here can publish packages")
	}
	registry = strings.TrimSpace(registry)
	if registry != "" && strings.ContainsAny(registry, "/:@ ") {
		return PackageCredential{}, fmt.Errorf("%q is not a registry host: give a hostname, with no scheme, port or path", registry)
	}
	name, ok := c.selectName(owner, repo)
	if !ok {
		return PackageCredential{}, fmt.Errorf(
			"no GitHub credential in this deployment covers %s/%s, so there is nothing to publish its packages as",
			owner, repo)
	}
	if f, isAmbient, fileErr := c.loadAmbientFile(name); isAmbient {
		if fileErr != nil {
			return PackageCredential{}, fmt.Errorf(
				"this deployment's %q credential for %s/%s has no token right now: ambient credential %q cannot be used: %v",
				name, owner, repo, name, fileErr)
		}
		if h := c.HostOf(name); !model.SameGitHost(h.Host, f.Host, model.DefaultGitHost) {
			return PackageCredential{}, fmt.Errorf(
				"this deployment's %q credential for %s/%s has no token right now: ambient credential %q cannot be used: "+
					"it is for %s, but its host record says %s; set it up again as an ambient credential",
				name, owner, repo, name, f.Host, h.Host)
		}
		if err := AmbientAvailable(); err != nil {
			return PackageCredential{}, fmt.Errorf(
				"this deployment's %q credential for %s/%s has no token right now: ambient credential %q cannot be used: %v",
				name, owner, repo, name, err)
		}
		if pkg, ok := packageCredentialSourceFor(f.Host, f.Source); ok {
			return commandPackageCredential(pkg, f.Host, registry, name, f.Source)
		}
	} else {
		repoHost := model.RepoFromStored(owner, repo).Host
		if repoHost == "" {
			repoHost = c.HostOf(name).Host
		}
		if pkg, ok := packageCredentialSourceFor(repoHost, AmbientSourceAuto); ok {
			if err := AmbientAvailable(); err != nil {
				return PackageCredential{}, fmt.Errorf(
					"this deployment's %q credential for %s/%s has no token right now: %v",
					name, owner, repo, err)
			}
			return commandPackageCredential(pkg, repoHost, registry, name, AmbientSourceAuto)
		}
	}
	if registry == "" {
		registry = DefaultPackageRegistry
	}
	credential := c.load(name, "")
	if credential.Token == nil || *credential.Token == "" {
		if credential.MintFailure != nil {
			return PackageCredential{}, fmt.Errorf(
				"this deployment's %q credential for %s/%s has no token right now: %s",
				credential.Name, owner, repo, credential.MintFailure.Describe())
		}
		if credential.Unusable != "" {
			return PackageCredential{}, fmt.Errorf(
				"this deployment's %q credential for %s/%s has no token right now: %s",
				credential.Name, owner, repo, credential.Unusable)
		}
		return PackageCredential{}, fmt.Errorf(
			"this deployment's credential for %s/%s (%q) is anonymous, and an anonymous push publishes nothing",
			owner, repo, credential.Name)
	}
	if registry == DefaultPackageRegistry && strings.HasPrefix(*credential.Token, "github_pat_") {
		return PackageCredential{}, fmt.Errorf(
			"this deployment's %q credential for %s/%s is a fine-grained personal access token (github_pat_...), "+
				"which %s does not accept for container packages: configure a GitHub App installation with packages:write "+
				"or a classic PAT (ghp_...) with write:packages",
			credential.Name, owner, repo, registry)
	}
	return PackageCredential{
		Registry: registry,
		Username: PackageUsername,
		Token:    *credential.Token,
		Name:     credential.Name,
	}, nil
}
