package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// makeWord is `\b make \b`: install_prerequisites naming the toolchain a
// deployed host no longer has. Spelled out here because Go's regexp is
// the only place in this file that needs one.
var makeWord = regexp.MustCompile(`\bmake\b`)

// repoRoot is the checkout this test file was compiled from, found by
// walking up from its own path rather than from the working directory:
// `go test` runs each package in its own directory, and these assertions
// are all about files two levels up.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file, so cannot locate the repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("%s does not look like the repository root: %v", root, err)
	}
	return root
}

// read returns one file of the checkout, named by the path segments a
// reader would say out loud ("deploy", "terraform", "gcp", "files", "deploy.sh").
func read(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{repoRoot(t)}, parts...)...)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", filepath.Join(parts...), err)
	}
	return string(b)
}

func setupText(t *testing.T) string { return read(t, "scripts", "setup.sh") }

func workflow(t *testing.T) string {
	return read(t, ".grain", "workflows", "release.yml")
}

// testsWorkflow is the other one: the credential-free suite that runs on
// every pull request, and so the definition of what a sandbox has to be
// able to reproduce.
func testsWorkflow(t *testing.T) string {
	return read(t, ".grain", "workflows", "tests.yml")
}

// liveAgentWorkflow is the third one, and the only one that holds a model
// credential: the nightly live agy run (tests/e2e/live_test.go).
func liveAgentWorkflow(t *testing.T) string {
	return read(t, ".grain", "workflows", "live-agent.yml")
}

// liveFrameworksWorkflow is live-agent.yml's counterpart for the other
// two frameworks a deployment can dispatch: the nightly claude and codex
// runs (pkg/agent/claude/claude_live_test.go,
// pkg/agent/codex/codex_live_test.go), one job and one environment per
// vendor credential.
func liveFrameworksWorkflow(t *testing.T) string {
	return read(t, ".grain", "workflows", "live-frameworks.yml")
}

// gcpSmokeWorkflow is the fourth, and the only one that holds a GCP
// credential: the nightly GCE and GKE lifecycle runs
// (scripts/gce-vm-smoke.sh, scripts/gke-cluster-smoke.sh).
func gcpSmokeWorkflow(t *testing.T) string {
	return read(t, ".grain", "workflows", "gcp-smoke.yml")
}

// liveSecretsRepoWorkflow is the fifth, and the only one that holds a
// credential able to *write* to GitHub: the nightly push, restore and
// two-deployment run against a real private repository
// (pkg/daemon/live_secrets_repo_test.go).
func liveSecretsRepoWorkflow(t *testing.T) string {
	return read(t, ".grain", "workflows", "live-secrets-repo.yml")
}

// nightlyWorkflow is the long suite: the checks that are too slow, too
// repetitive or too noisy to gate a pull request, run on a schedule
// instead (docs/nightly-test-review.md).
func nightlyWorkflow(t *testing.T) string {
	return read(t, ".grain", "workflows", "nightly.yml")
}

// agySurfaceWorkflow is the sixth, and the only one that holds no
// credential and no schedule: the dispatch-only job that installs agy and
// writes down what it says about itself (scripts/agy-surface.sh,
// docs/agy-surface.md).
func agySurfaceWorkflow(t *testing.T) string {
	return read(t, ".grain", "workflows", "agy-surface.yml")
}

// pruneTemplate is the declared template that runs package pruning (cmd/ghcr-prune).
func pruneTemplate(t *testing.T) string {
	return read(t, ".grain", "templates", "prune-packages.yml")
}

// executable asserts a file in the checkout is one CI can run by path.
// A script invoked as `./scripts/foo.sh` by a workflow nothing triggers
// on a push is a script whose lost +x bit is found by the schedule, at
// night, and by nobody else.
func executable(t *testing.T, parts ...string) {
	t.Helper()
	path := filepath.Join(append([]string{repoRoot(t)}, parts...)...)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", filepath.Join(parts...), err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("%s is not executable (%s), and CI runs it by path", filepath.Join(parts...), info.Mode())
	}
}

// setupCode is setup.sh with its comment lines dropped.
//
// That file is more comment than code, and much of that comment is about
// what the deploy *used* to do -- so "the script no longer runs X" has to
// be asked of the code alone, or every explanation of a removal reads as
// the removal not having happened.
func setupCode(t *testing.T) string { return stripComments(setupText(t)) }

