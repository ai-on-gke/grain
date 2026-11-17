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
import { checkPrincipalEntry } from "./settings-security.component";
import { filterSettingsGroups, labelSettingError } from "./settings-sections";

const sectionIds = (q: string) =>
  filterSettingsGroups(q).flatMap((g) => g.sections.map((s) => s.id));

describe("Filter settings", () => {
  it("finds a section by a field label, punctuation and all", () => {
    expect(sectionIds("Fallback models")).toEqual(["models"]);
    expect(sectionIds("pinned models (comma-separated)")).toContain("models");
    expect(sectionIds("PR rollout (%)")).toEqual(["tasks"]);
    expect(sectionIds("zzzqqq")).toEqual([]);
  });
});

describe("checkPrincipalEntry", () => {
  it("files a bare address as user:, or serviceAccount: for a service account", () => {
    expect(checkPrincipalEntry(" Bob@Example.com ")).toEqual({
      canonical: "user:bob@example.com",
      error: "",
    });
    expect(
      checkPrincipalEntry("svc@proj.iam.gserviceaccount.com").canonical,
    ).toBe("serviceAccount:svc@proj.iam.gserviceaccount.com");
    expect(checkPrincipalEntry("group:eng@example.com").canonical).toBe(
      "group:eng@example.com",
    );
  });

  it("refuses what is not an allowlist entry", () => {
    for (const raw of [
      "user:bob",
      "group:",
      "domain:example.com",
      "serviceAccount:bot@example.com",
      "user:svc@proj.iam.gserviceaccount.com",
    ]) {
      expect(checkPrincipalEntry(raw).error, raw).not.toBe("");
      expect(checkPrincipalEntry(raw).canonical, raw).toBe("");
    }
    expect(checkPrincipalEntry("")).toEqual({ canonical: "", error: "" });
  });
});

describe("labelSettingError", () => {
  it("names a refused field by its label", () => {
    expect(
      labelSettingError("taskBranchTemplate branch template is invalid"),
    ).toBe("Branch name template: branch template is invalid");
    expect(labelSettingError("something else broke")).toBe(
      "something else broke",
    );
  });
});
