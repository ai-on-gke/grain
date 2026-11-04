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
import SuiteRunOverlay from "./SuiteRunOverlay.jsx";
import api from "../api.js";
import { optionValues } from "../searchSelectTesting.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

describe("SuiteRunOverlay", () => {
  afterEach(() => api.mockReset());

  // grain/task-2910: the suite picker is a SearchSelect, so a word from
  // anywhere in a suite's name finds it.
  it("runs the suite a search of its name picked", async () => {
    api.mockResolvedValue({});
    const user = userEvent.setup();
    render(
      <SuiteRunOverlay
        suites={[
          { id: "s1", name: "Nightly hygiene" },
          { id: "s2", name: "Release readiness" },
          { id: "s3", name: "Docs sweep" },
        ]}
        repoOptions={["acme/widgets"]}
        config={{ defaultTarget: "acme/widgets" }}
        onClose={() => {}}
        showError={(err) => {
          throw err;
        }}
      />,
    );
    const picker = screen.getByRole("combobox", { name: /^Suite/ });
    expect(picker).toHaveValue("Nightly hygiene");
    await user.click(picker);
    await user.clear(picker);
    await user.type(picker, "readi");
    expect(optionValues(picker)).toEqual(["s2"]);
    await user.keyboard("{Enter}");
    await user.type(document.querySelector('input[name="base"]'), "main");
    await user.click(screen.getByRole("button", { name: /^Run/ }));

    const call = api.mock.calls.find(([path]) => path === "/api/suite-runs");
    expect(JSON.parse(call[1].body).suiteId).toBe("s2");
  });
});
