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

// JSON Schema helpers for capability server method arguments and server
// configuration forms (ported from classic ui/src/capabilityArgs.js).
// Supports the restricted keyword set enforced by pkg/capability/protocol/schema.go.

export interface JsonSchema {
  $ref?: string;
  $defs?: Record<string, JsonSchema>;
  definitions?: Record<string, JsonSchema>;
  type?: string;
  title?: string;
  description?: string;
  default?: unknown;
  const?: unknown;
  enum?: unknown[];
  format?: string;
  deprecated?: boolean;
  readOnly?: boolean;
  writeOnly?: boolean;
  pattern?: string;
  minLength?: number;
  maxLength?: number;
  minimum?: number;
  maximum?: number;
  exclusiveMinimum?: number;
  exclusiveMaximum?: number;
  minItems?: number;
  maxItems?: number;
  uniqueItems?: boolean;
  items?: JsonSchema | JsonSchema[];
  properties?: Record<string, JsonSchema>;
  required?: string[];
  additionalProperties?: boolean | JsonSchema;
  oneOf?: JsonSchema[];
  anyOf?: JsonSchema[];
  examples?: unknown[];
  "x-grain-person-granted"?: unknown;
  "x-grain-enum-labels"?: Record<string, string>;
  [key: string]: unknown;
}

export type FieldKind =
  "choice" | "boolean" | "string" | "number" | "array" | "object" | "variant";

export interface ArgumentRow {
  name: string;
  type: string;
  required: boolean;
  default: string;
  allowed: string;
  description: string;
  secret: boolean;
}

export interface ParsedGrant {
  ref: string;
  with: Record<string, unknown> | null;
  raw?: string;
}

export function isCapabilityRef(s: unknown): boolean {
  return typeof s === "string" && s.trim().startsWith("https://");
}

export function parseGrant(s: unknown): ParsedGrant {
  const text = String(s ?? "").trim();
  const eq = text.indexOf("=");
  if (eq < 0) return { ref: text, with: null };
  const ref = text.slice(0, eq);
  try {
    const parsed = JSON.parse(text.slice(eq + 1)) as unknown;
    return {
      ref,
      with:
        parsed && typeof parsed === "object" && !Array.isArray(parsed)
          ? (parsed as Record<string, unknown>)
          : null,
    };
  } catch {
    return { ref, with: null, raw: text.slice(eq + 1) };
  }
}

export function splitRef(ref: string): { capability: string; method: string } {
  const i = ref.indexOf("#");
  if (i < 0) return { capability: ref, method: "" };
  return { capability: ref.slice(0, i), method: ref.slice(i + 1) };
}

const SECRET_NAME =
  /(secret|token|passw(or)?d|passphrase|credential|private[_-]?key|api[_-]?key|access[_-]?key|bearer)/i;

export function isSecretField(schema: JsonSchema | null | undefined): boolean {
  return Boolean(
    schema && (schema.writeOnly === true || schema.format === "password"),
  );
}

export function isSecretLooking(
  name: string,
  schema: JsonSchema | null | undefined,
): boolean {
  if (isSecretField(schema)) return true;
  if (
    schema?.type === "boolean" ||
    schema?.type === "integer" ||
    schema?.type === "number"
  ) {
    return false;
  }
  if (Array.isArray(schema?.enum) && schema.enum.length > 0) return false;
  const items =
    schema?.items && !Array.isArray(schema.items) ? schema.items : null;
  if (Array.isArray(items?.enum) && items.enum.length > 0) return false;
  return SECRET_NAME.test(name || "");
}

const MAX_DEPTH = 12;

export function deref(
  root: JsonSchema | null | undefined,
  schema: JsonSchema | null | undefined,
  depth = 0,
): JsonSchema | null {
  let s: JsonSchema | null | undefined = schema;
  let d = depth;
  while (s && typeof s === "object" && typeof s.$ref === "string") {
    if (d++ > MAX_DEPTH) return null;
    const m = /^#\/(\$defs|definitions)\/(.+)$/.exec(s.$ref);
    if (!m) return null;
    const bucket = m[1] === "$defs" ? root?.$defs : root?.definitions;
    const target = bucket?.[decodeURIComponent(m[2])];
    if (!target) return null;
    const { $ref: _ignored, ...siblings } = s;
    void _ignored;
    s = { ...target, ...siblings };
  }
  return s ?? null;
}

