package deploy

// grain 2.0's install layout (task 4903): everything under one root,
// /var/lib/grain, split into data/ (GRAIN_DATA_DIR, the state to back
// up) and scratch/ (GRAIN_SANDBOX_DIR, disposable run space and caches),
// with the root itself root-owned and holding nothing else. These run
// setup.sh's own functions under bash against temporary directories.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// layoutDefaults is the block of setup.sh that names the layout's
// defaults, from the root down to the two variables' own defaults.
func layoutDefaults(t *testing.T) string {
	t.Helper()
	text := setupText(t)
	start := strings.Index(text, `GRAIN_ROOT_DIR="`)
	end := strings.Index(text, `GRAIN_SANDBOX_DIR="${GRAIN_SANDBOX_DIR:-`)
	if start < 0 || end < start {
		t.Fatal("scripts/setup.sh no longer declares GRAIN_ROOT_DIR ahead of GRAIN_SANDBOX_DIR's default")
	}
	end += strings.Index(text[end:], "\n")
	return text[start:end] + "\n"
}

// setupDefault is the value setup.sh gives variable name when this run
// names none, evaluated by bash from setup.sh's own defaults block.
func setupDefault(t *testing.T, name string) string {
	t.Helper()
	cmd := exec.Command("bash", "-c", "set -euo pipefail\nunset GRAIN_DATA_DIR GRAIN_SANDBOX_DIR\n"+
		layoutDefaults(t)+`printf '%s' "${`+name+`}"`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("evaluating setup.sh's default for %s: %v\n%s", name, err, out)
	}
	return string(out)
}

func TestTheDefaultLayoutIsDataAndScratchUnderOneRoot(t *testing.T) {
	if got := setupDefault(t, "GRAIN_ROOT_DIR"); got != "/var/lib/grain" {
		t.Errorf("GRAIN_ROOT_DIR = %q, want /var/lib/grain", got)
	}
	if got := setupDefault(t, "GRAIN_DATA_DIR"); got != "/var/lib/grain/data" {
		t.Errorf("GRAIN_DATA_DIR defaults to %q, want /var/lib/grain/data", got)
	}
	if got := setupDefault(t, "GRAIN_SANDBOX_DIR"); got != "/var/lib/grain/scratch" {
		t.Errorf("GRAIN_SANDBOX_DIR defaults to %q, want /var/lib/grain/scratch", got)
	}
	// An explicit setting keeps exactly what it says.
	cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+layoutDefaults(t)+
		`printf '%s %s %s %s' "$GRAIN_DATA_DIR" "$GRAIN_SANDBOX_DIR" "$GRAIN_DATA_DIR_GIVEN" "$GRAIN_SANDBOX_DIR_GIVEN"`)
	cmd.Env = append(os.Environ(), "GRAIN_DATA_DIR=/srv/grain", "GRAIN_SANDBOX_DIR=/mnt/fast")
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "/srv/grain /mnt/fast 1 1" {
		t.Errorf("explicit GRAIN_DATA_DIR/GRAIN_SANDBOX_DIR came out as %q (%v)", out, err)
	}

	// The help says so, and nothing in setup.sh still names 1.x's paths.
	text := setupText(t)
	contains(t, text, "(default: /var/lib/grain/data)")
	contains(t, text, "(default: /var/lib/grain/scratch)")
	contains(t, text, "never back up\nGRAIN_SANDBOX_DIR (/var/lib/grain/scratch)")
	absent(t, text, "/var/lib/grain"+"-sandbox")
}

// The root is root-owned 0755 and both halves are laid out beneath it in
// the main flow, the root first.
func TestSetupLaysTheRootOutBeforeEitherHalf(t *testing.T) {
	text := setupText(t)
	for _, fn := range []string{"setup_sandbox_dir() {", "setup_data_dir() {"} {
		contains(t, body(t, text, fn), "setup_layout_root")
	}
	root := body(t, text, "setup_layout_root() {")
	contains(t, root, `install -d -m0755 -o root -g root "$GRAIN_ROOT_DIR"`)
	contains(t, body(t, text, "setup_data_dir() {"), `install -d -m0750 -o "$GRAIN_USER" -g "$GRAIN_GROUP" "$GRAIN_DATA_DIR"`)
	contains(t, body(t, text, "setup_sandbox_dir() {"), `install -d -m0755 -o "$GRAIN_USER" -g "$GRAIN_GROUP" "$GRAIN_SANDBOX_DIR"`)
}

