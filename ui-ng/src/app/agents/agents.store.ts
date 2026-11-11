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
import type { Task, TaskDetail } from "../api/models";
import { errorText } from "../sessions/session-view";
import {
  type Agent,
  type SandboxReading,
  SPARK_WINDOW_MS,
  running,
  toAgent,
} from "./agent-view";

export const POLL_MS = 3000;
// Details (attempt, model, tokens) and sandbox probes move slowly.
export const SLOW_POLL_MS = 15000;
// How many detail reads run at once, so a large deployment's slow poll
// does not fire one request per running task in the same instant.
export const DETAIL_CONCURRENCY = 4;

type Host = { cpus?: number; loadAverage1?: number };

// AgentsStore is the Agents screen's state: running tasks from the same
// ETag task poll as Tasks, each run's current attempt, sandbox CPU probes
// where the backend has them, and a short history of activity updates
// for the sparklines. Polling pauses while the tab is hidden.
@Injectable({ providedIn: "root" })
export class AgentsStore {
  private readonly api = inject(GrainApi);
  private readonly events = inject(GrainEvents);
  private readonly zone = inject(NgZone);

  readonly tasks = signal<Task[]>([]);
  readonly details = signal<Record<string, TaskDetail>>({});
  readonly readings = signal<SandboxReading[]>([]);
  readonly host = signal<Host | null>(null);
  readonly loaded = signal(false);
  readonly loadError = signal<string | null>(null);
  readonly now = signal(Date.now());
  // Activity update times per task, for the sparkline.
  readonly history = signal<Record<string, number[]>>({});

  readonly agents = computed<Agent[]>(() => {
    const now = this.now();
    const d = this.details();
    const r = this.readings();
    return running(this.tasks()).map((t) =>
      toAgent(t, d[t.id] ?? null, r, now),
    );
  });

  private timer: ReturnType<typeof setInterval> | null = null;
  private slowTimer: ReturnType<typeof setInterval> | null = null;
  private eventSub: { unsubscribe(): void } | null = null;
  private refreshing: Promise<void> | null = null;
  private pending = false;
  private slowRefreshing: Promise<void> | null = null;
  // Detail reads in flight, and when a read last failed, so the fast poll
  // neither repeats a pending read nor retries a failing one every 3 s.
  private readonly detailInFlight = new Set<string>();
  private readonly detailFailedAt = new Map<string, number>();
  private readonly onVisibility = (): void => {
    if (document.visibilityState === "visible") {
      this.resume();
    } else {
      this.pause();
    }
  };

  start(): void {
    // Cards from an earlier visit must not show old quiet times.
    this.now.set(Date.now());
    this.eventSub ??= this.events.events().subscribe(() => void this.refresh());
    document.addEventListener("visibilitychange", this.onVisibility);
    if (document.visibilityState === "hidden") {
      void this.refresh();
      void this.refreshSlow();
    } else {
      this.resume();
    }
  }

  stop(): void {
    this.pause();
    this.eventSub?.unsubscribe();
    this.eventSub = null;
    document.removeEventListener("visibilitychange", this.onVisibility);
  }

  private resume(): void {
    if (this.timer) return;
    void this.refresh();
    void this.refreshSlow();
    this.zone.runOutsideAngular(() => {
      this.timer = setInterval(
        () => this.zone.run(() => void this.refresh()),
        POLL_MS,
      );
      this.slowTimer = setInterval(
        () => this.zone.run(() => void this.refreshSlow()),
        SLOW_POLL_MS,
      );
    });
  }

  private pause(): void {
    if (this.timer) clearInterval(this.timer);
    if (this.slowTimer) clearInterval(this.slowTimer);
    this.timer = null;
    this.slowTimer = null;
  }

