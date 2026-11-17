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

package cli

import (
	"regexp"
	"strings"
	"testing"
)

// "hostd" is an implementation detail: the CLI's help calls the machines
// grain runs host work on hosts, and the process on them the host agent
// (`grain host`; `grain hostd` is a hidden alias). These tests read every
// command's -h output, and the hand-written usage texts, for the word.
//
// hostdHelpAllowed strips what may still say it: the names a user types
// -- the host agent's binary, unit, state and config paths, the daemon
// flags and environment that name it, API paths -- and wire values
// quoted as such ("run: hostd", a demo executor's "hostd-remote").
var hostdHelpAllowed = []*regexp.Regexp{
	regexp.MustCompile(`/run/grain-hostd[\w./-]*`),
	regexp.MustCompile(`/etc/grain/hostd\.(yaml|token)(\.default)?`),
	regexp.MustCompile(`grain-hostd[\w.-]*`),
	regexp.MustCompile(`-hostd-[a-z-]+`),
	regexp.MustCompile(`GRAIN_HOSTD[A-Z_]*`),
	regexp.MustCompile(`/api/hostds?[\w/{}.-]*`),
	regexp.MustCompile(`pkg/hostd\b`),
	regexp.MustCompile(`run: hostd\b`),
	regexp.MustCompile(`\\?"hostd[\w-]*\\?"`),
	regexp.MustCompile(`needs-hostd:?`),
}

var hostdWord = regexp.MustCompile(`(?i)\bhostds?\b`)

// hostdLines is each line of text that says hostd once the allowed
// names are taken out.
func hostdLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		rest := line
		for _, re := range hostdHelpAllowed {
			rest = re.ReplaceAllString(rest, "")
		}
		if hostdWord.MatchString(rest) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

func TestHostdLinesAllowsOnlyWhatAUserTypes(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{
		"the allowlist (default \"/etc/grain/hostd.yaml\")",
		"run by grain-hostd.service before each start",
		"  -hostd-token-file string",
		"Register each host in the host registry (/api/hostds) instead",
	} {
		if got := hostdLines(ok); got != nil {
			t.Errorf("hostdLines(%q) = %q, want nothing", ok, got)
		}
	}
	for _, bad := range []string{"the hostd registry", "grain hostds list", "ask the hostd serving -socket"} {
		if got := hostdLines(bad); len(got) != 1 {
			t.Errorf("hostdLines(%q) = %q, want it found", bad, got)
		}
	}
}

func TestCLIUsageSaysHostNotHostd(t *testing.T) {
	t.Parallel()
	for name, text := range map[string]string{"usage": usage, "hostRegistryUsage": hostRegistryUsage} {
		for _, line := range hostdLines(text) {
			t.Errorf("%s says hostd: %s", name, line)
		}
	}
}

func TestEveryCommandsHelpSaysHostNotHostd(t *testing.T) {
	if testing.Short() {
		t.Skip("runs every command's -h in a child process")
	}
	t.Parallel()
	for _, path := range hostSpecHelpPaths(t) {
		for _, line := range hostdLines(hostSpecHelp(t, path)) {
			t.Errorf("grain %s -h says hostd: %s", strings.Join(path, " "), line)
		}
	}
}
