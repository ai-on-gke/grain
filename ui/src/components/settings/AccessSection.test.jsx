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

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import AccessSection, {
  actorName,
  validatePrincipalEntry,
} from "./AccessSection.jsx";
import api from "../../api.js";
import { TimeZoneProvider } from "../../TimeZoneContext.jsx";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

const section = (props = {}) => (
  <AccessSection showError={() => {}} {...props} />
);

describe("AccessSection", () => {
  beforeEach(() => {
    api.mockResolvedValue({ entries: [] });
  });
  afterEach(() => {
    api.mockReset();
    localStorage.clear();
  });

  it("says the access code signed this browser in, and logs out", async () => {
    const user = userEvent.setup();
    const onLogout = vi.fn();
    render(
      section({
        config: { authenticated: true, actor: "alice", authMethod: "header" },
        onLogout,
      }),
    );

    expect(
      screen.getByText("Signed in with the access code."),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Change password" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Log out" }));
    expect(onLogout).toHaveBeenCalled();
  });

  it("names the server-reported IAP actor, without its prefix, and logs out", async () => {
    const user = userEvent.setup();
    const onLogout = vi.fn();
    localStorage.setItem("grain.username", "alice");
    render(
      section({
        config: {
          authenticated: true,
          actor: "user:iap@example.com",
          authMethod: "iap",
        },
        onLogout,
      }),
    );

    expect(
      screen.getByText(/Signed in through IAP as iap@example\.com\./),
    ).toBeInTheDocument();
    expect(screen.queryByText(/alice/)).toBeNull();
    await user.click(screen.getByRole("button", { name: "Log out" }));
    expect(onLogout).toHaveBeenCalledTimes(1);
  });

  it("has no Username box, whatever this browser once stored", () => {
    localStorage.setItem("grain.username", "alice");
    for (const config of [
      {},
      { authenticated: false, actor: "debian" },
      { authenticated: true, actor: "alice" },
    ]) {
      const { unmount } = render(section({ config }));
      expect(screen.queryByLabelText("Username")).toBeNull();
      expect(screen.queryByText("alice")).toBeNull();
      unmount();
    }
  });

  it("shows a limited caller its role and no controls its role does not cover", async () => {
    api.mockImplementation(async (path) => {
      if (path === "/api/access/principals") {
        return {
          entries: [
            {
              entry: "user:viv@example.com",
              role: "grain/viewer",
              addedBy: "the access code",
              addedAt: "2026-11-03T05:00:00Z",
            },
          ],
          roles: [
            { name: "grain/operator", builtin: true, person: true },
            { name: "grain/viewer", builtin: true, person: false },
          ],
        };
      }
      return {};
    });
    render(
      section({
        config: {
          authenticated: true,
          actor: "user:viv@example.com",
          authMethod: "iap",
          access: {
            role: "grain/viewer",
            roles: ["grain/viewer"],
            full: false,
            scopes: ["access:read", "tasks:read"],
          },
        },
      }),
    );
    await screen.findByText("user:viv@example.com");
    expect(screen.getByTestId("access-own-role")).toHaveTextContent(
      "grain/viewer",
    );
    expect(screen.getByTestId("access-read-only")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Add allowed principal" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Remove user:viv@example.com" }),
    ).toBeNull();
    expect(screen.getByTestId("allowed-principals-table")).toHaveTextContent(
      "grain/viewer",
    );
  });

  it("asks a signed-in admin for a reason when a dangerous role is involved, confirms narrowing oneself, and shows the history", async () => {
    const user = userEvent.setup();
    const calls = [];
    let refusedSelf = false;
    api.mockImplementation(async (path, opts) => {
      if (path === "/api/access/principals" && !opts?.method) {
        return {
          entries: [
            { entry: "user:ops@example.com", role: "grain/operator" },
            { entry: "user:other@example.com", role: "grain/operator" },
            { entry: "group:eng@example.com", role: "grain/viewer" },
          ],
          roles: [
            { name: "grain/operator", builtin: true, person: true },
            { name: "grain/viewer", builtin: true, person: false },
            { name: "grain/contributor", builtin: true, person: false },
            { name: "contractors", person: false, denies: true },
          ],
        };
      }
      if (path === "/api/access/history") {
        return [
          {
            id: 7,
            at: "2026-11-09T10:00:00Z",
            actor: "ops@example.com",
            actorLabel: "ops@example.com",
            surface: "access",
            summary:
              "role user:other@example.com grain/viewer -> grain/operator (on call)",
          },
        ];
      }
      calls.push({ path, ...opts });
      if (opts?.method === "PATCH" && path.includes("ops%40") && !refusedSelf) {
        refusedSelf = true;
        const err = new Error(
          "user:ops@example.com matches you: confirm that you want to change its role grain/operator -> grain/viewer, which narrows your own access",
        );
        err.code = "confirm_self";
        err.status = 403;
        throw err;
      }
      return {};
    });
    render(
      section({
        config: {
          authenticated: true,
          actor: "user:ops@example.com",
          authMethod: "iap",
          access: {
            role: "grain/operator",
            roles: ["grain/operator"],
            full: true,
            scopes: ["access:read", "access:write"],
          },
        },
      }),
    );
    await screen.findByText("user:other@example.com");
    expect(await screen.findByTestId("access-history")).toHaveTextContent(
      "grain/viewer -> grain/operator (on call)",
    );

    // Removing another admin opens the reason field, and needs one.
    await user.click(
      screen.getByRole("button", { name: "Remove user:other@example.com" }),
    );
    expect(screen.getByTestId("access-change-panel")).toHaveTextContent(
      /give a reason/,
    );
    await user.click(screen.getByTestId("access-change-apply"));
    expect(screen.getByTestId("access-change-error")).toHaveTextContent(
      /Give a reason/,
    );
    expect(calls).toHaveLength(0);
    await user.type(screen.getByTestId("access-change-reason"), "offboarded");
    await user.click(screen.getByTestId("access-change-apply"));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].method).toBe("DELETE");
    expect(calls[0].path).toBe(
      "/api/access/principals/user%3Aother%40example.com?reason=offboarded",
    );

    // A non-sensitive change goes straight through, no reason field.
    await user.click(screen.getByLabelText("Role of group:eng@example.com"));
    await user.click(screen.getByRole("option", { name: "grain/contributor" }));
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(screen.queryByTestId("access-change-panel")).toBeNull();
    expect(JSON.parse(calls[1].body)).toEqual({ role: "grain/contributor" });

    // Narrowing one's own operator entry: a reason, then the server's
    // confirm_self, then a confirmed resend.
    await user.click(screen.getByLabelText("Role of user:ops@example.com"));
    await user.click(screen.getByRole("option", { name: "grain/viewer" }));
    await user.type(
      await screen.findByTestId("access-change-reason"),
      "stepping down",
    );
    await user.click(screen.getByTestId("access-change-apply"));
    expect(await screen.findByTestId("access-confirm-self")).toHaveTextContent(
      /narrows your own access/,
    );
    await user.click(
      screen.getByRole("button", { name: "Yes, narrow my own access" }),
    );
    await waitFor(() => expect(calls).toHaveLength(4));
    expect(JSON.parse(calls[3].body)).toEqual({
      role: "grain/viewer",
      reason: "stepping down",
      confirmSelf: true,
    });
    await waitFor(() =>
      expect(screen.queryByTestId("access-change-panel")).toBeNull(),
    );
  });

  it("validates, adds, and removes allowed users and groups for both access-code and IAP sessions", async () => {
    const user = userEvent.setup();
    let currentEntries = [
      {
        entry: "user:iap@example.com",
        addedBy: "the access code",
        addedAt: "2026-11-03T05:00:00Z",
      },
      {
        entry: "group:team@example.com",
        addedBy: "iap@example.com",
        addedAt: "2026-11-03T05:10:00Z",
      },
    ];
    api.mockImplementation(async (path, opts) => {
      if (path === "/api/access/principals" && !opts?.method) {
        return {
          entries: [...currentEntries],
          droppedFlagEntries: ["domain:legacy.example.com"],
        };
      }
      if (path === "/api/access/principals" && opts?.method === "POST") {
        expect(typeof opts.body).toBe("string");
        const parsed = JSON.parse(opts.body);
        currentEntries = [
          ...currentEntries,
          {
            entry: parsed.entry,
            reason: parsed.reason,
            addedBy: "iap@example.com",
            addedAt: "2026-11-03T06:00:00Z",
          },
        ];
        return { created: true };
      }
      if (
        path.startsWith("/api/access/principals/") &&
        opts?.method === "DELETE"
      ) {
        const target = decodeURIComponent(
          path.slice("/api/access/principals/".length),
        );
        currentEntries = currentEntries.filter((e) => e.entry !== target);
        return { entry: target, removed: true };
      }
      return {};
    });

    render(
      section({
        config: {
          authenticated: true,
          actor: "user:iap@example.com",
          authMethod: "iap",
        },
      }),
    );

    await screen.findByText("user:iap@example.com");
    expect(
      screen.getByTestId("dropped-flag-entries-warning"),
    ).toHaveTextContent("domain:legacy.example.com");

    // Reject invalid entries client-side.
    const input = screen.getByLabelText("User, group or service account");
    await user.type(input, "domain:example.com");
    await user.click(
      screen.getByRole("button", { name: "Add allowed principal" }),
    );
    expect(
      screen.getByText(/domain: entries are not supported/),
    ).toBeInTheDocument();

    // Add valid user entry.
    await user.clear(input);
    await user.type(input, "user:Bob@Example.com");
    await user.click(
      screen.getByRole("button", { name: "Add allowed principal" }),
    );
    await screen.findByText("user:bob@example.com");
    expect(screen.queryByTestId("allowed-service-accounts")).toBeNull();

    // A service account needs a reason, then is listed in its own group
    // with that reason.
    const sa = "serviceAccount:ci-bot@proj.iam.gserviceaccount.com";
    await user.type(input, sa);
    await user.click(
      screen.getByRole("button", { name: "Add allowed principal" }),
    );
    expect(screen.getByText(/Give a reason/)).toBeInTheDocument();
    await user.type(screen.getByTestId("access-reason-input"), "ci pipeline");
    await user.click(
      screen.getByRole("button", { name: "Add allowed principal" }),
    );
    await screen.findByText(sa);
    const group = screen.getByTestId("allowed-service-accounts");
    expect(group).toHaveTextContent(/held to its role/);
    expect(group).toHaveTextContent("ci pipeline");
    expect(
      screen.getByTestId("allowed-principals-table"),
    ).not.toHaveTextContent(sa);

    // Removing oneself prompts a warning before removing, and succeeds when confirmed.
    await user.click(
      screen.getByRole("button", { name: "Remove user:iap@example.com" }),
    );
    expect(screen.getByTestId("access-remove-warning")).toHaveTextContent(
      /removing your own account/,
    );
    await user.click(screen.getByRole("button", { name: "Remove anyway" }));
    await waitFor(() =>
      expect(screen.queryByText("user:iap@example.com")).toBeNull(),
    );
    expect(screen.getByTestId("access-removed-notice")).toHaveTextContent(
      /access code still works on a direct connection/,
    );
  });

  it("skips principal and history reads when the caller lacks access:read, and refreshes config on self-narrowing", async () => {
    const user = userEvent.setup();
    const showError = vi.fn();
    render(
      <AccessSection
        showError={showError}
        config={{
          authenticated: true,
          actor: "user:carol@example.com",
          authMethod: "iap",
          access: {
            role: "carol-limited",
            roles: ["carol-limited"],
            full: false,
            scopes: ["tasks:read", "tasks:admin", "dispatch:write"],
            repoLimits: { "tasks:admin": ["acme/widgets"] },
          },
        }}
      />,
    );
    expect(screen.getByTestId("access-read-only")).toHaveTextContent(
      /does not include access:read/,
    );
    expect(api).not.toHaveBeenCalled();
    expect(showError).not.toHaveBeenCalled();

    // Self-narrowing refreshes config via onSaved and clears stale confirmSelf on a subsequent refusal.
    let attempt = 0;
    api.mockImplementation(async (path, opts) => {
      if (path === "/api/access/principals" && !opts?.method) {
        return {
          entries: [{ entry: "user:bob@example.com", role: "grain/operator" }],
          roles: [
            { name: "grain/operator", builtin: true, person: true },
            { name: "carol-limited", builtin: false, person: false },
          ],
        };
      }
      if (path === "/api/access/history") return [];
      if (opts?.method === "PATCH") {
        attempt++;
        if (attempt === 1) {
          const err = new Error("confirm narrowing your own access");
          err.code = "confirm_self";
          err.status = 403;
          throw err;
        }
        if (attempt === 2) {
          const err = new Error(
            "user:bob@example.com is the last entry holding a full admin role",
          );
          err.code = "last_admin";
          err.status = 403;
          throw err;
        }
        return {};
      }
      return {};
    });

    const onSaved = vi.fn(async () => ({
      authenticated: true,
      actor: "user:bob@example.com",
      authMethod: "iap",
      access: {
        role: "carol-limited",
        roles: ["carol-limited"],
        full: false,
        scopes: ["tasks:read", "dispatch:write"],
      },
    }));

    const { rerender } = render(
      <AccessSection
        showError={showError}
        onSaved={onSaved}
        config={{
          authenticated: true,
          actor: "user:bob@example.com",
          authMethod: "iap",
          access: {
            role: "grain/operator",
            roles: ["grain/operator"],
            full: true,
            scopes: ["access:read", "access:write"],
          },
        }}
      />,
    );
    await screen.findByText("user:bob@example.com");
    await user.click(screen.getByLabelText("Role of user:bob@example.com"));
    await user.click(screen.getByRole("option", { name: "carol-limited" }));
    await user.type(
      await screen.findByTestId("access-change-reason"),
      "step down",
    );
    await user.click(screen.getByTestId("access-change-apply"));
    expect(
      await screen.findByTestId("access-confirm-self"),
    ).toBeInTheDocument();

    // Second attempt fails with last_admin: confirm_self prompt is cleared while error stays visible.
    await user.click(
      screen.getByRole("button", { name: "Yes, narrow my own access" }),
    );
    expect(await screen.findByTestId("access-change-error")).toHaveTextContent(
      /last entry holding a full admin role/,
    );
    expect(screen.queryByTestId("access-confirm-self")).toBeNull();

    // Third attempt succeeds (after adding another admin): onSaved runs and skips forbidden GETs.
    api.mockClear();
    await user.click(screen.getByTestId("access-change-apply"));
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(
      api.mock.calls.filter(
        ([p, o]) => !o?.method && p.startsWith("/api/access/"),
      ),
    ).toHaveLength(0);
    rerender(
      <AccessSection
        showError={showError}
        onSaved={onSaved}
        config={{
          authenticated: true,
          actor: "user:bob@example.com",
          authMethod: "iap",
          access: {
            role: "carol-limited",
            roles: ["carol-limited"],
            full: false,
            scopes: ["tasks:read", "dispatch:write"],
          },
        }}
      />,
    );
    expect(screen.queryByTestId("allowed-principals-table")).toBeNull();
  });
});

