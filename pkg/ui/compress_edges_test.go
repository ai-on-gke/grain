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

package ui_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// TestCIArtifactDownloadIsNotGzippedForAGzipCaller walks the edge the
// 2026-11-04 UI walkthrough could not reach on `grain demo -fake-ci`,
// which keeps no artifacts: a browser asking for a CI artifact always
// sends Accept-Encoding: gzip, and the artifact -- already a gzipped tar
// -- must reach it byte for byte, with its Content-Length, rather than
// wrapped in a second gzip layer. A Range request for it is passed
// through untouched as well.
func TestCIArtifactDownloadIsNotGzippedForAGzipCaller(t *testing.T) {
	t.Parallel()
	payload := strings.Repeat("a gzipped tar, pretend; ", 200) // well over gzipMinLength
	runner := &artifactRunner{
		available: true,
		held:      map[string]string{"run-1/ui-e2e/playwright-test-results": payload},
	}
	runner.runs = []ci.Run{{ID: "run-1", Repo: "acme/widgets", Jobs: []ci.JobRun{{JobID: "ui-e2e"}}}}
	srv, _ := ciServer(t, runner)

	const path = "/api/ci/runs/run-1/artifacts/ui-e2e/playwright-test-results"
	for _, headers := range []map[string]string{
		{"Accept-Encoding": "gzip, deflate, br"},
		{"Accept-Encoding": "gzip", "Range": "bytes=0-99"},
	} {
		rec := doWithHeaders(t, srv, http.MethodGet, path, "", headers)
		if rec.Code != http.StatusOK {
			t.Fatalf("%v: status = %d: %s", headers, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("%v: Content-Encoding = %q, want the artifact's own bytes", headers, got)
		}
		if got := rec.Body.String(); got != payload {
			t.Errorf("%v: body is %d bytes, want the %d-byte artifact unchanged", headers, len(got), len(payload))
		}
	}
}
