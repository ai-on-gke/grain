package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gke-labs/granule/pkg/granulefile"
)

func TestNightlyPromptsAndSuite(t *testing.T) {
	root := filepath.Join("..", "..")
	suiteFile := filepath.Join(root, ".grain", "suites", "nightly.yaml")
	suiteData, err := os.ReadFile(suiteFile)
	if err != nil {
		t.Fatalf("failed to read nightly suite config: %v", err)
	}
	if !strings.Contains(string(suiteData), "name: nightly-test-suite") {
		t.Errorf("nightly.yaml does not contain expected suite name")
	}
	if !strings.Contains(string(suiteData), "nightly") {
		t.Errorf("nightly.yaml suite does not reference template 'nightly'")
	}

	templateFile := filepath.Join(root, ".grain", "templates", "nightly.yaml")
	templateData, err := os.ReadFile(templateFile)
	if err != nil {
		t.Fatalf("failed to read nightly template config: %v", err)
	}
	if !strings.Contains(string(templateData), "name: nightly") {
		t.Errorf("nightly.yaml template does not contain expected template name")
	}
	if !strings.Contains(string(templateData), "title:") || !strings.Contains(string(templateData), "body:") {
		t.Errorf("nightly.yaml template missing title or body")
	}

	scheduleFile := filepath.Join(root, ".grain", "schedules", "nightly.yaml")
	scheduleData, err := os.ReadFile(scheduleFile)
	if err != nil {
		t.Fatalf("failed to read nightly schedule config: %v", err)
	}
	if !strings.Contains(string(scheduleData), "name: Granule Nightly Suite") {
		t.Errorf("nightly.yaml schedule does not contain expected schedule name")
	}
	if !strings.Contains(string(scheduleData), "suite: nightly") {
		t.Errorf("nightly.yaml schedule does not reference suite 'nightly'")
	}

	expectedPrompts := []string{
		"01-test-suite-and-quality-gates.md",
		"02-granulefile-conformance-and-compiler.md",
		"03-guest-image-lifecycle-and-rebase.md",
		"04-vm-runtime-networking-and-control-plane.md",
		"05-k8s-and-docker-orchestration.md",
		"06-storage-and-performance-benchmarks.md",
		"07-docs-and-api-spec-consistency.md",
		"08-nightly-regression-triage-and-summary.md",
		"09-daily-new-features-test-plan.md",
	}

	promptsDir := filepath.Join(root, ".grain", "prompts")
	for _, prompt := range expectedPrompts {
		promptPath := filepath.Join(promptsDir, prompt)
		content, err := os.ReadFile(promptPath)
		if err != nil {
			t.Errorf("expected prompt file %s missing: %v", prompt, err)
			continue
		}
		strContent := string(content)
		if !strings.Contains(strContent, "## Objective") {
			t.Errorf("prompt file %s missing '## Objective' section", prompt)
		}
		if !strings.Contains(strContent, "## Execution") {
			t.Errorf("prompt file %s missing '## Execution' section", prompt)
		}
		if !strings.Contains(strContent, "## Evaluation & Reporting Criteria") {
			t.Errorf("prompt file %s missing '## Evaluation & Reporting Criteria' section", prompt)
		}
	}

	// The nightly prompts report only what the user needs to address: a green
	// night stays silent, clear bugs become auto-merge follow-up tasks, and
	// only issues needing an architectural change or user intervention reach
	// the user.
	policy := map[string][]string{
		"01-test-suite-and-quality-gates.md": {
			"Report only what the user needs to address",
			"do not report",
			"`propose_task` with `auto_merge: true`",
			"architectural change or user intervention",
		},
		"08-nightly-regression-triage-and-summary.md": {
			"Report only what the user needs to address",
			"**Everything is fine**",
			"File nothing and do not report",
			"**Clear bugs**",
			"`propose_task` with `auto_merge: true`",
			"**Otherwise**",
			"architectural change or user intervention",
			"minimal reproducer",
			"architectural impact",
			"decision points",
		},
		"09-daily-new-features-test-plan.md": {
			"Report only what the user needs to address",
			"do not report",
			"`propose_task` with `auto_merge: true`",
			"architectural change or user intervention",
			"decision points",
		},
	}
	for prompt, phrases := range policy {
		content, err := os.ReadFile(filepath.Join(promptsDir, prompt))
		if err != nil {
			t.Errorf("expected prompt file %s missing: %v", prompt, err)
			continue
		}
		for _, phrase := range phrases {
			if !strings.Contains(string(content), phrase) {
				t.Errorf("prompt file %s does not state the reporting policy: missing %q", prompt, phrase)
			}
		}
	}
	if strings.Contains(string(templateData), "post a complete summary report") ||
		!strings.Contains(string(templateData), "Report only what the user needs to address") {
		t.Errorf("nightly.yaml template does not state the reporting policy")
	}

	t.Run("granulefile examples", func(t *testing.T) {
		checkPromptGranulefiles(t, filepath.Join(promptsDir, "02-granulefile-conformance-and-compiler.md"))
	})
	t.Run("asset paths", func(t *testing.T) {
		checkTestFPaths(t, root,
			filepath.Join(promptsDir, "03-guest-image-lifecycle-and-rebase.md"),
			filepath.Join(root, "docs", "test-plan.md"))
	})
	t.Run("published image references", func(t *testing.T) {
		checkPromptImageRefs(t, root, filepath.Join(promptsDir, "03-guest-image-lifecycle-and-rebase.md"))
	})
}

