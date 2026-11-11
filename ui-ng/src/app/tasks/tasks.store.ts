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
import { InboxStore } from "../inbox/inbox.store";
import type { Task } from "../api/models";
import { errorText } from "../sessions/session-view";
import {
  type MovePlan,
  type TabId,
  TABS,
  inTab,
  isWork,
  neighbours,
  priorityOf,
} from "./task-view";

export const POLL_MS = 3000;
export const CLOSED_PAGE = 50;

// An undo is the call that puts things back, described for the snackbar.
export interface Undo {
  label: string;
  run: () => Promise<void>;
}

// TasksStore is the Tasks page's state: the live task list (open tasks,
// in backlog order, polled with ETags as the classic UI does), the closed
// tasks a page at a time, and the calls that act on them. Every action
// re-reads the list; nothing is shown that the server has not said.
@Injectable({ providedIn: "root" })
export class TasksStore {
  private readonly api = inject(GrainApi);
  private readonly events = inject(GrainEvents);
  private readonly zone = inject(NgZone);
  // The shell's list feeds Next up and the Inbox badge; it is told to
  // read again after every action here so neither lags behind.
  private readonly inbox = inject(InboxStore);

  readonly live = signal<Task[]>([]);
  readonly closed = signal<Task[]>([]);
  readonly closedCursor = signal<string | null>(null);
  readonly closedLoaded = signal(false);
  readonly closedTotal = signal<number | null>(null);
  readonly loaded = signal(false);
  readonly loadError = signal<string | null>(null);
  readonly actionError = signal<string | null>(null);
  readonly busy = signal<ReadonlySet<string>>(new Set());

  readonly work = computed(() => this.live().filter(isWork));
  readonly backlog = computed(() =>
    this.work().filter((t) => t.state === "queued"),
  );
  readonly counts = computed(() => {
    const c = {} as Record<TabId, number | null>;
    for (const tab of TABS) {
      c[tab.id] =
        tab.id === "closed"
          ? this.closedTotal()
          : this.work().filter((t) => inTab(t, tab.id)).length;
    }
    return c;
  });

  private timer: ReturnType<typeof setInterval> | null = null;
  private eventSub: { unsubscribe(): void } | null = null;
  private refreshing: Promise<void> | null = null;
  private eventTimer: ReturnType<typeof setTimeout> | null = null;
  private pending = false;

  start(): void {
    void this.refresh();
    // Events come in bursts; one read covers a burst.
    this.eventSub ??= this.events.events().subscribe(() => {
      if (this.eventTimer) return;
      this.eventTimer = setTimeout(() => {
        this.eventTimer = null;
        void this.refresh();
      }, 250);
    });
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
    try {
      const r = await this.api.getIfChanged<Task[]>("GET /api/tasks");
      if (r.changed || this.live().length === 0) {
        this.live.set(Array.isArray(r.body) ? r.body : []);
      }
      this.loadError.set(null);
    } catch (err) {
      this.loadError.set(
        `The task list could not be read: ${errorText(err)}. This page tries again on its own.`,
      );
    }
    this.loaded.set(true);
  }

  // loadClosed reads the first page of closed tasks (or the next one).
  async loadClosed(more = false): Promise<void> {
    const cursor = more ? this.closedCursor() : null;
    if (more && !cursor) return;
    // cursor= is always sent: the server pages (newest closed first, with
    // nextCursor and total) only when the parameter is present.
    const q = `state=closed&limit=${CLOSED_PAGE}&cursor=${encodeURIComponent(cursor ?? "")}`;
    try {
      const r = await this.api.get<
        { tasks?: Task[]; nextCursor?: string; total?: number } | Task[]
      >("GET /api/tasks", undefined, q);
      const page = (Array.isArray(r) ? r : (r.tasks ?? [])).filter(isWork);
      const next = Array.isArray(r) ? null : (r.nextCursor ?? null);
      if (!Array.isArray(r) && typeof r.total === "number") {
        this.closedTotal.set(r.total);
      }
      this.closed.update((c) => (more ? [...c, ...page] : page));
      this.closedCursor.set(next || null);
      this.closedLoaded.set(true);
      if (this.closedTotal() === null)
        this.closedTotal.set(this.closed().length);
    } catch (err) {
      this.actionError.set(`Closed tasks could not be read: ${errorText(err)}`);
    }
  }

