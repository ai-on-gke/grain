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

// The ambient credential: <name>.ambient.json, which holds no secret at
// all. It records a git host and which of this host's own tools to ask
// for a token -- `gh auth token --hostname <host>`, or `git credential
// fill` -- and load asks it at the moment a token is needed, as the
// daemon's own account, so the deployment follows that account's GitHub
// login (a logout, a re-login, a rotated token) instead of holding a
// snapshot of it the way a token seeded from `gh auth token` would.
//
// What it costs, said plainly because it is the whole trade-off: pushes
// and API calls made with it act as whoever `gh` is logged in as, with
// all of that account's access. So it is refused on a daemon that is not
// running on the host (AmbientAvailable: inside the deployment container
// there is no meaningful login to borrow), pkg/ui lets only a system
// admin create one, it is never a ladder pattern or a nameable capability
// (SetPattern, ExtraNames) -- only the teams it is assigned to use it --
// and the token itself lives only in load's cache: never on disk, in the
// database, in a log line or in the state-repository export.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ai-on-gke/grain/pkg/authfix"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/procgroup"
	"github.com/ai-on-gke/grain/pkg/whererun"
)

// AuthTypeAmbient is AuthType's answer for an ambient credential.
const AuthTypeAmbient = "ambient"

// The sources an ambient credential asks that core grain knows itself:
// gh, git credential fill, and auto's choice between them. Any other is
// one RegisterAmbientSource added.
const (
	AmbientSourceGH   = "gh"
	AmbientSourceGit  = "git"
	AmbientSourceAuto = "auto"
)

// AmbientCommandSource is one host tool that prints a token for a host:
// the command, the environment it needs, and the command that fixes a
// missing login -- data, so another wrapper around gh is one more
// RegisterAmbientSource rather than more code. "{host}" anywhere in Args,
// Env, LoginFix or ScopeFix is the host name the token is for.
type AmbientCommandSource struct {
	// Name is the source as an ambient file stores it.
	Name string
	// Command is the executable, looked up on PATH; Args follow it.
	Command string
	Args    []string
	// Env is added to the daemon's own environment for the run.
	Env []string
	// LoginFix is the command that logs the daemon's account in.
	LoginFix string
	// Hosts are the host patterns this source is auto's answer for: an
	// exact host, or "*.domain" for any host under domain. A host none
	// of the sources names gets gh, then git.
	Hosts []string
	// OnlyHosts makes Hosts the only hosts the source may be asked for,
	// even by name -- a tool for one forge is never asked for a
	// github.com login.
	OnlyHosts bool
	// ScopeFix is the command that adds the scopes grain needs to the
	// login: `repo`, and `workflow` for a push that touches
	// .github/workflows.
	ScopeFix string
	// PushRefusedHint, when set, is asked about a push the source's host
	// rejected a ref of: message is the host's own words (the reasons on
	// its "ng" report lines and the "remote:" text beside them), and what
	// it answers is added to what the pusher sees, as more "remote:"
	// lines (or on the ng line, to a client without side-band): what to
	// run to fix a refusal the host's text names but does not explain,
	// such as a login without the `workflow` scope. Empty says nothing
	// more; so does nil (pushhint.go).
	PushRefusedHint func(host, message string) string
	// ForgeViaCommand, when set, marks a tool that is the only way to the
	// hosts it is for: they sit behind a front end that answers any
	// client without the workstation's own login with a redirect to a
	// login page, and the tool applies that login and transport itself.
	// grain then opens no connection of its own to such a host for a
	// credential from this source: its forge API calls and the git
	// proxy's upstream hop are each one `<Command> api` run on this host
	// (CommandForgeTransport, commandforge.go). A proxy in the host record
	// is optional, and honoured when an operator sets one.
	ForgeViaCommand *CommandForge
	// PackageCredential, when set, mints the container-registry login
	// (CredentialSet.PackageCredential) for repositories on this source's
	// hosts via a host tool instead of sending the git token to ghcr.io.
	PackageCredential *PackageCredentialSource
	// UI is how the connection editor offers the source. Nil offers it in
	// the Source menu by its command alone.
	UI *AmbientSourceUI
}

// AmbientSourceUI is what the connection editor says about a registered
// source: its own label in the Source menu, the hosts it fills the form
// in for, and what to tell the operator once one of them is chosen.
// "{host}" anywhere in Help is the host; text between backticks is shown
// as code.
type AmbientSourceUI struct {
	// Label is the source's entry in the Source menu.
	Label string
	// Help is said under the form when one of Presets is chosen: where
	// the token comes from, how to check the login and what to run when
	// it lapses.
	Help string
	// Presets are hosts the editor offers to fill a new connection in
	// for: the host, a GitHub kind and this source.
	Presets []AmbientSourcePreset
}

// AmbientSourcePreset is one host an AmbientSourceUI offers.
type AmbientSourcePreset struct {
	Host  string
	Label string
}

// CommandForge is what a ForgeViaCommand source says about its hosts
// beyond the tool itself. Every field is optional.
type CommandForge struct {
	// GitAuthorization is the Authorization header the hosts' git
	// smart-HTTP endpoints take for token, handed to the tool for those
	// requests only; nil sends them with the tool's own login, as a REST
	// call is.
	GitAuthorization func(token string) string
	// LoginPage reports whether text -- what the tool printed on stderr,
	// or an HTML page it answered with -- is the front end's login page,
	// and the login address it names, which authfix.ClassifyRedirect then
	// turns into what to run (LoginRedirectError).
	LoginPage func(text string) (location string, ok bool)
	// GitRefusedHint is said when a git endpoint refused a token whose
	// login the REST API still accepts: how the hosts' git endpoints take
	// a token and how to check that by hand. Empty, or nil, says nothing
	// more.
	GitRefusedHint func(host, owner, repo string) string
}

// builtinAmbientSources are the token-printing sources core grain has
// whatever is registered. git credential fill speaks a protocol on stdin
// rather than printing a token, and is gitCredentialToken below.
var builtinAmbientSources = []AmbientCommandSource{
	{
		Name:     AmbientSourceGH,
		Command:  "gh",
		Args:     []string{"auth", "token", "--hostname", "{host}"},
		Env:      []string{"GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"},
		LoginFix: "gh auth login --hostname {host}",
		ScopeFix: "gh auth refresh --hostname {host} --scopes repo,workflow",
		PushRefusedHint: func(host, message string) string {
			if !WorkflowScopeRefused(message) {
				return ""
			}
			return "run `gh auth refresh --hostname " + host + " --scopes repo,workflow` as " +
				ambientAccount() + " to give the login the `workflow` scope, then push again"
		},
	},
}