describe("validatePrincipalEntry", () => {
  it("accepts user:, group: and serviceAccount: entries and rejects other prefixes or malformed emails", () => {
    expect(validatePrincipalEntry(" user:Alice@Example.com ")).toEqual({
      canonical: "user:alice@example.com",
      error: "",
    });
    expect(validatePrincipalEntry("group:Ops@Example.com")).toEqual({
      canonical: "group:ops@example.com",
      error: "",
    });
    expect(validatePrincipalEntry("alice@example.com").error).toMatch(
      /must start with user:, group: or serviceAccount:/,
    );
    expect(
      validatePrincipalEntry("serviceAccount:SA@proj.iam.gserviceaccount.com"),
    ).toEqual({
      canonical: "serviceAccount:sa@proj.iam.gserviceaccount.com",
      error: "",
    });
    expect(
      validatePrincipalEntry("serviceAccount:bot@example.com").error,
    ).toMatch(/\*\.gserviceaccount\.com/);
    expect(
      validatePrincipalEntry("user:sa@proj.iam.gserviceaccount.com").error,
    ).toMatch(/must be added as serviceAccount:/);
    expect(validatePrincipalEntry("domain:example.com").error).toMatch(
      /not supported/,
    );
    expect(validatePrincipalEntry("user:not-an-email").error).toMatch(
      /valid email/,
    );
  });
  // Allowed-principal and service-account rows printed addedAt in UTC
  // regardless of the time zone chosen under Settings → Preferences, and
  // RoleChip rendered a <div> inside the caller-role <p> (2026-11-08
  // settings walkthrough).
  it("prints added times in this browser's chosen time zone and keeps RoleChip inline", async () => {
    const addedAt = "2026-11-06T08:01:00Z";
    api.mockResolvedValue({
      entries: [
        {
          entry: "user:alice@example.com",
          role: "grain/operator",
          addedBy: "bob@example.com",
          addedAt,
        },
        {
          entry: "serviceAccount:ci@proj.iam.gserviceaccount.com",
          role: "grain/viewer",
          reason: "CI reader",
          addedBy: "bob@example.com",
          addedAt,
        },
      ],
    });
    const errSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      render(
        <TimeZoneProvider zone="Asia/Tokyo">
          {section({
            config: {
              authenticated: true,
              actor: "user:dana@example.com",
              authMethod: "iap",
              access: {
                role: "grain/viewer",
                full: false,
                scopes: ["access:read"],
              },
            },
          })}
        </TimeZoneProvider>,
      );
      const inTokyo = new Date(addedAt).toLocaleString(undefined, {
        timeZone: "Asia/Tokyo",
      });
      const userRow = await screen.findByTestId(
        "principal-row-user:alice@example.com",
      );
      expect(userRow).toHaveTextContent(inTokyo);
      const saRow = screen.getByTestId(
        "principal-row-serviceAccount:ci@proj.iam.gserviceaccount.com",
      );
      expect(saRow).toHaveTextContent(inTokyo);

      const ownRole = screen.getByTestId("access-own-role");
      expect(ownRole.querySelector("div")).toBeNull();
      expect(errSpy).not.toHaveBeenCalled();
    } finally {
      errSpy.mockRestore();
    }
  });
});

describe("actorName", () => {
  it("strips the user: kind and reads either shape of actor", () => {
    expect(actorName({ actor: "user:ann@example.com" })).toBe(
      "ann@example.com",
    );
    expect(actorName({ actor: { id: "user:ann" } })).toBe("ann");
    expect(actorName({ actor: "" })).toBe("");
    expect(actorName(null)).toBe("");
  });
});
