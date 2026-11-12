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
  AttachmentUpload,
  CapabilityOption,
  ConsoleSession,
  ConsoleSessionList,
  GrainConfig,
  Task,
  TaskDetail,
} from "../api/models";
import {
  DIRECTOR_ID,
  errorText,
  isStaleEdit,
  lastLine,
  sessionStatus,
} from "./session-view";

// POLL_MS is the classic UI's refresh: every 3 s with ETags, so a reply
// shows up without the reader doing anything, events or not.
export const POLL_MS = 3000;
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
  private readonly zone = inject(NgZone);

  readonly sessions = signal<ConsoleSession[]>([]);
  readonly offered = signal<CapabilityOption[]>([]);
  readonly networkOptions = signal<string[]>(["open", "none"]);
  readonly models = signal<string[]>([]);
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

  private timer: ReturnType<typeof setInterval> | null = null;
  private eventSub: { unsubscribe(): void } | null = null;
  private refreshing: Promise<void> | null = null;
  private pending = false;

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
      if (Array.isArray(cfg?.models)) this.models.set(cfg.models);
      if (typeof cfg?.defaultModel === "string") {
        this.defaultModel.set(cfg.defaultModel);
      }
    } catch {
      // Optional model list read.
    }
  }

  stop(): void {
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
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
    const tasks = this.tasks();
    const ids = this.sessions()
      .map((s) => s.taskId)
      .filter(
        (t): t is string =>
          !!t && (t === sel || this.fetchedFor.get(t) !== rowKey(tasks[t])),
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
    }
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
    const row = this.selected();
    const taskId = row?.session.taskId;
    const typed = row ? this.draftFor(row.session.id) : "";
    const body = typed.trim();
    const attachments = row ? this.attachmentsFor(row.session.id) : [];
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
      if (this.draftFor(row.session.id) === typed) {
        this.setDraft(row.session.id, "");
      }
      if (this.attachmentsFor(row.session.id) === attachments) {
        this.setAttachments(row.session.id, []);
      }
    });
  }

  async stopTurn(): Promise<boolean> {
    const id = this.selectedId();
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
    const touchesRunFixed = RUN_FIXED_KEYS.some((k) => patch[k] !== undefined);
    if (touchesRunFixed) this.markLiveRunStale(id);
    return this.run("settings", () =>
      this.api.put("PUT /api/console-sessions/{id}", { id }, patch),
    );
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