var (
	ambientSourcesMu  sync.RWMutex
	registeredSources []AmbientCommandSource
)

// RegisterAmbientSource adds src to the sources an ambient credential
// may name, ahead of the built-in ones in auto's order, and returns what
// takes it away again (for a test). It panics on a name already taken,
// or one of git and auto: a registration is made once, from init().
func RegisterAmbientSource(src AmbientCommandSource) (unregister func()) {
	ambientSourcesMu.Lock()
	defer ambientSourcesMu.Unlock()
	if src.Name == "" || src.Command == "" {
		panic("gitproxy: RegisterAmbientSource needs a Name and a Command")
	}
	if src.Name == AmbientSourceGit || src.Name == AmbientSourceAuto {
		panic("gitproxy: ambient source " + src.Name + " is reserved")
	}
	for _, have := range append(append([]AmbientCommandSource{}, registeredSources...), builtinAmbientSources...) {
		if have.Name == src.Name {
			panic("gitproxy: ambient source " + src.Name + " registered twice")
		}
	}
	registeredSources = append(registeredSources, src)
	return func() {
		ambientSourcesMu.Lock()
		defer ambientSourcesMu.Unlock()
		for i, have := range registeredSources {
			if have.Name == src.Name {
				registeredSources = append(registeredSources[:i:i], registeredSources[i+1:]...)
				return
			}
		}
	}
}

// ambientCommandSources is every token-printing source, in the order
// auto prefers them for a host more than one claims: the registered
// ones in the order they were registered, then the built-in ones.
func ambientCommandSources() []AmbientCommandSource {
	ambientSourcesMu.RLock()
	defer ambientSourcesMu.RUnlock()
	out := make([]AmbientCommandSource, 0, len(registeredSources)+len(builtinAmbientSources))
	out = append(out, registeredSources...)
	return append(out, builtinAmbientSources...)
}

// RegisteredAmbientSources is every source RegisterAmbientSource added,
// in auto's order: what the connection editor offers beside gh and git.
func RegisteredAmbientSources() []AmbientCommandSource {
	ambientSourcesMu.RLock()
	defer ambientSourcesMu.RUnlock()
	return append([]AmbientCommandSource{}, registeredSources...)
}

// ambientCommandSource is the command source named name.
func ambientCommandSource(name string) (AmbientCommandSource, bool) {
	for _, src := range ambientCommandSources() {
		if src.Name == name {
			return src, true
		}
	}
	return AmbientCommandSource{}, false
}

// claims reports whether host is one of src's Hosts.
func (src AmbientCommandSource) claims(host string) bool {
	host = strings.ToLower(hostName(host))
	for _, pattern := range src.Hosts {
		if domain, ok := strings.CutPrefix(pattern, "*."); ok {
			if strings.HasSuffix(host, "."+domain) {
				return true
			}
		} else if host == pattern {
			return true
		}
	}
	return false
}

// expand is s with {host} replaced.
func expandHost(s, host string) string { return strings.ReplaceAll(s, "{host}", host) }

// AmbientLoginFix is the command that logs this host's account in to host
// for source: what a refusal tells the operator to run. For auto it is
// the command of the source auto asks first.
func AmbientLoginFix(host, source string) string {
	host = hostName(host)
	if host == "" {
		host = model.DefaultGitHost
	}
	if source == AmbientSourceGit {
		return "gh auth setup-git"
	}
	if source == "" || source == AmbientSourceAuto {
		source = ambientAutoSources(host)[0]
	}
	if src, ok := ambientCommandSource(source); ok {
		return expandHost(src.LoginFix, host)
	}
	return "gh auth login --hostname " + host
}

// ambientAutoSources is the order auto asks the sources in for host: the
// sources that claim host when any does, and gh then git otherwise.
func ambientAutoSources(host string) []string {
	var claimed []string
	for _, src := range ambientCommandSources() {
		if src.claims(host) {
			claimed = append(claimed, src.Name)
		}
	}
	if len(claimed) > 0 {
		return claimed
	}
	return []string{AmbientSourceGH, AmbientSourceGit}
}

// AmbientAutoSource is the source auto asks first for host: what the
// connection editor shows and the installer writes down.
func AmbientAutoSource(host string) string {
	return ambientAutoSources(hostName(host))[0]
}

// resolvedSource is the command source source means for host: itself,
// or auto's first choice.
func resolvedSource(host, source string) (AmbientCommandSource, bool) {
	if source == "" || source == AmbientSourceAuto {
		source = AmbientAutoSource(host)
	}
	return ambientCommandSource(source)
}

// AmbientCommandOf is the tool source asks for host's login ("gh", or a
// registered source's), or "git credential fill".
func AmbientCommandOf(host, source string) string {
	if src, ok := resolvedSource(hostName(host), source); ok {
		return src.Command
	}
	return "git credential fill"
}

// AmbientScopeFix is the command that gives source's login on host the
// scopes grain needs: `repo`, and `workflow` where the source's own fix
// asks for it.
func AmbientScopeFix(host, source string) string {
	host = hostName(host)
	if src, ok := resolvedSource(host, source); ok && src.ScopeFix != "" {
		return expandHost(src.ScopeFix, host)
	}
	return "gh auth refresh --hostname " + host + " --scopes repo,workflow"
}

// AmbientForgeViaCommand reports whether source's login on host comes
// from a tool that carries the host's traffic itself (ForgeViaCommand):
// grain's forge calls to it go through that tool, and its host record
// needs no proxy.
func AmbientForgeViaCommand(host, source string) bool {
	src, ok := resolvedSource(hostName(host), source)
	return ok && src.ForgeViaCommand != nil
}

