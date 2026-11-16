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

// The two built-in servers whose configuration Settings → Capabilities
// edits field by field rather than as one schema form: the config-map
// server's config maps and secret maps (the files each places in a
// sandbox), and the host-script server's scripts (docs/host-scripts.md).
// Ported from the classic ConfigMapList.jsx and HostScriptList.jsx; this
// file is the pure half, with no Angular in it.

export const DEFAULT_CONFIG_MAP_DIR = "/home/debian/.config-map";

// HOST_SCRIPT_TEST_TOOL is the control-plane tool a host-script server's
// attach status advertises; it is what Test calls through
// POST /api/capability-servers/test-script.
export const HOST_SCRIPT_TEST_TOOL = "test_script";

// PlacementServer is the part of a GET /api/capability-servers row these
// cards read.
export interface PlacementServer {
  id: string;
  builtin?: unknown;
  config?: {
    builtin?: string;
    config?: Record<string, unknown>;
    [key: string]: unknown;
  };
  // "<map>/<key>" of every secret map key with a stored value; the
  // values themselves never leave the server.
  configuredSecretKeys?: string[];
  status?: {
    builtin?: string;
    tools?: string[];
    configSchema?: { properties?: Record<string, unknown> };
    [key: string]: unknown;
  } | null;
}

function builtinName(server: PlacementServer): string {
  const b = server.builtin as { name?: unknown } | string | undefined;
  if (typeof b === "string") return b;
  if (b && typeof b.name === "string") return b.name;
  return server.config?.builtin ?? server.status?.builtin ?? "";
}

export function isConfigMapServer(server: PlacementServer): boolean {
  if (builtinName(server) === "config-map") return true;
  const props = server.status?.configSchema?.properties;
  return Boolean(props?.["config_maps"] && props?.["secret_values"]);
}

export function isHostScriptServer(server: PlacementServer): boolean {
  if (builtinName(server) === "host-script") return true;
  return Boolean(server.status?.tools?.includes(HOST_SCRIPT_TEST_TOOL));
}

// ---- Config maps and secret maps ----

export interface PlacedFile {
  // id holds a typed secret value while its key is renamed.
  id: string;
  key: string;
  mode: string;
  path: string;
  // Config map files only.
  content: string;
  executable: boolean;
}

export interface PlacementMap {
  name: string;
  description: string;
  dir: string;
  files: PlacedFile[];
}

export interface PlacementValue {
  configMaps: PlacementMap[];
  secretMaps: PlacementMap[];
}

let nextFileId = 0;
export function newFile(): PlacedFile {
  return {
    id: `f${++nextFileId}`,
    key: "",
    mode: "",
    path: "",
    content: "",
    executable: false,
  };
}

export function newMap(): PlacementMap {
  return { name: "", description: "", dir: "", files: [newFile()] };
}

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
}

function filesOf(data: unknown, secret: boolean): PlacedFile[] {
  if (!data || typeof data !== "object") return [];
  const entries = data as Record<string, Record<string, unknown> | null>;
  return Object.keys(entries)
    .sort()
    .map((key) => {
      const opts = entries[key] ?? {};
      return {
        ...newFile(),
        key,
        mode: str(opts["mode"]),
        path: str(opts["path"]),
        content: secret ? "" : str(opts["content"]),
        executable: secret ? false : Boolean(opts["executable"]),
      };
    });
}

function mapsOf(list: unknown, secret: boolean): PlacementMap[] {
  if (!Array.isArray(list)) return [];
  return list.map((m: Record<string, unknown> | null) => ({
    name: str(m?.["name"]),
    description: str(m?.["description"]),
    dir: str(m?.["dir"]),
    files: filesOf(secret ? m?.["keys"] : m?.["data"], secret),
  }));
}

// placementValue is the editor's rows for a config-map server's stored
// config: config_maps keyed by file under data, secrets keyed under keys.
export function placementValue(
  stored: Record<string, unknown> | undefined,
): PlacementValue {
  return {
    configMaps: mapsOf(stored?.["config_maps"], false),
    secretMaps: mapsOf(stored?.["secrets"], true),
  };
}

