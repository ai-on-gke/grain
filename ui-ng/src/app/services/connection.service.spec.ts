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

import { provideHttpClient } from "@angular/common/http";
import {
  HttpTestingController,
  provideHttpClientTesting,
} from "@angular/common/http/testing";
import { TestBed } from "@angular/core/testing";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NEVER } from "rxjs";
import {
  GrainApi,
  READ_TIMEOUT_MS,
  SLOW_READ_TIMEOUT_MS,
  isCancelledRead,
} from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import { TaskFeedStore } from "./task-feed.service";
import { AccessService } from "./access.service";
import {
  CONFIRM_DELAY_MS,
  ConnectionService,
  PROBE_TIMEOUT_MS,
  RECOVERY_POLL_MS,
  isUnreachable,
} from "./connection.service";

describe("ConnectionService & GrainApi bounded reads", () => {
  let api: GrainApi;
  let http: HttpTestingController;
  let conn: ConnectionService;
  let access: AccessService;

  beforeEach(() => {
    vi.useFakeTimers();
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    api = TestBed.inject(GrainApi);
    http = TestBed.inject(HttpTestingController);
    conn = TestBed.inject(ConnectionService);
    access = TestBed.inject(AccessService);
    conn.start();
  });

  afterEach(() => {
    conn.stop();
    api.ngOnDestroy();
    http.verify();
    TestBed.resetTestingModule();
    vi.useRealTimers();
  });

  it("classifies network drops, read timeouts, and non-verifier gateways as unreachable", () => {
    expect(isUnreachable({ timedOut: true, timeoutMs: 8000 })).toBe(true);
    expect(isUnreachable({ status: 0 })).toBe(true);
    expect(isUnreachable(new TypeError("Failed to fetch"))).toBe(true);
    expect(isUnreachable({ status: 502 })).toBe(true);
    expect(isUnreachable({ status: 503, error: { error: "busy" } })).toBe(true);
    expect(isUnreachable({ status: 504 })).toBe(true);

    expect(
      isUnreachable({
        status: 503,
        error: { code: "verifier_unavailable", error: "verifier down" },
      }),
    ).toBe(false);
    expect(isUnreachable({ status: 500 })).toBe(false);
    expect(isUnreachable({ status: 403 })).toBe(false);
    expect(isUnreachable(null)).toBe(false);
  });

  it("aborts a hung read after READ_TIMEOUT_MS, confirms via /api/config, and clears on recovery", async () => {
    let reconnected = 0;
    conn.onReconnect(() => reconnected++);

    let caught: unknown = null;
    const readP = api.getIfChanged("GET /api/tasks").catch((e) => {
      caught = e;
    });
    const hungTasks = http.expectOne("/api/tasks");
    expect(hungTasks.cancelled).toBe(false);

    // Advance to the read timeout: the hung request is cancelled and a
    // confirmation probe goes out to /api/config.
    await vi.advanceTimersByTimeAsync(READ_TIMEOUT_MS);
    await readP;
    expect(hungTasks.cancelled).toBe(true);
    expect(caught).toMatchObject({
      timedOut: true,
      timeoutMs: READ_TIMEOUT_MS,
    });
    expect(conn.disconnected()).toBeNull();

    const probe1 = http.expectOne("/api/config");
    await vi.advanceTimersByTimeAsync(PROBE_TIMEOUT_MS);
    expect(probe1.cancelled).toBe(true);

    expect(conn.disconnected()).not.toBeNull();
    expect(conn.disconnected()?.headline).toBe("Server is not responding");
    expect(conn.disconnected()?.detail).toContain("within 8s");

    // Recovery probe fires after RECOVERY_POLL_MS; answering clears the notice.
    await vi.advanceTimersByTimeAsync(RECOVERY_POLL_MS);
    const probe2 = http.expectOne("/api/config");
    probe2.flush({ version: "v1.2.3" });
    await vi.advanceTimersByTimeAsync(0);

    expect(conn.disconnected()).toBeNull();
    expect(reconnected).toBe(1);
    expect(access.rawConfig()?.version).toBe("v1.2.3");
  });

  it("keeps the banner down when a single route returns 503 while /api/config still answers", async () => {
    const readP = api.get("GET /api/tasks").catch(() => null);
    http
      .expectOne("/api/tasks")
      .flush(
        { error: "upstream timed out" },
        { status: 503, statusText: "Unavailable" },
      );
    await readP;

    await vi.advanceTimersByTimeAsync(CONFIRM_DELAY_MS);
    http.expectOne("/api/config").flush({ version: "v1.0.0" });
    await vi.advanceTimersByTimeAsync(0);

    expect(conn.disconnected()).toBeNull();
  });

  it("announces connection refusals and names an expected restart when restarting was set", async () => {
    access.setConfig({ version: "v1.0.0", restarting: true });

    const readP = api.get("GET /api/tasks").catch(() => null);
    http.expectOne("/api/tasks").error(new ProgressEvent("error"));
    await readP;

    // With restarting set on config, confirmation runs immediately.
    await vi.advanceTimersByTimeAsync(0);
    http.expectOne("/api/config").error(new ProgressEvent("error"));
    await vi.advanceTimersByTimeAsync(0);

    expect(conn.disconnected()?.restarting).toBe(true);
    expect(conn.disconnected()?.headline).toBe("Grain is restarting");
  });

  it("cancels stale reads on recovery without reporting them, and the next ask is a fresh request", async () => {
    let caught: unknown = null;
    const stale = api.getIfChanged("GET /api/tasks").catch((e) => {
      caught = e;
    });
    const req1 = http.expectOne("/api/tasks");
    await vi.advanceTimersByTimeAsync(2_000);

    api.abortStaleReads(1_500);
    // Asked again in the same tick, as a reconnect listener does: a new
    // request, not the cancelled one handed back from the in-flight map.
    const fresh = api.getIfChanged<{ id: string }[]>("GET /api/tasks");
    await stale;
    expect(req1.cancelled).toBe(true);
    expect(isCancelledRead(caught)).toBe(true);
    http.expectOne("/api/tasks").flush([{ id: "1" }]);
    expect((await fresh).body).toEqual([{ id: "1" }]);

    // The cancellation is not news about the server: no confirmation probe.
    await vi.advanceTimersByTimeAsync(CONFIRM_DELAY_MS + 10);
    http.expectNone("/api/config");
    expect(conn.disconnected()).toBeNull();
  });

  it("gives reads that wait on providers, guests or registries the slow timeout", async () => {
    const p = api.get("GET /api/agent-models").catch((e) => e);
    const req = http.expectOne("/api/agent-models");
    await vi.advanceTimersByTimeAsync(READ_TIMEOUT_MS + 1_000);
    expect(req.cancelled).toBe(false);
    req.flush({ models: [] });
    expect(await p).toEqual({ models: [] });
    expect(SLOW_READ_TIMEOUT_MS).toBeGreaterThan(60_000);
  });
});

