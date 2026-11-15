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

package upgrade

// grain/task-4153: a deployment on a registered install track was
// upgraded onto the open-source image from the Upgrade pane, while still
// on its own track, and had to be upgraded back -- three restarts, each
// losing the runs in flight, and the edition's capability servers gone
// in between. These hold the line: no cross-edition target is offered or
// accepted without the explicit "switch edition" override, the override
// works and is recorded, the log names the repository actually pulled,
// and a repository that cannot be pulled is never swapped for another.
//
// "acme" (installtracktest) stands in for a registered edition's track.

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/installtrack/installtracktest"
)

const ossRepo = installtrack.DefaultOSSGrainRepository

// editionUpgrader is an image upgrader on track whose pulled image says
// it is edition imageEdition when asked `version --json` (EditionArgs),
// and answers every other question in the run the way overrideUpgrader's
// does.
func editionUpgrader(t *testing.T, dir, pulled string, track installtrack.Track, repo, imageEdition string) *Upgrader {
	t.Helper()
	answer := `{"release": "v9.9.9"}`
	if imageEdition != "" {
		answer = `{"release": "v9.9.9", "edition": "` + imageEdition + `"}`
	}
	return newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: repo,
			Track:      track,
			RefFile:    filepath.Join(dir, "image.env"),
			PullCmd:    []string{"sh", "-c", `printf '%s\n' "$1" >> ` + pulled, "sh"},
			// $1 is the image, $2 on the question asked of it.
			RunCmd:      []string{"sh", "-c", `shift; if [ "$1" = version ]; then printf '%s\n' '` + answer + `'; else echo 7; fi`, "sh"},
			DigestCmd:   []string{"sh", "-c", `r="${1%@*}"; r="${r%:*}"; echo "$r@` + testDigest + `"`, "sh"},
			EditionArgs: []string{"version", "--json"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
}

func pulledLines(t *testing.T, pulled string) []string {
	t.Helper()
	data, err := os.ReadFile(pulled)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(data))
}

func TestRepositoryTrackClassifiesEditions(t *testing.T) {
	for _, c := range []struct {
		repo string
		want installtrack.Track
		ok   bool
	}{
		{ossRepo, installtrack.TrackOSS, true},
		{"grainai/grain", installtrack.TrackOSS, true},
		{"index.docker.io/grainai/grain", installtrack.TrackOSS, true},
		// The pre-split repository carries the core build now, even
		// though acme lists it among the repositories it moves off.
		{installtrack.LegacyGrainRepository, installtrack.TrackOSS, true},
		{installtracktest.Repository, installtracktest.Track, true},
		{installtracktest.OptInRepository, installtracktest.Track, true},
		{installtracktest.MovedRepository, installtracktest.Track, true},
		{"registry.example/someone/else", "", false},
		{"grain-acme/grain", "", false},
	} {
		got, ok := installtrack.RepositoryTrack(c.repo)
		if got != c.want || ok != c.ok {
			t.Errorf("RepositoryTrack(%q) = %q, %v, want %q, %v", c.repo, got, ok, c.want, c.ok)
		}
	}
}

