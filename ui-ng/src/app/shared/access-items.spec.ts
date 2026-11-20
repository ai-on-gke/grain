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

import type { AccessDecision } from "../api/models";
import {
  accessDecisionDetail,
  accessDecisionLabel,
  accessDecisionOutcome,
  accessHoldsList,
  accessItemText,
} from "./access-items";

const decision = (extra: Partial<AccessDecision>): AccessDecision => ({
  request: {
    summary: "Open network and tasks:admin",
    reason: "",
    createdAt: "2026-11-19T10:00:00Z",
    items: [
      { kind: "network", access: "open" },
      {
        kind: "grant",
        capability: "https://grain.dev/capabilities/api#tasks:admin",
      },
    ],
  },
  by: { Kind: "human", ID: "sam@example.com" },
  at: "2026-11-19T10:05:00Z",
  ...extra,
});

describe("access items", () => {
  it("says each kind of item in words", () => {
    expect(accessItemText({ kind: "read", repo: "acme/lib" })).toBe(
      "read acme/lib",
    );
    expect(
      accessItemText({ kind: "write", repo: "acme/lib", base: "release" }),
    ).toBe("write acme/lib (release)");
    expect(accessItemText({ kind: "network", access: "open" })).toBe(
      "network open",
    );
    expect(
      accessItemText({ kind: "grant", capability: "x#y", grant: "api:y" }),
    ).toBe("api:y");
  });

  it("lists what a task holds, reads to grants", () => {
    expect(accessHoldsList(undefined)).toEqual([]);
    expect(accessHoldsList({ reads: [], writes: [], grants: [] })).toEqual([]);
    expect(
      accessHoldsList({
        reads: ["acme/lib"],
        writes: [{ repo: "acme/web", base: "release" }, { repo: "acme/api" }],
        grants: ["api:ci-read@acme/web"],
        networkAccess: "none",
      }),
    ).toEqual(
      [
        "read acme/lib",
        "write acme/web (release)",
        "write acme/api",
        "network none",
        "api:ci-read@acme/web",
      ].map((text) => ({ text, full: text })),
    );
  });

  it("shortens a held grant's reference but keeps it whole beside", () => {
    expect(
      accessHoldsList({
        reads: [],
        writes: [],
        grants: [
          "https://grain.dev/capabilities/grain#source={}",
          'https://grain.dev/capabilities/api#ci-read={"repo":"acme/web"}',
          "https://example.com/caps/x#y",
        ],
      }),
    ).toEqual([
      {
        text: "grain#source",
        full: "https://grain.dev/capabilities/grain#source={}",
      },
      {
        text: 'api#ci-read={"repo":"acme/web"}',
        full: 'https://grain.dev/capabilities/api#ci-read={"repo":"acme/web"}',
      },
      {
        text: "https://example.com/caps/x#y",
        full: "https://example.com/caps/x#y",
      },
    ]);
  });

  it("says how an earlier answer went", () => {
    const denied = decision({ denied: true, reason: "too broad" });
    expect(accessDecisionOutcome(denied)).toBe("denied");
    expect(accessDecisionLabel(denied)).toBe("Denied before");
    expect(accessDecisionDetail(denied)).toBe(
      "declined network open, api#tasks:admin",
    );

    const partly = decision({
      granted: [{ kind: "network", access: "open" }],
      declined: [
        {
          kind: "grant",
          capability: "https://grain.dev/capabilities/api#tasks:admin",
        },
      ],
    });
    expect(accessDecisionOutcome(partly)).toBe("declined");
    expect(accessDecisionLabel(partly)).toBe("Partly granted before");
    expect(accessDecisionDetail(partly)).toBe(
      "granted network open; declined api#tasks:admin",
    );

    const granted = decision({
      granted: [{ kind: "network", access: "open" }],
    });
    expect(accessDecisionOutcome(granted)).toBe("granted");
    expect(accessDecisionLabel(granted)).toBe("Granted before");
  });
});
