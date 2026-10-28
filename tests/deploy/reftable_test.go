package deploy

// scripts/setup.sh's convert_reftable_repos (task 410): a repository
// under the data or sandbox directories whose refs a newer host git put in
// reftable is converted to the files backend before the cutover, since the
// git in the daemon image cannot open it -- or, when this host's git cannot
// convert it, the run warns, records it in REFTABLE_LEFT for the summary,
// and carries on. Run, not read: the block is sourced with log, step_start,
// step_done, as_repo_owner and systemctl stubbed.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	reftableBegin = "# --- 5b. convert reftable repositories to the files ref format"
	reftableEnd   = "# --- end of reftable repositories"
)

// convertReftable runs convert_reftable_repos over dataDir, with
// systemctl reporting the daemon active when active is set, and returns
// its output (systemctl's calls and REFTABLE_CONVERTED/REFTABLE_LEFT
// included) and whether it succeeded.
func convertReftable(t *testing.T, dataDir string, active bool) (string, bool) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	lib := filepath.Join(t.TempDir(), "reftable.sh")
	if err := os.WriteFile(lib, []byte(between(t, setupText(t), reftableBegin, reftableEnd)), 0o644); err != nil {
		t.Fatal(err)
	}
	isActive := "1"
	if active {
		isActive = "0"
	}
	sandboxDir := filepath.Join(t.TempDir(), "sandboxes")
	cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+
		"log() { echo \"==> $*\"; }\n"+
		"step_start() { log \"$*\"; }\n"+
		"step_done() { log \"  ... ${1:-done} in 0s\"; }\n"+
		"systemctl() { echo \"systemctl $*\"; [ \"$1\" != is-active ] || return "+isActive+"; }\n"+
		"source \"$1\"\n"+
		"as_repo_owner() { shift; env HOME=/nonexistent GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_DEFAULT_REF_FORMAT=files \"$@\"; }\n"+
		"convert_reftable_repos || exit 3\n"+
		"if [ \"${#REFTABLE_CONVERTED[@]}\" -gt 0 ]; then printf 'REFTABLE_CONVERTED: %s\\n' \"${REFTABLE_CONVERTED[@]}\"; fi\n"+
		"if [ \"${#REFTABLE_LEFT[@]}\" -gt 0 ]; then printf 'REFTABLE_LEFT: %s\\n' \"${REFTABLE_LEFT[@]}\"; fi\n", "bash", lib)
	cmd.Env = append(os.Environ(), "GRAIN_USER=grain", "GRAIN_DATA_DIR="+dataDir, "GRAIN_SANDBOX_DIR="+sandboxDir)
	b, err := cmd.CombinedOutput()
	if err != nil {
		if _, isExit := err.(*exec.ExitError); !isExit {
			t.Fatalf("running bash: %v", err)
		}
	}
	return string(b), err == nil
}

func refGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}

// markReftable makes dir's config say reftable, the way an old git reads
// a repository a new one made: it cannot make a real one, and its
// `refs migrate` does not exist, so the conversion fails.
func markReftable(t *testing.T, dir string) {
	t.Helper()
	refGit(t, dir, "init", "--quiet")
	config := filepath.Join(dir, ".git", "config")
	b, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(b), "repositoryformatversion = 0", "repositoryformatversion = 1", 1) +
		"[extensions]\n\trefStorage = reftable\n"
	if err := os.WriteFile(config, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSetupReftableLeavesFilesRepositoriesAlone(t *testing.T) {
	data := t.TempDir()
	state := filepath.Join(data, "state-repo")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	refGit(t, state, "init", "--quiet")
	out, ok := convertReftable(t, data, true)
	if !ok {
		t.Fatalf("convert_reftable_repos failed over a files repository:\n%s", out)
	}
	if out != "" {
		t.Errorf("convert_reftable_repos said something, or stopped the daemon, with nothing to convert:\n%s", out)
	}
}

func TestSetupReftableWarnsWhenItCannotConvert(t *testing.T) {
	if b, _ := exec.Command("git", "refs", "migrate", "-h").CombinedOutput(); strings.Contains(string(b), "--ref-format") {
		t.Skip("this git can convert reftable repositories; the conversion test covers it")
	}
	data := t.TempDir()
	state := filepath.Join(data, "state-repo")
	team := filepath.Join(data, "team-state-repos", "alpha")
	for _, dir := range []string{state, team} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		markReftable(t, dir)
	}
	out, ok := convertReftable(t, data, true)
	if !ok {
		t.Fatalf("convert_reftable_repos exited non-zero instead of warning and carrying on:\n%s", out)
	}
	for _, want := range []string{
		"systemctl stop grain-daemon.service",
		"setup.sh: warning: " + filepath.Join(state, ".git") + " is a reftable git repository",
		"git --git-dir=" + filepath.Join(state, ".git") + " refs migrate --ref-format=files",
		"git --git-dir=" + filepath.Join(team, ".git") + " refs migrate --ref-format=files",
		"0 converted, 2 left",
		"REFTABLE_LEFT: " + filepath.Join(state, ".git") + " (reftable): convert with git 2.46 or newer",
		"REFTABLE_LEFT: " + filepath.Join(team, ".git") + " (reftable): convert with git 2.46 or newer",
	} {
		contains(t, out, want)
	}
}

func TestSetupReftableConvertsWithANewEnoughGit(t *testing.T) {
	data := t.TempDir()
	state := filepath.Join(data, "state-repo")
	if b, err := exec.Command("git", "init", "--quiet", "--ref-format=reftable", state).CombinedOutput(); err != nil {
		t.Skipf("this git cannot make a reftable repository: %v: %s", err, b)
	}
	if err := os.WriteFile(filepath.Join(state, "dump.txt"), []byte("rows\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refGit(t, state, "add", "dump.txt")
	refGit(t, state, "commit", "--quiet", "-m", "export")
	refGit(t, state, "remote", "add", "origin", "https://example.com/grain/config.git")
	head := refGit(t, state, "rev-parse", "HEAD")
	out, ok := convertReftable(t, data, false)
	if !ok {
		t.Fatalf("convert_reftable_repos failed:\n%s", out)
	}
	gitDir := filepath.Join(state, ".git")
	contains(t, out, "converted "+gitDir+": reftable -> files")
	contains(t, out, "1 converted, 0 left")
	contains(t, out, "REFTABLE_CONVERTED: "+gitDir+" (reftable -> files,")
	if got := refGit(t, state, "rev-parse", "--show-ref-format"); got != "files" {
		t.Errorf("ref format after conversion = %q, want files", got)
	}
	if got := refGit(t, state, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD after conversion = %s, want %s", got, head)
	}
	if got := refGit(t, state, "remote", "get-url", "origin"); got != "https://example.com/grain/config.git" {
		t.Errorf("origin after conversion = %q", got)
	}
}

func TestSetupConvertsReftableBeforeTheStateRepository(t *testing.T) {
	code := setupCode(t)
	before(t, code, "\n  convert_reftable_repos\n", "\n  configure_state_repo\n",
		"setup_data_dir converts reftable repositories before the image's git first opens the state repository")
	if n := strings.Count(code, "\nreftable_repos() {"); n != 1 {
		t.Errorf("reftable_repos() defined %d times in setup.sh, want 1", n)
	}
	if n := strings.Count(code, "\nconvert_reftable_repos() {"); n != 1 {
		t.Errorf("convert_reftable_repos() defined %d times in setup.sh, want 1", n)
	}
	if n := strings.Count(code, "\n  convert_reftable_repos\n"); n != 1 {
		t.Errorf("convert_reftable_repos called %d times in setup.sh, want 1", n)
	}
}
