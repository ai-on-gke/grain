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

import { HttpErrorResponse } from "@angular/common/http";
import { type JsonSchema, isSecretField } from "../shared/capability-args";

export interface CapabilityMethodDetail {
  name: string;
  description?: string;
  releasable?: boolean;
  args?: JsonSchema;
}

export interface CapabilityServerConfig {
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  serverUrl?: string;
  headers?: Record<string, string>;
  run?: string;
  hostd?: string;
  disabled?: boolean;
  summary?: string;
  notice?: string;
  config?: Record<string, unknown>;
}

export interface CapabilityServerStatus {
  state?: string;
  error?: string;
  reason?: string;
  waiting?: string;
  summary?: string;
  builtin?: string;
  methods?: string[];
  methodDetails?: CapabilityMethodDetail[];
  tools?: string[];
  configSchema?: JsonSchema;
}

export interface CapabilityServerSecret {
  name: string;
  present: boolean;
}

export interface CapabilityServerManagedField {
  field: string;
  overridden?: boolean;
}

export interface CapabilityServerBuiltin {
  name: string;
  summary?: string;
}

export interface CapabilityServerInProcess {
  name: string;
  summary?: string;
}

export interface CapabilityServer {
  id: string;
  config?: CapabilityServerConfig;
  status?: CapabilityServerStatus;
  secrets?: CapabilityServerSecret[];
  managed?: CapabilityServerManagedField[];
  configuredSecretKeys?: string[];
  builtin?: CapabilityServerBuiltin;
  inProcess?: CapabilityServerInProcess;
}

export interface CapabilityGrantHolder {
  kind: string;
  id: string;
  name?: string;
  repo?: string;
  grant: string;
}

export interface CapabilityServerUsage {
  id: string;
  holders?: CapabilityGrantHolder[];
}

export interface HostdMissingEntry {
  name: string;
  command: string;
  entry: string;
}

export interface HostEntriesResponse {
  available: boolean;
  reason?: string;
  entries?: string[];
  missing?: HostdMissingEntry[];
}

export interface HostdEntry {
  name: string;
  owner?: string;
  revoked_at?: string;
  loginsKnown?: boolean;
  logins?: string[];
  connection?: {
    connected?: boolean;
    principal?: string;
    host?: string;
  };
}

export type HostdEntryProblem =
  | { kind: "unreachable"; reason: string }
  | { kind: "missing"; name: string; own?: HostdMissingEntry };

export interface ProbeResultData {
  ok: boolean;
  error?: string;
  warnings?: string[];
  methods?: CapabilityMethodDetail[];
  configSchema?: JsonSchema;
}

export interface ServerFormState {
  id: string;
  transport: "stdio" | "http";
  command: string;
  args: string;
  env: string;
  run: "container" | "host";
  hostd: string;
  serverUrl: string;
  headers: string;
}

export interface HostScriptSpec {
  name: string;
  description?: string;
  usage?: string;
  timeout?: number;
  max_output?: number;
  max_calls?: number;
  max_args?: number;
  max_arg_len?: number;
  cwd?: string;
  env?: Record<string, string>;
}

export interface HostScriptTestResult {
  error?: string;
  refused?: string;
  exit_code?: number;
  duration_ms?: number;
  timed_out?: boolean;
  truncated?: boolean;
  stdout?: string;
  stderr?: string;
}

export interface FigmaAccountSpec {
  name: string;
  auth_mode?: string;
  allow_sandbox_token?: boolean;
  allowed_file_keys?: string[];
}

export interface FigmaTestResult {
  error?: string;
  handle?: string;
  email?: string;
  user_id?: string;
}

export interface FigmaOAuthAuthorizeResponse {
  authorizeUrl: string;
  state: string;
}

export interface FigmaOAuthExchangeResponse {
  account: string;
  userId?: string;
}

export interface GChatSpaceSpec {
  name: string;
  transport?: string;
  space?: string;
  app?: string;
  display_name?: string;
  allowed?: { per_hour?: number };
}

export interface GChatUsageRow {
  space: string;
  posted: number;
  refused: number;
  failed: number;
  last_hour: number;
  per_hour?: number;
  last_post?: string;
}

export interface GChatTestResult {
  error?: string;
  message?: string;
  thread?: string;
}

export interface ConfigMapFileRow {
  id: string;
  key: string;
  mode: string;
  path: string;
  content?: string;
  executable?: boolean;
}

