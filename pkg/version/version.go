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

// Package version answers "which build of grain is this?" out of the
// binary itself: the release version, the commit `go build` stamped into it,
// when that commit was made, and whether the tree it was built from was dirty.
//
// `go build -buildvcs=auto` -- what the Makefile, Dockerfile and CI all
// run (see the Makefile's own BUILDVCS comment for the one case that
// turns it off) -- records vcs.revision, vcs.time and vcs.modified in
// the binary's build info, and this reads them back. A release tag or
// version string can also be stamped via -ldflags or read from build info.
// So a deployment says what it is running without anyone having remembered
// to tell it, and a rollback to an older image reports that older commit rather
// than whatever the release notes claim, the same reasoning
// pkg/cli/sandboximage.go gives for stamping its sandbox tag in.
//
// Absence is normal and is reported as an empty Info rather than an
// error: a `go build -buildvcs=false` binary carries no stamp, and
// neither does any test binary -- `go test` does not stamp VCS
// information at all, which is why the reading is split out into
// infoFrom below and tested there rather than through Get.
package version

import (
	"debug/buildinfo"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// Release is stamped into the binary at build time via -ldflags -X
// (e.g. -X github.com/ai-on-gke/grain/pkg/version.Release=v1.2.0).
var Release string

// Edition is the build edition or variant (e.g. "google"), stamped into the
// binary at build time via -ldflags -X
// (e.g. -X github.com/ai-on-gke/grain/pkg/version.Edition=google) or
// registered via RegisterEdition. Empty for core/open-source builds.
var Edition string

var registeredEdition string

// RegisterEdition records the build edition linked into this binary
// (for example, "google" when google/ is imported). An explicit
// -ldflags -X on Edition takes precedence.
func RegisterEdition(edition string) {
	registeredEdition = edition
}

// mainEditions maps a main package path prefix to the edition a binary
// built from it is (RegisterMainPath).
var mainEditions = map[string]string{}

// RegisterMainPath records that a binary whose main package path starts
// with prefix is edition, for reading another binary's build info
// (ReadFile) that carries no stamp. Call it from init().
func RegisterMainPath(prefix, edition string) {
	mainEditions[prefix] = edition
}

func editionOfMain(path string) string {
	best, edition := "", ""
	for prefix, e := range mainEditions {
		if strings.HasPrefix(path, prefix) && len(prefix) > len(best) {
			best, edition = prefix, e
		}
	}
	return edition
}

func currentEdition() string {
	if Edition != "" {
		return Edition
	}
	return registeredEdition
}

// Info is one build's identity. The zero value means this binary carries
// no VCS stamp, which every caller has to be able to show as "unknown"
// rather than treat as a failure -- see the package comment.
type Info struct {
	// Revision is the full commit hash the binary was built from, or ""
	// when unstamped. Full rather than shortened: this is the value you
	// paste into `git show`, and a caller that wants seven characters can
	// take them, while a caller handed seven cannot get the rest back.
	Revision string
	// Release is the version this build carries, as semver: a promoted
	// release ("v2.2.0"), a candidate build ("v2.2.0-rc.1.48+abc1234"), a
	// main build ("v2.3.0-dev.114+abc1234") -- the scheme
	// scripts/build-version.sh stamps, docs/release-candidate-management.md's
	// "Build versions". "" when unstamped. When Unversioned is set it is
	// only what Go read off the module, not a version anything stamped.
	Release string
	// Unversioned says no version was stamped into this build -- a local
	// `go build`, or a build whose checkout had no tags to count from --
	// so Release, when set, is Go's own module pseudo-version
	// ("v1.14.0-rc.1.0.20261022220659-8e1a209040ea", named after whatever
	// tag Go found) rather than where this build sits among releases.
	// Never set for an Info with nothing in it at all.
	Unversioned bool
	// Edition is the build edition or variant (e.g. "google" when
	// google/ is linked in), or "" for the core/open-source build.
	Edition string
	// Time is that commit's own timestamp (vcs.time), in UTC -- not the
	// time of the build, which Go does not record. Zero when unstamped,
	// or when a stamp carried a time that would not parse.
	Time time.Time
	// Modified is true when the tree the build ran in had uncommitted
	// changes (vcs.modified), so Revision alone does not describe what is
	// running -- a laptop build against local edits, never a CI one.
	Modified bool
}

// Get reports this binary's own build stamp. Read once: the answer
// cannot change while the process lives, and GET /api/config asks for it
// on every poll of every open UI tab.
var Get = sync.OnceValue(func() Info {
	return infoFrom(debug.ReadBuildInfo())
})

// ReadFile reports the build stamp of a grain binary sitting on disk,
// out of the same three settings Get reads out of this process --
// without running it, so it costs a file read and cannot be affected by
// anything the binary would do if started.
//
// It exists for the one question this process cannot answer about
// itself: pkg/upgrade's source path installs a binary at
// Config.InstallPath and a restart brings *that* up, so "which build is
// this deployment on" is a question about that file rather than about
// whoever is answering the request (grain/task-50). A binary that
// carries no stamp reads back as an empty Info and no error, exactly as
// Get reports one -- the error is for a path that cannot be read or is
// not a Go binary at all.
func ReadFile(path string) (Info, error) {
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return Info{}, err
	}
	return infoFromStamp("", bi, true), nil
}

