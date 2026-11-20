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
import { Injectable, NgZone, computed, inject, signal } from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type {
  AgentModel,
  AgentModelsResponse,
  AttachmentUpload,
  CapabilityOption,
  Comment,
  ConsoleSession,
  ConsoleSessionList,
  GrainConfig,
  Task,
  TaskDetail,
} from "../api/models";
import type { GrainPlanBlock } from "../director/director-draft";
import { AccessService } from "../services/access.service";
import { topologicalPlanOrder } from "./session-artifacts";
import {
  DIRECTOR_ID,
  errorText,
  isStaleEdit,
  lastLine,
  sessionStatus,
} from "./session-helpers";

// POLL_MS refreshes active standing sessions with ETags so a reply shows up
// promptly even if the SSE stream drops.
export const POLL_MS = 2500;
export const MAX_POLL_BACKOFF_MS = 15000;
export const TRANSCRIPT_POLL_MS = 1500;
export const MAX_TRANSCRIPT_BACKOFF_MS = 10000;
export const COMMENTS_PAGE_SIZE = 50;
export const COMMENTS_QUERY = `comments_limit=${COMMENTS_PAGE_SIZE}&attempts_limit=20`;
const BG_FAIL_COOLDOWN_MS = 10000;

interface OptimisticSend {
  comment: Comment;
  afterMaxId: number;
}

function mergeComments(older: Comment[], latest: Comment[]): Comment[] {
  if (!older.length) return latest;
  const byId = new Map<number, Comment>();
  for (const c of older) byId.set(c.id, c);
  for (const c of latest) byId.set(c.id, c);
  return [...byId.values()].sort((a, b) => a.id - b.id);
}

function reconcilePending(
  pending: readonly OptimisticSend[],
  serverComments: readonly Comment[],
): OptimisticSend[] {
  if (!pending.length) return [];
  const consumed = new Set<number>();
  return pending.filter((p) => {
    const idx = serverComments.findIndex(
      (c, i) =>
        !consumed.has(i) &&
        (c.authorKind === "human" || !c.authorKind) &&
        c.id > p.afterMaxId &&
        (c.body ?? "").trim() === (p.comment.body ?? "").trim(),
    );
    if (idx >= 0) {
      consumed.add(idx);
      return false;
    }
    return true;
  });
}

function rowKey(t: Task | undefined): string {
  return t ? `${t.state}|${t.updatedAt ?? ""}|${t.activityAt ?? ""}` : "";
}

export function liveSessionRunKey(
  task: Task | null | undefined,
  detail: TaskDetail | null | undefined,
): string {
  if (!task || task.state !== "running" || task.activityBySetup) return "";
  const open = (detail?.attempts ?? []).find((a) => !a.finishedAt);
  return open ? `attempt-${open.number}` : "live";
}

export interface SessionRow {
  session: ConsoleSession;
  task: Task | null;
  line: string;
}

// PendingSend is a message in a session's outbox: shown in the chat as
// sending, queued or, with failed set, not sent.
export interface PendingSend {
  key: number;
  body: string;
  attachments: AttachmentUpload[];
  failed?: string;
}

export interface CreateSessionOptions {
  title?: string;
  message?: string;
  team?: string;
  model?: string;
  networkAccess?: string;
  capabilities?: string[];
  attachments?: AttachmentUpload[];
}

export interface UpdateSessionPatch {
  title?: string;
  team?: string;
  model?: string;
  networkAccess?: string;
  tasksApproved?: boolean;
  keepRunning?: boolean;
  readsAll?: boolean;
  adoptShippedDefaults?: boolean;
  reads?: string[];
  writes?: { repo: string; base?: string }[];
}

const RUN_FIXED_KEYS: (keyof UpdateSessionPatch)[] = [
  "model",
  "networkAccess",
  "adoptShippedDefaults",
  "reads",
  "writes",
];

