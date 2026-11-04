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

package nsclient

import (
	"net/url"
	"testing"
)

func TestPathSplitsAnInlineQueryAndEscapesEachSegment(t *testing.T) {
	for _, tc := range []struct {
		prefix, uri, path, query string
	}{
		{SandboxPrefix, "ci://acme/widgets/run-1", "/fs/ci/acme/widgets/run-1", ""},
		{APIPrefix, "ci://acme/widgets/run-1/log?grep=FAIL&n=1", "/api/fs/ci/acme/widgets/run-1/log", "grep=FAIL&n=1"},
		{APIPrefix, "task://", "/api/fs/task", ""},
		{SandboxPrefix, "memory://repos/acme/a b.md/", "/fs/memory/repos/acme/a%20b.md", ""},
	} {
		path, inline, err := Path(tc.prefix, tc.uri)
		if err != nil {
			t.Fatalf("Path(%q): %v", tc.uri, err)
		}
		if path != tc.path || EncodeQuery(inline) != tc.query {
			t.Errorf("Path(%q, %q) = %q ?%s, want %q ?%s", tc.prefix, tc.uri, path, EncodeQuery(inline), tc.path, tc.query)
		}
	}
	if _, _, err := Path(SandboxPrefix, "not-a-uri"); err == nil {
		t.Error("Path took a URI with no scheme")
	}
	// A flag wins over the same parameter written into the URI, and a
	// bare one is sent bare.
	got := EncodeQuery(Merge(url.Values{"tail": {"10"}, "grep": {"x"}}, url.Values{"tail": {"99"}, "list": {""}}))
	if want := "grep=x&list&tail=99"; got != want {
		t.Errorf("Merge = %q, want %q", got, want)
	}
}
