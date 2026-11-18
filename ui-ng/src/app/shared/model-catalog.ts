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

// The vocabulary every Model picker offers: the models each of the
// deployment's connections advertises (model.AgentBackend.SupportedModels),
// grouped by connection. Kept free of Angular so any page -- Settings,
// New Task, task edit, team settings -- can build the same groups.

/** The fields of a stored connection (`agentBackends[]`) a picker reads. */
export interface ModelBackendLike {
  id: string;
  name?: string;
  framework?: string;
  models?: string[] | null;
  excludedModels?: string[] | null;
  disabled?: boolean;
  discovered?: {
    models?: string[] | null;
    refreshedAt?: string;
    error?: string;
  } | null;
}

/** One connection's models, in the order its provider listed them. */
export interface ModelGroup {
  id: string;
  label: string;
  framework: string;
  disabled: boolean;
  /** Pinned: the operator listed these; else they were discovered. */
  pinned: boolean;
  models: string[];
  refreshedAt?: string;
  error?: string;
}

export type ModelAvailability = "enabled" | "disabled-only" | "unadvertised";

function dedupe(list: readonly unknown[] | null | undefined): string[] {
  const out: string[] = [];
  for (const raw of list ?? []) {
    const m = String(raw ?? "").trim();
    if (m && !out.includes(m)) out.push(m);
  }
  return out;
}

function sameModel(a: string, b: string): boolean {
  return a.trim().toLowerCase() === b.trim().toLowerCase();
}

/**
 * advertisedModels mirrors model.AgentBackend.SupportedModels: the
 * operator's pinned list when there is one, else what the provider last
 * reported minus the connection's exclusions. Never a guessed name.
 */
export function advertisedModels(b: ModelBackendLike): string[] {
  const pinned = dedupe(b.models);
  if (pinned.length > 0) return pinned;
  const excluded = dedupe(b.excludedModels);
  return dedupe(b.discovered?.models).filter(
    (m) => !excluded.some((x) => sameModel(x, m)),
  );
}

/** modelGroups is one group per connection, in rotation order. */
export function modelGroups(
  backends: readonly ModelBackendLike[] | null | undefined,
): ModelGroup[] {
  return (backends ?? [])
    .filter((b) => b && typeof b.id === "string" && b.id)
    .map((b) => {
      const name = (b.name ?? "").trim();
      const framework = (b.framework ?? "").trim();
      const pinned = dedupe(b.models).length > 0;
      return {
        id: b.id,
        label: name && name !== b.id ? `${name} (${b.id})` : b.id,
        framework,
        disabled: Boolean(b.disabled),
        pinned,
        models: advertisedModels(b),
        refreshedAt: pinned ? undefined : b.discovered?.refreshedAt,
        error: pinned ? undefined : b.discovered?.error || undefined,
      };
    });
}

/**
 * modelAvailability says whether some enabled connection advertises m,
 * only disabled ones do, or none does (case-insensitive, as dispatch
 * matches models).
 */
export function modelAvailability(
  m: string,
  groups: readonly ModelGroup[],
): ModelAvailability {
  let disabledOnly = false;
  for (const g of groups) {
    if (!g.models.some((x) => sameModel(x, m))) continue;
    if (!g.disabled) return "enabled";
    disabledOnly = true;
  }
  return disabledOnly ? "disabled-only" : "unadvertised";
}

/** The label that says why a model is flagged, or "" when it is not. */
export function availabilityNote(a: ModelAvailability): string {
  if (a === "unadvertised") return "not advertised by any connection";
  if (a === "disabled-only") return "disabled connections only";
  return "";
}

/**
 * parseDefaultModels mirrors model.ParseDefaultModels: the ordered
 * default-model list (default first, then fallbacks) from a
 * comma-separated string or a list, dropping blanks and case-insensitive
 * duplicates while keeping the first-seen spelling.
 */
export function parseDefaultModels(source: unknown): string[] {
  const parts: string[] = [];
  if (Array.isArray(source)) {
    for (const p of source) parts.push(...String(p ?? "").split(","));
  } else if (typeof source === "string") {
    parts.push(...source.split(","));
  }
  const out: string[] = [];
  for (const p of parts) {
    const m = p.trim();
    if (m && !out.some((have) => sameModel(have, m))) out.push(m);
  }
  return out;
}

/** containsModel is a case-insensitive includes. */
export function containsModel(list: readonly string[], m: string): boolean {
  return list.some((x) => sameModel(x, m));
}

function normalizeFramework(framework: string): string {
  const f = framework.trim().toLowerCase();
  return f === "gemini" ? "antigravity" : f;
}

/**
 * inferredFramework mirrors model.ModelFramework's prefix fallback
 * (pkg/model/model_selection.go): when no connection advertises m (or its
 * framework is unset), claude* -> claude, gpt* / codex* / o3* / o4* -> codex,
 * and gemini* -> antigravity.
 */
function inferredFramework(m: string): string {
  const id = m.trim().toLowerCase();
  if (id.startsWith("claude")) return "claude";
  if (
    id.startsWith("gpt") ||
    id.startsWith("codex") ||
    id.startsWith("o3") ||
    id.startsWith("o4")
  ) {
    return "codex";
  }
  if (id.startsWith("gemini")) return "antigravity";
  return "";
}

/**
 * modelFramework is the framework of the first enabled connection
 * advertising m, else the first disabled one, else the framework implied by
 * m's prefix (model.ModelFramework) -- or "" when none applies.
 */
export function modelFramework(
  m: string,
  groups: readonly ModelGroup[],
): string {
  const match =
    groups.find(
      (g) => !g.disabled && g.framework.trim() && containsModel(g.models, m),
    ) ?? groups.find((g) => g.framework.trim() && containsModel(g.models, m));
  return match ? normalizeFramework(match.framework) : inferredFramework(m);
}

// The effort suffixes Antigravity's catalog spells into a model ID
// (gemini-3.1-pro-high, opus-5.5-xhigh), as pkg/agent/antigravity.Efforts
// and splitEffort read them (docs/agy-surface.md).
const NAMED_EFFORTS = ["low", "medium", "high", "xhigh", "max"];

/**
 * namedEffort is the reasoning effort an Antigravity model ID carries in
 * its own name ("high" for opus-5.5-high), or "" for one that carries
 * none. Codex and Claude IDs never do: gpt-5.1-codex-max is a model, not
 * an effort.
 */
export function namedEffort(m: string, framework: string): string {
  const f = normalizeFramework(framework) || inferredFramework(m);
  if (f === "codex" || f === "claude") return "";
  const id = m.trim().toLowerCase();
  return NAMED_EFFORTS.find((e) => id.endsWith(`-${e}`)) ?? "";
}

/**
 * noEffortReason says why model m takes no separate reasoning effort, or
 * "" when it does. It mirrors the runners: Claude has no effort flag,
 * agy refuses --effort with "auto" (it picks the model, and with it the
 * effort) and with a name that already spells one
 * (antigravity.modelTakesNoEffort), so a setting for any of those is
 * never applied.
 */
export function noEffortReason(m: string, framework: string): string {
  const id = m.trim();
  if (!id) return "";
  const f = normalizeFramework(framework) || inferredFramework(id);
  if (f === "claude") {
    return "Claude takes no reasoning effort.";
  }
  if (id.toLowerCase() === "auto") {
    return "auto picks the model, and its effort, itself.";
  }
  const named = namedEffort(id, f);
  if (named) return `Effort is part of the model name: ${named}.`;
  return "";
}