// A deployment on a registered track is not offered the open-source
// repository among its choices, and is refused it when it names it
// anyway -- before anything is pulled.
func TestRegisteredTrackRefusesTheOpenSourceImage(t *testing.T) {
	dir := t.TempDir()
	pulled := filepath.Join(dir, "pulled")
	u := editionUpgrader(t, dir, pulled, installtracktest.Track, installtracktest.Repository, "acme")

	target, ok := u.ImageTarget()
	if !ok {
		t.Fatal("ImageTarget() reported no image target")
	}
	if slices.Contains(target.Repositories, ossRepo) {
		t.Errorf("ImageTarget().Repositories = %v, offers %s to an acme deployment", target.Repositories, ossRepo)
	}
	if !slices.Contains(target.Repositories, installtracktest.OptInRepository) {
		t.Errorf("ImageTarget().Repositories = %v, want acme's own opt-in %s still offered", target.Repositories, installtracktest.OptInRepository)
	}
	if !slices.Contains(target.OtherEditionRepositories, EditionRepository{Repository: ossRepo, Edition: "oss"}) {
		t.Errorf("ImageTarget().OtherEditionRepositories = %v, want %s listed as oss for the override", target.OtherEditionRepositories, ossRepo)
	}
	if target.Edition != "acme" {
		t.Errorf("ImageTarget().Edition = %q, want acme", target.Edition)
	}
	losses := strings.Join(target.EditionSwitchLosses, "\n")
	for _, want := range []string{"capability servers", "acme install track's defaults", installtracktest.Repository} {
		if !strings.Contains(losses, want) {
			t.Errorf("EditionSwitchLosses = %q, want it to name %q", losses, want)
		}
	}

	for _, image := range []string{ossRepo, ossRepo + ":main", installtrack.LegacyGrainRepository + ":main"} {
		err := u.StartImage("main", image)
		if !errors.Is(err, ErrCrossEdition) {
			t.Errorf("StartImage(%q) = %v, want ErrCrossEdition", image, err)
		} else if !strings.Contains(err.Error(), "switch edition") || !strings.Contains(err.Error(), installtracktest.Repository) {
			t.Errorf("StartImage(%q) = %v, want the refusal to name this track's repository and the override", image, err)
		}
	}
	if got := pulledLines(t, pulled); len(got) != 0 {
		t.Errorf("refused upgrades pulled %v", got)
	}
}

// And the reverse: an open-source deployment is not offered, and is
// refused, a registered edition's repository.
func TestOpenSourceTrackRefusesARegisteredEditionsImage(t *testing.T) {
	dir := t.TempDir()
	pulled := filepath.Join(dir, "pulled")
	u := editionUpgrader(t, dir, pulled, installtrack.TrackOSS, ossRepo, "")

	target, _ := u.ImageTarget()
	for _, repo := range []string{installtracktest.Repository, installtracktest.OptInRepository} {
		if slices.Contains(target.Repositories, repo) {
			t.Errorf("ImageTarget().Repositories = %v, offers acme's %s to an oss deployment", target.Repositories, repo)
		}
		if !slices.Contains(target.OtherEditionRepositories, EditionRepository{Repository: repo, Edition: "acme"}) {
			t.Errorf("ImageTarget().OtherEditionRepositories = %v, want %s listed as acme", target.OtherEditionRepositories, repo)
		}
	}
	if err := u.StartImage("main", installtracktest.Repository+":main"); !errors.Is(err, ErrCrossEdition) {
		t.Errorf("StartImage onto acme's image = %v, want ErrCrossEdition", err)
	}
	if got := pulledLines(t, pulled); len(got) != 0 {
		t.Errorf("a refused upgrade pulled %v", got)
	}
}

// An -upgrade-image naming another edition's repository is refused by
// the plain Upgrade button (and by a schedule's validation), not pulled.
func TestStartRefusesAConfiguredRepositoryOfAnotherEdition(t *testing.T) {
	dir := t.TempDir()
	pulled := filepath.Join(dir, "pulled")
	u := editionUpgrader(t, dir, pulled, installtracktest.Track, ossRepo, "")
	if err := u.Start("main"); !errors.Is(err, ErrCrossEdition) {
		t.Errorf("Start = %v, want ErrCrossEdition", err)
	}
	if _, err := u.ValidateBranch("main"); !errors.Is(err, ErrCrossEdition) {
		t.Errorf("ValidateBranch = %v, want ErrCrossEdition", err)
	}
	if got := pulledLines(t, pulled); len(got) != 0 {
		t.Errorf("a refused upgrade pulled %v", got)
	}
}

// A ref file left naming another edition's image -- an earlier switch,
// or the mistaken pick this task is about -- does not carry the next
// ordinary upgrade on in that edition: it goes back to the track's own
// repository, which is what the pane then says it will pull.
func TestARefFileInAnotherEditionDoesNotStick(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "image.env"), "GRAIN_IMAGE="+ossRepo+":main@"+testDigest+"\n")
	u := editionUpgrader(t, dir, filepath.Join(dir, "pulled"), installtracktest.Track, installtracktest.Repository, "acme")
	if got := u.EffectiveRepository(); got != installtracktest.Repository {
		t.Errorf("EffectiveRepository() = %q, want %q (the track's own)", got, installtracktest.Repository)
	}
}