// sandboxWarning runs setup_sandbox_dir with install stubbed to mkdir
// (it chowns to a user this test is not) and returns what it printed.
func sandboxWarning(t *testing.T, data, sandbox string, extra string) string {
	t.Helper()
	script := logFn +
		"install() { local a; for a; do :; done; mkdir -p \"$a\"; }\n" +
		"setup_layout_root() { :; }\n" +
		extra +
		shellFunction(t, "same_filesystem") +
		shellFunction(t, "setup_sandbox_dir") +
		"setup_sandbox_dir\n"
	return runBash(t, script, "GRAIN_DATA_DIR="+data, "GRAIN_SANDBOX_DIR="+sandbox,
		"GRAIN_USER=grain", "GRAIN_GROUP=grain", "GRAIN_DEFAULT_SANDBOX_DIR=/var/lib/grain/scratch")
}

func TestTheSandboxInsideDataWarningAsksTheFilesystem(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}

	// Siblings under one root, the default: no warning, whatever disk
	// they are on.
	if out := sandboxWarning(t, data, filepath.Join(root, "scratch"), ""); strings.Contains(out, "WARNING") {
		t.Errorf("data/ and scratch/ side by side warned:\n%s", out)
	}
	// Inside the data directory on the same filesystem: warned.
	out := sandboxWarning(t, data, filepath.Join(data, "scratch"), "")
	contains(t, out, "WARNING: "+filepath.Join(data, "scratch")+" is inside $GRAIN_DATA_DIR")
	contains(t, out, "on the same filesystem")
	// Inside it by path but a separate mount (a different st_dev, which
	// stat is stubbed to report): its own disk, so no warning.
	separate := `stat() { case "$*" in *scratch*) echo 2 ;; *) echo 1 ;; esac; }` + "\n"
	if out := sandboxWarning(t, data, filepath.Join(data, "scratch"), separate); strings.Contains(out, "WARNING") {
		t.Errorf("a separate mount inside the data directory warned:\n%s", out)
	}
	// A path that only shares a prefix is not inside it.
	if out := sandboxWarning(t, data, data+"-scratch", ""); strings.Contains(out, "WARNING") {
		t.Errorf("%s-scratch was taken for a directory inside %s:\n%s", data, data, out)
	}

	// The comparison is by device, as pkg/sysstat groups disks.
	contains(t, body(t, setupText(t), "same_filesystem() {"), "stat -c %d")
}

// layoutScript is setup.sh's layout functions, with GRAIN_ROOT_DIR and
// the defaults moved under root so they can run unprivileged.
func layoutScript(t *testing.T, root string) string {
	t.Helper()
	defaults := regexp.MustCompile(`GRAIN_ROOT_DIR="[^"]*"`).ReplaceAllString(layoutDefaults(t), `GRAIN_ROOT_DIR="`+root+`"`)
	var fns strings.Builder
	for _, fn := range []string{"installed_unit_flag", "resolve_recorded_layout", "refuse_flat_layout",
		"uninstall_path", "uninstall_safe_dir", "uninstall_dir_contents", "uninstall_layout"} {
		fns.WriteString(shellFunction(t, fn))
	}
	return logFn + defaults + fns.String()
}

