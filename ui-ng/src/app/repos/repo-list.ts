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

import type { GrainConfig, Task } from "../api/models";

// The Repos list's rows, read the way the classic list reads them
// (ui/src/state.js repoRows): one row per repository this deployment
// knows -- its config repo, every target repo, every repo carrying
// configuration of its own, and, on an unrestricted deployment, every
// repo a task targets. Two spellings of one repository are one row,
// under the first spelling seen (an allowlist entry's where there is
// one). Session tasks are not counted, since the task list a row links
// to hides them.
//
// The counts are of open work only: GET /api/tasks leaves closed tasks
// out, and a closed count that is really "the closed tasks paged in so
// far" would read as a fact it is not. The repository's own page counts
// everything.
export interface RepoRow {
  repo: string;
  total: number;
  counts: Record<string, number>;
  blocked: number;
  // On config.targetRepos.
  configured: boolean;
  // Carries configuration of its own (default capabilities, standing
  // instructions, a setup command, CI capabilities, release settings or
  // declared workflows).
  defaults: boolean;
  configRepo: boolean;
}

export function repoKey(repo: string | undefined | null): string {
  return (repo || "").toLowerCase();
}

const CONFIG_LISTS = [
  "reposWithPromptExtension",
  "reposWithSetupCommand",
  "reposWithCICapabilities",
  "reposWithReleaseSettings",
  "reposWithWorkflows",
] as const;

export function targetReposOf(config: GrainConfig | null): string[] {
  const t = config?.["targetRepos"];
  return Array.isArray(t) ? t.filter((r): r is string => !!r) : [];
}

export function repoRows(
  config: GrainConfig | null,
  tasks: readonly Task[],
): RepoRow[] {
  const targets = targetReposOf(config);
  const restricted = targets.length > 0;

  const withConfig = new Map<string, string>();
  for (const [repo, ids] of Object.entries(
    config?.repoDefaultCapabilities ?? {},
  )) {
    if ((ids ?? []).length > 0) withConfig.set(repoKey(repo), repo);
  }
  for (const list of CONFIG_LISTS) {
    const v = config?.[list];
    if (!Array.isArray(v)) continue;
    for (const repo of v) {
      if (typeof repo === "string" && repo) withConfig.set(repoKey(repo), repo);
    }
  }

  const byKey = new Map<string, RepoRow>();
  const row = (repo: string): RepoRow => {
    const key = repoKey(repo);
    let r = byKey.get(key);
    if (!r) {
      r = {
        repo,
        total: 0,
        counts: {},
        blocked: 0,
        configured: false,
        defaults: withConfig.has(key),
        configRepo: false,
      };
      byKey.set(key, r);
    }
    return r;
  };
  if (config?.configRepo) row(config.configRepo).configRepo = true;
  for (const repo of targets) row(repo).configured = true;
  for (const repo of withConfig.values()) row(repo);
  for (const t of tasks) {
    if (!t.repo || t.consoleSession || t.interactive) continue;
    // A restricted deployment's tasks count towards a row the sources
    // above put up and never raise one: a repo off the allowlist is not
    // somewhere work can be filed any more.
    const r = restricted ? byKey.get(repoKey(t.repo)) : row(t.repo);
    if (!r) continue;
    r.total++;
    r.counts[t.state] = (r.counts[t.state] ?? 0) + 1;
    if (t.blocked) r.blocked++;
  }
  return [...byKey.values()].sort(byName);
}

// The order the count chips follow, the task list's own.
const STATE_ORDER = [
  "proposed",
  "queued",
  "running",
  "awaiting_reply",
  "awaiting_submit",
  "failed",
  "completed",
  "deferred",
] as const;

export function stateCounts(
  counts: Record<string, number>,
): { state: string; count: number }[] {
  const known = STATE_ORDER.filter((s) => counts[s]);
  const rest = Object.keys(counts).filter(
    (s) => counts[s] && !(STATE_ORDER as readonly string[]).includes(s),
  );
  return [...known, ...rest].map((state) => ({ state, count: counts[state] }));
}

