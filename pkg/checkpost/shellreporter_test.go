package checkpost

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The signing scheme is written twice -- once in sign.go and once in
// scripts/checkpost-report.sh, because the workflow job that reports a
// verdict is the last step of a build whose other jobs have finished,
// and installing a Go toolchain to send one 200-byte request would cost
// more than everything it is reporting on.
//
// Duplication like that is only safe if something fails when the two
// disagree. This is that something: it runs the real script against a
// real server with the real verifier behind it, so a change to either
// side's framing -- the "<timestamp>.<body>" separator, the hex
// encoding, the header names, the path -- fails here rather than in
// somebody's workflow three weeks later.
func TestTheShellReporterSignsTheSameWayGoDoes(t *testing.T) {
	requireCommands(t, "bash", "openssl", "curl", "jq")

	st, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := NewServer(st, testSecret)
	// No transition window: the script must sign the current scheme,
	// method and path included, not pass on the old-scheme fallback.
	srv.legacyReports = false
	ts := httptest.NewServer(srv)
	defer ts.Close()

	commit := sha('1')
	run(t, ts.URL, map[string]string{
		"REPO":       "acme/widgets",
		"COMMIT":     commit,
		"NAME":       "go",
		"CONCLUSION": "success",
		"CHECK_URL":  "https://github.com/acme/widgets/actions/runs/1",
		"ATTEMPT":    "3",
	})

	got := st.For("acme/widgets", commit)
	if len(got) != 1 {
		t.Fatalf("the script delivered %d verdicts, want 1 -- the two halves of the signing "+
			"scheme have drifted, or the request shape has", len(got))
	}
	if got[0].Name != "go" || got[0].Conclusion != "success" {
		t.Errorf("delivered %+v", got[0])
	}
	if got[0].URL != "https://github.com/acme/widgets/actions/runs/1" {
		t.Errorf("url = %q", got[0].URL)
	}
	// As a number: a re-run's answer only replaces a failure when this
	// arrives (Store.Put), so a string here would be a 400 on every run.
	if got[0].Attempt != 3 {
		t.Errorf("attempt = %d, want 3", got[0].Attempt)
	}
}

// A check name is a workflow's `name:`, which may perfectly well contain
// a quote or a backslash -- which is why the script builds its body with
// jq rather than printf. A name that broke the JSON would arrive as a
// 400 nobody reads, and the queue would wait on silence.
func TestTheShellReporterSurvivesAnAwkwardCheckName(t *testing.T) {
	requireCommands(t, "bash", "openssl", "curl", "jq")

	st, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := NewServer(st, testSecret)
	// No transition window: the script must sign the current scheme,
	// method and path included, not pass on the old-scheme fallback.
	srv.legacyReports = false
	ts := httptest.NewServer(srv)
	defer ts.Close()

	awkward := `sandbox "on" a \real\ VM`
	commit := sha('2')
	run(t, ts.URL, map[string]string{
		"REPO": "acme/widgets", "COMMIT": commit, "NAME": awkward, "CONCLUSION": "failure",
	})

	got := st.For("acme/widgets", commit)
	if len(got) != 1 || got[0].Name != awkward {
		t.Fatalf("got %+v, want the name through intact", got)
	}
}

// A verdict the service refuses has to fail the job that reported it.
// The alternative is a workflow that silently stopped reporting and a
// merge queue that quietly went back to waiting on nothing.
func TestTheShellReporterFailsWhenTheServiceRefuses(t *testing.T) {
	requireCommands(t, "bash", "openssl", "curl", "jq")

	st, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ts := httptest.NewServer(NewServer(st, "a different secret"))
	defer ts.Close()

	cmd := reporter(t, ts.URL, map[string]string{
		"REPO": "acme/widgets", "COMMIT": sha('1'), "NAME": "go", "CONCLUSION": "success",
	})
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the script succeeded against a service that refused it: %s", out)
	}
}

func TestTheShellReporterRefusesToRunWithSomethingMissing(t *testing.T) {
	requireCommands(t, "bash")
	cmd := reporter(t, "http://127.0.0.1:1", map[string]string{
		"REPO": "acme/widgets", "COMMIT": sha('1'), "NAME": "go",
		// CONCLUSION deliberately absent.
	})
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the script ran with no CONCLUSION: %s", out)
	}
	if !strings.Contains(string(out), "CONCLUSION is not set") {
		t.Errorf("the script did not name the missing variable: %s", out)
	}
}

// reporter builds the command, with the environment the script reads and
// nothing inherited: the point is that everything it needs arrives
// through those names.
func reporter(t *testing.T, url string, env map[string]string) *exec.Cmd {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "checkpost-report.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("the reporter script is not where this test expects it: %v", err)
	}
	cmd := exec.Command("bash", script)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"CHECKPOST_URL=" + url,
		"CHECKPOST_SECRET=" + testSecret,
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd
}

func run(t *testing.T, url string, env map[string]string) {
	t.Helper()
	cmd := reporter(t, url, env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the reporter script failed: %v\n%s", err, out)
	}
}

// requireCommands skips rather than fails when the shell tools are not
// there. Every one of them is on a GitHub runner and in grain's own
// sandbox image, so this skipping is for somebody's laptop -- and the
// job that matters (`shellcheck`, and this test in the `go` job) runs
// where they exist.
func requireCommands(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s is not on this machine, so the shell reporter cannot be exercised here", n)
		}
	}
}
