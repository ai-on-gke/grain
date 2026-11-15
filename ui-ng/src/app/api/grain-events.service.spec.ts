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
import { GrainEvents } from "./grain-events.service";

class FakeEventSource {
  static last: FakeEventSource | null = null;
  readyState = 1;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  listeners = new Map<string, (e: MessageEvent) => void>();
  closed = false;
  constructor(readonly url: string) {
    FakeEventSource.last = this;
  }
  addEventListener(type: string, fn: (e: MessageEvent) => void): void {
    this.listeners.set(type, fn);
  }
  close(): void {
    this.closed = true;
  }
  emit(type: string, data: unknown): void {
    this.listeners.get(type)?.({ data: JSON.stringify(data) } as MessageEvent);
  }
}

describe("GrainEvents", () => {
  it("opens one stream, delivers named events, and closes with its last subscriber", () => {
    const events = TestBed.inject(GrainEvents);
    events.factory = (url) =>
      new FakeEventSource(url) as unknown as EventSource;
    const got: string[] = [];
    const sub = events.events().subscribe((e) => got.push(e.type));
    const src = FakeEventSource.last!;
    expect(src.url).toBe("/api/events?since=now&stream=1");
    src.emit("task.state", { id: "1" });
    src.emit("task.question", { id: "2" });
    expect(got).toEqual(["task.state", "task.question"]);
    sub.unsubscribe();
    expect(src.closed).toBe(true);
  });

  it("closes on pagehide and reconnects on pageshow", () => {
    const events = TestBed.inject(GrainEvents);
    let count = 0;
    events.factory = (url) => {
      count += 1;
      return new FakeEventSource(url) as unknown as EventSource;
    };
    const sub = events.events().subscribe();
    const first = FakeEventSource.last!;
    expect(count).toBe(1);
    expect(first.closed).toBe(false);

    window.dispatchEvent(new Event("pagehide"));
    expect(first.closed).toBe(true);

    window.dispatchEvent(new Event("pageshow"));
    expect(count).toBe(2);
    expect(FakeEventSource.last).not.toBe(first);
    expect(FakeEventSource.last!.closed).toBe(false);

    sub.unsubscribe();
    expect(FakeEventSource.last!.closed).toBe(true);
  });
});