  isBusy(key: string): boolean {
    return this.busy().has(key);
  }

  // ---------- actions ----------

  async approve(ids: readonly string[]): Promise<number> {
    return this.fanOut("approve", ids, (id) =>
      this.api.post("POST /api/tasks/{id}/approve", { id }),
    );
  }

  async defer(ids: readonly string[]): Promise<number> {
    return this.fanOut("defer", ids, (id) =>
      this.api.post(
        "POST /api/tasks/{id}/defer",
        { id },
        { deferredUntil: "" },
      ),
    );
  }

  async undefer(ids: readonly string[]): Promise<number> {
    return this.fanOut("undefer", ids, (id) =>
      this.api.post("POST /api/tasks/{id}/undefer", { id }),
    );
  }

  async close(ids: readonly string[]): Promise<number> {
    return this.fanOut("close", ids, (id) =>
      this.api.post(
        "POST /api/tasks/{id}/close",
        { id },
        { close_pull_request: false },
      ),
    );
  }

  async reopen(ids: readonly string[]): Promise<number> {
    return this.fanOut("reopen", ids, (id) =>
      this.api.post("POST /api/tasks/{id}/reopen", { id }),
    );
  }

  // setPriority writes one priority to each task and returns how to put
  // each one's old priority back.
  async setPriority(
    ids: readonly string[],
    priority: number,
  ): Promise<{ failed: number; undo: Undo }> {
    const before = new Map(
      ids.map((id) => [id, priorityOf(this.find(id) ?? ({} as Task))]),
    );
    const failed = await this.fanOut("priority", ids, (id) =>
      this.api.patch("PATCH /api/tasks/{id}", { id }, { priority }),
    );
    return {
      failed,
      undo: {
        label: "Undo",
        run: async () => {
          await Promise.all(
            [...before].map(([id, p]) =>
              this.api.patch("PATCH /api/tasks/{id}", { id }, { priority: p }),
            ),
          );
          await this.refresh();
        },
      },
    };
  }

  // move applies a planned backlog move: the band's priority first when
  // it changes, then the new neighbours. Undo restores both exactly.
  async move(plan: MovePlan): Promise<Undo | null> {
    const backlog = this.backlog();
    const t = backlog.find((x) => x.id === plan.id);
    if (!t) return null;
    const was = neighbours(backlog, plan.id);
    const oldPriority = priorityOf(t);
    const ok = await this.run(`move:${plan.id}`, async () => {
      if (plan.priority !== null) {
        await this.api.patch(
          "PATCH /api/tasks/{id}",
          { id: plan.id },
          { priority: plan.priority },
        );
      }
      try {
        await this.api.post("POST /api/tasks/reorder", undefined, {
          ids: [plan.id],
          afterId: plan.afterId,
          beforeId: plan.beforeId,
        });
      } catch (err) {
        // Half a move is worse than none: put the priority back.
        if (plan.priority !== null) {
          await this.api
            .patch(
              "PATCH /api/tasks/{id}",
              { id: plan.id },
              { priority: oldPriority },
            )
            .catch(() => undefined);
        }
        throw err;
      }
    });
    if (!ok) return null;
    return {
      label: "Undo",
      run: async () => {
        if (plan.priority !== null) {
          await this.api.patch(
            "PATCH /api/tasks/{id}",
            { id: plan.id },
            { priority: oldPriority },
          );
        }
        await this.api.post("POST /api/tasks/reorder", undefined, {
          ids: [plan.id],
          afterId: was.afterId,
          beforeId: was.beforeId,
        });
        await this.refresh();
      },
    };
  }

