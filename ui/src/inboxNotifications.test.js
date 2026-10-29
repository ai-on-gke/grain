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

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  BURST,
  PREF_KEY,
  announce,
  freshEntries,
  inboxEntries,
  taskReason,
  useInboxNotifications,
  useNotificationSetting,
} from "./inboxNotifications.js";

// FakeNotification stands in for the browser's Notification, the one
// external boundary here: jsdom has none.
function installNotification(permission = "granted") {
  const shown = [];
  class FakeNotification {
    constructor(title, opts) {
      this.title = title;
      this.body = opts?.body;
      this.tag = opts?.tag;
      this.close = vi.fn();
      shown.push(this);
    }
  }
  FakeNotification.permission = permission;
  FakeNotification.requestPermission = vi.fn(async () => {
    FakeNotification.permission = "granted";
    return "granted";
  });
  window.Notification = FakeNotification;
  return { shown, N: FakeNotification };
}

function inBackground() {
  vi.spyOn(document, "hasFocus").mockReturnValue(false);
}

beforeEach(() => {
  inBackground();
});

afterEach(() => {
  delete window.Notification;
  vi.restoreAllMocks();
});

describe("inboxEntries", () => {
  it("keys a task by its id and the reason it is waiting", () => {
    const entries = inboxEntries(
      [
        { id: "task-1", state: "awaiting_reply", title: "Fix it" },
        { id: "task-2", state: "running" },
      ],
      [{ repo: "o/r", branch: "main" }],
    );
    expect(entries.map((e) => e.key)).toEqual([
      "task:task-1:reply",
      "gate:o/r main",
    ]);
    expect(entries[0].title).toBe("task-1 asked a question");
    expect(entries[0].body).toBe("Fix it");
  });

  it("leaves standing sessions out, as the inbox does", () => {
    expect(
      inboxEntries([
        { id: "task-3", state: "awaiting_reply", consoleSession: true },
      ]),
    ).toEqual([]);
  });

  it("treats a new reason on the same task as new", () => {
    const seen = new Set(
      inboxEntries([{ id: "t", state: "proposed" }]).map((e) => e.key),
    );
    const fresh = freshEntries(
      seen,
      inboxEntries([{ id: "t", state: "failed" }]),
    );
    expect(fresh.map((e) => e.key)).toEqual(["task:t:failed"]);
  });
});

describe("announce", () => {
  it("shows one notification per entry, tagged by its key, and opens the task on click", () => {
    const { shown } = installNotification();
    const onOpenTask = vi.fn();
    window.focus = vi.fn();
    announce(inboxEntries([{ id: "t1", state: "awaiting_reply" }]), {
      onOpenTask,
    });
    expect(shown).toHaveLength(1);
    expect(shown[0].tag).toBe("task:t1:reply");
    shown[0].onclick();
    expect(onOpenTask).toHaveBeenCalledWith("t1");
    expect(shown[0].close).toHaveBeenCalled();
  });

  // docs/test-runs/2026-10-12-base-gate-warnings.md: the gate rows left
  // the inbox (7b93861a0) and a gate's notification still opened it, onto
  // a page that does not mention the gate.
  it("opens a gate's repository rather than the inbox, which no longer lists gates", () => {
    const { shown } = installNotification();
    const onOpenInbox = vi.fn();
    const onOpenRepo = vi.fn();
    window.focus = vi.fn();
    announce(inboxEntries([], [{ repo: "acme/widgets", judged: false }]), {
      onOpenInbox,
      onOpenRepo,
    });
    expect(shown).toHaveLength(1);
    expect(shown[0].title).toBe("acme/widgets is not being tested");
    shown[0].onclick();
    expect(onOpenRepo).toHaveBeenCalledWith("acme/widgets");
    expect(onOpenInbox).not.toHaveBeenCalled();
  });

  it("summarizes a burst in one notification that opens the inbox", () => {
    const { shown } = installNotification();
    const onOpenInbox = vi.fn();
    window.focus = vi.fn();
    const tasks = Array.from({ length: BURST + 2 }, (_, i) => ({
      id: `t${i}`,
      state: "proposed",
    }));
    announce(inboxEntries(tasks), { onOpenInbox });
    expect(shown).toHaveLength(1);
    expect(shown[0].title).toBe(`${BURST + 2} new items in your inbox`);
    shown[0].onclick();
    expect(onOpenInbox).toHaveBeenCalled();
  });
});

