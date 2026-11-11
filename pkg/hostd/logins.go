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

package hostd

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Built-in CLI login names (Hello.Logins, Allowlist.Logins): what a model
// connection with ambient credentials placed on a host spends when the
// allowlist carries no logins: map of its own. A file being there says the
// CLI has signed in on this machine, not that the login is still good --
// the run that spends it finds that out.
const (
	LoginCodex       = "codex"
	LoginClaude      = "claude"
	LoginAntigravity = "antigravity"
)

type resolvedLogin struct {
	configured string
	resolved   string
}

func expandHomeWith(home, path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home == "" {
			return ""
		}
		return filepath.Join(home, rest)
	}
	return path
}

// builtinLoginMap is the built-in login table used when an allowlist has
// no logins: key, so existing allowlist files keep working:
//
//   - codex: $CODEX_HOME/auth.json, else ~/.codex/auth.json;
//   - claude: $CLAUDE_CONFIG_DIR/.credentials.json, else
//     ~/.claude/.credentials.json;
//   - antigravity: ~/.gemini/oauth_creds.json (plus ~/.gemini/antigravity-cli
//     for presence in loginsUnder).
func builtinLoginMap(home string, getenv func(string) string) map[string]string {
	under := func(env string, rel ...string) string {
		if dir := getenv(env); dir != "" {
			return dir
		}
		if home == "" {
			return ""
		}
		return filepath.Join(append([]string{home}, rel...)...)
	}
	out := map[string]string{}
	if dir := under("CODEX_HOME", ".codex"); dir != "" {
		out[LoginCodex] = filepath.Join(dir, "auth.json")
	}
	if dir := under("CLAUDE_CONFIG_DIR", ".claude"); dir != "" {
		out[LoginClaude] = filepath.Join(dir, ".credentials.json")
	}
	if home != "" {
		out[LoginAntigravity] = filepath.Join(home, ".gemini", "oauth_creds.json")
	}
	return out
}

func (a *Allowlist) configuredLogins(home string, getenv func(string) string) map[string]string {
	if a != nil && a.Logins != nil {
		return a.Logins
	}
	return builtinLoginMap(home, getenv)
}

func (a *Allowlist) resolvedLoginsUnder(home string, getenv func(string) string) map[string]resolvedLogin {
	raw := a.configuredLogins(home, getenv)
	out := make(map[string]resolvedLogin, len(raw))
	for name, p := range raw {
		expanded := expandHomeWith(home, p)
		if expanded == "" {
			continue
		}
		configured := filepath.Clean(expanded)
		if !filepath.IsAbs(configured) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(configured)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out[name] = resolvedLogin{configured: configured, resolved: resolved}
	}
	return out
}

// ambientLogins reports the names of logins that exist on this machine for
// a's logins: map (or the built-in table when absent), never their contents.
func (a *Allowlist) ambientLogins() []string {
	home, _ := os.UserHomeDir()
	return a.loginsUnder(home, os.Getenv)
}

// loginsUnder is ambientLogins for the built-in table under home and getenv.
func loginsUnder(home string, getenv func(string) string) []string {
	return (*Allowlist)(nil).loginsUnder(home, getenv)
}

func (a *Allowlist) loginsUnder(home string, getenv func(string) string) []string {
	resolved := a.resolvedLoginsUnder(home, getenv)
	var out []string
	for name := range resolved {
		out = append(out, name)
	}
	if (a == nil || a.Logins == nil) && home != "" {
		if _, ok := resolved[LoginAntigravity]; !ok {
			if info, err := os.Stat(filepath.Join(home, ".gemini", "antigravity-cli")); err == nil && info.IsDir() {
				out = append(out, LoginAntigravity)
			}
		}
	}
	sort.Strings(out)
	return out
}

// loginTargets maps each existing regular-file login in a (or the built-in
// table when a has no logins: map) to its resolved path on this machine,
// for Hello.LoginTargets on remote sessions.
func (a *Allowlist) loginTargets() map[string]string {
	home, _ := os.UserHomeDir()
	resolved := a.resolvedLoginsUnder(home, os.Getenv)
	if len(resolved) == 0 {
		return nil
	}
	out := make(map[string]string, len(resolved))
	for name, rl := range resolved {
		out[name] = rl.resolved
	}
	return out
}

// resolveSymlinkTarget checks that target names a login file in a (or the
// built-in table when a has no logins: map) and returns its resolved
// regular-file path.
func (a *Allowlist) resolveSymlinkTarget(target string) (string, bool) {
	if target == "" {
		return "", false
	}
	home, _ := os.UserHomeDir()
	expanded := expandHomeWith(home, target)
	if expanded == "" {
		return "", false
	}
	clean := filepath.Clean(expanded)
	if !filepath.IsAbs(clean) {
		return "", false
	}
	for _, rl := range a.resolvedLoginsUnder(home, os.Getenv) {
		if clean == rl.resolved || clean == rl.configured {
			return rl.resolved, true
		}
	}
	return "", false
}
