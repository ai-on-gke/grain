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

// The pure half of a capability server's page (Settings → Capabilities →
// <server>): the connection form and the stored entry it edits, the
// configure schema's fields, and where each grant holder lives. The
// classic CapabilityServersPanel.jsx and capabilityArgs.js are the
// reference; the wire shapes are pkg/ui/capability_servers.go's.

export type JsonSchema = Record<string, unknown> & {
  type?: string | string[];
  title?: string;
  description?: string;
  properties?: Record<string, JsonSchema>;
  required?: string[];
  items?: JsonSchema;
  enum?: unknown[];
  default?: unknown;
  deprecated?: boolean;
  writeOnly?: boolean;
  format?: string;
  pattern?: string;
  minimum?: number;
  maximum?: number;
  $ref?: string;
  "x-grain-enum-labels"?: Record<string, string>;
};

export interface CapabilityServerConfig {
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  serverUrl?: string;
  headers?: Record<string, string>;
  disabled?: boolean;
  run?: string;
  hostd?: string;
  builtin?: string;
  notice?: string;
  summary?: string;
  config?: Record<string, unknown>;
}

export interface CapabilityMethodDetail {
  name: string;
  description?: string;
  stateful?: boolean;
}

export interface CapabilityServerDetail {
  id: string;
  config: CapabilityServerConfig;
  secrets?: { name: string; present: boolean }[];
  managed?:
    | { field: string; secret: string; present: boolean; overridden: boolean }[]
    | null;
  builtin?: { name: string; summary?: string } | null;
  inProcess?: { name: string; summary?: string } | null;
  // "<map>/<key>" of each secret-map value the store holds (config-map).
  configuredSecretKeys?: string[];
  status?: {
    builtin?: string;
    state?: string;
    error?: string;
    reason?: string;
    summary?: string;
    methods?: string[];
    methodDetails?: CapabilityMethodDetail[];
    tools?: string[];
    configSchema?: JsonSchema | null;
  } | null;
}

export interface CapabilityHolder {
  kind: string;
  id: string;
  name?: string;
  repo?: string;
  grant: string;
}

export interface HostEntries {
  available: boolean;
  reason?: string;
  entries?: string[];
}

export const SECRET_PREFIX = "$secret:";

// serverPath is the page of one server under /next/, as the classic
// serverPath(id) is /settings/capabilities/<encoded id>: a router
// command array, so the id (a URL, slashes and all) is one segment.
export function serverPath(id: string): string[] {
  return ["/settings", "capabilities", id];
}

// serverName is what a server is called on its page: a built-in's or an
// in-process server's own name, or a custom server's id.
export function serverName(srv: CapabilityServerDetail): string {
  if (srv.inProcess?.name) return `${srv.inProcess.name} server`;
  if (srv.builtin?.name) return `${srv.builtin.name} server`;
  return srv.id;
}

export function linesToPairs(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    const eq = line.indexOf("=");
    if (eq <= 0) {
      throw new Error(`"${line}" is not NAME=VALUE`);
    }
    out[line.slice(0, eq).trim()] = line.slice(eq + 1);
  }
  return out;
}

export function pairsToLines(pairs?: Record<string, string>): string {
  return Object.entries(pairs ?? {})
    .map(([k, v]) => `${k}=${v}`)
    .join("\n");
}

export interface ConnectionForm {
  id: string;
  transport: "stdio" | "http";
  command: string;
  args: string;
  env: string;
  // container, or host (run: host / run: hostd with hostd set).
  run: "container" | "host";
  hostd: string;
  serverUrl: string;
  headers: string;
}

// formFromServer is the connection form for a stored entry, or an empty
// one for a new server (classic formFromServer).
export function formFromServer(
  srv: CapabilityServerDetail | null,
): ConnectionForm {
  const c = srv?.config ?? {};
  return {
    id: srv?.id ?? "",
    transport: srv && !c.command ? "http" : "stdio",
    command: c.command ?? "",
    args: (c.args ?? []).join("\n"),
    env: pairsToLines(c.env),
    run: c.run === "hostd" || c.run === "host" ? "host" : "container",
    hostd: c.hostd ?? "",
    serverUrl: c.serverUrl ?? "",
    headers: pairsToLines(c.headers),
  };
}

