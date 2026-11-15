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
import { beforeEach, describe, expect, it, onTestFinished, vi } from "vitest";
import RepositoriesSection, { connectionText } from "./RepositoriesSection.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

const config = { targetRepos: ["acme/widgets", "acme/gadgets"] };

const reach = {
  available: true,
  repos: [
    { repo: "acme/widgets", push: true },
    { repo: "acme/gadgets", push: false },
  ],
  connections: [{ credential: "github-token", host: "github.com", ok: true }],
};

function routeApi() {
  api.mockImplementation((path, opts) => {
    if (path === "/api/deployment/reachable-repos")
      return Promise.resolve(reach);
    if (path === "/api/repos/acme/widgets/declarations")
      return Promise.resolve({
        declarations: true,
        ciMode: "grain",
        skills: {
          enabled: true,
          folders: [".claude/skills", ".gemini/skills"],
        },
        effectiveSkillFolders: [".claude/skills", ".gemini/skills"],
      });
    if (
      opts &&
      path === "/api/repos/acme/widgets/base-repair-bypasses-human-gates"
    )
      return Promise.resolve({
        declarations: true,
        ciMode: "grain",
        baseRepairBypassesHumanGates: JSON.parse(opts.body)
          .baseRepairBypassesHumanGates,
      });
    if (path === "/api/repos/acme/widgets/release")
      return Promise.resolve({ baseBranches: ["main", "candidate"] });
    if (path === "/api/repos/acme/gadgets/declarations")
      return Promise.resolve({ declarations: false, ignoreGitHubCI: false });
    if (path === "/api/repos/acme/gadgets/release")
      return Promise.reject(new Error("boom"));
    return Promise.reject(new Error(`unexpected ${path}`));
  });
}

const row = (repo) =>
  screen
    .getAllByTestId("settings-repo-row")
    .find((r) => r.dataset.repo === repo);