  // moveToEnd puts the selected backlog tasks, in their current relative
  // order, at the top or bottom of the backlog. A task outside the end
  // band takes that band's priority, so the list never shows an order
  // Grain would not follow.
  async moveToEnd(
    ids: readonly string[],
    end: "top" | "bottom",
  ): Promise<{ raised: number; undo: Undo } | null> {
    const backlog = this.backlog();
    const picked = backlog.filter((t) => ids.includes(t.id));
    const rest = backlog.filter((t) => !ids.includes(t.id));
    if (picked.length === 0) return null;
    const edge = end === "top" ? rest[0] : rest[rest.length - 1];
    const target = edge ? priorityOf(edge) : null;
    const before = new Map(picked.map((t) => [t.id, priorityOf(t)]));
    // Undo puts each picked task back after the task that preceded it,
    // in the original order, so earlier restored picks anchor later ones.
    const order = backlog.map((t) => t.id);
    const needs = picked.filter(
      (t) =>
        target !== null &&
        (end === "top" ? priorityOf(t) < target : priorityOf(t) > target),
    );
    const ok = await this.run(`move:${end}`, async () => {
      await Promise.all(
        needs.map((t) =>
          this.api.patch(
            "PATCH /api/tasks/{id}",
            { id: t.id },
            { priority: target },
          ),
        ),
      );
      await this.api.post("POST /api/tasks/reorder", undefined, {
        ids: picked.map((t) => t.id),
        afterId: end === "bottom" ? (edge?.id ?? "") : "",
        beforeId: end === "top" ? (edge?.id ?? "") : "",
      });
    });
    if (!ok) return null;
    return {
      raised: needs.length,
      undo: {
        label: "Undo",
        run: async () => {
          await Promise.all(
            needs.map((t) =>
              this.api.patch(
                "PATCH /api/tasks/{id}",
                { id: t.id },
                { priority: before.get(t.id) },
              ),
            ),
          );
          for (const t of picked) {
            const i = order.indexOf(t.id);
            const after = i > 0 ? order[i - 1] : "";
            const before = after
              ? ""
              : (order.slice(i + 1).find((id) => !ids.includes(id)) ?? "");
            await this.api.post("POST /api/tasks/reorder", undefined, {
              ids: [t.id],
              afterId: after,
              beforeId: before,
            });
          }
          await this.refresh();
        },
      },
    };
  }

  find(id: string): Task | undefined {
    return (
      this.live().find((t) => t.id === id) ??
      this.closed().find((t) => t.id === id)
    );
  }

  // fanOut makes one call per task (there are no batch endpoints), reads
  // the list once, and returns how many did not go through.
  private async fanOut(
    key: string,
    ids: readonly string[],
    call: (id: string) => Promise<unknown>,
  ): Promise<number> {
    this.mark(key, true);
    this.actionError.set(null);
    const results = await Promise.allSettled(ids.map(call));
    const failures = results.filter(
      (r): r is PromiseRejectedResult => r.status === "rejected",
    );
    if (failures.length) {
      this.actionError.set(
        ids.length === 1
          ? errorText(failures[0].reason)
          : `${failures.length} of ${ids.length} did not go through: ${errorText(failures[0].reason)}`,
      );
    }
    await this.refresh();
    void this.inbox.refresh();
    if (key === "close" || key === "reopen") void this.loadClosed();
    this.mark(key, false);
    return failures.length;
  }

  private async run(key: string, fn: () => Promise<unknown>): Promise<boolean> {
    this.mark(key, true);
    this.actionError.set(null);
    try {
      await fn();
      return true;
    } catch (err) {
      this.actionError.set(errorText(err));
      return false;
    } finally {
      await this.refresh();
      void this.inbox.refresh();
      this.mark(key, false);
    }
  }

  private mark(key: string, on: boolean): void {
    this.busy.update((b) => {
      const next = new Set(b);
      if (on) next.add(key);
      else next.delete(key);
      return next;
    });
  }
}
