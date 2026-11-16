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
import { GrainApi, READ_TIMEOUT_MS } from "../api/grain-api.service";
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
});
