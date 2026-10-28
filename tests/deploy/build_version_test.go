package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The version every build carries (scripts/build-version.sh,
// docs/design/release-from-trunk.md's "Version Numbering Rules"), asked
// of the real script against a repository laid out the way release from
// trunk leaves grain's: v2.1.0 promoted on a main commit and patched on a
// release branch, v2.2.0-rc.1 tagged on main and dropped after its soak,
// v2.2.0-rc.2 tagged on a later main commit with candidate pointing at
// it, and main carrying on past both.

// versionRepo is that repository, and the commits each branch's tip is.
func versionRepo(t *testing.T) (string, map[string]string) {
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
	commit := func(msg string) string {
		git("commit", "-q", "--allow-empty", "-m", msg)
		return git("rev-parse", "HEAD")
	}
	tips := map[string]string{}
	git("init", "-q", "-b", "main")
	commit("start")
	// v2.1, cut on main and promoted on the same commit after its soak.
	tips["v2.1.0"] = commit("cut v2.1")
	git("tag", "-a", "v2.1.0-rc.1", "-m", "Release candidate v2.1.0-rc.1")
	git("tag", "-a", "v2.1.0", "-m", "Release v2.1.0")
	git("tag", "-a", "latest", "-m", "Latest release v2.1.0")
	// A patch branch off it, two picks along.
	git("checkout", "-q", "-b", "release/v2.1.1", "v2.1.0")
	commit("a pick")
	tips["release/v2.1.1"] = commit("another pick")
	// main: v2.2.0-rc.1 cut and dropped, rc.2 cut later, candidate on it.
	git("checkout", "-q", "main")
	commit("a feature")
	tips["v2.2.0-rc.1"] = commit("cut v2.2 rc.1")
	git("tag", "-a", "v2.2.0-rc.1", "-m", "Release candidate v2.2.0-rc.1")
	git("tag", "-a", "v2.2.0-rc.1-discarded", "-m", "Release candidate v2.2.0-rc.1 failed its staging soak")
	commit("a fix")
	tips["v2.2.0-rc.2"] = commit("cut v2.2 rc.2")
	git("tag", "-a", "v2.2.0-rc.2", "-m", "Release candidate v2.2.0-rc.2")
	git("branch", "candidate")
	commit("main goes on")
	commit("and on")
	tips["main"] = commit("and on again")
	// A legacy candidate that carried ports past its rc.
	git("checkout", "-q", "-b", "old-candidate", "v2.2.0-rc.2")
	commit("a port")
	tips["old-candidate"] = commit("another port")
	git("checkout", "-q", "main")
	return dir, tips
}

// commitConf commits scripts/release.conf with body onto dir's checkout.
func commitConf(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "release.conf"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "scripts/release.conf"}, {"commit", "-q", "-m", "release.conf"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func buildVersion(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(repoRoot(t), "scripts", "build-version.sh")}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("build-version.sh %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

func TestBuildVersionOfEachBranch(t *testing.T) {
	dir, tips := versionRepo(t)
	short := func(name string) string { return tips[name][:7] }
	for name, tc := range map[string]struct {
		branch, commit, want string
	}{
		// main: the minor after the highest promoted release, counted
		// from it -- neither v2.2.0 rc on main moves it on.
		"main": {"main", tips["main"], "v2.2.0-dev.7+" + short("main")},
		// main at an rc's own commit is still a dev build of that minor.
		"main at an rc": {"main", tips["v2.2.0-rc.2"], "v2.2.0-dev.4+" + short("v2.2.0-rc.2")},
		// main at the release itself is the next minor's first dev build.
		"main at the release": {"main", tips["v2.1.0"], "v2.2.0-dev.0+" + short("v2.1.0")},
		// candidate: exactly the rc it points at.
		"candidate": {"candidate", tips["v2.2.0-rc.2"], "v2.2.0-rc.2"},
		// A candidate past its rc: the rc, plus the commits since.
		"candidate past its rc": {"candidate", tips["old-candidate"], "v2.2.0-rc.2.2+" + short("old-candidate")},
		// A patch branch: below the patch, above the release it patches.
		"patch branch": {"release/v2.1.1", tips["release/v2.1.1"], "v2.1.1-rc.0.2+" + short("release/v2.1.1")},
		// Any other branch is versioned as main.
		"task branch": {"grain/task-1", tips["main"], "v2.2.0-dev.7+" + short("main")},
		// A legacy rc branch is versioned as candidate.
		"rc branch": {"rc/v2.2", tips["old-candidate"], "v2.2.0-rc.2.2+" + short("old-candidate")},
	} {
		t.Run(name, func(t *testing.T) {
			if got := buildVersion(t, dir, "--branch", tc.branch, "--commit", tc.commit); got != tc.want {
				t.Errorf("build-version.sh --branch %s = %q, want %q", tc.branch, got, tc.want)
			}
		})
	}
}

// Once v2.2.0 is promoted, on rc.2's commit, main moves on to v2.3.0.
func TestBuildVersionMovesOnWhenTheReleaseIsPromoted(t *testing.T) {
	dir, tips := versionRepo(t)
	cmd := exec.Command("git", "tag", "-a", "v2.2.0", "-m", "Release v2.2.0", tips["v2.2.0-rc.2"])
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag v2.2.0: %v\n%s", err, out)
	}
	if got, want := buildVersion(t, dir, "--branch", "main", "--commit", tips["main"]), "v2.3.0-dev.3+"+tips["main"][:7]; got != want {
		t.Errorf("build-version.sh on main after v2.2.0 = %q, want %q", got, want)
	}
}

// A release waiting for a person's approval is not a release: tagging
// rc.2 v2.2.0-pending (docs/design/release-from-trunk.md, §4) leaves
// main's builds versioned from v2.1.0, exactly as before, and candidate's
// from its rc. Only the approval's v2.2.0 moves them on.
func TestBuildVersionIgnoresAPendingRelease(t *testing.T) {
	dir, tips := versionRepo(t)
	before := map[string]string{}
	for _, branch := range []string{"main", "candidate"} {
		before[branch] = buildVersion(t, dir, "--branch", branch, "--commit", tips[map[string]string{"main": "main", "candidate": "v2.2.0-rc.2"}[branch]])
	}
	cmd := exec.Command("git", "tag", "-a", "v2.2.0-pending", "-m", "Release v2.2.0 pending approval", tips["v2.2.0-rc.2"])
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag v2.2.0-pending: %v\n%s", err, out)
	}
	if got := buildVersion(t, dir, "--branch", "main", "--commit", tips["main"]); got != before["main"] || strings.HasPrefix(got, "v2.3.0") {
		t.Errorf("build-version.sh on main with v2.2.0 pending = %q, want %q: a pending release is not promoted", got, before["main"])
	}
	if got := buildVersion(t, dir, "--branch", "candidate", "--commit", tips["v2.2.0-rc.2"]); got != before["candidate"] {
		t.Errorf("build-version.sh on candidate with v2.2.0 pending = %q, want %q", got, before["candidate"])
	}
}

// RELEASE_FLOOR_MINOR raises the next release's minor, and a release
// RELEASE_NEVER_PROMOTE names is stepped over; an rc it names is not a
// release, and steps over nothing.
func TestBuildVersionFollowsTheFloorAndNeverPromote(t *testing.T) {
	for conf, want := range map[string]string{
		"RELEASE_MAJOR=2\nRELEASE_FLOOR_MINOR=3\n":                                                "v2.3.0-dev.",
		"RELEASE_MAJOR=2\nRELEASE_FLOOR_MINOR=3\nRELEASE_NEVER_PROMOTE=\"v1.14.0 v2.3.0-rc.1\"\n": "v2.3.0-dev.",
		"RELEASE_MAJOR=2\nRELEASE_FLOOR_MINOR=3\nRELEASE_NEVER_PROMOTE=\"v2.3.0\" # skipped\n":    "v2.4.0-dev.",
		"RELEASE_MAJOR=2\nRELEASE_FLOOR_MINOR=1\n":                                                "v2.2.0-dev.",
	} {
		dir, _ := versionRepo(t)
		commitConf(t, dir, conf)
		if got := buildVersion(t, dir, "--branch", "main"); !strings.HasPrefix(got, want+"8+") {
			t.Errorf("build-version.sh with release.conf %q = %q, want %s8+<sha>", conf, got, want)
		}
	}
}

// A placeholder vX.Y.0 tag on main -- v2.5.0, a version number that was
// never released, which RELEASE_NEVER_PROMOTE names -- still uses its
// minor up: main builds the minor after it, so version numbers never go
// back to one the legacy cut already used.
func TestBuildVersionStepsPastAPlaceholderRelease(t *testing.T) {
	dir, _ := versionRepo(t)
	commitConf(t, dir, "RELEASE_MAJOR=2\nRELEASE_FLOOR_MINOR=3\nRELEASE_NEVER_PROMOTE=\"v1.14.0 v2.3.0-rc.1 v2.4.0-rc.1 v2.5.0\"\n")
	cmd := exec.Command("git", "tag", "-a", "v2.5.0", "-m", "Version-number placeholder, not a release")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag v2.5.0: %v\n%s", err, out)
	}
	if got := buildVersion(t, dir, "--branch", "main"); !strings.HasPrefix(got, "v2.6.0-dev.0+") {
		t.Errorf("build-version.sh on main at the v2.5.0 placeholder = %q, want v2.6.0-dev.0+<sha>", got)
	}
}

