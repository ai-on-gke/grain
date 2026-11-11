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
  readonly host = signal<{ cpus?: number; loadAverage1?: number } | null>(null);
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
  private readonly onVisibility = (): void => {
    if (document.visibilityState === "visible") {
      this.resume();
    } else {
      this.pause();
    }
  };

  start(): void {
    void this.refresh();
    void this.refreshSlow();
    this.eventSub ??= this.events.events().subscribe(() => void this.refresh());
    document.addEventListener("visibilitychange", this.onVisibility);
    this.resume();
  }

  stop(): void {
    this.pause();
    this.eventSub?.unsubscribe();
    this.eventSub = null;
    document.removeEventListener("visibilitychange", this.onVisibility);
  }

  private resume(): void {
    if (this.timer) return;
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

  async refresh(): Promise<void> {
    try {
      const r = await this.api.getIfChanged<Task[]>("GET /api/tasks");
      if (r.changed || !this.loaded()) {
        const list = Array.isArray(r.body) ? r.body : [];
        this.tasks.set(list);
        this.record(list);
      }
      this.loadError.set(null);
    } catch (err) {
      this.loadError.set(
        `Running tasks could not be read: ${errorText(err)}. This page tries again on its own.`,
      );
    }
    this.now.set(Date.now());
    this.loaded.set(true);
    // A run that appeared since the last slow read gets its detail now.
    const missing = running(this.tasks()).filter((t) => !this.details()[t.id]);
    if (missing.length) void this.loadDetails(missing);
  }

  async refreshSlow(): Promise<void> {
    await Promise.all([
      this.loadDetails(running(this.tasks())),
      this.loadReadings(),
    ]);
  }

  private async loadDetails(list: readonly Task[]): Promise<void> {
    const results = await Promise.allSettled(
      list.map((t) =>
        this.api.getIfChanged<TaskDetail>(
          "GET /api/tasks/{id}",
          { id: t.id },
          "comments_limit=1&attempts_limit=3",
        ),
      ),
    );
    const next = { ...this.details() };
    results.forEach((r, i) => {
      if (r.status === "fulfilled") next[list[i].id] = r.value.body;
    });
    this.details.set(next);
  }

  private async loadReadings(): Promise<void> {
    try {
      const r = await this.api.get<{
        sandboxes?: SandboxReading[];
        host?: { cpus?: number; loadAverage1?: number };
      }>("GET /api/sandboxes");
      this.readings.set(r?.sandboxes ?? []);
      this.host.set(r?.host ?? null);
    } catch {
      // No probe data (a role without deployment:read, or no backend
      // support): cards show CPU as unknown.
      this.readings.set([]);
    }
  }

  // record keeps when each running task last reported, for sparklines.
  private record(list: readonly Task[]): void {
    const now = Date.now();
    const h = { ...this.history() };
    for (const t of running(list)) {
      const at = Date.parse(t.activityAt ?? "");
      if (!Number.isFinite(at)) continue;
      const times = h[t.id] ?? [];
      if (times[times.length - 1] !== at) {
        h[t.id] = [...times, at].filter((x) => now - x < SPARK_WINDOW_MS);
      }
    }
    for (const id of Object.keys(h)) {
      if (!list.some((t) => t.id === id && t.state === "running")) delete h[id];
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
