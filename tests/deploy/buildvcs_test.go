package deploy

// scripts/buildvcs.sh, run rather than read.
//
// It exists because `go build -buildvcs=auto` does not mean "stamp the
// commit if you can": a checkout whose `.git` the building git cannot
// read fails the whole build with `error obtaining VCS status: exit
// status 128` and nothing else to go on. That is not a hypothetical --
// it is `make image` on an engineer's workstation, minutes in, having
// downloaded a module cache, over a commit stamp nothing in the image
// needs to run.
//
// The resolution can only be asserted by running the script: the shape
// of a repository git refuses is exactly what reading the file cannot
// tell you about.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// resolveBuildvcs runs the script in dir with the given setting (empty
// for none at all, which is how a caller asks for the default) and
// returns what it printed for `go build -buildvcs=` and what it said to
// an operator on the way.
func resolveBuildvcs(t *testing.T, dir, setting string) (string, string) {
	t.Helper()
	args := []string{filepath.Join(repoRoot(t), "scripts", "buildvcs.sh")}
	if setting != "" {
		args = append(args, setting)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("buildvcs.sh %s in %s: %v\n%s", setting, dir, err, stderr.String())
	}
	return strings.TrimSpace(string(out)), stderr.String()
}

// git runs one git command in dir, failing the test if it does not.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// A repository made here must not inherit the identity, hooks or
	// templates of whatever checkout the suite is running in.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=deploy test", "GIT_AUTHOR_EMAIL=deploy@test",
		"GIT_COMMITTER_NAME=deploy test", "GIT_COMMITTER_EMAIL=deploy@test",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// readableRepo is a checkout git is perfectly happy with: one commit, no
// surprises.
func readableRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", ".")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "first")
	return dir
}

// A checkout git can read keeps its stamp. This is every build on every
// machine that agrees with itself, and the case the fallback must not
// quietly take over.
func TestACheckoutGitCanReadStillStampsItsCommit(t *testing.T) {
	setting, warning := resolveBuildvcs(t, readableRepo(t), "auto")
	if setting != "auto" {
		t.Errorf("a readable checkout resolved to %q: the commit stamp is the default and this drops it", setting)
	}
	if warning != "" {
		t.Errorf("a readable checkout warned about something: %s", warning)
	}

	// And with no argument at all, which is what a bare `go build` would
	// have done anyway.
	if setting, _ = resolveBuildvcs(t, readableRepo(t), ""); setting != "auto" {
		t.Errorf("no setting resolved to %q, not auto", setting)
	}
}

// The failure this script was written for: a repository the building git
// refuses. Written here as an extension it does not recognise, which is
// the same fatal error a workstation's newer git produces against the
// Debian 12 git in the build image, and which needs no second uid to
// arrange.
func TestARepositoryTheBuildingGitRefusesCostsTheStampAndNotTheBuild(t *testing.T) {
	dir := readableRepo(t)
	gitIn(t, dir, "config", "core.repositoryformatversion", "1")
	gitIn(t, dir, "config", "extensions.newergit", "true")

	setting, warning := resolveBuildvcs(t, dir, "auto")
	if setting != "false" {
		t.Fatalf("a repository git cannot read resolved to %q, so `go build` would stop the whole build over a commit stamp", setting)
	}
	// Silence would be its own bug: the operator has to be able to tell
	// an unstamped binary from a stamped one, and to see the cause that
	// `go build`'s own message swallows.
	if !strings.Contains(warning, "without a commit stamp") {
		t.Errorf("the fallback said nothing about dropping the stamp: %q", warning)
	}
	if !strings.Contains(warning, "newergit") {
		t.Errorf("the fallback did not pass on git's own reason, which is the only actionable part: %q", warning)
	}
}

// The other shape of the same thing, and the one the Makefile's own
// comment names: a `.git` that does not lead to a repository from where
// the build runs -- a linked worktree or submodule whose real gitdir is
// outside the mount or the build context.
func TestAGitdirOutsideTheBuildStillBuilds(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"),
		[]byte("gitdir: "+filepath.Join(dir, "does-not-exist")+"\n"), 0o644); err != nil {
		t.Fatalf("writing the .git file: %v", err)
	}
	setting, warning := resolveBuildvcs(t, dir, "auto")
	if setting != "false" {
		t.Fatalf("a .git pointing outside the build resolved to %q", setting)
	}
	if warning == "" {
		t.Error("the fallback said nothing at all")
	}
}

// A tree with no repository in it is not a problem and must not be
// reported as one: `auto` and `false` produce the same binary there, so
// an export or a tarball context passes through in silence.
func TestATreeWithNoRepositoryIsNotWarnedAbout(t *testing.T) {
	setting, warning := resolveBuildvcs(t, t.TempDir(), "auto")
	if setting != "false" {
		t.Errorf("a tree with no repository resolved to %q", setting)
	}
	if warning != "" {
		t.Errorf("a tree with no repository was warned about: %s", warning)
	}
}

