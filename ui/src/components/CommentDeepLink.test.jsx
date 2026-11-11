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

import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import DetailOverlay from "./DetailOverlay.jsx";
import { resetDetailMocks, baseTask, config } from "./detail/testFixtures.js";

vi.mock("../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../attachments.js", () => ({ default: vi.fn() }));

// A conversation long enough that the comment linked to is nowhere near
// either end of it.
function longConversation(n = 150) {
  return Array.from({ length: n }, (_, i) => ({
    id: 1000 + i,
    author: i % 2 ? "alice" : "run",
    authorKind: i % 2 ? "human" : "agent",
    body: `message number ${i}`,
    createdAt: new Date(Date.UTC(2026, 10, 1, 0, i)).toISOString(),
  }));
}

function renderTask(task) {
  return render(
    <DetailOverlay
      task={task}
      tasks={[]}
      config={config}
      onClose={() => {}}
      onOpenTask={() => {}}
      act={vi.fn()}
    />,
  );
}

describe("comment deep links (/tasks/<id>#comment=<id>)", () => {
  let scrolled;
  beforeEach(() => {
    scrolled = [];
    // Only the comments scrolled to: the composer brings itself into
    // view too.
    Element.prototype.scrollIntoView = vi.fn(function () {
      if (this.hasAttribute("data-comment-id")) scrolled.push(this);
    });
  });
  afterEach(() => {
    resetDetailMocks();
    delete Element.prototype.scrollIntoView;
    window.history.replaceState(null, "", "/");
  });

  it("scrolls to and highlights a comment deep in a long conversation", async () => {
    window.history.replaceState(null, "", "/tasks/12#comment=1042");
    renderTask({ ...baseTask, comments: longConversation() });
    await waitFor(() => expect(scrolled).toHaveLength(1));
    const el = scrolled[0];
    expect(el.getAttribute("data-comment-id")).toBe("1042");
    expect(el.id).toBe("comment-1042");
    expect(el).toHaveTextContent("message number 42");
    expect(el).toHaveClass("comment-highlight");
    expect(screen.queryByTestId("comment-link-notice")).toBeNull();
    // Focus goes with it, for a screen reader to read the comment out.
    expect(el.getAttribute("tabindex")).toBe("-1");
    expect(document.activeElement).toBe(el);
  });

  it("follows a hash changed on the task already open", async () => {
    window.history.replaceState(null, "", "/tasks/12");
    renderTask({ ...baseTask, comments: longConversation() });
    expect(scrolled).toHaveLength(0);
    act(() => {
      window.history.pushState(null, "", "/tasks/12#comment=1100");
      window.dispatchEvent(new HashChangeEvent("hashchange"));
    });
    await waitFor(() => expect(scrolled).toHaveLength(1));
    expect(scrolled[0]).toHaveTextContent("message number 100");
  });

  it("opens a collapsed group to reach a comment only drawn inside it", async () => {
    // An interactive task's chat leaves grain's resting notes out, so
    // that comment is only in the "Task history" folded up beneath it.
    window.history.replaceState(null, "", "/tasks/12#comment=7");
    renderTask({
      ...baseTask,
      interactive: true,
      comments: [
        ...longConversation(5),
        {
          id: 7,
          author: "console-session",
          authorKind: "automation",
          body: "_Waiting for you._",
          createdAt: "2026-11-02T00:00:00Z",
        },
      ],
    });
    await waitFor(() => expect(scrolled).toHaveLength(1));
    const history = document.querySelector("details.task-chat-history");
    expect(history.open).toBe(true);
    expect(history.contains(scrolled[0])).toBe(true);
  });

  it("says so, and scrolls nowhere, for a comment not in this task", async () => {
    window.history.replaceState(null, "", "/tasks/12#comment=99999");
    renderTask({ ...baseTask, comments: longConversation(10) });
    expect(
      await screen.findByText(
        "Comment 99999 is not in this task's conversation.",
      ),
    ).toBeInTheDocument();
    expect(scrolled).toHaveLength(0);
  });

  it("says a #comment= naming no comment does not name one", async () => {
    window.history.replaceState(null, "", "/tasks/12#comment=abc");
    renderTask({ ...baseTask, comments: longConversation(10) });
    expect(
      await screen.findByText(
        "This link's #comment=abc does not name a comment.",
      ),
    ).toBeInTheDocument();
    expect(scrolled).toHaveLength(0);
  });

  it("leaves another task's link to that task's own pane", async () => {
    window.history.replaceState(null, "", "/tasks/12");
    renderTask({ ...baseTask, comments: longConversation(10) });
    act(() => {
      window.history.pushState(null, "", "/tasks/40#comment=5");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByTestId("comment-link-notice")).toBeNull();
    expect(scrolled).toHaveLength(0);
  });

  it("copies the comment's absolute link", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      writable: true,
      configurable: true,
    });
    renderTask({ ...baseTask, comments: longConversation(1) });
    fireEvent.click(screen.getByRole("button", { name: "Copy link" }));
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(
        `${window.location.origin}/tasks/12#comment=1000`,
      ),
    );
  });

  it("fetches earlier pages and opens summarised disclosure for a deep link before the loaded page", async () => {
    const api = (await import("../api.js")).default;
    const all = longConversation(80);
    // Initial loaded page has only the newest 50 comments (1030..1079),
    // compacted through 1020 with a handover at 1050.
    all[50].handover = true;
    api.mockImplementation(async (path) => {
      if (
        path ===
        "/api/tasks/12?comments_limit=200&comments_before=1030&attempts_limit=1"
      ) {
        return {
          id: "12",
          comments: all.slice(0, 30),
          olderComments: 0,
        };
      }
      return null;
    });

    window.history.replaceState(null, "", "/tasks/12#comment=1005");
    renderTask({
      ...baseTask,
      id: "12",
      compactedThrough: 1020,
      olderComments: 30,
      comments: all.slice(30),
    });

    await waitFor(() => expect(scrolled).toHaveLength(1));
    expect(scrolled[0].getAttribute("data-comment-id")).toBe("1005");
    expect(
      scrolled[0].querySelector("details.timeline-summarised"),
    ).toHaveAttribute("open");
    expect(screen.queryByTestId("comment-link-notice")).toBeNull();
  });
});
