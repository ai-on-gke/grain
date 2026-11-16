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

import { TestBed } from "@angular/core/testing";
import { NEVER } from "rxjs";
import { afterEach, describe, expect, it } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { Comment, TaskDetail } from "../api/models";
import { TaskStore } from "./task.store";

const mk = (id: number): Comment => ({ id, author: "a", body: `c${id}` });
const range = (from: number, to: number) =>
  Array.from({ length: to - from + 1 }, (_, i) => mk(from + i));

// A conversation of comments 1..total on the server; the poll answers the
// newest 100, a comments_before read the page before it. A read can be
// held (hold) or made to fail (fail) to drive the races review 6251 found.
class ServerApi {
  total = 300;
  calls: string[] = [];
  fail = false;
  private holds: Array<() => void> = [];
  hold = false;

  private page(before: number, limit: number): TaskDetail {
    const end = Math.min(before - 1, this.total);
    const start = Math.max(1, end - limit + 1);
    return {
      id: "7",
      title: "t",
      state: "running",
      comments: range(start, end),
      olderComments: start - 1,
    } as TaskDetail;
  }

  async getIfChanged(_r: string, _p: unknown, _q: string) {
    return { changed: true, body: this.page(this.total + 1, 100) };
  }

  async get(_r: string, _p: unknown, q: string): Promise<TaskDetail> {
    this.calls.push(q);
    if (this.hold) await new Promise<void>((r) => this.holds.push(r));
    if (this.fail) throw { status: 503, error: { error: "database busy" } };
    const m = /comments_limit=(\d+)&comments_before=(\d+)/.exec(q)!;
    return this.page(Number(m[2]), Number(m[1]));
  }

  release(): void {
    this.hold = false;
    for (const r of this.holds.splice(0)) r();
  }
}

function setup(api: ServerApi): TaskStore {
  TestBed.configureTestingModule({
    providers: [
      { provide: GrainApi, useValue: api },
      { provide: GrainEvents, useValue: { events: () => NEVER } },
    ],
  });
  return TestBed.inject(TaskStore);
}

const ids = (s: TaskStore) => (s.detail()?.comments ?? []).map((c) => c.id);

describe("TaskStore paging back for a #comment= link", () => {
  afterEach(() => {
    TestBed.inject(TaskStore).close();
    TestBed.resetTestingModule();
  });

  it("finds a comment pages back, and says missing only when it is not there", async () => {
    const api = new ServerApi();
    const s = setup(api);
    s.open("7");
    await s.refresh();
    expect(await s.loadCommentsThrough(5)).toBe("found");
    expect(ids(s)[0]).toBe(1);
    expect(await s.loadCommentsThrough(0)).toBe("missing");
  });

  it("waits for a walk already out instead of reporting the comment missing", async () => {
    const api = new ServerApi();
    const s = setup(api);
    s.open("7");
    await s.refresh();
    api.hold = true;
    const first = s.loadCommentsThrough(150);
    const second = s.loadCommentsThrough(3);
    api.release();
    expect(await first).toBe("found");
    expect(await second).toBe("found");
    expect(ids(s)).toContain(3);
  });

  it("answers aborted, not missing, when a page read fails", async () => {
    const api = new ServerApi();
    const s = setup(api);
    s.open("7");
    await s.refresh();
    api.fail = true;
    expect(await s.loadCommentsThrough(5)).toBe("aborted");
    expect(s.actionError()).toBeTruthy();
  });

  it("keeps the comments at the seam when a poll slides the newest page forward", async () => {
    const api = new ServerApi();
    const s = setup(api);
    s.open("7");
    await s.refresh();
    expect(await s.loadCommentsThrough(5)).toBe("found");
    api.total = 302;
    await s.refresh();
    expect(ids(s)).toEqual(range(1, 302).map((c) => c.id));
  });
});
