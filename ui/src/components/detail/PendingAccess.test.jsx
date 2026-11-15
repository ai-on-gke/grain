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
import PendingAccess, { itemText } from "./PendingAccess.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));

const pending = (over = {}) => ({
  summary: "Grant write to acme/lib so the fix lands in both",
  reason: "The bug is in the shared library.",
  items: [
    { kind: "read", repo: "acme/docs" },
    { kind: "write", repo: "acme/lib", base: "dev" },
    {
      kind: "grant",
      capability: "api:tasks:admin",
      grant: "api:tasks:admin",
      dangerous: true,
    },
  ],
  holds: { reads: [], writes: [], grants: ["self-debug"] },
  ...over,
});

const act = vi.fn(async (fn) => fn());

describe("PendingAccess", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it("renders nothing when no request is pending", () => {
    const { container } = render(<PendingAccess t={{ id: "7" }} act={act} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("lists each item, the reason and the dangerous mark, and grants all", async () => {
    const user = userEvent.setup();
    render(
      <PendingAccess t={{ id: "7", pendingAccess: pending() }} act={act} />,
    );
    expect(
      screen.getByText("The bug is in the shared library."),
    ).toBeInTheDocument();
    expect(
      screen.getByText("acme/lib › write · base: dev"),
    ).toBeInTheDocument();
    expect(screen.getByText("dangerous")).toBeInTheDocument();
    expect(screen.getByTestId("access-holds")).toHaveTextContent("1 grant");

    await user.click(screen.getByRole("button", { name: "Grant all" }));
    expect(api).toHaveBeenCalledWith("/api/tasks/7/access-request/grant", {
      method: "POST",
      body: "{}",
    });
  });

  it("grants only the ticked items", async () => {
    const user = userEvent.setup();
    render(
      <PendingAccess t={{ id: "7", pendingAccess: pending() }} act={act} />,
    );
    await user.click(
      screen.getByRole("checkbox", {
        name: "Grant acme/lib › write · base: dev",
      }),
    );
    await user.click(screen.getByRole("button", { name: "Grant 2 of 3" }));
    expect(api).toHaveBeenCalledWith("/api/tasks/7/access-request/grant", {
      method: "POST",
      body: JSON.stringify({ items: [0, 2] }),
    });
  });

  it("asks why before denying, and shows an earlier denial", async () => {
    const user = userEvent.setup();
    render(
      <PendingAccess
        t={{
          id: "7",
          pendingAccess: pending({
            earlier: [
              {
                denied: true,
                reason: "use the vendored copy",
                at: "x",
                by: { id: "alice" },
              },
            ],
          }),
        }}
        act={act}
      />,
    );
    expect(screen.getByTestId("access-earlier")).toHaveTextContent(
      "use the vendored copy",
    );
    await user.click(screen.getByRole("button", { name: "Deny" }));
    const deny = screen.getByRole("button", { name: "Deny request" });
    expect(deny).toBeDisabled();
    await user.type(
      screen.getByRole("textbox", { name: /Why/ }),
      "not this sprint",
    );
    await user.click(deny);
    expect(api).toHaveBeenCalledWith("/api/tasks/7/access-request/deny", {
      method: "POST",
      body: JSON.stringify({ reason: "not this sprint" }),
    });
  });

  it("offers the substitute for an item that cannot be granted, and grants it", async () => {
    const user = userEvent.setup();
    const ref = "https://grain.dev/capabilities/grain#ci";
    const sub =
      'https://grain.dev/capabilities/api#ci-read={"repo":"acme/lib"}';
    render(
      <PendingAccess
        t={{
          id: "7",
          pendingAccess: pending({
            items: [
              {
                kind: "grant",
                capability: ref,
                error: "it cannot be attached by hand",
                substitute: {
                  kind: "grant",
                  capability: "https://grain.dev/capabilities/api#ci-read",
                  with: { repo: "acme/lib" },
                },
                substituteGrant: sub,
              },
            ],
          }),
        }}
        act={act}
      />,
    );
    expect(screen.getByText(`instead of ${ref}`)).toBeInTheDocument();
    expect(screen.getByTestId("access-item-error")).toHaveTextContent(
      "Grant gives this task",
    );
    expect(
      screen.getByRole("checkbox", { name: `Grant ${sub}` }),
    ).toBeChecked();
    await user.click(screen.getByRole("button", { name: "Grant all" }));
    expect(api).toHaveBeenCalledWith("/api/tasks/7/access-request/grant", {
      method: "POST",
      body: "{}",
    });
  });

  it("disables an item that cannot be granted and has no substitute", () => {
    render(
      <PendingAccess
        t={{
          id: "7",
          pendingAccess: pending({
            items: [
              {
                kind: "grant",
                capability: "no-such",
                error: "unknown capability",
              },
            ],
          }),
        }}
        act={act}
      />,
    );
    expect(
      screen.getByRole("checkbox", { name: "Grant no-such" }),
    ).toBeDisabled();
    expect(screen.getByRole("button", { name: "Grant 0 of 1" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Deny" })).toBeEnabled();
    expect(screen.getByText("unknown capability")).toBeInTheDocument();
  });

  it("names items the way the grants editor does", () => {
    expect(itemText({ kind: "read", repo: "acme/x" })).toBe("acme/x › read");
    expect(itemText({ kind: "network", access: "open" })).toBe(
      "network › open",
    );
  });

  it("shows an earlier partial grant with its declined items and server Principal ID", () => {
    render(
      <PendingAccess
        t={{
          id: "7",
          pendingAccess: pending({
            items: [
              {
                kind: "grant",
                capability: "api:tasks:admin",
                grant: "api:tasks:admin",
                dangerous: true,
              },
            ],
            earlier: [
              {
                granted: [
                  { kind: "read", repo: "acme/docs" },
                  { kind: "network", access: "open" },
                ],
                declined: [{ kind: "grant", capability: "api:tasks:admin" }],
                at: "2026-11-04T21:00:00Z",
                by: { Kind: "human", ID: "alice", Source: "access-code" },
              },
            ],
          }),
        }}
        act={act}
      />,
    );
    const alert = screen.getByTestId("access-earlier");
    expect(alert).toHaveTextContent(
      "Answered before by alice: 2 granted, 1 declined (api:tasks:admin).",
    );
  });
});
