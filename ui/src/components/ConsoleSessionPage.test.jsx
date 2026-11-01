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
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import ConsoleSessionPage from "./ConsoleSessionPage.jsx";
import api from "../api.js";
import { resetCapabilityMethodsForTest } from "./capabilityMethods.js";

vi.mock("../api.js", () => ({ default: vi.fn(async () => ({})) }));

const session = (over = {}) => ({
  id: "director",
  title: "Director",
  taskId: "7",
  capabilities: ["self-debug"],
  ...over,
});

const task = (over = {}) => ({
  id: "7",
  state: "awaiting_reply",
  interactive: true,
  consoleSession: "director",
  author: "grain",
  authorKind: "automation",
  createdAt: "2026-09-08T10:00:00Z",
  description: "You are this deployment's director.",
  comments: [],
  ...over,
});

const capabilities = [
  { id: "self-debug", name: "Self debug", description: "read grain's source" },
  { id: "self-repair", name: "Self repair", description: "change this host" },
];

describe("ConsoleSessionPage", () => {
  it("uses the shared task state and chat treatment", () => {
    render(
      <ConsoleSessionPage
        session={session()}
        task={task()}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByText("Asked")).toHaveClass(
      "badge",
      "badge-awaiting_reply",
    );
    expect(screen.getByText("waiting for your reply")).toHaveAttribute(
      "role",
      "status",
    );
    expect(document.querySelector(".task-chat")).toBeInTheDocument();
  });

  it("aligns capabilities and restart controls in the compose toolbar without exposing the underlying task", () => {
    render(
      <ConsoleSessionPage
        session={session()}
        task={task({ continues: "3" })}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    const compose = document.querySelector(".task-chat-compose");
    expect(compose).toContainElement(
      screen.getByRole("button", { name: /Edit Director capabilities/ }),
    );
    expect(compose).toContainElement(
      screen.getByRole("button", { name: "Start a fresh chat" }),
    );
    expect(compose).toContainElement(
      screen.getByRole("button", { name: "Send" }),
    );
    expect(compose).toContainElement(
      screen.getByRole("button", { name: "Attach files" }),
    );
    expect(screen.queryByText("task 7")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "Open the task behind this session" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "Open the chat before this one" }),
    ).not.toBeInTheDocument();
  });

  it("cancels an in-flight or queued turn when Stop is clicked", async () => {
    const onSaved = vi.fn();
    const { rerender } = render(
      <ConsoleSessionPage
        session={session()}
        task={task({ state: "running" })}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={onSaved}
        showError={vi.fn()}
      />,
    );

    const stopBtn = screen.getByRole("button", { name: "Stop" });
    await userEvent.click(stopBtn);

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/console-sessions/director/cancel",
        {
          method: "POST",
        },
      ),
    );
    await waitFor(() => expect(onSaved).toHaveBeenCalled());

    rerender(
      <ConsoleSessionPage
        session={session()}
        task={task({ state: "queued" })}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={onSaved}
        showError={vi.fn()}
      />,
    );
    expect(screen.getByRole("button", { name: "Stop" })).toBeInTheDocument();
  });

  it("names the composer for assistive technology", () => {
    render(
      <ConsoleSessionPage
        session={session()}
        task={task()}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    const box = screen.getByRole("textbox", { name: "Message" });
    expect(box).toHaveAttribute("contenteditable", "true");
    expect(box).toHaveAttribute("aria-multiline", "true");
  });

  it("carries the purple live state into the session header", () => {
    render(
      <ConsoleSessionPage
        session={session()}
        task={task({ state: "running" })}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByText("Interactive")).toHaveClass(
      "badge",
      "badge-running",
      "badge-mark",
    );
    expect(
      document.querySelector(".console-session-header .grain-mark-interactive"),
    ).toBeInTheDocument();
  });

  it("renders the shared idle state and disables an empty composer", () => {
    render(
      <ConsoleSessionPage
        session={session()}
        task={task({ state: "proposed", description: "" })}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByText("No messages yet")).toBeVisible();
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
  });

  it("keeps the session's capabilities hidden until they are opened", async () => {
    render(
      <ConsoleSessionPage
        session={session()}
        task={task()}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByRole("heading", { name: "Director" })).toBeVisible();
    expect(
      screen.getByText("You are this deployment's director."),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("checkbox", { name: /Self debug/ }),
    ).not.toBeInTheDocument();

    await userEvent.click(
      screen.getByRole("button", { name: /Edit Director capabilities/ }),
    );

    expect(
      screen.getByRole("button", { name: /Hide Director capabilities/ }),
    ).toBeVisible();
    expect(screen.getByRole("checkbox", { name: /Self debug/ })).toBeChecked();
    expect(
      screen.getByRole("checkbox", { name: /Self repair/ }),
    ).not.toBeChecked();
  });

  // A box that could not be reached by ticking it is not an offer: a
  // capability this deployment has not configured fails the session's
  // next exchange rather than granting anything, so the pane leaves it
  // out -- unless the session already holds it, which is the one case
  // where the box is what takes it off again.
  it("offers only the capabilities this deployment has configured", async () => {
    render(
      <ConsoleSessionPage
        session={session({ capabilities: ["self-debug", "gcp-key"] })}
        task={task()}
        capabilities={[
          ...capabilities,
          { id: "gemini-key", name: "Gemini key", ready: false },
          { id: "gcp-key", name: "GCP key", ready: false },
        ]}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: /Director capabilities/ }),
    );
    expect(
      screen.queryByRole("checkbox", { name: /Gemini key/ }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: /GCP key/ })).toBeChecked();
  });

  // The whole reason the grants are in this pane rather than in Settings:
  // the answer to "I would need self-repair for that" is a tick box under
  // the message that said it.
  it("grants a capability from the pane the chat is in", async () => {
    const onSaved = vi.fn();
    render(
      <ConsoleSessionPage
        session={session()}
        task={task()}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={onSaved}
        showError={vi.fn()}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: /Director capabilities/ }),
    );
    await userEvent.click(
      screen.getByRole("checkbox", { name: /Self repair/ }),
    );

    await waitFor(() => expect(api).toHaveBeenCalled());
    const [path, options] = api.mock.calls.at(-1);
    expect(path).toBe("/api/console-sessions/director");
    expect(options.method).toBe("PUT");
    expect(JSON.parse(options.body).capabilities).toEqual([
      "self-debug",
      "self-repair",
    ]);
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
  });

  // A standing session holds capability server methods, with their
  // arguments, as well as api:* grants: GrantsPicker edits both, saving
  // each change with the legacy boxes' ids kept.
  it("grants capability server methods and api scopes with GrantsPicker", async () => {
    resetCapabilityMethodsForTest();
    const cloudKey = {
      ref: "https://example.com/caps/cloud#cloud-key",
      method: "cloud-key",
      args: {
        type: "object",
        required: ["account"],
        properties: { account: { type: "string", minLength: 1 } },
      },
    };
    const grantOf = (account) =>
      `${cloudKey.ref}=${JSON.stringify({ account })}`;
    const held = grantOf("staging");
    api.mockImplementation(async (path, opts) => {
      if (path === "/api/capability-methods") return [cloudKey];
      if (path === "/api/capability-grants/check")
        return JSON.parse(opts.body).grants.map(
          ({ grant, capability, with: w }) => ({
            grant,
            recorded: grant || `${capability}=${JSON.stringify(w)}`,
          }),
        );
      return {};
    });
    const user = userEvent.setup();
    render(
      <ConsoleSessionPage
        session={session({
          capabilities: ["self-debug", "api:tasks:read", held],
        })}
        task={task()}
        capabilities={[
          ...capabilities,
          { id: "api:tasks:read", name: "Read tasks" },
        ]}
        config={{ capabilityMethods: 1 }}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );
    await user.click(
      screen.getByRole("button", { name: /Director capabilities/ }),
    );
    const grants = screen.getByTestId("session-director-grants");
    const section = await waitFor(() => {
      const el = grants.querySelector(`[data-grant='${held}']`);
      expect(el?.querySelector("input")).toBeTruthy();
      return el;
    });
    const puts = () =>
      api.mock.calls
        .filter(([p]) => p === "/api/console-sessions/director")
        .map(([, o]) => JSON.parse(o.body).capabilities);

    const account = within(section).getByLabelText(/^account/);
    await user.clear(account);
    await user.type(account, "prod");
    await waitFor(() =>
      expect(puts()).toContainEqual([
        "self-debug",
        "api:tasks:read",
        grantOf("prod"),
      ]),
    );

    await user.click(
      within(grants).getByRole("button", { name: /^Remove api:tasks:read/ }),
    );
    await waitFor(() => expect(puts()).toContainEqual(["self-debug", held]));
  });

  it("turns the approval of what it files on from the same pane", async () => {
    render(
      <ConsoleSessionPage
        session={session()}
        task={task()}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: /Director capabilities/ }),
    );
    await userEvent.click(
      screen.getByRole("checkbox", { name: /File its tasks approved/ }),
    );

    await waitFor(() => expect(api).toHaveBeenCalled());
    const [, options] = api.mock.calls.at(-1);
    expect(JSON.parse(options.body)).toEqual({ tasksApproved: true });
  });

  it("toggles keepRunning on a console session from the capabilities pane", async () => {
    render(
      <ConsoleSessionPage
        session={session()}
        task={task()}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: /Director capabilities/ }),
    );
    await userEvent.click(
      screen.getByRole("checkbox", { name: /Keep running/ }),
    );

    await waitFor(() => expect(api).toHaveBeenCalled());
    const [, options] = api.mock.calls.at(-1);
    expect(JSON.parse(options.body)).toEqual({ keepRunning: true });
  });

  it("offers a member of the session's team every control", () => {
    render(
      <ConsoleSessionPage
        session={session({ team: "infra" })}
        task={task({ team: "infra" })}
        config={{ readOnlyTeams: ["frontend"] }}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByRole("button", { name: "Send" })).toBeVisible();
    expect(
      screen.getByRole("button", { name: /Director capabilities/ }),
    ).toBeVisible();
    expect(
      screen.getByRole("button", { name: "Delete session" }),
    ).toBeVisible();
  });

  // The setting the API has always taken and no control ever sent
  // (grain/task-399).
  it("changes what the session may reach on the internet", async () => {
    render(
      <ConsoleSessionPage
        session={session({ networkAccess: "none" })}
        task={task()}
        capabilities={capabilities}
        networkAccessOptions={["open", "none"]}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: /Director capabilities/ }),
    );
    const select = screen.getByLabelText(/What it may reach on the internet/);
    expect(select).toHaveValue("none");
    await userEvent.selectOptions(select, "open");

    await waitFor(() => expect(api).toHaveBeenCalled());
    const [, options] = api.mock.calls.at(-1);
    expect(JSON.parse(options.body)).toEqual({ networkAccess: "open" });
  });

  // Configured, but the cycle that files its task has not run yet: a
  // wait, said out loud, rather than an empty chat window that swallows
  // whatever is typed into it.
  it("says the chat is coming when the session has no task yet", () => {
    render(
      <ConsoleSessionPage
        session={session({ taskId: "" })}
        task={null}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByText(/files this session's task/)).toBeVisible();
    expect(
      screen.queryByText("You are this deployment's director."),
    ).not.toBeInTheDocument();
  });

  it("always offers a fresh chat while the session is open", () => {
    const { rerender } = render(
      <ConsoleSessionPage
        session={session()}
        task={task()}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );
    const offer = screen.getByRole("button", { name: "Start a fresh chat" });
    expect(document.querySelector(".task-chat-compose")).toContainElement(
      offer,
    );

    rerender(
      <ConsoleSessionPage
        session={session({
          crowded: true,
          crowdedNote: "The last exchange started with 240 KB already spent.",
        })}
        task={task()}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );
    expect(
      screen.getByRole("button", { name: "Start a fresh chat" }),
    ).toBeInTheDocument();
  });

  it("restarts the session fresh or carries over the standing summary", async () => {
    const onSaved = vi.fn();
    render(
      <ConsoleSessionPage
        session={session({
          crowded: true,
          crowdedNote: "grain cut the middle out of it.",
        })}
        task={task()}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={onSaved}
        showError={vi.fn()}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: "Start a fresh chat" }),
    );
    expect(screen.getByText("grain cut the middle out of it.")).toBeVisible();
    expect(
      screen.getByText(
        /closes the current chat and immediately opens a fresh one/,
      ),
    ).toBeVisible();
    expect(screen.queryByText(/closed task 7/)).not.toBeInTheDocument();

    await userEvent.click(
      screen.getByRole("button", {
        name: "Close this chat and start a fresh one",
      }),
    );

    await waitFor(() => expect(api).toHaveBeenCalled());
    let [path, options] = api.mock.calls.at(-1);
    expect(path).toBe("/api/console-sessions/director/restart");
    expect(options.method).toBe("POST");
    expect(JSON.parse(options.body)).toEqual({ carryHandover: false });
    await waitFor(() => expect(onSaved).toHaveBeenCalled());

    await userEvent.click(
      screen.getByRole("button", { name: "Start a fresh chat" }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Start fresh with summary" }),
    );

    await waitFor(() =>
      expect(api.mock.calls.at(-1)[1].body).toContain("true"),
    );
    [path, options] = api.mock.calls.at(-1);
    expect(path).toBe("/api/console-sessions/director/restart");
    expect(options.method).toBe("POST");
    expect(JSON.parse(options.body)).toEqual({ carryHandover: true });
  });

  it("deletes a session when delete button is clicked and confirmed", async () => {
    const onDeleted = vi.fn();
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(
      <ConsoleSessionPage
        session={session({ id: "reviewer", title: "Reviewer" })}
        task={task({ id: "8", consoleSession: "reviewer" })}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        onDeleted={onDeleted}
        showError={vi.fn()}
      />,
    );

    const deleteBtn = screen.getByRole("button", { name: "Delete session" });
    expect(deleteBtn).toBeInTheDocument();

    await userEvent.click(deleteBtn);

    expect(window.confirm).toHaveBeenCalled();
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/console-sessions/reviewer", {
        method: "DELETE",
      }),
    );
    expect(onDeleted).toHaveBeenCalledWith("reviewer");

    window.confirm.mockRestore();
  });

  it("stops offering a fresh chat once this one is closed", () => {
    render(
      <ConsoleSessionPage
        session={session({ crowded: true })}
        task={task({ state: "closed" })}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(
      screen.queryByRole("button", { name: "Start a fresh chat" }),
    ).not.toBeInTheDocument();
  });

  it("edits repositories it may read and write from the same pane", async () => {
    const onSaved = vi.fn();
    render(
      <ConsoleSessionPage
        session={session({
          reads: ["acme/docs"],
          writes: [{ repo: "acme/deploy", base: "prod" }],
        })}
        task={task()}
        config={{ targetRepos: ["acme/widgets", "acme/docs", "acme/deploy"] }}
        tasks={[]}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={onSaved}
        showError={vi.fn()}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: /Director capabilities/ }),
    );

    expect(screen.getByText("acme/docs")).toBeInTheDocument();
    expect(screen.getByText("acme/deploy")).toBeInTheDocument();
  });

  it("edits google chat space from the same pane", async () => {
    const onSaved = vi.fn();
    render(
      <ConsoleSessionPage
        session={session({
          googleChatSpace: "spaces/AAAAAAAAAAA",
        })}
        task={task()}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={onSaved}
        showError={vi.fn()}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: /Director capabilities/ }),
    );

    const input = screen.getByPlaceholderText("spaces/...");
    expect(input).toHaveValue("spaces/AAAAAAAAAAA");

    await userEvent.clear(input);
    await userEvent.type(input, "spaces/BBBBBBBBBBB");
    await userEvent.tab();

    await waitFor(() => expect(api).toHaveBeenCalled());
    const [path, options] = api.mock.calls.at(-1);
    expect(path).toBe("/api/console-sessions/director");
    expect(options.method).toBe("PUT");
    expect(JSON.parse(options.body)).toEqual({
      googleChatSpace: "spaces/BBBBBBBBBBB",
    });
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
  });

  it("does not carry a draft or an open panel over to another session", async () => {
    const user = userEvent.setup();
    const props = {
      capabilities,
      act: vi.fn(),
      onSaved: vi.fn(),
      showError: vi.fn(),
    };
    const { rerender } = render(
      <ConsoleSessionPage session={session()} task={task()} {...props} />,
    );

    const box = document.querySelector(".cs-message-input__content-editor");
    await user.click(box);
    await user.paste("meant for the director");
    await user.click(
      screen.getByRole("button", { name: /Edit Director capabilities/ }),
    );
    expect(screen.getByText("What it may reach")).toBeInTheDocument();

    rerender(
      <ConsoleSessionPage
        session={session({ id: "ops", title: "Ops", taskId: "8" })}
        task={task({ id: "8", consoleSession: "ops" })}
        {...props}
      />,
    );

    const next = document.querySelector(".cs-message-input__content-editor");
    expect(next.textContent).toBe("");
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    expect(screen.queryByText("What it may reach")).not.toBeInTheDocument();
  });

  it("changes which model the session runs on from the same pane", async () => {
    api.mockClear();
    const config = {
      deployment: { id: "default", defaultModel: "gpt-5.6-sol" },
      agentBackends: [
        {
          id: "claude-deep",
          framework: "claude",
          models: ["claude-opus-5"],
        },
      ],
    };
    render(
      <ConsoleSessionPage
        session={session()}
        task={task()}
        config={config}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    const user = userEvent.setup();
    await user.click(
      screen.getByRole("button", { name: /Director capabilities/ }),
    );
    const picker = screen.getByRole("combobox", { name: "Model" });
    expect(picker).toHaveValue("Deployment default (gpt-5.6-sol)");
    await user.click(picker);
    await user.click(screen.getByRole("option", { name: "claude-opus-5" }));

    await waitFor(() => expect(api).toHaveBeenCalled());
    const [path, options] = api.mock.calls.at(-1);
    expect(path).toBe("/api/console-sessions/director");
    expect(options.method).toBe("PUT");
    expect(JSON.parse(options.body)).toEqual({ model: "claude-opus-5" });
  });

  it("says a change takes effect on the next message when no run is open", async () => {
    render(
      <ConsoleSessionPage
        session={session()}
        task={task()}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: /Director capabilities/ }),
    );
    expect(
      screen.getByText(/No run is open on this session/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/next exchange/)).not.toBeInTheDocument();

    await userEvent.click(
      screen.getByRole("checkbox", { name: /Self repair/ }),
    );
    await waitFor(() => expect(api).toHaveBeenCalled());
    expect(
      screen.queryByRole("button", { name: "Apply now" }),
    ).not.toBeInTheDocument();
  });

  // A session's agent blocks in the chat tool between messages, so a
  // reply lands in the run that is already open -- and that run keeps the
  // grants it started with.
  it("says a live run keeps its grants and offers to apply a change now", async () => {
    api.mockClear();
    const onSaved = vi.fn();
    const live = task({
      state: "running",
      attempts: [
        {
          number: 1,
          startedAt: "2026-09-08T10:00:00Z",
          finishedAt: "2026-09-08T10:05:00Z",
        },
        { number: 2, startedAt: "2026-09-08T11:00:00Z" },
      ],
    });
    const { rerender } = render(
      <ConsoleSessionPage
        session={session()}
        task={live}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={onSaved}
        showError={vi.fn()}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: /Director capabilities/ }),
    );
    expect(
      screen.getByText(/A run is open on this session now/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Apply now" }),
    ).not.toBeInTheDocument();

    await userEvent.click(
      screen.getByRole("checkbox", { name: /Self repair/ }),
    );
    const apply = await screen.findByRole("button", { name: "Apply now" });
    expect(
      screen.getByText(/started before your change and does not have it/),
    ).toBeInTheDocument();

    // Closing the panel does not hide what has not landed yet.
    await userEvent.click(
      screen.getByRole("button", { name: /Hide Director capabilities/ }),
    );
    expect(screen.getByRole("button", { name: "Apply now" })).toBe(apply);

    await userEvent.click(apply);
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/console-sessions/director/cancel",
        { method: "POST" },
      ),
    );
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: "Apply now" }),
      ).not.toBeInTheDocument(),
    );

    // The run stopped: the session waits for its next message.
    rerender(
      <ConsoleSessionPage
        session={session()}
        task={task()}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={onSaved}
        showError={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "Apply now" }),
    ).not.toBeInTheDocument();
  });

  it("drops the banner once the run that lacked the change has ended", async () => {
    const running = (attempts) => task({ state: "running", attempts });
    const first = [{ number: 1, startedAt: "2026-09-08T10:00:00Z" }];
    const { rerender } = render(
      <ConsoleSessionPage
        session={session()}
        task={running(first)}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: /Director capabilities/ }),
    );
    await userEvent.selectOptions(
      screen.getByRole("combobox", {
        name: /What it may reach on the internet/,
      }),
      "none",
    );
    await screen.findByRole("button", { name: "Apply now" });

    // A fresh run started with what was saved.
    rerender(
      <ConsoleSessionPage
        session={session({ networkAccess: "none" })}
        task={running([
          { ...first[0], finishedAt: "2026-09-08T10:30:00Z" },
          { number: 2, startedAt: "2026-09-08T10:31:00Z" },
        ])}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "Apply now" }),
    ).not.toBeInTheDocument();
  });

  it("does not offer a restart for settings a run reads as it goes", async () => {
    render(
      <ConsoleSessionPage
        session={session()}
        task={task({ state: "running" })}
        capabilities={capabilities}
        act={vi.fn()}
        onSaved={vi.fn()}
        showError={vi.fn()}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: /Director capabilities/ }),
    );
    await userEvent.click(
      screen.getByRole("checkbox", { name: /File its tasks approved/ }),
    );
    await waitFor(() => expect(api).toHaveBeenCalled());
    expect(
      screen.queryByRole("button", { name: "Apply now" }),
    ).not.toBeInTheDocument();
  });
});
