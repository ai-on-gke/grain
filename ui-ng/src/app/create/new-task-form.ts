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

import type {
  AttachmentUpload,
  GrainConfig,
  Task,
  Template,
} from "../api/models";

// The builtin fields of ui/src/newTaskForm.js, plus foundIn, which only
// this form offers.
export type FieldId =
  | "title"
  | "description"
  | "attachments"
  | "customFields"
  | "repo"
  | "newBranch"
  | "base"
  | "writes"
  | "reads"
  | "autoMerge"
  | "noPort"
  | "fixesBaseBranch"
  | "foundIn"
  | "grants"
  | "model"
  | "dependsOn"
  | "agentBackendId"
  | "networkAccess"
  | "nothingNeededAction"
  | "reviewTemplateId"
  | "promptExtension"
  | "sandboxShape"
  | "approved"
  | "atFront";

export type Visibility = "shown" | "more" | "hidden";

export interface SandboxPreset {
  name: string;
  label?: string;
  cpus: number;
  memoryMb: number;
  diskGb?: number;
}

export interface FieldConfig {
  visibility: Visibility;
  order: number;
  label: string;
  helpText: string;
  default: unknown;
  allowNoRepo?: boolean;
  offered?: string[];
  presets?: SandboxPreset[];
  defaultPreset?: string;
  allowCustomNumbers?: boolean;
}

export const NO_REPO = "__none__";

// Clean, fast top-of-sheet layout by default: core fields are "shown", while
// secondary repo/capability/execution controls sit cleanly in collapsible sections
// ("Capabilities & context" and "More options") unless a deployment's
// newTaskForm config explicitly promotes or hides them.
const BUILTIN: Record<FieldId, { visibility: Visibility; order: number }> = {
  title: { visibility: "shown", order: 10 },
  description: { visibility: "shown", order: 20 },
  attachments: { visibility: "shown", order: 25 },
  customFields: { visibility: "shown", order: 30 },
  repo: { visibility: "shown", order: 40 },
  newBranch: { visibility: "shown", order: 45 },
  base: { visibility: "shown", order: 50 },
  autoMerge: { visibility: "shown", order: 70 },
  model: { visibility: "shown", order: 100 },
  dependsOn: { visibility: "shown", order: 110 },
  // Collapsible Capabilities & Context / Advanced fields:
  grants: { visibility: "more", order: 155 },
  reads: { visibility: "more", order: 160 },
  writes: { visibility: "more", order: 165 },
  noPort: { visibility: "more", order: 175 },
  fixesBaseBranch: { visibility: "more", order: 180 },
  foundIn: { visibility: "more", order: 185 },
  agentBackendId: { visibility: "more", order: 200 },
  networkAccess: { visibility: "more", order: 210 },
  nothingNeededAction: { visibility: "more", order: 220 },
  reviewTemplateId: { visibility: "more", order: 230 },
  promptExtension: { visibility: "more", order: 235 },
  sandboxShape: { visibility: "more", order: 240 },
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
    case "noPort":
    case "fixesBaseBranch":
      return false;
    case "writes":
    case "reads":
    case "dependsOn":
    case "attachments":
      return [];
    case "repo":
    case "grants":
    case "customFields":
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
      presets?: SandboxPreset[];
      default_preset?: string;
      allow_custom_numbers?: boolean;
    } | null
  >;
  capabilities?: {
    defaults?: string[];
    offered?: string[];
    hidden?: string[];
  };
}

interface FormConfig {
  default?: FormLayer;
  boards?: Record<string, FormLayer>;
  repos?: Record<string, FormLayer>;
  templates?: Record<string, FormLayer>;
}

function repoLayer(form: FormConfig, repo: string): FormLayer | null {
  if (!form.repos || !repo) return null;
  const key = Object.keys(form.repos).find(
    (k) => k.toLowerCase() === repo.toLowerCase(),
  );
  return key ? form.repos[key] : null;
}

