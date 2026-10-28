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

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import TaskChat from "./TaskChat.jsx";
import fileToAttachment from "../attachments.js";

vi.mock("../attachments.js", () => ({ default: vi.fn() }));

const task = (over = {}) => ({
  id: "42",
  state: "running",
  interactive: true,
  author: "brandon",
  authorKind: "human",
  createdAt: "2026-09-08T10:00:00Z",
  description: "walk me through pkg/dispatch",
  comments: [],
  ...over,
});

// act is App's own wrapper: it runs the call and refreshes the task. Here
// it only has to run it, so the component's own behaviour is what is
// under test rather than the poll behind it.
const act = (fn) => fn();

describe("TaskChat", () => {
  it("renders the conversation, with the task's body as its first message", () => {
    render(
      <TaskChat
        t={task({
          comments: [
            {
              id: 1,
              author: "grain",
              authorKind: "automation",
              onBehalfOf: "run-7",
              body: "It drains task_ready until max_workers.",
              createdAt: "2026-09-08T10:01:00Z",
            },
          ],
        })}
        act={act}
      />,
    );
    expect(
      screen.getByText("walk me through pkg/dispatch"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("It drains task_ready until max_workers."),
    ).toBeInTheDocument();
    expect(screen.getByText("grain on behalf of run-7")).toBeInTheDocument();
  });

  it("renders a message body as markdown, the way every other relayed answer is", () => {
    render(
      <TaskChat
        t={task({
          description: "",
          comments: [
            {
              id: 1,
              author: "grain",
              authorKind: "automation",
              body: "see `pkg/dispatch`",
              createdAt: "2026-09-08T10:01:00Z",
            },
          ],
        })}
        act={act}
      />,
    );
    expect(screen.getByText("pkg/dispatch").tagName).toBe("CODE");
  });

  it("says what the agent is doing while it works", () => {
    render(
      <TaskChat t={task({ activity: "reading pkg/dispatch" })} act={act} />,
    );
    expect(screen.getByText("reading pkg/dispatch")).toBeInTheDocument();
  });

  it("shows no typing indicator once the run has finished", () => {
    render(
      <TaskChat
        t={task({ state: "completed", activity: "reading pkg/dispatch" })}
        act={act}
      />,
    );
    expect(screen.queryByText("reading pkg/dispatch")).not.toBeInTheDocument();
  });

  it("says when the agent is waiting on a reply", () => {
    render(<TaskChat t={task({ state: "awaiting_reply" })} act={act} />);
    expect(screen.getByText("waiting for your reply")).toBeInTheDocument();
  });

  it("says why the chat went quiet when a run ended badly", () => {
    render(
      <TaskChat
        t={task({
          state: "failed",
          attempts: [
            {
              number: 1,
              finishedAt: "2026-09-08T10:03:00Z",
              outcome: "failed",
              detail: "the agent stopped without answering",
            },
          ],
        })}
        act={act}
      />,
    );
    expect(
      screen.getByText("Failed · the agent stopped without answering"),
    ).toBeInTheDocument();
  });

  it("puts a shortcut's requested prompt in the composer", async () => {
    const request = { text: "Work with me to build a new board." };
    const { rerender } = render(<TaskChat t={task()} act={act} />);

    rerender(<TaskChat t={task()} act={act} draftRequest={request} />);

    const box = document.querySelector(".cs-message-input__content-editor");
    await waitFor(() =>
      expect(box).toHaveTextContent("Work with me to build a new board."),
    );
  });

  it("posts what is typed as a comment on the task, and clears the box", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      headers: new Headers({ "Content-Type": "application/json" }),
      json: async () => ({}),
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    render(<TaskChat t={task()} act={act} />);

    // The library's composer is a contenteditable div rather than a
    // <textarea>, and carries no role of its own; TaskChat gives it one
    // and a name, so it is reached the way assistive technology reaches
    // it (2026-10-02 intake walkthrough found it unnamed).
    const box = screen.getByRole("textbox", { name: "Message" });
    expect(box).toHaveClass("cs-message-input__content-editor");
    expect(box).toHaveAttribute("aria-multiline", "true");
    await user.click(box);
    await user.paste("and when it is paused?");
    await user.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const [path, opts] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/tasks/42/comments");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body).body).toBe("and when it is paused?");
    await waitFor(() => expect(box.textContent).toBe(""));
    vi.unstubAllGlobals();
  });

  it("puts a message that failed to post back in the box rather than losing it", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockRejectedValue(new TypeError("Failed to fetch")),
    );
    // App's act: reports the failure and swallows it.
    const reported = [];
    const swallowing = async (fn) => {
      try {
        await fn();
      } catch (err) {
        reported.push(err);
      }
    };
    const user = userEvent.setup();
    render(<TaskChat t={task()} act={swallowing} />);

    const box = document.querySelector(".cs-message-input__content-editor");
    await user.click(box);
    await user.paste("sent while offline");
    await user.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(reported).toHaveLength(1));
    await waitFor(() => expect(box.textContent).toBe("sent while offline"));
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
    vi.unstubAllGlobals();
  });

  it("attaches an image pasted into the composer and includes it in comment payload", async () => {
    const upload = {
      filename: "director-prompt.png",
      contentType: "image/png",
      content: "cHJvbXB0",
    };
    fileToAttachment.mockResolvedValueOnce(upload);
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      headers: new Headers({ "Content-Type": "application/json" }),
      json: async () => ({}),
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    render(<TaskChat t={task()} act={act} />);

    const box = document.querySelector(".cs-message-input__content-editor");
    const file = new File(["fake"], "director-prompt.png", {
      type: "image/png",
    });
    fireEvent.paste(box, {
      clipboardData: {
        items: [
          {
            kind: "file",
            type: "image/png",
            getAsFile: () => file,
          },
        ],
      },
    });

    expect(await screen.findByText("director-prompt.png")).toBeInTheDocument();
    // Send button should be enabled even without text
    const sendButton = screen.getByRole("button", { name: "Send" });
    expect(sendButton).not.toBeDisabled();

    await user.click(sendButton);

    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const [path, opts] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/tasks/42/comments");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body)).toEqual({
      body: "",
      attachments: [upload],
    });
    vi.unstubAllGlobals();
  });

  it("sends nothing at all for an empty message", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    render(<TaskChat t={task()} act={act} />);
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    expect(fetchMock).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  it("renders Stop button when task is running and calls stop API on click", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      headers: new Headers({ "Content-Type": "application/json" }),
      json: async () => ({}),
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    render(<TaskChat t={{ ...task(), state: "running" }} act={act} />);

    const stopButton = screen.getByRole("button", { name: "Stop" });
    expect(stopButton).toBeInTheDocument();
    await user.click(stopButton);

    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const [path, opts] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/tasks/42/stop");
    expect(opts.method).toBe("POST");
    vi.unstubAllGlobals();
  });

  // The server records the comment's author (docs/design/settings-panel.md
  // §6.1): a browser no longer claims one, whatever it once stored.
  it("sends no author in the comment payload", async () => {
    localStorage.setItem("grain.username", "alice");
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      headers: new Headers({ "Content-Type": "application/json" }),
      json: async () => ({}),
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    render(<TaskChat t={task()} act={act} />);

    const box = document.querySelector(".cs-message-input__content-editor");
    await user.click(box);
    await user.paste("hello from alice");
    await user.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const [path, opts] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/tasks/42/comments");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body)).not.toHaveProperty("author");
    expect(JSON.parse(opts.body).body).toBe("hello from alice");
    expect(opts.headers).not.toHaveProperty("X-Grain-User");
    localStorage.removeItem("grain.username");
    vi.unstubAllGlobals();
  });

  it("does not render Stop button when task is not running", () => {
    render(<TaskChat t={{ ...task(), state: "awaiting_reply" }} act={act} />);
    expect(
      screen.queryByRole("button", { name: "Stop" }),
    ).not.toBeInTheDocument();
  });

  it("renders extra toolbar actions and children alongside composer controls", () => {
    render(
      <TaskChat
        t={task()}
        act={act}
        actions={<button type="button">Extra action</button>}
      >
        <div data-testid="extra-child">Extra content</div>
      </TaskChat>,
    );
    const compose = document.querySelector(".task-chat-compose");
    expect(compose).toContainElement(
      screen.getByRole("button", { name: "Extra action" }),
    );
    expect(compose).toContainElement(
      screen.getByRole("button", { name: "Send" }),
    );
    expect(compose).toContainElement(
      screen.getByRole("button", { name: "Attach files" }),
    );
    expect(screen.getByTestId("extra-child")).toBeInTheDocument();
  });

  it("attaches multiple files (including non-images) pasted into the chat and includes them in comment payload", async () => {
    const upload1 = {
      filename: "app.log",
      contentType: "text/plain",
      content: "bG9n",
    };
    const upload2 = {
      filename: "archive.zip",
      contentType: "application/zip",
      content: "emlw",
    };
    fileToAttachment
      .mockResolvedValueOnce(upload1)
      .mockResolvedValueOnce(upload2);
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      headers: new Headers({ "Content-Type": "application/json" }),
      json: async () => ({}),
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    render(<TaskChat t={task()} act={act} />);

    const chatContainer = document.querySelector(".task-chat");
    const file1 = new File(["log"], "app.log", { type: "text/plain" });
    const file2 = new File(["zip"], "archive.zip", { type: "application/zip" });

    fireEvent.paste(chatContainer, {
      clipboardData: {
        files: [file1, file2],
      },
    });

    expect(await screen.findByText("app.log")).toBeInTheDocument();
    expect(await screen.findByText("archive.zip")).toBeInTheDocument();

    const sendButton = screen.getByRole("button", { name: "Send" });
    expect(sendButton).not.toBeDisabled();

    await user.click(sendButton);

    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const [path, opts] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/tasks/42/comments");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body)).toEqual({
      body: "",
      attachments: [upload1, upload2],
    });
    vi.unstubAllGlobals();
  });

  it("attaches files dropped into the chat via drag-and-drop and includes them in comment payload", async () => {
    const upload = {
      filename: "code.go",
      contentType: "text/plain",
      content: "Y29kZQ==",
    };
    fileToAttachment.mockResolvedValueOnce(upload);
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      headers: new Headers({ "Content-Type": "application/json" }),
      json: async () => ({}),
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    render(<TaskChat t={task()} act={act} />);

    const chatContainer = document.querySelector(".task-chat");
    const file = new File(["package main"], "code.go", { type: "text/plain" });

    const dragOverEvt = new Event("dragover", {
      bubbles: true,
      cancelable: true,
    });
    chatContainer.dispatchEvent(dragOverEvt);
    expect(dragOverEvt.defaultPrevented).toBe(true);

    fireEvent.drop(chatContainer, {
      dataTransfer: {
        files: [file],
      },
    });

    expect(await screen.findByText("code.go")).toBeInTheDocument();

    const sendButton = screen.getByRole("button", { name: "Send" });
    expect(sendButton).not.toBeDisabled();

    await user.click(sendButton);

    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const [path, opts] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/tasks/42/comments");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body)).toEqual({
      body: "",
      attachments: [upload],
    });
    vi.unstubAllGlobals();
  });

  it("attaches multiple picked files from the file input", async () => {
    render(<TaskChat t={task()} act={act} />);
    const input = document.querySelector('input[type="file"]');
    const file1 = new File(["a"], "one.txt", { type: "text/plain" });
    const file2 = new File(["b"], "two.pdf", { type: "application/pdf" });

    fireEvent.change(input, {
      target: { files: [file1, file2] },
    });

    expect(await screen.findByText("one.txt")).toBeInTheDocument();
    expect(await screen.findByText("two.pdf")).toBeInTheDocument();
  });

  it("renders a copy button for agent responses and copies text on click", async () => {
    const writeTextMock = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: {
        writeText: writeTextMock,
      },
      writable: true,
      configurable: true,
    });

    render(
      <TaskChat
        t={task({
          comments: [
            {
              id: 1,
              author: "grain",
              authorKind: "automation",
              onBehalfOf: "run-7",
              body: "Here is the response.",
              createdAt: "2026-09-08T10:01:00Z",
            },
          ],
        })}
        act={act}
      />,
    );

    const copyBtn = screen.getByRole("button", { name: "Copy response" });
    expect(copyBtn).toBeInTheDocument();

    fireEvent.click(copyBtn);
    expect(writeTextMock).toHaveBeenCalledWith("Here is the response.");
  });

  it("does not render copy button for outgoing human messages", () => {
    render(
      <TaskChat
        t={task({
          description: "my question",
          comments: [],
        })}
        act={act}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "Copy response" }),
    ).not.toBeInTheDocument();
  });
});
