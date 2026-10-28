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

package workflows

import "testing"

func TestPathsMatchingIsGitHubsGlobbingPlusADirectoryPrefix(t *testing.T) {
	for _, tc := range []struct {
		pattern, file string
		want          bool
	}{
		{"docs/**", "docs/ci.md", true},
		{"docs/**", "docs/a/b/c.md", true},
		{"docs/**", "docsy/ci.md", false},
		{"docs/**", "pkg/docs/ci.md", false},
		{"docs", "docs/ci.md", true},   // the directory-prefix convenience
		{"docs/", "docs/ci.md", true},  // and the same with a slash
		{"docs", "docsy/ci.md", false}, // which is a segment boundary, not a prefix
		{"go.mod", "go.mod", true},
		{"go.mod", "v3/go.mod", false},
		{"**/*.go", "pkg/ci/plan.go", true},
		{"**/*.go", "main.go", true}, // `**/` is zero or more directories, as on GitHub
		{"docs/**/*.md", "docs/README.md", true},
		{"docs/**/*.md", "docs/a/b.md", true},
		{"docs/**/*.md", "docsy/b.md", false},
		{"**/docs/**", "docs/hello.md", true},
		{"**/docs/**", "dir/docs/my-file.txt", true},
		{"pkg/**/plan?.go", "pkg/ci/plan1.go", true}, // `?` is one character beside a `**` too
		{"pkg/**/plan?.go", "pkg/ci/plan?.go", true},
		{"pkg/**/plan?.go", "pkg/ci/plan/.go", false},
		{"*.go", "main.go", true},
		{"*.go", "pkg/main.go", false},
		{"", "anything", false},
	} {
		if got := matchPath(tc.pattern, tc.file); got != tc.want {
			t.Errorf("matchPath(%q, %q) = %v, want %v", tc.pattern, tc.file, got, tc.want)
		}
	}
}
