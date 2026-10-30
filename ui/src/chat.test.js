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
import { awaitsReply, chatEntries, typingNote, working } from "./chat.js";

const task = (over = {}) => ({
  id: "42",
  state: "running",
  author: "brandon",
  authorKind: "human",
  createdAt: "2026-09-08T10:00:00Z",
  description: "walk me through pkg/dispatch",
  ...over,
});

describe("chatEntries", () => {
  it("opens with the task's own body, as the message that started it", () => {
    const entries = chatEntries(task());
    expect(entries).toHaveLength(1);
    expect(entries[0]).toMatchObject({
      kind: "message",
      direction: "outgoing",
      sender: "brandon",
      body: "walk me through pkg/dispatch",
    });
  });

  it("leaves out an opening message for a task filed with no body", () => {
    expect(chatEntries(task({ description: "" }))).toHaveLength(0);
  });

  it("suppresses synthetic resting comments (_Waiting for you._) as message bubbles", () => {
    const entries = chatEntries(
      task({
        comments: [
          {
            id: 1,
            author: "console-session",
            authorKind: "automation",
            body: "_Waiting for you._",
            createdAt: "2026-09-08T10:01:00Z",
          },
          {
            id: 2,
            author: "brandon",
            authorKind: "human",
            body: "hello",
            createdAt: "2026-09-08T10:02:00Z",
          },
          {
            id: 3,
            author: "console-session",
            authorKind: "automation",
            body: "_Waiting for you._\n",
            createdAt: "2026-09-08T10:03:00Z",
          },
        ],
      }),
    );
    expect(entries.map((e) => e.body)).toEqual([
      "walk me through pkg/dispatch",
      "hello",
    ]);
  });

  it("keeps the resting words when somebody other than grain's session said them", () => {
    const entries = chatEntries(
      task({
        comments: [
          {
            id: 1,
            author: "brandon",
            authorKind: "human",
            body: "_Waiting for you._",
            createdAt: "2026-09-08T10:01:00Z",
          },
          {
            id: 2,
            author: "grain",
            authorKind: "automation",
            onBehalfOf: "agent",
            body: "_Waiting for you._",
            createdAt: "2026-09-08T10:02:00Z",
          },
        ],
      }),
    );
    expect(entries.map((e) => e.body)).toEqual([
      "walk me through pkg/dispatch",
      "_Waiting for you._",
      "_Waiting for you._",
    ]);
  });

  it("suppresses synthetic wake comments and console-session notification comments", () => {
    const entries = chatEntries(
      task({
        comments: [
          {
            id: 1,
            author: "schedule",
            authorKind: "automation",
            body: "_Woken early:_ a note from task 20. Read the queue again and act on what has changed since your handover.",
            createdAt: "2026-09-08T10:01:00Z",
          },
          {
            id: 2,
            author: "schedule",
            authorKind: "automation",
            body: "_Scheduled wake._ Read the queue again and act on what has changed since your handover.\n",
            createdAt: "2026-09-08T10:02:00Z",
          },
          {
            id: 3,
            author: "console-session",
            authorKind: "automation",
            body: "**Notification (warning):** acme/widgets main is red\n\n`pipeline.base-red:acme/widgets@main` -- acknowledge or investigate it from the [Inbox](/inbox).",
            createdAt: "2026-09-08T10:03:00Z",
          },
          {
            id: 4,
            author: "brandon",
            authorKind: "human",
            body: "**Notification (warning):** quoting a notification in chat",
            createdAt: "2026-09-08T10:04:00Z",
          },
          {
            id: 5,
            author: "grain",
            authorKind: "automation",
            onBehalfOf: "run-7",
            body: "_Woken early:_ quoting wake text from an agent",
            createdAt: "2026-09-08T10:05:00Z",
          },
        ],
      }),
    );
    expect(entries.map((e) => e.body)).toEqual([
      "walk me through pkg/dispatch",
      "**Notification (warning):** quoting a notification in chat",
      "_Woken early:_ quoting wake text from an agent",
    ]);
  });

  it("puts a human's comments on one side and everyone else's on the other", () => {
    const entries = chatEntries(
      task({
        description: "",
        comments: [
          {
            id: 1,
            author: "grain",
            authorKind: "automation",
            onBehalfOf: "run-7",
            body: "dispatch drains task_ready",
            createdAt: "2026-09-08T10:01:00Z",
          },
          {
            id: 2,
            author: "brandon",
            authorKind: "human",
            body: "and when it is paused?",
            createdAt: "2026-09-08T10:02:00Z",
          },
        ],
      }),
    );
    expect(entries.map((e) => e.direction)).toEqual(["incoming", "outgoing"]);
    // grain relaying an agent's words says so, rather than signing them
    // as its own.
    expect(entries[0].sender).toBe("grain on behalf of run-7");
    expect(entries[1].sender).toBe("brandon");
  });

  it("tags sender with author for multi-user human comments", () => {
    const entries = chatEntries(
      task({
        creator: "alice",
        description: "initial prompt by alice",
        comments: [
          {
            id: 1,
            author: "alice",
            authorKind: "human",
            body: "clarification by alice",
            createdAt: "2026-09-08T10:01:00Z",
          },
          {
            id: 2,
            author: "bob",
            authorKind: "human",
            body: "comment by bob",
            createdAt: "2026-09-08T10:02:00Z",
          },
        ],
      }),
    );
    expect(
      entries.map((e) => ({ direction: e.direction, sender: e.sender })),
    ).toEqual([
      { direction: "outgoing", sender: "alice" },
      { direction: "outgoing", sender: "alice" },
      { direction: "outgoing", sender: "bob" },
    ]);
  });

  it("orders the whole conversation oldest first", () => {
    const entries = chatEntries(
      task({
        comments: [
          {
            id: 2,
            author: "brandon",
            authorKind: "human",
            body: "second",
            createdAt: "2026-09-08T10:05:00Z",
          },
          {
            id: 1,
            author: "grain",
            authorKind: "automation",
            body: "first",
            createdAt: "2026-09-08T10:01:00Z",
          },
        ],
      }),
    );
    expect(entries.map((e) => e.body)).toEqual([
      "walk me through pkg/dispatch",
      "first",
      "second",
    ]);
  });

  it("says nothing about a run that succeeded or is still going", () => {
    const entries = chatEntries(
      task({
        description: "",
        attempts: [
          {
            number: 1,
            finishedAt: "2026-09-08T10:03:00Z",
            outcome: "succeeded",
          },
          { number: 2, startedAt: "2026-09-08T10:04:00Z" },
        ],
      }),
    );
    expect(entries).toHaveLength(0);
  });

  it("says why the chat went quiet when a run ended badly", () => {
    const entries = chatEntries(
      task({
        description: "",
        attempts: [
          {
            number: 1,
            finishedAt: "2026-09-08T10:03:00Z",
            outcome: "setup-failed",
            detail: "sandbox never booted",
          },
        ],
      }),
    );
    expect(entries).toEqual([
      expect.objectContaining({
        kind: "note",
        text: "Setup failed",
        detail: "sandbox never booted",
      }),
    ]);
  });
});

