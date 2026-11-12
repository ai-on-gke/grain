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

import type { GrainConfig, Task, Template } from "../api/models";
import {
  NO_REPO,
  buildPayload,
  defaultCapabilitiesForRepo,
  directorMessage,
  emptyDraft,
  fromTemplateStem,
  lastBaseForRepo,
  openingRepo,
  resolveFields,
  targetableRepos,
  validate,
} from "./new-task-form";

const config: GrainConfig = {
  access: null,
  configRepo: "grain/config",
  defaultTarget: "SIDTHE/grain",
  approvedByDefault: true,
  autoMergeByDefault: false,
  newestFirst: false,
  deployment: { targetRepos: ["acme/web", "sidthe/grain"] },
};

const tasks = [
  { id: "1", title: "a", state: "queued", repo: "acme/api" },
  {
    id: "2",
    title: "b",
    state: "queued",
    repo: "sidthe/grain",
    base: "old",
    createdAt: "2026-11-01T00:00:00Z",
  },
  {
    id: "3",
    title: "c",
    state: "queued",
    repo: "sidthe/grain",
    base: "release-1",
    createdAt: "2026-11-05T00:00:00Z",
  },
  {
    id: "5",
    title: "d",
    state: "failed",
    repo: "sidthe/grain",
    base: "dead",
    createdAt: "2026-11-06T00:00:00Z",
  },
  {
    id: "6",
    title: "e",
    state: "queued",
    repo: "sidthe/grain",
    base: "grain/task-3",
    stacked: true,
    createdAt: "2026-11-07T00:00:00Z",
  },
  {
    id: "7",
    title: "f",
    state: "queued",
    repo: "sidthe/grain",
    base: "suite-pass",
    authorKind: "automation",
    createdAt: "2026-11-08T00:00:00Z",
  },
] as Task[];