describe("TaskFeedStore under a confirmed outage", () => {
  afterEach(() => TestBed.resetTestingModule());

  function setup() {
    let next: () => Promise<unknown> = () =>
      Promise.resolve({ body: [{ id: "1", state: "queued" }], changed: true });
    TestBed.configureTestingModule({
      providers: [
        {
          provide: GrainApi,
          useValue: { getIfChanged: () => next(), config: () => NEVER },
        },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    const feed = TestBed.inject(TaskFeedStore);
    const conn = TestBed.inject(ConnectionService);
    return {
      feed,
      conn,
      fail: (err: unknown) => {
        next = () => Promise.reject(err);
      },
    };
  }

  it("keeps a loaded list's banner off past the sustained threshold while the shell reports the outage", async () => {
    const { feed, conn, fail } = setup();
    expect(await feed.refresh()).toBe(true);
    conn.disconnected.set({
      headline: "Server cannot be reached",
      detail: "",
      restarting: false,
      since: Date.now(),
    });
    fail({ status: 0 });
    for (let i = 0; i < 4; i++) await feed.refresh();
    expect(feed.loadError()).toBeNull();
    expect(feed.tasks().length).toBe(1);
  });

  it("still says so when one route keeps failing while the server answers", async () => {
    const { feed, fail } = setup();
    expect(await feed.refresh()).toBe(true);
    fail({ status: 503, error: { error: "upstream request timed out" } });
    for (let i = 0; i < 3; i++) await feed.refresh();
    expect(feed.loadError()).toBe("upstream request timed out");
  });

  it("does not count a cancelled read as a failed poll", async () => {
    const { feed, fail } = setup();
    fail(Object.assign(new Error("cancelled"), { cancelled: true }));
    expect(await feed.refresh()).toBe(false);
    expect(feed.loadError()).toBeNull();
    expect(feed.lastError()).toBeNull();
  });
});