describe("working and awaitsReply", () => {
  it("counts a queued turn as work in hand, and a finished one as not", () => {
    expect(working(task({ state: "running" }))).toBe(true);
    expect(working(task({ state: "queued" }))).toBe(true);
    expect(working(task({ state: "completed" }))).toBe(false);
    expect(working(task({ state: "awaiting_reply" }))).toBe(false);
  });

  it("knows when the agent is waiting on the reader", () => {
    expect(awaitsReply(task({ state: "awaiting_reply" }))).toBe(true);
    expect(awaitsReply(task({ state: "running" }))).toBe(false);
  });
});

describe("typingNote", () => {
  it("says whatever the run last said it was doing", () => {
    expect(typingNote(task({ activity: "reading pkg/dispatch" }))).toBe(
      "reading pkg/dispatch",
    );
  });

  it("marks grain's own setup phrase as grain's, not the agent's", () => {
    expect(
      typingNote(task({ activity: "cloning the repo", activityBySetup: true })),
    ).toBe("grain: cloning the repo");
  });

  it("falls back to saying only that something is happening", () => {
    expect(typingNote(task({ state: "running" }))).toBe("working");
    expect(typingNote(task({ state: "queued" }))).toBe("queued");
    expect(
      typingNote(
        task({
          state: "queued",
          activity: "waiting for Gemini API keys (gemini-key grant)",
          activityBySetup: true,
        }),
      ),
    ).toBe("queued");
  });
});
