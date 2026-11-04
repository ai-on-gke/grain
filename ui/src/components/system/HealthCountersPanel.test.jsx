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

import { act, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import api from "../../api.js";
import HealthCountersPanel, {
  COUNTERS_REFRESH_MS,
  HealthCountersView,
} from "./HealthCountersPanel.jsx";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

describe("HealthCountersView", () => {
  it("says nothing when no layer registers counters", () => {
    const { container } = render(<HealthCountersView sections={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("shows each section's counters sorted by key", () => {
    render(
      <HealthCountersView
        sections={[
          {
            name: "sign-in verification",
            counters: { "verifier:refused": 2, "verifier:admitted": 1200 },
          },
          { name: "quiet layer", counters: {} },
        ]}
      />,
    );
    const [verification, quiet] = screen.getAllByTestId(
      "health-counters-section",
    );
    expect(verification).toHaveTextContent("sign-in verification");
    const rows = within(verification).getAllByRole("row").slice(1);
    expect(rows.map((r) => r.textContent)).toEqual([
      `verifier:admitted${(1200).toLocaleString()}`,
      "verifier:refused2",
    ]);
    expect(quiet).toHaveTextContent("Nothing counted yet.");
  });
});

describe("HealthCountersPanel", () => {
  afterEach(() => {
    vi.useRealTimers();
    api.mockReset();
  });

  it("re-reads the counters section while open and keeps them across a failed read", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const section = (n) => ({
      counters: [{ name: "layer", counters: { "v:admitted": n } }],
    });
    api
      .mockResolvedValueOnce(section(1))
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce(section(3));
    render(<HealthCountersPanel />);
    expect(await screen.findByTestId("health-counters")).toHaveTextContent(
      "v:admitted1",
    );
    expect(api).toHaveBeenCalledWith("/api/sandboxes?section=counters");
    await act(() => vi.advanceTimersByTimeAsync(COUNTERS_REFRESH_MS));
    expect(screen.getByTestId("health-counters")).toHaveTextContent(
      "v:admitted1",
    );
    await act(() => vi.advanceTimersByTimeAsync(COUNTERS_REFRESH_MS));
    expect(api).toHaveBeenCalledTimes(3);
    expect(screen.getByTestId("health-counters")).toHaveTextContent(
      "v:admitted3",
    );
  });
});
