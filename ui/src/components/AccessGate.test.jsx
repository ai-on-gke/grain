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

import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Button } from "@mui/material";
import AccessGate from "./AccessGate.jsx";
import DetailOverlay from "./DetailOverlay.jsx";
import { RunDetail } from "./CIPage.jsx";
import BoardsPage from "./BoardsPage.jsx";
import TemplateOverlay from "./TemplateOverlay.jsx";
import SettingsPage from "./SettingsPage.jsx";
import { AccessConfigProvider } from "../AccessContext.jsx";
import { PreferencesProvider } from "../PreferencesContext.jsx";
import api from "../api.js";
import { viewerConfig, widgetsConfig } from "../accessTestConfigs.js";
import { resetDetailMocks, baseTask } from "./detail/testFixtures.js";

vi.mock("../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../attachments.js", () => ({ default: vi.fn() }));

// refusal is the tooltip a refused control's wrapper wears, or null for
// a control drawn as it always was.
function refusal(button) {
  const gate = button.closest("[data-access-denied]");
  return gate ? gate.getAttribute("title") : null;
}

function withAccess(config, node) {
  return <AccessConfigProvider config={config}>{node}</AccessConfigProvider>;
}

function detail(config, task) {
  return render(
    withAccess(
      config,
      <DetailOverlay
        task={task}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    ),
  );
}

describe("AccessGate", () => {
  it("draws its control as it is for an unlimited caller", () => {
    render(
      <AccessGate scope="tasks:write">
        <Button>Go</Button>
      </AccessGate>,
    );
    const button = screen.getByRole("button", { name: "Go" });
    expect(button).toBeEnabled();
    expect(refusal(button)).toBeNull();
  });

  it("disables a refused control and names the scope and role", () => {
    render(
      withAccess(
        viewerConfig,
        <AccessGate scope="tasks:write">
          <Button title="Does the thing">Go</Button>
        </AccessGate>,
      ),
    );
    const button = screen.getByRole("button", { name: "Go" });
    expect(button).toBeDisabled();
    expect(refusal(button)).toBe(
      "Needs tasks:write, which your role grain/viewer does not grant.",
    );
  });

  it("leaves a refused control out with hide", () => {
    render(
      withAccess(
        viewerConfig,
        <AccessGate scope="tasks:write" hide>
          <Button>Go</Button>
        </AccessGate>,
      ),
    );
    expect(screen.queryByRole("button", { name: "Go" })).toBeNull();
  });

  it("moves a refused control's margin onto its wrapper, not both", () => {
    render(
      withAccess(
        viewerConfig,
        <AccessGate scope="tasks:write" wrapperStyle={{ marginTop: -8 }}>
          <Button sx={{ mt: -1 }}>Go</Button>
        </AccessGate>,
      ),
    );
    const button = screen.getByRole("button", { name: "Go" });
    expect(button.closest(".access-gate").style.marginTop).toBe("-8px");
    expect(button.style.margin).toBe("0px");
  });
});

