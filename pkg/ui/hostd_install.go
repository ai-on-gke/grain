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
	_ "embed"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/version"
)

// hostdInstallPath is the bootstrap script a machine registering as a
// remote grain hostd runs (Settings -> Hostds shows the command).
const hostdInstallPath = "/api/hostd/install.sh"

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
	hostdInstallReleaseRE = regexp.MustCompile(`^[A-Za-z0-9.+\-]{1,128}$`)
	hostdInstallImageRE   = regexp.MustCompile(`^[A-Za-z0-9._/:@\-]{1,255}$`)
)

// hostdInstallRoute reports whether r asks for the install script. It
// is grain's own script and the URL it was asked for at -- the daemon's
// build and the image it runs, which GET /api/config already tells
// anybody, and never a token or the access code -- so like a hostd
// dialing in (hostdConnectRoute) it does not ask who the caller is: the
// machine fetching it has nothing to sign in with until it holds its
// hostd token.
func hostdInstallRoute(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return r.URL.Path == hostdInstallPath
}

// handleHostdInstallScript serves hostd_install.sh with this daemon's
// URL, as the caller reached it, filled in for the hostd to dial.
func (s *Server) handleHostdInstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(renderHostdInstallScript(hostdInstallDaemonURL(r), version.Get().Release, s.hostdInstallImage())))
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
// remembered: asking the upgrader can mean asking the cluster's API, which
// an unauthenticated route should not do once per request.
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
