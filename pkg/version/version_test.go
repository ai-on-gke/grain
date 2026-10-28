package version

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
	"time"
)

func stamp(settings ...debug.BuildSetting) *debug.BuildInfo {
	return &debug.BuildInfo{Settings: settings}
}

// The whole reading, out of the three settings `go build -buildvcs` puts
// there, in the form cmd/go writes them.
func TestInfoFromReadsTheVCSStamp(t *testing.T) {
	got := infoFrom(stamp(
		debug.BuildSetting{Key: "-buildmode", Value: "exe"},
		debug.BuildSetting{Key: "vcs", Value: "git"},
		debug.BuildSetting{Key: "vcs.revision", Value: "0fbfb4619f0a1c2d3e4f5a6b7c8d9e0f11223344"},
		debug.BuildSetting{Key: "vcs.time", Value: "2026-09-03T14:02:11Z"},
		debug.BuildSetting{Key: "vcs.modified", Value: "false"},
	), true)

	// Nothing stamped a version into it, so it says it is unversioned.
	want := Info{
		Revision:    "0fbfb4619f0a1c2d3e4f5a6b7c8d9e0f11223344",
		Time:        time.Date(2026, 9, 3, 14, 2, 11, 0, time.UTC),
		Unversioned: true,
	}
	if got != want {
		t.Errorf("infoFrom = %+v, want %+v", got, want)
	}
}

// vcs.time is reported in UTC whatever offset it was stamped with, so a
// caller formatting it does not have to normalise first.
func TestInfoFromNormalisesTimeToUTC(t *testing.T) {
	got := infoFrom(stamp(debug.BuildSetting{Key: "vcs.time", Value: "2026-09-03T16:02:11+02:00"}), true)

	want := time.Date(2026, 9, 3, 14, 2, 11, 0, time.UTC)
	if !got.Time.Equal(want) || got.Time.Location() != time.UTC {
		t.Errorf("infoFrom time = %v (%v), want %v (UTC)", got.Time, got.Time.Location(), want)
	}
}

func TestInfoFromReportsAModifiedTree(t *testing.T) {
	got := infoFrom(stamp(
		debug.BuildSetting{Key: "vcs.revision", Value: "abc1234"},
		debug.BuildSetting{Key: "vcs.modified", Value: "true"},
	), true)

	if !got.Modified {
		t.Errorf("infoFrom modified = false for vcs.modified=true, want true")
	}
}

func TestInfoFromReadsReleaseTag(t *testing.T) {
	for name, tc := range map[string]struct {
		bi   *debug.BuildInfo
		want string
	}{
		"vcs.tag setting": {
			bi: &debug.BuildInfo{
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abc1234"},
					{Key: "vcs.tag", Value: "v1.2.0"},
				},
			},
			want: "v1.2.0",
		},
		"vcs.version setting": {
			bi: &debug.BuildInfo{
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abc1234"},
					{Key: "vcs.version", Value: "v1.3.0-rc"},
				},
			},
			want: "v1.3.0-rc",
		},
		"bi.Main.Version module version": {
			bi: &debug.BuildInfo{
				Main: debug.Module{
					Version: "v1.2.0",
				},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abc1234"},
				},
			},
			want: "v1.2.0",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := infoFrom(tc.bi, true)
			if got.Release != tc.want {
				t.Errorf("infoFrom release = %q, want %q", got.Release, tc.want)
			}
		})
	}
}

// An unstamped build -- -buildvcs=false, or any test binary -- is an
// empty Info, not an error and not a panic: every caller shows it as
// "unknown".
func TestInfoFromWithoutAStamp(t *testing.T) {
	for name, bi := range map[string]*debug.BuildInfo{
		"no build info at all": nil,
		"no vcs settings":      stamp(debug.BuildSetting{Key: "-buildmode", Value: "exe"}),
	} {
		t.Run(name, func(t *testing.T) {
			ok := bi != nil
			if got := infoFrom(bi, ok); got != (Info{}) {
				t.Errorf("infoFrom = %+v, want the zero Info", got)
			}
		})
	}
}

// A stamp whose time is unreadable still names its commit: the two are
// read independently, so a broken half does not cost the good one.
func TestInfoFromKeepsTheRevisionWhenTheTimeIsUnparseable(t *testing.T) {
	got := infoFrom(stamp(
		debug.BuildSetting{Key: "vcs.revision", Value: "abc1234"},
		debug.BuildSetting{Key: "vcs.time", Value: "not a timestamp"},
	), true)

	if got.Revision != "abc1234" || !got.Time.IsZero() {
		t.Errorf("infoFrom = %+v, want revision abc1234 and a zero time", got)
	}
}

// Get is whatever this test binary carries, which is nothing -- `go
// test` does not stamp VCS information. Worth asserting anyway: it must
// answer, repeatedly and identically, rather than panicking on the
// absence.
func TestGetIsStableAndSurvivesAnUnstampedBinary(t *testing.T) {
	if Get() != Get() {
		t.Errorf("Get returned two different answers in one process")
	}
}

// TestReadFileReadsTheStampOfABinaryOnDisk: the same reading as Get, of
// a file rather than of this process -- what pkg/upgrade's source path
// asks about the binary it installed, which is not the one answering
// (grain/task-50). The test binary stands in for it: `go test` stamps no
// VCS information at all, so this is also the "no stamp" case, which is
// an empty Info and no error rather than a failure.
func TestReadFileReadsTheStampOfABinaryOnDisk(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(self); err != nil {
		t.Errorf("ReadFile(%s) = %v, want a build stamp (or an empty one) and no error", self, err)
	}
}

