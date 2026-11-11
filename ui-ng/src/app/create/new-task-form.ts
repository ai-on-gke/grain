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

// The fields this sheet renders, in newTaskForm.js's builtin order. The
// rest of the classic form (attachments, capabilities, reads and writes,
// templates, sandbox shape, review) stays on the classic page, linked
// from the sheet, until later milestones bring them over.
export type FieldId =
  | "title"
  | "description"
  | "repo"
  | "newBranch"
  | "base"
  | "autoMerge"
  | "model"
  | "dependsOn"
  | "networkAccess"
  | "nothingNeededAction"
  | "promptExtension"
  | "approved"
  | "atFront";

export type Visibility = "shown" | "more" | "hidden";

export interface FieldConfig {
  visibility: Visibility;
  order: number;
  label: string;
  helpText: string;
  default: unknown;
  // repo: whether "No repository" is offered (allow_no_repo).
  allowNoRepo?: boolean;
  // model, networkAccess: the values the picker offers (offered).
  offered?: string[];
}

// NO_REPO is the repository picker's value for a task with no repository
// (it answers in a comment). "" is the deployment's default target,
// which the server fills in (Config.DefaultTarget).
export const NO_REPO = "__none__";

const BUILTIN: Record<FieldId, { visibility: Visibility; order: number }> = {
  title: { visibility: "shown", order: 10 },
  description: { visibility: "shown", order: 20 },
  repo: { visibility: "shown", order: 40 },
  newBranch: { visibility: "shown", order: 45 },
  base: { visibility: "shown", order: 50 },
  autoMerge: { visibility: "shown", order: 70 },
  model: { visibility: "shown", order: 100 },
  dependsOn: { visibility: "shown", order: 110 },
  networkAccess: { visibility: "more", order: 210 },
  nothingNeededAction: { visibility: "more", order: 220 },
  promptExtension: { visibility: "more", order: 235 },
  approved: { visibility: "shown", order: 300 },
  atFront: { visibility: "shown", order: 310 },
};

function builtinDefault(id: FieldId, config: GrainConfig | null): unknown {
  switch (id) {
    case "autoMerge":
      return !!config?.["autoMergeByDefault"];
    case "approved":
      return !!config?.["approvedByDefault"];
    case "atFront":
      return !!config?.["newestFirst"];
    case "nothingNeededAction":
      return "close";
    case "newBranch":
      return false;
    case "dependsOn":
      return [];
    case "repo":
      return undefined;
    default:
      return "";
  }
}

interface FormLayer {
  fields?: Record<
    string,
    {
      visibility?: Visibility;
      order?: number;
      label?: string;
      help_text?: string;
      default?: unknown;
      allow_no_repo?: boolean;
      offered?: string[];
    } | null
  >;
}

interface FormConfig {
  default?: FormLayer;
  repos?: Record<string, FormLayer>;
}

function repoLayer(form: FormConfig, repo: string): FormLayer | null {
  if (!form.repos || !repo) return null;
  const key = Object.keys(form.repos).find(
    (k) => k.toLowerCase() === repo.toLowerCase(),
  );
  return key ? form.repos[key] : null;
}

// resolveFields is resolveNewTaskForm (ui/src/newTaskForm.js) for the
// fields this sheet renders: the builtin list, then the deployment's
// settings/new-task-form.yml layer, then the picked repository's.
export function resolveFields(
  config: GrainConfig | null,
  repo = "",
): Record<FieldId, FieldConfig> {
  const out = {} as Record<FieldId, FieldConfig>;
  for (const id of Object.keys(BUILTIN) as FieldId[]) {
    out[id] = {
      ...BUILTIN[id],
      label: "",
      helpText: "",
      default: builtinDefault(id, config),
    };
  }
  const form = (config?.["newTaskForm"] as FormConfig | null) ?? null;
  if (!form) return out;
  const layers = [form.default, repoLayer(form, repo)].filter(
    (l): l is FormLayer => !!l,
  );
  for (const layer of layers) {
    for (const [id, fc] of Object.entries(layer.fields ?? {})) {
      const f = out[id as FieldId];
      if (!f || !fc) continue;
      if (fc.visibility) f.visibility = fc.visibility;
      if (typeof fc.order === "number") f.order = fc.order;
      if (fc.label) f.label = fc.label;
      if (fc.help_text) f.helpText = fc.help_text;
      if (fc.default !== undefined && fc.default !== null) {
        f.default = fc.default;
      }
      if (typeof fc.allow_no_repo === "boolean")
        f.allowNoRepo = fc.allow_no_repo;
      if (Array.isArray(fc.offered)) f.offered = fc.offered;
    }
  }
  return out;
}

