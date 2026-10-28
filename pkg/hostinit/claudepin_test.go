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

package hostinit

import (
	"os"
	"regexp"
	"testing"
)

// TestClaudeCLIDefaultsMatchDockerfile holds this package's pinned claude
// CLI version and digests to the same values the deployment Dockerfile
// pins -- the same cross-file pinning discipline
// TestEveryBaseImageIsPinnedByDigest (tests/deploy) already holds the
// Dockerfiles themselves to, so the two never quietly drift apart.
func TestClaudeCLIDefaultsMatchDockerfile(t *testing.T) {
	data, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("reading the repository Dockerfile: %v", err)
	}
	text := string(data)

	check := func(argName, want string) {
		re := regexp.MustCompile(`(?m)^ARG ` + argName + `=(\S+)$`)
		m := re.FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("Dockerfile has no ARG %s= line", argName)
		}
		if m[1] != want {
			t.Errorf("Dockerfile's %s=%s does not match hostinit's Default%s=%s -- bump both together",
				argName, m[1], argName, want)
		}
	}
	check("CLAUDE_CLI_VERSION", DefaultClaudeCLIVersion)
	check("CLAUDE_CLI_SHA256_AMD64", DefaultClaudeCLISHA256Amd64)
	check("CLAUDE_CLI_SHA256_ARM64", DefaultClaudeCLISHA256Arm64)
}
