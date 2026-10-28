package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// grain/task-3688: a fresh setup.sh deployment had no account with a
// password, a CLI that looked for its daemon on the wrong port, and
// nothing anywhere saying how to become a system admin. These run the
// functions that fix that, one at a time, under bash.

const logFn = "log() { echo \"==> $*\"; }\n"

// The CLI's config file says where the daemon listens and where its
// data lives, as KEY=VALUE data the CLI parses without sourcing.
func TestSetupWritesTheCLIConfigFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "etc", "grain", "cli.env")
	runBash(t, logFn+shellFunction(t, "write_cli_env")+"write_cli_env",
		"CLI_ENV_FILE="+file, "GRAIN_UI_ADDR=127.0.0.1:80", "GRAIN_DATA_DIR=/var/lib/grain/data")
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\nGRAIN_SERVER=http://127.0.0.1:80\n", "\nGRAIN_DATA_DIR=/var/lib/grain/data\n"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("%s does not say %q:\n%s", file, strings.TrimSpace(want), got)
		}
	}
	if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o644 {
		t.Errorf("%s is %v, want 0644: every user's CLI reads it", file, fi.Mode().Perm())
	}

	// No port, no server line -- but the data directory still.
	runBash(t, logFn+shellFunction(t, "write_cli_env")+"write_cli_env",
		"CLI_ENV_FILE="+file, "GRAIN_UI_ADDR=localhost", "GRAIN_DATA_DIR=/var/lib/grain/data")
	got, _ = os.ReadFile(file)
	if strings.Contains(string(got), "GRAIN_SERVER=") || !strings.Contains(string(got), "GRAIN_DATA_DIR=/var/lib/grain/data") {
		t.Errorf("with no port in GRAIN_UI_ADDR, %s =\n%s", file, got)
	}
}

// Both modes write it: install_cli_wrappers ends in write_cli_profile
// either way, and that writes the file first.
func TestBothModesWriteTheCLIConfigFile(t *testing.T) {
	text := setupText(t)
	contains(t, body(t, text, "write_cli_profile() {"), "write_cli_env")
	wrappers := body(t, text, "install_cli_wrappers() {")
	if strings.Count(wrappers, "write_cli_profile") < 2 {
		t.Errorf("install_cli_wrappers no longer calls write_cli_profile in both modes")
	}
	// The path the CLI reads by default.
	contains(t, text, `CLI_ENV_FILE="${CLI_ENV_FILE:-/etc/grain/cli.env}"`)
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "pkg", "cli", "cliconn.go"))
	if err != nil {
		t.Fatal(err)
	}
	contains(t, string(src), `defaultCLIEnvFile = "/etc/grain/cli.env"`)
}

// GRAIN_ADMIN_USER defaults to whoever ran sudo, and an explicit empty
// value means nobody.
func TestTheAdminUserDefaultsToWhoeverRanSudo(t *testing.T) {
	block := between(t, setupText(t), `if [ "${GRAIN_ADMIN_USER+set}" != set ]; then`, "\nfi\n")
	script := block + "\nfi\nprintf '[%s]' \"$GRAIN_ADMIN_USER\""
	for _, c := range []struct {
		name string
		env  []string
		want string
	}{
		{"sudo", []string{"SUDO_USER=bsalmon"}, "[bsalmon]"},
		{"named", []string{"SUDO_USER=bsalmon", "GRAIN_ADMIN_USER=alice"}, "[alice]"},
		{"turned off", []string{"SUDO_USER=bsalmon", "GRAIN_ADMIN_USER="}, "[]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cmd := "unset GRAIN_ADMIN_USER SUDO_USER\n"
			for _, kv := range c.env {
				cmd += "export " + kv + "\n"
			}
			if got := runBash(t, cmd+script); got != c.want {
				t.Errorf("GRAIN_ADMIN_USER = %s, want %s", got, c.want)
			}
		})
	}
}

// signInFns is report_sign_in and the functions it calls.
func signInFns(t *testing.T) string {
	t.Helper()
	return shellFunction(t, "deployment_tenancy") + shellFunction(t, "report_cli_access") +
		shellFunction(t, "report_sign_in")
}

// A single-tenant deployment has no accounts: the report sends every
// caller, localhost included, to the access code, and says nothing of
// passwords, admin accounts, loopback trust or Admin mode (task 4498).
func TestTheReadinessReportSaysSingleTenantCallersUseTheAccessCode(t *testing.T) {
	script := shellFunction(t, "ui_addr_is_wildcard") + shellFunction(t, "ui_network_url") +
		signInFns(t) + "report_sign_in"
	env := []string{"GRAIN_UI_ADDR=127.0.0.1:4242", "GRAIN_DATA_DIR=" + t.TempDir(), "GRAIN_USER=grain",
		"GRAIN_GROUP=grain", "ADMIN_USER_STATUS=single",
		"GRAIN_HOSTD=1", "SUDO_USER=bsalmon", "CLI_ENV_FILE=/etc/grain/cli.env"}
	out := runBash(t, script, env...)
	for _, want := range []string{"with the access code (grain access-code", "localhost included",
		"grain access-code rotate", "sudo -u grain grain list"} {
		contains(t, out, want)
	}
	for _, wrong := range []string{"nobody signs in", "are the operator", "password", "system admin", `"Admin mode"`} {
		absent(t, out, wrong)
	}

	// The readiness block around it and the Done block above it say the
	// same, and neither offers loopback trust or password accounts.
	readiness := body(t, setupText(t), "report_readiness() {")
	contains(t, readiness, `if [ "$(deployment_tenancy)" = single ]; then`)
	contains(t, readiness, "every caller, localhost included, presents the access code")
	summary := body(t, setupText(t), "print_summary() {")
	contains(t, summary, `[ "$(deployment_tenancy)" = single ]`)
	contains(t, summary, "every visitor, localhost included, enters the")
}

