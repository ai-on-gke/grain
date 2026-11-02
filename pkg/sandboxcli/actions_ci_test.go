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

package sandboxcli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/redact"
)

// actionsCI is one GitHub Actions run of acme/widgets, as pkg/ui serves it.
type actionsCI struct{ run ci.Run }

func (a actionsCI) FindExternalRuns(_ context.Context, f ci.RunFilter, _ int) ([]ci.Run, string) {
	if f.Repo != a.run.Repo || (f.Provider != "" && f.Provider != ci.ProviderGitHubActions) {
		return nil, ""
	}
	return []ci.Run{a.run}, ""
}

func (a actionsCI) ExternalRun(_ context.Context, id string) (ci.Run, bool, error) {
	return a.run, id == a.run.ID, nil
}

func (a actionsCI) ExternalJobLog(_ context.Context, _, job string) (string, error) {
	if job != "701" {
		return "", os.ErrNotExist
	}
	return "login ok\npushing\nerror: GHCR_TOKEN is not set\n", nil
}

// The sandbox CLI's golden output for a GitHub Actions repository: ci
// list with both providers and each alone, ci show, and ci log of a job.
func TestSandboxCLIReadsGitHubActionsRuns(t *testing.T) {
	store := ci.NewRunStore(10)
	grainAt := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	store.Put(ci.Run{ID: "run-1", Repo: "acme/widgets", Ref: "main", Commit: "1111111111111111", Conclusion: ci.ConclusionSuccess,
		StartedAt: &grainAt, Jobs: []ci.JobRun{{Name: "go", Conclusion: ci.ConclusionSuccess}}})
	ghaAt := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	gha := ci.Run{ID: "gha:acme:widgets:42", Provider: ci.ProviderGitHubActions, Repo: "acme/widgets", Ref: "main",
		Commit: "abcdefabcdefabcdef", Event: "push", Conclusion: ci.ConclusionFailure, StartedAt: &ghaAt,
		Actions: &ci.ActionsRun{RunID: 42, RunAttempt: 1, Workflow: ".github/workflows/release.yml", WorkflowName: "Release",
			URL: "https://github.com/acme/widgets/actions/runs/42", FailedJobs: []string{"publish"}},
		Jobs: []ci.JobRun{
			{JobID: "700", Name: "build", Conclusion: ci.ConclusionSuccess, Steps: []ci.StepRun{{Name: "checkout", Conclusion: ci.ConclusionSuccess}}},
			{JobID: "701", Name: "publish", Conclusion: ci.ConclusionFailure, Steps: []ci.StepRun{
				{Name: "login", Conclusion: ci.ConclusionSuccess}, {Name: "push", Conclusion: ci.ConclusionFailure}}},
		}}
	tokenFile := filepath.Join(t.TempDir(), "tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb": "tok"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{CIStore: store, ExternalCI: actionsCI{run: gha}, Redactor: redact.New(nil)},
		NamespaceScopes: cliTestScopes{"sb": {TaskID: "7", RunID: "r", TaskRepo: "acme/widgets", TaskBranch: "grain/task-7",
			MayReadCI: true, AllowedRepos: map[string]bool{"acme/widgets": true}}},
	}
	srv := httptest.NewServer(gitproxy.NewHandler(proxy))
	defer srv.Close()
	env := fakeSandboxEnv(map[string]string{"GRAIN_SANDBOX_PROXY": srv.URL, "GRAIN_SANDBOX_TOKEN": "tok"}, "")
	run := func(args ...string) (string, string, int) {
		var out, errOut bytes.Buffer
		code, handled := maybeRunSandboxCLI(args, env, &out, &errOut)
		if !handled {
			t.Fatalf("%q not handled in sandbox mode", args)
		}
		return out.String(), errOut.String(), code
	}

	const ghaLine = "gha:acme:widgets:42 github-actions/release.yml main abcdefabcdef failure 2026-11-01T10:00:00Z failing: publish\n"
	const grainLine = "run-1 grain main 111111111111 success 2026-11-01T09:00:00Z\n"
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"ci", "list", "--repo", "acme/widgets"}, "2 of 2 matching run(s) in acme/widgets, newest first:\n" + ghaLine + grainLine},
		{[]string{"ci", "list", "--repo", "acme/widgets", "--provider", "grain"}, "1 of 1 matching run(s) in acme/widgets, newest first:\n" + grainLine},
		{[]string{"ci", "list", "--repo", "acme/widgets", "--provider", "github-actions"}, "1 of 1 matching run(s) in acme/widgets, newest first:\n" + ghaLine},
		{[]string{"ci", "show", "gha:acme:widgets:42"}, strings.TrimSuffix(ghaLine, "\n") + " (acme/widgets)\n" +
			"workflow: Release .github/workflows/release.yml, attempt 1, event push\n" +
			"url: https://github.com/acme/widgets/actions/runs/42\n" +
			"job build: success (id 700)\n  1. checkout: success\n" +
			"job publish: failure (id 701)\n  1. login: success\n  2. push: failure\n"},
		{[]string{"ci", "log", "gha:acme:widgets:42", "--job", "publish"}, "login ok\npushing\nerror: GHCR_TOKEN is not set\n"},
		{[]string{"ci", "log", "gha:acme:widgets:42", "--job", "publish", "--lines", "1"}, "error: GHCR_TOKEN is not set\n"},
	} {
		out, errOut, code := run(c.args...)
		if code != 0 || out != c.want {
			t.Errorf("grain %s = %d\n%s\nwant\n%s\n(stderr %s)", strings.Join(c.args, " "), code, out, c.want, errOut)
		}
	}
	if out, errOut, code := run("ci", "log", "gha:acme:widgets:42"); code != 0 || !strings.Contains(out, "publish") || !strings.Contains(out, "build") {
		t.Errorf("ci log with no job should list the jobs: %d %q %s", code, out, errOut)
	}
}
