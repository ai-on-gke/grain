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

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  commentIdFromHash,
  commentPath,
  commentUrl,
  inAppHref,
  navigateInApp,
} from "./commentLinks.js";

describe("commentIdFromHash", () => {
  it("reads the comment id off #comment=<id>", () => {
    expect(commentIdFromHash("#comment=13")).toBe("13");
    expect(commentIdFromHash("comment=7")).toBe("7");
  });

  it("is empty for any other hash", () => {
    for (const hash of [
      "",
      "#",
      "#comment=",
      "#comment=abc",
      "#comment=13x",
      "#task-13",
      "#ci",
      "#comment=1&x=2",
      undefined,
      null,
    ]) {
      expect(commentIdFromHash(hash)).toBe("");
    }
  });
});

describe("commentPath and commentUrl", () => {
  it("builds the root-relative link", () => {
    expect(commentPath("31", 13)).toBe("/tasks/31#comment=13");
  });

  it("makes it absolute against the page's own origin for Copy link", () => {
    expect(commentUrl("31", 13)).toBe(
      `${window.location.origin}/tasks/31#comment=13`,
    );
    expect(commentUrl("31", 13, "https://grain.example.com")).toBe(
      "https://grain.example.com/tasks/31#comment=13",
    );
  });

  it("round-trips through the hash parser", () => {
    const hash = commentPath("31", 13).split("#")[1];
    expect(commentIdFromHash(`#${hash}`)).toBe("13");
  });
});

describe("inAppHref", () => {
  it("is true for the UI's own task pages", () => {
    expect(inAppHref("/tasks/31")).toBe(true);
    expect(inAppHref("/tasks/31#comment=13")).toBe(true);
    expect(inAppHref("/tasks/31?x=1")).toBe(true);
  });

  it("is false for everything else", () => {
    for (const href of [
      "https://example.com/tasks/31",
      "//example.com/tasks/31",
      "/api/tasks/31/attachments/2",
      "/tasks/31/attempts/2",
      "/tasks/",
      "/settings/ci",
      "#task-31",
      "",
    ]) {
      expect(inAppHref(href)).toBe(false);
    }
  });
});

describe("navigateInApp", () => {
  afterEach(() => {
    window.history.replaceState(null, "", "/");
  });

  it("pushes the address and tells the app about it without a reload", () => {
    window.history.replaceState(null, "", "/tasks/30");
    const pop = vi.fn();
    const hash = vi.fn();
    window.addEventListener("popstate", pop);
    window.addEventListener("hashchange", hash);
    try {
      navigateInApp("/tasks/31#comment=13");
      expect(window.location.pathname).toBe("/tasks/31");
      expect(window.location.hash).toBe("#comment=13");
      expect(pop).toHaveBeenCalledTimes(1);
      expect(hash).toHaveBeenCalledTimes(1);
      // The same task again: only the hash moved, so no popstate.
      navigateInApp("/tasks/31#comment=14");
      expect(pop).toHaveBeenCalledTimes(1);
      expect(hash).toHaveBeenCalledTimes(2);
    } finally {
      window.removeEventListener("popstate", pop);
      window.removeEventListener("hashchange", hash);
    }
  });
});
