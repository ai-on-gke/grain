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

package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The CI part of a run's prompt -- sandboxToolingSection's CI sentence and
// grainCLISection's "CI & pull request" bullet -- names only the commands
// the run can use (task 4364). Before, it listed every `grain ci` / `grain
// pr` command to every run, so a run on a ci_mode: github repository tried
// `grain ci log`, got "forbidden", and asked a person for grain#ci, which
// no such repository can grant. These goldens pin each CI mode and grant
// set's wording whole; regenerate with
//
//	GRAIN_UPDATE_PROMPT_GOLDEN=1 go test ./pkg/orchestrator -run CIPromptGolden
func TestCIPromptGolden(t *testing.T) {
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	for _, tc := range []struct {
		name   string
		grants []model.Grant
	}{
		{name: "grain-ci", grants: []model.Grant{{Capability: grainci.Grant, Via: model.GrantByDefault}}},
		{name: "github-ci-default-read", grants: []model.Grant{model.DefaultCIReadGrant(repo)}},
		{name: "github-ci-no-read"},
		{name: "github-ci-read-elsewhere", grants: []model.Grant{{Capability: "api:ci-read@acme/other", Via: model.GrantByLabel}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := model.Task{ID: "7", Target: &repo, Grants: tc.grants}
			got := sandboxToolingSection(true, runCIAccessOf(task)) + grainCLISection(task)
			path := filepath.Join("testdata", "ci_prompt", tc.name+".golden")
			if os.Getenv("GRAIN_UPDATE_PROMPT_GOLDEN") != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v (GRAIN_UPDATE_PROMPT_GOLDEN=1 writes it)", path, err)
			}
			if got != string(want) {
				t.Errorf("prompt for %s differs from %s:\n--- got ---\n%s\n--- want ---\n%s", tc.name, path, got, want)
			}
		})
	}
}

// What every golden above must and must not say, so a regenerated golden
// cannot quietly reintroduce what this was about.
func TestCIPromptNamesOnlyUsableCommands(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	ciLine := func(task model.Task) string {
		for _, line := range strings.Split(grainCLISection(task), "\n") {
			if strings.HasPrefix(line, "- **CI & pull request**") {
				return line
			}
		}
		t.Fatalf("no CI bullet in %q", grainCLISection(task))
		return ""
	}

	grain := ciLine(model.Task{Target: &repo, Grants: []model.Grant{{Capability: grainci.Grant, Via: model.GrantByDefault}}})
	for _, want := range []string{"`grain ci run-job <job>`", "`grain ci retrigger`", "`grain ci test-history <test>`", "`grain pr status`"} {
		if !strings.Contains(grain, want) {
			t.Errorf("grain-CI bullet = %q, want %s", grain, want)
		}
	}

	read := ciLine(model.Task{Target: &repo, Grants: []model.Grant{model.DefaultCIReadGrant(repo)}})
	for _, want := range []string{"`grain ci wait`", "`grain ci list`", "`grain ci log", "`grain pr status`", "`grain pr base-status`", "`grain pr open`"} {
		if !strings.Contains(read, want) {
			t.Errorf("GitHub-CI bullet with ci:read = %q, want %s", read, want)
		}
	}
	for _, unwanted := range []string{"`grain ci run-job <job>`", "`grain ci retrigger`,", "--jobs"} {
		if strings.Contains(read, unwanted) {
			t.Errorf("GitHub-CI bullet with ci:read = %q offers %s", read, unwanted)
		}
	}

	none := ciLine(model.Task{Target: &repo})
	for _, want := range []string{"`grain ci wait`", "`grain pr open`", "`request_access` for `https://grain.dev/capabilities/api#ci-read` on acme/widgets", "never `grain#ci`"} {
		if !strings.Contains(none, want) {
			t.Errorf("GitHub-CI bullet without ci:read = %q, want %s", none, want)
		}
	}
	for _, unwanted := range []string{"`grain ci list`", "`grain ci log", "test-history", "`grain pr status`", "`grain pr base-status`", "run-job", "retrigger"} {
		if strings.Contains(none, unwanted) {
			t.Errorf("GitHub-CI bullet without ci:read = %q offers %s, which answers forbidden", none, unwanted)
		}
	}
	if n := strings.Count(none, "api#ci-read"); n != 1 {
		t.Errorf("GitHub-CI bullet without ci:read names api#ci-read %d times, want once", n)
	}

	// No variant offers grain#ci as something to ask for.
	for _, line := range []string{grain, read, none} {
		if strings.Contains(line, "request_access` for `https://grain.dev/capabilities/grain#ci") ||
			strings.Contains(line, "request grain#ci") {
			t.Errorf("CI bullet %q suggests requesting grain#ci", line)
		}
	}
}