describe("task actions for a signed-in role", () => {
  afterEach(resetDetailMocks);

  it("disables a grain/viewer's edit, approve and close, saying why", () => {
    detail(viewerConfig, {
      ...baseTask,
      repo: "acme/widgets",
      state: "proposed",
    });
    const approve = screen.getByRole("button", { name: "Approve" });
    expect(approve).toBeDisabled();
    expect(refusal(approve)).toBe(
      "Needs tasks:admin, which your role grain/viewer does not grant.",
    );
    for (const name of ["Edit", "Close", "Defer", "Pause", "Run now"]) {
      const button = screen.getByRole("button", { name });
      expect(button).toBeDisabled();
      expect(refusal(button)).toMatch(
        /^Needs \S+, which your role grain\/viewer/,
      );
    }
    // Reading is still for everybody.
    expect(screen.getByRole("button", { name: "Prompt" })).toBeEnabled();
  });

  it("disables a grain/viewer's retry and re-run CI on a failed task", () => {
    detail(viewerConfig, {
      ...baseTask,
      repo: "acme/widgets",
      state: "failed",
      ci: { conclusion: "failure", run: "r1" },
    });
    expect(screen.getByRole("button", { name: "Retry" })).toBeDisabled();
    const rerun = screen.getByRole("button", { name: "Re-run CI" });
    expect(rerun).toBeDisabled();
    expect(refusal(rerun)).toBe(
      "Needs ci:write, which your role grain/viewer does not grant.",
    );
  });

  it("enables a repository-limited role's actions only on a repository it reaches", () => {
    const { unmount } = detail(widgetsConfig, {
      ...baseTask,
      repo: "acme/widgets",
      state: "failed",
    });
    expect(screen.getByRole("button", { name: "Retry" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Edit" })).toBeEnabled();
    unmount();

    detail(widgetsConfig, {
      ...baseTask,
      repo: "acme/gadgets",
      state: "failed",
    });
    const retry = screen.getByRole("button", { name: "Retry" });
    expect(retry).toBeDisabled();
    expect(refusal(retry)).toBe(
      "Needs tasks:write on acme/gadgets: your role widgets-contributor holds it only on acme/widgets.",
    );
  });
});

describe("CI run actions for a signed-in role", () => {
  const run = (over = {}) => ({
    id: "r1",
    repo: "acme/widgets",
    ref: "main",
    startedAt: "2026-09-07T12:00:00Z",
    jobs: [],
    ...over,
  });

  it("disables a grain/viewer's cancel and retry", () => {
    const { unmount } = render(
      withAccess(
        viewerConfig,
        <RunDetail
          run={run()}
          zone="UTC"
          onCancel={vi.fn()}
          onRetry={vi.fn()}
        />,
      ),
    );
    const cancel = screen.getByRole("button", { name: "Cancel run" });
    expect(cancel).toBeDisabled();
    expect(refusal(cancel)).toBe(
      "Needs ci:write, which your role grain/viewer does not grant.",
    );
    unmount();

    render(
      withAccess(
        viewerConfig,
        <RunDetail
          run={run({
            conclusion: "cancelled",
            finishedAt: "2026-09-07T12:04:00Z",
          })}
          zone="UTC"
          onCancel={vi.fn()}
          onRetry={vi.fn()}
        />,
      ),
    );
    expect(screen.getByRole("button", { name: "Retry" })).toBeDisabled();
  });

  it("enables a limited role's cancel on its repository only", () => {
    const { unmount } = render(
      withAccess(
        widgetsConfig,
        <RunDetail
          run={run()}
          zone="UTC"
          onCancel={vi.fn()}
          onRetry={vi.fn()}
        />,
      ),
    );
    expect(screen.getByRole("button", { name: "Cancel run" })).toBeEnabled();
    unmount();
    render(
      withAccess(
        widgetsConfig,
        <RunDetail
          run={run({ repo: "acme/gadgets" })}
          zone="UTC"
          onCancel={vi.fn()}
          onRetry={vi.fn()}
        />,
      ),
    );
    expect(screen.getByRole("button", { name: "Cancel run" })).toBeDisabled();
  });
});

describe("boards, templates and settings for a grain/viewer", () => {
  afterEach(() => api.mockReset());

  it("disables New board, which files a task", () => {
    render(
      withAccess(
        viewerConfig,
        <BoardsPage boards={[]} onOpenBoard={vi.fn()} onNewBoard={vi.fn()} />,
      ),
    );
    for (const button of screen.getAllByRole("button", { name: "New board" })) {
      expect(button).toBeDisabled();
      expect(refusal(button)).toBe(
        "Needs tasks:write, which your role grain/viewer does not grant.",
      );
    }
  });

  it("disables a template's Save and Delete", () => {
    render(
      withAccess(
        viewerConfig,
        <TemplateOverlay
          template={{
            id: "template-1",
            name: "Dependency bump",
            title: "Bump dependencies",
            description: "",
            repo: "",
            reads: [],
            writes: [],
            capabilities: [],
            revision: 1,
          }}
          config={viewerConfig}
          onClose={() => {}}
          onSaved={vi.fn()}
          showError={vi.fn()}
        />,
      ),
    );
    for (const name of ["Save", "Delete"]) {
      const button = screen.getByRole("button", { name });
      expect(button).toBeDisabled();
      expect(refusal(button)).toBe(
        "Needs templates:write, which your role grain/viewer does not grant.",
      );
    }
  });

  it("draws a settings section read-only, saying which scope and role", async () => {
    api.mockImplementation((path) =>
      Promise.resolve(
        path === "/api/settings"
          ? { configured: true, promptExtension: "Run make test." }
          : {},
      ),
    );
    render(
      <PreferencesProvider>
        <SettingsPage
          config={viewerConfig}
          section="agents"
          onSection={vi.fn()}
          showError={vi.fn()}
        />
      </PreferencesProvider>,
    );
    const box = await screen.findByLabelText(
      "Standing instructions for every run",
    );
    expect(box).toBeDisabled();
    expect(screen.getByTestId("settings-role-read-only")).toHaveTextContent(
      "Needs settings:write, which your role grain/viewer does not grant.",
    );
    expect(box.closest("[data-settings-readonly]")).toHaveAttribute(
      "title",
      "Needs settings:write, which your role grain/viewer does not grant.",
    );
  });
});
