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

// The grant matrix (grain/task-4401): every capability method this build
// offers, crossed with every surface that has an "Add grant…" for it, and
// for each method every argument variant its schema admits. It is
// generated here from GET /api/capability-methods rather than written out
// by hand, so a method added to the build is in the matrix the day it
// lands: grant-matrix-api.spec.js walks every cell through the API and
// grant-matrix-ui.spec.js through the real form.
//
// Nothing here knows a method by name. A cell's argument sets come from
// the method's args schema (each oneOf/anyOf variant, enums, consts,
// patterns, arrays, nested objects), whether it needs arguments from
// requiresArgs, and which surfaces offer it from its policy verdicts.

// SURFACES are the places a grant is added by hand. kind is the run kind
// the capability policy judges a grant there as (model.CapabilityRun*):
// a CI-only method is refused on every task surface and offered only on
// repo-ci.
export const SURFACES = [
  { id: "new-task", kind: "task", label: "New task form" },
  { id: "task-pane", kind: "task", label: "Task pane" },
  { id: "template", kind: "task", label: "Template editor" },
  { id: "schedule", kind: "task", label: "Schedule editor" },
  { id: "session", kind: "process", label: "Console / director session" },
  { id: "settings-defaults", kind: "task", label: "Settings → Tasks defaults" },
  { id: "repo-defaults", kind: "task", label: "Repo default capabilities" },
  { id: "repo-ci", kind: "ci", label: "Repo CI capabilities" },
  { id: "access-card", kind: "task", label: "Access-request card Grant" },
];

// KNOWN_GAPS are cells already filed, keyed by "<ref> <variant>" or
// "<ref> *" for every variant of the method: the walkthroughs record
// them as known rather than failing on them, and once the fix lands the
// entry is deleted and the cell is held to the same bar as the rest.
// Only the UI's: the API grants every one of them today.
export const KNOWN_GAPS = {
  // A top-level oneOf draws no fields and says the arguments "match
  // none" (grain/task-4400, in review).
  "https://grain.dev/capabilities/repo#write *": "#4400",
};

export function knownGap(ref, variant) {
  return lookup(KNOWN_GAPS, ref, variant) || "";
}

// KNOWN_API_GAPS are the API's own: cells where POST
// /api/capability-grants/check records a grant that the task writes then
// refuse, so a form that trusts the check is told otherwise only on save.
// The refusals themselves are grant semantics and stay; the gap is the
// check not saying so, and the wording.
export const KNOWN_API_GAPS = {
  // A task's repository is its target (repo#target, repo#write {repo,
  // base}), given through the task's repository rather than its
  // capabilities, but the capabilities routes refuse it as "unknown
  // capability" and check records it as valid.
  "https://grain.dev/capabilities/repo#target *":
    "check passes; capability write says only 'unknown capability'",
  "https://grain.dev/capabilities/repo#write oneOf[1]":
    "check passes; capability write says only 'unknown capability'",
  "https://grain.dev/capabilities/repo#write oneOf[1] full":
    "check passes; capability write says only 'unknown capability'",
  // A scope set limited to some repositories: check passes a repo
  // outside them, the write refuses it.
  "https://grain.dev/capabilities/api#ops/triage full":
    "check passes a repo outside the scope set's reach; write refuses",
};

export function knownAPIGap(ref, variant) {
  return lookup(KNOWN_API_GAPS, ref, variant) || "";
}

// EXPECTED_REFUSALS are refusals a method's policy verdict does not
// carry, because they are grain's own rule for one spelling of it rather
// than the policy's: the cell passes when the task surfaces refuse it
// with this message, and fails if they grant it.
export const EXPECTED_REFUSALS = {
  // grain#ci follows the repository's CI Mode and is never attached by
  // hand (grainci.Refusal; grain/task-4361, grain/task-4364).
  "https://grain.dev/capabilities/grain#ci *": /CI Mode/,
  // repo#write {repo} is CI-only repository access; only {refs, force}
  // is a task grant.
  "https://grain.dev/capabilities/repo#write oneOf[0]": /CI capability/,
  "https://grain.dev/capabilities/repo#write oneOf[0] full": /CI capability/,
};

export function expectedRefusal(ref, variant) {
  return lookup(EXPECTED_REFUSALS, ref, variant) || null;
}

function lookup(table, ref, variant) {
  return table[`${ref} ${variant}`] || table[`${ref} *`];
}