export function resolveFields(
  config: GrainConfig | null,
  repo = "",
  templateId = "",
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
  const layers = [
    form.default,
    repoLayer(form, repo),
    templateId ? form.templates?.[templateId] : null,
  ].filter((l): l is FormLayer => !!l);
  for (const layer of layers) {
    for (const [rawId, fc] of Object.entries(layer.fields ?? {})) {
      const id = (rawId === "legacyCapabilities" ? "grants" : rawId) as FieldId;
      const f = out[id];
      if (!f || !fc) continue;
      if (fc.visibility) f.visibility = fc.visibility;
      if (typeof fc.order === "number") f.order = fc.order;
      if (fc.label) f.label = fc.label;
      if (fc.help_text) f.helpText = fc.help_text;
      if (fc.default !== undefined && fc.default !== null) {
        f.default = fc.default;
      }
      if (typeof fc.allow_no_repo === "boolean") {
        f.allowNoRepo = fc.allow_no_repo;
      }
      if (Array.isArray(fc.offered)) f.offered = fc.offered;
      if (Array.isArray(fc.presets)) f.presets = fc.presets;
      if (typeof fc.default_preset === "string") {
        f.defaultPreset = fc.default_preset;
      }
      if (typeof fc.allow_custom_numbers === "boolean") {
        f.allowCustomNumbers = fc.allow_custom_numbers;
      }
    }
  }
  return out;
}

export function defaultCapabilitiesForRepo(
  config: GrainConfig | null,
  repo: string,
): string[] {
  if (!config) return [];
  const form = (config["newTaskForm"] as FormConfig | null) ?? null;
  if (form) {
    const rl = repoLayer(form, repo);
    if (Array.isArray(rl?.capabilities?.defaults)) {
      return [...rl.capabilities.defaults];
    }
    if (Array.isArray(form.default?.capabilities?.defaults)) {
      return [...form.default.capabilities.defaults];
    }
  }
  const repoDefs = config.repoDefaultCapabilities;
  if (repoDefs && repo && repo !== NO_REPO) {
    const k = Object.keys(repoDefs).find(
      (x) => x.toLowerCase() === repo.toLowerCase(),
    );
    if (k && Array.isArray(repoDefs[k])) return [...repoDefs[k]];
  }
  if (Array.isArray(config.defaultCapabilities)) {
    return [...config.defaultCapabilities];
  }
  return (config.capabilities ?? []).filter((c) => c.default).map((c) => c.id);
}

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
  const dep = config?.["deployment"] as
    { targetRepos?: string[]; repos?: { repo?: string }[] } | undefined;
  const allowed = [
    ...((config?.["targetRepos"] as string[] | undefined) ?? []),
    ...(dep?.targetRepos ?? []),
    ...(dep?.repos?.map((x) => x.repo).filter(Boolean) ?? []),
  ];
  add(config?.["configRepo"]);
  if (allowed.length) allowed.forEach(add);
  else tasks.forEach((t) => add(t.repo));
  return [...seen.values()].sort((a, b) => a.localeCompare(b));
}

export function defaultTargetIn(
  config: GrainConfig | null,
  options: readonly string[],
): string {
  const pref = config?.["defaultTarget"];
  if (typeof pref !== "string") return "";
  return options.find((o) => o.toLowerCase() === pref.toLowerCase()) ?? "";
}

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

function suggestsBase(t: Task): boolean {
  const x = t as Task & { stacked?: boolean; authorKind?: string };
  if (x.state === "failed" || x.state === "closed") return false;
  if (x.stacked) return false;
  return !x.authorKind || x.authorKind === "human";
}

export function lastBaseForRepo(tasks: readonly Task[], repo: string): string {
  if (!repo || repo === NO_REPO) return "";
  let latest: Task | null = null;
  for (const t of tasks) {
    if (t.repo !== repo || !suggestsBase(t)) continue;
    if (!latest || (t.createdAt ?? "") > (latest.createdAt ?? "")) latest = t;
  }
  return (latest as (Task & { base?: string }) | null)?.base ?? "";
}

export function hasHistory(tasks: readonly Task[], repo: string): boolean {
  return tasks.some((t) => t.repo === repo && suggestsBase(t));
}

