package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// oldMakeBuild is the build line of a Dockerfile from before ARG RELEASE.
const oldMakeBuild = "RUN make build BUILDVCS=${BUILDVCS} GRAIN_IMAGE_REF=${GRAIN_IMAGE_REF}"

// `latest` is the last promoted release (grain/task-1681).
//
// On 2026-10-03 an operator upgraded a deployment to `latest` and got a
// build from 2026-09-25, ~1,940 commits behind main: the git tag `latest`
// had followed every promotion, and the image tag `latest` had followed
// none of them since GitHub Actions stopped moving it on main pushes. The
// release job's own `latest` branch fired only for a tag ref that its
// workflow never let through.
//
// What replaced both is `scripts/release-images.sh promote`, run by
// .grain/workflows/promote.yml: publish the images of the commit the git
// `latest` tag names as `v1.X.0` and `latest` -- the guest and
// k8s-sandbox `sha-<short>` images retagged, the grain image rebuilt from
// that commit with its release stamped in (grain/task-3064). These tests
// run it against a real repository's tags and a stub docker, because the
// claim is which tags it chooses and what it asks the registry for.

// releaseRepo is a repository with the tags a few promotions leave: two
// releases, an rc, and `latest` on the newer release. It returns the
// repository's path and the commits by name.
func releaseRepo(t *testing.T) (string, map[string]string) {
	t.Helper()
	return releaseRepoWithDockerfile(t, "FROM scratch\nARG GRAIN_IMAGE_REF=\n"+oldMakeBuild+"\n")
}

// splitDockerfile is the build of a Dockerfile from after the two daemon
// images: it takes MAIN_GRAIN, which is how a promotion tells that the
// release job built both grain and google-grain for the release.
const splitDockerfile = "FROM scratch\nARG GRAIN_IMAGE_REF=\nARG RELEASE=\nARG MAIN_GRAIN=./google/cmd/grain\n" +
	oldMakeBuild + " RELEASE=${RELEASE} MAIN_grain=${MAIN_GRAIN}\n"

// releaseRepoWithDockerfile is releaseRepo with the release's Dockerfile
// given.
func releaseRepoWithDockerfile(t *testing.T, dockerfile string) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	// What a promotion rebuilds the grain image from: a Dockerfile shaped
	// like v1.8.0's, from before it took a RELEASE, the go.mod the Go
	// version is read out of (its toolchain line, not its lower `go`
	// directive: GSA1-13), and the .gitignore entry that keeps the
	// CA a build is handed out of `git status`, as grain's own does.
	for name, body := range map[string]string{
		".gitignore": "/.grain-ca.crt\n",
		"Dockerfile": dockerfile,
		"go.mod":     "module example.com/release\n\ngo 1.26.6\n\ntoolchain go1.26.10\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	git("add", ".")
	commits := map[string]string{}
	for _, name := range []string{"old", "released", "unreleased", "candidate"} {
		git("commit", "-q", "--allow-empty", "-m", name)
		commits[name] = git("rev-parse", "HEAD")
	}
	git("tag", "-a", "v1.4.0", "-m", "Production release v1.4.0", commits["old"])
	git("tag", "-a", "v1.5.0", "-m", "Production release v1.5.0", commits["released"])
	git("tag", "-a", "latest", "-m", "Production release v1.5.0", commits["released"])
	git("tag", "-a", "v1.6.0-rc.1", "-m", "Release candidate v1.6.0-rc.1", commits["candidate"])
	// A lightweight tag on a commit nothing promoted, for the case of a
	// `latest` somebody moved by hand.
	git("tag", "hand-made", commits["unreleased"])
	return dir, commits
}

func retagLine(image, short, tag string) string {
	return "buildx imagetools create --tag ghcr.io/bwsalmon/grain/" + image + ":" + tag +
		" ghcr.io/bwsalmon/grain/" + image + ":sha-" + short
}

func promoteRun(t *testing.T, remote string, args []string, env map[string]string) (string, string, error) {
	t.Helper()
	all := map[string]string{
		"PATH":                  realToolPath(t),
		"RELEASE_IMAGES_REMOTE": remote,
	}
	for k, v := range env {
		all[k] = v
	}
	return releaseImagesRunArgs(t, args, all)
}

