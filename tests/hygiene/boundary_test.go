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

package hygiene

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// googleTokens are words that name Google-internal tools, hosts, paths
// and projects. This repository is grain's open-source core: the Google
// layer lives elsewhere and imports it, so nothing here may say them.
// Tests and docs use made-up fixtures instead (pkg/gitproxy/gitproxytest,
// pkg/authfix/authfixtest, pkg/installtrack/installtracktest).
var googleTokens = regexp.MustCompile(`gcert|googlers\.com|corp\.goog|corp\.google|/google/bin|/google/src|/usr/local/google|\bggh\b|\bcitc\b|google3|uberproxy|beyondcorp|moma_search|jetski-devs|gke-adaptive-orchestration|bsalmon-fun-sandbox`)

// googleTokensExempt are the tracked paths the scan skips: historical
// records, which say what was true when they were written (their Google
// names were scrubbed on import, but a later record may quote one), and
// this file, which has to spell the tokens to find them.
var googleTokensExempt = []string{
	"docs/test-runs/",
	"docs/test-plans/",
	"docs/releases/",
	"tests/hygiene/boundary_test.go",
}

// googleTokensPending are files that still say a Google token, imported
// from bwsalmon/grain before it was all moved out. The list may only
// shrink: a file here that no longer says one fails the test until it is
// taken off, so that it cannot quietly start again.
var googleTokensPending = map[string]string{
	// The google3 change target (propose_task's google3 argument, the
	// CLI's -google3 flag, the new-task form's google3 choice).
	"pkg/cli/google3_target_test.go":                    "google3 change target",
	"pkg/cli/taskcmd.go":                                "google3 change target",
	"pkg/mcp/mock_tools.go":                             "google3 change target",
	"pkg/mcp/propose_google3.go":                        "google3 change target",
	"pkg/mcp/propose_google3_test.go":                   "google3 change target",
	"pkg/model/change_target.go":                        "google3 change target",
	"pkg/model/migrations.go":                           "google3 change target",
	"pkg/orchestrator/finish.go":                        "google3 change target",
	"pkg/ui/change_target_test.go":                      "google3 change target",
	"ui/src/components/NewTaskOverlay.google3.test.jsx": "google3 change target",
	"ui/src/components/NewTaskOverlay.jsx":              "google3 change target",
	"ui/src/components/RepoField.jsx":                   "google3 change target",
	// The installer's google install track and the tests that drive it.
	"scripts/setup.sh":                          "google install track",
	"tests/deploy/deploy_test.go":               "google install track",
	"tests/deploy/hostd_builtins_test.go":       "google install track",
	"tests/deploy/hostd_detached_test.go":       "google install track",
	"tests/deploy/hostd_host_git_test.go":       "google install track",
	"tests/deploy/image_channel_test.go":        "google install track",
	"tests/deploy/root_writes_test.go":          "google install track",
	"ui/e2e/hostd-missing-entry-layout.spec.js": "google install track",
	"ui/e2e/upgrade-image-chooser.spec.js":      "google install track",
}

func TestCoreSaysNoGoogleTokens(t *testing.T) {
	root := repoRoot(t)
	seen := map[string]bool{}
	for _, rel := range trackedFiles(t, root) {
		if exempt(rel) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			continue // gone from the work tree, a directory symlink, or binary
		}
		loc := googleTokens.FindIndex(data)
		if loc == nil {
			continue
		}
		if _, ok := googleTokensPending[rel]; ok {
			seen[rel] = true
			continue
		}
		line := 1 + bytes.Count(data[:loc[0]], []byte("\n"))
		t.Errorf("%s:%d says %q, which is Google's: use a made-up fixture instead", rel, line, data[loc[0]:loc[1]])
	}
	for rel := range googleTokensPending {
		if !seen[rel] {
			t.Errorf("%s no longer says a Google token: take it off googleTokensPending", rel)
		}
	}
}

func exempt(rel string) bool {
	for _, p := range googleTokensExempt {
		if rel == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(rel, p)) {
			return true
		}
	}
	return false
}