describe("useInboxNotifications", () => {
  const render = (props) =>
    renderHook((p) => useInboxNotifications(p), { initialProps: props });

  it("takes the first load as the baseline and announces only what arrives after", () => {
    const { shown } = installNotification();
    const base = {
      tasks: [{ id: "old", state: "awaiting_reply" }],
      gates: [],
      tasksReady: true,
      gatesReady: true,
    };
    const { rerender } = render(base);
    expect(shown).toHaveLength(0);
    rerender({
      ...base,
      tasks: [...base.tasks, { id: "new", state: "awaiting_submit" }],
    });
    expect(shown.map((n) => n.title)).toEqual(["new is ready to submit"]);
  });

  it("does not announce anything before the lists have loaded", () => {
    const { shown } = installNotification();
    const { rerender } = render({
      tasks: [],
      gates: [],
      tasksReady: false,
      gatesReady: false,
    });
    rerender({
      tasks: [{ id: "x", state: "failed" }],
      gates: [],
      tasksReady: true,
      gatesReady: false,
    });
    // The gates' first answer is a baseline too.
    rerender({
      tasks: [{ id: "x", state: "failed" }],
      gates: [{ repo: "o/r", branch: "main" }],
      tasksReady: true,
      gatesReady: true,
    });
    expect(shown).toHaveLength(0);
  });

  it("stays quiet while the page is in front of the reader", () => {
    const { shown } = installNotification();
    document.hasFocus.mockReturnValue(true);
    const base = { tasks: [], gates: [], tasksReady: true, gatesReady: true };
    const { rerender } = render(base);
    rerender({ ...base, tasks: [{ id: "n", state: "failed" }] });
    expect(shown).toHaveLength(0);
  });

  it("stays quiet when the reader has turned them off", () => {
    const { shown } = installNotification();
    localStorage.setItem(PREF_KEY, "off");
    const base = { tasks: [], gates: [], tasksReady: true, gatesReady: true };
    const { rerender } = render(base);
    rerender({ ...base, tasks: [{ id: "n", state: "failed" }] });
    expect(shown).toHaveLength(0);
  });

  it("stays quiet without permission", () => {
    const { shown } = installNotification("default");
    const base = { tasks: [], gates: [], tasksReady: true, gatesReady: true };
    const { rerender } = render(base);
    rerender({ ...base, tasks: [{ id: "n", state: "failed" }] });
    expect(shown).toHaveLength(0);
  });
});

describe("useNotificationSetting", () => {
  it("asks the browser for permission when turned on, and remembers off", async () => {
    const { N } = installNotification("default");
    const { result } = renderHook(() => useNotificationSetting());
    expect(result.current.status).toBe("off");
    await act(() => result.current.toggle());
    expect(N.requestPermission).toHaveBeenCalled();
    expect(result.current.status).toBe("on");
    await act(() => result.current.toggle());
    expect(result.current.status).toBe("off");
    expect(localStorage.getItem(PREF_KEY)).toBe("off");
  });

  it("reports blocked and unsupported browsers", () => {
    installNotification("denied");
    expect(
      renderHook(() => useNotificationSetting()).result.current.status,
    ).toBe("denied");
    delete window.Notification;
    expect(
      renderHook(() => useNotificationSetting()).result.current.status,
    ).toBe("unsupported");
  });
});

describe("a question's notification", () => {
  it("is titled by the question's one-line summary", () => {
    const [entry] = inboxEntries(
      [
        {
          id: "42",
          title: "Ship the exporter",
          state: "awaiting_reply",
          questionSummary: "Keep the v1 API or remove it?",
        },
      ],
      [],
    );
    expect(entry.title).toBe("Keep the v1 API or remove it?");
    expect(entry.body).toBe("42 asked a question: Ship the exporter");
    expect(entry.key).toBe("task:42:reply");
  });

  it("falls back to saying that a task asked when there is no summary", () => {
    const [entry] = inboxEntries(
      [{ id: "42", title: "Ship the exporter", state: "awaiting_reply" }],
      [],
    );
    expect(entry.title).toBe("42 asked a question");
  });
});

describe("taskReason for a CL", () => {
  it("says a CL nobody has mailed needs mailing", () => {
    expect(
      taskReason({
        id: "1",
        state: "awaiting_submit",
        clReview: { needsMailing: true },
      }),
    ).toEqual({ id: "needs_mailing", text: "needs mailing" });
    expect(
      taskReason({
        id: "1",
        state: "awaiting_submit",
        clReview: { state: "in_review" },
      }).id,
    ).toBe("submit");
  });
});