const ANNOTATIONS = new Set([
  "$schema",
  "$id",
  "$comment",
  "$anchor",
  "title",
  "description",
  "default",
  "examples",
  "format",
  "deprecated",
  "readOnly",
  "writeOnly",
  "x-grain-person-granted",
  "x-grain-enum-labels",
]);

const KIND_KEYWORDS: Record<FieldKind, readonly string[]> = {
  choice: [
    "enum",
    "const",
    "type",
    "pattern",
    "minLength",
    "maxLength",
    "minimum",
    "maximum",
    "exclusiveMinimum",
    "exclusiveMaximum",
  ],
  boolean: ["type"],
  string: ["type", "pattern", "minLength", "maxLength"],
  number: [
    "type",
    "minimum",
    "maximum",
    "exclusiveMinimum",
    "exclusiveMaximum",
  ],
  array: ["type", "items", "minItems", "maxItems", "uniqueItems"],
  object: [
    "type",
    "properties",
    "required",
    "additionalProperties",
    "$defs",
    "definitions",
  ],
  variant: ["oneOf", "anyOf"],
};

export function fieldKind(
  schema: JsonSchema | null | undefined,
): FieldKind | null {
  if (!schema || typeof schema !== "object") return null;
  if ("enum" in schema || "const" in schema) return "choice";
  if ("oneOf" in schema || "anyOf" in schema) return "variant";
  switch (schema.type) {
    case "boolean":
      return "boolean";
    case "string":
      return "string";
    case "integer":
    case "number":
      return "number";
    case "array":
      return "array";
    case "object":
      return "object";
    default:
      if (schema.properties) return "object";
      return null;
  }
}

export function isStringMap(
  root: JsonSchema | null | undefined,
  schema: JsonSchema | null | undefined,
): boolean {
  const s = deref(root, schema);
  if (!s || fieldKind(s) !== "object") return false;
  if (Object.keys(s.properties || {}).length > 0) return false;
  if (!s.additionalProperties || typeof s.additionalProperties !== "object") {
    return false;
  }
  const ap = deref(root, s.additionalProperties);
  if (!ap || typeof ap !== "object" || ap.type !== "string") return false;
  return Object.keys(ap).every(
    (k) => ANNOTATIONS.has(k) || KIND_KEYWORDS.string.includes(k),
  );
}

export function isFixedList(
  root: JsonSchema | null | undefined,
  schema: JsonSchema | null | undefined,
): boolean {
  const s = deref(root, schema);
  if (!s || fieldKind(s) !== "array" || !s.uniqueItems) return false;
  if (!s.items || Array.isArray(s.items)) return false;
  const items = deref(root, s.items);
  const options = Array.isArray(items?.enum) ? items.enum : null;
  if (!options || options.length === 0) return false;
  return (
    s.minItems === options.length &&
    (s.maxItems === undefined || s.maxItems === options.length)
  );
}

export function isMultiline(schema: JsonSchema | null | undefined): boolean {
  return schema?.format === "textarea" || schema?.format === "code";
}

export function unsupportedReason(
  root: JsonSchema | null | undefined,
  schema: JsonSchema | null | undefined = root,
  path = "arguments",
  depth = 0,
): string {
  if (depth > MAX_DEPTH) return `${path}: nested too deeply`;
  const s = deref(root, schema, depth);
  if (!s) return `${path}: unresolvable $ref`;
  const kind = fieldKind(s);
  if (!kind) {
    const known = new Set(Object.values(KIND_KEYWORDS).flat());
    const odd = Object.keys(s).find(
      (k) => !ANNOTATIONS.has(k) && !known.has(k),
    );
    return odd ? `${path}: "${odd}"` : `${path}: no type the form renders`;
  }
  const allowed = new Set(KIND_KEYWORDS[kind]);
  for (const key of Object.keys(s)) {
    if (ANNOTATIONS.has(key) || allowed.has(key)) continue;
    return `${path}: "${key}"`;
  }
  if (kind === "object") {
    if (
      s.additionalProperties !== undefined &&
      s.additionalProperties !== false &&
      s.additionalProperties !== true &&
      !isStringMap(root, s)
    ) {
      return `${path}: a schema-valued additionalProperties`;
    }
    for (const [name, sub] of Object.entries(s.properties || {})) {
      const why = unsupportedReason(root, sub, `${path}.${name}`, depth + 1);
      if (why) return why;
    }
  }
  if (kind === "array") {
    if (!s.items || Array.isArray(s.items)) {
      return `${path}: an array without one items schema`;
    }
    const why = unsupportedReason(root, s.items, `${path}[]`, depth + 1);
    if (why) return why;
  }
  if (kind === "variant") {
    const subs = s.oneOf || s.anyOf || [];
    for (let i = 0; i < subs.length; i++) {
      const why = unsupportedReason(root, subs[i], `${path}/${i}`, depth + 1);
      if (why) return why;
    }
  }
  return "";
}

