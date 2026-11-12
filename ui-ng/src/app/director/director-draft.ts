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

export interface TaskShapeFields {
  title: string;
  description: string;
  repo: string;
  base: string;
  priority: number;
  model: string;
  dependsOn: string[];
}

export interface DraftFieldDiff {
  field: keyof TaskShapeFields;
  label: string;
  before: string;
  after: string;
}

export interface ProposedPlanTask {
  key?: string;
  title: string;
  description?: string;
  repo?: string;
  base?: string;
  priority?: number;
  model?: string;
  dependsOn?: string[];
  after?: string[];
}

export interface GrainPlanBlock {
  title?: string;
  summary?: string;
  repo?: string;
  base?: string;
  tasks: ProposedPlanTask[];
}

const DRAFT_FENCE_RE = /```(?:grain-draft|json)\s*\n([\s\S]*?)```/gi;
const PLAN_FENCE_RE = /```grain-plan\s*\n([\s\S]*?)```/gi;

// Extracts structured `grain-draft` JSON blocks from a Director message.
export function extractGrainDrafts(
  markdown: string,
): Partial<TaskShapeFields>[] {
  if (!markdown) return [];
  const out: Partial<TaskShapeFields>[] = [];
  DRAFT_FENCE_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = DRAFT_FENCE_RE.exec(markdown)) !== null) {
    const langTag = m[0].slice(3, 14).toLowerCase();
    const raw = m[1].trim();
    if (!raw.startsWith("{")) continue;
    try {
      const parsed = JSON.parse(raw) as Record<string, unknown>;
      // Only treat plain ```json``` blocks as drafts when tagged with grainDraft.
      if (!langTag.startsWith("grain-draft") && !parsed["grainDraft"]) {
        continue;
      }
      const patch = normalizeDraftObject(parsed);
      if (Object.keys(patch).length > 0) {
        out.push(patch);
      }
    } catch {
      // Ignore malformed JSON blocks.
    }
  }
  return out;
}

// Extracts structured `grain-plan` multi-task workstream blocks from a session reply.
export function extractGrainPlans(markdown: string): GrainPlanBlock[] {
  if (!markdown) return [];
  const out: GrainPlanBlock[] = [];
  PLAN_FENCE_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = PLAN_FENCE_RE.exec(markdown)) !== null) {
    const raw = m[1].trim();
    if (!raw.startsWith("{")) continue;
    try {
      const parsed = JSON.parse(raw) as Record<string, unknown>;
      const rawTasks = Array.isArray(parsed["tasks"]) ? parsed["tasks"] : [];
      const tasks: ProposedPlanTask[] = [];
      for (const item of rawTasks) {
        if (!item || typeof item !== "object") continue;
        const r = item as Record<string, unknown>;
        const title = typeof r["title"] === "string" ? r["title"].trim() : "";
        if (!title) continue;
        tasks.push({
          key: typeof r["key"] === "string" ? r["key"].trim() : undefined,
          title,
          description:
            typeof r["description"] === "string" ? r["description"] : undefined,
          repo: typeof r["repo"] === "string" ? r["repo"].trim() : undefined,
          base: typeof r["base"] === "string" ? r["base"].trim() : undefined,
          priority:
            typeof r["priority"] === "number" ? r["priority"] : undefined,
          model: typeof r["model"] === "string" ? r["model"].trim() : undefined,
          dependsOn: stringArray(r["dependsOn"] ?? r["depends_on"]),
          after: stringArray(r["after"]),
        });
      }
      if (tasks.length > 0) {
        out.push({
          title:
            typeof parsed["title"] === "string"
              ? parsed["title"].trim()
              : undefined,
          summary:
            typeof parsed["summary"] === "string"
              ? parsed["summary"].trim()
              : undefined,
          repo:
            typeof parsed["repo"] === "string"
              ? parsed["repo"].trim()
              : undefined,
          base:
            typeof parsed["base"] === "string"
              ? parsed["base"].trim()
              : undefined,
          tasks,
        });
      }
    } catch {
      // Ignore malformed plan blocks.
    }
  }
  return out;
}

function stringArray(v: unknown): string[] | undefined {
  if (!Array.isArray(v)) return undefined;
  const list = v
    .map((x) => (typeof x === "string" ? x.trim() : String(x ?? "").trim()))
    .filter(Boolean);
  return list.length > 0 ? list : undefined;
}

