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
import api from "../api.js";

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
});
