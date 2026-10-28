package upgrade

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/releasenotes"
)

// notesJSON is what `grain release-notes --json` prints for a build of
// version carrying notes for versions.
func notesJSON(t *testing.T, version string, versions ...string) string {
	t.Helper()
	report := releasenotes.Report{Version: version, Notes: []releasenotes.Note{}}
	for _, v := range versions {
		report.Notes = append(report.Notes, releasenotes.Note{
			Version:  releasenotes.Version{Version: v, Title: v, Date: "2026-10-11"},
			Markdown: "# " + v + "\n",
		})
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func previewVersions(p ReleaseNotesPreview) string {
	var out []string
	for _, n := range p.Notes {
		out = append(out, n.Version.Version)
	}
	return strings.Join(out, " ")
}

// imageNotesUpgrader is an image deployment running v1.6.0 whose
// `docker run` answers with answer (a shell snippet), recording what it
// was asked in the returned file.
func imageNotesUpgrader(t *testing.T, answer string) (*Upgrader, string) {
	t.Helper()
	dir := t.TempDir()
	ran := filepath.Join(dir, "ran")
	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			PullCmd:    []string{"true"},
			RunCmd:     []string{"sh", "-c", `printf '%s\n' "$*" >> ` + ran + "; " + answer, "sh"},
		},
		ReleaseNotesArgs: []string{"release-notes", "--json"},
		RunningRelease:   func() string { return "v1.6.0" },
		StatusFile:       filepath.Join(dir, "upgrade-status.json"),
	})
	return u, ran
}

// Choosing a target shows what it brings: the image is pulled, asked
// for its notes from the running version on, and every newer version
// up to its own is shown.
func TestReleaseNotesAsksThePulledImageFromTheRunningVersion(t *testing.T) {
	answer := filepath.Join(t.TempDir(), "answer.json")
	if err := os.WriteFile(answer, []byte(notesJSON(t, "v1.8.0", "v1.8.0", "v1.7.0")), 0o644); err != nil {
		t.Fatal(err)
	}
	u, ran := imageNotesUpgrader(t, "cat "+answer)

	got, err := u.ReleaseNotes(context.Background(), "v1.8.0")
	if err != nil {
		t.Fatalf("ReleaseNotes: %v", err)
	}
	if v := previewVersions(got); v != "v1.8.0 v1.7.0" {
		t.Errorf("notes = %q, want v1.8.0 v1.7.0", v)
	}
	if got.NotInThisBuild || got.Missing != "" || got.From != "v1.6.0" || got.Version != "v1.8.0" {
		t.Errorf("preview = %+v", got)
	}
	const pinned = "ghcr.io/bwsalmon/grain/grain:v1.8.0@" + testDigest
	if asked := readFile(t, ran); asked != pinned+" release-notes --json --from v1.6.0\n" {
		t.Errorf("asked %q, want the pinned image asked from v1.6.0", asked)
	}

	// Chosen again, it is not pulled and run again.
	if _, err := u.ReleaseNotes(context.Background(), "v1.8.0"); err != nil {
		t.Fatal(err)
	}
	if asked := readFile(t, ran); strings.Count(asked, "\n") != 1 {
		t.Errorf("a second preview ran the image again: %q", asked)
	}
}

// A candidate image is built before the cut writes its version's notes:
// it shows what it has and says its own are not in it.
func TestReleaseNotesSaysACandidateCarriesNoneForItself(t *testing.T) {
	answer := filepath.Join(t.TempDir(), "answer.json")
	if err := os.WriteFile(answer, []byte(notesJSON(t, "v1.9.0-rc.2", "v1.8.0", "v1.7.0")), 0o644); err != nil {
		t.Fatal(err)
	}
	u, _ := imageNotesUpgrader(t, "cat "+answer)
	got, err := u.ReleaseNotes(context.Background(), "candidate")
	if err != nil {
		t.Fatalf("ReleaseNotes: %v", err)
	}
	if !got.NotInThisBuild || got.Missing != "v1.9.0" || !strings.Contains(got.Message, NotInThisBuild) {
		t.Errorf("preview = %+v, want v1.9.0's notes reported missing", got)
	}
	if v := previewVersions(got); v != "v1.8.0 v1.7.0" {
		t.Errorf("notes = %q, want the ones it does carry", v)
	}
}

