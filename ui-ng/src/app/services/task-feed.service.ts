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

export const FAST_POLL_MS = 3000;
export const IDLE_POLL_MS = 15000;
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
  private refreshing: Promise<boolean> | null = null;
  private queuedRefresh: Promise<boolean> | null = null;

  private readonly onVisibility = (): void => {
    if (this.consumers <= 0) return;
    if (this.doc.hidden) {
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
    if (!this.loaded() && !this.refreshing) {
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

  private schedulePoll(): void {
    if (this.pollTimer) {
      clearTimeout(this.pollTimer);
      this.pollTimer = null;
    }
    if (this.consumers <= 0 || this.doc.hidden) return;
    const ms =
      this.fastConsumers > 0 || this.runningCount() > 0
        ? FAST_POLL_MS
        : IDLE_POLL_MS;
    this.zone.runOutsideAngular(() => {
      this.pollTimer = setTimeout(() => {
        this.pollTimer = null;
        this.zone.run(() => {
          void this.refresh().finally(() => this.schedulePoll());
        });
      }, ms);
    });
  }

  private scheduleEventRefresh(): void {
    if (this.doc.hidden) return;
    if (this.fastConsumers > 0) {
      void this.refresh();
      return;
    }
    if (this.debounceTimer) return;
    this.zone.runOutsideAngular(() => {
      this.debounceTimer = setTimeout(() => {
        this.debounceTimer = null;
        this.zone.run(() => void this.refresh());
      }, 200);
    });
  }

  onBeforeChange(fn: () => void): () => void {
    this.beforeChange.add(fn);
    return () => this.beforeChange.delete(fn);
  }

  refresh(): Promise<boolean> {
    if (!this.refreshing) {
      this.refreshing = this.load().finally(() => {
        this.refreshing = null;
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
      this.lastUpdatedAt.set(ts);
      this.now.set(ts);
      this.lastError.set(null);
      this.loadError.set(null);
      this.loaded.set(true);
      return true;
    } catch (err) {
      this.lastError.set(err);
      this.loadError.set(extractError(err));
      this.now.set(Date.now());
      return false;
    }
  }
}