export interface ConfigMapEntry {
  name: string;
  description: string;
  dir: string;
  files: ConfigMapFileRow[];
}

export interface ConfigMapEditorState {
  config_maps?: ConfigMapEntry[];
  secrets?: ConfigMapEntry[];
}

export const SECRET_PREFIX = "$secret:";
export const DEFAULT_CONFIG_MAP_DIR = "/home/debian/.config-map";
export const HOST_SCRIPT_TEST_TOOL = "test_script";
export const FIGMA_TEST_TOOL = "test_account";
export const GCHAT_TEST_TOOL = "test_post";

export const STATE_LABELS: Readonly<Record<string, string>> = {
  "needs-config": "needs configuring",
  unknown: "not attached",
};

export const STATE_HELP: Readonly<Record<string, string>> = {
  attached:
    "grain is connected to this server, and tasks can be granted its methods.",
  pending:
    "Saved; grain connects to it on its next pass, usually within seconds.",
  failed:
    "grain could not start or reach the server. The error is on its page; its methods cannot be minted until it attaches.",
  "needs-config":
    "The server is running but is waiting for its Server settings to be filled in before it can hand anything out.",
  disabled:
    "Turned off: grain does not connect to it and refuses grants of its methods. Its entry and settings are kept.",
  unknown:
    "No status reported yet: the daemon behind this page has not tried to connect to it.",
};

export const WAITING_HELP =
  "The server is waiting on something outside grain, such as a credential on the host that has to be renewed. grain tries it again every minute, and it attaches by itself once that is done; nothing here needs changing.";

export const NEEDS_HOSTD_HELP =
  "The host this server is placed on is not connected: a grant of it waits until that host dials in.";

export const KIND_HELP = {
  builtin:
    "Ships with grain and is listed on every deployment. Its capability ID is fixed; disable it if you do not want it.",
  custom: "Added on this deployment, by hand or through grain/config.",
  inProcess:
    "Ships inside grain itself and is listed on every deployment. It has nothing to configure and cannot be disabled or removed: what holds its methods is up to grants and the Capability policy.",
} as const;

export const GRANTED_HELP =
  "How many open tasks, templates, schedules, Settings-declared workflow jobs and default grants (the deployment's, or a repository's) grant one of this server's methods.";

export const HOLDER_KIND_LABELS: Readonly<Record<string, string>> = {
  task: "Task",
  template: "Template",
  schedule: "Schedule",
  workflow: "Workflow job",
};

export function serverState(server: CapabilityServer): string {
  return (
    server.status?.state || (server.config?.disabled ? "disabled" : "unknown")
  );
}

export function waitingFor(server: CapabilityServer): string {
  return (
    (server.status?.state === "needs-config" && server.status?.waiting) || ""
  );
}

export function needsHostd(state: string): boolean {
  return String(state).startsWith("needs-hostd:");
}

export function needsHostLabel(state: string): string {
  return needsHostd(state)
    ? `needs host ${String(state).slice("needs-hostd:".length)}`
    : "";
}

export function statePillTone(state: string): string {
  if (state === "attached") return "healthy";
  if (state === "failed") return "error";
  if (state === "disabled" || state === "unknown") return "neutral";
  if (state === "pending" || state === "needs-config" || needsHostd(state)) {
    return "warn";
  }
  return "neutral";
}

export interface RunsInfo {
  key: string;
  label: string;
  help: string;
}

const IN_PROCESS_RUNS: RunsInfo = {
  key: "in-process",
  label: "in-process",
  help: "Runs inside grain's own process: there is no command, URL or configuration, and it is always attached.",
};

export function serverRuns(server: CapabilityServer): RunsInfo {
  if (server.inProcess) return IN_PROCESS_RUNS;
  const config = server.config || {};
  if (!config.command) {
    return {
      key: "http",
      label: "remote (HTTP)",
      help: "grain reaches this server at its URL; it runs wherever that is, not under grain.",
    };
  }
  if (config.run === "host" || config.run === "hostd") {
    return {
      key: "host",
      label: config.hostd ? `on host ${config.hostd}` : "on the host",
      help: config.hostd
        ? `grain starts the command on host ${config.hostd}, on that host's machine, for every run that holds a grant of it.`
        : "grain starts the command on the bare machine through the host agent, outside any container.",
    };
  }
  return {
    key: "container",
    label: "beside grain",
    help: "grain starts the command itself, in the same container (or machine) as the grain daemon.",
  };
}

