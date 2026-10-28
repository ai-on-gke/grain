package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
}
