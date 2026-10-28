package releasenotes

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestParseReadsTheCutsHeader(t *testing.T) {
	md := "# v1.8.0\n\nReleased 2026-10-11 from commit `0ca8da306`. Changes since `v1.7.0` (`a9469b26f`):\nhttps://example/compare\n\n## Features\n"
	got := Parse("v1.8.0.md", []byte(md))
	want := Version{Version: "v1.8.0", Title: "v1.8.0", Date: "2026-10-11", Commit: "0ca8da306"}
	if got.Version != want {
		t.Errorf("Parse = %+v, want %+v", got.Version, want)
	}
	if got.Markdown != md {
		t.Errorf("Markdown was not kept whole: %q", got.Markdown)
	}
}

// Notes written by hand, which the cut leaves alone
// (tests/deploy/release_notes_test.go), carry no header line at all.
func TestParseToleratesNotesWithoutAHeader(t *testing.T) {
	got := Parse("v1.1.0.md", []byte("# Grain 1.1\n\nWritten by hand.\n"))
	want := Version{Version: "v1.1.0", Title: "Grain 1.1"}
	if got.Version != want {
		t.Errorf("Parse = %+v, want %+v", got.Version, want)
	}
	got = Parse("v1.1.0.md", []byte("No title at all.\n# Later heading\n"))
	if got.Title != "v1.1.0" {
		t.Errorf("untitled notes took title %q, want the version", got.Title)
	}
}

func TestLoadListsVersionsNewestFirstAndSkipsTheReadme(t *testing.T) {
	fsys := fstest.MapFS{
		"README.md":       {Data: []byte("# Releases\n")},
		"v1.2.0.md":       {Data: []byte("# v1.2.0\n")},
		"v1.10.0.md":      {Data: []byte("# v1.10.0\n")},
		"v1.9.0.md":       {Data: []byte("# v1.9.0\n")},
		"v1.10.0-rc.1.md": {Data: []byte("# rc\n")},
	}
	notes, err := load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range notes {
		got = append(got, n.Version.Version)
	}
	want := []string{"v1.10.0", "v1.10.0-rc.1", "v1.9.0", "v1.2.0"}
	if !slices.Equal(got, want) {
		t.Errorf("load = %v, want %v", got, want)
	}
}

// The binary carries the notes this repository has: v1.8.0, the release
// an operator went looking for, and the ones before it.
func TestTheBinaryCarriesThisRepositorysNotes(t *testing.T) {
	list := List()
	if len(list) == 0 {
		t.Fatal("no release notes embedded")
	}
	var names []string
	for _, v := range list {
		names = append(names, v.Version)
		if v.Version == "README" {
			t.Error("README.md was embedded as a release")
		}
	}
	for _, want := range []string{"v1.8.0", "v1.2.0"} {
		if !slices.Contains(names, want) {
			t.Errorf("List lacks %s: %v", want, names)
		}
	}
	for i := 1; i < len(list); i++ {
		if Compare(list[i-1].Version, list[i].Version) <= 0 {
			t.Errorf("List is not newest first: %v", names)
		}
	}
	n, ok := Get("v1.8.0")
	if !ok {
		t.Fatal("Get(v1.8.0) found nothing")
	}
	if n.Date != "2026-10-11" || !strings.HasPrefix(n.Markdown, "# v1.8.0") {
		t.Errorf("Get(v1.8.0) = %+v", n.Version)
	}
	if _, ok := Get("1.8.0"); !ok {
		t.Error("Get(1.8.0) did not accept a version without its v")
	}
	if _, ok := Get("v0.0.1"); ok {
		t.Error("Get found notes for a release that has none")
	}
}