export type ActivityKey = "running" | "open" | "idle";

export interface Activity {
  key: ActivityKey;
  label: string;
  title: string;
}

// repoActivity is the dot ahead of a row's name: something running,
// open work none of it moving, or nothing open (classic repoActivity).
export function repoActivity(r: RepoRow): Activity {
  const running = r.counts["running"] ?? 0;
  if (running > 0) {
    return {
      key: "running",
      label: "Active",
      title: `Active: ${running} task${running === 1 ? "" : "s"} running now`,
    };
  }
  const open = r.total - (r.counts["deferred"] ?? 0);
  if (open > 0) {
    return {
      key: "open",
      label: "Open",
      title: `${open} open task${open === 1 ? "" : "s"}, none running right now`,
    };
  }
  return {
    key: "idle",
    label: "Idle",
    title:
      r.total > 0
        ? "Idle: every open task here is deferred"
        : "Idle: no open tasks",
  };
}

export const ACTIVITY_RANK: Record<ActivityKey, number> = {
  running: 0,
  open: 1,
  idle: 2,
};

export type RepoSort = "custom" | "name" | "active" | "tasks";

export const REPO_SORTS: readonly { value: RepoSort; label: string }[] = [
  { value: "custom", label: "Custom order" },
  { value: "name", label: "Name (A–Z)" },
  { value: "active", label: "Active first" },
  { value: "tasks", label: "Most tasks" },
];

export function isRepoSort(v: unknown): v is RepoSort {
  return REPO_SORTS.some((s) => s.value === v);
}

function byName(a: RepoRow, b: RepoRow): number {
  return a.repo.localeCompare(b.repo);
}

// sortRows orders the rows for one sort. "custom" is the order this
// browser stored (a drag or a keyboard move), with rows it has never
// placed after every row it has, alphabetically -- so a repo added this
// morning arrives at the bottom rather than in the middle.
export function sortRows(
  rows: readonly RepoRow[],
  sort: RepoSort,
  order: readonly string[],
): RepoRow[] {
  const out = [...rows];
  switch (sort) {
    case "name":
      return out.sort(byName);
    case "active":
      return out.sort(
        (a, b) =>
          ACTIVITY_RANK[repoActivity(a).key] -
            ACTIVITY_RANK[repoActivity(b).key] || byName(a, b),
      );
    case "tasks":
      return out.sort((a, b) => b.total - a.total || byName(a, b));
    default: {
      const pos = new Map(order.map((id, i) => [repoKey(id), i]));
      return out.sort((a, b) => {
        const pa = pos.get(repoKey(a.repo));
        const pb = pos.get(repoKey(b.repo));
        if (pa !== undefined && pb !== undefined) return pa - pb;
        if (pa !== undefined) return -1;
        if (pb !== undefined) return 1;
        return byName(a, b);
      });
    }
  }
}

// moveRow is the custom order after moving `repo` to index `to` of the
// whole list (`rows` in its current custom order).
export function moveRow(
  rows: readonly RepoRow[],
  repo: string,
  to: number,
): string[] {
  const ids = rows.map((r) => r.repo);
  const from = ids.findIndex((id) => repoKey(id) === repoKey(repo));
  if (from < 0) return ids;
  const [id] = ids.splice(from, 1);
  ids.splice(Math.max(0, Math.min(ids.length, to)), 0, id);
  return ids;
}

// The same key the classic list keeps its order under
// (ui/src/listOrder.js), so an order dragged there holds here too.
export const REPO_ORDER_KEY = "grain.list-order.repos";

export function loadRepoOrder(storage: Storage | null): string[] {
  try {
    const raw = storage?.getItem(REPO_ORDER_KEY);
    if (!raw) return [];
    const ids = JSON.parse(raw) as unknown;
    return Array.isArray(ids)
      ? ids.filter((id): id is string => typeof id === "string")
      : [];
  } catch {
    return [];
  }
}

