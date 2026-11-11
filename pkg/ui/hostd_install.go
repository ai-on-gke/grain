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

package ui

import (
	"context"
	_ "embed"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/agent/claude"
	"github.com/ai-on-gke/grain/pkg/agent/codex"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/version"
)

// hostdInstallPath is the bootstrap script a machine registering as a
// remote grain hostd runs; hostdInstallShortPath is the shorter URL shown
// in Settings -> Hosts -> Invite hosts.
const (
	hostdInstallPath      = "/api/hostd/install.sh"
	hostdInstallShortPath = "/hostd/install"
)

//go:embed hostd_install.sh
var hostdInstallScript string

// The placeholders hostd_install.sh carries for what only the daemon
// serving it knows.
const (
	hostdInstallURLMark     = "@GRAIN_DAEMON_URL@"
	hostdInstallReleaseMark = "@GRAIN_DAEMON_RELEASE@"
	hostdInstallImageMark   = "@GRAIN_DAEMON_IMAGE@"
	hostdAgentdTemplateMark = "@GRAIN_AGENTD_TEMPLATE@"
)

// Each value is written into the script between single quotes and from
// there into a systemd unit, so each is held to characters neither can
// mistake for anything else; one that does not match is left out, and
// the script asks for it instead (--server, --image).
var (
	hostdInstallHostRE    = regexp.MustCompile(`^[A-Za-z0-9.\-]+(:[0-9]{1,5})?$|^\[[0-9A-Fa-f:.]+\](:[0-9]{1,5})?$`)
	hostdInstallReleaseRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+\-]{0,127}$`)
	hostdInstallImageRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@\-]{0,254}$`)
)

// hostdInstallRoute reports whether r asks for the install script. It
// is grain's own script and the URL it was asked for at -- never a token
// or the access code -- so like a hostd dialing in (hostdConnectRoute)
// it does not ask who the caller is: the machine fetching it has nothing
// to sign in with until it holds its hostd token. What only a caller the
// daemon trusts may learn, its release and the image it runs, is filled
// in for such a caller alone (hostdInstallCallerHasAccess); Settings ->
// Hostds hands them to everybody else's copy as --release and --image.
func hostdInstallRoute(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return r.URL.Path == hostdInstallPath || r.URL.Path == hostdInstallShortPath
}

// handleHostdInstallScript serves hostd_install.sh with this daemon's
// URL, as the caller reached it, filled in for the hostd to dial, and
// for a caller with access the daemon's release and image too. When the
// caller asks for the agentd allowlist template (?template=agentd or
// Accept: application/json), it serves the deployment's CLI allowlist
// template as JSON data instead.
func (s *Server) handleHostdInstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	hasAccess := s.hostdInstallCallerHasAccess(r)
	origin := hostdInstallOrigin(r)
	tmpl := s.agentdAllowlistTemplate(r.Context(), origin, hasAccess)
	if wantsAgentdAllowlistTemplate(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if b, err := json.Marshal(tmpl); err == nil {
			_, _ = w.Write(append(b, '\n'))
		}
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	release, image := "", ""
	if hasAccess {
		release, image = version.Get().Release, s.hostdInstallImage()
	}
	_, _ = w.Write([]byte(renderHostdInstallScriptWithTemplate(hostdInstallDaemonURL(r), release, image, tmpl)))
}

func wantsAgentdAllowlistTemplate(r *http.Request) bool {
	q := r.URL.Query()
	if q.Get(hostd.InstallTemplateQuery) == hostd.InstallTemplateAgentd ||
		q.Get("agentd") == "template" ||
		q.Get("agentd-template") == "1" {
		return true
	}
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		media, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(media), "application/json") {
			return true
		}
	}
	return false
}

// RegisterHostCLICandidates prepends candidate binary paths for CLI name in
// the allowlist template served by /hostd/install.
func RegisterHostCLICandidates(name string, paths ...string) (unregister func()) {
	return hostd.RegisterCLICandidates(name, paths...)
}

// RegisterHostCLITemplate adds or overrides an agent CLI entry in the
// allowlist template served by /hostd/install.
func RegisterHostCLITemplate(entry hostd.CLITemplateEntry) (unregister func()) {
	return hostd.RegisterCLITemplate(entry)
}