// Extracts a repository template stem suitable for POST /api/tasks/from-template
// (pkg/ui/tasks_from_template.go FromTemplateStem), or "" when the template
// must be launched through POST /api/tasks { templateId }.
export function fromTemplateStem(repo: string, tmpl: Template): string {
  if (!repo || repo === NO_REPO || tmpl.system) return "";
  const id = tmpl.id.trim();
  if (!id) return "";
  const [owner, name] = repo.trim().toLowerCase().split("/");
  if (!owner || !name) return "";
  const prefix = `declared:template:${owner}:${name}:`;
  if (id.toLowerCase().startsWith(prefix)) {
    const stem = id.slice(prefix.length);
    if (stem && !/[:/\\]/.test(stem) && !stem.startsWith(".")) return stem;
  }
  if (tmpl.declaredIn?.repo) {
    if (tmpl.declaredIn.repo.toLowerCase() !== repo.trim().toLowerCase()) {
      return "";
    }
    const lastColon = id.lastIndexOf(":");
    const stem = lastColon >= 0 ? id.slice(lastColon + 1) : id;
    if (stem && !/[:/\\]/.test(stem) && !stem.startsWith(".")) return stem;
  }
  if (!/[:/\\]/.test(id) && !id.startsWith(".")) {
    return id;
  }
  return "";
}

export interface NewTaskDraft {
  title: string;
  description: string;
  attachments: AttachmentUpload[];
  customFields: Record<string, string | string[]>;
  repo: string; // NO_REPO: none; "": the deployment's default target
  base: string;
  newBranch: boolean;
  newBranchName: string;
  reads: string[];
  writes: { repo: string; base?: string }[];
  autoMerge: boolean;
  noPort: boolean;
  fixesBaseBranch: boolean;
  // The release a bug is found in (model.LinkFoundIn): vX.Y.Z or
  // vX.Y.Z-rc.N, "" for none. The server checks it.
  foundIn: string;
  capabilities: string[] | null; // null = use deployment defaults unless edited
  model: string;
  agentFramework: string;
  agentBackendId: string;
  dependsOn: string[];
  networkAccess: string;
  nothingNeededAction: string;
  reviewTemplateId: string;
  reviewers: string[];
  promptExtension: string;
  sandboxCpus: number;
  sandboxMemoryMb: number;
  sandboxDiskGb: number;
  approved: boolean;
  atFront: boolean;
  origin?: string;
  parentId?: string;
  generatedFrom?: string;
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
  const seededCustom: Record<string, string | string[]> = {};
  for (const cf of config?.customFields ?? []) {
    if (cf.default !== undefined && cf.default !== null && cf.default !== "") {
      seededCustom[cf.name] = Array.isArray(cf.default)
        ? cf.default.map(String)
        : String(cf.default);
    }
  }
  return {
    title: "",
    description: "",
    attachments: [],
    customFields: seededCustom,
    repo,
    base: lastBaseForRepo(tasks, repo),
    newBranch: !!f.newBranch.default,
    newBranchName: "",
    reads: [],
    writes: [],
    autoMerge: !!f.autoMerge.default,
    noPort: !!f.noPort.default,
    fixesBaseBranch: !!f.fixesBaseBranch.default,
    foundIn: String(f.foundIn.default ?? ""),
    capabilities: null,
    model: String(f.model.default ?? ""),
    agentFramework: "",
    agentBackendId: String(f.agentBackendId.default ?? ""),
    dependsOn: [],
    networkAccess: String(f.networkAccess.default ?? ""),
    nothingNeededAction: String(f.nothingNeededAction.default || "close"),
    reviewTemplateId: String(f.reviewTemplateId.default ?? ""),
    reviewers: [],
    promptExtension: String(f.promptExtension.default ?? ""),
    sandboxCpus: 0,
    sandboxMemoryMb: 0,
    sandboxDiskGb: 0,
    approved: !!f.approved.default,
    atFront: !!f.atFront.default,
    ...prefill,
  };
}

