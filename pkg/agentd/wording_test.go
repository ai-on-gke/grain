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

package agentd

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// agentdHostdAllowed strips the names a user types that may still say
// hostd: the staging directory, the token environment variable, the API
// path. Everything else grain-agentd says to a person calls the machine a
// host and the process the host agent, as `grain` does
// (pkg/cli/host_wording_test.go, TestEveryCommandsHelpSaysHostNotHostd).
var agentdHostdAllowed = regexp.MustCompile(`hostd-stage|GRAIN_HOSTD[A-Z_]*|/api/hostds?[\w/{}.-]*`)

var agentdHostdWord = regexp.MustCompile(`(?i)\bhostds?\b`)

func agentdHostdLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if agentdHostdWord.MatchString(agentdHostdAllowed.ReplaceAllString(line, "")) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// grain-agentd's -h, install -h, the generated allowlist's header and the
// refusal of an allowlist naming capabilities say "host", not "hostd"
// (the 2026-11-07 hosts walkthrough found all four saying hostd, one
// sending the user to `grain hostd`).
func TestAgentdSaysHostNotHostd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	texts := map[string]string{}
	var runErr bytes.Buffer
	run([]string{"-h"}, &bytes.Buffer{}, &runErr)
	texts["grain-agentd -h"] = runErr.String()
	var installErr bytes.Buffer
	install([]string{"-h"}, &bytes.Buffer{}, &installErr)
	texts["grain-agentd install -h"] = installErr.String()
	texts["default allowlist"] = defaultAllowlist("/home/u/.grain/hostd-stage", "https://grain.example.com", "/usr/bin/agy")

	p := filepath.Join(t.TempDir(), "agentd.yaml")
	writeFile(t, p, "clis:\n  agy:\n    path: /bin/true\ncapabilities:\n  x:\n    command: /bin/true\n", 0o644)
	if err := checkAgentsOnly(p, owners()); err == nil {
		t.Fatal("checkAgentsOnly took an allowlist naming capabilities")
	} else {
		texts["capabilities refusal"] = err.Error()
		if !strings.Contains(err.Error(), "`grain host`") {
			t.Errorf("capabilities refusal %q does not point at `grain host`", err)
		}
	}
	for what, text := range texts {
		if strings.TrimSpace(text) == "" {
			t.Errorf("%s printed nothing", what)
		}
		for _, line := range agentdHostdLines(text) {
			t.Errorf("%s says hostd: %s", what, line)
		}
	}
}