export function isHiddenProperty(
  name: string,
  schema: JsonSchema | null | undefined,
): boolean {
  if (schema?.deprecated === true) return true;
  if (
    name === "grainDeploymentId" ||
    name === "grainInstanceId" ||
    name.startsWith("_grain_")
  ) {
    return true;
  }
  return false;
}

export function hasArguments(schema: JsonSchema | null | undefined): boolean {
  if (!schema) return false;
  const s = deref(schema, schema);
  if (!s) return true;
  if (fieldKind(s) !== "object") return true;
  return Object.entries(s.properties || {}).some(
    ([name, sub]) => !isHiddenProperty(name, deref(schema, sub)),
  );
}

function clone<T>(v: T): T {
  return v === undefined ? v : (JSON.parse(JSON.stringify(v)) as T);
}

export function initialValue(
  root: JsonSchema | null | undefined,
  schema: JsonSchema | null | undefined,
  nested = false,
  required = false,
): unknown {
  const s = deref(root, schema);
  if (!s) return undefined;
  if ("default" in s) return clone(s.default);
  if ("const" in s) return clone(s.const);
  const kind = fieldKind(s);
  if (kind === "object") {
    const out: Record<string, unknown> = {};
    const req = new Set(s.required || []);
    for (const [name, sub] of Object.entries(s.properties || {})) {
      const propSchema = deref(root, sub);
      if (isHiddenProperty(name, propSchema)) continue;
      if (isSecretLooking(name, propSchema)) continue;
      const v = initialValue(
        root,
        sub,
        true,
        (!nested || required) && req.has(name),
      );
      if (v !== undefined) out[name] = v;
    }
    if (nested && !required && Object.keys(out).length === 0) return undefined;
    return out;
  }
  if (kind === "array") {
    if (!required) return undefined;
    if (isFixedList(root, s) && s.items && !Array.isArray(s.items)) {
      const items = deref(root, s.items);
      return clone(items?.enum ?? []);
    }
    return [];
  }
  if (kind === "variant") {
    const subs = s.oneOf || s.anyOf || [];
    return nested ? undefined : initialValue(root, subs[0], false, required);
  }
  return undefined;
}

function typeOfValue(v: unknown): string {
  if (v === null) return "null";
  if (Array.isArray(v)) return "array";
  if (typeof v === "number") return Number.isInteger(v) ? "integer" : "number";
  return typeof v;
}

function typeMatches(t: string, v: unknown): boolean {
  const actual = typeOfValue(v);
  if (t === "number") return actual === "number" || actual === "integer";
  return t === actual;
}