// Which tags a promotion picks, asked through --dry-run so that the only
// thing under test is the choice.
func TestPromoteChoosesTheReleaseTheLatestGitTagNames(t *testing.T) {
	remote, commits := releaseRepo(t)
	released, old := commits["released"][:7], commits["old"][:7]

	t.Run("no argument follows the git latest tag", func(t *testing.T) {
		out, docker, err := promoteRun(t, remote, []string{"--dry-run", "promote"}, nil)
		if err != nil {
			t.Fatalf("promote --dry-run failed: %v\n%s", err, out)
		}
		containsRun(t, out, "would retag ghcr.io/bwsalmon/grain/{guest,k8s-sandbox}:sha-"+released+" as v1.5.0 and latest")
		containsRun(t, out, "and rebuild ghcr.io/bwsalmon/grain/grain:sha-"+released+" from "+commits["released"]+
			" with RELEASE=v1.5.0 as v1.5.0 and latest, unless ghcr.io/bwsalmon/grain/grain:v1.5.0 already reports v1.5.0")
		containsRun(t, out, "and point ghcr.io/bwsalmon/grain/grain:sha-"+released+" at the stamped ghcr.io/bwsalmon/grain/grain:v1.5.0")
		if docker != "" {
			t.Errorf("a dry run asked docker for something:\n%s", docker)
		}
	})

	t.Run("a named release that latest agrees with moves latest", func(t *testing.T) {
		out, _, err := promoteRun(t, remote, []string{"--dry-run", "promote", "v1.5.0"}, nil)
		if err != nil {
			t.Fatalf("promote v1.5.0 failed: %v\n%s", err, out)
		}
		containsRun(t, out, ":sha-"+released+" as v1.5.0 and latest")
	})

	t.Run("an older release is retagged as itself and latest stays put", func(t *testing.T) {
		out, _, err := promoteRun(t, remote, []string{"--dry-run", "promote", "v1.4.0"}, nil)
		if err != nil {
			t.Fatalf("promote v1.4.0 failed: %v\n%s", err, out)
		}
		containsRun(t, out, ":sha-"+old+" as v1.4.0\n")
		containsRun(t, out, "latest stays put: the git tag latest names "+commits["released"])
	})

	t.Run("main's head is never what latest means", func(t *testing.T) {
		out, _, err := promoteRun(t, remote, []string{"--dry-run", "promote"}, map[string]string{
			"GRAIN_CI_REF":    "main",
			"GRAIN_CI_COMMIT": commits["candidate"],
		})
		if err != nil {
			t.Fatalf("promote failed: %v\n%s", err, out)
		}
		if strings.Contains(out, "sha-"+commits["candidate"][:7]+" as") {
			t.Errorf("promote retagged the commit the run was for rather than the release:\n%s", out)
		}
		containsRun(t, out, ":sha-"+released+" as v1.5.0 and latest")
	})

	for _, c := range []struct {
		name, request, want string
	}{
		{"an rc is not a release", "v1.6.0-rc.1", "is not a release tag"},
		{"a branch name is not a release", "main", "is not a release tag"},
		{"a release that was never tagged", "v9.9.9", "has no tag v9.9.9"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, _, err := promoteRun(t, remote, []string{"--dry-run", "promote", c.request}, nil)
			if err == nil {
				t.Fatalf("promote %s succeeded:\n%s", c.request, out)
			}
			containsRun(t, out, c.want)
		})
	}
}

// The highest vX.Y.Z at the commit wins, compared as versions: v1.10.0
// is after v1.9.0, which a string sort gets wrong.
func TestPromoteNamesTheHighestReleaseAtTheCommit(t *testing.T) {
	remote, commits := releaseRepo(t)
	for _, tag := range []string{"v1.9.0", "v1.10.0"} {
		cmd := exec.Command("git", "tag", tag, commits["released"])
		cmd.Dir = remote
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git tag %s: %v\n%s", tag, err, out)
		}
	}
	out, _, err := promoteRun(t, remote, []string{"--dry-run", "promote"}, nil)
	if err != nil {
		t.Fatalf("promote failed: %v\n%s", err, out)
	}
	containsRun(t, out, " as v1.10.0 and latest")
}

