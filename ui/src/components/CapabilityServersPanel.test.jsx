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
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import CapabilityServersPanel from "./CapabilityServersPanel.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// jsdom draws no page to scroll; opening a server scrolls to its top.
window.scrollTo = vi.fn();

const gcp = "https://github.com/bwsalmon/grain/capabilities/gcp";

const failed = {
  id: gcp,
  config: {
    command: "grain",
    args: ["capability", "gcp"],
    env: { GCP_MINTER_KEY: "$secret:gcp-minter" },
  },
  secrets: [{ name: "gcp-minter", present: false }],
  status: {
    id: gcp,
    state: "failed",
    transport: "stdio",
    error: `capability server ${gcp}: references secrets that are missing or empty: gcp-minter`,
  },
};

const configSchema = {
  type: "object",
  additionalProperties: false,
  required: ["project"],
  properties: {
    project: { type: "string", pattern: "^[a-z][a-z0-9-]+$" },
    minterKey: { type: "string", writeOnly: true },
  },
};

// serve answers api by "METHOD path": a value, or a function of the
// parsed body. Each answer may be a list, used up one call at a time.
function serve(routes) {
  api.mockImplementation(async (path, opts) => {
    const key = `${opts?.method || "GET"} ${path}`;
    let answer = routes[key];
    if (answer === undefined) throw new Error(`unexpected ${key}`);
    if (Array.isArray(answer) && answer.isQueue)
      answer = answer.length > 1 ? answer.shift() : answer[0];
    if (typeof answer === "function")
      return answer(opts?.body ? JSON.parse(opts.body) : undefined);
    if (answer instanceof Error) throw answer;
    return answer;
  });
}

function queue(...answers) {
  const q = [...answers];
  q.isQueue = true;
  return q;
}

function lastCall(path) {
  const calls = api.mock.calls.filter(([p]) => p === path);
  const call = calls.at(-1);
  return (
    call && { ...call[1], body: call[1]?.body && JSON.parse(call[1].body) }
  );
}

const base = {
  "GET /api/capability-servers/usage": [],
  "GET /api/capability-servers/host-entries": {
    available: false,
    reason: "grain hostd is not running beside this daemon (as on Kubernetes)",
    entries: [],
  },
};

const detailOf = (id) =>
  document.querySelector(`[data-capability-server="${id}"]`);
const rowOf = (id) =>
  document.querySelector(`[data-capability-server-row="${id}"]`);

// card is a server's page: the one open, or the one its row in the list
// opens.
async function card(id) {
  if (detailOf(id)) return detailOf(id);
  const row = await waitFor(() => {
    if (!rowOf(id)) throw new Error(`no row for ${id}`);
    return rowOf(id);
  });
  fireEvent.click(row);
  return waitFor(() => {
    if (!detailOf(id)) throw new Error(`no page for ${id}`);
    return detailOf(id);
  });
}