// hostdInstallCallerHasAccess reports whether r is a caller the access
// gate would have let through to a route that is not open: one carrying
// the access code or its session, an in-process one (singleTenantTrusted),
// or a verified identity the daemon admits. A stranger is not told the
// daemon's release or the image it runs, which can name a private
// registry, by a route that answers anybody.
//
// Because singleTenantOpenRoute lets this route past singleTenantGate
// before bearer guesses are checked, a bearer token shaped like an access
// code is held to s.access here too, so wrong codes count and a limited
// source cannot tell the right code from a wrong one by whether the script
// carries the release and image.
func (s *Server) hostdInstallCallerHasAccess(r *http.Request) bool {
	cfg, _ := IAPConfigFromContext(r.Context())
	if tok, ok := bearerToken(r); ok && accessCodeAccepted(r, cfg) && couldBeAccessCode(tok, cfg.LocalAPIToken) {
		now := time.Now()
		source := accessSource(r)
		if !accessLimitExempt(r) && s.access.sourceLimited(source, now) {
			return false
		}
		if !presentsAccessCode(r, cfg) {
			s.access.failed(source, now)
			return false
		}
		return true
	}
	if singleTenantTrusted(r) {
		return true
	}
	cv := cachedOrVerifyCaller(r)
	return cv.present && cv.err == nil && s.tasks != nil && s.checkAllowedPrincipal(r.Context(), cv.id) == nil
}

// HostdInstall is what the install command Settings -> Hostds shows
// passes the script (--release, --image), since the script itself tells
// a caller without access neither.
//
// Lines and Note are the instructions themselves, rendered by the daemon
// (RegisterHostdInstall) so every build's text comes from Go; Release
// and Image stay for a client that still assembles the command itself.
type HostdInstall struct {
	// Release is the daemon's own build, the grain the script installs;
	// "" for an unstamped build, which takes any grain (--any-version).
	Release string `json:"release"`
	// Image is the image the daemon runs, to take that grain out of; ""
	// when the deployment cannot say.
	Image string `json:"image,omitempty"`
	// Lines are the shell lines that install the hostd on its machine, in
	// order, shown as one block to copy. They never carry the token: the
	// installer asks for it, or reads $GRAIN_HOSTD_TOKEN. Empty when the
	// daemon could not render them (an older daemon sends none either),
	// and the client then assembles the root installer's line itself.
	Lines []string `json:"lines,omitempty"`
	// Note is a short line shown beside Lines, such as who to run them
	// as; "" for none.
	Note string `json:"note,omitempty"`
}

// HostdInstallContext is what a HostdInstallProvider is told about the
// hostd whose install instructions it writes.
type HostdInstallContext struct {
	// Origin is this daemon's URL as the caller reached it ("https://host"
	// or "http://host:port", no path), or "" when that is not one a shell
	// line could safely carry.
	Origin string
	// Name is the hostd's registry name (model.ValidateHostdName).
	Name string
	// Owner is the hostd's owner, the identity it dials in as; "" for
	// none.
	Owner string
	// Release is the daemon's own build; "" for an unstamped one.
	Release string
	// Image is the image the daemon runs; "" when it cannot say.
	Image string
	// JoinCode is set when rendering the shared "Invite hosts" enrollment
	// snippet (--join <code>) for the deployment owner; "" for per-host
	// install instructions.
	JoinCode string
	// Features are the contract features the host advertised when already
	// connected; empty when the host has not connected yet.
	Features []hostd.Feature
}

// HasFeature reports whether c's connected host advertised f.
func (c HostdInstallContext) HasFeature(f hostd.Feature) bool {
	return slices.Contains(c.Features, f)
}

// HostdInstallInstructions is what Settings -> Hostds and `grain hostds`
// show for installing a hostd: shell lines in order, and an optional
// short note. They must never carry the hostd's token, which a provider
// is not given: an installer prompts for it or reads $GRAIN_HOSTD_TOKEN,
// and the note may say so ("prefix with GRAIN_HOSTD_TOKEN=<token>, or
// paste it when asked").
type HostdInstallInstructions struct {
	Lines []string
	Note  string
}

// HostdInstallProvider writes the install instructions for a hostd, in
// place of the root installer's line (defaultHostdInstall), for a build
// that installs hostds some other way.
type HostdInstallProvider func(HostdInstallContext) HostdInstallInstructions

var (
	hostdInstallProviderMu sync.RWMutex
	hostdInstallProvider   HostdInstallProvider
)

// RegisterHostdInstall makes f the writer of the install instructions
// Settings -> Hostds and `grain hostds` show for a hostd, in place of the
// default sudo line running /api/hostd/install.sh, and returns a function
// that puts back what was there before (for tests). nil puts back the
// default.
func RegisterHostdInstall(f HostdInstallProvider) (unregister func()) {
	hostdInstallProviderMu.Lock()
	defer hostdInstallProviderMu.Unlock()
	prev := hostdInstallProvider
	hostdInstallProvider = f
	return func() {
		hostdInstallProviderMu.Lock()
		defer hostdInstallProviderMu.Unlock()
		hostdInstallProvider = prev
	}
}

