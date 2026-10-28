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
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/policy"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// LocalSettings are the daemon's direct local-configuration flags: the
// short spellings a workstation's owner reaches for, each an alias over
// a setting the daemon already has (docs/local-policy.md).
type LocalSettings struct {
	// Repos is every -repo: a repository this workstation works on, as a
	// local checkout's path or anything model.ParseRepo reads (owner/name,
	// a browser URL, an SSH remote).
	Repos []string
	// SandboxBackend is -sandbox-backend: container, granule,
	// kubernetes, kubernetes-granule, host or none, for
	// -container-sandboxes and the rest.
	SandboxBackend string
	// ModelBackend is -model-backend: agy (antigravity), claude or codex,
	// for -agent-framework.
	ModelBackend string
}

// ApplyLocalSettings folds l into cfg. passed is which flags were given
// by name, so an alias contradicting the flag it stands for is refused
// rather than one of the two silently winning.
func ApplyLocalSettings(cfg Config, l LocalSettings, passed map[string]bool) (Config, error) {
	if b := strings.TrimSpace(l.SandboxBackend); b != "" {
		switch model.SandboxBackend(b) {
		case model.SandboxBackendContainer:
			cfg.ContainerSandboxes = true
		case model.SandboxBackendGranule:
			cfg.GranuleSandboxes = true
		case model.SandboxBackendKubernetes:
			cfg.K8sSandboxes = true
		case model.SandboxBackendKubernetesGranule:
			cfg.K8sGranuleSandboxes = true
		case model.SandboxBackendNone:
			cfg.DisableNativeSandboxes = true
		case model.SandboxBackendHost:
			// Under every refusal -host-sandboxes has, since this is
			// that flag (CheckHostSandboxes, run by CheckSandboxBackend).
			cfg.HostSandboxes = true
		default:
			return cfg, fmt.Errorf("-sandbox-backend=%s: not one of container, granule, kubernetes, kubernetes-granule, host, none", b)
		}
		// Another backend flag beside it is two backends at once, which
		// checkConfig refuses naming both (SelectedSandboxBackends).
	}
	if m := strings.TrimSpace(l.ModelBackend); m != "" {
		framework := model.NormalizeAgentFramework(m)
		if m == "agy" {
			framework = model.AgentFrameworkAntigravity
		}
		switch framework {
		case model.AgentFrameworkAntigravity, model.AgentFrameworkClaude, model.AgentFrameworkCodex:
		default:
			return cfg, fmt.Errorf("-model-backend=%s: not one of agy, claude, codex", m)
		}
		cfg.AgentFramework = framework
	}
	for _, arg := range l.Repos {
		repo, err := ResolveLocalRepo(arg)
		if err != nil {
			return cfg, fmt.Errorf("-repo %s: %w", arg, err)
		}
		cfg.LocalRepos = append(cfg.LocalRepos, repo)
	}
	if cfg.DefaultTargetRepo == "" && len(cfg.LocalRepos) > 0 {
		cfg.DefaultTargetRepo = cfg.LocalRepos[0].String()
	}
	return cfg, nil
}

// ResolveLocalRepo is the repository arg names: a directory is a local
// checkout, read through its origin remote; anything else is parsed as a
// repository (model.ParseRepo).
func ResolveLocalRepo(arg string) (model.RepoRef, error) {
	arg = strings.TrimSpace(arg)
	if fi, err := os.Stat(arg); err == nil && fi.IsDir() {
		cmd := exec.Command("git", "-C", arg, "remote", "get-url", "origin")
		// Reading a remote needs no credential, so git gets none of the
		// daemon's: only where to find itself and the user's own config.
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
		out, err := cmd.Output()
		if err != nil {
			return model.RepoRef{}, fmt.Errorf("reading the origin remote of the checkout at %s: %w", arg, err)
		}
		arg = strings.TrimSpace(string(out))
	}
	return model.ParseRepo(arg)
}

// loadLocalPolicy reads -policy, if cfg names one, and attaches -repo to
// it (attachLocalRepos). Run calls it once and keeps the answer in
// cfg.policy, for dispatch. A file that cannot be read or parsed stops
// the daemon.
func loadLocalPolicy(ctx context.Context, cfg Config, store *model.Store) (*policy.Policy, error) {
	var p *policy.Policy
	if cfg.PolicyFile != "" {
		var err error
		if p, err = policy.Load(cfg.PolicyFile); err != nil {
			return nil, fmt.Errorf("grain daemon: -policy: %w", err)
		}
	}
	if err := attachLocalRepos(ctx, cfg, store, p); err != nil {
		return nil, fmt.Errorf("grain daemon: -repo: %w", err)
	}
	if p != nil {
		owner := attachSingleTenantOwner(cfg, p)
		log.Printf("grain daemon: enforcing local policy %s (%d repositories allowed; local requesters: %s)%s; "+
			"edits to the file take effect when the daemon restarts",
			cfg.PolicyFile, len(p.Repos), localRequestersLine(p), owner)
	}
	return p, nil
}

// attachSingleTenantOwner adds this single-tenant daemon's own actor to p's
// local_requesters. Every task filed on such a daemon -- in the UI under
// the access code, or from the CLI -- is filed as that actor (-as, else
// the OS account the daemon runs as), and nobody else can approve it:
// without this a policy naming only repos holds every one of the
// owner's own tasks as an unapproved external request. It returns a note
// for the log line saying the name was added, or "".
func attachSingleTenantOwner(cfg Config, p *policy.Policy) string {
	owner := ActorID(cfg.Actor)
	if owner == "" {
		owner = ui.DefaultActor("").ID
	}
	if p.IsLocalRequester(owner) {
		return ""
	}
	p.LocalRequesters = append(p.LocalRequesters, owner)
	return fmt.Sprintf(" -- %s is this single-tenant daemon's owner, whom the UI and CLI file tasks as", owner)
}

func localRequestersLine(p *policy.Policy) string {
	if len(p.LocalRequesters) == 0 {
		return "none"
	}
	return strings.Join(p.LocalRequesters, ", ")
}

// attachLocalRepos makes cfg.LocalRepos this workstation's: each is
// added to the local policy's repos, and to the deployment's target
// repositories when that list restricts them at all (an empty one
// already allows every repository, and adding to it would narrow it).
func attachLocalRepos(ctx context.Context, cfg Config, store *model.Store, p *policy.Policy) error {
	if len(cfg.LocalRepos) == 0 {
		return nil
	}
	for _, r := range cfg.LocalRepos {
		if p != nil && !p.AllowsRepo(r) {
			p.Repos = append(p.Repos, r.String())
		}
	}
	return store.UpdateConfig(ctx, func(c *model.Config) error {
		if len(c.TargetRepos) == 0 {
			return nil
		}
		var missing []string
		for _, r := range cfg.LocalRepos {
			if !model.RepoAllowed(c.TargetRepos, r) {
				missing = append(missing, r.String())
			}
		}
		if len(missing) == 0 {
			return nil
		}
		log.Printf("grain daemon: -repo: adding %s to the deployment's target repositories", strings.Join(missing, ", "))
		c.TargetRepos = append(c.TargetRepos, missing...)
		return nil
	})
}
