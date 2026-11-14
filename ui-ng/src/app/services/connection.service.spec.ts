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
  HttpClient,
  HttpErrorResponse,
  provideHttpClient,
  withInterceptors,
} from "@angular/common/http";
import {
  HttpTestingController,
  provideHttpClientTesting,
} from "@angular/common/http/testing";
import { TestBed } from "@angular/core/testing";
import { firstValueFrom } from "rxjs";
import {
  ConnectionStatus,
  HANG_MS,
  HANG_REASON,
  connectionInterceptor,
  unreachableReason,
} from "./connection.service";

describe("unreachableReason", () => {
  it("names network failures and gateway answers, and nothing else", () => {
    const e = (status: number, error: unknown = null) =>
      new HttpErrorResponse({ status, error });
    expect(unreachableReason(e(0))).toContain("the connection to it failed");
    expect(unreachableReason(e(502))).toContain("502 Bad Gateway");
    expect(unreachableReason(e(503))).toContain("503 Service Unavailable");
    expect(unreachableReason(e(504))).toContain("504 Gateway Timeout");
    expect(unreachableReason(e(503, { code: "verifier_unavailable" }))).toBe(
      "",
    );
    expect(unreachableReason(e(500))).toBe("");
    expect(unreachableReason(e(401))).toBe("");
    expect(unreachableReason(new Error("x"))).toBe("");
  });
});

describe("connectionInterceptor", () => {
  let http: HttpClient;
  let ctl: HttpTestingController;
  let conn: ConnectionStatus;
  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [
        provideHttpClient(withInterceptors([connectionInterceptor])),
        provideHttpClientTesting(),
      ],
    });
    http = TestBed.inject(HttpClient);
    ctl = TestBed.inject(HttpTestingController);
    conn = TestBed.inject(ConnectionStatus);
  });
  afterEach(() => {
    vi.useRealTimers();
    TestBed.resetTestingModule();
  });

  it("raises on a failed connection and clears on the next answer", async () => {
    const a = firstValueFrom(http.get("/api/tasks")).catch(() => null);
    ctl
      .expectOne("/api/tasks")
      .error(new ProgressEvent("error"), { status: 0, statusText: "" });
    await a;
    expect(conn.disconnection()?.reason).toContain("failed");
    const since = conn.disconnection()?.since;

    const b = firstValueFrom(http.get("/api/config")).catch(() => null);
    ctl
      .expectOne("/api/config")
      .flush("bad gateway", { status: 502, statusText: "Bad Gateway" });
    await b;
    expect(conn.disconnection()?.reason).toContain("502");
    expect(conn.disconnection()?.since).toBe(since);

    const c = firstValueFrom(http.get("/api/tasks"));
    ctl.expectOne("/api/tasks").flush([]);
    await c;
    expect(conn.disconnection()).toBeNull();
  });

  it("counts a daemon error answer as reachable", async () => {
    conn.unreachable("x");
    const a = firstValueFrom(http.post("/api/tasks", {})).catch(() => null);
    ctl
      .expectOne("/api/tasks")
      .flush({ error: "nope" }, { status: 500, statusText: "Server Error" });
    await a;
    expect(conn.disconnection()).toBeNull();
  });

  it("raises when a polled read goes unanswered and fails it so the poll can go on", async () => {
    vi.useFakeTimers();
    const p = firstValueFrom(http.get("/api/tasks")).then(
      () => "answered",
      (e: HttpErrorResponse) => `failed ${e.status}`,
    );
    const req = ctl.expectOne("/api/tasks");
    vi.advanceTimersByTime(HANG_MS + 1);
    expect(await p).toBe("failed 0");
    expect(req.cancelled).toBe(true);
    expect(conn.disconnection()?.reason).toBe(HANG_REASON);
  });

  it("leaves other requests unbounded and non-API requests alone", async () => {
    vi.useFakeTimers();
    const p = firstValueFrom(http.get("/api/logs"));
    const req = ctl.expectOne("/api/logs");
    vi.advanceTimersByTime(HANG_MS * 3);
    expect(req.cancelled).toBe(false);
    req.flush({});
    await p;
    const q = firstValueFrom(http.get("/next/assets/x.json")).catch(() => null);
    ctl
      .expectOne("/next/assets/x.json")
      .error(new ProgressEvent("error"), { status: 0, statusText: "" });
    await q;
    expect(conn.disconnection()).toBeNull();
  });
});
