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

// The with: arguments of a capability server method's grant, as the UI
// edits them (docs/capabilities.md, "Granting from the UI"). A method
// declares its arguments as JSON Schema -- mint_<method>'s
// inputSchema.properties.args, which GET /api/capability-methods carries
// as `args` -- restricted to the keywords grain enforces
// (docs/capabilities-spec.md §4.2.1). CapabilityArgsForm renders a form
// from that schema; this file is the part of it that is not React: which
// schemas the form can render at all, the initial value, a live check of
// the same keywords the server checks, and how a grant reads as a chip.
//
// The live check is a convenience, never the authority: every grant is
// normalized by the server (POST /api/capability-grants/check, or the
// task capabilities route) before it is kept, and the server's field
// errors are shown in the same places.

// isCapabilityRef is model.IsCapabilityRef: a capability server
// reference rather than an in-process capability name.
export function isCapabilityRef(s) {
  return typeof s === "string" && s.trim().startsWith("https://");
}

// parseGrant reads a grant string -- <ref> or <ref>=<json> -- into its
// reference and with: arguments. A string whose JSON does not parse keeps
// its text as `raw`.
export function parseGrant(s) {
  const text = String(s || "").trim();
  const eq = text.indexOf("=");
  if (eq < 0) return { ref: text, with: null };
  const ref = text.slice(0, eq);
  try {
    return { ref, with: JSON.parse(text.slice(eq + 1)) };
  } catch {
    return { ref, with: null, raw: text.slice(eq + 1) };
  }
}

// splitRef is a reference's capability_id and #method.
export function splitRef(ref) {
  const i = ref.indexOf("#");
  if (i < 0) return { capability: ref, method: "" };
  return { capability: ref.slice(0, i), method: ref.slice(i + 1) };
}

// encodeGrant is model.EncodeCapabilityGrant: the reference alone with no
// arguments, otherwise reference=<JSON>.
export function encodeGrant(ref, args) {
  if (args == null) return ref;
  return `${ref}=${JSON.stringify(args)}`;
}

// SECRET_NAME is what a secret-looking argument is called. with: values
// are recorded in plain text on the task and shown in its pane, so a
// field like this is never offered (docs/capabilities.md).
const SECRET_NAME =
  /(secret|token|passw(or)?d|passphrase|credential|private[_-]?key|api[_-]?key|access[_-]?key|bearer)/i;

// isSecretLooking reports an argument that looks as though it would carry
// a secret: by its name, or by the schema saying so. A string confined to
// an enum is not one whatever it is called -- its values are the
// schema's own, fixed list (gcp's credential_source: configured or
// ambient) -- unless the schema marks it secret.
export function isSecretLooking(name, schema) {
  if (schema && (schema.writeOnly === true || schema.format === "password"))
    return true;
  if (Array.isArray(schema?.enum) && schema.enum.length > 0) return false;
  if (Array.isArray(schema?.items?.enum) && schema.items.enum.length > 0)
    return false;
  if (SECRET_NAME.test(name || "")) return true;
  return false;
}

const MAX_DEPTH = 12;

// deref resolves a local $ref ("#/$defs/name", "#/definitions/name")
// against root. An unresolvable one returns null.
export function deref(root, schema, depth = 0) {
  let s = schema;
  let d = depth;
  while (s && typeof s === "object" && typeof s.$ref === "string") {
    if (d++ > MAX_DEPTH) return null;
    const m = /^#\/(\$defs|definitions)\/(.+)$/.exec(s.$ref);
    if (!m) return null;
    const target = root?.[m[1]]?.[decodeURIComponent(m[2])];
    if (!target) return null;
    const { $ref: _ref, ...siblings } = s;
    s = { ...target, ...siblings };
  }
  return s;
}

