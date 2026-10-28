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

package ci

import (
	"strings"
	"testing"
)

// The parser moved to pkg/workflows, which may not import pkg/model, so
// grain's capability grammar reaches it through workflows.Capabilities,
// installed by this package's init. These pin that every grain binary --
// all of which link pkg/ci -- still parses `capabilities:` the way it did
// before the move: a malformed reference is refused at parse, and a
// reference grant with arguments decodes.

func TestParseRefusesAMalformedCapabilityReference(t *testing.T) {
	_, err := Parse(".grain/workflows/a.yml", []byte("on: push\njobs:\n  one:\n    capabilities:\n      - https://example.com/capabilities/gcp/#gcp-key\n    steps:\n      - run: make test\n"))
	if err == nil || !strings.Contains(err.Error(), "capabilities[0]") {
		t.Fatalf("a malformed capability reference parsed: %v", err)
	}
}

func TestParseDecodesACapabilityGrantWithArguments(t *testing.T) {
	w := mustParse(t, ".grain/workflows/a.yml", "on: push\njobs:\n  one:\n    capabilities:\n      - capability: https://example.com/capabilities/gcp#gcp-key\n        with: {account: infra-staging}\n    steps:\n      - run: make test\n")
	got := w.Jobs[0].Capabilities
	if len(got) != 1 || !strings.HasPrefix(got[0], "https://example.com/capabilities/gcp#gcp-key") || !strings.Contains(got[0], "infra-staging") {
		t.Fatalf("capabilities = %q, want the grant with its arguments", got)
	}
}
