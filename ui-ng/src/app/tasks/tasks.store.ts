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
  OnDestroy,
  computed,
  effect,
  inject,
  signal,
  untracked,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import { InboxStore } from "../inbox/inbox.store";
import type { Task, TaskDetail } from "../api/models";
import { TaskFeedStore } from "../services/task-feed.service";
import { isListCiPlaceholder } from "../shared/gates";
import { errorText } from "../sessions/session-view";
import {
  type MovePlan,
  type TabId,
  TABS,
  inTab,
  isBlocked,
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
export class TasksStore implements OnDestroy {
  private readonly api = inject(GrainApi);
  private readonly events = inject(GrainEvents);
  private readonly zone = inject(NgZone);
  private readonly feed = inject(TaskFeedStore);
  // The shell's list feeds Next up and the Inbox badge; it is told to
  // read again after every action here so neither lags behind.
  private readonly inbox = inject(InboxStore);

  readonly live = this.feed.tasks;
  readonly details = signal<Record<string, TaskDetail>>({});
  readonly closed = signal<Task[]>([]);
  readonly closedCursor = signal<string | null>(null);
  readonly closedLoaded = signal(false);
  readonly closedTotal = signal<number | null>(null);
  private readonly ownLoaded = signal(false);
  readonly loaded = computed(() => this.ownLoaded() || this.feed.loaded());
  readonly loadError = signal<string | null>(null);
  readonly actionError = signal<string | null>(null);
  readonly busy = signal<ReadonlySet<string>>(new Set());

  readonly work = computed(() => this.live().filter(isWork));
  readonly backlog = computed(() =>
    this.work().filter((t) => t.state === "queued"),
  );
  // ready is the backlog a free worker could take now: blocked tasks
  // wait on a dependency and are not counted.
  readonly ready = computed(() => this.backlog().filter((t) => !isBlocked(t)));
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

  private eventSub: { unsubscribe(): void } | null = null;
  private refreshing: Promise<void> | null = null;
  private eventTimer: ReturnType<typeof setTimeout> | null = null;
  private queuedRefresh: Promise<void> | null = null;
  private readonly detailInFlight = new Set<string>();
  private started = false;

  constructor() {
    effect(() => {
      const rev = this.feed.revision();
      const err = this.feed.loadError();
      const feedLoaded = this.feed.loaded();
      untracked(() => {
        if (err) {
          if (this.started) {
            this.loadError.set(
              `The task list could not be read: ${err}. This page tries again on its own.`,
            );
          }
          return;
        }
        if (rev === 0 && !feedLoaded) return;
        this.loadError.set(null);
        this.ownLoaded.set(true);
        if (this.started) {
          void this.ensureSubmitDetails(this.live());
        }
      });
    });
  }

  start(): void {
    const wasStarted = this.started;
    if (!this.started) {
      this.started = true;
      this.feed.start(false);
      this.feed.acquireFast();
    }
    if (!wasStarted && this.feed.loaded()) {
      void this.refresh();
    } else if (this.feed.loaded()) {
      void this.ensureSubmitDetails(this.live());
    }
    // TaskFeedStore owns task.* SSE refreshes and the 3s fast poll; here we
    // only wake submit-gate detail checks on non-task events (e.g. ci.*).
    this.eventSub ??= this.events.events().subscribe((ev) => {
      if (!ev.type || ev.type.startsWith("task.")) return;
      if (this.eventTimer) return;
      this.zone.runOutsideAngular(() => {
        this.eventTimer = setTimeout(() => {
          this.eventTimer = null;
          this.zone.run(() => void this.ensureSubmitDetails(this.live()));
        }, 250);
      });
    });
  }

  stop(): void {
    if (this.started) {
      this.started = false;
      this.feed.releaseFast();
      this.feed.stop(false);
    }
    if (this.eventTimer) {
      clearTimeout(this.eventTimer);
      this.eventTimer = null;
    }
    this.eventSub?.unsubscribe();
    this.eventSub = null;
  }

  ngOnDestroy(): void {
    this.stop();
  }

  refresh(): Promise<void> {
    if (!this.refreshing) {
      this.refreshing = this.load().finally(() => {
        this.refreshing = null;
      });
      return this.refreshing;
    }
    return (this.queuedRefresh ??= this.refreshing
      .catch(() => undefined)
      .then(() => {
        this.queuedRefresh = null;
        return this.refresh();
      }));
  }

  private async load(): Promise<void> {
    const ok = await this.feed.refresh();
    if (ok) {
      this.loadError.set(null);
    } else {
      this.loadError.set(
        `The task list could not be read: ${this.feed.loadError() ?? "The request failed."}. This page tries again on its own.`,
      );
    }
    this.ownLoaded.set(true);
    if (ok) await this.ensureSubmitDetails(this.live());
  }

  private async ensureSubmitDetails(tasks: readonly Task[]): Promise<void> {
    const liveIds = new Set(tasks.map((t) => t.id));
    const cur = this.details();
    if (Object.keys(cur).some((id) => !liveIds.has(id))) {
      this.details.update((m) => {
        const next: Record<string, TaskDetail> = {};
        for (const id of Object.keys(m)) {
          if (liveIds.has(id)) next[id] = m[id];
        }
        return next;
      });
    }
    const ids = tasks
      .filter(
        (t) =>
          isWork(t) &&
          !this.detailInFlight.has(t.id) &&
          (t.state === "awaiting_submit" ||
            t.reason?.reason === "base_repair" ||
            t.reason?.reason === "awaiting_submit" ||
            !!t.mergeGates?.some(isListCiPlaceholder)),
      )
      .map((t) => t.id)
      .slice(0, 20);
    await Promise.all(ids.map((id) => this.readSubmitDetail(id)));
  }

  private async readSubmitDetail(id: string): Promise<void> {
    this.detailInFlight.add(id);
    try {
      const d = await this.api.getIfChanged<TaskDetail>(
        "GET /api/tasks/{id}",
        { id },
        "comments_limit=1&attempts_limit=1",
      );
      if (d?.body) {
        this.details.update((m) => ({ ...m, [id]: d.body! }));
      }
    } catch {
      // Leave cached/list gates when detail fails.
    } finally {
      this.detailInFlight.delete(id);
    }
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