// The keywords each kind of field reads. A schema using anything else the
// validator enforces (allOf, not, a schema-valued additionalProperties, a
// type list) is one the form cannot render faithfully, so the whole grant
// falls back to the JSON editor rather than dropping a field.
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
]);
const KIND_KEYWORDS = {
  choice: ["enum", "const", "type"],
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

// fieldKind is which kind of field schema renders as, or null for one
// the form cannot render.
export function fieldKind(schema) {
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

// isStringMap is an object of free-form names to strings -- no
// properties of its own, additionalProperties a plain string schema --
// which the form edits as name/value rows (host-script's env).
export function isStringMap(root, schema) {
  const s = deref(root, schema);
  if (!s || fieldKind(s) !== "object") return false;
  if (Object.keys(s.properties || {}).length > 0) return false;
  const ap = deref(root, s.additionalProperties);
  if (!ap || typeof ap !== "object" || ap.type !== "string") return false;
  return Object.keys(ap).every(
    (k) => ANNOTATIONS.has(k) || KIND_KEYWORDS.string.includes(k),
  );
}

// isMultiline is a string the schema annotates as text of several lines:
// format "textarea", or "code" for one drawn in a monospace font.
export function isMultiline(schema) {
  return schema?.format === "textarea" || schema?.format === "code";
}

// unsupportedReason says why the form cannot render schema, or "" when it
// can: the first keyword or shape it does not know, with its path.
export function unsupportedReason(
  root,
  schema = root,
  path = "args",
  depth = 0,
) {
  if (depth > MAX_DEPTH) return `${path}: nests too deeply`;
  const s = deref(root, schema, depth);
  if (!s) return `${path}: an unresolvable $ref`;
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
    )
      return `${path}: a schema-valued additionalProperties`;
    for (const [name, sub] of Object.entries(s.properties || {})) {
      const why = unsupportedReason(root, sub, `${path}.${name}`, depth + 1);
      if (why) return why;
    }
  }
  if (kind === "array") {
    if (!s.items || Array.isArray(s.items))
      return `${path}: an array without one items schema`;
    const why = unsupportedReason(root, s.items, `${path}[]`, depth + 1);
    if (why) return why;
  }
  if (kind === "variant") {
    for (const [i, sub] of (s.oneOf || s.anyOf).entries()) {
      const why = unsupportedReason(root, sub, `${path}/${i}`, depth + 1);
      if (why) return why;
    }
  }
  return "";
}

// isHiddenProperty reports a property the UI form and schema table
// omit: a deprecated migration field or a daemon-injected identity
// field.
export function isHiddenProperty(name, schema) {
  if (schema?.deprecated === true) return true;
  if (
    name === "grainDeploymentId" ||
    name === "grainInstanceId" ||
    (typeof name === "string" && name.startsWith("_grain_"))
  )
    return true;
  return false;
}

// hasArguments reports a schema with anything to fill in.
export function hasArguments(schema) {
  if (!schema) return false;
  const s = deref(schema, schema);
  if (!s) return true;
  if (fieldKind(s) !== "object") return true;
  return Object.entries(s.properties || {}).some(
    ([name, sub]) => !isHiddenProperty(name, deref(schema, sub)),
  );
}

function clone(v) {
  return v === undefined ? undefined : JSON.parse(JSON.stringify(v));
}

// initialValue is what a fresh field starts at: its default, a const's
// one value, an object's defaults, a variant's first option's, or
// undefined (left out of with:).
export function initialValue(root, schema, nested = false) {
  const s = deref(root, schema);
  if (!s) return undefined;
  if ("default" in s) return clone(s.default);
  if ("const" in s) return clone(s.const);
  const kind = fieldKind(s);
  if (kind === "object") {
    const out = {};
    for (const [name, sub] of Object.entries(s.properties || {})) {
      const propSchema = deref(root, sub);
      if (isHiddenProperty(name, propSchema)) continue;
      if (isSecretLooking(name, propSchema)) continue;
      const v = initialValue(root, sub, true);
      if (v !== undefined) out[name] = v;
    }
    // A nested object with nothing to start from is left out, so an
    // optional one is not granted as {}; the arguments themselves are
    // always an object.
    if (nested && Object.keys(out).length === 0) return undefined;
    return out;
  }
  if (kind === "array") return s.minItems ? [] : undefined;
  // An argument that is one of several kinds starts unset, for whoever
  // fills it in to pick; picking one (nested false) starts that kind.
  if (kind === "variant")
    return nested ? undefined : initialValue(root, (s.oneOf || s.anyOf)[0]);
  return undefined;
}

function typeOf(v) {
  if (v === null) return "null";
  if (Array.isArray(v)) return "array";
  if (typeof v === "number") return Number.isInteger(v) ? "integer" : "number";
  return typeof v;
}

function typeMatches(t, v) {
  const actual = typeOf(v);
  if (t === "number") return actual === "number" || actual === "integer";
  return t === actual;
}

function equal(a, b) {
  return JSON.stringify(a) === JSON.stringify(b);
}

