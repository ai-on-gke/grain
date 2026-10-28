package installtrack_test

import (
	"slices"
	"testing"

	"github.com/bwsalmon/grain/pkg/installtrack"
	"github.com/bwsalmon/grain/pkg/installtrack/installtracktest"
)

func init() { installtracktest.Register() }

func TestDetectFallsBackToOSS(t *testing.T) {
	for _, p := range []installtrack.Probe{
		{},
		{Hostname: "devbox.example.com"},
		{Hostname: installtracktest.HostSuffix},
		{UnitText: "ExecStart=/usr/local/bin/grain daemon -agy-path /usr/local/bin/agy\n"},
	} {
		if got := p.Detect(); got.Track != installtrack.TrackOSS || len(got.Signals) != 0 {
			t.Errorf("%+v.Detect() = %+v, want oss with no signals", p, got)
		}
	}
}

func TestDetectReportsARegisteredTracksSignals(t *testing.T) {
	got := (installtrack.Probe{Hostname: "Box" + installtracktest.HostSuffix + "."}).Detect()
	if got.Track != installtracktest.Track || !slices.Equal(got.Signals, []string{installtracktest.Signal}) {
		t.Fatalf("Detect() = %+v", got)
	}
	// A unit's -agy-path is read from the unit file's text.
	unit := "[Service]\nExecStart=/usr/local/bin/grain daemon \\\n  -agy-path " + installtracktest.AgyPath + " \\\n  -ui-allowed-hosts grain.example.com\n"
	got = (installtrack.Probe{UnitText: unit}).Detect()
	if got.Track != installtracktest.Track || !slices.Contains(got.Signals, installtracktest.SignalAgyPath) {
		t.Fatalf("Detect() from the unit = %+v", got)
	}
	if tracks := installtrack.Tracks(); tracks[0] != installtrack.TrackOSS || !slices.Contains(tracks, installtracktest.Track) {
		t.Errorf("Tracks() = %v", tracks)
	}
}

func TestRepositoryTransitionRules(t *testing.T) {
	t.Setenv("GRAIN_ALLOWED_IMAGE_REPOS", "")
	t.Setenv(installtrack.OSSImageRepoEnv, "")
	t.Setenv(installtracktest.RepositoryEnv, "")
	if got := installtrack.DefaultRepository(installtrack.TrackOSS); got != installtrack.DefaultOSSGrainRepository {
		t.Errorf("DefaultRepository(oss) = %q", got)
	}
	if got := installtrack.DefaultRepository(installtracktest.Track); got != installtracktest.Repository {
		t.Errorf("DefaultRepository(acme) = %q", got)
	}
	if got := installtrack.DefaultRepository("unknown"); got != installtrack.DefaultOSSGrainRepository {
		t.Errorf("DefaultRepository(unknown) = %q", got)
	}
	for _, c := range []struct {
		from, to string
		ok       bool
	}{
		{installtrack.LegacyGrainRepository, installtracktest.Repository, true},
		{installtrack.LegacyGrainRepository, installtracktest.MovedRepository, true},
		{installtracktest.Repository, installtracktest.OptInRepository, true},
		{installtracktest.OptInRepository, installtracktest.Repository, true},
		{installtrack.LegacyGrainRepository, installtrack.DefaultOSSGrainRepository, true},
		{installtrack.LegacyGrainRepository, "registry.example/corp-eng/grain", false},
		{installtrack.LegacyGrainRepository, "evil.example/grain", false},
		{"ghcr.io/someone-else/grain/grain", installtrack.DefaultOSSGrainRepository, false},
	} {
		if got := installtrack.CanTransitionRepository(c.from, c.to); got != c.ok {
			t.Errorf("CanTransitionRepository(%q, %q) = %v, want %v", c.from, c.to, got, c.ok)
		}
	}
	if !installtrack.CanTransitionRepository(installtrack.LegacyGrainRepository, "registry.example/corp-eng/grain", "registry.example/corp-eng/grain") {
		t.Error("an explicitly allowed successor repository was refused")
	}
}

func TestRepositoryForTrack(t *testing.T) {
	for _, c := range []struct {
		repo  string
		track installtrack.Track
		want  string
	}{
		{installtrack.LegacyGrainRepository, installtracktest.Track, installtracktest.Repository},
		{installtrack.LegacyGrainRepository, installtrack.TrackOSS, installtrack.DefaultOSSGrainRepository},
		{installtrack.LegacyGrainRepository, "", installtrack.LegacyGrainRepository},
		{installtracktest.MovedRepository, installtracktest.Track, installtracktest.Repository},
		{installtracktest.MovedRepository, installtrack.TrackOSS, installtracktest.MovedRepository},
		{installtracktest.OptInRepository, installtracktest.Track, installtracktest.OptInRepository},
		{"registry.example.com/grain", installtracktest.Track, "registry.example.com/grain"},
	} {
		if got := installtrack.RepositoryForTrack(c.repo, c.track); got != c.want {
			t.Errorf("RepositoryForTrack(%q, %q) = %q, want %q", c.repo, c.track, got, c.want)
		}
	}
}

func TestImageRepositoryHonoursTheTracksOverride(t *testing.T) {
	t.Setenv(installtrack.OSSImageRepoEnv, "registry.example/oss/grain")
	t.Setenv(installtracktest.RepositoryEnv, "registry.example/acme-mirror/grain")
	if got := installtrack.ImageRepository(installtrack.TrackOSS); got != "registry.example/oss/grain" {
		t.Errorf("ImageRepository(oss) = %q", got)
	}
	if got := installtrack.ImageRepository(installtracktest.Track); got != "registry.example/acme-mirror/grain" {
		t.Errorf("ImageRepository(acme) = %q", got)
	}
	if err := installtrack.ValidateTargetRepository("", "registry.example/acme-mirror/grain"); err != nil {
		t.Errorf("the track's override repository was refused: %v", err)
	}
}

func TestValidateTargetRepository(t *testing.T) {
	t.Setenv("GRAIN_ALLOWED_IMAGE_REPOS", "")
	t.Setenv(installtrack.OSSImageRepoEnv, "")
	t.Setenv(installtracktest.RepositoryEnv, "")
	for _, c := range []struct {
		from, to string
		ok       bool
	}{
		{installtracktest.Repository, installtracktest.OptInRepository, true},
		{"", installtracktest.Repository, true},
		{installtracktest.Repository, "evil.example/grain", false},
		{"", "evil.example/grain", false},
		{installtracktest.Repository, "", false},
	} {
		err := installtrack.ValidateTargetRepository(c.from, c.to)
		if (err == nil) != c.ok {
			t.Errorf("ValidateTargetRepository(%q, %q) = %v, want ok=%v", c.from, c.to, err, c.ok)
		}
	}
}