export function serverDisplayTitle(server: CapabilityServer): string {
  if (server.inProcess) return `${server.inProcess.name} server`;
  if (server.builtin) return `${server.builtin.name} server`;
  return server.id;
}

export function serverSummaryText(server: CapabilityServer): string {
  return (
    server.inProcess?.summary ||
    server.builtin?.summary ||
    server.status?.summary ||
    server.config?.summary ||
    ""
  );
}

const SUMMARY_MAX_CHARS = 120;

export function summarizeServer(
  text: string | undefined,
  max = SUMMARY_MAX_CHARS,
): { short: string; full: string; truncated: boolean } {
  const full = (text || "").trim();
  if (!full) return { short: "", full: "", truncated: false };
  const firstLine = full.split(/\r?\n/)[0].trim();
  const sentenceMatch = firstLine.match(/^(.+?[.!?])(?=\s+\S)/);
  if (sentenceMatch && sentenceMatch[1].length <= max) {
    const short = sentenceMatch[1].trim();
    return { short, full, truncated: short !== full };
  }
  if (firstLine.length <= max) {
    return { short: firstLine, full, truncated: firstLine !== full };
  }
  const head = firstLine.slice(0, max);
  for (const sep of [" — ", " -- ", "; ", ": ", ", "]) {
    const idx = head.lastIndexOf(sep);
    if (idx >= 24) {
      const short = head.slice(0, idx).trim();
      return { short, full, truncated: true };
    }
  }
  const spaceIdx = head.lastIndexOf(" ");
  const short = `${(spaceIdx >= 24 ? head.slice(0, spaceIdx) : head).trim()}…`;
  return { short, full, truncated: true };
}

export function linesToPairs(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const i = trimmed.indexOf("=");
    if (i <= 0) throw new Error(`"${trimmed}" is not NAME=VALUE`);
    out[trimmed.slice(0, i)] = trimmed.slice(i + 1);
  }
  return out;
}

export function pairsToLines(
  pairs: Record<string, string> | undefined,
): string {
  return Object.entries(pairs || {})
    .map(([k, v]) => `${k}=${v}`)
    .join("\n");
}

export function secretFieldNames(
  schema: JsonSchema | null | undefined,
): string[] {
  return Object.entries(schema?.properties || {})
    .filter(([, sub]) => isSecretField(sub))
    .map(([name]) => name);
}

export function formFromServer(
  server: CapabilityServer | null | undefined,
): ServerFormState {
  const config = server?.config || {};
  return {
    id: server?.id || "",
    transport: server && !config.command ? "http" : "stdio",
    command: config.command || "",
    args: (config.args || []).join("\n"),
    env: pairsToLines(config.env),
    run: config.run === "hostd" || config.run === "host" ? "host" : "container",
    hostd: config.hostd || "",
    serverUrl: config.serverUrl || "",
    headers: pairsToLines(config.headers),
  };
}

export function configFromForm(
  form: ServerFormState,
  base?: CapabilityServerConfig,
): CapabilityServerConfig {
  const config: CapabilityServerConfig = {};
  if (base?.disabled) config.disabled = true;
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
    if (form.run === "host") config.run = form.hostd ? "hostd" : "host";
    if (form.run === "host" && form.hostd) config.hostd = form.hostd;
  } else {
    config.serverUrl = form.serverUrl.trim();
    const headers = linesToPairs(form.headers);
    if (Object.keys(headers).length) config.headers = headers;
  }
  return config;
}

export function hostChoice(
  hostEntries: HostEntriesResponse | null,
  command: string,
): { ok: boolean; why: string } {
  if (!hostEntries) return { ok: false, why: "checking for the host agent…" };
  if (!hostEntries.available) {
    return {
      ok: false,
      why: hostEntries.reason || "the host agent is not available here",
    };
  }
  const entries = hostEntries.entries || [];
  const cmd = command.trim();
  if (!cmd) {
    return {
      ok: false,
      why: "name the command to see if the host agent offers it",
    };
  }
  if (!entries.includes(cmd)) {
    return {
      ok: false,
      why: `the host agent's allowlist has no capabilities entry for "${cmd}"${
        entries.length ? ` (it offers ${entries.join(", ")})` : ""
      }`,
    };
  }
  return { ok: true, why: "" };
}

