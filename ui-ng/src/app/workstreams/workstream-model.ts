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

import type { Task } from "../api/models";
import { isMerged, submitBlock } from "../shared/gates";
import { ago, stateLabel } from "../tasks/task-view";

export type InitiatorKind = "user" | "system";
export type SystemKind =
  "schedule" | "suite" | "repair" | "release" | "upgrade" | "import" | "agent";
export type InitiatorSubKind = "manual" | "director" | SystemKind;

export type StreamState =
  "person" | "active" | "blocked" | "parked" | "done" | "planning";

export type NodeCategory =
  "done" | "run" | "person" | "blocked" | "failed" | "queued";

export type NodeGlyphKind =
  | "running"
  | "asked"
  | "merging"
  | "held"
  | "queued"
  | "proposed"
  | "deferred"
  | "merged"
  | "done"
  | "closed"
  | "failed"
  | "blocked";

export type ScopeFilter = "people" | "system" | "all";
export type StateFilter = "any" | "active" | "person" | "blocked" | "done";
export type SortKey = "attn" | "last" | "prog" | "size" | "new";

export const DONE_RETENTION_DAYS = 14;
export const ACTIVITY_DAYS = 9;
export const DIRECTOR_SPLIT_PREFIX = "director:";

export const KLABEL: Record<SystemKind, string> = {
  schedule: "Schedule",
  suite: "Suite run",
  repair: "Base-branch repair",
  release: "Release",
  upgrade: "Upgrade",
  import: "Issue import",
  agent: "Agent-proposed",
};

export const CATWORD: Record<NodeCategory, string> = {
  done: "Done",
  run: "Running",
  person: "Waiting on a person",
  blocked: "Blocked",
  failed: "Failed",
  queued: "Queued",
};

export const CATORDER: readonly NodeCategory[] = [
  "done",
  "run",
  "person",
  "blocked",
  "failed",
  "queued",
];

export interface WorkstreamInitiator {
  kind: InitiatorKind;
  sub: InitiatorSubKind;
  who: string;
  name: string;
  initials: string;
  isYou: boolean;
  // Explains client-side heuristics (Director split, schedule series collapse).
  heuristic?: string;
}

export interface WorkstreamNode {
  task: Task;
  id: string;
  parentId: string | null;
  deps: string[];
  crossStreamDeps: string[];
  foldedReviews: Task[];
  foldedTaskIds: string[];
  category: NodeCategory;
  glyph: NodeGlyphKind;
  waitingOnPerson: boolean;
  prNumber: number | null;
  title: string;
  stateText: string;
  activityText: string;
  createdMs: number;
  updatedMs: number;
  closedMs: number | null;
  virtual?: boolean;
}

export interface ActivityDay {
  key: string;
  label: string;
  created: number;
  done: number;
  isToday: boolean;
}

export interface WorkstreamProgress {
  done: number;
  run: number;
  person: number;
  // Proposals are counted inside `person` and exposed separately for the bar.
  proposed: number;
  blocked: number;
  failed: number;
  queued: number;
  total: number;
  percent: number;
}

export interface Workstream {
  key: string;
  rootId: string;
  root: WorkstreamNode;
  title: string;
  goal: string;
  by: WorkstreamInitiator;
  owner: string;
  repos: string[];
  state: StreamState;
  counts: WorkstreamProgress;
  nodes: WorkstreamNode[];
  byId: ReadonlyMap<string, WorkstreamNode>;
  runningNodes: WorkstreamNode[];
  proposedNodes: WorkstreamNode[];
  waitingNodes: WorkstreamNode[];
  crossStreamCount: number;
  seriesCount?: number;
  seriesRootIds?: string[];
  paused: boolean;
  planning: boolean;
  stale: boolean;
  excludeRootFromProgress: boolean;
  createdMs: number;
  lastActivityMs: number;
  days: ActivityDay[];
}

export interface WorkstreamDerivation {
  streams: Workstream[];
  byKey: ReadonlyMap<string, Workstream>;
  // Maps every task ID (including folded reviews & collapsed schedule runs)
  // to its primary Workstream.
  taskToStream: ReadonlyMap<string, Workstream>;
}

export interface DeriveOptions {
  startedIds?: ReadonlySet<string>;
  selectedKey?: string | null;
  viewer?: string;
  now?: number;
}

export interface WorkstreamFilterQuery {
  scope: ScopeFilter;
  mine: boolean;
  sub: ReadonlySet<SystemKind>;
  state: StateFilter;
  repo: string;
  sort: SortKey;
  query: string;
  selectedKey?: string | null;
}

export interface FilteredWorkstreams {
  visible: Workstream[];
  // Hidden system streams in `people` scope (excludes stale done).
  hiddenSystem: Workstream[];
  promotedCount: number;
}

const STREAM_RANK: Record<StreamState, number> = {
  person: 0,
  planning: 1,
  active: 2,
  blocked: 3,
  parked: 4,
  done: 5,
};

