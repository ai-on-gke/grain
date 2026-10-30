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
import { afterEach, describe, expect, it, onTestFinished, vi } from "vitest";
import { clearBranchCache } from "./BranchField.jsx";
import RepoPage, { workflowEditsUnsaved } from "./RepoPage.jsx";
import { leavingLosesEdits } from "../unsavedEdits.js";
import api from "../api.js";
import { resetCapabilityMethodsForTest } from "./capabilityMethods.js";

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
];

const noCaps = {
  repo: "acme/widgets",
  defaultCapabilities: [],
  deploymentDefaultCapabilities: [],
  effectiveDefaultCapabilities: [],
};

const noPrompt = {
  repo: "acme/widgets",
  promptExtension: "",
  deploymentPromptExtension: "",
  effectivePromptExtension: "",
};

const noSetup = {
  repo: "acme/widgets",
  setupCommand: "",
};

const noDeclarations = {
  repo: "acme/widgets",
  declarations: false,
};

const noUpToDate = {
  repo: "acme/widgets",
  requireBranchUpToDate: false,
};

const noUnfixable = {
  repo: "acme/widgets",
  unfixableChecks: [],
  grainCICheckName: "grain CI",
};

const noWorkflows = {
  repo: "acme/widgets",
  dir: ".grain/workflows",
  workflows: [],
};

// Nobody having said anything about this repository's standing work,
// which is the state very nearly every repository is in
// (RepoWorkOwnership.jsx).
const ownsItsWork = {
  repo: "acme/widgets",
  default: "",
  defaultRuns: true,
  items: [{ item: "release", kind: "release", override: "", runs: true }],
};

// routeApi answers the GETs the page issues on mount -- its branch
// list, its capability sets, its prompt extension,
// its setup command, its merge-queue switch, the CI workflows grain
// declares for it and the runs those workflows have had (RepoRuns.jsx)
// -- and leaves anything else to the per-test answerOnce answers below,
// so a test only has to say what it is actually about.
function routeApi({
  branches = [],
  caps = noCaps,
  prompt = noPrompt,
  setup = noSetup,
  declarations = noDeclarations,
  upToDate = noUpToDate,
  unfixable = noUnfixable,
  unfixableSave = null,
  workflows = noWorkflows,
  workflowSave = {},
  workOwnership = ownsItsWork,
  ciRuns = { enabled: true, runs: [] },
} = {}) {
  api.mockImplementation((path, opts) => {
    if (!opts && /\/branches$/.test(path)) return Promise.resolve(branches);
    if (!opts && /\/capabilities$/.test(path)) return Promise.resolve(caps);
    if (!opts && /\/prompt-extension$/.test(path))
      return Promise.resolve(prompt);
    if (!opts && /\/setup-command$/.test(path)) return Promise.resolve(setup);
    if (!opts && /\/declarations$/.test(path))
      return Promise.resolve(declarations);
    if (opts && /\/declarations$/.test(path))
      return Promise.resolve({ ...declarations, declarations: true });
    if (!opts && /\/require-branch-up-to-date$/.test(path))
      return Promise.resolve(upToDate);
    if (opts && /\/require-branch-up-to-date$/.test(path))
      return Promise.resolve({ ...upToDate, requireBranchUpToDate: true });
    if (opts && /\/merge-on-green$/.test(path))
      return Promise.resolve({ ...upToDate, mergeOnGreen: true });
    if (opts && /\/base-repair-bypasses-human-gates$/.test(path))
      return Promise.resolve({
        ...upToDate,
        baseRepairBypassesHumanGates: true,
      });
    if (!opts && /\/ignore-github-ci$/.test(path))
      return Promise.resolve(upToDate);
    if (opts && /\/ignore-github-ci$/.test(path))
      return Promise.resolve({
        ...upToDate,
        ignoreGitHubCI: true,
        ciMode: "grain",
      });
    if (!opts && /\/ci-mode$/.test(path)) return Promise.resolve(upToDate);
    if (opts && /\/ci-mode$/.test(path))
      return Promise.resolve({
        ...upToDate,
        ciMode: JSON.parse(opts.body).ciMode,
        ignoreGitHubCI: JSON.parse(opts.body).ciMode === "grain",
      });
    if (!opts && /\/unfixable-checks$/.test(path))
      return Promise.resolve(unfixable);
    if (opts && /\/unfixable-checks$/.test(path))
      return Promise.resolve(
        unfixableSave || {
          ...unfixable,
          unfixableChecks: JSON.parse(opts.body).unfixableChecks,
        },
      );
    if (!opts && /\/work-ownership$/.test(path))
      return Promise.resolve(workOwnership);
    if (!opts && /\/workflows$/.test(path)) return Promise.resolve(workflows);
    if (opts && /\/workflows$/.test(path)) return Promise.resolve(workflowSave);
    if (!opts && path.startsWith("/api/ci/runs"))
      return Promise.resolve(ciRuns);
    return Promise.resolve({});
  });
}

// answerOnce is one answer for one request, keyed on the request rather
// than on "whichever call comes next".
//
// api.mockImplementationOnce is the latter, and on this page that is a
// race rather than a way of saying which call is meant. RepoRuns polls
// GET /api/ci/runs?repo= every five seconds for as long as the page is
// open (RepoRuns.jsx, and the poll is the point of it -- a run started
// from that panel fills in while somebody watches). A test that installs
// a once-off answer and then types into a form is in a footrace with
// that poll: on a machine slow enough for the typing to cross five
// seconds, the poll takes the answer meant for the save, the save falls
// through to routeApi's own success, and the test asserts against a form
// that save has already cleared.
//
// That is not hypothetical. "reports the error when a workflow is
// refused, keeping what was typed" failed exactly that way on grain's
// own CI -- on every branch at once, reproducibly, while passing on
// every machine anybody ran it on by hand, including under the same
// `npm test -- --maxWorkers=2` the CI job uses. The refusal was being
// delivered to the poll (which is why the showError assertion still
// passed), the PUT was answered with routeApi's empty workflowSave, and
// the form the assertion was about had been rebuilt from it.
//
// Keyed, the answer goes to the call it was written for whatever else
// the page is doing, and the test stops depending on how fast the box
// is.
function answerOnce(match, answer) {
  const otherwise = api.getMockImplementation();
  let spent = false;
  api.mockImplementation((path, opts) => {
    if (!spent && match(path, opts)) {
      spent = true;
      return answer();
    }
    return otherwise(path, opts);
  });
}

// The requests the tests below mean by "the next one": each form's own
// save, and the branch list a successful add re-reads.
const addingBranch = (path, opts) =>
  !!opts && /\/branches$/.test(path) && opts.method === "POST";
const listingBranches = (path, opts) => !opts && /\/branches$/.test(path);
const savingCapabilities = (path, opts) =>
  !!opts && /\/capabilities$/.test(path) && opts.method === "PUT";
const savingCICapabilities = (path, opts) =>
  !!opts && /\/ci-capabilities$/.test(path) && opts.method === "PUT";
const savingPromptExtension = (path, opts) =>
  !!opts && /\/prompt-extension$/.test(path) && opts.method === "PUT";
const savingSetupCommand = (path, opts) =>
  !!opts && /\/setup-command$/.test(path) && opts.method === "PUT";
const savingWorkflows = (path, opts) =>
  !!opts && /\/workflows$/.test(path) && opts.method === "PUT";

function renderPage({ tab, ...overrides } = {}) {
  window.history.replaceState({}, "", tab ? `/#${tab}` : "/");
  const props = {
    repo: "acme/widgets",
    tasks,
    config: null,
    onBack: vi.fn(),
    onNewTask: vi.fn(),
    onOpenComparison: vi.fn(),
    onOpenReleases: vi.fn(),
    onOpenCIRun: vi.fn(),
    onRefreshConfig: vi.fn(),
    showError: vi.fn(),
    ...overrides,
  };
  render(<RepoPage {...props} />);
  return props;
}

// openTab selects one of the page's tabs, the way a reader does.
async function openTab(name) {
  await userEvent.click(screen.getByRole("tab", { name }));
}