func TestPromoteRefusesALatestNoReleaseNames(t *testing.T) {
	remote, commits := releaseRepo(t)
	del := exec.Command("git", "tag", "-d", "latest")
	del.Dir = remote
	if out, err := del.CombinedOutput(); err != nil {
		t.Fatalf("git tag -d latest: %v\n%s", err, out)
	}
	out, _, err := promoteRun(t, remote, []string{"--dry-run", "promote"}, nil)
	if err == nil {
		t.Fatalf("promote with no latest tag succeeded:\n%s", out)
	}
	containsRun(t, out, "has no `latest` tag")

	cmd := exec.Command("git", "tag", "latest", commits["unreleased"])
	cmd.Dir = remote
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag latest: %v\n%s", err, out)
	}
	out, _, err = promoteRun(t, remote, []string{"--dry-run", "promote"}, nil)
	if err == nil {
		t.Fatalf("promote of a latest no release names succeeded:\n%s", out)
	}
	containsRun(t, out, "which no vX.Y.Z tag names")
}

// stubAnswers writes a script the docker stub sources, answering what
// promote reads back off images: the label on the published vX.Y.Z
// (empty for one a retag published), what the release job's grain image
// was stamped with, and what the rebuilt one reports as its release. A
// build also logs the Dockerfile it was given and what `git status` says
// of the tree it was given, so the test can see both.
func stubAnswers(t *testing.T, short, label, release string) string {
	t.Helper()
	script := `case "$*" in
  "buildx imagetools inspect ghcr.io/bwsalmon/grain/grain:"*" --format "*) [ -z "` + label + `" ] || echo "` + label + `" ;;
  "run --rm "*" sandbox-image") echo "ghcr.io/bwsalmon/grain/guest:sha-` + short + `@sha256:1111" ;;
  "run --rm "*" k8s-sandbox-image") echo "ghcr.io/bwsalmon/grain/k8s-sandbox:sha-` + short + `@sha256:2222" ;;
  "run --rm "*" image") echo "ghcr.io/bwsalmon/grain/grain:sha-` + short + `" ;;
  "run --rm "*" version --json") printf '{\n  "release": "%s"\n}\n' "` + release + `" ;;
  "build "*) cat Dockerfile >> "$DOCKER_STUB_LOG"; printf 'git status: [%s]\n' "$(git status --porcelain)" >> "$DOCKER_STUB_LOG" ;;
esac
`
	path := filepath.Join(t.TempDir(), "answers.sh")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatalf("writing the stub's answers: %v", err)
	}
	return path
}

func publishEnv(answers string) map[string]string {
	return map[string]string{
		"GRAIN_CI":                 "true",
		"PACKAGE_PUBLISH_REGISTRY": "ghcr.io",
		"PACKAGE_PUBLISH_USERNAME": "x-access-token",
		"PACKAGE_PUBLISH_TOKEN":    "a-token",
		"DOCKER_STUB_ANSWERS":      answers,
		// No signing key: the release goes ahead unsigned, as it does
		// wherever image-signing is not granted.
		"IMAGE_SIGNING_KEY": "/nonexistent/image-signing-key",
	}
}

