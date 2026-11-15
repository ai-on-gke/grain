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
import { GrainApi, routePath } from "./grain-api.service";

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