describe("RepoPage", () => {
  // A repo's task defaults and CI grants hold capability server methods
  // (with their with: arguments) as well as the legacy ids, and its task
  // defaults api:* grants too: GrantsPicker edits those, the legacy
  // pickers the rest, and each save keeps the other's entries.
  it("edits default and CI method grants with GrantsPicker beside the legacy pickers", async () => {
    resetCapabilityMethodsForTest();
    const user = userEvent.setup();
    const cloudKey = {
      ref: "https://example.com/caps/cloud#cloud-key",
      method: "cloud-key",
      args: {
        type: "object",
        required: ["account"],
        properties: { account: { type: "string", minLength: 1 } },
      },
    };
    const held = `${cloudKey.ref}={"account":"staging"}`;
    const ciHeld = `${cloudKey.ref}={"account":"ci"}`;
    routeApi({
      caps: {
        ...noCaps,
        defaultCapabilities: ["gcp-key", "api:tasks:read", held],
        ciCapabilities: ["openai-proxy", ciHeld],
        availableCICapabilities: ["gcp-key", "openai-proxy"],
      },
    });
    const routed = api.getMockImplementation();
    api.mockImplementation((path, opts) => {
      // The default grants' picker asks for the listing as this repo's
      // agent tasks see it (?kind=task&repo=), the CI one for no run.
      if (path.split("?")[0] === "/api/capability-methods")
        return Promise.resolve([cloudKey]);
      if (path === "/api/capability-grants/check")
        return Promise.resolve(
          JSON.parse(opts.body).grants.map(({ grant }) => ({
            grant,
            recorded: grant,
          })),
        );
      return routed(path, opts);
    });
    renderPage({
      config: {
        capabilityMethods: 1,
        capabilities: [
          { id: "gcp-key", name: "GCP key" },
          { id: "openai-proxy", name: "OpenAI proxy" },
          { id: "api:tasks:read", name: "Read tasks" },
        ],
      },
    });
    const legacy = await screen.findByLabelText("Default capabilities");
    expect(legacy).toHaveTextContent("GCP key");
    expect(legacy).not.toHaveTextContent("cloud-key");
    expect(legacy).not.toHaveTextContent("api:tasks:read");
    const ciLegacy = screen.getByLabelText("CI capabilities");
    expect(ciLegacy).toHaveTextContent("OpenAI proxy");
    expect(ciLegacy).not.toHaveTextContent("cloud-key");

    const defaults = screen.getByTestId("repo-default-grants");
    await waitFor(() =>
      expect(
        defaults.querySelector(`[data-grant='${held}'] input`),
      ).toBeTruthy(),
    );
    const ci = screen.getByTestId("repo-ci-grants");
    await waitFor(() =>
      expect(ci.querySelector(`[data-grant='${ciHeld}'] input`)).toBeTruthy(),
    );

    await openTab("Agents");
    answerOnce(savingCapabilities, () =>
      Promise.resolve({
        ...noCaps,
        defaultCapabilities: ["gcp-key", held],
        ciCapabilities: ["openai-proxy", ciHeld],
      }),
    );
    await user.click(
      within(defaults).getByRole("button", { name: /^Remove api:tasks:read/ }),
    );
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/capabilities", {
        method: "PUT",
        body: JSON.stringify({ defaultCapabilities: ["gcp-key", held] }),
      }),
    );

    answerOnce(savingCICapabilities, () =>
      Promise.resolve({
        ...noCaps,
        defaultCapabilities: ["gcp-key", held],
        ciCapabilities: ["openai-proxy"],
      }),
    );
    await openTab("CI");
    const ciSection = ci.querySelector(`[data-grant='${ciHeld}']`);
    await user.click(
      within(ciSection).getByRole("button", { name: /^Remove / }),
    );
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/repos/acme/widgets/ci-capabilities",
        {
          method: "PUT",
          body: JSON.stringify({ ciCapabilities: ["openai-proxy"] }),
        },
      ),
    );
    // A CI job has no agent to hold an api grant, so CI's picker offers none.
    await user.click(within(ci).getByRole("button", { name: "Add grant…" }));
    expect(
      await screen.findByRole("menuitem", { name: /cloud-key/ }),
    ).toBeTruthy();
    expect(screen.queryByRole("menuitem", { name: /tasks/i })).toBeNull();
  });

  it("saves and clears CI capabilities independently from task defaults", async () => {
    const user = userEvent.setup();
    routeApi({
      caps: {
        ...noCaps,
        defaultCapabilities: ["gcp-key"],
        ciCapabilities: [],
        availableCICapabilities: ["gcp-key", "openai-proxy"],
      },
    });
    const props = renderPage({
      config: {
        capabilities: [
          { id: "gcp-key", name: "GCP key" },
          { id: "openai-proxy", name: "OpenAI proxy" },
          { id: "self-debug", name: "Self debug" },
        ],
      },
    });
    await user.click(await screen.findByLabelText("CI capabilities"));
    expect(
      screen.queryByRole("option", { name: /Self debug/ }),
    ).not.toBeInTheDocument();
    answerOnce(savingCICapabilities, () =>
      Promise.resolve({
        ...noCaps,
        ciCapabilities: ["openai-proxy"],
        availableCICapabilities: ["openai-proxy"],
      }),
    );
    await user.click(screen.getByRole("option", { name: /OpenAI proxy/ }));
    await user.keyboard("{Escape}");
    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/ci-capabilities",
      {
        method: "PUT",
        body: JSON.stringify({ ciCapabilities: ["openai-proxy"] }),
      },
    );
    expect(props.onRefreshConfig).toHaveBeenCalled();
    await user.click(screen.getByLabelText("CI capabilities"));
    answerOnce(savingCICapabilities, () =>
      Promise.resolve({ ...noCaps, ciCapabilities: [] }),
    );
    await user.click(screen.getByRole("option", { name: /OpenAI proxy/ }));
    await user.keyboard("{Escape}");
    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/ci-capabilities",
      {
        method: "PUT",
        body: JSON.stringify({ ciCapabilities: [] }),
      },
    );
    expect(screen.getByLabelText("Default capabilities")).toHaveTextContent(
      "GCP key",
    );
  });

  it("reports a CI capability save failure and preserves the selection", async () => {
    const user = userEvent.setup();
    routeApi({
      caps: {
        ...noCaps,
        ciCapabilities: ["openai-proxy"],
        availableCICapabilities: ["openai-proxy", "gcp-key"],
      },
    });
    const props = renderPage({
      config: {
        capabilities: [
          { id: "openai-proxy", name: "OpenAI proxy" },
          { id: "gcp-key", name: "GCP key" },
        ],
      },
    });
    await screen.findByLabelText("CI capabilities");
    const error = new Error("save refused");
    answerOnce(savingCICapabilities, () => Promise.reject(error));
    await user.click(screen.getByLabelText("CI capabilities"));
    await user.click(screen.getByRole("option", { name: /GCP key/ }));
    await user.keyboard("{Escape}");
    expect(props.showError).toHaveBeenCalledWith(error);
  });

  it("displays unselected CI-only capabilities (package-publish, registry-push) in CI capabilities dropdown", async () => {
    const user = userEvent.setup();
    routeApi({
      caps: {
        ...noCaps,
        defaultCapabilities: [],
        ciCapabilities: [],
        availableCICapabilities: [
          "gcp-key",
          "package-publish",
          "registry-push",
        ],
      },
    });
    renderPage({
      config: {
        capabilities: [{ id: "gcp-key", name: "GCP key" }],
        ciCapabilities: [
          { id: "gcp-key", name: "GCP key" },
          { id: "package-publish", name: "Package publish" },
          { id: "registry-push", name: "Registry push" },
        ],
      },
    });
    await user.click(await screen.findByLabelText("CI capabilities"));
    expect(
      screen.getByRole("option", { name: /Package publish/ }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("option", { name: /Registry push/ }),
    ).toBeInTheDocument();
    answerOnce(savingCICapabilities, () =>
      Promise.resolve({
        ...noCaps,
        ciCapabilities: ["package-publish"],
        availableCICapabilities: [
          "gcp-key",
          "package-publish",
          "registry-push",
        ],
      }),
    );
    await user.click(screen.getByRole("option", { name: /Package publish/ }));
    await user.keyboard("{Escape}");
    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/ci-capabilities",
      {
        method: "PUT",
        body: JSON.stringify({ ciCapabilities: ["package-publish"] }),
      },
    );
  });

  afterEach(() => {
    api.mockReset();
    window.history.replaceState({}, "", "/");
  });

  it("heads the page with the repo, its per-state counts and its total", async () => {
    routeApi();
    renderPage();

    expect(
      screen.getByRole("heading", { name: "acme/widgets" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Waiting 2")).toBeInTheDocument();
    expect(screen.getByText("Running 1")).toBeInTheDocument();
    expect(screen.getByText("Blocked 1")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "3 tasks filed here" }),
    ).toHaveAttribute("href", "/?f=repo:any:acme/widgets");
    expect(screen.queryByText("task list")).not.toBeInTheDocument();
    await screen.findByLabelText("Default capabilities");
  });

  // grain/task-47: this page is what a Repos row opens, so it is headed
  // by the same ring that list and the nav entry above it carry
  // (ItemGlyph.jsx, docs/brand.md) -- beside the heading rather than
  // inside it, so the repo's name is still its whole accessible name.
  // docs/test-runs/2026-10-15-repository-release-memory-ui.md: the tab is
  // the address's hash, and a hash-only change remounts nothing.
  it("follows a change to the address's hash to the tab it names", async () => {
    routeApi();
    renderPage({ tab: "agents" });
    await screen.findByLabelText("Default capabilities");
    expect(screen.getByRole("tab", { name: "Agents" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    act(() => {
      window.location.hash = "#ci";
      window.dispatchEvent(new HashChangeEvent("hashchange"));
    });
    expect(screen.getByRole("tab", { name: "CI" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  it("heads the page with the repos glyph", async () => {
    routeApi();
    renderPage();

    expect(
      document.querySelector('.repo-page-header svg[data-glyph="repos"]'),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: "acme/widgets" }),
    ).toBeInTheDocument();
    await screen.findByLabelText("Default capabilities");
  });

  // grain/task-143: the page is about a repo that lives on GitHub, and
  // the heading is the way out to it.
  it("links the heading to the repo's own page on GitHub", async () => {
    routeApi();
    renderPage();

    const link = screen.getByRole("link", { name: "acme/widgets" });
    expect(link).toHaveAttribute("href", "https://github.com/acme/widgets");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    // Still a heading named for the repo, with the link inside it.
    expect(
      screen.getByRole("heading", { name: "acme/widgets" }),
    ).toContainElement(link);
    await screen.findByLabelText("Default capabilities");
  });

  it("loads the repo's branches and capabilities on landing, with no button to press first", async () => {
    routeApi({
      branches: [
        {
          name: "myfeat",
          status: "created",
          createdAt: "2026-01-01T00:00:00Z",
        },
      ],
    });
    renderPage();

    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/branches");
    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/capabilities");
    expect((await screen.findByText("myfeat")).closest("li")).toHaveTextContent(
      "myfeat -- created",
    );
  });

  // A repo off the default host is routed with its host in the owner
  // segment (repoApi.js), not split into owner git.forge.example and
  // name experimental -- which the server refuses, leaving the page empty.
  it("loads a host-qualified repo through its host-qualified owner", async () => {
    routeApi();
    const depot = "git.forge.example/experimental/bsalmon-test";
    const root = "/api/repos/git.forge.example%2Fexperimental/bsalmon-test";
    renderPage({ repo: depot });

    await waitFor(() => expect(api).toHaveBeenCalledWith(`${root}/branches`));
    for (const route of [
      "capabilities",
      "prompt-extension",
      "setup-command",
      "workflows",
    ]) {
      expect(api).toHaveBeenCalledWith(`${root}/${route}`);
    }
    expect(
      api.mock.calls.filter(([path]) =>
        path.startsWith("/api/repos/git.forge.example/"),
      ),
    ).toEqual([]);
  });

  // grain/task-372: a branch is a kind with a figure of its own now, and
  // this is the one page that keeps a list of them -- so the section
  // heading carries it, and so does every branch under it.
  it("glyphs the branches section and each branch in it", async () => {
    routeApi({
      branches: [
        {
          name: "myfeat",
          status: "created",
          createdAt: "2026-01-01T00:00:00Z",
        },
      ],
    });
    renderPage({ tab: "branches" });

    const heading = await screen.findByText("Branches", { selector: "h6" });
    expect(heading.querySelector('svg[data-glyph="branches"]')).toBeTruthy();
    expect(
      (await screen.findByText("myfeat"))
        .closest("li")
        .querySelector('svg[data-glyph="branches"]'),
    ).toBeTruthy();
  });

  // grain/task-176: a name already on the repo is added by adopting the
  // ref that is there rather than by cutting one, and the list says which
  // of the two happened -- the status the reconciler landed on, verbatim.
  it("says a branch was adopted rather than created", async () => {
    routeApi({
      branches: [
        {
          name: "myfeat",
          status: "adopted",
          createdAt: "2026-01-01T00:00:00Z",
        },
      ],
    });
    renderPage();

    expect((await screen.findByText("myfeat")).closest("li")).toHaveTextContent(
      "myfeat -- adopted",
    );
  });

  it("goes back to the repo list", async () => {
    routeApi();
    const onBack = vi.fn();
    const user = userEvent.setup();
    renderPage({ onBack });

    await user.click(screen.getByRole("button", { name: /Repos/ }));

    expect(onBack).toHaveBeenCalled();
  });

  it("files a new task against this repo and opens its releases", async () => {
    routeApi();
    const onNewTask = vi.fn();
    const onOpenReleases = vi.fn();
    const user = userEvent.setup();
    renderPage({ onNewTask, onOpenReleases });

    await user.click(screen.getByRole("button", { name: "New task" }));
    await user.click(screen.getByRole("button", { name: "Releases" }));

    expect(onNewTask).toHaveBeenCalledWith("acme/widgets");
    expect(onOpenReleases).toHaveBeenCalledWith("acme/widgets");
  });

  it("adds a branch and refreshes the branch list on success", async () => {
    routeApi();
    const user = userEvent.setup();
    renderPage({ tab: "branches" });
    await screen.findByLabelText("Default capabilities");

    answerOnce(addingBranch, () =>
      Promise.resolve({
        repo: "acme/widgets",
        name: "myfeat",
        status: "pending",
        createdAt: "2026-01-01T00:00:00Z",
      }),
    );
    answerOnce(listingBranches, () =>
      Promise.resolve([
        {
          repo: "acme/widgets",
          name: "myfeat",
          status: "pending",
          createdAt: "2026-01-01T00:00:00Z",
        },
      ]),
    );

    await user.type(screen.getByPlaceholderText("feature/foo"), "myfeat");
    await user.click(screen.getByRole("button", { name: "Add branch" }));

    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/branches", {
      method: "POST",
      body: JSON.stringify({ name: "myfeat" }),
    });
    expect((await screen.findByText("myfeat")).closest("li")).toHaveTextContent(
      "myfeat -- pending",
    );
  });

  it("lists one row per branch name, the newest, even if the store holds two", async () => {
    routeApi({
      branches: [
        {
          name: "walk/topic-1004",
          status: "pending",
          createdAt: "2026-10-04T10:01:00Z",
        },
        {
          name: "walk/topic-1004",
          status: "pending",
          error: "boom",
          createdAt: "2026-10-04T10:00:00Z",
        },
      ],
    });
    renderPage();

    const row = (await screen.findByText("walk/topic-1004")).closest("li");
    expect(row).toHaveTextContent("walk/topic-1004 -- pending");
    expect(row).not.toHaveTextContent("boom");
    expect(screen.getAllByText("walk/topic-1004")).toHaveLength(1);
  });

  it("offers the repository's own branches to compare, not only the ones grain created", async () => {
    clearBranchCache();
    routeApi({
      branches: [
        {
          name: "walk/topic-1004",
          status: "pending",
          createdAt: "2026-10-04T10:00:00Z",
        },
      ],
    });
    const listed = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      !opts && path.endsWith("/github-branches")
        ? Promise.resolve(["main", "release/1.0"])
        : listed(path, opts),
    );
    const user = userEvent.setup();
    const props = renderPage({ tab: "branches" });
    await screen.findByText("walk/topic-1004");

    await user.click(screen.getByLabelText(/Head branch/));
    const offered = async () =>
      (await screen.findAllByRole("menuitem")).map((li) => li.textContent);
    await waitFor(async () =>
      expect(await offered()).toEqual(
        expect.arrayContaining(["main", "release/1.0", "walk/topic-1004"]),
      ),
    );
    await user.click(screen.getByRole("menuitem", { name: "release/1.0" }));
    await user.click(screen.getByRole("button", { name: "Compare" }));
    expect(props.onOpenComparison).toHaveBeenCalledWith({
      base: "main",
      head: "release/1.0",
    });
  });

  it("says so when the branch being added is already on the list", async () => {
    const existing = {
      repo: "acme/widgets",
      name: "walk/topic-1004",
      status: "pending",
      createdAt: "2026-10-04T10:00:00Z",
    };
    routeApi({ branches: [existing] });
    const user = userEvent.setup();
    renderPage({ tab: "branches" });
    await screen.findByText("walk/topic-1004");

    answerOnce(addingBranch, () =>
      Promise.resolve({ ...existing, alreadyRequested: true }),
    );
    await user.type(
      screen.getByPlaceholderText("feature/foo"),
      "walk/topic-1004",
    );
    await user.click(screen.getByRole("button", { name: "Add branch" }));

    expect(await screen.findByRole("status")).toHaveTextContent(
      "walk/topic-1004 is already on this repo's list (pending); nothing new was added.",
    );
    expect(screen.getAllByText("walk/topic-1004")).toHaveLength(1);
  });

  it("reports the error when adding a branch fails, without clearing the field", async () => {
    routeApi();
    const showError = vi.fn();
    const user = userEvent.setup();
    renderPage({ tab: "branches", showError });
    await screen.findByLabelText("Default capabilities");

    answerOnce(addingBranch, () =>
      Promise.reject(new Error("invalid branch name")),
    );

    await user.type(screen.getByPlaceholderText("feature/foo"), "bad name");
    await user.click(screen.getByRole("button", { name: "Add branch" }));

    expect(showError).toHaveBeenCalledWith(
      expect.objectContaining({ message: "invalid branch name" }),
    );
    expect(screen.getByPlaceholderText("feature/foo")).toHaveValue("bad name");
  });

  // grain/task-24: a repo's own default capability set is edited beside
  // the repo it belongs to rather than on the deployment-wide Settings
  // pane -- and the form says what a task filed against this repo would
  // actually start with, which is the union of both layers.
  it("says what a task filed against this repo starts with", async () => {
    routeApi({
      caps: {
        repo: "acme/widgets",
        defaultCapabilities: ["gcp-key"],
        deploymentDefaultCapabilities: ["gemini-key"],
        effectiveDefaultCapabilities: ["gemini-key", "gcp-key"],
      },
    });
    const config = {
      capabilities: [
        { id: "gcp-key", name: "GCP key" },
        { id: "gemini-key", name: "Gemini key" },
      ],
    };
    renderPage({ config });

    expect(
      await screen.findByText(
        /A task filed against acme\/widgets starts with:/,
      ),
    ).toHaveTextContent("Gemini key, GCP key");
  });

  // Both sets GET reports come back as stored, retired ids included, so
  // that one chosen before a build retired it can still be seen and
  // unticked. What a task starts with is the filtered union, though --
  // (*Client).defaultCapabilities drops a retired id before any grant is
  // written -- so this line must not list one.
  it("leaves a retired id out of what a task filed against the repo starts with", async () => {
    routeApi({
      caps: {
        repo: "acme/widgets",
        defaultCapabilities: ["gcp-key", "scratch-repo"],
        deploymentDefaultCapabilities: ["gemini-key", "old-deployment-key"],
        effectiveDefaultCapabilities: ["gemini-key", "gcp-key"],
      },
    });
    const config = {
      capabilities: [
        { id: "gcp-key", name: "GCP key" },
        { id: "gemini-key", name: "Gemini key" },
      ],
    };
    renderPage({ config });

    const line = await screen.findByText(
      /A task filed against acme\/widgets starts with:/,
    );
    expect(line).toHaveTextContent("Gemini key, GCP key");
    expect(line).not.toHaveTextContent("scratch-repo");
    expect(line).not.toHaveTextContent("old-deployment-key");
  });

  it("says a repo whose only defaults are retired ids starts with nothing", async () => {
    routeApi({
      caps: {
        repo: "acme/widgets",
        defaultCapabilities: ["scratch-repo"],
        deploymentDefaultCapabilities: ["old-deployment-key"],
        effectiveDefaultCapabilities: [],
      },
    });
    const config = { capabilities: [{ id: "gcp-key", name: "GCP key" }] };
    renderPage({ config });

    expect(
      await screen.findByText(
        /A task filed against acme\/widgets starts with:/,
      ),
    ).toHaveTextContent("nothing -- only what whoever files it ticks");
  });

  it("saves the repo's default capabilities and refreshes the config the new-task form seeds from", async () => {
    routeApi();
    const config = {
      capabilities: [
        { id: "gcp-key", name: "GCP key" },
        { id: "gemini-key", name: "Gemini key" },
      ],
    };
    const onRefreshConfig = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage({ config, onRefreshConfig });

    await user.click(await screen.findByLabelText("Default capabilities"));
    answerOnce(savingCapabilities, () =>
      Promise.resolve({
        repo: "acme/widgets",
        defaultCapabilities: ["gcp-key"],
        deploymentDefaultCapabilities: [],
        effectiveDefaultCapabilities: ["gcp-key"],
      }),
    );
    await user.click(await screen.findByRole("option", { name: "GCP key" }));
    await user.keyboard("{Escape}");

    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/capabilities", {
      method: "PUT",
      body: JSON.stringify({ defaultCapabilities: ["gcp-key"] }),
    });
    expect(onRefreshConfig).toHaveBeenCalled();
  });

  // grain/task-43: the per-repo set is reported as stored too, so a
  // capability retired since this repo named it arrives ticked with no
  // row in config.capabilities to untick it -- and PUT rejects the whole
  // set as "unknown capability" every time this form is saved with it
  // still there. Its own row is the only way out.
  it("offers a row for a stored repo default this build no longer lists", async () => {
    routeApi({
      caps: {
        repo: "acme/widgets",
        defaultCapabilities: ["gcp-key", "scratch-repo"],
        deploymentDefaultCapabilities: [],
        effectiveDefaultCapabilities: ["gcp-key"],
      },
    });
    const config = { capabilities: [{ id: "gcp-key", name: "GCP key" }] };
    const user = userEvent.setup();
    renderPage({ config });

    await user.click(await screen.findByLabelText("Default capabilities"));
    const retired = await screen.findByRole("option", { name: /scratch-repo/ });
    expect(retired).toHaveTextContent(
      "No longer offered -- untick to remove it",
    );

    answerOnce(savingCapabilities, () =>
      Promise.resolve({
        repo: "acme/widgets",
        defaultCapabilities: ["gcp-key"],
        deploymentDefaultCapabilities: [],
        effectiveDefaultCapabilities: ["gcp-key"],
      }),
    );
    await user.click(retired);
    await user.keyboard("{Escape}");

    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/capabilities", {
      method: "PUT",
      body: JSON.stringify({ defaultCapabilities: ["gcp-key"] }),
    });
  });

  it("reports the error when saving the repo's default capabilities fails", async () => {
    routeApi();
    const config = { capabilities: [{ id: "gcp-key", name: "GCP key" }] };
    const showError = vi.fn();
    const user = userEvent.setup();
    renderPage({ config, showError });
    await user.click(await screen.findByLabelText("Default capabilities"));

    answerOnce(savingCapabilities, () =>
      Promise.reject(new Error("unknown capability nope")),
    );
    await user.click(await screen.findByRole("option", { name: "GCP key" }));
    await user.keyboard("{Escape}");

    expect(showError).toHaveBeenCalledWith(
      expect.objectContaining({ message: "unknown capability nope" }),
    );
  });

  it("does not offer Remove for the config repo", async () => {
    routeApi();
    renderPage({
      tab: "advanced",
      config: {
        targetRepos: ["acme/widgets"],
        configRepo: "acme/widgets",
      },
    });

    expect(
      screen.queryByRole("button", { name: "Remove" }),
    ).not.toBeInTheDocument();
    await screen.findByLabelText("Default capabilities");
  });

  it("only offers Remove for a repo that is on the allowlist", async () => {
    routeApi();
    renderPage({ tab: "advanced", config: { targetRepos: ["acme/gadgets"] } });

    expect(
      screen.queryByRole("button", { name: "Remove" }),
    ).not.toBeInTheDocument();
    await screen.findByLabelText("Default capabilities");
  });

  it("removes the repo after confirmation and returns to the list", async () => {
    routeApi();
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const onBack = vi.fn();
    const onRefreshConfig = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage({
      tab: "advanced",
      config: { targetRepos: ["acme/widgets"] },
      onBack,
      onRefreshConfig,
    });

    await user.click(screen.getByRole("button", { name: "Remove" }));

    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets", {
      method: "DELETE",
    });
    expect(onRefreshConfig).toHaveBeenCalledTimes(1);
    expect(onBack).toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  // grain/task-411: removing a repo with work in it is allowed -- the
  // daemon parks that work rather than running it into a repo the list
  // no longer names -- so the confirmation is where an operator finds
  // out what the click costs. The count is the daemon's own, asked as
  // the very save this click makes (POST /api/settings/plan with the
  // repo dropped), because this page holds none of the work behind it.
  it("says what removing the repo would strand, in the daemon's own words", async () => {
    routeApi();
    const routed = api.getMockImplementation();
    const summary =
      "`acme/widgets` comes off this deployment's target repo list, so 2 tasks " +
      "still in play there are parked instead of dispatched.";
    api.mockImplementation((path, opts) => {
      if (path === "/api/settings/plan")
        return Promise.resolve({
          targetReposStranded: [
            { repo: "acme/widgets", tasks: ["t1", "t2"], summary },
          ],
        });
      return routed(path, opts);
    });
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const user = userEvent.setup();
    renderPage({
      tab: "advanced",
      config: { targetRepos: ["acme/widgets", "acme/gadgets"] },
    });
    await screen.findByLabelText("Default capabilities");

    await user.click(screen.getByRole("button", { name: "Remove" }));

    // Planned as the save it is: this repo gone, the rest of the list
    // exactly as it stands.
    expect(api).toHaveBeenCalledWith("/api/settings/plan", {
      method: "POST",
      body: JSON.stringify({ targetRepos: ["acme/gadgets"] }),
    });
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining(summary));
    // Told, not stopped: the removal still goes through.
    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets", {
      method: "DELETE",
    });
    vi.unstubAllGlobals();
  });

  // A daemon too old to plan settings, or one whose settings have never
  // been saved, answers that request with an error -- which must cost
  // this click nothing, since it never depended on a plan before.
  it("still removes the repo when the plan cannot be had", async () => {
    routeApi();
    const routed = api.getMockImplementation();
    api.mockImplementation((path, opts) => {
      if (path === "/api/settings/plan")
        return Promise.reject(new Error("not found"));
      return routed(path, opts);
    });
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const showError = vi.fn();
    const user = userEvent.setup();
    renderPage({
      tab: "advanced",
      config: { targetRepos: ["acme/widgets"] },
      showError,
    });
    await screen.findByLabelText("Default capabilities");

    await user.click(screen.getByRole("button", { name: "Remove" }));

    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets", {
      method: "DELETE",
    });
    // And the failed plan is not reported as though the removal failed.
    expect(showError).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  it("does not remove the repo when the confirmation is declined", async () => {
    routeApi();
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(false));
    const onBack = vi.fn();
    const onRefreshConfig = vi.fn();
    const user = userEvent.setup();
    renderPage({
      tab: "advanced",
      config: { targetRepos: ["acme/widgets"] },
      onBack,
      onRefreshConfig,
    });
    await screen.findByLabelText("Default capabilities");

    await user.click(screen.getByRole("button", { name: "Remove" }));

    expect(api).not.toHaveBeenCalledWith("/api/repos/acme/widgets", {
      method: "DELETE",
    });
    expect(onRefreshConfig).not.toHaveBeenCalled();
    expect(onBack).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  // A repo listed only because it carries defaults of its own has no
  // tasks and no allowlist entry to explain why this page exists at all.
  it("says why a repo with nothing but defaults of its own is known here", async () => {
    routeApi();
    const config = {
      targetRepos: [],
      repoDefaultCapabilities: { "acme/orphan": ["gcp-key"] },
      capabilities: [{ id: "gcp-key", name: "GCP key" }],
    };
    renderPage({ repo: "acme/orphan", config });

    expect(screen.getByText(/known here only because it/)).toBeInTheDocument();
    await screen.findByLabelText("Default capabilities");
  });

  // grain/task-114: a repo's own standing instructions, edited here for
  // the same reason its capabilities are -- on the page of the repo they
  // belong to -- and shown alongside the deployment-wide text they are
  // appended to, which is the thing somebody writing them is writing
  // after.
  it("loads this repo's own prompt extension and the deployment text it is appended to", async () => {
    routeApi({
      prompt: {
        repo: "acme/widgets",
        promptExtension: "Migrations live in db/.",
        deploymentPromptExtension: "Run `make lint` before you push.",
        effectivePromptExtension:
          "Run `make lint` before you push.\n\nMigrations live in db/.",
      },
    });
    renderPage();

    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/prompt-extension",
    );
    expect(
      await screen.findByLabelText(/Prompt extension for acme\/widgets/),
    ).toHaveValue("Migrations live in db/.");
    expect(
      screen.getByText(/Run `make lint` before you push./),
    ).toBeInTheDocument();
  });

  // grain/task-340: a repository may declare a prompt extension of its
  // own in `.grain/settings.yml`, and grain appends it to the operator's
  // text here rather than letting it replace it. This pane is where that
  // operator text is edited, so the declared half and the composition
  // both have to be visible from it -- text being added to by a file
  // nobody can see from here is how a setting comes to look broken.
  it("shows the prompt extension this repo declares for itself and what a run is actually told", async () => {
    routeApi({
      prompt: {
        repo: "acme/widgets",
        promptExtension: "Migrations live in db/.",
        deploymentPromptExtension: "Run `make lint` before you push.",
        declaredPromptExtension: "Regenerate the client after editing api/.",
        effectivePromptExtension:
          "Run `make lint` before you push.\n\nMigrations live in db/." +
          "\n\nRegenerate the client after editing api/.",
      },
    });
    renderPage();

    await screen.findByLabelText(/Prompt extension for acme\/widgets/);
    expect(
      screen.getByText(/Declared by acme\/widgets itself in .grain\/settings/),
    ).toHaveTextContent("Regenerate the client after editing api/.");
    // And said to be neither editable here nor the operator's to
    // replace: the two things somebody looking at an unexpected line in
    // a run's prompt needs to know next.
    expect(
      screen.getByText(/Declared by acme\/widgets itself in .grain\/settings/),
    ).toHaveTextContent(/added after your text rather than replacing it/);
    expect(
      screen.getByText(/What a run against acme\/widgets is told/),
    ).toHaveTextContent(
      "Run `make lint` before you push. Migrations live in db/. " +
        "Regenerate the client after editing api/.",
    );
  });

  // Nearly every repository declares nothing, and an empty row for the
  // absent case would be noise on all of their pages.
  it("says nothing about a declared prompt extension for a repo that declares none", async () => {
    routeApi({
      prompt: {
        repo: "acme/widgets",
        promptExtension: "Migrations live in db/.",
        deploymentPromptExtension: "",
        effectivePromptExtension: "Migrations live in db/.",
      },
    });
    renderPage();

    await screen.findByLabelText(/Prompt extension for acme\/widgets/);
    expect(screen.queryByText(/Declared by acme\/widgets itself/)).toBeNull();
    expect(screen.queryByText(/What a run against acme\/widgets is told/)).toBe(
      null,
    );
  });

  it("says so when the deployment adds nothing of its own", async () => {
    routeApi();
    renderPage();

    await screen.findByLabelText(/Prompt extension for acme\/widgets/);
    expect(
      screen.getByText(/Deployment-wide, set in Settings/),
    ).toHaveTextContent("nothing");
  });

  // The refresh matters as much as the save: config.reposWithPromptExtension
  // is one of the three sources repoRows lists a repo from, so a repo
  // whose standing instructions are the only thing this deployment knows
  // it by is missing from the list page until the config is re-read --
  // and a repo that just had its last ones cleared stays on it.
  it("saves the repo's own prompt extension and refreshes the config the repo list is built from", async () => {
    routeApi();
    const onRefreshConfig = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage({ onRefreshConfig });

    const input = await screen.findByLabelText(
      /Prompt extension for acme\/widgets/,
    );
    answerOnce(savingPromptExtension, () =>
      Promise.resolve({
        repo: "acme/widgets",
        promptExtension: "Migrations live in db/.",
        deploymentPromptExtension: "",
        effectivePromptExtension: "Migrations live in db/.",
      }),
    );
    await user.type(input, "Migrations live in db/.");
    await user.tab();

    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/prompt-extension",
      {
        method: "PUT",
        body: JSON.stringify({ promptExtension: "Migrations live in db/." }),
      },
    );
    expect(onRefreshConfig).toHaveBeenCalled();
  });

  it("reports the error when saving the repo's prompt extension fails", async () => {
    routeApi();
    const showError = vi.fn();
    const user = userEvent.setup();
    renderPage({ showError });
    const input = await screen.findByLabelText(
      /Prompt extension for acme\/widgets/,
    );

    answerOnce(savingPromptExtension, () =>
      Promise.reject(new Error("store is down")),
    );
    await user.type(input, "something");
    await user.tab();

    expect(showError).toHaveBeenCalledWith(
      expect.objectContaining({ message: "store is down" }),
    );
  });

  // The setup command is a property of the repo's toolchain rather than
  // of any one task, so it belongs on this page for the reason the two
  // forms above it do -- and it is the only one whose effect a run sees
  // before its first turn (grain/task-154).
  it("loads this repo's setup command", async () => {
    routeApi({ setup: { repo: "acme/widgets", setupCommand: "make deps" } });
    renderPage();

    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/setup-command");
    expect(
      await screen.findByLabelText(/Setup command for acme\/widgets/),
    ).toHaveValue("make deps");
  });

  // The declared half of the setup command (grain/task-340), read-only
  // beside the box that writes the operator's half. Both run, in that
  // order, as one script -- so somebody wondering why an extra `npm ci`
  // is happening in their checkouts can see where it comes from without
  // opening the repository.
  it("shows the setup command this repo declares for itself", async () => {
    routeApi({
      setup: {
        repo: "acme/widgets",
        setupCommand: "make deps",
        declaredSetupCommand: "npm ci",
      },
    });
    renderPage();

    expect(
      await screen.findByLabelText(/Setup command for acme\/widgets/),
    ).toHaveValue("make deps");
    expect(
      screen.getByText(/declares one of its own in .grain\/settings/),
    ).toHaveTextContent("npm ci");
    expect(
      screen.getByText(/declares one of its own in .grain\/settings/),
    ).toHaveTextContent(/run after yours as the next line of the same script/);
  });

  it("says nothing about a declared setup command for a repo that declares none", async () => {
    routeApi({ setup: { repo: "acme/widgets", setupCommand: "make deps" } });
    renderPage();

    await screen.findByLabelText(/Setup command for acme\/widgets/);
    expect(screen.queryByText(/declares one of its own/)).toBeNull();
  });

  // Refreshing the config matters here for the same reason it does for
  // the prompt extension: config.reposWithSetupCommand is one of the
  // sources repoRows lists a repo from, so a repo whose setup command is
  // the only thing this deployment knows it by is missing from the list
  // page until the config is re-read.
  it("saves the repo's setup command and refreshes the config the repo list is built from", async () => {
    routeApi();
    const onRefreshConfig = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage({ onRefreshConfig });

    const input = await screen.findByLabelText(
      /Setup command for acme\/widgets/,
    );
    answerOnce(savingSetupCommand, () =>
      Promise.resolve({ repo: "acme/widgets", setupCommand: "make deps" }),
    );
    await user.type(input, "make deps");
    await user.tab();

    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/setup-command", {
      method: "PUT",
      body: JSON.stringify({ setupCommand: "make deps" }),
    });
    expect(onRefreshConfig).toHaveBeenCalled();
  });

  it("reports the error when saving the repo's setup command fails", async () => {
    routeApi();
    const showError = vi.fn();
    const user = userEvent.setup();
    renderPage({ showError });
    const input = await screen.findByLabelText(
      /Setup command for acme\/widgets/,
    );

    answerOnce(savingSetupCommand, () =>
      Promise.reject(new Error("store is down")),
    );
    await user.type(input, "make deps");
    await user.tab();

    expect(showError).toHaveBeenCalledWith(
      expect.objectContaining({ message: "store is down" }),
    );
  });

  // The CI workflows grain declares for this repo (grain/task-245).
  // This form is the only place they can be written for a repository
  // grain does not control, which is most of them.
  it("loads the workflows grain declares for this repo", async () => {
    routeApi({
      workflows: {
        repo: "acme/widgets",
        dir: ".grain/workflows",
        workflows: [
          {
            path: ".grain/workflows/tests.yml",
            text: "name: tests\non: push\n",
            name: "tests",
          },
        ],
      },
    });
    renderPage();

    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/workflows");
    expect(await screen.findByLabelText("File")).toHaveValue(
      ".grain/workflows/tests.yml",
    );
    expect(screen.getByLabelText("Workflow")).toHaveValue(
      "name: tests\non: push\n",
    );
  });

  // A half-written workflow row is held back from the blur-save on
  // purpose, and it used to go with the page without a word: only the
  // Releases tab's job boxes asked before "← Repos", the rail or Back
  // dropped them (docs/test-runs/2026-10-15-repo-tabs-standing-work.md,
  // finding 1).
  it("says a half-written or refused workflow row has something to lose, and a stored one does not", () => {
    const stored = [
      { path: ".grain/workflows/tests.yml", text: "name: tests\n" },
    ];
    expect(workflowEditsUnsaved([], [])).toBe(false);
    expect(workflowEditsUnsaved([{ path: "", text: "" }], [])).toBe(false);
    expect(
      workflowEditsUnsaved(
        [{ path: "tests.yml", text: "name: tests\n" }],
        stored,
      ),
    ).toBe(false);
    expect(workflowEditsUnsaved([{ path: "lint.yml", text: "" }], [])).toBe(
      true,
    );
    expect(
      workflowEditsUnsaved(
        [{ path: "tests.yml", text: "name: edited\n" }],
        stored,
      ),
    ).toBe(true);
    expect(workflowEditsUnsaved([], stored)).toBe(true);
  });

  it("asks before leaving with a half-written workflow row", async () => {
    const user = userEvent.setup();
    routeApi({
      ciRuns: {
        enabled: true,
        runs: [
          {
            id: "run-1",
            workflow: "tests",
            ref: "main",
            commit: "1234567",
            status: "completed",
            conclusion: "success",
            startedAt: "2026-01-01T00:00:00Z",
          },
        ],
      },
    });
    const props = renderPage({ tab: "ci" });
    await screen.findByText("grain declares no workflows for this repo.");
    expect(leavingLosesEdits()).toBe(false);
    await user.click(screen.getByRole("button", { name: "Add workflow" }));
    expect(leavingLosesEdits()).toBe(false);
    await user.type(screen.getByLabelText("File"), "lint.yml");
    expect(leavingLosesEdits()).toBe(true);

    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    await user.click(screen.getByRole("button", { name: "Releases" }));
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(props.onOpenReleases).not.toHaveBeenCalled();

    await openTab("Branches");
    await user.type(await screen.findByLabelText(/Head branch/), "feat/thing");
    await user.click(screen.getByRole("button", { name: "Compare" }));
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(props.onOpenComparison).not.toHaveBeenCalled();

    await openTab("Overview");
    await user.click(
      await screen.findByRole("button", { name: "All runs for this repo" }),
    );
    expect(confirm).toHaveBeenCalledTimes(3);
    expect(props.onOpenCIRun).not.toHaveBeenCalled();

    // Opening one run opens an overlay over RepoPage without unmounting it,
    // so it does not ask.
    await user.click(screen.getByRole("button", { name: "main" }));
    expect(confirm).toHaveBeenCalledTimes(3);
    expect(props.onOpenCIRun).toHaveBeenCalledWith("run-1", "acme/widgets");
    confirm.mockRestore();
  });

  it("says so when grain declares no workflows for this repo", async () => {
    routeApi();
    renderPage();

    expect(
      await screen.findByText("grain declares no workflows for this repo."),
    ).toBeInTheDocument();
  });

  // The whole set goes in one PUT, so adding one is the same request as
  // editing one -- and the refresh matters here for the reason it does
  // for the setup command: config.reposWithWorkflows is one of the
  // sources repoRows lists a repo from, and a repo grain declares
  // workflows for is the likeliest to have nothing else keeping it on
  // that list.
  it("adds a workflow and saves the whole set, refreshing the config the repo list is built from", async () => {
    routeApi({
      workflowSave: {
        repo: "acme/widgets",
        dir: ".grain/workflows",
        workflows: [
          {
            path: ".grain/workflows/tests.yml",
            text: "name: tests",
            name: "tests",
          },
        ],
      },
    });
    const onRefreshConfig = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage({ tab: "ci", onRefreshConfig });

    await user.click(
      await screen.findByRole("button", { name: "Add workflow" }),
    );
    await user.type(screen.getByLabelText("File"), "tests.yml");
    await user.type(screen.getByLabelText("Workflow"), "name: tests");
    await user.tab();

    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/workflows", {
      method: "PUT",
      body: JSON.stringify({
        workflows: [{ path: "tests.yml", text: "name: tests" }],
      }),
    });
    expect(onRefreshConfig).toHaveBeenCalled();
    // The path the server settled on comes back, so the box says where
    // the file actually is rather than what was typed.
    await waitFor(() =>
      expect(screen.getByLabelText("File")).toHaveValue(
        ".grain/workflows/tests.yml",
      ),
    );
  });

  // Removing is local until Save: the set is replaced wholesale, so a
  // row taken out of the form is a deletion that same Save makes real.
  it("deletes a workflow by leaving it out of the set it saves", async () => {
    routeApi({
      workflows: {
        repo: "acme/widgets",
        dir: ".grain/workflows",
        workflows: [
          { path: ".grain/workflows/tests.yml", text: "name: tests" },
        ],
      },
    });
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const user = userEvent.setup();
    renderPage({ tab: "ci" });

    answerOnce(savingWorkflows, () => Promise.resolve(noWorkflows));
    await user.click(
      await screen.findByRole("button", {
        name: "Remove workflow .grain/workflows/tests.yml",
      }),
    );

    expect(confirm).toHaveBeenCalledWith(
      expect.stringContaining(
        "Delete the workflow .grain/workflows/tests.yml grain declares for acme/widgets?",
      ),
    );
    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/workflows", {
      method: "PUT",
      body: JSON.stringify({ workflows: [] }),
    });
  });

  it("keeps a stored workflow when its removal is not confirmed", async () => {
    routeApi({
      workflows: {
        repo: "acme/widgets",
        dir: ".grain/workflows",
        workflows: [
          { path: ".grain/workflows/tests.yml", text: "name: tests" },
        ],
      },
    });
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(false));
    const user = userEvent.setup();
    renderPage({ tab: "ci" });

    await user.click(
      await screen.findByRole("button", {
        name: "Remove workflow .grain/workflows/tests.yml",
      }),
    );

    expect(api.mock.calls.filter(([p, o]) => savingWorkflows(p, o))).toEqual(
      [],
    );
    expect(screen.getByLabelText("File")).toHaveValue(
      ".grain/workflows/tests.yml",
    );
  });

  // grain/task-2414: removing went through the same save as editing, and
  // that save is held back while any row is half written -- so the row
  // vanished from the form, nothing was sent, and the workflow came back
  // on the next load.
  it("deletes a stored workflow even while another row is half written", async () => {
    routeApi({
      workflows: {
        repo: "acme/widgets",
        dir: ".grain/workflows",
        workflows: [
          { path: ".grain/workflows/tests.yml", text: "name: tests" },
        ],
      },
    });
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const user = userEvent.setup();
    renderPage({ tab: "ci" });

    await user.click(
      await screen.findByRole("button", { name: "Add workflow" }),
    );
    await user.type(screen.getAllByLabelText("File")[1], "half.yml");
    answerOnce(savingWorkflows, () => Promise.resolve(noWorkflows));
    await user.click(
      screen.getByRole("button", {
        name: "Remove workflow .grain/workflows/tests.yml",
      }),
    );

    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/workflows", {
      method: "PUT",
      body: JSON.stringify({ workflows: [] }),
    });
    await waitFor(() =>
      expect(screen.getAllByLabelText("File").map((b) => b.value)).toEqual([
        "half.yml",
      ]),
    );
  });

  // grain/task-2414: leaving a box for Add workflow saves on the blur,
  // and the save's answer used to replace every row -- the new empty row
  // included -- so the button seemed to do nothing.
  it("keeps the row Add workflow adds while a save is in flight", async () => {
    const stored = {
      repo: "acme/widgets",
      dir: ".grain/workflows",
      workflows: [{ path: ".grain/workflows/tests.yml", text: "name: tests" }],
    };
    routeApi({ workflows: stored });
    const onRefreshConfig = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage({ tab: "ci", onRefreshConfig });

    let answer;
    answerOnce(
      savingWorkflows,
      () => new Promise((resolve) => (answer = resolve)),
    );
    await user.type(await screen.findByLabelText("Workflow"), "s2");
    await user.click(screen.getByRole("button", { name: "Add workflow" }));
    expect(screen.getAllByLabelText("File")).toHaveLength(2);

    answer({
      ...stored,
      workflows: [
        { path: ".grain/workflows/tests.yml", text: "name: testss2" },
      ],
    });
    await waitFor(() => expect(onRefreshConfig).toHaveBeenCalled());
    expect(screen.getAllByLabelText("File").map((b) => b.value)).toEqual([
      ".grain/workflows/tests.yml",
      "",
    ]);
  });

  // A workflow that does not parse is refused by the server, with the
  // file and the key named -- the point of parsing on save is that the
  // message lands here, over the boxes still holding what was typed.
  it("reports the error when a workflow is refused, keeping what was typed", async () => {
    routeApi();
    const showError = vi.fn();
    const user = userEvent.setup();
    renderPage({ tab: "ci", showError });

    await user.click(
      await screen.findByRole("button", { name: "Add workflow" }),
    );
    answerOnce(savingWorkflows, () =>
      Promise.reject(
        new Error(
          "ci: .grain/workflows/tests.yml: a workflow declares at least one job",
        ),
      ),
    );
    await user.type(screen.getByLabelText("File"), "tests.yml");
    await user.type(screen.getByLabelText("Workflow"), "nope");
    await user.tab();

    expect(showError).toHaveBeenCalledWith(
      expect.objectContaining({
        message:
          "ci: .grain/workflows/tests.yml: a workflow declares at least one job",
      }),
    );
    expect(screen.getByLabelText("File")).toHaveValue("tests.yml");
  });

  // What those workflows have actually run (grain/task-351), under the
  // form that writes them. The list itself is RepoRuns' own test; what
  // this page owes it is a place on the page and the way out of a row.
  // Comparing two branches opens the pull request those two refs name
  // (PullRequestPage.jsx, grain/task-375) rather than unfolding a diff
  // under the form: this page is about the repository, and a diff
  // somebody came to read is a destination of its own.
  it("opens the pull request for the two branches it is asked to compare", async () => {
    routeApi({ branches: [{ name: "topic/change", status: "adopted" }] });
    const { onOpenComparison } = renderPage({ tab: "branches" });
    const user = userEvent.setup();

    await user.type(
      await screen.findByLabelText(/Head branch/),
      "topic/change",
    );
    await user.click(screen.getByRole("button", { name: "Compare" }));

    expect(onOpenComparison).toHaveBeenCalledWith({
      base: "main",
      head: "topic/change",
    });
    // And nothing is read here: the comparison is the other page's.
    expect(api).not.toHaveBeenCalledWith(
      expect.stringContaining("/compare?"),
      undefined,
    );
  });

  it("shows this repo's own CI runs and opens one where a run can be read", async () => {
    routeApi({
      ciRuns: {
        enabled: true,
        runs: [
          {
            id: "9f2c1a",
            repo: "acme/widgets",
            ref: "grain/task-1",
            event: "push",
            startedAt: "2026-09-07T12:00:00Z",
            finishedAt: "2026-09-07T12:01:00Z",
            conclusion: "success",
            jobs: [{ workflow: "tests", jobId: "test", conclusion: "success" }],
          },
        ],
      },
    });
    const { onOpenCIRun } = renderPage();

    expect(
      await screen.findByRole("button", { name: "grain/task-1" }),
    ).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith("/api/ci/runs?repo=acme%2Fwidgets");

    await userEvent.click(screen.getByRole("button", { name: "grain/task-1" }));
    expect(onOpenCIRun).toHaveBeenCalledWith("9f2c1a", "acme/widgets");
  });

  // A repository's own templates, suites and schedules (grain/task-60).
  // The switch is the whole of what this deployment decides about them,
  // so the page has to show both halves: whether it is on, and what has
  // actually come of reading the repository -- which is the part a tick
  // in a box cannot say.
  it("turns on reading this repo's own declarations", async () => {
    routeApi();
    const user = userEvent.setup();
    const props = renderPage();

    const box = await screen.findByLabelText(
      "Read acme/widgets's own declarations",
    );
    expect(box).not.toBeChecked();
    await user.click(box);

    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/declarations", {
      method: "PUT",
      body: JSON.stringify({ declarations: true }),
    });
    expect(props.onRefreshConfig).toHaveBeenCalled();
  });

  it("says what grain last made of this repo's declarations", async () => {
    routeApi({
      declarations: {
        repo: "acme/widgets",
        declarations: true,
        declarationsStatus: {
          commit: "0f1e2d3c4b5a69788796",
          checkedAt: "2026-03-04T12:00:00Z",
          templates: [".grain/templates/nightly.yml"],
          schedules: [".grain/schedules/nightly.yml"],
          triggers: [".grain/triggers/design-docs.yml"],
        },
      },
    });
    renderPage();

    expect(
      await screen.findByText(
        /Read at 0f1e2d3c4b5a: 1 template\(s\), 0 suite\(s\), 1 schedule\(s\), 1 trigger\(s\)/,
      ),
    ).toBeInTheDocument();
    // A Processes trigger row opens this tab (§5.1), so the file it
    // names is listed here by path rather than only counted.
    const triggers = document.querySelector(".declared-triggers");
    expect(triggers).toHaveTextContent(
      "Path triggers: .grain/triggers/design-docs.yml",
    );
  });

  it("shows the warnings the last declarations read left on the merge rules", async () => {
    routeApi({
      declarations: {
        repo: "acme/widgets",
        declarations: true,
        declarationsStatus: {
          commit: "0f1e2d3c4b5a69788796",
          checkedAt: "2026-03-04T12:00:00Z",
          mergeRules: ".grain/merge-rules.yml",
          warnings: [
            "both .grain/merge-rules.yml and .grain/sensitive.yml exist; sensitive.yml is ignored",
          ],
        },
      },
    });
    renderPage();

    expect(
      await screen.findByText(/merge rules in \.grain\/merge-rules\.yml/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Warning: both .*sensitive\.yml is ignored/),
    ).toBeInTheDocument();
  });

  // The merge queue's strict rule (grain/task-198). Off is the default
  // and the cheap answer, so the pane has to say what turning it on
  // costs -- a suite per open branch per merge -- beside the tick that
  // buys it.
  it("turns on requiring branches to be up to date before merging", async () => {
    routeApi();
    const user = userEvent.setup();
    const props = renderPage();

    const box = await screen.findByLabelText(
      "Require branches to be up to date before merging into acme/widgets",
    );
    expect(box).not.toBeChecked();
    expect(
      screen.getByText(/every branch open against acme\/widgets is re-tested/),
    ).toBeInTheDocument();
    await user.click(box);

    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/require-branch-up-to-date",
      {
        method: "PUT",
        body: JSON.stringify({ requireBranchUpToDate: true }),
      },
    );
    expect(props.onRefreshConfig).toHaveBeenCalled();
    expect(box).toBeChecked();
  });

  it("shows the strict rule as on for a repo that has it on", async () => {
    routeApi({
      upToDate: { repo: "acme/widgets", requireBranchUpToDate: true },
    });
    renderPage();

    expect(
      await screen.findByLabelText(
        "Require branches to be up to date before merging into acme/widgets",
      ),
    ).toBeChecked();
  });

  // Merge when green (grain/task-545): off by default, the strict lane.
  it("turns on merging green pull requests without waiting in line", async () => {
    routeApi();
    const user = userEvent.setup();
    const props = renderPage();

    const box = await screen.findByLabelText(
      "Merge pull requests into acme/widgets when green and clean",
    );
    expect(box).not.toBeChecked();
    await user.click(box);

    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/merge-on-green", {
      method: "PUT",
      body: JSON.stringify({ mergeOnGreen: true }),
    });
    expect(props.onRefreshConfig).toHaveBeenCalled();
    expect(box).toBeChecked();
  });

  it("shows merge when green as on for a repo that has it on", async () => {
    routeApi({
      upToDate: { repo: "acme/widgets", mergeOnGreen: true },
    });
    renderPage();

    expect(
      await screen.findByLabelText(
        "Merge pull requests into acme/widgets when green and clean",
      ),
    ).toBeChecked();
  });

  it("turns on base-branch repair human-gate bypass and explains the risk", async () => {
    routeApi();
    const user = userEvent.setup();
    const props = renderPage();

    const box = await screen.findByLabelText(
      "Base-branch repair PRs in acme/widgets may merge without a human when their CI is green",
    );
    expect(box).not.toBeChecked();
    expect(
      screen.getByText(
        /Risk: an agent repairing a broken base branch can land changes to workflow declarations or trust-boundary code without human pre-merge sign-off/,
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
    expect(props.onRefreshConfig).toHaveBeenCalled();
    expect(box).toBeChecked();
  });

  it("declares grain CI this repo's gate and reports the save", async () => {
    const user = userEvent.setup();
    routeApi();
    const props = renderPage();

    const box = await screen.findByLabelText(
      "Make grain CI the gate for acme/widgets and stop reading GitHub's checks",
    );
    expect(box).not.toBeChecked();
    // The two sentences that make this an honest switch rather than a
    // silent one: which provider judges this repository, and what
    // turning it on means for a repository that has no grain CI.
    expect(
      screen.getByText(/are not a source of verdicts for/),
    ).toBeInTheDocument();
    expect(screen.getByText(/merges nothing/)).toBeInTheDocument();
    // The CI mode is what grants grain-ci (task 2015): the caption must
    // not send an operator off to attach the capability by hand.
    expect(screen.queryByText(/do not hold the/)).not.toBeInTheDocument();
    expect(screen.getAllByText(/declares no workflows/).length).toBeGreaterThan(
      0,
    );
    await user.click(box);

    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/ci-mode", {
      method: "PUT",
      body: JSON.stringify({ ciMode: "grain" }),
    });
    expect(props.onRefreshConfig).toHaveBeenCalled();
    expect(box).toBeChecked();
  });

  it("shows the grain CI gate as on for a repo that has declared it", async () => {
    routeApi({
      upToDate: { repo: "acme/widgets", ciMode: "grain" },
    });
    renderPage();

    expect(
      await screen.findByLabelText(
        "Make grain CI the gate for acme/widgets and stop reading GitHub's checks",
      ),
    ).toBeChecked();
  });

  // The default-branch watch's explanation names the repo mid-sentence;
  // JSX drops the line break before {repo}, so the space has to be
  // written out (2026-10-06 walkthrough read "It needsacme/widgets's").
  it("keeps the space before the repo in the default-branch watch's note", async () => {
    routeApi();
    renderPage();

    await screen.findByLabelText(
      "Watch acme/widgets's default branch with grain CI even when grain has not merged into it",
    );
    expect(document.body.textContent).toContain(
      "It needs acme/widgets’s CI mode set to",
    );
    expect(document.body.textContent).not.toContain("needsacme");
  });

  // The checks nothing pushed to a branch here clears (grain/task-291).
  // A list, not a tick -- and the sentence beside it has to say that this
  // holds the merge exactly as hard as before, since an operator who
  // reads it as an exemption would reach for it for a flaky job.
  it("declares a check nothing pushed to this repo can clear", async () => {
    const user = userEvent.setup();
    routeApi();
    const props = renderPage({ tab: "merge-queue" });

    expect(
      await screen.findByText(/grain treats every check acme\/widgets reports/),
    ).toBeInTheDocument();
    expect(screen.getByText(/This does not exempt them/)).toBeInTheDocument();

    await user.type(
      screen.getByLabelText("Check that cannot pass"),
      "cla/google",
    );
    await user.click(screen.getByRole("button", { name: "Add" }));

    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/unfixable-checks",
      {
        method: "PUT",
        body: JSON.stringify({ unfixableChecks: ["cla/google"] }),
      },
    );
    expect(props.onRefreshConfig).toHaveBeenCalled();
    expect(await screen.findByText("cla/google")).toBeInTheDocument();
    // The box is emptied by the save, so a second name is typed into a
    // clear field rather than appended to the last one.
    expect(screen.getByLabelText("Check that cannot pass")).toHaveValue("");
  });

  it("withdraws a check this repo had declared unfixable", async () => {
    const user = userEvent.setup();
    routeApi({
      unfixable: {
        ...noUnfixable,
        unfixableChecks: ["cla/google", "license/cla"],
      },
    });
    renderPage();

    await user.click(
      await screen.findByTitle("Stop declaring cla/google unfixable"),
    );

    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/unfixable-checks",
      {
        method: "PUT",
        body: JSON.stringify({ unfixableChecks: ["license/cla"] }),
      },
    );
    await waitFor(() =>
      expect(screen.queryByText("cla/google")).not.toBeInTheDocument(),
    );
  });

  // grain CI's own verdict is never covered by a declaration
  // (forge.Unfixable.Covers), so the pane says why instead of writing a
  // row that would sit there looking like a setting.
  it("refuses to declare grain CI's own verdict unfixable, and says why", async () => {
    const user = userEvent.setup();
    routeApi();
    renderPage({ tab: "merge-queue" });

    await user.type(
      await screen.findByLabelText("Check that cannot pass"),
      "grain ci",
    );
    await user.click(screen.getByRole("button", { name: "Add" }));

    expect(
      await screen.findByText(/judges one exact commit on the branch/),
    ).toBeInTheDocument();
    expect(api).not.toHaveBeenCalledWith(
      "/api/repos/acme/widgets/unfixable-checks",
      expect.objectContaining({ method: "PUT" }),
    );
  });

  it("turns away a check this repo already declares, in any case", async () => {
    const user = userEvent.setup();
    routeApi({
      unfixable: { ...noUnfixable, unfixableChecks: ["cla/google"] },
    });
    renderPage({ tab: "merge-queue" });

    await user.type(
      await screen.findByLabelText("Check that cannot pass"),
      "CLA/Google",
    );
    await user.click(screen.getByRole("button", { name: "Add" }));

    expect(
      await screen.findByText(/already declares CLA\/Google unfixable/),
    ).toBeInTheDocument();
    expect(api).not.toHaveBeenCalledWith(
      "/api/repos/acme/widgets/unfixable-checks",
      expect.objectContaining({ method: "PUT" }),
    );
  });

  it("says why the last read of this repo's declarations failed", async () => {
    routeApi({
      declarations: {
        repo: "acme/widgets",
        declarations: true,
        declarationsStatus: {
          commit: "0f1e2d3c4b5a69788796",
          checkedAt: "2026-03-04T12:00:00Z",
          error: "nightly.yml: line 3: did not find expected key",
        },
      },
    });
    renderPage();

    // The last good commit's declarations keep running, and the page
    // says so rather than leaving it to be inferred (grain/task-1829).
    expect(
      await screen.findByText(
        /Last read failed: nightly.yml: line 3.*still running what 0f1e2d3c4b5a declared/,
      ),
    ).toBeInTheDocument();
  });

  it("still counts what the last good commit declared when a read fails", async () => {
    routeApi({
      declarations: {
        repo: "acme/widgets",
        declarations: true,
        declarationsStatus: {
          commit: "0f1e2d3c4b5a69788796",
          checkedAt: "2026-03-04T12:00:00Z",
          error: ".grain/triggers/broken.yml: yaml: line 2",
          templates: [".grain/templates/nightly.yml"],
          suites: [".grain/suites/smoke.yml"],
          schedules: [".grain/schedules/nightly.yml"],
          triggers: [".grain/triggers/payments.yml"],
          mergeRules: ".grain/merge-rules.yml",
        },
      },
    });
    renderPage();

    // A failed read must not hide the merge rules and schedules still in
    // force (docs/test-runs/2026-10-23-sessions-worktree-and-pinned-nav.md).
    expect(
      await screen.findByText(
        "Still running from 0f1e2d3c4b5a: 1 template(s), 1 suite(s), 1 schedule(s), 1 trigger(s), merge rules in .grain/merge-rules.yml",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText(/^Last read failed:/)).toBeInTheDocument();
  });

  it("says nothing is still running when a failed read has no good commit behind it", async () => {
    routeApi({
      declarations: {
        repo: "acme/widgets",
        declarations: true,
        declarationsStatus: {
          checkedAt: "2026-03-04T12:00:00Z",
          error: "nightly.yml: line 3: did not find expected key",
        },
      },
    });
    renderPage();

    expect(await screen.findByText(/^Last read failed:/)).toBeInTheDocument();
    expect(screen.queryByText(/^Still running from/)).not.toBeInTheDocument();
  });

  it("puts each refused file of a failed declaration read on a line of its own", async () => {
    routeApi({
      declarations: {
        repo: "acme/widgets",
        declarations: true,
        declarationsStatus: {
          commit: "0f1e2d3c4b5a69788796",
          checkedAt: "2026-03-04T12:00:00Z",
          error:
            'reading acme/widgets\'s declarations: .grain/schedules/a.yml: no workflow "nosuch"\n' +
            ".grain/schedules/b.yml: a schedule that runs a workflow cannot overlap",
        },
      },
    });
    renderPage();

    const line = await screen.findByText(/Last read failed: reading acme/);
    expect(line.textContent).toContain(
      'no workflow "nosuch"\n.grain/schedules/b.yml: a schedule that runs a workflow cannot overlap',
    );
    expect(getComputedStyle(line).whiteSpace).toBe("pre-line");
  });

  // grain/task-3224: a reader who administers no repository is refused
  // every write on this page by the server, so the page says why once
  // and draws its controls disabled.
  describe("for a reader who administers no repository", () => {
    const readOnlyConfig = {
      adminScoped: true,
      adminRepos: [],
    };
    const refusal =
      "you can read acme/widgets's settings but not change them: that needs a repository admin";
    const routes = {
      unfixable: { ...noUnfixable, unfixableChecks: ["cla/google"] },
      workflows: {
        ...noWorkflows,
        workflows: [{ path: ".grain/workflows/tests.yml", text: "jobs: {}" }],
      },
    };
    const radioName = /Make grain CI the gate for acme\/widgets/;

    it("draws every setting disabled and says why", async () => {
      routeApi(routes);
      renderPage({ config: readOnlyConfig });

      expect(await screen.findByText(refusal)).toBeInTheDocument();
      await openTab("CI");
      const radio = await screen.findByRole("radio", { name: radioName });
      expect(radio).toBeDisabled();
      expect(radio).toHaveAccessibleDescription(refusal);
      expect(
        screen.getByRole("radio", { name: "Use GitHub CI for acme/widgets" }),
      ).toBeDisabled();
      expect(
        screen.getByRole("radio", { name: "Use No CI for acme/widgets" }),
      ).toBeDisabled();
      await openTab("Merge queue");
      expect(
        screen.getByRole("checkbox", {
          name: /Require branches to be up to date/,
        }),
      ).toBeDisabled();
      await openTab("CI");
      expect(
        screen.getByRole("checkbox", { name: /Watch acme\/widgets's default/ }),
      ).toBeDisabled();
      await openTab("Advanced");
      expect(
        await screen.findByRole("checkbox", {
          name: "Read acme/widgets's own declarations",
        }),
      ).toBeDisabled();
      await openTab("CI");
      expect(await screen.findByLabelText("CI capabilities")).toHaveAttribute(
        "aria-disabled",
        "true",
      );
      await openTab("Agents");
      expect(screen.getByLabelText("Default capabilities")).toHaveAttribute(
        "aria-disabled",
        "true",
      );
      expect(
        screen.getByLabelText("Prompt extension for acme/widgets"),
      ).toBeDisabled();
      expect(
        screen.getByLabelText("Setup command for acme/widgets"),
      ).toBeDisabled();
      await openTab("Branches");
      expect(screen.getByLabelText("Branch name")).toBeDisabled();
      expect(screen.getByRole("button", { name: "Add branch" })).toBeDisabled();
      await openTab("Merge queue");
      expect(
        await screen.findByLabelText("Check that cannot pass"),
      ).toBeDisabled();
      expect(
        screen.queryByTitle("Stop declaring cla/google unfixable"),
      ).not.toBeInTheDocument();
      await openTab("CI");
      expect(
        await screen.findByRole("button", { name: "Add workflow" }),
      ).toBeDisabled();
      expect(
        screen.getByRole("button", { name: /Remove workflow/ }),
      ).toBeDisabled();
      expect(screen.getByLabelText("Workflow")).toBeDisabled();
      await openTab("Releases");
      expect(
        await screen.findByRole("button", {
          name: "the repository default: on",
        }),
      ).toBeDisabled();

      // Comparing two branches writes nothing, and stays offered.
      await openTab("Branches");
      expect(screen.getByRole("button", { name: "Compare" })).toBeEnabled();

      expect(
        api.mock.calls.filter(([, opts]) => opts?.method === "PUT"),
      ).toEqual([]);
    });

    it("offers everything to an admin of the repository", async () => {
      routeApi(routes);
      renderPage({
        tab: "ci",
        config: { ...readOnlyConfig, adminRepos: ["acme/*"] },
      });
      expect(
        await screen.findByRole("radio", { name: radioName }),
      ).toBeEnabled();
      expect(screen.queryByText(/but not change them/)).not.toBeInTheDocument();
      expect(
        await screen.findByRole("button", { name: "Add workflow" }),
      ).toBeEnabled();
    });
  });

  // grain/task-3681: a member is no viewer, but every setting on this
  // page is still checkRepoPerson's, so a member who administers neither
  // the repository nor a team owning it was offered controls whose every
  // click came back 403. /api/config's adminRepos says which repositories
  // they do administer (repoAdministered).
  describe("for a member who is not a repository admin", () => {
    const memberConfig = {
      adminScoped: true,
      adminRepos: [],
    };
    const releaseSettings = {
      repo: "acme/widgets",
      enabled: true,
      available: false,
      secrets: [],
      baseBranchJobs: [],
      postMergeDispatchJobs: [],
      baseBranches: ["main"],
    };
    function routeMember() {
      routeApi({
        workflows: {
          ...noWorkflows,
          workflows: [{ path: ".grain/workflows/tests.yml", text: "jobs: {}" }],
        },
      });
      const otherwise = api.getMockImplementation();
      api.mockImplementation((path, opts) =>
        !opts && path === "/api/repos/acme/widgets/release"
          ? Promise.resolve(releaseSettings)
          : otherwise(path, opts),
      );
    }
    const refusal =
      "you can read acme/widgets's settings but not change them: that needs a repository admin";

    afterEach(() => localStorage.removeItem("grain.selectedTeam"));

    it("draws every setting disabled and says why", async () => {
      routeMember();
      renderPage({ config: memberConfig });

      expect(await screen.findByText(refusal)).toBeInTheDocument();
      await openTab("CI");
      const radio = await screen.findByRole("radio", {
        name: "Use GitHub CI for acme/widgets",
      });
      expect(radio).toBeDisabled();
      // The reason is the control's own description, not only the
      // page's alert (2026-10-14 role-shaped sweep).
      expect(radio).toHaveAccessibleDescription(refusal);
      expect(
        screen.getByRole("checkbox", { name: /Watch acme\/widgets's default/ }),
      ).toBeDisabled();
      expect(
        await screen.findByRole("button", { name: "Add workflow" }),
      ).toBeDisabled();
      await openTab("Merge queue");
      expect(
        screen.getByRole("checkbox", {
          name: /Require branches to be up to date/,
        }),
      ).toBeDisabled();
      await openTab("Agents");
      expect(
        screen.getByLabelText("Prompt extension for acme/widgets"),
      ).toBeDisabled();
      expect(
        screen.getByLabelText("Setup command for acme/widgets"),
      ).toBeDisabled();
      await openTab("Branches");
      expect(screen.getByRole("button", { name: "Add branch" })).toBeDisabled();
      expect(
        screen.getByRole("button", { name: "Add branch" }),
      ).toHaveAccessibleDescription(refusal);
      // A field's own helper text stays part of its description.
      expect(screen.getByLabelText("Branch name")).toHaveAccessibleDescription(
        `${refusal} Created from the repo's current default branch, or adopted as it stands if it already exists`,
      );
      await openTab("Advanced");
      expect(
        await screen.findByRole("checkbox", {
          name: "Read acme/widgets's own declarations",
        }),
      ).toBeDisabled();
      await openTab("Releases");
      expect(
        await screen.findByRole("button", {
          name: "the repository default: on",
        }),
      ).toBeDisabled();
      expect(
        await screen.findByRole("button", {
          name: "Turn off automatic releases",
        }),
      ).toBeDisabled();
      expect(
        screen.getByRole("button", { name: "Run release by hand" }),
      ).toBeDisabled();
      expect(
        screen.getByRole("button", { name: "Run release by hand" }),
      ).toHaveAccessibleDescription(refusal);
      // Asking for a base branch's check is a repository admin's too
      // (handleRequestBaseBranchRun's requireRepoPerson).
      expect(
        screen.getByRole("button", { name: "Run the base-branch check now" }),
      ).toBeDisabled();

      expect(
        api.mock.calls.filter(([, opts]) => opts?.method === "PUT"),
      ).toEqual([]);
    });

    it("offers everything to a repository admin", async () => {
      routeMember();
      renderPage({
        tab: "ci",
        config: { ...memberConfig, adminRepos: ["acme/*"] },
      });
      expect(
        await screen.findByRole("radio", {
          name: "Use GitHub CI for acme/widgets",
        }),
      ).toBeEnabled();
      expect(
        screen.getByRole("radio", { name: "Use GitHub CI for acme/widgets" }),
      ).not.toHaveAttribute("aria-describedby");
      expect(screen.queryByText(/but not change them/)).not.toBeInTheDocument();
      expect(
        await screen.findByRole("button", { name: "Add workflow" }),
      ).toBeEnabled();
    });
  });

  it("displays an alert when there is an unjudged base branch gate for this repo", async () => {
    routeApi();
    renderPage({
      baseBranchGates: [
        {
          repo: "acme/widgets",
          branch: "main",
          judged: false,
          runID: "ci-run-123",
          detail:
            "Nothing is testing acme/widgets's base branch main, and the merge queue is merging onto it anyway.",
        },
        {
          repo: "other/repo",
          branch: "main",
          judged: false,
          detail: "Other repo warning",
        },
      ],
    });

    const alert = await screen.findByText(
      /Nothing is testing acme\/widgets's base branch main/,
    );
    expect(alert).toBeInTheDocument();
    expect(screen.queryByText("Other repo warning")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "View CI run" }),
    ).toBeInTheDocument();
  });

  // docs/test-runs/2026-10-12-base-gate-warnings.md: on the demo the
  // warning sat in the page header, which lowercases its text ("grain ci
  // is not enabled"), offered no way to the CI Mode that judges the
  // branch, and stayed up for a minute after that switch was thrown --
  // and the release pane's copy of it until the page was reloaded.
  it("points an unjudged gate's warning at CI Mode and clears it as soon as CI Mode is saved", async () => {
    const user = userEvent.setup();
    routeApi();
    const scrolled = vi.fn();
    const original = Element.prototype.scrollIntoView;
    Element.prototype.scrollIntoView = scrolled;
    onTestFinished(() => {
      Element.prototype.scrollIntoView = original;
    });
    const onRefreshGates = vi.fn(async () => {});
    renderPage({
      onRefreshGates,
      baseBranchGates: [
        {
          repo: "acme/widgets",
          judged: false,
          reason: "grain-ci-off",
          detail: "Nothing is testing acme/widgets's default branch.",
        },
      ],
    });

    const alert = (
      await screen.findByText(/Nothing is testing acme\/widgets's default/)
    ).closest(".MuiAlert-root");
    expect(alert).toHaveStyle({ textTransform: "none" });
    expect(
      screen.queryByRole("button", { name: "View CI run" }),
    ).not.toBeInTheDocument();
    await screen.findByLabelText(
      "Make grain CI the gate for acme/widgets and stop reading GitHub's checks",
    );
    await user.click(screen.getByRole("button", { name: "Go to CI Mode" }));
    expect(scrolled).toHaveBeenCalled();
    expect(scrolled.mock.contexts[0].id).toBe("repo-ci-mode");

    const releaseReads = () =>
      api.mock.calls.filter(
        ([path, opts]) => path === "/api/repos/acme/widgets/release" && !opts,
      ).length;
    const before = releaseReads();
    await user.click(
      screen.getByLabelText(
        "Make grain CI the gate for acme/widgets and stop reading GitHub's checks",
      ),
    );
    await waitFor(() => expect(onRefreshGates).toHaveBeenCalled());
    await waitFor(() => expect(releaseReads()).toBeGreaterThan(before));
  });

  // The release's work-ownership override and the "Automatic releases"
  // switch are one setting drawn twice (model.RunsWorkItem). Saving either
  // left the other saying what was true before until a reload: "release:
  // off" beside "Automatic releases on" (2026-10-13 walkthrough).
  it("keeps the release's ownership row and its automatic-release switch in step", async () => {
    const user = userEvent.setup();
    routeApi();
    let override = "";
    const ownership = () => ({
      ...ownsItsWork,
      items: [
        {
          item: "release",
          kind: "release",
          override,
          runs: override !== "off",
        },
      ],
    });
    const release = () => ({
      repo: "acme/widgets",
      enabled: override !== "off",
      available: false,
      secrets: [],
      baseBranchJobs: [],
      postMergeDispatchJobs: [],
      baseBranches: [],
    });
    const otherwise = api.getMockImplementation();
    api.mockImplementation((path, opts) => {
      if (/\/work-ownership$/.test(path)) {
        if (opts) override = JSON.parse(opts.body).state;
        return Promise.resolve(ownership());
      }
      if (path === "/api/repos/acme/widgets/release") {
        if (opts) override = JSON.parse(opts.body).enabled ? "on" : "off";
        return Promise.resolve(release());
      }
      return otherwise(path, opts);
    });
    renderPage();
    await openTab("Releases");

    await screen.findByText("Automatic releases on");
    await user.click(screen.getByRole("button", { name: "release: off" }));
    expect(
      await screen.findByText("Automatic releases off"),
    ).toBeInTheDocument();

    await user.click(
      screen.getByRole("button", { name: "Turn on automatic releases" }),
    );
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "release: on" }),
      ).toHaveAttribute("aria-pressed", "true"),
    );
  });
});