// forgeCommand is the source whose tool carries credential name's forge
// and git traffic, and the host record it is for: set for an ambient
// credential from a ForgeViaCommand source, and ok false for any other.
func (c *CredentialSet) forgeCommand(name string) (AmbientCommandSource, CredentialHost, bool) {
	if c == nil || !c.IsAmbient(name) {
		return AmbientCommandSource{}, CredentialHost{}, false
	}
	f, ok := c.AmbientOf(name)
	if !ok {
		return AmbientCommandSource{}, CredentialHost{}, false
	}
	src, ok := resolvedSource(hostName(f.Host), f.Source)
	if !ok || src.ForgeViaCommand == nil {
		return AmbientCommandSource{}, CredentialHost{}, false
	}
	h := c.HostOf(name)
	if !model.SameGitHost(h.Host, f.Host, model.DefaultGitHost) {
		h = CredentialHost{Host: f.Host, Kind: h.Kind}
	}
	return src, h, true
}

// ForgeViaCommand reports whether credential name's forge calls go
// through a host tool (`<tool> api`) rather than grain's own HTTP client.
func (c *CredentialSet) ForgeViaCommand(name string) bool {
	_, _, ok := c.forgeCommand(name)
	return ok
}

// ForgeHTTPClient is the http.Client for credential name's forge API,
// bounded by timeout: one that runs `<tool> api` for a credential whose
// source is such a tool, and the host record's own (HTTPClient)
// otherwise.
func (c *CredentialSet) ForgeHTTPClient(name string, timeout time.Duration) *http.Client {
	if src, h, ok := c.forgeCommand(name); ok {
		return commandForgeClient(src, h, timeout)
	}
	return c.HostOf(name).HTTPClient(timeout)
}

// ForgeTransport is the github.Transport for credential name's own
// host's REST API: what a check of that credential, or a listing of what
// it reaches, sends with.
func (c *CredentialSet) ForgeTransport(name string) (github.Transport, error) {
	if c.IsAmbient(name) {
		f, _ := c.AmbientOf(name)
		host := f.Host
		if host == "" {
			host = c.HostOf(name).Host
		}
		return c.ambientTransportFor(name, host)
	}
	h := c.HostOf(name)
	apiHost, prefix, ok := h.APIBase()
	if !ok {
		return nil, fmt.Errorf("credential %q is on %s, a plain git host with no API", name, h.Host)
	}
	transport := github.NewRealTransport(apiHost)
	transport.Host = apiHost
	transport.PathPrefix = prefix
	transport.UseTLS = h.useTLS()
	transport.Client = h.HTTPClient(appMintTimeout)
	return transport, nil
}

// forgeAPITimeout bounds one REST call ForgeTransportOn's transport
// makes over a plain HTTP client. A command-backed one is bounded per run
// of the tool instead (commandForgeTimeout).
const forgeAPITimeout = 60 * time.Second

// ForgeTransportOn is the github.Transport for the REST API of a
// repository on host whose git traffic credential name carries -- the
// one route the server side reaches a forge on any host but its own
// -github-host by, so that a pull request, a branch read or a merge for a
// repository on that host goes where its pushes went:
//
//   - an ambient credential (the host's own login) is asked through
//     ambientTransportFor, which for a ForgeViaCommand source means a
//     `<tool> api` run per request (CommandForgeTransport) against
//     https://<host>/api/v3, the one way through the front end;
//   - a credential recorded on host is sent to that host's API base
//     (APIBase: api.github.com, or <host>/api/v3 for an Enterprise
//     host), through its proxy if it names one;
//   - no credential, or one on another host, falls back to host's own
//     record, so the request at least goes to the right host and is
//     refused there rather than answered by some other forge.
//
// A plain git host has no API to send to, and is an error.
func (c *CredentialSet) ForgeTransportOn(name, host string) (github.Transport, error) {
	if name != "" && name != "anonymous" && c.IsAmbient(name) {
		return c.ambientTransportFor(name, host)
	}
	h := c.HostOf(host)
	if name != "" && name != "anonymous" {
		if rec := c.HostOf(name); model.SameGitHost(rec.Host, host, model.DefaultGitHost) {
			h = rec
		}
	}
	if !model.SameGitHost(h.Host, host, model.DefaultGitHost) {
		h = CredentialHost{Host: host, Kind: model.GitHostGitHub}
	}
	apiHost, prefix, ok := h.APIBase()
	if !ok {
		return nil, fmt.Errorf("repository host %s is a plain git host with no forge API", host)
	}
	transport := github.NewRealTransport(apiHost)
	transport.Host = apiHost
	transport.PathPrefix = prefix
	transport.UseTLS = h.useTLS()
	transport.Client = h.HTTPClient(forgeAPITimeout)
	return transport, nil
}

// ambientSourceAllowed is nil when source may be asked for a login to
// host: a source with OnlyHosts is never asked for any other host's.
func ambientSourceAllowed(host, source string) error {
	src, ok := ambientCommandSource(source)
	if !ok || !src.OnlyHosts || src.claims(host) {
		return nil
	}
	return fmt.Errorf("%s is only for %s, never for %s: use %s there",
		src.Command, strings.Join(src.Hosts, ", "), hostName(host), AmbientAutoSource(host))
}

// ambientTokenTTL is how long load trusts a token the host's tools
// handed it before asking again. Short, so a logout or a rotated token
// is followed within minutes; Refused drops it sooner on a 401.
const ambientTokenTTL = 5 * time.Minute

// ambientFailureRetryDelay is how long a failed lookup is cached, so a
// host that is not logged in is not asked again by every request in a
// burst, while `gh auth login` is picked up almost at once.
const ambientFailureRetryDelay = 15 * time.Second

// ambientCommandTimeout bounds one `gh` or `git credential` run
// (runHostTool). load does not hold mu across it: concurrent loads of the
// same credential share one run (loadAmbientLocked), and every other
// credential's lookup goes on meanwhile. A var so a test can shorten it.
var ambientCommandTimeout = 15 * time.Second

// ambientWaitDelay is how long runHostTool waits for a host tool's output
// pipes to close once the tool itself has exited or been killed, before
// closing them itself (exec.Cmd.WaitDelay).
var ambientWaitDelay = 2 * time.Second

// AmbientFile is <name>.ambient.json: a host and a source, and nothing
// that is a secret.
type AmbientFile struct {
	Host   string `json:"host"`
	Source string `json:"source"`
}

// AmbientCredential is set on a Credential loaded from an ambient file:
// where its token came from, so a refusal can say what to fix and a 401
// can drop it (Refused).
type AmbientCredential struct {
	Host   string
	Source string
	// From is the source that actually answered: "gh" or "git", which
	// for AmbientSourceAuto is whichever came first.
	From string
	// User is the username `git credential fill` answered with beside
	// the password, when that is the source that answered. It is who a
	// credential on a plain git host acts as: such a host has no /user
	// to ask (AmbientIdentityOf).
	User string
}