// The daemon is single-tenant, unless its build predates the
// single-tenant engine (DAEMON_PREDATES_TENANCY), which is always multi.
func TestDeploymentTenancyIsSingleUnlessTheBuildPredatesIt(t *testing.T) {
	out := runBash(t, shellFunction(t, "deployment_tenancy")+`echo "tenancy=$(deployment_tenancy)"`)
	contains(t, out, "tenancy=single")
	out = runBash(t, shellFunction(t, "deployment_tenancy")+`echo "tenancy=$(deployment_tenancy)"`,
		"DAEMON_PREDATES_TENANCY=1")
	contains(t, out, "tenancy=multi")
}

// The report says who to sign in as and the tunnel, names no password
// (grain keeps none), and no longer mentions the Admin mode switch, which
// is gone.
func TestTheReadinessReportSaysHowToSignIn(t *testing.T) {
	script := shellFunction(t, "ui_addr_is_wildcard") + shellFunction(t, "ui_network_url") +
		signInFns(t) + "report_sign_in"
	env := []string{"GRAIN_UI_ADDR=127.0.0.1:80", "GRAIN_DATA_DIR=" + t.TempDir(), "GRAIN_USER=grain",
		"GRAIN_GROUP=grain", "GRAIN_ADMIN_USER=bsalmon", "ADMIN_USER_NAME=bsalmon", "ADMIN_USER_STATUS=ok",
		"GRAIN_HOSTD=1", "SUDO_USER=bsalmon",
		"CLI_ENV_FILE=/etc/grain/cli.env", "DAEMON_PREDATES_TENANCY=1"}
	out := runBash(t, script, env...)
	for _, want := range []string{"ssh -L 80:localhost:80", "as bsalmon, a system admin", "identity", "sudo -u grain grain list"} {
		contains(t, out, want)
	}
	for _, wrong := range []string{"Admin mode", "password is in", "grain user"} {
		absent(t, out, wrong)
	}
	contains(t, body(t, setupText(t), "report_readiness() {"), "report_sign_in")

	// One that could not be set up says how to do it by hand.
	out = runBash(t, script, append(env[:len(env):len(env)], "ADMIN_USER_STATUS=failed")...)
	contains(t, out, "bsalmon was not made a system admin")

	// On the default all-interfaces bind it gives the network URL too.
	out = runBash(t, script, "GRAIN_UI_ADDR=0.0.0.0:4242", "GRAIN_DATA_DIR=/var/lib/grain/data", "GRAIN_USER=grain",
		"GRAIN_GROUP=grain", "ADMIN_USER_NAME=bsalmon", "ADMIN_USER_STATUS=ok", "GRAIN_HOSTD=0")
	contains(t, out, ":4242/, or http://localhost:4242/ after ssh -L 4242:localhost:4242")
}

// Order: the admin once the daemon is running.
func TestTheAdminIsSetUpOnceTheDaemonIsRunning(t *testing.T) {
	main := from(t, setupCode(t), "main() {")
	order := []string{"enable_services", "ensure_admin_user", "print_summary"}
	last := -1
	for _, step := range order {
		i := strings.Index(main, "\n  "+step+"\n")
		if i < 0 {
			t.Fatalf("main() does not call %s", step)
		}
		if i < last {
			t.Errorf("main() calls %s out of order (want %s)", step, strings.Join(order, ", "))
		}
		last = i
	}
}

// A single-tenant daemon keeps no accounts, so setup.sh asks the CLI to
// make none, and says so rather than failing.
func TestSetupBootstrapsNoAdminUserForASingleTenantDaemon(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	stub := `grain_cli() { printf '%s\n' "$*" >> "$CALLS"; }
`
	script := logFn + stub + shellFunction(t, "ensure_admin_user") + `ensure_admin_user; echo "status=$ADMIN_USER_STATUS"`
	out := runBash(t, script, "CALLS="+calls, "GRAIN_UI_ADDR=127.0.0.1:80", "GRAIN_DATA_DIR=/var/lib/grain/data",
		"GRAIN_USER=grain", "GRAIN_ADMIN_USER=bsalmon")
	if !strings.Contains(out, "status=single") {
		t.Errorf("ensure_admin_user printed %q", out)
	}
	if got, err := os.ReadFile(calls); err == nil && len(got) > 0 {
		t.Errorf("a single-tenant setup called the CLI: %s", got)
	}
}
