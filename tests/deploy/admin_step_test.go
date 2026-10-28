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

package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// adminFns is ensure_admin_user and the functions it calls.
func adminFns(t *testing.T) string {
	var b strings.Builder
	b.WriteString(logFn)
	for _, fn := range []string{"progress_is_tty", "daemon_progress_status", "wait_with_progress", "admin_daemon_ready", "ensure_admin_user", "admin_accounts", "add_system_admin"} {
		b.WriteString(shellFunction(t, fn))
	}
	return b.String()
}

// settingsDaemon is a daemon behind the grain CLI, its system admins in
// $STATE/admins: `grain list` answers when $DOWN is unset, `-json
// settings` prints them, and `settings -add-admin` appends one. Every call
// is recorded in $STATE/calls.
const settingsDaemon = `
grain_cli() {
  printf 'grain %s\n' "$*" >> "$STATE/calls"
  [ -z "${DOWN:-}" ] || return 1
  [ "$1" = -server ] && shift 2
  case "$*" in
    list) ;;
    "-json settings") printf '{\n  "pollInterval": "10s",\n  "adminAccounts": %s,\n  "x": 1\n}\n' "$(cat "$STATE/admins")" ;;
    "settings -add-admin "*)
      local admins
      admins="$(cat "$STATE/admins")"
      printf '%s' "${admins%]}" > "$STATE/admins"
      [ "$admins" = "[]" ] || printf ',' >> "$STATE/admins"
      printf '"%s"]' "$3" >> "$STATE/admins" ;;
    *) return 1 ;;
  esac
}
`

func adminRun(t *testing.T, state string, extra ...string) string {
	t.Helper()
	script := adminFns(t) + settingsDaemon + `ensure_admin_user; echo "status=$ADMIN_USER_STATUS user=$ADMIN_USER_NAME"`
	env := append([]string{"STATE=" + state, "GRAIN_UI_ADDR=127.0.0.1:4242", "GRAIN_DATA_DIR=" + state,
		"GRAIN_USER=grain", "GRAIN_ADMIN_USER=debian", "DAEMON_PREDATES_TENANCY=1", "PROGRESS_INTERVAL=0"}, extra...)
	return runBash(t, script, env...)
}

func newAdminState(t *testing.T, admins string) string {
	t.Helper()
	state := t.TempDir()
	for name, content := range map[string]string{"admins": admins} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

// ensure_admin_user adds GRAIN_ADMIN_USER to the system admins through
// `grain settings -add-admin`, and takes nobody out. grain
// keeps no passwords (docs/single-tenant-reset.md, stage 3d), so there is
// no account to create or password file to write.
func TestSetupMakesTheAdminUserASystemAdmin(t *testing.T) {
	state := newAdminState(t, `["grain"]`)
	out := adminRun(t, state)
	contains(t, out, "status=ok user=debian")
	contains(t, out, "debian is now a system admin")
	calls, _ := os.ReadFile(filepath.Join(state, "calls"))
	contains(t, string(calls), "grain -server http://127.0.0.1:4242 settings -add-admin debian")
	absent(t, string(calls), "user ")
	if got, _ := os.ReadFile(filepath.Join(state, "admins")); string(got) != `["grain","debian"]` {
		t.Errorf("admins = %s, want grain kept and debian added", got)
	}
	if _, err := os.Stat(filepath.Join(state, "admin-password")); err == nil {
		t.Error("ensure_admin_user wrote a password file")
	}

	// A re-run adds nobody twice.
	os.Remove(filepath.Join(state, "calls"))
	out = adminRun(t, state)
	contains(t, out, "status=ok")
	contains(t, out, "debian was already a system admin")
	calls, _ = os.ReadFile(filepath.Join(state, "calls"))
	absent(t, string(calls), "-add-admin")
	if got, _ := os.ReadFile(filepath.Join(state, "admins")); string(got) != `["grain","debian"]` {
		t.Errorf("admins after a re-run = %s", got)
	}

	// With no GRAIN_ADMIN_USER it is the daemon's own account, already
	// an admin.
	out = adminRun(t, state, "GRAIN_ADMIN_USER=")
	contains(t, out, "status=ok user=grain")
	contains(t, out, "grain was already a system admin")

	// A daemon whose settings cannot be read is reported, not fatal.
	out = adminRun(t, state, "GRAIN_ADMIN_USER=alice", "DOWN=1")
	contains(t, out, "status=failed user=alice")
}

// A build from before the single-tenant engine (v1.9.0) is
// multi-tenant, which is said and set up as one; any later build is
// passed no -tenancy, which this build does not define.
func TestTheUnitSetsUpABuildFromBeforeTheSingleTenantEngineAsMulti(t *testing.T) {
	block := between(t, setupText(t), "  # Tenancy. This build is single-tenant only", "\n  if [ -n \"$GRAIN_CODEX_AUTH_FILE\" ]")
	run := func(spec string) string {
		stub := "daemon_flag_spec() { printf '%b' \"$SPEC\"; }\n"
		script := logFn + stub + shellFunction(t, "daemon_spec_defines") + "daemon_args=()\n" + block +
			"\necho \"args=[${daemon_args[*]}] predates=$DAEMON_PREDATES_TENANCY.\""
		return runBash(t, script, "SPEC="+spec, "GRAIN_IMAGE_REF=grain:old")
	}
	out := run(`data-dir value\n`)
	contains(t, out, "args=[] predates=1.")
	contains(t, out, "WARNING: grain:old predates the single-tenant engine")

	out = run(`data-dir value\nui-access-session-lifetime value\n`)
	contains(t, out, "args=[] predates=.")
	absent(t, out, "WARNING")

	// A build that cannot list its flags is not taken for an old one.
	contains(t, run(""), "args=[] predates=.")
}