// The --uninstall and --purge halves that touch the layout, in both
// modes: keep-data keeps data/ and removes scratch/; --purge removes
// both and then the root, once it is empty, and never the root while it
// holds anything else.
func TestUninstallKeepsDataAndPurgeRemovesTheEmptyRoot(t *testing.T) {
	layout := func(t *testing.T) (root, data, scratch string) {
		root = filepath.Join(t.TempDir(), "grain")
		data, scratch = filepath.Join(root, "data"), filepath.Join(root, "scratch")
		for _, f := range []string{filepath.Join(data, "store"), filepath.Join(data, "secrets", "secrets.key"),
			filepath.Join(scratch, "runs", "task-1", "README")} {
			if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }

	t.Run("keep data", func(t *testing.T) {
		root, data, scratch := layout(t)
		runBash(t, layoutScript(t, root)+"uninstall_layout 0\n", "GRAIN_DATA_DIR_GIVEN=", "GRAIN_SANDBOX_DIR_GIVEN=")
		if !exists(filepath.Join(data, "secrets", "secrets.key")) {
			t.Errorf("--uninstall removed %s", data)
		}
		if exists(scratch) {
			t.Errorf("--uninstall kept %s, which is disposable", scratch)
		}
	})
	t.Run("purge", func(t *testing.T) {
		root, _, _ := layout(t)
		out := runBash(t, layoutScript(t, root)+"uninstall_layout 1\n", "GRAIN_DATA_DIR_GIVEN=", "GRAIN_SANDBOX_DIR_GIVEN=")
		if exists(root) {
			t.Errorf("--purge left %s behind though nothing else was in it:\n%s", root, out)
		}
	})
	t.Run("purge keeps a root that holds something else", func(t *testing.T) {
		root, data, scratch := layout(t)
		other := filepath.Join(root, "operator-notes")
		if err := os.WriteFile(other, []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
		out := runBash(t, layoutScript(t, root)+"uninstall_layout 1\n", "GRAIN_DATA_DIR_GIVEN=", "GRAIN_SANDBOX_DIR_GIVEN=")
		if exists(data) || exists(scratch) {
			t.Errorf("--purge kept data/ or scratch/:\n%s", out)
		}
		if !exists(other) {
			t.Errorf("--purge removed %s, which setup.sh never wrote:\n%s", other, out)
		}
		contains(t, out, "kept "+root+": it is not empty (operator-notes)")
	})
	t.Run("keep data inside the sandbox directory", func(t *testing.T) {
		root, _, _ := layout(t)
		data := filepath.Join(root, "scratch", "data")
		if err := os.MkdirAll(data, 0o755); err != nil {
			t.Fatal(err)
		}
		out := runBash(t, layoutScript(t, root)+"uninstall_layout 0\n",
			"GRAIN_DATA_DIR="+data, "GRAIN_DATA_DIR_GIVEN=1", "GRAIN_SANDBOX_DIR_GIVEN=")
		if !exists(data) {
			t.Errorf("--uninstall removed the data directory inside %s/scratch:\n%s", root, out)
		}
	})

	// uninstall_grain uses these, after reading back what was installed.
	ug := body(t, setupText(t), "uninstall_grain() {")
	contains(t, ug, "resolve_recorded_layout")
	contains(t, ug, `uninstall_layout "$purge"`)
	before(t, ug, "resolve_recorded_layout", "Stopping and removing grain's systemd units",
		"uninstall_grain must read the recorded layout before it removes the unit and cli.env that record it")
	absent(t, body(t, setupText(t), "uninstall_layout() {"), `uninstall_path "$GRAIN_ROOT_DIR"`)
}

// A re-run on a 1.x host whose cli.env records /var/lib/grain keeps it,
// and the sandbox directory its unit names, and says so in one line.
func TestAReRunKeepsAnOldRecordedDataDirectory(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "grain")
	env := filepath.Join(dir, "cli.env")
	unit := filepath.Join(dir, "grain-daemon.service")
	if err := os.WriteFile(env, []byte("# written by setup.sh\nGRAIN_SERVER=http://127.0.0.1:4242\nGRAIN_DATA_DIR="+root+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unit, []byte("[Service]\nExecStart=/usr/local/bin/grain daemon \\\n  -data-dir "+root+" \\\n  -sandbox-dir /srv/old-sandbox \\\n  -ui-addr 0.0.0.0:4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The 1.x data directory is the root itself, store and all: kept, not
	// refused, since it is the one this deployment recorded.
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "local-api-token"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := layoutScript(t, root) +
		"resolve_recorded_layout\nrefuse_flat_layout\n" +
		`echo "DATA=$GRAIN_DATA_DIR SANDBOX=$GRAIN_SANDBOX_DIR"` + "\n"
	vars := []string{"CLI_ENV_FILE=" + env, "GRAIN_DAEMON_UNIT=" + unit}

	out := runBash(t, script, append(vars, "GRAIN_DATA_DIR=", "GRAIN_SANDBOX_DIR=")...)
	if want := "DATA=" + root + " SANDBOX=/srv/old-sandbox"; lastLine(out) != want {
		t.Errorf("a re-run settled on %q, want %q: the recorded layout kept", lastLine(out), want)
	}
	if n := strings.Count(out, "==> "); n != 1 {
		t.Errorf("a re-run printed %d lines about the layout, want one:\n%s", n, out)
	}
	contains(t, out, "Data directory: keeping "+root+", recorded in "+env)
	contains(t, out, root+"/data and "+root+"/scratch; nothing is moved")
	if _, err := os.Stat(filepath.Join(root, "data")); err == nil {
		t.Errorf("a re-run laid %s/data out beside the recorded data directory", root)
	}

	// Told explicitly, the run keeps what it was told.
	out = runBash(t, script, append(vars, "GRAIN_DATA_DIR=/srv/new", "GRAIN_SANDBOX_DIR=/srv/scratch")...)
	if lastLine(out) != "DATA=/srv/new SANDBOX=/srv/scratch" {
		t.Errorf("explicit settings came out as %q", lastLine(out))
	}

	// A 2.0 install's own record says nothing.
	if err := os.WriteFile(env, []byte("GRAIN_DATA_DIR="+root+"/data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "local-api-token")); err != nil {
		t.Fatal(err)
	}
	out = runBash(t, script, append(vars, "GRAIN_DATA_DIR=", "GRAIN_SANDBOX_DIR=")...)
	if strings.Contains(out, "==> ") {
		t.Errorf("a re-run of a 2.0 install talked about its layout:\n%s", out)
	}

	// The install path runs both, after the uninstall branch.
	text := setupText(t)
	before(t, text, "  uninstall_grain\n  exit 0\nfi\n", "\nresolve_recorded_layout\nif ! refuse_flat_layout; then\n  exit 2\nfi\n",
		"the install path must resolve the recorded layout, and refuse a flat one, once the uninstall branch is behind it")
}

// A fresh install on a host whose /var/lib/grain is a 1.x flat data
// directory refuses, rather than laying data/ out beside it.
func TestAFreshInstallRefusesAnOldFlatDataDirectory(t *testing.T) {
	for _, leftover := range []string{"store", "store.db", "local-api-token"} {
		t.Run(leftover, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "grain")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, leftover), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+layoutScript(t, root)+"refuse_flat_layout\necho laid-out\n")
			cmd.Env = append(os.Environ(), "GRAIN_DATA_DIR=", "GRAIN_SANDBOX_DIR=", "CLI_ENV_FILE=/nonexistent")
			out, err := cmd.CombinedOutput()
			if err == nil || strings.Contains(string(out), "laid-out") {
				t.Fatalf("a fresh install over %s/%s went ahead:\n%s", root, leftover, out)
			}
			contains(t, string(out), "holds a grain 1.x data directory")
			contains(t, string(out), "move the old data")
			if _, err := os.Stat(filepath.Join(root, "data")); err == nil {
				t.Errorf("the refusal laid %s/data out anyway", root)
			}
		})
	}

	// An empty root, or one already laid out, goes ahead.
	root := filepath.Join(t.TempDir(), "grain")
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data", "local-api-token"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	runBash(t, layoutScript(t, root)+"refuse_flat_layout\n", "GRAIN_DATA_DIR=", "GRAIN_SANDBOX_DIR=")
}
