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

// What typing in a chat costs, as a property rather than as a
// stopwatch (grain/task-174).
//
// The bug these pin: MessageInput is controlled by state on TaskChat,
// and TaskChat's render body *was* the transcript -- chatEntries(t) with
// its sort, then a <Markdown> per message -- so every character typed
// re-ran the markdown pipeline over the whole conversation. On a
// 41-message chat that was 112ms of script between one character and the
// next, and it grew with the conversation: 200ms at 81 messages
// (the 2026-09-17 chat-keystroke hand run).
//
// A stopwatch would be the wrong test for it -- what broke was not a
// constant, it was that the constant depended on N -- so what is
// asserted here is the count of message bodies rendered per keystroke,
// and that it is the same count on a long conversation as on a short
// one. Markdown.jsx is mocked to report every render of a body; it is
// the expensive component, so counting its renders counts the work that
// mattered. (Markdown.memo.test.jsx pins the other half: that a re-render
// which does reach it still does not re-parse an unchanged body.)
import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import TaskChat from "./TaskChat.jsx";
import { bodies } from "./Markdown.jsx";

vi.mock("./Markdown.jsx", () => {
  const bodies = { count: 0 };
  return {
    bodies,
    default: ({ children, className }) => {
      bodies.count += 1;
      return <div className={className}>{children}</div>;
    },
  };
});

const task = (n) => ({
  id: "42",
  state: "running",
  interactive: true,
  author: "brandon",
  authorKind: "human",
  createdAt: "2026-09-17T09:00:00Z",
  activity: "reading pkg/dispatch",
  description: "walk me through pkg/dispatch",
  comments: Array.from({ length: n }, (_, i) => ({
    id: i + 1,
    author: i % 2 === 0 ? "grain" : "brandon",
    authorKind: i % 2 === 0 ? "automation" : "human",
    body: `## Turn ${i}\n\nit drains \`task_ready\` until \`max_workers\`.`,
    createdAt: new Date(Date.UTC(2026, 8, 17, 9, 1 + i)).toISOString(),
    attachments: [],
  })),
  attempts: [],
  attachments: [],
});

const act = (fn) => fn();
const composer = () =>
  document.querySelector(".cs-message-input__content-editor");

// One character through the composer, and what the transcript did about
// it. Returns the number of message bodies rendered while it was typed.
async function costOfAKeystroke(user, text = "a") {
  await user.click(composer());
  bodies.count = 0;
  await user.keyboard(text);
  await waitFor(() => expect(composer()).toHaveTextContent(text));
  return bodies.count;
}

describe("TaskChat, what a keystroke re-renders", () => {
  it("re-renders no message body while somebody types", async () => {
    const user = userEvent.setup();
    render(<TaskChat t={task(8)} act={act} />);
    expect(bodies.count).toBe(9);

    expect(await costOfAKeystroke(user, "does")).toBe(0);
  });

  it("costs the same on a long conversation as on a short one", async () => {
    const user = userEvent.setup();
    const short = render(<TaskChat t={task(2)} act={act} />);
    const onThree = await costOfAKeystroke(user, "a");
    short.unmount();

    render(<TaskChat t={task(40)} act={act} />);
    const onFortyOne = await costOfAKeystroke(user, "a");

    // The property that actually broke: not that either number is
    // small, but that the second is not thirteen times the first.
    expect(onFortyOne).toBe(onThree);
  });

  it("still draws a message that arrives while the draft is being typed", async () => {
    const user = userEvent.setup();
    const { rerender } = render(<TaskChat t={task(2)} act={act} />);
    await user.click(composer());
    await user.keyboard("and then?");

    const next = task(3);
    next.comments[2].body = "then orchestrator claims it.";
    rerender(<TaskChat t={next} act={act} />);

    await waitFor(() =>
      expect(
        screen.getByText("then orchestrator claims it."),
      ).toBeInTheDocument(),
    );
    // And the draft the poll landed on top of is still there.
    expect(composer()).toHaveTextContent("and then?");
  });
});
