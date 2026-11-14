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

import { inject } from "@angular/core";
import {
  type RedirectFunction,
  Router,
  type Routes,
  type UrlMatchResult,
  UrlSegment,
} from "@angular/router";

function workstreamMultiSegmentMatcher(
  segments: UrlSegment[],
): UrlMatchResult | null {
  if (segments.length < 3 || segments[0]?.path !== "workstreams") return null;
  const fullKey = segments
    .slice(1)
    .map((s) => s.path)
    .join("/");
  return {
    consumed: segments,
    posParams: { key: new UrlSegment(fullKey, {}) },
  };
}

// Where the classic UI's System tabs (/system/<tab>) and its older
// addresses live in this app: a bookmark or a link pasted out of the
// classic UI lands on the page that has the same content rather than
// falling through to the Inbox. Each entry is the path and, for Fleet,
// the ?tab= that shows it.
const SYSTEM_TABS: Record<string, readonly [string, string?]> = {
  health: ["/fleet"],
  sandboxes: ["/fleet"],
  top: ["/fleet"],
  logs: ["/fleet", "logs"],
  pipeline: ["/fleet", "controls"],
  power: ["/fleet", "controls"],
  caches: ["/settings/caches"],
  metrics: ["/telemetry"],
  upgrade: ["/settings/upgrade"],
};

// legacy redirects to the page SYSTEM_TABS names for `tab`, keeping the
// query the old link carried (a /system/metrics?repo=... narrowing, say).
function legacy(tab: string): RedirectFunction {
  return ({ queryParams }) => {
    const [path, fleetTab] = SYSTEM_TABS[tab] ?? ["/fleet"];
    return inject(Router).createUrlTree([path], {
      queryParams: fleetTab ? { ...queryParams, tab: fleetTab } : queryParams,
    });
  };
}

function classicRepoSettingsMatcher(s: UrlSegment[]): UrlMatchResult | null {
  if (
    (s.length !== 4 && s.length !== 5) ||
    s[0]?.path !== "repos" ||
    s[3]?.path !== "settings" ||
    /[.:]/.test(s[1].path)
  ) {
    return null;
  }
  return {
    consumed: s,
    posParams: {
      owner: s[1],
      name: s[2],
      ...(s[4] ? { tab: s[4] } : {}),
    },
  };
}

// repoLeaveGuard asks the repository page before leaving it with an
// unsaved edit (RepoDetailPageComponent.canLeave). It is duck-typed so
// the page stays lazy: importing the component here would pull it into
// the shell's bundle.
const repoLeaveGuard = (page: { canLeave?: () => Promise<boolean> } | null) =>
  page?.canLeave ? page.canLeave() : true;

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
  {
    matcher: workstreamMultiSegmentMatcher,
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
  { path: "system", redirectTo: "fleet", pathMatch: "full" },
  {
    path: "system/:tab",
    redirectTo: (route) => legacy(String(route.params["tab"]))(route),
  },
  { path: "admin/logs", redirectTo: legacy("logs") },
  { path: "sandboxes", redirectTo: legacy("sandboxes"), pathMatch: "full" },
  { path: "caches", redirectTo: legacy("caches"), pathMatch: "full" },
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
  // The classic UI keeps a repository's tab at
  // /repos/<owner>/<name>/settings/<tab>; pasted under /next/ that read
  // as host "owner", owner "name", repo "settings" and loaded a repo
  // that does not exist (2026-11-14 repository walkthrough). Send it to
  // /repos/<owner>/<name>/<tab>, the address this app uses.
  {
    matcher: classicRepoSettingsMatcher,
    redirectTo: ({ params }) => {
      const tab = params["tab"];
      return tab && tab !== "overview"
        ? `/repos/${params["owner"]}/${params["name"]}/${tab}`
        : `/repos/${params["owner"]}/${params["name"]}`;
    },
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
    canDeactivate: [repoLeaveGuard],
  },
  {
    path: "repos/:owner/:name",
    title: "Repository · grain",
    loadComponent: () =>
      import("./repos/repo-detail-page.component").then(
        (m) => m.RepoDetailPageComponent,
      ),
    canDeactivate: [repoLeaveGuard],
  },
  {
    path: "repos/:owner/:name/:tab",
    title: "Repository · grain",
    loadComponent: () =>
      import("./repos/repo-detail-page.component").then(
        (m) => m.RepoDetailPageComponent,
      ),
    canDeactivate: [repoLeaveGuard],
  },
  {
    path: "repos/:host/:owner/:name",
    title: "Repository · grain",
    loadComponent: () =>
      import("./repos/repo-detail-page.component").then(
        (m) => m.RepoDetailPageComponent,
      ),
    canDeactivate: [repoLeaveGuard],
  },
  {
    path: "repos/:host/:owner/:name/:tab",
    title: "Repository · grain",
    loadComponent: () =>
      import("./repos/repo-detail-page.component").then(
        (m) => m.RepoDetailPageComponent,
      ),
    canDeactivate: [repoLeaveGuard],
  },
  { path: "**", redirectTo: "inbox" },
];
