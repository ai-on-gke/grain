package hostinit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// goReleaseFile is one file entry in go.dev's own release index
// (https://go.dev/dl/?mode=json&include=all) -- just the fields
// goTarballSHA256 reads.
type goReleaseFile struct {
	Filename string `json:"filename"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	SHA256   string `json:"sha256"`
	Kind     string `json:"kind"`
}

// goRelease is one version's entry in that same index.
type goRelease struct {
	Version string          `json:"version"`
	Stable  bool            `json:"stable"`
	Files   []goReleaseFile `json:"files"`
}

// GoReleaseIndexURL is go.dev's own machine-readable release list --
// fetched fresh on every ensureGo rather than pinning a digest in this
// source tree, so a Go version bump here never ships a stale check
// nobody remembered to move. include=all because a version already
// superseded by a later patch release is still "stable": false in the
// default listing, and ensureGo may be asked for an exact go.mod version
// that is not the newest in its line.
const GoReleaseIndexURL = "https://go.dev/dl/?mode=json&include=all"

// goTarballSHA256 reads index (GoReleaseIndexURL's own body) for the
// archive matching goVersion (e.g. "1.26.6", no "go" prefix), goos and
// goarch, and returns its filename and the sha256 go.dev itself
// published for it. An index with no matching entry is an error naming
// what was asked for, not a zero value a caller could mistake for "no
// verification needed".
func goTarballSHA256(index []byte, goVersion, goos, goarch string) (filename, sha256 string, err error) {
	var releases []goRelease
	if err := json.Unmarshal(index, &releases); err != nil {
		return "", "", fmt.Errorf("parsing go.dev's release index: %w", err)
	}
	want := "go" + goVersion
	for _, rel := range releases {
		if rel.Version != want {
			continue
		}
		for _, f := range rel.Files {
			if f.OS == goos && f.Arch == goarch && f.Kind == "archive" {
				if f.SHA256 == "" {
					return "", "", fmt.Errorf("go.dev's release index has no sha256 for %s (%s/%s)", want, goos, goarch)
				}
				return f.Filename, f.SHA256, nil
			}
		}
	}
	return "", "", fmt.Errorf("go.dev's release index has no %s archive for %s/%s", want, goos, goarch)
}

// goBuildVersion reads the Go version repoRoot/go.mod is built with:
// its `toolchain` line when it has one, its `go` directive when it does
// not -- the same rule the Makefile's GO_VERSION follows to pin a
// container build's toolchain, so a host and an image never disagree
// about which Go grain is built with. The toolchain line is where a
// security release is taken without raising the directive's floor, so
// reading only the directive would install the very patch release that
// line was added to move off (GSA1-13). ensureGo checks the host's own
// toolchain against it with goVersionAtLeast rather than string
// equality: a host is a thing people upgrade on their own schedule, and
// a toolchain newer than the one go.mod names already satisfies it, so
// treating "newer" as "missing" would make ensureGo reinstall Go on
// every already-current host.
func goBuildVersion(repoRoot string) (string, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("reading go.mod: %w", err)
	}
	if m := goToolchainRE.FindSubmatch(data); m != nil {
		return string(m[1]), nil
	}
	m := goDirectiveRE.FindSubmatch(data)
	if m == nil {
		return "", fmt.Errorf("go.mod has no `go` directive")
	}
	return string(m[1]), nil
}

var (
	goDirectiveRE = regexp.MustCompile(`(?m)^go[ \t]+(\S+)[ \t]*$`)
	goToolchainRE = regexp.MustCompile(`(?m)^toolchain[ \t]+go(\S+)[ \t]*$`)
)

// goVersionFromOutput extracts the version `go version`'s own output
// names -- "go version go1.26.6 linux/amd64" -> "1.26.6" -- so ensureGo
// can compare it to goBuildVersion without shelling out a second
// time for something narrower.
func goVersionFromOutput(output string) (string, bool) {
	m := goVersionOutputRE.FindStringSubmatch(output)
	if m == nil {
		return "", false
	}
	return m[1], true
}

var goVersionOutputRE = regexp.MustCompile(`\bgo(\d+\.\d+(?:\.\d+)?)\b`)

// goVersionAtLeast reports whether got (e.g. "1.27.1", as read off an
// installed toolchain) satisfies want (e.g. "1.26.6", go.mod's own `go`
// directive): component by component, numerically, so "1.27.1" reads as
// satisfying "1.26.6" rather than as a mismatch the way a plain string
// comparison would see it. A component want has that got lacks (want
// has a patch version, got does not) is treated as got having 0 there.
func goVersionAtLeast(got, want string) bool {
	gotParts := strings.Split(got, ".")
	wantParts := strings.Split(want, ".")
	for i := 0; i < len(wantParts); i++ {
		var g, w int
		if i < len(gotParts) {
			g, _ = strconv.Atoi(gotParts[i])
		}
		w, _ = strconv.Atoi(wantParts[i])
		if g != w {
			return g > w
		}
	}
	return true
}