export function hostdEntryProblem(
  hostEntries: HostEntriesResponse | null,
  command: string | undefined,
): HostdEntryProblem | null {
  if (!hostEntries || !command) return null;
  if (!hostEntries.available) {
    return { kind: "unreachable", reason: hostEntries.reason || "" };
  }
  if ((hostEntries.entries || []).includes(command)) return null;
  const own = (hostEntries.missing || []).find((m) => m.name === command);
  return { kind: "missing", name: command, own };
}

export function hostdLogins(h: HostdEntry): string {
  if (!h.loginsKnown) return "";
  const logins = h.logins || [];
  return logins.length ? `logins: ${logins.join(", ")}` : "no CLI logins";
}

export function hostdOption(h: HostdEntry): string {
  const parts = [h.name];
  const c = h.connection;
  parts.push(
    c?.connected || h.name === "local" ? "connected" : "not connected",
  );
  if (h.owner) parts.push(`owner ${h.owner}`);
  if (c?.principal) parts.push(`runs as ${c.principal}`);
  if (c?.host) parts.push(`on ${c.host}`);
  const logins = hostdLogins(h);
  if (logins) parts.push(logins);
  return parts.join(" · ");
}

export function isHostScriptServer(
  server: CapabilityServer | null | undefined,
): boolean {
  return Boolean(server?.status?.tools?.includes(HOST_SCRIPT_TEST_TOOL));
}

export function isFigmaServer(
  server: CapabilityServer | null | undefined,
): boolean {
  return (
    server?.builtin?.name === "figma" ||
    Boolean(server?.status?.tools?.includes(FIGMA_TEST_TOOL))
  );
}

export function figmaRedirectURI(
  origin = typeof window !== "undefined" ? window.location.origin : "",
): string {
  return `${origin}/settings/capabilities`;
}

export function waitForOAuthCode(
  popup: Window | null,
  opts: { interval?: number; timeout?: number } = {},
): Promise<{ code: string; state: string }> {
  const interval = opts.interval ?? 500;
  const timeout = opts.timeout;
  return new Promise((resolve, reject) => {
    const started = Date.now();
    const timer = setInterval(() => {
      if (!popup || popup.closed) {
        clearInterval(timer);
        reject(new Error("the Figma window was closed before it finished"));
        return;
      }
      if (timeout && Date.now() - started > timeout) {
        clearInterval(timer);
        reject(new Error("timed out waiting for Figma"));
        return;
      }
      let search: string;
      try {
        if (popup.location.origin !== window.location.origin) return;
        search = popup.location.search;
      } catch {
        return;
      }
      const params = new URLSearchParams(search);
      const code = params.get("code");
      const error = params.get("error");
      if (!code && !error) return;
      clearInterval(timer);
      popup.close();
      if (error) {
        reject(new Error(`Figma refused: ${error}`));
        return;
      }
      resolve({ code: code || "", state: params.get("state") || "" });
    }, interval);
  });
}

export function isGChatServer(
  server: CapabilityServer | null | undefined,
): boolean {
  return (
    server?.builtin?.name === "gchat" ||
    Boolean(server?.status?.tools?.includes(GCHAT_TEST_TOOL))
  );
}

export function isConfigMapServer(
  server: CapabilityServer | null | undefined,
): boolean {
  if (
    server?.builtin?.name === "config-map" ||
    server?.status?.builtin === "config-map"
  ) {
    return true;
  }
  const props = server?.status?.configSchema?.properties;
  return Boolean(props?.["config_maps"] && props?.["secret_values"]);
}

export function secretValueKey(mapName: string, key: string): string {
  return `${(mapName || "").trim()}/${(key || "").trim()}`;
}

export function isSecretKeySet(
  server: CapabilityServer | null | undefined,
  mapName: string,
  key: string,
): boolean {
  if (!(mapName || "").trim() || !(key || "").trim()) return false;
  return (server?.configuredSecretKeys || []).includes(
    secretValueKey(mapName, key),
  );
}

export function defaultMapDir(name: string): string {
  return `${DEFAULT_CONFIG_MAP_DIR}/${(name || "").trim() || "<name>"}`;
}

let nextConfigMapRowId = 0;
export function newConfigMapRowId(): string {
  return `row-${++nextConfigMapRowId}`;
}

export function newConfigMapFileRow(secret: boolean): ConfigMapFileRow {
  return secret
    ? { id: newConfigMapRowId(), key: "", mode: "", path: "" }
    : {
        id: newConfigMapRowId(),
        key: "",
        mode: "",
        path: "",
        content: "",
        executable: false,
      };
}

