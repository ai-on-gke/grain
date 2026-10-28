package ui

// versionResponseFrom is unexported, so this is package ui rather than
// ui_test. The handler half -- that GET /api/config carries whatever
// this produces -- is server_test.go's TestConfigReportsTheBuildVersion.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/version"
)

func TestVersionResponseFromCarriesTheWholeStamp(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 3, 14, 2, 11, 0, time.UTC)
	got := versionResponseFrom(version.Info{Revision: "0fbfb4619f0a", Release: "v1.2.0", Time: at, Modified: true})

	if got == nil {
		t.Fatalf("versionResponseFrom = nil for a stamped build, want a response")
	}
	if got.Release != "v1.2.0" {
		t.Errorf("release = %q, want v1.2.0", got.Release)
	}
	if got.Commit != "0fbfb4619f0a" {
		t.Errorf("commit = %q, want the full hash unshortened", got.Commit)
	}
	if got.CommittedAt == nil || !got.CommittedAt.Equal(at) {
		t.Errorf("committedAt = %v, want %v", got.CommittedAt, at)
	}
	if !got.Modified {
		t.Errorf("modified = false, want true")
	}
}

func TestVersionResponseFromCarriesEdition(t *testing.T) {
	t.Parallel()
	got := versionResponseFrom(version.Info{Release: "v2.0.0-google", Edition: "google", Revision: "0fbfb4619f0a"})
	if got == nil {
		t.Fatalf("versionResponseFrom = nil, want a response")
	}
	if got.Edition != "google" || got.Release != "v2.0.0-google" {
		t.Errorf("versionResponseFrom = %+v, want edition=google release=v2.0.0-google", got)
	}
	editionOnly := versionResponseFrom(version.Info{Edition: "google"})
	if editionOnly == nil || editionOnly.Edition != "google" {
		t.Errorf("versionResponseFrom(Edition: google) = %+v, want edition=google", editionOnly)
	}
}

func TestVersionResponseFromCarriesReleaseAlone(t *testing.T) {
	t.Parallel()
	got := versionResponseFrom(version.Info{Release: "v1.3.0-rc"})
	if got == nil || got.Release != "v1.3.0-rc" {
		t.Fatalf("versionResponseFrom = %+v, want release v1.3.0-rc", got)
	}
	if got.Commit != "" {
		t.Errorf("commit = %q, want empty", got.Commit)
	}
}

// An unstamped binary -- -buildvcs=false, or any test binary -- leaves
// the field off the response entirely rather than sending a version
// object of empty strings for the frontend to have to recognise.
func TestVersionResponseFromOmitsAnUnstampedBuild(t *testing.T) {
	t.Parallel()
	if got := versionResponseFrom(version.Info{}); got != nil {
		t.Fatalf("versionResponseFrom = %+v for an unstamped build, want nil", got)
	}
}

// Half a stamp is still worth reporting: the commit rides even when no
// readable time came with it, and committedAt is then absent rather than
// the zero time, which reads as the year 1.
func TestVersionResponseFromOmitsAnAbsentTime(t *testing.T) {
	t.Parallel()
	got := versionResponseFrom(version.Info{Revision: "abc1234"})
	if got == nil || got.Commit != "abc1234" {
		t.Fatalf("versionResponseFrom = %+v, want the commit reported", got)
	}
	if got.CommittedAt != nil {
		t.Errorf("committedAt = %v with no stamped time, want nil", got.CommittedAt)
	}

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("unmarshalling %s: %v", body, err)
	}
	if _, ok := fields["committedAt"]; ok {
		t.Errorf("committedAt is present in %s, want the key absent", body)
	}
	if _, ok := fields["modified"]; ok {
		t.Errorf("modified is present in %s for an unmodified build, want the key absent", body)
	}
}