describe("new task form", () => {
  it("offers the config repo and the allowlist only, or task repos when there is none", () => {
    expect(targetableRepos(config, tasks)).toEqual([
      "acme/web",
      "grain/config",
      "sidthe/grain",
    ]);
    expect(targetableRepos({ access: null }, tasks)).toEqual([
      "acme/api",
      "sidthe/grain",
    ]);
  });

  it("starts on the default target, then the form's repo default, never the first repo", () => {
    const opts = targetableRepos(config, tasks);
    expect(openingRepo(config, opts)).toBe("sidthe/grain");
    expect(openingRepo(config, opts, "acme/web")).toBe("acme/web");
    expect(openingRepo({ access: null }, ["a/a", "b/b"])).toBe("");
    expect(
      openingRepo(
        {
          access: null,
          newTaskForm: { default: { fields: { repo: { default: "none" } } } },
        },
        ["a/a"],
      ),
    ).toBe(NO_REPO);
  });

  it("repeats the newest hand-filed, live task's base for the repo", () => {
    expect(lastBaseForRepo(tasks, "sidthe/grain")).toBe("release-1");
    expect(lastBaseForRepo(tasks, "SIDTHE/grain")).toBe("");
    expect(lastBaseForRepo(tasks, "acme/web")).toBe("");
  });

  it("takes defaults from the config and layers settings/new-task-form.yml across all 22 fields", () => {
    const f = resolveFields(
      {
        ...config,
        newTaskForm: {
          default: {
            fields: {
              model: { visibility: "more", default: "fast" },
              title: { label: "What needs doing" },
              legacyCapabilities: { visibility: "shown" },
            },
          },
          repos: {
            "Sidthe/Grain": {
              fields: { networkAccess: { visibility: "hidden" } },
            },
          },
        },
      },
      "sidthe/grain",
    );
    expect(Object.keys(f)).toHaveLength(23);
    expect(f.approved.default).toBe(true);
    expect(f.autoMerge.default).toBe(false);
    expect(f.model).toMatchObject({ visibility: "more", default: "fast" });
    expect(f.title.label).toBe("What needs doing");
    expect(f.grants.visibility).toBe("shown");
    expect(f.networkAccess.visibility).toBe("hidden");
    expect(resolveFields(null).nothingNeededAction.default).toBe("close");
  });

  it("builds the body for all 22 fields and resolves repo template stems", () => {
    const d = emptyDraft(config, tasks, {
      title: "  Fix it ",
      description: "x",
    });
    expect(buildPayload(d, config)).toMatchObject({
      title: "Fix it",
      description: "x",
      repo: "sidthe/grain",
      noRepo: false,
      base: "release-1",
      approved: true,
      atFront: false,
      nothingNeededAction: "close",
      dependsOn: [],
      attachments: [],
      reads: [],
      writes: [],
    });

    const rich = buildPayload(
      {
        ...d,
        sandboxCpus: 4,
        sandboxMemoryMb: 8192,
        sandboxDiskGb: 32,
        agentBackendId: "antigravity-ambient",
        agentFramework: "antigravity",
        capabilities: ["self-debug", "api:tasks-read"],
        reads: ["acme/web"],
        writes: [{ repo: "grain/config", base: "main" }],
        reviewTemplateId: "sec-review",
        reviewers: ["ana"],
        noPort: true,
        fixesBaseBranch: true,
        customFields: { team: "infra" },
      },
      config,
    );
    expect(rich).toMatchObject({
      sandboxCpus: 4,
      sandboxMemoryMb: 8192,
      sandboxDiskGb: 32,
      agentBackendId: "antigravity-ambient",
      agentFramework: "antigravity",
      capabilities: ["self-debug", "api:tasks-read"],
      reads: ["acme/web"],
      writes: [{ repo: "grain/config", base: "main" }],
      reviewTemplateId: "sec-review",
      reviewers: ["ana"],
      noPort: true,
      fixesBaseBranch: true,
      customFields: { team: "infra" },
    });

    const branch = buildPayload(
      { ...d, newBranch: true, newBranchName: " feat/x " },
      config,
    );
    expect(branch).toMatchObject({
      base: "feat/x",
      newBranch: true,
      newBranchFrom: "release-1",
    });

    const none = buildPayload({ ...d, repo: NO_REPO, newBranch: true }, config);
    expect(none).toMatchObject({ repo: "", noRepo: true, base: "" });
    expect(none).not.toHaveProperty("newBranch");

    expect(
      buildPayload(
        { ...d, networkAccess: "none", sandboxCpus: 8 },
        { ...config, sandboxShapeIgnored: true },
      ),
    ).not.toHaveProperty("sandboxCpus");

    expect(
      defaultCapabilitiesForRepo(
        {
          defaultCapabilities: ["a"],
          repoDefaultCapabilities: { "sidthe/grain": ["b", "c"] },
        },
        "SIDTHE/grain",
      ),
    ).toEqual(["b", "c"]);

    const repoTmpl: Template = {
      id: "declared:template:sidthe:grain:bugfix",
      name: "Bugfix",
      title: "Fix {{area}}",
      description: "",
      declaredIn: { repo: "sidthe/grain" },
    };
    expect(fromTemplateStem("sidthe/grain", repoTmpl)).toBe("bugfix");
    expect(fromTemplateStem("acme/web", repoTmpl)).toBe("");
    expect(
      fromTemplateStem("sidthe/grain", { ...repoTmpl, id: "triage" }),
    ).toBe("triage");
    expect(
      fromTemplateStem("sidthe/grain", { ...repoTmpl, system: true }),
    ).toBe("");
  });

  it("says what stops filing, including required template fields", () => {
    const d = emptyDraft(config, tasks);
    expect(validate(d)).toEqual({
      field: "title",
      message: "Give the task a title.",
    });
    expect(validate({ ...d, title: "t", newBranch: true })?.field).toBe(
      "newBranchName",
    );
    expect(validate({ ...d, title: "t" })).toBeNull();

    const tmpl: Template = {
      id: "bugfix",
      name: "Bugfix",
      title: "Fix {{component}}",
      description: "",
      fields: [{ name: "component", label: "Component", required: true }],
    };
    expect(validate(d, undefined, tmpl, {})).toEqual({
      field: "templateField",
      message: 'Fill in required template field "Component".',
    });
    expect(validate(d, undefined, tmpl, { component: "auth" })).toBeNull();

    const hidden = resolveFields({
      access: null,
      newTaskForm: {
        default: {
          fields: { newBranch: { visibility: "hidden", default: true } },
        },
      },
    });
    expect(validate({ ...d, title: "t", newBranch: true }, hidden)).toBeNull();
  });

  it("phrases the draft as a request to the Director", () => {
    const msg = directorMessage({
      ...emptyDraft(config, tasks),
      title: "Add dark mode",
      description: "To settings",
      dependsOn: ["4"],
    });
    expect(msg).toContain("**Add dark mode**");
    expect(msg).toContain("To settings");
    expect(msg).toContain("Repository: sidthe/grain");
    expect(msg).toContain("Depends on: #4");
  });

  it("includes origin and autoApproveProposals in the payload only when supplied", () => {
    const plain = buildPayload(emptyDraft(config, tasks), config);
    expect(plain).not.toHaveProperty("origin");
    expect(plain).not.toHaveProperty("autoApproveProposals");

    const wsPayload = buildPayload(
      emptyDraft(config, tasks, {
        workstream: true,
        origin: "",
        autoApproveProposals: true,
      }),
      config,
    );
    expect(wsPayload).toMatchObject({
      origin: "",
      autoApproveProposals: true,
    });

    const childPayload = buildPayload(
      emptyDraft(config, tasks, { origin: "task:12" }),
      config,
    );
    expect(childPayload).toMatchObject({ origin: "task:12" });
    expect(childPayload).not.toHaveProperty("autoApproveProposals");
  });
});
