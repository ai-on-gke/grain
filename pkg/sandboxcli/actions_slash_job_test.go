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

// slashJobCI is a GitHub Actions run whose jobs' logs are logs, by job id.
type slashJobCI struct {
	actionsCI
	logs map[string]string
}

func (a slashJobCI) ExternalJobLog(_ context.Context, _, job string) (string, error) {
	if text, ok := a.logs[job]; ok {
		return text, nil
	}
	return "", os.ErrNotExist
}

// A job whose name holds a "/" -- the nightly "pkg/cli stress under
// load" -- is read by its name, by the %2F-escaped name grain ls lists
// it under, and by its id, through grain ci log and grain ls/read alike;
// a job named "pkg" beside it is not what any of them reach.
func TestSandboxCLIReadsAnActionsJobWhoseNameHasASlash(t *testing.T) {
	const job = "pkg/cli stress under load"
	at := func(sec int) *time.Time {
		t := time.Date(2026, 11, 15, 15, 0, sec, 0, time.UTC)
		return &t
	}
	gha := ci.Run{ID: "gha:acme:widgets:43", Provider: ci.ProviderGitHubActions, Repo: "acme/widgets", Ref: "main",
		Commit: "abcdefabcdefabcdef", Event: "schedule", Conclusion: ci.ConclusionFailure,
		Actions: &ci.ActionsRun{RunID: 43, RunAttempt: 1, Workflow: ".github/workflows/nightly.yml", WorkflowName: "Nightly"},
		Jobs: []ci.JobRun{
			{JobID: "900", Name: "pkg", Conclusion: ci.ConclusionSuccess},
			{JobID: "132854265019", Name: job, Conclusion: ci.ConclusionFailure, Steps: []ci.StepRun{
				{Name: "Build", Conclusion: ci.ConclusionSuccess, StartedAt: at(0), FinishedAt: at(1)},
				{Name: "Stress", Conclusion: ci.ConclusionFailure, StartedAt: at(5), FinishedAt: at(6)},
			}},
		}}
	const log = "2026-11-15T15:00:00.1Z building\n" +
		"2026-11-15T15:00:05.1Z --- FAIL: TestStress\n" +
		"2026-11-15T15:00:08.1Z cleaning up\n"
	tokenFile := filepath.Join(t.TempDir(), "tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb": "tok"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	ext := slashJobCI{actionsCI: actionsCI{run: gha}, logs: map[string]string{"900": "the wrong job\n", "132854265019": log}}
	proxy := &gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{CIStore: ci.NewRunStore(10), ExternalCI: ext, Redactor: redact.New(nil)},
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

	const runURI = "ci://acme/widgets/gha:acme:widgets:43"
	out, errOut, code := run("ls", runURI)
	if code != 0 || !strings.Contains(out, "pkg%2Fcli stress under load/") || strings.Contains(out, job+"/") {
		t.Fatalf("grain ls %s = %d %q (stderr %s): the job should list as one escaped segment", runURI, code, out, errOut)
	}
	const stress = "2026-11-15T15:00:05.1Z --- FAIL: TestStress\n"
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"ci", "log", "gha:acme:widgets:43", "--job", job}, log},
		{[]string{"ci", "log", "gha:acme:widgets:43", "--job", "pkg%2Fcli stress under load"}, log},
		{[]string{"ci", "log", "gha:acme:widgets:43", "--job", "132854265019"}, log},
		{[]string{"ci", "log", "gha:acme:widgets:43", "--job", job, "--step", "Stress"}, stress},
		{[]string{"ci", "log", "gha:acme:widgets:43", "--job", "pkg%2Fcli stress under load", "--step", "Stress"}, stress},
		{[]string{"ci", "log", "gha:acme:widgets:43", "--job", "132854265019", "--lines", "1"}, "2026-11-15T15:00:08.1Z cleaning up\n"},
		{[]string{"ci", "log", "gha:acme:widgets:43", "--job", "pkg"}, "the wrong job\n"},
		{[]string{"read", runURI + "/" + job + "/job.log"}, log},
		{[]string{"read", runURI + "/pkg%2Fcli stress under load/job.log"}, log},
		{[]string{"read", runURI + "/" + job + "/Stress.log"}, stress},
	} {
		out, errOut, code := run(c.args...)
		if code != 0 || out != c.want {
			t.Errorf("grain %s = %d\n%s\nwant\n%s\n(stderr %s)", strings.Join(c.args, " "), code, out, c.want, errOut)
		}
	}
	for _, uri := range []string{runURI + "/" + job, runURI + "/pkg%2Fcli stress under load"} {
		if out, errOut, code := run("ls", uri); code != 0 || !strings.Contains(out, "job.log") || !strings.Contains(out, "Stress.log") {
			t.Errorf("grain ls %s = %d %q (stderr %s), want the job's logs", uri, code, out, errOut)
		}
	}
}
