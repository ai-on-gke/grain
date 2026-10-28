// Package releases carries this directory's per-version release notes
// into the grain binary, so a deployment can show what each version
// brought without reaching bwsalmon/grain (pkg/releasenotes reads them).
//
// It lives here rather than beside its reader because go:embed cannot
// reach out of its own package directory, and the notes have to stay
// where the release-notes task commits them (README.md).
package releases

import "embed"

// Files is every v*.md in this directory -- one per release -- and
// nothing else: README.md describes the directory and is no release.
//
//go:embed v*.md
var Files embed.FS
