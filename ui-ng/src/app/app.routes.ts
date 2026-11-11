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
    path: "tasks",
    title: "Tasks · grain",
    loadComponent: () =>
      import("./tasks/tasks-page.component").then((m) => m.TasksPageComponent),
  },
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
  { path: "**", redirectTo: "inbox" },
];
