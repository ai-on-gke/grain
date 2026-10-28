package upgrade

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/installtrack"
	"github.com/bwsalmon/grain/pkg/installtrack/installtracktest"
)

func TestParseImageOverride(t *testing.T) {
	const repo = installtracktest.Repository
	for _, c := range []struct {
		image string
		want  ImageOverride
	}{
		{repo, ImageOverride{Repository: repo}},
		{repo + ":main", ImageOverride{Repository: repo, Tag: "main"}},
		{repo + "@" + testDigest, ImageOverride{Repository: repo, Digest: testDigest}},
		{repo + ":v2.1.0@" + testDigest, ImageOverride{Repository: repo, Tag: "v2.1.0", Digest: testDigest}},
		{"localhost:5000/grain:main", ImageOverride{Repository: "localhost:5000/grain", Tag: "main"}},
		{"  " + repo + ":candidate\n", ImageOverride{Repository: repo, Tag: "candidate"}},
	} {
		got, err := ParseImageOverride(c.image)
		if err != nil || got != c.want {
			t.Errorf("ParseImageOverride(%q) = %+v, %v, want %+v", c.image, got, err, c.want)
		}
	}
	for _, bad := range []string{
		"",
		"grain",
		repo + ":",
		repo + "@sha256:short",
		repo + ":main; rm -rf /",
		"$(evil)/grain:main",
	} {
		if got, err := ParseImageOverride(bad); err == nil {
			t.Errorf("ParseImageOverride(%q) = %+v, want an error", bad, got)
		}
	}
}

func overrideUpgrader(t *testing.T, dir, pulled string) *Upgrader {
	t.Helper()
	return newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: installtracktest.Repository,
			Track:      installtracktest.Track,
			RefFile:    filepath.Join(dir, "image.env"),
			PullCmd:    []string{"sh", "-c", `printf '%s\n' "$1" >> ` + pulled, "sh"},
			RunCmd:     []string{"sh", "-c", "echo 7"},
			// The repository of whatever was asked, tag and digest both
			// stripped, at testDigest.
			DigestCmd: []string{"sh", "-c", `r="${1%@*}"; r="${r%:*}"; echo "$r@` + testDigest + `"`, "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
}

// An operator on a registered track can point an upgrade at an opt-in
// repository -- a known successor -- and the deployment then
// stays on it for later channel upgrades, without anything in the host's
// environment changing.
func TestStartImageSwitchesRepositoryAndRemembersIt(t *testing.T) {
	const ar = installtracktest.OptInRepository
	dir := t.TempDir()
	pulled := filepath.Join(dir, "pulled")
	refFile := filepath.Join(dir, "image.env")
	writeTestFile(t, refFile, "GRAIN_IMAGE="+installtracktest.Repository+":main\n")
	u := overrideUpgrader(t, dir, pulled)

	target, ok := u.ImageTarget()
	if !ok || target.Repository != installtracktest.Repository ||
		target.DefaultRepository != installtracktest.Repository {
		t.Fatalf("ImageTarget() = %+v, %v", target, ok)
	}
	if !strings.Contains(strings.Join(target.Repositories, " "), ar) {
		t.Errorf("ImageTarget().Repositories = %v, want %s offered", target.Repositories, ar)
	}

	if err := u.StartImage("main", ar); err != nil {
		t.Fatalf("StartImage: %v", err)
	}
	waitForPhase(t, u, PhaseOK)
	if got := readFile(t, pulled); got != ar+":main\n" {
		t.Errorf("pulled %q, want %s:main", got, ar)
	}
	if got, want := readFile(t, refFile), "GRAIN_IMAGE="+ar+":main@"+testDigest+"\n"; got != want {
		t.Errorf("ref file = %q, want %q", got, want)
	}
	if got := u.EffectiveRepository(); got != ar {
		t.Errorf("EffectiveRepository() after the switch = %q, want %q", got, ar)
	}
	if got := u.overrideRef(); got != "" {
		t.Errorf("override %q left behind after the upgrade", got)
	}
}

// A full reference names the build: its tag is the branch recorded, and
// a digest is pulled by digest.
func TestStartImagePullsAnExplicitReference(t *testing.T) {
	const repo = installtracktest.Repository
	for _, c := range []struct {
		name, image, wantPull, wantBranch string
	}{
		{"tag", repo + ":candidate", repo + ":candidate", "candidate"},
		{"digest", repo + "@" + testDigest, repo + "@" + testDigest, "main"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			pulled := filepath.Join(dir, "pulled")
			u := overrideUpgrader(t, dir, pulled)
			branch, ref, err := u.ValidateImage("main", c.image)
			if err != nil || branch != c.wantBranch || ref != c.wantPull {
				t.Fatalf("ValidateImage = %q, %q, %v, want %q, %q", branch, ref, err, c.wantBranch, c.wantPull)
			}
			if err := u.StartImage("main", c.image); err != nil {
				t.Fatalf("StartImage: %v", err)
			}
			status := waitForPhase(t, u, PhaseOK)
			if status.Branch != c.wantBranch {
				t.Errorf("status.Branch = %q, want %q", status.Branch, c.wantBranch)
			}
			if got := readFile(t, pulled); got != c.wantPull+"\n" {
				t.Errorf("pulled %q, want %q", got, c.wantPull)
			}
		})
	}
}