export function defaultMapDir(name: string): string {
  return `${DEFAULT_CONFIG_MAP_DIR}/${name.trim() || "<name>"}`;
}

export function defaultFileMode(secret: boolean, executable: boolean): string {
  if (secret) return "0600";
  return executable ? "0755" : "0644";
}

// secretValueKey is the secret_values key of one secret map key.
export function secretValueKey(map: string, key: string): string {
  return `${map.trim()}/${key.trim()}`;
}

export function isSecretKeySet(
  server: PlacementServer,
  map: string,
  key: string,
): boolean {
  if (!map.trim() || !key.trim()) return false;
  return (server.configuredSecretKeys ?? []).includes(secretValueKey(map, key));
}

function mapHeader(m: PlacementMap): Record<string, unknown> {
  const entry: Record<string, unknown> = { name: m.name.trim() };
  if (m.description.trim()) entry["description"] = m.description.trim();
  if (m.dir.trim()) entry["dir"] = m.dir.trim();
  return entry;
}

// placementPayload is the PUT /api/capability-servers body for value:
// the server's stored config with config_maps and secrets replaced
// (anything else it holds, such as its secret_values reference, kept)
// and configSecrets carrying only the secret values typed this time, as
// {"<map>/<key>": value} -- a blank box keeps the stored value.
export function placementPayload(
  server: PlacementServer,
  value: PlacementValue,
  typed: Record<string, string>,
): {
  id: string;
  config: Record<string, unknown>;
  configSecrets: Record<string, string>;
} {
  const stored = server.config?.config ?? {};
  const configMaps = value.configMaps.map((m) => {
    const data: Record<string, unknown> = {};
    for (const f of m.files) {
      const opts: Record<string, unknown> = { content: f.content };
      if (f.mode.trim()) opts["mode"] = f.mode.trim();
      if (f.executable) opts["executable"] = true;
      if (f.path.trim()) opts["path"] = f.path.trim();
      data[f.key.trim()] = opts;
    }
    return { ...mapHeader(m), data };
  });
  const values: Record<string, string> = {};
  const secrets = value.secretMaps.map((m) => {
    const keys: Record<string, unknown> = {};
    for (const f of m.files) {
      const key = f.key.trim();
      const opts: Record<string, unknown> = {};
      if (f.mode.trim()) opts["mode"] = f.mode.trim();
      if (f.path.trim()) opts["path"] = f.path.trim();
      keys[key] = opts;
      const v = typed[f.id] ?? "";
      if (m.name.trim() && key && v) values[secretValueKey(m.name, key)] = v;
    }
    return { ...mapHeader(m), keys };
  });
  return {
    id: server.id,
    config: {
      ...server.config,
      config: { ...stored, config_maps: configMaps, secrets },
    },
    configSecrets:
      Object.keys(values).length > 0
        ? { secret_values: JSON.stringify(values) }
        : {},
  };
}

// placementErrors is what the editor refuses before a save: a map with
// no name, two maps of one kind sharing a name, a file with no key, or
// two files of one map sharing a key, or a secret map key with neither
// a stored value nor one typed now (the server refuses those). Keyed
// "config:<i>" / "secret:<i>" for a map and "config:<i>:<j>" /
// "secret:<i>:<j>" for a file.
export function placementErrors(
  value: PlacementValue,
  server?: PlacementServer,
  typed: Record<string, string> = {},
): Record<string, string> {
  const errs: Record<string, string> = {};
  const check = (list: PlacementMap[], kind: "config" | "secret") => {
    const names = new Set<string>();
    list.forEach((m, i) => {
      const name = m.name.trim();
      if (!name) errs[`${kind}:${i}`] = "Name the map.";
      else if (names.has(name)) {
        errs[`${kind}:${i}`] = `Another ${kind} map is named ${name}.`;
      }
      names.add(name);
      const keys = new Set<string>();
      m.files.forEach((f, j) => {
        const key = f.key.trim();
        if (!key) errs[`${kind}:${i}:${j}`] = "Name the file's key.";
        else if (keys.has(key)) {
          errs[`${kind}:${i}:${j}`] = `This map already places ${key}.`;
        }
        keys.add(key);
        if (
          kind === "secret" &&
          key &&
          name &&
          !typed[f.id] &&
          !(server && isSecretKeySet(server, name, key))
        ) {
          errs[`secret:${i}:${j}:value`] = "Type this key's value.";
        }
      });
    });
  };
  check(value.configMaps, "config");
  check(value.secretMaps, "secret");
  return errs;
}

