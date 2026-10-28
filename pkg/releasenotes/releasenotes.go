// Package releasenotes reads the per-version release notes this binary
// carries (docs/releases, embedded by that directory's own package):
// which versions there are, when each was released, and what each one's
// notes say.
//
// They are read out of the binary rather than out of bwsalmon/grain so
// that a deployment shows them whether or not it can reach the
// repository, and so that a build answers for exactly the notes it was
// built with -- which is what the upgrade pane asks a target image for
// (`grain release-notes --from <running> --json`, pkg/upgrade).
//
// These are not the weekly notes the release-notes job writes about a
// deployment's own work (model.ReleaseNote, pkg/ui/releasenotes.go):
// those are per deployment and per week, these are per grain release.
package releasenotes

import (
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	releases "github.com/bwsalmon/grain/docs/releases"
)

// Version is one release's entry in the list: what it is called and,
// when its notes say so, when and from which commit it was cut.
type Version struct {
	// Version is the release tag, "v1.8.0" -- the file's own name.
	Version string `json:"version"`
	// Title is the notes' first heading, or Version when they have none.
	Title string `json:"title"`
	// Date is the YYYY-MM-DD the header's "Released ... from commit"
	// line names, or "" for notes written without one (by hand, say).
	Date string `json:"date,omitempty"`
	// Commit is the commit that line names, as abbreviated there.
	Commit string `json:"commit,omitempty"`
}

// Note is one release's whole notes.
type Note struct {
	Version
	Markdown string `json:"markdown"`
}

// Report is what `grain release-notes --json` prints, and what an
// upgrade reads back out of the image it is about to run.
type Report struct {
	// Version is the release of the binary that answered
	// (pkg/version), "" when it carries no stamp -- a merge's build.
	Version string `json:"version,omitempty"`
	// Notes is the notes asked for, newest first.
	Notes []Note `json:"notes"`
}

// headerLine is the line scripts/release-notes.sh writes under the
// title: "Released 2026-10-11 from commit `0ca8da306`. Changes since ...".
var headerLine = regexp.MustCompile("(?m)^Released (\\d{4}-\\d{2}-\\d{2}) from commit `?([0-9A-Fa-f]+)`?")

// Parse reads one release's notes. name is the file name ("v1.8.0.md")
// or the version itself; the version is taken from it rather than from
// the title, since the title is free text and the name is the tag.
func Parse(name string, data []byte) Note {
	md := string(data)
	v := strings.TrimSuffix(path.Base(name), ".md")
	n := Note{Version: Version{Version: v, Title: v}, Markdown: md}
	for _, line := range strings.Split(md, "\n") {
		line = strings.TrimSpace(line)
		if title, ok := strings.CutPrefix(line, "# "); ok {
			if title = strings.TrimSpace(title); title != "" {
				n.Title = title
			}
			break
		}
		if line != "" {
			// Text before any heading: the notes have no title line.
			break
		}
	}
	if m := headerLine.FindStringSubmatch(md); m != nil {
		n.Date, n.Commit = m[1], m[2]
	}
	return n
}

