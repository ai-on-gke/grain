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
