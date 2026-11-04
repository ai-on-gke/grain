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

import { render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import AuditSection from "./AuditSection.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

// answer routes each audit endpoint to its own reply.
function answer(replies) {
  api.mockImplementation(async (path) => {
    const key = Object.keys(replies).find((p) => path.startsWith(p));
    const reply = replies[key];
    if (reply instanceof Error) throw reply;
    return reply ?? [];
  });
}

const VERIFIED = {
  id: 2,
  at: "2026-11-03T10:00:00Z",
  actor: "alice@example.com",
  actorKind: "human",
  actorSource: "iap",
  actorLabel: "alice@example.com",
  surface: "repo:example/widgets",
  summary: "work ownership set to on",
};
const OPERATOR = {
  id: 1,
  at: "2026-11-02T09:00:00Z",
  actor: "operator",
  actorKind: "human",
  actorSource: "access-code",
  actorLabel: "operator (access code)",
  surface: "settings",
  summary: "default model changed",
};

describe("AuditSection", () => {
  afterEach(() => vi.resetAllMocks());

  it("labels a verified and an operator config change, newest first", async () => {
    answer({ "/api/deployment/config-audit": [VERIFIED, OPERATOR] });
    render(<AuditSection />);
    const table = await screen.findByTestId("config-audit-table");
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows.map((r) => r.dataset.testid)).toEqual([
      "config-audit-row-2",
      "config-audit-row-1",
    ]);
    const verified = screen.getByTestId("config-audit-row-2");
    expect(within(verified).getByText("alice@example.com")).toBeInTheDocument();
    expect(
      within(verified).getByText("repo:example/widgets"),
    ).toBeInTheDocument();
    expect(
      within(verified).getByText("work ownership set to on"),
    ).toBeInTheDocument();
    const operator = screen.getByTestId("config-audit-row-1");
    // The label, not the bare ID the row stores.
    expect(
      within(operator).getByText("operator (access code)"),
    ).toBeInTheDocument();
    expect(within(operator).queryByText("operator")).toBeNull();
    expect(within(operator).getByText("settings")).toBeInTheDocument();
    expect(
      within(operator).getByText("default model changed"),
    ).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith("/api/deployment/config-audit?limit=100");
  });

  it("shows team and secret audit principals as stored", async () => {
    answer({
      "/api/deployment/audit-logs": [
        {
          timestamp: "2026-11-03T11:00:00Z",
          kind: "git",
          resource: "example/widgets",
          action: "fetch",
          credential: "github-app",
          outcome: "allowed",
          taskId: "42",
          principal: "",
        },
        {
          timestamp: "2026-11-03T10:30:00Z",
          kind: "secret",
          resource: "stripe-api-key",
          principal: "bob@example.com",
        },
      ],
      "/api/deployment/secret-audit": [
        {
          id: 7,
          timestamp: "2026-11-03T10:30:00Z",
          secretName: "stripe-api-key",
          key: "value",
          taskId: "42",
          runId: "r1",
          principal: "operator",
        },
      ],
    });
    render(<AuditSection />);
    const team = await screen.findByTestId("team-audit-table");
    expect(within(team).getByText("bob@example.com")).toBeInTheDocument();
    expect(within(team).getByText("fetch example/widgets")).toBeInTheDocument();
    expect(within(team).getByText("via github-app")).toBeInTheDocument();
    const secret = await screen.findByTestId("secret-audit-row-7");
    expect(within(secret).getByText("operator")).toBeInTheDocument();
    expect(
      within(secret).getByText("stripe-api-key/value"),
    ).toBeInTheDocument();
    expect(within(secret).getByText("task 42 (run r1)")).toBeInTheDocument();
    expect(await screen.findByTestId("config-audit-table-empty")).toBeVisible();
  });

  it("stacks each entry on a phone, leaving out empty columns", async () => {
    const original = window.matchMedia;
    window.matchMedia = (query) => ({
      matches: query.includes("max-width"),
      media: query,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
    });
    try {
      answer({
        "/api/deployment/config-audit": [OPERATOR],
        "/api/deployment/secret-audit": [
          { id: 7, timestamp: OPERATOR.at, secretName: "stripe-api-key" },
        ],
      });
      render(<AuditSection />);
      const table = await screen.findByTestId("config-audit-table");
      expect(table).toHaveAttribute("data-layout", "stacked");
      const row = within(table).getByTestId("config-audit-row-1");
      expect(within(row).getByText("Who")).toBeInTheDocument();
      expect(
        within(row).getByText("operator (access code)"),
      ).toBeInTheDocument();
      const secret = await screen.findByTestId("secret-audit-row-7");
      // No principal and no task: neither line is drawn.
      expect(within(secret).queryByText("Principal")).toBeNull();
      expect(within(secret).queryByText("Task")).toBeNull();
    } finally {
      window.matchMedia = original;
    }
  });

  it("keeps the other logs when one cannot be read", async () => {
    answer({
      "/api/deployment/config-audit": [OPERATOR],
      "/api/deployment/audit-logs": new Error("forbidden"),
      "/api/deployment/secret-audit": new Error("forbidden"),
    });
    render(<AuditSection />);
    expect(await screen.findByTestId("config-audit-row-1")).toBeInTheDocument();
    expect(
      await screen.findByTestId("team-audit-table-error"),
    ).toHaveTextContent("forbidden");
    expect(
      await screen.findByTestId("secret-audit-table-error"),
    ).toHaveTextContent("forbidden");
  });
});
