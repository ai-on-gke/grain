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

package daemon

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/policy"
)

func TestLocalSettingsAreAliasesOverTheDaemonsOwn(t *testing.T) {
	cfg, err := ApplyLocalSettings(Config{AgentFramework: model.AgentFrameworkAntigravity},
		LocalSettings{SandboxBackend: "granule", ModelBackend: "claude", Repos: []string{"acme/widgets", "https://github.com/acme/tools.git"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.GranuleSandboxes || cfg.SandboxBackend() != model.SandboxBackendGranule {
		t.Errorf("-sandbox-backend=granule selected %q", cfg.SandboxBackend())
	}
	if cfg.AgentFramework != model.AgentFrameworkClaude {
		t.Errorf("-model-backend=claude set the framework to %q", cfg.AgentFramework)
	}
	if want := []model.RepoRef{{Owner: "acme", Name: "widgets"}, {Owner: "acme", Name: "tools"}}; !reflect.DeepEqual(cfg.LocalRepos, want) {
		t.Errorf("-repo resolved to %+v, want %+v", cfg.LocalRepos, want)
	}
	if cfg.DefaultTargetRepo != "acme/widgets" {
		t.Errorf("default target repo = %q, want the first -repo", cfg.DefaultTargetRepo)
	}

	cfg, err = ApplyLocalSettings(Config{}, LocalSettings{ModelBackend: "agy", SandboxBackend: "container"}, nil)
	if err != nil || cfg.AgentFramework != model.AgentFrameworkAntigravity || !cfg.ContainerSandboxes {
		t.Errorf("-model-backend=agy -sandbox-backend=container: %+v, %v", cfg.AgentFramework, err)
	}
	cfg, _ = ApplyLocalSettings(Config{DefaultTargetRepo: "acme/main"}, LocalSettings{Repos: []string{"acme/widgets"}}, nil)
	if cfg.DefaultTargetRepo != "acme/main" {
		t.Errorf("-repo replaced an explicit -default-target-repo: %q", cfg.DefaultTargetRepo)
	}

	for name, c := range map[string]struct {
		l      LocalSettings
		cfg    Config
		passed map[string]bool
		want   string
	}{
		"unknown backend":  {l: LocalSettings{SandboxBackend: "vm"}, want: "not one of container"},
		"unknown model":    {l: LocalSettings{ModelBackend: "gpt"}, want: "not one of agy"},
		"not a repository": {l: LocalSettings{Repos: []string{"widgets"}}, want: "-repo widgets"},
	} {
		if _, err := ApplyLocalSettings(c.cfg, c.l, c.passed); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one saying %q", name, err, c.want)
		}
	}
	// host is -host-sandboxes, refused where that flag is (GSA1-21).
	cfg, err = ApplyLocalSettings(Config{}, LocalSettings{SandboxBackend: "host"}, nil)
	if err != nil || !cfg.HostSandboxes {
		t.Errorf("-sandbox-backend=host: host sandboxes %v, %v", cfg.HostSandboxes, err)
	}
	if err := CheckSandboxBackend(cfg, false); err == nil || !strings.Contains(err.Error(), "-sandbox-dir is required") {
		t.Errorf("-sandbox-backend=host with no -sandbox-dir: %v, want CheckHostSandboxes' refusal", err)
	}
	cfg.SandboxDir, cfg.UiAddr = t.TempDir(), "0.0.0.0:4242"
	if err := CheckSandboxBackend(cfg, false); err == nil || !strings.Contains(err.Error(), "reachable by others") {
		t.Errorf("-sandbox-backend=host on a shared daemon: %v, want it refused", err)
	}
	cfg.UiAddr = "127.0.0.1:4242"
	if err := CheckSandboxBackend(cfg, false); err != nil {
		t.Errorf("-sandbox-backend=host on a loopback workstation: %v", err)
	}
	// Two backends at once is checkConfig's to refuse, naming both.
	cfg, _ = ApplyLocalSettings(Config{ContainerSandboxes: true}, LocalSettings{SandboxBackend: "granule"}, nil)
	if got := SelectedSandboxBackends(cfg); len(got) != 2 {
		t.Errorf("-container-sandboxes -sandbox-backend=granule selected %v", got)
	}
}

func TestResolveLocalRepoReadsACheckoutsOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@github.com:Acme/Widgets.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	got, err := ResolveLocalRepo(dir)
	if err != nil || got != (model.RepoRef{Owner: "Acme", Name: "Widgets"}) {
		t.Errorf("ResolveLocalRepo(checkout) = %+v, %v", got, err)
	}
	if _, err := ResolveLocalRepo(t.TempDir()); err == nil {
		t.Error("a directory with no origin resolved to a repository")
	}
}

// grain/task-4501: on a single-tenant daemon every task is filed as the
// daemon's own actor, and a policy naming only repos held all of them as
// unapproved external requests the owner could never approve.
func TestSingleTenantOwnerIsALocalRequester(t *testing.T) {
	p, err := policy.Parse([]byte("repos: [acme/widgets]\n"))
	if err != nil {
		t.Fatal(err)
	}
	note := attachSingleTenantOwner(Config{Actor: "debian"}, p)
	if !p.IsLocalRequester("debian") || !strings.Contains(note, "debian") {
		t.Errorf("single-tenant owner not attached: %q, %v", note, p.LocalRequesters)
	}
	if !p.IsLocal(policy.Requester{Name: "debian"}) {
		t.Error("the owner's task is not local")
	}
	// Already listed: nothing added twice, and nothing to say.
	if note := attachSingleTenantOwner(Config{Actor: "DEBIAN"}, p); note != "" || len(p.LocalRequesters) != 1 {
		t.Errorf("re-attaching the owner = %q, %v", note, p.LocalRequesters)
	}
}
