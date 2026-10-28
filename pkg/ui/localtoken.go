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
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// The local API token is how the grain CLI on the daemon's own host, and
// the processes the daemon starts there itself (a run's `grain
// mcpserver`), keep reaching the UI/API over loopback, where every
// caller presents the access code (grain/task-2078). The daemon writes a random token to
// LocalAPITokenPath(dataDir), mode 0600 (0640 where scripts/setup.sh
// has granted the account's own group read, see EnsureLocalAPIToken),
// owned by the account it runs as; HTTPClient reads it and sends it as a bearer token to a loopback
// server; and a loopback request carrying it is trusted exactly as a
// loopback request was before -- as the deployment's own actor, no
// person borrowed. So the file is the credential: whoever can read it
// (the grain account, root, and the CLI wrapper setup.sh installs,
// which runs as the grain account) is the deployment's operator, and
// anybody else on the host -- somebody whose SSH port-forward lands on
// localhost -- signs in like anyone else.
const (
	// LocalAPITokenFileName is the token file's name under -data-dir.
	LocalAPITokenFileName = "local-api-token"
	// LocalAPITokenFileEnv names the token file for a client that does
	// not know the data directory: the daemon sets it in its own
	// environment, so every process it starts inherits it.
	LocalAPITokenFileEnv = "GRAIN_LOCAL_API_TOKEN_FILE"
)

// LocalAPITokenPath is where a daemon started with dataDir keeps its
// local API token.
func LocalAPITokenPath(dataDir string) string {
	return filepath.Join(dataDir, LocalAPITokenFileName)
}

// EnsureLocalAPIToken returns the token in the file at path, writing a
// new random one there (mode 0600) first if there is none. A token
// already there is kept, so a CLI holding it keeps working across
// daemon restarts; deleting the file and restarting rotates it.
func EnsureLocalAPIToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			// Tightened on every start rather than trusted to have
			// stayed as written: the file is a credential. Group read
			// is the one bit kept if it is already set: scripts/
			// setup.sh grants it, on a daemon account whose group is
			// its own, so the operator it adds to that group can run
			// the CLI without sudo (grain/task-3688). Nothing here
			// ever sets it, and nothing anyone else could be given
			// survives.
			if fi, err := os.Stat(path); err == nil {
				if err := os.Chmod(path, fi.Mode().Perm()&0o640|0o600); err != nil {
					return "", fmt.Errorf("restricting %s: %w", path, err)
				}
			}
			return tok, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// New tokens are access codes (accesscode.go): what a person types
	// into a single-tenant daemon's login page as well as what the CLI
	// sends. One from before that ("grain-local-<hex>") is kept above.
	tok, err := newAccessCode()
	if err != nil {
		return "", err
	}
	if err := writeAccessCodeFile(path, tok, 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// localAPIToken is the token HTTPClient sends to a loopback server it
// has no other credential for: the file LocalAPITokenFileEnv names, else
// $GRAIN_DATA_DIR's. "" when there is none, or it cannot be read -- a
// user on the host who is not the grain account, which is the point.
func localAPIToken() string {
	path := LocalAPITokenFile()
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// LocalAPITokenFile is the token file a client on this host would read:
// the one LocalAPITokenFileEnv names, else $GRAIN_DATA_DIR's, else "".
func LocalAPITokenFile() string {
	if path := strings.TrimSpace(os.Getenv(LocalAPITokenFileEnv)); path != "" {
		return path
	}
	if dir := strings.TrimSpace(os.Getenv("GRAIN_DATA_DIR")); dir != "" {
		return LocalAPITokenPath(dir)
	}
	return ""
}

// isLoopbackURL reports whether baseURL names this host: the only
// server HTTPClient offers the local API token to.
func isLoopbackURL(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return isLoopbackHost(u.Hostname())
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isLoopbackRequest reports whether r arrived from loopback.
func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	return isLoopbackHost(host)
}
