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

import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import GrantsPicker from "./GrantsPicker.jsx";
import {
  capabilityMethods,
  invalidateCapabilityMethods,
  resetCapabilityMethodsForTest,
} from "./capabilityMethods.js";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const hostScript = {
  ref: "https://example.com/caps/host#host-script",
  method: "host-script",
  args: null,
};
const gcpKey = {
  ref: "https://github.com/bwsalmon/grain/capabilities/gcp#gcp-key",
  method: "gcp-key",
  args: null,
};

// serve answers GET /api/capability-methods with whatever listing holds
// at the time, so a test can attach a server after the first read.
function serve(listing) {
  api.mockImplementation(async (path, opts) => {
    if (path === "/api/capability-methods") return listing.current;
    if (path === "/api/capability-grants/check")
      return JSON.parse(opts.body).grants.map(({ grant }) => ({
        grant,
        recorded: grant,
      }));
    throw new Error(`unexpected ${path}`);
  });
}

const methodReads = () =>
  api.mock.calls.filter(([p]) => p === "/api/capability-methods").length;

describe("GrantsPicker", () => {
  beforeEach(() => resetCapabilityMethodsForTest());
  afterEach(() => {
    vi.useRealTimers();
    api.mockReset();
  });

  // grain/task-1056: the listing used to be fetched once per page, so a
  // server configured after the page loaded never reached the menu.
  it("offers a method added after the first read when the menu opens", async () => {
    const listing = { current: [hostScript] };
    serve(listing);
    const user = userEvent.setup();
    render(
      <GrantsPicker
        grants={[]}
        config={{ capabilityMethods: 1 }}
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    await user.click(await screen.findByRole("button", { name: "Add grant…" }));
    expect(await screen.findByText("host-script")).toBeInTheDocument();
    expect(screen.queryByText("gcp-key")).toBeNull();
    await user.keyboard("{Escape}");

    listing.current = [hostScript, gcpKey];
    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    expect(await screen.findByText("gcp-key")).toBeInTheDocument();
  });

  it("does not flag a grant for a method the cached listing predates", async () => {
    const listing = { current: [hostScript] };
    serve(listing);
    // Another form on the page read the listing before gcp was configured.
    await capabilityMethods();
    listing.current = [hostScript, gcpKey];

    render(
      <GrantsPicker
        grants={[gcpKey.ref]}
        config={{ capabilityMethods: 1 }}
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    const chip = await screen.findByLabelText("github.com › gcp-key");
    await waitFor(() => expect(methodReads()).toBe(2));
    expect(chip).toBeInTheDocument();
    expect(screen.queryByLabelText(/server not attached/)).toBeNull();
  });

  it("still flags a grant whose method is absent from a fresh read", async () => {
    serve({ current: [hostScript] });
    render(
      <GrantsPicker
        grants={[gcpKey.ref]}
        config={{ capabilityMethods: 1 }}
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    expect(
      await screen.findByLabelText(
        "github.com › gcp-key (server not attached)",
      ),
    ).toBeInTheDocument();
  });

  it("reuses a listing for its TTL and reads again once invalidated", async () => {
    serve({ current: [hostScript] });
    await capabilityMethods();
    await capabilityMethods();
    expect(methodReads()).toBe(1);
    invalidateCapabilityMethods();
    await capabilityMethods();
    expect(methodReads()).toBe(2);

    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(Date.now() + 31000);
    await act(async () => {
      await capabilityMethods();
    });
    expect(methodReads()).toBe(3);
  });

  const config = {
    capabilityMethods: 1,
    targetRepos: ["acme/widgets"],
    capabilities: [
      { id: "gcp-key", name: "GCP key", description: "a key" },
      {
        id: "api:task-admin",
        name: "API preset: task-admin",
        description: "Edit tasks",
        summary: "Edit tasks",
        preset: true,
        scopes: ["tasks:read", "tasks:write"],
      },
      {
        id: "api:tasks:read",
        description: "Read tasks",
        summary: "Read tasks",
      },
      {
        id: "api:tasks:admin",
        description: "Approve tasks",
        summary: "Approve tasks",
        dangerous: true,
      },
      { id: "api:ci:read", description: "Read CI", summary: "Read CI" },
    ],
  };

  it("lists api presets, api scopes by area and server methods in one menu, grouped by kind", async () => {
    serve({ current: [hostScript] });
    const user = userEvent.setup();
    render(
      <GrantsPicker
        grants={[]}
        config={config}
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    const menu = await screen.findByRole("menu", { name: "Grants to add" });
    await within(menu).findByText("host-script");
    const groups = [...menu.querySelectorAll("[data-grant-group]")].map(
      (el) => el.textContent,
    );
    expect(groups).toEqual([
      "Grain API presets",
      "Grain API scopes: tasks",
      "Grain API scopes: ci",
      "Capability server methods",
    ]);
    // The legacy in-process ids are the checklist's, not this menu's.
    expect(within(menu).queryByText(/GCP key/)).toBeNull();
    expect(
      within(menu).getByText("grants tasks:read, tasks:write"),
    ).toBeTruthy();
    expect(
      within(menu)
        .getByRole("menuitemcheckbox", { name: "tasks:admin" })
        .querySelector(".api-dangerous"),
    ).not.toBeNull();
  });

  it("narrows the menu by search across every kind", async () => {
    serve({ current: [hostScript, gcpKey] });
    const user = userEvent.setup();
    render(
      <GrantsPicker
        grants={[]}
        config={config}
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    const menu = await screen.findByRole("menu", { name: "Grants to add" });
    await within(menu).findByText("gcp-key");
    await user.type(screen.getByLabelText("Search grants"), "ci");
    expect(within(menu).getByRole("menuitemcheckbox", { name: "ci:read" }));
    expect(
      within(menu).queryByRole("menuitemcheckbox", { name: "tasks:read" }),
    ).toBeNull();
    expect(within(menu).queryByText("gcp-key")).toBeNull();
    await user.clear(screen.getByLabelText("Search grants"));
    await user.type(screen.getByLabelText("Search grants"), "zzz");
    expect(within(menu).getByText("Nothing matches")).toBeTruthy();
  });

  it("adds an api grant, limited to a repository, and removes one from its line", async () => {
    serve({ current: [] });
    const onApiChange = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(
      <GrantsPicker
        grants={["gcp-key", "api:tasks:admin"]}
        config={config}
        onApiChange={onApiChange}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    // Held grants are one list of sections; the legacy id is not among them.
    const held = screen.getByTestId("grant-grants");
    expect(within(held).queryByText(/gcp-key/)).toBeNull();
    expect(
      within(held).getByLabelText("api:tasks:admin (dangerous)"),
    ).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    await user.click(screen.getByLabelText("Grain API grants apply to"));
    await user.click(screen.getByRole("option", { name: "acme/widgets" }));
    await user.click(screen.getByRole("menuitemcheckbox", { name: "ci:read" }));
    expect(onApiChange).toHaveBeenLastCalledWith([
      "api:tasks:admin",
      "api:ci:read@acme/widgets",
    ]);

    rerender(
      <GrantsPicker
        grants={["api:tasks:admin", "api:gone:read"]}
        config={config}
        onApiChange={onApiChange}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    await user.keyboard("{Escape}");
    expect(
      screen.getByLabelText("api:gone:read (no longer offered)"),
    ).toBeTruthy();
    const line = screen.getByLabelText("api:tasks:admin (dangerous)");
    expect(line).toHaveAttribute("data-grant-kind", "api");
    expect(within(line).getByText("dangerous")).toBeTruthy();
    expect(within(line).getByText("Approve tasks")).toBeTruthy();
    await user.click(
      within(line).getByRole("button", {
        name: "Remove api:tasks:admin (dangerous)",
      }),
    );
    expect(onApiChange).toHaveBeenLastCalledWith(["api:gone:read"]);
  });

  it("grants a method without arguments straight from the menu", async () => {
    serve({ current: [hostScript] });
    const onSubmit = vi.fn().mockResolvedValue();
    const user = userEvent.setup();
    render(
      <GrantsPicker
        grants={[]}
        config={config}
        onApiChange={vi.fn()}
        onSubmit={onSubmit}
        onRemove={vi.fn()}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    await user.click(
      await screen.findByRole("menuitem", { name: /host-script/ }),
    );
    expect(onSubmit).toHaveBeenCalledWith(
      { capability: hostScript.ref, with: undefined },
      null,
    );
  });

  // cloudKey has a required argument with no default (account) and one
  // with a default (ttl): picking it leaves a draft to fill in.
  const cloudKey = {
    ref: "https://example.com/caps/cloud#cloud-key",
    method: "cloud-key",
    args: {
      type: "object",
      required: ["account"],
      properties: {
        account: { type: "string", minLength: 1 },
        ttl: { type: "integer", default: 60, maximum: 720 },
      },
    },
  };
  // viewer's only argument has a default, so it is granted at once.
  const viewer = {
    ref: "https://example.com/caps/cloud#viewer",
    method: "viewer",
    args: {
      type: "object",
      properties: { ttl: { type: "integer", default: 30 } },
    },
  };
  const grantOf = (m, w) => `${m.ref}=${JSON.stringify(w)}`;

  it("grants a method whose defaults pass at once, with no dialog", async () => {
    serve({ current: [viewer] });
    const onSubmit = vi.fn().mockResolvedValue();
    const user = userEvent.setup();
    render(
      <GrantsPicker
        grants={[]}
        config={config}
        onApiChange={vi.fn()}
        onSubmit={onSubmit}
        onRemove={vi.fn()}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    await user.click(await screen.findByRole("menuitem", { name: /viewer/ }));
    expect(onSubmit).toHaveBeenCalledWith(
      { capability: viewer.ref, with: { ttl: 30 } },
      null,
    );
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("keeps a draft inline until its required arguments pass, then grants it", async () => {
    serve({ current: [cloudKey] });
    const onSubmit = vi.fn().mockResolvedValue();
    const user = userEvent.setup();
    render(
      <GrantsPicker
        grants={[]}
        config={config}
        onApiChange={vi.fn()}
        onSubmit={onSubmit}
        onRemove={vi.fn()}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    await user.click(
      await screen.findByRole("menuitem", { name: /cloud-key/ }),
    );
    expect(screen.queryByRole("dialog")).toBeNull();
    const held = screen.getByTestId("grant-grants");
    const draft = held.querySelector("[data-grant-draft]");
    expect(draft).not.toBeNull();
    expect(
      within(draft).getByText("Fill in the arguments marked to grant it"),
    ).toBeTruthy();
    expect(onSubmit).not.toHaveBeenCalled();

    await user.type(within(draft).getByLabelText(/^account/), "dev");
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith(
        { capability: cloudKey.ref, with: { account: "dev", ttl: 60 } },
        null,
      ),
    );
    // One write for the word, not one per keystroke.
    expect(onSubmit).toHaveBeenCalledTimes(1);
  });

  it("discards a draft from its remove button without a write", async () => {
    serve({ current: [cloudKey] });
    const onSubmit = vi.fn().mockResolvedValue();
    const onRemove = vi.fn();
    const user = userEvent.setup();
    render(
      <GrantsPicker
        grants={[]}
        config={config}
        onApiChange={vi.fn()}
        onSubmit={onSubmit}
        onRemove={onRemove}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    await user.click(
      await screen.findByRole("menuitem", { name: /cloud-key/ }),
    );
    await user.click(
      screen.getByRole("button", { name: "Remove cloud-key draft" }),
    );
    expect(document.querySelector("[data-grant-draft]")).toBeNull();
    expect(onSubmit).not.toHaveBeenCalled();
    expect(onRemove).not.toHaveBeenCalled();
  });

  it("saves an edit to a held grant in place, replacing it, and shows a refusal against its field", async () => {
    serve({ current: [cloudKey] });
    const held = grantOf(cloudKey, { account: "dev", ttl: 60 });
    const onSubmit = vi.fn().mockResolvedValue();
    const onRemove = vi.fn().mockResolvedValue();
    const user = userEvent.setup();
    render(
      <GrantsPicker
        grants={[held, grantOf(cloudKey, { account: "qa", ttl: 60 })]}
        config={config}
        onApiChange={vi.fn()}
        onSubmit={onSubmit}
        onRemove={onRemove}
      />,
    );
    const section = await waitFor(() => {
      const el = document.querySelector(`[data-grant='${held}']`);
      expect(el?.querySelector("input")).toBeTruthy();
      return el;
    });
    // Every section's inputs have ids of their own.
    const ids = [...document.querySelectorAll(".grants-held input[id]")].map(
      (el) => el.id,
    );
    expect(new Set(ids).size).toBe(ids.length);
    // "Add another" is on the method's last section only.
    expect(
      screen.getAllByRole("button", { name: "Add another cloud-key" }),
    ).toHaveLength(1);

    const ttl = within(section).getByLabelText(/^ttl/);
    await user.clear(ttl);
    await user.type(ttl, "1000");
    expect(within(section).getByText(/at most 720/)).toBeTruthy();
    await new Promise((r) => setTimeout(r, 700));
    expect(onSubmit).not.toHaveBeenCalled();

    await user.clear(ttl);
    await user.type(ttl, "90");
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith(
        { capability: cloudKey.ref, with: { account: "dev", ttl: 90 } },
        held,
      ),
    );
    expect(onSubmit).toHaveBeenCalledTimes(1);

    const refusal = new Error("account: not allowed");
    refusal.fields = { account: "not allowed here" };
    onSubmit.mockRejectedValueOnce(refusal);
    const account = within(section).getByLabelText(/^account/);
    await user.type(account, "x");
    expect(await within(section).findByText("not allowed here")).toBeTruthy();

    await user.click(within(section).getByRole("button", { name: /^Remove / }));
    expect(onRemove).toHaveBeenCalledWith(held);
  });

  it("draws a grant without arguments on one line, flagged when its server is gone", async () => {
    serve({ current: [hostScript] });
    render(
      <GrantsPicker
        grants={[gcpKey.ref, hostScript.ref]}
        config={config}
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    const gone = await screen.findByLabelText(
      "github.com › gcp-key (server not attached)",
    );
    expect(gone).toHaveAttribute("data-grant-kind", "method");
    expect(within(gone).getByText("server not attached")).toBeTruthy();
    expect(gone.querySelector("input")).toBeNull();
  });

  it("shows the capability policy verdict for the picker's run kind", async () => {
    serve({
      current: [
        { ...hostScript, policy: { task: "denied", ci: "may_be_refused" } },
      ],
    });
    const user = userEvent.setup();
    render(
      <GrantsPicker
        grants={[hostScript.ref]}
        config={config}
        runKind="ci"
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    const heldLine = await screen.findByLabelText(
      "example.com › host-script (may be refused by the capability policy for CI jobs)",
    );
    expect(within(heldLine).getByText("may be refused")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    const item = await screen.findByRole("menuitem", { name: /host-script/ });
    expect(item).toHaveTextContent(
      "may be refused by the capability policy for CI jobs",
    );
    expect(
      within(item).getByRole("link", { name: "Settings → Capabilities" }),
    ).toHaveAttribute("href", "/settings/capabilities#capability-policy");
  });

  it("shows method and argument descriptions in (i) popovers rather than inline", async () => {
    const describedGcpKey = {
      ...gcpKey,
      description: "Mint a short-lived GCP key.",
    };
    const describedCloudKey = {
      ...cloudKey,
      description: "Mint a cloud account key.",
    };
    serve({ current: [describedGcpKey, describedCloudKey] });
    const user = userEvent.setup();
    render(
      <GrantsPicker
        grants={[
          describedGcpKey.ref,
          `${describedCloudKey.ref}={"account":"dev"}`,
        ]}
        config={config}
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    const line = await screen.findByLabelText("github.com › gcp-key");
    expect(line).not.toHaveTextContent(describedGcpKey.description);
    await user.hover(
      within(line).getByRole("button", { name: "About github.com › gcp-key" }),
    );
    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      describedGcpKey.description,
    );
    await user.unhover(
      within(line).getByRole("button", { name: "About github.com › gcp-key" }),
    );

    const section = await screen.findByLabelText(
      "example.com › cloud-key · account: dev",
    );
    expect(section).not.toHaveTextContent(describedCloudKey.description);
    await user.hover(
      within(section).getByRole("button", {
        name: "About cloud-key",
      }),
    );
    await waitFor(() =>
      expect(screen.getByRole("tooltip")).toHaveTextContent(
        describedCloudKey.description,
      ),
    );
  });
});
