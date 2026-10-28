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

package e2e

import (
	"testing"
)

// buildGrainBinary compiles cmd/grain so a live run's agy can fork its
// "mcpserver" subcommand to reach the sandbox -- the same binary a real
// deployment builds once and reuses across every dispatch.
//
// Only the live tests in this package need it. A scripted run
// (antigravity.NewForTest) stands up the same MCP tools in-process and
// forks nothing, which is why the rest of this suite needs no binary on
// disk at all.
//
// It is the same build cli_test.go's buildGrainCLI wants, so it is the
// same build: one per test binary, for the reason that one's own doc
// comment gives.
func buildGrainBinary(t *testing.T) string {
	t.Helper()
	return buildGrainCLI(t)
}
