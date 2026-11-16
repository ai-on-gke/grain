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
  inject,
  signal,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { Task } from "../api/models";
import { ReadTrouble } from "./request-trouble";

// With `/api/events` SSE delivering `task.*` updates immediately, the steady
// heartbeat only guards against silent stream stalls or unstreamed activity
// notes (`15s` = 4 req/min). While reads are failing, `FAILURE_PROBE_MS`
// probes recovery with exponential backoff (3s -> 6s -> 12s -> 24s -> 48s -> 60s).
export const FAST_POLL_MS = 15000;
export const IDLE_POLL_MS = 15000;
export const FAILURE_PROBE_MS = 3000;
const EVENT_COALESCE_MS = 1000;
const CLOCK_TICK_MS = 5000;

function extractError(err: unknown): string {
  const e = err as { error?: unknown; message?: string };
  if (e && typeof e.error === "string" && e.error) return e.error;
  if (e && e.error && typeof e.error === "object") {
    const nested = e.error as { error?: string; message?: string };
    if (nested.error) return nested.error;
    if (nested.message) return nested.message;
  }
  return e?.message || "The request failed.";
}

export function isOpenWorkTask(t: Task): boolean {
  return !t.consoleSession && !t.interactive;
}

// TaskFeedStore is the single shared open-task feed (GET /api/tasks with ETag
// conditional reads + /api/events SSE wakeup) consumed by AppComponent
// (NextUp, live-pill, bottom telemetry), InboxStore, WorkstreamsStore,
// TasksStore, and AgentsStore so no two surfaces ever show contradictory
// running or queued counts on the same tick.
@Injectable({ providedIn: "root" })
export class TaskFeedStore implements OnDestroy {
  private readonly api = inject(GrainApi);
  private readonly events = inject(GrainEvents);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);

  readonly tasks = signal<Task[]>([]);
  readonly loaded = signal(false);
  // loadError is a failure worth a banner: any failure before the list
  // has been read once, and after that only a sustained run of them
  // (request-trouble.ts) -- one failed poll keeps the last good list and
  // says nothing. lastError is every failure, for a caller that needs to
  // know this very read failed.
  readonly loadError = signal<string | null>(null);
  readonly lastError = signal<unknown>(null);
  readonly lastChanged = signal(false);
  readonly revision = signal(0);
  readonly lastUpdatedAt = signal<number | null>(null);
  readonly now = signal<number>(Date.now());

  readonly runningTasks = computed(() =>
    this.tasks().filter((t) => !t.consoleSession && t.state === "running"),
  );
  readonly runningCount = computed(() => this.runningTasks().length);
  readonly queuedTasks = computed(() =>
    this.tasks().filter((t) => isOpenWorkTask(t) && t.state === "queued"),
  );

  private consumers = 0;
  private fastConsumers = 0;
  private readonly beforeChange = new Set<() => void>();
  private eventSub: { unsubscribe(): void } | null = null;
  private pollTimer: ReturnType<typeof setTimeout> | null = null;
  private clockTimer: ReturnType<typeof setInterval> | null = null;
  private debounceTimer: ReturnType<typeof setTimeout> | null = null;
  private lastEventRefreshAt = 0;
  private lastAttemptAt = 0;
  private refreshing: Promise<boolean> | null = null;
  private queuedRefresh: Promise<boolean> | null = null;
  private readonly trouble = new ReadTrouble();

  private readonly onVisibility = (): void => {
    if (this.consumers <= 0) return;
    if (this.doc.hidden) {
      if (this.debounceTimer) {
        clearTimeout(this.debounceTimer);
        this.debounceTimer = null;
      }
      this.clearTimers();
    } else {
      this.now.set(Date.now());
      void this.refresh();
      this.schedulePoll();
      this.startClock();
    }
  };

  start(fast = false): void {
    this.consumers++;
    if (fast) this.fastConsumers++;
    if (this.consumers === 1) {
      this.doc.addEventListener("visibilitychange", this.onVisibility);
      this.eventSub = this.events.events().subscribe((ev) => {
        if (!ev.type || ev.type.startsWith("task.")) {
          this.scheduleEventRefresh();
        }
      });
    }
    if (
      !this.loaded() &&
      !this.refreshing &&
      Date.now() - this.lastAttemptAt >= 1_000
    ) {
      void this.refresh();
    }
    if (!this.doc.hidden) {
      this.schedulePoll();
      this.startClock();
    }
  }

  stop(fast = false): void {
    if (fast && this.fastConsumers > 0) this.fastConsumers--;
    if (this.consumers > 0) this.consumers--;
    if (this.consumers === 0) {
      this.doc.removeEventListener("visibilitychange", this.onVisibility);
      this.eventSub?.unsubscribe();
      this.eventSub = null;
      if (this.debounceTimer) {
        clearTimeout(this.debounceTimer);
        this.debounceTimer = null;
      }
      this.clearTimers();
    } else if (!this.doc.hidden) {
      this.schedulePoll();
    }
  }

  acquireFast(): void {
    this.fastConsumers++;
    if (this.consumers > 0 && !this.doc.hidden) {
      this.schedulePoll();
    }
  }

  releaseFast(): void {
    if (this.fastConsumers > 0) this.fastConsumers--;
    if (this.consumers > 0 && !this.doc.hidden) {
      this.schedulePoll();
    }
  }

  ngOnDestroy(): void {
    this.consumers = 0;
    this.fastConsumers = 0;
    this.doc.removeEventListener("visibilitychange", this.onVisibility);
    this.eventSub?.unsubscribe();
    this.eventSub = null;
    if (this.debounceTimer) {
      clearTimeout(this.debounceTimer);
      this.debounceTimer = null;
    }
    this.clearTimers();
    this.beforeChange.clear();
  }

  private clearTimers(): void {
    if (this.pollTimer) {
      clearTimeout(this.pollTimer);
      this.pollTimer = null;
    }
    if (this.clockTimer) {
      clearInterval(this.clockTimer);
      this.clockTimer = null;
    }
  }

  private startClock(): void {
    if (this.clockTimer || this.consumers <= 0) return;
    this.zone.runOutsideAngular(() => {
      this.clockTimer = setInterval(() => {
        if (!this.doc.hidden) {
          this.zone.run(() => this.now.set(Date.now()));
        }
      }, CLOCK_TICK_MS);
    });
  }

  private globalBackoffMs(): number {
    return typeof this.api.backoffRemainingMs === "function"
      ? this.api.backoffRemainingMs()
      : 0;
  }

  private schedulePoll(): void {
    if (this.pollTimer) {
      clearTimeout(this.pollTimer);
      this.pollTimer = null;
    }
    if (this.consumers <= 0 || this.doc.hidden) return;
    const baseMs = this.trouble.failing ? FAILURE_PROBE_MS : IDLE_POLL_MS;
    const ms = Math.max(this.trouble.nextDelay(baseMs), this.globalBackoffMs());
    this.zone.runOutsideAngular(() => {
      this.pollTimer = setTimeout(() => {
        this.pollTimer = null;
        this.zone.run(() => {
          void this.refresh();
        });
      }, ms);
    });
  }

  private scheduleEventRefresh(): void {
    if (this.doc.hidden) return;
    // While reads are failing the backed-off poll is what asks next: a
    // burst of events would otherwise put the server back at full rate.
    if (this.trouble.failing) return;
    const now = Date.now();
    const backoffMs = this.globalBackoffMs();
    const sinceLast = now - this.lastEventRefreshAt;
    if (
      this.fastConsumers > 0 &&
      backoffMs === 0 &&
      sinceLast >= EVENT_COALESCE_MS
    ) {
      this.lastEventRefreshAt = now;
      void this.refresh();
      return;
    }
    if (this.debounceTimer) return;
    const waitMs = Math.max(
      200,
      backoffMs,
      this.fastConsumers > 0 ? EVENT_COALESCE_MS - sinceLast : 200,
    );
    this.zone.runOutsideAngular(() => {
      this.debounceTimer = setTimeout(() => {
        this.debounceTimer = null;
        if (this.doc.hidden || this.trouble.failing) return;
        this.lastEventRefreshAt = Date.now();
        this.zone.run(() => void this.refresh());
      }, waitMs);
    });
  }

  onBeforeChange(fn: () => void): () => void {
    this.beforeChange.add(fn);
    return () => this.beforeChange.delete(fn);
  }

  refreshIfStale(maxAgeMs = 3_000): Promise<boolean> {
    if (this.refreshing) return this.refreshing;
    const now = Date.now();
    const updatedAt = this.lastUpdatedAt();
    if (
      this.loaded() &&
      !this.lastError() &&
      updatedAt !== null &&
      now - updatedAt < maxAgeMs
    ) {
      return Promise.resolve(true);
    }
    if (this.lastError() && now - this.lastAttemptAt < 1_000) {
      return Promise.resolve(false);
    }
    return this.refresh();
  }

  refresh(): Promise<boolean> {
    if (!this.refreshing) {
      this.refreshing = this.load().finally(() => {
        this.refreshing = null;
        if (this.consumers > 0 && !this.doc.hidden) {
          this.schedulePoll();
        }
      });
      return this.refreshing;
    }
    return (this.queuedRefresh ??= this.refreshing
      .catch(() => false)
      .then(() => {
        this.queuedRefresh = null;
        return this.refresh();
      }));
  }

  private async load(): Promise<boolean> {
    try {
      let body: Task[];
      let changed: boolean;
      if (typeof this.api.getIfChanged === "function") {
        const r = await this.api.getIfChanged<Task[]>("GET /api/tasks");
        body = Array.isArray(r?.body) ? r.body : [];
        changed = !!r?.changed;
      } else {
        const raw = await this.api.tasks();
        body = Array.isArray(raw) ? raw : [];
        changed = true;
      }
      this.lastChanged.set(changed);
      if (changed || body !== this.tasks() || !this.loaded()) {
        for (const fn of this.beforeChange) fn();
        this.tasks.set(body);
        this.revision.update((r) => r + 1);
      }
      const ts = Date.now();
      this.lastAttemptAt = ts;
      this.lastUpdatedAt.set(ts);
      this.now.set(ts);
      this.lastError.set(null);
      this.loadError.set(null);
      this.loaded.set(true);
      this.trouble.ok();
      return true;
    } catch (err) {
      const ts = Date.now();
      this.lastAttemptAt = ts;
      this.lastError.set(err);
      if (this.trouble.fail(err) || !this.loaded()) {
        this.loadError.set(extractError(err));
      }
      this.now.set(ts);
      return false;
    }
  }
}
