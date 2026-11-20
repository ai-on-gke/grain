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

import { TestBed } from "@angular/core/testing";
import { AccessService, initialsFromIdentity } from "./access.service";

describe("AccessService", () => {
  let svc: AccessService;
  beforeEach(() => {
    svc = TestBed.inject(AccessService);
  });

  it("fails closed for operator-only (scope: null) routes until config is loaded", () => {
    expect(svc.configLoaded()).toBe(false);
    expect(svc.can("GET /api/hostds/enrollment-codes")).toBe(false);
    expect(svc.can("POST /api/state-repo/remote")).toBe(false);

    svc.setConfig({ access: null });
    expect(svc.configLoaded()).toBe(true);
    expect(svc.can("GET /api/hostds/enrollment-codes")).toBe(true);
  });

  it("allows everything to an unlimited caller", () => {
    svc.setConfig({ access: null });
    expect(svc.can("POST /api/tasks/{id}/approve")).toBe(true);
    expect(svc.deniedReason("POST /api/tasks/{id}/approve")).toBe("");
  });

  it("checks a limited role against the scope the route table names", () => {
    svc.setConfig({
      access: {
        role: "grain/viewer",
        roles: ["grain/viewer"],
        full: false,
        scopes: ["tasks:read"],
      },
    });
    expect(svc.can("GET /api/tasks")).toBe(true);
    expect(svc.can("POST /api/tasks/{id}/approve")).toBe(false);
    expect(svc.deniedReason("POST /api/tasks/{id}/approve")).toContain(
      "grain/viewer",
    );
    expect(svc.deniedReason("POST /api/tasks/{id}/approve")).toContain(
      "tasks:admin",
    );
    // A signed-in route is open to any role.
    expect(svc.can("GET /api/config")).toBe(true);
  });

  it("refuses deployment-wide reads to a role limited to some repositories", () => {
    svc.setConfig({
      access: {
        role: "widgets/viewer",
        roles: ["widgets/viewer"],
        full: false,
        scopes: ["tasks:read", "settings:read"],
        repoLimits: {
          "tasks:read": ["acme/widgets"],
          "settings:read": ["acme/widgets"],
        },
      },
    });
    // can() holds on some repository; the server still refuses a read
    // that names none (docs/test-runs/2026-11-14-next-heavy-data.md).
    expect(svc.can("GET /api/notifications")).toBe(true);
    expect(svc.canGlobal("GET /api/notifications")).toBe(false);
    expect(svc.canGlobal("GET /api/settings")).toBe(false);
    expect(svc.repoLimited("GET /api/settings")).toBe(true);
    // Not held at all is not "limited": deniedReason says why.
    expect(svc.repoLimited("GET /api/agent-keys")).toBe(false);
    expect(svc.globalDeniedReason("GET /api/settings")).toContain(
      "settings:read only on acme/widgets",
    );
    // A scope the role does not hold at all reads as deniedReason does.
    expect(svc.globalDeniedReason("GET /api/agent-keys")).toBe(
      svc.deniedReason("GET /api/agent-keys"),
    );

    svc.setConfig({
      access: {
        role: "grain/viewer",
        roles: ["grain/viewer"],
        full: false,
        scopes: ["tasks:read"],
      },
    });
    expect(svc.canGlobal("GET /api/notifications")).toBe(true);
    expect(svc.globalDeniedReason("GET /api/notifications")).toBe("");
  });

  it("honours a scope's repository limit and names refused repositories in deniedReason", () => {
    svc.setConfig({
      access: {
        role: "r",
        roles: ["r"],
        full: false,
        scopes: ["tasks:write", "suites:write"],
        repoLimits: {
          "tasks:write": ["acme/widgets"],
          "suites:write": ["acme/widgets"],
        },
      },
    });
    expect(svc.can("POST /api/tasks/{id}/close", "acme/widgets")).toBe(true);
    expect(svc.can("POST /api/tasks/{id}/close", "acme/other")).toBe(false);
    expect(
      svc.deniedReason("POST /api/tasks/{id}/close", "acme/other"),
    ).toContain("does not hold tasks:write on acme/other");
    expect(
      svc.deniedReason("PATCH /api/suites/{id}", [
        "acme/widgets",
        "acme/gadgets",
        "acme/gadgets",
      ]),
    ).toContain("does not hold suites:write on acme/gadgets,");
  });

  it("derives crisp initials and ignores parenthetical suffixes", () => {
    expect(initialsFromIdentity("")).toBe("G");
    expect(initialsFromIdentity("operator (local)")).toBe("OP");
    expect(initialsFromIdentity("Alice Example (access code)")).toBe("AE");
    expect(initialsFromIdentity("bob.smith@example.com")).toBe("BS");
  });
});