// Neither decision a caller states on the command line is second-guessed
// -- including `true` against a repository git cannot read, which is the
// publish paths' "stamp it or fail" and has to keep failing.
func TestAnExplicitSettingIsPassedThroughUntouched(t *testing.T) {
	broken := readableRepo(t)
	gitIn(t, broken, "config", "core.repositoryformatversion", "1")
	gitIn(t, broken, "config", "extensions.newergit", "true")

	for _, tc := range []struct{ dir, setting string }{
		{readableRepo(t), "false"},
		{readableRepo(t), "true"},
		{broken, "true"},
		{t.TempDir(), "false"},
	} {
		if got, _ := resolveBuildvcs(t, tc.dir, tc.setting); got != tc.setting {
			t.Errorf("BUILDVCS=%s resolved to %q: a setting given on the command line is a decision already taken", tc.setting, got)
		}
	}
}

// Every path that compiles a binary has to go through the resolution, or
// the one that does not is the one that fails on a workstation -- and
// every path that *publishes* one has to insist on the stamp instead,
// because a warning in a CI log nobody reads is not a decision anybody
// took.
func TestEveryBuildPathResolvesBuildvcsAndEveryPublishedOneInsistsOnTheStamp(t *testing.T) {
	executable(t, "scripts", "buildvcs.sh")

	contains(t, read(t, "Makefile"), "go build -buildvcs=$$(./scripts/buildvcs.sh $(BUILDVCS))")
	// The deployment image builds grain through `make build`, so the
	// Makefile line above covers that one -- but granule is built in
	// the same stage by a `go build` of its own, out of a module that
	// sits inside this same repository, so it consults the same .git and
	// refuses over it in the same way.
	dockerfile := stripComments(read(t, "Dockerfile"))
	contains(t, dockerfile, "./scripts/buildvcs.sh ${BUILDVCS}")
	for _, line := range strings.Split(dockerfile, "\n") {
		if strings.Contains(line, "go build") && !strings.Contains(line, "-buildvcs") {
			t.Errorf("the Dockerfile compiles something without resolving -buildvcs, so an unreadable .git still stops the image build: %q", strings.TrimSpace(line))
		}
	}
	// The guest build and the guest probe compile out of this same
	// checkout as well -- granule from third_party/granule, a module
	// that sits *inside* this repository and so consults this
	// repository's .git, and grain's own manifest-server beside it.
	// Neither binary is shipped and nothing reads the stamp either one
	// carries, so `auto` stopping those builds would cost a guest image
	// for nothing.
	for _, parts := range [][]string{
		{"scripts", "granule-guest-probe.sh"},
	} {
		script := stripComments(read(t, parts...))
		contains(t, script, "buildvcs.sh")
		for _, line := range strings.Split(script, "\n") {
			if strings.Contains(line, "go build") && !strings.Contains(line, "-buildvcs") {
				t.Errorf("%s compiles something without resolving -buildvcs, so a .git this host's git cannot read stops the guest build over a stamp nothing reads: %q",
					filepath.Join(parts...), strings.TrimSpace(line))
			}
		}
	}

	// Both stages read the copied-in tree as git rather than as somebody
	// else's repository, which is the one cause worth settling in favour
	// of the stamp instead of falling back.
	contains(t, read(t, "Dockerfile"), "git config --global --add safe.directory /src")
	// None of which the image build can run if the script does not reach
	// the build context. Asked of the scripts the build runs by path
	// rather than of the string "scripts", because .dockerignore now also
	// drops a *build output* that happens to live under that directory --
	// the multi-GB kernel and disk images the pre-Granulefile guest build
	// left in scripts/granule/output/, which no build reads and every
	// build on a host that had built a guest was uploading.
	for _, line := range strings.Split(stripComments(read(t, ".dockerignore")), "\n") {
		pattern := strings.TrimSuffix(strings.TrimSpace(line), "/")
		if pattern == "" {
			continue
		}
		for _, script := range []string{"scripts/buildvcs.sh", "scripts/export-source.sh"} {
			if script == pattern || strings.HasPrefix(script, pattern+"/") {
				t.Errorf(".dockerignore drops %s through %q, and the image build runs it out of the context by path",
					script, pattern)
			}
		}
	}

	// The release path insists on the same thing about the *image* it is
	// about to publish, which is the artifact that outlives the log,
	// rather than about the build flag that stood in for it: the commit
	// it labels the image with is the one the release event named, it
	// refuses to push an image labelled with anything else, and where the
	// binary took a stamp of its own that stamp has to agree. That is why
	// it no longer passes BUILDVCS=true -- "stamp it or fail" made a
	// release depend on git inside the build container being able to read
	// a copied-in .git, which fails for environmental reasons alone and
	// fails it after the guest and sandbox images are already published.
	release := read(t, "scripts", "release-images.sh")
	contains(t, release, `GIT_REVISION="$commit"`)
	contains(t, release, "org.opencontainers.image.revision")
	contains(t, release, "go version -m")
	absent(t, stripComments(release), "BUILDVCS=true")
}
