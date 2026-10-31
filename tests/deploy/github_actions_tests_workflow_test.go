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
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"gopkg.in/yaml.v3"
)

type githubActionsWorkflow struct {
	Name string `yaml:"name"`
	On   struct {
		Push struct {
			Branches []string `yaml:"branches"`
		} `yaml:"push"`
		PullRequest      any `yaml:"pull_request"`
		MergeGroup       any `yaml:"merge_group"`
		WorkflowDispatch struct {
			Inputs map[string]struct {
				Description string `yaml:"description"`
				Required    bool   `yaml:"required"`
				Default     string `yaml:"default"`
				Type        string `yaml:"type"`
			} `yaml:"inputs"`
		} `yaml:"workflow_dispatch"`
	} `yaml:"on"`
	Jobs map[string]struct {
		Name    string            `yaml:"name"`
		Needs   []string          `yaml:"needs"`
		If      string            `yaml:"if"`
		Outputs map[string]string `yaml:"outputs"`
		Steps   []struct {
			Name string            `yaml:"name"`
			ID   string            `yaml:"id"`
			Uses string            `yaml:"uses"`
			Env  map[string]string `yaml:"env"`
			Run  string            `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func runGitHubCIChanges(t *testing.T, dir string, env map[string]string) (map[string]string, string, error) {
	t.Helper()
	outFile := filepath.Join(t.TempDir(), "github_output")
	script := filepath.Join(repoRoot(t), "scripts", "github-ci-changes.sh")

	cmd := exec.Command("bash", script)
	if dir != "" {
		cmd.Dir = dir
	} else {
		cmd.Dir = repoRoot(t)
	}

	var cleanEnv []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GITHUB_") ||
			strings.HasPrefix(kv, "EVENT_NAME=") ||
			strings.HasPrefix(kv, "BASE_SHA=") ||
			strings.HasPrefix(kv, "HEAD_SHA=") ||
			strings.HasPrefix(kv, "BASE_REF=") ||
			strings.HasPrefix(kv, "INPUT_JOBS=") ||
			strings.HasPrefix(kv, "CHANGED_FILES=") {
			continue
		}
		cleanEnv = append(cleanEnv, kv)
	}
	cleanEnv = append(cleanEnv, "GITHUB_OUTPUT="+outFile)
	for k, v := range env {
		cleanEnv = append(cleanEnv, k+"="+v)
	}
	cmd.Env = cleanEnv

	rawOut, err := cmd.CombinedOutput()
	if err != nil {
		return nil, string(rawOut), err
	}

	data, readErr := os.ReadFile(outFile)
	if readErr != nil {
		t.Fatalf("reading GITHUB_OUTPUT: %v", readErr)
	}

	outputs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("malformed GITHUB_OUTPUT line %q", line)
		}
		outputs[k] = v
	}
	return outputs, string(rawOut), nil
}

func TestGitHubActionsTestsWorkflowStructure(t *testing.T) {
	raw := read(t, ".github", "workflows", "tests.yml")
	var wf githubActionsWorkflow
	if err := yaml.Unmarshal([]byte(raw), &wf); err != nil {
		t.Fatalf(".github/workflows/tests.yml does not parse as YAML: %v", err)
	}

	jobsInput, ok := wf.On.WorkflowDispatch.Inputs["jobs"]
	if !ok {
		t.Fatal(".github/workflows/tests.yml is missing on.workflow_dispatch.inputs.jobs")
	}
	if jobsInput.Required {
		t.Error("on.workflow_dispatch.inputs.jobs must have required: false")
	}
	if jobsInput.Default != "" {
		t.Errorf("on.workflow_dispatch.inputs.jobs default = %q, want empty string", jobsInput.Default)
	}
	if jobsInput.Type != "string" {
		t.Errorf("on.workflow_dispatch.inputs.jobs type = %q, want \"string\"", jobsInput.Type)
	}

	changesJob, ok := wf.Jobs["changes"]
	if !ok {
		t.Fatal(".github/workflows/tests.yml is missing the `changes` pre-job")
	}

	wantOutputs := []string{
		"go_program", "go_backend", "go_unit", "granule", "frontend", "lint", "terraform",
		"go", "go-e2e", "go-orchestrator", "go-model", "go-unit", "postgres",
		"run-host", "host-script-container", "ui-e2e", "installer-e2e", "installer",
		"package-cache", "real-vm", "real-kubernetes",
	}
	for _, outKey := range wantOutputs {
		if _, exists := changesJob.Outputs[outKey]; !exists {
			t.Errorf("jobs.changes.outputs is missing %q", outKey)
		}
	}

	foundFilterStep := false
	for _, step := range changesJob.Steps {
		if strings.Contains(step.Run, "${{") {
			t.Errorf("jobs.changes step %q expands ${{ ... }} inside `run:`; pass inputs/context via `env:` instead", step.Name)
		}
		if step.ID == "filter" {
			foundFilterStep = true
			if !strings.Contains(step.Run, "./scripts/github-ci-changes.sh") {
				t.Errorf("jobs.changes filter step run = %q, want ./scripts/github-ci-changes.sh", step.Run)
			}
			for _, envKey := range []string{"EVENT_NAME", "BASE_SHA", "HEAD_SHA", "BASE_REF", "INPUT_JOBS"} {
				if _, ok := step.Env[envKey]; !ok {
					t.Errorf("jobs.changes filter step is missing env.%s", envKey)
				}
			}
		}
	}
	if !foundFilterStep {
		t.Error("jobs.changes has no step with id: filter")
	}

	expectedJobGates := map[string]struct {
		group      string
		selfHosted bool
	}{
		"go":                    {group: "go_program"},
		"go-e2e":                {group: "go_program"},
		"ui-e2e":                {group: "go_program"},
		"installer-e2e":         {group: "go_program"},
		"go-orchestrator":       {group: "go_backend"},
		"go-model":              {group: "go_backend"},
		"postgres":              {group: "go_backend"},
		"run-host":              {group: "go_backend"},
		"host-script-container": {group: "go_backend"},
		"package-cache":         {group: "go_backend"},
		"go-unit":               {group: "go_unit"},
		"granule":               {group: "granule"},
		"frontend":              {group: "frontend"},
		"lint":                  {group: "lint"},
		"terraform":             {group: "terraform"},
		"real-vm":               {group: "go_backend", selfHosted: true},
		"real-kubernetes":       {group: "go_backend", selfHosted: true},
	}

	for jobID, want := range expectedJobGates {
		job, exists := wf.Jobs[jobID]
		if !exists {
			t.Errorf(".github/workflows/tests.yml is missing job %q", jobID)
			continue
		}
		if !slices.Contains(job.Needs, "changes") {
			t.Errorf("job %q needs = %v, want it to include \"changes\"", jobID, job.Needs)
		}
		if !strings.Contains(job.If, "needs.changes.outputs."+want.group+" == 'true'") {
			t.Errorf("job %q if = %q, want it to reference needs.changes.outputs.%s == 'true'", jobID, job.If, want.group)
		}
		if !strings.Contains(job.If, "needs.changes.outputs."+jobID+" == 'true'") {
			t.Errorf("job %q if = %q, want it to reference needs.changes.outputs.%s == 'true'", jobID, job.If, jobID)
		}
		if want.selfHosted && !strings.Contains(job.If, "vars.ENABLE_SELF_HOSTED_TESTS == 'true'") {
			t.Errorf("self-hosted job %q if = %q, want it to retain vars.ENABLE_SELF_HOSTED_TESTS == 'true'", jobID, job.If)
		}
	}
}

func TestGitHubCIChangesPathClassification(t *testing.T) {
	allGroups := []string{"go_program", "go_backend", "go_unit", "granule", "frontend", "lint", "terraform"}
	groupToJobs := map[string][]string{
		"go_program": {"go", "go-e2e", "ui-e2e", "installer-e2e", "installer"},
		"go_backend": {"go-orchestrator", "go-model", "postgres", "run-host", "host-script-container", "package-cache", "real-vm", "real-kubernetes"},
		"go_unit":    {"go-unit"},
		"granule":    {"granule"},
		"frontend":   {"frontend"},
		"lint":       {"lint"},
		"terraform":  {"terraform"},
	}

	cases := []struct {
		name        string
		files       []string
		wantEnabled []string
	}{
		{
			name:        "docs-only changes skip all jobs",
			files:       []string{"docs/foo.md", "README.md", "LICENSE", "NOTICE"},
			wantEnabled: nil,
		},
		{
			name:        "agent surface doc enables go_program, go_backend, and go_unit",
			files:       []string{"docs/claude-surface.md"},
			wantEnabled: []string{"go_program", "go_backend", "go_unit"},
		},
		{
			name:        "ui component enables frontend and go_program only",
			files:       []string{"ui/src/App.jsx"},
			wantEnabled: []string{"frontend", "go_program"},
		},
		{
			name:        "ui/src/state.js enables frontend, go_program, and go_unit but skips go_backend",
			files:       []string{"ui/src/state.js"},
			wantEnabled: []string{"frontend", "go_program", "go_unit"},
		},
		{
			name:        "ui/src/taskAttributes.js enables frontend, go_program, and go_unit but skips go_backend",
			files:       []string{"ui/src/taskAttributes.js"},
			wantEnabled: []string{"frontend", "go_program", "go_unit"},
		},
		{
			name:        "backend go file enables go_program, go_backend, and go_unit",
			files:       []string{"pkg/orchestrator/foo.go"},
			wantEnabled: []string{"go_program", "go_backend", "go_unit"},
		},
		{
			name:        "granule file enables granule, go_program, go_backend, and go_unit",
			files:       []string{"third_party/granule/foo.go"},
			wantEnabled: []string{"granule", "go_program", "go_backend", "go_unit"},
		},
		{
			name:        "scripts/foo.sh enables lint, go_program, go_backend, and go_unit",
			files:       []string{"scripts/foo.sh"},
			wantEnabled: []string{"lint", "go_program", "go_backend", "go_unit"},
		},
		{
			name:        "scripts/run-go-tests.sh also enables granule",
			files:       []string{"scripts/run-go-tests.sh"},
			wantEnabled: []string{"granule", "lint", "go_program", "go_backend", "go_unit"},
		},
		{
			name:        "scripts/warm-npm-cache.sh also enables frontend",
			files:       []string{"scripts/warm-npm-cache.sh"},
			wantEnabled: []string{"frontend", "lint", "go_program", "go_backend", "go_unit"},
		},
		{
			name:        "deploy/terraform/gcp/main.tf enables terraform, go_program, go_backend, and go_unit",
			files:       []string{"deploy/terraform/gcp/main.tf"},
			wantEnabled: []string{"terraform", "go_program", "go_backend", "go_unit"},
		},
		{
			name:        "deploy/terraform/gcp/files/deploy.sh enables terraform, lint, go_program, go_backend, and go_unit",
			files:       []string{"deploy/terraform/gcp/files/deploy.sh"},
			wantEnabled: []string{"terraform", "lint", "go_program", "go_backend", "go_unit"},
		},
		{
			name:        "embedded playbook markdown outside top-level is not ignored",
			files:       []string{"pkg/capability/bootstrap/playbooks/gcp-capabilities.md"},
			wantEnabled: []string{"go_program", "go_backend", "go_unit"},
		},
		{
			name:        ".github/workflows/tests.yml enables all groups",
			files:       []string{".github/workflows/tests.yml"},
			wantEnabled: allGroups,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outputs, _, err := runGitHubCIChanges(t, "", map[string]string{
				"EVENT_NAME":    "pull_request",
				"CHANGED_FILES": strings.Join(tc.files, "\n"),
			})
			if err != nil {
				t.Fatalf("github-ci-changes.sh failed: %v", err)
			}

			for _, group := range allGroups {
				want := "false"
				if slices.Contains(tc.wantEnabled, group) {
					want = "true"
				}
				if got := outputs[group]; got != want {
					t.Errorf("group %q = %q, want %q (files=%v)", group, got, want, tc.files)
				}
				for _, jobID := range groupToJobs[group] {
					if got := outputs[jobID]; got != want {
						t.Errorf("job %q = %q, want %q (files=%v)", jobID, got, want, tc.files)
					}
				}
			}
		})
	}
}

func TestGitHubCIChangesMirrorsGrainTestsWorkflowPaths(t *testing.T) {
	grainJobs := []string{
		"go", "go-e2e", "go-orchestrator", "go-model", "go-unit",
		"granule", "postgres", "run-host", "host-script-container",
		"frontend", "ui-e2e", "lint", "terraform", "installer-e2e", "package-cache",
	}
	samplePaths := []string{
		"docs/design.md",
		"docs/ci.md",
		"docs/claude-surface.md",
		"docs/codex-surface.md",
		"docs/agy-surface.md",
		"README.md",
		"LICENSE",
		"NOTICE",
		"Makefile",
		"ui/package.json",
		"ui/src/App.jsx",
		"ui/src/state.js",
		"ui/src/taskAttributes.js",
		"pkg/orchestrator/foo.go",
		"pkg/ci/paths.go",
		"pkg/capability/bootstrap/playbooks/gcp-capabilities.md",
		"third_party/granule/pkg/forward/forward.go",
		"scripts/run-go-tests.sh",
		"scripts/warm-npm-cache.sh",
		"scripts/setup.sh",
		"scripts/granule/guest-inputs.sh",
		"deploy/terraform/gcp/main.tf",
		"deploy/terraform/gcp/files/deploy.sh",
	}

	grainJobDefs := make(map[string]ci.Job, len(grainJobs))
	for _, jobID := range grainJobs {
		grainJobDefs[jobID] = grainJob(t, ".grain/workflows/tests.yml", jobID)
	}

	for _, path := range samplePaths {
		outputs, _, err := runGitHubCIChanges(t, "", map[string]string{
			"EVENT_NAME":    "pull_request",
			"CHANGED_FILES": path,
		})
		if err != nil {
			t.Fatalf("github-ci-changes.sh(%q) failed: %v", path, err)
		}
		for _, jobID := range grainJobs {
			job := grainJobDefs[jobID]
			want := "false"
			if selectsPath(job, path) {
				want = "true"
			}
			if got := outputs[jobID]; got != want {
				t.Errorf("path %q job %q: github-ci-changes.sh = %q, .grain/workflows/tests.yml selectsPath = %q",
					path, jobID, got, want)
			}
		}
	}
}

func TestGitHubCIChangesWorkflowDispatchAndEvents(t *testing.T) {
	allJobKeys := []string{
		"go", "go-e2e", "go-orchestrator", "go-model", "go-unit",
		"granule", "postgres", "run-host", "host-script-container",
		"frontend", "ui-e2e", "lint", "terraform", "installer-e2e", "installer",
		"package-cache", "real-vm", "real-kubernetes",
	}

	t.Run("workflow_dispatch with empty jobs enables all jobs", func(t *testing.T) {
		outputs, _, err := runGitHubCIChanges(t, "", map[string]string{
			"EVENT_NAME": "workflow_dispatch",
			"INPUT_JOBS": "",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, k := range allJobKeys {
			if outputs[k] != "true" {
				t.Errorf("output %q = %q, want \"true\"", k, outputs[k])
			}
		}
	})

	t.Run("workflow_dispatch with go-model,postgres enables only those jobs", func(t *testing.T) {
		outputs, _, err := runGitHubCIChanges(t, "", map[string]string{
			"EVENT_NAME": "workflow_dispatch",
			"INPUT_JOBS": "go-model, postgres",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, k := range allJobKeys {
			want := "false"
			if k == "go-model" || k == "postgres" {
				want = "true"
			}
			if outputs[k] != want {
				t.Errorf("output %q = %q, want %q", k, outputs[k], want)
			}
		}
		if outputs["go_backend"] != "true" {
			t.Errorf("go_backend = %q, want \"true\"", outputs["go_backend"])
		}
		if outputs["go_program"] != "false" {
			t.Errorf("go_program = %q, want \"false\"", outputs["go_program"])
		}
	})

	t.Run("workflow_dispatch with installer alias enables installer-e2e", func(t *testing.T) {
		outputs, _, err := runGitHubCIChanges(t, "", map[string]string{
			"EVENT_NAME": "workflow_dispatch",
			"INPUT_JOBS": "installer",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outputs["installer-e2e"] != "true" || outputs["installer"] != "true" || outputs["go_program"] != "true" {
			t.Errorf("installer outputs = %v, want installer-e2e=true, installer=true, go_program=true", outputs)
		}
		if outputs["go"] != "false" {
			t.Errorf("go = %q, want \"false\"", outputs["go"])
		}
	})

	t.Run("workflow_dispatch with unknown job fails fast", func(t *testing.T) {
		_, out, err := runGitHubCIChanges(t, "", map[string]string{
			"EVENT_NAME": "workflow_dispatch",
			"INPUT_JOBS": "go-model,not-a-job",
		})
		if err == nil {
			t.Fatal("expected non-zero exit status for unknown job ID, got nil")
		}
		if !strings.Contains(out, "not-a-job") {
			t.Errorf("error output %q did not mention unknown job ID \"not-a-job\"", out)
		}
	})

	for _, ev := range []string{"push", "merge_group"} {
		t.Run(ev+" enables all jobs unconditionally", func(t *testing.T) {
			outputs, _, err := runGitHubCIChanges(t, "", map[string]string{
				"EVENT_NAME": ev,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, k := range allJobKeys {
				if outputs[k] != "true" {
					t.Errorf("event %s: output %q = %q, want \"true\"", ev, k, outputs[k])
				}
			}
		})
	}
}

func TestGitHubCIChangesGitDiffResolutionAndFailOpen(t *testing.T) {
	tmpRepo := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpRepo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(tmpRepo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "init")
	baseSHA := runGit("rev-parse", "HEAD")

	if err := os.MkdirAll(filepath.Join(tmpRepo, "ui", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpRepo, "ui", "src", "App.jsx"), []byte("export default null;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "ui/src/App.jsx")
	runGit("commit", "-m", "touch ui")
	headSHA := runGit("rev-parse", "HEAD")

	outputs, _, err := runGitHubCIChanges(t, tmpRepo, map[string]string{
		"EVENT_NAME": "pull_request",
		"BASE_SHA":   baseSHA,
		"HEAD_SHA":   headSHA,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outputs["frontend"] != "true" || outputs["go_program"] != "true" {
		t.Errorf("expected frontend=true and go_program=true from git diff, got %v", outputs)
	}
	if outputs["go_backend"] != "false" || outputs["go_unit"] != "false" {
		t.Errorf("expected go_backend=false and go_unit=false from git diff, got %v", outputs)
	}

	// Unresolvable base SHA fails open to all jobs enabled.
	failOpenOutputs, _, err := runGitHubCIChanges(t, tmpRepo, map[string]string{
		"EVENT_NAME": "pull_request",
		"BASE_SHA":   "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		"HEAD_SHA":   headSHA,
	})
	if err != nil {
		t.Fatalf("unexpected error on fail-open: %v", err)
	}
	for _, group := range []string{"go_program", "go_backend", "go_unit", "granule", "frontend", "lint", "terraform"} {
		if failOpenOutputs[group] != "true" {
			t.Errorf("fail-open group %q = %q, want \"true\"", group, failOpenOutputs[group])
		}
	}
}
