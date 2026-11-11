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
import type { RouteKey } from "../api/generated-routes";
import type {
  DiffPreview,
  LocalScriptApproval,
  Notification,
  Task,
  TaskDetail,
} from "../api/models";
import { AccessService } from "../services/access.service";
import { versionLabel } from "../api/version";
import { ViewFilter } from "../services/view-filter.service";
import {
  type FilterId,
  type InboxAction,
  type InboxEntry,
  buildEntries,
  groupEntries,
  matchesFilter,
  matchesQuery,
} from "./inbox-classifier";

// The call that takes back an answer, by the route that gave it.
const INVERSE: Partial<Record<RouteKey, RouteKey>> = {
  "POST /api/tasks/{id}/approve": "POST /api/tasks/{id}/withdraw-approval",
  "POST /api/tasks/{id}/defer": "POST /api/tasks/{id}/undefer",
  "POST /api/tasks/{id}/close": "POST /api/tasks/{id}/reopen",
};

export const SEEN_STORAGE_KEY = "grain.next.inbox.seen";
// POLL_MS backs the event stream up. Tasks, notifications and
// host-script approvals all have events (GRAIN_EVENT_TYPES), so the
// timer only catches what the stream cannot say: a dropped connection,
// or a host-script call that ran past its deadline unanswered.
export const POLL_MS = 60000;

// DIFF_KINDS are the entries whose decision is about a change, so the
// inspector shows its diff beside them; any other task entry shows one
// only when its task already has a pull request.
const DIFF_KINDS = new Set<string>([
  "submit",
  "review",
  "merge-blocked",
  "github-approval",
  "merge-stalled",
]);
export const DIFF_PREVIEW_HUNKS = 6;
// DIFF_TTL_MS is how long a preview is shown before selecting its row
// reads it again.
const DIFF_TTL_MS = 60000;

export function wantsDiff(e: InboxEntry | null): boolean {
  if (!e?.taskId) return false;
  return DIFF_KINDS.has(e.kind) || !!e.task?.pullRequest;
}

function loadSeen(): Set<string> {
  try {
    const raw = localStorage.getItem(SEEN_STORAGE_KEY);
    const arr = raw ? (JSON.parse(raw) as unknown) : [];
    return new Set(Array.isArray(arr) ? arr.map(String) : []);
  } catch {
    return new Set();
  }
}

function errorText(err: unknown): string {
  const e = err as { error?: unknown; message?: string; status?: number };
  if (e && typeof e.error === "string" && e.error) return e.error;
  if (e && e.error && typeof e.error === "object") {
    const m =
      (e.error as { error?: string; message?: string }).error ??
      (e.error as { message?: string }).message;
    if (m) return m;
  }
  return e?.message || "The request failed.";
}

// InboxStore is the Inbox's state: what the server says is waiting, how
// the reader has narrowed it, what is selected, and the calls that
// answer it. Everything the page shows is a computed signal over it.
@Injectable({ providedIn: "root" })
export class InboxStore {
  private readonly api = inject(GrainApi);
  private readonly events = inject(GrainEvents);
  private readonly access = inject(AccessService);
  private readonly zone = inject(NgZone);

  readonly tasks = signal<Task[]>([]);
  readonly notifications = signal<Notification[]>([]);
  readonly localScripts = signal<LocalScriptApproval[]>([]);
  readonly loaded = signal(false);
  readonly loadError = signal<string | null>(null);
  readonly actionError = signal<string | null>(null);
  readonly busy = signal<string | null>(null);
  readonly filter = signal<FilterId>("all");
  readonly query = inject(ViewFilter).query;
  readonly selectedKey = signal<string | null>(null);
  readonly checked = signal<ReadonlySet<string>>(new Set());
  readonly seen = signal<ReadonlySet<string>>(loadSeen());
  readonly sheetOpen = signal(false);
  readonly details = signal<Record<string, TaskDetail>>({});
  readonly version = signal("");
  // diffs holds each task's diff preview: null when there is none to
  // show (nothing pushed yet, no comparison on this server, or the
  // reader may not read it).
  readonly diffs = signal<Record<string, DiffPreview | null>>({});
  private readonly diffAt = new Map<string, number>();

