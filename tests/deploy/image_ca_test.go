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

package deploy

import (
	"regexp"
	"strings"
	"testing"
)

var dockerFromLine = regexp.MustCompile(`(?mi)^FROM\s`)

// TestPublishedServiceImagesDoNotTrustTheBuildCA guards the images grain
// publishes for running outside a grain sandbox -- the daemon and the
// registry. A release built inside a sandbox stages the deployment's
// TLS-intercepting proxy CA as .grain-ca.crt so the build stage can reach
// the network; the runtime stage must install its own trust store and
// never inherit that CA, or every published image would trust whoever
// holds the deployment's key. (The sandbox images are built to run behind
// that proxy and are deliberately not listed here.)
func TestPublishedServiceImagesDoNotTrustTheBuildCA(t *testing.T) {
	for _, name := range []string{"Dockerfile", "Dockerfile.hub", "Dockerfile.registry"} {
		code := stripComments(read(t, name))
		froms := dockerFromLine.FindAllStringIndex(code, -1)
		if len(froms) < 2 {
			t.Fatalf("%s: expected a multi-stage build, found %d FROM lines", name, len(froms))
		}
		final := code[froms[len(froms)-1][0]:]
		if strings.Contains(final, "grain-ca") {
			t.Errorf("%s: the final (published) stage mentions grain-ca; the build CA belongs to the build stage only:\n%s", name, final)
		}
		for _, line := range strings.Split(final, "\n") {
			l := strings.ToLower(line)
			if strings.Contains(l, "copy") && strings.Contains(l, "--from=") &&
				(strings.Contains(l, "ca-certificates") || strings.Contains(l, "/etc/ssl")) {
				t.Errorf("%s: the final stage copies a trust store from an earlier stage, which carries the build CA: %q", name, strings.TrimSpace(line))
			}
		}
	}
}