// The promotion of a release whose grain image is still the one built
// when its commit was the candidate (grain/task-3064): the guest and
// k8s-sandbox images are retagged, and the grain image is rebuilt from
// the release's own commit with RELEASE=vX.Y.Z, naming exactly the
// sandbox images and image reference the candidate's build named, and
// only then published as vX.Y.Z and latest.
func TestPromoteRebuildsTheGrainImageWithTheReleaseStamped(t *testing.T) {
	remote, commits := releaseRepo(t)
	short := commits["released"][:7]
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(stubAnswers(t, short, "", "v1.5.0")))
	if err != nil {
		t.Fatalf("promote failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	for _, image := range []string{"guest", "k8s-sandbox", "grain"} {
		containsRun(t, docker, "buildx imagetools inspect ghcr.io/bwsalmon/grain/"+image+":sha-"+short)
	}
	for _, image := range []string{"guest", "k8s-sandbox"} {
		for _, tag := range []string{"v1.5.0", "latest"} {
			containsRun(t, docker, retagLine(image, short, tag))
		}
	}
	for _, want := range []string{
		"--build-arg GO_VERSION=1.26.10",
		"--build-arg SANDBOX_IMAGE=ghcr.io/bwsalmon/grain/guest:sha-" + short + "@sha256:1111",
		"--build-arg K8S_SANDBOX_IMAGE=ghcr.io/bwsalmon/grain/k8s-sandbox:sha-" + short + "@sha256:2222",
		"--build-arg GRAIN_IMAGE_REF=ghcr.io/bwsalmon/grain/grain:sha-" + short,
		"--build-arg GIT_REVISION=" + commits["released"],
		"--build-arg RELEASE=v1.5.0 --label org.opencontainers.image.version=v1.5.0 -t ghcr.io/bwsalmon/grain/grain:v1.5.0",
		// The release's own Dockerfile, given the ARG it predates.
		"ARG RELEASE=\n" + oldMakeBuild + " RELEASE=${RELEASE}\n",
		"push ghcr.io/bwsalmon/grain/grain:v1.5.0",
		"buildx imagetools create --tag ghcr.io/bwsalmon/grain/grain:latest ghcr.io/bwsalmon/grain/grain:v1.5.0",
		// The candidate's build is replaced under its sha- tag by the
		// stamped release, so cmd/ghcr-prune keeps that tag with vX.Y.Z.
		"buildx imagetools create --tag ghcr.io/bwsalmon/grain/grain:sha-" + short + " ghcr.io/bwsalmon/grain/grain:v1.5.0",
	} {
		containsRun(t, docker, want)
	}
	containsRun(t, out, "predates ARG RELEASE")
	// A release from before the two daemon images built only grain, and
	// that grain is the Google build: google-grain is published as it,
	// so a Google install has a google-grain:latest to pull.
	containsRun(t, out, "predates the two daemon images")
	for _, tag := range []string{"v1.5.0", "latest", "sha-" + short} {
		containsRun(t, docker, "buildx imagetools create --tag ghcr.io/bwsalmon/grain/google-grain:"+tag+" ghcr.io/bwsalmon/grain/grain:v1.5.0")
	}
	if strings.Contains(docker, "MAIN_GRAIN") || strings.Contains(docker, "-t ghcr.io/bwsalmon/grain/google-grain") {
		t.Errorf("promote of a release from before the split built a google-grain of its own, or asked its Dockerfile for a MAIN_GRAIN it does not take:\n%s", docker)
	}
	if strings.Contains(docker, "--tag ghcr.io/bwsalmon/grain/grain:latest ghcr.io/bwsalmon/grain/grain:sha-") ||
		strings.Contains(docker, "--tag ghcr.io/bwsalmon/grain/grain:v1.5.0 ") {
		t.Errorf("promote retagged the candidate's grain image, which reports its rc, as the release:\n%s", docker)
	}
}

// The ARG a pre-RELEASE Dockerfile is given must not make the release
// report a dirty tree: the checkout's .git is built with it, and `go
// build` stamps vcs.modified from `git status`, so a promoted v1.8.0
// showed `<short>-dirty` in `grain version` and the UI
// (docs/test-runs/2026-10-12-release-version-stamp.md).
func TestPromoteRebuildsFromATreeGitCallsClean(t *testing.T) {
	remote, commits := releaseRepo(t)
	short := commits["released"][:7]
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(stubAnswers(t, short, "", "v1.5.0")))
	if err != nil {
		t.Fatalf("promote failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	containsRun(t, out, "predates ARG RELEASE")
	containsRun(t, docker, "ARG RELEASE=\n"+oldMakeBuild+" RELEASE=${RELEASE}\n")
	containsRun(t, docker, "git status: []\n")
}

// A rebuild that reports a modified tree is not published, and no tag
// moves: it was built from a clean checkout of the tag, so whatever made
// it dirty was this script.
func TestPromoteOfARebuildReportingAModifiedTreeMovesNothing(t *testing.T) {
	remote, commits := releaseRepo(t)
	short := commits["released"][:7]
	answers := stubAnswers(t, short, "", "v1.5.0")
	body, err := os.ReadFile(answers)
	if err != nil {
		t.Fatalf("reading the stub's answers: %v", err)
	}
	dirty := strings.Replace(string(body), `"release": "%s"\n}`, `"release": "%s",\n  "modified": true\n}`, 1)
	if dirty == string(body) {
		t.Fatal("the stub's version --json answer was not where this test expected it")
	}
	if err := os.WriteFile(answers, []byte(dirty), 0o644); err != nil {
		t.Fatalf("writing the stub's answers: %v", err)
	}
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(answers))
	if err == nil {
		t.Fatalf("promote published a rebuild reporting a modified tree:\n%s", out)
	}
	containsRun(t, out, "reports a modified tree")
	if strings.Contains(docker, "push ") || strings.Contains(docker, "imagetools create") {
		t.Errorf("promote moved a tag although the rebuild reported a modified tree:\n%s", docker)
	}
}

