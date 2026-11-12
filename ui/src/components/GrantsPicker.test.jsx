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
import Overlay from "./Overlay.jsx";
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
    if (path.split("?")[0] === "/api/capability-methods")
      return listing.current;
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

  it("adds a canonical api grant, edits its with: {repo} limit inline, and still removes legacy api: lines", async () => {
    serve({ current: [] });
    const onApiChange = vi.fn();
    const onSubmit = vi.fn().mockResolvedValue();
    const onRemove = vi.fn().mockResolvedValue();
    const user = userEvent.setup();
    const { rerender } = render(
      <GrantsPicker
        grants={["gcp-key", "api:tasks:admin"]}
        config={config}
        onApiChange={onApiChange}
        onSubmit={onSubmit}
        onRemove={onRemove}
      />,
    );
    // Held grants are one list of sections, bare capability ids included.
    const held = screen.getByTestId("grant-grants");
    expect(within(held).getByLabelText("gcp-key")).toHaveAttribute(
      "data-grant-kind",
      "capability",
    );
    expect(
      within(held).getByLabelText("api#tasks:admin (dangerous)"),
    ).toBeTruthy();

    // Picking ci:read writes the canonical .../api#ci:read reference, and the
    // legacy "Grain API grants apply to" dropdown is gone.
    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    expect(screen.queryByLabelText("Grain API grants apply to")).toBeNull();
    await user.click(screen.getByRole("menuitemcheckbox", { name: "ci:read" }));
    expect(onSubmit).toHaveBeenLastCalledWith(
      {
        capability: "https://grain.dev/capabilities/api#ci:read",
        with: undefined,
      },
      null,
    );

    // Rerender with the canonical grant recorded, then fill in with: {repo}
    // inline on its section and blur to save.
    rerender(
      <GrantsPicker
        grants={[
          "https://grain.dev/capabilities/api#ci:read",
          "https://grain.dev/capabilities/api#tasks:admin",
          "https://grain.dev/capabilities/api#gone:read",
          "api:tasks:admin",
          "api:gone:read",
        ]}
        config={config}
        onApiChange={onApiChange}
        onSubmit={onSubmit}
        onRemove={onRemove}
      />,
    );
    await user.keyboard("{Escape}");

    const ciSection = screen.getByLabelText("api#ci:read");
    const repoInput = within(ciSection).getByLabelText(/^repo/);
    await user.type(repoInput, "acme/widgets");
    await user.tab();
    await waitFor(() =>
      expect(onSubmit).toHaveBeenLastCalledWith(
        {
          capability: "https://grain.dev/capabilities/api#ci:read",
          with: { repo: "acme/widgets" },
        },
        "https://grain.dev/capabilities/api#ci:read",
      ),
    );

    // Canonical dangerous and no-longer-offered sections carry their alerts,
    // and removing a canonical grant calls onRemove.
    const goneItems = screen.getAllByLabelText(
      "api#gone:read (no longer offered)",
    );
    expect(goneItems).toHaveLength(2);
    const canonicalDangerous = screen
      .getAllByLabelText("api#tasks:admin (dangerous)")
      .find((el) => el.getAttribute("data-grant-kind") === "method");
    expect(canonicalDangerous).toBeTruthy();
    await user.click(
      within(canonicalDangerous).getByRole("button", {
        name: "Remove api#tasks:admin (dangerous)",
      }),
    );
    expect(onRemove).toHaveBeenCalledWith(
      "https://grain.dev/capabilities/api#tasks:admin",
    );

    // Unmigrated legacy api: grants still render and remove via onApiChange.
    const legacyLine = screen
      .getAllByLabelText("api#tasks:admin (dangerous)")
      .find((el) => el.getAttribute("data-grant-kind") === "api");
    expect(legacyLine).toBeTruthy();
    expect(within(legacyLine).getByText("dangerous")).toBeTruthy();
    expect(within(legacyLine).getByText("Approve tasks")).toBeTruthy();
    await user.click(
      within(legacyLine).getByRole("button", {
        name: "Remove api#tasks:admin (dangerous)",
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

  // settings/new-task-form.yml's capabilities.hidden narrows the "Add
  // grant…" menu only. A grant the draft already holds is drawn as its own
  // line, outside the menu, so it stays removable while hidden. (The
  // legacy checklists needed useHeldIds for this; GrantsPicker does not.)
  it("keeps a held grant removable when the form hides it from the menu", async () => {
    serve({ current: [hostScript, gcpKey] });
    const onRemove = vi.fn().mockResolvedValue();
    const user = userEvent.setup();
    render(
      <GrantsPicker
        grants={[hostScript.ref]}
        config={config}
        formCapabilities={{
          offered: null,
          hidden: [hostScript.ref],
          display: {},
        }}
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={onRemove}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    expect(
      await screen.findByRole("menuitem", { name: /gcp-key/ }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /host-script/ })).toBeNull();
    await user.keyboard("{Escape}");
    await user.click(
      await screen.findByRole("button", { name: /^Remove .*host-script/ }),
    );
    expect(onRemove).toHaveBeenCalledWith(hostScript.ref);
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

  it("removes a held grant with an edit not saved yet without writing the edit back", async () => {
    serve({ current: [cloudKey] });
    const held = grantOf(cloudKey, { account: "dev", ttl: 60 });
    const onSubmit = vi.fn().mockResolvedValue();
    const onRemove = vi.fn().mockResolvedValue();
    const user = userEvent.setup();
    const { rerender } = render(
      <GrantsPicker
        grants={[held]}
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
    await user.type(within(section).getByLabelText(/^account/), "x");
    await user.click(within(section).getByRole("button", { name: /^Remove / }));
    expect(onRemove).toHaveBeenCalledWith(held);
    rerender(
      <GrantsPicker
        grants={[]}
        config={config}
        onApiChange={vi.fn()}
        onSubmit={onSubmit}
        onRemove={onRemove}
      />,
    );
    await new Promise((r) => setTimeout(r, 700));
    expect(onSubmit).not.toHaveBeenCalled();
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

  // grain/task-1810: a method the capability policy denies the form's
  // run is listed disabled with the policy's reason, not offered; a
  // held grant of one is flagged.
  it("shows a method the policy denies disabled, with the rule as reason", async () => {
    const reason = `the capability policy denies ${hostScript.ref} to this task on acme/widgets (rule 1: deny ${hostScript.ref})`;
    api.mockImplementation(async (path, opts) => {
      if (path === "/api/capability-methods?kind=task&repo=acme%2Fwidgets")
        return [{ ...hostScript, policyRefusal: reason }, gcpKey];
      if (path === "/api/capability-grants/check")
        return JSON.parse(opts.body).grants.map(({ grant }) => ({
          grant,
          recorded: grant,
        }));
      throw new Error(`unexpected ${path}`);
    });
    const onSubmit = vi.fn();
    const user = userEvent.setup();
    render(
      <GrantsPicker
        grants={[hostScript.ref]}
        config={{ capabilityMethods: 2 }}
        onApiChange={vi.fn()}
        onSubmit={onSubmit}
        onRemove={vi.fn()}
        policyRepo="acme/widgets"
      />,
    );
    expect(
      await screen.findByLabelText(
        "example.com › host-script (denied by the capability policy)",
      ),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    const menu = await screen.findByRole("menu", { name: "Grants to add" });
    const denied = await within(menu).findByText("host-script");
    const row = denied.closest('[role="menuitem"]');
    expect(row).toHaveAttribute("aria-disabled", "true");
    expect(row).toHaveAttribute("data-policy-denied", "true");
    expect(row).toHaveTextContent(reason);
    expect(
      within(row).getByRole("link", { name: "Settings → Capabilities" }),
    ).toHaveAttribute("href", "/settings/capabilities#capability-policy");
    const allowed = within(menu)
      .getByText("gcp-key")
      .closest('[role="menuitem"]');
    expect(allowed).not.toHaveAttribute("aria-disabled");
    await user.click(allowed);
    expect(onSubmit).toHaveBeenCalledWith(
      { capability: gcpKey.ref, with: undefined },
      null,
    );
  });

  // grain/task-2726: repo#read is refused every task and process by
  // grain's own CI-only rule, not by the capability policy, so the
  // picker says CI only and points at the repository's CI capabilities
  // rather than at a policy with no rule to change.
  it("words a CI-only method as CI only, not as denied by the policy", async () => {
    const repoRead = {
      ref: "https://grain.dev/capabilities/repo#read",
      method: "read",
      args: null,
      policy: { task: "ci_only", process: "ci_only" },
    };
    const refusal = `${repoRead.ref} is a CI capability, not a task grant: it gives a CI job read access to another repository, and only a CI job is handed it -- a task run is not one.`;
    api.mockImplementation(async (path, opts) => {
      if (path === "/api/capability-methods?kind=task&repo=acme%2Fwidgets")
        return [{ ...repoRead, policyRefusal: refusal }, gcpKey];
      if (path.startsWith("/api/capability-methods")) return [repoRead];
      if (path === "/api/capability-grants/check")
        return JSON.parse(opts.body).grants.map(({ grant }) => ({
          grant,
          recorded: grant,
        }));
      throw new Error(`unexpected ${path}`);
    });
    const user = userEvent.setup();
    const { unmount } = render(
      <GrantsPicker
        grants={[]}
        config={{ capabilityMethods: 1 }}
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
        runKind="process"
      />,
    );
    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    let menu = await screen.findByRole("menu", { name: "Grants to add" });
    let row = (await within(menu).findByText("repo server › read")).closest(
      '[role="menuitem"]',
    );
    expect(row).toHaveAttribute("aria-disabled", "true");
    expect(row).toHaveAttribute("data-policy-verdict", "ci_only");
    expect(row).not.toHaveAttribute("data-policy-denied");
    expect(row).toHaveTextContent(
      "CI only: not a grant for processes; a CI job declares it in its workflow's capabilities:",
    );
    expect(row).not.toHaveTextContent("capability policy");
    // The repository's CI capabilities list does not grant repository
    // access (docs/test-runs/2026-11-03-capabilities-ci-repo.md, F1), so
    // the row points at how a job declares it rather than at that list.
    expect(row).not.toHaveTextContent("CI capabilities");
    expect(
      within(row).getByRole("link", { name: "Repository access docs" }),
    ).toHaveAttribute(
      "href",
      expect.stringContaining("docs/ci-capabilities.md#repository-access"),
    );
    await user.keyboard("{Escape}");
    unmount();

    // A task on a known repo holding one: the line and the menu name the
    // repo's own CI capabilities, and the refusal is the CI-only one.
    render(
      <GrantsPicker
        grants={[repoRead.ref]}
        config={{ capabilityMethods: 2 }}
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
        policyRepo="acme/widgets"
      />,
    );
    const [line] = await screen.findAllByLabelText(/read \(CI only\)$/);
    expect(line).not.toHaveTextContent("denied by policy");
    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    menu = await screen.findByRole("menu", { name: "Grants to add" });
    row = (await within(menu).findByText("repo server › read")).closest(
      '[role="menuitem"]',
    );
    expect(row).toHaveAttribute("aria-disabled", "true");
    expect(row).toHaveAttribute("title", `${repoRead.ref} — ${refusal}`);
    expect(row).not.toHaveTextContent("capability policy");
    expect(
      within(row).getByRole("link", { name: "Repository access docs" }),
    ).toHaveAttribute(
      "href",
      expect.stringContaining("docs/ci-capabilities.md#repository-access"),
    );
  });

  // A task's one run on a known repo is judged by the listing: a method
  // a repo-conditional rule admits there is not "may be refused" because
  // some other repo's task would be. A CI job's event is still unknown,
  // so its picker keeps the kind's verdict.
  it("drops the kind's verdict once the listing judged the task's repo", async () => {
    const mayBe = {
      ...hostScript,
      policy: { task: "may_be_refused", ci: "may_be_refused" },
    };
    api.mockImplementation(async (path, opts) => {
      if (path.startsWith("/api/capability-methods?kind=")) return [mayBe];
      if (path === "/api/capability-grants/check")
        return JSON.parse(opts.body).grants.map(({ grant }) => ({
          grant,
          recorded: grant,
        }));
      throw new Error(`unexpected ${path}`);
    });
    const user = userEvent.setup();
    const { unmount } = render(
      <GrantsPicker
        grants={[hostScript.ref]}
        config={{ capabilityMethods: 1 }}
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
        policyRepo="acme/gadgets"
      />,
    );
    const line = await screen.findByLabelText("example.com › host-script");
    expect(line).not.toHaveTextContent("may be refused");
    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    const item = await screen.findByRole("menuitem", { name: /host-script/ });
    expect(item).not.toHaveTextContent("may be refused");
    expect(item).not.toHaveAttribute("data-policy-verdict");
    unmount();

    render(
      <GrantsPicker
        grants={[]}
        config={{ capabilityMethods: 1 }}
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
        runKind="ci"
        policyRepo="acme/gadgets"
      />,
    );
    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    expect(
      await screen.findByRole("menuitem", { name: /host-script/ }),
    ).toHaveTextContent("may be refused by the capability policy for CI jobs");
    expect(api).toHaveBeenCalledWith(
      "/api/capability-methods?kind=ci&repo=acme%2Fgadgets",
    );
  });

  it("asks about no repo while the one typed is not owner/name", async () => {
    serve({ current: [hostScript] });
    render(
      <GrantsPicker
        grants={[]}
        config={{ capabilityMethods: 1 }}
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
        policyRepo="not a repo"
      />,
    );
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/capability-methods"),
    );
    expect(screen.queryByRole("alert")).toBeNull();
  });

  // grain/task-3527: Escape inside an open "Add grant…" popover must close
  // only the popover and return focus to "Add grant…", without asking
  // DISCARD_PROMPT or closing the surrounding template/schedule Overlay pane
  // until a second Escape is pressed.
  it("closes only the Add grant… popover on Escape inside an Overlay pane, then lets the next Escape close the pane", async () => {
    const denied = {
      ...hostScript,
      policyRefusal: "denied by capability policy rule 1",
    };
    serve({ current: [denied, gcpKey] });
    const onClose = vi.fn();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const user = userEvent.setup();
    let dirty = false;

    render(
      <Overlay
        label="Edit template"
        onClose={onClose}
        pane
        animate={false}
        isDirty={() => dirty}
      >
        <GrantsPicker
          grants={[]}
          config={{
            capabilityMethods: 2,
            capabilities: [
              {
                id: "api:tasks:read",
                area: "tasks",
                summary: "Read tasks",
              },
            ],
            targetRepos: ["acme/widgets"],
          }}
          onApiChange={vi.fn()}
          onSubmit={vi.fn()}
          onRemove={vi.fn()}
          runKind="process"
        />
      </Overlay>,
    );

    // 1. Clean pane: first Escape closes only the popover and focuses Add grant…;
    // second Escape closes the pane.
    const addBtn = await screen.findByRole("button", { name: "Add grant…" });
    await user.click(addBtn);
    expect(
      await screen.findByRole("menu", { name: "Grants to add" }),
    ).toBeInTheDocument();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu", { name: "Grants to add" })).toBeNull();
    expect(confirm).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(addBtn).toHaveFocus();

    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);
    onClose.mockClear();

    // 2. Dirty pane: first Escape closes Add grant… without prompting,
    // second Escape asks DISCARD_PROMPT.
    dirty = true;
    await user.click(addBtn);
    expect(
      await screen.findByRole("menu", { name: "Grants to add" }),
    ).toBeInTheDocument();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu", { name: "Grants to add" })).toBeNull();
    expect(confirm).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(addBtn).toHaveFocus();

    await user.keyboard("{Escape}");
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(onClose).not.toHaveBeenCalled();
    confirm.mockClear();

    // 3. Focus on the policy link inside a disabled MenuItem: Escape still
    // closes the popover and restores focus to Add grant… without prompting.
    await user.click(addBtn);
    const menu = await screen.findByRole("menu", { name: "Grants to add" });
    const policyLink = within(menu).getByRole("link", {
      name: "Settings → Capabilities",
    });
    policyLink.focus();
    expect(policyLink).toHaveFocus();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu", { name: "Grants to add" })).toBeNull();
    expect(confirm).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(addBtn).toHaveFocus();

    confirm.mockRestore();
  });

  it("leaves https://grain.dev/capabilities/api methods out of the generic method menu and edits held api references as their own sections", async () => {
    const apiMethod = {
      ref: "https://grain.dev/capabilities/api#ci:read",
      capability: "https://grain.dev/capabilities/api",
      method: "ci:read",
      description: "Read CI",
      args: {
        type: "object",
        properties: { repo: { type: "string" } },
      },
    };
    serve({ current: [apiMethod, hostScript] });
    const onApiChange = vi.fn();
    const onSubmit = vi.fn();
    const onRemove = vi.fn();
    const user = userEvent.setup();
    render(
      <GrantsPicker
        grants={[
          "https://grain.dev/capabilities/api#tasks:admin",
          'https://grain.dev/capabilities/api#ci:read={"repo":"acme/widgets"}',
        ]}
        config={config}
        onApiChange={onApiChange}
        onSubmit={onSubmit}
        onRemove={onRemove}
      />,
    );

    expect(
      screen.getByLabelText("api#tasks:admin (dangerous)"),
    ).toBeInTheDocument();
    // Slice 3: a held api reference is its own section, its repository
    // limit the {repo} argument edited inline rather than an @repo suffix.
    const ciSection = screen.getByLabelText("api#ci:read · repo: acme/widgets");
    expect(ciSection).toHaveAttribute("data-grant-kind", "method");
    expect(within(ciSection).getByLabelText(/^repo/)).toHaveValue(
      "acme/widgets",
    );
    // Held api references are not drawn a second time under a generic
    // method label.
    expect(screen.queryByLabelText(/grain\.dev ›/)).toBeNull();

    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    const menu = await screen.findByRole("menu", { name: "Grants to add" });
    await within(menu).findByText("host-script");
    expect(
      menu.querySelector(
        '[data-method="https://grain.dev/capabilities/api#ci:read"]',
      ),
    ).toBeNull();

    const adminItem = within(menu).getByRole("menuitemcheckbox", {
      name: "tasks:admin",
    });
    expect(adminItem).toHaveAttribute("aria-checked", "true");
    await user.click(adminItem);
    expect(onRemove).toHaveBeenLastCalledWith(
      "https://grain.dev/capabilities/api#tasks:admin",
    );
    expect(onApiChange).not.toHaveBeenCalled();
  });

  // docs/test-plans/2026-11-09.md §1 item 3: when GET
  // /api/capability-methods lists nothing but grain's own api
  // references, they are not a generic method group, so the menu says
  // there is nothing to grant -- and a picker with nothing to offer and
  // nothing held is not drawn at all.
  it("says there is nothing to grant when api references are the only methods", async () => {
    const apiOnly = [
      "https://grain.dev/capabilities/api#tasks:read",
      "https://grain.dev/capabilities/api#ci-read",
    ].map((ref) => ({
      ref,
      capability: "https://grain.dev/capabilities/api",
      method: ref.split("#")[1],
      args: null,
    }));
    serve({ current: apiOnly });
    const user = userEvent.setup();
    const { container, rerender } = render(
      <GrantsPicker
        grants={["https://grain.dev/capabilities/api#tasks:read"]}
        config={{ capabilityMethods: 2 }}
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    await user.click(await screen.findByRole("button", { name: "Add grant…" }));
    const menu = await screen.findByRole("menu", { name: "Grants to add" });
    expect(within(menu).getByText("Nothing to grant")).toBeInTheDocument();
    expect(menu.querySelectorAll(".method-grant-row")).toHaveLength(0);
    await user.keyboard("{Escape}");

    rerender(
      <GrantsPicker
        grants={[]}
        config={{ capabilityMethods: 2 }}
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    await waitFor(() =>
      expect(container.querySelector('[role="group"]')).toBeNull(),
    );
  });

  it("renders a held bare capability id as a read-only GrantLine naming its retired alias and removes it", async () => {
    api.mockImplementation(async (path, opts) => {
      if (path.split("?")[0] === "/api/capability-methods") return [];
      if (path === "/api/capability-grants/check") {
        return JSON.parse(opts.body).grants.map(({ grant }) => {
          if (grant === "self-debug") {
            return {
              grant,
              aliasOf: [
                "api:self-debug",
                "https://grain.dev/capabilities/grain#source={}",
              ],
            };
          }
          if (grant === "read-issues") {
            return {
              grant,
              recorded: "https://grain.dev/capabilities/github#read-issues={}",
              aliasOf: ["https://grain.dev/capabilities/github#read-issues={}"],
            };
          }
          return { grant, error: `capability "${grant}" is not offered` };
        });
      }
      throw new Error(`unexpected ${path}`);
    });
    const onRemove = vi.fn().mockResolvedValue();
    const user = userEvent.setup();
    const { rerender } = render(
      <GrantsPicker
        grants={["self-debug", "read-issues", "gone-bare"]}
        config={{}}
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={onRemove}
      />,
    );

    const selfDebug = await screen.findByLabelText(
      "self-debug (retired alias)",
    );
    expect(selfDebug).toHaveAttribute("data-grant", "self-debug");
    expect(selfDebug).toHaveAttribute("data-grant-kind", "capability");
    expect(selfDebug.querySelector("input")).toBeNull();
    expect(
      within(selfDebug).getByText(
        "Retired alias of api#self-debug + grain#source",
      ),
    ).toBeInTheDocument();
    expect(within(selfDebug).getByText("retired alias")).toBeInTheDocument();

    const readIssues = screen.getByLabelText("read-issues (retired alias)");
    expect(
      within(readIssues).getByText("Retired alias of github#read-issues"),
    ).toBeInTheDocument();

    const gone = screen.getByLabelText("gone-bare (no longer offered)");
    expect(within(gone).getByText("No longer offered")).toBeInTheDocument();

    await user.click(
      within(selfDebug).getByRole("button", {
        name: "Remove self-debug (retired alias)",
      }),
    );
    expect(onRemove).toHaveBeenCalledWith("self-debug");

    rerender(
      <GrantsPicker
        grants={["self-debug"]}
        config={{}}
        disabled
        onApiChange={vi.fn()}
        onSubmit={vi.fn()}
        onRemove={onRemove}
      />,
    );
    const readOnlyLine = await screen.findByLabelText(
      "self-debug (retired alias)",
    );
    expect(
      within(readOnlyLine).queryByRole("button", { name: /remove/i }),
    ).toBeNull();
  });
});