// configFromForm is the protocol.ServerConfig the form describes,
// keeping what it does not edit (disabled, the configure config, the
// built-in marker) from base. A host placement is run: hostd with the
// host it names, or run: host for the default host.
export function configFromForm(
  form: ConnectionForm,
  base?: CapabilityServerConfig,
): CapabilityServerConfig {
  const config: CapabilityServerConfig = {};
  if (base?.disabled) config.disabled = true;
  if (base?.builtin) config.builtin = base.builtin;
  if (base?.config && Object.keys(base.config).length) {
    config.config = base.config;
  }
  if (form.transport === "stdio") {
    config.command = form.command.trim();
    const args = form.args
      .split("\n")
      .map((a) => a.trim())
      .filter(Boolean);
    if (args.length) config.args = args;
    const env = linesToPairs(form.env);
    if (Object.keys(env).length) config.env = env;
    if (form.run === "host") {
      config.run = form.hostd ? "hostd" : "host";
      if (form.hostd) config.hostd = form.hostd;
    }
  } else {
    config.serverUrl = form.serverUrl.trim();
    const headers = linesToPairs(form.headers);
    if (Object.keys(headers).length) config.headers = headers;
  }
  return config;
}

// runsAt is the one-line "where it runs" a server's page shows.
export function runsAt(c: CapabilityServerConfig): string {
  if (!c.command) return c.serverUrl ? `HTTP at ${c.serverUrl}` : "HTTP";
  if (c.run === "hostd" && c.hostd) return `on host ${c.hostd}`;
  if (c.run === "host" || c.run === "hostd") return "on the default host";
  return "in the daemon's container";
}

// hostChoice says whether a host placement can be offered for command,
// and why not when it cannot (no host agent beside the daemon, or no
// allowlist entry for the command). Naming a host is always allowed: the
// server waits for it (needs host) until it dials in.
export function hostChoice(
  entries: HostEntries | null,
  command: string,
  hostd: string,
): { ok: boolean; why: string } {
  if (hostd) return { ok: true, why: "" };
  if (!entries) return { ok: false, why: "checking for the host agent…" };
  if (!entries.available) {
    return {
      ok: false,
      why: entries.reason || "the host agent is not available here",
    };
  }
  const cmd = command.trim();
  if (!cmd) {
    return { ok: false, why: "name the command to see if the host offers it" };
  }
  const list = entries.entries ?? [];
  if (!list.includes(cmd)) {
    return {
      ok: false,
      why: `the host agent's allowlist has no capabilities entry for "${cmd}"${
        list.length ? ` (it offers ${list.join(", ")})` : ""
      }`,
    };
  }
  return { ok: true, why: "" };
}

export function isSecretSchema(s: JsonSchema | undefined): boolean {
  return Boolean(s && (s.writeOnly === true || s.format === "password"));
}

// isHiddenProperty is a field the form never draws: a deprecated one,
// or what grain itself injects (classic capabilityArgs.isHiddenProperty).
export function isHiddenProperty(name: string, s?: JsonSchema): boolean {
  if (s?.deprecated === true) return true;
  return (
    name === "grainDeploymentId" ||
    name === "grainInstanceId" ||
    name.startsWith("_grain_")
  );
}

// deref resolves a local "#/..." $ref against root, once per level.
export function deref(
  root: JsonSchema,
  s: JsonSchema | undefined,
): JsonSchema | undefined {
  let cur = s;
  for (let i = 0; i < 8 && cur && typeof cur.$ref === "string"; i++) {
    const ref = cur.$ref;
    if (!ref.startsWith("#/")) return cur;
    let node: unknown = root;
    for (const part of ref.slice(2).split("/")) {
      node = (node as Record<string, unknown> | undefined)?.[
        part.replace(/~1/g, "/").replace(/~0/g, "~")
      ];
    }
    cur = node as JsonSchema | undefined;
  }
  return cur;
}

export type FieldKind =
  | "enum"
  | "boolean"
  | "number"
  | "string"
  | "lines"
  | "objects"
  | "object"
  | "json";

export function fieldKind(root: JsonSchema, raw?: JsonSchema): FieldKind {
  const s = deref(root, raw);
  if (!s) return "json";
  if (Array.isArray(s.enum) && s.enum.every((v) => typeof v === "string")) {
    return "enum";
  }
  const t = Array.isArray(s.type) ? s.type.find((x) => x !== "null") : s.type;
  if (t === "boolean") return "boolean";
  if (t === "integer" || t === "number") return "number";
  if (t === "string") return "string";
  if (t === "array") {
    const it = deref(root, s.items);
    const k = it ? fieldKind(root, it) : "json";
    if (k === "string" || k === "enum" || k === "number") return "lines";
    if (k === "object") return "objects";
    return "json";
  }
  if (t === "object" || (!t && s.properties)) {
    return s.properties ? "object" : "json";
  }
  return "json";
}