describe("RepositoriesSection", () => {
  beforeEach(() => {
    api.mockReset();
  });

  it("shows each repository's base branch, declarations, CI mode and connection", async () => {
    routeApi();
    render(<RepositoriesSection config={config} tasks={[]} />);
    const widgets = row("acme/widgets");
    expect(
      await within(widgets).findByText("main, candidate"),
    ).toBeInTheDocument();
    expect(
      within(widgets).getByTestId("settings-repo-declarations"),
    ).toHaveTextContent("Read");
    expect(
      within(widgets).getByTestId("settings-repo-skills"),
    ).toHaveTextContent(".claude/skills, .gemini/skills");
    expect(within(widgets).getByTestId("settings-repo-ci")).toHaveTextContent(
      "Grain CI",
    );
    expect(within(widgets).getByTestId("settings-repo-git")).toHaveTextContent(
      "github-token",
    );

    const gadgets = row("acme/gadgets");
    expect(await within(gadgets).findByText("Unknown")).toBeInTheDocument();
    expect(await within(gadgets).findByText("Not read")).toBeInTheDocument();
    expect(
      within(gadgets).getByTestId("settings-repo-skills"),
    ).toHaveTextContent("Default folders");
    expect(within(gadgets).getByTestId("settings-repo-ci")).toHaveTextContent(
      "GitHub CI",
    );
    expect(within(gadgets).getByTestId("settings-repo-git")).toHaveTextContent(
      "github-token (read only)",
    );
  });

  it("links each fact to the tab of the repo's page that changes it", async () => {
    routeApi();
    const onOpenRepo = vi.fn();
    const onSection = vi.fn();
    render(
      <RepositoriesSection
        config={config}
        tasks={[]}
        onOpenRepo={onOpenRepo}
        onSection={onSection}
      />,
    );
    const ci = within(row("acme/widgets")).getByTestId("settings-repo-ci");
    const link = await within(ci).findByRole("link", { name: "Grain CI" });
    expect(link).toHaveAttribute("href", "/repos/acme/widgets/settings/ci");
    expect(
      within(row("acme/widgets")).getByRole("link", { name: "acme/widgets" }),
    ).toHaveAttribute("href", "/repos/acme/widgets");

    // Modified clicks open in a new tab instead of intercepting in-app.
    const gitLink = within(
      within(row("acme/widgets")).getByTestId("settings-repo-git"),
    ).getByRole("link", { name: "github-token" });
    const root = link.closest("body > div");
    const leftToBrowser = [];
    const follow = (e) => {
      leftToBrowser.push(!e.defaultPrevented);
      e.preventDefault();
    };
    root.addEventListener("click", follow);
    fireEvent.click(link, { ctrlKey: true });
    fireEvent.click(gitLink, { metaKey: true });
    root.removeEventListener("click", follow);
    expect(leftToBrowser).toEqual([true, true]);
    expect(onOpenRepo).not.toHaveBeenCalled();
    expect(onSection).not.toHaveBeenCalled();

    const user = userEvent.setup();
    await user.click(link);
    expect(onOpenRepo).toHaveBeenCalledWith("acme/widgets", "ci");
    await user.click(gitLink);
    expect(onSection).toHaveBeenCalledWith("git");
  });

  it("toggles base-branch repair human-gate bypass and explains the risk", async () => {
    routeApi();
    const confirm = vi.fn().mockReturnValue(true);
    vi.stubGlobal("confirm", confirm);
    onTestFinished(() => vi.unstubAllGlobals());
    const user = userEvent.setup();
    render(<RepositoriesSection config={config} tasks={[]} />);
    const box = await screen.findByLabelText(
      "acme/widgets: Base-branch repair PRs may merge without a human when their CI is green",
    );
    await waitFor(() => expect(box).not.toBeDisabled());
    expect(box).not.toBeChecked();
    expect(
      screen.getByText(
        /Risk: while a base branch is red, an agent repairing it can merge changes to workflow declarations/,
      ),
    ).toBeInTheDocument();
    await user.click(box);
    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/base-repair-bypasses-human-gates",
      {
        method: "PUT",
        body: JSON.stringify({ baseRepairBypassesHumanGates: true }),
      },
    );
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(confirm).toHaveBeenCalledWith(
      expect.stringMatching(
        /acme\/widgets.*trust-boundary code with no human sign-off/,
      ),
    );
    await waitFor(() => expect(box).toBeChecked());

    // Turning it back off does not ask.
    await user.click(box);
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/base-repair-bypasses-human-gates",
      {
        method: "PUT",
        body: JSON.stringify({ baseRepairBypassesHumanGates: false }),
      },
    );
    await waitFor(() => expect(box).not.toBeChecked());
  });

  it("leaves the base-branch repair bypass off when the confirmation is declined", async () => {
    routeApi();
    const confirm = vi.fn().mockReturnValue(false);
    vi.stubGlobal("confirm", confirm);
    onTestFinished(() => vi.unstubAllGlobals());
    const user = userEvent.setup();
    render(<RepositoriesSection config={config} tasks={[]} />);
    const box = await screen.findByLabelText(
      "acme/widgets: Base-branch repair PRs may merge without a human when their CI is green",
    );
    await waitFor(() => expect(box).not.toBeDisabled());
    await user.click(box);
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(api).not.toHaveBeenCalledWith(
      "/api/repos/acme/widgets/base-repair-bypasses-human-gates",
      expect.anything(),
    );
    expect(box).not.toBeChecked();
  });

  // 2026-11-07 walkthrough: the switch stayed live where the repo's own
  // Merge queue tab draws it disabled, and the API's refusal was
  // swallowed, so a click simply sprang back with no word why.
  it("disables the bypass where the repo page does: config-repo only, or not the repo's admin", async () => {
    routeApi();
    const { unmount } = render(
      <RepositoriesSection
        config={{ ...config, configRepoOnly: true }}
        tasks={[]}
      />,
    );
    const label =
      "acme/widgets: Base-branch repair PRs may merge without a human when their CI is green";
    expect(
      await within(row("acme/widgets")).findByText("main, candidate"),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(label)).toBeDisabled();
    unmount();

    routeApi();
    render(
      <RepositoriesSection
        config={{ ...config, adminScoped: true, adminRepos: ["acme/gadgets"] }}
        tasks={[]}
      />,
    );
    expect(
      await within(row("acme/widgets")).findByText("main, candidate"),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(label)).toBeDisabled();
    await waitFor(() =>
      expect(
        screen.getByLabelText(
          "acme/gadgets: Base-branch repair PRs may merge without a human when their CI is green",
        ),
      ).not.toBeDisabled(),
    );
  });

  it("says why when the bypass write is refused", async () => {
    routeApi();
    const routed = api.getMockImplementation();
    const refusal = Object.assign(new Error("forbidden"), { status: 403 });
    api.mockImplementation((path, opts) =>
      opts && path.endsWith("/base-repair-bypasses-human-gates")
        ? Promise.reject(refusal)
        : routed(path, opts),
    );
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    onTestFinished(() => vi.unstubAllGlobals());
    const showError = vi.fn();
    const user = userEvent.setup();
    render(
      <RepositoriesSection config={config} tasks={[]} showError={showError} />,
    );
    const box = await screen.findByLabelText(
      "acme/widgets: Base-branch repair PRs may merge without a human when their CI is green",
    );
    await waitFor(() => expect(box).not.toBeDisabled());
    await user.click(box);
    await waitFor(() => expect(showError).toHaveBeenCalledWith(refusal));
    expect(box).not.toBeChecked();
  });

  it("says when no connection lists the repository", () => {
    expect(connectionText(null, "acme/widgets")).toBe("…");
    expect(connectionText(false, "acme/widgets")).toBe("Unknown");
    expect(connectionText({ available: false }, "acme/widgets")).toBe(
      "Not listed",
    );
    expect(connectionText(reach, "acme/other")).toBe("Not reached");
  });

  it("warns when a stored hostless entry matches a repository reachable only on a non-default git host", async () => {
    api.mockImplementation((path) => {
      if (path === "/api/deployment/reachable-repos") {
        return Promise.resolve({
          available: true,
          repos: [
            { repo: "acme/widgets", push: true },
            { repo: "git.forge.example/experimental/grain", push: true },
          ],
          connections: [
            { credential: "github-token", host: "github.com", ok: true },
            { credential: "forge-token", host: "git.forge.example", ok: true },
          ],
        });
      }
      if (path.endsWith("/declarations"))
        return Promise.resolve({ declarations: false });
      if (path.endsWith("/release"))
        return Promise.resolve({ baseBranches: ["main"] });
      return Promise.reject(new Error(`unexpected ${path}`));
    });
    render(
      <RepositoriesSection
        config={{ targetRepos: ["acme/widgets", "experimental/grain"] }}
        tasks={[]}
      />,
    );
    const alert = await screen.findByTestId("repo-host-mismatch-alert");
    expect(alert).toHaveTextContent(
      "experimental/grain: this entry resolves to github.com; did you mean git.forge.example/experimental/grain?",
    );
  });
});