func TestStartImageRefusesUntrustedRepositoriesAndBranches(t *testing.T) {
	dir := t.TempDir()
	u := overrideUpgrader(t, dir, filepath.Join(dir, "pulled"))
	u.cfg.AllowedBranches = DefaultAllowedBranches
	if err := u.StartImage("main", "evil.example/grain:main"); err == nil {
		t.Error("StartImage onto evil.example/grain succeeded, want refused")
	}
	if err := u.StartImage("main", installtracktest.Repository+":some-branch"); !errors.Is(err, ErrBranchNotAllowed) {
		t.Errorf("StartImage onto a tag outside the allowlist = %v, want ErrBranchNotAllowed", err)
	}
	src := newUpgrader(t, Config{StatusFile: filepath.Join(dir, "s.json")})
	if err := src.StartImage("main", installtracktest.Repository); err == nil {
		t.Error("StartImage on the source pipeline succeeded, want refused")
	}
	if _, ok := src.ImageTarget(); ok {
		t.Error("ImageTarget on the source pipeline reported an image target")
	}
}

func TestRegistryHint(t *testing.T) {
	const ar = installtracktest.OptInRepository + ":main"
	for _, c := range []struct {
		err  string
		want string
	}{
		{"docker pull: exit status 1: GET https://oauth2.googleapis.com/token?x: status code 403: Unauthenticated",
			"refused this host's credentials for " + ar},
		{"denied: Permission \"artifactregistry.repositories.downloadArtifacts\" denied", "choose " + installtrack.DefaultOSSGrainRepository},
		{"manifest for " + ar + " not found: manifest unknown", ar + " has not been published to " + installtracktest.OptInRepository},
		{"404 Not Found", "has not been published"},
		{"exit status 1: no space left on device", ""},
		{"error pulling " + ar + "@sha256:a404b401", ""},
	} {
		got := registryHint(ar, errors.New(c.err))
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("registryHint(%q) = %q, want it to contain %q", c.err, got, c.want)
		}
	}
}

// A failed pull says which reference failed and what to do about it.
func TestStageImagePullFailureCarriesARegistryHint(t *testing.T) {
	dir := t.TempDir()
	u := overrideUpgrader(t, dir, filepath.Join(dir, "pulled"))
	u.cfg.Image.PullCmd = []string{"sh", "-c", `echo "manifest for $1 not found: manifest unknown" >&2; exit 1`, "sh"}
	if err := u.StartImage("main", installtracktest.OptInRepository); err != nil {
		t.Fatalf("StartImage: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	want := installtracktest.OptInRepository + ":main has not been published"
	if !strings.Contains(status.Detail, want) {
		t.Errorf("status.Detail = %q, want it to contain %q", status.Detail, want)
	}
}

func init() { installtracktest.Register() }