var (
	promptHeredoc = regexp.MustCompile(`(?ms)^cat << 'EOF' > (\S+)\n(.*?)^EOF$`)
	promptCheck   = regexp.MustCompile(`(?m)^/tmp/granule granulefile check -f (\S+)\s*$`)
	promptBuild   = regexp.MustCompile(`(?m)^/tmp/granule granulefile build -f (\S+) -print\s*$`)
	promptRefused = regexp.MustCompile(`(?m)^if /tmp/granule granulefile check -f (\S+)`)
	markdownTestF = regexp.MustCompile(`(?m)^\s*test -f (\S+)\s*$`)

	// A full image reference, registry to tag. A placeholder such as
	// ghcr.io/gke-labs/granule/<variant>:latest is not one, and is left
	// alone.
	imageRef = regexp.MustCompile(`ghcr\.io/[A-Za-z0-9._/-]+:[A-Za-z0-9._-]+`)
	// A tag on its own in backticks, naming no repository: the shape the
	// pre-per-variant layout was described in (`:alpine`, `:debian12`).
	bareTag = regexp.MustCompile("`:[A-Za-z0-9._-]+`")
	// What publish-images.sh pushes: "+ docker push <ref>" for the guests
	// and their rootfs, OUTPUT_IMAGE=<ref> ... PUSH=true for the runner
	// images, which deploy/github-runner-images/build.sh pushes itself.
	publishedPush   = regexp.MustCompile(`(?m)^\+ docker push (\S+)`)
	publishedRunner = regexp.MustCompile(`(?m)^\+ OUTPUT_IMAGE=(\S+) PUSH=true `)
)