// AmbientAvailable is nil when this process may use the host's own login:
// the daemon runs on the bare machine, not inside a container. Which of
// the two it is comes from pkg/whererun, which calls a daemon
// containerised only on evidence of a container. Every machine install
// runs the daemon in the release container (host mode was removed, task
// 5041), so in practice this is a development daemon's: `grain demo`, or
// a local `grain daemon` -- or a daemon in the release container beside
// grain hostd, when hostd's allowlist offers the host-git capability
// server (UseHostGit, hostgit.go): the host's gh (or a registered source's tool) then runs on the
// host, through that server.
func AmbientAvailable() error {
	return ambientAvailable(whererun.Detect(), activeHostGit())
}

func ambientAvailable(d whererun.Detection, hg *HostGitClient) error {
	if d.Host {
		return nil
	}
	if hg != nil {
		if err := hg.available(); err != nil {
			return fmt.Errorf("an ambient git credential uses this host's own gh (or a registered source's tool) login, which this daemon, "+
				"in a container (%s), reaches through grain hostd's %s capability server, and that is not "+
				"available: %v", d.Signal, HostGitEntry, err)
		}
		return nil
	}
	return fmt.Errorf("an ambient git credential uses this host's own gh or git login, so it is only "+
		"available when the daemon runs on the bare machine, or beside grain hostd; this daemon runs in a "+
		"container (%s) with no grain hostd, where there is no login of the operator's to use -- use a "+
		"fine-grained personal access token or a GitHub App instead, or install hostd (GRAIN_HOSTD=1)", d.Signal)
}

// NormalizeAmbientSource is source as stored: "" is AmbientSourceAuto.
func NormalizeAmbientSource(source string) (string, error) {
	switch s := strings.ToLower(strings.TrimSpace(source)); s {
	case "":
		return AmbientSourceAuto, nil
	case AmbientSourceGit, AmbientSourceAuto:
		return s, nil
	default:
		if _, ok := ambientCommandSource(s); ok {
			return s, nil
		}
		return "", fmt.Errorf("unknown ambient source %q: %s", source, strings.Join(AmbientSourceNames(), ", "))
	}
}

// AmbientSourceNames is every source an ambient file may name.
func AmbientSourceNames() []string {
	names := []string{}
	for _, src := range ambientCommandSources() {
		names = append(names, src.Name)
	}
	return append(names, AmbientSourceGit, AmbientSourceAuto)
}

func (c *CredentialSet) ambientFile(name string) string {
	return filepath.Join(c.dir, name+".ambient.json")
}

// PrimaryAmbientCredential returns the name of an ambient credential in c
// when one exists.
func (c *CredentialSet) PrimaryAmbientCredential() string {
	if c == nil || c.dir == "" {
		return ""
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if name, ok := strings.CutSuffix(e.Name(), ".ambient.json"); ok && name != "" {
			if c.IsAmbient(name) {
				return name
			}
		}
	}
	return ""
}

// IsAmbient reports whether name is backed by a <name>.ambient.json file.
func (c *CredentialSet) IsAmbient(name string) bool {
	if namesOneFile(name) != nil {
		return false
	}
	_, err := os.Stat(c.ambientFile(name))
	return err == nil
}

// AmbientOf is name's ambient file, when it is one.
func (c *CredentialSet) AmbientOf(name string) (AmbientFile, bool) {
	f, ok, err := c.loadAmbientFile(name)
	if !ok || err != nil {
		return AmbientFile{}, false
	}
	return f, true
}

func (c *CredentialSet) loadAmbientFile(name string) (AmbientFile, bool, error) {
	data, err := os.ReadFile(c.ambientFile(name))
	if os.IsNotExist(err) {
		return AmbientFile{}, false, nil
	}
	if err != nil {
		return AmbientFile{}, true, err
	}
	var f AmbientFile
	if err := json.Unmarshal(data, &f); err != nil {
		return AmbientFile{}, true, fmt.Errorf("%s.ambient.json is not valid JSON: %v", name, err)
	}
	if f.Source, err = NormalizeAmbientSource(f.Source); err != nil {
		return AmbientFile{}, true, err
	}
	if f.Host = strings.TrimSpace(f.Host); f.Host == "" {
		f.Host = model.DefaultGitHost
	}
	return f, true, nil
}

// SetAmbient writes name as an ambient credential for host, asking
// source, replacing any token, App or SSH key of the same name. It is
// refused where AmbientAvailable is: the guard is here, and not only in
// the UI, so nothing that writes credentials can create one in a
// container.
func (c *CredentialSet) SetAmbient(name, host, source string) error {
	return c.SetAmbientOn(name, CredentialHost{Host: host, Kind: model.GitHostGitHub}, source)
}