// listCapabilityMethods is every method, every page of it, each row in
// full (fields=full: a paged row cuts long enums, which would leave a
// variant out).
export async function listCapabilityMethods(request) {
  const out = [];
  let cursor = "";
  for (let i = 0; i < 100; i++) {
    const q = new URLSearchParams({ limit: "50", fields: "full" });
    if (cursor) q.set("cursor", cursor);
    const res = await request.get(`/api/capability-methods?${q}`);
    if (!res.ok()) {
      throw new Error(
        `GET /api/capability-methods: ${res.status()} ${await res.text()}`,
      );
    }
    const page = await res.json();
    out.push(...page.items);
    if (!page.nextCursor) return out;
    cursor = page.nextCursor;
  }
  throw new Error("GET /api/capability-methods never ran out of pages");
}

// deref resolves a local $ref ("#/$defs/x") against root.
export function deref(root, s, depth = 0) {
  if (!s || typeof s !== "object" || !s.$ref || depth > 8) return s;
  const parts = s.$ref.replace(/^#\//, "").split("/");
  let at = root;
  for (const p of parts) at = at?.[p];
  return deref(root, at, depth + 1);
}

// isSecret mirrors what the form keeps out of with: (capabilityArgs.js
// isSecretLooking): such a field is never offered, so never filled. A
// field choosing from an enum is never one.
const SECRET_NAME =
  /(secret|token|passw(or)?d|passphrase|credential|private[_-]?key|api[_-]?key|access[_-]?key|bearer)/i;

export function isSecret(name, s) {
  if (s?.writeOnly === true || s?.format === "password") return true;
  if (Array.isArray(s?.enum) && s.enum.length > 0) return false;
  if (Array.isArray(s?.items?.enum) && s.items.enum.length > 0) return false;
  return SECRET_NAME.test(name || "");
}

// STRING_CANDIDATES are tried, in order, against a string's pattern and
// length bounds; the first that fits is the value.
const STRING_CANDIDATES = [
  "acme/widgets",
  "matrix-value",
  "main",
  "matrix",
  "abc123",
  "a",
];

function stringValue(s) {
  const fits = (v) =>
    (!s.pattern || new RegExp(s.pattern, "u").test(v)) &&
    (s.minLength === undefined || v.length >= s.minLength) &&
    (s.maxLength === undefined || v.length <= s.maxLength) &&
    !(s.not && "const" in s.not && s.not.const === v);
  if (s.default !== undefined && fits(s.default)) return s.default;
  if (!s.pattern) return fits("main") ? "main" : "a";
  return STRING_CANDIDATES.find(fits) ?? "matrix";
}

// valueFor is a valid value for s. full fills optional properties of
// nested objects too; required-only otherwise.
export function valueFor(root, schema, full = false) {
  const s = deref(root, schema) || {};
  if ("const" in s) return s.const;
  if (Array.isArray(s.enum)) return s.enum[0];
  if (s.oneOf || s.anyOf) return valueFor(root, (s.oneOf || s.anyOf)[0], full);
  const type = Array.isArray(s.type) ? s.type[0] : s.type;
  switch (type) {
    case "boolean":
      return s.default ?? true;
    case "integer":
    case "number":
      return s.default ?? s.minimum ?? 1;
    case "array": {
      const items = deref(root, s.items) || { type: "string" };
      const n = Math.max(s.minItems ?? 1, 1);
      if (Array.isArray(items.enum))
        return items.enum.slice(0, Math.min(n, items.enum.length));
      if (s.default !== undefined) return s.default;
      const out = [];
      for (let i = 0; i < n; i++) {
        const v = valueFor(root, items, full);
        out.push(typeof v === "string" && i > 0 ? `${v}${i}` : v);
      }
      return out;
    }
    case "object":
      return objectValue(root, s, full);
    case "string":
    default:
      if (type === undefined && s.properties) return objectValue(root, s, full);
      return stringValue(s);
  }
}

function objectValue(root, s, full) {
  const out = {};
  const required = new Set(s.required || []);
  for (const [name, sub] of Object.entries(s.properties || {})) {
    const d = deref(root, sub);
    if (isSecret(name, d)) continue;
    if (!required.has(name) && !full) continue;
    out[name] = valueFor(root, d, full);
  }
  return out;
}

// argVariants is every valid argument set to exercise for one method:
// each top-level oneOf/anyOf branch on its own (its required arguments,
// then every argument), else the required arguments and every argument,
// and for each top-level enum one set per value. A method taking no
// arguments has one variant, granted without with:. Duplicates go.
export function argVariants(method) {
  const root = method.args;
  const out = [];
  const add = (variant, args) => {
    const key = JSON.stringify(args);
    if (!out.some((v) => JSON.stringify(v.args) === key))
      out.push({ variant, args });
  };
  if (!root || !hasProperties(root)) {
    add("no-args", null);
    return out;
  }
  const branches = root.oneOf || root.anyOf;
  if (branches) {
    const kw = root.oneOf ? "oneOf" : "anyOf";
    branches.forEach((b, i) => {
      const branch = deref(root, b);
      add(`${kw}[${i}]`, objectValue(root, branch, false));
      add(`${kw}[${i}] full`, objectValue(root, branch, true));
    });
    return out;
  }
  const s = deref(root, root);
  const required = objectValue(root, s, false);
  add(Object.keys(required).length ? "required" : "empty", required);
  add("full", objectValue(root, s, true));
  for (const [name, sub] of Object.entries(s.properties || {})) {
    const d = deref(root, sub);
    if (isSecret(name, d)) continue;
    const values = Array.isArray(d.enum)
      ? d.enum
      : Array.isArray(deref(root, d.items)?.enum) && d.minItems === undefined
        ? deref(root, d.items).enum.map((v) => [v])
        : [];
    for (const v of values)
      add(`${name}=${JSON.stringify(v)}`, { ...required, [name]: v });
  }
  return out;
}

function hasProperties(s) {
  if (s.oneOf || s.anyOf) return true;
  return Object.keys(s.properties || {}).length > 0;
}

// invalidArgs is an argument set the schema refuses, and the field the
// refusal should name ("" for the arguments as a whole): a bad value for
// the first field that can have one, else an argument the schema does not
// declare. null for a method that takes no arguments and admits extra
// ones.
export function invalidArgs(method) {
  const root = method.args;
  if (!root) return null;
  const branches = root.oneOf || root.anyOf;
  if (branches) return { args: {}, field: "" };
  const s = deref(root, root);
  const valid = objectValue(root, s, false);
  for (const [name, sub] of Object.entries(s.properties || {})) {
    const d = deref(root, sub);
    if (isSecret(name, d)) continue;
    if (Array.isArray(d.enum))
      return { args: { ...valid, [name]: "not-an-option" }, field: name };
    if (d.pattern)
      return { args: { ...valid, [name]: "Not valid!" }, field: name };
    if (d.type === "boolean")
      return { args: { ...valid, [name]: "yes" }, field: name };
    if (d.type === "integer" && d.maximum !== undefined)
      return { args: { ...valid, [name]: d.maximum + 1 }, field: name };
  }
  if (s.additionalProperties === false)
    return { args: { ...valid, "matrix-unknown": 1 }, field: "matrix-unknown" };
  return null;
}

// policyFor is how the capability policy treats method on a surface
// (model.CapabilityPolicyVerdict): "ci_only" (a CI capability, refused on
// every task and session surface), "denied", or "" where it is admitted
// -- may_be_refused depends on the repository, and a cell names none.
export function policyFor(method, surface) {
  const verdict = method.policy?.[surface.kind] || "";
  return verdict === "ci_only" || verdict === "denied" ? verdict : "";
}

// grantMatrix is every cell: method × surface × argument variant, each
// saying whether the surface should grant it (expect "grant") or refuse
// it on policy (expect "refused").
export function grantMatrix(methods, surfaces = SURFACES) {
  const cells = [];
  for (const method of methods) {
    const variants = argVariants(method);
    for (const surface of surfaces) {
      const policy = policyFor(method, surface);
      for (const { variant, args } of variants) {
        cells.push({
          ref: method.ref,
          surface: surface.id,
          variant,
          args,
          requiresArgs: method.requiresArgs,
          policy,
          expect: policy ? "refused" : "grant",
          knownGap: knownGap(method.ref, variant),
        });
      }
    }
  }
  return cells;
}

// shapeKey is what decides how the form draws a method's arguments and
// how the policy treats it: two methods with the same key are the same
// cell to a form, which is how the UI walkthrough covers every method on
// the task pane and one of each shape on every other surface.
export function shapeKey(method) {
  return JSON.stringify([method.args || null, method.policy || null]);
}

// grantString is the grant as a task records it before normalization:
// the bare reference, or <ref>=<json>.
export function grantString(ref, args) {
  return args ? `${ref}=${JSON.stringify(args)}` : ref;
}