// droppedPlacements names the saved maps and files value no longer
// holds ("secret map deploy", "config map tools: run.sh"), so a save that
// removes any asks first.
export function droppedPlacements(
  stored: PlacementValue,
  value: PlacementValue,
): string[] {
  const out: string[] = [];
  const diff = (a: PlacementMap[], b: PlacementMap[], label: string) => {
    for (const m of a) {
      const now = b.find((n) => n.name.trim() === m.name);
      if (!now) {
        out.push(`${label} ${m.name}`);
        continue;
      }
      const keys = new Set(now.files.map((f) => f.key.trim()));
      for (const f of m.files) {
        if (!keys.has(f.key)) out.push(`${label} ${m.name}: ${f.key}`);
      }
    }
  };
  diff(stored.configMaps, value.configMaps, "config map");
  diff(stored.secretMaps, value.secretMaps, "secret map");
  return out;
}

// ---- Host scripts ----

export interface HostScriptRow {
  name: string;
  description: string;
  usage?: string;
  script?: string;
  timeout?: number;
  env?: Record<string, string>;
  cwd?: string;
  approve?: boolean;
  max_output?: number;
  max_stdin?: number;
  max_calls?: number;
  max_args?: number;
  max_arg_len?: number;
}

export function hostScripts(server: PlacementServer): HostScriptRow[] {
  const list = server.config?.config?.["scripts"];
  return Array.isArray(list) ? (list as HostScriptRow[]) : [];
}

// scriptLimits is the chips a script's row shows for the limits it sets.
export function scriptLimits(s: HostScriptRow): string[] {
  const out: string[] = [];
  if (s.timeout !== undefined) out.push(`timeout ${s.timeout}s`);
  if (s.max_output !== undefined) out.push(`output ≤ ${s.max_output} B`);
  if (s.max_stdin !== undefined) out.push(`stdin ≤ ${s.max_stdin} B`);
  if (s.max_calls !== undefined) out.push(`${s.max_calls} calls`);
  if (s.max_args !== undefined) out.push(`≤ ${s.max_args} args`);
  if (s.max_arg_len !== undefined) out.push(`arg ≤ ${s.max_arg_len} B`);
  if (s.cwd) out.push(`cwd ${s.cwd}`);
  if (s.approve) out.push("approval per call");
  const env = Object.keys(s.env ?? {});
  if (env.length) out.push(`env ${env.join(", ")}`);
  return out;
}

// ScriptDraft is the edit form: every field a string, numbers included,
// so a blank box means "the server's default" and is left out on save.
export interface ScriptDraft {
  name: string;
  description: string;
  usage: string;
  script: string;
  timeout: string;
  cwd: string;
  approve: boolean;
  env: string;
  max_output: string;
  max_stdin: string;
  max_calls: string;
  max_args: string;
  max_arg_len: string;
}

export const SCRIPT_LIMIT_FIELDS = [
  "max_output",
  "max_stdin",
  "max_calls",
  "max_args",
  "max_arg_len",
] as const;

export function scriptDraft(s?: HostScriptRow): ScriptDraft {
  const n = (v: number | undefined) => (v === undefined ? "" : String(v));
  return {
    name: s?.name ?? "",
    description: s?.description ?? "",
    usage: s?.usage ?? "",
    script: s?.script ?? "",
    timeout: s ? n(s.timeout) : "60",
    cwd: s?.cwd ?? "",
    approve: Boolean(s?.approve),
    env: Object.entries(s?.env ?? {})
      .map(([k, v]) => `${k}=${v}`)
      .join("\n"),
    max_output: n(s?.max_output),
    max_stdin: n(s?.max_stdin),
    max_calls: n(s?.max_calls),
    max_args: n(s?.max_args),
    max_arg_len: n(s?.max_arg_len),
  };
}

const SCRIPT_NAME = /^[a-z0-9][a-z0-9_-]{0,62}$/;
const ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;