export interface SchemaField {
  name: string;
  schema: JsonSchema;
  kind: FieldKind;
  required: boolean;
  secret: boolean;
  label: string;
}

// schemaFields are the fields an object schema draws, in its order, with
// deprecated and grain-injected ones left out.
export function schemaFields(
  root: JsonSchema,
  obj: JsonSchema | undefined,
): SchemaField[] {
  const s = deref(root, obj);
  if (!s?.properties) return [];
  const required = new Set(s.required ?? []);
  const out: SchemaField[] = [];
  for (const [name, raw] of Object.entries(s.properties)) {
    const p = deref(root, raw) ?? {};
    if (isHiddenProperty(name, p)) continue;
    out.push({
      name,
      schema: p,
      kind: fieldKind(root, p),
      required: required.has(name),
      secret: isSecretSchema(p),
      label: p.title || name,
    });
  }
  return out;
}

// enumLabel is how a choice reads: the schema's own x-grain-enum-labels
// ("token (recommended, no keys)") or the value itself.
export function enumLabel(s: JsonSchema, v: string): string {
  return s["x-grain-enum-labels"]?.[v] ?? v;
}

// fieldApplies says whether a field means anything given its siblings:
// gcp's pool_max is key mode only ("Omit for token mode"), and a set
// acting as an existing service_account takes none of the managed-set
// knobs. Everything else always applies.
export function fieldApplies(
  name: string,
  siblings: Record<string, unknown>,
  parent: JsonSchema,
): boolean {
  const props = parent.properties ?? {};
  if (name === "pool_max" && props["credential"]) {
    const cred = siblings["credential"] ?? props["credential"]?.default;
    if (cred === "token") return false;
  }
  if (
    (name === "pool_max" || name === "resource_scope") &&
    props["service_account"] &&
    typeof siblings["service_account"] === "string" &&
    siblings["service_account"] !== ""
  ) {
    return false;
  }
  if (name === "admin_credential" && props["admin_credential_source"]) {
    const src =
      siblings["admin_credential_source"] ??
      props["admin_credential_source"]?.default;
    if (src === "ambient") return false;
  }
  return true;
}

// newItem is a fresh array item: every field with a default takes it, so
// a new gcp permission set starts in token (keyless) mode, while an
// existing set with no credential is left saying so rather than shown as
// token.
export function newItem(
  root: JsonSchema,
  items: JsonSchema | undefined,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const f of schemaFields(root, items)) {
    if (f.schema.default !== undefined) out[f.name] = f.schema.default;
  }
  return out;
}

// gcpCredentialMode is what a gcp permission set's credential means, in
// a line: keyless short-lived tokens (impersonating an existing account
// when service_account is set), or a per-attempt key from a pool.
export function gcpCredentialMode(set: Record<string, unknown>): string {
  const sa =
    typeof set["service_account"] === "string" ? set["service_account"] : "";
  const cred = set["credential"];
  if (cred === "key") {
    return "Key mode: grain mints a per-attempt service-account key from a pool of up to pool_max accounts it creates.";
  }
  if (cred === undefined) {
    return "No credential mode stored: the server's default applies. Choose token (keyless) or key to make it explicit.";
  }
  return sa
    ? `Keyless: grain impersonates ${sa} with short-lived tokens, and creates or binds nothing.`
    : "Keyless: grain creates one service account for the set, binds its roles and mints short-lived access tokens; no key is ever made.";
}

// isGcpServer and isFigmaServer pick the servers whose pages add their
// own explanation or actions beside the schema form.
export function isGcpServer(srv: CapabilityServerDetail): boolean {
  return (
    srv.builtin?.name === "gcp" ||
    srv.config.builtin === "gcp" ||
    srv.id === "https://grain.dev/capabilities/gcp"
  );
}

export function isFigmaServer(srv: CapabilityServerDetail): boolean {
  return (
    srv.builtin?.name === "figma" ||
    Boolean(srv.status?.tools?.includes("test_account"))
  );
}

export interface FigmaAccount {
  name: string;
  auth_mode?: string;
  allow_sandbox_token?: boolean;
  allowed_file_keys?: string[];
}

export function figmaAccounts(srv: CapabilityServerDetail): FigmaAccount[] {
  const a = srv.config.config?.["accounts"];
  return Array.isArray(a)
    ? (a.filter(
        (x) => x && typeof (x as FigmaAccount).name === "string",
      ) as FigmaAccount[])
    : [];
}