export function buildPayload(
  d: NewTaskDraft,
  config: GrainConfig | null,
  templateOpts?: { templateId: string; fields: Record<string, string> } | null,
): Record<string, unknown> {
  const none = d.repo === NO_REPO;
  const creating = d.newBranch && !none;
  const shapeIgnored = !!config?.["sandboxShapeIgnored"];
  const hasCustom = Object.keys(d.customFields ?? {}).some((k) => {
    const v = d.customFields[k];
    return Array.isArray(v) ? v.length > 0 : String(v ?? "").trim() !== "";
  });

  return {
    title: d.title.trim(),
    description: d.description,
    ...(templateOpts
      ? { templateId: templateOpts.templateId, fields: templateOpts.fields }
      : {}),
    repo: none ? "" : d.repo,
    noRepo: none,
    ...(creating
      ? { base: d.newBranchName.trim(), newBranch: true, newBranchFrom: d.base }
      : { base: none ? "" : d.base }),
    autoMerge: d.autoMerge,
    ...(d.noPort ? { noPort: true } : {}),
    ...(d.fixesBaseBranch && !none ? { fixesBaseBranch: true } : {}),
    ...(d.foundIn.trim() ? { foundIn: d.foundIn.trim() } : {}),
    model: d.model,
    ...(d.agentFramework ? { agentFramework: d.agentFramework } : {}),
    ...(d.agentBackendId ? { agentBackendId: d.agentBackendId } : {}),
    networkAccess: shapeIgnored ? "" : d.networkAccess,
    ...(!shapeIgnored && d.sandboxCpus > 0
      ? { sandboxCpus: d.sandboxCpus }
      : {}),
    ...(!shapeIgnored && d.sandboxMemoryMb > 0
      ? { sandboxMemoryMb: d.sandboxMemoryMb }
      : {}),
    ...(!shapeIgnored && d.sandboxDiskGb > 0
      ? { sandboxDiskGb: d.sandboxDiskGb }
      : {}),
    nothingNeededAction: d.nothingNeededAction,
    ...(d.reviewTemplateId ? { reviewTemplateId: d.reviewTemplateId } : {}),
    ...(d.reviewers.length ? { reviewers: d.reviewers } : {}),
    promptExtension: d.promptExtension,
    dependsOn: d.dependsOn,
    reads: d.reads,
    writes: d.writes,
    attachments: d.attachments,
    ...(d.capabilities !== null ? { capabilities: d.capabilities } : {}),
    ...(hasCustom ? { customFields: d.customFields } : {}),
    approved: d.approved,
    atFront: d.atFront,
    ...(d.origin !== undefined ? { origin: d.origin } : {}),
    ...(d.parentId ? { parentId: d.parentId } : {}),
    ...(d.autoApproveProposals !== undefined
      ? { autoApproveProposals: d.autoApproveProposals }
      : {}),
  };
}

export interface Problem {
  field: "title" | "newBranchName" | "templateField" | "foundIn" | "form";
  message: string;
}

const FOUND_IN_TAG_RE = /^v?\d+\.\d+\.\d+(?:-rc\.\d+)?$/;

export function validate(
  d: NewTaskDraft,
  fields?: Record<FieldId, FieldConfig>,
  template?: Template | null,
  templateValues?: Record<string, string>,
): Problem | null {
  if (template) {
    for (const tf of template.fields ?? []) {
      if (tf.required && !(templateValues?.[tf.name] ?? "").trim()) {
        return {
          field: "templateField",
          message: `Fill in required template field "${tf.label || tf.name}".`,
        };
      }
    }
  } else if (!d.title.trim()) {
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
  const foundInShown = !fields || fields.foundIn.visibility !== "hidden";
  if (
    foundInShown &&
    d.foundIn.trim() &&
    !FOUND_IN_TAG_RE.test(d.foundIn.trim())
  ) {
    return {
      field: "foundIn",
      message: "Use a release tag such as v3.6.0 or v3.6.0-rc.2.",
    };
  }
  return null;
}

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
