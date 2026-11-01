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
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

func directorTask(reads ...model.RepoRef) model.Task {
	return model.Task{
		ID:    "director-task",
		Reads: reads,
		Tags:  []string{model.ConsoleSessionTag(model.DirectorSessionID)},
	}
}

func TestDirectorStateRepositoryIsCheckedOutReadOnly(t *testing.T) {
	t.Parallel()
	remoteBase := t.TempDir()
	repo := model.RepoRef{Owner: "acme", Name: "grain-home"}
	seedStateRemote(t, remoteBase, repo)
	root := t.TempDir()

	prepared, err := prepareCheckout(context.Background(), mcp.NewSandboxTools(root), remoteBase,
		directorTask(repo), "", setupNotes{}, repo)
	if err != nil {
		t.Fatalf("prepareCheckout: %v", err)
	}
	if prepared.DirectorState != directorStateReady {
		t.Fatalf("state = %q, want ready", prepared.DirectorState)
	}
	if _, err := os.Stat(filepath.Join(root, DirectorStateDir, "tables", "task_template.json")); err != nil {
		t.Fatalf("state checkout: %v", err)
	}
	if got := git(t, filepath.Join(root, DirectorStateDir), "remote", "get-url", "--push", "origin"); got != "grain-read-only" {
		t.Fatalf("push URL = %q, want disabled URL", got)
	}
	prompt := directorStateSection(prepared, directorTask(repo))
	if !strings.Contains(prompt, filepath.Join(root, DirectorStateDir)) ||
		!strings.Contains(prompt, remoteBase+"/OWNER/REPO.git") {
		t.Fatalf("prompt does not document checkout and proxy: %q", prompt)
	}
}

func TestDirectorStartsWithoutAConfiguredStateRepository(t *testing.T) {
	t.Parallel()
	prepared, err := prepareCheckout(context.Background(), mcp.NewSandboxTools(t.TempDir()), "http://proxy.invalid",
		directorTask(), "", setupNotes{}, model.RepoRef{})
	if err != nil {
		t.Fatalf("prepareCheckout: %v", err)
	}
	if prepared.DirectorState != directorStateAbsent {
		t.Fatalf("state = %q, want absent", prepared.DirectorState)
	}
	if got := directorStateSection(prepared, directorTask()); !strings.Contains(got, "no configured state repository") {
		t.Fatalf("prompt = %q", got)
	}
}

func TestDirectorStartsWithoutStateRepositoryReadScope(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "grain-home"}
	prepared, err := prepareCheckout(context.Background(), mcp.NewSandboxTools(t.TempDir()), "http://proxy.invalid",
		directorTask(), "", setupNotes{}, repo)
	if err != nil {
		t.Fatalf("prepareCheckout: %v", err)
	}
	if prepared.DirectorState != directorStateUnauthorized {
		t.Fatalf("state = %q, want unauthorized", prepared.DirectorState)
	}
	if got := directorStateSection(prepared, directorTask()); !strings.Contains(got, "not granted read access") {
		t.Fatalf("prompt = %q", got)
	}
}

func TestDirectorStartsWhenScopedProxyIsUnavailable(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "grain-home"}
	prepared, err := prepareCheckout(context.Background(), mcp.NewSandboxTools(t.TempDir()), "",
		directorTask(repo), "", setupNotes{}, repo)
	if err != nil {
		t.Fatalf("prepareCheckout: %v", err)
	}
	if prepared.DirectorState != directorStateUnavailable {
		t.Fatalf("state = %q, want unavailable", prepared.DirectorState)
	}
	if got := directorStateSection(prepared, directorTask(repo)); !strings.Contains(got, "proxy is unavailable") {
		t.Fatalf("prompt = %q", got)
	}
}

func TestDirectorProxyGuidanceRedactsProvisioningCredential(t *testing.T) {
	t.Parallel()
	const credential = "super-secret"
	base := "http://grain:" + credential + "@172.17.0.1:8080"
	if got := proxyBaseWithoutCredentials(base); got != "http://172.17.0.1:8080" {
		t.Fatalf("sanitized proxy = %q", got)
	}
	prepared := checkout{
		Root:              "/home/debian",
		DirectorState:     directorStateReady,
		DirectorStateRepo: model.RepoRef{Owner: "acme", Name: "grain-home"},
		ProxyBase:         proxyBaseWithoutCredentials(base),
	}
	if prompt := directorStateSection(prepared, directorTask(prepared.DirectorStateRepo)); strings.Contains(prompt, credential) {
		t.Fatalf("prompt exposed credential: %q", prompt)
	}
}

func TestDirectorStateRepositoryIsWritableWhenInWrites(t *testing.T) {
	t.Parallel()
	remoteBase := t.TempDir()
	repo := model.ConfigRepo()
	seedStateRemote(t, remoteBase, repo)
	root := t.TempDir()
	task := directorTask()
	task.Writes = []model.WriteTarget{{Repo: repo}}

	prepared, err := prepareCheckout(context.Background(), mcp.NewSandboxTools(root), remoteBase,
		task, "", setupNotes{}, repo)
	if err != nil {
		t.Fatalf("prepareCheckout: %v", err)
	}
	if prepared.DirectorState != directorStateReady || !prepared.DirectorStateWritable {
		t.Fatalf("state = %q writable = %v, want a ready writable checkout", prepared.DirectorState, prepared.DirectorStateWritable)
	}
	dir := filepath.Join(root, DirectorStateDir)
	if got := git(t, dir, "remote", "get-url", "--push", "origin"); got == "grain-read-only" {
		t.Fatalf("push URL = %q, want the proxy's", got)
	}
	if got := git(t, dir, "config", "user.email"); got == "" {
		t.Fatal("no author identity configured on the writable checkout")
	}
	prompt := directorStateSection(prepared, task)
	if !strings.Contains(prompt, "propose_config_change") || !strings.Contains(prompt, dir) {
		t.Fatalf("prompt does not explain the proposal workflow: %q", prompt)
	}
}
