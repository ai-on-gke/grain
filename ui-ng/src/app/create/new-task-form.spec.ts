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
  defaultAdmissionForRepo,
  directorMessage,
  emptyDraft,
  forkHeadProblem,
  fromTemplateStem,
  lastBaseForRepo,
  openingRepo,
  priorityProblem,
  resolveFields,
  targetableRepos,
  templatePriority,
  validate,
} from "./new-task-form";
import { forkHeadHelp, parseForkHead } from "./fork-head";
import {
  PRIORITY_REFUSAL,
  taskPriority,
  wholeNumber,
} from "../shared/task-priority";

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
    expect(Object.keys(f)).toHaveLength(25);
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
        foundIn: " v2.5.0-rc.2 ",
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
      foundIn: "v2.5.0-rc.2",
      customFields: { team: "infra" },
    });
    // No release stated: the body says nothing of one, and the server
    // records what the task's own title and branches say.
    expect(buildPayload(d, config)).not.toHaveProperty("foundIn");
    expect(resolveFields(null).foundIn).toMatchObject({
      visibility: "more",
      default: "",
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
    // The server's own declared id, with no declaredIn beside it.
    expect(
      fromTemplateStem("sidthe/grain", {
        ...repoTmpl,
        id: "repo:template:sidthe:grain:triage",
        declaredIn: undefined,
      }),
    ).toBe("triage");
    // A stored template (POST /api/templates) is not the repository's:
    // it files through POST /api/tasks { templateId }, never from-template.
    expect(
      fromTemplateStem("sidthe/grain", {
        id: "template-3",
        name: "Urgent fix",
        title: "Urgent: ",
        description: "",
        priority: 8,
      }),
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
    expect(validate({ ...d, title: "t", foundIn: "not-a-tag" })).toEqual({
      field: "foundIn",
      message: "Use a release tag such as v3.6.0 or v3.6.0-rc.2.",
    });
    expect(validate({ ...d, title: "t", foundIn: "v3.6.0" })).toBeNull();
    expect(validate({ ...d, title: "t", foundIn: "v3.6.0-rc.2" })).toBeNull();

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

    // Missing repository when the deployment has no defaultTarget:
    const noDefaultCfg: GrainConfig = { access: null };
    const noRepoChosen = emptyDraft(noDefaultCfg, tasks, { title: "t" });
    expect(validate(noRepoChosen, undefined, null, {}, noDefaultCfg)).toEqual({
      field: "repo",
      message: "Pick a repository, or choose No repository.",
    });
    const disallowNoRepo = resolveFields({
      access: null,
      newTaskForm: {
        default: { fields: { repo: { allow_no_repo: false } } },
      },
    });
    expect(
      validate(noRepoChosen, disallowNoRepo, null, {}, noDefaultCfg),
    ).toEqual({
      field: "repo",
      message: "Pick a repository.",
    });
    expect(
      validate(
        { ...noRepoChosen, repo: NO_REPO },
        undefined,
        null,
        {},
        noDefaultCfg,
      ),
    ).toBeNull();
    expect(validate(noRepoChosen, undefined, null, {}, config)).toBeNull();
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

  it("reads Task priority as a whole number from 1 up, exponents included", () => {
    expect(taskPriority("7")).toBe(7);
    expect(taskPriority(" 1e3 ")).toBe(1000);
    expect(taskPriority("1")).toBe(1);
    for (const bad of ["0", "-2", "2.5", "abc", "", "9007199254740993"]) {
      expect(taskPriority(bad)).toBeNaN();
    }
    expect(wholeNumber("8e3")).toBe(8000);
    expect(PRIORITY_REFUSAL).toBe(
      "a whole number, 1 or more; default 5, higher numbers run first",
    );
  });

  it("files Task priority, Owner, Keep running and Admission", () => {
    const d = emptyDraft(config, tasks, { title: "x" });
    expect(d).toMatchObject({
      priority: "5",
      owner: "",
      keepRunning: false,
      admission: "standard",
    });
    // The defaults say nothing: the server's own are the same.
    const plain = buildPayload(d, config);
    expect(plain).not.toHaveProperty("priority");
    expect(plain).not.toHaveProperty("owner");
    expect(plain).not.toHaveProperty("keepRunning");
    expect(plain).not.toHaveProperty("admission");

    expect(
      buildPayload(
        {
          ...d,
          priority: "1e3",
          owner: "  alice@example.com ",
          keepRunning: true,
          admission: "durable",
        },
        config,
      ),
    ).toMatchObject({
      priority: 1000,
      owner: "alice@example.com",
      keepRunning: true,
      admission: "durable",
    });
    expect(
      buildPayload({ ...d, admission: "standard" }, config),
    ).not.toHaveProperty("admission");
    // With a template picked a 5 is stated, or the template's own priority
    // would be filed instead of the one chosen.
    expect(
      buildPayload(d, config, { templateId: "t", fields: {} }),
    ).toMatchObject({ priority: 5 });
    expect(templatePriority({ id: "t", priority: 8 } as Template)).toBe("8");
    expect(templatePriority(null)).toBe("5");
  });

  it("starts Admission on the server's own default and says Standard when it is not", () => {
    // The higher of the deployment's and the repo's, as
    // defaultTaskAdmission in pkg/ui/client.go picks it.
    const cfg: GrainConfig = {
      ...config,
      defaultAdmission: "bypass",
      repoDefaultAdmissions: {
        "Acme/Web": "durable",
        "sidthe/grain": "standard",
      },
    };
    expect(defaultAdmissionForRepo(config, "acme/web")).toBe("standard");
    expect(defaultAdmissionForRepo(cfg, "acme/web")).toBe("durable");
    expect(defaultAdmissionForRepo(cfg, "sidthe/grain")).toBe("bypass");
    expect(defaultAdmissionForRepo(cfg, NO_REPO)).toBe("bypass");
    // "" is the default target, SIDTHE/grain here.
    expect(defaultAdmissionForRepo(cfg, "")).toBe("bypass");

    const durable: GrainConfig = { ...config, defaultAdmission: "durable" };
    const d = emptyDraft(durable, tasks, { title: "x", repo: "acme/web" });
    expect(d.admission).toBe("durable");
    // Left on the default, nothing is said and the server files durable.
    expect(buildPayload(d, durable)).not.toHaveProperty("admission");
    // Standard picked on a durable deployment is said, or the task would
    // be filed durable anyway.
    expect(
      buildPayload({ ...d, admission: "standard" }, durable),
    ).toMatchObject({ admission: "standard" });
    // A template picked always hears it: its own would win otherwise.
    expect(
      buildPayload({ ...d, admission: "durable" }, durable, {
        templateId: "t",
        fields: {},
      }),
    ).toMatchObject({ admission: "durable" });

    // settings/new-task-form.yml places Admission like any other field.
    const placed: GrainConfig = {
      ...config,
      newTaskForm: {
        default: {
          fields: { admission: { visibility: "hidden", default: "bypass" } },
        },
      },
    };
    expect(resolveFields(placed).admission.visibility).toBe("hidden");
    expect(emptyDraft(placed, tasks, { title: "x" }).admission).toBe("bypass");
  });

  it("refuses a zero, negative or non-whole priority before submit", () => {
    const d = emptyDraft(config, tasks, { title: "x" });
    expect(priorityProblem(d)).toBeNull();
    expect(priorityProblem({ ...d, priority: "" })).toBeNull();
    expect(priorityProblem({ ...d, priority: "1e3" })).toBeNull();
    for (const bad of ["0", "-3", "2.5"]) {
      expect(validate({ ...d, priority: bad })).toEqual({
        field: "priority",
        message: "Fix the task priority: it must be a whole number, 1 or more.",
      });
    }
  });

  it("explains a well-formed fork head and refuses a malformed one", () => {
    expect(parseForkHead("main")).toBeNull();
    expect(parseForkHead("release/1.2")).toBeNull();
    expect(parseForkHead(" octo:fix/thing ")).toEqual({
      owner: "octo",
      branch: "fix/thing",
    });
    expect(parseForkHead("bad:label:here")).toEqual({
      error: `"bad:label:here" has more than one ':': a fork head is <owner>:<branch>`,
    });
    expect(parseForkHead("  :nope")).toEqual({
      error: `":nope" is not a fork head: it needs both an owner and a branch, as <owner>:<branch>`,
    });
    expect(parseForkHead("-x:y")).toEqual({
      error: `"-x:y" is not a fork head: "-x" is not a GitHub login`,
    });
    expect(parseForkHead("octo:a..b")).toEqual({
      error: `"octo:a..b" is not a fork head: "a..b" is not a usable branch name`,
    });
    expect(forkHeadHelp({ owner: "octo", branch: "fix" }, "acme/web")).toBe(
      "GitHub fork head: branch fix on octo's fork. grain files this task against the open pull request on acme/web it heads.",
    );

    const d = emptyDraft(config, tasks, { title: "x", base: "bad:label:here" });
    const fields = resolveFields(config);
    expect(validate(d, fields)).toMatchObject({ field: "base" });
    expect(forkHeadProblem({ ...d, base: "octo:fix" }, fields)).toBeNull();
    // Cutting a new branch from it, or filing with no repo, uses no label.
    expect(
      forkHeadProblem({ ...d, newBranch: true, newBranchName: "n" }, fields),
    ).toBeNull();
    expect(forkHeadProblem({ ...d, repo: NO_REPO }, fields)).toBeNull();
  });
});