function equalValues(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

export function variantTitle(
  schema: JsonSchema | null | undefined,
  i: number,
): string {
  return (
    schema?.title ||
    schema?.description ||
    `${schema?.type || "option"} ${i + 1}`
  );
}

export function validateValue(
  root: JsonSchema | null | undefined,
  schema: JsonSchema | null | undefined,
  value: unknown,
  path = "",
  errors: Record<string, string> = {},
  depth = 0,
): Record<string, string> {
  const s = deref(root, schema, depth);
  if (!s || depth > MAX_DEPTH) return errors;
  const fail = (msg: string): void => {
    if (!(path in errors)) errors[path] = msg;
  };
  if (s.oneOf || s.anyOf) {
    const subs = s.oneOf || s.anyOf || [];
    const results = subs.map((sub, i) => ({
      name: variantTitle(deref(root, sub), i),
      errors: validateValue(root, sub, value, "", {}, depth + 1),
    }));
    const matched = results.filter((r) => Object.keys(r.errors).length === 0);
    if (matched.length === 0) {
      const why = results
        .map(
          (r) =>
            `${r.name}: ${Object.entries(r.errors)
              .map(([k, msg]) => (k ? `${k} ${msg}` : msg))
              .join(", ")}`,
        )
        .join("; ");
      fail(`matches none of the options -- ${why}`);
    } else if (s.oneOf && matched.length > 1) {
      fail(
        `matches more than one option: ${matched.map((r) => r.name).join(", ")}`,
      );
    }
    return errors;
  }
  if (s.type && !typeMatches(s.type, value)) {
    fail(`must be ${s.type === "integer" ? "a whole number" : `a ${s.type}`}`);
    return errors;
  }
  if ("const" in s && !equalValues(s.const, value)) {
    fail(`must be ${JSON.stringify(s.const)}`);
  }
  if (s.enum && !s.enum.some((e) => equalValues(e, value))) {
    fail(`must be one of ${s.enum.map((e) => JSON.stringify(e)).join(", ")}`);
  }
  if (typeof value === "string") {
    const len = [...value].length;
    if (s.minLength !== undefined && len < s.minLength) {
      fail(`must be at least ${s.minLength} characters`);
    }
    if (s.maxLength !== undefined && len > s.maxLength) {
      fail(`must be at most ${s.maxLength} characters`);
    }
    if (s.pattern !== undefined) {
      let re: RegExp | null = null;
      try {
        re = new RegExp(s.pattern, "u");
      } catch {
        // Leave patterns JS cannot parse to the server.
      }
      if (re && !re.test(value)) fail(`must match ${s.pattern}`);
    }
  }
  if (typeof value === "number") {
    if (s.minimum !== undefined && value < s.minimum) {
      fail(`must be at least ${s.minimum}`);
    }
    if (s.maximum !== undefined && value > s.maximum) {
      fail(`must be at most ${s.maximum}`);
    }
    if (s.exclusiveMinimum !== undefined && value <= s.exclusiveMinimum) {
      fail(`must be greater than ${s.exclusiveMinimum}`);
    }
    if (s.exclusiveMaximum !== undefined && value >= s.exclusiveMaximum) {
      fail(`must be less than ${s.exclusiveMaximum}`);
    }
  }
  if (Array.isArray(value)) {
    if (s.minItems !== undefined && value.length < s.minItems) {
      fail(`needs at least ${s.minItems} item${s.minItems === 1 ? "" : "s"}`);
    }
    if (s.maxItems !== undefined && value.length > s.maxItems) {
      fail(`takes at most ${s.maxItems} item${s.maxItems === 1 ? "" : "s"}`);
    }
    if (s.uniqueItems) {
      const seen = new Set<string>();
      value.forEach((item, i) => {
        const k = JSON.stringify(item);
        if (seen.has(k)) errors[`${path}[${i}]`] = "repeats an earlier item";
        seen.add(k);
      });
    }
    if (s.items && !Array.isArray(s.items)) {
      const itemSchema = s.items;
      value.forEach((item, i) =>
        validateValue(
          root,
          itemSchema,
          item,
          `${path}[${i}]`,
          errors,
          depth + 1,
        ),
      );
    }
  }
  if (value && typeof value === "object" && !Array.isArray(value)) {
    const rec = value as Record<string, unknown>;
    const props = s.properties || {};
    const join = (name: string) => (path ? `${path}.${name}` : name);
    for (const name of s.required || []) {
      if (rec[name] === undefined) errors[join(name)] = "is required";
    }
    for (const [name, v] of Object.entries(rec)) {
      if (props[name]) {
        validateValue(root, props[name], v, join(name), errors, depth + 1);
      } else if (s.additionalProperties === false) {
        errors[join(name)] = "is not an accepted argument";
      } else if (
        s.additionalProperties &&
        typeof s.additionalProperties === "object"
      ) {
        validateValue(
          root,
          s.additionalProperties,
          v,
          join(name),
          errors,
          depth + 1,
        );
      }
    }
  }
  return errors;
}

function sameKind(schema: JsonSchema, value: unknown): boolean {
  if ("const" in schema) return equalValues(schema.const, value);
  if (schema.enum) return schema.enum.some((e) => equalValues(e, value));
  switch (fieldKind(schema)) {
    case "array":
      return Array.isArray(value);
    case "object":
      return (
        Boolean(value) && typeof value === "object" && !Array.isArray(value)
      );
    case "number":
      return typeof value === "number";
    default:
      return typeof value === schema.type;
  }
}

export function pickVariant(
  root: JsonSchema | null | undefined,
  options: (JsonSchema | null)[],
  value: unknown,
): number {
  if (value === undefined) return 0;
  const valid = options.findIndex(
    (o) => o && Object.keys(validateValue(root, o, value)).length === 0,
  );
  if (valid >= 0) return valid;
  if (value && typeof value === "object" && !Array.isArray(value)) {
    const keys = Object.keys(value);
    const covers = options.findIndex(
      (o) =>
        o &&
        fieldKind(o) === "object" &&
        keys.length > 0 &&
        keys.every((k) => k in (o.properties || {})),
    );
    if (covers >= 0) return covers;
  }
  const kind = options.findIndex(
    (o) => o && fieldKind(o) && sameKind(o, value),
  );
  return kind < 0 ? 0 : kind;
}

const BUILTIN_CAPABILITY_PREFIX = "https://grain.dev/capabilities/";

export function builtinServerName(capability: string | undefined): string {
  return capability?.startsWith(BUILTIN_CAPABILITY_PREFIX)
    ? capability.slice(BUILTIN_CAPABILITY_PREFIX.length)
    : "";
}

export function methodMenuLabel(ref: string, method: string): string {
  const name = builtinServerName(splitRef(ref).capability);
  return name ? `${name} server › ${method}` : method;
}

function describeType(
  root: JsonSchema | null | undefined,
  s: JsonSchema | null | undefined,
): string {
  if (!s) return "?";
  if ("const" in s) return s.type || typeOfValue(s.const);
  if (s.enum) return s.type || "enum";
  if (s.oneOf || s.anyOf) {
    return (s.oneOf || s.anyOf || [])
      .map((sub) => describeType(root, deref(root, sub)))
      .join(" | ");
  }
  if (s.type === "array" && s.items && !Array.isArray(s.items)) {
    return `${describeType(root, deref(root, s.items))}[]`;
  }
  return s.type || (s.properties ? "object" : "any");
}

function describeAllowed(
  root: JsonSchema | null | undefined,
  s: JsonSchema | null | undefined,
): string {
  if (!s) return "";
  const parts: string[] = [];
  if ("const" in s) parts.push(`= ${JSON.stringify(s.const)}`);
  if (s.enum) parts.push(s.enum.map((e) => JSON.stringify(e)).join(", "));
  if (s.pattern !== undefined) parts.push(`/${s.pattern}/`);
  if (s.minLength !== undefined) parts.push(`length ≥ ${s.minLength}`);
  if (s.maxLength !== undefined) parts.push(`length ≤ ${s.maxLength}`);
  if (s.minimum !== undefined) parts.push(`≥ ${s.minimum}`);
  if (s.exclusiveMinimum !== undefined) parts.push(`> ${s.exclusiveMinimum}`);
  if (s.maximum !== undefined) parts.push(`≤ ${s.maximum}`);
  if (s.exclusiveMaximum !== undefined) parts.push(`< ${s.exclusiveMaximum}`);
  if (s.minItems !== undefined) parts.push(`≥ ${s.minItems} items`);
  if (s.maxItems !== undefined) parts.push(`≤ ${s.maxItems} items`);
  if (s.uniqueItems) parts.push("unique");
  if (s.type === "array" && s.items && !Array.isArray(s.items)) {
    const inner = describeAllowed(root, deref(root, s.items));
    if (inner) parts.push(`each: ${inner}`);
  }
  return parts.join("; ");
}

export function argumentRows(
  schema: JsonSchema | null | undefined,
): ArgumentRow[] {
  if (!schema) return [];
  const rows: ArgumentRow[] = [];
  const walk = (s: JsonSchema | null, prefix: string, depth: number): void => {
    if (!s || depth > MAX_DEPTH) return;
    const required = new Set(s.required || []);
    for (const [name, raw] of Object.entries(s.properties || {})) {
      const p = deref(schema, raw);
      if (isHiddenProperty(name, p)) continue;
      const full = prefix ? `${prefix}.${name}` : name;
      rows.push({
        name: full,
        type: describeType(schema, p),
        required: required.has(name),
        default: p && "default" in p ? JSON.stringify(p.default) : "",
        allowed: describeAllowed(schema, p),
        description: p?.description || "",
        secret: isSecretLooking(name, p),
      });
      if (p && fieldKind(p) === "object") walk(p, full, depth + 1);
    }
  };
  walk(deref(schema, schema), "", 0);
  return rows;
}
