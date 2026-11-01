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

package agent_test

import (
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The arguments a forked mcpserver needs before it serves the self-debug
// capability's own tools: the name of the grant, and where grain's own
// source is.
func TestGrantArgsCarriesTheGrantAndTheSourceDir(t *testing.T) {
	args := agent.GrantArgs(agent.RunConfig{
		Grants: []string{"self-debug"}, GrainSourceDir: "/usr/local/share/grain/src",
	})
	want := []string{"-grant", "self-debug", "-grain-src-dir", "/usr/local/share/grain/src"}
	if !slices.Equal(args, want) {
		t.Fatalf("GrantArgs = %v, want %v", args, want)
	}
}

// A deployment with no checkout of grain's source still gets the task
// half of the capability, so the grant is passed on its own rather than
// with an empty directory beside it.
func TestGrantArgsOmitsAnAbsentSourceDir(t *testing.T) {
	args := agent.GrantArgs(agent.RunConfig{Grants: []string{"self-debug"}})
	if want := []string{"-grant", "self-debug"}; !slices.Equal(args, want) {
		t.Fatalf("GrantArgs = %v, want %v", args, want)
	}
}

// Every grant a task holds travels, one -grant pair each -- what makes a
// fourth capability wanting these tools a name at either end and no
// change here. The source directory belongs to self-debug and is written
// beside it, not at the end of the list.
func TestGrantArgsCarriesEveryGrant(t *testing.T) {
	args := agent.GrantArgs(agent.RunConfig{
		Grants:         []string{"self-debug", "bootstrap-playbooks"},
		GrainSourceDir: "/usr/local/share/grain/src",
	})
	want := []string{
		"-grant", "self-debug", "-grain-src-dir", "/usr/local/share/grain/src",
		"-grant", "bootstrap-playbooks",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("GrantArgs = %v, want %v", args, want)
	}
}

// A grant with no source directory to go with it passes on its own, and
// the deployment's own checkout stays out of the arguments of a run that
// was never granted a way to read it.
func TestGrantArgsWithholdsTheSourceDirFromAnotherGrant(t *testing.T) {
	args := agent.GrantArgs(agent.RunConfig{
		Grants: []string{"bootstrap-playbooks"}, GrainSourceDir: "/usr/local/share/grain/src",
	})
	if want := []string{"-grant", "bootstrap-playbooks"}; !slices.Equal(args, want) {
		t.Fatalf("GrantArgs = %v, want %v", args, want)
	}
}

// An ordinary task passes nothing at all: its run's tool roster is
// exactly what it was before this existed, whatever else its dispatch
// happens to know.
func TestGrantArgsIsNothingWithoutAGrant(t *testing.T) {
	if args := agent.GrantArgs(agent.RunConfig{GrainSourceDir: "/src"}); len(args) != 0 {
		t.Fatalf("GrantArgs = %v, want none for a task holding no tool-granting capability", args)
	}
}

// The grain-log grant's own second argument: where the process that will
// serve read_grain_log reads the credentials it redacts out of the
// journal before answering. It is written beside its grant, the way the
// source directory is beside self-debug's, so a process's arguments say
// which grant a directory belongs to.
func TestGrantArgsCarriesTheSecretsDirWithTheLogGrant(t *testing.T) {
	args := agent.GrantArgs(agent.RunConfig{
		Grants: []string{"grain-log"}, GrainSecretsDir: "/var/lib/grain/data/secrets",
	})
	want := []string{"-grant", "grain-log", "-grain-secrets-dir", "/var/lib/grain/data/secrets"}
	if !slices.Equal(args, want) {
		t.Fatalf("GrantArgs = %v, want %v", args, want)
	}
}

// And it stays out of the arguments of a run that was not granted the
// journal, even on a deployment that has one to name. A task holding
// self-debug alone is not a task holding grain-log, and nothing about
// where this host keeps its credentials travels with it.
func TestGrantArgsWithholdsTheSecretsDirFromAnotherGrant(t *testing.T) {
	args := agent.GrantArgs(agent.RunConfig{
		Grants:          []string{"self-debug"},
		GrainSourceDir:  "/usr/local/share/grain/src",
		GrainSecretsDir: "/var/lib/grain/data/secrets",
	})
	want := []string{"-grant", "self-debug", "-grain-src-dir", "/usr/local/share/grain/src"}
	if !slices.Equal(args, want) {
		t.Fatalf("GrantArgs = %v, want %v", args, want)
	}
}

// A task holding both -- the upgrade check -- gets both grants with
// their own arguments, and neither directory attaches to the other's
// grant.
func TestGrantArgsCarriesBothSelfDebugAndTheLogGrant(t *testing.T) {
	args := agent.GrantArgs(agent.RunConfig{
		Grants:          []string{"self-debug", "grain-log"},
		GrainSourceDir:  "/usr/local/share/grain/src",
		GrainSecretsDir: "/var/lib/grain/data/secrets",
	})
	want := []string{
		"-grant", "self-debug", "-grain-src-dir", "/usr/local/share/grain/src",
		"-grant", "grain-log", "-grain-secrets-dir", "/var/lib/grain/data/secrets",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("GrantArgs = %v, want %v", args, want)
	}
}

// Deployment health reads controller and state output only after the same
// redactor as grain-log has loaded this deployment's credential values. It
// therefore needs both controller-side directories on its own: requiring an
// unrelated grain-log grant made a deliberately narrowed Director refuse all
// diagnostic reads even though deployment-health itself was granted.
func TestGrantArgsCarriesStateAndSecretsWithDeploymentHealth(t *testing.T) {
	for _, grant := range []string{"deployment-health", model.GrainHealthGrant} {
		args := agent.GrantArgs(agent.RunConfig{
			Grants:          []string{grant},
			GrainStateDir:   "/var/lib/grain/data/state-repo",
			GrainSecretsDir: "/var/lib/grain/data/secrets",
		})
		want := []string{
			"-grant", grant,
			"-grain-secrets-dir", "/var/lib/grain/data/secrets",
			"-grain-state-dir", "/var/lib/grain/data/state-repo",
		}
		if !slices.Equal(args, want) {
			t.Fatalf("GrantArgs(%s) = %v, want %v", grant, args, want)
		}
	}
}

// Local scripts travel with the grants, so every Framework that forks
// the mcpserver passes them: each declaration as NAME=PATH, and the
// credentials as a file path, never their values.
func TestGrantArgsCarriesLocalScripts(t *testing.T) {
	args := agent.GrantArgs(agent.RunConfig{
		Grants:              []string{"self-debug"},
		LocalScripts:        []agent.LocalScripts{{Name: "ops", Path: "/state/local-scripts/ops/local-scripts.yaml"}},
		LocalScriptsEnvFile: "/tmp/grain-local-scripts-1.json",
	})
	want := []string{"-grant", "self-debug",
		"-local-scripts", "ops=/state/local-scripts/ops/local-scripts.yaml",
		"-local-scripts-env-file", "/tmp/grain-local-scripts-1.json"}
	if !slices.Equal(args, want) {
		t.Fatalf("GrantArgs = %v, want %v", args, want)
	}
	if args := agent.GrantArgs(agent.RunConfig{LocalScriptsEnvFile: "/tmp/x"}); len(args) != 0 {
		t.Fatalf("GrantArgs = %v: want no credentials file without a declaration", args)
	}
}

// A durable judgement task's wake tells its mcpserver to count the tool
// budget; every other run's arguments are unchanged.
func TestGrantArgsCarriesTheToolBudget(t *testing.T) {
	if args := agent.GrantArgs(agent.RunConfig{Grants: []string{"self-debug"}, ToolBudget: true}); !slices.Contains(args, "-tool-budget") {
		t.Errorf("GrantArgs = %v, want -tool-budget", args)
	}
	if args := agent.GrantArgs(agent.RunConfig{Grants: []string{"self-debug"}}); slices.Contains(args, "-tool-budget") {
		t.Errorf("GrantArgs = %v, want no -tool-budget", args)
	}
}