// SetAmbientOn is SetAmbient on a host of either kind. On a plain git
// host (model.GitHostGit) the only tool that can know a login is git's
// own credential helper, so source must be git (auto is stored as git):
// gh holds GitHub logins only.
func (c *CredentialSet) SetAmbientOn(name string, h CredentialHost, source string) error {
	if err := ValidCredentialName(name); err != nil {
		return err
	}
	if err := AmbientAvailable(); err != nil {
		return err
	}
	source, err := NormalizeAmbientSource(source)
	if err != nil {
		return err
	}
	if strings.TrimSpace(h.Host) == "" {
		h.Host = model.DefaultGitHost
	}
	if h.Kind == "" {
		h.Kind = model.GitHostGitHub
	}
	h, err = NormalizeCredentialHost(h)
	if err != nil {
		return err
	}
	// Before anything is written: SetHost would refuse it too, but only
	// after the ambient file was.
	if err := h.CheckTransport(); err != nil {
		return err
	}
	if err := ambientSourceAllowed(h.Host, source); err != nil {
		return err
	}
	if h.Kind == model.GitHostGit {
		switch source {
		case AmbientSourceGit:
		case AmbientSourceAuto:
			source = AmbientSourceGit
		default:
			return fmt.Errorf("%s is a plain git host, and %s holds GitHub logins only: use source git "+
				"(`git credential fill`) for it", h.Host, source)
		}
	}
	data, err := json.MarshalIndent(AmbientFile{Host: h.Host, Source: source}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(c.dir, name+".ambient.json", append(data, '\n')); err != nil {
		return fmt.Errorf("writing credential %q: %w", name, err)
	}
	if err := c.removeKinds(name, ".token", ".app.json", ".ssh.json"); err != nil {
		return err
	}
	if err := c.SetHost(name, h); err != nil {
		return err
	}
	c.forget(name)
	return nil
}

// Refused tells the set that cred's token was refused (a 401): an
// ambient credential's cached token is dropped so the next request asks
// the host's tools again and picks up a re-login. Nothing else is
// affected -- a stored token does not get better by being re-read.
func (c *CredentialSet) Refused(cred Credential) {
	if cred.Ambient == nil {
		return
	}
	log.Printf("gitproxy: ambient credential %q was refused (401): asking %s again on its next use",
		cred.Name, cred.Ambient.From)
	c.forget(cred.Name)
}

// RefusedToken is Refused for a caller that holds only the token it sent
// (the REST client): every ambient credential currently serving that
// token is dropped.
func (c *CredentialSet) RefusedToken(token string) {
	if token == "" {
		return
	}
	var names []string
	c.mu.Lock()
	for _, entry := range c.cache {
		if entry.cred.Ambient != nil && entry.cred.Token != nil && *entry.cred.Token == token {
			names = append(names, entry.cred.Name)
		}
	}
	c.mu.Unlock()
	for _, name := range names {
		c.Refused(Credential{Name: name, Ambient: &AmbientCredential{From: "the host's login"}})
	}
}

// ambientFlight is one in-progress ambient token fetch, which every
// load of the same cache key that arrives while it runs waits on instead
// of asking the host's tools again.
type ambientFlight struct {
	done chan struct{}
	cred Credential
}

// loadAmbientLocked is load's ambient branch. Callers hold mu, and get it
// back held -- but it is released across the fetch itself, which can take
// up to ambientCommandTimeout (and longer for a helper that hangs worse
// than that, before runHostTool's bound), so that one credential whose
// host tool is stuck holds up nothing but loads of that same credential.
func (c *CredentialSet) loadAmbientLocked(name, key string, f AmbientFile, fileErr error, now time.Time) Credential {
	amb := &AmbientCredential{Host: f.Host, Source: f.Source}
	fail := func(msg string) Credential {
		cred := Credential{Name: name, Ambient: amb, Unusable: msg}
		c.cache[key] = cacheEntry{cred: cred, expiresAt: now.Add(ambientFailureRetryDelay)}
		return cred
	}
	if fileErr != nil {
		return fail(fmt.Sprintf("ambient credential %q cannot be used: %v", name, fileErr))
	}
	// The proxy and the REST client route by the host record, while the
	// token is asked for the file's host: two that disagree would send
	// one host's login to the other, so neither is believed.
	if h := c.HostOf(name); !model.SameGitHost(h.Host, f.Host, model.DefaultGitHost) {
		log.Printf("gitproxy: ambient credential %q is for %s but recorded on %s: refusing it", name, f.Host, h.Host)
		return fail(fmt.Sprintf("ambient credential %q cannot be used: it is for %s, but its host record says %s; "+
			"set it up again as an ambient credential", name, f.Host, h.Host))
	}
	if err := AmbientAvailable(); err != nil {
		return fail(fmt.Sprintf("ambient credential %q cannot be used: %v", name, err))
	}
	if fl, ok := c.flights[key]; ok {
		c.mu.Unlock()
		<-fl.done
		c.mu.Lock()
		return fl.cred
	}
	if c.flights == nil {
		c.flights = map[string]*ambientFlight{}
	}
	fl := &ambientFlight{done: make(chan struct{})}
	c.flights[key] = fl
	gen := c.generations[name]
	c.mu.Unlock()

	start := time.Now()
	token, from, user, err := ambientTokenVia(f.Host, c.HostOf(name).BaseURL, f.Source)
	took := time.Since(start).Round(time.Millisecond)
	var entry cacheEntry
	if err != nil {
		log.Printf("gitproxy: ambient credential %q: no token after %s: %v", name, took, err)
		entry = cacheEntry{cred: Credential{Name: name, Ambient: amb,
			Unusable: fmt.Sprintf("ambient credential %q has no token: %v", name, err)},
			expiresAt: now.Add(ambientFailureRetryDelay)}
	} else {
		amb.From, amb.User = from, user
		// The token itself is never logged; which source answered is.
		log.Printf("gitproxy: ambient credential %q: token for %s fetched from %s in %s", name, f.Host, from, took)
		entry = cacheEntry{cred: Credential{Name: name, Token: &token, Ambient: amb}, expiresAt: now.Add(ambientTokenTTL)}
	}

	c.mu.Lock()
	// A forget while this ran (SetAmbient, Remove, a 401) means the
	// answer is of a credential that has since changed: the waiters
	// already here get it, but it is not cached for anyone later.
	if c.generations[name] == gen {
		c.cache[key] = entry
	}
	if c.flights[key] == fl {
		delete(c.flights, key)
	}
	fl.cred = entry.cred
	close(fl.done)
	return entry.cred
}

// AmbientToken asks this host's own tools for a token for host: gh, git,
// or gh then git (AmbientSourceAuto). The error is the sentence to fix
// it by, naming the account and the host to run the fix as and on.
func AmbientToken(host, source string) (token, from string, err error) {
	token, from, _, err = ambientToken(host, source)
	return token, from, err
}

// AmbientLogin is AmbientToken for a host reached on baseURL (a host
// record's BaseURL), with the username `git credential fill` answered
// with beside the token when git is the source that answered: the whole
// login a plain git host behind basic auth checks.
func AmbientLogin(host, baseURL, source string) (token, user string, err error) {
	token, _, user, err = ambientTokenVia(host, baseURL, source)
	return token, user, err
}

// ambientToken is AmbientToken with the username `git credential fill`
// answered with, when git is the source that answered.
func ambientToken(host, source string) (token, from, user string, err error) {
	return ambientTokenVia(host, "", source)
}

// ambientTokenVia is ambientToken for a host reached on baseURL (a host
// record's BaseURL; empty is https on host itself). `git credential fill`
// is asked for the protocol and host[:port] git itself asks for when it
// clones from there, which is what a helper's stored login is keyed by:
// a login for http://git.example:8080 is never handed out for
// https://git.example. gh is asked by hostname alone, as gh keys them.
func ambientTokenVia(host, baseURL, source string) (token, from, user string, err error) {
	source, err = NormalizeAmbientSource(source)
	if err != nil {
		return "", "", "", err
	}
	protocol, target := "https", strings.TrimSpace(host)
	if u, perr := url.Parse(baseURL); perr == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") {
		protocol, target = u.Scheme, u.Host
	}
	host = hostName(host)
	if host == "" {
		host = model.DefaultGitHost
	}
	if target == "" {
		target = host
	}
	sources := []string{source}
	if source == AmbientSourceAuto {
		sources = ambientAutoSources(host)
	} else if err := ambientSourceAllowed(host, source); err != nil {
		return "", "", "", err
	}
	var errs []string
	for _, name := range sources {
		if name == AmbientSourceGit {
			token, user, err := gitCredentialToken(protocol, target)
			if err == nil {
				return token, AmbientSourceGit, user, nil
			}
			errs = append(errs, err.Error())
			continue
		}
		src, _ := ambientCommandSource(name)
		token, err := commandToken(src, host)
		if err == nil {
			return token, name, "", nil
		}
		errs = append(errs, err.Error())
	}
	return "", "", "", errors.New(strings.Join(errs, "; and "))
}

