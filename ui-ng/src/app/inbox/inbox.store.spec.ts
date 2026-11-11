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
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { LocalScriptApproval, Notification } from "../api/models";
import { InboxStore } from "./inbox.store";

class FakeEventSource {
  static last: FakeEventSource | null = null;
  readyState = 1;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  listeners = new Map<string, (e: MessageEvent) => void>();
  constructor(readonly url: string) {
    FakeEventSource.last = this;
  }
  addEventListener(type: string, fn: (e: MessageEvent) => void): void {
    this.listeners.set(type, fn);
  }
  close(): void {}
  emit(type: string, data: unknown): void {
    this.listeners.get(type)?.({ data: JSON.stringify(data) } as MessageEvent);
  }
}

class FakeApi {
  notes: Notification[] = [];
  scripts: LocalScriptApproval[] = [];
  reads = 0;
  config = () => Promise.resolve({ access: null, version: "v1" });
  tasks = () => Promise.resolve([]);
  task = (id: string) => Promise.resolve({ id });
  openNotifications = () => {
    this.reads++;
    return Promise.resolve(this.notes);
  };
  localScriptApprovals = () => Promise.resolve(this.scripts);
}

const settle = () => new Promise((r) => setTimeout(r, 400));

describe("InboxStore", () => {
  afterEach(() => TestBed.inject(InboxStore).stop());

  it("reads the queue again on notification and host-script events, not only on its timer", async () => {
    localStorage.clear();
    const api = new FakeApi();
    TestBed.configureTestingModule({
      providers: [{ provide: GrainApi, useValue: api }],
    });
    const events = TestBed.inject(GrainEvents);
    events.factory = (url) =>
      new FakeEventSource(url) as unknown as EventSource;
    const store = TestBed.inject(InboxStore);
    store.start();
    await settle();
    expect(store.notifications()).toEqual([]);
    const src = FakeEventSource.last!;

    api.notes = [
      {
        id: 7,
        subject: "ci.red:acme/widgets",
        family: "ci.red",
        severity: "warning",
        title: "acme/widgets main is red",
      } as Notification,
    ];
    const before = api.reads;
    src.emit("notification.raised", { type: "notification.raised" });
    await settle();
    expect(api.reads).toBe(before + 1);
    expect(store.notifications().map((n) => n.id)).toEqual([7]);

    api.notes = [];
    src.emit("notification.cleared", { type: "notification.cleared" });
    await settle();
    expect(store.notifications()).toEqual([]);

    for (const type of [
      "notification.acked",
      "local_script.pending",
      "local_script.answered",
    ]) {
      const n = api.reads;
      src.emit(type, { type });
      await settle();
      expect(api.reads, type).toBe(n + 1);
    }
  });
});
