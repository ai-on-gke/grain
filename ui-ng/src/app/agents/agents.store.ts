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
  effect,
  inject,
  signal,
  untracked,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import type { Task, TaskDetail } from "../api/models";
import { TaskFeedStore } from "../services/task-feed.service";
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

// AgentsStore is the Live view over TaskFeedStore: running tasks come
// directly from the shared open-task feed, enriched with each run's
// current attempt, sandbox CPU probes where the backend has them, and a
// short history of activity updates for the sparklines.
@Injectable({ providedIn: "root" })
export class AgentsStore implements OnDestroy {
  private readonly api = inject(GrainApi);
  private readonly feed = inject(TaskFeedStore);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);

  readonly tasks = this.feed.tasks;
  readonly details = signal<Record<string, TaskDetail>>({});
  readonly readings = signal<SandboxReading[]>([]);
  readonly host = signal<Host | null>(null);
  private readonly ownLoaded = signal(false);
  readonly loaded = computed(() => this.ownLoaded() || this.feed.loaded());
  readonly loadError = signal<string | null>(null);
  readonly now = signal(Date.now());
  // Activity update times per task, for the sparkline.
  readonly history = signal<Record<string, number[]>>({});

  readonly agents = computed<Agent[]>(() => {
    const now = this.now();
    const d = this.details();
    const r = this.readings();
    const hostCpus = this.host()?.cpus;
    return running(this.tasks()).map((t) =>
      toAgent(t, d[t.id] ?? null, r, now, hostCpus),
    );
  });

  private active = false;
  private slowTimer: ReturnType<typeof setInterval> | null = null;
  private refreshing: Promise<void> | null = null;
  private pending = false;
  private slowRefreshing: Promise<void> | null = null;
  private readonly detailInFlight = new Set<string>();
  private readonly detailFailedAt = new Map<string, number>();

  private readonly onVisibility = (): void => {
    if (!this.active) return;
    if (this.doc.visibilityState === "visible") {
      this.resume();
    } else {
      this.pause();
    }
  };

  constructor() {
    // React to TaskFeedStore's monotonic revision and loadError so sparkline
    // history and missing details stay in sync without duplicate 3s polls or
    // duplicate task.* SSE refresh stampedes.
    effect(() => {
      const rev = this.feed.revision();
      const err = this.feed.loadError();
      const feedLoaded = this.feed.loaded();
      untracked(() => {
        if (err) {
          if (this.active) {
            this.loadError.set(
              `Running tasks could not be read: ${err}. This page tries again on its own.`,
            );
          }
          return;
        }
        if (rev === 0 && !feedLoaded) return;
        const list = this.feed.tasks();
        this.loadError.set(null);
        this.ownLoaded.set(true);
        this.record(list);
        this.now.set(Date.now());
        if (this.active) {
          this.fetchMissingDetails(list);
        }
      });
    });
  }

  start(): void {
    this.now.set(Date.now());
    const wasActive = this.active;
    if (!this.active) {
      this.active = true;
      this.feed.start(false);
      this.feed.acquireFast();
    }
    this.doc.addEventListener("visibilitychange", this.onVisibility);
    if (this.doc.visibilityState === "hidden") {
      if (!wasActive && this.feed.loaded()) {
        this.fetchMissingDetails(this.tasks());
      }
      void this.refreshSlow();
    } else {
      this.resume(!wasActive);
    }
  }

  stop(): void {
    if (this.active) {
      this.active = false;
      this.feed.releaseFast();
      this.feed.stop(false);
    }
    this.pause();
    this.doc.removeEventListener("visibilitychange", this.onVisibility);
  }

  ngOnDestroy(): void {
    this.stop();
  }

  private resume(firstActivation = false): void {
    if (this.slowTimer) return;
    if (firstActivation && this.feed.loaded()) {
      void this.refresh();
    }
    void this.refreshSlow();
    this.zone.runOutsideAngular(() => {
      this.slowTimer = setInterval(
        () => this.zone.run(() => void this.refreshSlow()),
        SLOW_POLL_MS,
      );
    });
  }

  private pause(): void {
    if (this.slowTimer) clearInterval(this.slowTimer);
    this.slowTimer = null;
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
    const ok = await this.feed.refresh();
    if (ok) {
      this.record(this.tasks());
      this.loadError.set(null);
      this.ownLoaded.set(true);
    } else {
      this.loadError.set(
        `Running tasks could not be read: ${this.feed.loadError() ?? "The request failed."}. This page tries again on its own.`,
      );
    }
    this.now.set(Date.now());
    this.fetchMissingDetails(this.tasks());
  }

  private fetchMissingDetails(list: readonly Task[]): void {
    const now = Date.now();
    const missing = running(list).filter(
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
          if (r?.body) got[t.id] = r.body;
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
      this.readings.set([]);
      this.host.set(null);
    }
  }

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

  async contact(message: string): Promise<number> {
    const r = await this.api.post<{ delivered_count?: number }>(
      "POST /api/deployment/broadcast",
      undefined,
      { message, running_only: true },
    );
    return r?.delivered_count ?? 0;
  }
}