func registeredHostdInstall() HostdInstallProvider {
	hostdInstallProviderMu.RLock()
	defer hostdInstallProviderMu.RUnlock()
	return hostdInstallProvider
}

// defaultHostdInstall is the one line that installs the hostd from this
// daemon's /api/hostd/install.sh as root, the same line Settings -> Hostds
// assembled itself before the daemon rendered it (installCommand in
// HostdsSection.jsx). It has no lines without an origin to fetch from.
func defaultHostdInstall(c HostdInstallContext) HostdInstallInstructions {
	if c.Origin == "" || c.Name == "" {
		return HostdInstallInstructions{}
	}
	cmd := `sudo sh -c "$(curl -fsSL '` + c.Origin + hostdInstallPath + `')" grain-hostd-install --name ` + c.Name
	if c.Release != "" {
		cmd += " --release " + c.Release
	} else {
		cmd += " --any-version"
	}
	if c.Image != "" {
		cmd += " --image " + c.Image
	}
	return HostdInstallInstructions{Lines: []string{cmd}}
}

// defaultHostInviteInstall builds the shared enrollment command shown in
// Settings -> Hosts -> Invite hosts.
func defaultHostInviteInstall(c HostdInstallContext) HostdInstallInstructions {
	if c.Origin == "" || c.JoinCode == "" {
		return HostdInstallInstructions{}
	}
	cmd := "curl -fsSL " + c.Origin + hostdInstallShortPath + " | sh -s -- --join " + c.JoinCode
	// As defaultHostdInstall: the script installs only the daemon's own
	// release unless the command it is given says otherwise, in sight.
	if c.Release != "" {
		cmd += " --release " + c.Release
	} else {
		cmd += " --any-version"
	}
	if c.Image != "" {
		cmd += " --image " + c.Image
	}
	return HostdInstallInstructions{Lines: []string{cmd}}
}

// hostInviteInstallFor renders the shared "Invite hosts" command for joinCode
// using a registered HostdInstallProvider when present or defaultHostInviteInstall.
func (s *Server) hostInviteInstallFor(r *http.Request, joinCode string) *HostdInstall {
	out := s.hostdInstallInfo()
	c := HostdInstallContext{
		Origin:   hostdInstallOrigin(r),
		Release:  out.Release,
		Image:    out.Image,
		JoinCode: joinCode,
	}
	inst := defaultHostInviteInstall(c)
	if f := registeredHostdInstall(); f != nil && joinCode != "" {
		if got := f(c); len(got.Lines) > 0 {
			lines := append([]string(nil), got.Lines...)
			hasJoin := false
			for _, l := range lines {
				if strings.Contains(l, joinCode) || strings.Contains(l, "--join") {
					hasJoin = true
					break
				}
			}
			if !hasJoin {
				lines[len(lines)-1] += " --join " + joinCode
			}
			inst = HostdInstallInstructions{Lines: lines, Note: got.Note}
		}
	}
	out.Lines, out.Note = inst.Lines, inst.Note
	return out
}

// hostdInstallOrigin is the URL r reached this daemon at, or "" when it
// is not one a shell line can carry between single quotes.
func hostdInstallOrigin(r *http.Request) string {
	url := hostdInstallDaemonURL(r)
	host := strings.TrimPrefix(strings.TrimPrefix(url, "http://"), "https://")
	if host == url || !hostdInstallHostRE.MatchString(host) {
		return ""
	}
	return url
}

// hostdInstallFor is HostdInstall for hostd h, with the instructions the
// registered provider writes (or the default's). token, when the caller
// is about to be shown it, is held out of them: instructions that carry
// it anyway are dropped for the default's.
func (s *Server) hostdInstallFor(r *http.Request, h model.Hostd, token string) *HostdInstall {
	out := s.hostdInstallInfo()
	var features []hostd.Feature
	if s.tasks != nil {
		if st, ok := s.tasks.Config.HostdRemote.(hostdStatuses); ok {
			if cur, ok := st.Statuses()[h.Name]; ok && cur.Connected {
				features = slices.Clone(cur.Features)
			}
		}
	}
	c := HostdInstallContext{
		Origin:   hostdInstallOrigin(r),
		Name:     h.Name,
		Owner:    h.Owner,
		Release:  out.Release,
		Image:    out.Image,
		Features: features,
	}
	inst := defaultHostdInstall(c)
	if f := registeredHostdInstall(); f != nil {
		if got := f(c); len(got.Lines) > 0 {
			if hostdInstallCarries(got, token) {
				log.Printf("grain hostd: the registered install instructions for hostd %s carry its token; showing the default ones instead", h.Name)
			} else {
				inst = got
			}
		}
	}
	if hostdInstallCarries(inst, token) {
		inst = HostdInstallInstructions{}
	}
	out.Lines, out.Note = inst.Lines, inst.Note
	return out
}

