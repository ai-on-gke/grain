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
import { provideRouter } from "@angular/router";
import { NEVER } from "rxjs";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { GrainEvents } from "./grain-events.service";
import { GrainApi, routePath } from "./grain-api.service";
import { TasksPageComponent } from "../tasks/tasks-page.component";

describe("GrainApi.getIfChanged", () => {
  let api: GrainApi;
  let http: HttpTestingController;
  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    api = TestBed.inject(GrainApi);
    http = TestBed.inject(HttpTestingController);
  });
  afterEach(() => {
    http.verify();
    TestBed.resetTestingModule();
  });

  it("sends If-None-Match after the first read and returns the cached body on 304", async () => {
    const first = api.getIfChanged<{ id: string }[]>("GET /api/tasks");
    const req1 = http.expectOne("/api/tasks");
    expect(req1.request.headers.has("If-None-Match")).toBe(false);
    req1.flush([{ id: "1" }], { headers: { ETag: '"v1"' } });
    expect(await first).toEqual({ body: [{ id: "1" }], changed: true });

    const second = api.getIfChanged<{ id: string }[]>("GET /api/tasks");
    const req2 = http.expectOne("/api/tasks");
    expect(req2.request.headers.get("If-None-Match")).toBe('"v1"');
    req2.flush(null, { status: 304, statusText: "Not Modified" });
    expect(await second).toEqual({ body: [{ id: "1" }], changed: false });
  });

  it("keeps a separate tag per query and rethrows real errors", async () => {
    const a = api.getIfChanged("GET /api/tasks", undefined, "state=closed");
    http
      .expectOne("/api/tasks?state=closed")
      .flush({ tasks: [] }, { headers: { ETag: '"c1"' } });
    await a;
    const b = api.getIfChanged("GET /api/tasks");
    const req = http.expectOne("/api/tasks");
    expect(req.request.headers.has("If-None-Match")).toBe(false);
    req.flush("boom", { status: 500, statusText: "Server Error" });
    await expect(b).rejects.toMatchObject({ status: 500 });
  });

  it("names the problem when a 304 arrives with no cached copy", async () => {
    const p = api.getIfChanged("GET /api/tasks");
    http
      .expectOne("/api/tasks")
      .flush(null, { status: 304, statusText: "Not Modified" });
    await expect(p).rejects.toThrow(/304 Not Modified with no cached copy/);
  });

  it("forgets cached tags after clearConditionalCache", async () => {
    const a = api.getIfChanged("GET /api/tasks");
    http.expectOne("/api/tasks").flush([], { headers: { ETag: '"v1"' } });
    await a;
    api.clearConditionalCache();
    const b = api.getIfChanged("GET /api/tasks");
    const req = http.expectOne("/api/tasks");
    expect(req.request.headers.has("If-None-Match")).toBe(false);
    req.flush([]);
    await b;
  });

  it("formats {path...} namespace routes, reads plain text, and passes query strings on post/delete", async () => {
    expect(routePath("GET /api/fs/memory/{path...}", { path: "" })).toBe(
      "/api/fs/memory/",
    );
    expect(
      routePath("GET /api/fs/memory/{path...}", {
        path: "repos/acme/widgets.md",
      }),
    ).toBe("/api/fs/memory/repos/acme/widgets.md");

    const txt = api.getText(
      "GET /api/fs/memory/{path...}",
      { path: "global.md" },
      "tail=64",
    );
    const reqTxt = http.expectOne("/api/fs/memory/global.md?tail=64");
    expect(reqTxt.request.responseType).toBe("text");
    reqTxt.flush("# Global memory\n");
    expect(await txt).toBe("# Global memory\n");

    const flush = api.post(
      "POST /api/package-cache/flush",
      undefined,
      {},
      "kind=npm",
    );
    http
      .expectOne("/api/package-cache/flush?kind=npm")
      .flush({ entries: 2, bytes: 1024 });
    expect(await flush).toEqual({ entries: 2, bytes: 1024 });

    const lift = api.delete(
      "DELETE /api/pause",
      undefined,
      undefined,
      "connection=c1",
    );
    http.expectOne("/api/pause?connection=c1").flush({ enabled: true });
    expect(await lift).toEqual({ enabled: true });
  });

  it("encodes {path...} wildcard segments, reads text, and appends query params to delete", async () => {
    expect(
      routePath("GET /api/fs/config/{path...}", {
        path: "tables/grain_config.json",
      }),
    ).toBe("/api/fs/config/tables/grain_config.json");

    const txtP = api.getText("GET /api/fs/config/{path...}", {
      path: "memory/deployment.md",
    });
    http
      .expectOne("/api/fs/config/memory/deployment.md")
      .flush("# Deployment memory");
    expect(await txtP).toBe("# Deployment memory");

    const delP = api.delete(
      "DELETE /api/upgrade/current",
      undefined,
      undefined,
      "reason=cancelled",
    );
    const delReq = http.expectOne("/api/upgrade/current?reason=cancelled");
    expect(delReq.request.method).toBe("DELETE");
    delReq.flush({ ok: true });
    await delP;
  });

  it("coalesces concurrent get and getIfChanged calls, caches config() briefly without ETag, and invalidates on mutations", async () => {
    // Concurrent get() shares one HTTP request.
    const [g1, g2] = [
      api.get<{ ok: boolean }>("GET /api/settings"),
      api.get<{ ok: boolean }>("GET /api/settings"),
    ];
    const reqSet = http.expectOne("/api/settings");
    reqSet.flush({ ok: true });
    expect(await g1).toEqual({ ok: true });
    expect(await g2).toEqual({ ok: true });

    // config() shares concurrent calls and short-TTL cached reads without sending If-None-Match;
    // POST /api/settings evicts the /api/config cache immediately.
    const [c1, c2] = [api.config(), api.config()];
    const cfg1 = http.expectOne("/api/config");
    expect(cfg1.request.headers.has("If-None-Match")).toBe(false);
    cfg1.flush({ version: "v1" });
    expect(await c1).toEqual({ version: "v1" });
    expect(await c2).toEqual({ version: "v1" });

    const c3 = api.config();
    http.expectNone("/api/config");
    expect(await c3).toEqual({ version: "v1" });

    const save = api.post("POST /api/settings", undefined, { maxWorkers: 2 });
    http.expectOne("/api/settings").flush({ maxWorkers: 2 });
    await save;

    const c4 = api.config();
    const cfg3 = http.expectOne("/api/config");
    expect(cfg3.request.headers.has("If-None-Match")).toBe(false);
    cfg3.flush({ version: "v2" });
    expect(await c4).toEqual({ version: "v2" });

    // taskSummary uses comments_limit=1&attempts_limit=1 and is invalidated by task comment mutation.
    const sum1 = api.taskSummary("42");
    http
      .expectOne("/api/tasks/42?comments_limit=1&attempts_limit=1")
      .flush({ id: "42" }, { headers: { ETag: '"t42-1"' } });
    expect(await sum1).toEqual({ id: "42" });

    // Unrelated prefix clear does not drop task 42's ETag.
    api.clearConditionalCache("/api/tasks/4");
    const sum2 = api.taskSummary("42");
    const sumReq2 = http.expectOne(
      "/api/tasks/42?comments_limit=1&attempts_limit=1",
    );
    expect(sumReq2.request.headers.get("If-None-Match")).toBe('"t42-1"');
    sumReq2.flush(null, { status: 304, statusText: "Not Modified" });
    await sum2;

    const comment = api.post(
      "POST /api/tasks/{id}/comments",
      { id: "42" },
      { body: "hi" },
    );
    http.expectOne("/api/tasks/42/comments").flush({ id: "42" });
    await comment;

    const sum3 = api.taskSummary("42");
    const sumReq3 = http.expectOne(
      "/api/tasks/42?comments_limit=1&attempts_limit=1",
    );
    expect(sumReq3.request.headers.has("If-None-Match")).toBe(false);
    sumReq3.flush({ id: "42" });
    await sum3;
  });

  it("scopes prefix clearConditionalCache invalidation without clobbering unrelated in-flight ETags", async () => {
    const cfgInflight = api.getIfChanged("GET /api/config");
    const taskCond = api.getIfChanged("GET /api/tasks/{id}", { id: "42" });
    const taskGet = api.get("GET /api/tasks/{id}", { id: "42" });

    api.clearConditionalCache("/api/tasks/42");

    http
      .expectOne("/api/config")
      .flush({ version: "v1" }, { headers: { ETag: '"cfg-ok"' } });
    const oldCondReq = http.match(
      (r) =>
        r.url === "/api/tasks/42" && r.headers.has("If-None-Match") === false,
    );
    expect(oldCondReq.length).toBeGreaterThanOrEqual(1);
    for (const r of oldCondReq) {
      r.flush({ id: "42", stale: true }, { headers: { ETag: '"t42-stale"' } });
    }
    await Promise.all([cfgInflight, taskCond, taskGet]);

    // /api/config retained its ETag despite the prefixed task clear.
    const cfgNext = api.getIfChanged("GET /api/config");
    const cfgNextReq = http.expectOne("/api/config");
    expect(cfgNextReq.request.headers.get("If-None-Match")).toBe('"cfg-ok"');
    cfgNextReq.flush(null, { status: 304, statusText: "Not Modified" });
    await cfgNext;

    // /api/tasks/42 did NOT store '"t42-stale"' and does not reuse evicted inflight get.
    const taskNext = api.getIfChanged("GET /api/tasks/{id}", { id: "42" });
    const taskNextReq = http.expectOne("/api/tasks/42");
    expect(taskNextReq.request.headers.has("If-None-Match")).toBe(false);
    taskNextReq.flush({ id: "42", stale: false });
    await taskNext;
  });
});