describe("CapabilityServersPanel", () => {
  it("lists every server full width with what it gives, its kind, where it runs, its grants and state, and opens one's page", async () => {
    const canonical = "https://grain.dev/capabilities/gcp";
    const listed = [
      {
        id: canonical,
        config: {
          command: "grain",
          args: ["capability", "gcp"],
          builtin: "gcp",
          aliases: [gcp],
          notice: `the hand-added entry ${gcp} was adopted as it.`,
        },
        secrets: [],
        builtin: {
          name: "gcp",
          id: canonical,
          summary: "per-attempt GCP keys",
        },
        status: {
          id: canonical,
          state: "needs-config",
          transport: "stdio",
          builtin: "gcp",
          reason: "gcp: no permission sets are configured",
          configSchema,
        },
      },
      { ...failed, id: "https://example.com/caps/own" },
    ];
    serve({
      ...base,
      "GET /api/capability-servers": listed,
      "POST /api/capability-servers/dismiss-notice": () => listed,
    });
    const user = userEvent.setup();
    render(<CapabilityServersPanel showError={vi.fn()} />);
    const list = await screen.findByRole("list", {
      name: "Capability servers",
    });
    const rows = list.querySelectorAll("[data-capability-server-row]");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveAttribute("data-capability-server-row", canonical);
    // A row says what the server is: its full name, what it gives a
    // task, built-in or custom, where it runs, its grants and its state.
    expect(rows[0]).toHaveTextContent("per-attempt GCP keys");
    const r0 = within(rows[0]);
    expect(r0.getByTestId("capability-server-kind")).toHaveTextContent(
      "built-in",
    );
    expect(r0.getByTestId("capability-server-runs")).toHaveTextContent(
      "beside grain",
    );
    expect(r0.getByTestId("capability-server-usage-count")).toHaveTextContent(
      "granted by 0",
    );
    expect(r0.getByTestId("capability-server-state")).toHaveTextContent(
      "needs configuring",
    );
    expect(r0.getByTestId("capability-server-row-status")).toHaveAttribute(
      "data-status",
      "degraded",
    );
    expect(rows[1]).toHaveAttribute(
      "data-capability-server-row",
      "https://example.com/caps/own",
    );
    expect(rows[1]).toHaveTextContent("example.com/caps/own");
    expect(
      within(rows[1]).getByTestId("capability-server-kind"),
    ).toHaveTextContent("custom");
    // Each row is a link to the server's own page.
    expect(rows[1]).toHaveAttribute(
      "href",
      `/settings/capabilities/${encodeURIComponent("https://example.com/caps/own")}`,
    );
    expect(
      screen.getByRole("button", { name: "+ Add server" }),
    ).toBeInTheDocument();
    // No server's page is open until one is picked.
    expect(document.querySelectorAll("[data-capability-server]")).toHaveLength(
      0,
    );

    const builtinCard = await card(canonical);
    const b = within(builtinCard);
    expect(b.getByText("gcp server")).toBeInTheDocument();
    expect(b.getByTestId("capability-server-kind")).toHaveTextContent(
      "built-in",
    );
    expect(b.getByTestId("capability-server-state")).toHaveTextContent(
      "needs configuring",
    );
    expect(b.getByTestId("capability-server-needs-config")).toHaveTextContent(
      "no permission sets",
    );
    expect(
      b.getByRole("button", { name: "Edit server settings" }),
    ).toBeInTheDocument();
    expect(
      b.getByRole("button", { name: "Edit connection" }),
    ).toBeInTheDocument();
    expect(b.queryByRole("button", { name: "Remove" })).toBeNull();
    await user.click(b.getByRole("button", { name: "Dismiss" }));
    expect(lastCall("/api/capability-servers/dismiss-notice").body).toEqual({
      id: canonical,
    });

    // Back to the list, and into the custom server: it can be removed.
    await user.click(screen.getByTestId("capability-servers-back"));
    const ownCard = await card("https://example.com/caps/own");
    expect(document.querySelectorAll("[data-capability-server]")).toHaveLength(
      1,
    );
    expect(
      within(ownCard).getByRole("button", { name: "Remove" }),
    ).toBeInTheDocument();
  });

  it("explains every field, label and action in plain words", async () => {
    serve({
      ...base,
      "GET /api/capability-servers": [
        {
          ...failed,
          status: { ...failed.status, configSchema },
        },
      ],
    });
    const user = userEvent.setup();
    render(<CapabilityServersPanel showError={vi.fn()} />);
    const intro = await screen.findByTestId("capability-servers-intro");
    expect(intro).toHaveTextContent(
      /A capability is something a task is given/,
    );
    expect(intro).toHaveTextContent(/A capability server is the program/);
    expect(intro).toHaveTextContent(/A task holds a capability when/);
    expect(
      within(intro).getByRole("link", { name: "Guide to this page" }),
    ).toHaveAttribute(
      "href",
      expect.stringContaining("docs/capability-servers-ui.md"),
    );
    expect(
      within(intro).getByRole("link", { name: "Legacy capabilities" }),
    ).toHaveAttribute("href", "/settings/credentials");
    const glossary = screen.getByTestId("capability-glossary");
    for (const term of [
      "built-in",
      "custom",
      "attached",
      "failed",
      "needs configuring",
      "disabled",
      "pending",
      "not attached",
      "beside grain",
      "on the host",
      "remote (HTTP)",
      "granted by N",
      "capability ID",
      "$secret:<name>",
    ])
      expect(
        within(glossary).getByText(term, { selector: "dt" }),
      ).toBeInTheDocument();

    // Leaving an edited form asks first; say yes to it here.
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    // The add form's every field says what it is for.
    await user.click(screen.getByRole("button", { name: "+ Add server" }));
    const add = screen.getByTestId("capability-server-add");
    expect(add).toHaveTextContent(
      /The name tasks grant this server's methods by/,
    );
    expect(add).toHaveTextContent(/grain starts a program and talks to it/);
    expect(add).toHaveTextContent(/The program grain starts/);
    expect(add).toHaveTextContent(/Given to the command, one per line/);
    expect(add).toHaveTextContent(
      /Environment variables the command starts with/,
    );
    expect(add).toHaveTextContent(/Where grain starts the command/);
    await user.click(within(add).getByLabelText("Transport"));
    await user.click(await screen.findByRole("option", { name: /HTTP/ }));
    expect(add).toHaveTextContent(/Where the server answers MCP requests/);
    expect(add).toHaveTextContent(/Sent with every request to the server/);
    await user.click(screen.getByTestId("capability-servers-back"));
    expect(confirm).toHaveBeenCalled();
    confirm.mockRestore();

    // A server's page: every section has its caption, every action its
    // help, and the state chip its meaning.
    const c = await card(gcp);
    const sections = within(c).getAllByTestId("capability-section-caption");
    expect(
      sections.map((el) => el.closest("section").getAttribute("aria-label")),
    ).toEqual([
      "What it gives a task",
      "Connection",
      "Server settings",
      "Granted by",
      "Check and manage",
    ]);
    for (const el of sections)
      expect(el.textContent.length).toBeGreaterThan(20);
    expect(
      within(c).getByTestId("capability-server-state-help"),
    ).toHaveTextContent(/could not start or reach the server/);
    const help = within(c)
      .getAllByTestId("capability-action-help")
      .map((el) => el.textContent);
    expect(help.some((t) => /Bind a secret stores a credential/.test(t))).toBe(
      true,
    );
    expect(help.some((t) => /Saves nothing/.test(t))).toBe(true);
    expect(help.some((t) => /keeps its entry and settings/.test(t))).toBe(true);
    expect(
      help.some((t) => /Deletes the entry from grain\/config/.test(t)),
    ).toBe(true);
    await user.click(within(c).getByRole("button", { name: "Bind a secret" }));
    expect(c).toHaveTextContent(/The variable the server reads/);
    expect(c).toHaveTextContent(/What the value is stored under/);
    expect(c).toHaveTextContent(/Text put before the secret/);
    expect(c).toHaveTextContent(/The credential itself/);
  });

  it("follows the server the caller routes to, and tells it which one is picked", async () => {
    serve({ ...base, "GET /api/capability-servers": [failed] });
    const onSelect = vi.fn();
    const { rerender } = render(
      <CapabilityServersPanel
        showError={vi.fn()}
        selected=""
        onSelect={onSelect}
      />,
    );
    const user = userEvent.setup();
    await waitFor(() => expect(rowOf(gcp)).not.toBeNull());
    await user.click(rowOf(gcp));
    expect(onSelect).toHaveBeenLastCalledWith(gcp);
    rerender(
      <CapabilityServersPanel
        showError={vi.fn()}
        selected={gcp}
        onSelect={onSelect}
      />,
    );
    expect(detailOf(gcp)).not.toBeNull();
    await user.click(screen.getByTestId("capability-servers-back"));
    expect(onSelect).toHaveBeenLastCalledWith("");
    rerender(
      <CapabilityServersPanel
        showError={vi.fn()}
        selected="https://example.com/nope"
        onSelect={onSelect}
      />,
    );
    expect(screen.getByTestId("capability-server-unknown")).toHaveTextContent(
      "https://example.com/nope",
    );
  });

  it("adds a server from + Add server, opens its page, and goes back to the list on removal", async () => {
    const canonical = "https://grain.dev/capabilities/gcp";
    const builtinServer = {
      id: canonical,
      config: { command: "grain", args: ["capability", "gcp"], builtin: "gcp" },
      secrets: [],
      builtin: { name: "gcp", id: canonical, summary: "per-attempt GCP keys" },
      status: { id: canonical, state: "attached", transport: "stdio" },
    };
    const customServer = {
      id: "https://example.com/caps/custom",
      config: { serverUrl: "https://cap.example.com/mcp" },
      secrets: [],
      status: {
        id: "https://example.com/caps/custom",
        state: "attached",
        transport: "http",
      },
    };
    serve({
      ...base,
      "GET /api/capability-servers": [builtinServer],
      "PUT /api/capability-servers": [builtinServer, customServer],
      "POST /api/capability-servers/remove": [builtinServer],
    });
    const user = userEvent.setup();
    render(<CapabilityServersPanel showError={vi.fn()} />);

    await waitFor(() => expect(rowOf(canonical)).not.toBeNull());
    expect(
      within(rowOf(canonical)).getByTestId("capability-server-row-status"),
    ).toHaveAttribute("data-status", "ok");

    await user.click(screen.getByTestId("capability-server-add-button"));
    expect(screen.getByTestId("capability-server-add")).toBeInTheDocument();
    await user.type(screen.getByLabelText(/Capability ID/), customServer.id);
    await user.click(screen.getByLabelText("Transport"));
    await user.click(await screen.findByRole("option", { name: /HTTP/ }));
    await user.type(
      screen.getByLabelText(/Server URL/),
      customServer.config.serverUrl,
    );
    await user.click(screen.getByRole("button", { name: "Add server" }));

    // The newly added server's page opens.
    const addedCard = await waitFor(() => {
      if (!detailOf(customServer.id)) throw new Error("not open");
      return detailOf(customServer.id);
    });

    // Removing it goes back to the list, without it.
    await user.click(within(addedCard).getByRole("button", { name: "Remove" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(rowOf(canonical)).not.toBeNull());
    expect(rowOf(customServer.id)).toBeNull();
    expect(detailOf(customServer.id)).toBeNull();
  });

  afterEach(() => {
    api.mockReset();
  });

  it("lists a gchat server's spaces with their usage and sends a test message to one", async () => {
    const id = "https://grain.dev/capabilities/gchat";
    const gchat = encodeURIComponent(id);
    const posted = {
      space: "team-room",
      transport: "webhook",
      posted: 3,
      refused: 1,
      failed: 0,
      last_hour: 2,
      per_hour: 60,
      last_post: "2026-10-26T12:00:00Z",
    };
    serve({
      ...base,
      "GET /api/capability-servers": [
        {
          id,
          config: {
            command: "grain",
            args: ["capability", "gchat"],
            builtin: "gchat",
            config: {
              spaces: [
                { name: "team-room", transport: "webhook" },
                {
                  name: "corp",
                  transport: "app",
                  space: "spaces/AAAA",
                  app: "corp-bot",
                },
              ],
            },
          },
          secrets: [],
          builtin: { name: "gchat", id, summary: "post to Google Chat" },
          status: {
            id,
            state: "attached",
            transport: "stdio",
            builtin: "gchat",
            methods: ["post"],
            tools: ["test_post", "usage"],
          },
        },
      ],
      [`GET /api/capability-servers/gchat-usage?id=${gchat}`]: queue(
        [{ ...posted, posted: 2, last_post: undefined }],
        [posted],
      ),
      "POST /api/capability-servers/gchat-test": (body) =>
        body.space === "team-room"
          ? {
              space: "team-room",
              message: "spaces/X/messages/1",
              thread: "spaces/X/threads/1",
            }
          : { space: body.space, error: "Google Chat answered 403 Forbidden" },
    });
    const user = userEvent.setup();
    render(<CapabilityServersPanel showError={vi.fn()} />);
    await card(id);
    const list = await screen.findByTestId("gchat-space-list");
    const room = list.querySelector('[data-gchat-space="team-room"]');
    expect(room).toHaveTextContent("webhook · incoming webhook");
    await within(room).findByText("2 posted");
    expect(within(room).getByText("2 / 60 this hour")).toBeInTheDocument();
    const corp = list.querySelector('[data-gchat-space="corp"]');
    expect(corp).toHaveTextContent("app · spaces/AAAA as corp-bot");

    await user.click(
      within(room).getByRole("button", { name: "Send test message" }),
    );
    expect(
      await within(room).findByTestId("gchat-test-result"),
    ).toHaveTextContent(
      "Posted spaces/X/messages/1 in thread spaces/X/threads/1.",
    );
    expect(lastCall("/api/capability-servers/gchat-test").body).toEqual({
      id,
      space: "team-room",
    });
    // The usage is read again after the post.
    await within(room).findByText("3 posted");
    expect(within(room).getByText(/^last post /)).toBeInTheDocument();

    await user.click(
      within(corp).getByRole("button", { name: "Send test message" }),
    );
    expect(
      await within(corp).findByTestId("gchat-test-result"),
    ).toHaveTextContent("Not posted: Google Chat answered 403 Forbidden");
  });

  it("shows each server's state, error and missing secret by name", async () => {
    serve({ ...base, "GET /api/capability-servers": [failed] });
    render(<CapabilityServersPanel showError={() => {}} />);

    const c = await card(gcp);
    expect(within(c).getByTestId("capability-server-state")).toHaveTextContent(
      "failed",
    );
    expect(
      within(c).getByText(/missing or empty: gcp-minter/),
    ).toBeInTheDocument();
    expect(within(c).getByText("gcp-minter: missing")).toBeInTheDocument();
    expect(within(c).getByText("grain capability gcp")).toBeInTheDocument();
  });

  it("reads a server with no status again only a few times, not forever", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const git = "https://grain.dev/capabilities/git";
      serve({
        ...base,
        "GET /api/capability-servers": [
          {
            id: git,
            builtin: { name: "git" },
            config: { command: "grain", args: ["capability", "git"] },
            secrets: [],
            status: null,
          },
        ],
      });
      render(<CapabilityServersPanel showError={() => {}} />);
      const c = await card(git);
      expect(within(c).getByText("not attached")).toBeInTheDocument();
      const reads = () =>
        api.mock.calls.filter(([p]) => p === "/api/capability-servers").length;
      for (let i = 0; i < 20; i++) await vi.advanceTimersByTimeAsync(2000);
      const settled = reads();
      expect(settled).toBeGreaterThan(1);
      expect(settled).toBeLessThanOrEqual(6);
      await vi.advanceTimersByTimeAsync(20000);
      expect(reads()).toBe(settled);
    } finally {
      vi.useRealTimers();
    }
  });

  it("binds an env entry to a secret, sending the value only in the bind call", async () => {
    serve({
      ...base,
      "GET /api/capability-servers": [failed],
      "POST /api/capability-servers/bind-secret": [
        {
          ...failed,
          secrets: [{ name: "gcp-minter", present: true }],
          status: { ...failed.status, state: "pending", error: "" },
        },
      ],
    });
    render(<CapabilityServersPanel showError={() => {}} />);
    const c = await card(gcp);
    const user = userEvent.setup();
    expect(within(c).queryByLabelText(/Env name/)).not.toBeInTheDocument();
    await user.click(within(c).getByRole("button", { name: "Bind a secret" }));
    await user.type(within(c).getByLabelText(/Env name/), "GCP_MINTER_KEY");
    await user.type(within(c).getByLabelText(/Secret name/), "gcp-minter");
    await user.type(within(c).getByLabelText(/Value/), "key-material");
    await user.click(within(c).getByRole("button", { name: "Bind secret" }));

    expect(lastCall("/api/capability-servers/bind-secret").body).toMatchObject({
      id: gcp,
      kind: "env",
      name: "GCP_MINTER_KEY",
      secret: "gcp-minter",
      value: "key-material",
    });
    expect(await screen.findByText("gcp-minter: set")).toBeInTheDocument();
    expect(screen.queryByText(/key-material/)).not.toBeInTheDocument();
  });

  it("adds an HTTP server and disables one", async () => {
    const added = {
      id: "https://example.com/caps/x",
      config: { serverUrl: "https://cap.example.com/mcp" },
      secrets: [],
    };
    serve({
      ...base,
      "GET /api/capability-servers": [],
      "PUT /api/capability-servers": [added],
      "POST /api/capability-servers/disable": [
        { ...added, config: { ...added.config, disabled: true } },
      ],
    });
    render(<CapabilityServersPanel showError={() => {}} />);
    expect(
      await screen.findByText("No capability servers configured."),
    ).toBeInTheDocument();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "+ Add server" }));
    await user.type(
      screen.getByLabelText(/Capability ID/),
      "https://example.com/caps/x",
    );
    await user.click(screen.getByLabelText("Transport"));
    await user.click(await screen.findByRole("option", { name: /HTTP/ }));
    await user.type(
      screen.getByLabelText(/Server URL/),
      "https://cap.example.com/mcp",
    );
    await user.click(screen.getByRole("button", { name: "Add server" }));
    expect(lastCall("/api/capability-servers").body).toEqual({
      id: "https://example.com/caps/x",
      config: { serverUrl: "https://cap.example.com/mcp" },
    });

    await user.click(await screen.findByRole("button", { name: "Disable" }));
    expect(lastCall("/api/capability-servers/disable").body).toEqual({
      id: added.id,
    });
    expect(
      await screen.findByRole("button", { name: "Enable" }),
    ).toBeInTheDocument();
  });

  it("edits a server in place and renames it only after the usage warning", async () => {
    serve({
      ...base,
      "GET /api/capability-servers": [failed],
      "GET /api/capability-servers/usage": [
        {
          id: gcp,
          holders: [
            {
              kind: "task",
              id: "42",
              name: "rotate keys",
              repo: "acme/widgets",
              grant: `${gcp}#gcp-key`,
            },
          ],
        },
      ],
      "PUT /api/capability-servers": (body) => [
        { ...failed, id: body.id, config: body.config },
      ],
    });
    render(<CapabilityServersPanel showError={() => {}} />);
    const c = await card(gcp);
    expect(
      within(c).getByTestId("capability-server-usage-count"),
    ).toHaveTextContent("granted by 1");
    const user = userEvent.setup();
    await user.click(
      within(c).getByRole("button", { name: "Edit connection" }),
    );
    const form = within(c).getByTestId("capability-server-edit");
    const args = within(form).getByLabelText(/Arguments/);
    await user.clear(args);
    await user.type(args, "capability{enter}gcp{enter}-v");
    await user.click(
      within(form).getByRole("button", { name: "Save connection" }),
    );
    expect(lastCall("/api/capability-servers").body).toEqual({
      id: gcp,
      config: { ...failed.config, args: ["capability", "gcp", "-v"] },
    });

    await user.click(
      within(await card(gcp)).getByRole("button", { name: "Edit connection" }),
    );
    const again = within(await card(gcp)).getByTestId("capability-server-edit");
    const id = within(again).getByLabelText(/Capability ID/);
    await user.clear(id);
    await user.type(id, `${gcp}-v2`);
    const puts = api.mock.calls.filter(
      ([p]) => p === "/api/capability-servers",
    ).length;
    await user.click(
      within(again).getByRole("button", { name: "Save connection" }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/Task #42/)).toHaveTextContent(
      "rotate keys",
    );
    expect(
      api.mock.calls.filter(([p]) => p === "/api/capability-servers").length,
    ).toBe(puts);
    await user.click(within(dialog).getByRole("button", { name: "Rename" }));
    expect(lastCall("/api/capability-servers").body).toMatchObject({
      id: `${gcp}-v2`,
      renameFrom: gcp,
    });
  });

  it("offers run: host only when hostd is there, saying why not", async () => {
    serve({ ...base, "GET /api/capability-servers": [] });
    render(<CapabilityServersPanel showError={() => {}} />);
    await userEvent
      .setup()
      .click(await screen.findByRole("button", { name: "+ Add server" }));
    expect(
      await screen.findByText(
        /host is not available: grain hostd is not running/,
      ),
    ).toBeInTheDocument();
  });

  it("configures a server from its schema with a write-only secret, and shows field errors inline", async () => {
    const configurable = {
      id: gcp,
      config: {
        command: "grain",
        args: ["capability", "gcp"],
        config: { project: "p1", minterKey: "$secret:gcp-minter/key" },
      },
      secrets: [{ name: "gcp-minter/key", present: true }],
      status: { id: gcp, state: "attached", transport: "stdio", configSchema },
    };
    const refusal = new Error("config.project: must match ^[a-z][a-z0-9-]+$");
    refusal.fields = { project: "must match ^[a-z][a-z0-9-]+$" };
    serve({
      ...base,
      "GET /api/capability-servers": [configurable],
      "PUT /api/capability-servers": queue(refusal, [configurable]),
      "POST /api/capability-servers/test": {
        ok: false,
        methods: [],
        configSchema,
        error: 'config schema uses "format", which grain does not enforce',
      },
    });
    const showError = vi.fn();
    render(<CapabilityServersPanel showError={showError} />);
    const c = await card(gcp);
    const user = userEvent.setup();
    await user.click(
      within(c).getByRole("button", { name: "Edit server settings" }),
    );
    const form = within(c).getByTestId("capability-server-config");
    expect(
      within(form).getByTestId("secret-state-minterKey"),
    ).toHaveTextContent("set");
    const secret = within(form).getByLabelText(/minterKey/);
    expect(secret).toHaveAttribute("type", "password");
    expect(secret).toHaveValue("");
    await user.type(secret, "new-key");
    const project = within(form).getByLabelText(/^project/);
    await user.clear(project);
    await user.type(project, "Bad");
    await user.click(
      within(form).getByRole("button", { name: "Save server settings" }),
    );
    expect(lastCall("/api/capability-servers").body).toEqual({
      id: gcp,
      config: {
        ...configurable.config,
        config: { project: "Bad", minterKey: "$secret:gcp-minter/key" },
      },
      configSecrets: { minterKey: "new-key" },
    });
    expect(await within(form).findByText(/must match/)).toBeInTheDocument();

    await user.click(
      within(form).getByRole("button", { name: "Test server settings" }),
    );
    expect(
      await within(form).findByText(/"format", which grain does not enforce/),
    ).toBeInTheDocument();
  });

  it("shows a grain-managed token as managed, rotates it, and keeps its override under Advanced", async () => {
    const workspace = "https://grain.dev/capabilities/workspace";
    const tokenSchema = {
      type: "object",
      additionalProperties: false,
      properties: {
        dataPlaneToken: {
          type: "string",
          writeOnly: true,
          title: "Data-plane token",
        },
      },
    };
    const managed = {
      id: workspace,
      config: { command: "grain", args: ["capability", "workspace"] },
      secrets: [],
      managed: [
        {
          field: "dataPlaneToken",
          secret: "capability-managed-0a1b2c3d/data-plane-token",
          present: true,
          overridden: false,
        },
      ],
      status: {
        id: workspace,
        state: "attached",
        transport: "stdio",
        configSchema: tokenSchema,
      },
    };
    serve({
      ...base,
      "GET /api/capability-servers": [managed],
      "POST /api/capability-servers/rotate-managed": [managed],
      "PUT /api/capability-servers": [managed],
    });
    render(<CapabilityServersPanel showError={vi.fn()} />);
    const c = await card(workspace);
    const user = userEvent.setup();
    await user.click(
      within(c).getByRole("button", { name: "Edit server settings" }),
    );
    const form = within(c).getByTestId("capability-server-config");
    expect(
      within(form).getByTestId("managed-state-dataPlaneToken"),
    ).toHaveTextContent("managed by grain");
    expect(within(form).queryByLabelText(/Data-plane token/)).toBeNull();

    await user.click(within(form).getByRole("button", { name: "Rotate" }));
    expect(lastCall("/api/capability-servers/rotate-managed").body).toEqual({
      id: workspace,
    });
    // Rotating, like saving, closes the form over the refreshed list.
    const c2 = await card(workspace);
    await user.click(
      await within(c2).findByRole("button", { name: "Edit server settings" }),
    );
    const after = within(c2).getByTestId("capability-server-config");

    await user.click(within(after).getByRole("button", { name: "Advanced" }));
    const override = await within(after).findByLabelText(
      /Override Data-plane token/,
    );
    expect(override).toHaveAttribute("type", "password");
    await user.type(override, "x".repeat(40));
    await user.click(
      within(after).getByRole("button", { name: "Save server settings" }),
    );
    expect(lastCall("/api/capability-servers").body.configSecrets).toEqual({
      dataPlaneToken: "x".repeat(40),
    });
  });

  it("configures config maps and secret maps of several keys, writing only filled secret values", async () => {
    const cmId = "https://grain.dev/capabilities/config-map";
    const stored = {
      config_maps: [
        {
          name: "my-tools",
          description: "Tool settings",
          data: {
            "settings.toml": { content: "[user]\nname = Grain\n" },
            "run.sh": { content: "#!/bin/sh\n", executable: true },
          },
        },
      ],
      secrets: [
        {
          name: "deploy-creds",
          dir: "/home/debian/.creds",
          description: "Deploy credentials",
          keys: {
            "key.json": { mode: "0400" },
            token: {},
            old: {},
          },
        },
      ],
      secret_values: "$secret:cap-cm/secret_values",
    };
    const cmServer = {
      id: cmId,
      builtin: {
        name: "config-map",
        id: cmId,
        summary:
          "configurable files and secrets placed in the sandbox (mint_config-map)",
        needsConfig: true,
      },
      config: {
        command: "grain",
        args: ["capability", "config-map"],
        builtin: "config-map",
        config: stored,
      },
      secrets: [{ name: "cap-cm/secret_values", present: true }],
      configuredSecretKeys: ["deploy-creds/key.json", "deploy-creds/old"],
      status: {
        id: cmId,
        builtin: "config-map",
        state: "attached",
        transport: "stdio",
        configSchema: {
          type: "object",
          properties: {
            config_maps: { type: "array" },
            secrets: { type: "array" },
            secret_values: { type: "string", writeOnly: true },
          },
        },
      },
    };
    serve({
      ...base,
      "GET /api/capability-servers": [cmServer],
      "PUT /api/capability-servers": [cmServer],
    });
    render(<CapabilityServersPanel showError={() => {}} />);
    const c = await card(cmId);
    const list = within(c).getByTestId("config-map-list");
    expect(list).toHaveTextContent("my-tools");
    expect(list).toHaveTextContent("/home/debian/.config-map/my-tools");
    expect(list).toHaveTextContent("key.json: set");
    expect(list).toHaveTextContent("token: not set");

    const user = userEvent.setup();
    await user.click(
      within(c).getByRole("button", { name: "Edit server settings" }),
    );
    const form = within(c).getByTestId("capability-server-config");
    const [cmCard] = within(form).getAllByTestId("config-map-entry-row");
    expect(within(cmCard).getByLabelText(/^Directory/)).toHaveAttribute(
      "placeholder",
      "/home/debian/.config-map/my-tools",
    );
    expect(within(cmCard).getAllByTestId("config-map-file-row")).toHaveLength(
      2,
    );
    // Add a third file to the config map.
    await user.click(within(cmCard).getByRole("button", { name: "Add file" }));
    const cmFiles = within(cmCard).getAllByTestId("config-map-file-row");
    await user.type(within(cmFiles[2]).getByLabelText(/^Key/), "extra.conf");
    await user.type(within(cmFiles[2]).getByLabelText(/File content/), "x=1");
    await user.type(
      within(cmFiles[2]).getByLabelText(/Path override/),
      "/home/debian/.extra.conf",
    );

    const [secCard] = within(form).getAllByTestId("config-map-secret-row");
    expect(
      within(secCard).getByTestId("secret-state-deploy-creds/key.json"),
    ).toHaveTextContent("set");
    expect(
      within(secCard).getByTestId("secret-state-deploy-creds/token"),
    ).toHaveTextContent("not set");
    const secRows = within(secCard).getAllByTestId("config-map-secret-key-row");
    // Rows are sorted by key: key.json, old, token.
    const tokenInput = within(secRows[2]).getByLabelText(/Secret value/);
    expect(tokenInput).toHaveAttribute("type", "password");
    expect(tokenInput).toHaveValue("");
    await user.type(tokenInput, "new-token");
    // Remove the "old" key.
    await user.click(
      within(secRows[1]).getByRole("button", { name: "Remove file" }),
    );

    await user.click(
      within(form).getByRole("button", { name: "Save server settings" }),
    );
    expect(lastCall("/api/capability-servers").body).toEqual({
      id: cmId,
      config: {
        ...cmServer.config,
        config: {
          config_maps: [
            {
              name: "my-tools",
              description: "Tool settings",
              data: {
                "run.sh": { content: "#!/bin/sh\n", executable: true },
                "settings.toml": { content: "[user]\nname = Grain\n" },
                "extra.conf": {
                  content: "x=1",
                  path: "/home/debian/.extra.conf",
                },
              },
            },
          ],
          secrets: [
            {
              name: "deploy-creds",
              description: "Deploy credentials",
              dir: "/home/debian/.creds",
              keys: { "key.json": { mode: "0400" }, token: {} },
            },
          ],
          secret_values: "$secret:cap-cm/secret_values",
        },
      },
      configSecrets: {
        secret_values: JSON.stringify({ "deploy-creds/token": "new-token" }),
      },
    });
  });

  it("says a run: host built-in's hostd entry is missing, that grain adds it, and the text to paste, with Configure disabled until it starts", async () => {
    const workspace = "https://grain.dev/capabilities/workspace";
    const entry =
      "  grain-workspace:\n    command: /usr/local/bin/grain\n    args: [capability, workspace]\n";
    serve({
      ...base,
      "GET /api/capability-servers/host-entries": {
        available: true,
        entries: ["grain-gcp", "host-git"],
        missing: [
          {
            name: "grain-workspace",
            entry,
            command: "sudo systemctl start grain-hostd-allowlist.service",
          },
        ],
      },
      "GET /api/capability-servers": [
        {
          id: workspace,
          config: {
            command: "grain-workspace",
            run: "host",
            args: ["-listen", "127.0.0.1:4250"],
            builtin: "workspace",
          },
          secrets: [],
          builtin: {
            name: "workspace",
            id: workspace,
            summary: "workspace workspaces",
          },
          status: {
            id: workspace,
            state: "needs-config",
            transport: "stdio",
            run: "host",
            builtin: "workspace",
            error: `capability server grain-workspace is set to run: host, but grain hostd's allowlist offers no capability entry "grain-workspace"`,
          },
        },
        {
          id: "https://example.com/caps/own",
          config: { command: "corp-search", run: "host" },
          secrets: [],
          status: { id: "https://example.com/caps/own", state: "failed" },
        },
      ],
    });
    const user = userEvent.setup();
    render(<CapabilityServersPanel />);
    const row = await card(workspace);
    const note = await within(row).findByTestId("hostd-entry-missing");
    expect(note).toHaveTextContent("grain adds it automatically");
    expect(note).toHaveTextContent(
      "sudo systemctl start grain-hostd-allowlist.service",
    );
    expect(within(note).getByTestId("hostd-entry-text").textContent).toBe(
      entry,
    );
    expect(
      within(row).getByTestId("capability-server-configure-unavailable"),
    ).toBeDisabled();
    expect(row).toHaveTextContent(
      "Configure becomes available once the server starts — fix:",
    );
    // An operator's own entry is theirs to add: no promise, no text.
    await user.click(screen.getByTestId("capability-servers-back"));
    const own = await card("https://example.com/caps/own");
    const ownNote = within(own).getByTestId("hostd-entry-missing");
    expect(ownNote).toHaveTextContent(
      "grain never edits entries it does not own",
    );
    expect(within(ownNote).queryByTestId("hostd-entry-text")).toBeNull();
  });

  it("says when grain hostd is not reachable for a run: host server", async () => {
    serve({
      ...base,
      "GET /api/capability-servers": [
        {
          id: "https://example.com/caps/own",
          config: { command: "corp-search", run: "host" },
          secrets: [],
          status: { id: "https://example.com/caps/own", state: "failed" },
        },
      ],
    });
    render(<CapabilityServersPanel />);
    const row = await card("https://example.com/caps/own");
    expect(
      await within(row).findByTestId("hostd-unreachable"),
    ).toHaveTextContent("grain hostd is not reachable");
  });

  it("shortens long server summaries and puts server, method, and field descriptions in (i) popovers", async () => {
    const leaseId = "https://grain.dev/capabilities/workspace-lease";
    const longSummary =
      "Leased workspaces on the host machine, provisioned per attempt with narrow proxy access — gives sandboxes a read/write view of a single checkout without exposing the host's full credentials or other workspaces.";
    const longMethodDesc =
      "Lease a dedicated workspace on the host for this attempt and mount it through a narrow proxy that only serves that single workspace.";
    const longFieldDesc =
      "Prefix used when naming workspaces leased for task attempts; each workspace is named <prefix>-<attempt_id> so reaping can identify orphaned directories.";
    const leaseServer = {
      id: leaseId,
      config: {
        command: "grain-workspace-lease",
        run: "host",
        builtin: "workspace-lease",
        config: { client_prefix: "grain" },
      },
      secrets: [],
      builtin: {
        name: "workspace-lease",
        id: leaseId,
        summary: longSummary,
      },
      status: {
        id: leaseId,
        state: "attached",
        transport: "stdio",
        builtin: "workspace-lease",
        methods: ["workspace-lease"],
        methodDetails: [
          {
            name: "workspace-lease",
            description: longMethodDesc,
            releasable: true,
            args: {
              type: "object",
              properties: {
                base_rev: {
                  type: "string",
                  description: "Optional base revision to sync.",
                },
              },
            },
          },
        ],
        configSchema: {
          type: "object",
          additionalProperties: false,
          properties: {
            client_prefix: {
              type: "string",
              description: longFieldDesc,
            },
          },
        },
      },
    };
    serve({
      ...base,
      "GET /api/capability-servers": [leaseServer],
    });
    const user = userEvent.setup();
    render(<CapabilityServersPanel showError={vi.fn()} />);

    // In the server list row, only the first clause is inline; the full summary is in (i).
    await waitFor(() => expect(rowOf(leaseId)).not.toBeNull());
    const r = within(rowOf(leaseId));
    const rowSummary = r.getByTestId("capability-server-summary");
    expect(rowSummary).toHaveTextContent(
      "Leased workspaces on the host machine, provisioned per attempt with narrow proxy access",
    );
    expect(rowSummary).not.toHaveTextContent("without exposing the host's");
    await user.hover(r.getByRole("button", { name: "About workspace-lease" }));
    expect(await screen.findByRole("tooltip")).toHaveTextContent(longSummary);
    await user.unhover(
      r.getByRole("button", { name: "About workspace-lease" }),
    );

    // Open the server detail card.
    const c = await card(leaseId);
    const detailSummary = within(c).getByTestId("capability-server-summary");
    expect(detailSummary).not.toHaveTextContent("without exposing the host's");
    await user.hover(
      within(c).getByRole("button", { name: "About workspace-lease server" }),
    );
    expect(await screen.findByRole("tooltip")).toHaveTextContent(longSummary);
    await user.unhover(
      within(c).getByRole("button", { name: "About workspace-lease server" }),
    );

    // Method descriptions are in (i) popovers, not inline.
    const methodsSection = within(c).getByTestId("capability-server-methods");
    expect(methodsSection).not.toHaveTextContent(longMethodDesc);
    await user.hover(
      within(methodsSection).getByRole("button", {
        name: "About workspace-lease",
      }),
    );
    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      longMethodDesc,
    );
    await user.unhover(
      within(methodsSection).getByRole("button", {
        name: "About workspace-lease",
      }),
    );

    // Configure schema field descriptions are in (i) popovers, not inline.
    await user.click(
      within(c).getByRole("button", { name: "Edit server settings" }),
    );
    const form = within(c).getByTestId("capability-server-config");
    expect(form).not.toHaveTextContent(longFieldDesc);
    const fieldInfo = within(form).getByRole("button", {
      name: "About client_prefix",
    });
    fieldInfo.focus();
    await waitFor(() =>
      expect(screen.getByRole("tooltip")).toHaveTextContent(longFieldDesc),
    );
  });
});
