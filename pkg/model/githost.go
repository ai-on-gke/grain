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

package model

// Git hosts: which server a credential, a team connection or (later) a
// repository lives on. docs/git-hosts.md is the design this is the first
// piece of.

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
)

// DefaultGitHost is the host a repository or credential written without
// one means: github.com, unless a deployment forwards somewhere else
// (gitproxy.BuildConfig.ForwardHost), in which case that host is the
// default instead -- which is what "existing credentials migrate in place
// to the current ForwardHost" amounts to (docs/git-hosts.md).
const DefaultGitHost = "github.com"

// GitHostKind says what a host offers beyond git itself.
type GitHostKind string

const (
	// GitHostGitHub is github.com or a GitHub Enterprise Server: git, plus
	// the REST API at https://api.github.com or https://<host>/api/v3, and
	// GitHub App installation tokens minted there.
	GitHostGitHub GitHostKind = "github"
	// GitHostGit is a plain git server: clone, fetch and push, and no
	// forge API -- no pull requests, checks, merges or App tokens.
	GitHostGit GitHostKind = "git"
)

// ParseGitHostKind reads a kind as written in a credential's host record
// or a team connection; empty is GitHostGitHub, which is what every
// credential grain held before hosts existed was.
func ParseGitHostKind(s string) (GitHostKind, error) {
	switch GitHostKind(strings.ToLower(strings.TrimSpace(s))) {
	case "", GitHostGitHub:
		return GitHostGitHub, nil
	case GitHostGit:
		return GitHostGit, nil
	}
	return "", fmt.Errorf("git host kind must be %q or %q, got %q", GitHostGitHub, GitHostGit, s)
}

// NormalizeGitHost is the one spelling of a host grain compares and
// stores: lowercase host[:port], with a default port (:443) dropped and a
// scheme, path or trailing slash ignored, so "https://GHE.example.com/"
// and "ghe.example.com:443" are one host. Empty stays empty -- the
// caller's default host. Userinfo, a query or anything that is not a host
// at all is refused rather than folded into one.
func NormalizeGitHost(s string) (string, error) {
	text := strings.TrimSpace(s)
	if text == "" {
		return "", nil
	}
	hostport := text
	if strings.Contains(text, "://") {
		u, err := url.Parse(text)
		if err != nil || u.Host == "" {
			return "", fmt.Errorf("git host %q is not a host or URL", s)
		}
		if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", fmt.Errorf("git host %q must be a bare host or base URL", s)
		}
		hostport = u.Host
	} else {
		hostport = strings.TrimSuffix(hostport, "/")
	}
	hostport = strings.ToLower(hostport)
	name, port, err := net.SplitHostPort(hostport)
	if err != nil {
		name, port = hostport, ""
	}
	if name == "" || strings.ContainsAny(name, "/@?#\\ \t") {
		return "", fmt.Errorf("git host %q is not a host name", s)
	}
	if port == "443" {
		port = ""
	}
	if port != "" {
		return net.JoinHostPort(name, port), nil
	}
	return name, nil
}

// SameGitHost reports whether a and b name one host once each is
// normalized, an empty one standing for def (itself empty meaning
// DefaultGitHost). A host that does not parse is the same as nothing.
func SameGitHost(a, b, def string) bool {
	na, okA := resolveGitHost(a, def)
	nb, okB := resolveGitHost(b, def)
	return okA && okB && na == nb
}

// ResolveGitHost is NormalizeGitHost with an empty host standing for def,
// and an empty def for DefaultGitHost.
func ResolveGitHost(host, def string) (string, error) {
	if strings.TrimSpace(host) == "" {
		host = def
	}
	if strings.TrimSpace(host) == "" {
		host = DefaultGitHost
	}
	return NormalizeGitHost(host)
}

func resolveGitHost(host, def string) (string, bool) {
	n, err := ResolveGitHost(host, def)
	return n, err == nil && n != ""
}

// defaultRepoHost is the deployment's default git host as RepoRef reads
// it: the host a repository written without one is on, which ParseRepo
// drops and String leaves out. Empty is DefaultGitHost. A process-wide
// value rather than a parameter because a RepoRef is parsed and printed
// in hundreds of places that have no deployment in hand, and the answer
// is the same for all of them (SetDefaultGitHost).
var defaultRepoHost atomic.Value // string

// SetDefaultGitHost sets the host a repository written without one is on
// -- the daemon's -github-host, the same value Store.GitDefaultHost
// carries. Empty, or anything that does not parse as a host, is
// DefaultGitHost.
func SetDefaultGitHost(host string) {
	n, err := ResolveGitHost(host, "")
	if err != nil {
		n = DefaultGitHost
	}
	defaultRepoHost.Store(n)
}

// DefaultRepoHost is the host a RepoRef with no Host is on
// (SetDefaultGitHost), normalized and never empty.
func DefaultRepoHost() string {
	if h, ok := defaultRepoHost.Load().(string); ok && h != "" {
		return h
	}
	return DefaultGitHost
}

// canonicalRepoHost is host as a RepoRef carries it: normalized, and
// empty for the default host, so that one repository has one RepoRef
// however its host was written.
func canonicalRepoHost(host string) (string, error) {
	n, err := NormalizeGitHost(host)
	if err != nil || n == "" {
		return "", err
	}
	if !validHostName(hostOnly(n)) {
		return "", fmt.Errorf("git host %q is not a host name", host)
	}
	if n == DefaultRepoHost() {
		return "", nil
	}
	return n, nil
}

// hostOnly is host[:port] without its port.
func hostOnly(hostport string) string {
	if name, _, err := net.SplitHostPort(hostport); err == nil {
		return name
	}
	return hostport
}

// validHostName reports whether name is a DNS name or IPv4 address: dot
// separated labels of letters, digits, hyphens and underscores, none of
// them empty -- which is what keeps a path segment like ".." from being
// read as the host of a repository.
func validHostName(name string) bool {
	if name == "" {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return false
			}
		}
	}
	return true
}