// The override: an explicit edition switch pulls the other edition's
// image, and is recorded -- on the status the pane reads and in the log.
func TestSwitchEditionOverrideIsAllowedAndRecorded(t *testing.T) {
	dir := t.TempDir()
	pulled := filepath.Join(dir, "pulled")
	u := editionUpgrader(t, dir, pulled, installtracktest.Track, installtracktest.Repository, "")
	journal := logToTest(t)

	err := u.StartImageWith("main", ossRepo, ImageOptions{SwitchEdition: true, RequestedBy: "operator alice"})
	if err != nil {
		t.Fatalf("StartImageWith(SwitchEdition) = %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)
	waitForIdle(t, u)
	if got := pulledLines(t, pulled); !slices.Equal(got, []string{ossRepo + ":main"}) {
		t.Errorf("pulled %v, want exactly %s:main", got, ossRepo)
	}
	want := EditionSwitch{From: "acme", To: "oss", Repository: ossRepo, RequestedBy: "operator alice"}
	if status.EditionSwitch == nil || *status.EditionSwitch != want {
		t.Errorf("status.EditionSwitch = %+v, want %+v", status.EditionSwitch, want)
	}
	if got := readFile(t, filepath.Join(dir, "image.env")); got != "GRAIN_IMAGE="+ossRepo+":main@"+testDigest+"\n" {
		t.Errorf("ref file = %q, want it to name the switched-to image", got)
	}
	log := journal.String()
	for _, line := range []string{
		"switching edition from acme to oss at operator alice's explicit request, pulling " + ossRepo + ":main",
		"pulling from " + ossRepo + " and pointing",
	} {
		if !strings.Contains(log, line) {
			t.Errorf("log does not say %q:\n%s", line, log)
		}
	}
	if strings.Contains(log, "pulling from "+installtracktest.Repository) {
		t.Errorf("log names %s as the repository pulled from, which this upgrade never touched:\n%s", installtracktest.Repository, log)
	}
	if verify := stepByName(t, status, stepVerify); verify.State != StepDone || !strings.Contains(verify.Detail, "switching edition from acme") {
		t.Errorf("verify step = %+v, want it done, saying the edition switch was asked for", verify)
	}

	// Asking to switch onto this edition's own repository is refused:
	// there is nothing to switch, and a record saying otherwise would lie.
	if err := u.StartImageWith("main", installtracktest.OptInRepository, ImageOptions{SwitchEdition: true}); err == nil {
		t.Error("switching edition onto acme's own opt-in repository succeeded, want refused")
	}
	if err := u.StartImageWith("main", "", ImageOptions{SwitchEdition: true}); err == nil {
		t.Error("switching edition with no image succeeded, want refused")
	}
}

// The verify step reads the pulled image's own edition stamp: a
// repository this build files under its own edition that nonetheless
// serves another edition's build is refused before the cut-over, and the
// deployment stays where it was.
func TestVerifyStepRefusesAnImageOfAnotherEdition(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	const prev = "GRAIN_IMAGE=" + installtracktest.Repository + ":old@sha256:1111111111111111111111111111111111111111111111111111111111111111\n"
	writeTestFile(t, refFile, prev)
	u := editionUpgrader(t, dir, filepath.Join(dir, "pulled"), installtracktest.Track, installtracktest.Repository, "")
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start = %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	if !strings.Contains(status.Detail, "oss edition's build") || !strings.Contains(status.Detail, "switch edition") {
		t.Errorf("status.Detail = %q, want it to name the image's edition and the override", status.Detail)
	}
	if got := readFile(t, refFile); got != prev {
		t.Errorf("ref file = %q, want it untouched (%q)", got, prev)
	}
	if status.EditionSwitch != nil {
		t.Errorf("status.EditionSwitch = %+v on an upgrade nobody asked to switch", status.EditionSwitch)
	}
}

// And an image that says it is this edition's build passes, with the
// verify step saying so.
func TestVerifyStepPassesAnImageOfThisEdition(t *testing.T) {
	dir := t.TempDir()
	u := editionUpgrader(t, dir, filepath.Join(dir, "pulled"), installtracktest.Track, installtracktest.Repository, "acme")
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start = %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)
	if verify := stepByName(t, status, stepVerify); verify.State != StepDone || !strings.Contains(verify.Detail, "acme edition's build") {
		t.Errorf("verify step = %+v, want it done, naming the acme edition", verify)
	}
}

// grain/task-4512: a deployment running an edition's build on a host
// showing none of its signals settled on the oss track, and then refused
// every upgrade onto its own edition's images -- the very image it was
// running included. With the edition naming its track as the build's own
// (installtrack.SetBuildTrack), the track the daemon detects is the
// edition's, and that edition's image passes the verify step.
func TestASignalLessDeploymentOnAnEditionBuildAcceptsItsOwnEdition(t *testing.T) {
	t.Cleanup(installtrack.SetBuildTrack(installtracktest.Track))
	// The daemon's probe (pkg/daemon newUpgrader), minus the live host.
	track := (installtrack.Probe{Hostname: "ci-runner.example.com"}).Detect().Track
	if track != installtracktest.Track {
		t.Fatalf("signal-less Detect().Track = %q, want the build's own %q", track, installtracktest.Track)
	}
	dir := t.TempDir()
	pulled := filepath.Join(dir, "pulled")
	u := editionUpgrader(t, dir, pulled, track, installtracktest.Repository, string(installtracktest.Track))
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start = %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)
	if verify := stepByName(t, status, stepVerify); verify.State != StepDone || !strings.Contains(verify.Detail, "acme edition's build") {
		t.Errorf("verify step = %+v, want it done, naming the acme edition", verify)
	}
	if got := pulledLines(t, pulled); len(got) != 1 || !strings.HasPrefix(got[0], installtracktest.Repository+":") {
		t.Errorf("pulled %v, want one pull from %s", got, installtracktest.Repository)
	}
}

