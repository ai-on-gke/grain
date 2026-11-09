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
	"net/http"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestHostingSettingAndLegacyConsoleAlias(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	ctx := context.Background()

	// 1. Default task hosting starts unattended; explicit hosting: "console"
	// and legacy capabilities: ["console"] both store HostingConsole with no
	// "console" grant row.
	plain, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "plain", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Hosting != "unattended" {
		t.Fatalf("plain.Hosting = %q, want unattended", plain.Hosting)
	}

	explicit, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "explicit console", Hosting: "console", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Hosting != "console" {
		t.Fatalf("explicit.Hosting = %q, want console", explicit.Hosting)
	}

	legacyCaps := []string{"console", "github-sandbox"}
	aliased, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "legacy console", Capabilities: &legacyCaps, Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if aliased.Hosting != "console" || !slices.Equal(aliased.Capabilities, []string{model.GitHubSandboxRepoGrant}) {
		t.Fatalf("aliased = (hosting=%q, caps=%v), want (console, [%s])", aliased.Hosting, aliased.Capabilities, model.GitHubSandboxRepoGrant)
	}

	// 2. Updating hosting records TransitionHostingChanged with OldHosting/NewHosting,
	// and SetCapability("console", true/false) toggles hosting as a one-release alias.
	toConsole := "console"
	updated, err := c.UpdateTask(ctx, plain.ID, ui.UpdateTaskRequest{Hosting: &toConsole})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Hosting != "console" {
		t.Fatalf("updated.Hosting = %q, want console", updated.Hosting)
	}
	detail, err := c.GetTask(ctx, plain.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundTransition := false
	for _, tr := range detail.Transitions {
		if tr.State == model.TransitionHostingChanged && tr.OldHosting == "unattended" && tr.NewHosting == "console" {
			foundTransition = true
		}
	}
	if !foundTransition {
		t.Fatalf("transitions = %+v, want hosting_changed unattended -> console", detail.Transitions)
	}

	if err := c.SetCapability(ctx, plain.ID, "console", false); err != nil {
		t.Fatalf("SetCapability detach console alias: %v", err)
	}
	afterDetach, err := c.Task(ctx, plain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterDetach.Hosting != "unattended" {
		t.Fatalf("after detach hosting = %q, want unattended", afterDetach.Hosting)
	}
	if err := c.SetCapability(ctx, plain.ID, "console", true); err != nil {
		t.Fatalf("SetCapability attach console alias: %v", err)
	}
	afterAttach, err := c.Task(ctx, plain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterAttach.Hosting != "console" || len(afterAttach.Capabilities) != 0 {
		t.Fatalf("after attach = (hosting=%q, caps=%v), want (console, [])", afterAttach.Hosting, afterAttach.Capabilities)
	}

	// 3. Deployment and repo defaults compose, and bare "console" in defaultCapabilities
	// folds into defaultHosting without storing "console" in defaultCapabilities.
	if _, err := c.SetRepoDefaultCapabilities(ctx, "acme/widgets", []string{"console"}); err != nil {
		t.Fatal(err)
	}
	repoDefs, err := c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if repoDefs.DefaultHosting != "console" || repoDefs.EffectiveDefaultHosting != "console" || len(repoDefs.DefaultCapabilities) != 0 {
		t.Fatalf("repoDefaults = %+v, want DefaultHosting=console and empty DefaultCapabilities", repoDefs)
	}
	onWidgets, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "on widgets", Repo: "acme/widgets", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if onWidgets.Hosting != "console" {
		t.Fatalf("onWidgets.Hosting = %q, want console from repo default", onWidgets.Hosting)
	}

	// 4. Templates, schedules, and console sessions accept hosting and legacy "console".
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Console Tmpl", Title: "Shell", Capabilities: []string{"console"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Hosting != "console" || len(tmpl.Capabilities) != 0 {
		t.Fatalf("tmpl = (hosting=%q, caps=%v), want (console, [])", tmpl.Hosting, tmpl.Capabilities)
	}
	fromTmpl, err := c.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: tmpl.ID, Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if fromTmpl.Hosting != "console" {
		t.Fatalf("fromTmpl.Hosting = %q, want console", fromTmpl.Hosting)
	}

	sessions, err := c.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		ID: "shell-session", Title: "Shell Session", Capabilities: []string{"console"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var shellSession *ui.ConsoleSessionSetting
	for i := range sessions {
		if sessions[i].ID == "shell-session" {
			shellSession = &sessions[i]
		}
	}
	if shellSession == nil || shellSession.Hosting != "console" || slices.Contains(shellSession.Capabilities, "console") {
		t.Fatalf("shellSession = %+v, want Hosting=console and no console capability", shellSession)
	}

	// 5. No person-only rule: an agent holding api:tasks:write can file a task
	// with hosting: "console" or legacy capabilities: ["console"], and PATCH hosting,
	// without holding any console grant of its own.
	agentGrants := []string{"api:tasks:write"}
	agentTask, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "agent", Capabilities: &agentGrants, Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	agentPrincipal := model.Principal{Kind: model.PrincipalAgent, ID: agentTask.ID}
	rec := doAsPrincipal(t, srv, agentPrincipal, http.MethodPost, "/api/tasks",
		`{"title":"agent console task","capabilities":["console"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("agent POST /api/tasks with legacy console: status %d, body %s", rec.Code, rec.Body.String())
	}
	agentCreated := decode[ui.Task](t, rec)
	if agentCreated.Hosting != "console" {
		t.Fatalf("agentCreated.Hosting = %q, want console", agentCreated.Hosting)
	}
	rec = doAsPrincipal(t, srv, agentPrincipal, http.MethodPatch, "/api/tasks/"+agentCreated.ID,
		`{"hosting":"unattended"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent PATCH hosting: status %d, body %s", rec.Code, rec.Body.String())
	}
}
