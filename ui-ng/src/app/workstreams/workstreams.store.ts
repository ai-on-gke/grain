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

import { DOCUMENT } from "@angular/common";
import {
  Injectable,
  NgZone,
  OnDestroy,
  computed,
  effect,
  inject,
  signal,
  untracked,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { Task, TaskDetail, TaskListPage } from "../api/models";
import { InboxStore } from "../inbox/inbox.store";
import { AccessService } from "../services/access.service";
import { TaskFeedStore } from "../services/task-feed.service";
import { type Scope, scopeOf } from "../shared/scope";
import {
  DONE_RETENTION_DAYS,
  type Workstream,
  type WorkstreamNode,
  deriveWorkstreams,
} from "./workstream-model";

export const STARTED_STORAGE_KEY = "grain.ws.started";
export const ACTIVE_POLL_MS = 3000;
export const IDLE_POLL_MS = 15000;
export const CLOSED_PAGE_LIMIT = 100;
export const MAX_CLOSED_PAGES = 4;
const MAX_STARTED_IDS = 64;

function loadStarted(): Set<string> {
  try {
    const raw = localStorage.getItem(STARTED_STORAGE_KEY);
    const parsed = raw ? (JSON.parse(raw) as unknown) : [];
    return new Set(Array.isArray(parsed) ? parsed.map(String) : []);
  } catch {
    return new Set();
  }
}

function saveStarted(ids: ReadonlySet<string>): void {
  try {
    localStorage.setItem(
      STARTED_STORAGE_KEY,
      JSON.stringify([...ids].slice(-MAX_STARTED_IDS)),
    );
  } catch {
    // Storage disabled: keep started IDs in memory only.
  }
}

function errorText(err: unknown): string {
  const e = err as { error?: unknown; message?: string };
  if (e && typeof e.error === "string" && e.error) return e.error;
  if (e && e.error && typeof e.error === "object") {
    const nested = e.error as { error?: string; message?: string };
    if (nested.error) return nested.error;
    if (nested.message) return nested.message;
  }
  return e?.message || "The request failed.";
}

export interface StreamBatchPreview {
  nodes: WorkstreamNode[];
  scope: Scope;
}

export interface BatchResult {
  succeeded: number;
  failed: number;
}

@Injectable({ providedIn: "root" })
export class WorkstreamsStore implements OnDestroy {
  private readonly api = inject(GrainApi);
  private readonly events = inject(GrainEvents);
  private readonly access = inject(AccessService);
  private readonly inbox = inject(InboxStore);
  private readonly feed = inject(TaskFeedStore);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);

  readonly openTasks = this.feed.tasks;
  readonly closedTasks = signal<Task[]>([]);
  readonly extraTasks = signal<Record<string, TaskDetail>>({});
  readonly loaded = signal(false);
  readonly closedLoading = signal(false);
  readonly loadError = signal<string | null>(null);
  readonly actionError = signal<string | null>(null);
  readonly busy = signal<string | null>(null);
  readonly startedIds = signal<ReadonlySet<string>>(loadStarted());
  readonly selectedKey = signal<string | null>(null);

  readonly allTasks = computed<Task[]>(() => {
    const open =
      this.openTasks().length > 0 ? this.openTasks() : this.inbox.tasks();
    const closed = this.closedTasks();
    const extra = this.extraTasks();

    const byId = new Map<string, Task>();
    for (const t of open) {
      if (!t?.id) continue;
      const d = extra[t.id];
      byId.set(
        t.id,
        d ? { ...d, ...t, description: t.description || d.description } : t,
      );
    }
    for (const t of closed) {
      if (!t?.id || byId.has(t.id)) continue;
      const d = extra[t.id];
      byId.set(
        t.id,
        d ? { ...d, ...t, description: t.description || d.description } : t,
      );
    }
    for (const [id, d] of Object.entries(extra)) {
      if (id && !byId.has(id)) byId.set(id, d);
    }
    return [...byId.values()];
  });

  readonly derivation = computed(() =>
    deriveWorkstreams(this.allTasks(), {
      startedIds: this.startedIds(),
      selectedKey: this.selectedKey(),
      viewer: this.access.viewer(),
    }),
  );

  readonly streams = computed(() => this.derivation().streams);
  readonly byKey = computed(() => this.derivation().byKey);
  readonly taskToStream = computed(() => this.derivation().taskToStream);

  readonly allNodesById = computed<ReadonlyMap<string, WorkstreamNode>>(() => {
    const map = new Map<string, WorkstreamNode>();
    for (const s of this.streams()) {
      for (const n of s.nodes) {
        if (!n.virtual && !map.has(n.id)) map.set(n.id, n);
      }
    }
    return map;
  });

  readonly hasLiveStreams = computed(() =>
    this.streams().some(
      (s) =>
        s.state === "active" ||
        s.state === "planning" ||
        s.runningNodes.length > 0,
    ),
  );

  private active = false;
  private pollTimer: ReturnType<typeof setTimeout> | null = null;
  private eventSub: { unsubscribe(): void } | null = null;
  private debounceTimer: ReturnType<typeof setTimeout> | null = null;
  private refreshing: Promise<void> | null = null;
  private queuedRefresh: Promise<void> | null = null;
  private forceClosedPending = false;
  private pollCount = 0;
  private closedFetchedOnce = false;
  private lastSeenRevision = -1;
  private readonly detailInFlight = new Set<string>();
  private readonly onVisibility = (): void => {
    if (!this.active) return;
    if (this.doc.hidden) {
      this.clearPoll();
    } else {
      void this.refresh(true);
      this.scheduleNextPoll();
    }
  };

  constructor() {
    effect(() => {
      const rev = this.feed.revision();
      untracked(() => {
        if (
          this.active &&
          rev > 0 &&
          rev !== this.lastSeenRevision &&
          !this.refreshing
        ) {
          this.lastSeenRevision = rev;
          void this.loadClosedPages();
        }
      });
    });
  }

  ensureLoaded(forceClosed = false): void {
    if (
      !this.refreshing &&
      (!this.loaded() || (forceClosed && !this.closedFetchedOnce))
    ) {
      void this.refresh(forceClosed, true);
    }
  }

  start(): void {
    if (!this.active) {
      this.active = true;
      this.feed.start(false);
      this.feed.acquireFast();
    }
    void this.refresh(!this.closedFetchedOnce);
    if (!this.eventSub) {
      this.eventSub = this.events.events().subscribe((ev) => {
        if (ev.type && !ev.type.startsWith("task.")) {
          this.scheduleEventRefresh();
        }
      });
    }
    this.doc.addEventListener("visibilitychange", this.onVisibility);
    this.scheduleNextPoll();
  }

  stop(): void {
    if (this.active) {
      this.active = false;
      this.feed.releaseFast();
      this.feed.stop(false);
    }
    this.eventSub?.unsubscribe();
    this.eventSub = null;
    if (this.debounceTimer) {
      clearTimeout(this.debounceTimer);
      this.debounceTimer = null;
    }
    this.clearPoll();
    this.doc.removeEventListener("visibilitychange", this.onVisibility);
  }

  ngOnDestroy(): void {
    this.stop();
  }

  private clearPoll(): void {
    if (this.pollTimer) {
      clearTimeout(this.pollTimer);
      this.pollTimer = null;
    }
  }

  private scheduleNextPoll(): void {
    this.clearPoll();
    if (!this.active || this.doc.hidden) return;
    this.zone.runOutsideAngular(() => {
      this.pollTimer = setTimeout(() => {
        this.zone.run(() => {
          void this.loadClosedPages().finally(() => this.scheduleNextPoll());
        });
      }, IDLE_POLL_MS);
    });
  }

  private scheduleEventRefresh(): void {
    if (this.debounceTimer || this.doc.hidden) return;
    const contextOnly = !this.active;
    this.zone.runOutsideAngular(() => {
      this.debounceTimer = setTimeout(() => {
        this.debounceTimer = null;
        this.zone.run(() => void this.refresh(!contextOnly, contextOnly));
      }, 250);
    });
  }

  refresh(forceClosed = false, contextOnly = false): Promise<void> {
    if (forceClosed) this.forceClosedPending = true;
    if (!this.refreshing) {
      const fetchClosed =
        this.forceClosedPending || (!contextOnly && !this.closedFetchedOnce);
      this.forceClosedPending = false;
      this.refreshing = this.load(fetchClosed, contextOnly).finally(() => {
        this.refreshing = null;
      });
      return this.refreshing;
    }
    return (this.queuedRefresh ??= this.refreshing
      .catch(() => undefined)
      .then(() => {
        this.queuedRefresh = null;
        return this.refresh(forceClosed, contextOnly);
      }));
  }

  private hasMissingAncestor(open: readonly Task[]): boolean {
    const ids = new Set(open.map((t) => t.id));
    return open.some((t) => {
      if (t.generatedFrom && !ids.has(t.generatedFrom)) return true;
      if (t.origin?.startsWith("task:") && !ids.has(t.origin.slice(5))) {
        return true;
      }
      return false;
    });
  }

  private async load(fetchClosed: boolean, contextOnly = false): Promise<void> {
    const ok = await this.feed.refresh();
    const rev = this.feed.revision();
    const openChanged = rev !== this.lastSeenRevision;
    const currentOpen = this.openTasks();
    if (ok) {
      this.loadError.set(null);
    } else if (!this.loaded()) {
      this.loadError.set(
        `Could not read workstreams: ${this.feed.loadError() ?? errorText(this.feed.lastError())}`,
      );
    }

    this.pollCount++;
    const needClosed =
      fetchClosed ||
      (contextOnly
        ? !this.closedFetchedOnce && this.hasMissingAncestor(currentOpen)
        : openChanged || !this.closedFetchedOnce || this.pollCount % 5 === 0);
    if (needClosed) {
      this.closedLoading.set(true);
      this.lastSeenRevision = rev;
    }
    this.loaded.set(true);
    if (needClosed) {
      await this.loadClosedPages();
    }
  }

  private async loadClosedPages(): Promise<void> {
    const collected: Task[] = [];
    let cursor = "";
    const cutoffMs = Date.now() - DONE_RETENTION_DAYS * 86_400_000;

    const closedSince = encodeURIComponent(new Date(cutoffMs).toISOString());

    this.closedLoading.set(true);
    try {
      for (let page = 0; page < MAX_CLOSED_PAGES; page++) {
        const q = `state=closed&closedSince=${closedSince}&limit=${CLOSED_PAGE_LIMIT}&cursor=${encodeURIComponent(cursor)}`;
        const res = await this.api.get<Task[] | TaskListPage>(
          "GET /api/tasks",
          undefined,
          q,
        );
        const batch = Array.isArray(res) ? res : (res?.tasks ?? []);
        if (batch.length === 0) break;
        collected.push(...batch);
        this.closedFetchedOnce = true;

        const next = Array.isArray(res) ? "" : (res?.nextCursor ?? "");
        if (!next) break;
        // Stop paging once we have >1 page and reached tasks closed well
        // beyond the 14-day Done window.
        if (page >= 1) {
          const newestInBatch = Math.max(
            ...batch.map(
              (t) =>
                Date.parse(t.closedAt || t.prMergedAt || t.updatedAt || "") ||
                0,
            ),
          );
          if (newestInBatch > 0 && newestInBatch < cutoffMs) break;
        }
        cursor = next;
      }
      if (collected.length > 0 || !this.closedFetchedOnce) {
        this.closedTasks.set(collected);
        this.closedFetchedOnce = true;
      }
    } catch {
      // Closed history failure is non-fatal when open tasks succeeded.
    } finally {
      this.closedLoading.set(false);
    }
  }

  async ensureRootDetail(rootId: string): Promise<void> {
    if (!rootId || rootId.includes("/") || rootId.includes("#")) return;
    if (this.extraTasks()[rootId] || this.detailInFlight.has(rootId)) return;
    const known =
      this.openTasks().find((t) => t.id === rootId) ??
      this.closedTasks().find((t) => t.id === rootId);
    const allowed = known
      ? this.access.canAccessTask("GET /api/tasks/{id}", known.repo ?? null)
      : this.access.can("GET /api/tasks/{id}");
    if (!allowed) return;
    this.detailInFlight.add(rootId);
    try {
      const detail = await this.api.get<TaskDetail>(
        "GET /api/tasks/{id}",
        { id: rootId },
        "comments_limit=1&attempts_limit=1",
      );
      if (detail?.id) {
        this.extraTasks.update((m) => ({ ...m, [detail.id]: detail }));
      }
    } catch {
      // Fall back to list metadata cleanly if single task detail fails.
    } finally {
      this.detailInFlight.delete(rootId);
    }
  }

  recordStartedWorkstream(taskId: string): void {
    const clean = String(taskId || "").trim();
    if (!clean) return;
    this.startedIds.update((prev) => {
      const next = new Set(prev);
      next.add(clean);
      saveStarted(next);
      return next;
    });
  }

  approvePreview(stream: Workstream): StreamBatchPreview {
    const nodes = [...stream.proposedNodes]
      .filter((n) =>
        this.access.canAccessTask(
          "POST /api/tasks/{id}/approve",
          n.task.repo ?? null,
        ),
      )
      .sort((a, b) => Number(a.id) - Number(b.id) || a.id.localeCompare(b.id));
    return {
      nodes,
      scope: scopeOf(
        nodes.map((n) => n.task),
        this.access.viewer(),
      ),
    };
  }

  pausePreview(stream: Workstream): StreamBatchPreview {
    const pool = stream.planning
      ? [stream.root]
      : stream.nodes.filter((n) => !n.virtual && n.id !== stream.root.id);
    // Go Client.Defer strictly accepts proposed or queued (not running/failed/completed).
    const nodes = pool.filter(
      (n) =>
        (n.task.state === "proposed" || n.task.state === "queued") &&
        this.access.canAccessTask(
          "POST /api/tasks/{id}/defer",
          n.task.repo ?? null,
        ),
    );
    return {
      nodes,
      scope: scopeOf(
        nodes.map((n) => n.task),
        this.access.viewer(),
      ),
    };
  }

  resumePreview(stream: Workstream): StreamBatchPreview {
    const pool = stream.planning
      ? [stream.root]
      : stream.nodes.filter((n) => !n.virtual && n.id !== stream.root.id);
    const nodes = pool.filter(
      (n) =>
        n.glyph === "deferred" &&
        this.access.canAccessTask(
          "POST /api/tasks/{id}/undefer",
          n.task.repo ?? null,
        ),
    );
    return {
      nodes,
      scope: scopeOf(
        nodes.map((n) => n.task),
        this.access.viewer(),
      ),
    };
  }

  async approveProposals(stream: Workstream): Promise<BatchResult> {
    const targets = this.approvePreview(stream).nodes;
    return this.runBatch("approve", targets, (n) =>
      this.api.post("POST /api/tasks/{id}/approve", { id: n.id }),
    );
  }

  async pauseStream(stream: Workstream): Promise<BatchResult> {
    const targets = this.pausePreview(stream).nodes;
    return this.runBatch("pause", targets, (n) =>
      this.api.post(
        "POST /api/tasks/{id}/defer",
        { id: n.id },
        { deferredUntil: "" },
      ),
    );
  }

  async resumeStream(stream: Workstream): Promise<BatchResult> {
    const targets = this.resumePreview(stream).nodes;
    return this.runBatch("resume", targets, (n) =>
      this.api.post("POST /api/tasks/{id}/undefer", { id: n.id }),
    );
  }

  private async runBatch(
    action: string,
    targets: readonly WorkstreamNode[],
    call: (n: WorkstreamNode) => Promise<unknown>,
  ): Promise<BatchResult> {
    if (targets.length === 0) return { succeeded: 0, failed: 0 };
    this.busy.set(action);
    this.actionError.set(null);
    try {
      const results = await Promise.allSettled(
        targets.map((n) => Promise.resolve().then(() => call(n))),
      );
      const succeeded = results.filter((r) => r.status === "fulfilled").length;
      const failed = results.length - succeeded;
      if (failed > 0) {
        this.actionError.set(
          `${failed} of ${results.length} task updates failed.`,
        );
      }
      await this.refresh(true);
      return { succeeded, failed };
    } finally {
      this.busy.set(null);
    }
  }
}