func TestBetweenIsWhatAnUpgradeBrings(t *testing.T) {
	var notes []Note
	for _, v := range []string{"v1.8.0", "v1.7.0", "v1.6.0", "v1.5.0"} {
		notes = append(notes, Note{Version: Version{Version: v}})
	}
	versions := func(ns []Note) []string {
		var out []string
		for _, n := range ns {
			out = append(out, n.Version.Version)
		}
		return out
	}
	for _, tc := range []struct {
		from, to string
		want     []string
	}{
		{"v1.6.0", "", []string{"v1.8.0", "v1.7.0"}},
		{"v1.6.0", "v1.7.0", []string{"v1.7.0"}},
		{"", "v1.6.0", []string{"v1.6.0", "v1.5.0"}},
		{"", "", []string{"v1.8.0", "v1.7.0", "v1.6.0", "v1.5.0"}},
		// A candidate of v1.7.0 has not had v1.7.0 yet.
		{"v1.7.0-rc.3", "", []string{"v1.8.0", "v1.7.0"}},
		{"v1.7.0-rc.3-google", "", []string{"v1.8.0", "v1.7.0"}},
		{"v1.7.0-google", "", []string{"v1.8.0"}},
		{"v1.7.0+google", "", []string{"v1.8.0"}},
		{"v1.6.0-google", "v1.7.0-google", []string{"v1.7.0"}},
		{"v1.8.0", "", nil},
		{"v1.8.0-google", "", nil},
		{"v1.8.0+google", "", nil},
		// A branch name is no bound at all.
		{"main", "", []string{"v1.8.0", "v1.7.0", "v1.6.0", "v1.5.0"}},
	} {
		if got := versions(Between(notes, tc.from, tc.to)); !slices.Equal(got, tc.want) {
			t.Errorf("Between(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestCompareAndRelease(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"v1.10.0", "v1.9.0", 1},
		{"v1.9.0", "v1.9.0-rc.2", 1},
		{"v1.9.0-rc.10", "v1.9.0-rc.2", 1},
		{"v1.9.0-rc.1", "v1.9.0-rc.1", 0},
		{"main", "v0.0.1", -1},
		{"1.2.0", "v1.2.0", 0},
		{"v2.0.0-google", "v2.0.0", 0},
		{"v2.0.0+google", "v2.0.0", 0},
		{"v2.0.0-google", "v2.0.0+google", 0},
		{"v2.0.0-oss", "v2.0.0", 0},
		{"v2.0.0-google", "v2.0.0-rc.1-google", 1},
		{"v2.0.0", "v2.0.0-rc.1-google", 1},
		{"v2.0.0-google", "v2.0.0-rc.1", 1},
		{"v2.0.0-rc.1-google", "v2.0.0-rc.1", 0},
		{"v2.0.0-rc.10-google", "v2.0.0-rc.2-google", 1},
		{"v2.0.0-google-rc.2", "v2.0.0-rc.1-google", 1},
		{"v2.0.0-google", "v1.9.0", 1},
	} {
		if got := Compare(tc.a, tc.b); got != tc.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
	for in, want := range map[string]string{
		"v1.9.0-rc.2":        "v1.9.0",
		"v1.8.0":             "v1.8.0",
		"v2.0.0-google":      "v2.0.0",
		"v2.0.0+google":      "v2.0.0",
		"v2.0.0-oss":         "v2.0.0",
		"v2.0.0-rc.1-google": "v2.0.0",
		"v2.0.0-rc.1+google": "v2.0.0",
		"main":               "",
		"":                   "",
	} {
		if got := Release(in); got != want {
			t.Errorf("Release(%q) = %q, want %q", in, got, want)
		}
	}
	if n, ok := Get("v1.8.0-google"); !ok || n.Version.Version != "v1.8.0" {
		t.Errorf("Get(v1.8.0-google) = (%+v, %v), want v1.8.0", n.Version, ok)
	}
}

// The build versions scripts/build-version.sh stamps order the way the
// branches they come from relate (docs/release-candidate-management.md,
// "Build versions"): a candidate's builds after its rc tag sit between
// that rc and its release, a main build sits above the release candidate
// is heading for and below the rc main will be cut as, and a patch
// branch's builds sit between the release they patch and the patch.
func TestBuildVersionsOrderTheWayTheBranchesRelate(t *testing.T) {
	ordered := []string{
		"v2.1.0",
		"v2.1.1-rc.0.3+3373f53", // release/v2.1.1, three picks on v2.1.0
		"v2.1.1",
		"v2.2.0-rc.1",
		"v2.2.0-rc.1.48+6f65669", // candidate, 48 commits past rc.1
		"v2.2.0-rc.1.49+0a1b2c3",
		"v2.2.0-rc.2",
		"v2.2.0-rc.2.1+1234567",
		"v2.2.0",
		"v2.3.0-dev.114+abc1234", // main, 114 commits past the v2.2.0 cut
		"v2.3.0-dev.115+0000000",
		"v2.3.0-rc.1",
		"v2.3.0",
		"v2.4.0-dev.0+abc1234",
		"v3.0.0-dev.1+abc1234", // main after RELEASE_MAJOR=3
		"v3.0.0-rc.1",
	}
	for i := range ordered {
		for j := range ordered {
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got := Compare(ordered[i], ordered[j]); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
	// Each is a version, of the release it is heading for.
	for v, release := range map[string]string{
		"v2.3.0-dev.114+abc1234": "v2.3.0",
		"v2.2.0-rc.1.48+6f65669": "v2.2.0",
		"v2.1.1-rc.0.3+3373f53":  "v2.1.1",
	} {
		if got := Release(v); got != release {
			t.Errorf("Release(%s) = %q, want %q", v, got, release)
		}
	}
}

// Semver ignores build metadata when ordering, so two builds that differ
// only in their +<sha7> compare equal. Saying which build it is, is the
// metadata's job; ordering is the rest's.
func TestBuildMetadataDoesNotOrder(t *testing.T) {
	if got := Compare("v2.3.0-dev.114+abc1234", "v2.3.0-dev.114+fedcba9"); got != 0 {
		t.Errorf("Compare of two dev.114 builds = %d, want 0", got)
	}
}
