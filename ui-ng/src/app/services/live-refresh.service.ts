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
import { Injectable, NgZone, OnDestroy, inject, signal } from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents, type GrainEvent } from "../api/grain-events.service";
import { ReadTrouble } from "./request-trouble";

export const SHARED_PULSE_MS = 30_000;
export const INITIAL_TRANSCRIPT_POLL_MS = 3_000;
export const MAX_TRANSCRIPT_POLL_MS = 30_000;

export function eventTaskId(ev: GrainEvent): string | null {
  if (!ev.data || typeof ev.data !== "object") return null;
  const d = ev.data as { taskId?: unknown; id?: unknown };
  if (typeof d.taskId === "string" && d.taskId) return d.taskId;
  if (typeof d.taskId === "number") return String(d.taskId);
  // Plain IntegrationEvent uses `id` for the event sequence number when `type`
  // is a server event and `taskId` is present; unit-test fixtures sometimes
  // emit `{ type: "task.updated", id: "2" }` with no `taskId` field.
  if (
    !("taskId" in d) &&
    (typeof d.id === "string" || typeof d.id === "number")
  ) {
    const s = String(d.id);
    return s || null;
  }
  return null;
}

export interface TranscriptWatchCallbacks {
  fetch: (taskId: string, attempt: number) => Promise<string>;
  onText: (text: string) => void;
  onError?: (err: unknown) => void;
  isActive?: () => boolean;
}

export interface TranscriptPollerHandle {
  watch(taskId: string, attempt: number, live: boolean): void;
  stop(): void;
  readonly activeKey: () => string;
}

