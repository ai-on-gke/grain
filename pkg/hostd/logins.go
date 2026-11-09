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
)

// The agent CLIs' own logins on a host's machine (Hello.Logins): what a
// model connection with ambient credentials placed on that host spends.
// Each is read where its CLI keeps it, as the user hostd runs as; a file
// being there says the CLI has signed in on this machine, not that the
// login is still good -- the run that spends it finds that out.
const (
	LoginCodex       = "codex"
	LoginClaude      = "claude"
	LoginAntigravity = "antigravity"
)

// ambientLogins is the logins on this machine, for hostd's hello.
func ambientLogins() []string {
	home, _ := os.UserHomeDir()
	return loginsUnder(home, os.Getenv)
}

// loginsUnder is ambientLogins for a home directory and environment,
// sorted:
//
//   - codex: $CODEX_HOME/auth.json, else ~/.codex/auth.json (what
//     `codex login` writes, and what Hello.CodexAuth names);
//   - claude: $CLAUDE_CONFIG_DIR/.credentials.json, else
//     ~/.claude/.credentials.json (what `claude login` writes on Linux);
//   - antigravity: ~/.gemini/oauth_creds.json, or agy's own state
//     directory ~/.gemini/antigravity-cli, which it creates once signed in.
func loginsUnder(home string, getenv func(string) string) []string {
	under := func(env string, rel ...string) string {
		if dir := getenv(env); dir != "" {
			return dir
		}
		if home == "" {
			return ""
		}
		return filepath.Join(append([]string{home}, rel...)...)
	}
	isFile := func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && info.Mode().IsRegular()
	}
	isDir := func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && info.IsDir()
	}
	var out []string
	if dir := under("CODEX_HOME", ".codex"); dir != "" && isFile(filepath.Join(dir, "auth.json")) {
		out = append(out, LoginCodex)
	}
	if dir := under("CLAUDE_CONFIG_DIR", ".claude"); dir != "" && isFile(filepath.Join(dir, ".credentials.json")) {
		out = append(out, LoginClaude)
	}
	if home != "" && (isFile(filepath.Join(home, ".gemini", "oauth_creds.json")) ||
		isDir(filepath.Join(home, ".gemini", "antigravity-cli"))) {
		out = append(out, LoginAntigravity)
	}
	sort.Strings(out)
	return out
}
