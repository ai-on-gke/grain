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

import { Injectable, NgZone, computed, inject, signal } from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type {
  AgentModel,
  AgentModelsResponse,
  AttachmentUpload,
  CapabilityOption,
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

// POLL_MS is the classic UI's refresh: every 3 s with ETags, so a reply
// shows up without the reader doing anything, events or not.
export const POLL_MS = 3000;
export const TRANSCRIPT_POLL_MS = 1500;
export const COMMENTS_QUERY = "comments_limit=50&attempts_limit=20";

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

export type PendingSendState = "sending" | "queued" | "sent" | "failed";

export interface PendingSessionMessage {
  key: string;
  sessionId: string;
  taskId: string;
  body: string;
  attachments: AttachmentUpload[];
  createdAt: string;
  authorLabel: string;
  afterMaxId: number;
  afterCommentLen: number;
  postStarted: boolean;
  state: PendingSendState;
  error?: string;
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
  readonly pendingMessages = signal<PendingSessionMessage[]>([]);
  readonly sendNotice = signal<string>("");
  // Tracks sessions whose running turn predates a run-fixed edit (grants, model, network).
  readonly staleRuns = signal<Record<string, string>>({});

  readonly rows = computed<SessionRow[]>(() => {
    const tasks = this.tasks();
    const details = this.details();
    const pending = this.pendingMessages();
    const list = [...this.sessions()].sort(
      (a, b) => Number(b.id === DIRECTOR_ID) - Number(a.id === DIRECTOR_ID),
    );
    return list.map((session) => {
      const d = session.taskId ? details[session.taskId] : undefined;
      const t = session.taskId ? tasks[session.taskId] : undefined;
      const task = d || t ? ({ ...d, ...t } as Task) : null;
      const lastPending = pending
        .filter(
          (p) =>
            p.sessionId === session.id &&
            (!session.taskId || p.taskId === session.taskId) &&
            p.state !== "failed",
        )
        .at(-1);
      const fallbackPending = lastPending?.body
        ? lastLine([{ id: 0, body: lastPending.body }])
        : "";
      return {
        session,
        task,
        line: lastLine(d?.comments) || task?.activity || fallbackPending || "",
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

  readonly selectedPending = computed<PendingSessionMessage[]>(() => {
    const sid = this.selectedId();
    const tid = this.selected()?.session.taskId;
    return this.pendingMessages().filter(
      (p) => p.sessionId === sid && (!tid || p.taskId === tid),
    );
  });

  readonly directorRow = computed<SessionRow | null>(() => {
    return this.rows().find((r) => r.session.id === DIRECTOR_ID) ?? null;
  });

  readonly directorDetail = computed<TaskDetail | null>(() => {
    const tid = this.directorRow()?.session.taskId;
    return tid ? (this.details()[tid] ?? null) : null;
  });

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
  private configLoaded = false;
  private pendingSeq = 0;
  private inFlightSends = 0;
  private readonly activeSessionSends = new Map<string, number>();
  private readonly sendChains = new Map<string, Promise<void>>();
  private readonly claimedCommentKeys = new Set<string>();

  private timer: ReturnType<typeof setInterval> | null = null;
  private transcriptTimer: ReturnType<typeof setInterval> | null = null;
  private transcriptKey = "";
  private readonly retainers = new Set<string>();
  private readonly directorWatchers = new Set<string>();
  private eventSub: { unsubscribe(): void } | null = null;
  private refreshing: Promise<void> | null = null;
  private pending = false;

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
    if (!this.eventSub) {
      this.eventSub = this.events.events().subscribe(() => {
        void this.refresh();
      });
    }
    if (!this.timer) {
      this.zone.runOutsideAngular(() => {
        this.timer = setInterval(
          () => this.zone.run(() => void this.refresh()),
          POLL_MS,
        );
      });
    }
  }

  private async loadConfigModels(): Promise<void> {
    try {
      const cfg = (await this.api.config()) as GrainConfig | null;
      if (cfg && !this.access.configLoaded()) {
        this.access.setConfig(cfg);
      }
      if (Array.isArray(cfg?.models)) this.models.set(cfg.models);
      if (typeof cfg?.defaultModel === "string") {
        this.defaultModel.set(cfg.defaultModel);
      }
    } catch {
      // Optional model list read.
    }
    if (typeof this.api.get !== "function") return;
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
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
    this.stopDirectorTranscript();
    this.eventSub?.unsubscribe();
    this.eventSub = null;
  }

  select(id: string): void {
    if (this.selectedId() === id) return;
    this.selectedId.set(id);
    this.actionError.set(null);
    void this.refresh();
  }

  refresh(): Promise<void> {
    if (this.refreshing) {
      this.pending = true;
      return this.refreshing;
    }
    this.refreshing = this.load().finally(() => {
      this.refreshing = null;
      if (this.pending) {
        this.pending = false;
        void this.refresh();
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
    const [list, tasks] = await Promise.allSettled([
      this.api.get<ConsoleSessionList>("GET /api/console-sessions"),
      this.api.getIfChanged<Task[]>("GET /api/tasks"),
    ]);
    if (list.status === "fulfilled") {
      this.applyList(list.value);
      this.loadError.set(null);
    } else {
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
    }
    this.loaded.set(true);
    await this.loadDetails();
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

  private async loadDetails(): Promise<void> {
    const sel = this.selected()?.session.taskId;
    const dir =
      this.directorWatchers.size > 0
        ? this.directorRow()?.session.taskId
        : undefined;
    const tasks = this.tasks();
    const ids = this.sessions()
      .map((s) => s.taskId)
      .filter(
        (t): t is string =>
          !!t &&
          (t === sel ||
            t === dir ||
            this.fetchedFor.get(t) !== rowKey(tasks[t])),
      );
    await Promise.all(
      ids.map(async (id) => {
        try {
          await this.loadDetail(id, rowKey(tasks[id]));
          if (id === sel) this.detailError.set(null);
        } catch (err) {
          if (id === sel) {
            this.detailError.set(
              `This conversation could not be read: ${errorText(err)}`,
            );
          }
        }
      }),
    );
  }

  private async loadDetail(id: string, key: string): Promise<void> {
    const r = await this.api.getIfChanged<TaskDetail>(
      "GET /api/tasks/{id}",
      { id },
      COMMENTS_QUERY,
    );
    this.fetchedFor.set(id, key);
    if (r.changed || !this.details()[id]) {
      this.details.update((m) => ({ ...m, [id]: r.body }));
      this.pruneConsumedForTask(id);
    }
  }

  private commentBaseline(taskId: string): {
    afterMaxId: number;
    afterCommentLen: number;
  } {
    const comments = this.details()[taskId]?.comments ?? [];
    let afterMaxId = 0;
    for (const c of comments) {
      if (typeof c.id === "number" && c.id > afterMaxId) {
        afterMaxId = c.id;
      }
    }
    return { afterMaxId, afterCommentLen: comments.length };
  }

  private pruneConsumedForTask(taskId: string): void {
    const serverComments = this.details()[taskId]?.comments;
    if (!serverComments?.length) return;
    const current = this.pendingMessages();
    if (
      !current.some(
        (p) => p.taskId === taskId && p.postStarted && p.state !== "failed",
      )
    ) {
      return;
    }
    const consumedIdx = new Set<number>();
    let changed = false;
    const next = current.filter((p) => {
      if (p.taskId !== taskId || !p.postStarted || p.state === "failed") {
        return true;
      }
      const idx = serverComments.findIndex((c, i) => {
        if (consumedIdx.has(i)) return false;
        if (
          typeof c.id === "number" &&
          this.claimedCommentKeys.has(`${taskId}:${c.id}`)
        ) {
          return false;
        }
        if (c.authorKind && c.authorKind !== "human") return false;
        const isAfter =
          typeof c.id === "number"
            ? c.id > p.afterMaxId
            : i >= p.afterCommentLen;
        if (!isAfter) return false;
        if ((c.body ?? "").trim() !== p.body.trim()) return false;
        if (!p.body.trim() && p.attachments.length > 0) {
          return (c.attachments?.length ?? 0) >= p.attachments.length;
        }
        return true;
      });
      if (idx !== -1) {
        consumedIdx.add(idx);
        const cid = serverComments[idx].id;
        if (typeof cid === "number") {
          this.claimedCommentKeys.add(`${taskId}:${cid}`);
        }
        changed = true;
        return false;
      }
      return true;
    });
    if (changed) {
      this.pendingMessages.set(next);
    }
  }

  private updatePending(
    key: string,
    patch: Partial<PendingSessionMessage>,
  ): void {
    this.pendingMessages.update((list) =>
      list.map((p) => (p.key === key ? { ...p, ...patch } : p)),
    );
  }

  private bumpSendBusy(delta: number): void {
    this.inFlightSends = Math.max(0, this.inFlightSends + delta);
    this.busy.update((b) => {
      const next = new Set(b);
      if (this.inFlightSends > 0) next.add("send");
      else next.delete("send");
      return next;
    });
  }

  isBusy(name: string): boolean {
    return this.busy().has(name);
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

  async send(): Promise<boolean> {
    const id = this.selectedId();
    const typed = this.draftFor(id);
    const attachments = this.attachmentsFor(id);
    return this.sendTo(id, typed, attachments, true);
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
    if (!row || !taskId || (!body && attachments.length === 0)) {
      return false;
    }
    // Clear synchronously before the first await so rapid double-Enter on the
    // same text is ignored, while new text typed during an in-flight POST can
    // be queued immediately behind it.
    if (clearSessionDraft) {
      this.setDraft(sessionId, "");
      this.setAttachments(sessionId, []);
    }
    this.actionError.set(null);
    this.sendNotice.set("");

    const activeCount = this.activeSessionSends.get(sessionId) ?? 0;
    const initialState: PendingSendState =
      activeCount > 0 ? "queued" : "sending";
    this.activeSessionSends.set(sessionId, activeCount + 1);

    const baseline = this.commentBaseline(taskId);
    const key = `pending-${++this.pendingSeq}`;
    const entry: PendingSessionMessage = {
      key,
      sessionId,
      taskId,
      body,
      attachments: [...attachments],
      createdAt: new Date().toISOString(),
      authorLabel: this.access.identityLabel() || "Operator",
      afterMaxId: baseline.afterMaxId,
      afterCommentLen: baseline.afterCommentLen,
      postStarted: initialState === "sending",
      state: initialState,
    };
    this.pendingMessages.update((list) => [...list, entry]);

    return this.enqueuePendingDelivery(key, sessionId);
  }

  async retryMessage(key: string): Promise<boolean> {
    const existing = this.pendingMessages().find((p) => p.key === key);
    if (!existing || existing.state !== "failed") {
      return false;
    }
    const { sessionId } = existing;
    const row = this.rows().find((r) => r.session.id === sessionId);
    const taskId = row?.session.taskId || existing.taskId;
    if (!taskId) return false;

    if (this.actionError() === existing.error) {
      this.actionError.set(null);
    }
    this.sendNotice.set("");

    const activeCount = this.activeSessionSends.get(sessionId) ?? 0;
    const nextState: PendingSendState = activeCount > 0 ? "queued" : "sending";
    this.activeSessionSends.set(sessionId, activeCount + 1);

    const baseline = this.commentBaseline(taskId);
    const updated: PendingSessionMessage = {
      ...existing,
      taskId,
      createdAt: new Date().toISOString(),
      afterMaxId: baseline.afterMaxId,
      afterCommentLen: baseline.afterCommentLen,
      postStarted: nextState === "sending",
      state: nextState,
      error: undefined,
    };

    this.pendingMessages.update((list) => {
      const without = list.filter((p) => p.key !== key);
      let insertAt = -1;
      for (let i = 0; i < without.length; i++) {
        if (
          without[i].sessionId === sessionId &&
          without[i].state !== "failed"
        ) {
          insertAt = i;
        }
      }
      if (insertAt === -1) {
        const firstFailed = without.findIndex(
          (p) => p.sessionId === sessionId && p.state === "failed",
        );
        if (firstFailed === -1) return [...without, updated];
        return [
          ...without.slice(0, firstFailed),
          updated,
          ...without.slice(firstFailed),
        ];
      }
      return [
        ...without.slice(0, insertAt + 1),
        updated,
        ...without.slice(insertAt + 1),
      ];
    });

    return this.enqueuePendingDelivery(key, sessionId);
  }

  discardMessage(key: string): void {
    const existing = this.pendingMessages().find((p) => p.key === key);
    if (!existing) return;
    this.pendingMessages.update((list) => list.filter((p) => p.key !== key));
    if (this.actionError() === existing.error) {
      this.actionError.set(null);
    }
    this.sendNotice.set("");
  }

  private async enqueuePendingDelivery(
    key: string,
    sessionId: string,
  ): Promise<boolean> {
    this.bumpSendBusy(1);
    const prev = this.sendChains.get(sessionId) ?? Promise.resolve();
    let postOk = false;
    let postedTaskId = "";

    const step = prev.then(async () => {
      try {
        const cur = this.pendingMessages().find((p) => p.key === key);
        if (!cur || (cur.state !== "sending" && cur.state !== "queued")) {
          return;
        }
        const latestTaskId =
          this.rows().find((r) => r.session.id === sessionId)?.session.taskId ||
          cur.taskId;
        postedTaskId = latestTaskId;
        const baseline = this.commentBaseline(latestTaskId);
        this.updatePending(key, {
          taskId: latestTaskId,
          afterMaxId: baseline.afterMaxId,
          afterCommentLen: baseline.afterCommentLen,
          postStarted: true,
          state: "sending",
          error: undefined,
        });

        await this.api.post(
          "POST /api/tasks/{id}/comments",
          { id: latestTaskId },
          { body: cur.body, attachments: cur.attachments },
        );
        postOk = true;
        this.fetchedFor.delete(latestTaskId);
        this.updatePending(key, { state: "sent", error: undefined });
        this.pruneConsumedForTask(latestTaskId);
      } catch (err) {
        const msg = errorText(err);
        this.actionError.set(msg);
        this.sendNotice.set(
          "Message not sent. Retry it from the conversation.",
        );
        this.updatePending(key, { state: "failed", error: msg });
      } finally {
        const nextCount = (this.activeSessionSends.get(sessionId) ?? 1) - 1;
        if (nextCount <= 0) {
          this.activeSessionSends.delete(sessionId);
        } else {
          this.activeSessionSends.set(sessionId, nextCount);
        }
      }
    });

    const tail = step.catch(() => undefined);
    this.sendChains.set(sessionId, tail);

    try {
      await step;
      if (postOk && postedTaskId) {
        await this.refresh();
      }
      return postOk;
    } finally {
      this.bumpSendBusy(-1);
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
    if (this.transcriptKey === key && this.transcriptTimer) return;
    this.stopDirectorTranscript();
    this.transcriptKey = key;
    void this.pollDirectorTranscript(taskId, attemptNo, key);
    this.zone.runOutsideAngular(() => {
      this.transcriptTimer = setInterval(
        () =>
          this.zone.run(
            () => void this.pollDirectorTranscript(taskId, attemptNo, key),
          ),
        TRANSCRIPT_POLL_MS,
      );
    });
  }

  private stopDirectorTranscript(): void {
    if (this.transcriptTimer) {
      clearInterval(this.transcriptTimer);
      this.transcriptTimer = null;
    }
    this.transcriptKey = "";
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
        this.directorTranscript.set(r?.transcript ?? "");
      }
    } catch {
      // Transient transcript polling errors preserve existing partial output.
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
      if (res?.sessions) {
        this.applyList(res);
        const nextTaskId = res.sessions.find((s) => s.id === id)?.taskId;
        this.pendingMessages.update((list) =>
          list.filter(
            (p) =>
              p.sessionId !== id || (!!nextTaskId && p.taskId === nextTaskId),
          ),
        );
      }
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
      this.pendingMessages.update((list) =>
        list.filter((p) => p.sessionId !== id),
      );
      this.sendChains.delete(id);
      this.activeSessionSends.delete(id);
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
