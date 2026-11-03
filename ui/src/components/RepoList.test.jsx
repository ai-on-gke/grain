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

import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import RepoList from "./RepoList.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const tasks = [
  {
    id: "1",
    title: "Fix the widget",
    repo: "acme/widgets",
    state: "queued",
    blocked: false,
    capabilities: [],
  },
  {
    id: "2",
    title: "Ship the widget",
    repo: "acme/widgets",
    state: "running",
    blocked: false,
    capabilities: [],
  },
  {
    id: "3",
    title: "Recall the widget",
    repo: "acme/widgets",
    state: "queued",
    blocked: true,
    blockedBy: ["1"],
    capabilities: [],
  },
  {
    id: "4",
    title: "Ship the gadget",
    repo: "acme/gadgets",
    state: "completed",
    blocked: false,
    capabilities: [],
  },
  {
    id: "5",
    title: "Untargeted proposal",
    repo: "",
    state: "proposed",
    blocked: false,
    capabilities: [],
  },
];

function renderList(overrides = {}) {
  const props = {
    tasks,
    config: null,
    onOpenRepo: vi.fn(),
    onRefreshConfig: vi.fn(),
    showError: vi.fn(),
    ...overrides,
  };
  render(<RepoList {...props} />);
  return props;
}

describe("RepoList", () => {
  // grain/task-1429: a repo row held nothing focusable at all outside a
  // custom order's drag handle. Its name is a link to the repo's page.
  it("opens a repo from the keyboard through the link its name is", async () => {
    const user = userEvent.setup();
    const { onOpenRepo } = renderList();

    const link = screen.getByRole("link", { name: "acme/widgets" });
    expect(link).toHaveAttribute("href", "/repos/acme/widgets");
    link.focus();
    await user.keyboard("{Enter}");
    expect(onOpenRepo).toHaveBeenCalledTimes(1);
    expect(onOpenRepo).toHaveBeenCalledWith("acme/widgets");
  });

  // The row order is this browser's own now (listOrder.js), so one
  // test's drag must not be the next test's starting order.
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    api.mockReset();
  });

  it("lists one row per repo with per-state counts and a total", () => {
    renderList();

    expect(screen.getByText("acme/widgets")).toBeInTheDocument();
    expect(screen.getByText("acme/gadgets")).toBeInTheDocument();
    expect(screen.getByText("Waiting 2")).toBeInTheDocument();
    expect(screen.getByText("Running 1")).toBeInTheDocument();
    expect(screen.getByText("Blocked 1")).toBeInTheDocument();
    expect(screen.getByText("3 tasks")).toBeInTheDocument();
    expect(screen.getByText("1 task")).toBeInTheDocument();
  });

  // ChipStrip hides a row's last chips first when they do not fit, so a
  // Blocked chip behind the state counts was the first one a busy repo
  // lost (grain/task-2235): it leads the strip instead.
  it("puts the Blocked chip ahead of the state counts", () => {
    renderList();
    const row = screen.getByText("acme/widgets").closest(".repo-list-row");
    const chips = [...row.querySelectorAll(".chips > *")].map(
      (c) => c.textContent,
    );
    expect(chips[0]).toBe("Blocked 1");
    expect(chips).toContain("Waiting 2");
  });

  it("omits tasks with no target repo", () => {
    renderList();
    expect(screen.getAllByRole("listitem")).toHaveLength(2);
  });

  // Two rules at once, since one config exercises both: a targetRepos
  // entry with no tasks yet still gets a row, and acme/gadgets -- which
  // the default tasks target but this allowlist does not name -- gets
  // none, because a restricted deployment lists what it can work in
  // rather than what it once did (state.js's repoRows).
  it("also lists a targetRepos entry that has no tasks yet", () => {
    const config = { targetRepos: ["acme/widgets", "acme/newrepo"] };
    renderList({ config });

    expect(screen.getAllByRole("listitem")).toHaveLength(2);
    expect(screen.getByText("acme/newrepo")).toBeInTheDocument();
    expect(screen.getByText("0 tasks")).toBeInTheDocument();
    expect(screen.queryByText("acme/gadgets")).not.toBeInTheDocument();
  });

  // A repo that carries default capabilities of its own is listed even
  // when nothing else here mentions it: PUT /api/repos/{owner}/{name}/
  // capabilities never required an allowlist entry, and its own page is
  // the only place that set can be edited.
  it("also lists a repo that only carries default capabilities of its own", () => {
    const config = {
      targetRepos: [],
      repoDefaultCapabilities: { "acme/orphan": ["gcp-key"] },
      capabilities: [{ id: "gcp-key", name: "GCP key" }],
    };
    renderList({ config });

    const row = screen.getByText("acme/orphan").closest("li");
    expect(within(row).getByText("Defaults only")).toBeInTheDocument();
  });

  // The same argument for the other kind of per-repo configuration
  // (grain/task-114): a repo whose only presence here is standing
  // instructions of its own, which reach every run against it and can
  // only be read or edited from its page.
  it("also lists a repo known only for its own prompt extension", () => {
    const config = {
      targetRepos: [],
      reposWithPromptExtension: ["acme/prompt-only"],
    };
    renderList({ config });

    const row = screen.getByText("acme/prompt-only").closest("li");
    expect(within(row).getByText("Defaults only")).toBeInTheDocument();
  });

  it("does not mark a repo that carries defaults and has tasks of its own as defaults-only", () => {
    const config = {
      targetRepos: [],
      repoDefaultCapabilities: { "acme/gadgets": ["gcp-key"] },
    };
    renderList({ config });

    const row = screen.getByText("acme/gadgets").closest("li");
    expect(within(row).queryByText("Defaults only")).not.toBeInTheDocument();
  });

  // grain/task-111: a row is a name and its counts. Everything that used
  // to sit on one -- New branch, Capabilities, Releases, Remove, "+",
  // and the chevron that folded the repo's tasks out in place -- is on
  // the repo's own page now.
  it("carries no per-row buttons at all", () => {
    const config = { targetRepos: ["acme/widgets"] };
    renderList({ config });

    const row = screen.getByText("acme/widgets").closest("li");
    // The drag handle is the one button a row does carry, and only
    // because it became one in grain/task-23 -- tapping it picks the row
    // up, which is how this list is reordered without a drag. It is an
    // affordance for moving the row, not an action on the repo, so it is
    // named here rather than counted as one of the buttons this test is
    // about.
    expect(within(row).queryAllByRole("button")).toHaveLength(1);
    expect(
      within(row).getByRole("button", { name: "Move acme/widgets" }),
    ).toBeInTheDocument();
    expect(screen.queryByText("Fix the widget")).not.toBeInTheDocument();
  });

  it("filters the list by repo name", async () => {
    const user = userEvent.setup();
    renderList();

    await user.type(
      screen.getByPlaceholderText("Filter added repos…"),
      "gadgets",
    );

    expect(screen.getByText("acme/gadgets")).toBeInTheDocument();
    expect(screen.queryByText("acme/widgets")).not.toBeInTheDocument();
  });

  // The list pages have no toolbar Clear of their own, so the × in the
  // box is the only thing that puts the whole list back.
  it("puts the whole list back from the search box's own clear button", async () => {
    const user = userEvent.setup();
    renderList();

    await user.type(
      screen.getByPlaceholderText("Filter added repos…"),
      "gadgets",
    );
    await user.click(screen.getByTitle("Clear the search box"));

    expect(screen.getByText("acme/gadgets")).toBeInTheDocument();
    expect(screen.getByText("acme/widgets")).toBeInTheDocument();
  });

  it("shows a message when a search matches no repos", async () => {
    const user = userEvent.setup();
    renderList();

    await user.type(screen.getByPlaceholderText("Filter added repos…"), "nope");

    expect(screen.getByText("No repos match your search.")).toBeInTheDocument();
  });

  it("calls onOpenRepo when a row is clicked", async () => {
    const onOpenRepo = vi.fn();
    const user = userEvent.setup();
    renderList({ onOpenRepo });

    await user.click(screen.getByText("acme/gadgets"));

    expect(onOpenRepo).toHaveBeenCalledWith("acme/gadgets");
  });

  it("shows an empty message when there are no known repos", () => {
    renderList({ tasks: [] });
    expect(
      screen.getByText(
        "No repos yet -- add one above, or file a task with a target repo.",
      ),
    ).toBeInTheDocument();
  });

  it("adds a repo and refreshes config on success", async () => {
    api.mockResolvedValueOnce({});
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const onRefreshConfig = vi.fn();
    const user = userEvent.setup();
    renderList({ tasks: [], onRefreshConfig });

    await user.type(screen.getByPlaceholderText("owner/name"), "acme/widgets");
    await user.click(screen.getByRole("button", { name: "Add repo" }));

    expect(api).toHaveBeenCalledWith("/api/repos", {
      method: "POST",
      body: JSON.stringify({ repo: "acme/widgets" }),
    });
    expect(onRefreshConfig).toHaveBeenCalledTimes(1);
    vi.unstubAllGlobals();
  });

  it("adds a non-GitHub repo with its clone URL", async () => {
    api.mockImplementation((path) =>
      path === "/api/deployment/reachable-repos"
        ? Promise.resolve({
            available: true,
            repos: [{ repo: "acme/gadgets", push: true }],
            connections: [
              { credential: "gh", host: "github.com", ok: true, repoCount: 1 },
            ],
          })
        : Promise.resolve({}),
    );
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const user = userEvent.setup();
    renderList({ tasks: [] });

    const input = screen.getByPlaceholderText("owner/name");
    await user.click(input);
    await user.click(await screen.findByRole("option", { name: "Other…" }));
    await user.type(input, "acme/widgets");
    await user.type(
      screen.getByPlaceholderText("Clone URL (GitHub by default)"),
      "git@gitlab.example.com:acme/widgets.git",
    );
    await user.click(screen.getByRole("button", { name: "Add repo" }));

    expect(api).toHaveBeenCalledWith("/api/repos", {
      method: "POST",
      body: JSON.stringify({
        repo: "acme/widgets",
        cloneUrl: "git@gitlab.example.com:acme/widgets.git",
      }),
    });
    vi.unstubAllGlobals();
  });

  // A discovered repo carries its host and is cloned through the
  // connection that found it, so the Clone URL field is only there for
  // "Other…" or a host none of the team's connections is on.
  it("offers the clone URL only for Other… or a host no connection covers", async () => {
    api.mockImplementation((path) =>
      path === "/api/deployment/reachable-repos"
        ? Promise.resolve({
            available: true,
            repos: [
              { repo: "acme/widgets", push: true },
              { repo: "depot.code.forge.test/team/repo", push: true },
            ],
            connections: [
              { credential: "gh", host: "github.com", ok: true, repoCount: 1 },
              {
                credential: "depot",
                host: "depot.code.forge.test",
                ok: true,
                repoCount: 1,
              },
            ],
          })
        : Promise.resolve({}),
    );
    const user = userEvent.setup();
    renderList({ tasks: [] });
    const clone = () =>
      screen.queryByPlaceholderText("Clone URL (GitHub by default)");

    expect(clone()).not.toBeInTheDocument();
    const input = screen.getByPlaceholderText("owner/name");
    await user.type(input, "acme/other");
    expect(clone()).not.toBeInTheDocument();
    await user.clear(input);
    await user.type(input, "depot.code.forge.test/team/other");
    expect(clone()).not.toBeInTheDocument();
    await user.clear(input);
    await user.type(input, "gitlab.example.com/acme/widgets");
    expect(clone()).toBeInTheDocument();
    await user.clear(input);
    expect(clone()).not.toBeInTheDocument();

    // Clearing the box leaves the list open, and Other… on it.
    await user.click(await screen.findByRole("option", { name: "Other…" }));
    expect(clone()).toBeInTheDocument();
  });

  it("adds a discovered depot repo without a clone URL", async () => {
    api.mockImplementation((path) =>
      path === "/api/deployment/reachable-repos"
        ? Promise.resolve({
            available: true,
            repos: [{ repo: "depot.code.forge.test/team/repo", push: true }],
            connections: [
              {
                credential: "depot",
                host: "depot.code.forge.test",
                ok: true,
                repoCount: 1,
              },
            ],
          })
        : Promise.resolve({}),
    );
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const user = userEvent.setup();
    renderList({ tasks: [] });

    await user.click(screen.getByPlaceholderText("owner/name"));
    await user.click(
      await screen.findByRole("option", {
        name: "depot.code.forge.test/team/repo",
      }),
    );
    expect(
      screen.queryByPlaceholderText("Clone URL (GitHub by default)"),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Add repo" }));

    expect(api).toHaveBeenCalledWith("/api/repos", {
      method: "POST",
      body: JSON.stringify({ repo: "depot.code.forge.test/team/repo" }),
    });
    vi.unstubAllGlobals();
  });

  // A fresh install starts with no git connection (a Cloudtop's no
  // longer seeds depot): the page says to add one before its picker,
  // which discovers nothing, reads as broken.
  it("says to add a git connection first for a deployment that has none", async () => {
    api.mockResolvedValue({ available: false, repos: [], connections: [] });
    const onOpenSettings = vi.fn();
    const user = userEvent.setup();
    renderList({
      tasks: [],
      config: {
        deployment: { id: "default", hasGitCredential: false },
      },
      onOpenSettings,
    });
    const notice = await screen.findByTestId("no-git-connection");
    expect(notice).toHaveTextContent("has no git connection yet");
    await user.click(
      within(notice).getByRole("link", {
        name: "Settings → Git",
      }),
    );
    expect(onOpenSettings).toHaveBeenCalledWith("git");
  });

  describe("discovery status", () => {
    const status = () => screen.getByTestId("repo-discovery-status");

    it("says it is discovering while reachable-repos is in flight", async () => {
      api.mockImplementation(() => new Promise(() => {}));
      const user = userEvent.setup();
      renderList({ tasks: [] });

      expect(status()).toHaveAttribute("data-status", "loading");
      expect(status()).toHaveTextContent("Discovering repositories…");
      // The picker opens on focus, with its arrow, even before it has
      // anything to offer -- and says it is loading.
      expect(screen.getByRole("button", { name: "Open" })).toBeInTheDocument();
      await user.click(screen.getByPlaceholderText("owner/name"));
      expect(await screen.findByRole("listbox")).toBeInTheDocument();
    });

    it("counts the repos found and the connections that found them", async () => {
      api.mockImplementation((path) =>
        path === "/api/deployment/reachable-repos"
          ? Promise.resolve({
              available: true,
              repos: [
                { repo: "acme/widgets", push: true },
                { repo: "acme/docs", push: false },
                { repo: "depot.code.forge.test/team/repo", push: true },
              ],
              connections: [
                {
                  credential: "gh",
                  host: "github.com",
                  ok: true,
                  repoCount: 2,
                },
                {
                  credential: "depot",
                  host: "depot.code.forge.test",
                  ok: true,
                  repoCount: 1,
                },
              ],
            })
          : Promise.resolve({}),
      );
      renderList({ tasks: [] });

      expect(
        await screen.findByText("3 repos discovered from 2 Git connections"),
      ).toBeInTheDocument();
      expect(status()).toHaveAttribute("data-status", "found");
    });

    it("says none were found and links to the Git connections", async () => {
      api.mockImplementation((path) =>
        path === "/api/deployment/reachable-repos"
          ? Promise.resolve({
              available: false,
              reason: "team has no git credential",
              repos: [],
              connections: [],
            })
          : Promise.resolve({}),
      );
      const onOpenSettings = vi.fn();
      const user = userEvent.setup();
      renderList({ tasks: [], onOpenSettings });

      const link = await screen.findByRole("link", {
        name: "Git connection in Settings",
      });
      expect(status()).toHaveAttribute("data-status", "none");
      expect(status()).toHaveTextContent(
        "No repositories discovered — add or check a Git connection in Settings",
      );
      expect(status()).toHaveTextContent("team has no git credential");

      // Modified clicks open in a new tab instead of intercepting in-app.
      const root = link.closest("body > div");
      const leftToBrowser = [];
      const follow = (e) => {
        leftToBrowser.push(!e.defaultPrevented);
        e.preventDefault();
      };
      root.addEventListener("click", follow);
      fireEvent.click(link, { ctrlKey: true });
      fireEvent.click(link, { metaKey: true });
      fireEvent.click(link, { shiftKey: true });
      fireEvent.click(link, { altKey: true });
      fireEvent.click(link, { button: 1 });
      root.removeEventListener("click", follow);
      expect(leftToBrowser).toEqual([true, true, true, true, true]);
      expect(onOpenSettings).not.toHaveBeenCalled();

      await user.click(link);
      expect(onOpenSettings).toHaveBeenCalledWith("git");
    });

    it("names each connection that failed, and why", async () => {
      api.mockImplementation((path) =>
        path === "/api/deployment/reachable-repos"
          ? Promise.resolve({
              available: false,
              reason: "listing failed",
              repos: [],
              connections: [
                {
                  credential: "depot",
                  host: "depot.code.forge.test",
                  ok: false,
                  error: "forge login needed: run `forgecli-login` as alice",
                  repoCount: 0,
                  loginNeeded: true,
                },
              ],
            })
          : Promise.resolve({}),
      );
      renderList({ tasks: [] });

      expect(
        await screen.findByText(
          "depot on depot.code.forge.test: forge login needed: run `forgecli-login` as alice",
        ),
      ).toBeInTheDocument();
      expect(status()).toHaveAttribute("data-status", "error");
      expect(status()).toHaveTextContent("Couldn't discover repositories");
    });

    it("says so when the request itself fails", async () => {
      api.mockImplementation((path) =>
        path === "/api/deployment/reachable-repos"
          ? Promise.reject(new Error("502 Bad Gateway"))
          : Promise.resolve({}),
      );
      renderList({ tasks: [] });

      expect(
        await screen.findByText(
          "Couldn't discover repositories: 502 Bad Gateway",
        ),
      ).toBeInTheDocument();
      expect(status()).toHaveAttribute("data-status", "error");
    });
  });

  it("heads the list as the repos already added, apart from the add form", () => {
    renderList();
    expect(screen.getByTestId("your-repos-label")).toHaveTextContent(
      "Your repos",
    );
    expect(
      screen.getByPlaceholderText("Filter added repos…"),
    ).toBeInTheDocument();
  });

  it("reports the error and does not refresh config when adding fails", async () => {
    // Only the add fails: the plan the confirmation asks for first
    // (grain/task-411) is a different call, and one whose failure is
    // deliberately silent.
    api.mockImplementation((path) =>
      path === "/api/repos"
        ? Promise.reject(new Error("repo must be owner/name"))
        : Promise.resolve({}),
    );
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const onRefreshConfig = vi.fn();
    const showError = vi.fn();
    const user = userEvent.setup();
    renderList({ tasks: [], onRefreshConfig, showError });

    await user.type(screen.getByPlaceholderText("owner/name"), "not-a-repo");
    await user.click(screen.getByRole("button", { name: "Add repo" }));

    expect(showError).toHaveBeenCalledWith(
      expect.objectContaining({ message: "repo must be owner/name" }),
    );
    expect(onRefreshConfig).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  it("populates autocomplete with reachable repos and submits canonical identifier", async () => {
    api.mockImplementation((path) => {
      if (path === "/api/deployment/reachable-repos") {
        return Promise.resolve({
          available: true,
          repos: [
            { repo: "acme/widgets", push: true },
            {
              repo: "depot.code.forge.test/experimental/bsalmon-test",
              push: true,
            },
          ],
        });
      }
      if (path === "/api/repos") {
        return Promise.resolve({});
      }
      return Promise.resolve({});
    });
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const onRefreshConfig = vi.fn();
    const user = userEvent.setup();
    renderList({ tasks: [], onRefreshConfig });

    const input = await screen.findByPlaceholderText("owner/name");
    await user.click(input);
    const option = await screen.findByRole("option", {
      name: "depot.code.forge.test/experimental/bsalmon-test",
    });
    await user.click(option);
    await user.click(screen.getByRole("button", { name: "Add repo" }));

    expect(api).toHaveBeenCalledWith("/api/repos", {
      method: "POST",
      body: JSON.stringify({
        repo: "depot.code.forge.test/experimental/bsalmon-test",
      }),
    });
    expect(onRefreshConfig).toHaveBeenCalledTimes(1);
    vi.unstubAllGlobals();
  });

  it("allows custom typing via Other… escape hatch", async () => {
    api.mockImplementation((path) => {
      if (path === "/api/deployment/reachable-repos") {
        return Promise.resolve({
          available: true,
          repos: [{ repo: "acme/widgets", push: true }],
        });
      }
      if (path === "/api/repos") {
        return Promise.resolve({});
      }
      return Promise.resolve({});
    });
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const onRefreshConfig = vi.fn();
    const user = userEvent.setup();
    renderList({ tasks: [], onRefreshConfig });

    const input = await screen.findByPlaceholderText("owner/name");
    await user.click(input);
    const otherOption = await screen.findByRole("option", { name: "Other…" });
    await user.click(otherOption);
    await user.type(input, "custom/private-repo");
    await user.click(screen.getByRole("button", { name: "Add repo" }));

    expect(api).toHaveBeenCalledWith("/api/repos", {
      method: "POST",
      body: JSON.stringify({ repo: "custom/private-repo" }),
    });
    expect(onRefreshConfig).toHaveBeenCalledTimes(1);
    vi.unstubAllGlobals();
  });

  // grain/task-45: an empty targetRepos means unrestricted, so the first
  // add narrows the deployment rather than widening it. The pane says so
  // twice -- standing on the page, and again at the click.
  it("says the deployment is unrestricted, and what adding the first repo would mean", () => {
    renderList({ config: { targetRepos: [] } });

    expect(
      screen.getByText(/an empty allowlist is what means unrestricted/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/restricts it to that one repo/),
    ).toBeInTheDocument();
  });

  it("says a one-repo allowlist allows only that repo", () => {
    renderList({ config: { targetRepos: ["acme/widgets"] } });

    expect(screen.getByText(/allows only acme\/widgets/)).toBeInTheDocument();
    expect(
      screen.queryByText(/an empty allowlist is what means unrestricted/),
    ).not.toBeInTheDocument();
  });

  it("drops both notes once the allowlist names more than one repo", () => {
    renderList({ config: { targetRepos: ["acme/widgets", "acme/gadgets"] } });

    expect(
      screen.queryByText(/an empty allowlist is what means unrestricted/),
    ).not.toBeInTheDocument();
    expect(screen.queryByText(/allows only/)).not.toBeInTheDocument();
  });

  it("confirms the first add, naming the repos that would fall off the allowlist", async () => {
    api.mockResolvedValueOnce({});
    const confirmed = vi.fn().mockReturnValue(true);
    vi.stubGlobal("confirm", confirmed);
    const user = userEvent.setup();
    renderList({ config: { targetRepos: [] } });

    await user.type(screen.getByPlaceholderText("owner/name"), "acme/widgets");
    await user.click(screen.getByRole("button", { name: "Add repo" }));

    const msg = confirmed.mock.calls[0][0];
    expect(msg).toContain("only repo this deployment allows");
    // The repo being added is not one of the repos that fall off it.
    const falling = msg.split("Off the allowlist as of this click: ")[1];
    expect(falling).toContain("acme/gadgets");
    expect(falling).not.toContain("acme/widgets");
    expect(api).toHaveBeenCalledWith("/api/repos", {
      method: "POST",
      body: JSON.stringify({ repo: "acme/widgets" }),
    });
    vi.unstubAllGlobals();
  });

  // grain/task-411: the first add is a narrowing -- an empty allowlist
  // means unrestricted -- so the confirmation asks the daemon what the
  // repos it leaves out still have in them, and says it. How much work
  // falls off is the store's to count; this page can only name the repos.
  it("confirms the first add with what the daemon says it would strand", async () => {
    const summary =
      "`acme/gadgets` comes off this deployment's target repo list, so 1 task " +
      "still in play there is parked instead of dispatched.";
    api.mockImplementation((path) =>
      Promise.resolve(
        path === "/api/settings/plan"
          ? { targetReposStranded: [{ repo: "acme/gadgets", summary }] }
          : {},
      ),
    );
    const confirmed = vi.fn().mockReturnValue(true);
    vi.stubGlobal("confirm", confirmed);
    const user = userEvent.setup();
    renderList({ config: { targetRepos: [] } });

    await user.type(screen.getByPlaceholderText("owner/name"), "acme/widgets");
    await user.click(screen.getByRole("button", { name: "Add repo" }));

    // Planned as the save this click makes: the list it would write.
    expect(api).toHaveBeenCalledWith("/api/settings/plan", {
      method: "POST",
      body: JSON.stringify({ targetRepos: ["acme/widgets"] }),
    });
    expect(confirmed).toHaveBeenCalledWith(expect.stringContaining(summary));
    // Told, not stopped.
    expect(api).toHaveBeenCalledWith("/api/repos", {
      method: "POST",
      body: JSON.stringify({ repo: "acme/widgets" }),
    });
    vi.unstubAllGlobals();
  });

  it("does not add the first repo when the confirmation is declined", async () => {
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(false));
    const onRefreshConfig = vi.fn();
    const user = userEvent.setup();
    renderList({ config: { targetRepos: [] }, onRefreshConfig });

    await user.type(screen.getByPlaceholderText("owner/name"), "acme/widgets");
    await user.click(screen.getByRole("button", { name: "Add repo" }));

    // The plan the confirmation was built from is the one call a decline
    // still makes, and it writes nothing (POST /api/settings/plan is a
    // computed answer about a body, not a save).
    expect(api).not.toHaveBeenCalledWith("/api/repos", expect.anything());
    expect(onRefreshConfig).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  // grain/task-327: a row opens with a status dot, so a repo with an
  // agent working in it right now is visible without reading its counts.
  describe("status dot", () => {
    it("calls a repo with a running task active", () => {
      renderList();

      const row = screen.getByText("acme/widgets").closest("li");
      expect(
        within(row).getByLabelText("Active -- 1 task running now"),
      ).toBeInTheDocument();
    });

    // "Queued for merge" is not an ending: the task is still waiting on
    // the merge queue, so the repo still has work outstanding.
    it("calls a repo with work but nothing running open", () => {
      renderList();

      const row = screen.getByText("acme/gadgets").closest("li");
      expect(
        within(row).getByLabelText("1 open task, none running right now"),
      ).toBeInTheDocument();
    });

    it("calls a repo whose every task is closed idle", () => {
      renderList({
        tasks: [
          {
            id: "9",
            title: "Old news",
            repo: "acme/done",
            state: "closed",
            blocked: false,
            capabilities: [],
          },
        ],
      });

      const row = screen.getByText("acme/done").closest("li");
      expect(
        within(row).getByLabelText("Idle -- every task here is closed"),
      ).toBeInTheDocument();
    });

    it("titles each name with the whole of it, for the row that truncates a long one", () => {
      const long =
        "example-corporation/exceptionally-long-repository-name-for-responsive-layout";
      renderList({ config: { targetRepos: [long] }, tasks: [] });

      expect(screen.getByText(long)).toHaveAttribute("title", long);
    });

    it("calls a repo with no tasks at all idle", () => {
      renderList({ config: { targetRepos: ["acme/fresh"] }, tasks: [] });

      const row = screen.getByText("acme/fresh").closest("li");
      expect(
        within(row).getByLabelText("Idle -- no tasks here yet"),
      ).toBeInTheDocument();
    });
  });

  // grain/task-327: the rows can be dragged into an order of this
  // browser's own -- a display order, stored here and nowhere else,
  // which is why nothing is posted to the API when one moves.
  describe("custom order", () => {
    const namesInOrder = () =>
      [...document.querySelectorAll(".repo-list-name")].map(
        (e) => e.textContent,
      );

    const rowFor = (repo) => screen.getByText(repo).closest("li");

    it("opens in alphabetical order, before anything has been dragged", () => {
      renderList();
      expect(namesInOrder()).toEqual(["acme/gadgets", "acme/widgets"]);
    });

    it("keeps a dragged row where it was dropped, across a remount", () => {
      const { unmount } = render(
        <RepoList
          tasks={tasks}
          config={null}
          onOpenRepo={vi.fn()}
          onRefreshConfig={vi.fn()}
          showError={vi.fn()}
        />,
      );

      fireEvent.dragStart(rowFor("acme/widgets"));
      fireEvent.dragOver(rowFor("acme/gadgets"));
      fireEvent.drop(rowFor("acme/gadgets"));

      expect(namesInOrder()).toEqual(["acme/widgets", "acme/gadgets"]);
      expect(api).not.toHaveBeenCalled();

      unmount();
      renderList();
      expect(namesInOrder()).toEqual(["acme/widgets", "acme/gadgets"]);
    });

    it("drops a row at the very end of the list", () => {
      renderList();

      fireEvent.dragStart(rowFor("acme/gadgets"));
      // The trailing drop zone only renders once a drag is in flight.
      fireEvent.dragOver(document.querySelector(".repo-list .task-drop-end"));
      fireEvent.drop(document.querySelector(".repo-list .task-drop-end"));

      expect(namesInOrder()).toEqual(["acme/widgets", "acme/gadgets"]);
    });

    it("clicking the drag handle does not open the repo", async () => {
      const onOpenRepo = vi.fn();
      const user = userEvent.setup();
      renderList({ onOpenRepo });

      await user.click(document.querySelector(".task-drag-handle"));

      expect(onOpenRepo).not.toHaveBeenCalled();
    });

    // Every other order is one this page computes, so there is nowhere
    // for a drop to land: the handles go with the gesture.
    it("withdraws the handles when the list is sorted some other way", async () => {
      const user = userEvent.setup();
      renderList();

      expect(document.querySelectorAll(".task-drag-handle")).toHaveLength(2);

      await user.click(screen.getByLabelText("Sort"));
      await user.click(
        await screen.findByRole("option", { name: "Name (A–Z)" }),
      );

      expect(document.querySelectorAll(".task-drag-handle")).toHaveLength(0);
    });

    it("sorts the busiest repo first when asked for most tasks", async () => {
      const user = userEvent.setup();
      renderList();

      await user.click(screen.getByLabelText("Sort"));
      await user.click(
        await screen.findByRole("option", { name: "Most tasks" }),
      );

      expect(namesInOrder()).toEqual(["acme/widgets", "acme/gadgets"]);
    });

    it("sorts the repo with a run in flight first when asked for active first", async () => {
      const user = userEvent.setup();
      renderList();

      await user.click(screen.getByLabelText("Sort"));
      await user.click(
        await screen.findByRole("option", { name: "Active first" }),
      );

      expect(namesInOrder()).toEqual(["acme/widgets", "acme/gadgets"]);
    });
  });

  it("does not confirm an add that only widens an allowlist that already restricts", async () => {
    api.mockResolvedValueOnce({});
    const confirmed = vi.fn().mockReturnValue(true);
    vi.stubGlobal("confirm", confirmed);
    const user = userEvent.setup();
    renderList({ config: { targetRepos: ["acme/widgets"] } });

    await user.type(screen.getByPlaceholderText("owner/name"), "acme/gadgets");
    await user.click(screen.getByRole("button", { name: "Add repo" }));

    expect(confirmed).not.toHaveBeenCalled();
    expect(api).toHaveBeenCalledWith("/api/repos", {
      method: "POST",
      body: JSON.stringify({ repo: "acme/gadgets" }),
    });
    vi.unstubAllGlobals();
  });
});