var (
	ambientHomeDirsMu sync.RWMutex
	ambientHomeDirs   []string

	userCurrent = user.Current
)

// RegisterAmbientHomeDir adds dir as a parent directory of per-user
// local-disk homes (<dir>/<username>) that AmbientHome prefers over the
// passwd entry's HomeDir when <dir>/<username> is a directory owned by the
// daemon's own uid -- for workstations whose NSS/LDAP passwd entry names an
// autofs home that hangs on non-interactive access. It returns a function
// that removes the registration (for tests).
func RegisterAmbientHomeDir(dir string) (unregister func()) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		panic("gitproxy: RegisterAmbientHomeDir needs a directory")
	}
	ambientHomeDirsMu.Lock()
	defer ambientHomeDirsMu.Unlock()
	ambientHomeDirs = append(ambientHomeDirs, dir)
	return func() {
		ambientHomeDirsMu.Lock()
		defer ambientHomeDirsMu.Unlock()
		for i, have := range ambientHomeDirs {
			if have == dir {
				ambientHomeDirs = append(ambientHomeDirs[:i:i], ambientHomeDirs[i+1:]...)
				return
			}
		}
	}
}

// ambientAccount is "<user> on <hostname>", for the sentence that says
// where to log in.
// AmbientAccount is the account host tools run as, and the host, as a
// refusal names them ("alice on build-1"): who to run a fix as.
func AmbientAccount() string { return ambientAccount() }

func ambientAccount() string {
	name := os.Getenv("USER")
	if u, err := userCurrent(); err == nil && u.Username != "" {
		name = u.Username
	}
	if name == "" {
		name = "the daemon's account"
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "this host"
	}
	return name + " on " + host
}

// AmbientHome is the account's real home directory: where its gh login
// (.config/gh/hosts.yml) and git config live.
func AmbientHome() string { return ambientHome() }

// ambientHome is the account's real home directory: where its gh login
// (.config/gh/hosts.yml) and git config live. A host-mode unit points the
// daemon's HOME into its data directory (scripts/setup.sh), where no gh
// login lives; the account's own login is in the home its passwd entry
// names.
//
// When a workstation environment registers a local-disk home parent
// (RegisterAmbientHomeDir), <dir>/<username> is preferred over the passwd
// HomeDir (which on NSS/LDAP workstations may name an autofs mount that
// hangs on non-interactive access) when it is a directory owned by the
// daemon's own uid: only the passwd username names it, never $USER or $HOME,
// and a directory somebody else owns is not this account's home whatever it
// is called.
func ambientHome() string {
	if h := os.Getenv("GRAIN_TEST_AMBIENT_HOME"); h != "" {
		return h
	}
	u, err := userCurrent()
	if err != nil {
		return ""
	}
	if u.Username != "" && !strings.ContainsAny(u.Username, "/\\") && u.Username != "." && u.Username != ".." {
		ambientHomeDirsMu.RLock()
		dirs := append([]string(nil), ambientHomeDirs...)
		ambientHomeDirsMu.RUnlock()
		for _, root := range dirs {
			local := filepath.Join(root, u.Username)
			if fi, err := os.Stat(local); err == nil && fi.IsDir() && ownedByUs(fi) {
				return local
			}
		}
	}
	return u.HomeDir
}

// ambientEnv is the environment the host's tools run with: the daemon's
// own, with HOME the account's real home directory (ambientHome).
func ambientEnv() []string {
	env := os.Environ()
	home := ambientHome()
	if home == "" {
		return env
	}
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "HOME=") {
			out = append(out, kv)
		}
	}
	return append(out, "HOME="+home)
}

// errHostToolTimeout marks runHostTool's answer for a command that did
// not finish within ambientCommandTimeout, so the callers below say so
// rather than blaming the login.
var errHostToolTimeout = errors.New("host tool timed out")

// runHostTool runs one of this host's own tools (gh, git) for an ambient
// token and returns what it printed, bounded for real.
// exec.CommandContext's deadline alone is not a bound: it kills only the
// direct child, and Wait then waits for every process that inherited its
// stdout or stderr -- a git-credential-cache daemon, a keyring agent,
// credential renewal or GCM helpers -- which can be forever. So the command runs in a
// process group of its own that the deadline kills whole
// (procgroup.Prepare), its stdin is /dev/null unless stdin is given, and
// WaitDelay stops waiting on the output pipes ambientWaitDelay after the
// command exits or is killed, whichever comes first -- the case of a
// helper that daemonized out of the group and kept them open.
//
// Every run is logged with how long it took and how it ended, never with
// what it printed: a slow helper then shows up in the daemon's journal.
func runHostTool(env []string, stdin string, path string, args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), ambientCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	procgroup.Prepare(cmd)
	cmd.WaitDelay = ambientWaitDelay
	line := hostToolCommandLine(path, args)
	start := time.Now()
	err = cmd.Run()
	took := time.Since(start).Round(time.Millisecond)
	switch {
	case ctx.Err() != nil:
		log.Printf("gitproxy: ambient: `%s` did not finish within %s: killed after %s", line, ambientCommandTimeout, took)
		return "", "", fmt.Errorf("%w: `%s` did not finish within %s, so it was killed: run `%s` by hand as %s "+
			"to see what it waits on (a credential helper prompting, or one that never answers)",
			errHostToolTimeout, line, ambientCommandTimeout, line, ambientAccount())
	case errors.Is(err, exec.ErrWaitDelay):
		// The command itself exited 0; something it started kept its
		// output open. What it printed before exiting is its answer.
		log.Printf("gitproxy: ambient: `%s` succeeded in %s (a process it started kept its output open)", line, took)
		err = nil
	case err != nil:
		// A tool the host would not start for want of its own
		// credentials is said as the fix authfix knows for it.
		err = authfix.Wrap(err, errOut.String(), path)
		log.Printf("gitproxy: ambient: `%s` failed in %s: %v", line, took, err)
	default:
		log.Printf("gitproxy: ambient: `%s` succeeded in %s", line, took)
	}
	return out.String(), errOut.String(), err
}