// LiveRefreshService is the single coordinator for `/api/events`-driven
// refreshes, `document.hidden` suspension, global 503/429 `Retry-After`
// backoff, and adaptive live-transcript polling across `/next/`.
@Injectable({ providedIn: "root" })
export class LiveRefreshService implements OnDestroy {
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);
  private readonly api = inject(GrainApi);
  private readonly events = inject(GrainEvents);

  readonly hidden = signal(Boolean(this.doc?.hidden));
  readonly visiblePulse = signal(0);
  readonly pulse = signal(0);

  private pulseRetainers = 0;
  private pulseTimer: ReturnType<typeof setTimeout> | null = null;
  private readonly visibilityListeners = new Set<() => void>();

  private readonly onVisibility = (): void => {
    const isHidden = Boolean(this.doc?.hidden);
    const wasHidden = this.hidden();
    this.zone.run(() => {
      this.hidden.set(isHidden);
      if (isHidden) {
        this.clearPulseTimer();
      } else if (wasHidden) {
        this.visiblePulse.update((n) => n + 1);
        for (const fn of this.visibilityListeners) fn();
        if (this.pulseRetainers > 0) this.schedulePulse();
      }
    });
  };

  constructor() {
    this.doc?.addEventListener("visibilitychange", this.onVisibility);
  }

  ngOnDestroy(): void {
    this.doc?.removeEventListener("visibilitychange", this.onVisibility);
    this.clearPulseTimer();
    this.visibilityListeners.clear();
  }

  private globalBackoffMs(): number {
    return typeof this.api.backoffRemainingMs === "function"
      ? this.api.backoffRemainingMs()
      : 0;
  }

  retainPulse(): () => void {
    this.pulseRetainers++;
    if (this.pulseRetainers === 1 && !this.hidden()) {
      this.schedulePulse();
    }
    let released = false;
    return () => {
      if (released) return;
      released = true;
      this.pulseRetainers = Math.max(0, this.pulseRetainers - 1);
      if (this.pulseRetainers === 0) {
        this.clearPulseTimer();
      }
    };
  }

  private clearPulseTimer(): void {
    if (this.pulseTimer) {
      clearTimeout(this.pulseTimer);
      this.pulseTimer = null;
    }
  }

  private schedulePulse(): void {
    this.clearPulseTimer();
    if (this.pulseRetainers <= 0 || this.hidden()) return;
    const delay = Math.max(SHARED_PULSE_MS, this.globalBackoffMs());
    this.zone.runOutsideAngular(() => {
      this.pulseTimer = setTimeout(() => {
        this.pulseTimer = null;
        if (this.pulseRetainers <= 0 || this.hidden()) return;
        const waitMore = this.globalBackoffMs();
        if (waitMore > 0) {
          this.schedulePulse();
          return;
        }
        this.zone.run(() => {
          this.pulse.update((n) => n + 1);
          this.schedulePulse();
        });
      }, delay);
    });
  }

  // Subscribes to `/api/events`, coalescing bursts over `debounceMs`, pausing
  // while `document.hidden` (with a single catch-up flush when shown again),
  // and honouring global 503/429 `Retry-After` backoff.
  subscribeEvents(
    match: (ev: GrainEvent) => boolean,
    callback: (events: readonly GrainEvent[]) => void,
    debounceMs = 250,
  ): () => void {
    let timer: ReturnType<typeof setTimeout> | null = null;
    let buffered: GrainEvent[] = [];
    let deferredWhileHidden = false;
    let disposed = false;

    const flush = () => {
      if (disposed) return;
      if (this.hidden()) {
        deferredWhileHidden = true;
        return;
      }
      const backoff = this.globalBackoffMs();
      if (backoff > 0) {
        schedule(backoff);
        return;
      }
      const batch = buffered;
      buffered = [];
      deferredWhileHidden = false;
      this.zone.run(() => callback(batch));
    };

    const schedule = (ms: number) => {
      if (timer || disposed) return;
      this.zone.runOutsideAngular(() => {
        timer = setTimeout(() => {
          timer = null;
          flush();
        }, ms);
      });
    };

    const onVisible = () => {
      if (deferredWhileHidden && !disposed) {
        deferredWhileHidden = false;
        schedule(Math.max(50, this.globalBackoffMs()));
      }
    };
    this.visibilityListeners.add(onVisible);

    const sub = this.events.events().subscribe((ev) => {
      if (disposed || !match(ev)) return;
      buffered.push(ev);
      if (this.hidden()) {
        deferredWhileHidden = true;
        return;
      }
      schedule(Math.max(debounceMs, this.globalBackoffMs()));
    });

    return () => {
      disposed = true;
      if (timer) {
        clearTimeout(timer);
        timer = null;
      }
      buffered = [];
      this.visibilityListeners.delete(onVisible);
      sub.unsubscribe();
    };
  }

  // Creates an adaptive transcript poller for a single selected, visible,
  // running attempt: starts at 3s, doubles up to 30s whenever output is
  // unchanged, resets to 3s on output growth or a matching task SSE event,
  // pauses completely while `document.hidden`, and honours 503/429 backoff.
  createTranscriptPoller(cb: TranscriptWatchCallbacks): TranscriptPollerHandle {
    let currentKey = "";
    let currentTaskId = "";
    let currentAttempt = 0;
    let isLive = false;
    let delayMs = INITIAL_TRANSCRIPT_POLL_MS;
    let lastText: string | null = null;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let inFlight = false;
    let unsubEvents: (() => void) | null = null;
    const trouble = new ReadTrouble();

    const clearTimer = () => {
      if (timer) {
        clearTimeout(timer);
        timer = null;
      }
    };

    const scheduleNext = (overrideMs?: number) => {
      clearTimer();
      if (!currentKey || !isLive || this.hidden()) return;
      if (cb.isActive && !cb.isActive()) return;
      const wait = Math.max(
        overrideMs ?? delayMs,
        trouble.failing ? trouble.nextDelay(INITIAL_TRANSCRIPT_POLL_MS) : 0,
        this.globalBackoffMs(),
      );
      this.zone.runOutsideAngular(() => {
        timer = setTimeout(() => {
          timer = null;
          this.zone.run(() => void readOnce());
        }, wait);
      });
    };

    const readOnce = async (): Promise<void> => {
      const keyAtStart = currentKey;
      if (!keyAtStart || inFlight) return;
      if (this.hidden() || (cb.isActive && !cb.isActive())) return;
      const backoff = this.globalBackoffMs();
      if (backoff > 0 && lastText !== null) {
        scheduleNext(backoff);
        return;
      }
      inFlight = true;
      try {
        const text = await cb.fetch(currentTaskId, currentAttempt);
        if (currentKey !== keyAtStart) return;
        trouble.ok();
        if (lastText !== null && text === lastText) {
          delayMs = Math.min(delayMs * 2, MAX_TRANSCRIPT_POLL_MS);
        } else {
          delayMs = INITIAL_TRANSCRIPT_POLL_MS;
        }
        lastText = text;
        cb.onText(text);
      } catch (err) {
        if (currentKey !== keyAtStart) return;
        trouble.fail(err);
        cb.onError?.(err);
      } finally {
        inFlight = false;
        if (currentKey === keyAtStart && isLive) {
          scheduleNext();
        }
      }
    };

    const onVisible = () => {
      if (currentKey && isLive && !this.hidden()) {
        if (!cb.isActive || cb.isActive()) {
          void readOnce();
        }
      }
    };

    const stop = () => {
      clearTimer();
      unsubEvents?.();
      unsubEvents = null;
      this.visibilityListeners.delete(onVisible);
      currentKey = "";
      currentTaskId = "";
      currentAttempt = 0;
      isLive = false;
      lastText = null;
      inFlight = false;
      delayMs = INITIAL_TRANSCRIPT_POLL_MS;
      trouble.ok();
    };

    const watch = (taskId: string, attempt: number, live: boolean) => {
      const nextKey = `${taskId}:${attempt}`;
      if (
        currentKey === nextKey &&
        isLive === live &&
        (timer || inFlight || !live)
      ) {
        return;
      }
      stop();
      currentKey = nextKey;
      currentTaskId = taskId;
      currentAttempt = attempt;
      isLive = live;
      if (live) {
        this.visibilityListeners.add(onVisible);
        unsubEvents = this.subscribeEvents(
          (ev) => {
            if (!ev.type.startsWith("task.")) return false;
            const evId = eventTaskId(ev);
            return evId === taskId;
          },
          () => {
            delayMs = INITIAL_TRANSCRIPT_POLL_MS;
            if (!inFlight) void readOnce();
          },
          200,
        );
      }
      if (!this.hidden() || lastText === null) {
        void readOnce();
      }
    };

    return {
      watch,
      stop,
      activeKey: () => currentKey,
    };
  }
}
