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

// The rail's destinations, in the reference design's order (Inbox,
// Tasks, Agents, ... then Settings at the bottom). A destination this app
// has built is a route; one it has not built yet is a plain link into
// the classic UI, so nobody is stranded on a page that does not exist
// here. Moving a page over is changing its `route` from null to a path.
export interface Destination {
  id: string;
  label: string;
  icon: string;
  // The path inside this app (under /next/), or null while the page
  // still lives in the classic UI.
  route: string | null;
  // The classic UI's path for the same destination.
  classic: string;
  // One sentence on what the page is for, shown as its tooltip.
  tip: string;
  // Shown in the phone bottom bar (at most three besides More).
  phone?: boolean;
}

export const DESTINATIONS: readonly Destination[] = [
  {
    id: "inbox",
    label: "Inbox",
    icon: "inbox",
    route: "/inbox",
    classic: "/inbox",
    tip: "Decisions waiting on a person: questions, proposals, held merges and alerts.",
    phone: true,
  },
  {
    id: "tasks",
    label: "Tasks",
    icon: "tasks",
    route: null,
    classic: "/tasks",
    tip: "Every task: the backlog in run order, what is running, merging and done.",
    phone: true,
  },
  {
    id: "sessions",
    label: "Sessions",
    icon: "sessions",
    route: "/sessions",
    classic: "/sessions",
    tip: "Standing conversations with agents, the Director first.",
    phone: true,
  },
  {
    id: "processes",
    label: "Processes",
    icon: "processes",
    route: null,
    classic: "/processes",
    tip: "Schedules, templates, suites and triggers that file work on their own.",
  },
  {
    id: "ci",
    label: "CI",
    icon: "ci",
    route: null,
    classic: "/ci",
    tip: "Grain CI and GitHub checks: what is red, whose it is, and why.",
  },
  {
    id: "telemetry",
    label: "Telemetry",
    icon: "telemetry",
    route: null,
    classic: "/metrics",
    tip: "Throughput, decisions and tokens over time.",
  },
  {
    id: "fleet",
    label: "Fleet",
    icon: "fleet",
    route: null,
    classic: "/system",
    tip: "Sandboxes, host agents and this machine's health.",
  },
];

export const SETTINGS: Destination = {
  id: "settings",
  label: "Settings",
  icon: "settings",
  route: null,
  classic: "/settings",
  tip: "Models, access, repositories and this deployment's configuration.",
};

// The classic UI's create paths, used until this app has its own sheets.
export const CLASSIC_NEW_TASK = "/new";
export const CLASSIC_NEW_SESSION = "/session";

// hrefFor is where a destination's link points: its route under /next/,
// or the classic page.
export function hrefFor(d: Destination): string {
  return d.route ? `/next${d.route}` : d.classic;
}
