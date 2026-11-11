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
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	agentdrelease "github.com/ai-on-gke/grain/pkg/agentd/release"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/version"
)

// hostdInstallPath is the bootstrap script a machine registering as a
// remote grain hostd runs; hostdInstallShortPath is the shorter URL shown
// in Settings -> Hosts -> Invite hosts.
//
// hostdAgentdBinaryRoute and hostdAgentdManifestRoute serve the per-platform
// grain-agentd static binary and its signed manifest directly from the
// controller, for hosts that cannot reach the internet.
const (
	hostdInstallPath        = "/api/hostd/install.sh"
	hostdInstallShortPath   = "/hostd/install"
	hostdAgentdBinaryRoute  = "/api/hostd/agentd/{os}/{arch}"
	hostdAgentdManifestPath = "/api/hostd/agentd/{os}/{arch}/manifest.json"

	defaultAgentdDistDir      = "/usr/local/share/grain/agentd"
	agentdDistDirEnv          = "GRAIN_AGENTD_DIST_DIR"
	agentdReleaseURLEnv       = "GRAIN_AGENTD_RELEASE_URL"
	agentdReleasePublicKeyEnv = "GRAIN_AGENTD_RELEASE_PUBLIC_KEY"

	maxAgentdManifestBytes = 64 << 10 // 64 KiB
	maxAgentdBinaryBytes   = 64 << 20 // 64 MiB
	agentdFetchBackoff     = 30 * time.Second
)

//go:embed hostd_install.sh
var hostdInstallScript string

// The placeholders hostd_install.sh carries for what only the daemon
// serving it knows.
const (
	hostdInstallURLMark     = "@GRAIN_DAEMON_URL@"
	hostdInstallReleaseMark = "@GRAIN_DAEMON_RELEASE@"
	hostdInstallImageMark   = "@GRAIN_DAEMON_IMAGE@"
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
	if r.URL.Path == hostdInstallPath || r.URL.Path == hostdInstallShortPath {
		return true
	}
	_, _, ok := hostdAgentdReleasePath(r.URL.Path)
	return ok
}

// hostdAgentdReleasePath parses /api/hostd/agentd/{os}/{arch}[/manifest.json].
func hostdAgentdReleasePath(p string) (platform string, manifest bool, ok bool) {
	rest, ok := strings.CutPrefix(p, "/api/hostd/agentd/")
	if !ok || rest == "" {
		return "", false, false
	}
	if trimmed, isManifest := strings.CutSuffix(rest, "/manifest.json"); isManifest {
		rest = trimmed
		manifest = true
	}
	osName, arch, hasSlash := strings.Cut(rest, "/")
	if !hasSlash || osName == "" || arch == "" || strings.Contains(arch, "/") {
		return "", false, false
	}
	for _, s := range []string{osName, arch} {
		for i := 0; i < len(s); i++ {
			c := s[i]
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
				return "", false, false
			}
		}
	}
	return osName + "/" + arch, manifest, true
}

// handleHostdInstallScript serves hostd_install.sh with this daemon's
// URL, as the caller reached it, filled in for the hostd to dial, and
// for a caller with access the daemon's release and image too.
func (s *Server) handleHostdInstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	release, image := "", ""
	if s.hostdInstallCallerHasAccess(r) {
		release, image = version.Get().Release, s.hostdInstallImage()
	}
	_, _ = w.Write([]byte(renderHostdInstallScript(hostdInstallDaemonURL(r), release, image)))
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