  readonly entries = computed(() =>
    buildEntries(this.tasks(), this.notifications(), this.localScripts()),
  );
  readonly visible = computed(() => {
    const f = this.filter();
    const q = this.query();
    const seen = this.seen();
    return this.entries().filter(
      (e) => matchesFilter(e, f, seen) && matchesQuery(e, q),
    );
  });
  readonly groups = computed(() => groupEntries(this.visible()));
  readonly ordered = computed(() => this.groups().flatMap((g) => g.entries));
  readonly counts = computed(() => {
    const seen = this.seen();
    const all = this.entries();
    const n = (f: FilterId) =>
      all.filter((e) => matchesFilter(e, f, seen)).length;
    return {
      all: all.length,
      asked: n("asked"),
      proposals: n("proposals"),
      merges: n("merges"),
      approvals: n("approvals"),
      alerts: n("alerts"),
      unread: n("unread"),
    } as Record<FilterId, number>;
  });
  readonly selected = computed<InboxEntry | null>(() => {
    const key = this.selectedKey();
    const list = this.ordered();
    return list.find((e) => e.key === key) ?? list[0] ?? null;
  });
  readonly selectedDetail = computed<TaskDetail | null>(() => {
    const id = this.selected()?.taskId;
    return id ? (this.details()[id] ?? null) : null;
  });
  // selectedDiff is the selected entry's preview: undefined while it is
  // being read or when the entry is not about a change.
  readonly selectedDiff = computed<DiffPreview | null | undefined>(() => {
    const e = this.selected();
    if (!wantsDiff(e)) return undefined;
    return this.diffs()[e!.taskId as string];
  });
  readonly checkedEntries = computed(() => {
    const c = this.checked();
    return this.entries().filter((e) => c.has(e.key));
  });

  private timer: ReturnType<typeof setInterval> | null = null;
  private refreshing: Promise<void> | null = null;
  private pending = false;
  private eventSub: { unsubscribe(): void } | null = null;

  start(): void {
    void this.refresh();
    if (!this.eventSub) {
      this.eventSub = this.events
        .events()
        .subscribe(() => this.scheduleRefresh());
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
    this.eventSub?.unsubscribe();
    this.eventSub = null;
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
  }

  private debounce: ReturnType<typeof setTimeout> | null = null;
  scheduleRefresh(): void {
    if (this.debounce) return;
    this.zone.runOutsideAngular(() => {
      this.debounce = setTimeout(() => {
        this.debounce = null;
        this.zone.run(() => void this.refresh());
      }, 250);
    });
  }

  // refresh reads everything again. Overlapping calls coalesce into one
  // more read after the one in flight.
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
    const [config, tasks, notifications, scripts] = await Promise.allSettled([
      this.api.config(),
      this.api.tasks(),
      this.api.openNotifications(),
      this.access.can("GET /api/local-script-approvals")
        ? this.api.localScriptApprovals()
        : Promise.resolve([] as LocalScriptApproval[]),
    ]);
    if (config.status === "fulfilled") {
      this.access.setConfig(config.value);
      this.version.set(versionLabel(config.value.version));
    }
    if (tasks.status === "fulfilled") {
      this.tasks.set(Array.isArray(tasks.value) ? tasks.value : []);
      this.loadError.set(null);
    } else {
      this.loadError.set(
        "The task list could not be read, so this page cannot show what is waiting on you yet. It tries again on its own.",
      );
    }
    if (notifications.status === "fulfilled") {
      this.notifications.set(notifications.value);
    }
    if (scripts.status === "fulfilled") this.localScripts.set(scripts.value);
    this.loaded.set(true);
    await Promise.all([this.ensureDetail(), this.ensureDiff()]);
  }

  // ensureDiff reads the selected change's diff preview, unless one read
  // in the last DIFF_TTL_MS is already held.
  async ensureDiff(): Promise<void> {
    const e = this.selected();
    if (!wantsDiff(e)) return;
    const id = e!.taskId as string;
    const at = this.diffAt.get(id);
    if (at !== undefined && Date.now() - at < DIFF_TTL_MS) return;
    this.diffAt.set(id, Date.now());
    let preview: DiffPreview | null = null;
    if (this.access.can("GET /api/tasks/{id}/diff-preview", e!.repo)) {
      try {
        preview = await this.api.diffPreview(id, DIFF_PREVIEW_HUNKS);
      } catch {
        // No branch pushed yet, or no comparison here: the inspector
        // links to the pull request instead.
      }
    }
    this.diffs.update((m) => ({ ...m, [id]: preview }));
  }

  // ensureDetail reads the selected task's detail: the question it is
  // parked on and any access request, which the list does not carry.
  async ensureDetail(force = false): Promise<void> {
    const e = this.selected();
    if (!e?.taskId || (e.kind !== "reply" && e.kind !== "access")) return;
    if (!force && this.details()[e.taskId]) return;
    try {
      const d = await this.api.task(e.taskId);
      this.details.update((m) => ({ ...m, [e.taskId as string]: d }));
    } catch {
      // The row still shows its summary; the inspector says less.
    }
  }

  select(key: string): void {
    this.selectedKey.set(key);
    this.sheetOpen.set(true);
    this.markSeen(key);
    void this.ensureDetail(true);
    void this.ensureDiff();
  }

  closeSheet(): void {
    this.sheetOpen.set(false);
  }

  step(delta: number): void {
    const list = this.ordered();
    if (list.length === 0) return;
    const cur = list.findIndex((e) => e.key === this.selected()?.key);
    const next = list[Math.max(0, Math.min(list.length - 1, cur + delta))];
    this.select(next.key);
  }