// scriptDraftErrors is what the form refuses by field before a save,
// mirroring the server's own parseHostScripts checks; others names the
// scripts already configured besides the one being edited.
export function scriptDraftErrors(
  d: ScriptDraft,
  others: string[],
): Partial<Record<keyof ScriptDraft, string>> {
  const errs: Partial<Record<keyof ScriptDraft, string>> = {};
  const name = d.name.trim();
  if (!SCRIPT_NAME.test(name)) {
    errs.name =
      "Lower-case letters, digits, - and _, starting with a letter or digit.";
  } else if (others.includes(name)) {
    errs.name = `A script named ${name} is already configured.`;
  }
  if (!d.description.trim()) errs.description = "Describe what it does.";
  if (!d.script.trim()) errs.script = "The script is empty.";
  const t = Number(d.timeout.trim());
  if (!d.timeout.trim() || !Number.isInteger(t) || t < 1 || t > 86400) {
    errs.timeout = "Whole seconds from 1 to 86400.";
  }
  const cwd = d.cwd.trim();
  if (cwd && (!cwd.startsWith("/") || /\/\.{0,2}(\/|$)/.test(cwd.slice(1)))) {
    errs.cwd = "A clean absolute path.";
  }
  for (const f of SCRIPT_LIMIT_FIELDS) {
    const raw = d[f].trim();
    if (!raw) continue;
    const v = Number(raw);
    const zeroOK = f === "max_args" || f === "max_stdin";
    if (!Number.isInteger(v) || v < 0 || (v === 0 && !zeroOK)) {
      errs[f] = zeroOK
        ? "A whole number, 0 or more."
        : "A whole number, 1 or more.";
    }
  }
  for (const line of d.env.split("\n")) {
    if (!line.trim()) continue;
    const k = line.split("=")[0]?.trim() ?? "";
    if (!line.includes("=") || !ENV_NAME.test(k)) {
      errs.env = `Write each variable as NAME=value (${line.trim()}).`;
      break;
    }
  }
  return errs;
}

// DRAFT_FIELDS are the script fields the form edits; scriptFromDraft
// keeps any other field the stored script holds.
const DRAFT_FIELDS = new Set<string>([
  "name",
  "description",
  "usage",
  "script",
  "timeout",
  "cwd",
  "approve",
  "env",
  ...SCRIPT_LIMIT_FIELDS,
]);

export function scriptFromDraft(
  d: ScriptDraft,
  base?: HostScriptRow,
): HostScriptRow {
  const kept = Object.fromEntries(
    Object.entries(base ?? {}).filter(([k]) => !DRAFT_FIELDS.has(k)),
  );
  const s: HostScriptRow = {
    ...kept,
    name: d.name.trim(),
    description: d.description.trim(),
    script: d.script,
    timeout: Number(d.timeout.trim()),
  };
  if (d.usage.trim()) s.usage = d.usage;
  if (d.cwd.trim()) s.cwd = d.cwd.trim();
  if (d.approve) s.approve = true;
  for (const f of SCRIPT_LIMIT_FIELDS) {
    if (d[f].trim()) s[f] = Number(d[f].trim());
  }
  const env: Record<string, string> = {};
  for (const line of d.env.split("\n")) {
    const at = line.indexOf("=");
    if (at <= 0) continue;
    env[line.slice(0, at).trim()] = line.slice(at + 1);
  }
  if (Object.keys(env).length) s.env = env;
  return s;
}

// scriptsPayload is the PUT /api/capability-servers body that stores
// scripts as the host-script server's whole list, keeping the rest of its
// stored config.
export function scriptsPayload(
  server: PlacementServer,
  scripts: HostScriptRow[],
): { id: string; config: Record<string, unknown> } {
  const { config: stored, ...rest } = server.config ?? {};
  return {
    id: server.id,
    config: { ...rest, config: { ...(stored ?? {}), scripts } },
  };
}

// ScriptTestResult is POST /api/capability-servers/test-script's answer.
export interface ScriptTestResult {
  exit_code?: number;
  duration_ms?: number;
  stdout?: string;
  stderr?: string;
  timed_out?: boolean;
  truncated?: boolean;
  refused?: string;
  error?: string;
}
