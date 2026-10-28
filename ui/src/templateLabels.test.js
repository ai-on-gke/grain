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
import { templateLabeler } from "./templateLabels.js";

describe("templateLabeler", () => {
  it("labels a template whose name is unique by its name alone", () => {
    const templates = [
      { id: "template-1", name: "release-qualification", title: "Qual" },
      { id: "template-2", name: "security-audit", title: "Audit" },
    ];
    const label = templateLabeler(templates);
    expect(label(templates[0])).toBe("release-qualification");
    expect(label(templates[1])).toBe("security-audit");
  });

  it("adds the title and id to every template sharing a name", () => {
    const templates = [
      { id: "template-1", name: "release-qualification", title: "Qual" },
      {
        id: "template-5",
        name: "Release-Qualification ",
        title: "dup name via UI",
      },
      { id: "template-6", name: "release-qualification", title: "" },
      { id: "template-2", name: "security-audit", title: "Audit" },
    ];
    const label = templateLabeler(templates);
    expect(label(templates[0])).toBe(
      "release-qualification — Qual (template-1)",
    );
    expect(label(templates[1])).toBe(
      "Release-Qualification — dup name via UI (template-5)",
    );
    expect(label(templates[2])).toBe("release-qualification (template-6)");
    expect(label(templates[3])).toBe("security-audit");
  });

  it("falls back to the id for a template with no name", () => {
    expect(templateLabeler(null)({ id: "template-9" })).toBe("template-9");
  });
});