// A replayed promotion of a release whose vX.Y.Z already says so builds
// nothing and pushes nothing: `latest` is pointed at the same digest it
// already holds, so it does not churn on every merge.
func TestPromoteOfAnAlreadyStampedReleaseRebuildsNothing(t *testing.T) {
	remote, commits := releaseRepo(t)
	short := commits["released"][:7]
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(stubAnswers(t, short, "v1.5.0", "v1.5.0")))
	if err != nil {
		t.Fatalf("promote failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	containsRun(t, out, "ghcr.io/bwsalmon/grain/grain:v1.5.0 already reports v1.5.0; nothing to rebuild")
	containsRun(t, docker, "buildx imagetools create --tag ghcr.io/bwsalmon/grain/grain:latest ghcr.io/bwsalmon/grain/grain:v1.5.0")
	for _, line := range strings.Split(docker, "\n") {
		for _, forbidden := range []string{"build ", "push ", "pull "} {
			if strings.HasPrefix(line, forbidden) {
				t.Errorf("promote of an already-stamped release ran `docker %s`:\n%s", line, docker)
			}
		}
	}
}

// A rebuild that does not report the release it was built to stamp is
// not published, and no tag moves.
func TestPromoteOfARebuildThatDoesNotReportTheReleaseMovesNothing(t *testing.T) {
	remote, commits := releaseRepo(t)
	short := commits["released"][:7]
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(stubAnswers(t, short, "", "v1.5.0-rc.1")))
	if err == nil {
		t.Fatalf("promote published a rebuild reporting v1.5.0-rc.1:\n%s", out)
	}
	containsRun(t, out, "not release v1.5.0")
	if strings.Contains(docker, "push ") || strings.Contains(docker, "imagetools create") {
		t.Errorf("promote moved a tag although the rebuild did not report its release:\n%s", docker)
	}
}

// A release whose images were never published moves nothing, and says
// which image is missing -- rather than leaving `latest` naming a grain
// from one commit and a guest from another, or building anything.
func TestPromoteOfAReleaseWithAnImageMissingMovesNothing(t *testing.T) {
	remote, commits := releaseRepo(t)
	missing := "ghcr.io/bwsalmon/grain/guest:sha-" + commits["released"][:7]
	env := publishEnv(stubAnswers(t, commits["released"][:7], "", "v1.5.0"))
	env["DOCKER_STUB_MISSING"] = missing
	out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
	if err == nil {
		t.Fatalf("promote succeeded with %s missing:\n%s", missing, out)
	}
	containsRun(t, out, "never published "+missing)
	if strings.Contains(docker, "imagetools create") || strings.Contains(docker, "build ") {
		t.Errorf("promote moved a tag although %s is missing:\n%s", missing, docker)
	}
}

