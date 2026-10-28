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

// Per-credential git hosts (docs/git-hosts.md).
//
// Every credential records the host it belongs to, in <name>.host.json
// beside its <name>.token or <name>.app.json:
//
//	{"host": "ghe.example.com", "kind": "github", "base_url": "https://ghe.example.com:8443"}
//
// host is the git host (host[:port]); kind is "github" (github.com or a
// GitHub Enterprise Server: REST at https://api.github.com or
// https://<host>/api/v3, and App installation tokens minted there) or
// "git" (a plain git server, no forge API); base_url optionally gives the
// scheme and port the host is reached on, for a server not on https:443;
// proxy optionally names the HTTP(S) proxy every connection to that host
// goes through -- Google's GitHub-on-Prem is reached only through the
// workstation's local uplink proxy -- and nothing else's (HTTPClient).
//
// A credential with no host file is on the set's default host -- the
// proxy's ForwardHost, or github.com -- which is every credential grain
// held before hosts existed. BuildProxy writes that default down for a
// deployment forwarding somewhere other than github.com
// (MigrateCredentialHosts), so the record survives ForwardHost changing.
//
// The record is what keeps a credential on its own host: an App token is
// minted against that host's API and nowhere else (appTransportFor), and
// a set that knows where its tokens are going (ForwardHost) never hands
// out a credential recorded for any other host (Credential.WrongHost).

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// CredentialHost is one credential's host record.
type CredentialHost struct {
	// Host is the normalized git host (model.NormalizeGitHost).
	Host string `json:"host"`
	// Kind is model.GitHostGitHub or model.GitHostGit.
	Kind model.GitHostKind `json:"kind,omitempty"`
	// BaseURL is the scheme://host[:port] the host is reached on, when
	// that is not https on the default port. Empty means https://Host.
	BaseURL string `json:"base_url,omitempty"`
	// Proxy is the http(s)://host:port proxy every connection grain's
	// daemon makes to this host goes through -- git fetches and pushes,
	// REST calls, token checks -- and that no other host's traffic uses.
	// Empty means the daemon's own environment decides, as for any host.
	// It never carries a login: the record is not a secret.
	Proxy string `json:"proxy,omitempty"`
	// Recorded is false for a credential with no host file, whose host
	// is the set's default.
	Recorded bool `json:"-"`
}

// NormalizeCredentialHost checks and normalizes h: a host is required,
// the kind must be known, and a base URL must be an http(s) URL on that
// same host with nothing past it.
func NormalizeCredentialHost(h CredentialHost) (CredentialHost, error) {
	host, err := model.NormalizeGitHost(h.Host)
	if err != nil {
		return h, err
	}
	base := strings.TrimSuffix(strings.TrimSpace(h.BaseURL), "/")
	if host == "" && base != "" {
		host, err = model.NormalizeGitHost(base)
		if err != nil {
			return h, err
		}
	}
	if host == "" {
		return h, fmt.Errorf("a credential's host is required")
	}
	kind, err := model.ParseGitHostKind(string(h.Kind))
	if err != nil {
		return h, err
	}
	if base != "" {
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return h, fmt.Errorf("base URL %q must be http(s)://host[:port]", h.BaseURL)
		}
		if bh, _ := model.NormalizeGitHost(u.Host); bh != host && hostName(bh) != hostName(host) {
			return h, fmt.Errorf("base URL %q is not on host %s", h.BaseURL, host)
		}
		base = u.Scheme + "://" + u.Host
	}
	proxy, err := NormalizeHostProxy(h.Proxy)
	if err != nil {
		return h, err
	}
	return CredentialHost{Host: host, Kind: kind, BaseURL: base, Proxy: proxy, Recorded: h.Recorded}, nil
}

// NormalizeHostProxy checks a host record's proxy: empty, or an
// http(s)://host:port URL with nothing else in it. A login in it is
// refused rather than stored, because the host record is not a secret
// (it is exported, shown and logged like any setting).
func NormalizeHostProxy(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("proxy %q must be http(s)://host:port", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("proxy %q carries a login, and a host record is not a secret: "+
			"give the proxy's address only", u.Redacted())
	}
	return u.Scheme + "://" + u.Host, nil
}

// HTTPTransport is the round tripper for connections to h: a clone of
// base (http.DefaultTransport when nil) sending through h's proxy when it
// has one, and base itself otherwise -- so a host with no proxy is
// reached exactly as before, the daemon's own HTTPS_PROXY and NO_PROXY
// included.
func (h CredentialHost) HTTPTransport(base *http.Transport) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport.(*http.Transport)
	}
	if h.Proxy == "" {
		return base
	}
	u, err := url.Parse(h.Proxy)
	if err != nil {
		// NormalizeCredentialHost let nothing unparseable through; a
		// record built by hand that did must not fall back to going
		// direct, which for a proxy-only host leaks nothing but hangs.
		return failingTransport{fmt.Errorf("proxy %q for %s is not a URL: %v", h.Proxy, h.Host, err)}
	}
	t := base.Clone()
	t.Proxy = http.ProxyURL(u)
	return t
}

