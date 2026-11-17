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

package cli

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/ciapi"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// TestAGrantedCIJobsStepReadsItsAPITokenInAHostSandbox: a job holding an
// api grant, run in a host sandbox (-host-sandboxes), finds GRAIN_API_URL
// and GRAIN_API_TOKEN in its step's environment, and the token is the one
// grain's CI API listener honours for the job's repository. The files
// ciapi places under /home/debian/.grain-api land inside the run's own
// directory there, where no step looks for them, so the environment is
// what has to carry them (the 2026-11-11 capabilities walkthrough's §6
// item 4 found a granted job's step holding neither).
func TestAGrantedCIJobsStepReadsItsAPITokenInAHostSandbox(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	ctx := context.Background()

	// acme/widgets, served from a directory as the git proxy would serve it.
	remotes := t.TempDir()
	seed := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", seed},
		{"-C", seed, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "seed"},
		{"clone", "-q", "--bare", seed, filepath.Join(remotes, "acme", "widgets.git")},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	gate := ciapi.NewEndpoint()
	gate.SetURL("http://127.0.0.1:9731")
	reg := model.NewCapabilityRegistry()
	reg.RegisterCIDynamic(ciapi.DynamicProvider(gate, ciapi.Config{Repos: []model.RepoRef{{Owner: "acme", Name: "widgets"}}}))

	adapter, _, _ := newCIAdapter(&fakeSandbox{})
	adapter.Sandboxes = orchestrator.NewHostSandboxes(t.TempDir())
	git := httptest.NewServer(gitHTTPBackendLive(remotes))
	t.Cleanup(git.Close)
	adapter.GitRemoteBase = git.URL
	adapter.Capabilities = func() *model.CapabilityRegistry { return reg }

	job, err := adapter.Acquire(ctx, ci.SandboxRequest{
		Name: "ci-api-env-probe", Repo: "acme/widgets", Ref: "main", Event: ci.EventManual,
		Capabilities: []string{model.APIRef("ci-read")},
	})
	if err != nil {
		t.Fatalf("acquiring the granted job's host sandbox: %v", err)
	}
	t.Cleanup(func() { _ = job.Release(context.WithoutCancel(ctx)) })

	res, err := job.Exec(ctx, ci.Command{Script: `printf 'url=%s\ntoken=%s\n' "${GRAIN_API_URL:-}" "${GRAIN_API_TOKEN:-}"`})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("the step = %+v, %v", res, err)
	}
	var url, token string
	for _, line := range strings.Split(res.Output, "\n") {
		if v, ok := strings.CutPrefix(line, "url="); ok {
			url = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(line, "token="); ok {
			token = strings.TrimSpace(v)
		}
	}
	if url != gate.URL() {
		t.Errorf("the step's GRAIN_API_URL = %q, want the listener's %q (output %q)", url, gate.URL(), res.Output)
	}
	grant, ok := gate.Tokens.Lookup(token)
	if token == "" || !ok {
		t.Fatalf("the step's GRAIN_API_TOKEN = %q, want a token the listener honours (output %q)", token, res.Output)
	}
	if grant.Repo.String() != "acme/widgets" {
		t.Errorf("the step's token is worth %+v, want acme/widgets's job", grant)
	}

	// A step's own setting of either name is the step's to make.
	res, err = job.Exec(ctx, ci.Command{
		Script: `printf '%s' "$GRAIN_API_URL"`,
		Env:    map[string]string{ciapi.URLEnv: "http://127.0.0.1:1"},
	})
	if err != nil || strings.TrimSpace(res.Output) != "http://127.0.0.1:1" {
		t.Errorf("a step setting GRAIN_API_URL itself saw %q, %v", res.Output, err)
	}

	// And a job that holds no api grant is told nothing.
	plain, err := adapter.Acquire(ctx, ci.SandboxRequest{Name: "ci-api-env-none", Repo: "acme/widgets", Ref: "main", Event: ci.EventManual})
	if err != nil {
		t.Fatalf("acquiring an ungranted job's host sandbox: %v", err)
	}
	t.Cleanup(func() { _ = plain.Release(context.WithoutCancel(ctx)) })
	res, err = plain.Exec(ctx, ci.Command{Script: `printf '%s%s' "${GRAIN_API_URL:-}" "${GRAIN_API_TOKEN:-}"`})
	if err != nil || strings.TrimSpace(res.Output) != "" {
		t.Errorf("an ungranted job's step saw %q, %v; want no API address or token", res.Output, err)
	}
}
