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
  inject,
  signal,
} from "@angular/core";
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
import { TaskFeedStore } from "../services/task-feed.service";
import { versionLabel } from "../api/version";
import { ViewFilter } from "../services/view-filter.service";
import {
  type FilterId,
  type InboxAction,
  type InboxEntry,
  QUIET_GROUPS,
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
export const LIVE_TRANSCRIPT_POLL_MS = 1500;

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
export class InboxStore implements OnDestroy {
  private readonly api = inject(GrainApi);
  private readonly events = inject(GrainEvents);
  private readonly access = inject(AccessService);
  private readonly feed = inject(TaskFeedStore);
  private readonly zone = inject(NgZone);

  readonly tasks = this.feed.tasks;
  readonly notifications = signal<Notification[]>([]);
  readonly localScripts = signal<LocalScriptApproval[]>([]);
  readonly loaded = signal(false);
  readonly loadError = signal<string | null>(null);
  readonly actionError = signal<string | null>(null);
  readonly busy = signal<string | null>(null);
  readonly filter = signal<FilterId>("all");
  readonly query = inject(ViewFilter).query;
  readonly wsTaskIds = signal<ReadonlySet<string> | null>(null);
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

  // liveEntries is the queue as the server last said it is. entries is
  // what the list shows: the same, except that while the person has a
  // row selected (or focus in the list) existing rows keep their order
  // and rows that left stay, greyed, until the buffered updates are
  // applied ("N updates").
  readonly liveEntries = computed(() =>
    this.withStoppers(
      buildEntries(
        this.tasks(),
        this.notifications(),
        this.localScripts(),
        this.details(),
      ),
    ),
  );
  readonly scopedLiveEntries = computed(() => {
    const ids = this.wsTaskIds();
    const live = this.liveEntries();
    return ids ? live.filter((e) => !!e.taskId && ids.has(e.taskId)) : live;
  });
  // decisions is what is waiting on a person across the entire Inbox (used by
  // the global rail badge). scopedDecisions tracks the current ?ws= scope.
  readonly decisions = computed(() =>
    this.liveEntries().filter((e) => !QUIET_GROUPS.has(e.groupId)),
  );
  readonly scopedDecisions = computed(() =>
    this.scopedLiveEntries().filter((e) => !QUIET_GROUPS.has(e.groupId)),
  );
  private readonly frozen = signal<readonly InboxEntry[] | null>(null);
  // The last version of every row seen, so a row that left still shows
  // what it was.
  private readonly lastSeen = new Map<string, InboxEntry>();
  readonly entries = computed<InboxEntry[]>(() => {
    const live = this.scopedLiveEntries();
    const held = this.frozen();
    if (!held) return live;
    const ids = this.wsTaskIds();
    const byKey = new Map(live.map((e) => [e.key, e]));
    return held
      .filter((e) => !ids || (!!e.taskId && ids.has(e.taskId)))
      .map(
        (e) =>
          byKey.get(e.key) ?? {
            ...(this.lastSeen.get(e.key) ?? e),
            gone: true,
            actions: [],
          },
      );
  });
  // pendingUpdates is how many changes the held order is not showing:
  // rows that arrived, rows that left, and rows that moved.
  readonly pendingUpdates = computed(() => {
    const rawHeld = this.frozen();
    if (!rawHeld) return 0;
    const ids = this.wsTaskIds();
    const held = ids
      ? rawHeld.filter((e) => !!e.taskId && ids.has(e.taskId))
      : rawHeld;
    const live = this.scopedLiveEntries();
    const heldKeys = new Set(held.map((e) => e.key));
    const liveKeys = new Set(live.map((e) => e.key));
    const added = live.filter((e) => !heldKeys.has(e.key)).length;
    const removed = held.filter((e) => !liveKeys.has(e.key)).length;
    const a = held.map((e) => e.key).filter((k) => liveKeys.has(k));
    const b = live.map((e) => e.key).filter((k) => heldKeys.has(k));
    const n = a.length;
    const dp = new Array<number>(n + 1).fill(0);
    for (const k of b) {
      let prev = 0;
      for (let j = 1; j <= n; j++) {
        const tmp = dp[j];
        dp[j] = k === a[j - 1] ? prev + 1 : Math.max(dp[j], dp[j - 1]);
        prev = tmp;
      }
    }
    const moved = n - dp[n];
    return added + removed + moved;
  });
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
    const all = this.scopedLiveEntries();
    const n = (f: FilterId) =>
      all.filter((e) => matchesFilter(e, f, seen)).length;
    return {
      all: all.length,
      asked: n("asked"),
      proposals: n("proposals"),
      merges: n("merges"),
      approvals: n("approvals"),
      alerts: n("alerts"),
      paused: n("paused"),
      unread: n("unread"),
    } as Record<FilterId, number>;
  });
  // selected is pinned to the row the person chose. When that row leaves
  // the queue it is still shown, read-only (gone), with why it left; it
  // never falls back to another row. Only with nothing chosen yet does
  // the first row stand in.
  readonly selected = computed<InboxEntry | null>(() => {
    const key = this.selectedKey();
    const list = this.ordered();
    if (key === null) return list[0] ?? null;
    const hit = list.find((e) => e.key === key);
    if (hit && !hit.gone) return hit;
    const last = hit ?? this.lastSeen.get(key) ?? this.picked;
    return last ? { ...last, gone: true, actions: [] } : null;
  });
  // Where the selected row was in the list, so Next item and j/k carry
  // on from there after it has gone.
  private lastIndex = 0;
  // The row as it was when chosen, for a row that leaves before any
  // refresh has recorded it.
  private picked: InboxEntry | null = null;
  // What this page last did to a task, so a row it answered says so.
  private readonly answered = new Map<string, string>();
  private readonly detailInFlight = new Set<string>();
  answeredWith(taskId: string): string | undefined {
    return this.answered.get(taskId);
  }
  readonly selectedDetail = computed<TaskDetail | null>(() => {
    const id = this.selected()?.taskId;
    return id ? (this.details()[id] ?? null) : null;
  });
  readonly selectedTranscript = signal<string | null>(null);
  private transcriptTimer: ReturnType<typeof setInterval> | null = null;
  private transcriptKey = "";
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
  private queuedRefresh: Promise<void> | null = null;
  private eventSub: { unsubscribe(): void } | null = null;
  private started = false;

  constructor() {
    this.feed.onBeforeChange(() => this.remember());
  }

  start(): void {
    if (!this.started) {
      this.started = true;
      this.feed.start(false);
    }
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
    if (this.started) {
      this.started = false;
      this.feed.stop(false);
    }
    this.stopSelectedTranscript();
    if (this.debounce) {
      clearTimeout(this.debounce);
      this.debounce = null;
    }
    this.eventSub?.unsubscribe();
    this.eventSub = null;
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
  }

  ngOnDestroy(): void {
    this.stop();
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

  // refresh reads everything again. Concurrent calls wait on a fresh read
  // started after the active one finishes so callers never observe stale
  // pre-mutation state.
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
    this.remember();
    const needWhoami =
      !this.access.whoami() && typeof this.api.whoami === "function";
    const [config, feedOk, notifications, scripts, who] =
      await Promise.allSettled([
        this.api.config(),
        this.feed.refresh(),
        this.api.openNotifications(),
        this.access.can("GET /api/local-script-approvals")
          ? this.api.localScriptApprovals()
          : Promise.resolve([] as LocalScriptApproval[]),
        needWhoami
          ? this.api.whoami().catch(() => null)
          : Promise.resolve(null),
      ]);
    if (config.status === "fulfilled") {
      this.access.setConfig(config.value);
      this.version.set(versionLabel(config.value.version));
    }
    if (who.status === "fulfilled" && who.value) {
      this.access.setWhoami(who.value);
    }
    if (feedOk.status === "fulfilled" && feedOk.value) {
      this.loadError.set(null);
    } else {
      this.loadError.set("Could not read tasks; retrying.");
    }
    if (notifications.status === "fulfilled") {
      this.notifications.set(notifications.value);
    }
    if (scripts.status === "fulfilled") this.localScripts.set(scripts.value);
    this.loaded.set(true);
    this.trackIndex();
    await Promise.all([
      this.ensureDetail(),
      this.ensureQueueDetails(),
      this.ensureDiff(),
    ]);
    this.syncSelectedTranscript();
  }

  // ensureDiff reads the selected change's diff preview, unless one read
  // in the last DIFF_TTL_MS is already held.
  async ensureDiff(): Promise<void> {
    const e = this.selected();
    if (!wantsDiff(e) || e?.gone) return;
    const id = e!.taskId as string;
    const at = this.diffAt.get(id);
    if (at !== undefined && Date.now() - at < DIFF_TTL_MS) return;
    this.diffAt.set(id, Date.now());
    let preview: DiffPreview | null = null;
    if (this.access.can("GET /api/tasks/{id}/diff-preview", e!.repo)) {
      preview = await this.api
        .diffPreview(id, DIFF_PREVIEW_HUNKS)
        .catch(() => null);
    }
    this.diffs.update((m) => ({ ...m, [id]: preview }));
  }

  private remember(): void {
    for (const e of this.liveEntries()) this.lastSeen.set(e.key, e);
    // Only rows still shown, held or selected are worth keeping.
    const selKey = this.selectedKey();
    const keepTasks = new Set<string>();
    const keep = new Set<string>(selKey ? [selKey] : []);
    const mark = (e: InboxEntry | null | undefined) => {
      if (!e) return;
      keep.add(e.key);
      if (e.taskId) keepTasks.add(e.taskId);
    };
    this.liveEntries().forEach(mark);
    this.frozen()?.forEach(mark);
    mark(
      selKey === null
        ? null
        : (this.lastSeen.get(selKey) ??
            (this.picked?.key === selKey ? this.picked : null)),
    );
    for (const k of this.lastSeen.keys()) {
      if (!keep.has(k)) this.lastSeen.delete(k);
    }
    if (Object.keys(this.details()).some((id) => !keepTasks.has(id))) {
      this.details.update((m) =>
        Object.fromEntries(
          Object.entries(m).filter(([id]) => keepTasks.has(id)),
        ),
      );
    }
    for (const id of this.answered.keys()) {
      if (!keepTasks.has(id)) this.answered.delete(id);
    }
  }

  private trackIndex(): void {
    const key = this.selected()?.key;
    const i = this.ordered().findIndex((e) => e.key === key && !e.gone);
    if (i >= 0) this.lastIndex = i;
  }

  // ---------- held order ----------

  // hold keeps the rows in the order shown until applyUpdates.
  hold(): void {
    if (!this.frozen()) this.frozen.set(this.liveEntries());
  }

  // release lets the list follow the server again, unless a row is
  // selected (which keeps it held).
  release(): void {
    if (this.selectedKey() === null) this.frozen.set(null);
  }

  // applyUpdates shows the queue as it is now and, while something is
  // selected, holds that new order.
  applyUpdates(): void {
    this.frozen.set(this.selectedKey() === null ? null : this.liveEntries());
    this.trackIndex();
  }

  // ---------- who stopped a turn ----------

  // withStoppers names who stopped each paused turn, once the task's
  // detail (its stop comment) has been read.
  private withStoppers(list: InboxEntry[]): InboxEntry[] {
    const details = this.details();
    return list.map((e) => {
      if (e.kind !== "stopped" || !e.taskId) return e;
      const who = stopperOf(details[e.taskId], e.task?.questionCommentId);
      if (!who) return e;
      return {
        ...e,
        summary: `Stopped by ${who}.`,
        actions: e.actions.map((a) =>
          a.id === "resume"
            ? { ...a, confirm: `Resume #${e.taskId}? ${who} stopped it.` }
            : a,
        ),
      };
    });
  }

  private async ensureQueueDetails(): Promise<void> {
    const cached = this.details();
    const ids = new Set<string>();
    for (const e of this.liveEntries()) {
      if (!e.taskId || this.detailInFlight.has(e.taskId)) continue;
      if (e.kind === "stopped" && !cached[e.taskId]) ids.add(e.taskId);
      else if (
        e.kind === "submit" ||
        e.kind === "review" ||
        e.kind === "base-repair"
      ) {
        ids.add(e.taskId);
      }
    }
    await Promise.all([...ids].slice(0, 20).map((id) => this.readDetail(id)));
  }

  private async readDetail(id: string): Promise<void> {
    this.detailInFlight.add(id);
    try {
      const d = await this.api.task(id);
      this.details.update((m) => ({ ...m, [id]: d }));
    } catch {
      // The row still shows its summary; the inspector says less.
    } finally {
      this.detailInFlight.delete(id);
    }
  }

  // ensureDetail reads the selected task's detail: conversation thread,
  // attempts, the question it is parked on, and any access request.
  async ensureDetail(force = false): Promise<void> {
    const e = this.selected();
    if (!e?.taskId || e.gone) return;
    if (!force && this.details()[e.taskId]) {
      this.syncSelectedTranscript();
      return;
    }
    await this.readDetail(e.taskId);
    this.syncSelectedTranscript();
  }

  private stopSelectedTranscript(): void {
    if (this.transcriptTimer) {
      clearInterval(this.transcriptTimer);
      this.transcriptTimer = null;
    }
    this.transcriptKey = "";
    this.selectedTranscript.set(null);
  }

  private syncSelectedTranscript(): void {
    const e = this.selected();
    const d = this.selectedDetail();
    const taskId = e?.taskId;
    const running =
      !!taskId &&
      !e?.gone &&
      (e?.task?.state === "running" || d?.state === "running");
    if (!running || !taskId) {
      this.stopSelectedTranscript();
      return;
    }
    const repo = e?.repo || d?.repo || undefined;
    if (
      !this.access.can("GET /api/tasks/{id}/attempts/{number}/transcript", repo)
    ) {
      this.stopSelectedTranscript();
      return;
    }
    const liveAttempt = [...(d?.attempts ?? [])]
      .sort((a, b) => b.number - a.number)
      .find((a) => !a.finishedAt);
    const attemptNo = liveAttempt?.number;
    if (!attemptNo) {
      this.stopSelectedTranscript();
      return;
    }
    const key = `${taskId}:${attemptNo}`;
    if (this.transcriptKey === key && this.transcriptTimer) return;
    this.stopSelectedTranscript();
    this.transcriptKey = key;
    void this.pollSelectedTranscript(taskId, attemptNo, key);
    this.zone.runOutsideAngular(() => {
      this.transcriptTimer = setInterval(
        () =>
          this.zone.run(
            () => void this.pollSelectedTranscript(taskId, attemptNo, key),
          ),
        LIVE_TRANSCRIPT_POLL_MS,
      );
    });
  }

  private async pollSelectedTranscript(
    taskId: string,
    attemptNo: number,
    key: string,
  ): Promise<void> {
    if (typeof this.api.get !== "function") return;
    try {
      const r = await this.api.get<{ transcript?: string }>(
        "GET /api/tasks/{id}/attempts/{number}/transcript",
        { id: taskId, number: String(attemptNo) },
      );
      if (this.transcriptKey === key) {
        this.selectedTranscript.set(r?.transcript ?? "");
      }
    } catch {
      // Transient transcript errors leave existing partial output intact.
    }
  }

  // select pins a row. Choosing a row holds the list's order, so nothing
  // moves under the person while they read or type.
  select(key: string): void {
    const entry = this.entries().find((e) => e.key === key) ?? null;
    if (entry && !entry.gone) this.picked = entry;
    this.selectedKey.set(key);
    this.hold();
    this.trackIndex();
    this.sheetOpen.set(true);
    this.markSeen(key);
    void this.ensureDetail(true);
    void this.ensureDiff();
  }

  // next selects the row that now stands where the selected one was (or
  // after it): the explicit move off a row that has gone.
  next(): void {
    this.applyUpdates();
    const list = this.ordered().filter((e) => !e.gone);
    if (list.length === 0) return;
    const sel = this.selected();
    const cur = list.findIndex((e) => e.key === sel?.key);
    const i = cur >= 0 ? cur + 1 : this.lastIndex;
    this.select(list[Math.min(i, list.length - 1)].key);
  }

  closeSheet(): void {
    this.sheetOpen.set(false);
  }

  step(delta: number): void {
    const list = this.ordered();
    if (list.length === 0) return;
    if (this.selectedKey() === null) {
      this.select(list[0].key);
      return;
    }
    const sel = this.selected();
    const cur = list.findIndex((e) => e.key === sel?.key);
    let i: number;
    if (cur >= 0) i = cur + delta;
    // Off a row that is no longer listed: forward lands on the row that
    // took its place, back on the one before it.
    else if (sel) i = delta > 0 ? this.lastIndex : this.lastIndex - 1;
    else i = 0;
    const next = list[Math.max(0, Math.min(list.length - 1, i))];
    this.select(next.key);
  }

  markSeen(key: string): void {
    if (this.seen().has(key)) return;
    const next = new Set(this.seen());
    next.add(key);
    // Only keys still in the queue are worth remembering.
    const live = new Set(this.liveEntries().map((e) => e.key));
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

  // Re-reads the queue and verifies the entry, action, merge gates, and
  // access still hold before posting (server 409 preconditions close the
  // remaining TOCTOU window).
  async actByKey(
    entryKey: string,
    actionOrId: string | InboxAction,
    body?: unknown,
  ): Promise<boolean> {
    if (typeof actionOrId !== "string" && actionOrId.blocked) {
      this.actionError.set(actionOrId.blocked);
      return false;
    }
    this.busy.set(typeof actionOrId === "string" ? actionOrId : actionOrId.id);
    this.actionError.set(null);
    try {
      await this.refresh();
      const loadErr = this.loadError();
      if (loadErr) {
        this.actionError.set(loadErr);
        return false;
      }
      const entry = this.liveEntries().find((e) => e.key === entryKey);
      const action =
        typeof actionOrId === "string"
          ? entry?.actions.find((a) => a.id === actionOrId)
          : actionOrId;
      if (!entry || !action) {
        this.actionError.set("This item moved on.");
        return false;
      }
      if (action.blocked) {
        this.actionError.set(action.blocked);
        return false;
      }
      if (!this.access.can(action.route, action.repo)) {
        this.actionError.set("No permission for this action.");
        return false;
      }
      await this.api.post(action.route, action.params, body ?? action.body);
      this.noteAnswered(action);
      await this.refresh();
      // The person's own answer applies at once: the row they answered
      // leaves, and the list shows what is waiting now.
      this.applyUpdates();
      return true;
    } catch (err) {
      this.actionError.set(errorText(err));
      return false;
    } finally {
      this.busy.set(null);
    }
  }

  private noteAnswered(a: InboxAction): void {
    const id = (a.params as { id?: string } | undefined)?.id;
    if (id && a.route.startsWith("POST /api/tasks/{id}/")) {
      this.answered.set(String(id), a.label);
    }
  }

  // actAll gives every checked row (or the given keys) that offers it the
  // same answer after reading the queue fresh. Keys that moved on or
  // turned blocked are skipped, never replaced by another. Rows that
  // failed stay selected.
  async actAll(
    actionId: string,
    keys?: readonly string[],
  ): Promise<{
    count: number;
    failed: number;
    undo: { route: RouteKey; ids: string[] } | null;
  }> {
    const wantKeys = new Set(keys ?? [...this.checked()]);
    this.busy.set(actionId);
    this.actionError.set(null);
    try {
      await this.refresh();
      const loadErr = this.loadError();
      if (loadErr) {
        this.actionError.set(loadErr);
        return { count: 0, failed: 0, undo: null };
      }
      const pool = this.scopedLiveEntries().filter((e) => wantKeys.has(e.key));
      const targets = pool
        .map((e) => ({ e, a: e.actions.find((x) => x.id === actionId) }))
        .filter(
          (t): t is { e: InboxEntry; a: InboxAction } =>
            !!t.a && !t.a.blocked && this.access.can(t.a.route, t.a.repo),
        );
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
        this.noteAnswered(a);
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
          `${failed} of ${targets.length} failed; they stay selected.`,
        );
      }
      this.checked.update((c) => new Set([...c].filter((k) => !landed.has(k))));
      await this.refresh();
      this.applyUpdates();
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
      for (const id of taskIds) this.answered.delete(id);
      await this.refresh();
      this.applyUpdates();
      if (failedIds.length) {
        const which = failedIds.map((id) => `#${id}`).join(", ");
        const tail =
          "(a task that has started cannot be withdrawn; close it to cancel).";
        const msg =
          failedIds.length === taskIds.length
            ? `Could not undo: ${which} ${failedIds.length === 1 ? "has" : "have"} moved on ${tail}`
            : `Undid ${taskIds.length - failedIds.length} of ${taskIds.length}; ${which} could not be undone ${tail}`;
        this.actionError.set(msg);
        if (failedIds.length === taskIds.length) throw new Error(msg);
      }
    } finally {
      this.busy.set(null);
    }
  }

  // reply posts to the task the person was typing to, and only while it
  // still waits on the question they saw: the task is read directly
  // before posting, and a read error or changed state refuses. Server 409
  // preconditions on questionCommentId close the remaining race.
  async reply(
    taskId: string,
    body: string,
    refuse: (t: Task | undefined) => string,
  ): Promise<boolean> {
    this.busy.set("reply");
    this.actionError.set(null);
    try {
      let fresh: TaskDetail;
      try {
        fresh = await this.api.task(taskId);
      } catch (err) {
        this.actionError.set(`Could not verify #${taskId}: ${errorText(err)}`);
        return false;
      }
      this.details.update((m) => ({ ...m, [taskId]: fresh }));
      const why = refuse(fresh);
      if (why) {
        this.actionError.set(why);
        return false;
      }
      await this.api.post(
        "POST /api/tasks/{id}/comments",
        { id: taskId },
        { body },
      );
      this.answered.set(taskId, "reply");
      await this.refresh();
      this.applyUpdates();
      return true;
    } catch (err) {
      this.actionError.set(errorText(err));
      return false;
    } finally {
      this.busy.set(null);
    }
  }

  // talkToTask posts a comment to a task-backed Inbox item and, when wake is
  // true, wakes an agent run on that task/PR branch (clearing CompletedAt via
  // run-now on awaiting_submit/completed items, falling back to retry on
  // failed items without dispatch:write, or passing reopen:true on closed).
  async talkToTask(
    taskId: string,
    body: string,
    wake: boolean,
    refuse: (t: TaskDetail | undefined) => string,
  ): Promise<{ ok: boolean; woke: boolean }> {
    this.busy.set(wake ? "wake" : "comment");
    this.actionError.set(null);
    try {
      let fresh: TaskDetail;
      try {
        fresh = await this.api.task(taskId);
      } catch (err) {
        this.actionError.set(`Could not verify #${taskId}: ${errorText(err)}`);
        return { ok: false, woke: false };
      }
      this.details.update((m) => ({ ...m, [taskId]: fresh }));
      const why = refuse(fresh);
      if (why) {
        this.actionError.set(why);
        return { ok: false, woke: false };
      }
      const repo = fresh.repo || undefined;
      if (!this.access.can("POST /api/tasks/{id}/comments", repo)) {
        this.actionError.set("No permission to comment on this task.");
        return { ok: false, woke: false };
      }

      let woke = false;
      if (wake && fresh.state === "closed") {
        await this.api.post(
          "POST /api/tasks/{id}/comments",
          { id: taskId },
          { body, reopen: true },
        );
        woke = true;
      } else {
        await this.api.post(
          "POST /api/tasks/{id}/comments",
          { id: taskId },
          { body },
        );
        if (!wake) {
          await this.readDetail(taskId);
          await this.refresh();
          return { ok: true, woke: false };
        }
        if (
          fresh.state === "awaiting_reply" &&
          !fresh.turnStopped &&
          fresh.reason?.reason !== "awaiting_reply/stopped" &&
          !fresh.pendingSecret &&
          !fresh.pendingAccess &&
          !fresh.pendingConfigChange
        ) {
          woke = true;
        } else if (fresh.state === "running" || fresh.state === "queued") {
          woke = true;
        } else if (this.access.can("POST /api/tasks/{id}/run-now", repo)) {
          await this.api.post("POST /api/tasks/{id}/run-now", { id: taskId });
          woke = true;
        } else if (
          (fresh.state === "failed" ||
            fresh.turnStopped ||
            fresh.reason?.reason === "awaiting_reply/stopped") &&
          this.access.can("POST /api/tasks/{id}/retry", repo)
        ) {
          await this.api.post("POST /api/tasks/{id}/retry", { id: taskId });
          woke = true;
        }
      }

      if (woke) {
        this.answered.set(taskId, "woke agent");
      }
      await this.readDetail(taskId);
      await this.refresh();
      if (woke) {
        this.applyUpdates();
      }
      return { ok: true, woke };
    } catch (err) {
      this.actionError.set(errorText(err));
      return { ok: false, woke: false };
    } finally {
      this.busy.set(null);
    }
  }
}

// stopperOf names who stopped a turn: the author of the stop comment the
// task is parked on.
export function stopperOf(
  d: TaskDetail | undefined,
  questionCommentId: number | undefined,
): string {
  if (!d || !questionCommentId) return "";
  const c = d.comments?.find((x) => x.id === questionCommentId);
  return c?.authorLabel || c?.author || "";
}