function parseMs(iso?: string | null): number {
  if (!iso) return 0;
  const n = Date.parse(iso);
  return Number.isFinite(n) ? n : 0;
}

export function compareTaskIds(a: string, b: string): number {
  const na = Number(a);
  const nb = Number(b);
  if (Number.isFinite(na) && Number.isFinite(nb) && na !== nb) {
    return na - nb;
  }
  return a.localeCompare(b);
}

function initialsOf(name: string): string {
  const clean = name.trim();
  if (!clean) return "?";
  const parts = clean.split(/[\s@._-]+/).filter(Boolean);
  if (parts.length >= 2) {
    return (parts[0][0] + parts[1][0]).toUpperCase();
  }
  return clean.slice(0, 2).toUpperCase();
}

function isConsoleTask(t: Task | undefined): boolean {
  if (!t) return false;
  return Boolean(t.consoleSession) || t.reason?.reason === "console_session";
}

function isGitHubOrigin(origin: string | undefined): boolean {
  if (!origin) return false;
  return !origin.startsWith("task:") && /#\d+$/.test(origin);
}

function prNumberOf(t: Task): number | null {
  const pr = t.pullRequest;
  if (!pr) return null;
  const raw = typeof pr === "string" ? pr : (pr.url ?? pr["id"] ?? "");
  const m = /(?:#|\/pull\/)(\d+)\b/.exec(String(raw));
  return m ? Number(m[1]) : null;
}

function waitsOnHuman(t: Task): boolean {
  if (isConsoleTask(t)) return false;
  if (t.state === "proposed" || t.state === "awaiting_reply") return true;
  if (
    t.state === "awaiting_submit" &&
    (!t.humanApprovedBy ||
      Boolean(t.reviewQuestions) ||
      Boolean(t.reviewFindings?.length) ||
      Boolean(
        t.humanReviewHold?.some((h) => h.category === "security-findings"),
      ))
  ) {
    return true;
  }
  if (t.state === "closed" || isMerged(t) || Boolean(t.prMergedAt)) {
    return false;
  }
  if (t.mergeQueueState === "waiting_github_approval") return true;
  if (t.mergeQueueStalledAt || t.mergeQueueBlockedAt) return true;
  if (t.humanReviewHold?.length) return true;
  if (t.redBaseBranch && t.pullRequest && t.mergeQueueState !== "merging") {
    return true;
  }
  return Boolean(t.awaitingAccess);
}

function isDoneStateWord(s: string | undefined): boolean {
  return s === "completed" || s === "closed" || s === "merged";
}

function isTaskDone(t: Task): boolean {
  if (isMerged(t) || Boolean(t.prMergedAt) || t.state === "closed") {
    return true;
  }
  // A completed task without a PR did not open a mergeable change, so it is done.
  return t.state === "completed" && !t.pullRequest;
}

export function classifyTaskState(
  t: Task,
  tasksById: ReadonlyMap<string, Task>,
): { category: NodeCategory; glyph: NodeGlyphKind; waitingOnPerson: boolean } {
  if (t.state === "proposed") {
    return { category: "person", glyph: "proposed", waitingOnPerson: true };
  }
  if (t.state === "awaiting_reply") {
    return { category: "person", glyph: "asked", waitingOnPerson: true };
  }
  if (waitsOnHuman(t)) {
    return { category: "person", glyph: "held", waitingOnPerson: true };
  }
  if (isMerged(t) || Boolean(t.prMergedAt)) {
    return { category: "done", glyph: "merged", waitingOnPerson: false };
  }
  if (t.state === "completed" && !t.pullRequest) {
    return { category: "done", glyph: "done", waitingOnPerson: false };
  }
  if (t.state === "closed") {
    return { category: "done", glyph: "closed", waitingOnPerson: false };
  }
  if (t.state === "failed") {
    return { category: "failed", glyph: "failed", waitingOnPerson: false };
  }
  if (
    t.state === "completed" ||
    t.state === "merging" ||
    t.mergeQueueState === "merging" ||
    t.state === "awaiting_submit"
  ) {
    return { category: "run", glyph: "merging", waitingOnPerson: false };
  }
  if (t.state === "running" || t.state === "assigned" || t.state === "setup") {
    return { category: "run", glyph: "running", waitingOnPerson: false };
  }
  if (t.state === "deferred" || Boolean(t.deferredUntil)) {
    return { category: "queued", glyph: "deferred", waitingOnPerson: false };
  }
  const deps = t.dependsOn ?? [];
  const hasUndoneDep = deps.some((depId) => {
    if (depId === t.id) return false;
    const dep = tasksById.get(depId);
    if (dep) return !isTaskDone(dep);
    const state = t.dependsOnStates?.[depId];
    return Boolean(state) && !isDoneStateWord(state);
  });
  if (hasUndoneDep || t.state === "blocked") {
    return { category: "blocked", glyph: "blocked", waitingOnPerson: false };
  }
  return { category: "queued", glyph: "queued", waitingOnPerson: false };
}

function normalizeWho(raw: string | undefined): string {
  return (raw ?? "").trim().replace(/^@/, "").toLowerCase();
}

function initiatorOf(
  root: Task,
  isDirectorSplit: boolean,
  viewer: string,
): WorkstreamInitiator {
  const normViewer = normalizeWho(viewer);
  if (root.grainBaseRepair || root.fixesBaseBranch) {
    return {
      kind: "system",
      sub: "repair",
      who: "merge-queue",
      name: "merge-queue",
      initials: "MQ",
      isYou: false,
    };
  }
  if (
    root.scheduled ||
    root.author === "schedule" ||
    root.authorSource === "schedule"
  ) {
    return {
      kind: "system",
      sub: "schedule",
      who: normalizeWho(root.author) || "schedule",
      name: root.authorLabel || "schedule",
      initials: "SC",
      isYou: false,
    };
  }
  if (root.suiteRun || root.author === "suite") {
    return {
      kind: "system",
      sub: "suite",
      who: "suite",
      name: root.authorLabel || "suite",
      initials: "SU",
      isYou: false,
    };
  }
  if (
    root.imported ||
    root.awaitingImportApproval ||
    isGitHubOrigin(root.origin)
  ) {
    return {
      kind: "system",
      sub: "import",
      who: "issue-import",
      name: root.authorLabel || "issue-import",
      initials: "IM",
      isYou: false,
    };
  }
  if (
    /^\[?release-train\]?/i.test(root.title) ||
    normalizeWho(root.author) === "release-triage"
  ) {
    return {
      kind: "system",
      sub: "release",
      who: normalizeWho(root.author) || "release",
      name: root.authorLabel || root.author || "release",
      initials: "RL",
      isYou: false,
    };
  }
  if (normalizeWho(root.author) === "auto-upgrade") {
    return {
      kind: "system",
      sub: "upgrade",
      who: "auto-upgrade",
      name: root.authorLabel || "auto-upgrade",
      initials: "UP",
      isYou: false,
    };
  }
  if (
    !isDirectorSplit &&
    (root.authorKind === "agent" || root.authorKind === "automation")
  ) {
    const nm = root.authorLabel || root.author || "grain";
    return {
      kind: "system",
      sub: "agent",
      who: normalizeWho(root.author) || "grain",
      name: nm,
      initials: initialsOf(nm),
      isYou: false,
    };
  }

  const rawWho = isDirectorSplit
    ? root.creator || root.owner || root.approvedBy || root.author || ""
    : root.author || root.creator || root.owner || "";
  const who = normalizeWho(rawWho) || "person";
  const display =
    (isDirectorSplit
      ? root.creator || root.owner || root.approvedBy
      : root.authorLabel || root.author || root.creator || root.owner) ||
    "Person";
  const cleanName = display.replace(/\s*\(.*?\)\s*/g, "").trim() || display;
  const isYou = Boolean(normViewer && who === normViewer);
  return {
    kind: "user",
    sub: isDirectorSplit ? "director" : "manual",
    who,
    name: cleanName,
    initials: initialsOf(cleanName),
    isYou,
    ...(isDirectorSplit
      ? { heuristic: "Heuristic: split direct child of Director session" }
      : {}),
  };
}

function scheduleTitleStem(title: string): string {
  let stem = title.trim();
  for (let i = 0; i < 3; i++) {
    const next = stem
      .replace(
        /\s*(?:[·•—|-]\s*)?(?:week\s+\d+|run\s+#?\d+|pass\s+#?\d+|#\d+|\d{4}-\d{2}-\d{2}|(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)\s+\d{1,2})\s*$/i,
        "",
      )
      .trim();
    if (next === stem) break;
    stem = next;
  }
  return (stem || title.trim()).toLowerCase();
}

function buildDayHistogram(
  nodes: readonly WorkstreamNode[],
  now: number,
): ActivityDay[] {
  const MS_DAY = 86_400_000;
  const todayUtc = Math.floor(now / MS_DAY);
  const startDay = todayUtc - (ACTIVITY_DAYS - 1);
  const days: ActivityDay[] = [];
  const months = [
    "Jan",
    "Feb",
    "Mar",
    "Apr",
    "May",
    "Jun",
    "Jul",
    "Aug",
    "Sep",
    "Oct",
    "Nov",
    "Dec",
  ];
  for (let d = startDay; d <= todayUtc; d++) {
    const dt = new Date(d * MS_DAY);
    days.push({
      key: `${d}`,
      label: `${months[dt.getUTCMonth()]} ${dt.getUTCDate()}`,
      created: 0,
      done: 0,
      isToday: d === todayUtc,
    });
  }
  for (const n of nodes) {
    if (n.virtual) continue;
    if (n.createdMs > 0) {
      const idx = Math.floor(n.createdMs / MS_DAY) - startDay;
      if (idx >= 0 && idx < days.length) days[idx].created++;
    }
    const doneMs = n.closedMs ?? parseMs(n.task.prMergedAt);
    if (doneMs > 0 && n.category === "done") {
      const idx = Math.floor(doneMs / MS_DAY) - startDay;
      if (idx >= 0 && idx < days.length) days[idx].done++;
    }
  }
  return days;
}

interface RawCluster {
  key: string;
  rootTask: Task | null;
  tasks: Task[];
  parentMap: Map<string, string | null>;
  isDirectorSplit: boolean;
  excludeRootFromProgress: boolean;
  directorSessionId?: string;
}

function resolveLineage(
  tasks: readonly Task[],
  byTaskId: ReadonlyMap<string, Task>,
): {
  clusters: Map<string, RawCluster>;
  standaloneSchedules: Map<string, Task[]>;
} {
  const nonSession = tasks.filter((t) => !isConsoleTask(t));
  const rawGenParent = new Map<string, string | null>();

  for (const t of nonSession) {
    let p: string | null = null;
    if (
      t.generatedFrom &&
      t.generatedFrom !== t.id &&
      byTaskId.has(t.generatedFrom)
    ) {
      p = t.generatedFrom;
    } else if (t.origin?.startsWith("task:")) {
      const oid = t.origin.slice(5);
      if (oid && oid !== t.id && byTaskId.has(oid)) p = oid;
    }
    rawGenParent.set(t.id, p);
  }

  // Locate each task's stream root and key, breaking at console sessions.
  const clusterForTask = new Map<
    string,
    {
      key: string;
      rootId: string;
      directorSessionId?: string;
      excludeRoot: boolean;
    }
  >();

  for (const t of nonSession) {
    const origin = t.origin?.trim() ?? "";
    if (isGitHubOrigin(origin)) {
      clusterForTask.set(t.id, {
        key: origin,
        rootId: "",
        excludeRoot: false,
      });
      continue;
    }

    // Walk up generatedFrom first to spot Director-session roots cleanly even
    // when children carry `origin: "task:<sessionTaskId>"`. Keep the path so
    // any cycle picks the same canonical lowest-ID root for every member.
    const seen = new Set<string>([t.id]);
    const path: Task[] = [t];
    let cur: Task | null = t;
    let directorSessionId: string | undefined;
    const minIdInCycle = (hitId: string): Task => {
      const idx = path.findIndex((x) => x.id === hitId);
      const cycle = idx >= 0 ? path.slice(idx) : path;
      return [...cycle].sort((a, b) => compareTaskIds(a.id, b.id))[0];
    };
    while (cur) {
      const gen: string | undefined = cur.generatedFrom;
      if (gen && byTaskId.has(gen)) {
        if (seen.has(gen)) {
          cur = minIdInCycle(gen);
          break;
        }
        const up: Task = byTaskId.get(gen)!;
        if (isConsoleTask(up)) {
          directorSessionId = String(up.consoleSession || up.id);
          break;
        }
        seen.add(gen);
        path.push(up);
        cur = up;
        continue;
      }
      // If cur has a `task:<id>` origin, check whether that origin task is a
      // console session or another non-session ancestor.
      if (cur.origin?.startsWith("task:")) {
        const oid = cur.origin.slice(5);
        const origTask = byTaskId.get(oid);
        if (origTask && isConsoleTask(origTask)) {
          directorSessionId = String(origTask.consoleSession || origTask.id);
          break;
        }
        if (oid && oid !== cur.id) {
          if (seen.has(oid)) {
            cur = minIdInCycle(oid);
            break;
          }
          if (origTask) {
            seen.add(oid);
            path.push(origTask);
            cur = origTask;
            continue;
          }
          // Unloaded origin root task: still group cleanly under `task:<oid>`.
          clusterForTask.set(t.id, {
            key: `task:${oid}`,
            rootId: oid,
            excludeRoot: true,
          });
          cur = null;
          break;
        }
      }
      break;
    }

    if (!cur) continue;
    if (isGitHubOrigin(cur.origin)) {
      clusterForTask.set(t.id, {
        key: cur.origin!,
        rootId: "",
        excludeRoot: false,
      });
      continue;
    }
    if (directorSessionId) {
      clusterForTask.set(t.id, {
        key: `${DIRECTOR_SPLIT_PREFIX}${directorSessionId}:${cur.id}`,
        rootId: cur.id,
        directorSessionId,
        excludeRoot: true,
      });
    } else {
      clusterForTask.set(t.id, {
        key: `task:${cur.id}`,
        rootId: cur.id,
        excludeRoot: true,
      });
    }
  }

  const clusters = new Map<string, RawCluster>();
  for (const t of nonSession) {
    const meta = clusterForTask.get(t.id)!;
    let c = clusters.get(meta.key);
    if (!c) {
      const rootTask = meta.rootId ? (byTaskId.get(meta.rootId) ?? null) : null;
      c = {
        key: meta.key,
        rootTask,
        tasks: [],
        parentMap: new Map(),
        isDirectorSplit: Boolean(meta.directorSessionId),
        excludeRootFromProgress: meta.excludeRoot,
        directorSessionId: meta.directorSessionId,
      };
      clusters.set(meta.key, c);
    }
    c.tasks.push(t);
  }

  // Assign stream-local parent IDs cleanly.
  for (const c of clusters.values()) {
    const memberIds = new Set(c.tasks.map((x) => x.id));
    for (const t of c.tasks) {
      if (c.rootTask && t.id === c.rootTask.id) {
        c.parentMap.set(t.id, null);
        continue;
      }
      const p = rawGenParent.get(t.id);
      if (p && p !== t.id && memberIds.has(p)) {
        c.parentMap.set(t.id, p);
      } else if (c.rootTask && t.id !== c.rootTask.id) {
        c.parentMap.set(t.id, c.rootTask.id);
      } else {
        c.parentMap.set(t.id, null);
      }
    }
  }

  // Group single-task scheduled runs by author + normalized title stem so
  // recurring single-task schedules collapse into a workstream series.
  const standaloneSchedules = new Map<string, Task[]>();
  for (const c of clusters.values()) {
    if (c.tasks.length === 1) {
      const only = c.tasks[0];
      if (
        only.scheduled ||
        only.author === "schedule" ||
        only.authorSource === "schedule"
      ) {
        const stem = `${normalizeWho(only.author) || "schedule"}::${scheduleTitleStem(only.title)}`;
        const arr = standaloneSchedules.get(stem) ?? [];
        arr.push(only);
        standaloneSchedules.set(stem, arr);
      }
    }
  }

  return { clusters, standaloneSchedules };
}

function nodeActivityText(t: Task): string {
  if (t.activity) return t.activity;
  if (t.questionSummary) return t.questionSummary;
  const block = submitBlock(t);
  if (block) return block;
  return "";
}

function buildWorkstream(
  c: RawCluster,
  byTaskId: ReadonlyMap<string, Task>,
  viewer: string,
  now: number,
): Workstream {
  const clusterIds = new Set(c.tasks.map((t) => t.id));
  // Ensure we have a single root ID in the cluster.
  let rootTask = c.rootTask;
  let syntheticRoot = false;
  if (!rootTask) {
    const topLevel = c.tasks.filter((t) => c.parentMap.get(t.id) == null);
    if (topLevel.length === 1 && !c.excludeRootFromProgress) {
      // GitHub origin with one top-level tracking task: use it as root.
      rootTask = topLevel[0];
    } else if (topLevel.length === 1 && c.key.startsWith("task:")) {
      rootTask = topLevel[0];
      c.excludeRootFromProgress = false;
    } else {
      syntheticRoot = true;
      const first = topLevel[0] ?? c.tasks[0];
      const fallbackId = c.key.startsWith("task:") ? c.key.slice(5) : c.key;
      rootTask = {
        ...first,
        id: fallbackId,
        title: c.key.startsWith("task:") ? first.title : c.key,
        state: "completed",
      };
      for (const top of topLevel) c.parentMap.set(top.id, rootTask.id);
      c.parentMap.set(rootTask.id, null);
      clusterIds.add(rootTask.id);
    }
  }

  const allRawTasks = syntheticRoot ? [rootTask, ...c.tasks] : [...c.tasks];
  // Build raw children map for folding check.
  const rawChildren = new Map<string, Task[]>();
  for (const t of allRawTasks) {
    const pid = c.parentMap.get(t.id);
    if (pid && pid !== t.id) {
      const list = rawChildren.get(pid) ?? [];
      list.push(t);
      rawChildren.set(pid, list);
    }
  }

  // Fold stacked review/fix children onto their parent if not waiting on a
  // person, parent isn't root (unless non-folded siblings exist), and they
  // have no non-folded children of their own.
  const foldedInto = new Map<string, Task[]>();
  const foldedAway = new Set<string>();
  const classifications = new Map<
    string,
    { category: NodeCategory; glyph: NodeGlyphKind; waitingOnPerson: boolean }
  >();
  for (const t of allRawTasks) {
    classifications.set(t.id, classifyTaskState(t, byTaskId));
  }

  for (const t of allRawTasks) {
    if (t.id === rootTask.id) continue;
    const isReviewOrFix = Boolean(t.stacked || t.review);
    if (!isReviewOrFix) continue;
    const pid = c.parentMap.get(t.id);
    if (!pid || pid === rootTask.id) continue;
    const cls = classifications.get(t.id)!;
    // Never fold active, failed, blocked, queued, or waiting-on-person reviews.
    if (cls.category !== "done" || cls.waitingOnPerson) continue;
    const kids = rawChildren.get(t.id) ?? [];
    if (kids.length > 0) continue;
    const list = foldedInto.get(pid) ?? [];
    list.push(t);
    foldedInto.set(pid, list);
    foldedAway.add(t.id);
  }

  // Traverse top-down from root (avoiding cycles).
  const orderedTasks: Task[] = [];
  const visited = new Set<string>();
  const queue: Task[] = [rootTask];
  visited.add(rootTask.id);
  while (queue.length > 0) {
    const cur = queue.shift()!;
    orderedTasks.push(cur);
    const kids = (rawChildren.get(cur.id) ?? [])
      .filter((k) => !foldedAway.has(k.id) && !visited.has(k.id))
      .sort((a, b) => compareTaskIds(a.id, b.id));
    for (const k of kids) {
      visited.add(k.id);
      queue.push(k);
    }
  }
  // Catch any orphaned cycle members within cluster.
  for (const t of allRawTasks) {
    if (!foldedAway.has(t.id) && !visited.has(t.id)) {
      c.parentMap.set(t.id, rootTask.id);
      orderedTasks.push(t);
    }
  }

  const nodes: WorkstreamNode[] = orderedTasks.map((t) => {
    const cls = classifications.get(t.id)!;
    const folded = foldedInto.get(t.id) ?? [];
    const deps = Array.from(
      new Set((t.dependsOn ?? []).filter((d) => d && d !== t.id)),
    );
    const crossStreamDeps = deps.filter((d) => !clusterIds.has(d));
    const createdMs = parseMs(t.createdAt);
    const closedMs = parseMs(t.closedAt) || parseMs(t.prMergedAt) || null;
    const updatedMs = Math.max(
      createdMs,
      parseMs(t.updatedAt),
      parseMs(t.activityAt),
      closedMs ?? 0,
    );
    return {
      task: t,
      id: t.id,
      parentId:
        t.id === rootTask.id ? null : (c.parentMap.get(t.id) ?? rootTask.id),
      deps,
      crossStreamDeps,
      foldedReviews: folded,
      foldedTaskIds: [t.id, ...folded.map((f) => f.id)],
      category: cls.category,
      glyph: cls.glyph,
      waitingOnPerson: cls.waitingOnPerson,
      prNumber: prNumberOf(t),
      title: t.title || `Task #${t.id}`,
      stateText:
        cls.glyph === "merged"
          ? "Merged"
          : cls.category === "done" && t.state === "completed"
            ? "Done"
            : cls.category === "blocked" && t.state === "queued"
              ? "Blocked"
              : stateLabel(t.state),
      activityText: nodeActivityText(t),
      createdMs,
      updatedMs,
      closedMs,
      ...(syntheticRoot && t.id === rootTask.id ? { virtual: true } : {}),
    };
  });

  const byId = new Map<string, WorkstreamNode>();
  for (const n of nodes) byId.set(n.id, n);
  const root = nodes[0];

  const counted = nodes.filter(
    (n) => !n.virtual && (!c.excludeRootFromProgress || n.id !== root.id),
  );

  const counts: WorkstreamProgress = {
    done: 0,
    run: 0,
    person: 0,
    proposed: 0,
    blocked: 0,
    failed: 0,
    queued: 0,
    total: counted.length,
    percent: 0,
  };
  for (const n of counted) {
    counts[n.category]++;
    if (n.glyph === "proposed") counts.proposed++;
  }
  counts.percent =
    counts.total > 0 ? Math.round((100 * counts.done) / counts.total) : 0;

  const runningNodes = nodes.filter(
    (n) => n.glyph === "running" || n.glyph === "merging",
  );
  const proposedNodes = nodes.filter((n) => n.glyph === "proposed");
  const waitingNodes = nodes.filter((n) => n.waitingOnPerson);
  const crossStreamCount = nodes.reduce(
    (s, n) => s + n.crossStreamDeps.length,
    0,
  );

  const planning =
    counts.total === 0 &&
    !root.virtual &&
    (root.glyph === "proposed" ||
      root.category === "run" ||
      root.glyph === "queued" ||
      root.glyph === "asked" ||
      root.glyph === "held");
  const allNonDoneDeferred =
    counted.length > 0 &&
    counted.filter((n) => n.category !== "done").length > 0 &&
    counted
      .filter((n) => n.category !== "done")
      .every((n) => n.glyph === "deferred");
  const paused =
    allNonDoneDeferred || (counted.length === 0 && root.glyph === "deferred");

  let state: StreamState;
  if (planning) {
    state = root.waitingOnPerson ? "person" : "planning";
  } else if (
    (counts.total > 0 && counts.done === counts.total) ||
    (counts.total === 0 && !root.virtual && root.category === "done")
  ) {
    state = "done";
  } else if (paused) {
    state = "parked";
  } else if (counts.person > 0 || root.waitingOnPerson) {
    state = "person";
  } else if (counts.run > 0 || root.category === "run") {
    state = "active";
  } else if (
    counts.blocked > 0 ||
    counts.failed > 0 ||
    root.category === "blocked" ||
    root.category === "failed"
  ) {
    state = "blocked";
  } else {
    state = "parked";
  }

  const reposSet = new Set<string>();
  for (const n of nodes) {
    if (n.task.repo) reposSet.add(n.task.repo);
    for (const w of n.task.writes ?? []) {
      if (w.repo) reposSet.add(w.repo);
    }
  }
  const createdMs = Math.min(
    ...nodes.map((n) => n.createdMs || Infinity).filter(Number.isFinite),
  );
  const lastActivityMs = Math.max(...nodes.map((n) => n.updatedMs), 0);
  const stale =
    state === "done" &&
    lastActivityMs > 0 &&
    now - lastActivityMs > DONE_RETENTION_DAYS * 86_400_000;

  const owner = rootTask.owner || rootTask.creator || "";
  const goal =
    rootTask.description?.trim() ||
    nodes.find((n) => n.task.description?.trim())?.task.description?.trim() ||
    "";

  return {
    key: c.key,
    rootId: root.id,
    root,
    title: rootTask.title || c.key,
    goal,
    by: initiatorOf(rootTask, c.isDirectorSplit, viewer),
    owner,
    repos: [...reposSet],
    state,
    counts,
    nodes,
    byId,
    runningNodes,
    proposedNodes,
    waitingNodes,
    crossStreamCount,
    paused,
    planning,
    stale,
    excludeRootFromProgress: c.excludeRootFromProgress,
    createdMs: Number.isFinite(createdMs) ? createdMs : lastActivityMs,
    lastActivityMs,
    days: buildDayHistogram(nodes, now),
  };
}

function collapseScheduleSeries(streams: Workstream[]): {
  collapsed: Workstream[];
  aliasKeys: Map<string, Workstream>;
  aliasTasks: Map<string, Workstream>;
} {
  const byStem = new Map<string, Workstream[]>();
  const nonSeries: Workstream[] = [];
  for (const s of streams) {
    if (s.by.sub === "schedule") {
      const k = `${s.by.who}::${scheduleTitleStem(s.title)}`;
      const list = byStem.get(k) ?? [];
      list.push(s);
      byStem.set(k, list);
    } else {
      nonSeries.push(s);
    }
  }

  const collapsed = [...nonSeries];
  const aliasKeys = new Map<string, Workstream>();
  const aliasTasks = new Map<string, Workstream>();

  for (const runs of byStem.values()) {
    const sorted = [...runs].sort(
      (a, b) =>
        b.lastActivityMs - a.lastActivityMs ||
        compareTaskIds(b.rootId, a.rootId),
    );
    const head = sorted[0];
    if (sorted.length > 1) {
      const rep: Workstream = {
        ...head,
        seriesCount: sorted.length,
        seriesRootIds: sorted.map((r) => r.rootId),
        by: {
          ...head.by,
          heuristic: `Heuristic: grouped ${sorted.length} runs by author + title stem`,
        },
      };
      collapsed.push(rep);
      for (const r of sorted) {
        aliasKeys.set(r.key, rep);
        for (const n of r.nodes) {
          for (const tid of n.foldedTaskIds) aliasTasks.set(tid, rep);
        }
      }
    } else {
      collapsed.push(head);
    }
  }
  return { collapsed, aliasKeys, aliasTasks };
}

export function deriveWorkstreams(
  tasks: readonly Task[],
  options: DeriveOptions = {},
): WorkstreamDerivation {
  const viewer = options.viewer ?? "";
  const now = options.now ?? Date.now();
  const startedIds = options.startedIds ?? new Set<string>();
  const selectedKey = options.selectedKey ?? null;

  const byTaskId = new Map<string, Task>();
  for (const t of tasks) {
    if (!t?.id) continue;
    if (!byTaskId.has(t.id)) byTaskId.set(t.id, t);
  }

  const { clusters, standaloneSchedules } = resolveLineage(
    [...byTaskId.values()],
    byTaskId,
  );

  // Build candidate workstreams, keeping any cluster that has >= 2 non-session
  // tasks, OR is explicitly started, OR is currently selected in URL, OR is a
  // recurring single-task schedule series (>= 2 runs with the same stem).
  const candidateStreams: Workstream[] = [];
  for (const c of clusters.values()) {
    const first = c.tasks[0];
    const schedStem =
      first &&
      (first.scheduled ||
        first.author === "schedule" ||
        first.authorSource === "schedule")
        ? `${normalizeWho(first.author) || "schedule"}::${scheduleTitleStem(first.title)}`
        : "";
    const inSchedSeries = Boolean(
      schedStem && (standaloneSchedules.get(schedStem)?.length ?? 0) >= 2,
    );
    const qualifies =
      c.tasks.length >= 2 ||
      Boolean(c.rootTask && startedIds.has(c.rootTask.id)) ||
      (first && startedIds.has(first.id)) ||
      (Boolean(selectedKey) && selectedKey === c.key) ||
      inSchedSeries;
    if (!qualifies) continue;
    candidateStreams.push(buildWorkstream(c, byTaskId, viewer, now));
  }

  const { collapsed, aliasKeys, aliasTasks } =
    collapseScheduleSeries(candidateStreams);

  const byKey = new Map<string, Workstream>();
  const taskToStream = new Map<string, Workstream>();
  for (const s of collapsed) {
    byKey.set(s.key, s);
    for (const n of s.nodes) {
      if (n.virtual) continue;
      for (const tid of n.foldedTaskIds) {
        if (!taskToStream.has(tid)) taskToStream.set(tid, s);
      }
    }
  }
  for (const [k, rep] of aliasKeys.entries()) {
    if (!byKey.has(k)) byKey.set(k, rep);
  }
  for (const [tid, rep] of aliasTasks.entries()) {
    taskToStream.set(tid, rep);
  }

  return { streams: collapsed, byKey, taskToStream };
}

export function sortWorkstreams(
  streams: readonly Workstream[],
  sort: SortKey,
): Workstream[] {
  const list = [...streams];
  list.sort((a, b) => {
    switch (sort) {
      case "last":
        return (
          b.lastActivityMs - a.lastActivityMs ||
          compareTaskIds(b.rootId, a.rootId)
        );
      case "prog": {
        const pa = a.counts.total ? a.counts.done / a.counts.total : 0;
        const pb = b.counts.total ? b.counts.done / b.counts.total : 0;
        return pa - pb || b.lastActivityMs - a.lastActivityMs;
      }
      case "size":
        return (
          b.counts.total - a.counts.total || b.lastActivityMs - a.lastActivityMs
        );
      case "new":
        return b.createdMs - a.createdMs || compareTaskIds(b.rootId, a.rootId);
      case "attn":
      default:
        return (
          STREAM_RANK[a.state] - STREAM_RANK[b.state] ||
          b.counts.person - a.counts.person ||
          b.lastActivityMs - a.lastActivityMs
        );
    }
  });
  return list;
}

export function matchesWorkstreamNonScope(
  s: Workstream,
  q: WorkstreamFilterQuery,
): boolean {
  if (q.state === "any" && s.stale && s.key !== q.selectedKey) return false;
  if (q.mine && !s.by.isYou) return false;
  if (
    q.sub.size > 0 &&
    (s.by.kind !== "system" || !q.sub.has(s.by.sub as SystemKind))
  ) {
    return false;
  }
  if (q.state !== "any") {
    if (
      q.state === "active" &&
      s.state !== "active" &&
      s.state !== "person" &&
      s.state !== "planning"
    ) {
      return false;
    }
    if (q.state === "person" && s.state !== "person") return false;
    if (q.state === "blocked" && s.state !== "blocked") return false;
    if (q.state === "done" && s.state !== "done") return false;
  }
  if (q.repo && !s.repos.includes(q.repo)) return false;
  const needle = q.query.trim().toLowerCase();
  if (needle) {
    const hay = [
      s.key,
      s.title,
      s.goal,
      s.by.name,
      s.owner,
      ...s.repos,
      ...s.nodes.map((n) => `#${n.id} ${n.title}`),
    ]
      .join("\n")
      .toLowerCase();
    if (!hay.includes(needle)) return false;
  }
  return true;
}

export function filterWorkstreams(
  streams: readonly Workstream[],
  q: WorkstreamFilterQuery,
): FilteredWorkstreams {
  const base = streams.filter((s) => matchesWorkstreamNonScope(s, q));
  const hiddenSystem: Workstream[] = [];
  const visible: Workstream[] = [];
  let promotedCount = 0;

  for (const s of base) {
    if (q.scope === "people") {
      if (s.by.kind === "user") {
        visible.push(s);
      } else if (s.state === "person") {
        visible.push(s);
        promotedCount++;
      } else {
        hiddenSystem.push(s);
      }
    } else if (q.scope === "system") {
      if (s.by.kind === "system") visible.push(s);
    } else {
      visible.push(s);
    }
  }

  return {
    visible: sortWorkstreams(visible, q.sort),
    hiddenSystem: sortWorkstreams(hiddenSystem, q.sort),
    promotedCount,
  };
}

export function progressSummaryText(s: Workstream): string {
  if (s.planning) return "Planning first run";
  const parts: string[] = [];
  for (const cat of CATORDER) {
    if (cat === "done") continue;
    const n = s.counts[cat];
    if (!n) continue;
    if (cat === "person" && s.counts.proposed > 0) {
      const other = n - s.counts.proposed;
      if (other > 0)
        parts.push(`${other} waiting on person, ${s.counts.proposed} proposed`);
      else parts.push(`${s.counts.proposed} proposed`);
    } else {
      parts.push(`${n} ${CATWORD[cat].toLowerCase()}`);
    }
  }
  return `${s.counts.done} of ${s.counts.total} done${parts.length ? `; ${parts.join(", ")}` : ""}`;
}

export function lastActivityLabel(s: Workstream, now = Date.now()): string {
  if (!s.lastActivityMs) return "";
  return ago(new Date(s.lastActivityMs).toISOString(), now);
}
