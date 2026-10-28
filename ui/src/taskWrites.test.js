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
import { forceMergeOffered, repoAdministered } from "./taskWrites.js";

describe("forceMergeOffered", () => {
  it("offers Force merge to anybody config does not scope", () => {
    expect(forceMergeOffered({}, {})).toBe(true);
    expect(forceMergeOffered(null, {})).toBe(true);
  });
});

describe("forceMergeOffered for a repository admin", () => {
  const cfg = {
    adminScoped: true,
    adminRepos: ["acme/widgets"],
  };
  it("offers it on a task in a repo they administer", () => {
    expect(forceMergeOffered(cfg, { repo: "acme/widgets" })).toBe(true);
    expect(forceMergeOffered(cfg, { pullRequest: "Acme/Widgets#7" })).toBe(
      true,
    );
  });
  it("leaves it out on a task in any other repo", () => {
    expect(forceMergeOffered(cfg, { repo: "acme/gadgets" })).toBe(false);
    expect(forceMergeOffered(cfg, {})).toBe(false);
  });
});

describe("repoAdministered", () => {
  it("is true for anybody config does not scope", () => {
    expect(repoAdministered({}, "acme/widgets")).toBe(true);
    expect(repoAdministered(null, "acme/widgets")).toBe(true);
  });
  it("reads adminRepos the way model.RepoAllowed reads an entry", () => {
    const cfg = { adminScoped: true, adminRepos: ["acme/*", "Other/One"] };
    expect(repoAdministered(cfg, "acme/widgets")).toBe(true);
    expect(repoAdministered(cfg, "other/one")).toBe(true);
    expect(repoAdministered(cfg, "other/two")).toBe(false);
    expect(repoAdministered(cfg, "")).toBe(false);
    expect(
      repoAdministered({ adminScoped: true, adminRepos: ["*"] }, "x/y"),
    ).toBe(true);
    expect(repoAdministered({ adminScoped: true }, "acme/widgets")).toBe(false);
  });
});
