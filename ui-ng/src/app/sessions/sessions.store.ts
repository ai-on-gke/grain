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

import {
  Injectable,
  NgZone,
  type OnDestroy,
  computed,
  effect,
  inject,
  signal,
  untracked,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
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
import {
  INITIAL_TRANSCRIPT_POLL_MS,
  LiveRefreshService,
  eventTaskId,
} from "../services/live-refresh.service";
import { ReadTrouble } from "../services/request-trouble";
import { TaskFeedStore } from "../services/task-feed.service";
import { topologicalPlanOrder } from "./session-artifacts";
import {
  DIRECTOR_ID,
  errorText,
  isStaleEdit,
  lastLine,
  sessionStatus,
} from "./session-helpers";

export const POLL_MS = 3000;
export const TRANSCRIPT_POLL_MS = INITIAL_TRANSCRIPT_POLL_MS;
export const COMMENTS_QUERY = "comments_limit=50&attempts_limit=20";
const FAILURE_RETRY_MS = 5000;

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
export class SessionsStore implements OnDestroy {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly feed = inject(TaskFeedStore);
  private readonly liveRefresh = inject(LiveRefreshService);
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
  // Tracks sessions whose running turn predates a run-fixed edit (grants, model, network).
  readonly staleRuns = signal<Record<string, string>>({});

  readonly rows = computed<SessionRow[]>(() => {
    const tasks = this.tasks();
    const details = this.details();
    const list = [...this.sessions()].sort(
      (a, b) => Number(b.id === DIRECTOR_ID) - Number(a.id === DIRECTOR_ID),
    );
    return list.map((session) => {
      const d = session.taskId ? details[session.taskId] : undefined;
      const t = session.taskId ? tasks[session.taskId] : undefined;
      const task = d || t ? ({ ...d, ...t } as Task) : null;
      return {
        session,
        task,
        line: lastLine(d?.comments) || task?.activity || "",
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

  private readonly fullFetchedFor = new Map<string, string>();
  private readonly summaryFetchedFor = new Map<string, string>();
  private readonly detailInFlight = new Map<string, Promise<void>>();
  private configLoaded = false;
  private started = false;
  private feedRetained = false;

  private readonly retainers = new Set<string>();
  private readonly directorWatchers = new Set<string>();
  private eventDispose: (() => void) | null = null;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private readonly trouble = new ReadTrouble();
  private refreshing: Promise<void> | null = null;
  private pending = false;

  private readonly transcriptPoller = this.liveRefresh.createTranscriptPoller({
    fetch: async (taskId, attemptNo) => {
      const r = await this.api.get<{ transcript?: string }>(
        "GET /api/tasks/{id}/attempts/{number}/transcript",
        { id: taskId, number: attemptNo },
      );
      return r?.transcript ?? "";
    },
    onText: (text) => {
      this.directorTranscript.set(text);
    },
    isActive: () => this.started && this.directorWatchers.size > 0,
  });

  constructor() {
    effect(() => {
      const rev = this.feed.revision();
      const list = this.feed.tasks();
      if (rev === 0 && list.length === 0) return;
      untracked(() => {
        const map: Record<string, Task> = {};
        for (const t of list) map[t.id] = t;
        this.tasks.set(map);
        if (this.started && this.sessions().length > 0) {
          void this.loadDetails(false).then(() => {
            this.pruneFinishedStaleRuns();
            this.syncDirectorTranscript();
          });
        }
      });
    });

    effect(() => {
      const vp = this.liveRefresh.visiblePulse();
      if (vp > 0 && untracked(() => this.started)) {
        untracked(() => void this.refresh());
      }
    });
  }

  ngOnDestroy(): void {
    this.retainers.clear();
    this.directorWatchers.clear();
    this.stopTimers();
  }

  retain(token: string): void {
    this.retainers.add(token);
    if (!this.started) {
      this.start();
    }
  }

  release(token: string): void {
    this.retainers.delete(token);
    if (this.retainers.size === 0) {
      this.stopTimers();
    }
  }

  setDirectorWatch(token: string, on: boolean): void {
    if (on) {
      const added = !this.directorWatchers.has(token);
      const wasStarted = this.started;
      this.directorWatchers.add(token);
      this.retain(`watch:${token}`);
      if (added && wasStarted) {
        void this.refresh();
      } else {
        this.syncDirectorTranscript();
      }
    } else {
      this.directorWatchers.delete(token);
      if (this.directorWatchers.size === 0) {
        this.stopDirectorTranscript();
      }
      this.release(`watch:${token}`);
    }
  }

  start(): void {
    const firstStart = !this.started;
    this.started = true;
    if (!this.feedRetained) {
      this.feedRetained = true;
      this.feed.start(false);
      if (this.feed.loaded()) {
        const map: Record<string, Task> = {};
        for (const t of this.feed.tasks()) map[t.id] = t;
        this.tasks.set(map);
      }
    }
    if (!this.configLoaded) {
      this.configLoaded = true;
      void this.loadConfigModels();
    }
    if (!this.eventDispose) {
      this.eventDispose = this.liveRefresh.subscribeEvents(
        (ev) =>
          ev.type.startsWith("console_session.") || ev.type.startsWith("task."),
        (batch) => {
          if (!this.started) return;
          if (batch.some((ev) => ev.type.startsWith("console_session."))) {
            void this.refresh();
            return;
          }
          const sessionIds = new Set(
            this.sessions()
              .map((s) => s.taskId)
              .filter((t): t is string => !!t),
          );
          let hasUntargeted = false;
          let needsConsoleList = false;
          const touched = new Set<string>();
          for (const ev of batch) {
            const tid = eventTaskId(ev);
            if (!tid) {
              hasUntargeted = true;
            } else if (sessionIds.has(tid)) {
              touched.add(tid);
              if (ev.type === "task.closed") {
                needsConsoleList = true;
              }
            }
          }
          if (needsConsoleList || (hasUntargeted && sessionIds.size > 0)) {
            void this.refresh();
            return;
          }
          if (touched.size > 0) {
            void this.refreshTouchedDetails(touched);
          }
        },
        250,
      );
    }
    if (firstStart) {
      void this.ensureInitialLoad();
    } else {
      void this.refresh();
    }
  }

  private ensureInitialLoad(): Promise<void> {
    if (this.refreshing) return this.refreshing;
    return this.runLoad("ifStale", true);
  }

  private async refreshTouchedDetails(ids: ReadonlySet<string>): Promise<void> {
    const sel = this.selected()?.session.taskId;
    const dir =
      this.directorWatchers.size > 0
        ? this.directorRow()?.session.taskId
        : undefined;
    const tasks = this.tasks();
    await Promise.all(
      [...ids].map(async (id) => {
        if (
          id === sel ||
          id === dir ||
          typeof this.api.taskSummaries !== "function"
        ) {
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
        } else {
          this.summaryFetchedFor.delete(id);
        }
      }),
    );
    await this.loadDetails(false);
    this.pruneFinishedStaleRuns();
    this.syncDirectorTranscript();
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

  private clearRetryTimer(): void {
    if (this.retryTimer) {
      clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
  }

  private scheduleRetry(): void {
    this.clearRetryTimer();
    if (!this.started || this.liveRefresh.hidden()) return;
    const backoffMs =
      typeof this.api.backoffRemainingMs === "function"
        ? this.api.backoffRemainingMs()
        : 0;
    const delay = Math.max(this.trouble.nextDelay(FAILURE_RETRY_MS), backoffMs);
    this.zone.runOutsideAngular(() => {
      this.retryTimer = setTimeout(() => {
        this.retryTimer = null;
        if (!this.started || this.liveRefresh.hidden()) return;
        this.zone.run(() => void this.refresh());
      }, delay);
    });
  }

  private stopTimers(): void {
    this.started = false;
    this.clearRetryTimer();
    this.trouble.ok();
    this.stopDirectorTranscript();
    this.eventDispose?.();
    this.eventDispose = null;
    if (this.feedRetained) {
      this.feedRetained = false;
      this.feed.stop(false);
    }
  }

  select(id: string): void {
    if (this.selectedId() === id) return;
    this.selectedId.set(id);
    this.actionError.set(null);
    if (!this.started) return;
    void this.loadDetails(true).then(() => {
      this.pruneFinishedStaleRuns();
      this.syncDirectorTranscript();
    });
  }

  refresh(): Promise<void> {
    return this.runLoad("always", true);
  }

  private runLoad(
    feedMode: "always" | "ifStale",
    forceSelected: boolean,
  ): Promise<void> {
    if (this.refreshing) {
      this.pending = true;
      return this.refreshing;
    }
    this.refreshing = this.load(feedMode, forceSelected).finally(() => {
      this.refreshing = null;
      if (this.pending) {
        this.pending = false;
        void this.refresh();
      }
    });
    return this.refreshing;
  }

  private async load(
    feedMode: "always" | "ifStale",
    forceSelected: boolean,
  ): Promise<void> {
    if (
      typeof this.api.get !== "function" ||
      typeof this.api.getIfChanged !== "function"
    ) {
      this.loaded.set(true);
      return;
    }
    this.clearRetryTimer();
    const wasStarted = this.started;
    const listReq = this.api
      .get<ConsoleSessionList>("GET /api/console-sessions")
      .then(
        (val) => {
          this.applyList(val);
          this.loadError.set(null);
          this.trouble.ok();
        },
        (reason) => {
          this.trouble.fail(reason);
          this.loadError.set(
            `Sessions could not be read: ${errorText(reason)}. This page tries again on its own.`,
          );
          this.scheduleRetry();
        },
      );
    const feedReq =
      feedMode === "ifStale" ? this.feed.refreshIfStale() : this.feed.refresh();
    await Promise.allSettled([listReq, feedReq]);
    if (this.feed.loaded()) {
      const map: Record<string, Task> = {};
      for (const t of this.feed.tasks()) map[t.id] = t;
      this.tasks.set(map);
    }
    this.loaded.set(true);
    if (wasStarted && !this.started) return;
    await this.loadDetails(forceSelected);
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

  private async loadDetails(forceSelected: boolean): Promise<void> {
    const sel = this.selected()?.session.taskId;
    const dir =
      this.directorWatchers.size > 0
        ? this.directorRow()?.session.taskId
        : undefined;
    const tasks = this.tasks();
    const allIds = this.sessions()
      .map((s) => s.taskId)
      .filter((t): t is string => !!t);

    const fullIds: string[] = [];
    const summaryIds: string[] = [];
    for (const id of allIds) {
      const key = rowKey(tasks[id]);
      if (id === sel || id === dir) {
        if (forceSelected || this.fullFetchedFor.get(id) !== key) {
          fullIds.push(id);
        }
      } else if (this.summaryFetchedFor.get(id) !== key) {
        if (typeof this.api.taskSummaries === "function") {
          summaryIds.push(id);
        } else {
          fullIds.push(id);
        }
      }
    }

    const ops: Promise<void>[] = fullIds.map(async (id) => {
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
    });

    if (summaryIds.length > 0 && typeof this.api.taskSummaries === "function") {
      ops.push(this.loadBatchSummaries(summaryIds, tasks));
    }

    await Promise.all(ops);
  }

  private async loadBatchSummaries(
    ids: string[],
    tasks: Record<string, Task>,
  ): Promise<void> {
    try {
      const batch = await this.api.taskSummaries(ids, 1, 1);
      this.details.update((prev) => {
        const next = { ...prev };
        for (const id of ids) {
          const key = rowKey(tasks[id]);
          this.summaryFetchedFor.set(id, key);
          if (this.fullFetchedFor.get(id) !== key) {
            this.fullFetchedFor.delete(id);
            if (batch[id]) {
              next[id] = batch[id];
            }
          }
        }
        return next;
      });
    } catch {
      // Sidebar preview degrades cleanly to task.activity when batch summary fails.
    }
  }

  private loadDetail(id: string, key: string): Promise<void> {
    const inflight = this.detailInFlight.get(id);
    if (inflight) return inflight;
    const p = (async () => {
      const r = await this.api.getIfChanged<TaskDetail>(
        "GET /api/tasks/{id}",
        { id },
        COMMENTS_QUERY,
      );
      this.fullFetchedFor.set(id, key);
      this.summaryFetchedFor.set(id, key);
      if (r.changed || !this.details()[id]) {
        this.details.update((m) => ({ ...m, [id]: r.body }));
      }
    })().finally(() => {
      this.detailInFlight.delete(id);
    });
    this.detailInFlight.set(id, p);
    return p;
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
    if (
      !row ||
      !taskId ||
      (!body && attachments.length === 0) ||
      this.isBusy("send")
    ) {
      return false;
    }
    return this.run("send", async () => {
      await this.api.post(
        "POST /api/tasks/{id}/comments",
        { id: taskId },
        { body, attachments },
      );
      if (clearSessionDraft) {
        if (this.draftFor(sessionId) === rawBody) {
          this.setDraft(sessionId, "");
        }
        if (this.attachmentsFor(sessionId) === attachments) {
          this.setAttachments(sessionId, []);
        }
      }
    });
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
      !this.started ||
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
    this.transcriptPoller.watch(row.session.taskId, open.number, true);
  }

  private stopDirectorTranscript(): void {
    this.transcriptPoller.stop();
    this.directorTranscript.set(null);
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
