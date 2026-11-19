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

import {
  accessItemGrantable,
  accessItemText,
  chosenAccessItems,
  grantAccessLabel,
} from "./access-items";

describe("access-items", () => {
  it("counts only unheld clean or substitute-bearing items as grantable", () => {
    const items = [
      { kind: "network", access: "open" },
      { kind: "grant", capability: "github#read-issues", held: true },
      {
        kind: "grant",
        capability: "grain#ci",
        error: "not gated by grain CI",
        substituteGrant: "api:ci-read@acme/widgets",
      },
      { kind: "read", repo: "acme/off-list", error: "not configured" },
    ];
    expect(items.map(accessItemGrantable)).toEqual([true, false, true, false]);
    expect(chosenAccessItems(items, new Set())).toEqual([0, 2]);
    expect(chosenAccessItems(items, new Set([2]))).toEqual([0]);
    expect(grantAccessLabel(2, 4)).toBe("Grant 2 of 4");
    expect(grantAccessLabel(4, 4)).toBe("Grant all");
  });

  it("names a substitute grant beside the requested item for both surfaces", () => {
    expect(
      accessItemText({
        kind: "grant",
        capability: "https://grain.dev/capabilities/grain#ci",
        error: "ci_mode is github",
        substituteGrant:
          'https://grain.dev/capabilities/api#ci-read={"repo":"acme/widgets"}',
      }),
    ).toBe(
      'https://grain.dev/capabilities/grain#ci (grants https://grain.dev/capabilities/api#ci-read={"repo":"acme/widgets"} instead)',
    );
    expect(accessItemText({ kind: "network", access: "open" })).toBe(
      "network access: open",
    );
    expect(
      accessItemText({ kind: "write", repo: "acme/widgets", base: "main" }),
    ).toBe("write acme/widgets (main)");
  });
});