// hostToolCommandLine is path and args as the command to type by hand.
func hostToolCommandLine(path string, args []string) string {
	return strings.Join(append([]string{filepath.Base(path)}, args...), " ")
}

// commandToken runs src for a token for host.
func commandToken(src AmbientCommandSource, host string) (string, error) {
	if c := activeHostGit(); c != nil {
		return c.token(src.Name, host)
	}
	fix := expandHost(src.LoginFix, host)
	path, err := exec.LookPath(src.Command)
	if err != nil {
		return "", fmt.Errorf("%s is not installed for %s: install it and run `%s`", src.Command, ambientAccount(), fix)
	}
	env := ambientEnv()
	for _, kv := range src.Env {
		env = append(env, expandHost(kv, host))
	}
	args := make([]string, len(src.Args))
	for i, a := range src.Args {
		args[i] = expandHost(a, host)
	}
	stdout, stderr, err := runHostTool(env, "", path, args...)
	if errors.Is(err, errHostToolTimeout) {
		return "", err
	}
	if _, expired := authfix.As(err); expired {
		// Not a login the tool's own fix would repair: the kernel
		// would not start it at all until its login has been renewed.
		return "", fmt.Errorf("%s token for %s: %w", src.Command, host, err)
	}
	if err != nil {
		return "", fmt.Errorf("%s is not logged in to %s (%s): run `%s` as %s",
			src.Command, host, shortMessage(strings.TrimSpace(stderr)+" "+err.Error()), fix, ambientAccount())
	}
	token := strings.TrimSpace(stdout)
	if token == "" || strings.ContainsAny(token, " \n\t") {
		return "", fmt.Errorf("%s printed no token for %s: run `%s` as %s", src.Command, host, fix, ambientAccount())
	}
	return token, nil
}

