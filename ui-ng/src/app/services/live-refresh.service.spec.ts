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
import { TestBed } from "@angular/core/testing";
import { Subject } from "rxjs";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents, type GrainEvent } from "../api/grain-events.service";
import {
  INITIAL_TRANSCRIPT_POLL_MS,
  LiveRefreshService,
  MAX_TRANSCRIPT_POLL_MS,
  SHARED_PULSE_MS,
} from "./live-refresh.service";

describe("LiveRefreshService", () => {
  let events$: Subject<GrainEvent>;
  let backoffMs = 0;
  let docHidden = false;
  let doc: Document;

  beforeEach(() => {
    vi.useFakeTimers();
    events$ = new Subject<GrainEvent>();
    backoffMs = 0;
    docHidden = false;

    TestBed.configureTestingModule({
      providers: [
        {
          provide: GrainApi,
          useValue: {
            backoffRemainingMs: () => backoffMs,
          },
        },
        {
          provide: GrainEvents,
          useValue: {
            events: () => events$.asObservable(),
          },
        },
      ],
    });
    doc = TestBed.inject(DOCUMENT);
    Object.defineProperty(doc, "hidden", {
      configurable: true,
      get: () => docHidden,
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    TestBed.resetTestingModule();
  });

  it("coalesces matching SSE bursts, pauses while hidden, and honours 503/429 backoff", async () => {
    const svc = TestBed.inject(LiveRefreshService);
    const batches: GrainEvent[][] = [];
    const dispose = svc.subscribeEvents(
      (ev) => ev.type.startsWith("notification."),
      (batch) => batches.push([...batch]),
      250,
    );

    // Non-matching events never schedule a callback.
    events$.next({ type: "task.updated", data: { taskId: "1" } });
    await vi.advanceTimersByTimeAsync(500);
    expect(batches).toHaveLength(0);

    // Rapid burst of matching events coalesces into a single invocation.
    events$.next({ type: "notification.created", data: { id: 1 } });
    events$.next({ type: "notification.created", data: { id: 2 } });
    await vi.advanceTimersByTimeAsync(249);
    expect(batches).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(2);
    expect(batches).toHaveLength(1);
    expect(batches[0]).toHaveLength(2);

    // Events arriving while document.hidden are deferred until visible again.
    docHidden = true;
    doc.dispatchEvent(new Event("visibilitychange"));
    events$.next({ type: "notification.resolved", data: { id: 1 } });
    await vi.advanceTimersByTimeAsync(5_000);
    expect(batches).toHaveLength(1);

    docHidden = false;
    doc.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(100);
    expect(batches).toHaveLength(2);

    // Global 503/429 Retry-After backoff delays the flush until the window clears.
    backoffMs = 4_000;
    events$.next({ type: "notification.created", data: { id: 3 } });
    await vi.advanceTimersByTimeAsync(1_000);
    expect(batches).toHaveLength(2);
    backoffMs = 0;
    await vi.advanceTimersByTimeAsync(3_100);
    expect(batches).toHaveLength(3);

    dispose();
  });

  it("shares a single ref-counted 30s pulse and suspends it while hidden", async () => {
    const svc = TestBed.inject(LiveRefreshService);
    const r1 = svc.retainPulse();
    const r2 = svc.retainPulse();
    expect(svc.pulse()).toBe(0);

    await vi.advanceTimersByTimeAsync(SHARED_PULSE_MS);
    expect(svc.pulse()).toBe(1);

    docHidden = true;
    doc.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(SHARED_PULSE_MS * 4);
    expect(svc.pulse()).toBe(1);

    docHidden = false;
    doc.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(SHARED_PULSE_MS);
    expect(svc.pulse()).toBe(2);

    r1();
    await vi.advanceTimersByTimeAsync(SHARED_PULSE_MS);
    expect(svc.pulse()).toBe(3);

    r2();
    await vi.advanceTimersByTimeAsync(SHARED_PULSE_MS * 3);
    expect(svc.pulse()).toBe(3);
  });

  it("backs off live transcript polls from 3s up to 30s when unchanged and resets to 3s on growth or SSE", async () => {
    const svc = TestBed.inject(LiveRefreshService);
    let currentOutput = "step 1";
    let fetchCount = 0;
    const seen: string[] = [];

    const poller = svc.createTranscriptPoller({
      fetch: async () => {
        fetchCount += 1;
        return currentOutput;
      },
      onText: (t) => seen.push(t),
    });

    poller.watch("42", 1, true);
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchCount).toBe(1);
    expect(seen).toEqual(["step 1"]);

    // Unchanged #1 at +3s -> next delay doubles to 6s.
    await vi.advanceTimersByTimeAsync(INITIAL_TRANSCRIPT_POLL_MS);
    expect(fetchCount).toBe(2);

    await vi.advanceTimersByTimeAsync(5_900);
    expect(fetchCount).toBe(2);
    await vi.advanceTimersByTimeAsync(100);
    // Unchanged #2 at +9s -> next delay doubles to 12s.
    expect(fetchCount).toBe(3);

    await vi.advanceTimersByTimeAsync(12_000);
    // Unchanged #3 at +21s -> next delay doubles to 24s.
    expect(fetchCount).toBe(4);

    await vi.advanceTimersByTimeAsync(24_000);
    // Unchanged #4 at +45s -> next delay caps at 30s.
    expect(fetchCount).toBe(5);

    await vi.advanceTimersByTimeAsync(MAX_TRANSCRIPT_POLL_MS - 100);
    expect(fetchCount).toBe(5);
    await vi.advanceTimersByTimeAsync(100);
    expect(fetchCount).toBe(6);

    // Matching task SSE event wakes the poller and resets cadence back to 3s.
    currentOutput = "step 1\nstep 2";
    events$.next({ type: "task.activity", data: { taskId: "42" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(fetchCount).toBe(7);
    expect(seen.at(-1)).toBe("step 1\nstep 2");

    // Next poll fires at 3s (not 30s) because output grew.
    currentOutput = "step 1\nstep 2\nstep 3";
    await vi.advanceTimersByTimeAsync(INITIAL_TRANSCRIPT_POLL_MS);
    expect(fetchCount).toBe(8);
    expect(seen.at(-1)).toBe("step 1\nstep 2\nstep 3");

    // Hidden tab pauses transcript polling completely.
    docHidden = true;
    doc.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(60_000);
    expect(fetchCount).toBe(8);

    poller.stop();
  });
});
