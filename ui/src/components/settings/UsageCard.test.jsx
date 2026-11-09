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

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

import api from "../../api.js";
import UsageCard, { formatTokens } from "./UsageCard.jsx";

const report = {
  mode: "advisory",
  maxWorkers: 6,
  effectiveWorkers: 6,
  sustainableWorkers: 4,
  rollup: {
    runs: 12,
    billable: 2400000,
    inputTokens: 2000000,
    outputTokens: 400000,
    cacheReadTokens: 9000000,
    burnPerHour: 300000,
  },
  scopes: [
    {
      connection: "c1",
      connectionName: "cloudtop-antigravity",
      model: "opus-5.5-high",
      used: 1200000,
      limit: 3000000,
      limitSource: "learned",
      remaining: 1800000,
      resetAt: "2026-11-05T17:00:00Z",
      burnPerHour: 300000,
      sustainableWorkers: 4,
    },
  ],
  advisories: [
    "at this rate cloudtop-antigravity's opus-5.5-high budget runs out in ~6h; sustainable ≈ 4 workers",
  ],
  warnings: [],
  settings: {},
};

describe("formatTokens", () => {
  it("renders counts compactly", () => {
    expect(formatTokens(950)).toBe("950");
    expect(formatTokens(1234)).toBe("1.2k");
    expect(formatTokens(45000)).toBe("45k");
    expect(formatTokens(4500000)).toBe("4.5M");
  });
});

describe("UsageCard", () => {
  beforeEach(() => {
    api.mockReset();
    api.mockImplementation((path, options) => {
      if (path === "/api/usage") return Promise.resolve(report);
      if (path === "/api/usage/settings" && options?.method === "PUT") {
        const body = JSON.parse(options.body);
        return Promise.resolve({
          ...report,
          mode: body.mode,
          effectiveWorkers: 4,
          settings: body,
        });
      }
      return Promise.reject(new Error(`unexpected ${path}`));
    });
  });

  it("shows each connection's budget, the rollup and the advisory", async () => {
    render(<UsageCard showError={vi.fn()} />);
    const row = await screen.findByTestId("usage-row");
    expect(within(row).getByText("cloudtop-antigravity")).toBeTruthy();
    expect(within(row).getByText("opus-5.5-high")).toBeTruthy();
    expect(within(row).getByText("1.2M")).toBeTruthy();
    expect(within(row).getByText("3.0M~")).toBeTruthy();
    expect(within(row).getByText("1.8M")).toBeTruthy();
    expect(screen.getByTestId("usage-rollup").textContent).toContain(
      "12 runs recorded",
    );
    expect(screen.getByTestId("usage-workers").textContent).toContain(
      "sustains ≈ 4",
    );
    expect(screen.getByTestId("usage-advisory").textContent).toContain(
      "runs out in ~6h",
    );
  });

  it("saves the automatic mode", async () => {
    const user = userEvent.setup();
    render(<UsageCard showError={vi.fn()} />);
    await screen.findByTestId("usage-row");
    await user.click(screen.getByRole("combobox", { name: "Concurrency" }));
    await user.click(
      screen.getByRole("option", {
        name: "Automatic: cap workers at the estimate",
      }),
    );
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/usage/settings",
        expect.objectContaining({ method: "PUT" }),
      ),
    );
    const put = api.mock.calls.find((c) => c[0] === "/api/usage/settings");
    expect(JSON.parse(put[1].body)).toMatchObject({ mode: "auto" });
    await waitFor(() =>
      expect(screen.getByTestId("usage-workers").textContent).toContain(
        "Worker cap 4",
      ),
    );
  });
});