export function newConfigMapEntry(secret: boolean): ConfigMapEntry {
  return {
    name: "",
    description: "",
    dir: "",
    files: [newConfigMapFileRow(secret)],
  };
}

export function configMapEditorValue(
  stored: Record<string, unknown> | undefined,
): ConfigMapEditorState {
  const rows = (
    data: Record<string, Record<string, unknown>> | undefined,
    secret: boolean,
  ): ConfigMapFileRow[] =>
    Object.keys(data || {})
      .sort()
      .map((key) => {
        const opts = data?.[key] || {};
        const row: ConfigMapFileRow = {
          id: newConfigMapRowId(),
          key,
          mode: String(opts["mode"] ?? ""),
          path: String(opts["path"] ?? ""),
        };
        if (!secret) {
          row.content = String(opts["content"] ?? "");
          row.executable = Boolean(opts["executable"]);
        }
        return row;
      });
  const next: ConfigMapEditorState = {};
  if (Array.isArray(stored?.["config_maps"])) {
    next.config_maps = (stored["config_maps"] as Record<string, unknown>[]).map(
      (cm) => ({
        name: String(cm?.["name"] ?? ""),
        description: String(cm?.["description"] ?? ""),
        dir: String(cm?.["dir"] ?? ""),
        files: rows(
          cm?.["data"] as Record<string, Record<string, unknown>> | undefined,
          false,
        ),
      }),
    );
  }
  if (Array.isArray(stored?.["secrets"])) {
    next.secrets = (stored["secrets"] as Record<string, unknown>[]).map(
      (sec) => ({
        name: String(sec?.["name"] ?? ""),
        description: String(sec?.["description"] ?? ""),
        dir: String(sec?.["dir"] ?? ""),
        files: rows(
          sec?.["keys"] as Record<string, Record<string, unknown>> | undefined,
          true,
        ),
      }),
    );
  }
  return next;
}

export function normalizeConfigMapPayload(
  value: ConfigMapEditorState,
  perSecretValues: Record<string, string>,
): {
  config: Record<string, unknown>;
  configSecrets: Record<string, string>;
} {
  const mapHeader = (m: ConfigMapEntry): Record<string, unknown> => {
    const entry: Record<string, unknown> = { name: (m.name || "").trim() };
    if ((m.description || "").trim())
      entry["description"] = m.description.trim();
    if ((m.dir || "").trim()) entry["dir"] = m.dir.trim();
    return entry;
  };
  const next: Record<string, unknown> = {};
  if (Array.isArray(value?.config_maps)) {
    next["config_maps"] = value.config_maps.map((cm) => {
      const entry = mapHeader(cm);
      const data: Record<string, Record<string, unknown>> = {};
      for (const f of cm.files || []) {
        const opts: Record<string, unknown> = { content: f.content ?? "" };
        if ((f.mode || "").trim()) opts["mode"] = f.mode.trim();
        if (f.executable) opts["executable"] = true;
        if ((f.path || "").trim()) opts["path"] = f.path.trim();
        data[(f.key || "").trim()] = opts;
      }
      entry["data"] = data;
      return entry;
    });
  }
  const secretMap: Record<string, string> = {};
  if (Array.isArray(value?.secrets)) {
    next["secrets"] = value.secrets.map((sec) => {
      const entry = mapHeader(sec);
      const keys: Record<string, Record<string, unknown>> = {};
      for (const f of sec.files || []) {
        const key = (f.key || "").trim();
        const opts: Record<string, unknown> = {};
        if ((f.mode || "").trim()) opts["mode"] = f.mode.trim();
        if ((f.path || "").trim()) opts["path"] = f.path.trim();
        keys[key] = opts;
        const typed = perSecretValues?.[f.id] ?? "";
        const name = String(entry["name"] || "");
        if (name && key && typed) {
          secretMap[secretValueKey(name, key)] = typed;
        }
      }
      entry["keys"] = keys;
      return entry;
    });
  }
  const configSecrets: Record<string, string> =
    Object.keys(secretMap).length > 0
      ? { secret_values: JSON.stringify(secretMap) }
      : {};
  return { config: next, configSecrets };
}

export function extractFieldErrors(err: unknown): Record<string, string> {
  if (!(err instanceof HttpErrorResponse)) return {};
  const body = err.error;
  if (!body || typeof body !== "object") return {};
  const raw = (body as Record<string, unknown>)["fields"];
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(raw as Record<string, unknown>)) {
    if (typeof v === "string" && v) out[k] = v;
  }
  return out;
}
