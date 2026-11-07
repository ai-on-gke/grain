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

package ui_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/grainserver"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The in-process grain and repo servers have no mcp_servers entry, but
// Settings → Capabilities lists them with their methods, so an operator
// can see who holds a grain#host-exec or repo#write grant (2026-11-02
// walkthrough, finding 6).
func TestInProcessCapabilityServersAreListedWithTheirMethods(t *testing.T) {
	_, hc, _, _ := configTestServer(t, &fakeProber{})
	ctx := context.Background()
	list, err := hc.ListCapabilityServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ui.CapabilityServer{}
	for _, s := range list {
		byID[s.ID] = s
	}
	for id, want := range map[string]struct{ name, method string }{
		grainserver.ID:       {"grain", grainserver.HostExecMethod},
		grainserver.RepoID:   {"repo", grainserver.RepoWriteMethod},
		grainserver.GitHubID: {"github", grainserver.ReadIssuesMethod},
	} {
		s, ok := byID[id]
		if !ok {
			t.Fatalf("%s is not listed: %+v", id, list)
		}
		if s.InProcess == nil || s.InProcess.Name != want.name || s.InProcess.Summary == "" || s.Builtin != nil {
			t.Errorf("%s = %+v, want in-process %s with a summary", id, s, want.name)
		}
		if s.Status == nil || s.Status.Transport != "in-process" || !strings.Contains(strings.Join(s.Status.Methods, " "), want.method) {
			t.Errorf("%s status = %+v, want attached in-process with #%s", id, s.Status, want.method)
		}
	}
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: grainserver.ID, Config: protocol.ServerConfig{Command: "grain"}}); err == nil {
		t.Error("an mcp_servers entry was saved over the in-process grain server")
	}
}

// Default grants are seeded onto every new task, so the deployment's and
// a repository's are holders of what they name.
func TestCapabilityServerUsageListsDefaultGrants(t *testing.T) {
	_, hc, client, _ := configTestServer(t, &fakeProber{})
	ctx := context.Background()
	hostExec := grainserver.HostExecGrant
	if err := client.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.DefaultCapabilities = []string{hostExec}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	repoWrite := grainserver.RepoWriteRef + `={"repo":"acme/other"}`
	if err := client.Store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, DefaultCapabilities: []string{repoWrite}}); err != nil {
		t.Fatal(err)
	}
	usage, err := hc.CapabilityServerUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, u := range usage {
		for _, h := range u.Holders {
			got[u.ID] += h.Kind + ":" + h.ID + ":" + h.Repo + " "
		}
	}
	if got[grainserver.ID] != "default:deployment: " {
		t.Errorf("grain holders = %q, want the deployment defaults", got[grainserver.ID])
	}
	if got[grainserver.RepoID] != "default:acme/widgets:acme/widgets " {
		t.Errorf("repo holders = %q, want acme/widgets' defaults", got[grainserver.RepoID])
	}
}
