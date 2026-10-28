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

package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubStarterWorkflowCorpus(t *testing.T) {
	// These placeholders are populated by GitHub when it creates a workflow.
	// The original upstream templates remain unchanged in testdata.
	render := strings.NewReplacer(
		"$default-branch", "main",
		"$protected-branches", "release",
		"$cron-daily", "'17 9 * * *'",
		"$cron-weekly", "'17 9 * * 1'",
		"$codeql-languages-matrix", "include: [{language: go, build-mode: autobuild}]",
		"$supported-codeql-languages", "go",
	)
	for _, tc := range []struct {
		file, wantError string
		manual          bool
	}{
		{"ci/go.yml", "", false},
		{"ci/node.js.yml", "", false},
		{"ci/python-package.yml", "", false},
		{"ci/docker-publish.yml", "tags", false},
		{"code-scanning/codeql.yml", "runs-on must be", false},
		{"pages/static.yml", "", true},
	} {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata/starter-workflows", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{WorkflowDir, GitHubWorkflowDir} {
				w, err := Parse(dir+"/"+filepath.Base(tc.file), []byte(render.Replace(string(data))))
				if tc.wantError != "" {
					if err == nil || !strings.Contains(err.Error(), tc.wantError) {
						t.Fatalf("error = %v, want %q", err, tc.wantError)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if !w.On.Matches(EventPush, "main") || w.On.Matches(EventPush, "unrelated") || w.On.Manual != tc.manual {
					t.Fatalf("wrong trigger semantics: %+v", w.On)
				}
				p, err := PlanRun(Request{Repo: "example/project", Ref: "main", Event: EventPush}, []Workflow{w})
				if err != nil {
					t.Fatal(err)
				}
				if len(p.Jobs) != 1 || p.Jobs[0].Job.Actions == nil || !p.Jobs[0].Job.Cache {
					t.Fatalf("wrong Actions plan: %+v", p)
				}
			}
		})
	}
}
