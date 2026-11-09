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

// GrantsPicker's repository grants (RepoGrants.jsx, unified-grants
// slice 5): the target line, repo#read and repo#write lines, declared
// push: refs, and "Add grant…"'s Repository access rows.

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import GrantsPicker from "./GrantsPicker.jsx";
import { resetCapabilityMethodsForTest } from "./capabilityMethods.js";
import { repoGrantBox } from "../repoGrantsTesting.js";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const REPO = "https://grain.dev/capabilities/repo";
const listing = [
  {
    ref: `${REPO}#read`,
    method: "read",
    args: null,
    policy: { task: "ci_only" },
  },
  { ref: `${REPO}#write`, method: "write", args: null },
];

function picker(repos, extra = {}) {
  return (
    <GrantsPicker
      grants={[]}
      config={{ capabilityMethods: listing.length }}
      onApiChange={vi.fn()}
      onSubmit={vi.fn()}
      onRemove={vi.fn()}
      repos={{
        target: "acme/widgets",
        base: "main",
        reads: [],
        writes: [],
        readOptions: ["acme/widgets", "acme/shared", "acme/docs"],
        writeOptions: ["acme/widgets", "acme/docs"],
        onReadsChange: vi.fn(),
        onWritesChange: vi.fn(),
        ...repos,
      }}
      {...extra}
    />
  );
}

describe("GrantsPicker repository grants", () => {
  beforeEach(() => {
    resetCapabilityMethodsForTest();
    api.mockImplementation(async (path) => {
      if (path.split("?")[0] === "/api/capability-methods") return listing;
      if (path === "/api/capability-grants/check") return [];
      throw new Error(`unexpected ${path}`);
    });
  });
  afterEach(() => api.mockReset());

  it("shows the target as a line that cannot be removed", () => {
    render(picker());
    const line = screen.getByRole("group", {
      name: "acme/widgets · main · target",
    });
    expect(line).toHaveAttribute("data-grant", `${REPO}#target`);
    expect(within(line).queryByRole("button")).not.toBeInTheDocument();
  });

  it("names a default branch when the target has no base", () => {
    render(picker({ base: "" }));
    expect(
      screen.getByRole("group", {
        name: "acme/widgets · default branch · target",
      }),
    ).toBeInTheDocument();
  });

  it("draws held reads and writes inline, with their normalized grants", () => {
    render(
      picker({
        reads: ["acme/shared"],
        writes: [{ repo: "acme/docs", base: "publish" }],
      }),
    );
    expect(
      screen.getByRole("group", { name: "acme/shared · read" }),
    ).toHaveAttribute("data-grant", `${REPO}#read={"repo":"acme/shared"}`);
    expect(
      screen.getByRole("group", { name: "acme/docs · publish · write" }),
    ).toHaveAttribute(
      "data-grant",
      `${REPO}#write={"base":"publish","repo":"acme/docs"}`,
    );
  });

  it("removes a read and a write", async () => {
    const onReadsChange = vi.fn();
    const onWritesChange = vi.fn();
    const onWritesCommit = vi.fn();
    const user = userEvent.setup();
    render(
      picker({
        reads: ["acme/shared", "acme/docs"],
        writes: [{ repo: "acme/docs", base: "" }],
        onReadsChange,
        onWritesChange,
        onWritesCommit,
      }),
    );
    await user.click(
      screen.getByRole("button", { name: "Remove acme/shared · read" }),
    );
    expect(onReadsChange).toHaveBeenCalledWith(["acme/docs"]);
    await user.click(
      screen.getByRole("button", {
        name: "Remove acme/docs · default branch · write",
      }),
    );
    expect(onWritesChange).toHaveBeenCalledWith([]);
    expect(onWritesCommit).toHaveBeenCalledWith([]);
  });

  // The listed repo#read is a CI job's grant: a task's reads are offered
  // as "Read a repository" instead, so the menu does not show it twice.
  it("offers repository access in Add grant…, not the CI-only method", async () => {
    const user = userEvent.setup();
    render(picker());
    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    expect(
      await screen.findByRole("separator", { name: "Repository access" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("menuitem", { name: /Read a repository/ }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("menuitem", { name: /Write a repository/ }),
    ).toBeInTheDocument();
    expect(document.querySelector(`[data-method="${REPO}#read"]`)).toBeNull();
  });

  it("adds a read, without offering the target or what is held", async () => {
    const onReadsChange = vi.fn();
    const user = userEvent.setup();
    render(picker({ reads: ["acme/docs"], onReadsChange }));
    await user.click(await repoGrantBox(user, "read"));
    expect(
      await screen.findByRole("menuitem", { name: "acme/shared" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("menuitem", { name: "acme/widgets" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("menuitem", { name: "acme/docs" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("menuitem", { name: "acme/shared" }));
    expect(onReadsChange).toHaveBeenCalledWith(["acme/docs", "acme/shared"]);
    expect(
      screen.queryByRole("textbox", { name: "Repo to read" }),
    ).not.toBeInTheDocument();
  });

  it("adds a write on the repo's default branch", async () => {
    const onWritesChange = vi.fn();
    const onWritesCommit = vi.fn();
    const user = userEvent.setup();
    render(picker({ onWritesChange, onWritesCommit }));
    await user.click(await repoGrantBox(user, "write"));
    await user.click(
      await screen.findByRole("menuitem", { name: "acme/docs" }),
    );
    const next = [{ repo: "acme/docs", base: "" }];
    expect(onWritesChange).toHaveBeenCalledWith(next);
    expect(onWritesCommit).toHaveBeenCalledWith(next);
  });

  it("drops a repo picker that is cancelled", async () => {
    const user = userEvent.setup();
    render(picker());
    await repoGrantBox(user, "read");
    await user.click(
      screen.getByRole("button", { name: "Remove repo to read" }),
    );
    expect(
      screen.queryByRole("textbox", { name: "Repo to read" }),
    ).not.toBeInTheDocument();
  });

  it("shows declared push refs read-only", () => {
    render(picker({ push: ["acme/widgets refs/tags/v*"] }));
    const line = screen.getByRole("group", {
      name: "acme/widgets refs/tags/v* · push (declared by the template)",
    });
    expect(within(line).queryByRole("button")).not.toBeInTheDocument();
  });

  it("leaves out the rows for a list the form does not edit", async () => {
    const user = userEvent.setup();
    render(picker({ reads: undefined, writes: undefined }));
    await user.click(screen.getByRole("button", { name: "Add grant…" }));
    await screen.findByRole("menu");
    expect(
      screen.queryByRole("menuitem", { name: /a repository/ }),
    ).not.toBeInTheDocument();
  });

  it("offers no remove on a disabled form", () => {
    render(
      picker(
        { reads: ["acme/shared"], writes: [{ repo: "acme/docs", base: "" }] },
        { disabled: true },
      ),
    );
    expect(
      screen.queryByRole("button", { name: /^Remove/ }),
    ).not.toBeInTheDocument();
  });
});