// targetableRepos is what the repository picker offers (ui/src/state.js
// targetableRepos): the config repository plus the deployment's allowed
// target repos; with no allowlist, every repo a task has used. Offering
// repos off the allowlist would file tasks that only park.
export function targetableRepos(
  config: GrainConfig | null,
  tasks: readonly Task[],
): string[] {
  const seen = new Map<string, string>();
  const add = (r: unknown) => {
    if (typeof r !== "string" || !r) return;
    const k = r.toLowerCase();
    if (!seen.has(k)) seen.set(k, r);
  };
  const dep = config?.["deployment"] as { targetRepos?: string[] } | undefined;
  const allowed = [
    ...((config?.["targetRepos"] as string[] | undefined) ?? []),
    ...(dep?.targetRepos ?? []),
  ];
  add(config?.["configRepo"]);
  if (allowed.length) allowed.forEach(add);
  else tasks.forEach((t) => add(t.repo));
  return [...seen.values()].sort((a, b) => a.localeCompare(b));
}

// defaultTargetIn is the deployment's default target when the picker
// offers it, else "".
export function defaultTargetIn(
  config: GrainConfig | null,
  options: readonly string[],
): string {
  const pref = config?.["defaultTarget"];
  if (typeof pref !== "string") return "";
  return options.find((o) => o.toLowerCase() === pref.toLowerCase()) ?? "";
}

// openingRepo is where the picker starts (NewTaskOverlay.jsx): the
// opener's repo, else the deployment's default target, else the form's
// repo default ("none" meaning no repository), else "" -- the server's
// default target -- never just the first repo alphabetically
// (grain/task-1533).
export function openingRepo(
  config: GrainConfig | null,
  options: readonly string[],
  prefillRepo?: string,
): string {
  if (prefillRepo !== undefined) return prefillRepo;
  const target = defaultTargetIn(config, options);
  if (target) return target;
  const formDefault = resolveFields(config).repo.default;
  const d = typeof formDefault === "string" ? formDefault.trim() : "";
  if (d.toLowerCase() === "none") return NO_REPO;
  return d;
}

// Tasks that say nothing about where a repo's work lives now (state.js
// suggestsBase): failed or closed ones, stacked reviews and repairs, and
// anything not filed by a person.
function suggestsBase(t: Task): boolean {
  const x = t as Task & { stacked?: boolean; authorKind?: string };
  if (x.state === "failed" || x.state === "closed") return false;
  if (x.stacked) return false;
  return !x.authorKind || x.authorKind === "human";
}

// lastBaseForRepo is the base branch the newest hand-filed task on a
// repo used, so the next one starts there too (state.js lastBaseForRepo).
export function lastBaseForRepo(tasks: readonly Task[], repo: string): string {
  if (!repo || repo === NO_REPO) return "";
  let latest: Task | null = null;
  for (const t of tasks) {
    if (t.repo !== repo || !suggestsBase(t)) continue;
    if (!latest || (t.createdAt ?? "") > (latest.createdAt ?? "")) latest = t;
  }
  return (latest as (Task & { base?: string }) | null)?.base ?? "";
}

// hasHistory says whether picking the repo has a base to suggest at all.
export function hasHistory(tasks: readonly Task[], repo: string): boolean {
  return tasks.some((t) => t.repo === repo && suggestsBase(t));
}

export interface NewTaskDraft {
  title: string;
  description: string;
  repo: string; // NO_REPO: none; "": the deployment's default target
  base: string;
  newBranch: boolean;
  newBranchName: string;
  autoMerge: boolean;
  model: string;
  dependsOn: string[];
  networkAccess: string;
  nothingNeededAction: string;
  promptExtension: string;
  approved: boolean;
  atFront: boolean;
  origin?: string;
  workstream?: boolean;
  autoApproveProposals?: boolean;
}

