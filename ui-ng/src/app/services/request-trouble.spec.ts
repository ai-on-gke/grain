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

import { describe, expect, it } from "vitest";
import {
  POLL_BACKOFF_MAX_MS,
  ReadTrouble,
  SUSTAINED_MS,
  retryAfterMs,
} from "./request-trouble";

const headers = (h: Record<string, string>) => ({
  get: (name: string) => h[name] ?? null,
});

describe("ReadTrouble", () => {
  it("is not sustained by one or two failures, and is by the third", () => {
    const t = new ReadTrouble();
    expect(t.fail({}, 0)).toBe(false);
    expect(t.fail({}, 1000)).toBe(false);
    expect(t.fail({}, 2000)).toBe(true);
  });

  it("is sustained by failures lasting SUSTAINED_MS however few", () => {
    const t = new ReadTrouble();
    expect(t.fail({}, 0)).toBe(false);
    expect(t.fail({}, SUSTAINED_MS)).toBe(true);
  });

  it("starts over after a success", () => {
    const t = new ReadTrouble();
    t.fail({}, 0);
    t.fail({}, 1);
    t.ok();
    expect(t.failing).toBe(false);
    expect(t.fail({}, 2)).toBe(false);
  });

  it("backs off exponentially, capped, and honours Retry-After", () => {
    const t = new ReadTrouble();
    expect(t.nextDelay(3000)).toBe(3000);
    t.fail({});
    expect(t.nextDelay(3000)).toBe(3000);
    t.fail({});
    expect(t.nextDelay(3000)).toBe(6000);
    t.fail({});
    expect(t.nextDelay(3000)).toBe(12000);
    for (let i = 0; i < 10; i++) t.fail({});
    expect(t.nextDelay(3000)).toBe(POLL_BACKOFF_MAX_MS);
    t.ok();
    t.fail({ headers: headers({ "Retry-After": "20" }) });
    expect(t.nextDelay(3000)).toBe(20000);
  });
});

describe("retryAfterMs", () => {
  it("reads seconds and dates, and nothing else", () => {
    expect(retryAfterMs({ headers: headers({ "Retry-After": "5" }) })).toBe(
      5000,
    );
    const now = Date.parse("2026-11-15T00:00:00Z");
    expect(
      retryAfterMs(
        {
          headers: headers({ "Retry-After": "Sun, 15 Nov 2026 00:00:10 GMT" }),
        },
        now,
      ),
    ).toBe(10000);
    expect(retryAfterMs({})).toBeNull();
  });
});
