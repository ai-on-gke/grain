package secretsrepo_test

// What the file is called, and why the name has the shape it has: a
// secrets repository is assumed to be shared -- with other grain
// deployments, and with whatever else an operator keeps encrypted
// material in -- so the name has to say that this is grain's, and which
// grain's.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/secretsrepo"
)

func TestFileNameNamesGrainAndTheStateRepository(t *testing.T) {
	for _, tc := range []struct {
		remote string
		want   string
	}{
		{"https://github.com/bwsalmon/grain-home", "grain-bwsalmon-grain-home.secrets.enc"},
		{"https://github.com/bwsalmon/grain-home.git", "grain-bwsalmon-grain-home.secrets.enc"},
		{"git@github.com:bwsalmon/grain-home.git", "grain-bwsalmon-grain-home.secrets.enc"},
		{"https://github.com/Bwsalmon/Grain-Home", "grain-bwsalmon-grain-home.secrets.enc"},
		// A local-only state repository names nothing, and every such
		// host lands on the same file: Settings.File is how two of them
		// sharing a repository tell each other apart.
		{"", "grain-local.secrets.enc"},
		{"not a url", "grain-local.secrets.enc"},
	} {
		if got := secretsrepo.FileName(tc.remote); got != tc.want {
			t.Errorf("FileName(%q) = %q, want %q", tc.remote, got, tc.want)
		}
	}
}

// Nothing an owner or a repository name can hold may turn into a path,
// or into a name a filesystem argues about.
func TestFileNameFlattensWhatARemoteCanHold(t *testing.T) {
	got := secretsrepo.FileName("https://example.com/an org/state repo")
	if want := "grain-an-org-state-repo.secrets.enc"; got != want {
		t.Errorf("FileName = %q, want %q", got, want)
	}
	if filepath.Base(got) != got {
		t.Errorf("FileName rendered something with a path in it: %q", got)
	}
}

func TestSettingsFileOverridesTheDerivedName(t *testing.T) {
	s := secretsrepo.Settings{File: "grain/production.enc"}
	if got := s.FileNameFor("https://github.com/owner/state"); got != "grain/production.enc" {
		t.Errorf("FileNameFor = %q", got)
	}
	var none secretsrepo.Settings
	if got := none.FileNameFor("https://github.com/owner/state"); got != "grain-owner-state.secrets.enc" {
		t.Errorf("FileNameFor with nothing configured = %q", got)
	}
}

// A data directory with no secrets-repo.json is a deployment that keeps
// its secrets on this host alone -- the state every install starts in,
// and not an error anybody has to handle.
func TestSettingsAbsentIsTheZeroValue(t *testing.T) {
	s, err := secretsrepo.LoadSettings(t.TempDir())
	if err != nil {
		t.Fatalf("loading settings that are not there: %v", err)
	}
	if s.Remote != "" {
		t.Errorf("a deployment with no settings file has a remote: %+v", s)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := secretsrepo.Settings{
		Remote:    "https://github.com/bwsalmon/secrets",
		Branch:    "main",
		TokenFile: filepath.Join(dir, "token"),
		File:      "grain-bwsalmon-grain-home.secrets.enc",
	}
	if err := secretsrepo.SaveSettings(dir, want); err != nil {
		t.Fatalf("saving: %v", err)
	}
	got, err := secretsrepo.LoadSettings(dir)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if got != want {
		t.Errorf("round trip gave %+v, want %+v", got, want)
	}
	info, err := os.Stat(filepath.Join(dir, secretsrepo.SettingsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the settings file is mode %v, not 0600", perm)
	}
}
