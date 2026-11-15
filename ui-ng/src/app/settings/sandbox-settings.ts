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

// What Settings → Sandboxes checks before anything is sent, so a refusal
// names the box by its label rather than coming back from the server
// under the JSON key (pkg/ui/settings.go applySettings, model.
// OvercommitSettings.Validate). The server checks every one of these too.

/** The auto overcommit controller's bounds (model.OvercommitSettings). */
export interface OvercommitSettings {
  mode: string;
  cpuMin: number;
  cpuMax: number;
  memoryMin: number;
  memoryMax: number;
  cpuPressureTarget: number;
  memoryPressureTarget: number;
}

export type OvercommitBound = Exclude<keyof OvercommitSettings, "mode">;

// OVERCOMMIT_DEFAULTS are grain's own (model.DefaultOvercommit*), what a
// cleared bound box goes back to.
export const OVERCOMMIT_DEFAULTS: OvercommitSettings = {
  mode: "fixed",
  cpuMin: 1,
  cpuMax: 6,
  memoryMin: 1,
  memoryMax: 2,
  cpuPressureTarget: 25,
  memoryPressureTarget: 5,
};

export function resolveOvercommit(raw: unknown): OvercommitSettings {
  const out = { ...OVERCOMMIT_DEFAULTS };
  if (typeof raw === "string" && raw) out.mode = raw;
  if (raw && typeof raw === "object") {
    const r = raw as Record<string, unknown>;
    if (typeof r["mode"] === "string" && r["mode"]) out.mode = r["mode"];
    for (const k of Object.keys(OVERCOMMIT_DEFAULTS) as OvercommitBound[]) {
      if ((k as string) === "mode") continue;
      const v = Number(r[k]);
      // 0 is the server's "grain's default" for a bound.
      if (r[k] !== undefined && r[k] !== null && Number.isFinite(v) && v > 0) {
        out[k] = v;
      }
    }
  }
  return out;
}

export interface NumberBox {
  key: string;
  label: string;
  min: number;
  max?: number;
  integer?: boolean;
}

// readNumberBox reads a number box's text: null for blank (the caller
// decides what blank means), else the value or a refusal naming the box.
export function readNumberBox(
  raw: string,
  box: NumberBox,
  badInput = false,
): { value: number | null; error?: string } {
  const t = String(raw ?? "").trim();
  // A half-typed "1e" reads as "" from a number box; only its validity
  // says it is not blank.
  if (t === "" && !badInput) return { value: null };
  const v = badInput ? Number.NaN : Number(t);
  const range =
    box.max === undefined
      ? `of at least ${box.min}`
      : `from ${box.min} to ${box.max}`;
  const kind = box.integer ? "a whole number" : "a number";
  if (
    !Number.isFinite(v) ||
    (box.integer && !Number.isInteger(v)) ||
    v < box.min ||
    (box.max !== undefined && v > box.max)
  ) {
    return { value: null, error: `${box.label} must be ${kind} ${range}.` };
  }
  return { value: v };
}

export interface BoundField extends NumberBox {
  field: OvercommitBound;
  step: number;
  fallback: number;
  hint: string;
}

const bound = (
  field: OvercommitBound,
  label: string,
  min: number,
  step: number,
  hint: string,
  max?: number,
): BoundField => ({
  field,
  key: overcommitBoundKey(field),
  label,
  min,
  max,
  step,
  fallback: OVERCOMMIT_DEFAULTS[field],
  hint,
});

export function overcommitBoundKey(field: OvercommitBound): string {
  return `sandboxOvercommit${field[0]!.toUpperCase()}${field.slice(1)}`;
}

export const OVERCOMMIT_BOUND_FIELDS: readonly BoundField[] = [
  bound(
    "cpuMin",
    "Auto CPU minimum",
    0.25,
    0.25,
    "Lowest CPU ratio auto mode sets (default 1.0x).",
  ),
  bound(
    "cpuMax",
    "Auto CPU maximum",
    0.25,
    0.25,
    "Highest CPU ratio auto mode sets (default 6.0x).",
  ),
  bound(
    "memoryMin",
    "Auto memory minimum",
    0.25,
    0.05,
    "Lowest memory ratio auto mode sets (default 1.0x).",
  ),
  bound(
    "memoryMax",
    "Auto memory maximum",
    0.25,
    0.05,
    "Highest memory ratio auto mode sets (default 2.0x).",
  ),
  bound(
    "cpuPressureTarget",
    "CPU pressure target (%)",
    0.5,
    0.5,
    "CPU pressure auto mode backs off above (default 25%).",
    100,
  ),
  bound(
    "memoryPressureTarget",
    "Memory pressure target (%)",
    0.5,
    0.5,
    "Memory pressure auto mode backs off above (default 5%).",
    100,
  ),
];

// overcommitRangeError is the refusal of a minimum above its maximum,
// keyed by the pair it is about ("Cpu", "Memory"), or null.
export function overcommitRangeError(
  o: OvercommitSettings,
  pair: "cpu" | "memory",
): string | null {
  const min = pair === "cpu" ? o.cpuMin : o.memoryMin;
  const max = pair === "cpu" ? o.cpuMax : o.memoryMax;
  if (min <= max) return null;
  const what = pair === "cpu" ? "CPU" : "memory";
  return `Auto ${what} minimum (${min}) is above Auto ${what} maximum (${max}).`;
}

export type K8sKind =
  | "label"
  | "subdomain"
  | "image"
  | "path"
  | "address"
  | "selector"
  | "priority"
  | "free";

const LABEL_NAME = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/;
const SUBDOMAIN_NAME =
  /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/;
const ADDRESS = /^[A-Za-z0-9]([A-Za-z0-9.:-]*[A-Za-z0-9])?$/;

// k8sFieldError is the refusal of one Kubernetes box's text, naming the
// box, or null for a value the server would take (blank always is).
export function k8sFieldError(
  kind: K8sKind,
  label: string,
  raw: string,
): string | null {
  const v = String(raw ?? "").trim();
  if (v === "") return null;
  switch (kind) {
    case "label":
      return LABEL_NAME.test(v)
        ? null
        : `${label} "${v}" is not a Kubernetes name: at most 63 lowercase letters, digits and '-', starting and ending with a letter or digit.`;
    case "subdomain":
    case "priority":
      return v.length <= 253 && SUBDOMAIN_NAME.test(v)
        ? null
        : `${label} "${v}" is not a Kubernetes name: lowercase letters, digits, '-' and '.', starting and ending with a letter or digit.`;
    case "image":
      return /\s/.test(v) || v.includes("://")
        ? `${label} must be one OCI image reference, such as registry.example/team/sandbox:v1, with no scheme or spaces.`
        : null;
    case "path":
      return v.startsWith("/")
        ? null
        : `${label} "${v}" is not an absolute path, such as /home/agent.`;
    case "address":
      return ADDRESS.test(v)
        ? null
        : `${label} "${v}" is not a host name or IP address (no scheme, path or spaces).`;
    case "selector": {
      for (const entry of parseNodeSelector(v)) {
        const eq = entry.indexOf("=");
        if (eq <= 0 || entry.slice(0, eq).trim() === "") {
          return `${label} entry "${entry}" must be key=value.`;
        }
      }
      return null;
    }
    default:
      return null;
  }
}

export function parseNodeSelector(raw: string): string[] {
  return String(raw ?? "")
    .split(",")
    .map((e) => e.trim())
    .filter(Boolean);
}