// A release whose vX.Y.Z grain image is already stamped no longer needs
// the grain sha- build it was rebuilt from, and cmd/ghcr-prune deletes
// that build once it ages out: every promotion tick after that must still
// move `latest`, not fail naming it (v1.9.0, grain/task-3867).
func TestPromoteOfAStampedReleaseWhoseGrainBuildWasPrunedStillMovesLatest(t *testing.T) {
	remote, commits := releaseRepo(t)
	short := commits["released"][:7]
	env := publishEnv(stubAnswers(t, short, "v1.5.0", "v1.5.0"))
	env["DOCKER_STUB_MISSING"] = "ghcr.io/bwsalmon/grain/grain:sha-" + short
	out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
	if err != nil {
		t.Fatalf("promote failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	containsRun(t, out, "ghcr.io/bwsalmon/grain/grain:v1.5.0 already reports v1.5.0; nothing to rebuild")
	containsRun(t, docker, retagLine("guest", short, "latest"))
	containsRun(t, docker, "buildx imagetools create --tag ghcr.io/bwsalmon/grain/grain:latest ghcr.io/bwsalmon/grain/grain:v1.5.0")
	// And the pruned tag is put back, on the release's own version: a
	// deployment that went onto this commit as the candidate names it as
	// its image, and its config repo's state check pulls it
	// (grain:sha-adea62d, grain/task-3926).
	containsRun(t, docker, "buildx imagetools create --tag ghcr.io/bwsalmon/grain/grain:sha-"+short+" ghcr.io/bwsalmon/grain/grain:v1.5.0")
}

// The grain sha- build is still required while the release has not been
// stamped: it is what the rebuild reads its sandbox images from.
func TestPromoteOfAnUnstampedReleaseWithoutItsGrainBuildMovesNothing(t *testing.T) {
	remote, commits := releaseRepo(t)
	missing := "ghcr.io/bwsalmon/grain/grain:sha-" + commits["released"][:7]
	env := publishEnv(stubAnswers(t, commits["released"][:7], "", "v1.5.0"))
	env["DOCKER_STUB_MISSING"] = missing
	out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
	if err == nil {
		t.Fatalf("promote succeeded with %s missing:\n%s", missing, out)
	}
	containsRun(t, out, "never published "+missing)
	if strings.Contains(docker, "imagetools create") || strings.Contains(docker, "build ") {
		t.Errorf("promote moved a tag although %s is missing:\n%s", missing, docker)
	}
}

// Exactly one path moves `latest`: the promote phase. The release job's
// push publishes sha- and branch tags only.
func TestOnlyThePromotionMovesLatest(t *testing.T) {
	script := read(t, "scripts", "release-images.sh")
	if published := publishImagesBody(t, script); strings.Contains(published, ":latest") {
		t.Errorf("publish_images moves a :latest tag again: a merge publishes sha- and branch tags, and `latest` is the last promoted release:\n%s", published)
	}
	contains(t, releaseScriptFunction(t, script, "promote_images"), "targets+=(latest)")

	text := read(t, ".grain", "workflows", "promote.yml")
	contains(t, text, "bash ./scripts/release-images.sh promote")
	contains(t, text, "capabilities: [package-publish]")
}

// splitStubAnswers is stubAnswers for a release with both daemon images:
// google-grain reports the google edition, as ./google/cmd/grain does,
// and grain reports none.
func splitStubAnswers(t *testing.T, short, label, release string) string {
	t.Helper()
	path := stubAnswers(t, short, label, release)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the stub's answers: %v", err)
	}
	google := "case \"$*\" in\n" +
		`  "run --rm ghcr.io/bwsalmon/grain/google-grain:"*" version --json") printf '{\n  "release": "%s",\n  "edition": "google"\n}\n' "` + release + `" ;;` + "\n"
	text := strings.Replace(string(body), "case \"$*\" in\n", google, 1)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("writing the stub's answers: %v", err)
	}
	return path
}

// A release cut after the split has two daemon images, and promotion
// rebuilds each from its own main -- grain from ./cmd/grain, google-grain
// from ./google/cmd/grain -- stamps the release into both, and moves
// vX.Y.Z, latest and sha-<short> on both (grain/task-4944).
func TestPromoteRebuildsBothDaemonImages(t *testing.T) {
	remote, commits := releaseRepoWithDockerfile(t, splitDockerfile)
	short := commits["released"][:7]

	out, docker, err := promoteRun(t, remote, []string{"--dry-run", "promote"}, nil)
	if err != nil {
		t.Fatalf("promote --dry-run failed: %v\n%s", err, out)
	}
	for _, image := range []string{"grain", "google-grain"} {
		containsRun(t, out, "and rebuild ghcr.io/bwsalmon/grain/"+image+":sha-"+short)
		containsRun(t, out, "and point ghcr.io/bwsalmon/grain/"+image+":sha-"+short+" at the stamped ghcr.io/bwsalmon/grain/"+image+":v1.5.0")
	}
	if docker != "" {
		t.Errorf("a dry run asked docker for something:\n%s", docker)
	}

	out, docker, err = promoteRun(t, remote, []string{"promote"}, publishEnv(splitStubAnswers(t, short, "", "v1.5.0")))
	if err != nil {
		t.Fatalf("promote failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	for image, main := range map[string]string{"grain": "./cmd/grain", "google-grain": "./google/cmd/grain"} {
		containsRun(t, docker, "buildx imagetools inspect ghcr.io/bwsalmon/grain/"+image+":sha-"+short)
		containsRun(t, docker, "--build-arg MAIN_GRAIN="+main+" ")
		containsRun(t, docker, "--build-arg RELEASE=v1.5.0 --label org.opencontainers.image.version=v1.5.0 -t ghcr.io/bwsalmon/grain/"+image+":v1.5.0")
		containsRun(t, docker, "push ghcr.io/bwsalmon/grain/"+image+":v1.5.0")
		containsRun(t, docker, "buildx imagetools create --tag ghcr.io/bwsalmon/grain/"+image+":latest ghcr.io/bwsalmon/grain/"+image+":v1.5.0")
		containsRun(t, docker, "buildx imagetools create --tag ghcr.io/bwsalmon/grain/"+image+":sha-"+short+" ghcr.io/bwsalmon/grain/"+image+":v1.5.0")
	}
	if strings.Contains(out, "predates the two daemon images") {
		t.Errorf("promote took a release whose Dockerfile takes MAIN_GRAIN for one from before the split:\n%s", out)
	}
	if strings.Contains(docker, "--tag ghcr.io/bwsalmon/grain/google-grain:latest ghcr.io/bwsalmon/grain/grain:") {
		t.Errorf("promote published core grain as google-grain:\n%s", docker)
	}
}

// A google-grain rebuild that does not report the google edition is core
// grain under the Google name -- what a Dockerfile that dropped
// MAIN_GRAIN would build -- and nothing moves.
func TestPromoteOfAGoogleGrainThatIsNotTheGoogleBuildMovesNothing(t *testing.T) {
	remote, commits := releaseRepoWithDockerfile(t, splitDockerfile)
	short := commits["released"][:7]
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(stubAnswers(t, short, "", "v1.5.0")))
	if err == nil {
		t.Fatalf("promote published a google-grain reporting no edition:\n%s", out)
	}
	containsRun(t, out, "should report edition google")
	if strings.Contains(docker, ":latest") {
		t.Errorf("promote moved latest although google-grain is not the Google build:\n%s", docker)
	}
}