// And the errors: a path with nothing at it, and a file that is not a Go
// binary. Both mean "cannot say what this deployment is on", which a
// caller shows as nothing at all -- so they must not come back looking
// like an unstamped build, which is a different answer.
func TestReadFileReportsWhatItCannotRead(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadFile(filepath.Join(dir, "grain")); err == nil {
		t.Error("ReadFile of a path with no file at it returned no error")
	}
	notABinary := filepath.Join(dir, "grain")
	if err := os.WriteFile(notABinary, []byte("#!/bin/sh\nexec grain \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(notABinary); err == nil {
		t.Error("ReadFile of a shell script returned no error")
	}
}

func TestInfoFromReadsEdition(t *testing.T) {
	savedEdition, savedRegistered := Edition, registeredEdition
	t.Cleanup(func() {
		Edition = savedEdition
		registeredEdition = savedRegistered
	})

	// 1. Registered via RegisterEdition (e.g. from google/google.go).
	Edition = ""
	RegisterEdition("google")
	got := infoFrom(stamp(
		debug.BuildSetting{Key: "vcs.revision", Value: "abc1234"},
		debug.BuildSetting{Key: "vcs.tag", Value: "v2.0.0-google"},
	), true)
	if got.Edition != "google" || got.Release != "v2.0.0-google" {
		t.Errorf("with RegisterEdition(\"google\"), infoFrom = %+v, want Edition=google Release=v2.0.0-google", got)
	}

	// 2. Explicit -ldflags -X on Edition overrides RegisterEdition.
	Edition = "oss"
	if got := infoFrom(nil, false); got.Edition != "oss" {
		t.Errorf("with Edition=\"oss\" override, infoFrom = %+v, want Edition=oss", got)
	}

	// 3. vcs.edition build setting when nothing was registered or stamped.
	Edition = ""
	RegisterEdition("")
	got = infoFrom(stamp(
		debug.BuildSetting{Key: "vcs.revision", Value: "abc1234"},
		debug.BuildSetting{Key: "vcs.edition", Value: "google"},
	), true)
	if got.Edition != "google" {
		t.Errorf("with vcs.edition=google, infoFrom = %+v, want Edition=google", got)
	}

	// 4. A registered main path in buildinfo (e.g. ReadFile on an
	// edition's main).
	RegisterMainPath("example.com/acme/grain/", "acme")
	defer delete(mainEditions, "example.com/acme/grain/")
	got = infoFrom(&debug.BuildInfo{
		Path: "example.com/acme/grain/cmd/grain",
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc1234"},
		},
	}, true)
	if got.Edition != "acme" {
		t.Errorf("for a registered main path, infoFrom = %+v, want Edition=acme", got)
	}
}

// A build version stamped through -ldflags is the version, and the build
// says it is versioned; one nothing stamped falls back to Go's module
// pseudo-version, as it always has, and says it is unversioned -- that
// pseudo-version names whatever tag Go found (v1.14.0-rc.1 on main for
// a while), so it is not where the build sits among releases.
func TestInfoFromSaysWhetherAVersionWasStamped(t *testing.T) {
	saved := Release
	t.Cleanup(func() { Release = saved })

	pseudo := &debug.BuildInfo{
		Main:     debug.Module{Version: "v1.14.0-rc.1.0.20261022220659-8e1a209040ea"},
		Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "8e1a209040ea9d32d432256b3c7796bfc297d289"}},
	}
	Release = ""
	if got := infoFrom(pseudo, true); got.Release != pseudo.Main.Version || !got.Unversioned {
		t.Errorf("an unstamped build = %+v, want Go's pseudo-version, reported as unversioned", got)
	}

	Release = "v2.3.0-dev.114+8e1a209"
	if got := infoFrom(pseudo, true); got.Release != Release || got.Unversioned {
		t.Errorf("a stamped build = %+v, want release %s and versioned", got, Release)
	}
}

// ReadFile reports the version a binary on disk was stamped with, read
// back out of the -ldflags its build info records -- not this process's
// own, which is a different build whenever an upgrade has installed one.
func TestReadFileReadsTheStampedVersionOutOfTheFilesOwnLdflags(t *testing.T) {
	saved := Release
	t.Cleanup(func() { Release = saved })
	Release = "v2.2.0-rc.1.48+6f65669"

	bi := stamp(
		debug.BuildSetting{Key: "-ldflags", Value: "-X github.com/ai-on-gke/grain/pkg/version.Release=v2.3.0-dev.114+8e1a209 -X github.com/ai-on-gke/grain/pkg/daemon.DefaultGrainImage=x"},
		debug.BuildSetting{Key: "vcs.revision", Value: "8e1a209040ea9d32d432256b3c7796bfc297d289"},
	)
	if got := infoFromStamp("", bi, true); got.Release != "v2.3.0-dev.114+8e1a209" || got.Unversioned {
		t.Errorf("the file's own stamp = %+v, want v2.3.0-dev.114+8e1a209, versioned", got)
	}
	if got := stampedRelease(stamp(debug.BuildSetting{Key: "-ldflags", Value: "-X=github.com/ai-on-gke/grain/pkg/version.Release=v2.2.0"})); got != "v2.2.0" {
		t.Errorf("stampedRelease of -X=... = %q, want v2.2.0", got)
	}
}
