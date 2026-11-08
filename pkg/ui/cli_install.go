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
	"os"
	"regexp"
	"strings"

	"github.com/ai-on-gke/grain/pkg/version"
)

// cliInstallPath is the script Settings -> CLI tells a workstation to
// pipe to sh to install the grain CLI
// (design/spec/external-cli/external-cli.spec.md#XC-32).
const cliInstallPath = "/cli/install.sh"

//go:embed cli_install.sh
var cliInstallScript string

// The placeholders cli_install.sh carries for what only the daemon
// serving it knows.
const (
	cliInstallReleaseMark = "@GRAIN_CLI_RELEASE@"
	cliInstallURLMark     = "@GRAIN_CLI_RELEASE_URL@"
)

// defaultCLIReleaseURL is where release archives are published, as
// <url>/<version>/grain-<os>-<arch>.tar.gz beside a checksums.txt.
const defaultCLIReleaseURL = "https://github.com/ai-on-gke/grain/releases/download"

// cliReleaseURLEnv names a release mirror the daemon's operator
// configures in its place, for a deployment whose workstations cannot
// reach GitHub or that publishes its own builds.
const cliReleaseURLEnv = "GRAIN_CLI_RELEASE_URL"

// Each value is written into the script between single quotes, so each
// is held to characters that cannot end the quote or start a command; a
// value that does not match is left out, and the script asks for it
// instead (GRAIN_CLI_VERSION, GRAIN_CLI_RELEASE_URL).
var cliInstallURLRE = regexp.MustCompile(`^https?://[A-Za-z0-9.\-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~%/+\-]*)?$`)

// cliInstallRelease is the release the script pins: the daemon's own,
// which GET /api/auth/whoami already tells anybody. A test stamps one
// in, since a test binary carries none.
var cliInstallRelease = func() string {
	if info := version.Get(); !info.Unversioned {
		return info.Release
	}
	return ""
}

// cliInstallRoute reports whether r asks for the CLI install script. The
// script holds the daemon's release and where releases are published,
// never a token, and the workstation fetching it has no credential until
// `grain login` -- so like the hostd's own install script it does not ask
// who the caller is.
func cliInstallRoute(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL.Path == cliInstallPath
}

func (s *Server) handleCLIInstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	base := strings.TrimSpace(os.Getenv(cliReleaseURLEnv))
	if base == "" {
		base = defaultCLIReleaseURL
	}
	_, _ = w.Write([]byte(renderCLIInstallScript(cliInstallRelease(), base)))
}

// renderCLIInstallScript fills cli_install.sh in with release and the
// releases base URL, leaving out either one it cannot safely quote.
func renderCLIInstallScript(release, baseURL string) string {
	if !hostdInstallReleaseRE.MatchString(release) {
		release = ""
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if !cliInstallURLRE.MatchString(baseURL) {
		baseURL = ""
	}
	return strings.NewReplacer(
		cliInstallReleaseMark, release,
		cliInstallURLMark, baseURL,
	).Replace(cliInstallScript)
}