// An image older than the subcommand refuses it with a usage error: not
// a failure, just a build with no notes to show.
func TestReleaseNotesFromABuildOlderThanTheSubcommand(t *testing.T) {
	u, _ := imageNotesUpgrader(t, `echo "flag provided but not defined: -json" >&2; exit 2`)
	got, err := u.ReleaseNotes(context.Background(), "v1.7.0")
	if err != nil {
		t.Fatalf("ReleaseNotes: %v", err)
	}
	if !got.NotInThisBuild || !strings.Contains(got.Message, NotInThisBuild) || len(got.Notes) != 0 {
		t.Errorf("preview = %+v, want %q", got, NotInThisBuild)
	}

	// The task CLI answering a stray argument with its own text, exit 0,
	// is the same build saying nothing it can be read for.
	u, _ = imageNotesUpgrader(t, `echo "usage: grain [global flags] <command>"`)
	if got, err = u.ReleaseNotes(context.Background(), "v1.7.0"); err != nil || !got.NotInThisBuild {
		t.Errorf("preview = %+v, %v; want %q", got, err, NotInThisBuild)
	}
}

// A host that could not run the image at all has not heard from the
// build, and says so as an error rather than blaming it.
func TestReleaseNotesReportsAnImageThatCouldNotBeRun(t *testing.T) {
	u, _ := imageNotesUpgrader(t, `exit 125`)
	if _, err := u.ReleaseNotes(context.Background(), "v1.7.0"); err == nil {
		t.Error("a docker that could not start the container was read as the build's answer")
	}
	dir := t.TempDir()
	failing := newUpgrader(t, Config{
		Image:            &ImageConfig{Repository: "ghcr.io/bwsalmon/grain/grain", PullCmd: []string{"false"}, RunCmd: []string{"true"}},
		ReleaseNotesArgs: []string{"release-notes", "--json"},
		StatusFile:       filepath.Join(dir, "upgrade-status.json"),
	})
	if _, err := failing.ReleaseNotes(context.Background(), "v1.7.0"); err == nil || !strings.Contains(err.Error(), "pulling") {
		t.Errorf("err = %v, want the failed pull", err)
	}
}

// A tag the registry does not have says so, rather than docker's
// "failed to resolve reference" (the 10-15 install walkthrough, against
// a registry with no v9.9.9).
func TestReleaseNotesSaysTheRegistryHasNoSuchTag(t *testing.T) {
	dir := t.TempDir()
	missing := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "localhost:5000/x/grain",
			PullCmd:    []string{"sh", "-c", `echo "Error response from daemon: failed to resolve reference \"$1\": $1: not found" >&2; exit 1`, "sh"},
			RunCmd:     []string{"true"},
		},
		ReleaseNotesArgs: []string{"release-notes", "--json"},
		StatusFile:       filepath.Join(dir, "upgrade-status.json"),
	})
	_, err := missing.ReleaseNotes(context.Background(), "v9.9.9")
	if want := "the registry has no image localhost:5000/x/grain:v9.9.9"; err == nil || err.Error() != want {
		t.Errorf("preview of a missing tag: err = %v, want %s", err, want)
	}
}

// A signature that does not verify means the image is never run.
func TestReleaseNotesDoesNotRunAnImageThatFailsVerification(t *testing.T) {
	u, ran := imageNotesUpgrader(t, "true")
	u.cfg.Image.VerifyCmd = []string{"false"}
	if _, err := u.ReleaseNotes(context.Background(), "v1.8.0"); err == nil {
		t.Fatal("an unverified image was previewed")
	}
	if _, err := os.Stat(ran); err == nil {
		t.Errorf("the unverified image was run: %q", readFile(t, ran))
	}
}

func TestReleaseNotesRefusesABranchItWouldNotUpgradeTo(t *testing.T) {
	u, _ := imageNotesUpgrader(t, "true")
	u.cfg.AllowedBranches = []string{"main"}
	if _, err := u.ReleaseNotes(context.Background(), "someones/branch"); err == nil {
		t.Error("previewed a branch this deployment would not upgrade onto")
	}
}