// The checked-out branch, and GRAIN_CI_REF/GRAIN_CI_COMMIT, are what it
// reads when not told.
func TestBuildVersionReadsTheCheckout(t *testing.T) {
	dir, tips := versionRepo(t)
	cmd := exec.Command("git", "checkout", "-q", "old-candidate")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout old-candidate: %v\n%s", err, out)
	}
	if got, want := buildVersion(t, dir, "--branch", "candidate"), "v2.2.0-rc.2.2+"+tips["old-candidate"][:7]; got != want {
		t.Errorf("build-version.sh of the checkout = %q, want %q", got, want)
	}
	if got, want := buildVersion(t, dir), "v2.2.0-dev.6+"+tips["old-candidate"][:7]; got != want {
		t.Errorf("build-version.sh on old-candidate, a branch versioned as main = %q, want %q", got, want)
	}
}

// A RELEASE_MAJOR above every promoted major makes main's next release
// that major's first, so main's builds are its -dev.
func TestBuildVersionFollowsReleaseMajor(t *testing.T) {
	dir, _ := versionRepo(t)
	commitConf(t, dir, "# the next major\nRELEASE_MAJOR=3\n")
	if got := buildVersion(t, dir, "--branch", "main"); !strings.HasPrefix(got, "v3.0.0-dev.8+") {
		t.Errorf("build-version.sh after RELEASE_MAJOR=3 = %q, want v3.0.0-dev.8+<sha>", got)
	}
}