// failingTransport answers every request with err.
type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// HTTPClient is an http.Client for h's REST API, bounded by timeout and
// sent through h's proxy when it has one (HTTPTransport). A redirect to
// a login page authfix knows is not followed but answered as
// LoginRedirectError, so a host behind a front end whose login has lapsed
// says so rather than handing back the login page's HTML.
func (h CredentialHost) HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: h.HTTPTransport(nil), CheckRedirect: loginRedirectCheck}
}

// hostName is host[:port] without its port.
func hostName(hostport string) string {
	if name, _, err := net.SplitHostPort(hostport); err == nil {
		return name
	}
	return hostport
}

// UseTLS reports whether h is reached over https.
func (h CredentialHost) UseTLS() bool {
	return !strings.HasPrefix(h.BaseURL, "http://")
}

func (h CredentialHost) useTLS() bool {
	return h.UseTLS()
}

// CheckTransport is whether the git proxy will forward to h at all: the
// refusal ValidateForwardTarget gives every request to an http:// base
// URL on anything but loopback, given when the host is saved instead of
// on each clone after it.
//
// A host behind a proxy of its own is left alone. Its REST calls go to
// that proxy, which is what reaches the host (GitHub-on-Prem behind
// uplink), and its git traffic can go over SSH and never reach the
// forwarder.
func (h CredentialHost) CheckTransport() error {
	if h.Proxy != "" {
		return nil
	}
	return ValidateForwardTarget(h.hostPort(), h.useTLS())
}

// hostPort is where h's git and API traffic goes: BaseURL's host[:port]
// when set, Host otherwise.
func (h CredentialHost) hostPort() string {
	if h.BaseURL != "" {
		if u, err := url.Parse(h.BaseURL); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return h.Host
}

// APIBase is h's REST API, as the github.RealTransport host and path
// prefix a call to it needs: api.github.com for github.com, and
// <host>/api/v3 for a GitHub Enterprise Server. A git-kind host has no
// API, and answers ok false.
func (h CredentialHost) APIBase() (host, prefix string, ok bool) {
	if h.Kind == model.GitHostGit {
		return "", "", false
	}
	if h.Host == model.DefaultGitHost && h.BaseURL == "" {
		return github.APIHost(h.Host), "", true
	}
	return h.hostPort(), "/api/v3", true
}

// hostFile is name's host record path.
func (c *CredentialSet) hostFile(name string) string {
	return filepath.Join(c.dir, name+".host.json")
}

// defaultHost is the host a credential with no record is on: the
// ForwardHost this set's tokens go to, or github.com.
func (c *CredentialSet) defaultHost() string {
	host, err := model.ResolveGitHost(c.ForwardHost, "")
	if err != nil || host == "" {
		return model.DefaultGitHost
	}
	return host
}

// HostOf is name's host record, or the set's default host for a
// credential with none. When name is a host rather than a credential in
// the set, it returns the host record of the first credential recorded
// on that host. A record that will not parse is logged and read as the
// default host too -- the same fail-soft every other credential file
// gets here -- except that it is never quietly some *other* host.
func (c *CredentialSet) HostOf(name string) CredentialHost {
	def := CredentialHost{Host: c.defaultHost(), Kind: model.GitHostGitHub}
	if c.ForwardInsecure && c.ForwardHost != "" {
		def.BaseURL = "http://" + c.ForwardHost
	}
	if name == "" || name == "anonymous" {
		return def
	}
	if h, ok := c.readHostFile(name, def); ok {
		return h
	}
	if !c.exists(name) {
		for _, cred := range c.Names() {
			if h, ok := c.readHostFile(cred, def); ok && h.Recorded && model.SameGitHost(h.Host, name, def.Host) {
				return h
			}
		}
	}
	return def
}

func (c *CredentialSet) readHostFile(name string, def CredentialHost) (CredentialHost, bool) {
	if namesOneFile(name) != nil {
		return CredentialHost{}, false
	}
	data, err := os.ReadFile(c.hostFile(name))
	if err != nil {
		return CredentialHost{}, false
	}
	var raw CredentialHost
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Printf("gitproxy: %s.host.json is not valid JSON: %v -- reading it as %s", name, err, def.Host)
		return def, true
	}
	h, err := NormalizeCredentialHost(raw)
	if err != nil {
		log.Printf("gitproxy: %s.host.json: %v -- reading it as %s", name, err, def.Host)
		return def, true
	}
	h.Recorded = true
	return h, true
}