// checkPromptGranulefiles runs every Granulefile a nightly prompt writes
// with a heredoc through the compiler the way the prompt's own commands
// do: a bare "granulefile check -f" must pass the check, a
// "granulefile build -f ... -print" must also plan a build (which is
// stricter: it refuses what granule cannot build today), and an
// "if granulefile check -f" is a negative test that must be refused. The
// nightly run executes these commands literally, so an example that has
// drifted from the compiler fails here rather than as a false nightly
// regression.
func checkPromptGranulefiles(t *testing.T, promptPath string) {
	t.Helper()
	data, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("read %s: %v", promptPath, err)
	}
	content := string(data)
	name := filepath.Base(promptPath)

	files := map[string]string{}
	for _, m := range promptHeredoc.FindAllStringSubmatch(content, -1) {
		files[m[1]] = m[2]
	}
	if len(files) == 0 {
		t.Fatalf("%s writes no Granulefile heredocs; the pattern this test reads them with has drifted", name)
	}
	source := func(path string) (string, bool) {
		src, ok := files[path]
		if !ok {
			t.Errorf("%s runs the compiler on %s, which no heredoc in the prompt writes", name, path)
		}
		return src, ok
	}

	runs := 0
	for _, m := range promptCheck.FindAllStringSubmatch(content, -1) {
		src, ok := source(m[1])
		if !ok {
			continue
		}
		runs++
		if _, err := granulefile.Check(m[1], src, granulefile.Options{}); err != nil {
			t.Errorf("%s: \"granulefile check -f %s\" fails:\n%v", name, m[1], err)
		}
	}
	for _, m := range promptBuild.FindAllStringSubmatch(content, -1) {
		src, ok := source(m[1])
		if !ok {
			continue
		}
		runs++
		// No DockerBinary: the plan is made without inspecting base
		// images, so this needs neither docker nor a network.
		err := granulefile.Build(context.Background(), m[1], src, granulefile.BuildOptions{PrintDockerfile: true})
		if err != nil {
			t.Errorf("%s: \"granulefile build -f %s -print\" fails:\n%v", name, m[1], err)
		}
	}
	for _, m := range promptRefused.FindAllStringSubmatch(content, -1) {
		src, ok := source(m[1])
		if !ok {
			continue
		}
		runs++
		if _, err := granulefile.Check(m[1], src, granulefile.Options{}); err == nil {
			t.Errorf("%s: negative test %s passes \"granulefile check\"; it is meant to be refused", name, m[1])
		}
	}
	if runs == 0 {
		t.Errorf("%s runs the compiler on none of its heredocs; the pattern this test reads commands with has drifted", name)
	}
}

// checkTestFPaths requires every "test -f <path>" a prompt or test plan
// tells a run to execute to name a file that is in the tree.
func checkTestFPaths(t *testing.T, root string, docs ...string) {
	t.Helper()
	for _, doc := range docs {
		data, err := os.ReadFile(doc)
		if err != nil {
			t.Errorf("read %s: %v", doc, err)
			continue
		}
		for _, m := range markdownTestF.FindAllStringSubmatch(string(data), -1) {
			if _, err := os.Stat(filepath.Join(root, m[1])); err != nil {
				t.Errorf("%s runs \"test -f %s\", which fails: %v", filepath.Base(doc), m[1], err)
			}
		}
	}
}

// checkPromptImageRefs requires every image reference a prompt tells a
// run to look for in "deploy/publish-images.sh --dry-run --no-login all"
// to be one that dry run actually pushes. The nightly run checks the
// output for exactly these references, so a prompt left behind by a
// change to the tag layout fails here rather than as a false nightly
// regression.
func checkPromptImageRefs(t *testing.T, root, promptPath string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed, so there is nothing here to run the publish script with")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed, and the publish script names its tags after this commit")
	}
	data, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("read %s: %v", promptPath, err)
	}
	content := string(data)
	name := filepath.Base(promptPath)

	for _, m := range bareTag.FindAllString(content, -1) {
		t.Errorf("%s names the tag %s without the repository it belongs to; name the full image reference publish-images.sh pushes", name, m)
	}
	refs := imageRef.FindAllString(content, -1)
	if len(refs) == 0 {
		t.Fatalf("%s names no image references; the pattern this test reads them with has drifted", name)
	}

	// A stub docker first on PATH: the script requires one to exist, and
	// a dry run must not need the real one -- nor reach it, if it ever
	// stopped being dry. GUEST_ARCH spares it asking the daemon.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("writing the docker stub: %v", err)
	}
	cmd := exec.Command("bash", filepath.Join("deploy", "publish-images.sh"),
		"--dry-run", "--no-login", "--no-grain-sandbox", "all")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GUEST_ARCH=amd64", "REGISTRY=ghcr.io", "REPO=gke-labs/granule")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("publish-images.sh --dry-run --no-login all: %v\n%s", err, stderr.String())
	}

	pushed := map[string]bool{}
	for _, re := range []*regexp.Regexp{publishedPush, publishedRunner} {
		for _, m := range re.FindAllStringSubmatch(string(out), -1) {
			pushed[m[1]] = true
		}
	}
	if len(pushed) == 0 {
		t.Fatalf("publish-images.sh --dry-run pushes nothing this test recognizes; the patterns it reads pushes with have drifted:\n%s", out)
	}
	for _, ref := range refs {
		if !pushed[ref] {
			t.Errorf("%s expects publish-images.sh to publish %s, which its dry run does not push", name, ref)
		}
	}
}