// A repository that cannot be pulled fails the upgrade with what to do
// about it. Nothing tries another repository: one pull, of the
// configured repository's tag, and no hint pointing at another edition.
func TestAFailedPullNeverFallsBackToAnotherRepository(t *testing.T) {
	dir := t.TempDir()
	pulled := filepath.Join(dir, "pulled")
	u := editionUpgrader(t, dir, pulled, installtracktest.Track, installtracktest.OptInRepository, "acme")
	u.cfg.Image.PullCmd = []string{"sh", "-c", `printf '%s\n' "$1" >> ` + pulled + `; echo "denied: requested access to the resource is denied" >&2; exit 1`, "sh"}
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start = %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	if got := pulledLines(t, pulled); !slices.Equal(got, []string{installtracktest.OptInRepository + ":main"}) {
		t.Errorf("pulled %v, want exactly one pull of %s:main", got, installtracktest.OptInRepository)
	}
	if strings.Contains(status.Detail, ossRepo) {
		t.Errorf("status.Detail = %q points an acme deployment at the open-source repository", status.Detail)
	}
	if !strings.Contains(status.Detail, "choose "+installtracktest.Repository) {
		t.Errorf("status.Detail = %q, want it to suggest acme's own default repository", status.Detail)
	}

	// A repository naming no registry host is refused outright: no pull
	// at all, and certainly not of some other repository.
	dir2 := t.TempDir()
	pulled2 := filepath.Join(dir2, "pulled")
	hostless := editionUpgrader(t, dir2, pulled2, installtracktest.Track, "grain-acme/grain", "acme")
	if err := hostless.Start("main"); err != nil {
		t.Fatalf("Start = %v", err)
	}
	status = waitForPhase(t, hostless, PhaseFailed)
	if got := pulledLines(t, pulled2); len(got) != 0 {
		t.Errorf("a hostless repository's upgrade pulled %v, want nothing", got)
	}
	if !strings.Contains(status.Detail, "grain-acme/grain") {
		t.Errorf("status.Detail = %q, want it to name the configured repository", status.Detail)
	}
}