// The Kubernetes path asks in a pod of its own, after the same security
// context read the upgrade makes.
func TestReleaseNotesAsksInAPodOnKubernetes(t *testing.T) {
	kubectl, calls, _ := fakeKubectlAnswering(t, kubeSucceeded, notesJSON(t, "v1.8.0", "v1.8.0", "v1.7.0", "v1.6.0"), "")
	u := kubeUpgrader(t, kubectl, nil)
	u.cfg.ReleaseNotesArgs = []string{"release-notes", "--json"}
	u.cfg.RunningRelease = func() string { return "v1.6.0" }
	u.cfg.AllowedBranches = anyBranch

	got, err := u.ReleaseNotes(context.Background(), "v1.8.0")
	if err != nil {
		t.Fatalf("ReleaseNotes: %v", err)
	}
	if v := previewVersions(got); v != "v1.8.0 v1.7.0" {
		t.Errorf("notes = %q, want v1.8.0 v1.7.0", v)
	}
	if !strings.Contains(calls(), releaseNotesPod) || strings.Contains(calls(), kubeAskPod) {
		t.Errorf("calls = %q, want the preview's own pod", calls())
	}
}

func TestReleaseNotesFromAPodThatFailsIsNotInThisBuild(t *testing.T) {
	kubectl, _, _ := fakeKubectlAnswering(t, strings.Replace(kubeSucceeded, "Succeeded", "Failed", 1), "flag provided but not defined", "")
	u := kubeUpgrader(t, kubectl, nil)
	u.cfg.ReleaseNotesArgs = []string{"release-notes", "--json"}
	u.cfg.AllowedBranches = anyBranch
	got, err := u.ReleaseNotes(context.Background(), "v1.5.0")
	if err != nil {
		t.Fatalf("ReleaseNotes: %v", err)
	}
	if !got.NotInThisBuild {
		t.Errorf("preview = %+v, want %q", got, NotInThisBuild)
	}
}