export function emptyDraft(
  config: GrainConfig | null,
  tasks: readonly Task[],
  prefill: Partial<NewTaskDraft> = {},
): NewTaskDraft {
  const repos = targetableRepos(config, tasks);
  const repo = openingRepo(config, repos, prefill.repo);
  const f = resolveFields(config, repo);
  return {
    title: "",
    description: "",
    repo,
    base: lastBaseForRepo(tasks, repo),
    newBranch: !!f.newBranch.default,
    newBranchName: "",
    autoMerge: !!f.autoMerge.default,
    model: String(f.model.default ?? ""),
    dependsOn: [],
    networkAccess: String(f.networkAccess.default ?? ""),
    nothingNeededAction: String(f.nothingNeededAction.default || "close"),
    promptExtension: String(f.promptExtension.default ?? ""),
    approved: !!f.approved.default,
    atFront: !!f.atFront.default,
    ...prefill,
  };
}

// buildPayload is the POST /api/tasks body for a draft, matching what
// the classic NewTaskOverlay sends for the same fields: a task creating
// its branch is filed onto that branch with newBranchFrom as its base;
// a task with no repository says noRepo and names no base.
export function buildPayload(
  d: NewTaskDraft,
  config: GrainConfig | null,
): Record<string, unknown> {
  const none = d.repo === NO_REPO;
  const creating = d.newBranch && !none;
  return {
    title: d.title.trim(),
    description: d.description,
    repo: none ? "" : d.repo,
    noRepo: none,
    ...(creating
      ? { base: d.newBranchName.trim(), newBranch: true, newBranchFrom: d.base }
      : { base: none ? "" : d.base }),
    autoMerge: d.autoMerge,
    model: d.model,
    // "" is the deployment's own setting; a deployment that cannot honour
    // a per-task network (sandboxShapeIgnored) gets no override.
    networkAccess: config?.["sandboxShapeIgnored"] ? "" : d.networkAccess,
    nothingNeededAction: d.nothingNeededAction,
    promptExtension: d.promptExtension,
    dependsOn: d.dependsOn,
    reads: [],
    writes: [],
    attachments: [],
    approved: d.approved,
    atFront: d.atFront,
    ...(d.origin !== undefined ? { origin: d.origin } : {}),
    ...(d.autoApproveProposals !== undefined
      ? { autoApproveProposals: d.autoApproveProposals }
      : {}),
  };
}

export interface Problem {
  field: "title" | "newBranchName";
  message: string;
}

// validate says what stops the draft being filed, or null. A hidden New
// branch field asks for no name: nobody could type one.
export function validate(
  d: NewTaskDraft,
  fields?: Record<FieldId, FieldConfig>,
): Problem | null {
  if (!d.title.trim()) {
    return { field: "title", message: "Give the task a title." };
  }
  const branchShown = !fields || fields.newBranch.visibility !== "hidden";
  if (
    branchShown &&
    d.newBranch &&
    d.repo !== NO_REPO &&
    !d.newBranchName.trim()
  ) {
    return {
      field: "newBranchName",
      message: "Name the new branch, or untick the new-branch box.",
    };
  }
  return null;
}

// directorMessage is what "Ask the Director" sends: the draft as a
// request to file (or shape) the task, so the Director can ask what is
// missing before anything runs.
export function directorMessage(d: NewTaskDraft): string {
  const lines = [
    "Please help me file this as a task (ask me anything that is missing):",
    "",
    `**${d.title.trim() || "(no title yet)"}**`,
  ];
  if (d.description.trim()) lines.push("", d.description.trim());
  lines.push(
    "",
    `Repository: ${d.repo === NO_REPO ? "none" : d.repo || "the default target"}`,
  );
  if (d.repo !== NO_REPO && d.base) lines.push(`Base branch: ${d.base}`);
  if (d.dependsOn.length) {
    lines.push(`Depends on: ${d.dependsOn.map((id) => `#${id}`).join(", ")}`);
  }
  return lines.join("\n");
}
