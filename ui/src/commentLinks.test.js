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
  malformedCommentHash,
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

describe("malformedCommentHash", () => {
  it("is true for a #comment= naming no comment", () => {
    for (const hash of [
      "#comment=",
      "#comment=abc",
      "#comment=13x",
      "comment=-1",
    ]) {
      expect(malformedCommentHash(hash)).toBe(true);
    }
  });

  it("is false for a comment link and for any other hash", () => {
    for (const hash of ["#comment=13", "", "#", "#task-13", "#ci", null]) {
      expect(malformedCommentHash(hash)).toBe(false);
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

  // The pane's "← Back" returns to the page it was opened over, which
  // App reads from the entry's state on this push and on any later Back
  // or Forward onto it (the Inbox's question rows go through here).
  it("keeps the page a task is opened over in the pushed entry", () => {
    window.history.replaceState(null, "", "/inbox?state=open");
    navigateInApp("/tasks/4#comment=37");
    expect(window.history.state).toEqual({ behind: "/inbox?state=open" });
    // From that pane to another: the inbox is still what is behind.
    navigateInApp("/tasks/8");
    expect(window.history.state).toEqual({ behind: "/inbox?state=open" });
    // A pane loaded on its own has no page behind it to keep.
    window.history.replaceState(null, "", "/tasks/9");
    navigateInApp("/tasks/10");
    expect(window.history.state).toEqual({});
  });
});