function normalizeDraftObject(
  obj: Record<string, unknown>,
): Partial<TaskShapeFields> {
  const patch: Partial<TaskShapeFields> = {};
  if (typeof obj["title"] === "string" && obj["title"].trim()) {
    patch.title = obj["title"].trim();
  }
  if (typeof obj["description"] === "string") {
    patch.description = obj["description"];
  }
  if (typeof obj["repo"] === "string") {
    patch.repo = obj["repo"].trim();
  }
  if (typeof obj["base"] === "string") {
    patch.base = obj["base"].trim();
  }
  if (typeof obj["priority"] === "number" && Number.isFinite(obj["priority"])) {
    patch.priority = obj["priority"];
  }
  if (typeof obj["model"] === "string") {
    patch.model = obj["model"].trim();
  }
  const deps = obj["dependsOn"] ?? obj["depends_on"];
  if (Array.isArray(deps)) {
    patch.dependsOn = deps.map((x) => String(x ?? "").trim()).filter(Boolean);
  }
  return patch;
}

export function diffDraftFields(
  current: TaskShapeFields,
  proposed: Partial<TaskShapeFields>,
): DraftFieldDiff[] {
  const diffs: DraftFieldDiff[] = [];
  if (
    proposed.title !== undefined &&
    proposed.title.trim() !== current.title.trim()
  ) {
    diffs.push({
      field: "title",
      label: "Title",
      before: current.title || "(empty)",
      after: proposed.title,
    });
  }
  if (proposed.repo !== undefined && proposed.repo !== current.repo) {
    diffs.push({
      field: "repo",
      label: "Repository",
      before: current.repo || "(none)",
      after: proposed.repo || "(none)",
    });
  }
  if (proposed.base !== undefined && proposed.base !== current.base) {
    diffs.push({
      field: "base",
      label: "Base branch",
      before: current.base || "(default)",
      after: proposed.base || "(default)",
    });
  }
  if (
    proposed.priority !== undefined &&
    proposed.priority !== current.priority
  ) {
    diffs.push({
      field: "priority",
      label: "Priority",
      before: String(current.priority),
      after: String(proposed.priority),
    });
  }
  if (proposed.model !== undefined && proposed.model !== current.model) {
    diffs.push({
      field: "model",
      label: "Model",
      before: current.model || "(default)",
      after: proposed.model || "(default)",
    });
  }
  if (proposed.dependsOn !== undefined) {
    const beforeDeps = current.dependsOn.join(", ");
    const afterDeps = proposed.dependsOn.join(", ");
    if (beforeDeps !== afterDeps) {
      diffs.push({
        field: "dependsOn",
        label: "Dependencies",
        before: beforeDeps || "(none)",
        after: afterDeps || "(none)",
      });
    }
  }
  if (
    proposed.description !== undefined &&
    proposed.description.trim() !== current.description.trim()
  ) {
    diffs.push({
      field: "description",
      label: "Description",
      before: summarizeText(current.description),
      after: summarizeText(proposed.description),
    });
  }
  return diffs;
}

function summarizeText(s: string): string {
  const clean = s.trim().replace(/\s+/g, " ");
  if (!clean) return "(empty)";
  return clean.length > 140 ? `${clean.slice(0, 137)}…` : clean;
}

export function buildShapingPrompt(
  mode: "create" | "edit",
  current: TaskShapeFields,
  instruction: string,
  meta?: { taskId?: string; state?: string; failureSummary?: string },
): string {
  const header =
    mode === "edit" && meta?.taskId
      ? `[Shape Task #${meta.taskId}${meta.state ? ` · ${meta.state}` : ""}]`
      : "[Shape New Task]";
  const snapshot = {
    title: current.title || undefined,
    repo: current.repo || undefined,
    base: current.base || undefined,
    priority: current.priority,
    model: current.model || undefined,
    dependsOn: current.dependsOn.length ? current.dependsOn : undefined,
    description: current.description || undefined,
  };
  const lines = [
    header,
    instruction.trim() ||
      (mode === "edit"
        ? "Help refine this task's scope, title, or acceptance criteria."
        : "Help turn this rough idea into a crisp, scoped task."),
  ];
  if (meta?.failureSummary?.trim()) {
    lines.push("", `Recent issue/context: ${meta.failureSummary.trim()}`);
  }
  lines.push(
    "",
    "Current fields:",
    "```json",
    JSON.stringify(snapshot, null, 2),
    "```",
    "",
    "Please help refine this task in place without filing a separate task (unless asked to split into a workstream). When proposing updates, include a ```grain-draft fenced JSON block with the changed fields (`title`, `description`, `repo`, `base`, `priority`, `model`, `dependsOn`).",
  );
  return lines.join("\n");
}