  markSeen(key: string): void {
    if (this.seen().has(key)) return;
    const next = new Set(this.seen());
    next.add(key);
    // Only keys still in the queue are worth remembering.
    const live = new Set(this.entries().map((e) => e.key));
    const kept = [...next].filter((k) => live.has(k) || k === key);
    this.seen.set(new Set(kept));
    try {
      localStorage.setItem(SEEN_STORAGE_KEY, JSON.stringify(kept));
    } catch {
      // Storage unavailable: read state lasts for this page only.
    }
  }

  toggleChecked(key: string): void {
    const next = new Set(this.checked());
    if (next.has(key)) next.delete(key);
    else next.add(key);
    this.checked.set(next);
  }

  clearChecked(): void {
    this.checked.set(new Set());
  }

  // act makes one call and reads the queue again; an answered row leaves
  // the queue because the server no longer lists it, never on a guess.
  async act(action: InboxAction, body?: unknown): Promise<boolean> {
    this.busy.set(action.id);
    this.actionError.set(null);
    try {
      await this.api.post(action.route, action.params, body ?? action.body);
      await this.refresh();
      return true;
    } catch (err) {
      this.actionError.set(errorText(err));
      return false;
    } finally {
      this.busy.set(null);
    }
  }

  // actAll gives every checked row (or the given keys) that offers it the
  // same answer and reads the queue once. It reports how many rows went
  // through, and which tasks can be undone and by which call (the inverse
  // of the route used: approve -> withdraw approval, defer -> undefer,
  // close -> reopen). Rows that failed stay selected.
  async actAll(
    actionId: string,
    keys?: readonly string[],
  ): Promise<{
    count: number;
    failed: number;
    undo: { route: RouteKey; ids: string[] } | null;
  }> {
    const pool = keys
      ? this.entries().filter((e) => keys.includes(e.key))
      : this.checkedEntries();
    const targets = pool
      .map((e) => ({ e, a: e.actions.find((x) => x.id === actionId) }))
      .filter(
        (t): t is { e: InboxEntry; a: InboxAction } =>
          !!t.a && this.access.can(t.a.route, t.a.repo),
      );
    this.busy.set(actionId);
    this.actionError.set(null);
    try {
      // Each call is its own promise, so one that throws at once still
      // leaves the others to finish.
      const results = await Promise.allSettled(
        targets.map((t) =>
          Promise.resolve().then(() =>
            this.api.post(t.a.route, t.a.params, t.a.body),
          ),
        ),
      );
      const landed = new Set<string>();
      const undoIds: string[] = [];
      let route: RouteKey | null = null;
      results.forEach((r, i) => {
        if (r.status !== "fulfilled") return;
        const { e, a } = targets[i];
        landed.add(e.key);
        const inverse = INVERSE[a.route];
        const id = (a.params as { id?: string })?.id;
        if (inverse && id && a.route.startsWith("POST /api/tasks/{id}/")) {
          route = inverse;
          undoIds.push(id);
        }
      });
      const failed = results.length - landed.size;
      if (failed > 0) {
        this.actionError.set(
          `${failed} of ${targets.length} did not go through; they stay selected.`,
        );
      }
      this.checked.update((c) => new Set([...c].filter((k) => !landed.has(k))));
      await this.refresh();
      return {
        count: landed.size,
        failed,
        undo: route && undoIds.length ? { route, ids: undoIds } : null,
      };
    } finally {
      this.busy.set(null);
    }
  }

  // undoAll makes the inverse calls and says plainly what could not be
  // undone (the server refuses to withdraw approval from a task that has
  // started, for one). It throws when nothing was undone, so the snackbar
  // reports the failure too.
  async undoAll(route: RouteKey, taskIds: readonly string[]): Promise<void> {
    this.busy.set("undo");
    try {
      const results = await Promise.allSettled(
        taskIds.map((id) =>
          Promise.resolve().then(() => this.api.post(route, { id })),
        ),
      );
      const failedIds = taskIds.filter(
        (_, i) => results[i].status === "rejected",
      );
      await this.refresh();
      if (failedIds.length) {
        const which = failedIds.map((id) => `#${id}`).join(", ");
        const msg =
          failedIds.length === taskIds.length
            ? `Could not undo: ${which} ${failedIds.length === 1 ? "has" : "have"} moved on (a task that has started cannot be withdrawn; close it to cancel).`
            : `Undid ${taskIds.length - failedIds.length} of ${taskIds.length}; ${which} could not be undone (a task that has started cannot be withdrawn; close it to cancel).`;
        this.actionError.set(msg);
        if (failedIds.length === taskIds.length) throw new Error(msg);
      }
    } finally {
      this.busy.set(null);
    }
  }

  async reply(taskId: string, body: string): Promise<boolean> {
    this.busy.set("reply");
    this.actionError.set(null);
    try {
      await this.api.post(
        "POST /api/tasks/{id}/comments",
        { id: taskId },
        { body },
      );
      await this.refresh();
      return true;
    } catch (err) {
      this.actionError.set(errorText(err));
      return false;
    } finally {
      this.busy.set(null);
    }
  }
}