// No tags, or no git: nothing, and still success -- the build goes on
// unversioned, which the binary then says of itself.
func TestBuildVersionWithNothingToCountFromIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if got := buildVersion(t, dir, "--branch", "main"); got != "" {
		t.Errorf("build-version.sh outside a checkout = %q, want nothing", got)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "untagged"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	for _, branch := range []string{"main", "candidate", "release/v2.1.1"} {
		if got := buildVersion(t, dir, "--branch", branch); got != "" {
			t.Errorf("build-version.sh on an untagged %s = %q, want nothing", branch, got)
		}
	}
}

// `make build` stamps what the script works out, through the same -X the
// release has always used, and a RELEASE given to make wins.
func TestMakefileStampsTheBuildVersion(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skipf("make not on PATH: %v", err)
	}
	dir, tips := versionRepo(t)
	ask := func(extra ...string) string {
		t.Helper()
		args := append([]string{"-s", "--no-print-directory", "-C", repoRoot(t),
			"--eval", "print-test-version: ; @echo '$(LDFLAGS)'", "print-test-version"}, extra...)
		cmd := exec.Command("make", args...)
		cmd.Env = append(os.Environ(), "GIT_DIR="+filepath.Join(dir, ".git"),
			"GRAIN_CI_REF=candidate", "GRAIN_CI_COMMIT="+tips["old-candidate"])
		cmd.Env = withoutEnv(cmd.Env, "RELEASE")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("make print-test-version: %v\n%s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	want := "-X github.com/bwsalmon/grain/pkg/version.Release=v2.2.0-rc.2.2+" + tips["old-candidate"][:7]
	if got := ask(); !strings.Contains(got, want) {
		t.Errorf("the Makefile's LDFLAGS = %q, want them to carry %q", got, want)
	}
	if got := ask("RELEASE=v2.2.0"); !strings.Contains(got, "version.Release=v2.2.0") || strings.Contains(got, "rc.1") {
		t.Errorf("with RELEASE=v2.2.0 the Makefile's LDFLAGS = %q, want the release given", got)
	}
}

func withoutEnv(env []string, name string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, name+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// What a release job stamps on each branch, through the build phase's
// dry run: the -X ldflag both daemon binaries get and the OCI version
// label both images get, and the caller's RELEASE winning.
func TestReleaseImagesDryRunNamesTheBuildVersionOfEachBranch(t *testing.T) {
	dir, tips := versionRepo(t)
	for name, tc := range map[string]struct {
		branch, commit, release, want string
	}{
		"main":          {"main", tips["main"], "", "v2.2.0-dev.7+" + tips["main"][:7]},
		"candidate":     {"candidate", tips["old-candidate"], "", "v2.2.0-rc.2.2+" + tips["old-candidate"][:7]},
		"rc tag commit": {"candidate", tips["v2.2.0-rc.2"], "", "v2.2.0-rc.2"},
		"patch branch":  {"release/v2.1.1", tips["release/v2.1.1"], "", "v2.1.1-rc.0.2+" + tips["release/v2.1.1"][:7]},
		"given RELEASE": {"candidate", tips["old-candidate"], "v2.2.0", "v2.2.0"},
	} {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{
				"PATH":            realToolPath(t),
				"GIT_DIR":         filepath.Join(dir, ".git"),
				"GRAIN_CI_REF":    tc.branch,
				"GRAIN_CI_COMMIT": tc.commit,
			}
			if tc.release != "" {
				env["RELEASE"] = tc.release
			}
			out, _, err := releaseImagesRunArgs(t, []string{"--dry-run", "build"}, env)
			if err != nil {
				t.Fatalf("release-images.sh --dry-run build: %v\n%s", err, out)
			}
			short := tc.commit[:7]
			containsRun(t, out, "[dry-run] version: "+tc.want+", stamped into both daemon binaries as "+
				"-X github.com/bwsalmon/grain/pkg/version.Release="+tc.want+
				" and labelled onto ghcr.io/bwsalmon/grain/grain:sha-"+short+
				" and ghcr.io/bwsalmon/grain/google-grain:sha-"+short+
				" as org.opencontainers.image.version="+tc.want)
		})
	}
}

