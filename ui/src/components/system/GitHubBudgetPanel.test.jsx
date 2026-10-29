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
import { describe, expect, it } from "vitest";
import { GitHubBudgetView } from "./GitHubBudgetPanel.jsx";

const now = Date.parse("2026-10-26T04:10:00Z");

function github(budget, callers = []) {
  return {
    enabled: true,
    budget: { limit: 5000, lowFraction: 0.15, ...budget },
    callers,
  };
}

describe("GitHubBudgetView", () => {
  it("says nothing until a response has reported a budget", () => {
    const { container } = render(
      <GitHubBudgetView github={{ enabled: true, budget: { limit: 0 } }} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("projects when the hour runs out and names who is spending it", () => {
    render(
      <GitHubBudgetView
        now={now}
        github={github(
          {
            remaining: 1650,
            resetsAt: "2026-10-26T04:48:00Z",
            spendPerHour: 3300,
            exhaustedAt: "2026-10-26T04:40:00Z",
          },
          [
            {
              caller: "sync",
              spent: 900,
              revalidated: 1500,
              kinds: [{ kind: "PR read", spent: 380 }],
            },
          ],
        )}
      />,
    );
    expect(screen.getByText(/1,650 of 5,000 calls left/)).toBeInTheDocument();
    expect(screen.getByText(/resets in 38m 0s/)).toBeInTheDocument();
    expect(screen.getByTestId("health-github-runs-out")).toHaveTextContent(
      "runs out in 30m 0s",
    );
    expect(screen.getByText("sync")).toBeInTheDocument();
    expect(screen.getByText("PR read (380)")).toBeInTheDocument();
  });

  it("says polling is cut back while the budget is low", () => {
    render(
      <GitHubBudgetView
        now={now}
        github={github({ remaining: 400, low: true })}
      />,
    );
    expect(screen.getByText(/Under 15% left/)).toBeInTheDocument();
    expect(screen.queryByTestId("health-github-runs-out")).toBeNull();
  });

  it("shows idle-poll backoff summary when pull requests are tracked", () => {
    render(
      <GitHubBudgetView
        now={now}
        github={{
          ...github({ remaining: 3200 }),
          polls: {
            tracked: 4,
            backedOff: 3,
            baseSeconds: 30,
            capSeconds: 480,
            medianSeconds: 120,
            maxSeconds: 480,
          },
        }}
      />,
    );
    expect(screen.getByTestId("health-github-polls")).toHaveTextContent(
      "Idle-poll backoff: 3 of 4 tracked PRs backed off · poll interval median 2m 0s, max 8m 0s (30s base, 8m 0s cap).",
    );
  });
});