// stripComments drops every whole-line comment, for the same reason
// setupCode does: an assertion that a spelling is gone must not be
// satisfied, or defeated, by prose describing its removal.
func stripComments(text string) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// workflowJobKey matches the two-space key that opens a job in a
// workflow's `jobs:` block, which is the only indentation level in these
// files where a bare `name:` line means "a new job starts here".
var workflowJobKey = regexp.MustCompile(`(?m)^  [A-Za-z0-9_.-]+:[ \t]*$`)

// namedJob returns one job out of a workflow file: everything from its
// own key down to the next job's, or to the end of the file.
//
// Written this way rather than as `between(text, "go-test:", "\n
// ui-e2e:")`, which is what the assertions below used to do. That form
// silently changes meaning when a job is inserted between the two it
// names -- the span shrinks to the new job, every `contains` in it still
// passes against whatever that job happens to say, and nothing reports
// that the assertions are now about a different job. Splitting the
// frontend suite out of go-test is exactly that edit, so the helper has
// to name one job and find its real end.
func namedJob(t *testing.T, text, job string) string {
	t.Helper()
	key := "\n  " + job + ":\n"
	start := strings.Index(text, key)
	if start < 0 {
		t.Fatalf("the workflow has no %s job", job)
	}
	start++
	rest := text[start+len(key)-1:]
	if at := workflowJobKey.FindStringIndex(rest); at != nil {
		return text[start : start+len(key)-1+at[0]]
	}
	return text[start:]
}

// from returns everything at and after the first occurrence of marker.
func from(t *testing.T, text, marker string) string {
	t.Helper()
	at := strings.Index(text, marker)
	if at < 0 {
		t.Fatalf("%q does not appear at all", marker)
	}
	return text[at:]
}

// upTo returns everything before the first occurrence of marker.
func upTo(t *testing.T, text, marker string) string {
	t.Helper()
	at := strings.Index(text, marker)
	if at < 0 {
		t.Fatalf("%q does not appear at all", marker)
	}
	return text[:at]
}

// between returns the span that starts at marker and ends at the first
// end after it.
func between(t *testing.T, text, marker, end string) string {
	t.Helper()
	span := from(t, text, marker)
	at := strings.Index(span, end)
	if at < 0 {
		t.Fatalf("%q opens but never reaches %q", marker, end)
	}
	return span[:at]
}

// body is one shell function, from its opening line to the `}` in column
// zero that closes it.
func body(t *testing.T, text, marker string) string {
	t.Helper()
	return between(t, text, marker, "\n}\n")
}

func contains(t *testing.T, text, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Errorf("expected to find %q", want)
	}
}

func absent(t *testing.T, text, unwanted string) {
	t.Helper()
	if strings.Contains(text, unwanted) {
		t.Errorf("expected %q to be gone", unwanted)
	}
}

// before asserts that first appears ahead of second -- the shape of every
// ordering claim here, where what matters is which of two steps a script
// or a workflow reaches first.
func before(t *testing.T, text, first, second, msg string) {
	t.Helper()
	a := strings.Index(text, first)
	if a < 0 {
		t.Errorf("%q does not appear at all", first)
		return
	}
	b := strings.Index(text, second)
	if b < 0 {
		t.Errorf("%q does not appear at all", second)
		return
	}
	if a >= b {
		t.Errorf("%s (%q comes after %q)", msg, first, second)
	}
}

// lastLine is the final non-empty line of text, which for a span cut at
// the start of some other line is the line immediately above it.
func lastLine(text string) string {
	trimmed := strings.TrimRight(text, "\n")
	if at := strings.LastIndex(trimmed, "\n"); at >= 0 {
		return trimmed[at+1:]
	}
	return trimmed
}

// dockerRunArgs is the docker_run_args shell function, which is where
// every flag the daemon container is started with is assembled.
func dockerRunArgs(t *testing.T, text string) string {
	t.Helper()
	return body(t, text, "docker_run_args() {")
}

// machineNames is what setup.sh defaults GRAIN_UI_ALLOWED_HOSTS to:
// `hostname -f` and `hostname -s`, comma-separated, the second left out
// when it is the first.
func machineNames(t *testing.T) string {
	t.Helper()
	name := func(args ...string) string {
		out, _ := exec.Command("hostname", args...).Output()
		return strings.TrimSpace(string(out))
	}
	f, s := name("-f"), name("-s")
	if f == "" {
		f = name()
	}
	if s != "" && s != f {
		if f == "" {
			return s
		}
		return f + "," + s
	}
	return f
}
