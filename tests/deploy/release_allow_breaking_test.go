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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"gopkg.in/yaml.v3"
)

// githubPromoteReleasePath is promote-release.yml's GitHub Actions port.
const githubPromoteReleasePath = ".github/workflows/promote-release.yml"

// stubReleaseFromTrunk is a checkout whose scripts/release-from-trunk.sh
// only says what it was handed: its arguments and RELEASE_ALLOW_BREAKING.
func stubReleaseFromTrunk(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	stub := "#!/usr/bin/env bash\necho \"args=$* allow_breaking=${RELEASE_ALLOW_BREAKING-unset}\"\n"
	if err := os.WriteFile(filepath.Join(dir, "scripts", "release-from-trunk.sh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runStep(t *testing.T, dir, script string, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// grain's promote-release approve job passes RELEASE_ALLOW_BREAKING=<the
// release> on to release-from-trunk.sh only when the person-only approval
// route vouched for it with GRAIN_CI_RELEASE_ALLOW_BREAKING=true -- a
// variable no API caller and no env: block can set -- and blanks one
// that reached the step any other way.
func TestGrainPromoteReleaseForwardsAllowBreakingOnlyFromTheApproval(t *testing.T) {
	w := promoteReleaseWorkflow(t)
	var approve string
	for _, job := range w.Jobs {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "scripts/release-from-trunk.sh approve") {
				if job.ID != "approve" {
					t.Errorf("%s job %s runs release-from-trunk.sh approve; only the approve job may", promoteReleasePath, job.ID)
				}
				approve = step.Run
			}
		}
	}
	if approve == "" {
		t.Fatalf("%s has no step running release-from-trunk.sh approve", promoteReleasePath)
	}
	dir := stubReleaseFromTrunk(t)
	version := ci.ReleaseVersionEnv + "=v2.4.0"
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"vouched", []string{version, ci.ReleaseAllowBreakingEnv + "=true"}, "args=approve v2.4.0 allow_breaking=v2.4.0"},
		{"not asked", []string{version}, "args=approve v2.4.0 allow_breaking="},
		{"not true", []string{version, ci.ReleaseAllowBreakingEnv + "=v2.4.0"}, "args=approve v2.4.0 allow_breaking="},
		{"unvouched", []string{version, "RELEASE_ALLOW_BREAKING=v2.4.0"}, "args=approve v2.4.0 allow_breaking="},
	} {
		out, err := runStep(t, dir, approve, tc.env...)
		if err != nil {
			t.Errorf("%s: the approve step failed: %v\n%s", tc.name, err, out)
			continue
		}
		if got := strings.TrimSpace(out); got != tc.want {
			t.Errorf("%s: the approve step ran %q, want %q", tc.name, got, tc.want)
		}
	}
}

func githubPromoteReleaseWorkflow(t *testing.T) githubActionsWorkflow {
	t.Helper()
	var w githubActionsWorkflow
	if err := yaml.Unmarshal([]byte(read(t, githubPromoteReleasePath)), &w); err != nil {
		t.Fatalf("%s does not parse: %v", githubPromoteReleasePath, err)
	}
	return w
}

// The GitHub Actions port takes the same answer as an allow_breaking
// input and hands the approve step RELEASE_ALLOW_BREAKING=<version> when
// it is true, and nothing otherwise.
func TestGitHubPromoteReleaseForwardsAllowBreaking(t *testing.T) {
	w := githubPromoteReleaseWorkflow(t)
	in, ok := w.On.WorkflowDispatch.Inputs["allow_breaking"]
	if !ok || in.Type != "boolean" || in.Required || in.Default != "false" {
		t.Errorf("%s's allow_breaking input = %+v (declared %v), want an optional boolean defaulting to false",
			githubPromoteReleasePath, in, ok)
	}
	var found bool
	for id, job := range w.Jobs {
		for _, step := range job.Steps {
			if !strings.Contains(step.Run, "scripts/release-from-trunk.sh approve") {
				continue
			}
			found = true
			if id != "approve" {
				t.Errorf("%s job %s runs release-from-trunk.sh approve; only the approve job may", githubPromoteReleasePath, id)
			}
			const want = "${{ inputs.allow_breaking && inputs.version || '' }}"
			if got := step.Env["RELEASE_ALLOW_BREAKING"]; got != want {
				t.Errorf("%s's approve step sets RELEASE_ALLOW_BREAKING: %q, want %q", githubPromoteReleasePath, got, want)
			}
		}
	}
	if !found {
		t.Fatalf("%s has no step running release-from-trunk.sh approve", githubPromoteReleasePath)
	}
}

