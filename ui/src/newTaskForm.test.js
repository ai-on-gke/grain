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
  formFieldsIn,
  pickerAllows,
  resolveNewTaskForm,
  sortByDisplay,
} from "./newTaskForm.js";

const form = {
  default: {
    fields: {
      autoMerge: { visibility: "shown", default: true },
      networkAccess: { visibility: "more", offered: ["", "none"] },
      legacyCapabilities: { label: "Capabilities" },
    },
    capabilities: {
      defaults: ["corp-research"],
      hidden: ["secret-thing"],
      display: { "corp-research": { label: "Search", order: 1 } },
    },
  },
  boards: {
    triage: { fields: { approved: { visibility: "hidden", default: false } } },
  },
  repos: { "ai-on-gke/grain": { capabilities: { defaults: ["self-debug"] } } },
  templates: {
    "release-cut": {
      fields: {
        autoMerge: { default: false },
        networkAccess: { visibility: "hidden", default: "none" },
      },
    },
  },
};

describe("resolveNewTaskForm", () => {
  it("is the built-in form when nothing is configured", () => {
    const r = resolveNewTaskForm(
      { autoMergeByDefault: true, newestFirst: true },
      {},
    );
    expect(r.configured).toBe(false);
    expect(r.fields.autoMerge).toMatchObject({
      visibility: "shown",
      default: true,
      defaultSet: false,
    });
    expect(r.fields.atFront.default).toBe(true);
    expect(r.fields.nothingNeededAction.default).toBe("close");
    expect(formFieldsIn(r, "more")).toEqual([
      "agentBackendId",
      "networkAccess",
      "nothingNeededAction",
      "reviewTemplateId",
      "promptExtension",
      "sandboxShape",
    ]);
    expect(formFieldsIn(r, "hidden")).toEqual([]);
    expect(r.capabilities.defaults).toBeNull();
  });

  it("merges default, board, repo and template in that order", () => {
    const r = resolveNewTaskForm(
      { newTaskForm: form },
      { repo: "AI-on-GKE/grain", templateId: "release-cut", boardId: "triage" },
    );
    expect(r.fields.autoMerge).toMatchObject({
      visibility: "shown",
      default: false,
      defaultSet: true,
    });
    expect(r.fields.networkAccess).toMatchObject({
      visibility: "hidden",
      default: "none",
      offered: ["", "none"],
    });
    expect(r.fields.approved).toMatchObject({
      visibility: "hidden",
      default: false,
    });
    expect(r.fields.grants.label).toBe("Capabilities");
    expect(r.capabilities.defaults).toEqual(["self-debug"]);
    expect(r.capabilities.hidden).toEqual(["secret-thing"]);

    const plain = resolveNewTaskForm(
      { newTaskForm: form },
      { repo: "other/repo" },
    );
    expect(plain.fields.approved.visibility).toBe("shown");
    expect(plain.capabilities.defaults).toEqual(["corp-research"]);
  });

  it("filters and orders the picker by offered, hidden and display", () => {
    const caps = {
      offered: ["corp-research", "api:task-reader", "https://x.example/s"],
      hidden: ["api:task-reader"],
      display: { "https://x.example/s": { order: 1 } },
    };
    expect(pickerAllows(caps, "corp-research")).toBe(true);
    expect(pickerAllows(caps, "api:task-reader@a/b")).toBe(false);
    expect(pickerAllows(caps, "https://x.example/s#mint")).toBe(true);
    expect(pickerAllows(caps, "self-debug")).toBe(false);
    expect(pickerAllows({ offered: ["*"], hidden: [] }, "self-debug")).toBe(
      true,
    );
    expect(pickerAllows(null, "anything")).toBe(true);
    const rows = [{ id: "corp-research" }, { id: "https://x.example/s#mint" }];
    expect(sortByDisplay(caps, rows, (r) => r.id).map((r) => r.id)).toEqual([
      "https://x.example/s#mint",
      "corp-research",
    ]);
  });
});
