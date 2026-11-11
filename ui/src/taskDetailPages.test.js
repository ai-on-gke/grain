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
  earlierButtonLabel,
  mergeFreshDetail,
  prependEarlierPage,
  taskDetailPath,
  taskEarlierPath,
} from "./taskDetailPages.js";

describe("taskDetailPages", () => {
  it("builds bounded head and earlier URLs", () => {
    expect(taskDetailPath("42")).toBe(
      "/api/tasks/42?comments_limit=50&attempts_limit=20",
    );
    expect(
      taskEarlierPath({
        id: "42",
        comments: [{ id: 71 }],
        olderComments: 70,
        attempts: [{ number: 5 }],
        olderAttempts: 0,
      }),
    ).toBe(
      "/api/tasks/42?comments_limit=50&comments_before=71&attempts_limit=1",
    );
    expect(
      taskEarlierPath({
        id: "42",
        comments: [{ id: 71 }],
        olderComments: 70,
        attempts: [{ number: 24 }],
        olderAttempts: 23,
      }),
    ).toBe(
      "/api/tasks/42?comments_limit=50&comments_before=71&attempts_limit=20&attempts_before=24",
    );
  });

  it("prepends earlier pages and preserves them across head polls", () => {
    const initial = {
      id: "42",
      comments: [
        { id: 4, body: "d" },
        { id: 5, body: "e" },
      ],
      olderComments: 3,
      attempts: [{ number: 3 }],
      olderAttempts: 2,
    };
    const expanded = prependEarlierPage(
      initial,
      {
        id: "42",
        comments: [
          { id: 2, body: "b" },
          { id: 3, body: "c" },
        ],
        olderComments: 1,
        attempts: [{ number: 1 }, { number: 2 }],
        olderAttempts: 0,
      },
      { loadedComments: true, loadedAttempts: true },
    );
    expect(expanded.comments.map((c) => c.id)).toEqual([2, 3, 4, 5]);
    expect(expanded.olderComments).toBe(1);
    expect(expanded.attempts.map((a) => a.number)).toEqual([1, 2, 3]);
    expect(expanded.olderAttempts).toBe(0);

    const polled = mergeFreshDetail(expanded, {
      id: "42",
      state: "completed",
      comments: [
        { id: 5, body: "e" },
        { id: 6, body: "f" },
      ],
      olderComments: 4,
      attempts: [{ number: 3 }, { number: 4 }],
      olderAttempts: 2,
    });
    expect(polled.state).toBe("completed");
    expect(polled.comments.map((c) => c.id)).toEqual([2, 3, 4, 5, 6]);
    expect(polled.olderComments).toBe(1);
    expect(polled.attempts.map((a) => a.number)).toEqual([1, 2, 3, 4]);
    expect(polled.olderAttempts).toBe(0);
  });

  it("formats the Show earlier label cleanly", () => {
    expect(earlierButtonLabel(12, 0)).toBe("Show earlier (12 comments)");
    expect(earlierButtonLabel(1, 3)).toBe("Show earlier (1 comment, 3 runs)");
    expect(earlierButtonLabel(0, 1)).toBe("Show earlier (1 run)");
    expect(earlierButtonLabel(0, 0)).toBe("");
  });
});