// hostdInstallCarries reports whether any of in's text holds token.
func hostdInstallCarries(in HostdInstallInstructions, token string) bool {
	if token == "" {
		return false
	}
	if strings.Contains(in.Note, token) {
		return true
	}
	for _, l := range in.Lines {
		if strings.Contains(l, token) {
			return true
		}
	}
	return false
}

// hostdInstallInfo is HostdInstall for this daemon, each value held to
// the pattern the script would carry it under.
func (s *Server) hostdInstallInfo() *HostdInstall {
	out := &HostdInstall{Release: version.Get().Release, Image: s.hostdInstallImage()}
	if !hostdInstallReleaseRE.MatchString(out.Release) {
		out.Release = ""
	}
	if !hostdInstallImageRE.MatchString(out.Image) {
		out.Image = ""
	}
	return out
}

// renderHostdInstallScript is hostd_install.sh with url, release, image
// and the default agentd allowlist template in its placeholders, each left
// empty when it does not match its pattern.
func renderHostdInstallScript(url, release, image string) string {
	safeURL := sanitizeHostdInstallURL(url)
	return renderHostdInstallScriptWithTemplate(url, release, image, defaultAgentdAllowlistTemplate(safeURL))
}

func sanitizeHostdInstallURL(url string) string {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") ||
		!hostdInstallHostRE.MatchString(strings.TrimPrefix(strings.TrimPrefix(url, "http://"), "https://")) {
		return ""
	}
	return url
}

func renderHostdInstallScriptWithTemplate(url, release, image string, tmpl hostd.AllowlistTemplate) string {
	url = sanitizeHostdInstallURL(url)
	if !hostdInstallReleaseRE.MatchString(release) {
		release = ""
	}
	if !hostdInstallImageRE.MatchString(image) {
		image = ""
	}
	tmpl.Server = url
	return strings.NewReplacer(
		hostdInstallURLMark, url,
		hostdInstallReleaseMark, release,
		hostdInstallImageMark, image,
		hostdAgentdTemplateMark, encodeHeredocTemplate(tmpl),
	).Replace(hostdInstallScript)
}

func encodeHeredocTemplate(tmpl hostd.AllowlistTemplate) string {
	if err := tmpl.Validate(); err != nil {
		return ""
	}
	raw, err := json.Marshal(tmpl)
	if err != nil {
		return ""
	}
	s := string(raw)
	if strings.ContainsAny(s, "\r\n\x00") || strings.Contains(s, "AGENTD_TEMPLATE") {
		return ""
	}
	return s
}

func (s *Server) agentdAllowlistTemplate(ctx context.Context, serverURL string, includeCustomPaths bool) hostd.AllowlistTemplate {
	if s == nil || s.tasks == nil || s.tasks.Store == nil {
		return defaultAgentdAllowlistTemplate(serverURL)
	}
	cfg, err := s.tasks.Store.GetConfig(ctx)
	if err != nil {
		return defaultAgentdAllowlistTemplate(serverURL)
	}
	return buildAgentdAllowlistTemplate(cfg, serverURL, includeCustomPaths)
}

func defaultAgentdAllowlistTemplate(serverURL string) hostd.AllowlistTemplate {
	return buildAgentdAllowlistTemplate(nil, serverURL, false)
}

func buildAgentdAllowlistTemplate(cfg *model.Config, serverURL string, includeCustomPaths bool) hostd.AllowlistTemplate {
	frameworks := orderedTemplateFrameworks(cfg)
	customPaths := map[string][]string{}
	if cfg != nil && includeCustomPaths {
		for _, b := range cfg.AgentBackends {
			bin := model.AgentCLIBinary(b.Framework)
			p := strings.TrimSpace(b.CLIPath)
			if bin == "" || p == "" {
				continue
			}
			if !slices.Contains(customPaths[bin], p) {
				customPaths[bin] = append(customPaths[bin], p)
			}
		}
	}

	clis := make([]hostd.CLITemplateEntry, 0, len(frameworks))
	for _, fw := range frameworks {
		entry, ok := frameworkCLITemplateEntry(fw, customPaths[model.AgentCLIBinary(fw)])
		if ok {
			clis = append(clis, entry)
		}
	}
	for _, extra := range hostd.RegisteredCLITemplates() {
		extra.Candidates = mergeUniqueCandidates(hostd.RegisteredCLICandidates(extra.Name), extra.Candidates)
		if err := extra.Validate(); err != nil {
			continue
		}
		replaced := false
		for i := range clis {
			if clis[i].Name == extra.Name {
				clis[i] = extra
				replaced = true
				break
			}
		}
		if !replaced {
			clis = append(clis, extra)
		}
	}
	return hostd.AllowlistTemplate{
		Version: hostd.AllowlistTemplateVersion,
		Server:  serverURL,
		CLIs:    clis,
	}
}

