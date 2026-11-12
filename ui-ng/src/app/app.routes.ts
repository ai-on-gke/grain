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

import type { Routes } from "@angular/router";

// Every page is lazy, so the shell's own bundle stays inside the budget
// (angular.json) however many pages move over. A path this app does not
// have yet goes to the Inbox; the shell's links take the reader to the
// classic UI for everything else.
export const routes: Routes = [
  { path: "", pathMatch: "full", redirectTo: "inbox" },
  {
    path: "inbox",
    title: "Inbox · grain",
    loadComponent: () =>
      import("./inbox/inbox-page.component").then((m) => m.InboxPageComponent),
  },
  {
    path: "workstreams",
    title: "Workstreams · grain",
    loadComponent: () =>
      import("./workstreams/workstreams-page.component").then(
        (m) => m.WorkstreamsPageComponent,
      ),
  },
  {
    path: "workstreams/:key",
    title: "Workstream · grain",
    loadComponent: () =>
      import("./workstreams/workstreams-page.component").then(
        (m) => m.WorkstreamsPageComponent,
      ),
  },
  { path: "topology", redirectTo: "/workstreams", pathMatch: "prefix" },
  {
    path: "tasks",
    title: "Tasks · grain",
    loadComponent: () =>
      import("./tasks/tasks-page.component").then((m) => m.TasksPageComponent),
  },
  {
    path: "agents",
    title: "Agents · grain",
    loadComponent: () =>
      import("./agents/agents-page.component").then(
        (m) => m.AgentsPageComponent,
      ),
  },
  {
    path: "task/:id",
    title: "Task · grain",
    loadComponent: () =>
      import("./task/task-page.component").then((m) => m.TaskPageComponent),
  },
  { path: "tasks/:id", redirectTo: "task/:id" },
  { path: "director", redirectTo: "sessions/director" },
  {
    path: "sessions",
    title: "Sessions · grain",
    loadComponent: () =>
      import("./sessions/sessions-page.component").then(
        (m) => m.SessionsPageComponent,
      ),
  },
  {
    path: "sessions/:id",
    title: "Sessions · grain",
    loadComponent: () =>
      import("./sessions/sessions-page.component").then(
        (m) => m.SessionsPageComponent,
      ),
  },
  {
    path: "ci",
    title: "CI · grain",
    loadComponent: () =>
      import("./ci/ci-page.component").then((m) => m.CIPageComponent),
  },
  {
    path: "ci/runs/:id",
    title: "CI run · grain",
    loadComponent: () =>
      import("./ci/ci-page.component").then((m) => m.CIPageComponent),
  },
  {
    path: "processes",
    title: "Processes · grain",
    loadComponent: () =>
      import("./processes/processes-page.component").then(
        (m) => m.ProcessesPageComponent,
      ),
  },
  {
    path: "processes/:tab",
    title: "Processes · grain",
    loadComponent: () =>
      import("./processes/processes-page.component").then(
        (m) => m.ProcessesPageComponent,
      ),
  },
  { path: "schedules", redirectTo: "processes/schedules", pathMatch: "full" },
  { path: "templates", redirectTo: "processes/templates", pathMatch: "full" },
  { path: "suites", redirectTo: "processes/suites", pathMatch: "full" },
  {
    path: "telemetry",
    title: "Telemetry · grain",
    loadComponent: () =>
      import("./telemetry/telemetry-page.component").then(
        (m) => m.TelemetryPageComponent,
      ),
  },
  { path: "metrics", redirectTo: "telemetry" },
  {
    path: "fleet",
    title: "Fleet · grain",
    loadComponent: () =>
      import("./fleet/fleet-page.component").then((m) => m.FleetPageComponent),
  },
  { path: "system", redirectTo: "fleet" },
  { path: "staging", redirectTo: "fleet" },
  {
    path: "context",
    title: "Context · grain",
    loadComponent: () =>
      import("./context/context-page.component").then(
        (m) => m.ContextPageComponent,
      ),
  },
  { path: "memory", redirectTo: "context" },
  { path: "config", redirectTo: "settings/config-repo", pathMatch: "full" },
  {
    path: "settings",
    title: "Settings · grain",
    loadComponent: () =>
      import("./settings/settings-page.component").then(
        (m) => m.SettingsPageComponent,
      ),
  },
  {
    path: "settings/:section",
    title: "Settings · grain",
    loadComponent: () =>
      import("./settings/settings-page.component").then(
        (m) => m.SettingsPageComponent,
      ),
  },
  {
    matcher: (s) =>
      s.length === 4 && s[0].path === "repos" && /[.:]/.test(s[1].path)
        ? {
            consumed: s,
            posParams: { host: s[1], owner: s[2], name: s[3] },
          }
        : null,
    title: "Repository · grain",
    loadComponent: () =>
      import("./repos/repo-detail-page.component").then(
        (m) => m.RepoDetailPageComponent,
      ),
  },
  {
    path: "repos/:owner/:name",
    title: "Repository · grain",
    loadComponent: () =>
      import("./repos/repo-detail-page.component").then(
        (m) => m.RepoDetailPageComponent,
      ),
  },
  {
    path: "repos/:owner/:name/:tab",
    title: "Repository · grain",
    loadComponent: () =>
      import("./repos/repo-detail-page.component").then(
        (m) => m.RepoDetailPageComponent,
      ),
  },
  {
    path: "repos/:host/:owner/:name",
    title: "Repository · grain",
    loadComponent: () =>
      import("./repos/repo-detail-page.component").then(
        (m) => m.RepoDetailPageComponent,
      ),
  },
  {
    path: "repos/:host/:owner/:name/:tab",
    title: "Repository · grain",
    loadComponent: () =>
      import("./repos/repo-detail-page.component").then(
        (m) => m.RepoDetailPageComponent,
      ),
  },
  { path: "**", redirectTo: "inbox" },
];