function verifierUnavailableOpts(retryAfter?: string) {
  return {
    status: 503,
    statusText: "Service Unavailable",
    ...(retryAfter !== undefined
      ? { headers: { "Retry-After": retryAfter } }
      : {}),
  };
}

function verifierUnavailableBody(ref = "0123456789ab") {
  return {
    error: `sign-in verification could not check your identity right now; retrying shortly (ref ${ref})`,
    code: "verifier_unavailable",
    retryAfter: 5,
    ref,
  };
}

describe("GrainApi verifier_unavailable retry", () => {
  let api: GrainApi;
  let http: HttpTestingController;

  beforeEach(() => {
    vi.useFakeTimers();
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    api = TestBed.inject(GrainApi);
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    try {
      http.verify();
    } finally {
      TestBed.resetTestingModule();
      vi.restoreAllMocks();
      vi.useRealTimers();
    }
  });

  it("retries a GET on 503 verifier_unavailable twice within Retry-After ±30% jitter bounds and resolves on 200", async () => {
    const p = api.get<{ id: string }[]>("GET /api/tasks");

    http
      .expectOne("/api/tasks")
      .flush(verifierUnavailableBody("r1"), verifierUnavailableOpts("4"));

    // Lower jitter bound for Retry-After: 4 is 2800ms (4s * 0.7).
    await vi.advanceTimersByTimeAsync(2799);
    http.expectNone("/api/tasks");

    // Upper jitter bound for Retry-After: 4 is 5200ms (4s * 1.3).
    await vi.advanceTimersByTimeAsync(5200 - 2799);
    const retry1 = http.expectOne("/api/tasks");
    retry1.flush(verifierUnavailableBody("r2"), verifierUnavailableOpts("4"));

    await vi.advanceTimersByTimeAsync(2799);
    http.expectNone("/api/tasks");

    await vi.advanceTimersByTimeAsync(5200 - 2799);
    const retry2 = http.expectOne("/api/tasks");
    retry2.flush([{ id: "42" }]);

    expect(await p).toEqual([{ id: "42" }]);
  });

  it("defaults Retry-After to 2s when absent and caps Retry-After at 10s", async () => {
    // Absent Retry-After -> 2s ± 30% => [1400ms, 2600ms].
    const p1 = api.get<{ ok: boolean }>("GET /api/config");
    http
      .expectOne("/api/config")
      .flush(verifierUnavailableBody(), verifierUnavailableOpts());

    await vi.advanceTimersByTimeAsync(1399);
    http.expectNone("/api/config");
    await vi.advanceTimersByTimeAsync(2600 - 1399);
    http.expectOne("/api/config").flush({ ok: true });
    expect(await p1).toEqual({ ok: true });

    // Large Retry-After (60s) -> capped at 10s ± 30% => [7000ms, 13000ms].
    const p2 = api.get<{ ok: boolean }>("GET /api/whoami");
    http
      .expectOne("/api/whoami")
      .flush(verifierUnavailableBody(), verifierUnavailableOpts("60"));

    await vi.advanceTimersByTimeAsync(6999);
    http.expectNone("/api/whoami");
    await vi.advanceTimersByTimeAsync(13000 - 6999);
    http.expectOne("/api/whoami").flush({ ok: true });
    expect(await p2).toEqual({ ok: true });
  });

  it("retries getIfChanged, getText, and head on 503 verifier_unavailable", async () => {
    // Seed ETag for getIfChanged first.
    const seed = api.getIfChanged<{ id: string }[]>("GET /api/tasks");
    http
      .expectOne("/api/tasks")
      .flush([{ id: "1" }], { headers: { ETag: '"v1"' } });
    await seed;

    const cond = api.getIfChanged<{ id: string }[]>("GET /api/tasks");
    const c0 = http.expectOne("/api/tasks");
    expect(c0.request.headers.get("If-None-Match")).toBe('"v1"');
    c0.flush(verifierUnavailableBody(), verifierUnavailableOpts("2"));
    await vi.advanceTimersByTimeAsync(2600);
    const c1 = http.expectOne("/api/tasks");
    expect(c1.request.headers.get("If-None-Match")).toBe('"v1"');
    c1.flush(null, { status: 304, statusText: "Not Modified" });
    expect(await cond).toEqual({ body: [{ id: "1" }], changed: false });

    // getText receives a JSON string body in HttpErrorResponse.error.
    const txt = api.getText("GET /api/fs/memory/{path...}", {
      path: "global.md",
    });
    http
      .expectOne("/api/fs/memory/global.md")
      .flush(
        JSON.stringify(verifierUnavailableBody()),
        verifierUnavailableOpts("2"),
      );
    await vi.advanceTimersByTimeAsync(2600);
    http.expectOne("/api/fs/memory/global.md").flush("# Memory");
    expect(await txt).toBe("# Memory");

    // head retries idempotently too.
    const h = api.head("GET /api/tasks");
    const h0 = http.expectOne("/api/tasks");
    expect(h0.request.method).toBe("HEAD");
    h0.flush(null, verifierUnavailableOpts("2"));
    // Non-JSON null body on HEAD cannot carry verifier_unavailable; test with JSON body.
    await expect(h).rejects.toMatchObject({ status: 503 });

    const hJson = api.head("GET /api/tasks");
    const hj0 = http.expectOne("/api/tasks");
    expect(hj0.request.method).toBe("HEAD");
    hj0.flush(
      verifierUnavailableBody() as unknown as null,
      verifierUnavailableOpts("2"),
    );
    await vi.advanceTimersByTimeAsync(2600);
    const hj1 = http.expectOne("/api/tasks");
    expect(hj1.request.method).toBe("HEAD");
    hj1.flush(null);
    await expect(hJson).resolves.toBeNull();
  });

  it("stops after at most 3 retries and surfaces the original error", async () => {
    let caught: unknown;
    const p = api.get("GET /api/tasks").catch((e) => {
      caught = e;
    });

    for (let attempt = 0; attempt < 3; attempt++) {
      http
        .expectOne("/api/tasks")
        .flush(
          verifierUnavailableBody(`attempt-${attempt}`),
          verifierUnavailableOpts("2"),
        );
      await vi.advanceTimersByTimeAsync(2600);
    }

    // 4th attempt (3rd retry) fails too -> no further retry scheduled.
    http
      .expectOne("/api/tasks")
      .flush(
        verifierUnavailableBody("attempt-final"),
        verifierUnavailableOpts("2"),
      );
    await vi.advanceTimersByTimeAsync(20_000);
    http.expectNone("/api/tasks");
    expect(vi.getTimerCount()).toBe(0);

    await p;
    expect(caught).toMatchObject({
      status: 503,
      error: {
        code: "verifier_unavailable",
        ref: "attempt-final",
      },
    });
  });

  it("stops retrying once the 20s total cap would be exceeded", async () => {
    // Fix jitter at 0% so Retry-After: 10 waits exactly 10s per retry.
    vi.spyOn(Math, "random").mockReturnValue(0.5);

    let caught: unknown;
    const p = api.get("GET /api/tasks").catch((e) => {
      caught = e;
    });

    // Attempt 0 at t=0 -> waits 10s -> Attempt 1 at t=10s -> waits 10s -> Attempt 2 at t=20s.
    http
      .expectOne("/api/tasks")
      .flush(verifierUnavailableBody("t0"), verifierUnavailableOpts("10"));
    await vi.advanceTimersByTimeAsync(10_000);
    http
      .expectOne("/api/tasks")
      .flush(verifierUnavailableBody("t10"), verifierUnavailableOpts("10"));
    await vi.advanceTimersByTimeAsync(10_000);
    http
      .expectOne("/api/tasks")
      .flush(verifierUnavailableBody("t20"), verifierUnavailableOpts("10"));

    // At t=20s, 2 retries have happened and the 20s window is reached: no 3rd retry.
    await vi.advanceTimersByTimeAsync(20_000);
    http.expectNone("/api/tasks");
    expect(vi.getTimerCount()).toBe(0);

    await p;
    expect(caught).toMatchObject({
      status: 503,
      error: { code: "verifier_unavailable", ref: "t20" },
    });
  });

  it("stops immediately when a positive-jitter retry delay would cross the 20s cap", async () => {
    // Math.random = 0.9 => +24% jitter => Retry-After: 10 waits 12,400ms.
    vi.spyOn(Math, "random").mockReturnValue(0.9);
    let caught: unknown;
    const p = api.get("GET /api/tasks").catch((e) => {
      caught = e;
    });

    http
      .expectOne("/api/tasks")
      .flush(verifierUnavailableBody("p0"), verifierUnavailableOpts("10"));
    await vi.advanceTimersByTimeAsync(12_400);
    http
      .expectOne("/api/tasks")
      .flush(verifierUnavailableBody("p1"), verifierUnavailableOpts("10"));

    // A second 12,400ms wait would reach 24,800ms > 20,000ms, so it surfaces p1 right away.
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.getTimerCount()).toBe(0);
    http.expectNone("/api/tasks");
    await p;
    expect(caught).toMatchObject({
      status: 503,
      error: { code: "verifier_unavailable", ref: "p1" },
    });
  });

  it("never retries POST, PUT, PATCH, or DELETE on 503 verifier_unavailable", async () => {
    const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
    const calls = [
      api.post("POST /api/tasks", undefined, { title: "x" }),
      api.put("PUT /api/tasks/{id}/goal", { id: "1" }, { kind: "base-green" }),
      api.patch("PATCH /api/tasks/{id}", { id: "1" }, { title: "y" }),
      api.delete("DELETE /api/tasks/{id}/goal", { id: "1" }),
    ];
    const settled = calls.map((p) =>
      p.then(
        () => ({ ok: true as const }),
        (err) => ({ ok: false as const, err }),
      ),
    );

    for (const url of ["/api/tasks", "/api/tasks/1/goal", "/api/tasks/1"]) {
      const matching = http.match(url);
      for (const req of matching) {
        req.flush(verifierUnavailableBody(), verifierUnavailableOpts("5"));
      }
    }

    // Must settle immediately without scheduling any timer or issuing retries.
    await vi.advanceTimersByTimeAsync(0);
    expect(setTimeoutSpy).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);

    const results = await Promise.all(settled);
    expect(results).toHaveLength(4);
    for (const r of results) {
      expect(r.ok).toBe(false);
      if (!r.ok) {
        expect(r.err).toMatchObject({
          status: 503,
          error: { code: "verifier_unavailable" },
        });
      }
    }
  });

  it("does not retry 401, 403, or a 503 with another code", async () => {
    const req401 = api.get("GET /api/tasks").then(
      () => null,
      (err) => err,
    );
    http
      .expectOne("/api/tasks")
      .flush(
        { error: "unauthorized" },
        { status: 401, statusText: "Unauthorized" },
      );
    expect(vi.getTimerCount()).toBe(0);
    expect(await req401).toMatchObject({ status: 401 });

    const req403 = api.get("GET /api/settings").then(
      () => null,
      (err) => err,
    );
    http
      .expectOne("/api/settings")
      .flush(
        { error: "forbidden", code: "delegate_not_allowed" },
        { status: 403, statusText: "Forbidden" },
      );
    expect(vi.getTimerCount()).toBe(0);
    expect(await req403).toMatchObject({ status: 403 });

    const req503Other = api.get("GET /api/config").then(
      () => null,
      (err) => err,
    );
    http
      .expectOne("/api/config")
      .flush(
        { error: "service unavailable", code: "unavailable" },
        verifierUnavailableOpts("5"),
      );
    expect(vi.getTimerCount()).toBe(0);
    expect(await req503Other).toMatchObject({
      status: 503,
      error: { code: "unavailable" },
    });

    const req503Text = api.get("GET /api/whoami").then(
      () => null,
      (err) => err,
    );
    http
      .expectOne("/api/whoami")
      .flush("gateway unavailable", verifierUnavailableOpts("5"));
    expect(vi.getTimerCount()).toBe(0);
    expect(await req503Text).toMatchObject({ status: 503 });

    // A 503 verifier_unavailable followed by a 401 on retry stops immediately at the 401.
    const reqFlip = api.get("GET /api/tasks").then(
      () => null,
      (err) => err,
    );
    http
      .expectOne("/api/tasks")
      .flush(verifierUnavailableBody("f1"), verifierUnavailableOpts("2"));
    await vi.advanceTimersByTimeAsync(2600);
    http
      .expectOne("/api/tasks")
      .flush(
        { error: "bad assertion" },
        { status: 401, statusText: "Unauthorized" },
      );
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.getTimerCount()).toBe(0);
    expect(await reqFlip).toMatchObject({ status: 401 });
  });

  it("shares one backoff timer across 20 concurrent GETs during a blip", async () => {
    const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
    const ids = Array.from({ length: 20 }, (_, i) => String(i + 1));
    const promises = ids.map((id) =>
      api.get<{ id: string }>("GET /api/tasks/{id}", { id }),
    );

    const initialReqs = http.match((r) => r.url.startsWith("/api/tasks/"));
    expect(initialReqs).toHaveLength(20);
    for (const req of initialReqs) {
      req.flush(verifierUnavailableBody(), verifierUnavailableOpts("5"));
    }

    // Drain microtasks so all 20 failures enter backoff.
    await vi.advanceTimersByTimeAsync(0);
    // Only ONE shared backoff timer was scheduled for all 20 requests.
    expect(setTimeoutSpy).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(1);

    // Before minimum jitter (5s * 0.7 = 3500ms), zero retries fire.
    await vi.advanceTimersByTimeAsync(3499);
    expect(http.match((r) => r.url.startsWith("/api/tasks/"))).toHaveLength(0);

    // By maximum jitter (5s * 1.3 = 6500ms), all 20 issue exactly 1 retry.
    await vi.advanceTimersByTimeAsync(6500 - 3499);
    const retryReqs = http.match((r) => r.url.startsWith("/api/tasks/"));
    expect(retryReqs).toHaveLength(20);
    for (const req of retryReqs) {
      const id = req.request.url.split("/").pop()!;
      req.flush({ id });
    }

    const resolved = await Promise.all(promises);
    expect(resolved.map((r) => r.id)).toEqual(ids);
  });

  it("bounds shared timers and total attempts when 20 concurrent GETs exhaust retries", async () => {
    const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
    const ids = Array.from({ length: 20 }, (_, i) => String(i + 1));
    let totalAttempts = 0;
    const results = ids.map((id) =>
      api.get<{ id: string }>("GET /api/tasks/{id}", { id }).then(
        () => ({ ok: true as const }),
        (err) => ({ ok: false as const, err }),
      ),
    );

    for (let round = 0; round < 3; round++) {
      const reqs = http.match((r) => r.url.startsWith("/api/tasks/"));
      expect(reqs).toHaveLength(20);
      totalAttempts += reqs.length;
      for (const req of reqs) {
        req.flush(
          verifierUnavailableBody(`r${round}`),
          verifierUnavailableOpts("4"),
        );
      }
      await vi.advanceTimersByTimeAsync(0);
      expect(setTimeoutSpy).toHaveBeenCalledTimes(round + 1);
      expect(vi.getTimerCount()).toBe(1);
      await vi.advanceTimersByTimeAsync(5200);
    }

    const finalReqs = http.match((r) => r.url.startsWith("/api/tasks/"));
    expect(finalReqs).toHaveLength(20);
    totalAttempts += finalReqs.length;
    for (const req of finalReqs) {
      req.flush(verifierUnavailableBody("final"), verifierUnavailableOpts("4"));
    }

    await vi.advanceTimersByTimeAsync(10_000);
    expect(vi.getTimerCount()).toBe(0);
    expect(setTimeoutSpy).toHaveBeenCalledTimes(3);
    expect(totalAttempts).toBe(80);

    const settled = await Promise.all(results);
    for (const r of settled) {
      expect(r.ok).toBe(false);
      if (!r.ok) {
        expect(r.err).toMatchObject({
          status: 503,
          error: { code: "verifier_unavailable", ref: "final" },
        });
      }
    }
  });

  it("shows no page error banner while retrying and surfaces the banner once retries are exhausted", async () => {
    TestBed.resetTestingModule();
    TestBed.configureTestingModule({
      imports: [TasksPageComponent],
      providers: [
        provideRouter([{ path: "**", children: [] }]),
        provideHttpClient(),
        provideHttpClientTesting(),
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    http = TestBed.inject(HttpTestingController);

    const fixture = TestBed.createComponent(TasksPageComponent);
    fixture.autoDetectChanges();
    const el = fixture.nativeElement as HTMLElement;

    const flushAll503 = (ref: string) => {
      const pending = http.match(() => true);
      expect(pending.length).toBeGreaterThan(0);
      for (const req of pending) {
        req.flush(verifierUnavailableBody(ref), verifierUnavailableOpts("5"));
      }
    };

    // Initial load hits 503 -> no banner while waiting to retry.
    flushAll503("b0");
    await vi.advanceTimersByTimeAsync(100);
    fixture.detectChanges();
    expect(el.querySelector(".banner-error")).toBeNull();

    // Retry 1, 2, and 3 fail with 503; banner stays hidden until exhaustion.
    for (let retry = 1; retry <= 3; retry++) {
      await vi.advanceTimersByTimeAsync(6500);
      flushAll503(`b${retry}`);
      await vi.advanceTimersByTimeAsync(0);
      fixture.detectChanges();
      if (retry < 3) {
        expect(el.querySelector(".banner-error")).toBeNull();
      }
    }

    const banner = el.querySelector(".banner-error");
    expect(banner).not.toBeNull();
    expect(banner?.textContent).toContain(
      "sign-in verification could not check your identity right now; retrying shortly",
    );
    expect(banner?.textContent).toContain("b3");

    fixture.destroy();
    // Flush any queued follow-up poll started when the initial feed settled.
    for (const req of http.match(() => true)) {
      req.flush([]);
    }
  });

  it("recovers cleanly on a page without ever showing the error banner when a retry succeeds", async () => {
    TestBed.resetTestingModule();
    TestBed.configureTestingModule({
      imports: [TasksPageComponent],
      providers: [
        provideRouter([{ path: "**", children: [] }]),
        provideHttpClient(),
        provideHttpClientTesting(),
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    http = TestBed.inject(HttpTestingController);

    const fixture = TestBed.createComponent(TasksPageComponent);
    fixture.autoDetectChanges();
    const el = fixture.nativeElement as HTMLElement;

    // Initial attempt fails with 503 -> no banner while waiting.
    for (const req of http.match(() => true)) {
      req.flush(verifierUnavailableBody("r0"), verifierUnavailableOpts("4"));
    }
    await vi.advanceTimersByTimeAsync(2000);
    fixture.detectChanges();
    expect(el.querySelector(".banner-error")).toBeNull();

    // Retry 1 fails with 503 -> still no banner.
    await vi.advanceTimersByTimeAsync(3200);
    for (const req of http.match(() => true)) {
      req.flush(verifierUnavailableBody("r1"), verifierUnavailableOpts("4"));
    }
    await vi.advanceTimersByTimeAsync(0);
    fixture.detectChanges();
    expect(el.querySelector(".banner-error")).toBeNull();

    // Retry 2 succeeds with 200 -> page renders tasks, zero error banners.
    await vi.advanceTimersByTimeAsync(5200);
    for (const req of http.match(() => true)) {
      req.flush([{ id: "7", title: "Recovered task", state: "queued" }]);
    }
    await vi.advanceTimersByTimeAsync(0);
    fixture.detectChanges();
    expect(el.querySelector(".banner-error")).toBeNull();
    expect(el.textContent).toContain("Recovered task");

    fixture.destroy();
    for (const req of http.match(() => true)) {
      if (req.request.url === "/api/base-branch-gates") {
        req.flush({ gates: [] });
      } else {
        req.flush([]);
      }
    }
  });
});