// figmaRedirectURI is the callback a Figma OAuth app registers: the
// classic Settings → Capabilities address, so an app registered for the
// classic UI keeps working, and the popup only has to land on this
// origin for the page to read its ?code.
export function figmaRedirectURI(origin: string): string {
  return `${origin}/settings/capabilities`;
}

// secretFieldState is a write-only field's chip: set when the store
// holds the $secret:<name> the entry names, missing when it names one
// the store lacks, unset when the entry names none.
export function secretFieldState(
  srv: CapabilityServerDetail,
  field: string,
): "set" | "missing" | "unset" {
  const ref = srv.config.config?.[field];
  if (typeof ref !== "string" || !ref.startsWith(SECRET_PREFIX)) {
    return "unset";
  }
  const name = ref.slice(SECRET_PREFIX.length);
  return srv.secrets?.some((s) => s.name === name && s.present)
    ? "set"
    : "missing";
}

export interface HolderLink {
  key: string;
  label: string;
  // A classic route (a task's page until /next/ is the default) is an
  // href; a /next/ page is a router command.
  href?: string;
  route?: string[];
  fragment?: string;
  detail: string;
  grant: string;
}

const HOLDER_KIND: Record<string, string> = {
  task: "Task",
  template: "Template",
  schedule: "Schedule",
  workflow: "Workflow job",
};

// holderLink is where one grant holder is changed: a task's own page
// (classic /tasks/<id>), a template or schedule's /next/ page, the
// workflow jobs Settings → Tasks declares, or the default grants of the
// deployment or a repository.
export function holderLink(h: CapabilityHolder): HolderLink {
  const key = `${h.kind}:${h.id}:${h.grant}`;
  const on = h.repo ? ` on ${h.repo}` : "";
  switch (h.kind) {
    case "task":
      return {
        key,
        label: `Task #${h.id}`,
        href: `/tasks/${encodeURIComponent(h.id)}`,
        detail: `${h.name ? `“${h.name}”` : ""}${on}`.trim(),
        grant: h.grant,
      };
    case "template":
      return {
        key,
        label: `Template ${h.name ? `“${h.name}”` : h.id}`,
        route: ["/templates", h.id],
        detail: on.trim(),
        grant: h.grant,
      };
    case "schedule":
      return {
        key,
        label: `Schedule ${h.name ? `“${h.name}”` : h.id}`,
        route: ["/schedules", h.id],
        detail: on.trim(),
        grant: h.grant,
      };
    case "workflow":
      return {
        key,
        label: `Workflow job ${h.name ? `“${h.name}”` : h.id}`,
        route: ["/settings", "tasks"],
        detail: on.trim(),
        grant: h.grant,
      };
    case "default":
      if (h.repo) {
        const [owner, name] = h.repo.split("/");
        return {
          key,
          label: `Default grants of ${h.repo}`,
          route: owner && name ? ["/repos", owner, name] : ["/repos"],
          detail: "",
          grant: h.grant,
        };
      }
      return {
        key,
        label: "Deployment defaults",
        route: ["/settings", "tasks"],
        detail: "",
        grant: h.grant,
      };
    default:
      return {
        key,
        label: `${HOLDER_KIND[h.kind] ?? h.kind} ${h.name || h.id}`,
        detail: on.trim(),
        grant: h.grant,
      };
  }
}

// removeMessage and renameMessage are what the Remove and Rename
// confirms say. holders is null when grain could not read who holds the
// server (the usage read was not allowed or failed): then the confirm
// says that rather than claiming nothing grants it.
export function removeMessage(
  id: string,
  holders: HolderLink[] | null,
): string {
  if (holders === null) {
    return `Remove ${id}? Its own secrets go with it. Who holds a grant on it could not be read; any task, template or schedule that does can no longer use it.`;
  }
  return holders.length
    ? `Remove ${id}? Its own secrets go with it, and these grants can no longer be used:`
    : `Remove ${id}? Nothing grants it now. Its own secrets go with it.`;
}

export function renameMessage(
  from: string,
  to: string,
  holders: HolderLink[] | null,
): string {
  if (holders === null) {
    return `Rename ${from} to ${to}? Who holds a grant on it could not be read; any grant naming the old ID will no longer resolve.`;
  }
  return holders.length
    ? `Renaming ${from} to ${to} leaves these grants naming an ID that no longer resolves:`
    : `Rename ${from} to ${to}? Nothing grants it now; any grant written with the old ID will no longer resolve.`;
}