@Injectable({ providedIn: "root" })
export class SessionsStore {
  private readonly api = inject(GrainApi);
  private readonly events = inject(GrainEvents);
  private readonly access = inject(AccessService);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);

  readonly sessions = signal<ConsoleSession[]>([]);
  readonly offered = signal<CapabilityOption[]>([]);
  readonly networkOptions = signal<string[]>(["open", "none"]);
  readonly models = signal<string[]>([]);
  readonly agentModels = signal<AgentModel[]>([]);
  readonly agentModelsError = signal<string | null>(null);
  readonly defaultModel = signal<string>("");
  readonly tasks = signal<Record<string, Task>>({});
  readonly details = signal<Record<string, TaskDetail>>({});
  readonly selectedId = signal<string>(DIRECTOR_ID);
  readonly loaded = signal(false);
  readonly loadError = signal<string | null>(null);
  readonly actionError = signal<string | null>(null);
  readonly busy = signal<ReadonlySet<string>>(new Set());
  readonly detailError = signal<string | null>(null);
  readonly drafts = signal<Record<string, string>>({});
  readonly stagedAttachments = signal<Record<string, AttachmentUpload[]>>({});
  // Messages sent from a session's composer that the server has not yet
  // taken, oldest first (send).
  readonly outbox = signal<Record<string, readonly PendingSend[]>>({});
  private pendingSeq = 0;
  private readonly draining = new Set<string>();
  // Tracks sessions whose running turn predates a run-fixed edit (grants, model, network).
  readonly staleRuns = signal<Record<string, string>>({});
  // Optimistic outgoing comments shown immediately before POST or polling settles.
  private readonly pendingSends = signal<Record<string, OptimisticSend[]>>({});
  private optimisticSeq = 0;

  // Per-task older comment pages preserved across live refreshes.
  private readonly pagedComments = new Map<string, Comment[]>();
  private readonly pagedOlderComments = new Map<string, number>();

  readonly rows = computed<SessionRow[]>(() => {
    const tasks = this.tasks();
    const details = this.details();
    const pending = this.pendingSends();
    const list = [...this.sessions()].sort(
      (a, b) => Number(b.id === DIRECTOR_ID) - Number(a.id === DIRECTOR_ID),
    );
    return list.map((session) => {
      const tid = session.taskId;
      const d = tid ? details[tid] : undefined;
      const t = tid ? tasks[tid] : undefined;
      const task = d || t ? ({ ...d, ...t } as Task) : null;
      const pend = tid ? (pending[tid] ?? []) : [];
      const allComments = pend.length
        ? [
            ...(d?.comments ?? []),
            ...reconcilePending(pend, d?.comments ?? []).map((p) => p.comment),
          ]
        : d?.comments;
      return {
        session,
        task,
        line: lastLine(allComments) || task?.activity || "",
      };
    });
  });

  readonly selected = computed<SessionRow | null>(() => {
    const id = this.selectedId();
    return this.rows().find((r) => r.session.id === id) ?? null;
  });

  readonly selectedDetail = computed<TaskDetail | null>(() => {
    const tid = this.selected()?.session.taskId;
    return tid ? (this.details()[tid] ?? null) : null;
  });

  readonly selectedComments = computed<Comment[]>(() =>
    this.commentsForTask(this.selected()?.session.taskId),
  );

  readonly directorRow = computed<SessionRow | null>(() => {
    return this.rows().find((r) => r.session.id === DIRECTOR_ID) ?? null;
  });

  readonly directorDetail = computed<TaskDetail | null>(() => {
    const tid = this.directorRow()?.session.taskId;
    return tid ? (this.details()[tid] ?? null) : null;
  });

  readonly directorComments = computed<Comment[]>(() =>
    this.commentsForTask(this.directorRow()?.session.taskId),
  );

  readonly directorLiveStatus = computed(() =>
    sessionStatus(this.directorRow()?.task),
  );

  readonly directorTranscript = signal<string | null>(null);

  readonly status = computed(() => sessionStatus(this.selected()?.task));

  readonly currentLiveKey = computed(() =>
    liveSessionRunKey(this.selected()?.task, this.selectedDetail()),
  );

  // True when a run-fixed setting/grant was saved while the current turn was already running.
  readonly needsApplyNow = computed(() => {
    const sid = this.selectedId();
    const recorded = this.staleRuns()[sid];
    if (!recorded) return false;
    const live = this.currentLiveKey();
    if (!live) return false;
    return recorded === live || recorded === "live" || live === "live";
  });

  private readonly fetchedFor = new Map<string, string>();
  private readonly bgFailedAt = new Map<string, number>();
  private configLoaded = false;
  private configInFlight: Promise<void> | null = null;

  private timer: ReturnType<typeof setTimeout> | null = null;
  private transcriptTimer: ReturnType<typeof setTimeout> | null = null;
  private transcriptKey = "";
  private transcriptFailures = 0;
  private pollFailures = 0;
  private lastLoadHadError = false;
  private readonly retainers = new Set<string>();
  private readonly directorWatchers = new Set<string>();
  private eventSub: { unsubscribe(): void } | null = null;
  private visibilityBound = false;
  private refreshing: Promise<void> | null = null;
  private pending = false;

  private readonly onVisibility = (): void => {
    if (this.retainers.size === 0) return;
    if (this.doc.hidden) {
      if (this.timer) {
        clearTimeout(this.timer);
        this.timer = null;
      }
      if (this.transcriptTimer) {
        clearTimeout(this.transcriptTimer);
        this.transcriptTimer = null;
      }
      return;
    }
    this.pollFailures = 0;
    this.transcriptFailures = 0;
    void this.refresh();
    this.schedulePoll(POLL_MS);
    this.syncDirectorTranscript();
  };

  retain(token: string): void {
    this.retainers.add(token);
    this.start();
  }

  release(token: string): void {
    this.retainers.delete(token);
    if (this.retainers.size === 0) {
      this.stopTimers();
    }
  }

  setDirectorWatch(token: string, on: boolean): void {
    if (on) {
      this.directorWatchers.add(token);
      this.retain(`watch:${token}`);
      this.syncDirectorTranscript();
    } else {
      this.directorWatchers.delete(token);
      if (this.directorWatchers.size === 0) {
        this.stopDirectorTranscript();
      }
      this.release(`watch:${token}`);
    }
  }

  start(): void {
    void this.refresh();
    if (!this.configLoaded) {
      this.configLoaded = true;
      void this.loadConfigModels();
    }
    if (!this.visibilityBound) {
      this.visibilityBound = true;
      this.zone.runOutsideAngular(() => {
        this.doc.addEventListener("visibilitychange", this.onVisibility);
      });
    }
    if (!this.eventSub) {
      this.eventSub = this.events.events().subscribe(() => {
        this.pollFailures = 0;
        void this.refresh();
      });
    }
    if (!this.timer && !this.doc.hidden) {
      this.schedulePoll(POLL_MS);
    }
  }

  private schedulePoll(delayMs: number): void {
    if (this.retainers.size === 0 || this.doc.hidden) return;
    if (this.timer) {
      clearTimeout(this.timer);
      this.timer = null;
    }
    this.zone.runOutsideAngular(() => {
      this.timer = setTimeout(() => {
        this.timer = null;
        if (this.retainers.size === 0 || this.doc.hidden) return;
        this.zone.run(() => void this.refresh());
      }, delayMs);
    });
  }

  private ensureConfig(force = false): Promise<void> {
    if (typeof this.api.config !== "function") {
      return Promise.resolve();
    }
    if (!force && this.access.configLoaded()) {
      return Promise.resolve();
    }
    return (this.configInFlight ??= Promise.resolve()
      .then(() => this.api.config() as Promise<GrainConfig | null>)
      .then((cfg) => {
        if (cfg) {
          if (!this.access.configLoaded()) {
            this.access.setConfig(cfg);
          }
          if (Array.isArray(cfg.models)) this.models.set(cfg.models);
          if (typeof cfg.defaultModel === "string") {
            this.defaultModel.set(cfg.defaultModel);
          }
        }
      })
      .catch(() => {
        // Optional model list read.
      })
      .finally(() => {
        this.configInFlight = null;
      }));
  }

  private async loadConfigModels(): Promise<void> {
    await this.ensureConfig(true);
    if (
      typeof this.api.get !== "function" ||
      !this.access.canGlobal("GET /api/agent-models")
    ) {
      return;
    }
    try {
      const am = await this.api.get<AgentModelsResponse>(
        "GET /api/agent-models",
      );
      if (am?.available && Array.isArray(am.models)) {
        this.agentModels.set(am.models);
      }
      this.agentModelsError.set(am?.error || null);
    } catch {
      // Fall back to config().models cleanly when unavailable.
    }
  }

  stop(): void {
    if (this.retainers.size > 0) return;
    this.stopTimers();
  }

  private stopTimers(): void {
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
    this.stopDirectorTranscript();
    this.eventSub?.unsubscribe();
    this.eventSub = null;
    if (this.visibilityBound) {
      this.visibilityBound = false;
      this.doc.removeEventListener("visibilitychange", this.onVisibility);
    }
  }

  select(id: string): void {
    if (this.selectedId() === id) return;
    this.selectedId.set(id);
    this.actionError.set(null);
    this.pollFailures = 0;
    void this.refresh();
  }

  refresh(): Promise<void> {
    if (this.refreshing) {
      this.pending = true;
      return this.refreshing;
    }
    this.refreshing = this.load().finally(() => {
      this.refreshing = null;
      const delay =
        this.pollFailures > 0
          ? Math.min(
              POLL_MS * 2 ** Math.min(this.pollFailures, 3),
              MAX_POLL_BACKOFF_MS,
            )
          : POLL_MS;
      if (this.pending) {
        this.pending = false;
        if (!this.lastLoadHadError) {
          void this.refresh();
          return;
        }
      }
      if (!this.timer && this.retainers.size > 0 && !this.doc.hidden) {
        this.schedulePoll(delay);
      }
    });
    return this.refreshing;
  }

  private async load(): Promise<void> {
    if (
      typeof this.api.get !== "function" ||
      typeof this.api.getIfChanged !== "function"
    ) {
      this.loaded.set(true);
      return;
    }
    if (!this.access.configLoaded()) {
      await this.ensureConfig();
    }
    // The session list is deployment-wide: a role holding sessions:read
    // only on some repositories is refused it.
    if (this.access.repoLimited("GET /api/console-sessions")) {
      this.loaded.set(true);
      return;
    }
    // Hydrate already-known active session(s) concurrently with list/tasks so
    // slow /api/tasks or /api/console-sessions responses do not delay messages.
    const preSel = this.selected()?.session.taskId;
    const preDir =
      this.directorWatchers.size > 0
        ? this.directorRow()?.session.taskId
        : undefined;
    const preIds = [
      ...new Set([preSel, preDir].filter((x): x is string => !!x)),
    ];
    const preReads = new Map<string, Promise<boolean>>();
    const preTasks = this.tasks();
    for (const id of preIds) {
      preReads.set(
        id,
        this.fetchActiveDetail(id, rowKey(preTasks[id]), id === preSel),
      );
    }

    const [list, tasks] = await Promise.allSettled([
      this.api.get<ConsoleSessionList>("GET /api/console-sessions"),
      this.api.getIfChanged<Task[]>("GET /api/tasks"),
    ]);
    let hadErr = false;
    if (list.status === "fulfilled") {
      this.applyList(list.value);
      this.loadError.set(null);
    } else {
      hadErr = true;
      this.loadError.set(
        `Sessions could not be read: ${errorText(list.reason)}. This page tries again on its own.`,
      );
    }
    if (
      tasks.status === "fulfilled" &&
      (tasks.value.changed || Object.keys(this.tasks()).length === 0)
    ) {
      const map: Record<string, Task> = {};
      for (const t of tasks.value.body ?? []) map[t.id] = t;
      this.tasks.set(map);
    } else if (tasks.status === "rejected") {
      hadErr = true;
    }
    this.loaded.set(true);
    const detailOk = await this.loadDetails(preReads);
    if (!detailOk) hadErr = true;
    this.lastLoadHadError = hadErr;
    this.pollFailures = hadErr ? this.pollFailures + 1 : 0;
    this.pruneFinishedStaleRuns();
    this.syncDirectorTranscript();
  }

  private pruneFinishedStaleRuns(): void {
    const stale = this.staleRuns();
    const keys = Object.keys(stale);
    if (!keys.length) return;
    let changed = false;
    const next = { ...stale };
    for (const sid of keys) {
      const row = this.rows().find((r) => r.session.id === sid);
      const detail = row?.session.taskId
        ? this.details()[row.session.taskId]
        : undefined;
      const live = liveSessionRunKey(row?.task, detail);
      if (
        !live ||
        (stale[sid] !== "live" && live !== "live" && stale[sid] !== live)
      ) {
        delete next[sid];
        changed = true;
      }
    }
    if (changed) this.staleRuns.set(next);
  }

  private markLiveRunStale(sessionId: string): void {
    const row = this.rows().find((r) => r.session.id === sessionId);
    const detail = row?.session.taskId
      ? this.details()[row.session.taskId]
      : undefined;
    const live = liveSessionRunKey(row?.task, detail);
    if (live) {
      this.staleRuns.update((m) => ({ ...m, [sessionId]: live }));
    }
  }

  private applyList(list: ConsoleSessionList): void {
    this.sessions.set(list.sessions ?? []);
    if (list.capabilities) this.offered.set(list.capabilities);
    if (list.networkAccessOptions?.length) {
      this.networkOptions.set(list.networkAccessOptions);
    }
  }

  private commentsForTask(taskId: string | undefined): Comment[] {
    if (!taskId) return [];
    const base = this.details()[taskId]?.comments ?? [];
    const pend = this.pendingSends()[taskId] ?? [];
    if (!pend.length) return base;
    const remaining = reconcilePending(pend, base);
    if (!remaining.length) return base;
    return [...base, ...remaining.map((p) => p.comment)];
  }

  private applyPagedMerge(id: string, fresh: TaskDetail): TaskDetail {
    const paged = this.pagedComments.get(id);
    if (!paged?.length) return fresh;
    // Once older history is loaded, every comment seen stays: each poll
    // answers only the newest page, so a window that slides forward would
    // otherwise drop the comments between the paged history and it.
    const all = mergeComments(
      paged,
      (fresh.comments ?? []).filter((c) => c.id > 0),
    );
    this.pagedComments.set(id, all);
    return {
      ...fresh,
      comments: mergeComments(all, fresh.comments ?? []),
      olderComments:
        this.pagedOlderComments.get(id) ?? fresh.olderComments ?? 0,
    };
  }

  private applyServerDetail(id: string, raw: TaskDetail): void {
    const merged = this.applyPagedMerge(id, raw);
    this.details.update((m) => ({ ...m, [id]: merged }));
    const curPend = this.pendingSends()[id];
    if (curPend?.length) {
      const nextPend = reconcilePending(curPend, merged.comments ?? []);
      if (nextPend.length !== curPend.length) {
        this.pendingSends.update((m) => {
          const copy = { ...m };
          if (nextPend.length) copy[id] = nextPend;
          else delete copy[id];
          return copy;
        });
      }
    }
  }

  private async fetchActiveDetail(
    id: string,
    key: string,
    isSelected: boolean,
  ): Promise<boolean> {
    try {
      await this.loadDetail(id, key);
      if (isSelected) this.detailError.set(null);
      return true;
    } catch (err) {
      if (isSelected) {
        this.detailError.set(
          `This conversation could not be read: ${errorText(err)}`,
        );
      }
      return false;
    }
  }

  private async loadDetails(
    preReads: Map<string, Promise<boolean>> = new Map(),
  ): Promise<boolean> {
    const sel = this.selected()?.session.taskId;
    const dir =
      this.directorWatchers.size > 0
        ? this.directorRow()?.session.taskId
        : undefined;
    const tasks = this.tasks();
    const activeIds = [...new Set([sel, dir].filter((x): x is string => !!x))];
    const activeResults = await Promise.all(
      activeIds.map(
        (id) =>
          preReads.get(id) ??
          this.fetchActiveDetail(id, rowKey(tasks[id]), id === sel),
      ),
    );

    const now = Date.now();
    const activeSet = new Set<string>([...activeIds, ...preReads.keys()]);
    const bgIds = this.sessions()
      .map((s) => s.taskId)
      .filter((t): t is string => {
        if (!t || activeSet.has(t)) return false;
        const rk = rowKey(tasks[t]);
        if (this.fetchedFor.get(t) === rk) return false;
        const failedAt = this.bgFailedAt.get(t);
        return !failedAt || now - failedAt >= BG_FAIL_COOLDOWN_MS;
      });

    if (bgIds.length > 0) {
      let cursor = 0;
      const workerCount = Math.min(2, bgIds.length);
      await Promise.all(
        Array.from({ length: workerCount }, async () => {
          while (cursor < bgIds.length) {
            const id = bgIds[cursor++];
            try {
              await this.loadDetail(id, rowKey(tasks[id]));
              this.bgFailedAt.delete(id);
            } catch {
              this.bgFailedAt.set(id, Date.now());
            }
          }
        }),
      );
    }

    return activeResults.every(Boolean);
  }

  private async loadDetail(id: string, key: string): Promise<void> {
    const r = await this.api.getIfChanged<TaskDetail>(
      "GET /api/tasks/{id}",
      { id },
      COMMENTS_QUERY,
    );
    this.fetchedFor.set(id, key);
    if (r.changed || !this.details()[id]) {
      this.applyServerDetail(id, r.body);
    }
  }

  olderCommentsFor(taskId?: string): number {
    const id = taskId ?? this.selected()?.session.taskId;
    if (!id) return 0;
    return this.details()[id]?.olderComments ?? 0;
  }

  isLoadingOlder(taskId?: string): boolean {
    const id = taskId ?? this.selected()?.session.taskId;
    return !!id && this.isBusy(`older:${id}`);
  }

  async loadOlderComments(taskId?: string): Promise<void> {
    const id = taskId ?? this.selected()?.session.taskId;
    const cur = id ? this.details()[id] : undefined;
    const first = (cur?.comments ?? []).find((c) => c.id > 0);
    const busyKey = `older:${id ?? ""}`;
    if (!id || !cur || !first || this.isBusy(busyKey)) return;
    this.addBusy(busyKey);
    try {
      const page = await this.api.get<TaskDetail>(
        "GET /api/tasks/{id}",
        { id },
        `comments_limit=${COMMENTS_PAGE_SIZE}&comments_before=${encodeURIComponent(String(first.id))}&attempts_limit=1`,
      );
      const live = (this.details()[id]?.comments ?? []).filter((c) => c.id > 0);
      this.pagedComments.set(
        id,
        mergeComments(
          page.comments ?? [],
          mergeComments(this.pagedComments.get(id) ?? [], live),
        ),
      );
      this.pagedOlderComments.set(id, page.olderComments ?? 0);
      const latest = this.details()[id];
      if (latest) {
        this.applyServerDetail(id, latest);
      }
    } catch (err) {
      this.actionError.set(errorText(err));
    } finally {
      this.removeBusy(busyKey);
    }
  }

  isBusy(name: string): boolean {
    return this.busy().has(name);
  }

  private addBusy(name: string): void {
    this.busy.update((b) => new Set(b).add(name));
  }

  private removeBusy(name: string): void {
    this.busy.update((b) => {
      const next = new Set(b);
      next.delete(name);
      return next;
    });
  }

  private removePendingSend(taskId: string, tempId: number): void {
    this.pendingSends.update((m) => {
      const cur = m[taskId];
      if (!cur) return m;
      const next = cur.filter((p) => p.comment.id !== tempId);
      const copy = { ...m };
      if (next.length) copy[taskId] = next;
      else delete copy[taskId];
      return copy;
    });
  }

  draftFor(id: string): string {
    return this.drafts()[id] ?? "";
  }

  setDraft(id: string, text: string): void {
    this.drafts.update((m) => ({ ...m, [id]: text }));
  }

  attachmentsFor(id: string): AttachmentUpload[] {
    return this.stagedAttachments()[id] ?? [];
  }

  setAttachments(id: string, list: AttachmentUpload[]): void {
    this.stagedAttachments.update((m) => ({ ...m, [id]: list }));
  }

  outboxFor(id: string): readonly PendingSend[] {
    return this.outbox()[id] ?? [];
  }

  // send queues the selected session's draft and posts it: the message
  // shows at once as a pending bubble and the composer is cleared, so a
  // second Enter while the first is still in flight queues behind it
  // rather than being dropped. Queued messages post in order; one that
  // fails stays put, marked, holding the ones behind it, until it is sent
  // again (retry) or taken back into the composer (edit).
  send(): boolean {
    const id = this.selectedId();
    const typed = this.draftFor(id);
    const attachments = this.attachmentsFor(id);
    const body = typed.trim();
    const row = this.rows().find((r) => r.session.id === id);
    if (!row?.session.taskId || (!body && attachments.length === 0)) {
      return false;
    }
    const pending: PendingSend = { key: ++this.pendingSeq, body, attachments };
    this.outbox.update((m) => ({ ...m, [id]: [...(m[id] ?? []), pending] }));
    this.setDraft(id, "");
    this.setAttachments(id, []);
    void this.drainOutbox(id);
    return true;
  }

  retrySend(id: string, key: number): void {
    this.patchPending(id, key, { failed: undefined });
    void this.drainOutbox(id);
  }

  // editSend takes a message that failed to send back out of the queue
  // and into the composer, ahead of whatever is typed there now.
  editSend(id: string, key: number): void {
    const p = this.outboxFor(id).find((x) => x.key === key);
    if (!p) return;
    this.dropPending(id, key);
    const typed = this.draftFor(id);
    this.setDraft(id, typed.trim() ? `${p.body}\n\n${typed}` : p.body);
    this.setAttachments(id, [...p.attachments, ...this.attachmentsFor(id)]);
    void this.drainOutbox(id);
  }

  private async drainOutbox(id: string): Promise<void> {
    if (this.draining.has(id)) return;
    this.draining.add(id);
    try {
      for (;;) {
        const next = this.outboxFor(id)[0];
        if (!next || next.failed) return;
        const taskId = this.rows().find((r) => r.session.id === id)?.session
          .taskId;
        if (!taskId) {
          this.patchPending(id, next.key, {
            failed: "This session has no conversation to send to.",
          });
          return;
        }
        try {
          await this.api.post(
            "POST /api/tasks/{id}/comments",
            { id: taskId },
            { body: next.body, attachments: next.attachments },
          );
        } catch (err) {
          this.patchPending(id, next.key, { failed: errorText(err) });
          return;
        }
        // Read the conversation back before the bubble goes, so the sent
        // message replaces it rather than leaving a gap between the two. A
        // read already in flight may predate the post, so the one queued
        // behind it is waited for too.
        await this.refresh();
        if (this.refreshing) await this.refreshing;
        this.dropPending(id, next.key);
      }
    } finally {
      this.draining.delete(id);
    }
  }

  private patchPending(id: string, key: number, patch: Partial<PendingSend>) {
    this.outbox.update((m) => ({
      ...m,
      [id]: (m[id] ?? []).map((p) => (p.key === key ? { ...p, ...patch } : p)),
    }));
  }

  private dropPending(id: string, key: number): void {
    this.outbox.update((m) => ({
      ...m,
      [id]: (m[id] ?? []).filter((p) => p.key !== key),
    }));
  }

  async sendTo(
    sessionId: string,
    rawBody: string,
    attachments: AttachmentUpload[] = [],
    clearSessionDraft = false,
  ): Promise<boolean> {
    const row = this.rows().find((r) => r.session.id === sessionId);
    const taskId = row?.session.taskId;
    const body = rawBody.trim();
    if (
      !row ||
      !taskId ||
      (!body && attachments.length === 0) ||
      this.isBusy("send")
    ) {
      return false;
    }
    const existing = this.details()[taskId]?.comments ?? [];
    const afterMaxId = existing.reduce(
      (max, c) => (c.id > max ? c.id : max),
      0,
    );
    const tempId = -++this.optimisticSeq;
    const optimistic: Comment = {
      id: tempId,
      author: this.access.viewer() || "human",
      authorKind: "human",
      authorLabel: this.access.identityLabel() || "You",
      body,
      createdAt: new Date().toISOString(),
      ...(attachments.length
        ? {
            attachments: attachments.map((a, idx) => ({
              id: tempId * 100 - idx,
              filename: a.filename,
              contentType: a.contentType,
            })),
          }
        : {}),
    };
    this.addBusy("send");
    this.actionError.set(null);
    this.pendingSends.update((m) => ({
      ...m,
      [taskId]: [...(m[taskId] ?? []), { comment: optimistic, afterMaxId }],
    }));
    if (clearSessionDraft) {
      this.setDraft(sessionId, "");
      this.setAttachments(sessionId, []);
    }
    try {
      const res = await this.api.post<Partial<TaskDetail>>(
        "POST /api/tasks/{id}/comments",
        { id: taskId },
        { body, attachments },
      );
      if (res && Array.isArray(res.comments)) {
        const total = res.comments.length;
        const newest =
          total > COMMENTS_PAGE_SIZE
            ? res.comments.slice(-COMMENTS_PAGE_SIZE)
            : res.comments;
        const older = Math.max(res.olderComments ?? 0, total - newest.length);
        this.applyServerDetail(taskId, {
          ...(this.details()[taskId] ?? {
            id: taskId,
            title: row.session.title,
          }),
          ...res,
          id: taskId,
          comments: newest,
          olderComments: older,
        });
      }
      void this.refresh();
      return true;
    } catch (err) {
      this.removePendingSend(taskId, tempId);
      if (clearSessionDraft) {
        if (!this.draftFor(sessionId)) {
          this.setDraft(sessionId, rawBody);
        }
        if (
          this.attachmentsFor(sessionId).length === 0 &&
          attachments.length > 0
        ) {
          this.setAttachments(sessionId, attachments);
        }
      }
      this.actionError.set(errorText(err));
      return false;
    } finally {
      this.removeBusy("send");
    }
  }

  async stopTurn(): Promise<boolean> {
    return this.stopTurnFor(this.selectedId());
  }

  async stopTurnFor(id: string): Promise<boolean> {
    const ok = await this.run("stop", () =>
      this.api.post("POST /api/console-sessions/{id}/cancel", { id }),
    );
    if (ok) {
      this.staleRuns.update((m) => {
        const next = { ...m };
        delete next[id];
        return next;
      });
    }
    return ok;
  }

  private syncDirectorTranscript(): void {
    const row = this.directorRow();
    const detail = this.directorDetail();
    if (
      this.directorWatchers.size === 0 ||
      !row?.session.taskId ||
      row.task?.state !== "running" ||
      row.task?.activityBySetup
    ) {
      this.stopDirectorTranscript();
      return;
    }
    const open = (detail?.attempts ?? []).find((a) => !a.finishedAt);
    if (!open) {
      this.stopDirectorTranscript();
      return;
    }
    const taskId = row.session.taskId;
    const attemptNo = open.number;
    const key = `${taskId}:${attemptNo}`;
    if (this.transcriptKey === key) {
      if (!this.transcriptTimer && !this.doc.hidden) {
        this.scheduleTranscriptPoll(taskId, attemptNo, key, TRANSCRIPT_POLL_MS);
      }
      return;
    }
    this.stopDirectorTranscript();
    this.transcriptKey = key;
    this.transcriptFailures = 0;
    void this.pollDirectorTranscript(taskId, attemptNo, key);
  }

  private scheduleTranscriptPoll(
    taskId: string,
    attemptNo: number,
    key: string,
    delayMs: number,
  ): void {
    if (this.transcriptKey !== key || this.doc.hidden) return;
    if (this.transcriptTimer) {
      clearTimeout(this.transcriptTimer);
      this.transcriptTimer = null;
    }
    this.zone.runOutsideAngular(() => {
      this.transcriptTimer = setTimeout(() => {
        this.transcriptTimer = null;
        if (this.transcriptKey !== key || this.doc.hidden) return;
        this.zone.run(
          () => void this.pollDirectorTranscript(taskId, attemptNo, key),
        );
      }, delayMs);
    });
  }

  private stopDirectorTranscript(): void {
    if (this.transcriptTimer) {
      clearTimeout(this.transcriptTimer);
      this.transcriptTimer = null;
    }
    this.transcriptKey = "";
    this.transcriptFailures = 0;
    this.directorTranscript.set(null);
  }

  private async pollDirectorTranscript(
    taskId: string,
    attemptNo: number,
    key: string,
  ): Promise<void> {
    try {
      const r = await this.api.get<{ transcript?: string }>(
        "GET /api/tasks/{id}/attempts/{number}/transcript",
        { id: taskId, number: attemptNo },
      );
      if (this.transcriptKey === key) {
        this.transcriptFailures = 0;
        this.directorTranscript.set(r?.transcript ?? "");
      }
    } catch {
      if (this.transcriptKey === key) {
        this.transcriptFailures++;
      }
    } finally {
      if (this.transcriptKey === key && !this.doc.hidden) {
        const delay =
          this.transcriptFailures > 0
            ? Math.min(
                TRANSCRIPT_POLL_MS * 2 ** Math.min(this.transcriptFailures, 3),
                MAX_TRANSCRIPT_BACKOFF_MS,
              )
            : TRANSCRIPT_POLL_MS;
        this.scheduleTranscriptPoll(taskId, attemptNo, key, delay);
      }
    }
  }

  // Apply now stops the in-flight turn (POST /api/console-sessions/{id}/cancel)
  // while preserving the session & transcript so the next turn picks up the saved grants/model.
  applyNow(): Promise<boolean> {
    return this.stopTurn();
  }

  restart(carryHandover: boolean): Promise<boolean> {
    const id = this.selectedId();
    return this.run("restart", async () => {
      const res = await this.api.post<ConsoleSessionList>(
        "POST /api/console-sessions/{id}/restart",
        { id },
        { carryHandover },
      );
      if (res?.sessions) this.applyList(res);
      this.staleRuns.update((m) => {
        const next = { ...m };
        delete next[id];
        return next;
      });
    });
  }

  async create(
    titleOrOpts: string | CreateSessionOptions,
    message = "",
  ): Promise<string | null> {
    const opts: CreateSessionOptions =
      typeof titleOrOpts === "string"
        ? { title: titleOrOpts, message }
        : titleOrOpts;
    let created: string | null = null;
    const ok = await this.run("create", async () => {
      const payload: Record<string, unknown> = {
        title: opts.title?.trim() || undefined,
        message: opts.message?.trim() || undefined,
        attachments: opts.attachments ?? [],
      };
      if (opts.team?.trim()) payload["team"] = opts.team.trim();
      if (opts.model?.trim()) payload["model"] = opts.model.trim();
      if (opts.networkAccess?.trim()) {
        payload["networkAccess"] = opts.networkAccess.trim();
      }
      if (opts.capabilities?.length) {
        payload["capabilities"] = opts.capabilities;
      }
      const res = await this.api.post<ConsoleSessionList>(
        "POST /api/console-sessions",
        undefined,
        payload,
      );
      if (res?.sessions) this.applyList(res);
      created = res?.created ?? null;
    });
    return ok ? created : null;
  }

  remove(id: string): Promise<boolean> {
    return this.run("delete", async () => {
      await this.api.delete("DELETE /api/console-sessions/{id}", { id });
      if (this.selectedId() === id) this.selectedId.set(DIRECTOR_ID);
    });
  }

  toggleCapability(capId: string): Promise<boolean> {
    const id = this.selectedId();
    const session = this.sessions().find((s) => s.id === id);
    if (!session) return Promise.resolve(false);
    const on = !(session.capabilities ?? []).includes(capId);
    const edit = (current: string[]) => {
      const rest = current.filter((c) => c !== capId);
      return on ? [...rest, capId] : rest;
    };
    return this.mutateCapabilities(id, capId, edit);
  }

  setCapabilities(nextList: string[]): Promise<boolean> {
    const id = this.selectedId();
    const prev = this.sessions().find((s) => s.id === id)?.capabilities ?? [];
    const added = nextList.filter((x) => !prev.includes(x));
    const removed = new Set(prev.filter((x) => !nextList.includes(x)));
    return this.mutateCapabilities(id, "grants", (current) => {
      const base = current.filter((x) => !removed.has(x));
      for (const a of added) {
        if (!base.includes(a)) base.push(a);
      }
      return base;
    });
  }

  private async mutateCapabilities(
    id: string,
    busyKey: string,
    edit: (current: string[]) => string[],
  ): Promise<boolean> {
    const session = this.sessions().find((s) => s.id === id);
    if (!session) return false;
    this.markLiveRunStale(id);
    const put = (current: string[]) =>
      this.api.put(
        "PUT /api/console-sessions/{id}",
        { id },
        { capabilities: edit(current), expectCapabilities: current },
      );
    return this.run(busyKey, async () => {
      try {
        await put(session.capabilities ?? []);
      } catch (err) {
        if (!isStaleEdit(err)) throw err;
        const fresh = await this.api.get<ConsoleSessionList>(
          "GET /api/console-sessions",
        );
        this.applyList(fresh);
        const row = fresh.sessions?.find((s) => s.id === id);
        if (!row) throw err;
        await put(row.capabilities ?? []);
      }
    });
  }

  async updateSettings(patch: UpdateSessionPatch): Promise<boolean> {
    const id = this.selectedId();
    const sessionObj = this.selected()?.session;
    const touchesRunFixed = RUN_FIXED_KEYS.some((k) => patch[k] !== undefined);
    if (touchesRunFixed) this.markLiveRunStale(id);
    return this.run("settings", async () => {
      const p = { ...patch };
      if (p.tasksApproved !== undefined && sessionObj?.taskId) {
        await this.api.post("POST /api/tasks/{id}/approve", {
          id: sessionObj.taskId,
        });
        delete p.tasksApproved;
        if (Object.keys(p).length === 0) return;
      }
      // Every setting here is the session's own row, which
      // PUT /api/console-sessions/{id} edits whether or not a run is open
      // (as the classic page does). A session between runs has no taskId;
      // its edits used to go to POST /api/settings, the deployment's
      // settings, which ignored them, so a ticked box or a picked model
      // reverted on the next read.
      if (!id) return;
      await this.api.put("PUT /api/console-sessions/{id}", { id }, p);
    });
  }

  async launchWorkstreamPlan(plan: GrainPlanBlock): Promise<string | null> {
    if (!plan.tasks.length) return null;
    let origin: string | null = null;
    const ok = await this.run("launch-plan", async () => {
      const ordered = topologicalPlanOrder(plan.tasks);
      const root = await this.api.post<Task>("POST /api/tasks", undefined, {
        title: plan.title || "Workstream",
        description:
          plan.summary ||
          `Launched from session plan (${plan.tasks.length} tasks).`,
        ...(plan.repo ? { repo: plan.repo } : {}),
        ...(plan.base ? { base: plan.base } : {}),
        approved: false,
      });
      if (!root?.id) throw new Error("root task ID missing");
      origin = `task:${root.id}`;
      const createdIds: string[] = [root.id];
      const keyToTaskId = new Map<string, string>();
      try {
        for (const item of ordered) {
          const depIds = [...(item.dependsOn ?? []), ...(item.after ?? [])]
            .map((k) => keyToTaskId.get(k) ?? (/^\d+$/.test(k) ? k : ""))
            .filter(Boolean);
          const created = await this.api.post<Task>(
            "POST /api/tasks",
            undefined,
            {
              title: item.title,
              description: item.description || "",
              ...(item.repo || plan.repo
                ? { repo: item.repo || plan.repo }
                : {}),
              ...(item.base || plan.base
                ? { base: item.base || plan.base }
                : {}),
              ...(item.model ? { model: item.model } : {}),
              ...(item.priority ? { priority: item.priority } : {}),
              ...(depIds.length ? { dependsOn: depIds } : {}),
              origin,
              approved: true,
            },
          );
          if (created?.id) {
            createdIds.push(created.id);
            if (item.key) keyToTaskId.set(item.key, created.id);
            keyToTaskId.set(item.title, created.id);
          }
        }
      } catch (err) {
        await Promise.allSettled(
          [...createdIds].reverse().map((id) =>
            this.api.post(
              "POST /api/tasks/{id}/close",
              { id },
              {
                reason: "workstream plan creation rolled back after error",
              },
            ),
          ),
        );
        throw err;
      }
    });
    return ok ? origin : null;
  }

  private async run(
    name: string,
    fn: () => Promise<unknown>,
  ): Promise<boolean> {
    this.busy.update((b) => new Set(b).add(name));
    this.actionError.set(null);
    try {
      await fn();
      await this.refresh();
      return true;
    } catch (err) {
      this.actionError.set(errorText(err));
      return false;
    } finally {
      this.busy.update((b) => {
        const next = new Set(b);
        next.delete(name);
        return next;
      });
    }
  }
}
