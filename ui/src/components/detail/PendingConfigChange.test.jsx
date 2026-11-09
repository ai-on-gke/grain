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
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import PendingConfigChange from "./PendingConfigChange.jsx";
import { AccessConfigProvider } from "../../AccessContext.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));

const diff = `diff --git a/settings/templates.yml b/settings/templates.yml
index 1111111..2222222 100644
--- a/settings/templates.yml
+++ b/settings/templates.yml
@@ -1,2 +1,2 @@
 templates:
-  max_parallel: 2
+  max_parallel: 4
`;

const pending = (over = {}) => ({
  branch: "grain/config/42/raise-parallelism",
  baseSha: "a".repeat(40),
  headSha: "b".repeat(40),
  summary: "Raise max_parallel to 4",
  files: ["settings/templates.yml"],
  diff,
  validation: { ok: true, warnings: ["Secret gcp-keys/new-sa is not set yet"] },
  ...over,
});

// act runs what it is handed and swallows a rejection, the way App's does.
const act = vi.fn(async (fn) => {
  try {
    await fn();
    return true;
  } catch {
    return false;
  }
});

describe("PendingConfigChange", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it("renders nothing when no change is pending", () => {
    const { container } = render(
      <PendingConfigChange t={{ id: "42" }} act={act} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("shows the summary, file links, validation and approves the commit shown", async () => {
    const user = userEvent.setup();
    render(
      <PendingConfigChange
        t={{ id: "42", pendingConfigChange: pending() }}
        act={act}
      />,
    );

    expect(
      screen.getByText("Config change waiting for your approval"),
    ).toBeInTheDocument();
    expect(screen.getByText("Raise max_parallel to 4")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "settings/templates.yml" }),
    ).toHaveAttribute("href", "/config?path=settings%2Ftemplates.yml");
    expect(screen.getByTestId("config-change-validation")).toHaveTextContent(
      "Dry-run apply succeeded",
    );
    expect(
      screen.getByText("Secret gcp-keys/new-sa is not set yet"),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Approve & Apply" }));
    expect(api).toHaveBeenCalledWith("/api/tasks/42/config-change/approve", {
      method: "POST",
      body: JSON.stringify({ headSha: "b".repeat(40) }),
    });
  });

  it("rejects with the reason typed", async () => {
    const user = userEvent.setup();
    render(
      <PendingConfigChange
        t={{ id: "42", pendingConfigChange: pending() }}
        act={act}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Reject" }));
    await user.type(
      screen.getByLabelText("Why are you rejecting it?"),
      "too many runs",
    );
    await user.click(screen.getByRole("button", { name: "Reject change" }));
    expect(api).toHaveBeenCalledWith("/api/tasks/42/config-change/reject", {
      method: "POST",
      body: JSON.stringify({
        headSha: "b".repeat(40),
        reason: "too many runs",
      }),
    });
  });

  it("will not approve a change that failed validation", () => {
    render(
      <PendingConfigChange
        t={{
          id: "42",
          pendingConfigChange: pending({
            validation: { ok: false, error: "the change would lock dispatch" },
          }),
        }}
        act={act}
      />,
    );
    expect(screen.getByTestId("config-change-validation")).toHaveTextContent(
      "the change would lock dispatch",
    );
    expect(
      screen.getByRole("button", { name: "Approve & Apply" }),
    ).toBeDisabled();
  });

  it("replaces Schema valid and disables Approve & Apply after a 422 refusal, including on reload", async () => {
    const user = userEvent.setup();
    const conflict =
      "the change conflicts with the current main: rebase it onto origin/main and propose it again";
    const err = new Error(
      `the config change does not pass validation: ${conflict}`,
    );
    err.status = 422;
    err.validation = {
      check: { SchemaVersion: 0, Rows: null, Warnings: null },
    };
    api.mockRejectedValueOnce(err);

    const { rerender } = render(
      <PendingConfigChange
        t={{ id: "42", pendingConfigChange: pending() }}
        act={act}
      />,
    );
    expect(screen.getByTestId("config-change-validation")).toHaveTextContent(
      "Schema valid",
    );

    await user.click(screen.getByRole("button", { name: "Approve & Apply" }));

    const banner = screen.getByTestId("config-change-validation");
    expect(banner).not.toHaveTextContent("Schema valid");
    expect(banner).not.toHaveTextContent("Dry-run apply succeeded");
    expect(banner).toHaveTextContent("Stored validation no longer holds");
    expect(banner).toHaveTextContent(conflict);
    expect(banner).toHaveTextContent("Reject");
    expect(
      screen.getByRole("button", { name: "Approve & Apply" }),
    ).toBeDisabled();
    expect(screen.getByRole("button", { name: "Reject" })).not.toBeDisabled();

    // After a reload, the server's persisted stale validation renders the
    // same state without needing local refusal state.
    rerender(
      <PendingConfigChange
        t={{
          id: "42",
          pendingConfigChange: pending({
            validation: { ok: false, stale: true, error: conflict },
          }),
        }}
        act={act}
      />,
    );
    expect(screen.getByTestId("config-change-validation")).toHaveTextContent(
      "Stored validation no longer holds",
    );
    expect(
      screen.getByRole("button", { name: "Approve & Apply" }),
    ).toBeDisabled();
  });

  it("offers a role without settings:write neither decision, and says why", async () => {
    const user = userEvent.setup();
    const viewer = {
      access: {
        role: "grain/viewer",
        roles: ["grain/viewer"],
        full: false,
        scopes: ["settings:read", "tasks:read"],
      },
    };
    render(
      <AccessConfigProvider config={viewer}>
        <PendingConfigChange
          t={{ id: "42", pendingConfigChange: pending() }}
          act={act}
        />
      </AccessConfigProvider>,
    );
    const approve = screen.getByRole("button", { name: "Approve & Apply" });
    const reject = screen.getByRole("button", { name: "Reject" });
    expect(approve).toBeDisabled();
    expect(reject).toBeDisabled();
    expect(approve.closest("[data-access-denied]")).toHaveAttribute(
      "data-access-denied",
      "settings:write",
    );
    await user.hover(reject.closest("[data-access-denied]"));
    expect(
      await screen.findByText(
        "Needs settings:write, which your role grain/viewer does not grant.",
      ),
    ).toBeInTheDocument();
    expect(api).not.toHaveBeenCalled();
  });
});