// pendingAnswers is stubAnswers for a release staged as vX.Y.0-pending
// (scripts/release-images.sh pending): grain:v1.5.0 is not published yet,
// and grain:v1.5.0-pending reports v1.5.0 built from revision.
func pendingAnswers(t *testing.T, short, revision string) string {
	t.Helper()
	path := stubAnswers(t, short, "", "v1.5.0")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pending := `case "$*" in
  "buildx imagetools inspect ghcr.io/bwsalmon/grain/grain:v1.5.0-pending --format "*revision*) echo "` + revision + `" ;;
  "buildx imagetools inspect ghcr.io/bwsalmon/grain/grain:v1.5.0-pending --format "*) echo "v1.5.0" ;;
`
	script := strings.Replace(string(body), "case \"$*\" in\n", pending, 1)
	if script == string(body) {
		t.Fatal("the stub's answers did not start where this test expected")
	}
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Approving a pending release (docs/design/release-from-trunk.md §4) is a
// retag: promote finds grain:v1.5.0-pending stamped v1.5.0 from the
// release's own commit and publishes it as v1.5.0 and latest, rebuilding
// nothing.
func TestPromoteOfAPendingReleaseRetagsTheStagedBuild(t *testing.T) {
	remote, commits := releaseRepo(t)
	short := commits["released"][:7]
	out, docker, err := promoteRun(t, remote, []string{"promote", "v1.5.0"}, publishEnv(pendingAnswers(t, short, commits["released"])))
	if err != nil {
		t.Fatalf("promote failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	containsRun(t, out, "ghcr.io/bwsalmon/grain/grain:v1.5.0-pending is "+commits["released"]+" stamped v1.5.0; promoting it as built")
	containsRun(t, docker, "buildx imagetools create --tag ghcr.io/bwsalmon/grain/grain:v1.5.0 ghcr.io/bwsalmon/grain/grain:v1.5.0-pending")
	if strings.Contains(docker, "-t ghcr.io/bwsalmon/grain/grain:v1.5.0") {
		t.Errorf("promote rebuilt grain although its pending build was staged:\n%s", docker)
	}
}

// A :v1.5.0-pending from another commit -- left behind by a rejected
// release -- says the same version but is not this release: promote
// rebuilds rather than shipping it.
func TestPromoteIgnoresAPendingBuildOfAnotherCommit(t *testing.T) {
	remote, commits := releaseRepo(t)
	short := commits["released"][:7]
	out, docker, err := promoteRun(t, remote, []string{"promote", "v1.5.0"},
		publishEnv(pendingAnswers(t, short, strings.Repeat("0", 40))))
	if err != nil {
		t.Fatalf("promote failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	if strings.Contains(docker, "--tag ghcr.io/bwsalmon/grain/grain:v1.5.0 ghcr.io/bwsalmon/grain/grain:v1.5.0-pending") {
		t.Errorf("promote shipped another commit's pending build:\n%s", docker)
	}
	containsRun(t, docker, "-t ghcr.io/bwsalmon/grain/grain:v1.5.0")
}