export function saveRepoOrder(
  storage: Storage | null,
  ids: readonly string[],
): void {
  try {
    if (ids.length === 0) storage?.removeItem(REPO_ORDER_KEY);
    else storage?.setItem(REPO_ORDER_KEY, JSON.stringify(ids));
  } catch {
    // A browser that will not store it keeps the order until reload.
  }
}

// repoHostOf is the git host a typed repo names, lowercased, or "" for
// a bare owner/name on the deployment's default host (classic
// RepoList.jsx repoHostOf, model.ParseRepo's host rule).
export function repoHostOf(text: string): string {
  let spec = (text || "").trim();
  if (spec.includes("://")) {
    spec = spec.slice(spec.indexOf("://") + 3);
    const authority = spec.split("/")[0];
    return authority.slice(authority.lastIndexOf("@") + 1).toLowerCase();
  }
  const at = spec.indexOf("@");
  if (at >= 0 && spec.indexOf(":", at) >= 0) {
    return spec.slice(at + 1, spec.indexOf(":", at)).toLowerCase();
  }
  const [first, ...rest] = spec.split("/");
  if (rest.length > 0 && /[.:]/.test(first)) return first.toLowerCase();
  return "";
}

export interface ReachConnection {
  credential?: string;
  host?: string;
  ok?: boolean;
  error?: string;
}

export interface Reach {
  loading?: boolean;
  requestError?: string;
  available?: boolean;
  reason?: string;
  repos?: { repo?: string }[];
  connections?: ReachConnection[];
}

export interface DiscoveryLine {
  kind: "loading" | "found" | "none" | "error";
  text: string;
  reason: string;
  failures: string[];
}

// discoveryLine is the line under the Add repo field: what
// GET /api/deployment/reachable-repos found and from how many Git
// connections, or why it found nothing (classic discoveryStatus).
export function discoveryLine(reach: Reach): DiscoveryLine {
  if (reach.loading) {
    return {
      kind: "loading",
      text: "Discovering repositories…",
      reason: "",
      failures: [],
    };
  }
  if (reach.requestError) {
    return {
      kind: "error",
      text: `Couldn't discover repositories: ${reach.requestError}`,
      reason: "",
      failures: [],
    };
  }
  const conns = reach.connections ?? [];
  const failures = conns
    .filter((c) => !c.ok)
    .map((c) => `${c.credential} on ${c.host}: ${c.error || "not listed"}`);
  const repos = reach.repos ?? [];
  if (reach.available && repos.length > 0) {
    const listed = conns.filter((c) => c.ok).length || 1;
    return {
      kind: "found",
      text:
        `${repos.length} repo${repos.length === 1 ? "" : "s"} discovered from ` +
        `${listed} Git connection${listed === 1 ? "" : "s"}`,
      reason: "",
      failures,
    };
  }
  if (!reach.available && conns.some((c) => !c.ok && c.error)) {
    return {
      kind: "error",
      text: "Couldn't discover repositories from the deployment's Git connections",
      reason: "",
      failures,
    };
  }
  return {
    kind: "none",
    text: "No repositories discovered",
    reason: reach.available ? "" : reach.reason || "",
    failures,
  };
}

// firstAddWarning is the question asked before the first repo goes on
// an empty allowlist -- which means unrestricted, so the add narrows the
// deployment to that one repo -- or null when the add can only widen.
export function firstAddWarning(
  targets: readonly string[],
  rows: readonly RepoRow[],
  repo: string,
): string | null {
  if (targets.length > 0) return null;
  const falling = rows
    .map((r) => r.repo)
    .filter((r) => repoKey(r) !== repoKey(repo));
  let msg =
    "This deployment doesn't restrict target repos today (an empty allowlist means " +
    `unrestricted), so adding ${repo} narrows it to that one repo rather than widening anything.`;
  if (falling.length > 0) {
    msg +=
      ` Off the allowlist as of this click: ${falling.join(", ")}. ` +
      "Tasks already filed against them keep their target, but each one is parked instead of " +
      "dispatched until the repo is added back here.";
  }
  return msg;
}