func orderedTemplateFrameworks(cfg *model.Config) []string {
	var order []string
	add := func(fw string) {
		norm := model.NormalizeAgentFrameworkName(strings.TrimSpace(fw))
		if model.ValidAgentFramework(norm) && !slices.Contains(order, norm) {
			order = append(order, norm)
		}
	}
	if cfg != nil {
		if strings.TrimSpace(cfg.AgentFramework) != "" {
			add(cfg.AgentFramework)
		}
		for _, b := range model.DeploymentAgentBackends(cfg.AgentBackends, false) {
			add(b.Framework)
		}
	}
	for _, fw := range model.AgentFrameworks() {
		add(fw)
	}
	return order
}

func frameworkCLITemplateEntry(framework string, customPaths []string) (hostd.CLITemplateEntry, bool) {
	norm := model.NormalizeAgentFrameworkName(framework)
	bin := model.AgentCLIBinary(norm)
	if bin == "" {
		return hostd.CLITemplateEntry{}, false
	}
	var env, aliases []string
	switch norm {
	case model.AgentFrameworkAntigravity:
		env = antigravity.HostdEnv()
		aliases = []string{model.AgentFrameworkAntigravity}
	case model.AgentFrameworkClaude:
		env = claude.HostdEnv()
	case model.AgentFrameworkCodex:
		env = codex.HostdEnv()
	default:
		return hostd.CLITemplateEntry{}, false
	}
	defaults := []string{
		bin,
		"~/.local/bin/" + bin,
		"/usr/local/bin/" + bin,
		"/usr/bin/" + bin,
	}
	candidates := mergeUniqueCandidates(hostd.RegisteredCLICandidates(bin), defaults)
	for _, cp := range customPaths {
		test := hostd.CLITemplateEntry{Name: bin, Candidates: []string{cp}}
		if test.Validate() == nil && !slices.Contains(candidates, cp) {
			candidates = append(candidates, cp)
		}
	}
	return hostd.CLITemplateEntry{
		Name:       bin,
		Aliases:    aliases,
		Candidates: candidates,
		Env:        env,
		PassEnv:    slices.Clone(hostd.ProxyVars),
	}, true
}

func mergeUniqueCandidates(first, rest []string) []string {
	out := make([]string, 0, len(first)+len(rest))
	for _, list := range [][]string{first, rest} {
		for _, c := range list {
			c = strings.TrimSpace(c)
			if c != "" && !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	return out
}

// hostdInstallDaemonURL is the URL r reached this daemon at: https when
// it came over TLS or through a proxy that says so (requestIsHTTPS), and
// the Host it asked for. A caller that lies in either only gets a script
// that dials somewhere else, which is theirs to run or not; the script
// takes --server for a daemon whose URL is not the one it was fetched at.
func hostdInstallDaemonURL(r *http.Request) string {
	scheme := "http"
	if requestIsHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// hostdInstallImageTTL is how long the image the script suggests is
// remembered: asking the upgrader can mean asking the cluster's API.
const hostdInstallImageTTL = time.Minute

// hostdImageCache is hostdInstallImage's last answer and when it was
// asked; its zero value is empty.
type hostdImageCache struct {
	sync.Mutex
	at    time.Time
	image string
}

// hostdInstallImage is the image this deployment runs (Upgrader.Running),
// for the script to take a grain of the daemon's own version out of, or
// "" when the deployment cannot say.
func (s *Server) hostdInstallImage() string {
	if s.tasks == nil || s.tasks.Config.Upgrader == nil {
		return ""
	}
	c := &s.hostdImage
	c.Lock()
	defer c.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < hostdInstallImageTTL {
		return c.image
	}
	c.image = ""
	if running, err := s.tasks.Config.Upgrader.Running(); err == nil {
		c.image = running.Ref
	}
	c.at = time.Now()
	return c.image
}