  // refresh reads the task list. One read at a time: a call while one is
  // in flight asks for one more read after it, so answers never land out
  // of order.
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
      // An unchanged answer still carries the newest body: another page
      // may have taken the fresh copy (getIfChanged shares tags per URL).
      const list = Array.isArray(r.body) ? r.body : [];
      if (r.changed || list !== this.tasks()) {
        this.tasks.set(list);
      }
      this.record(this.tasks());
      this.loadError.set(null);
    } catch (err) {
      this.loadError.set(
        `Running tasks could not be read: ${errorText(err)}. This page tries again on its own.`,
      );
    }
    this.now.set(Date.now());
    this.loaded.set(true);
    // A run that appeared since the last slow read gets its detail now.
    const now = Date.now();
    const missing = running(this.tasks()).filter(
      (t) =>
        !this.details()[t.id] &&
        now - (this.detailFailedAt.get(t.id) ?? 0) >= SLOW_POLL_MS,
    );
    if (missing.length) void this.loadDetails(missing);
  }

  refreshSlow(): Promise<void> {
    this.slowRefreshing ??= Promise.all([
      this.loadDetails(running(this.tasks())),
      this.loadReadings(),
    ])
      .then(() => undefined)
      .finally(() => (this.slowRefreshing = null));
    return this.slowRefreshing;
  }

  private async loadDetails(list: readonly Task[]): Promise<void> {
    const todo = list.filter((t) => !this.detailInFlight.has(t.id));
    for (const t of todo) this.detailInFlight.add(t.id);
    const got: Record<string, TaskDetail> = {};
    let next = 0;
    const worker = async (): Promise<void> => {
      while (next < todo.length) {
        const t = todo[next++];
        try {
          const r = await this.api.getIfChanged<TaskDetail>(
            "GET /api/tasks/{id}",
            { id: t.id },
            "comments_limit=1&attempts_limit=3",
          );
          got[t.id] = r.body;
          this.detailFailedAt.delete(t.id);
        } catch {
          this.detailFailedAt.set(t.id, Date.now());
        } finally {
          this.detailInFlight.delete(t.id);
        }
      }
    };
    await Promise.all(
      Array.from({ length: Math.min(DETAIL_CONCURRENCY, todo.length) }, () =>
        Promise.resolve().then(worker),
      ),
    );
    // Keep details for running tasks only.
    const live = new Set(running(this.tasks()).map((t) => t.id));
    const merged: Record<string, TaskDetail> = {};
    for (const [id, d] of Object.entries({ ...this.details(), ...got })) {
      if (live.has(id)) merged[id] = d;
    }
    for (const id of this.detailFailedAt.keys()) {
      if (!live.has(id)) this.detailFailedAt.delete(id);
    }
    this.details.set(merged);
  }

  private async loadReadings(): Promise<void> {
    try {
      const r = await this.api.get<{
        sandboxes?: SandboxReading[];
        host?: Host;
      }>("GET /api/sandboxes");
      this.readings.set(r?.sandboxes ?? []);
      this.host.set(r?.host ?? null);
    } catch {
      // No probe data (a role without deployment:read, or no backend
      // support): cards show CPU as unknown.
      this.readings.set([]);
      this.host.set(null);
    }
  }

  // record keeps when each running task last reported, for sparklines,
  // and drops times older than the window and tasks no longer running.
  private record(list: readonly Task[]): void {
    const now = Date.now();
    const h: Record<string, number[]> = {};
    const prev = this.history();
    for (const t of running(list)) {
      let times = (prev[t.id] ?? []).filter((x) => now - x < SPARK_WINDOW_MS);
      const at = Date.parse(t.activityAt ?? "");
      if (
        Number.isFinite(at) &&
        times[times.length - 1] !== at &&
        now - at < SPARK_WINDOW_MS
      ) {
        times = [...times, at];
      }
      if (times.length) h[t.id] = times;
    }
    this.history.set(h);
  }

  // contact sends one notice to every running agent (the classic "Send a
  // notice to all currently running task agents").
  async contact(message: string): Promise<number> {
    const r = await this.api.post<{ delivered_count?: number }>(
      "POST /api/deployment/broadcast",
      undefined,
      { message, running_only: true },
    );
    return r?.delivered_count ?? 0;
  }
}
