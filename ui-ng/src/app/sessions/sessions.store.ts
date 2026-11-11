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
  CapabilityOption,
  ConsoleSession,
  ConsoleSessionList,
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

// rowKey is what about a list row means its detail may have moved.
function rowKey(t: Task | undefined): string {
  return t ? `${t.state}|${t.updatedAt ?? ""}|${t.activityAt ?? ""}` : "";
}

export interface SessionRow {
  session: ConsoleSession;
  task: Task | null;
  line: string;
}

// SessionsStore is the Sessions page's state: the standing
// conversations, the selected one's task with its messages, and the
// calls that talk to it. A session's conversation is the comments of its
// task (pkg/ui console_sessions.go).
@Injectable({ providedIn: "root" })
export class SessionsStore {
  private readonly api = inject(GrainApi);
  private readonly events = inject(GrainEvents);
  private readonly zone = inject(NgZone);

  readonly sessions = signal<ConsoleSession[]>([]);
  readonly offered = signal<CapabilityOption[]>([]);
  readonly tasks = signal<Record<string, Task>>({});
  readonly details = signal<Record<string, TaskDetail>>({});
  readonly selectedId = signal<string>(DIRECTOR_ID);
  readonly loaded = signal(false);
  readonly loadError = signal<string | null>(null);
  readonly actionError = signal<string | null>(null);
  // busy holds the actions in flight ("send", "stop", "restart", a
  // capability id, ...), so only those controls wait, and two at once do
  // not clear each other.
  readonly busy = signal<ReadonlySet<string>>(new Set());
  // detailError is why the selected conversation could not be read.
  readonly detailError = signal<string | null>(null);
  readonly drafts = signal<Record<string, string>>({});

  // The Director first, then the rest in the server's order.
  readonly rows = computed<SessionRow[]>(() => {
    const tasks = this.tasks();
    const details = this.details();
    const list = [...this.sessions()].sort(
      (a, b) => Number(b.id === DIRECTOR_ID) - Number(a.id === DIRECTOR_ID),
    );
    return list.map((session) => {
      const d = session.taskId ? details[session.taskId] : undefined;
      const t = session.taskId ? tasks[session.taskId] : undefined;
      // The list is read every poll and the detail of a session not on
      // screen only when its list row moves, so the list wins for state
      // and activity; the detail adds the messages.
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

  // What each detail was read for (rowKey of its list row), so a
  // background session's detail is read again when its row moves.
  private readonly fetchedFor = new Map<string, string>();

  private timer: ReturnType<typeof setInterval> | null = null;
  private eventSub: { unsubscribe(): void } | null = null;
  private refreshing: Promise<void> | null = null;
  private pending = false;

  start(): void {
    void this.refresh();
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

  // refresh reads everything once; a call made while one is in flight
  // runs once more after it rather than piling up.
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
    // An unchanged answer still seeds an empty map: another reader of the
    // same URL may have taken the fresh copy (getIfChanged shares tags).
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
  }

  private applyList(list: ConsoleSessionList): void {
    this.sessions.set(list.sessions ?? []);
    if (list.capabilities) this.offered.set(list.capabilities);
  }

  // loadDetails reads the selected session's conversation every time and
  // each other session's when its list row has moved since, for its last
  // line. Only the selected one's failure is shown.
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

  // send posts the draft as a message on the session's task, which starts
  // a run with the session's grants. The draft clears once the server has
  // it, and stays (with the error) when it does not.
  async send(): Promise<boolean> {
    const row = this.selected();
    const taskId = row?.session.taskId;
    const typed = row ? this.draftFor(row.session.id) : "";
    const body = typed.trim();
    // One send at a time: a second Enter before the first lands would
    // post the message twice, and each post starts a run.
    if (!row || !taskId || !body || this.isBusy("send")) return false;
    return this.run("send", async () => {
      await this.api.post(
        "POST /api/tasks/{id}/comments",
        { id: taskId },
        { body, attachments: [] },
      );
      // Keep anything typed while the message was on its way.
      if (this.draftFor(row.session.id) === typed) {
        this.setDraft(row.session.id, "");
      }
    });
  }

  stopTurn(): Promise<boolean> {
    const id = this.selectedId();
    return this.run("stop", () =>
      this.api.post("POST /api/console-sessions/{id}/cancel", { id }),
    );
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
    });
  }

  // create starts a new session and returns its id.
  async create(title: string, message: string): Promise<string | null> {
    let created: string | null = null;
    const ok = await this.run("create", async () => {
      const res = await this.api.post<ConsoleSessionList>(
        "POST /api/console-sessions",
        undefined,
        {
          title: title.trim() || undefined,
          message: message.trim() || undefined,
          attachments: [],
        },
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

  // toggleCapability grants or removes one capability. The PUT takes the
  // whole list, with the list it was computed from as
  // expectCapabilities; when the server says that list is stale it is
  // read again and the same edit is made to it, once.
  toggleCapability(capId: string): Promise<boolean> {
    const id = this.selectedId();
    const session = this.sessions().find((s) => s.id === id);
    if (!session) return Promise.resolve(false);
    const on = !(session.capabilities ?? []).includes(capId);
    const edit = (current: string[]) => {
      const rest = current.filter((c) => c !== capId);
      return on ? [...rest, capId] : rest;
    };
    const put = (current: string[]) =>
      this.api.put(
        "PUT /api/console-sessions/{id}",
        { id },
        { capabilities: edit(current), expectCapabilities: current },
      );
    return this.run(capId, async () => {
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
