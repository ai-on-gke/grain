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

// Progress reporting, credential notice placement, and redundant-wait cuts
// across scripts/setup.sh.
package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func progressFns(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(logFn)
	for _, fn := range []string{
		"progress_is_tty",
		"step_start",
		"step_done",
		"daemon_progress_status",
		"wait_with_progress",
		"warn_if_no_agent_credential",
	} {
		b.WriteString(shellFunction(t, fn))
	}
	return b.String()
}

func TestProgressReportingTTYNonTTYAndTimeout(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")

	stubs := `
journalctl() {
  echo 'time=2026-10-20T17:13:00Z level=INFO msg="starting store migrations" schema=42'
}
check_after_three() {
  local n=0
  [ -f "$COUNTER" ] && n="$(cat "$COUNTER")"
  n=$(( n + 1 ))
  printf '%s' "$n" > "$COUNTER"
  [ "$n" -ge 3 ]
}
never_ready() {
  return 1
}
`
	// 1. TTY mode: updates in-place with \r and [Ns] spinner, includes
	// journalctl msg= summary without raw log fields, and finishes with "... ready in".
	_ = os.Remove(counter)
	ttyOut := runBash(t, progressFns(t)+stubs+
		`wait_with_progress "Waiting for grain to finish starting (first start can take a minute)" 5 ready check_after_three`,
		"COUNTER="+counter, "PROGRESS_TTY=1", "PROGRESS_INTERVAL=0")
	for _, want := range []string{
		"==> Waiting for grain to finish starting (first start can take a minute)",
		"\r==>   [1s] / waiting... (starting store migrations)\x1b[K",
		"==>   ... ready in ",
	} {
		contains(t, ttyOut, want)
	}
	absent(t, ttyOut, "schema=42")

	// 2. Non-TTY mode: logs a line every 10 iterations and finishes with "... ready in".
	checkAfterTwelve := `
check_after_twelve() {
  local n=0
  [ -f "$COUNTER" ] && n="$(cat "$COUNTER")"
  n=$(( n + 1 ))
  printf '%s' "$n" > "$COUNTER"
  [ "$n" -ge 12 ]
}
`
	_ = os.Remove(counter)
	nonTTYOut := runBash(t, progressFns(t)+stubs+checkAfterTwelve+
		`wait_with_progress "Waiting for grain to finish starting (first start can take a minute)" 15 ready check_after_twelve`,
		"COUNTER="+counter, "PROGRESS_TTY=0", "PROGRESS_INTERVAL=0", "PROGRESS_REPORT_EVERY=10")
	for _, want := range []string{
		"==> Waiting for grain to finish starting (first start can take a minute)",
		"==>   [10s] still waiting... (starting store migrations)",
		"==>   ... ready in ",
	} {
		contains(t, nonTTYOut, want)
	}
	absent(t, nonTTYOut, "\r==>   [")

	// 3. Timeout: names what timed out, the last daemon status, and the exact journalctl command.
	timeoutOut := runBash(t, progressFns(t)+stubs+
		`wait_with_progress "Waiting for grain to finish starting" 3 ready never_ready || echo "rc=$?"`,
		"PROGRESS_TTY=0", "PROGRESS_INTERVAL=0", "PROGRESS_REPORT_EVERY=10")
	for _, want := range []string{
		"==>   timed out after 3s: Waiting for grain to finish starting (last status: starting store migrations)",
		"==>   inspect the daemon logs with: journalctl -u grain-daemon -n 50",
		"rc=1",
	} {
		contains(t, timeoutOut, want)
	}
}

func TestCredentialNoticeIsRepeatedAtTheEndOfPrintSummary(t *testing.T) {
	code := setupCode(t)
	enable := body(t, code, "enable_services() {")
	contains(t, enable, "warn_if_no_agent_credential")

	summary := body(t, code, "print_summary() {")
	before(t, summary, "report_readiness", "warn_if_no_agent_credential",
		"print_summary should repeat the missing-credential notice at the very end after report_readiness")

	dir := t.TempDir()
	out := runBash(t, progressFns(t)+`warn_if_no_agent_credential`,
		"GRAIN_DATA_DIR="+dir, "GRAIN_CODEX_AUTH_FILE=")
	contains(t, out, "grain-daemon.service is running, but no agent credential is configured")
	contains(t, out, "roles/serviceusage.apiKeysAdmin")
}

func TestCheapWaitCutsInExtractorAndReadiness(t *testing.T) {
	code := setupCode(t)
	extractor := body(t, code, "write_host_extractor() {")
	contains(t, extractor, `extracted_id_file="${EXTRACTED_ID_FILE:-$id_file}"`)
	contains(t, code, "HOSTD_EXTRACTED_ID_FILE=/usr/local/lib/grain/.extracted-image-id-hostd")

	readiness := body(t, code, "report_readiness() {")
	if n := strings.Count(readiness, `grain_cli secrets -data-dir "$GRAIN_DATA_DIR" list`); n != 1 {
		t.Errorf("report_readiness calls `grain_cli secrets list` %d times, want 1", n)
	}
	contains(t, body(t, code, "verify_agent_cli() {"), "CACHED_AGENT_CLI_IN_IMAGE_")
	contains(t, body(t, code, "report_agent_cli() {"), "CACHED_AGENT_CLI_IN_IMAGE_")
}
