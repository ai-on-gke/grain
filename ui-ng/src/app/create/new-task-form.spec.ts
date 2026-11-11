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

import type { GrainConfig, Task } from "../api/models";
import {
  buildPayload,
  defaultRepo,
  directorMessage,
  emptyDraft,
  knownRepos,
  lastBaseForRepo,
  resolveFields,
  validate,
} from "./new-task-form";

const config: GrainConfig = {
  access: null,
  configRepo: "grain/config",
  defaultTarget: "SIDTHE/grain",
  approvedByDefault: true,
  autoMergeByDefault: false,
  newestFirst: false,
  deployment: { repos: [{ repo: "sidthe/grain" }], targetRepos: ["acme/web"] },
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
] as Task[];

describe("new task form", () => {
  it("offers the config repo, deployment repos and task repos once each, sorted", () => {
    expect(knownRepos(config, tasks)).toEqual([
      "acme/api",
      "acme/web",
      "grain/config",
      "sidthe/grain",
    ]);
  });

  it("starts on the deployment's default target, matched case-insensitively", () => {
    expect(defaultRepo(config, knownRepos(config, tasks))).toBe("sidthe/grain");
    expect(defaultRepo({ access: null }, ["x/y"])).toBe("x/y");
  });

  it("repeats the newest task's base branch for the repo", () => {
    expect(lastBaseForRepo(tasks, "sidthe/grain")).toBe("release-1");
    expect(lastBaseForRepo(tasks, "acme/web")).toBe("");
  });

  it("takes defaults from the config and layers settings/new-task-form.yml", () => {
    const f = resolveFields(
      {
        ...config,
        newTaskForm: {
          default: {
            fields: {
              model: { visibility: "more", default: "fast" },
              title: { label: "What needs doing" },
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
    expect(f.approved.default).toBe(true);
    expect(f.autoMerge.default).toBe(false);
    expect(f.model).toMatchObject({ visibility: "more", default: "fast" });
    expect(f.title.label).toBe("What needs doing");
    expect(f.networkAccess.visibility).toBe("hidden");
    expect(resolveFields(null).nothingNeededAction.default).toBe("close");
  });

  it("builds the body the classic form sends", () => {
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
    const none = buildPayload({ ...d, repo: "", newBranch: true }, config);
    expect(none).toMatchObject({ repo: "", noRepo: true, base: "" });
    expect(none).not.toHaveProperty("newBranch");
    expect(
      buildPayload(
        { ...d, networkAccess: "none" },
        {
          ...config,
          sandboxShapeIgnored: true,
        },
      ).networkAccess,
    ).toBe("");
  });

  it("says what stops filing", () => {
    const d = emptyDraft(config, tasks);
    expect(validate(d)).toBe("Give the task a title.");
    expect(validate({ ...d, title: "t", newBranch: true })).toMatch(/branch/);
    expect(validate({ ...d, title: "t" })).toBeNull();
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
});
