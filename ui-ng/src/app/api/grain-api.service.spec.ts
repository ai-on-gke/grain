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

    const lift = api.delete("DELETE /api/pause", undefined, "connection=c1");
    http.expectOne("/api/pause?connection=c1").flush({ enabled: true });
    expect(await lift).toEqual({ enabled: true });
  });
});