// renderHostdInstallScript is hostd_install.sh with url, release and
// image in its placeholders, each left empty when it does not match
// its pattern.
func renderHostdInstallScript(url, release, image string) string {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") ||
		!hostdInstallHostRE.MatchString(strings.TrimPrefix(strings.TrimPrefix(url, "http://"), "https://")) {
		url = ""
	}
	if !hostdInstallReleaseRE.MatchString(release) {
		release = ""
	}
	if !hostdInstallImageRE.MatchString(image) {
		image = ""
	}
	return strings.NewReplacer(
		hostdInstallURLMark, url,
		hostdInstallReleaseMark, release,
		hostdInstallImageMark, image,
	).Replace(hostdInstallScript)
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

type cachedAgentdRelease struct {
	manifestJSON []byte
	manifest     agentdrelease.Manifest
	binaryPath   string
	failedAt     time.Time
}

type agentdReleaseCache struct {
	mu      sync.Mutex
	entries map[string]cachedAgentdRelease
}

func (s *Server) handleHostdAgentdBinary(w http.ResponseWriter, r *http.Request) {
	s.serveHostdAgentdRelease(w, r, false)
}

func (s *Server) handleHostdAgentdManifest(w http.ResponseWriter, r *http.Request) {
	s.serveHostdAgentdRelease(w, r, true)
}

func (s *Server) serveHostdAgentdRelease(w http.ResponseWriter, r *http.Request, wantManifest bool) {
	// Hold bearer access-code guesses to s.access just as handleHostdInstallScript
	// does on the open install routes.
	_ = s.hostdInstallCallerHasAccess(r)

	platform := r.PathValue("os") + "/" + r.PathValue("arch")
	if !slices.Contains(agentdrelease.Platforms, platform) {
		http.NotFound(w, r)
		return
	}
	entry, ok := s.resolveAgentdRelease(r.Context(), platform)
	if !ok {
		http.Error(w, "no grain-agentd release asset available for "+platform, http.StatusNotFound)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Grain-Agentd-Version", entry.manifest.Version)
	w.Header().Set("X-Grain-Agentd-SHA256", entry.manifest.SHA256)

	if wantManifest {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(entry.manifestJSON)))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(entry.manifestJSON)
		return
	}

	f, err := os.Open(entry.binaryPath)
	if err != nil {
		http.Error(w, "grain-agentd binary unavailable", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() != entry.manifest.Size {
		http.Error(w, "grain-agentd binary unavailable", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", entry.manifest.Asset))
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = io.Copy(w, f)
}

func (s *Server) resolveAgentdRelease(ctx context.Context, platform string) (cachedAgentdRelease, bool) {
	dirs := agentdLocalDirs()
	// 1. Prefer a local directory carrying a signed, verified manifest + binary.
	for _, dir := range dirs {
		if entry, ok := loadLocalAgentdAsset(dir, platform, true); ok {
			return entry, true
		}
	}

	// 2. When the daemon knows its release and upstream fetching is enabled,
	// fetch and verify the signed release manifest + binary once from the
	// release URL so hosts without internet access receive the signed release.
	if rel := cliInstallRelease(); rel != "" {
		if base := agentdReleaseBaseURL(); base != "" && base != "off" {
			if entry, ok := s.fetchAndCacheAgentdRelease(ctx, base, rel, platform); ok {
				return entry, true
			}
		}
	}

	// 3. Fall back to a bundled local binary + hash-verified manifest (for
	// example /usr/local/share/grain/agentd inside an offline controller image).
	for _, dir := range dirs {
		if entry, ok := loadLocalAgentdAsset(dir, platform, false); ok {
			return entry, true
		}
	}
	return cachedAgentdRelease{}, false
}

func agentdLocalDirs() []string {
	if env := strings.TrimSpace(os.Getenv(agentdDistDirEnv)); env != "" {
		return []string{env}
	}
	dirs := []string{defaultAgentdDistDir}
	if exe, err := os.Executable(); err == nil && exe != "" {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "agentd"))
	}
	return dirs
}

func agentdReleaseBaseURL() string {
	if u := strings.TrimSpace(os.Getenv(agentdReleaseURLEnv)); u != "" {
		if u == "off" {
			return "off"
		}
		u = strings.TrimRight(u, "/")
		if cliInstallURLRE.MatchString(u) {
			return u
		}
		return ""
	}
	if u := strings.TrimSpace(os.Getenv(cliReleaseURLEnv)); u != "" {
		u = strings.TrimRight(u, "/")
		if cliInstallURLRE.MatchString(u) {
			return u
		}
		return ""
	}
	return defaultCLIReleaseURL
}

func controllerPinnedAgentdKey() ([]byte, error) {
	if raw := strings.TrimSpace(os.Getenv(agentdReleasePublicKeyEnv)); raw != "" {
		return agentdrelease.ParsePublicKey(raw)
	}
	return agentdrelease.EmbeddedPublicKey()
}

func loadLocalAgentdAsset(dir, platform string, requireSigned bool) (cachedAgentdRelease, bool) {
	binName, err := agentdrelease.AssetName(platform)
	if err != nil {
		return cachedAgentdRelease{}, false
	}
	manifestName, err := agentdrelease.ManifestAssetName(platform)
	if err != nil {
		return cachedAgentdRelease{}, false
	}
	manifestPath := filepath.Join(dir, manifestName)
	binPath := filepath.Join(dir, binName)

	manifestRaw, err := os.ReadFile(manifestPath)
	if err != nil || len(manifestRaw) > maxAgentdManifestBytes {
		return cachedAgentdRelease{}, false
	}
	m, err := agentdrelease.ParseManifest(manifestRaw)
	if err != nil || m.Platform != platform {
		return cachedAgentdRelease{}, false
	}
	if requireSigned && m.Signature == "" {
		return cachedAgentdRelease{}, false
	}
	binBytes, err := os.ReadFile(binPath)
	if err != nil {
		return cachedAgentdRelease{}, false
	}
	if m.Signature != "" {
		pinnedPub, err := controllerPinnedAgentdKey()
		if err != nil {
			return cachedAgentdRelease{}, false
		}
		if _, err := agentdrelease.VerifyBinary(m, binBytes, pinnedPub); err != nil {
			return cachedAgentdRelease{}, false
		}
	} else {
		if err := agentdrelease.VerifyBinaryHash(m, binBytes); err != nil {
			return cachedAgentdRelease{}, false
		}
	}
	return cachedAgentdRelease{
		manifestJSON: manifestRaw,
		manifest:     m,
		binaryPath:   binPath,
	}, true
}

func (s *Server) fetchAndCacheAgentdRelease(ctx context.Context, baseURL, rel, platform string) (cachedAgentdRelease, bool) {
	key := baseURL + "|" + rel + "|" + platform
	c := &s.agentdRelease
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]cachedAgentdRelease)
	}
	if cur, ok := c.entries[key]; ok {
		if cur.binaryPath != "" {
			if st, err := os.Stat(cur.binaryPath); err == nil && st.Size() == cur.manifest.Size {
				return cur, true
			}
		}
		if !cur.failedAt.IsZero() && time.Since(cur.failedAt) < agentdFetchBackoff {
			return cachedAgentdRelease{}, false
		}
	}

	binName, _ := agentdrelease.AssetName(platform)
	manifestName, _ := agentdrelease.ManifestAssetName(platform)
	prefix := strings.TrimRight(baseURL, "/") + "/" + rel + "/"

	manifestRaw, err := fetchBoundedURL(ctx, prefix+manifestName, maxAgentdManifestBytes, 15*time.Second)
	if err != nil {
		c.entries[key] = cachedAgentdRelease{failedAt: time.Now()}
		return cachedAgentdRelease{}, false
	}
	m, err := agentdrelease.ParseManifest(manifestRaw)
	if err != nil || m.Platform != platform || m.Version != rel {
		c.entries[key] = cachedAgentdRelease{failedAt: time.Now()}
		return cachedAgentdRelease{}, false
	}
	binBytes, err := fetchBoundedURL(ctx, prefix+binName, maxAgentdBinaryBytes, 60*time.Second)
	if err != nil {
		c.entries[key] = cachedAgentdRelease{failedAt: time.Now()}
		return cachedAgentdRelease{}, false
	}
	if m.Signature != "" {
		pinnedPub, err := controllerPinnedAgentdKey()
		if err != nil {
			c.entries[key] = cachedAgentdRelease{failedAt: time.Now()}
			return cachedAgentdRelease{}, false
		}
		if _, err := agentdrelease.VerifyBinary(m, binBytes, pinnedPub); err != nil {
			c.entries[key] = cachedAgentdRelease{failedAt: time.Now()}
			return cachedAgentdRelease{}, false
		}
	} else if err := agentdrelease.VerifyBinaryHash(m, binBytes); err != nil {
		c.entries[key] = cachedAgentdRelease{failedAt: time.Now()}
		return cachedAgentdRelease{}, false
	}

	cacheDir := filepath.Join(os.TempDir(), "grain-agentd-release-cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		c.entries[key] = cachedAgentdRelease{failedAt: time.Now()}
		return cachedAgentdRelease{}, false
	}
	binPath := filepath.Join(cacheDir, m.SHA256+"-"+binName)
	if st, err := os.Stat(binPath); err != nil || st.Size() != m.Size {
		tmp := fmt.Sprintf("%s.tmp.%d", binPath, os.Getpid())
		if err := os.WriteFile(tmp, binBytes, 0o600); err != nil {
			c.entries[key] = cachedAgentdRelease{failedAt: time.Now()}
			return cachedAgentdRelease{}, false
		}
		if err := os.Rename(tmp, binPath); err != nil {
			_ = os.Remove(tmp)
			c.entries[key] = cachedAgentdRelease{failedAt: time.Now()}
			return cachedAgentdRelease{}, false
		}
	}
	entry := cachedAgentdRelease{
		manifestJSON: manifestRaw,
		manifest:     m,
		binaryPath:   binPath,
	}
	c.entries[key] = entry
	return entry, true
}

func fetchBoundedURL(ctx context.Context, rawURL string, maxBytes int64, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", rawURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("GET %s exceeded %d bytes", rawURL, maxBytes)
	}
	return body, nil
}