// validateValue checks value against schema with the keywords
// ValidateArgs enforces (pkg/capability/protocol/schema.go) and returns
// every refusal keyed by its argument path ("project", "scopes[1]",
// "labels.team"; "" for the arguments as a whole) -- the same keys the
// server's field errors use.
export function validateValue(
  root,
  schema,
  value,
  path = "",
  errors = {},
  depth = 0,
) {
  const s = deref(root, schema, depth);
  if (!s || depth > MAX_DEPTH) return errors;
  const fail = (msg) => {
    if (!(path in errors)) errors[path] = msg;
  };
  if (s.oneOf || s.anyOf) {
    const subs = s.oneOf || s.anyOf;
    const matched = subs.filter(
      (sub) =>
        Object.keys(validateValue(root, sub, value, "", {}, depth + 1))
          .length === 0,
    ).length;
    if (s.oneOf && matched !== 1)
      fail(
        matched === 0
          ? "matches none of the options"
          : "matches more than one option",
      );
    if (s.anyOf && matched === 0) fail("matches none of the options");
    return errors;
  }
  if (s.type && !typeMatches(s.type, value)) {
    fail(`must be ${s.type === "integer" ? "a whole number" : `a ${s.type}`}`);
    return errors;
  }
  if ("const" in s && !equal(s.const, value))
    fail(`must be ${JSON.stringify(s.const)}`);
  if (s.enum && !s.enum.some((e) => equal(e, value)))
    fail(`must be one of ${s.enum.map((e) => JSON.stringify(e)).join(", ")}`);
  if (typeof value === "string") {
    const len = [...value].length;
    if (s.minLength !== undefined && len < s.minLength)
      fail(`must be at least ${s.minLength} characters`);
    if (s.maxLength !== undefined && len > s.maxLength)
      fail(`must be at most ${s.maxLength} characters`);
    if (s.pattern !== undefined) {
      let re = null;
      try {
        re = new RegExp(s.pattern, "u");
      } catch {
        // A pattern JavaScript cannot read (Go's RE2 accepts a few it
        // does not) is left to the server.
      }
      if (re && !re.test(value)) fail(`must match ${s.pattern}`);
    }
  }
  if (typeof value === "number") {
    if (s.minimum !== undefined && value < s.minimum)
      fail(`must be at least ${s.minimum}`);
    if (s.maximum !== undefined && value > s.maximum)
      fail(`must be at most ${s.maximum}`);
    if (s.exclusiveMinimum !== undefined && value <= s.exclusiveMinimum)
      fail(`must be greater than ${s.exclusiveMinimum}`);
    if (s.exclusiveMaximum !== undefined && value >= s.exclusiveMaximum)
      fail(`must be less than ${s.exclusiveMaximum}`);
  }
  if (Array.isArray(value)) {
    if (s.minItems !== undefined && value.length < s.minItems)
      fail(`needs at least ${s.minItems} item${s.minItems === 1 ? "" : "s"}`);
    if (s.maxItems !== undefined && value.length > s.maxItems)
      fail(`takes at most ${s.maxItems} item${s.maxItems === 1 ? "" : "s"}`);
    if (s.uniqueItems) {
      const seen = new Set();
      value.forEach((item, i) => {
        const k = JSON.stringify(item);
        if (seen.has(k)) errors[`${path}[${i}]`] = "repeats an earlier item";
        seen.add(k);
      });
    }
    if (s.items)
      value.forEach((item, i) =>
        validateValue(root, s.items, item, `${path}[${i}]`, errors, depth + 1),
      );
  }
  if (value && typeof value === "object" && !Array.isArray(value)) {
    const props = s.properties || {};
    const join = (name) => (path ? `${path}.${name}` : name);
    for (const name of s.required || []) {
      if (value[name] === undefined) errors[join(name)] = "is required";
    }
    for (const [name, v] of Object.entries(value)) {
      if (props[name])
        validateValue(root, props[name], v, join(name), errors, depth + 1);
      else if (s.additionalProperties === false)
        errors[join(name)] = "is not an accepted argument";
      else if (
        s.additionalProperties &&
        typeof s.additionalProperties === "object"
      )
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
  return errors;
}

// formatValue is one argument's value as a chip shows it.
export function formatValue(v) {
  if (Array.isArray(v)) return v.map(formatValue).join(", ");
  if (v && typeof v === "object")
    return Object.entries(v)
      .map(([k, x]) => `${k}=${formatValue(x)}`)
      .join(" ");
  return String(v);
}

// BUILTIN_CAPABILITY_PREFIX is the namespace of grain's built-in
// capability servers' canonical ids (intree.CanonicalIDPrefix).
export const BUILTIN_CAPABILITY_PREFIX = "https://grain.dev/capabilities/";

// builtinServerName is the in-tree server a capability id names, or ""
// for any other server's: "gcp" for https://grain.dev/capabilities/gcp.
export function builtinServerName(capability) {
  return capability?.startsWith(BUILTIN_CAPABILITY_PREFIX)
    ? capability.slice(BUILTIN_CAPABILITY_PREFIX.length)
    : "";
}

// methodMenuLabel is how a server method is offered in a grant picker:
// "gcp server › gcp-key" for a built-in server's, so it reads apart from
// the in-process capability of the same name, otherwise the method.
export function methodMenuLabel(ref, method) {
  const name = builtinServerName(splitRef(ref).capability);
  return name ? `${name} server › ${method}` : method;
}

// grantLabel is a grant as its chip reads: the method, then each
// argument the grant sets to something other than its default -- the
// rest are in the chip's tooltip (grantDetail).
export function grantLabel(method, args, schema) {
  const name = method?.method || splitRef(method?.ref || "").method || "grant";
  const shown = nonDefaultArgs(args, schema);
  if (shown.length === 0) return name;
  return `${name} · ${shown.map(([k, v]) => `${k}: ${formatValue(v)}`).join(", ")}`;
}

// nonDefaultArgs is the [name, value] pairs of args a schema does not
// declare as its default. With no schema every argument counts.
function nonDefaultArgs(args, schema) {
  const props = deref(schema, schema)?.properties || {};
  return Object.entries(args || {}).filter(([k, v]) => {
    const p = deref(schema, props[k]);
    return !(p && "default" in p && equal(p.default, v));
  });
}

// GRANT_ARGS_MAX is how many characters of arguments a grant chip shows
// before it ends them with an ellipsis; the tooltip has the rest.
export const GRANT_ARGS_MAX = 32;

// truncate is text cut to max characters, the last of them an ellipsis.
export function truncate(text, max = GRANT_ARGS_MAX) {
  const s = String(text ?? "");
  return s.length > max ? `${s.slice(0, max - 1)}…` : s;
}

// serverShortName is how a chip names a capability server: the built-in
// server's own name ("workspace"), otherwise the host of its URL, or the
// capability id itself when it is no URL at all.
export function serverShortName(capability) {
  const builtin = builtinServerName(capability);
  if (builtin) return builtin;
  try {
    return new URL(capability).host || capability;
  } catch {
    return capability;
  }
}

// describeGrant is a grant as a chip shows it (GrantChip.jsx): a short
// label, the abbreviated arguments that differ from their defaults, and
// the full grant line by line for its tooltip. grant is a task's stored
// capabilities entry -- an in-process capability's id, an api grant, or
// a server method's <ref> or <ref>=<json> -- method the method listing's
// entry for its ref when there is one (its args schema says which
// arguments are defaults), config /api/config for in-process names.
//
// kind is "capability" (in-process), "method", or "raw" for a method
// grant whose arguments do not parse, which shows as its own text, cut.
export function describeGrant(grant, { method, config } = {}) {
  const text = String(grant ?? "").trim();
  // An audit record names its grant <capability_id>#<method> even when
  // the id is no URL, so a "#" makes it a method grant too.
  if (!isCapabilityRef(text) && !splitRef(text.split("=")[0]).method) {
    const all = (config?.capabilities || []).concat(
      config?.ciCapabilities || [],
    );
    const c = all.find((x) => x.id === text);
    const name = c?.name || text;
    const lines = [text];
    if (c?.description) lines.push(c.description);
    return { kind: "capability", label: name, args: "", lines, full: text };
  }
  const parsed = parseGrant(text);
  if (parsed.raw !== undefined) {
    return {
      kind: "raw",
      label: truncate(text, GRANT_ARGS_MAX + 16),
      args: "",
      lines: [text, "(its arguments are not valid JSON)"],
      full: text,
    };
  }
  const { capability, method: name } = splitRef(parsed.ref);
  const server = serverShortName(capability);
  const label = name ? `${server} › ${name}` : server;
  const shown = nonDefaultArgs(parsed.with, method?.args);
  const args = truncate(
    shown.map(([k, v]) => `${k}: ${formatValue(v)}`).join(", "),
  );
  const lines = [capability];
  if (name) lines.push(`method: ${name}`);
  if (parsed.with == null) lines.push("no arguments");
  else lines.push(...grantDetail(parsed.with).split("\n"));
  return { kind: "method", label, args, lines, full: text };
}

// grantDetail is every normalized argument, for the chip's tooltip.
export function grantDetail(args) {
  const entries = Object.entries(args || {});
  if (entries.length === 0) return "no arguments";
  return entries.map(([k, v]) => `${k}: ${formatValue(v)}`).join("\n");
}

function describeType(root, s) {
  if (!s) return "?";
  if ("const" in s) return s.type || typeOf(s.const);
  if (s.enum) return s.type || "enum";
  if (s.oneOf || s.anyOf)
    return (s.oneOf || s.anyOf)
      .map((sub) => describeType(root, deref(root, sub)))
      .join(" | ");
  if (s.type === "array")
    return `${describeType(root, deref(root, s.items))}[]`;
  return s.type || (s.properties ? "object" : "any");
}

function describeAllowed(root, s) {
  if (!s) return "";
  const parts = [];
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
  if (s.type === "array" && s.items) {
    const inner = describeAllowed(root, deref(root, s.items));
    if (inner) parts.push(`each: ${inner}`);
  }
  return parts.join("; ");
}

// argumentRows is a method's arguments as Settings tabulates them: one
// row per argument, nested objects' own arguments as dotted names.
export function argumentRows(schema) {
  if (!schema) return [];
  const rows = [];
  const walk = (s, prefix, depth) => {
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