// The build phase fetches tags before it counts from them: a CI checkout
// is not promised to have them.
func TestReleaseImagesFetchesTagsForTheBuildVersion(t *testing.T) {
	script := stripComments(read(t, "scripts", "build-version.sh"))
	_ = script
	releaseScript := stripComments(read(t, "scripts", "release-images.sh"))
	contains(t, releaseScript, `version_args=(--branch "$GRAIN_CI_REF" --commit "$commit")`)
	contains(t, releaseScript, `version_args+=(--fetch "$remote")`)
	contains(t, releaseScript, `build-version.sh" "${version_args[@]}"`)
	contains(t, stripComments(read(t, "scripts", "build-version.sh")), `git fetch -q --force --tags "$fetch"`)
}

// cmd/grit carries its own release tags (grit/vX.Y.Z), separate from grain's
// vX.Y.Z tags. --tag-prefix grit/ counts from grit/v* tags alone.
func TestBuildVersionSupportsGritTagPrefix(t *testing.T) {
	dir, tips := versionRepo(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("tag", "-a", "grit/v0.1.0", tips["v2.1.0"], "-m", "grit v0.1.0")
	git("tag", "-a", "grit/v0.2.0-rc.1", tips["v2.2.0-rc.2"], "-m", "grit v0.2.0-rc.1")

	wantMain := "grit/v0.2.0-dev.7+" + tips["main"][:7]
	if got := buildVersion(t, dir, "--tag-prefix", "grit/", "--branch", "main", "--commit", tips["main"]); got != wantMain {
		t.Errorf("build-version.sh --tag-prefix grit/ on main = %q, want %q", got, wantMain)
	}
	if got := buildVersion(t, dir, "--tag-prefix", "grit/", "--branch", "grit/candidate", "--commit", tips["v2.2.0-rc.2"]); got != "grit/v0.2.0-rc.1" {
		t.Errorf("build-version.sh --tag-prefix grit/ on rc commit = %q, want grit/v0.2.0-rc.1", got)
	}
	// Grain's own version is unaffected by grit/v* tags on the same commits.
	wantGrain := "v2.2.0-dev.7+" + tips["main"][:7]
	if got := buildVersion(t, dir, "--branch", "main", "--commit", tips["main"]); got != wantGrain {
		t.Errorf("build-version.sh without prefix after grit/v* tags = %q, want %q", got, wantGrain)
	}
}