// SetHost records name's host. The credential itself must already exist:
// a host record for nothing is a file nobody would ever read.
func (c *CredentialSet) SetHost(name string, h CredentialHost) error {
	if err := ValidCredentialName(name); err != nil {
		return err
	}
	if !c.exists(name) {
		return fmt.Errorf("no credential named %q is configured", name)
	}
	h, err := NormalizeCredentialHost(h)
	if err != nil {
		return err
	}
	if err := h.CheckTransport(); err != nil {
		return err
	}
	if h.Kind == model.GitHostGit && c.IsApp(name) {
		return fmt.Errorf("%q is a GitHub App credential, and a plain git host has no App API to mint its tokens", name)
	}
	// An ambient credential's host is the one its file asks the host's
	// login for a token for (SetAmbientOn writes both): a record naming
	// any other host would send that login's token there.
	if f, ok := c.AmbientOf(name); ok && !model.SameGitHost(h.Host, f.Host, model.DefaultGitHost) {
		return fmt.Errorf("%q is an ambient credential for %s: it cannot be recorded on %s -- "+
			"set it up again as an ambient credential for that host instead", name, f.Host, h.Host)
	}
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(c.dir, name+".host.json", append(data, '\n')); err != nil {
		return fmt.Errorf("recording the host of credential %q: %w", name, err)
	}
	c.forget(name)
	return nil
}

// MigrateCredentialHosts writes the set's default host down for every
// credential that has no host record yet, so a credential configured
// while the deployment forwarded to one host stays on that host if
// ForwardHost later changes. It does nothing on a github.com set, whose
// unrecorded credentials are already where they always were.
func (c *CredentialSet) MigrateCredentialHosts() {
	def := c.defaultHost()
	if def == model.DefaultGitHost {
		return
	}
	for _, name := range c.Names() {
		if _, err := os.Stat(c.hostFile(name)); err == nil {
			continue
		}
		if err := c.SetHost(name, c.HostOf(name)); err != nil {
			log.Printf("gitproxy: recording credential %q's host as %s: %v", name, def, err)
		}
	}
}

// wrongHost is the refusal for a credential recorded on a host other than
// host, the one the request it is about to go out on is for (empty: the
// set's default host), or "" when it may be used. A set that was never
// told its ForwardHost (one built for REST calls) has nothing to compare
// a default-host request against, and refuses nothing for one.
func (c *CredentialSet) wrongHost(name, host string) string {
	if host == "" && c.ForwardHost == "" {
		return ""
	}
	want, err := model.ResolveGitHost(host, c.defaultHost())
	if err != nil {
		return fmt.Sprintf("git host %q is not a host name", host)
	}
	h := c.HostOf(name)
	if h.Host == want {
		return ""
	}
	if name == "anonymous" {
		return fmt.Sprintf("no credential is configured for git host %s -- add a git connection for %s", want, want)
	}
	return fmt.Sprintf("credential %q is for git host %s, and this request goes to %s -- grain never sends "+
		"one host's credential to another; add a git connection for %s", name, h.Host, want, want)
}

// DefaultHost is the host a credential with no host record is on, and
// the host a request naming none goes to: the ForwardHost this set's
// tokens go to, or github.com.
func (c *CredentialSet) DefaultHost() string { return c.defaultHost() }

// appTransportFor is where name's App installation token is minted: the
// injected AppTransport if there is one (a test's), otherwise the REST API
// of the credential's own host -- api.github.com, or <host>/api/v3 on a
// GitHub Enterprise Server. The App's JWT goes there and nowhere else. A
// credential on a git-kind host has no API to mint against, and gets an
// error instead.
func (c *CredentialSet) appTransportFor(name string) (github.Transport, error) {
	if c.AppTransport != nil {
		return c.AppTransport, nil
	}
	h := c.HostOf(name)
	host, prefix, ok := h.APIBase()
	if !ok {
		return nil, fmt.Errorf("credential %q is on %s, a plain git host with no GitHub App API to mint "+
			"installation tokens against", name, h.Host)
	}
	transport := github.NewRealTransport(host)
	transport.Host = host
	transport.PathPrefix = prefix
	transport.UseTLS = h.useTLS()
	transport.Client = h.HTTPClient(appMintTimeout)
	return transport, nil
}

// writeFileAtomic writes data to dir/file through a temporary file and a
// rename, mode 0600, the way SetToken writes a token.
func writeFileAtomic(dir, file string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+file+".tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, file))
}