// Every step that writes inputs.version into $GITHUB_ENV refuses one that
// is not vX.Y.Z first: a newline in it would otherwise set any variable
// for the steps after it, RELEASE_ALLOW_BREAKING among them. The rollback
// job's rollback_to is held to the same rule.
func TestGitHubPromoteReleaseValidatesTheVersionBeforeGITHUB_ENV(t *testing.T) {
	w := githubPromoteReleaseWorkflow(t)
	var checked int
	for id, job := range w.Jobs {
		for _, step := range job.Steps {
			if step.Env["INPUT_VERSION"] != "${{ inputs.version }}" {
				continue
			}
			checked++
			for _, version := range []string{"v2.4.0\nRELEASE_ALLOW_BREAKING=v2.4.0", "2.4.0", "v2.4", "v2.4.0-pending", ""} {
				envFile := filepath.Join(t.TempDir(), "github_env")
				out, err := runStep(t, t.TempDir(), step.Run,
					"INPUT_VERSION="+version, "INPUT_DRY_RUN=true", "GH_REF_NAME=main",
					"GITHUB_EVENT_NAME=workflow_dispatch", "GITHUB_ACTOR=alice", "GITHUB_REF=refs/heads/main",
					"GITHUB_REPOSITORY=a/b", "GITHUB_SHA=0123abc",
					"GITHUB_ENV="+envFile)
				if err == nil {
					t.Errorf("%s job %s step %q accepted version %q:\n%s", githubPromoteReleasePath, id, step.Name, version, out)
				}
				if written, _ := os.ReadFile(envFile); len(written) > 0 {
					t.Errorf("%s job %s step %q wrote $GITHUB_ENV for version %q:\n%s",
						githubPromoteReleasePath, id, step.Name, version, written)
				}
			}
			envFile := filepath.Join(t.TempDir(), "github_env")
			out, err := runStep(t, t.TempDir(), step.Run,
				"INPUT_VERSION=v2.4.0", "INPUT_DRY_RUN=true", "GH_REF_NAME=main",
				"GITHUB_EVENT_NAME=workflow_dispatch", "GITHUB_ACTOR=alice", "GITHUB_REF=refs/heads/main",
				"GITHUB_REPOSITORY=a/b", "GITHUB_SHA=0123abc",
				"GITHUB_ENV="+envFile)
			written, _ := os.ReadFile(envFile)
			if err != nil || !strings.Contains(string(written), "GRAIN_CI_RELEASE_VERSION=v2.4.0\n") {
				t.Errorf("%s job %s step %q refused v2.4.0 (%v):\n%s\n$GITHUB_ENV:\n%s",
					githubPromoteReleasePath, id, step.Name, err, out, written)
			}
		}
	}
	rollback := 0
	for id, job := range w.Jobs {
		for _, step := range job.Steps {
			if step.Env["INPUT_ROLLBACK_TO"] != "${{ inputs.rollback_to }}" {
				continue
			}
			rollback++
			for _, to := range []string{"v2.3.0\nRELEASE_ALLOW_BREAKING=v2.4.0", "2.3.0", "latest", "v2.3.0-retracted"} {
				envFile := filepath.Join(t.TempDir(), "github_env")
				out, err := runStep(t, t.TempDir(), step.Run,
					"INPUT_VERSION=v2.4.0", "INPUT_ROLLBACK_TO="+to, "INPUT_DRY_RUN=true", "GH_REF_NAME=main",
					"GITHUB_EVENT_NAME=workflow_dispatch", "GITHUB_ACTOR=alice", "GITHUB_REF=refs/heads/main",
					"GITHUB_REPOSITORY=a/b", "GITHUB_SHA=0123abc", "GITHUB_ENV="+envFile)
				if err == nil {
					t.Errorf("%s job %s step %q accepted rollback_to %q:\n%s", githubPromoteReleasePath, id, step.Name, to, out)
				}
				if written, _ := os.ReadFile(envFile); len(written) > 0 {
					t.Errorf("%s job %s step %q wrote $GITHUB_ENV for rollback_to %q:\n%s", githubPromoteReleasePath, id, step.Name, to, written)
				}
			}
			envFile := filepath.Join(t.TempDir(), "github_env")
			out, err := runStep(t, t.TempDir(), step.Run,
				"INPUT_VERSION=v2.4.0", "INPUT_ROLLBACK_TO=v2.3.0", "INPUT_DRY_RUN=true", "GH_REF_NAME=main",
				"GITHUB_EVENT_NAME=workflow_dispatch", "GITHUB_ACTOR=alice", "GITHUB_REF=refs/heads/main",
				"GITHUB_REPOSITORY=a/b", "GITHUB_SHA=0123abc", "GITHUB_ENV="+envFile)
			written, _ := os.ReadFile(envFile)
			if err != nil || !strings.Contains(string(written), "RELEASE_ROLLBACK_TO=v2.3.0\n") {
				t.Errorf("%s job %s step %q refused rollback_to v2.3.0 (%v):\n%s\n$GITHUB_ENV:\n%s",
					githubPromoteReleasePath, id, step.Name, err, out, written)
			}
		}
	}
	if rollback != 1 {
		t.Errorf("%s has %d steps reading inputs.rollback_to, want 1 (rollback)", githubPromoteReleasePath, rollback)
	}
	if checked != 3 {
		t.Errorf("%s has %d steps reading inputs.version as INPUT_VERSION, want 3 (approve, reject and rollback)", githubPromoteReleasePath, checked)
	}
}
