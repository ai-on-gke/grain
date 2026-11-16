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

// Per-page steady-state idle HTTP request budgets enforced by
// `ui-ng/e2e/next-request-budget.spec.ts` against a seeded `grain demo`
// (100+ tasks, 12 running agents, open notifications, live session with
// transcript). Keep every ceiling in this file so budgets are easy to audit
// and tune in one place.

export interface PageRequestBudget {
  /** Stable identifier matching `buildMeasurementPlan(seed)` in `request-harness.mjs`. */
  readonly id: string;
  /** Human-readable label shown in CI failure annotations. */
  readonly label: string;
  /** Maximum steady-state requests per minute while the page sits visible and idle. */
  readonly maxRequestsPerMin: number;
  /** Maximum duplicate fetches of the exact same URL within 80% of its nominal poll window. */
  readonly maxDuplicates: number;
  /** When true, zero individual `GET /api/tasks/{id}` reads may fire in steady-state idle. */
  readonly forbidPerRowTaskFetch?: boolean;
}

/** Settle window after initial navigation before steady-state counting starts. */
export const BUDGET_SETTLE_MS = 3_500;

/** Steady-state idle measurement window used in CI (seconds). */
export const BUDGET_IDLE_SAMPLE_SECONDS = 60;

/** Hidden-tab verification window (seconds); hidden tabs must emit 0 requests. */
export const BUDGET_HIDDEN_SAMPLE_SECONDS = 20;

/** Maximum requests allowed across any hidden tab during `BUDGET_HIDDEN_SAMPLE_SECONDS`. */
export const MAX_HIDDEN_TAB_REQUESTS = 0;

/** Maximum ratio of `/next/` steady-state idle req/min to Classic `/` on the same page. */
export const MAX_RATIO_TO_CLASSIC = 1.5;

export const NEXT_IDLE_REQUEST_BUDGETS: readonly PageRequestBudget[] = [
  {
    id: "inbox",
    label: "Inbox (/next/inbox)",
    maxRequestsPerMin: 8,
    maxDuplicates: 0,
    forbidPerRowTaskFetch: true,
  },
  {
    id: "tasks",
    label: "Tasks (/next/tasks)",
    maxRequestsPerMin: 8,
    maxDuplicates: 0,
    forbidPerRowTaskFetch: true,
  },
  {
    id: "agents",
    label: "Agents (/next/agents)",
    maxRequestsPerMin: 8,
    maxDuplicates: 0,
    forbidPerRowTaskFetch: true,
  },
  {
    id: "workstreams",
    label: "Workstreams (/next/workstreams)",
    maxRequestsPerMin: 8,
    maxDuplicates: 0,
    forbidPerRowTaskFetch: true,
  },
  {
    id: "task-detail",
    label: "Task detail (/next/task/{id}?tab=runs)",
    maxRequestsPerMin: 10,
    maxDuplicates: 0,
  },
  {
    id: "sessions",
    label: "Sessions (/next/sessions/director)",
    maxRequestsPerMin: 10,
    maxDuplicates: 0,
    forbidPerRowTaskFetch: true,
  },
  {
    id: "processes",
    label: "Processes (/next/processes)",
    maxRequestsPerMin: 8,
    maxDuplicates: 0,
    forbidPerRowTaskFetch: true,
  },
  {
    id: "ci",
    label: "CI (/next/ci)",
    maxRequestsPerMin: 8,
    maxDuplicates: 0,
    forbidPerRowTaskFetch: true,
  },
  {
    id: "repos",
    label: "Repositories (/next/repos)",
    maxRequestsPerMin: 8,
    maxDuplicates: 0,
    forbidPerRowTaskFetch: true,
  },
  {
    id: "telemetry",
    label: "Telemetry (/next/telemetry)",
    maxRequestsPerMin: 8,
    maxDuplicates: 0,
    forbidPerRowTaskFetch: true,
  },
  {
    id: "context",
    label: "Context (/next/context)",
    maxRequestsPerMin: 8,
    maxDuplicates: 0,
    forbidPerRowTaskFetch: true,
  },
  {
    id: "fleet",
    label: "Fleet (/next/fleet)",
    maxRequestsPerMin: 8,
    maxDuplicates: 0,
    forbidPerRowTaskFetch: true,
  },
  {
    id: "settings",
    label: "Settings (/next/settings)",
    maxRequestsPerMin: 8,
    maxDuplicates: 0,
    forbidPerRowTaskFetch: true,
  },
];
