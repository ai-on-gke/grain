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
import { AccessService } from "./access.service";

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

  it("honours a scope's repository limit", () => {
    svc.setConfig({
      access: {
        role: "r",
        roles: ["r"],
        full: false,
        scopes: ["tasks:write"],
        repoLimits: { "tasks:write": ["acme/widgets"] },
      },
    });
    expect(svc.can("POST /api/tasks/{id}/close", "acme/widgets")).toBe(true);
    expect(svc.can("POST /api/tasks/{id}/close", "acme/other")).toBe(false);
  });
});
