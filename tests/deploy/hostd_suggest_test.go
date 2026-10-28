package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A re-run never writes the operator's allowlist, but says which default
// cli entry it lacks and where this run's default is to copy it from.
func TestTheReadinessReportSuggestsAMissingDefaultAllowlistEntry(t *testing.T) {
	dir := t.TempDir()
	allow := filepath.Join(dir, "hostd.yaml")
	lib := shellFunction(t, "hostd_allowlist_content") + shellFunction(t, "hostd_allowlist_clis") +
		shellFunction(t, "report_hostd_suggestions") + "report_hostd_suggestions\n"
	env := []string{
		"HOSTD_ALLOWLIST=" + allow, "HOSTD_ALLOWLIST_DEFAULT=" + allow + ".default",
		"HOSTD_AGY_PATH=/opt/agy", "GRAIN_DATA_DIR=/var/lib/grain/data", "GRAIN_SANDBOX_DIR=/var/lib/grain/sandbox",
	}
	run := func(content string) string {
		t.Helper()
		if err := os.WriteFile(allow, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return runBash(t, "HOSTD_PROXY_VARS=(HTTPS_PROXY)\n"+lib, env...)
	}

	got := run("clis:\n  claude:\n    path: /usr/bin/claude\ncapabilities: {}\n")
	if !strings.Contains(got, "suggested: add the default clis entry for agy") || !strings.Contains(got, allow+".default") {
		t.Errorf("an allowlist without agy is not told to add it, from the default:\n%s", got)
	}
	if b, _ := os.ReadFile(allow); strings.Contains(string(b), "agy") {
		t.Errorf("the suggestion wrote into the operator's allowlist:\n%s", b)
	}
	if got := run("clis:\n  agy:\n    path: /opt/agy\n"); strings.TrimSpace(got) != "" {
		t.Errorf("an allowlist that has agy is still told something: %q", got)
	}
}