// releaseVar is the symbol the build's -ldflags stamp Release through.
const releaseVar = "github.com/ai-on-gke/grain/pkg/version.Release="

// stampedRelease is the Release a build's own -ldflags stamped, read back
// out of its build info ("-ldflags" is one of the settings `go build`
// records) -- which is how ReadFile tells the version of a binary on disk
// rather than reporting this process's own.
func stampedRelease(bi *debug.BuildInfo) string {
	for _, s := range bi.Settings {
		if s.Key != "-ldflags" {
			continue
		}
		fields := strings.Fields(s.Value)
		for i, f := range fields {
			arg := ""
			switch {
			case f == "-X" && i+1 < len(fields):
				arg = fields[i+1]
			case strings.HasPrefix(f, "-X="):
				arg = strings.TrimPrefix(f, "-X=")
			}
			if v, ok := strings.CutPrefix(strings.Trim(arg, `'"`), releaseVar); ok {
				return v
			}
		}
	}
	return ""
}

// infoFrom is Get's whole body, taking debug.ReadBuildInfo's pair as
// arguments so it can be tested against a stamp -- the test binary this
// package's own tests run in has none of its own.
func infoFrom(bi *debug.BuildInfo, ok bool) Info {
	return infoFromStamp(Release, bi, ok)
}

// infoFromStamp is infoFrom with the stamped release given: this
// process's own Release for Get, nothing for ReadFile, which reads the
// file's from its build info.
func infoFromStamp(release string, bi *debug.BuildInfo, ok bool) Info {
	info := Info{Release: release, Edition: currentEdition()}
	if !ok || bi == nil {
		return info
	}
	if info.Release == "" {
		info.Release = stampedRelease(bi)
	}
	stamped := info.Release != ""
	if info.Release == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		info.Release = bi.Main.Version
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Revision = s.Value
		case "vcs.time":
			// RFC3339, per cmd/go. A value that does not parse leaves
			// Time zero and the revision beside it intact: half a stamp
			// is still worth reporting.
			if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
				info.Time = t.UTC()
			}
		case "vcs.modified":
			info.Modified = s.Value == "true"
		case "vcs.tag", "vcs.version", "vcs.release":
			if info.Release == "" {
				info.Release = s.Value
			}
		case "vcs.edition":
			if info.Edition == "" {
				info.Edition = s.Value
			}
		}
	}
	if info.Edition == "" {
		info.Edition = editionOfMain(bi.Path)
	}
	info.Unversioned = !stamped && (info.Release != "" || info.Revision != "")
	return info
}