// load reads every note in fsys, newest version first. Files whose
// name is not a version (README.md, were it ever embedded) are skipped.
func load(fsys fs.FS) ([]Note, error) {
	names, err := fs.Glob(fsys, "*.md")
	if err != nil {
		return nil, err
	}
	var notes []Note
	for _, name := range names {
		if _, ok := parseVersion(strings.TrimSuffix(name, ".md")); !ok {
			continue
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		notes = append(notes, Parse(name, data))
	}
	sort.SliceStable(notes, func(i, j int) bool { return Compare(notes[i].Version.Version, notes[j].Version.Version) > 0 })
	return notes, nil
}

// embedded is this binary's own notes, read once: they cannot change
// while the process lives. An embedded FS that will not read is a broken
// build, and is reported as having no notes rather than as a panic.
var embedded = sync.OnceValue(func() []Note {
	notes, _ := load(releases.Files)
	return notes
})

// All is every release's notes this binary carries, newest first.
func All() []Note {
	return append([]Note(nil), embedded()...)
}

// List is every release this binary carries notes for, newest first.
func List() []Version {
	notes := embedded()
	out := make([]Version, 0, len(notes))
	for _, n := range notes {
		out = append(out, n.Version)
	}
	return out
}

// Get is one release's notes, by tag ("v1.8.0"; "1.8.0" and
// edition-qualified "v1.8.0-google" are accepted too).
func Get(v string) (Note, bool) {
	v = normalize(v)
	for _, n := range embedded() {
		if n.Version.Version == v {
			return n, true
		}
	}
	if p, ok := parseVersion(v); ok && len(p.pre) == 0 && p.edition != "" {
		base := Release(v)
		for _, n := range embedded() {
			if n.Version.Version == base {
				return n, true
			}
		}
	}
	return Note{}, false
}

// Between is the notes newer than from and no newer than to, newest
// first: what an upgrade from `from` onto `to` brings. Either bound may
// be "" -- or anything that is not a version, like a branch name -- to
// leave that side open.
func Between(notes []Note, from, to string) []Note {
	fromV, hasFrom := parseVersion(normalize(from))
	toV, hasTo := parseVersion(normalize(to))
	var out []Note
	for _, n := range notes {
		v, ok := parseVersion(n.Version.Version)
		if !ok {
			continue
		}
		if hasFrom && v.compare(fromV) <= 0 {
			continue
		}
		if hasTo && v.compare(toV) > 0 {
			continue
		}
		out = append(out, n)
	}
	return out
}

// Release is the release a version belongs to: its tag with any
// pre-release suffix dropped, so "v1.9.0-rc.2" is "v1.9.0". "" when v
// is not a version at all.
func Release(v string) string {
	p, ok := parseVersion(normalize(v))
	if !ok {
		return ""
	}
	return "v" + strconv.Itoa(p.nums[0]) + "." + strconv.Itoa(p.nums[1]) + "." + strconv.Itoa(p.nums[2])
}

// Compare orders two version tags the way semver does: -1, 0 or 1.
// Anything that is not a version sorts below everything that is, and
// two such compare by their text.
func Compare(a, b string) int {
	pa, okA := parseVersion(normalize(a))
	pb, okB := parseVersion(normalize(b))
	switch {
	case okA && okB:
		return pa.compare(pb)
	case okA:
		return 1
	case okB:
		return -1
	}
	return strings.Compare(a, b)
}

func normalize(v string) string {
	v = strings.TrimSpace(v)
	if v != "" && v[0] >= '0' && v[0] <= '9' {
		return "v" + v
	}
	return v
}

type parsedVersion struct {
	nums    [3]int
	pre     []string
	edition string
}

func isEdition(s string) bool {
	return s == "google" || s == "oss"
}

// parseVersion reads "vMAJOR.MINOR.PATCH[-PRERELEASE][-EDITION][+BUILD]".
// An edition qualifier (-google, +google, -oss, +oss) is separated from
// any pre-release suffix so that an edition-tagged release (v2.0.0-google)
// ranks equal to its base vX.Y.Z release rather than as a pre-release
// below it.
func parseVersion(v string) (parsedVersion, bool) {
	var p parsedVersion
	rest, ok := strings.CutPrefix(v, "v")
	if !ok {
		return p, false
	}
	rest, build, hasBuild := strings.Cut(rest, "+")
	if hasBuild {
		if build == "" {
			return p, false
		}
		if isEdition(build) {
			p.edition = build
		}
	}
	core, pre, hasPre := strings.Cut(rest, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return p, false
	}
	for i, s := range parts {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || s == "" {
			return p, false
		}
		p.nums[i] = n
	}
	if hasPre {
		if pre == "" {
			return p, false
		}
		if isEdition(pre) {
			p.edition = pre
			pre = ""
		} else if i := strings.LastIndexByte(pre, '-'); i > 0 && isEdition(pre[i+1:]) {
			p.edition = pre[i+1:]
			pre = pre[:i]
		} else if i := strings.IndexByte(pre, '-'); i > 0 && i+1 < len(pre) && isEdition(pre[:i]) {
			p.edition = pre[:i]
			pre = pre[i+1:]
		}
		if pre != "" {
			preParts := strings.Split(pre, ".")
			for _, part := range preParts {
				if part == "" {
					return p, false
				}
			}
			p.pre = preParts
		}
	}
	return p, true
}

func (a parsedVersion) compare(b parsedVersion) int {
	for i := range a.nums {
		if a.nums[i] != b.nums[i] {
			if a.nums[i] < b.nums[i] {
				return -1
			}
			return 1
		}
	}
	// A release outranks its own pre-releases.
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := comparePre(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.pre) < len(b.pre):
		return -1
	case len(a.pre) > len(b.pre):
		return 1
	}
	return 0
}

func comparePre(a, b string) int {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)
	switch {
	case errA == nil && errB == nil:
		if na < nb {
			return -1
		} else if na > nb {
			return 1
		}
		return 0
	case errA == nil:
		return -1
	case errB == nil:
		return 1
	}
	return strings.Compare(a, b)
}