func gitCredentialToken(protocol, host string) (password, username string, err error) {
	if activeHostGit() != nil {
		// Through hostd only a named source's own command runs
		// (hostgit.go): git credential fill would hand the container
		// every login the account's credential helpers hold.
		return "", "", fmt.Errorf("git credential fill is read only by a daemon on the bare machine; this one reaches "+
			"the host's login through grain hostd, which runs gh (or a registered source's tool) for it -- use one of those sources for %s", host)
	}
	path, err := exec.LookPath("git")
	if err != nil {
		return "", "", fmt.Errorf("git is not installed for %s", ambientAccount())
	}
	stdout, _, runErr := runHostTool(
		append(ambientEnv(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_ASKPASS=", "SSH_ASKPASS="),
		"protocol="+protocol+"\nhost="+host+"\n\n", path, "credential", "fill")
	if errors.Is(runErr, errHostToolTimeout) {
		return "", "", runErr
	}
	for _, line := range strings.Split(stdout, "\n") {
		if v, ok := strings.CutPrefix(line, "password="); ok {
			password = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(line, "username="); ok {
			username = strings.TrimSpace(v)
		}
	}
	if runErr != nil || password == "" {
		return "", "", fmt.Errorf("git credential fill returned no password for %s://%s: configure a credential "+
			"helper with a login for %s as %s (for example `gh auth setup-git`)", protocol, host, host, ambientAccount())
	}
	return password, username, nil
}

// AmbientIdentity is who an ambient credential's token acts as, and
// whether it carries the `repo` and `workflow` scopes: GitHub's own /user lookup with the
// token load would send, so it is the answer for exactly the login a
// push would use. scopes is nil for a token GitHub reports none for (a
// fine-grained or App user token).
type AmbientIdentity struct {
	Login  string
	Scopes []string
	// NoRepoScope is set for a classic OAuth token without `repo`, which
	// can read public repositories and push nothing.
	NoRepoScope bool
	// NoWorkflowScope is set for a classic OAuth token without
	// `workflow`, whose host refuses a push that creates or changes a
	// file under .github/workflows. A warning rather than a fault: work
	// that never edits a workflow does not need the scope.
	NoWorkflowScope bool
	// ID and Name are the account's numeric id and display name, as
	// /user reports them: what NoreplyEmail and AmbientGitIdentity
	// (ambient_identity.go) build a commit identity for the login out
	// of. Zero and empty on a plain git host, which has no /user.
	ID   int64
	Name string
}

// NoreplyEmail is the address GitHub itself attributes to this login's
// commits when the account keeps its address private --
// <id>+<login>@users.noreply.<host> -- and so one GitHub matches to the
// account exactly as it does a verified address (and a CLA or DCO check
// that looks the author up finds the person, not nobody). Empty for an
// identity /user did not answer for.
func (id AmbientIdentity) NoreplyEmail(host string) string {
	if id.ID == 0 || id.Login == "" {
		return ""
	}
	h := hostName(host)
	if h == "" {
		h = model.DefaultGitHost
	}
	return fmt.Sprintf("%d+%s@users.noreply.%s", id.ID, id.Login, h)
}

// checkAmbientLogin is AmbientIdentityOf asked afresh, past any answer
// cached for the same token: whether the host's login is good right now.
func (c *CredentialSet) checkAmbientLogin(name string) (AmbientIdentity, error) {
	c.mu.Lock()
	delete(c.identities, name)
	c.mu.Unlock()
	return c.AmbientIdentityOf(name)
}

// AmbientIdentityOf resolves name the way a request would and asks GitHub
// who that token is.
func (c *CredentialSet) AmbientIdentityOf(name string) (AmbientIdentity, error) {
	if !c.IsAmbient(name) {
		return AmbientIdentity{}, fmt.Errorf("%q is not an ambient credential", name)
	}
	cred := c.load(name, "")
	if cred.Unusable != "" {
		return AmbientIdentity{}, errors.New(cred.Unusable)
	}
	if cred.Token == nil {
		return AmbientIdentity{}, fmt.Errorf("ambient credential %q has no token", name)
	}
	if c.HostOf(name).Kind == model.GitHostGit {
		// A plain git host has no API to ask who a token is: the
		// credential helper's own username is the whole answer.
		return AmbientIdentity{Login: cred.Ambient.User}, nil
	}
	c.mu.Lock()
	cached, ok := c.identities[name]
	c.mu.Unlock()
	if ok && cached.token == *cred.Token {
		return cached.id, nil
	}
	transport, err := c.ambientTransportFor(name, cred.Ambient.Host)
	if err != nil {
		return AmbientIdentity{}, err
	}
	resp, err := transport.Request("GET", "/user", map[string]string{
		"Accept":        "application/vnd.github+json",
		"User-Agent":    "grain-automation",
		"Authorization": "token " + *cred.Token,
	}, nil)
	if err != nil {
		if login, ok := AsLoginRedirect(err); ok {
			return AmbientIdentity{}, login
		}
		return AmbientIdentity{}, fmt.Errorf("asking %s who the ambient login is: %s", cred.Ambient.Host, shortMessage(err.Error()))
	}
	if resp.Status == http.StatusUnauthorized {
		c.Refused(cred)
		return AmbientIdentity{}, fmt.Errorf("%s refused the token %s handed out (401): run `%s` as %s",
			cred.Ambient.Host, cred.Ambient.From, AmbientLoginFix(cred.Ambient.Host, cred.Ambient.From), ambientAccount())
	}
	if resp.Status != http.StatusOK {
		return AmbientIdentity{}, fmt.Errorf("asking %s who the ambient login is: HTTP %d", cred.Ambient.Host, resp.Status)
	}
	var who struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(resp.Body, &who); err != nil {
		return AmbientIdentity{}, fmt.Errorf("reading %s's /user answer: %v", cred.Ambient.Host, err)
	}
	out := AmbientIdentity{Login: who.Login, ID: who.ID, Name: strings.TrimSpace(who.Name)}
	if raw, ok := headerValue(resp.Headers, "X-OAuth-Scopes"); ok {
		out.NoRepoScope, out.NoWorkflowScope = true, true
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out.Scopes = append(out.Scopes, s)
				switch s {
				case "repo":
					out.NoRepoScope = false
				case "workflow":
					out.NoWorkflowScope = false
				}
			}
		}
	}
	c.mu.Lock()
	if c.identities == nil {
		c.identities = map[string]ambientIdentityEntry{}
	}
	c.identities[name] = ambientIdentityEntry{token: *cred.Token, id: out}
	c.mu.Unlock()
	return out, nil
}

// ambientTransportFor is appTransportFor for an ambient credential: the
// API of host, the one its file asked for a token for, and never of
// whatever the host record says (load refuses the two disagreeing, and
// this does not lean on that). The record's BaseURL, always on its own
// host (NormalizeCredentialHost), is kept only while that is host.
func (c *CredentialSet) ambientTransportFor(name, host string) (github.Transport, error) {
	if c.AppTransport != nil {
		return c.AppTransport, nil
	}
	rec := c.HostOf(name)
	h := CredentialHost{Host: host, Kind: rec.Kind}
	if model.SameGitHost(rec.Host, host, model.DefaultGitHost) {
		h.BaseURL, h.Proxy = rec.BaseURL, rec.Proxy
	}
	apiHost, prefix, ok := h.APIBase()
	if !ok {
		return nil, fmt.Errorf("credential %q is on %s, a plain git host with no API", name, host)
	}
	transport := github.NewRealTransport(apiHost)
	transport.Host = apiHost
	transport.PathPrefix = prefix
	transport.UseTLS = h.useTLS()
	transport.Client = h.HTTPClient(appMintTimeout)
	if src, _, ok := c.forgeCommand(name); ok {
		// Behind the front end only the tool gets through: the same
		// API, asked by `<tool> api` (commandforge.go).
		transport.Client = commandForgeClient(src, h, appMintTimeout)
	}
	return transport, nil
}

// identityFlight is one AmbientIdentityOf run started by
// AmbientIdentityWithin, which later callers for the same name join.
type identityFlight struct {
	done chan struct{}
	id   AmbientIdentity
	err  error
}

// AmbientIdentityWithin is AmbientIdentityOf for a caller that answers a
// person -- a settings pane, a Save -- and so must not sit on a host tool
// that is slow or stuck: it waits at most wait, and otherwise reports
// pending while the lookup goes on in the background, so that asking
// again later (the pane polls) finds it done, or joins it if it is not.
// The lookup itself stays bounded by ambientCommandTimeout and
// appMintTimeout.
func (c *CredentialSet) AmbientIdentityWithin(name string, wait time.Duration) (id AmbientIdentity, pending bool, err error) {
	c.mu.Lock()
	fl, ok := c.identityFlights[name]
	if !ok {
		if c.identityFlights == nil {
			c.identityFlights = map[string]*identityFlight{}
		}
		fl = &identityFlight{done: make(chan struct{})}
		c.identityFlights[name] = fl
		go func() {
			id, err := c.AmbientIdentityOf(name)
			c.mu.Lock()
			if c.identityFlights[name] == fl {
				delete(c.identityFlights, name)
			}
			fl.id, fl.err = id, err
			c.mu.Unlock()
			close(fl.done)
		}()
	}
	c.mu.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-fl.done:
		return fl.id, false, fl.err
	case <-timer.C:
		return AmbientIdentity{}, true, nil
	}
}

// ambientIdentityEntry is AmbientIdentityOf's answer for one token, kept
// in memory beside load's cache so a settings pane read does not ask
// GitHub again until the host's login hands out a different token.
type ambientIdentityEntry struct {
	token string
	id    AmbientIdentity
}

func headerValue(headers map[string]string, name string) (string, bool) {
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}
