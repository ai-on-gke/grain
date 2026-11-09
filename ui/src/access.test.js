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

import { describe, expect, it } from "vitest";
import {
  accessDeniedReason,
  accessRoleLabel,
  canAccess,
  repoLimit,
} from "./access.js";
import { viewerConfig, widgetsConfig } from "./accessTestConfigs.js";

describe("canAccess", () => {
  it("lets an unlimited caller -- the access code, or no config yet -- do everything", () => {
    expect(canAccess(null, "tasks:write")).toBe(true);
    expect(canAccess({}, "settings:write", "acme/widgets")).toBe(true);
    expect(
      canAccess(
        { access: { role: "grain/operator", full: true, scopes: [] } },
        "merge:force",
      ),
    ).toBe(true);
  });

  it("refuses grain/viewer every write, and allows its reads", () => {
    for (const scope of [
      "tasks:write",
      "tasks:admin",
      "ci:write",
      "settings:write",
      "schedules:write",
      "templates:write",
      "boards:write",
    ]) {
      expect(canAccess(viewerConfig, scope)).toBe(false);
      expect(canAccess(viewerConfig, scope, "acme/widgets")).toBe(false);
    }
    expect(canAccess(viewerConfig, "tasks:read")).toBe(true);
  });

  it("allows a repository-limited scope only on a repository it reaches", () => {
    expect(canAccess(widgetsConfig, "tasks:write")).toBe(true);
    expect(canAccess(widgetsConfig, "tasks:write", "acme/widgets")).toBe(true);
    expect(canAccess(widgetsConfig, "tasks:write", "Acme/Widgets")).toBe(true);
    expect(canAccess(widgetsConfig, "tasks:write", "acme/gadgets")).toBe(false);
    // An unlimited scope reaches every repository.
    expect(canAccess(widgetsConfig, "tasks:read", "acme/gadgets")).toBe(true);
    // A batch reaches only if it reaches every one.
    expect(
      canAccess(widgetsConfig, "tasks:write", ["acme/widgets", "acme/gadgets"]),
    ).toBe(false);
    expect(canAccess(widgetsConfig, "tasks:write", ["acme/widgets"])).toBe(
      true,
    );
    expect(repoLimit(widgetsConfig, "ci:write")).toEqual(["acme/widgets"]);
    expect(repoLimit(widgetsConfig, "tasks:read")).toBeNull();
  });
});

describe("accessDeniedReason", () => {
  it("is empty where the caller may", () => {
    expect(accessDeniedReason(null, "tasks:write")).toBe("");
    expect(accessDeniedReason(widgetsConfig, "ci:write", "acme/widgets")).toBe(
      "",
    );
  });

  it("names the missing scope and the role", () => {
    expect(accessDeniedReason(viewerConfig, "tasks:write")).toBe(
      "Needs tasks:write, which your role grain/viewer does not grant.",
    );
    expect(accessRoleLabel(viewerConfig)).toBe("grain/viewer");
  });

  it("names the repositories a limited role does reach", () => {
    expect(
      accessDeniedReason(widgetsConfig, "tasks:write", "acme/gadgets"),
    ).toBe(
      "Needs tasks:write on acme/gadgets: your role widgets-contributor holds it only on acme/widgets.",
    );
  });

  it("says roles, plural, when several entries matched", () => {
    const both = {
      access: {
        role: "grain/viewer, triage",
        roles: ["grain/viewer", "triage"],
        full: false,
        scopes: ["tasks:read"],
      },
    };
    expect(accessDeniedReason(both, "ci:write")).toBe(
      "Needs ci:write, which your roles grain/viewer, triage do not grant.",
    );
  });
});
