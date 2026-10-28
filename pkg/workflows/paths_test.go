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