// The source path builds nothing until it upgrades, so it reads the
// notes the branch would embed out of git.
func TestReleaseNotesOnTheSourcePathReadsTheFetchedBranch(t *testing.T) {
	root := t.TempDir()
	origin := initBareRepo(t, filepath.Join(root, "origin.git"))
	seed := filepath.Join(root, "seed")
	runGit(t, root, "init", "--quiet", "-b", "main", seed)
	notes := filepath.Join(seed, "docs", "releases")
	if err := os.MkdirAll(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"README.md": "# Releases\n",
		"v1.6.0.md": "# v1.6.0\n",
		"v1.7.0.md": "# v1.7.0\n\nReleased 2026-10-04 from commit `abc1234`.\n",
		"v1.8.0.md": "# v1.8.0\n\nReleased 2026-10-11 from commit `0ca8da306`.\n",
	} {
		if err := os.WriteFile(filepath.Join(notes, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, seed, "add", ".")
	runGit(t, seed, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--quiet", "-m", "notes")
	runGit(t, seed, "push", "--quiet", origin, "HEAD:refs/heads/main")
	checkout := filepath.Join(root, "checkout")
	runGit(t, root, "clone", "--quiet", "-b", "main", origin, checkout)
	// The checkout itself is behind: what is read is origin's branch.
	runGit(t, checkout, "reset", "--quiet", "--hard", "HEAD")

	u := newUpgrader(t, Config{
		SrcDir:           checkout,
		ReleaseNotesArgs: []string{"release-notes", "--json"},
		RunningRelease:   func() string { return "v1.6.0" },
		StatusFile:       filepath.Join(root, "upgrade-status.json"),
	})
	got, err := u.ReleaseNotes(context.Background(), "main")
	if err != nil {
		t.Fatalf("ReleaseNotes: %v", err)
	}
	if v := previewVersions(got); v != "v1.8.0 v1.7.0" {
		t.Errorf("notes = %q, want v1.8.0 v1.7.0", v)
	}
	if got.Notes[0].Date != "2026-10-11" {
		t.Errorf("v1.8.0 = %+v, want its date read", got.Notes[0].Version)
	}

	// A branch origin does not have says so, rather than only that git
	// exited 128 (docs/test-runs/2026-10-12-release-notes-versions.md).
	if _, err := u.ReleaseNotes(context.Background(), "no-such-branch"); err == nil || err.Error() != "origin has no branch no-such-branch" {
		t.Errorf("preview of a missing branch: err = %v, want origin has no branch no-such-branch", err)
	}

	// Nothing newer than what runs: not in this build.
	u.cfg.RunningRelease = func() string { return "v1.8.0" }
	u.notes = nil
	if got, err = u.ReleaseNotes(context.Background(), "main"); err != nil || !got.NotInThisBuild {
		t.Errorf("preview = %+v, %v; want %q", got, err, NotInThisBuild)
	}
}

// A deployment that does not configure the question says the notes are
// not available rather than running anything.
func TestReleaseNotesWithoutArgsAsksNothing(t *testing.T) {
	u, ran := imageNotesUpgrader(t, "true")
	u.cfg.ReleaseNotesArgs = nil
	got, err := u.ReleaseNotes(context.Background(), "main")
	if err != nil || !got.NotInThisBuild {
		t.Errorf("preview = %+v, %v", got, err)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("an image was run with no question configured")
	}
}

// What the preview makes of the build versions every build now carries
// (scripts/build-version.sh): the target's own version against the one
// this deployment runs, compared as semver.
func TestReleaseNotesPreviewOfBuildVersions(t *testing.T) {
	report := func(version string, notes ...string) releasenotes.Report {
		r := releasenotes.Report{Version: version, Notes: []releasenotes.Note{}}
		for _, v := range notes {
			r.Notes = append(r.Notes, releasenotes.Note{Version: releasenotes.Version{Version: v, Title: v}})
		}
		return r
	}
	for name, tc := range map[string]struct {
		from       string
		report     releasenotes.Report
		notes      string
		missing    string
		notNewer   bool
		notInBuild bool
	}{
		// main onto a newer main: an upgrade, whose own notes are not
		// written until the cut.
		"main to a newer main": {
			from:       "v2.3.0-dev.114+abc1234",
			report:     report("v2.3.0-dev.130+def5678", "v2.2.0", "v2.1.1"),
			missing:    "v2.3.0",
			notInBuild: true,
		},
		"main to an older main": {
			from:     "v2.3.0-dev.114+abc1234",
			report:   report("v2.3.0-dev.100+0123456", "v2.2.0", "v2.1.1"),
			notNewer: true,
		},
		// A main deployment is never offered latest as an upgrade: the
		// release main is ahead of is older than every main build.
		"main to latest": {
			from:     "v2.3.0-dev.114+abc1234",
			report:   report("v2.2.0", "v2.2.0", "v2.1.1"),
			notNewer: true,
		},
		"main to candidate": {
			from:     "v2.3.0-dev.114+abc1234",
			report:   report("v2.2.0-rc.1.48+6f65669", "v2.1.1"),
			notNewer: true,
		},
		// candidate onto the release it was a candidate for: an upgrade,
		// bringing that release's notes and nothing older.
		"candidate to its release": {
			from:   "v2.2.0-rc.1.48+6f65669",
			report: report("v2.2.0", "v2.2.0", "v2.1.1", "v2.1.0"),
			notes:  "v2.2.0",
		},
		"candidate to a newer candidate build": {
			from:       "v2.2.0-rc.1.48+6f65669",
			report:     report("v2.2.0-rc.2", "v2.1.1"),
			missing:    "v2.2.0",
			notInBuild: true,
		},
		"latest to main": {
			from:       "v2.2.0",
			report:     report("v2.3.0-dev.114+abc1234", "v2.2.0", "v2.1.1"),
			missing:    "v2.3.0",
			notInBuild: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			preview := ReleaseNotesPreview{From: tc.from}
			fillPreview(&preview, tc.report)
			if got := previewVersions(preview); got != tc.notes {
				t.Errorf("notes = %q, want %q", got, tc.notes)
			}
			if preview.Missing != tc.missing || preview.NotInThisBuild != tc.notInBuild {
				t.Errorf("preview = %+v, want missing %q, not-in-this-build %v", preview, tc.missing, tc.notInBuild)
			}
			if got := strings.Contains(preview.Message, "is not newer than "+tc.from); got != tc.notNewer {
				t.Errorf("message %q: saying it is not newer = %v, want %v", preview.Message, got, tc.notNewer)
			}
		})
	}
}
