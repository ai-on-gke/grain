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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import SuiteOverlay from "./SuiteOverlay.jsx";
import { AccessConfigProvider } from "../AccessContext.jsx";
import { viewerConfig } from "../accessTestConfigs.js";
import api from "../api.js";
import {
  chips,
  chooseOptions,
  chosenValues,
  optionValues,
  removeOption,
} from "../searchSelectTesting.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

describe("SuiteOverlay", () => {
  beforeEach(() => {
    api.mockImplementation(() => Promise.resolve({}));
  });
  afterEach(() => {
    vi.restoreAllMocks();
    api.mockReset();
  });

  it("falls back to until_clean for unrecognized legacy suite modes without MUI Select warnings", async () => {
    const warnSpy = vi.spyOn(console, "warn");
    const errorSpy = vi.spyOn(console, "error");
    const onSaved = vi.fn(() => Promise.resolve());
    const onClose = vi.fn();
    const user = userEvent.setup();

    render(
      <SuiteOverlay
        suite={{
          id: "suite-legacy",
          name: "Legacy Qualify",
          mode: "once",
          items: [{ templateId: "template-1" }],
          revision: "rev-1",
        }}
        templates={[
          {
            id: "template-1",
            name: "Bump",
            title: "Bump deps",
            repo: "acme/widgets",
          },
        ]}
        onSaved={onSaved}
        onClose={onClose}
        showError={() => {}}
      />,
    );

    expect(
      screen.getByText("Run until no repo changes or new tasks"),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Max passes")).toHaveValue(5);

    const muiWarnings = [
      ...warnSpy.mock.calls.flat(),
      ...errorSpy.mock.calls.flat(),
    ].filter((msg) => String(msg).includes("out-of-range"));
    expect(muiWarnings).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Save" }));
    const patchCall = api.mock.calls.find(
      ([url, opts]) =>
        url === "/api/suites/suite-legacy" && opts?.method === "PATCH",
    );
    expect(patchCall).toBeTruthy();
    expect(JSON.parse(patchCall[1].body)).toMatchObject({
      name: "Legacy Qualify",
      mode: "until_clean",
      maxPasses: 5,
      revision: "rev-1",
    });
  });

  it("preserves count mode when suite.mode is count", () => {
    render(
      <SuiteOverlay
        suite={{
          id: "suite-count",
          name: "Count Suite",
          mode: "count",
          count: 3,
          items: [{ templateId: "template-1" }],
        }}
        templates={[
          {
            id: "template-1",
            name: "Bump",
            title: "Bump deps",
            repo: "acme/widgets",
          },
        ]}
        onSaved={() => Promise.resolve()}
        onClose={() => {}}
        showError={() => {}}
      />,
    );

    expect(screen.getByText("Run a fixed number of times")).toBeInTheDocument();
    expect(screen.getByLabelText("Number of times")).toHaveValue(3);
  });

  // A suite's templates are a list that grows with the deployment, so
  // they are searched (SearchSelect, grain/task-2578): every word of a
  // name, its title and repo shown beneath it, and a chip per pick that
  // takes it back off.
  it("searches, ticks and unticks templates", async () => {
    const user = userEvent.setup();
    render(
      <SuiteOverlay
        templates={[
          {
            id: "t-bump",
            name: "bump-deps",
            title: "Bump deps",
            repo: "acme/widgets",
          },
          { id: "t-lint", name: "lint-all", title: "Lint" },
          { id: "t-docs", name: "docs-check", title: "Docs" },
        ]}
        onSaved={() => Promise.resolve()}
        onClose={() => {}}
        showError={() => {}}
      />,
    );
    const box = screen.getByRole("combobox", { name: "Templates" });

    await user.click(box);
    const bump = await screen.findByRole("option", { name: /bump-deps/ });
    expect(bump).toHaveTextContent("Bump deps -- acme/widgets");
    expect(bump).toHaveAttribute("aria-selected", "false");

    await user.type(box, "deps bump");
    expect(optionValues(box)).toEqual(["t-bump"]);
    await user.clear(box);

    await chooseOptions(user, box, ["t-lint", "docs-check"]);
    expect(chosenValues(box)).toEqual(["t-lint", "t-docs"]);
    // The list stays open while rows are ticked, and says which are.
    expect(screen.getByRole("option", { name: /lint-all/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await user.keyboard("{Escape}");

    await removeOption(user, box, "t-lint");
    expect(chosenValues(box)).toEqual(["t-docs"]);
    expect(chips(box).map((c) => c.textContent)).toEqual(["docs-check"]);

    await user.type(screen.getByLabelText("Name"), "Gate");
    await user.click(screen.getByRole("button", { name: "Add suite" }));
    const post = api.mock.calls.find(
      ([url, opts]) => url === "/api/suites" && opts?.method === "POST",
    );
    expect(post).toBeTruthy();
    expect(JSON.parse(post[1].body).templateIds).toEqual(["t-docs"]);
  });

  // A grain/viewer may open a suite but not change it: Delete, Save and
  // "+ New template" are drawn disabled with the role's reason, as every
  // other write on the Processes page is (AccessGate.jsx) -- this pane
  // was missed when the rest were gated (2026-11-07 walkthrough).
  it("draws a suite's writes disabled for a role without suites:write", () => {
    render(
      <AccessConfigProvider config={viewerConfig}>
        <SuiteOverlay
          suite={{
            id: "suite-1",
            name: "Release qualification",
            mode: "count",
            count: 1,
            items: [{ templateId: "template-1" }],
          }}
          templates={[{ id: "template-1", name: "Bump", title: "Bump deps" }]}
          onSaved={() => Promise.resolve()}
          onClose={() => {}}
          showError={() => {}}
        />
      </AccessConfigProvider>,
    );
    for (const name of ["Delete", "Save", "+ New template"]) {
      expect(screen.getByRole("button", { name })).toBeDisabled();
    }
    expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
    const denied = [...document.querySelectorAll("[data-access-denied]")].map(
      (el) => el.getAttribute("data-access-denied"),
    );
    expect(denied.sort()).toEqual([
      "suites:write",
      "suites:write",
      "templates:write",
    ]);
  });
});
